package cli

// A command or flag that needs elevated rights shows the marker, and no other
// does. Every command is run against a recording stand-in for the API; the
// documented operations it reached decide whether it needs elevated rights,
// and the published documents decide which operations do.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/pem"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/mimiry-com/mimiry-sdk/internal/apispec"
)

const markerOrg = "550e8400-e29b-41d4-a716-446655440000"

// markerRuns: how to run each command, and for a flag that needs elevated
// rights, how to run it with that flag. The key is "<command>" or
// "<command> --<flag>".
var markerRuns = map[string][]string{
	"auth":          {"auth"},
	"version":       {"version"},
	"availability":  {"availability"},
	"balance":       {"balance"},
	"balance --org": {"balance", "--org", "o-acme"},
	"quota":         {"quota"},
	"transactions":  {"transactions"},

	"session create":    {"session", "create", "img", "--name", "n", "--gpu", "A100"},
	"session list":      {"session", "list"},
	"session status":    {"session", "status", "s-1"},
	"session logs":      {"session", "logs", "s-1"},
	"session terminate": {"session", "terminate", "s-1"},
	"session wait":      {"session", "wait", "s-1", "--timeout", "300ms"},
	"volume create":     {"volume", "create", "--name", "v", "--size", "100", "--location", "FIN-01"},
	"volume list":       {"volume", "list"},
	"volume status":     {"volume", "status", "v-1"},
	"volume extend":     {"volume", "extend", "v-1", "--size", "200"},
	"volume delete":     {"volume", "delete", "v-1"},
	"volume wait":       {"volume", "wait", "v-1", "--timeout", "300ms"},

	"org list":             {"org", "list"},
	"org show":             {"org", "show", markerOrg},
	"org create":           {"org", "create", "Acme", "--slug", "o-acme", "--no-wait"},
	"org update":           {"org", "update", markerOrg, "--name", "Acme 2", "--no-wait"},
	"org delete":           {"org", "delete", markerOrg, "--no-wait"},
	"org wind-down":        {"org", "wind-down", markerOrg, "--readable-days", "30", "--no-wait"},
	"org cancel-wind-down": {"org", "cancel-wind-down", markerOrg, "--no-wait"},
	"org transfer":         {"org", "transfer", markerOrg, "u-2", "--no-wait"},
	"org members":          {"org", "members", markerOrg},
	"org invite":           {"org", "invite", markerOrg, "jane@example.com", "--no-wait"},
	"org set-role":         {"org", "set-role", markerOrg, "u-2", "admin", "--no-wait"},
	"org remove":           {"org", "remove", markerOrg, "u-2", "--no-wait"},
	"org leave":            {"org", "leave", markerOrg, "--no-wait"},
	"org invitations":      {"org", "invitations", markerOrg},
	"org revoke":           {"org", "revoke", markerOrg, "i-1", "--no-wait"},
	"org invitation":       {"org", "invitation", "tok-1"},
	"org accept":           {"org", "accept", "tok-1", "--no-wait"},
	"org wait":             {"org", "wait", "r-1", "--timeout", "300ms"},
}

// markerRunExceptions: a run whose marker differs from its operations', and why.
var markerRunExceptions = map[string]string{
	"org leave": "removes the caller, which every member may; the operation is marked because removing anyone else needs elevated rights",
}

// noAPICall are the runs that reach no documented operation, and why.
var noAPICall = map[string]string{
	"auth":    "signs in only",
	"version": "prints the version",
}

// subjects is every command, and every flag whose usage carries the marker,
// with whether it shows the marker.
func subjects(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	add := func(name string, c *command) {
		out[name] = strings.Contains(c.title(), apispec.Marker)
		if c.flags == nil {
			return
		}
		fs := flag.NewFlagSet(name, flag.ContinueOnError)
		c.flags(fs)
		fs.VisitAll(func(f *flag.Flag) {
			if strings.Contains(f.Usage, apispec.Marker) {
				out[name+" --"+f.Name] = true
			}
		})
	}
	for n, c := range topLevel {
		add(n, c)
	}
	for g, grp := range groups {
		for n, c := range grp.cmds {
			add(g+" "+n, c)
		}
	}
	return out
}

func TestElevatedCommandsAreMarked(t *testing.T) {
	docs, err := apispec.LoadAll("../../spec")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("MIMIRY_HOME", t.TempDir())
	t.Setenv("MIMIRY_HOST", "")
	t.Setenv("MIMIRY_SSH_KEY", "")

	// Not newFakeAPI: it refuses a request without a token, and the public
	// commands send none. Every request is recorded here.
	var mu sync.Mutex
	var reached []apispec.Op
	var unknown []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/token" {
			io.WriteString(w, `{"access_token":"tok","expires_in":300,"user_id":"u-1"}`)
			return
		}
		op, ok := apispec.Match(docs, r.Method, r.URL.EscapedPath())
		mu.Lock()
		if ok {
			reached = append(reached, op)
		} else {
			unknown = append(unknown, r.Method+" "+r.URL.EscapedPath())
		}
		mu.Unlock()
		io.WriteString(w, `{}`)
	}))
	t.Cleanup(srv.Close)
	_, key, _ := ed25519.GenerateKey(nil)
	block, err := ssh.MarshalPrivateKey(key, "")
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}

	marked := subjects(t)
	names := make([]string, 0, len(marked))
	for n := range marked {
		names = append(names, n)
	}
	sort.Strings(names)
	used := map[string]bool{}
	for _, name := range names {
		args, ok := markerRuns[name]
		if !ok {
			t.Errorf("%s is not run here; add it to markerRuns", name)
			continue
		}
		mu.Lock()
		reached, unknown = nil, nil
		mu.Unlock()
		var out, errOut bytes.Buffer
		Run(context.Background(), append([]string{"--host", srv.URL, "--key", keyPath}, args...),
			strings.NewReader(""), &out, &errOut)

		mu.Lock()
		got, missing := reached, unknown
		mu.Unlock()
		for _, u := range missing {
			t.Errorf("%s called %s, which no document describes", name, u)
		}
		if _, ok := noAPICall[name]; !ok && len(got) == 0 {
			t.Errorf("%s reached no documented operation (stderr: %s)", name, strings.TrimSpace(errOut.String()))
		}
		elevated := false
		for _, op := range got {
			elevated = elevated || op.Elevated
		}
		if elevated == marked[name] {
			continue
		}
		if _, ok := markerRunExceptions[name]; ok {
			used[name] = true
			continue
		}
		if elevated {
			t.Errorf("%s reaches an operation that needs elevated rights, and does not show %q", name, apispec.Marker)
		} else {
			t.Errorf("%s shows %q, but nothing it reaches needs elevated rights", name, apispec.Marker)
		}
	}
	for name := range markerRuns {
		if _, ok := marked[name]; !ok {
			t.Errorf("markerRuns names %s, which is not a command or a marked flag", name)
		}
	}
	for name, why := range markerRunExceptions {
		if !used[name] {
			t.Errorf("exception %s (%s) excuses nothing any more — remove it", name, why)
		}
	}
}
