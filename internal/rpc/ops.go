package rpc

import (
	"time"

	"github.com/mimiry-com/mimiry-sdk/mimiry"
)

// ops is every operation by name. Params decode into the library's own types,
// so the wire names are the library's json tags and there is no second list.
var ops = map[string]func(*call) (any, error){
	"auth.token":    authToken(false),
	"auth.sign_in":  authToken(true),
	"auth.sign_out": func(cl *call) (any, error) { return struct{}{}, cl.c.SignOut() },

	"sessions.create": func(cl *call) (any, error) {
		var req mimiry.CreateSessionRequest
		if err := cl.decode(&req); err != nil {
			return nil, err
		}
		return cl.c.Sessions().Create(cl.ctx, req)
	},
	"sessions.list": func(cl *call) (any, error) {
		var p struct {
			mimiry.SessionListOptions
			All bool `json:"all,omitzero"`
		}
		if err := cl.decode(&p); err != nil {
			return nil, err
		}
		if p.All {
			return collect(cl.c.Sessions().All(cl.ctx, p.SessionListOptions))
		}
		return page(cl.c.Sessions().List(cl.ctx, p.SessionListOptions))
	},
	"sessions.get": func(cl *call) (any, error) {
		var p struct {
			ID string `json:"id"`
			mimiry.GetSessionOptions
		}
		if err := cl.decodeID(&p, &p.ID); err != nil {
			return nil, err
		}
		return cl.c.Sessions().Get(cl.ctx, p.ID, p.GetSessionOptions)
	},
	"sessions.terminate": func(cl *call) (any, error) {
		var p struct {
			ID string `json:"id"`
		}
		if err := cl.decodeID(&p, &p.ID); err != nil {
			return nil, err
		}
		return cl.c.Sessions().Terminate(cl.ctx, p.ID)
	},
	"sessions.logs": func(cl *call) (any, error) {
		var p struct {
			ID string `json:"id"`
			mimiry.LogsOptions
		}
		if err := cl.decodeID(&p, &p.ID); err != nil {
			return nil, err
		}
		return cl.c.Sessions().Logs(cl.ctx, p.ID, p.LogsOptions)
	},
	"sessions.wait_ready": func(cl *call) (any, error) {
		var p waitParams
		if err := cl.decodeID(&p, &p.ID); err != nil {
			return nil, err
		}
		return cl.c.Sessions().WaitReady(cl.ctx, p.ID, p.options(cl))
	},

	"volumes.create": func(cl *call) (any, error) {
		var req mimiry.CreateVolumeRequest
		if err := cl.decode(&req); err != nil {
			return nil, err
		}
		return cl.c.Volumes().Create(cl.ctx, req)
	},
	"volumes.list": func(cl *call) (any, error) {
		var p struct {
			mimiry.VolumeListOptions
			All bool `json:"all,omitzero"`
		}
		if err := cl.decode(&p); err != nil {
			return nil, err
		}
		if p.All {
			return collect(cl.c.Volumes().All(cl.ctx, p.VolumeListOptions))
		}
		return page(cl.c.Volumes().List(cl.ctx, p.VolumeListOptions))
	},
	"volumes.get": func(cl *call) (any, error) {
		var p struct {
			ID string `json:"id"`
		}
		if err := cl.decodeID(&p, &p.ID); err != nil {
			return nil, err
		}
		return cl.c.Volumes().Get(cl.ctx, p.ID)
	},
	"volumes.delete": func(cl *call) (any, error) {
		var p struct {
			ID string `json:"id"`
		}
		if err := cl.decodeID(&p, &p.ID); err != nil {
			return nil, err
		}
		return cl.c.Volumes().Delete(cl.ctx, p.ID)
	},
	"volumes.extend": func(cl *call) (any, error) {
		var p struct {
			ID        string `json:"id"`
			NewSizeGB int    `json:"new_size_gb"`
		}
		if err := cl.decodeID(&p, &p.ID); err != nil {
			return nil, err
		}
		if p.NewSizeGB <= 0 {
			return nil, &usageError{"params: new_size_gb is required"}
		}
		return cl.c.Volumes().Extend(cl.ctx, p.ID, p.NewSizeGB)
	},
	"volumes.wait_provisioned": func(cl *call) (any, error) {
		var p waitParams
		if err := cl.decodeID(&p, &p.ID); err != nil {
			return nil, err
		}
		return cl.c.Volumes().WaitProvisioned(cl.ctx, p.ID, p.options(cl))
	},

	"gpus.catalog": func(cl *call) (any, error) {
		if err := cl.decode(&struct{}{}); err != nil {
			return nil, err
		}
		return cl.c.GPUs().Catalog(cl.ctx)
	},
	"gpus.availability": func(cl *call) (any, error) {
		var o mimiry.AvailabilityOptions
		if err := cl.decode(&o); err != nil {
			return nil, err
		}
		return cl.c.GPUs().Availability(cl.ctx, o)
	},

	"account.balance": func(cl *call) (any, error) {
		if err := cl.decode(&struct{}{}); err != nil {
			return nil, err
		}
		return cl.c.Account().Balance(cl.ctx)
	},
	"account.org_balance": func(cl *call) (any, error) {
		var p struct {
			IDOrSlug string `json:"id_or_slug"`
		}
		if err := cl.decodeID(&p, &p.IDOrSlug); err != nil {
			return nil, err
		}
		return cl.c.Account().OrgBalance(cl.ctx, p.IDOrSlug)
	},
	"orgs.list": func(cl *call) (any, error) {
		var p struct {
			mimiry.OrgListOptions
			All bool `json:"all,omitzero"`
		}
		if err := cl.decode(&p); err != nil {
			return nil, err
		}
		if p.All {
			return collect(cl.c.Orgs().All(cl.ctx, p.OrgListOptions))
		}
		return page(cl.c.Orgs().List(cl.ctx, p.OrgListOptions))
	},
	"orgs.get": func(cl *call) (any, error) {
		var p struct {
			IDOrSlug string `json:"id_or_slug"`
		}
		if err := cl.decodeID(&p, &p.IDOrSlug); err != nil {
			return nil, err
		}
		return cl.c.Orgs().Get(cl.ctx, p.IDOrSlug)
	},
	"orgs.create": func(cl *call) (any, error) {
		var req mimiry.CreateOrgRequest
		if err := cl.decode(&req); err != nil {
			return nil, err
		}
		return cl.c.Orgs().Create(cl.ctx, req)
	},
	"orgs.update": func(cl *call) (any, error) {
		var p struct {
			OrgID string `json:"org_id"`
			mimiry.UpdateOrgRequest
		}
		if err := cl.decodeID(&p, &p.OrgID); err != nil {
			return nil, err
		}
		return cl.c.Orgs().Update(cl.ctx, p.OrgID, p.UpdateOrgRequest)
	},
	"orgs.delete":           orgOp(func(cl *call, org string) (any, error) { return cl.c.Orgs().Delete(cl.ctx, org) }),
	"orgs.cancel_wind_down": orgOp(func(cl *call, org string) (any, error) { return cl.c.Orgs().CancelWindDown(cl.ctx, org) }),
	"orgs.members":          orgOp(func(cl *call, org string) (any, error) { return cl.c.Orgs().Members(cl.ctx, org) }),
	"orgs.leave":            orgOp(func(cl *call, org string) (any, error) { return cl.c.Orgs().Leave(cl.ctx, org) }),
	"orgs.start_wind_down": func(cl *call) (any, error) {
		var p struct {
			OrgID string `json:"org_id"`
			mimiry.WindDownRequest
		}
		if err := cl.decodeID(&p, &p.OrgID); err != nil {
			return nil, err
		}
		return cl.c.Orgs().StartWindDown(cl.ctx, p.OrgID, p.WindDownRequest)
	},
	"orgs.transfer_ownership": func(cl *call) (any, error) {
		var p struct {
			OrgID      string `json:"org_id"`
			NewOwnerID string `json:"new_owner_id"`
		}
		if err := cl.decodeID(&p, &p.OrgID); err != nil {
			return nil, err
		}
		if p.NewOwnerID == "" {
			return nil, &usageError{"params: new_owner_id is required"}
		}
		return cl.c.Orgs().TransferOwnership(cl.ctx, p.OrgID, p.NewOwnerID)
	},
	"orgs.invite": func(cl *call) (any, error) {
		var p struct {
			OrgID string `json:"org_id"`
			mimiry.InviteRequest
		}
		if err := cl.decodeID(&p, &p.OrgID); err != nil {
			return nil, err
		}
		return cl.c.Orgs().Invite(cl.ctx, p.OrgID, p.InviteRequest)
	},
	"orgs.set_member_role": func(cl *call) (any, error) {
		var p struct {
			OrgID  string         `json:"org_id"`
			UserID string         `json:"user_id"`
			Role   mimiry.OrgRole `json:"role"`
		}
		if err := cl.decodeID(&p, &p.OrgID); err != nil {
			return nil, err
		}
		if p.UserID == "" || p.Role == "" {
			return nil, &usageError{"params: user_id and role are required"}
		}
		return cl.c.Orgs().SetMemberRole(cl.ctx, p.OrgID, p.UserID, p.Role)
	},
	"orgs.remove_member": func(cl *call) (any, error) {
		var p struct {
			OrgID  string `json:"org_id"`
			UserID string `json:"user_id"`
		}
		if err := cl.decodeID(&p, &p.OrgID); err != nil {
			return nil, err
		}
		if p.UserID == "" {
			return nil, &usageError{"params: user_id is required"}
		}
		return cl.c.Orgs().RemoveMember(cl.ctx, p.OrgID, p.UserID)
	},
	"orgs.invitations": func(cl *call) (any, error) {
		var p struct {
			OrgID string `json:"org_id"`
			mimiry.ListOptions
			All bool `json:"all,omitzero"`
		}
		if err := cl.decodeID(&p, &p.OrgID); err != nil {
			return nil, err
		}
		if p.All {
			return collect(cl.c.Orgs().AllInvitations(cl.ctx, p.OrgID))
		}
		return page(cl.c.Orgs().Invitations(cl.ctx, p.OrgID, p.ListOptions))
	},
	"orgs.revoke_invitation": func(cl *call) (any, error) {
		var p struct {
			OrgID        string `json:"org_id"`
			InvitationID string `json:"invitation_id"`
		}
		if err := cl.decodeID(&p, &p.OrgID); err != nil {
			return nil, err
		}
		if p.InvitationID == "" {
			return nil, &usageError{"params: invitation_id is required"}
		}
		return cl.c.Orgs().RevokeInvitation(cl.ctx, p.OrgID, p.InvitationID)
	},
	"orgs.invitation": tokenOp(func(cl *call, token string) (any, error) { return cl.c.Orgs().Invitation(cl.ctx, token) }),
	"orgs.accept_invitation": tokenOp(func(cl *call, token string) (any, error) {
		return cl.c.Orgs().AcceptInvitation(cl.ctx, token)
	}),
	"orgs.operation": func(cl *call) (any, error) {
		var p struct {
			ID string `json:"id"`
		}
		if err := cl.decodeID(&p, &p.ID); err != nil {
			return nil, err
		}
		return cl.c.Orgs().Operation(cl.ctx, p.ID)
	},
	"orgs.wait_operation": func(cl *call) (any, error) {
		var p waitParams
		if err := cl.decodeID(&p, &p.ID); err != nil {
			return nil, err
		}
		return cl.c.Orgs().WaitOperation(cl.ctx, p.ID, p.options(cl))
	},

	"account.transactions": func(cl *call) (any, error) {
		var p struct {
			mimiry.TransactionListOptions
			All bool `json:"all,omitzero"`
		}
		if err := cl.decode(&p); err != nil {
			return nil, err
		}
		if p.All {
			return collect(cl.c.Account().AllTransactions(cl.ctx, p.TransactionListOptions))
		}
		return page(cl.c.Account().Transactions(cl.ctx, p.TransactionListOptions))
	},
	"account.quota": func(cl *call) (any, error) {
		if err := cl.decode(&struct{}{}); err != nil {
			return nil, err
		}
		return cl.c.Account().Quota(cl.ctx)
	},
}

// elevatedOps are the ops that need elevated rights, which describe lists. A
// test holds this set to the published documents: an op is here exactly when
// it reaches an operation they mark.
var elevatedOps = map[string]bool{
	"account.org_balance":     true,
	"orgs.update":             true,
	"orgs.delete":             true,
	"orgs.start_wind_down":    true,
	"orgs.cancel_wind_down":   true,
	"orgs.transfer_ownership": true,
	"orgs.invite":             true,
	"orgs.set_member_role":    true,
	"orgs.remove_member":      true,
	"orgs.revoke_invitation":  true,
}

// orgOp is an op whose only parameter is the organisation's ID.
func orgOp(f func(cl *call, orgID string) (any, error)) func(*call) (any, error) {
	return func(cl *call) (any, error) {
		var p struct {
			OrgID string `json:"org_id"`
		}
		if err := cl.decodeID(&p, &p.OrgID); err != nil {
			return nil, err
		}
		return f(cl, p.OrgID)
	}
}

// tokenOp is an op whose only parameter is an invitation token.
func tokenOp(f func(cl *call, token string) (any, error)) func(*call) (any, error) {
	return func(cl *call) (any, error) {
		var p struct {
			Token string `json:"token"`
		}
		if err := cl.decodeID(&p, &p.Token); err != nil {
			return nil, err
		}
		return f(cl, p.Token)
	}
}

// tokenResult is what auth.token answers. The token itself is included: the
// Python package makes no HTTP calls of its own, but a caller may.
type tokenResult struct {
	AccessToken string    `json:"access_token"`
	ExpiresAt   time.Time `json:"expires_at"`
	UserID      string    `json:"user_id"`
	Email       string    `json:"email,omitempty"`
	BaseURL     string    `json:"base_url"`
	Fingerprint string    `json:"fingerprint"`
}

func authToken(fresh bool) func(*call) (any, error) {
	return func(cl *call) (any, error) {
		if err := cl.decode(&struct{}{}); err != nil {
			return nil, err
		}
		var t *mimiry.Token
		var err error
		if fresh {
			t, err = cl.c.SignIn(cl.ctx)
		} else {
			t, err = cl.c.Token(cl.ctx)
		}
		if err != nil {
			return nil, err
		}
		return tokenResult(*t), nil
	}
}

type waitParams struct {
	ID                   string  `json:"id"`
	IntervalSeconds      float64 `json:"interval_seconds,omitzero"`
	NotFoundGraceSeconds float64 `json:"not_found_grace_seconds,omitzero"`
}

func (p waitParams) options(cl *call) mimiry.WaitOptions {
	return mimiry.WaitOptions{
		Interval:      seconds(p.IntervalSeconds),
		NotFoundGrace: seconds(p.NotFoundGraceSeconds),
		OnPoll:        cl.progress,
	}
}

func seconds(s float64) time.Duration { return time.Duration(s * float64(time.Second)) }

// decodeID decodes params and requires the identifying field.
func (cl *call) decodeID(v any, id *string) error {
	if err := cl.decode(v); err != nil {
		return err
	}
	if *id == "" {
		return &usageError{"params: the id is required"}
	}
	return nil
}

// pageResult is a list page on the wire, the same shape as the API's own.
type pageResult[T any] struct {
	Items       []T `json:"items"`
	TotalCount  int `json:"total_count"`
	Limit       int `json:"limit"`
	Offset      int `json:"offset"`
	Unavailable int `json:"unavailable"`
}

func page[T any](p *mimiry.Page[T], err error) (any, error) {
	if err != nil {
		return nil, err
	}
	return pageResult[T](*p), nil
}

// collect gathers every item of an iteration; all=true answers with every item
// in one list, and the same page shape so the caller reads one form.
func collect[T any](seq func(func(T, error) bool)) (any, error) {
	items := []T{}
	for it, err := range seq {
		if err != nil {
			return nil, err
		}
		items = append(items, it)
	}
	return pageResult[T]{Items: items, TotalCount: len(items), Limit: len(items)}, nil
}
