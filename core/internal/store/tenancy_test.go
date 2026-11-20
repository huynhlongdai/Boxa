package store

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"boxaz/core/internal/model"
)

func openSQLite(t *testing.T) *SQL {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestTenancyCRUD_SQLite(t *testing.T) { runTenancyCRUD(t, openSQLite(t)) }

func runTenancyCRUD(t *testing.T, s *SQL) {
	ctx := context.Background()
	now := time.Now().UTC()

	// users
	u := &model.User{ID: "usr_1", Email: "  Nina@Example.COM ", PasswordHash: "h1", CreatedAt: now}
	if err := s.PutUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetUserByEmail(ctx, "nina@example.com")
	if err != nil || got.ID != "usr_1" || got.PasswordHash != "h1" || got.EmailVerified {
		t.Fatalf("user by email %+v %v", got, err)
	}
	u.EmailVerified, u.PlatformAdmin = true, true
	if err := s.PutUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	var ev, pa int
	s.db.QueryRow("SELECT email_verified, platform_admin FROM users WHERE id='usr_1'").Scan(&ev, &pa)
	if ev != 1 || pa != 1 {
		t.Fatalf("bool columns %d %d", ev, pa)
	}
	if err := s.PutUser(ctx, &model.User{ID: "usr_dup", Email: "nina@example.com", CreatedAt: now}); err == nil {
		t.Fatal("duplicate email must fail")
	}
	s.PutUser(ctx, &model.User{ID: "usr_2", Email: "b@x.io", PasswordHash: "h2", CreatedAt: now.Add(time.Second)})
	if l, _ := s.ListUsers(ctx); len(l) != 2 || l[0].PasswordHash != "" {
		t.Fatalf("list users %d", len(l))
	}

	// workspaces + memberships
	w := &model.Workspace{ID: "ws_a", Name: "Team A", OwnerUserID: "usr_1", CreatedAt: now}
	if err := s.PutWorkspace(ctx, w); err != nil {
		t.Fatal(err)
	}
	gw, err := s.GetWorkspace(ctx, "ws_a")
	if err != nil || gw.MaxBoxes != 5 || gw.MaxTTLHours != 12 || gw.Name != "Team A" {
		t.Fatalf("workspace %+v %v", gw, err)
	}
	var mb int
	s.db.QueryRow("SELECT max_boxes FROM workspaces WHERE id='ws_a'").Scan(&mb)
	if mb != 5 {
		t.Fatalf("max_boxes col %d", mb)
	}
	s.PutWorkspace(ctx, &model.Workspace{ID: "ws_b", Name: "B", OwnerUserID: "usr_2", MaxBoxes: 2, CreatedAt: now})
	if err := s.PutMembership(ctx, &model.Membership{WorkspaceID: "ws_a", UserID: "usr_1", Role: model.RoleOwner, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	s.PutMembership(ctx, &model.Membership{WorkspaceID: "ws_a", UserID: "usr_2", Role: model.RoleMember, CreatedAt: now})
	s.PutMembership(ctx, &model.Membership{WorkspaceID: "ws_b", UserID: "usr_2", Role: model.RoleOwner, CreatedAt: now})
	if err := s.PutMembership(ctx, &model.Membership{WorkspaceID: "ws_b", UserID: "usr_1", Role: "god"}); err == nil {
		t.Fatal("bad role accepted")
	}
	if m, err := s.GetMembership(ctx, "ws_a", "usr_2"); err != nil || m.Role != model.RoleMember {
		t.Fatalf("membership %+v %v", m, err)
	}
	if l, _ := s.ListMemberships(ctx, "ws_a"); len(l) != 2 {
		t.Fatalf("members %d", len(l))
	}
	if l, _ := s.ListUserWorkspaces(ctx, "usr_2"); len(l) != 2 {
		t.Fatalf("user workspaces %d", len(l))
	}
	if l, _ := s.ListUserMemberships(ctx, "usr_1"); len(l) != 1 {
		t.Fatalf("user memberships %d", len(l))
	}
	s.DeleteMembership(ctx, "ws_a", "usr_2")
	if _, err := s.GetMembership(ctx, "ws_a", "usr_2"); !errors.Is(err, model.ErrNoRows) {
		t.Fatal("membership not deleted")
	}

	// sessions
	se := &model.Session{ID: "ses_1", TokenHash: "th1", UserID: "usr_1", WorkspaceID: "ws_a", ExpiresAt: now.Add(time.Hour), CreatedAt: now}
	if err := s.PutSession(ctx, se); err != nil {
		t.Fatal(err)
	}
	if g, err := s.GetSessionByHash(ctx, "th1", now); err != nil || g.UserID != "usr_1" || g.TokenHash != "th1" {
		t.Fatalf("session %+v %v", g, err)
	}
	if _, err := s.GetSessionByHash(ctx, "th1", now.Add(2*time.Hour)); !errors.Is(err, model.ErrNoRows) {
		t.Fatal("expired session returned")
	}
	if _, err := s.GetSessionByHash(ctx, "", now); err == nil {
		t.Fatal("empty hash matched")
	}
	s.PutSession(ctx, &model.Session{ID: "ses_2", TokenHash: "th2", UserID: "usr_1", ExpiresAt: now.Add(-time.Minute), CreatedAt: now})
	if n, err := s.DeleteExpiredSessions(ctx, now); err != nil || n != 1 {
		t.Fatalf("expired deleted %d %v", n, err)
	}
	s.DeleteUserSessions(ctx, "usr_1")
	if _, err := s.GetSessionByHash(ctx, "th1", now); !errors.Is(err, model.ErrNoRows) {
		t.Fatal("user sessions not deleted")
	}

	// invite codes
	exp := now.Add(24 * time.Hour)
	ic := &model.InviteCode{ID: "inv_1", Code: "HELLO", MaxUses: 2, ExpiresAt: &exp, Note: "beta", CreatedBy: "usr_1", CreatedAt: now}
	if err := s.PutInviteCode(ctx, ic); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RedeemInvite(ctx, "HELLO", "usr_1", now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RedeemInvite(ctx, "HELLO", "usr_1", now); !errors.Is(err, ErrInviteUnavailable) {
		t.Fatalf("double redeem same user: %v", err)
	}
	if g, _ := s.GetInviteCode(ctx, "HELLO"); g.Uses != 1 {
		t.Fatalf("uses after rollback %d", g.Uses)
	}
	if _, err := s.RedeemInvite(ctx, "HELLO", "usr_2", now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RedeemInvite(ctx, "HELLO", "usr_3", now); !errors.Is(err, ErrInviteUnavailable) {
		t.Fatal("exhausted code redeemed")
	}
	ic.Note = "edited"
	s.PutInviteCode(ctx, ic) // must not reset uses
	g, err := s.GetInviteCode(ctx, "HELLO")
	if err != nil || g.Uses != 2 || g.Note != "edited" || g.ExpiresAt == nil || !g.ExpiresAt.Equal(exp.Truncate(time.Microsecond)) {
		t.Fatalf("invite %+v %v", g, err)
	}
	s.PutInviteCode(ctx, &model.InviteCode{ID: "inv_2", Code: "OLD", MaxUses: 5, ExpiresAt: ptrTime(now.Add(-time.Hour)), CreatedAt: now})
	if _, err := s.RedeemInvite(ctx, "OLD", "usr_1", now); !errors.Is(err, ErrInviteUnavailable) {
		t.Fatal("expired code redeemed")
	}
	if _, err := s.RedeemInvite(ctx, "NOPE", "usr_1", now); !errors.Is(err, ErrInviteUnavailable) {
		t.Fatal("unknown code redeemed")
	}
	s.PutInviteCode(ctx, &model.InviteCode{ID: "inv_3", Code: "FOREVER", MaxUses: 1, CreatedAt: now})
	if _, err := s.RedeemInvite(ctx, "FOREVER", "usr_1", now); err != nil {
		t.Fatalf("no-expiry code: %v", err)
	}
	if l, _ := s.ListInviteRedemptions(ctx, "inv_1"); len(l) != 2 {
		t.Fatalf("redemptions %d", len(l))
	}
	if l, _ := s.ListInviteCodes(ctx); len(l) != 3 {
		t.Fatalf("codes %d", len(l))
	}
	s.DeleteInviteCode(ctx, "inv_2")
	if _, err := s.GetInviteCode(ctx, "OLD"); !errors.Is(err, model.ErrNoRows) {
		t.Fatal("invite not deleted")
	}

	// waitlist
	s.AddWaitlist(ctx, &model.WaitlistEntry{Email: "W@x.io", Note: "hi", CreatedAt: now})
	if err := s.AddWaitlist(ctx, &model.WaitlistEntry{Email: "w@x.io", Note: "again", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	wl, _ := s.ListWaitlist(ctx)
	if len(wl) != 1 || wl[0].Note != "again" || wl[0].Email != "w@x.io" {
		t.Fatalf("waitlist %+v", wl)
	}
	s.DeleteWaitlist(ctx, "W@X.io")
	if wl, _ := s.ListWaitlist(ctx); len(wl) != 0 {
		t.Fatal("waitlist not deleted")
	}

	// password tokens
	pt := &model.PasswordToken{ID: "pt_1", UserID: "usr_1", Purpose: model.TokenPurposeReset, TokenHash: "rh", ExpiresAt: now.Add(time.Hour), CreatedAt: now}
	if err := s.PutPasswordToken(ctx, pt); err != nil {
		t.Fatal(err)
	}
	if err := s.PutPasswordToken(ctx, &model.PasswordToken{ID: "pt_x", Purpose: "steal"}); err == nil {
		t.Fatal("bad purpose accepted")
	}
	gt, err := s.GetPasswordTokenByHash(ctx, "rh")
	if err != nil || gt.UsedAt != nil || gt.Purpose != "reset" || gt.TokenHash != "rh" {
		t.Fatalf("token %+v %v", gt, err)
	}
	if err := s.UsePasswordToken(ctx, "pt_1", now); err != nil {
		t.Fatal(err)
	}
	if err := s.UsePasswordToken(ctx, "pt_1", now); !errors.Is(err, ErrTokenUsed) {
		t.Fatal("token reused")
	}
	if gt, _ := s.GetPasswordTokenByHash(ctx, "rh"); gt.UsedAt == nil {
		t.Fatal("used_at not set")
	}
	s.DeletePasswordTokens(ctx, "usr_1", "")
	if _, err := s.GetPasswordTokenByHash(ctx, "rh"); !errors.Is(err, model.ErrNoRows) {
		t.Fatal("token not deleted")
	}

	// workspace-scoped agents: same name allowed in two workspaces, not twice in one
	if err := s.PutAgent(ctx, &model.Agent{ID: "agt_a", WorkspaceID: "ws_a", Name: "bot", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutAgent(ctx, &model.Agent{ID: "agt_b", WorkspaceID: "ws_b", Name: "bot", CreatedAt: now}); err != nil {
		t.Fatalf("same name other workspace: %v", err)
	}
	if err := s.PutAgent(ctx, &model.Agent{ID: "agt_c", WorkspaceID: "ws_a", Name: "bot", CreatedAt: now}); err == nil {
		t.Fatal("duplicate name in workspace accepted")
	}
	if a, err := s.GetAgentInWorkspace(ctx, "ws_b", "bot"); err != nil || a.ID != "agt_b" || a.WorkspaceID != "ws_b" {
		t.Fatalf("agent in ws %+v %v", a, err)
	}
	if l, _ := s.ListAgentsInWorkspace(ctx, "ws_a"); len(l) != 1 {
		t.Fatalf("agents in ws_a %d", len(l))
	}
	s.PutBox(ctx, &model.Box{ID: "box_a", WorkspaceID: "ws_a", AgentID: "agt_a", Status: model.StatusRunning, Lifecycle: "task", Provider: "local", HostID: "h", CreatedAt: now})
	s.PutBox(ctx, &model.Box{ID: "box_b", WorkspaceID: "ws_b", AgentID: "agt_b", Status: model.StatusRunning, Lifecycle: "task", Provider: "local", HostID: "h", CreatedAt: now})
	if l, _ := s.ListBoxes(ctx, BoxFilter{WorkspaceID: "ws_a"}); len(l) != 1 || l[0].WorkspaceID != "ws_a" {
		t.Fatalf("boxes in ws_a %d", len(l))
	}
	agt := "agt_a"
	s.PutKey(ctx, &model.APIKey{ID: "key_a", WorkspaceID: "ws_a", AgentID: &agt, Hash: "kh", CreatedAt: now})
	if l, _ := s.ListKeysInWorkspace(ctx, "ws_a"); len(l) != 1 {
		t.Fatal("keys in ws")
	}
	s.PutSecret(ctx, &model.Secret{Name: "TOKEN_A", WorkspaceID: "ws_b", ValueEnc: "enc", CreatedAt: now})
	if l, _ := s.ListSecretsInWorkspace(ctx, "ws_b"); len(l) != 1 || l[0].WorkspaceID != "ws_b" {
		t.Fatal("secrets in ws")
	}

	// delete workspace removes its memberships
	s.DeleteWorkspace(ctx, "ws_b")
	if l, _ := s.ListUserMemberships(ctx, "usr_2"); len(l) != 0 {
		t.Fatalf("memberships after ws delete %d", len(l))
	}
	s.DeleteUser(ctx, "usr_2")
	if _, err := s.GetUser(ctx, "usr_2"); !errors.Is(err, model.ErrNoRows) {
		t.Fatal("user not deleted")
	}
}

func ptrTime(t time.Time) *time.Time { return &t }

// TestBackfill_SQLite builds a pre-tenancy database (migrations 001+002 only),
// inserts legacy rows, then opens it normally so 003 + backfill run.
func TestBackfill_SQLite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	legacy := &SQL{db: raw, dialect: "sqlite"}
	runBackfill(t, legacy, func() *SQL {
		raw.Close()
		s, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		return s
	})
}

func runBackfill(t *testing.T, legacy *SQL, reopen func() *SQL) {
	ctx := context.Background()
	if err := legacy.migrateUpTo(ctx, "002_event_data.sql"); err != nil {
		t.Fatal(err)
	}
	now := FmtTime(time.Now())
	ins := func(q string, args ...any) {
		if err := legacy.exec(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	ins("INSERT INTO agents(id, name, created_at, doc) VALUES(?,?,?,?)", "agt_old", "legacy", now, `{"id":"agt_old","name":"legacy"}`)
	ins("INSERT INTO boxes(id, agent_id, status, lifecycle, provider, host_id, created_at, doc) VALUES(?,?,?,?,?,?,?,?)",
		"box_old", "agt_old", "running", "task", "local", "h1", now, `{"id":"box_old","agent_id":"agt_old","status":"running"}`)
	ins("INSERT INTO api_keys(id, agent_id, hash, created_at, doc) VALUES(?,?,?,?,?)", "key_old", "agt_old", "hh", now, `{"id":"key_old","hash":"hh"}`)
	ins("INSERT INTO secrets(id, created_at, doc) VALUES(?,?,?)", "S_OLD", now, `{"name":"S_OLD","value_enc":"x"}`)

	s := reopen()
	ws, err := s.GetWorkspace(ctx, model.DefaultWorkspaceID)
	if err != nil || ws.Name != "default" || ws.MaxBoxes != 5 || ws.MaxTTLHours != 12 {
		t.Fatalf("default ws %+v %v", ws, err)
	}
	if a, err := s.GetAgent(ctx, "agt_old"); err != nil || a.WorkspaceID != model.DefaultWorkspaceID {
		t.Fatalf("agent %+v %v", a, err)
	}
	if a, err := s.GetAgentInWorkspace(ctx, model.DefaultWorkspaceID, "legacy"); err != nil || a.ID != "agt_old" {
		t.Fatalf("agent by ws+name %v", err)
	}
	if b, err := s.GetBox(ctx, "box_old"); err != nil || b.WorkspaceID != model.DefaultWorkspaceID {
		t.Fatalf("box %+v %v", b, err)
	}
	if k, err := s.GetKeyByHash(ctx, "hh"); err != nil || k.WorkspaceID != model.DefaultWorkspaceID || k.Hash != "hh" {
		t.Fatalf("key %+v %v", k, err)
	}
	if sec, err := s.GetSecret(ctx, "S_OLD"); err != nil || sec.WorkspaceID != model.DefaultWorkspaceID || sec.ValueEnc != "x" {
		t.Fatalf("secret %+v %v", sec, err)
	}
	for _, tbl := range []string{"agents", "boxes", "api_keys", "secrets"} {
		var n int
		s.db.QueryRow("SELECT COUNT(*) FROM " + tbl + " WHERE workspace_id IS NULL OR workspace_id=''").Scan(&n)
		if n != 0 {
			t.Fatalf("%s has %d rows without workspace", tbl, n)
		}
	}
	// idempotent
	if n, err := s.BackfillDefaultWorkspace(ctx); err != nil || n != 0 {
		t.Fatalf("second backfill moved %d %v", n, err)
	}
	if l, _ := s.ListWorkspaces(ctx); len(l) != 1 {
		t.Fatalf("workspaces %d", len(l))
	}
}

// ---- Postgres (optional): TEST_DATABASE_URL or work/gw/neon.env ----

func pgDSN(t *testing.T) string {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		if b, err := os.ReadFile(os.Getenv("TEST_NEON_ENV")); err == nil {
			for _, l := range strings.Split(string(b), "\n") {
				l = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), "export "))
				if strings.HasPrefix(l, "DATABASE_URL=") {
					dsn = strings.Trim(strings.TrimPrefix(l, "DATABASE_URL="), `"'`)
				}
			}
		}
	}
	if dsn == "" {
		t.Skip("no Postgres DSN (set TEST_DATABASE_URL or TEST_NEON_ENV)")
	}
	return dsn
}

// pgSchema returns a *sql.DB pinned to a fresh schema (SET search_path on
// every new connection, since Neon drops the search_path startup parameter)
// and drops the schema on cleanup. It verifies isolation before returning.
func pgSchema(t *testing.T, name string) *sql.DB {
	dsn := pgDSN(t)
	u, err := url.Parse(dsn)
	if err != nil {
		t.Skip("unparseable DSN")
	}
	q := u.Query()
	q.Del("channel_binding")
	u.RawQuery = q.Encode()
	u.Host = strings.Replace(u.Host, "-pooler.", ".", 1) // session-level SET needs a direct connection
	base := u.String()
	admin, err := sql.Open("pgx", base)
	if err != nil {
		t.Skip("pgx open failed")
	}
	if err := admin.Ping(); err != nil {
		admin.Close()
		t.Skip("Postgres unreachable")
	}
	admin.Exec("DROP SCHEMA IF EXISTS " + name + " CASCADE")
	if _, err := admin.Exec("CREATE SCHEMA " + name); err != nil {
		admin.Close()
		t.Fatalf("create schema: %v", err)
	}
	cfg, err := pgx.ParseConfig(base)
	if err != nil {
		t.Fatal(err)
	}
	db := stdlib.OpenDB(*cfg, stdlib.OptionAfterConnect(func(ctx context.Context, c *pgx.Conn) error {
		_, err := c.Exec(ctx, "SET search_path TO "+name)
		return err
	}))
	t.Cleanup(func() {
		db.Close()
		admin.Exec("DROP SCHEMA IF EXISTS " + name + " CASCADE")
		admin.Close()
	})
	var cur sql.NullString
	if err := db.QueryRow("SELECT current_schema()").Scan(&cur); err != nil || cur.String != name {
		t.Fatalf("schema not isolated (current_schema=%q); refusing to touch the database", cur.String)
	}
	return db
}

func openPG(t *testing.T, db *sql.DB, migrate bool) *SQL {
	s := &SQL{db: db, dialect: "postgres"}
	if migrate {
		if err := s.migrate(context.Background()); err != nil {
			t.Fatalf("migrate pg: %v", err)
		}
		if _, err := s.BackfillDefaultWorkspace(context.Background()); err != nil {
			t.Fatalf("backfill pg: %v", err)
		}
	}
	return s
}

func TestTenancyCRUD_Postgres(t *testing.T) {
	s := openPG(t, pgSchema(t, "test_x_crud"), true)
	// migrations are idempotent on Postgres too
	if err := s.migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	runTenancyCRUD(t, s)
}

func TestBackfill_Postgres(t *testing.T) {
	db := pgSchema(t, "test_x_backfill")
	legacy := openPG(t, db, false)
	runBackfill(t, legacy, func() *SQL { return openPG(t, db, true) })
}
