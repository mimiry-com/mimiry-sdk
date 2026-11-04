package mimiry

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"net/url"
	"regexp"
)

const orgAPI = "/api/organizations/v1"

// OrgsService manages organisations, their members and their invitations.
//
// Every change is carried out asynchronously: the call answers once the
// request is accepted, with an OperationAccepted, and WaitOperation follows it
// to the outcome. Accepted is not done, and not necessarily allowed: some
// checks are made only when the request is processed, and a refusal then ends
// the operation as failed.
type OrgsService struct{ c *Client }

// Orgs is the organisation API.
func (c *Client) Orgs() *OrgsService { return &OrgsService{c} }

// OrgListOptions pages and sorts the caller's organisations.
type OrgListOptions struct {
	ListOptions
	// Sort is name (the default), slug, role, member_count or created_at.
	// role is the caller's role in each organisation.
	Sort string `json:"sort,omitempty"`
	// Dir is asc (the default) or desc.
	Dir string `json:"dir,omitempty"`
}

func (o OrgListOptions) query() url.Values {
	q := url.Values{}
	o.ListOptions.encode(q)
	if o.Sort != "" {
		q.Set("sort", o.Sort)
	}
	if o.Dir != "" {
		q.Set("dir", o.Dir)
	}
	return q
}

type orgList struct {
	Organizations []Org `json:"organizations"`
	TotalCount    int   `json:"total_count"`
	Limit         int   `json:"limit"`
	Offset        int   `json:"offset"`
}

// List returns one page of the organisations the caller owns or belongs to.
func (s *OrgsService) List(ctx context.Context, o OrgListOptions) (*Page[Org], error) {
	var out orgList
	if err := s.c.Do(ctx, http.MethodGet, orgAPI, o.query(), nil, &out); err != nil {
		return nil, err
	}
	return &Page[Org]{Items: nonNil(out.Organizations), TotalCount: out.TotalCount,
		Limit: out.Limit, Offset: out.Offset}, nil
}

// All iterates every organisation the caller owns or belongs to.
func (s *OrgsService) All(ctx context.Context, o OrgListOptions) iter.Seq2[Org, error] {
	return all(ctx, func(ctx context.Context, p ListOptions) (*Page[Org], error) {
		o.ListOptions = p
		return s.List(ctx, o)
	})
}

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Get returns an organisation, with its members and settings, by ID or by
// slug. Members only.
func (s *OrgsService) Get(ctx context.Context, idOrSlug string) (*Org, error) {
	path := orgAPI + "/by-slug/" + url.PathEscape(idOrSlug)
	if uuidPattern.MatchString(idOrSlug) {
		path = orgAPI + "/" + url.PathEscape(idOrSlug)
	}
	var out Org
	if err := s.c.Do(ctx, http.MethodGet, path, nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Create creates an organisation, owned by the caller. On completion the
// operation's Result["organization"] is the new organisation.
func (s *OrgsService) Create(ctx context.Context, req CreateOrgRequest) (*OperationAccepted, error) {
	return s.send(ctx, http.MethodPost, orgAPI, req)
}

// Update changes an organisation's name or slug; only the fields set are
// changed. The owner or an admin may. (requires elevated rights)
func (s *OrgsService) Update(ctx context.Context, orgID string, req UpdateOrgRequest) (*OperationAccepted, error) {
	return s.send(ctx, http.MethodPatch, orgPath(orgID), req)
}

// Delete deletes an organisation. Only the owner may, and only once it has no
// other members and no invitation that can still be accepted; otherwise the
// operation fails and says what is in the way. StartWindDown retires an
// organisation that still has members. (requires elevated rights)
func (s *OrgsService) Delete(ctx context.Context, orgID string) (*OperationAccepted, error) {
	return s.send(ctx, http.MethodDelete, orgPath(orgID), nil)
}

// StartWindDown starts retiring an organisation: it is frozen, members keep
// read access for ReadableDays, and it can be deleted after that. Only the
// owner may. (requires elevated rights)
func (s *OrgsService) StartWindDown(ctx context.Context, orgID string, req WindDownRequest) (*OperationAccepted, error) {
	return s.send(ctx, http.MethodPost, orgPath(orgID)+"/wind-down", req)
}

// CancelWindDown ends a wind-down and unfreezes the organisation. Only the
// owner may. (requires elevated rights)
func (s *OrgsService) CancelWindDown(ctx context.Context, orgID string) (*OperationAccepted, error) {
	return s.send(ctx, http.MethodDelete, orgPath(orgID)+"/wind-down", nil)
}

// TransferOwnership hands an organisation to another member. Only the owner
// may, and the new owner must already be a member. (requires elevated rights)
func (s *OrgsService) TransferOwnership(ctx context.Context, orgID, newOwnerID string) (*OperationAccepted, error) {
	body := struct {
		NewOwnerID string `json:"new_owner_id"`
	}{newOwnerID}
	return s.send(ctx, http.MethodPost, orgPath(orgID)+"/transfer-ownership", body)
}

// Members returns every member of an organisation. Members only.
func (s *OrgsService) Members(ctx context.Context, orgID string) ([]Member, error) {
	var out struct {
		Members []Member `json:"members"`
	}
	if err := s.c.Do(ctx, http.MethodGet, orgPath(orgID)+"/members", nil, nil, &out); err != nil {
		return nil, err
	}
	return nonNil(out.Members), nil
}

// Invite invites someone to an organisation by email address or username.
// The owner or an admin may. (requires elevated rights)
func (s *OrgsService) Invite(ctx context.Context, orgID string, req InviteRequest) (*OperationAccepted, error) {
	return s.send(ctx, http.MethodPost, orgPath(orgID)+"/members/invite", req)
}

// SetMemberRole changes a member's role to admin or member. Only the owner
// may. (requires elevated rights)
func (s *OrgsService) SetMemberRole(ctx context.Context, orgID, userID string, role OrgRole) (*OperationAccepted, error) {
	body := struct {
		Role OrgRole `json:"role"`
	}{role}
	return s.send(ctx, http.MethodPatch, orgPath(orgID)+"/members/"+url.PathEscape(userID)+"/role", body)
}

// RemoveMember removes a member. The owner or an admin may remove anyone but
// the owner. Leave removes the caller. (requires elevated rights)
func (s *OrgsService) RemoveMember(ctx context.Context, orgID, userID string) (*OperationAccepted, error) {
	return s.send(ctx, http.MethodDelete, orgPath(orgID)+"/members/"+url.PathEscape(userID), nil)
}

// Leave removes the caller from an organisation. The owner cannot leave; they
// transfer ownership first.
func (s *OrgsService) Leave(ctx context.Context, orgID string) (*OperationAccepted, error) {
	t, err := s.c.Token(ctx)
	if err != nil {
		return nil, err
	}
	if t.UserID == "" {
		return nil, errors.New("mimiry: leave: the token does not say who the caller is; sign in again")
	}
	return s.send(ctx, http.MethodDelete, orgPath(orgID)+"/members/"+url.PathEscape(t.UserID), nil)
}

type invitationList struct {
	Invitations []Invitation `json:"invitations"`
	TotalCount  int          `json:"total_count"`
	Limit       int          `json:"limit"`
	Offset      int          `json:"offset"`
}

// Invitations returns one page of an organisation's invitations that can
// still be accepted. Members only.
func (s *OrgsService) Invitations(ctx context.Context, orgID string, o ListOptions) (*Page[Invitation], error) {
	q := url.Values{}
	o.encode(q)
	var out invitationList
	if err := s.c.Do(ctx, http.MethodGet, orgPath(orgID)+"/invitations", q, nil, &out); err != nil {
		return nil, err
	}
	return &Page[Invitation]{Items: nonNil(out.Invitations), TotalCount: out.TotalCount,
		Limit: out.Limit, Offset: out.Offset}, nil
}

// AllInvitations iterates every invitation of an organisation that can still
// be accepted.
func (s *OrgsService) AllInvitations(ctx context.Context, orgID string) iter.Seq2[Invitation, error] {
	return all(ctx, func(ctx context.Context, p ListOptions) (*Page[Invitation], error) {
		return s.Invitations(ctx, orgID, p)
	})
}

// RevokeInvitation withdraws a pending invitation. The owner or an admin may.
// (requires elevated rights)
func (s *OrgsService) RevokeInvitation(ctx context.Context, orgID, invitationID string) (*OperationAccepted, error) {
	return s.send(ctx, http.MethodDelete, orgPath(orgID)+"/invitations/"+url.PathEscape(invitationID), nil)
}

// Invitation says whether the caller may accept the invitation with this
// token, before they try. Only the invitee is told which organisation it is.
func (s *OrgsService) Invitation(ctx context.Context, token string) (*InvitationPreview, error) {
	var out InvitationPreview
	if err := s.c.Do(ctx, http.MethodGet, orgAPI+"/invitations/"+url.PathEscape(token), nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// AcceptInvitation accepts the invitation with this token, which must be
// addressed to the caller.
func (s *OrgsService) AcceptInvitation(ctx context.Context, token string) (*OperationAccepted, error) {
	return s.send(ctx, http.MethodPost, orgAPI+"/invitations/"+url.PathEscape(token)+"/accept", nil)
}

// Operation reads the status of an organisation change. A status is kept for
// 15 minutes.
func (s *OrgsService) Operation(ctx context.Context, requestID string) (*Operation, error) {
	var out Operation
	if err := s.c.Do(ctx, http.MethodGet, orgAPI+"/operations/"+url.PathEscape(requestID), nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// OperationFailedError is returned by WaitOperation when the change was
// refused or could not be made. Operation.Error says why.
type OperationFailedError struct {
	Operation *Operation
}

func (e *OperationFailedError) Error() string {
	if e.Operation.Error != "" {
		return fmt.Sprintf("operation %s failed: %s", e.Operation.RequestID, e.Operation.Error)
	}
	return fmt.Sprintf("operation %s failed", e.Operation.RequestID)
}

// WaitOperation polls an organisation change until it has completed, and
// returns it, or failed, and returns *OperationFailedError.
func (s *OrgsService) WaitOperation(ctx context.Context, requestID string, o WaitOptions) (*Operation, error) {
	return poll(ctx, o, func(ctx context.Context) (*Operation, bool, error) {
		op, err := s.Operation(ctx, requestID)
		if err != nil {
			return nil, false, err
		}
		switch op.Status {
		case OperationCompleted:
			return op, true, nil
		case OperationFailed:
			return op, true, &OperationFailedError{Operation: op}
		}
		return op, false, nil
	})
}

func (s *OrgsService) send(ctx context.Context, method, path string, body any) (*OperationAccepted, error) {
	var out OperationAccepted
	if err := s.c.Do(ctx, method, path, nil, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func orgPath(orgID string) string { return orgAPI + "/" + url.PathEscape(orgID) }
