package mimiry

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"
)

// orgCall records the one request a test makes and answers it with reply.
type orgCall struct {
	method, path, query string
	body                map[string]any
}

func orgClient(t *testing.T, reply string) (*Client, *orgCall) {
	t.Helper()
	got := &orgCall{}
	c := computeClient(t, func(w http.ResponseWriter, r *http.Request) {
		got.method, got.path, got.query = r.Method, r.URL.EscapedPath(), r.URL.RawQuery
		got.body = nil
		json.NewDecoder(r.Body).Decode(&got.body)
		io.WriteString(w, reply)
	})
	return c, got
}

const acceptedJSON = `{"request_id":"r-1","status":"pending","message":"Organization update in progress",
  "status_url":"/api/organizations/v1/operations/r-1","retry_after":2}`

func TestGetTakesAnIDOrASlug(t *testing.T) {
	c, got := orgClient(t, `{"id":"`+testOrgID+`","slug":"o-acme","members":[{"user_id":"u-1","role":"owner"}]}`)
	ctx := context.Background()

	org, err := c.Orgs().Get(ctx, testOrgID)
	if err != nil || got.path != "/api/organizations/v1/"+testOrgID {
		t.Fatalf("by ID: %v, asked %s", err, got.path)
	}
	if org.Slug != "o-acme" || len(org.Members) != 1 || org.Members[0].Role != OrgRoleOwner {
		t.Errorf("decoded %+v", org)
	}
	if _, err := c.Orgs().Get(ctx, "o-acme"); err != nil || got.path != "/api/organizations/v1/by-slug/o-acme" {
		t.Fatalf("by slug: %v, asked %s", err, got.path)
	}
}

// The body is the role only; the path names the member.
func TestSetMemberRoleSendsTheRoleOnly(t *testing.T) {
	c, got := orgClient(t, acceptedJSON)
	op, err := c.Orgs().SetMemberRole(context.Background(), testOrgID, "u-2", OrgRoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	if got.method != "PATCH" || got.path != "/api/organizations/v1/"+testOrgID+"/members/u-2/role" {
		t.Errorf("asked %s %s", got.method, got.path)
	}
	if len(got.body) != 1 || got.body["role"] != "admin" {
		t.Errorf("body %v, want only the role", got.body)
	}
	if op.RequestID != "r-1" || op.StatusURL == "" {
		t.Errorf("answer %+v", op)
	}
}

func TestLeaveRemovesTheCaller(t *testing.T) {
	c, got := orgClient(t, acceptedJSON)
	if _, err := c.Orgs().Leave(context.Background(), testOrgID); err != nil {
		t.Fatal(err)
	}
	// u-1 is who the fake API's sign-in says the caller is.
	if got.method != "DELETE" || got.path != "/api/organizations/v1/"+testOrgID+"/members/u-1" {
		t.Errorf("asked %s %s", got.method, got.path)
	}
}

func TestRequestBodies(t *testing.T) {
	c, got := orgClient(t, acceptedJSON)
	ctx := context.Background()

	c.Orgs().Invite(ctx, testOrgID, InviteRequest{Username: "jane", Role: OrgRoleMember})
	if len(got.body) != 2 || got.body["username"] != "jane" || got.body["role"] != "member" {
		t.Errorf("invite body %v: an empty email must not be sent", got.body)
	}

	name := "Acme Corporation"
	c.Orgs().Update(ctx, testOrgID, UpdateOrgRequest{Name: &name})
	if len(got.body) != 1 || got.body["name"] != name {
		t.Errorf("update body %v: unset fields must not be sent", got.body)
	}

	c.Orgs().TransferOwnership(ctx, testOrgID, "u-2")
	if got.path != "/api/organizations/v1/"+testOrgID+"/transfer-ownership" || len(got.body) != 1 || got.body["new_owner_id"] != "u-2" {
		t.Errorf("transfer: %s %v", got.path, got.body)
	}

	c.Orgs().StartWindDown(ctx, testOrgID, WindDownRequest{ReadableDays: 30, VolumeGraceDays: 0})
	if got.body["readable_days"] != float64(30) || got.body["volume_grace_days"] != float64(0) {
		t.Errorf("wind-down body %v: a zero grace is a choice and must be sent", got.body)
	}

	c.Orgs().List(ctx, OrgListOptions{ListOptions: ListOptions{Limit: 10}, Sort: "created_at", Dir: "desc"})
	if got.query != "dir=desc&limit=10&sort=created_at" {
		t.Errorf("list query %q", got.query)
	}
}

func TestWaitOperationEndsOnTheOutcome(t *testing.T) {
	h, _ := script(t,
		`202 {"request_id":"r-1","status":"pending"}`,
		`502 bad gateway`,
		`{"request_id":"r-1","status":"completed","result":{"org_id":"o-1","action":"org_updated"}}`,
	)
	c := computeClient(t, h)
	op, err := c.Orgs().WaitOperation(context.Background(), "r-1", fast)
	if err != nil || op.Status != OperationCompleted || op.Result["action"] != "org_updated" {
		t.Fatalf("completed: %+v, %v", op, err)
	}

	h, _ = script(t, `{"request_id":"r-2","status":"failed","error":"Only the organization owner can wind it down"}`)
	c = computeClient(t, h)
	op, err = c.Orgs().WaitOperation(context.Background(), "r-2", fast)
	var failed *OperationFailedError
	if !errors.As(err, &failed) || failed.Operation.Error == "" || op == nil {
		t.Fatalf("failed: %+v, %v", op, err)
	}
	if err.Error() != "operation r-2 failed: Only the organization owner can wind it down" {
		t.Errorf("message %q", err.Error())
	}
}
