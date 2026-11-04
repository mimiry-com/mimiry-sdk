package rpc

// describe's "elevated" list is the ops that reach an operation the published
// documents mark as needing elevated rights: no more, no fewer. The Python
// package marks its functions from that list, so a wrong entry here is a wrong
// marker there. Every op is called against a recording stand-in for the API,
// and the documented operations it reached decide.

import (
	"crypto/ed25519"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/mimiry-com/mimiry-sdk/internal/apispec"
	"github.com/mimiry-com/mimiry-sdk/mimiry"
)

const orgID = "550e8400-e29b-41d4-a716-446655440000"

// opParams are the params each op is called with. An op that reaches no
// documented operation (sign-in) is listed with params and reaches nothing.
var opParams = map[string]string{
	"auth.token":    `{}`,
	"auth.sign_in":  `{}`,
	"auth.sign_out": `{}`,

	"sessions.create":     `{"name":"x"}`,
	"sessions.list":       `{}`,
	"sessions.get":        `{"id":"s-1"}`,
	"sessions.terminate":  `{"id":"s-1"}`,
	"sessions.logs":       `{"id":"s-1"}`,
	"sessions.wait_ready": `{"id":"s-1"}`,

	"volumes.create":           `{"name":"x"}`,
	"volumes.list":             `{}`,
	"volumes.get":              `{"id":"v-1"}`,
	"volumes.delete":           `{"id":"v-1"}`,
	"volumes.extend":           `{"id":"v-1","new_size_gb":200}`,
	"volumes.wait_provisioned": `{"id":"v-1"}`,

	"gpus.catalog":      `{}`,
	"gpus.availability": `{}`,

	"account.balance":      `{}`,
	"account.org_balance":  `{"id_or_slug":"o-acme"}`,
	"account.transactions": `{}`,
	"account.quota":        `{}`,

	"orgs.list":               `{}`,
	"orgs.get":                `{"id_or_slug":"o-acme"}`,
	"orgs.create":             `{"name":"Acme","slug":"o-acme"}`,
	"orgs.update":             `{"org_id":"` + orgID + `","name":"Acme 2"}`,
	"orgs.delete":             `{"org_id":"` + orgID + `"}`,
	"orgs.start_wind_down":    `{"org_id":"` + orgID + `","readable_days":30}`,
	"orgs.cancel_wind_down":   `{"org_id":"` + orgID + `"}`,
	"orgs.transfer_ownership": `{"org_id":"` + orgID + `","new_owner_id":"u-2"}`,
	"orgs.members":            `{"org_id":"` + orgID + `"}`,
	"orgs.invite":             `{"org_id":"` + orgID + `","email":"jane@example.com","role":"member"}`,
	"orgs.set_member_role":    `{"org_id":"` + orgID + `","user_id":"u-2","role":"admin"}`,
	"orgs.remove_member":      `{"org_id":"` + orgID + `","user_id":"u-2"}`,
	"orgs.leave":              `{"org_id":"` + orgID + `"}`,
	"orgs.invitations":        `{"org_id":"` + orgID + `"}`,
	"orgs.revoke_invitation":  `{"org_id":"` + orgID + `","invitation_id":"i-1"}`,
	"orgs.invitation":         `{"token":"tok-1"}`,
	"orgs.accept_invitation":  `{"token":"tok-1"}`,
	"orgs.operation":          `{"id":"r-1"}`,
	"orgs.wait_operation":     `{"id":"r-1"}`,
}

// opExceptions: an op whose listing differs from the operations it reaches, and why.
var opExceptions = map[string]string{
	"orgs.leave": "removes the caller, which every member may; the operation is marked because removing anyone else needs elevated rights",
}

func TestDescribeListsExactlyTheElevatedOps(t *testing.T) {
	docs, err := apispec.LoadAll("../../spec")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var reached []apispec.Op
	var unknown []string
	// Not api(): it refuses a request without a token before the handler
	// sees it, and the public ops (the GPU catalogue and availability) send
	// none. Every request is recorded here.
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
	signer, _ := ssh.NewSignerFromKey(key)
	factory := func(string, string) (*mimiry.Client, error) {
		return mimiry.New(mimiry.Options{Host: srv.URL, Signer: signer})
	}

	names := make([]string, 0, len(ops))
	for n := range ops {
		names = append(names, n)
	}
	sort.Strings(names)
	used := map[string]bool{}
	for _, name := range names {
		params, ok := opParams[name]
		if !ok {
			t.Errorf("op %s has no params in opParams; add it", name)
			continue
		}
		mu.Lock()
		reached, unknown = nil, nil
		mu.Unlock()
		// The waits poll a stand-in that never finishes; the first poll is
		// what counts, and the timeout ends them.
		serve(t, factory, `{"v":1,"op":"`+name+`","params":`+params+`,"timeout_seconds":0.3}`)

		mu.Lock()
		for _, u := range unknown {
			t.Errorf("%s called %s, which no document describes", name, u)
		}
		if len(reached) == 0 && !strings.HasPrefix(name, "auth.") {
			t.Errorf("%s reached no documented operation; its params may be refused before any call", name)
		}
		elevated := false
		for _, op := range reached {
			elevated = elevated || op.Elevated
		}
		mu.Unlock()
		if elevated == elevatedOps[name] {
			continue
		}
		if _, ok := opExceptions[name]; ok {
			used[name] = true
			continue
		}
		if elevated {
			t.Errorf("%s reaches an operation that needs elevated rights, and is not in elevatedOps", name)
		} else {
			t.Errorf("%s is in elevatedOps, but nothing it reaches needs elevated rights", name)
		}
	}
	for name := range opParams {
		if _, ok := ops[name]; !ok {
			t.Errorf("opParams names %s, which is not an op", name)
		}
	}
	for name, why := range opExceptions {
		if !used[name] {
			t.Errorf("exception %s (%s) excuses nothing any more — remove it", name, why)
		}
	}

	// And describe says what elevatedOps says.
	_, lines := serve(t, nil, `{"v":1,"op":"describe"}`)
	var d struct {
		Elevated []string `json:"elevated"`
	}
	raw, _ := json.Marshal(last(lines)["result"])
	json.Unmarshal(raw, &d)
	if len(d.Elevated) != len(elevatedOps) {
		t.Fatalf("describe lists %d elevated ops, want %d", len(d.Elevated), len(elevatedOps))
	}
	for _, n := range d.Elevated {
		if !elevatedOps[n] {
			t.Errorf("describe lists %s as elevated", n)
		}
	}
}
