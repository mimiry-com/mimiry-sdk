package mimiry

import "time"

// OrgRole is a member's role in an organisation.
type OrgRole string

const (
	OrgRoleOwner  OrgRole = "owner"
	OrgRoleAdmin  OrgRole = "admin"
	OrgRoleMember OrgRole = "member"
)

// Org is an organisation.
type Org struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Slug        string `json:"slug"`
	Description string `json:"description,omitempty"`
	OwnerID     string `json:"owner_id"`
	// OwnerName is the owner's display name. Set in lists only.
	OwnerName   string    `json:"owner_name,omitempty"`
	Members     []Member  `json:"members"`
	MemberCount int       `json:"member_count"`
	IsActive    bool      `json:"is_active,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Member is one member of an organisation.
type Member struct {
	UserID    string    `json:"user_id"`
	Email     string    `json:"email"`
	Role      OrgRole   `json:"role"`
	JoinedAt  time.Time `json:"joined_at"`
	InvitedBy string    `json:"invited_by"`
}

// CreateOrgRequest creates an organisation.
type CreateOrgRequest struct {
	// Name is at least 3 characters.
	Name string `json:"name"`
	// Slug is "o-" followed by 1–32 lowercase letters, digits and single
	// hyphens, and unique across the platform.
	Slug string `json:"slug"`
}

// UpdateOrgRequest changes an organisation. Nil fields are left as they are.
type UpdateOrgRequest struct {
	Name *string `json:"name,omitempty"`
	Slug *string `json:"slug,omitempty"`
}

// InviteRequest invites someone by email address or by username: exactly one.
type InviteRequest struct {
	Email    string `json:"email,omitempty"`
	Username string `json:"username,omitempty"`
	// Role is admin or member.
	Role OrgRole `json:"role"`
}

// WindDownRequest is how an organisation is retired.
type WindDownRequest struct {
	// ReadableDays (1–90) is how long members keep read access before the
	// organisation can be deleted.
	ReadableDays int `json:"readable_days"`
	// DrainSessions lets running sessions finish instead of ending them.
	DrainSessions bool `json:"drain_sessions"`
	// VolumeGraceDays (0–30, at most ReadableDays) keeps volumes that long.
	// They are billed until then.
	VolumeGraceDays int    `json:"volume_grace_days"`
	Reason          string `json:"reason,omitempty"`
}

// Invitation is an invitation that can still be accepted. It is addressed to
// an email address or to an account: one of Email and InvitedUserID is set.
type Invitation struct {
	ID            string    `json:"id"`
	Email         string    `json:"email,omitempty"`
	InvitedUserID string    `json:"invited_user_id,omitempty"`
	Role          OrgRole   `json:"role"`
	InvitedBy     string    `json:"invited_by"`
	CreatedAt     time.Time `json:"created_at"`
	ExpiresAt     time.Time `json:"expires_at"`
}

// InvitationPreview says whether the caller may accept an invitation. The
// fields after Message are set for the invitee only.
type InvitationPreview struct {
	CanAccept bool `json:"can_accept"`
	// Reason is why not: USER_MISMATCH, EMAIL_MISMATCH, NO_TARGET,
	// NOT_PENDING or EXPIRED.
	Reason    string     `json:"reason,omitempty"`
	Message   string     `json:"message,omitempty"`
	OrgID     string     `json:"org_id,omitempty"`
	OrgName   string     `json:"org_name,omitempty"`
	Role      OrgRole    `json:"role,omitempty"`
	Status    string     `json:"status,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// OperationAccepted is the answer to an organisation change: accepted, not
// yet done. WaitOperation follows it.
type OperationAccepted struct {
	RequestID string `json:"request_id"`
	Status    string `json:"status"`
	Message   string `json:"message"`
	StatusURL string `json:"status_url"`
	// RetryAfter is the suggested wait before the first poll, in seconds.
	RetryAfter int `json:"retry_after,omitempty"`
}

// OperationStatus is where an organisation change stands.
type OperationStatus string

const (
	OperationPending   OperationStatus = "pending"
	OperationCompleted OperationStatus = "completed"
	OperationFailed    OperationStatus = "failed"
)

// Operation is the status of an organisation change.
type Operation struct {
	RequestID string          `json:"request_id"`
	Status    OperationStatus `json:"status"`
	// Result describes what was done, when completed; its keys depend on the
	// change ("organization" for a create, "org_id" and others elsewhere).
	Result map[string]any `json:"result,omitempty"`
	// Error says why, when failed.
	Error     string    `json:"error,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
