package manager

// v2 tenancy, auth layer: sign-up (first user / invite / open / closed),
// password login with server-side sessions, email verification, password
// reset, waitlist, workspace switching and the platform-admin invite tools.
// Storage lives in store/tenancy.go; HTTP in api/tenancy.go.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/mail"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"boxmes/core/internal/model"
	"boxmes/core/internal/notify"
	"boxmes/core/internal/secret"
	"boxmes/core/internal/store"
)

const (
	SignupInvite = "invite" // default: an invite code is required (the very first user never needs one)
	SignupOpen   = "open"   // anyone with an email can sign up
	SignupClosed = "closed" // only the first user; everyone else joins the waitlist

	SessionPrefix = "bxs_"

	minPasswordLen = 8
	maxPasswordLen = 72 // bcrypt limit

	verifyTokenTTL = 48 * time.Hour
	resetTokenTTL  = time.Hour

	loginFailWindow = 15 * time.Minute
	loginFailMax    = 10
)

// Mailer sends one plain-text email (tests stub it via Config.Mailer).
type Mailer func(ctx context.Context, to, subject, body string) error

// ---- sign-up mode ----

func (m *Manager) signupMode() string {
	switch strings.ToLower(strings.TrimSpace(m.Cfg.SignupMode)) {
	case SignupOpen:
		return SignupOpen
	case SignupClosed:
		return SignupClosed
	}
	return SignupInvite
}

// AuthConfig is the public view of how this instance signs people up.
func (m *Manager) AuthConfig(ctx context.Context) map[string]any {
	users, _ := m.Store.ListUsers(ctx)
	return map[string]any{
		"signup_mode":        m.signupMode(),
		"first_user":         len(users) == 0, // the next sign-up becomes platform admin
		"min_password_len":   minPasswordLen,
		"email_verification": true,
		"email_configured":   m.mailConfigured(ctx),
	}
}

// ---- passwords ----

func hashPassword(pw string) (string, error) {
	if len(pw) < minPasswordLen {
		return "", model.E(model.ErrInvalid, "password must be at least %d characters", minPasswordLen)
	}
	if len(pw) > maxPasswordLen {
		return "", model.E(model.ErrInvalid, "password must be at most %d bytes", maxPasswordLen)
	}
	h, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	return string(h), err
}

func checkPassword(hash, pw string) bool {
	return hash != "" && bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}

func cleanEmail(e string) (string, error) {
	e = strings.ToLower(strings.TrimSpace(e))
	a, err := mail.ParseAddress(e)
	if err != nil || a.Address != e || !strings.Contains(e[strings.LastIndex(e, "@")+1:], ".") {
		return "", model.E(model.ErrInvalid, "a valid email address is required")
	}
	return e, nil
}

// ---- login throttling (per email, failures only) ----

type loginFails struct {
	mu sync.Mutex
	m  map[string][]time.Time
}

var fails = &loginFails{m: map[string][]time.Time{}}

func (l *loginFails) blocked(email string, now time.Time) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	keep := l.m[email][:0]
	for _, t := range l.m[email] {
		if now.Sub(t) < loginFailWindow {
			keep = append(keep, t)
		}
	}
	l.m[email] = keep
	if len(keep) >= loginFailMax {
		return int(loginFailWindow.Seconds()-now.Sub(keep[0]).Seconds()) + 1
	}
	return 0
}
func (l *loginFails) add(email string, now time.Time) {
	l.mu.Lock()
	l.m[email] = append(l.m[email], now)
	l.mu.Unlock()
}
func (l *loginFails) reset(email string) { l.mu.Lock(); delete(l.m, email); l.mu.Unlock() }

// ---- sessions ----

// AuthResult is returned by sign-up and login; Token is shown once.
type AuthResult struct {
	Token     string           `json:"token"`
	ExpiresAt time.Time        `json:"expires_at"`
	User      *model.User      `json:"user"`
	Workspace *model.Workspace `json:"workspace"`
	Role      string           `json:"role"`
}

type ClientInfo struct{ UserAgent, IP string }

func (m *Manager) sessionTTL() time.Duration {
	if m.Cfg.SessionTTL > 0 {
		return m.Cfg.SessionTTL
	}
	return 30 * 24 * time.Hour
}

func (m *Manager) newSession(ctx context.Context, u *model.User, wsID string, ci ClientInfo) (*AuthResult, error) {
	if wsID == "" {
		ms, err := m.Store.ListUserMemberships(ctx, u.ID)
		if err != nil {
			return nil, err
		}
		for _, mb := range ms { // prefer a workspace the user owns
			if mb.Role == model.RoleOwner {
				wsID = mb.WorkspaceID
				break
			}
		}
		if wsID == "" && len(ms) > 0 {
			wsID = ms[0].WorkspaceID
		}
	}
	tok := SessionPrefix + secret.RandHex(32)
	now := time.Now().UTC()
	se := &model.Session{ID: secret.ID("ses"), TokenHash: secret.Hash(tok), UserID: u.ID, WorkspaceID: wsID,
		ExpiresAt: now.Add(m.sessionTTL()), CreatedAt: now, UserAgent: clip(ci.UserAgent, 200), IP: ci.IP}
	if err := m.Store.PutSession(ctx, se); err != nil {
		return nil, err
	}
	m.Store.DeleteExpiredSessions(ctx, now) // opportunistic cleanup
	res := &AuthResult{Token: tok, ExpiresAt: se.ExpiresAt, User: u}
	if wsID != "" {
		res.Workspace, _ = m.Store.GetWorkspace(ctx, wsID)
		if mb, err := m.Store.GetMembership(ctx, wsID, u.ID); err == nil {
			res.Role = mb.Role
		}
	}
	return res, nil
}

// clip cuts s to n bytes (no ellipsis, unlike trunc).
func clip(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// authenticateSession resolves a bxs_ token into a principal. Platform admins
// get the admin scope (the whole v1 operator API); other users are workspace
// principals without key scopes until the workspace-scoped API lands.
func (m *Manager) authenticateSession(ctx context.Context, token string) (*Principal, error) {
	now := time.Now().UTC()
	se, err := m.Store.GetSessionByHash(ctx, secret.Hash(token), now)
	if err != nil {
		return nil, model.EH(model.ErrUnauthorized, "Sign in again.", "session expired or invalid")
	}
	u, err := m.Store.GetUser(ctx, se.UserID)
	if err != nil {
		return nil, model.EH(model.ErrUnauthorized, "Sign in again.", "session expired or invalid")
	}
	p := &Principal{User: u, UserID: u.ID, SessionID: se.ID, WorkspaceID: se.WorkspaceID, Admin: u.PlatformAdmin, session: se}
	if se.WorkspaceID != "" {
		if mb, err := m.Store.GetMembership(ctx, se.WorkspaceID, u.ID); err == nil {
			p.Role = mb.Role
		} else {
			p.WorkspaceID = "" // removed from the workspace since login
		}
	}
	scopes := []string{}
	if u.PlatformAdmin {
		scopes = []string{model.ScopeAdmin}
	}
	p.Key = &model.APIKey{ID: se.ID, WorkspaceID: p.WorkspaceID, Name: "session " + u.Email, Prefix: SessionPrefix, Scopes: scopes, CreatedAt: se.CreatedAt}
	if !p.Admin {
		if err := m.allow(ctx, "user:"+u.ID); err != nil {
			return nil, err
		}
	}
	return p, nil
}

func requireUser(p *Principal) error {
	if p == nil || p.UserID == "" {
		return model.EH(model.ErrUnauthorized, "Sign in with email and password (POST /v1/auth/login); API keys have no user.", "a signed-in user is required")
	}
	return nil
}

// ---- sign-up ----

type SignupInput struct {
	Email      string `json:"email"`
	Password   string `json:"password"`
	Name       string `json:"name"`
	InviteCode string `json:"invite_code"`
	Workspace  string `json:"workspace"` // optional name for the personal workspace
}

func (m *Manager) Signup(ctx context.Context, in SignupInput, ci ClientInfo) (*AuthResult, error) {
	email, err := cleanEmail(in.Email)
	if err != nil {
		return nil, err
	}
	ph, err := hashPassword(in.Password)
	if err != nil {
		return nil, err
	}
	if _, err := m.Store.GetUserByEmail(ctx, email); err == nil {
		return nil, model.EH(model.ErrInvalid, "Sign in instead, or reset the password.", "an account with this email already exists")
	} else if !errors.Is(err, model.ErrNoRows) {
		return nil, err
	}
	users, err := m.Store.ListUsers(ctx)
	if err != nil {
		return nil, err
	}
	first := len(users) == 0
	now := time.Now().UTC()
	u := &model.User{ID: secret.ID("usr"), Email: email, Name: clip(strings.TrimSpace(in.Name), 80), PasswordHash: ph, PlatformAdmin: first, CreatedAt: now}
	code := strings.ToUpper(strings.TrimSpace(in.InviteCode))
	if !first {
		switch m.signupMode() {
		case SignupClosed:
			e := model.EH(model.ErrPolicyDenied, "Join the waitlist (POST /v1/waitlist); the operator sends invites.", "sign-up is closed")
			e.Next = []string{"POST /v1/waitlist {email}"}
			return nil, e
		case SignupInvite:
			if code == "" {
				e := model.EH(model.ErrPolicyDenied, "Enter an invite code, or join the waitlist (POST /v1/waitlist).", "an invite code is required to sign up")
				e.Next = []string{"POST /v1/waitlist {email}"}
				return nil, e
			}
		}
	}
	if code != "" && !first {
		if _, err := m.Store.RedeemInvite(ctx, code, u.ID, now); err != nil {
			if errors.Is(err, store.ErrInviteUnavailable) {
				e := model.EH(model.ErrInvalid, "Check the code, or join the waitlist (POST /v1/waitlist).", "invite code is invalid, expired or used up")
				e.Next = []string{"POST /v1/waitlist {email}"}
				return nil, e
			}
			return nil, err
		}
	}
	if err := m.Store.PutUser(ctx, u); err != nil {
		return nil, err
	}
	var ws *model.Workspace
	if first {
		// The first user owns the default workspace (all pre-v2 agents, boxes, keys).
		ws, err = m.Store.GetWorkspace(ctx, model.DefaultWorkspaceID)
		if err != nil {
			return nil, err
		}
		ws.OwnerUserID = u.ID
	} else {
		name := strings.TrimSpace(in.Workspace)
		if name == "" {
			name = strings.SplitN(email, "@", 2)[0]
		}
		ws = &model.Workspace{ID: secret.ID("ws"), Name: clip(name, 60), OwnerUserID: u.ID,
			MaxBoxes: model.DefaultMaxBoxes, MaxTTLHours: model.DefaultMaxTTLHours, CreatedAt: now}
	}
	if err := m.Store.PutWorkspace(ctx, ws); err != nil {
		return nil, err
	}
	if err := m.Store.PutMembership(ctx, &model.Membership{ID: secret.ID("mem"), WorkspaceID: ws.ID, UserID: u.ID, Role: model.RoleOwner, CreatedAt: now}); err != nil {
		return nil, err
	}
	m.Store.DeleteWaitlist(ctx, email)
	m.sendVerify(ctx, u)
	m.EmitData(ctx, "user:"+u.ID, "", "", "user.signed_up", "user "+email+" signed up", nil, map[string]any{"invite": code != "", "platform_admin": first})
	return m.newSession(ctx, u, ws.ID, ci)
}

// ---- login / logout ----

type LoginInput struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (m *Manager) Login(ctx context.Context, in LoginInput, ci ClientInfo) (*AuthResult, error) {
	email := strings.ToLower(strings.TrimSpace(in.Email))
	now := time.Now()
	if wait := fails.blocked(email, now); wait > 0 {
		return nil, model.E(model.ErrRateLimited, "too many failed sign-ins; try again later").WithRetry(wait)
	}
	u, err := m.Store.GetUserByEmail(ctx, email)
	if err != nil || !checkPassword(u.PasswordHash, in.Password) {
		if err != nil {
			bcrypt.CompareHashAndPassword(dummyHash, []byte(in.Password)) // same timing for unknown emails
		}
		fails.add(email, now)
		return nil, model.EH(model.ErrUnauthorized, "Check the email and password, or reset the password.", "wrong email or password")
	}
	fails.reset(email)
	return m.newSession(ctx, u, "", ci)
}

var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("boxmes-dummy-password"), bcrypt.DefaultCost)

func (m *Manager) Logout(ctx context.Context, p *Principal, all bool) error {
	if err := requireUser(p); err != nil {
		return err
	}
	if all {
		return m.Store.DeleteUserSessions(ctx, p.UserID)
	}
	return m.Store.DeleteSession(ctx, p.SessionID)
}

// ---- me / workspaces ----

type WorkspaceView struct {
	*model.Workspace
	Role string `json:"role"`
}

type MeView struct {
	User       *model.User      `json:"user"`
	Workspace  *WorkspaceView   `json:"workspace"`
	Workspaces []*WorkspaceView `json:"workspaces"`
}

func (m *Manager) userWorkspaces(ctx context.Context, userID string) ([]*WorkspaceView, error) {
	ms, err := m.Store.ListUserMemberships(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := []*WorkspaceView{}
	for _, mb := range ms {
		if w, err := m.Store.GetWorkspace(ctx, mb.WorkspaceID); err == nil {
			out = append(out, &WorkspaceView{Workspace: w, Role: mb.Role})
		}
	}
	return out, nil
}

func (m *Manager) Me(ctx context.Context, p *Principal) (*MeView, error) {
	if err := requireUser(p); err != nil {
		return nil, err
	}
	wss, err := m.userWorkspaces(ctx, p.UserID)
	if err != nil {
		return nil, err
	}
	v := &MeView{User: p.User, Workspaces: wss}
	for _, w := range wss {
		if w.ID == p.WorkspaceID {
			v.Workspace = w
		}
	}
	return v, nil
}

// SwitchWorkspace points the current session at another workspace the user belongs to.
func (m *Manager) SwitchWorkspace(ctx context.Context, p *Principal, wsID string) (*MeView, error) {
	if err := requireUser(p); err != nil {
		return nil, err
	}
	if _, err := m.Store.GetMembership(ctx, wsID, p.UserID); err != nil {
		return nil, notFound("workspace", wsID)
	}
	se, err := m.sessionByID(ctx, p)
	if err != nil {
		return nil, err
	}
	se.WorkspaceID = wsID
	if err := m.Store.PutSession(ctx, se); err != nil {
		return nil, err
	}
	p.WorkspaceID = wsID
	return m.Me(ctx, p)
}

// sessionByID re-reads the caller's session row (the store looks sessions up by hash only).
func (m *Manager) sessionByID(ctx context.Context, p *Principal) (*model.Session, error) {
	if p.session != nil {
		return p.session, nil
	}
	return nil, model.EH(model.ErrUnauthorized, "Sign in again.", "session expired or invalid")
}

type ProfileInput struct {
	Name *string `json:"name"`
}

func (m *Manager) UpdateProfile(ctx context.Context, p *Principal, in ProfileInput) (*model.User, error) {
	if err := requireUser(p); err != nil {
		return nil, err
	}
	u, err := m.Store.GetUser(ctx, p.UserID)
	if err != nil {
		return nil, err
	}
	if in.Name != nil {
		u.Name = clip(strings.TrimSpace(*in.Name), 80)
	}
	return u, m.Store.PutUser(ctx, u)
}

type ChangePasswordInput struct {
	Current string `json:"current_password"`
	New     string `json:"new_password"`
}

// ChangePassword signs out every other session of the user.
func (m *Manager) ChangePassword(ctx context.Context, p *Principal, in ChangePasswordInput) error {
	if err := requireUser(p); err != nil {
		return err
	}
	u, err := m.Store.GetUser(ctx, p.UserID)
	if err != nil {
		return err
	}
	if !checkPassword(u.PasswordHash, in.Current) {
		return model.EH(model.ErrUnauthorized, "Check the email and password, or reset the password.", "current password is wrong")
	}
	if u.PasswordHash, err = hashPassword(in.New); err != nil {
		return err
	}
	if err := m.Store.PutUser(ctx, u); err != nil {
		return err
	}
	se, _ := m.sessionByID(ctx, p)
	m.Store.DeleteUserSessions(ctx, u.ID)
	if se != nil {
		m.Store.PutSession(ctx, se) // keep the caller signed in
	}
	m.Emit(ctx, "user:"+u.ID, "", "", "user.password_changed", "password changed for "+u.Email, nil)
	return nil
}

// ---- email verification & password reset ----

func (m *Manager) newPasswordToken(ctx context.Context, userID, purpose string, ttl time.Duration) (string, error) {
	m.Store.DeletePasswordTokens(ctx, userID, purpose) // one live token per purpose
	tok := secret.RandHex(24)
	now := time.Now().UTC()
	t := &model.PasswordToken{ID: secret.ID("pwt"), UserID: userID, Purpose: purpose, TokenHash: secret.Hash(tok), ExpiresAt: now.Add(ttl), CreatedAt: now}
	return tok, m.Store.PutPasswordToken(ctx, t)
}

// consumeToken validates and burns a verify/reset token.
func (m *Manager) consumeToken(ctx context.Context, tok, purpose string) (*model.User, error) {
	bad := model.EH(model.ErrInvalid, "Request a new link.", "link is invalid or expired")
	t, err := m.Store.GetPasswordTokenByHash(ctx, secret.Hash(strings.TrimSpace(tok)))
	if err != nil || t.Purpose != purpose || t.UsedAt != nil || time.Now().After(t.ExpiresAt) {
		return nil, bad
	}
	if err := m.Store.UsePasswordToken(ctx, t.ID, time.Now().UTC()); err != nil {
		return nil, bad
	}
	u, err := m.Store.GetUser(ctx, t.UserID)
	if err != nil {
		return nil, bad
	}
	return u, nil
}

func (m *Manager) dashboardLink(path, tok string) string {
	base := strings.TrimRight(m.Cfg.DashboardURL, "/")
	if base == "" {
		base = strings.TrimRight(m.Cfg.PublicURL, "/")
	}
	return base + path + "?token=" + tok
}

func (m *Manager) sendVerify(ctx context.Context, u *model.User) {
	if u.EmailVerified {
		return
	}
	tok, err := m.newPasswordToken(ctx, u.ID, model.TokenPurposeVerify, verifyTokenTTL)
	if err != nil {
		log.Printf("verify token for %s: %v", u.Email, err)
		return
	}
	link := m.dashboardLink("/verify-email", tok)
	m.sendMail(ctx, u.Email, "Confirm your email",
		"Confirm your Boxmes email address by opening this link (valid 48 hours):\n\n"+link+"\n\nIf you did not sign up, ignore this email.\n")
}

func (m *Manager) ResendVerify(ctx context.Context, p *Principal) error {
	if err := requireUser(p); err != nil {
		return err
	}
	if p.User.EmailVerified {
		return model.E(model.ErrInvalid, "email is already verified")
	}
	m.sendVerify(ctx, p.User)
	return nil
}

func (m *Manager) VerifyEmail(ctx context.Context, tok string) (*model.User, error) {
	u, err := m.consumeToken(ctx, tok, model.TokenPurposeVerify)
	if err != nil {
		return nil, err
	}
	u.EmailVerified = true
	if err := m.Store.PutUser(ctx, u); err != nil {
		return nil, err
	}
	m.Emit(ctx, "user:"+u.ID, "", "", "user.email_verified", u.Email+" verified", nil)
	return u, nil
}

// ForgotPassword always succeeds so it never reveals whether an account exists.
func (m *Manager) ForgotPassword(ctx context.Context, email string) {
	u, err := m.Store.GetUserByEmail(ctx, strings.ToLower(strings.TrimSpace(email)))
	if err != nil {
		return
	}
	tok, err := m.newPasswordToken(ctx, u.ID, model.TokenPurposeReset, resetTokenTTL)
	if err != nil {
		log.Printf("reset token for %s: %v", u.Email, err)
		return
	}
	link := m.dashboardLink("/reset-password", tok)
	m.sendMail(ctx, u.Email, "Reset your password",
		"Someone asked to reset the password of your Boxmes account. Open this link within 1 hour to choose a new one:\n\n"+link+"\n\nIf it was not you, ignore this email; your password stays the same.\n")
}

type ResetPasswordInput struct {
	Token    string `json:"token"`
	Password string `json:"password"`
}

// ResetPassword sets a new password, verifies the email (the link proved it)
// and signs out every session.
func (m *Manager) ResetPassword(ctx context.Context, in ResetPasswordInput) error {
	ph, err := hashPassword(in.Password)
	if err != nil {
		return err
	}
	u, err := m.consumeToken(ctx, in.Token, model.TokenPurposeReset)
	if err != nil {
		return err
	}
	u.PasswordHash, u.EmailVerified = ph, true
	if err := m.Store.PutUser(ctx, u); err != nil {
		return err
	}
	m.Store.DeleteUserSessions(ctx, u.ID)
	fails.reset(u.Email)
	m.Emit(ctx, "user:"+u.ID, "", "", "user.password_reset", "password reset for "+u.Email, nil)
	return nil
}

// ---- mail ----

func (m *Manager) mailConfigured(ctx context.Context) bool {
	if m.Cfg.Mailer != nil {
		return true
	}
	s := m.Settings(ctx).Notify.SMTP
	return s.Host != "" && s.From != ""
}

// sendMail uses Config.Mailer, else the SMTP server from settings.notify.
// Without SMTP the message is logged so a self-hosted operator can copy the link.
func (m *Manager) sendMail(ctx context.Context, to, subject, body string) {
	subject = "[Boxmes] " + subject
	if m.Cfg.Mailer != nil {
		if err := m.Cfg.Mailer(ctx, to, subject, body); err != nil {
			log.Printf("mail to %s failed: %v", to, err)
		}
		return
	}
	s := m.Settings(ctx).Notify.SMTP
	cfg := notify.SMTP{Host: s.Host, Port: s.Port, Username: s.Username, From: s.From, TLS: s.TLS}
	if !cfg.Configured() {
		log.Printf("mail (SMTP not configured) to %s: %s\n%s", to, subject, body)
		return
	}
	cfg.Password, _ = m.systemSecret(ctx, sysSecretSMTP)
	go func() {
		c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := cfg.Send(c, []string{to}, subject, body); err != nil {
			log.Printf("mail to %s failed: %v", to, err)
		}
	}()
}

// ---- waitlist ----

type WaitlistInput struct {
	Email string `json:"email"`
	Note  string `json:"note"`
}

func (m *Manager) JoinWaitlist(ctx context.Context, in WaitlistInput) (map[string]any, error) {
	email, err := cleanEmail(in.Email)
	if err != nil {
		return nil, err
	}
	if _, err := m.Store.GetUserByEmail(ctx, email); err == nil {
		return map[string]any{"ok": true, "email": email}, nil // do not reveal accounts
	}
	if err := m.Store.AddWaitlist(ctx, &model.WaitlistEntry{Email: email, Note: clip(strings.TrimSpace(in.Note), 500)}); err != nil {
		return nil, err
	}
	list, _ := m.Store.ListWaitlist(ctx)
	pos := len(list)
	for i, e := range list {
		if e.Email == email {
			pos = i + 1
		}
	}
	return map[string]any{"ok": true, "email": email, "position": pos}, nil
}

// ---- platform admin: users, invites, waitlist ----

func requirePlatformAdmin(p *Principal) error {
	if p == nil || !p.Admin {
		return model.EH(model.ErrUnauthorized, "Use an admin key or a platform admin account.", "platform admin only")
	}
	return nil
}

type UserView struct {
	*model.User
	Workspaces []*WorkspaceView `json:"workspaces"`
}

func (m *Manager) ListUsersAdmin(ctx context.Context, p *Principal) ([]*UserView, error) {
	if err := requirePlatformAdmin(p); err != nil {
		return nil, err
	}
	us, err := m.Store.ListUsers(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*UserView, 0, len(us))
	for _, u := range us {
		wss, _ := m.userWorkspaces(ctx, u.ID)
		out = append(out, &UserView{User: u, Workspaces: wss})
	}
	return out, nil
}

type AdminUserInput struct {
	PlatformAdmin *bool `json:"platform_admin"`
	EmailVerified *bool `json:"email_verified"`
}

func (m *Manager) PatchUserAdmin(ctx context.Context, p *Principal, id string, in AdminUserInput) (*model.User, error) {
	if err := requirePlatformAdmin(p); err != nil {
		return nil, err
	}
	u, err := m.Store.GetUser(ctx, id)
	if err != nil {
		return nil, notFound("user", id)
	}
	if in.PlatformAdmin != nil {
		if !*in.PlatformAdmin && u.PlatformAdmin && m.countPlatformAdmins(ctx) <= 1 {
			return nil, model.E(model.ErrInvalid, "cannot remove the last platform admin")
		}
		u.PlatformAdmin = *in.PlatformAdmin
	}
	if in.EmailVerified != nil {
		u.EmailVerified = *in.EmailVerified
	}
	if err := m.Store.PutUser(ctx, u); err != nil {
		return nil, err
	}
	m.Emit(ctx, p.Actor(), "", "", "user.updated", "user "+u.Email+" updated", nil)
	return u, nil
}

func (m *Manager) countPlatformAdmins(ctx context.Context) int {
	us, _ := m.Store.ListUsers(ctx)
	n := 0
	for _, u := range us {
		if u.PlatformAdmin {
			n++
		}
	}
	return n
}

// DeleteUserAdmin removes a user, their sessions, tokens and memberships.
// Workspaces they solely own stay (with their boxes) for the operator to clean up.
func (m *Manager) DeleteUserAdmin(ctx context.Context, p *Principal, id string) error {
	if err := requirePlatformAdmin(p); err != nil {
		return err
	}
	u, err := m.Store.GetUser(ctx, id)
	if err != nil {
		return notFound("user", id)
	}
	if u.PlatformAdmin && m.countPlatformAdmins(ctx) <= 1 {
		return model.E(model.ErrInvalid, "cannot remove the last platform admin")
	}
	ms, _ := m.Store.ListUserMemberships(ctx, id)
	for _, mb := range ms {
		m.Store.DeleteMembership(ctx, mb.WorkspaceID, id)
	}
	m.Store.DeleteUserSessions(ctx, id)
	m.Store.DeletePasswordTokens(ctx, id, "")
	if err := m.Store.DeleteUser(ctx, id); err != nil {
		return err
	}
	m.Emit(ctx, p.Actor(), "", "", "user.deleted", "user "+u.Email+" deleted", nil)
	return nil
}

type InviteInput struct {
	Code           string `json:"code"`             // optional; generated when empty
	MaxUses        int    `json:"max_uses"`         // default 1
	ExpiresInHours int    `json:"expires_in_hours"` // 0 = never
	Note           string `json:"note"`
	Email          string `json:"email"` // optional: mail the code to this address
}

var inviteAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

func newInviteCode() string {
	r := secret.RandHex(8)
	var b strings.Builder
	b.WriteString("BOXMES-")
	for i := 0; i < 8; i++ {
		var v int
		fmt.Sscanf(r[i*2:i*2+2], "%02x", &v)
		if i == 4 {
			b.WriteByte('-')
		}
		b.WriteByte(inviteAlphabet[v%len(inviteAlphabet)])
	}
	return b.String()
}

var inviteCodeRe = func(s string) bool {
	if len(s) < 4 || len(s) > 40 {
		return false
	}
	for _, c := range s {
		if !(c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

type InviteView struct {
	*model.InviteCode
	Redemptions int `json:"redemptions"`
}

func (m *Manager) CreateInvite(ctx context.Context, p *Principal, in InviteInput) (*model.InviteCode, error) {
	if err := requirePlatformAdmin(p); err != nil {
		return nil, err
	}
	code := strings.ToUpper(strings.TrimSpace(in.Code))
	if code == "" {
		code = newInviteCode()
	} else if !inviteCodeRe(code) {
		return nil, model.E(model.ErrInvalid, "invite code must be 4-40 characters: letters, digits, - or _")
	}
	if _, err := m.Store.GetInviteCode(ctx, code); err == nil {
		return nil, model.E(model.ErrInvalid, "invite code %q already exists", code)
	}
	if in.MaxUses == 0 {
		in.MaxUses = 1
	}
	if in.MaxUses < 1 || in.MaxUses > 100000 {
		return nil, model.E(model.ErrInvalid, "max_uses must be 1..100000")
	}
	if in.ExpiresInHours < 0 {
		return nil, model.E(model.ErrInvalid, "expires_in_hours must be >= 0")
	}
	var to string
	if in.Email != "" {
		var err error
		if to, err = cleanEmail(in.Email); err != nil {
			return nil, err
		}
	}
	now := time.Now().UTC()
	c := &model.InviteCode{ID: secret.ID("inv"), Code: code, MaxUses: in.MaxUses, Note: clip(strings.TrimSpace(in.Note), 200), CreatedBy: p.Actor(), CreatedAt: now}
	if in.ExpiresInHours > 0 {
		e := now.Add(time.Duration(in.ExpiresInHours) * time.Hour)
		c.ExpiresAt = &e
	}
	if err := m.Store.PutInviteCode(ctx, c); err != nil {
		return nil, err
	}
	if to != "" {
		m.mailInvite(ctx, to, c)
	}
	m.EmitData(ctx, p.Actor(), "", "", "invite.created", "invite "+code+" created", nil, map[string]any{"max_uses": c.MaxUses})
	return c, nil
}

func (m *Manager) mailInvite(ctx context.Context, to string, c *model.InviteCode) {
	link := m.dashboardLink("/signup", "")
	link = strings.TrimSuffix(link, "?token=") + "?invite=" + c.Code
	m.sendMail(ctx, to, "Your invite",
		"You are invited to Boxmes. Sign up with this invite code:\n\n    "+c.Code+"\n\n"+link+"\n")
}

func (m *Manager) ListInvites(ctx context.Context, p *Principal) ([]*InviteView, error) {
	if err := requirePlatformAdmin(p); err != nil {
		return nil, err
	}
	cs, err := m.Store.ListInviteCodes(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*InviteView, 0, len(cs))
	for _, c := range cs {
		rs, _ := m.Store.ListInviteRedemptions(ctx, c.ID)
		out = append(out, &InviteView{InviteCode: c, Redemptions: len(rs)})
	}
	return out, nil
}

func (m *Manager) DeleteInvite(ctx context.Context, p *Principal, id string) error {
	if err := requirePlatformAdmin(p); err != nil {
		return err
	}
	cs, _ := m.Store.ListInviteCodes(ctx)
	for _, c := range cs {
		if c.ID == id || c.Code == strings.ToUpper(id) {
			m.Emit(ctx, p.Actor(), "", "", "invite.deleted", "invite "+c.Code+" deleted", nil)
			return m.Store.DeleteInviteCode(ctx, c.ID)
		}
	}
	return notFound("invite", id)
}

func (m *Manager) ListWaitlistAdmin(ctx context.Context, p *Principal) ([]*model.WaitlistEntry, error) {
	if err := requirePlatformAdmin(p); err != nil {
		return nil, err
	}
	return m.Store.ListWaitlist(ctx)
}

func (m *Manager) DeleteWaitlistAdmin(ctx context.Context, p *Principal, email string) error {
	if err := requirePlatformAdmin(p); err != nil {
		return err
	}
	return m.Store.DeleteWaitlist(ctx, email)
}

type WaitlistInviteInput struct {
	Emails         []string `json:"emails"` // explicit addresses, or
	Count          int      `json:"count"`  // the first N of the waitlist
	ExpiresInHours int      `json:"expires_in_hours"`
}

// InviteFromWaitlist mails a single-use code to each chosen entry. Entries stay
// on the list until they sign up (Signup removes them).
func (m *Manager) InviteFromWaitlist(ctx context.Context, p *Principal, in WaitlistInviteInput) ([]map[string]any, error) {
	if err := requirePlatformAdmin(p); err != nil {
		return nil, err
	}
	emails := in.Emails
	if len(emails) == 0 {
		if in.Count < 1 {
			return nil, model.E(model.ErrInvalid, "give emails or count >= 1")
		}
		list, err := m.Store.ListWaitlist(ctx)
		if err != nil {
			return nil, err
		}
		for i := 0; i < len(list) && i < in.Count; i++ {
			emails = append(emails, list[i].Email)
		}
	}
	if in.ExpiresInHours == 0 {
		in.ExpiresInHours = 14 * 24
	}
	out := []map[string]any{}
	for _, e := range emails {
		c, err := m.CreateInvite(ctx, p, InviteInput{MaxUses: 1, ExpiresInHours: in.ExpiresInHours, Note: "waitlist " + e, Email: e})
		if err != nil {
			out = append(out, map[string]any{"email": e, "error": err.Error()})
			continue
		}
		out = append(out, map[string]any{"email": e, "code": c.Code, "invite_id": c.ID})
	}
	return out, nil
}
