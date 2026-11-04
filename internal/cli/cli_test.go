package cli

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/mimiry-com/mimiry-sdk/internal/rpc"
	"github.com/mimiry-com/mimiry-sdk/mimiry"
)

// request is one call the fake API received, sign-in excluded.
type request struct {
	Method, Path string
	Body         map[string]any
}

// fakeAPI answers sign-in and records every other request, answering it with
// reply. It returns the --host and --key to pass, and the key's public half in
// authorized_keys form.
type fakeAPI struct {
	host, key, pub string

	mu   sync.Mutex
	reqs []request
}

func newFakeAPI(t *testing.T, reply string) *fakeAPI {
	t.Helper()
	t.Setenv("MIMIRY_HOME", t.TempDir())
	t.Setenv("MIMIRY_HOST", "")
	t.Setenv("MIMIRY_SSH_KEY", "")
	f := &fakeAPI{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/auth/v1/token" {
			io.WriteString(w, `{"access_token":"tok","expires_in":300,"user_id":"u-1"}`)
			return
		}
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(401)
			return
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.reqs = append(f.reqs, request{r.Method, r.URL.Path, body})
		f.mu.Unlock()
		io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)

	pubKey, key, _ := ed25519.GenerateKey(nil)
	block, err := ssh.MarshalPrivateKey(key, "")
	if err != nil {
		t.Fatal(err)
	}
	f.key = filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(f.key, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	sp, _ := ssh.NewPublicKey(pubKey)
	f.pub = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sp)))
	f.host = srv.URL
	return f
}

// run runs the CLI against the fake and returns the exit code, stdout and stderr.
func (f *fakeAPI) run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	all := append([]string{"--host", f.host, "--key", f.key}, args...)
	code := Run(context.Background(), all, strings.NewReader(""), &out, &errOut)
	return code, out.String(), errOut.String()
}

func (f *fakeAPI) requests() []request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]request(nil), f.reqs...)
}

// only returns the single request the command made.
func (f *fakeAPI) only(t *testing.T) request {
	t.Helper()
	r := f.requests()
	if len(r) != 1 {
		t.Fatalf("want 1 request, got %d: %+v", len(r), r)
	}
	return r[0]
}

const accepted = `{"id":"s-1","status":"submitted","message":"Session submitted","status_url":"/x"}`

func TestSessionCreateBody(t *testing.T) {
	f := newFakeAPI(t, accepted)
	code, out, stderr := f.run(t, "session", "create", "img:1",
		"--name", "train", "--gpu", "A100,H100", "--vram", "80", "--max-rate", "2.5", "--location", "FIN-01",
		"--min-memory", "64", "--command", "python train.py",
		"--env", "A=1", "--env", "B=x=y", "--volume", "data:/mnt/data", "--org", "o-1",
		"--auto-terminate", "on_success", "--max-duration", "2h")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	r := f.only(t)
	if r.Method != "POST" || r.Path != "/api/compute/v1/sessions" {
		t.Fatalf("%s %s", r.Method, r.Path)
	}
	got, _ := json.Marshal(r.Body)
	want := `{"auto_terminate":{"mode":"on_success","timeout_seconds":7200},"command":"python train.py",` +
		`"environment_vars":{"A":"1","B":"x=y"},"gpu":{"location":"FIN-01","max_hourly_rate":2.5,` +
		`"min_memory_gb":64,"types":["A100","H100"],"vram_gb":[80]},"image":{"uri":"img:1"},"name":"train",` +
		`"org_id":"o-1","ssh_public_key":"` + f.pub + `","volume_mounts":[{"mount_path":"/mnt/data","volume_name":"data"}]}`
	if string(got) != want {
		t.Fatalf("body\n got %s\nwant %s", got, want)
	}
	if !strings.Contains(out, "s-1") || !strings.Contains(out, "mimiry session wait s-1") {
		t.Fatalf("output %q", out)
	}
}

func TestSessionCreateSSH(t *testing.T) {
	pubFile := filepath.Join(t.TempDir(), "k.pub")
	os.WriteFile(pubFile, []byte("ssh-ed25519 AAAAother me@host\n"), 0o600)

	for _, tc := range []struct {
		name  string
		flags []string
		want  map[string]any // ssh fields expected in the body; absent ones must be absent
	}{
		{"default is the sign-in key", nil, map[string]any{"ssh_public_key": "PUB"}},
		{"no-ssh", []string{"--no-ssh"}, map[string]any{"ssh_enabled": false}},
		{"key id", []string{"--ssh-key-id", "k-1"}, map[string]any{"ssh_key_id": "k-1"}},
		{"key file", []string{"--ssh-public-key", pubFile}, map[string]any{"ssh_public_key": "ssh-ed25519 AAAAother me@host"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeAPI(t, accepted)
			args := append([]string{"session", "create", "img", "--name", "n", "--gpu", "A100"}, tc.flags...)
			if code, _, stderr := f.run(t, args...); code != 0 {
				t.Fatalf("exit %d: %s", code, stderr)
			}
			body := f.only(t).Body
			for _, k := range []string{"ssh_enabled", "ssh_key_id", "ssh_public_key"} {
				want, ok := tc.want[k]
				if want == "PUB" {
					want = f.pub
				}
				if got, has := body[k]; has != ok || got != want {
					t.Errorf("%s: got %v (present %v), want %v (present %v)", k, got, has, want, ok)
				}
			}
		})
	}
}

func TestSessionCreateRefusesBeforeCalling(t *testing.T) {
	priv := filepath.Join(t.TempDir(), "id")
	os.WriteFile(priv, []byte("-----BEGIN OPENSSH PRIVATE KEY-----\nx\n"), 0o600)

	for name, args := range map[string][]string{
		"no image":          {"--name", "n", "--gpu", "A100"},
		"no name":           {"img", "--gpu", "A100"},
		"no machine":        {"img", "--name", "n"},
		"bad vram":          {"img", "--name", "n", "--vram", "big"},
		"bad env":           {"img", "--name", "n", "--gpu", "A", "--env", "NOEQUALS"},
		"env without name":  {"img", "--name", "n", "--gpu", "A", "--env", "=x"},
		"relative mount":    {"img", "--name", "n", "--gpu", "A", "--volume", "data:mnt"},
		"bad mode":          {"img", "--name", "n", "--gpu", "A", "--auto-terminate", "sometimes"},
		"no-ssh with key":   {"img", "--name", "n", "--gpu", "A", "--no-ssh", "--ssh-key-id", "k"},
		"two keys":          {"img", "--name", "n", "--gpu", "A", "--ssh-key-id", "k", "--ssh-public-key", priv},
		"private key given": {"img", "--name", "n", "--gpu", "A", "--ssh-public-key", priv},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakeAPI(t, accepted)
			code, _, _ := f.run(t, append([]string{"session", "create"}, args...)...)
			if code != 2 {
				t.Fatalf("exit %d, want 2", code)
			}
			if n := len(f.requests()); n != 0 {
				t.Fatalf("made %d requests", n)
			}
		})
	}
}

func TestSessionCreateMaxDurationAlone(t *testing.T) {
	f := newFakeAPI(t, accepted)
	if code, _, stderr := f.run(t, "session", "create", "img", "--name", "n", "--gpu", "A100", "--max-duration", "30m"); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	got, _ := json.Marshal(f.only(t).Body["auto_terminate"])
	if want := `{"mode":"on_complete","timeout_seconds":1800}`; string(got) != want {
		t.Fatalf("auto_terminate %s, want %s", got, want)
	}
}

func TestSessionCreateWaits(t *testing.T) {
	// The fake answers every request with this body: the create reads the id,
	// the wait reads a running session.
	f := newFakeAPI(t, `{"id":"s-1","name":"n","state":"running","message":"ok","created_at":"2026-11-01T00:00:00Z"}`)
	code, out, stderr := f.run(t, "session", "create", "img", "--name", "n", "--gpu", "A100", "--wait")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	r := f.requests()
	if len(r) < 2 || r[0].Method != "POST" || r[1].Method != "GET" || r[1].Path != "/api/compute/v1/sessions/s-1" {
		t.Fatalf("requests %+v", r)
	}
	if !strings.Contains(out, "running") {
		t.Fatalf("output %q", out)
	}
}

func TestVolumeCommands(t *testing.T) {
	const vaccepted = `{"id":"v-1","status":"submitted","message":"ok","status_url":"/x"}`
	for _, tc := range []struct {
		args         []string
		method, path string
		body         string
	}{
		{[]string{"volume", "create", "--name", "data", "--size", "100", "--type", "hdd", "--location", "FIN-01", "--org", "o-1"},
			"POST", "/api/compute/v1/volumes", `{"location":"FIN-01","name":"data","org_id":"o-1","size_gb":100,"volume_type":"hdd"}`},
		{[]string{"volume", "create", "--name", "data", "--size", "100"},
			"POST", "/api/compute/v1/volumes", `{"name":"data","size_gb":100}`},
		{[]string{"volume", "extend", "v-1", "--size", "200"},
			"PATCH", "/api/compute/v1/volumes/v-1", `{"size_gb":200}`},
		{[]string{"volume", "delete", "v-1"},
			"DELETE", "/api/compute/v1/volumes/v-1", `null`},
	} {
		t.Run(strings.Join(tc.args[:2], " "), func(t *testing.T) {
			f := newFakeAPI(t, vaccepted)
			if code, _, stderr := f.run(t, tc.args...); code != 0 {
				t.Fatalf("exit %d: %s", code, stderr)
			}
			r := f.only(t)
			got, _ := json.Marshal(r.Body)
			if r.Method != tc.method || r.Path != tc.path || string(got) != tc.body {
				t.Fatalf("got %s %s %s", r.Method, r.Path, got)
			}
		})
	}
}

func TestVolumeRefusesBeforeCalling(t *testing.T) {
	for name, args := range map[string][]string{
		"create no name":    {"volume", "create", "--size", "10"},
		"create no size":    {"volume", "create", "--name", "d"},
		"create bad type":   {"volume", "create", "--name", "d", "--size", "10", "--type", "ssd"},
		"create positional": {"volume", "create", "d", "--name", "d", "--size", "10"},
		"extend no size":    {"volume", "extend", "v-1"},
		"delete no id":      {"volume", "delete"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakeAPI(t, `{}`)
			if code, _, _ := f.run(t, args...); code != 2 {
				t.Fatalf("exit %d, want 2", code)
			}
			if n := len(f.requests()); n != 0 {
				t.Fatalf("made %d requests", n)
			}
		})
	}
}

func TestCreateHelp(t *testing.T) {
	f := newFakeAPI(t, `{}`)
	code, _, stderr := f.run(t, "session", "create", "--help")
	if code != 0 || !strings.Contains(stderr, "the key you sign in with") || !strings.Contains(stderr, "-max-duration") {
		t.Fatalf("exit %d, help %q", code, stderr)
	}
}

// --version needs no host or key: it is what someone runs before anything is
// set up, and what a bug report asks for when the platform cannot be reached.
func TestVersionNeedsNoHostOrKey(t *testing.T) {
	t.Setenv("MIMIRY_HOME", t.TempDir())
	t.Setenv("MIMIRY_HOST", "")
	t.Setenv("MIMIRY_SSH_KEY", "")

	var out, errOut bytes.Buffer
	if code := Run(context.Background(), []string{"--version"}, nil, &out, &errOut); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut.String())
	}
	want := "mimiry " + mimiry.Version + " (rpc protocol " + fmt.Sprint(rpc.Protocol) + ", "
	if !strings.HasPrefix(out.String(), want) {
		t.Errorf("got %q, want it to start %q", out.String(), want)
	}

	out.Reset()
	if code := Run(context.Background(), []string{"--json", "--version"}, nil, &out, &errOut); code != 0 {
		t.Fatalf("--json: exit %d, stderr %q", code, errOut.String())
	}
	var got struct {
		Version  string `json:"version"`
		Protocol int    `json:"protocol"`
		Go       string `json:"go"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("--json output is not JSON: %v: %q", err, out.String())
	}
	if got.Version != mimiry.Version || got.Protocol != rpc.Protocol || !strings.HasPrefix(got.Go, "go") {
		t.Errorf("--json: got %+v", got)
	}
}
