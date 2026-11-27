package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"boxmes/core/internal/model"
)

// ErrInviteUnavailable: code unknown, expired, exhausted or already redeemed by this user.
var ErrInviteUnavailable = errors.New("invite code unavailable")

// ErrTokenUsed: password token already consumed.
var ErrTokenUsed = errors.New("token already used")

// Tenancy is the multi-tenant part of the store (users, workspaces, auth state).
type Tenancy interface {
	PutUser(ctx context.Context, u *model.User) error
	GetUser(ctx context.Context, id string) (*model.User, error)
	GetUserByEmail(ctx context.Context, email string) (*model.User, error)
	ListUsers(ctx context.Context) ([]*model.User, error)
	DeleteUser(ctx context.Context, id string) error

	PutWorkspace(ctx context.Context, w *model.Workspace) error
	GetWorkspace(ctx context.Context, id string) (*model.Workspace, error)
	ListWorkspaces(ctx context.Context) ([]*model.Workspace, error)
	ListUserWorkspaces(ctx context.Context, userID string) ([]*model.Workspace, error)
	DeleteWorkspace(ctx context.Context, id string) error

	PutMembership(ctx context.Context, m *model.Membership) error
	GetMembership(ctx context.Context, workspaceID, userID string) (*model.Membership, error)
	ListMemberships(ctx context.Context, workspaceID string) ([]*model.Membership, error)
	ListUserMemberships(ctx context.Context, userID string) ([]*model.Membership, error)
	DeleteMembership(ctx context.Context, workspaceID, userID string) error

	PutSession(ctx context.Context, s *model.Session) error
	GetSessionByHash(ctx context.Context, hash string, now time.Time) (*model.Session, error)
	DeleteSession(ctx context.Context, id string) error
	DeleteUserSessions(ctx context.Context, userID string) error
	DeleteExpiredSessions(ctx context.Context, now time.Time) (int64, error)

	PutInviteCode(ctx context.Context, c *model.InviteCode) error
	GetInviteCode(ctx context.Context, code string) (*model.InviteCode, error)
	ListInviteCodes(ctx context.Context) ([]*model.InviteCode, error)
	DeleteInviteCode(ctx context.Context, id string) error
	RedeemInvite(ctx context.Context, code, userID string, now time.Time) (*model.InviteRedemption, error)
	ListInviteRedemptions(ctx context.Context, inviteCodeID string) ([]*model.InviteRedemption, error)

	AddWaitlist(ctx context.Context, e *model.WaitlistEntry) error
	ListWaitlist(ctx context.Context) ([]*model.WaitlistEntry, error)
	DeleteWaitlist(ctx context.Context, email string) error

	PutPasswordToken(ctx context.Context, t *model.PasswordToken) error
	GetPasswordTokenByHash(ctx context.Context, hash string) (*model.PasswordToken, error)
	UsePasswordToken(ctx context.Context, id string, now time.Time) error
	DeletePasswordTokens(ctx context.Context, userID, purpose string) error

	GetAgentInWorkspace(ctx context.Context, workspaceID, name string) (*model.Agent, error)
	ListAgentsInWorkspace(ctx context.Context, workspaceID string) ([]*model.Agent, error)
	ListKeysInWorkspace(ctx context.Context, workspaceID string) ([]*model.APIKey, error)
	ListSecretsInWorkspace(ctx context.Context, workspaceID string) ([]*model.Secret, error)

	BackfillDefaultWorkspace(ctx context.Context) (int, error)
}

var _ Tenancy = (*SQL)(nil)

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func normEmail(e string) string { return strings.ToLower(strings.TrimSpace(e)) }

func fmtOptTime(t *time.Time) string {
	if t == nil || t.IsZero() {
		return ""
	}
	return FmtTime(*t)
}

func parseOptTime(s string) *time.Time {
	if s == "" {
		return nil
	}
	t, err := time.Parse(TimeFormat, s)
	if err != nil {
		return nil
	}
	return &t
}

func parseTime(s string) time.Time { t, _ := time.Parse(TimeFormat, s); return t }

// ---- users ----

type userDoc struct {
	model.User
	PasswordHash string `json:"password_hash"`
}

func (s *SQL) PutUser(ctx context.Context, u *model.User) error {
	u.Email = normEmail(u.Email)
	return s.putDoc(ctx, "users", u.ID, u.CreatedAt, map[string]any{
		"email": u.Email, "password_hash": u.PasswordHash,
		"email_verified": b2i(u.EmailVerified), "platform_admin": b2i(u.PlatformAdmin),
	}, userDoc{*u, u.PasswordHash})
}
func unUser(d *userDoc, err error) (*model.User, error) {
	if err != nil {
		return nil, err
	}
	u := d.User
	u.PasswordHash = d.PasswordHash
	return &u, nil
}
func (s *SQL) GetUser(ctx context.Context, id string) (*model.User, error) {
	return unUser(getDoc[userDoc](ctx, s, "users", "id=?", id))
}
func (s *SQL) GetUserByEmail(ctx context.Context, email string) (*model.User, error) {
	return unUser(getDoc[userDoc](ctx, s, "users", "email=?", normEmail(email)))
}
func (s *SQL) ListUsers(ctx context.Context) ([]*model.User, error) {
	ds, err := listDocs[userDoc](ctx, s, "users", "", "created_at")
	if err != nil {
		return nil, err
	}
	out := make([]*model.User, 0, len(ds))
	for _, d := range ds {
		u := d.User // hash stays out of list results
		out = append(out, &u)
	}
	return out, nil
}
func (s *SQL) DeleteUser(ctx context.Context, id string) error {
	return s.exec(ctx, "DELETE FROM users WHERE id=?", id)
}

// ---- workspaces ----

func (s *SQL) PutWorkspace(ctx context.Context, w *model.Workspace) error {
	if w.MaxBoxes <= 0 {
		w.MaxBoxes = model.DefaultMaxBoxes
	}
	if w.MaxTTLHours <= 0 {
		w.MaxTTLHours = model.DefaultMaxTTLHours
	}
	return s.putDoc(ctx, "workspaces", w.ID, w.CreatedAt, map[string]any{
		"name": w.Name, "owner_user_id": w.OwnerUserID, "max_boxes": w.MaxBoxes, "max_ttl_hours": w.MaxTTLHours,
	}, w)
}
func (s *SQL) GetWorkspace(ctx context.Context, id string) (*model.Workspace, error) {
	return getDoc[model.Workspace](ctx, s, "workspaces", "id=?", id)
}
func (s *SQL) ListWorkspaces(ctx context.Context) ([]*model.Workspace, error) {
	return listDocs[model.Workspace](ctx, s, "workspaces", "", "created_at")
}
func (s *SQL) ListUserWorkspaces(ctx context.Context, userID string) ([]*model.Workspace, error) {
	return listDocs[model.Workspace](ctx, s, "workspaces", "id IN (SELECT workspace_id FROM memberships WHERE user_id=?)", "created_at", userID)
}
func (s *SQL) DeleteWorkspace(ctx context.Context, id string) error {
	if err := s.exec(ctx, "DELETE FROM memberships WHERE workspace_id=?", id); err != nil {
		return err
	}
	return s.exec(ctx, "DELETE FROM workspaces WHERE id=?", id)
}

// ---- memberships ----

func (s *SQL) PutMembership(ctx context.Context, m *model.Membership) error {
	if m.ID == "" {
		m.ID = "mem_" + m.WorkspaceID + "_" + m.UserID
	}
	if m.Role != model.RoleOwner && m.Role != model.RoleMember {
		return fmt.Errorf("bad role %q", m.Role)
	}
	return s.putDoc(ctx, "memberships", m.ID, m.CreatedAt, map[string]any{
		"workspace_id": m.WorkspaceID, "user_id": m.UserID, "role": m.Role,
	}, m)
}
func (s *SQL) GetMembership(ctx context.Context, workspaceID, userID string) (*model.Membership, error) {
	return getDoc[model.Membership](ctx, s, "memberships", "workspace_id=? AND user_id=?", workspaceID, userID)
}
func (s *SQL) ListMemberships(ctx context.Context, workspaceID string) ([]*model.Membership, error) {
	return listDocs[model.Membership](ctx, s, "memberships", "workspace_id=?", "created_at", workspaceID)
}
func (s *SQL) ListUserMemberships(ctx context.Context, userID string) ([]*model.Membership, error) {
	return listDocs[model.Membership](ctx, s, "memberships", "user_id=?", "created_at", userID)
}
func (s *SQL) DeleteMembership(ctx context.Context, workspaceID, userID string) error {
	return s.exec(ctx, "DELETE FROM memberships WHERE workspace_id=? AND user_id=?", workspaceID, userID)
}

// ---- sessions ----

type sessionDoc struct {
	model.Session
	TokenHash string `json:"token_hash"`
}

func (s *SQL) PutSession(ctx context.Context, se *model.Session) error {
	return s.putDoc(ctx, "sessions", se.ID, se.CreatedAt, map[string]any{
		"token_hash": se.TokenHash, "user_id": se.UserID, "workspace_id": se.WorkspaceID, "expires_at": FmtTime(se.ExpiresAt),
	}, sessionDoc{*se, se.TokenHash})
}

// GetSessionByHash returns only sessions that have not expired at now.
func (s *SQL) GetSessionByHash(ctx context.Context, hash string, now time.Time) (*model.Session, error) {
	d, err := getDoc[sessionDoc](ctx, s, "sessions", "token_hash=? AND token_hash<>'' AND expires_at>?", hash, FmtTime(now))
	if err != nil {
		return nil, err
	}
	se := d.Session
	se.TokenHash = d.TokenHash
	return &se, nil
}
func (s *SQL) DeleteSession(ctx context.Context, id string) error {
	return s.exec(ctx, "DELETE FROM sessions WHERE id=?", id)
}
func (s *SQL) DeleteUserSessions(ctx context.Context, userID string) error {
	return s.exec(ctx, "DELETE FROM sessions WHERE user_id=?", userID)
}
func (s *SQL) DeleteExpiredSessions(ctx context.Context, now time.Time) (int64, error) {
	r, err := s.db.ExecContext(ctx, s.rebind("DELETE FROM sessions WHERE expires_at<=?"), FmtTime(now))
	if err != nil {
		return 0, err
	}
	return r.RowsAffected()
}

// ---- invite codes ----

const inviteCols = "id, code, max_uses, uses, expires_at, note, created_by, created_at"

func scanInvite(sc interface{ Scan(...any) error }) (*model.InviteCode, error) {
	var c model.InviteCode
	var exp, created string
	if err := sc.Scan(&c.ID, &c.Code, &c.MaxUses, &c.Uses, &exp, &c.Note, &c.CreatedBy, &created); err != nil {
		return nil, err
	}
	c.ExpiresAt = parseOptTime(exp)
	c.CreatedAt = parseTime(created)
	return &c, nil
}

// PutInviteCode upserts the code's settings; the uses counter is only changed by RedeemInvite.
func (s *SQL) PutInviteCode(ctx context.Context, c *model.InviteCode) error {
	if c.MaxUses <= 0 {
		c.MaxUses = 1
	}
	return s.exec(ctx, `INSERT INTO invite_codes(`+inviteCols+`) VALUES(?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET code=excluded.code, max_uses=excluded.max_uses, expires_at=excluded.expires_at, note=excluded.note, created_by=excluded.created_by`,
		c.ID, c.Code, c.MaxUses, c.Uses, fmtOptTime(c.ExpiresAt), c.Note, c.CreatedBy, FmtTime(c.CreatedAt))
}
func (s *SQL) GetInviteCode(ctx context.Context, code string) (*model.InviteCode, error) {
	c, err := scanInvite(s.db.QueryRowContext(ctx, s.rebind("SELECT "+inviteCols+" FROM invite_codes WHERE code=?"), code))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrNoRows
	}
	return c, err
}
func (s *SQL) ListInviteCodes(ctx context.Context) ([]*model.InviteCode, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+inviteCols+" FROM invite_codes ORDER BY created_at DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*model.InviteCode{}
	for rows.Next() {
		c, err := scanInvite(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
func (s *SQL) DeleteInviteCode(ctx context.Context, id string) error {
	return s.exec(ctx, "DELETE FROM invite_codes WHERE id=?", id)
}

// RedeemInvite atomically consumes one use of code for userID.
func (s *SQL) RedeemInvite(ctx context.Context, code, userID string, now time.Time) (*model.InviteRedemption, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var id string
	err = tx.QueryRowContext(ctx, s.rebind("SELECT id FROM invite_codes WHERE code=?"), code).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrInviteUnavailable
	}
	if err != nil {
		return nil, err
	}
	res, err := tx.ExecContext(ctx, s.rebind("UPDATE invite_codes SET uses=uses+1 WHERE id=? AND uses<max_uses AND (expires_at='' OR expires_at>?)"), id, FmtTime(now))
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrInviteUnavailable
	}
	r := &model.InviteRedemption{ID: fmt.Sprintf("inr_%s_%s", id, userID), InviteCodeID: id, UserID: userID, CreatedAt: now.UTC()}
	if _, err := tx.ExecContext(ctx, s.rebind("INSERT INTO invite_redemptions(id, invite_code_id, user_id, created_at) VALUES(?,?,?,?)"),
		r.ID, r.InviteCodeID, r.UserID, FmtTime(now)); err != nil {
		return nil, ErrInviteUnavailable // already redeemed by this user (unique)
	}
	return r, tx.Commit()
}
func (s *SQL) ListInviteRedemptions(ctx context.Context, inviteCodeID string) ([]*model.InviteRedemption, error) {
	q, args := "SELECT id, invite_code_id, user_id, created_at FROM invite_redemptions", []any{}
	if inviteCodeID != "" {
		q, args = q+" WHERE invite_code_id=?", []any{inviteCodeID}
	}
	rows, err := s.db.QueryContext(ctx, s.rebind(q+" ORDER BY created_at"), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*model.InviteRedemption{}
	for rows.Next() {
		var r model.InviteRedemption
		var ts string
		if err := rows.Scan(&r.ID, &r.InviteCodeID, &r.UserID, &ts); err != nil {
			return nil, err
		}
		r.CreatedAt = parseTime(ts)
		out = append(out, &r)
	}
	return out, rows.Err()
}

// ---- waitlist ----

// AddWaitlist inserts or refreshes the note for an email (one row per email).
func (s *SQL) AddWaitlist(ctx context.Context, e *model.WaitlistEntry) error {
	e.Email = normEmail(e.Email)
	if e.ID == "" {
		e.ID = "wl_" + e.Email
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now().UTC()
	}
	return s.exec(ctx, "INSERT INTO waitlist(id, email, note, created_at) VALUES(?,?,?,?) ON CONFLICT(email) DO UPDATE SET note=excluded.note",
		e.ID, e.Email, e.Note, FmtTime(e.CreatedAt))
}
func (s *SQL) ListWaitlist(ctx context.Context) ([]*model.WaitlistEntry, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id, email, note, created_at FROM waitlist ORDER BY created_at")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*model.WaitlistEntry{}
	for rows.Next() {
		var e model.WaitlistEntry
		var ts string
		if err := rows.Scan(&e.ID, &e.Email, &e.Note, &ts); err != nil {
			return nil, err
		}
		e.CreatedAt = parseTime(ts)
		out = append(out, &e)
	}
	return out, rows.Err()
}
func (s *SQL) DeleteWaitlist(ctx context.Context, email string) error {
	return s.exec(ctx, "DELETE FROM waitlist WHERE email=?", normEmail(email))
}

// ---- password tokens ----

func (s *SQL) PutPasswordToken(ctx context.Context, t *model.PasswordToken) error {
	if t.Purpose != model.TokenPurposeVerify && t.Purpose != model.TokenPurposeReset {
		return fmt.Errorf("bad token purpose %q", t.Purpose)
	}
	return s.exec(ctx, `INSERT INTO password_tokens(id, user_id, purpose, token_hash, expires_at, used_at, created_at) VALUES(?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET user_id=excluded.user_id, purpose=excluded.purpose, token_hash=excluded.token_hash, expires_at=excluded.expires_at, used_at=excluded.used_at`,
		t.ID, t.UserID, t.Purpose, t.TokenHash, FmtTime(t.ExpiresAt), fmtOptTime(t.UsedAt), FmtTime(t.CreatedAt))
}
func (s *SQL) GetPasswordTokenByHash(ctx context.Context, hash string) (*model.PasswordToken, error) {
	var t model.PasswordToken
	var exp, used, created string
	err := s.db.QueryRowContext(ctx, s.rebind("SELECT id, user_id, purpose, token_hash, expires_at, used_at, created_at FROM password_tokens WHERE token_hash=? AND token_hash<>''"), hash).
		Scan(&t.ID, &t.UserID, &t.Purpose, &t.TokenHash, &exp, &used, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrNoRows
	}
	if err != nil {
		return nil, err
	}
	t.ExpiresAt, t.UsedAt, t.CreatedAt = parseTime(exp), parseOptTime(used), parseTime(created)
	return &t, nil
}

// UsePasswordToken marks a token used exactly once; a second call returns ErrTokenUsed.
func (s *SQL) UsePasswordToken(ctx context.Context, id string, now time.Time) error {
	r, err := s.db.ExecContext(ctx, s.rebind("UPDATE password_tokens SET used_at=? WHERE id=? AND used_at=''"), FmtTime(now), id)
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return ErrTokenUsed
	}
	return nil
}
func (s *SQL) DeletePasswordTokens(ctx context.Context, userID, purpose string) error {
	if purpose == "" {
		return s.exec(ctx, "DELETE FROM password_tokens WHERE user_id=?", userID)
	}
	return s.exec(ctx, "DELETE FROM password_tokens WHERE user_id=? AND purpose=?", userID, purpose)
}

// ---- workspace-scoped lookups of existing entities ----

func (s *SQL) GetAgentInWorkspace(ctx context.Context, workspaceID, name string) (*model.Agent, error) {
	return getDoc[model.Agent](ctx, s, "agents", "workspace_id=? AND name=?", workspaceID, name)
}
func (s *SQL) ListAgentsInWorkspace(ctx context.Context, workspaceID string) ([]*model.Agent, error) {
	return listDocs[model.Agent](ctx, s, "agents", "workspace_id=?", "created_at", workspaceID)
}
func (s *SQL) ListKeysInWorkspace(ctx context.Context, workspaceID string) ([]*model.APIKey, error) {
	ds, err := listDocs[keyDoc](ctx, s, "api_keys", "workspace_id=?", "created_at", workspaceID)
	if err != nil {
		return nil, err
	}
	out := make([]*model.APIKey, 0, len(ds))
	for _, d := range ds {
		k := d.APIKey
		out = append(out, &k)
	}
	return out, nil
}
func (s *SQL) ListSecretsInWorkspace(ctx context.Context, workspaceID string) ([]*model.Secret, error) {
	ds, err := listDocs[secretDoc](ctx, s, "secrets", "workspace_id=?", "id", workspaceID)
	if err != nil {
		return nil, err
	}
	out := make([]*model.Secret, 0, len(ds))
	for _, d := range ds {
		sec := d.Secret
		out = append(out, &sec)
	}
	return out, nil
}

// ---- backfill ----

// BackfillDefaultWorkspace ensures the "default" workspace exists and moves
// every agent, box, api key and secret without a workspace into it (column and
// JSON doc). Idempotent; returns the number of rows moved.
func (s *SQL) BackfillDefaultWorkspace(ctx context.Context) (int, error) {
	if _, err := s.GetWorkspace(ctx, model.DefaultWorkspaceID); errors.Is(err, model.ErrNoRows) {
		w := &model.Workspace{ID: model.DefaultWorkspaceID, Name: model.DefaultWorkspaceName,
			MaxBoxes: model.DefaultMaxBoxes, MaxTTLHours: model.DefaultMaxTTLHours, CreatedAt: time.Now().UTC()}
		if err := s.PutWorkspace(ctx, w); err != nil {
			return 0, err
		}
	} else if err != nil {
		return 0, err
	}
	moved := 0
	for _, table := range []string{"agents", "boxes", "api_keys", "secrets"} {
		rows, err := s.db.QueryContext(ctx, "SELECT id, doc FROM "+table+" WHERE workspace_id IS NULL OR workspace_id=''")
		if err != nil {
			return moved, err
		}
		type row struct{ id, doc string }
		var todo []row
		for rows.Next() {
			var r row
			if err := rows.Scan(&r.id, &r.doc); err != nil {
				rows.Close()
				return moved, err
			}
			todo = append(todo, r)
		}
		rows.Close()
		for _, r := range todo {
			m := map[string]any{}
			if err := json.Unmarshal([]byte(r.doc), &m); err != nil {
				return moved, fmt.Errorf("%s %s: %w", table, r.id, err)
			}
			m["workspace_id"] = model.DefaultWorkspaceID
			b, _ := json.Marshal(m)
			if err := s.exec(ctx, "UPDATE "+table+" SET workspace_id=?, doc=? WHERE id=?", model.DefaultWorkspaceID, string(b), r.id); err != nil {
				return moved, err
			}
			moved++
		}
	}
	return moved, nil
}
