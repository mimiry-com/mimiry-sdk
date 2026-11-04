package cli

import (
	"strings"
	"testing"
)

// One reply serves every step: the org by slug, its members, the accepted
// change and its outcome.
func orgReply(status, errMsg string) string {
	return `{"id":"550e8400-e29b-41d4-a716-446655440000","members":[{"user_id":"u-2","email":"Jane@example.com","role":"member"}],` +
		`"request_id":"r-1","status":"` + status + `","error":"` + errMsg + `","status_url":"/x","message":"Role update request accepted"}`
}

func TestSetRoleResolvesTheOrgAndTheMemberAndWaits(t *testing.T) {
	f := newFakeAPI(t, orgReply("completed", ""))
	code, out, stderr := f.run(t, "org", "set-role", "o-acme", "jane@example.com", "admin")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	var got []string
	for _, r := range f.requests() {
		got = append(got, r.Method+" "+r.Path)
	}
	const org = "/api/organizations/v1/550e8400-e29b-41d4-a716-446655440000"
	want := []string{
		"GET /api/organizations/v1/by-slug/o-acme",
		"GET " + org + "/members",
		"PATCH " + org + "/members/u-2/role",
		"GET /api/organizations/v1/operations/r-1",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("requests\n got %v\nwant %v", got, want)
	}
	if body := f.requests()[2].Body; len(body) != 1 || body["role"] != "admin" {
		t.Errorf("body %v, want the role only", body)
	}
	if !strings.Contains(out, "Done.") {
		t.Errorf("output %q", out)
	}
}

// A refusal made when the change is processed is a failure, not "accepted".
func TestARefusedChangeFails(t *testing.T) {
	f := newFakeAPI(t, orgReply("failed", "Only the organization owner can wind it down"))
	code, _, stderr := f.run(t, "org", "wind-down", "550e8400-e29b-41d4-a716-446655440000", "--readable-days", "30")
	if code != 1 || !strings.Contains(stderr, "Only the organization owner can wind it down") {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
}

func TestOrgUsageIsCheckedBeforeCalling(t *testing.T) {
	f := newFakeAPI(t, orgReply("completed", ""))
	for _, args := range [][]string{
		{"org", "update", "o-acme"},          // nothing to change
		{"org", "wind-down", "o-acme"},       // readable days required
		{"org", "create", "Acme"},            // slug required
		{"org", "set-role", "o-acme", "u-2"}, // role missing
	} {
		if code, _, _ := f.run(t, args...); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
	if n := len(f.requests()); n != 0 {
		t.Errorf("%d requests made for refused usage", n)
	}
}
