package model

import "time"

// Tenancy entities (v2 multi-tenant). Hash fields are never serialised to
// API clients; the store keeps them in indexed columns.

const (
	RoleOwner  = "owner"
	RoleMember = "member"

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
