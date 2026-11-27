package model

import "time"

// Tenancy entities (v2 multi-tenant). Hash fields are never serialised to
// API clients; the store keeps them in indexed columns.

const (
	// Workspace roles (v2.0, docs/boxmes-ui-spec-v2.md §19). RoleMember is
	// the pre-v2.0 name of a non-owner member; it is read as developer.
	RoleOwner     = "owner"
	RoleAdmin     = "admin"
	RoleDeveloper = "developer"
	RoleBilling   = "billing"
	RoleViewer    = "viewer"
	RoleAuditor   = "auditor"
	RoleMember    = "member"

	TokenPurposeVerify = "verify"
	TokenPurposeReset  = "reset"

	DefaultWorkspaceID   = "ws_default"
	DefaultWorkspaceName = "default"
	DefaultMaxBoxes      = 5
	DefaultMaxTTLHours   = 12
)

type User struct {
	ID            string    `json:"id"`
	Email         string    `json:"email"`
	Name          string    `json:"name,omitempty"`
	PasswordHash  string    `json:"-"`
	EmailVerified bool      `json:"email_verified"`
	PlatformAdmin bool      `json:"platform_admin"`
	CreatedAt     time.Time `json:"created_at"`
}

type Workspace struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	OwnerUserID string    `json:"owner_user_id"`
	MaxBoxes    int       `json:"max_boxes"`
	MaxTTLHours int       `json:"max_ttl_hours"`
	CreatedAt   time.Time `json:"created_at"`
	// v2.0: plan (free|pro|team|unlimited) + per-workspace quota overrides.
	// Empty plan = the instance default (BOXMES_DEFAULT_PLAN, free).
	Plan      string          `json:"plan,omitempty"`
	Overrides *QuotaOverrides `json:"quota_overrides,omitempty"`
	Status    string          `json:"status,omitempty"` // active (default) | suspended
	CreatedBy string          `json:"created_by,omitempty"`
}

// ValidRole reports a workspace role name (owner, admin, developer, billing, viewer, auditor).
func ValidRole(r string) bool {
	switch r {
	case RoleOwner, RoleAdmin, RoleDeveloper, RoleBilling, RoleViewer, RoleAuditor:
		return true
	}
	return false
}

// NormRole maps the pre-v2.0 "member" role to developer.
func NormRole(r string) string {
	if r == RoleMember {
		return RoleDeveloper
	}
	return r
}

// WorkspaceInvite invites an email into a workspace with a role (token link,
// 7 days). TokenHash never leaves the store.
type WorkspaceInvite struct {
	ID          string     `json:"id"`
	WorkspaceID string     `json:"workspace_id"`
	Email       string     `json:"email"`
	Role        string     `json:"role"`
	InvitedBy   string     `json:"invited_by"`
	TokenHash   string     `json:"-"`
	ExpiresAt   time.Time  `json:"expires_at"`
	AcceptedAt  *time.Time `json:"accepted_at"`
	AcceptedBy  string     `json:"accepted_by,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
}

type Membership struct {
	ID          string    `json:"id"`
	WorkspaceID string    `json:"workspace_id"`
	UserID      string    `json:"user_id"`
	Role        string    `json:"role"` // owner|member
	CreatedAt   time.Time `json:"created_at"`
}

type Session struct {
	ID          string    `json:"id"`
	TokenHash   string    `json:"-"`
	UserID      string    `json:"user_id"`
	WorkspaceID string    `json:"workspace_id"`
	ExpiresAt   time.Time `json:"expires_at"`
	CreatedAt   time.Time `json:"created_at"`
	UserAgent   string    `json:"user_agent,omitempty"`
	IP          string    `json:"ip,omitempty"`
}

type InviteCode struct {
	ID        string     `json:"id"`
	Code      string     `json:"code"`
	MaxUses   int        `json:"max_uses"`
	Uses      int        `json:"uses"`
	ExpiresAt *time.Time `json:"expires_at"`
	Note      string     `json:"note"`
	CreatedBy string     `json:"created_by"`
	CreatedAt time.Time  `json:"created_at"`
}

type InviteRedemption struct {
	ID           string    `json:"id"`
	InviteCodeID string    `json:"invite_code_id"`
	UserID       string    `json:"user_id"`
	CreatedAt    time.Time `json:"created_at"`
}

type WaitlistEntry struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	Note      string    `json:"note"`
	CreatedAt time.Time `json:"created_at"`
}

type PasswordToken struct {
	ID        string     `json:"id"`
	UserID    string     `json:"user_id"`
	Purpose   string     `json:"purpose"` // verify|reset
	TokenHash string     `json:"-"`
	ExpiresAt time.Time  `json:"expires_at"`
	UsedAt    *time.Time `json:"used_at"`
	CreatedAt time.Time  `json:"created_at"`
}
