package e2e

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"

	"boxmes/core/internal/manager"
)

type mailbox struct {
	mu   sync.Mutex
	msgs []struct{ to, subject, body string }
}

func (mb *mailbox) send(_ context.Context, to, subject, body string) error {
	mb.mu.Lock()
	defer mb.mu.Unlock()
	mb.msgs = append(mb.msgs, struct{ to, subject, body string }{to, subject, body})
	return nil
}

var mailTokRe = regexp.MustCompile(`token=([0-9a-f]+)`)

// last token mailed to addr whose subject contains subj.
func (mb *mailbox) token(t *testing.T, to, subj string) string {
	t.Helper()
	mb.mu.Lock()
	defer mb.mu.Unlock()
	for i := len(mb.msgs) - 1; i >= 0; i-- {
		m := mb.msgs[i]
		if m.to == to && strings.Contains(m.subject, subj) {
			if g := mailTokRe.FindStringSubmatch(m.body); g != nil {
				return g[1]
			}
		}
	}
	t.Fatalf("no %q mail to %s", subj, to)
	return ""
}

func call(t *testing.T, base, method, path, token string, body any) (int, map[string]any) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	}
	req, _ := http.NewRequest(method, base+path, rd)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	raw, _ := io.ReadAll(resp.Body)
	json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

func errCode(m map[string]any) string {
	if e, ok := m["error"].(map[string]any); ok {
		s, _ := e["code"].(string)
		return s
	}
	return ""
}

func TestTenancyAuth(t *testing.T) {
	mb := &mailbox{}
	e := setupWith(t, func(c *manager.Config) { c.Mailer = mb.send }, 0)
	u := e.srv.URL

	st, cfg := call(t, u, "GET", "/v1/auth/config", "", nil)
	if st != 200 || cfg["signup_mode"] != "invite" || cfg["first_user"] != true {
		t.Fatalf("config %d %v", st, cfg)
	}

	// first user: no invite needed, platform admin, owns the default workspace
	st, a := call(t, u, "POST", "/v1/auth/signup", "", map[string]any{"email": "Owner@Example.com", "password": "correct horse", "name": "Owner"})
	if st != 201 {
		t.Fatalf("first signup %d %v", st, a)
	}
	ownerTok := a["token"].(string)
	if !strings.HasPrefix(ownerTok, "bxs_") || a["user"].(map[string]any)["platform_admin"] != true || a["workspace"].(map[string]any)["id"] != "ws_default" || a["role"] != "owner" {
		t.Fatalf("first signup result %v", a)
	}
	if a["user"].(map[string]any)["email"] != "owner@example.com" {
		t.Fatalf("email not normalised: %v", a["user"])
	}
	if _, ok := a["user"].(map[string]any)["password_hash"]; ok {
		t.Fatal("password hash leaked")
	}

	// platform admin session can use the operator API
	if st, _ := call(t, u, "GET", "/v1/agents", ownerTok, nil); st != 200 {
		t.Fatalf("admin session /agents %d", st)
	}

	// verify email
	st, v := call(t, u, "POST", "/v1/auth/verify-email", "", map[string]any{"token": mb.token(t, "owner@example.com", "Confirm")})
	if st != 200 || v["email"] != "owner@example.com" {
		t.Fatalf("verify %d %v", st, v)
	}
	if st, _ := call(t, u, "POST", "/v1/auth/verify-email", "", map[string]any{"token": mb.token(t, "owner@example.com", "Confirm")}); st != 400 {
		t.Fatalf("verify token reuse %d", st)
	}

	// second user without invite: refused with a waitlist hint
	st, r := call(t, u, "POST", "/v1/auth/signup", "", map[string]any{"email": "bob@example.com", "password": "bobbobbob"})
	if st == 201 || errCode(r) != "POLICY_DENIED" {
		t.Fatalf("signup without invite %d %v", st, r)
	}
	// waitlist
	st, w := call(t, u, "POST", "/v1/waitlist", "", map[string]any{"email": "bob@example.com", "note": "pls"})
	if st != 201 || w["position"].(float64) != 1 {
		t.Fatalf("waitlist %d %v", st, w)
	}
	// admin invites the waitlist (API key works too)
	st, inv := call(t, u, "POST", "/v1/admin/waitlist/invite", "bx_admin_e2e", map[string]any{"count": 5})
	if st != 200 {
		t.Fatalf("waitlist invite %d %v", st, inv)
	}
	items := inv["items"].([]any)
	code := items[0].(map[string]any)["code"].(string)
	if !strings.HasPrefix(code, "BOXMES-") {
		t.Fatalf("code %q", code)
	}
	// bad code
	if st, r := call(t, u, "POST", "/v1/auth/signup", "", map[string]any{"email": "bob@example.com", "password": "bobbobbob", "invite_code": "NOPE-1234"}); st != 400 {
		t.Fatalf("bad code %d %v", st, r)
	}
	// short password
	if st, _ := call(t, u, "POST", "/v1/auth/signup", "", map[string]any{"email": "bob@example.com", "password": "short", "invite_code": code}); st != 400 {
		t.Fatalf("short password %d", st)
	}
	st, b := call(t, u, "POST", "/v1/auth/signup", "", map[string]any{"email": "bob@example.com", "password": "bobbobbob", "invite_code": strings.ToLower(code), "workspace": "Bob Labs"})
	if st != 201 {
		t.Fatalf("bob signup %d %v", st, b)
	}
	bobTok := b["token"].(string)
	bobWS := b["workspace"].(map[string]any)
	if bobWS["name"] != "Bob Labs" || bobWS["max_boxes"].(float64) != 5 || b["user"].(map[string]any)["platform_admin"] != false {
		t.Fatalf("bob result %v", b)
	}
	// single-use code is gone
	if st, _ := call(t, u, "POST", "/v1/auth/signup", "", map[string]any{"email": "carol@example.com", "password": "carolcarol", "invite_code": code}); st != 400 {
		t.Fatalf("reused code %d", st)
	}
	// waitlist entry removed on sign-up
	_, wl := call(t, u, "GET", "/v1/admin/waitlist", ownerTok, nil)
	if len(wl["items"].([]any)) != 0 {
		t.Fatalf("waitlist not cleared: %v", wl)
	}
	// duplicate email
	if st, _ := call(t, u, "POST", "/v1/auth/signup", "", map[string]any{"email": "BOB@example.com", "password": "bobbobbob"}); st != 400 {
		t.Fatalf("duplicate %d", st)
	}

	// member session (v2.0): workspace-scoped data API yes, platform operator API no
	st, me := call(t, u, "GET", "/v1/auth/me", bobTok, nil)
	if st != 200 || me["workspace"].(map[string]any)["role"] != "owner" || len(me["workspaces"].([]any)) != 1 || me["role"] != "owner" {
		t.Fatalf("me %d %v", st, me)
	}
	for _, p := range []string{"/v1/agents", "/v1/boxes", "/v1/keys"} {
		if st, l := call(t, u, "GET", p, bobTok, nil); st != 200 || len(l["items"].([]any)) != 0 {
			t.Fatalf("member %s → %d %v, want 200 and an empty list", p, st, l)
		}
	}
	for _, p := range []string{"/v1/admin/users", "/v1/settings"} {
		if st, _ := call(t, u, "GET", p, bobTok, nil); st != 401 {
			t.Fatalf("member %s → %d, want 401", p, st)
		}
	}
	if st, _ := call(t, u, "POST", "/v1/auth/workspace", bobTok, map[string]any{"workspace_id": "ws_default"}); st != 404 {
		t.Fatalf("switch to foreign workspace %d", st)
	}
	if st, _ := call(t, u, "GET", "/v1/auth/me", "bx_admin_e2e", nil); st != 401 {
		t.Fatalf("me with api key %d", st)
	}

	// login / wrong password / logout
	if st, _ := call(t, u, "POST", "/v1/auth/login", "", map[string]any{"email": "bob@example.com", "password": "wrongwrong"}); st != 401 {
		t.Fatalf("wrong password %d", st)
	}
	if st, _ := call(t, u, "POST", "/v1/auth/login", "", map[string]any{"email": "nobody@example.com", "password": "wrongwrong"}); st != 401 {
		t.Fatalf("unknown user %d", st)
	}
	st, l := call(t, u, "POST", "/v1/auth/login", "", map[string]any{"email": " Bob@Example.com ", "password": "bobbobbob"})
	if st != 200 || l["workspace"].(map[string]any)["id"] != bobWS["id"] {
		t.Fatalf("login %d %v", st, l)
	}
	tok2 := l["token"].(string)
	if st, _ := call(t, u, "POST", "/v1/auth/logout", tok2, nil); st != 200 {
		t.Fatal("logout")
	}
	if st, _ := call(t, u, "GET", "/v1/auth/me", tok2, nil); st != 401 {
		t.Fatalf("session alive after logout %d", st)
	}

	// change password keeps the caller, drops other sessions
	_, l3 := call(t, u, "POST", "/v1/auth/login", "", map[string]any{"email": "bob@example.com", "password": "bobbobbob"})
	other := l3["token"].(string)
	if st, _ := call(t, u, "POST", "/v1/auth/password", bobTok, map[string]any{"current_password": "nope", "new_password": "newpassword1"}); st != 401 {
		t.Fatalf("change pw wrong current %d", st)
	}
	if st, r := call(t, u, "POST", "/v1/auth/password", bobTok, map[string]any{"current_password": "bobbobbob", "new_password": "newpassword1"}); st != 200 {
		t.Fatalf("change pw %d %v", st, r)
	}
	if st, _ := call(t, u, "GET", "/v1/auth/me", bobTok, nil); st != 200 {
		t.Fatalf("caller signed out by password change %d", st)
	}
	if st, _ := call(t, u, "GET", "/v1/auth/me", other, nil); st != 401 {
		t.Fatalf("other session survived password change %d", st)
	}

	// forgot / reset: same answer for unknown emails; reset signs out everywhere
	if st, _ := call(t, u, "POST", "/v1/auth/password/forgot", "", map[string]any{"email": "ghost@example.com"}); st != 202 {
		t.Fatal("forgot unknown")
	}
	if st, _ := call(t, u, "POST", "/v1/auth/password/forgot", "", map[string]any{"email": "bob@example.com"}); st != 202 {
		t.Fatal("forgot")
	}
	rt := mb.token(t, "bob@example.com", "Reset")
	if st, r := call(t, u, "POST", "/v1/auth/password/reset", "", map[string]any{"token": rt, "password": "resetpass9"}); st != 200 {
		t.Fatalf("reset %d %v", st, r)
	}
	if st, _ := call(t, u, "POST", "/v1/auth/password/reset", "", map[string]any{"token": rt, "password": "again12345"}); st != 400 {
		t.Fatalf("reset token reuse %d", st)
	}
	if st, _ := call(t, u, "GET", "/v1/auth/me", bobTok, nil); st != 401 {
		t.Fatalf("session survived reset %d", st)
	}
	st, l4 := call(t, u, "POST", "/v1/auth/login", "", map[string]any{"email": "bob@example.com", "password": "resetpass9"})
	if st != 200 || l4["user"].(map[string]any)["email_verified"] != true {
		t.Fatalf("login after reset %d %v", st, l4)
	}
	bobTok = l4["token"].(string)

	// admin: invites with uses, users list, promote, last-admin guard
	st, ic := call(t, u, "POST", "/v1/admin/invites", ownerTok, map[string]any{"code": "team-2026", "max_uses": 2, "expires_in_hours": 24})
	if st != 201 || ic["code"] != "TEAM-2026" {
		t.Fatalf("create invite %d %v", st, ic)
	}
	if st, _ := call(t, u, "POST", "/v1/admin/invites", ownerTok, map[string]any{"code": "TEAM-2026"}); st != 400 {
		t.Fatal("duplicate invite code")
	}
	for _, em := range []string{"c1@example.com", "c2@example.com"} {
		if st, r := call(t, u, "POST", "/v1/auth/signup", "", map[string]any{"email": em, "password": "password12", "invite_code": "TEAM-2026"}); st != 201 {
			t.Fatalf("signup %s %d %v", em, st, r)
		}
	}
	if st, _ := call(t, u, "POST", "/v1/auth/signup", "", map[string]any{"email": "c3@example.com", "password": "password12", "invite_code": "TEAM-2026"}); st != 400 {
		t.Fatal("invite over max_uses")
	}
	_, il := call(t, u, "GET", "/v1/admin/invites", ownerTok, nil)
	found := false
	for _, it := range il["items"].([]any) {
		m := it.(map[string]any)
		if m["code"] == "TEAM-2026" {
			found = m["uses"].(float64) == 2 && m["redemptions"].(float64) == 2
		}
	}
	if !found {
		t.Fatalf("invite stats %v", il)
	}
	_, ul := call(t, u, "GET", "/v1/admin/users", ownerTok, nil)
	users := ul["items"].([]any)
	if len(users) != 4 {
		t.Fatalf("users %d", len(users))
	}
	ownerID := a["user"].(map[string]any)["id"].(string)
	if st, _ := call(t, u, "PATCH", "/v1/admin/users/"+ownerID, ownerTok, map[string]any{"platform_admin": false}); st != 400 {
		t.Fatal("last platform admin removed")
	}
	bobID := b["user"].(map[string]any)["id"].(string)
	if st, _ := call(t, u, "PATCH", "/v1/admin/users/"+bobID, ownerTok, map[string]any{"platform_admin": true}); st != 200 {
		t.Fatal("promote bob")
	}
	if st, _ := call(t, u, "GET", "/v1/agents", bobTok, nil); st != 200 {
		t.Fatalf("promoted bob cannot use operator API: %d", st)
	}
	if st, _ := call(t, u, "DELETE", "/v1/admin/users/"+bobID, ownerTok, nil); st != 200 {
		t.Fatal("delete bob")
	}
	if st, _ := call(t, u, "GET", "/v1/auth/me", bobTok, nil); st != 401 {
		t.Fatal("deleted user's session alive")
	}
}

func TestTenancySignupModes(t *testing.T) {
	e := setupWith(t, func(c *manager.Config) { c.SignupMode = "open"; c.Mailer = (&mailbox{}).send }, 0)
	u := e.srv.URL
	for _, em := range []string{"first@example.com", "second@example.com"} {
		if st, r := call(t, u, "POST", "/v1/auth/signup", "", map[string]any{"email": em, "password": "password12"}); st != 201 {
			t.Fatalf("open signup %s %d %v", em, st, r)
		}
	}
	e2 := setupWith(t, func(c *manager.Config) { c.SignupMode = "closed"; c.Mailer = (&mailbox{}).send }, 0)
	u2 := e2.srv.URL
	if st, _ := call(t, u2, "POST", "/v1/auth/signup", "", map[string]any{"email": "first@example.com", "password": "password12"}); st != 201 {
		t.Fatal("closed: first user must still sign up")
	}
	if st, r := call(t, u2, "POST", "/v1/auth/signup", "", map[string]any{"email": "second@example.com", "password": "password12", "invite_code": "WHATEVER"}); st == 201 || errCode(r) != "POLICY_DENIED" {
		t.Fatalf("closed: second user %d %v", st, r)
	}
	if st, _ := call(t, u2, "POST", "/v1/waitlist", "", map[string]any{"email": "not-an-email"}); st != 400 {
		t.Fatal("waitlist bad email")
	}
}
