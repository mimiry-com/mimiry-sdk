package mimiry

// Every function that needs elevated rights says so, and no other does.
//
// Public surfaces show such functions to everyone, marked "(requires elevated
// rights)": the server decides, and the marker tells the reader why a call
// may be refused. Which operations need elevated rights is decided in one
// place, the published documents (x-requires-elevated-rights). This test
// calls every service method against a recording stand-in for the API, looks
// up the documented operations each one reached, and holds its doc comment to
// them. A method that reaches no documented operation fails too: the library
// must not call what the documents do not describe.

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/mimiry-com/mimiry-sdk/internal/apispec"
)

const testOrgID = "550e8400-e29b-41d4-a716-446655440000"

// markerCalls exercises each method once. The key is "<Service>.<Method>".
var markerCalls = map[string]func(context.Context, *Client){
	"SessionsService.Create":    func(ctx context.Context, c *Client) { c.Sessions().Create(ctx, CreateSessionRequest{}) },
	"SessionsService.List":      func(ctx context.Context, c *Client) { c.Sessions().List(ctx, SessionListOptions{}) },
	"SessionsService.All":       func(ctx context.Context, c *Client) { drain(c.Sessions().All(ctx, SessionListOptions{})) },
	"SessionsService.Get":       func(ctx context.Context, c *Client) { c.Sessions().Get(ctx, "s-1", GetSessionOptions{}) },
	"SessionsService.Terminate": func(ctx context.Context, c *Client) { c.Sessions().Terminate(ctx, "s-1") },
	"SessionsService.Logs":      func(ctx context.Context, c *Client) { c.Sessions().Logs(ctx, "s-1", LogsOptions{}) },
	"VolumesService.Create":     func(ctx context.Context, c *Client) { c.Volumes().Create(ctx, CreateVolumeRequest{}) },
	"VolumesService.List":       func(ctx context.Context, c *Client) { c.Volumes().List(ctx, VolumeListOptions{}) },
	"VolumesService.All":        func(ctx context.Context, c *Client) { drain(c.Volumes().All(ctx, VolumeListOptions{})) },
	"VolumesService.Get":        func(ctx context.Context, c *Client) { c.Volumes().Get(ctx, "v-1") },
	"VolumesService.Delete":     func(ctx context.Context, c *Client) { c.Volumes().Delete(ctx, "v-1") },
	"VolumesService.Extend":     func(ctx context.Context, c *Client) { c.Volumes().Extend(ctx, "v-1", 200) },
	"GPUsService.Catalog":       func(ctx context.Context, c *Client) { c.GPUs().Catalog(ctx) },
	"GPUsService.Availability":  func(ctx context.Context, c *Client) { c.GPUs().Availability(ctx, AvailabilityOptions{}) },
	"AccountService.Balance":    func(ctx context.Context, c *Client) { c.Account().Balance(ctx) },
	"AccountService.OrgBalance": func(ctx context.Context, c *Client) { c.Account().OrgBalance(ctx, "o-acme") },
	"AccountService.Transactions": func(ctx context.Context, c *Client) {
		c.Account().Transactions(ctx, TransactionListOptions{})
	},
	"AccountService.AllTransactions": func(ctx context.Context, c *Client) {
		drain(c.Account().AllTransactions(ctx, TransactionListOptions{}))
	},
	"AccountService.Quota": func(ctx context.Context, c *Client) { c.Account().Quota(ctx) },

	"OrgsService.List": func(ctx context.Context, c *Client) { c.Orgs().List(ctx, OrgListOptions{}) },
	"OrgsService.All":  func(ctx context.Context, c *Client) { drain(c.Orgs().All(ctx, OrgListOptions{})) },
	"OrgsService.Get": func(ctx context.Context, c *Client) {
		c.Orgs().Get(ctx, testOrgID)
		c.Orgs().Get(ctx, "o-acme")
	},
	"OrgsService.Create": func(ctx context.Context, c *Client) { c.Orgs().Create(ctx, CreateOrgRequest{}) },
	"OrgsService.Update": func(ctx context.Context, c *Client) { c.Orgs().Update(ctx, testOrgID, UpdateOrgRequest{}) },
	"OrgsService.Delete": func(ctx context.Context, c *Client) { c.Orgs().Delete(ctx, testOrgID) },
	"OrgsService.StartWindDown": func(ctx context.Context, c *Client) {
		c.Orgs().StartWindDown(ctx, testOrgID, WindDownRequest{ReadableDays: 30})
	},
	"OrgsService.CancelWindDown":    func(ctx context.Context, c *Client) { c.Orgs().CancelWindDown(ctx, testOrgID) },
	"OrgsService.TransferOwnership": func(ctx context.Context, c *Client) { c.Orgs().TransferOwnership(ctx, testOrgID, "u-2") },
	"OrgsService.Members":           func(ctx context.Context, c *Client) { c.Orgs().Members(ctx, testOrgID) },
	"OrgsService.Invite":            func(ctx context.Context, c *Client) { c.Orgs().Invite(ctx, testOrgID, InviteRequest{}) },
	"OrgsService.SetMemberRole": func(ctx context.Context, c *Client) {
		c.Orgs().SetMemberRole(ctx, testOrgID, "u-2", OrgRoleAdmin)
	},
	"OrgsService.RemoveMember":     func(ctx context.Context, c *Client) { c.Orgs().RemoveMember(ctx, testOrgID, "u-2") },
	"OrgsService.Leave":            func(ctx context.Context, c *Client) { c.Orgs().Leave(ctx, testOrgID) },
	"OrgsService.Invitations":      func(ctx context.Context, c *Client) { c.Orgs().Invitations(ctx, testOrgID, ListOptions{}) },
	"OrgsService.AllInvitations":   func(ctx context.Context, c *Client) { drain(c.Orgs().AllInvitations(ctx, testOrgID)) },
	"OrgsService.RevokeInvitation": func(ctx context.Context, c *Client) { c.Orgs().RevokeInvitation(ctx, testOrgID, "i-1") },
	"OrgsService.Invitation":       func(ctx context.Context, c *Client) { c.Orgs().Invitation(ctx, "tok-1") },
	"OrgsService.AcceptInvitation": func(ctx context.Context, c *Client) { c.Orgs().AcceptInvitation(ctx, "tok-1") },
	"OrgsService.Operation":        func(ctx context.Context, c *Client) { c.Orgs().Operation(ctx, "r-1") },
}

// markerDelegates are methods that only repeat another one (the waits poll a
// read), and are held to the same marker as it.
var markerDelegates = map[string]string{
	"SessionsService.WaitReady":      "SessionsService.Get",
	"VolumesService.WaitProvisioned": "VolumesService.Get",
	"OrgsService.WaitOperation":      "OrgsService.Operation",
}

// markerExceptions: a method whose marker differs from its operations', and why.
var markerExceptions = map[string]string{
	"OrgsService.Leave": "removes the caller, which every member may; the operation is marked because removing anyone else needs elevated rights",
}

func drain[T any](seq func(func(T, error) bool)) {
	for range seq {
	}
}

type recorded struct{ method, path string }

func recordingClient(t *testing.T) (*Client, func() []recorded) {
	t.Helper()
	var mu sync.Mutex
	var reqs []recorded
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/token" {
			json.NewEncoder(w).Encode(map[string]any{"access_token": "tok", "expires_in": 300, "user_id": "u-1"})
			return
		}
		mu.Lock()
		reqs = append(reqs, recorded{r.Method, r.URL.EscapedPath()})
		mu.Unlock()
		io.WriteString(w, `{}`)
	}))
	t.Cleanup(srv.Close)
	c := newTestClient(t, srv, genKey(t), nil)
	return c, func() []recorded {
		mu.Lock()
		defer mu.Unlock()
		out := reqs
		reqs = nil
		return out
	}
}

// serviceMethods reads every exported method of a *…Service type from this
// package's source, with whether its doc comment carries the marker.
func serviceMethods(t *testing.T) map[string]bool {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.ParseComments)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Recv == nil || !fd.Name.IsExported() {
				continue
			}
			star, ok := fd.Recv.List[0].Type.(*ast.StarExpr)
			if !ok {
				continue
			}
			recv, ok := star.X.(*ast.Ident)
			if !ok || !strings.HasSuffix(recv.Name, "Service") {
				continue
			}
			out[recv.Name+"."+fd.Name.Name] = strings.Contains(fd.Doc.Text(), apispec.Marker)
		}
	}
	if len(out) == 0 {
		t.Fatal("found no service methods — the read is broken")
	}
	return out
}

func TestElevatedRightsAreMarked(t *testing.T) {
	docs, err := apispec.LoadAll(specDir)
	if err != nil {
		t.Fatal(err)
	}
	methods := serviceMethods(t)
	c, take := recordingClient(t)
	ctx := context.Background()

	elevated := map[string]bool{}
	names := make([]string, 0, len(methods))
	for name := range methods {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		call, ok := markerCalls[name]
		if !ok {
			continue
		}
		call(ctx, c)
		reqs := take()
		if len(reqs) == 0 {
			t.Errorf("%s made no request", name)
			continue
		}
		for _, r := range reqs {
			op, ok := apispec.Match(docs, r.method, r.path)
			if !ok {
				t.Errorf("%s called %s %s, which no document describes", name, r.method, r.path)
				continue
			}
			elevated[name] = elevated[name] || op.Elevated
		}
	}
	for name, of := range markerDelegates {
		elevated[name] = elevated[of]
	}

	used := map[string]bool{}
	for _, name := range names {
		_, called := markerCalls[name]
		_, delegated := markerDelegates[name]
		if !called && !delegated {
			t.Errorf("%s is not exercised here; add it to markerCalls or markerDelegates", name)
			continue
		}
		if methods[name] == elevated[name] {
			continue
		}
		if _, ok := markerExceptions[name]; ok {
			used[name] = true
			continue
		}
		if elevated[name] {
			t.Errorf("%s reaches an operation that needs elevated rights; its doc comment must say %q", name, apispec.Marker)
		} else {
			t.Errorf("%s says %q, but nothing it reaches needs them", name, apispec.Marker)
		}
	}
	for name := range markerCalls {
		if _, ok := methods[name]; !ok {
			t.Errorf("markerCalls names %s, which is not a service method", name)
		}
	}
	for name, why := range markerExceptions {
		if !used[name] {
			t.Errorf("exception %s (%s) excuses nothing any more — remove it", name, why)
		}
	}
}
