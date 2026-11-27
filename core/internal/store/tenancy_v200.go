package store

// v2.0 tenancy storage: workspace invites, session lookup by id and the
// transactional sign-up (first user = platform admin decided atomically).

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"boxmes/core/internal/model"
)

var (
	// ErrSignupClosed: not the first user and sign-up is closed.
	ErrSignupClosed = errors.New("sign-up is closed")
	// ErrInviteRequired: not the first user, invite mode, no code given.
	ErrInviteRequired = errors.New("invite code required")
	// ErrEmailTaken: an account with this email exists.
	ErrEmailTaken = errors.New("email already registered")
	// ErrWorkspaceInviteUnavailable: unknown, expired, accepted or for another email.
	ErrWorkspaceInviteUnavailable = errors.New("workspace invite unavailable")
)

// firstAdminClaim is the kv row that makes "first user becomes platform
// admin" happen exactly once, even for concurrent sign-ups.
const firstAdminClaim = "platform:first_admin"

// ---- sessions ----

// GetSession returns a live session by id (access tickets remember the
// session id of the caller that issued them).
func (s *SQL) GetSession(ctx context.Context, id string, now time.Time) (*model.Session, error) {
	d, err := getDoc[sessionDoc](ctx, s, "sessions", "id=? AND expires_at>?", id, FmtTime(now))
	if err != nil {
		return nil, err
	}
	se := d.Session
	se.TokenHash = d.TokenHash
	return &se, nil
}

// ---- workspace invites ----

type wsInviteDoc struct {
	model.WorkspaceInvite
	TokenHash string `json:"token_hash"`
}

func unWSInvite(d *wsInviteDoc, err error) (*model.WorkspaceInvite, error) {
	if err != nil {
		return nil, err
	}
	inv := d.WorkspaceInvite
	inv.TokenHash = d.TokenHash
	return &inv, nil
}

func (s *SQL) putWSInviteQ(ctx context.Context, qr querier, inv *model.WorkspaceInvite) error {
	inv.Email = normEmail(inv.Email)
	return s.putDocQ(ctx, qr, "workspace_invites", inv.ID, inv.CreatedAt, map[string]any{
		"workspace_id": inv.WorkspaceID, "email": inv.Email, "role": inv.Role, "token_hash": inv.TokenHash,
		"expires_at": FmtTime(inv.ExpiresAt), "accepted_at": fmtOptTime(inv.AcceptedAt),
	}, wsInviteDoc{*inv, inv.TokenHash})
}

func (s *SQL) PutWorkspaceInvite(ctx context.Context, inv *model.WorkspaceInvite) error {
	return s.putWSInviteQ(ctx, s.db, inv)
}
func (s *SQL) GetWorkspaceInvite(ctx context.Context, id string) (*model.WorkspaceInvite, error) {
	return unWSInvite(getDoc[wsInviteDoc](ctx, s, "workspace_invites", "id=?", id))
}
func (s *SQL) GetWorkspaceInviteByHash(ctx context.Context, hash string) (*model.WorkspaceInvite, error) {
	return unWSInvite(getDoc[wsInviteDoc](ctx, s, "workspace_invites", "token_hash=? AND token_hash<>''", hash))
}
func (s *SQL) ListWorkspaceInvites(ctx context.Context, workspaceID string) ([]*model.WorkspaceInvite, error) {
	ds, err := listDocs[wsInviteDoc](ctx, s, "workspace_invites", "workspace_id=?", "created_at DESC", workspaceID)
	if err != nil {
		return nil, err
	}
	out := make([]*model.WorkspaceInvite, 0, len(ds))
	for _, d := range ds {
		inv := d.WorkspaceInvite // hash stays out of list results
		out = append(out, &inv)
	}
	return out, nil
}
func (s *SQL) DeleteWorkspaceInvite(ctx context.Context, id string) error {
	return s.exec(ctx, "DELETE FROM workspace_invites WHERE id=?", id)
}

// claimWSInviteQ marks an open invite accepted (exactly once) inside qr.
func (s *SQL) claimWSInviteQ(ctx context.Context, qr querier, inv *model.WorkspaceInvite, userID string, now time.Time) error {
	r, err := qr.ExecContext(ctx, s.rebind("UPDATE workspace_invites SET accepted_at=? WHERE id=? AND accepted_at='' AND expires_at>?"), FmtTime(now), inv.ID, FmtTime(now))
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return ErrWorkspaceInviteUnavailable
	}
	t := now.UTC()
	inv.AcceptedAt, inv.AcceptedBy = &t, userID
	return s.putWSInviteQ(ctx, qr, inv)
}

// AcceptWorkspaceInvite consumes the invite and adds (or re-roles) the
// membership in one transaction.
func (s *SQL) AcceptWorkspaceInvite(ctx context.Context, id, userID string, now time.Time) (*model.Membership, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	inv, err := unWSInvite(getDocQ[wsInviteDoc](ctx, s, tx, "workspace_invites", "id=?", id))
	if err != nil {
		return nil, ErrWorkspaceInviteUnavailable
	}
	if err := s.claimWSInviteQ(ctx, tx, inv, userID, now); err != nil {
		return nil, err
	}
	mb := &model.Membership{ID: "mem_" + inv.WorkspaceID + "_" + userID, WorkspaceID: inv.WorkspaceID, UserID: userID, Role: inv.Role, CreatedAt: now.UTC()}
	if old, err := getDocQ[model.Membership](ctx, s, tx, "memberships", "workspace_id=? AND user_id=?", inv.WorkspaceID, userID); err == nil {
		mb.ID, mb.CreatedAt = old.ID, old.CreatedAt
		if old.Role == model.RoleOwner {
			mb.Role = model.RoleOwner // an invite never demotes an owner
		}
	}
	if err := s.putDocQ(ctx, tx, "memberships", mb.ID, mb.CreatedAt, map[string]any{"workspace_id": mb.WorkspaceID, "user_id": mb.UserID, "role": mb.Role}, mb); err != nil {
		return nil, err
	}
	return mb, tx.Commit()
}

// ---- sign-up ----

// SignupTx is everything a sign-up writes; Signup applies it atomically.
type SignupTx struct {
	User                *model.User      // PlatformAdmin is set by Signup
	RequireInvite       bool             // invite mode: a code (or a workspace invite) is needed unless first
	Closed              bool             // closed mode: only the first user (or a workspace invite)
	InviteCode          string           // optional platform invite code
	WorkspaceInviteHash string           // optional workspace invite token hash (joins that workspace)
	NewWorkspace        *model.Workspace // personal workspace for a regular sign-up
	Now                 time.Time
}

// SignupResult reports what Signup decided.
type SignupResult struct {
	First           bool
	Workspace       *model.Workspace
	Role            string
	InviteCodeUsed  bool
	WorkspaceInvite *model.WorkspaceInvite
}

// Signup creates the user, its membership and (when needed) its workspace in
// one transaction. The first account ever becomes platform admin and owner of
// the default workspace: the claim row in kv is inserted once, so two
// concurrent first sign-ups cannot both win (SQLite serialises the
// transaction on its single connection; on Postgres the second INSERT waits
// for the first commit and then inserts nothing). Any failure rolls back
// everything (no orphan user).
func (s *SQL) Signup(ctx context.Context, in SignupTx) (*SignupResult, error) {
	if in.User == nil {
		return nil, fmt.Errorf("signup: no user")
	}
	now := in.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	u := in.User
	u.Email = normEmail(u.Email)

	var n int
	if err := tx.QueryRowContext(ctx, s.rebind("SELECT COUNT(*) FROM users WHERE email=?"), u.Email).Scan(&n); err != nil {
		return nil, err
	}
	if n > 0 {
		return nil, ErrEmailTaken
	}
	res := &SignupResult{}
	claim, err := tx.ExecContext(ctx, s.rebind("INSERT INTO kv(id, created_at, doc) VALUES(?,?,?) ON CONFLICT(id) DO NOTHING"), firstAdminClaim, FmtTime(now), `{"claimed":true}`)
	if err != nil {
		return nil, err
	}
	claimed, _ := claim.RowsAffected()
	if claimed == 1 {
		var users int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM users").Scan(&users); err != nil {
			return nil, err
		}
		res.First = users == 0
	}
	u.PlatformAdmin = res.First

	var wsInv *model.WorkspaceInvite
	if in.WorkspaceInviteHash != "" && !res.First {
		wsInv, err = unWSInvite(getDocQ[wsInviteDoc](ctx, s, tx, "workspace_invites", "token_hash=? AND token_hash<>''", in.WorkspaceInviteHash))
		if err != nil || wsInv.AcceptedAt != nil || !now.Before(wsInv.ExpiresAt) || normEmail(wsInv.Email) != u.Email {
			return nil, ErrWorkspaceInviteUnavailable
		}
	}
	code := strings.TrimSpace(in.InviteCode)
	if !res.First && wsInv == nil {
		if in.Closed {
			return nil, ErrSignupClosed
		}
		if in.RequireInvite && code == "" {
			return nil, ErrInviteRequired
		}
	}
	if code != "" && !res.First && wsInv == nil {
		var id string
		err := tx.QueryRowContext(ctx, s.rebind("SELECT id FROM invite_codes WHERE code=?"), code).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrInviteUnavailable
		}
		if err != nil {
			return nil, err
		}
		r, err := tx.ExecContext(ctx, s.rebind("UPDATE invite_codes SET uses=uses+1 WHERE id=? AND uses<max_uses AND (expires_at='' OR expires_at>?)"), id, FmtTime(now))
		if err != nil {
			return nil, err
		}
		if n, _ := r.RowsAffected(); n == 0 {
			return nil, ErrInviteUnavailable
		}
		if _, err := tx.ExecContext(ctx, s.rebind("INSERT INTO invite_redemptions(id, invite_code_id, user_id, created_at) VALUES(?,?,?,?)"),
			fmt.Sprintf("inr_%s_%s", id, u.ID), id, u.ID, FmtTime(now)); err != nil {
			return nil, ErrInviteUnavailable
		}
		res.InviteCodeUsed = true
	}

	if _, err := tx.ExecContext(ctx, s.rebind("INSERT INTO users(id, email, password_hash, email_verified, platform_admin, created_at, doc) VALUES(?,?,?,?,?,?,?)"),
		u.ID, u.Email, u.PasswordHash, b2i(u.EmailVerified), b2i(u.PlatformAdmin), FmtTime(u.CreatedAt), mustJSON(userDoc{*u, u.PasswordHash})); err != nil {
		if isUnique(err) {
			return nil, ErrEmailTaken
		}
		return nil, err
	}

	var ws *model.Workspace
	role := model.RoleOwner
	switch {
	case res.First:
		ws, err = getDocQ[model.Workspace](ctx, s, tx, "workspaces", "id=?", model.DefaultWorkspaceID)
		if err != nil {
			return nil, fmt.Errorf("default workspace: %w", err)
		}
		ws.OwnerUserID = u.ID
		if ws.Plan == "" {
			ws.Plan = model.PlanUnlimited
		}
	case wsInv != nil:
		if err := s.claimWSInviteQ(ctx, tx, wsInv, u.ID, now); err != nil {
			return nil, err
		}
		ws, err = getDocQ[model.Workspace](ctx, s, tx, "workspaces", "id=?", wsInv.WorkspaceID)
		if err != nil {
			return nil, ErrWorkspaceInviteUnavailable
		}
		role = wsInv.Role
		res.WorkspaceInvite = wsInv
	default:
		if in.NewWorkspace == nil {
			return nil, fmt.Errorf("signup: no workspace")
		}
		ws = in.NewWorkspace
		ws.OwnerUserID = u.ID
	}
	if wsInv == nil {
		if err := s.putWorkspaceQ(ctx, tx, ws); err != nil {
			return nil, err
		}
	}
	mb := &model.Membership{ID: "mem_" + ws.ID + "_" + u.ID, WorkspaceID: ws.ID, UserID: u.ID, Role: role, CreatedAt: now.UTC()}
	if err := s.putDocQ(ctx, tx, "memberships", mb.ID, mb.CreatedAt, map[string]any{"workspace_id": mb.WorkspaceID, "user_id": mb.UserID, "role": mb.Role}, mb); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, s.rebind("DELETE FROM waitlist WHERE email=?"), u.Email); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	res.Workspace, res.Role = ws, role
	return res, nil
}

func (s *SQL) putWorkspaceQ(ctx context.Context, qr querier, w *model.Workspace) error {
	if w.MaxBoxes <= 0 {
		w.MaxBoxes = model.DefaultMaxBoxes
	}
	if w.MaxTTLHours <= 0 {
		w.MaxTTLHours = model.DefaultMaxTTLHours
	}
	return s.putDocQ(ctx, qr, "workspaces", w.ID, w.CreatedAt, map[string]any{
		"name": w.Name, "owner_user_id": w.OwnerUserID, "max_boxes": w.MaxBoxes, "max_ttl_hours": w.MaxTTLHours,
	}, w)
}

func isUnique(err error) bool {
	m := strings.ToLower(err.Error())
	return strings.Contains(m, "unique") || strings.Contains(m, "duplicate key")
}
