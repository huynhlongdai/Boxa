package e2e

// v2.0 tenancy: two workspaces on one core never see each other's data;
// roles, workspace keys, quotas, invites and box locations.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"boxmes/core/internal/manager"
	"boxmes/core/internal/model"
	"boxmes/core/internal/secret"
)

var inviteLinkRe = regexp.MustCompile(`/invite/(bxi_[0-9a-f]+)`)


func decodeJSON(r io.Reader, v any) error { return json.NewDecoder(r).Decode(v) }

// has reports a mail to addr whose body contains s.
func (mb *mailbox) has(to, s string) bool {
	mb.mu.Lock()
	defer mb.mu.Unlock()
	for _, m := range mb.msgs {
		if m.to == to && strings.Contains(m.body, s) {
			return true
		}
	}
	return false
}

// match returns the first group of re in the last mail to addr.
func (mb *mailbox) match(t *testing.T, to string, re *regexp.Regexp) string {
	t.Helper()
	mb.mu.Lock()
	defer mb.mu.Unlock()
	for i := len(mb.msgs) - 1; i >= 0; i-- {
		if mb.msgs[i].to == to {
			if g := re.FindStringSubmatch(mb.msgs[i].body); g != nil {
				return g[1]
			}
		}
	}
	t.Fatalf("no mail matching %s to %s", re, to)
	return ""
}

func items(t *testing.T, v map[string]any) []map[string]any {
	t.Helper()
	raw, ok := v["items"].([]any)
	if !ok {
		t.Fatalf("no items in %v", v)
	}
	out := []map[string]any{}
	for _, x := range raw {
		out = append(out, x.(map[string]any))
	}
	return out
}

func ids(list []map[string]any, key string) map[string]bool {
	out := map[string]bool{}
	for _, x := range list {
		out[fmt.Sprint(x[key])] = true
	}
	return out
}

// mcpCall runs one MCP tools/call over HTTP and returns the tool's text.
func mcpTool(t *testing.T, base, token, tool string, args map[string]any) string {
	t.Helper()
	body := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":%q,"arguments":%s}}`, tool, string(mustJSON(args)))
	req, _ := http.NewRequest("POST", base+"/mcp", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
		Error any `json:"error"`
	}
	if err := decodeJSON(resp.Body, &out); err != nil || out.Error != nil {
		t.Fatalf("mcp %s: %v %v", tool, err, out.Error)
	}
	var sb strings.Builder
	for _, c := range out.Result.Content {
		sb.WriteString(c.Text)
	}
	return sb.String()
}

func TestV200TenancyIsolation(t *testing.T) {
	mb := &mailbox{}
	e := setupWith(t, func(c *manager.Config) { c.Mailer = mb.send; c.SignupMode = manager.SignupOpen }, 1)
	u, ctx := e.srv.URL, e.ctx

	// first account = platform admin (owner of ws_default); then two tenants
	st, root := call(t, u, "POST", "/v1/auth/signup", "", map[string]any{"email": "root@example.com", "password": "rootrootroot"})
	if st != 201 || root["user"].(map[string]any)["platform_admin"] != true {
		t.Fatalf("root %d %v", st, root)
	}
	rootTok := root["token"].(string)
	signup := func(email, ws string) (string, string) {
		st, v := call(t, u, "POST", "/v1/auth/signup", "", map[string]any{"email": email, "password": "password123", "workspace": ws})
		if st != 201 || v["user"].(map[string]any)["platform_admin"] != false {
			t.Fatalf("signup %s %d %v", email, st, v)
		}
		return v["token"].(string), v["workspace"].(map[string]any)["id"].(string)
	}
	aTok, wsA := signup("alice@example.com", "Alice Co")
	bTok, wsB := signup("bob@example.com", "Bob Co")
	// both on the pro plan for the isolation part (quotas are tested on free below)
	for _, ws := range []string{wsA, wsB} {
		if st, v := call(t, u, "PATCH", "/v1/admin/workspaces/"+ws, "bx_admin_e2e", map[string]any{"plan": "pro"}); st != 200 || v["plan"] != "pro" {
			t.Fatalf("set plan %d %v", st, v)
		}
	}
	if st, v := call(t, u, "PATCH", "/v1/workspaces/current", aTok, map[string]any{"plan": "team"}); st != 403 || errCode(v) != "POLICY_DENIED" {
		t.Fatalf("member changed own plan: %d %v", st, v)
	}

	type tenant struct {
		tok, ws, box, agent, agentKey, agentKeyID, wsKey, wsKeyID string
	}
	mk := func(tok, ws string) *tenant {
		tn := &tenant{tok: tok, ws: ws}
		st, a := call(t, u, "POST", "/v1/agents", tok, map[string]any{"name": "coder", "kind": "claude-code"}) // same name in both workspaces
		if st != 201 || a["workspace_id"] != ws {
			t.Fatalf("agent %d %v", st, a)
		}
		tn.agent = a["id"].(string)
		st, k := call(t, u, "POST", "/v1/keys", tok, map[string]any{"agent_id": tn.agent, "name": "agent key"})
		if st != 201 || !strings.HasPrefix(k["key"].(string), "bx_agent_") || k["workspace_id"] != ws || k["kind"] != "agent" {
			t.Fatalf("agent key %d %v", st, k)
		}
		tn.agentKey, tn.agentKeyID = k["key"].(string), k["id"].(string)
		st, k = call(t, u, "POST", "/v1/keys", tok, map[string]any{"name": "ci", "scopes": []string{"boxes:read", "boxes:write", "exec", "secrets:read"}, "expires_in_days": 30})
		if st != 201 || k["kind"] != "workspace" || k["workspace_id"] != ws || k["expires_at"] == nil || !strings.HasPrefix(k["key"].(string), "bx_ws_") {
			t.Fatalf("workspace key %d %v", st, k)
		}
		tn.wsKey, tn.wsKeyID = k["key"].(string), k["id"].(string)
		if st, v := call(t, u, "POST", "/v1/secrets", tok, map[string]any{"name": "API_TOKEN", "value": "value-of-" + ws}); st != 201 {
			t.Fatalf("secret %d %v", st, v)
		}
		if st, v := call(t, u, "POST", "/v1/templates", tok, map[string]any{"name": "tpl-" + ws[3:9], "yaml": "template: base\n"}); st != 201 {
			t.Fatalf("template %d %v", st, v)
		}
		st, b := call(t, u, "POST", "/v1/boxes", tn.agentKey, map[string]any{"template": "base", "lifecycle": "session", "secrets": []string{"API_TOKEN"}, "spec": map[string]any{"cpu": 1, "ram_mb": 512, "disk_gb": 5}})
		if st != 201 || b["workspace_id"] != ws || b["status"] != "running" {
			t.Fatalf("box %d %v", st, b)
		}
		tn.box = b["id"].(string)
		st, x := call(t, u, "POST", "/v1/boxes/"+tn.box+"/exec", tok, map[string]any{"command": "echo $API_TOKEN"})
		if st != 200 || !strings.Contains(fmt.Sprint(x["stdout_tail"]), "value-of-"+ws) {
			t.Fatalf("exec with secret %d %v", st, x)
		}
		if st, v := call(t, u, "POST", "/v1/boxes/"+tn.box+"/snapshot", tok, nil); st != 201 {
			t.Fatalf("snapshot %d %v", st, v)
		}
		return tn
	}
	A, B := mk(aTok, wsA), mk(bTok, wsB)
	// approvals and costs in each workspace
	for _, tn := range []*tenant{A, B} {
		e.m.Store.PutApproval(ctx, &model.Approval{ID: "apr_" + tn.ws, Type: "budget", Title: "t", Status: "pending", CreatedAt: time.Now().UTC(), WorkspaceID: tn.ws, BoxID: tn.box, AgentID: tn.agent})
		e.m.Store.AddLedger(ctx, &model.LedgerEntry{ID: secret.ID("led"), TS: time.Now().UTC(), AgentID: tn.agent, BoxID: tn.box, Provider: "local", Kind: "compute", AmountUSD: 0.5, WorkspaceID: tn.ws})
	}

	// ---- A cannot see, change, exec or delete anything of B (404, never 403) ----
	for _, cred := range []struct{ name, tok string }{{"session", A.tok}, {"workspace key", A.wsKey}, {"agent key", A.agentKey}} {
		_, l := call(t, u, "GET", "/v1/boxes?all=true", cred.tok, nil)
		if got := ids(items(t, l), "id"); got[B.box] || !got[A.box] {
			t.Fatalf("%s box list %v", cred.name, got)
		}
		for _, rq := range []struct{ m, p string }{
			{"GET", "/v1/boxes/" + B.box}, {"POST", "/v1/boxes/" + B.box + "/exec"}, {"POST", "/v1/boxes/" + B.box + "/sleep"},
			{"GET", "/v1/boxes/" + B.box + "/snapshots"}, {"GET", "/v1/boxes/" + B.box + "/events"}, {"GET", "/v1/boxes/" + B.box + "/files?path=/"},
			{"POST", "/v1/boxes/" + B.box + "/fork"}, {"POST", "/v1/boxes/" + B.box + "/tickets"}, {"GET", "/v1/boxes/" + B.box + "/ports"},
			{"DELETE", "/v1/boxes/" + B.box},
		} {
			var body any
			if rq.p == "/v1/boxes/"+B.box+"/exec" {
				body = map[string]any{"command": "id"}
			}
			if st, v := call(t, u, rq.m, rq.p, cred.tok, body); st != 404 || errCode(v) != "NOT_FOUND" {
				t.Fatalf("%s %s %s → %d %v, want 404", cred.name, rq.m, rq.p, st, v)
			}
		}
	}
	// agents, keys, secrets, templates, approvals, costs, events
	_, l := call(t, u, "GET", "/v1/agents", A.tok, nil)
	if got := ids(items(t, l), "id"); got[B.agent] || !got[A.agent] {
		t.Fatalf("agents %v", got)
	}
	for _, rq := range []struct{ m, p string }{{"GET", "/v1/agents/" + B.agent}, {"PATCH", "/v1/agents/" + B.agent}, {"DELETE", "/v1/agents/" + B.agent},
		{"DELETE", "/v1/keys/" + B.wsKeyID}, {"DELETE", "/v1/keys/" + B.agentKeyID}, {"POST", "/v1/approvals/apr_" + wsB + "/approve"}} {
		if st, v := call(t, u, rq.m, rq.p, A.tok, map[string]any{}); st != 404 {
			t.Fatalf("A %s %s → %d %v", rq.m, rq.p, st, v)
		}
	}
	_, l = call(t, u, "GET", "/v1/keys", A.tok, nil)
	if got := ids(items(t, l), "id"); got[B.wsKeyID] || got[B.agentKeyID] || !got[A.wsKeyID] || got["key"] {
		t.Fatalf("keys %v", got)
	}
	_, l = call(t, u, "GET", "/v1/secrets", A.tok, nil)
	if sl := items(t, l); len(sl) != 1 || sl[0]["workspace_id"] != wsA {
		t.Fatalf("secrets %v", sl)
	}
	_, l = call(t, u, "GET", "/v1/templates", A.tok, nil)
	if got := ids(items(t, l), "name"); got["tpl-"+wsB[3:9]] || !got["tpl-"+wsA[3:9]] || !got["base"] {
		t.Fatalf("templates %v", got)
	}
	_, l = call(t, u, "GET", "/v1/approvals", A.tok, nil)
	if got := ids(items(t, l), "id"); len(got) != 1 || !got["apr_"+wsA] {
		t.Fatalf("approvals %v", got)
	}
	_, c := call(t, u, "GET", "/v1/costs", A.tok, nil)
	for _, it := range items(t, c) {
		if it["box_id"] == B.box {
			t.Fatalf("costs leak %v", it)
		}
	}
	if c["summary"].(map[string]any)["total"].(float64) < 0.5 {
		t.Fatalf("costs total %v", c["summary"])
	}
	_, ov := call(t, u, "GET", "/v1/overview", A.tok, nil)
	if ov["approvals_pending"].(float64) != 1 || ov["boxes_running"].(float64) != 1 {
		t.Fatalf("overview %v", ov)
	}
	_, au := call(t, u, "GET", "/v1/workspaces/current/audit?limit=500", A.tok, nil)
	for _, ev := range items(t, au) {
		if ev["box_id"] == B.box || ev["workspace_id"] != wsA {
			t.Fatalf("audit leak %v", ev)
		}
	}
	if st, _ := call(t, u, "GET", "/v1/admin/audit", A.tok, nil); st != 401 {
		t.Fatalf("member admin audit %d", st)
	}
	// rotating "API_TOKEN" in A touches only A's secret
	call(t, u, "POST", "/v1/secrets/API_TOKEN/rotate", A.tok, map[string]any{"value": "rotated"})
	if _, x := call(t, u, "POST", "/v1/boxes/"+B.box+"/exec", B.tok, map[string]any{"command": "echo $API_TOKEN"}); !strings.Contains(fmt.Sprint(x["stdout_tail"]), "value-of-"+wsB) {
		t.Fatalf("B secret changed: %v", x)
	}

	// ---- MCP tools are scoped ----
	for _, tok := range []string{A.tok, A.agentKey, A.wsKey} {
		txt := mcpTool(t, u, tok, "box_list", map[string]any{})
		if strings.Contains(txt, B.box) || !strings.Contains(txt, A.box) {
			t.Fatalf("mcp box_list leak: %s", txt)
		}
		if txt := mcpTool(t, u, tok, "box_exec", map[string]any{"box_id": B.box, "command": "id"}); !strings.Contains(txt, "NOT_FOUND") {
			t.Fatalf("mcp exec on B: %s", txt)
		}
	}

	// ---- SSE: A's stream never carries B's events ----
	sctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(sctx, "GET", u+"/v1/stream", nil)
	req.Header.Set("Authorization", "Bearer "+A.tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	seen := make(chan string, 1)
	go func() {
		var sb strings.Builder
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			sb.WriteString(sc.Text() + "\n")
		}
		seen <- sb.String()
	}()
	time.Sleep(200 * time.Millisecond)
	call(t, u, "POST", "/v1/boxes/"+B.box+"/exec", B.tok, map[string]any{"command": "echo b"})
	call(t, u, "POST", "/v1/boxes/"+A.box+"/exec", A.tok, map[string]any{"command": "echo a"})
	time.Sleep(500 * time.Millisecond)
	cancel()
	resp.Body.Close()
	stream := <-seen
	if strings.Contains(stream, B.box) || !strings.Contains(stream, A.box) {
		t.Fatalf("SSE scope: B leaked=%v A seen=%v", strings.Contains(stream, B.box), strings.Contains(stream, A.box))
	}

	// ---- platform admin: global, ?workspace= filter ----
	_, l = call(t, u, "GET", "/v1/boxes", "bx_admin_e2e", nil)
	if got := ids(items(t, l), "id"); !got[A.box] || !got[B.box] {
		t.Fatalf("admin global list %v", got)
	}
	_, l = call(t, u, "GET", "/v1/boxes?workspace="+wsB, rootTok, nil)
	if got := ids(items(t, l), "id"); got[A.box] || !got[B.box] {
		t.Fatalf("admin filtered list %v", got)
	}
	_, l = call(t, u, "GET", "/v1/boxes?workspace="+wsB, A.tok, nil) // ignored for members
	if got := ids(items(t, l), "id"); got[B.box] {
		t.Fatalf("member ?workspace= leak %v", got)
	}

	// ---- workspace key scopes ----
	st, rk := call(t, u, "POST", "/v1/keys", A.tok, map[string]any{"name": "ro", "scopes": []string{"boxes:read"}})
	if st != 201 {
		t.Fatalf("ro key %d %v", st, rk)
	}
	ro := rk["key"].(string)
	if st, _ := call(t, u, "GET", "/v1/boxes/"+A.box, ro, nil); st != 200 {
		t.Fatalf("ro get %d", st)
	}
	if st, v := call(t, u, "POST", "/v1/boxes/"+A.box+"/exec", ro, map[string]any{"command": "id"}); st != 401 || errCode(v) != "UNAUTHORIZED" {
		t.Fatalf("ro exec %d %v", st, v)
	}
	if st, _ := call(t, u, "POST", "/v1/secrets", ro, map[string]any{"name": "X", "value": "y"}); st != 401 {
		t.Fatalf("ro secret create %d", st)
	}
	if st, _ := call(t, u, "GET", "/v1/hosts", A.wsKey, nil); st != 401 {
		t.Fatalf("workspace key on platform route %d", st)
	}
	// last_used_ip recorded, expiry enforced
	_, l = call(t, u, "GET", "/v1/keys", A.tok, nil)
	for _, k := range items(t, l) {
		if k["id"] == rk["id"] && (k["last_used_ip"] == nil || k["last_used_at"] == nil || k["created_by"] == nil) {
			t.Fatalf("key usage not recorded %v", k)
		}
	}
	if st, v := call(t, u, "POST", "/v1/keys", A.tok, map[string]any{"name": "old", "expires_at": time.Now().Add(-time.Hour).Format(time.RFC3339)}); st != 400 {
		t.Fatalf("past expiry %d %v", st, v)
	}
	full, _ := e.m.Store.GetKey(ctx, rk["id"].(string))
	past := time.Now().Add(-time.Minute)
	full.ExpiresAt = &past
	e.m.Store.PutKey(ctx, full)
	if st, v := call(t, u, "GET", "/v1/boxes", ro, nil); st != 401 || !strings.Contains(fmt.Sprint(v["error"]), "expired") {
		t.Fatalf("expired key %d %v", st, v)
	}

	// ---- tickets issued from a session still work (session principal) ----
	st, tk := call(t, u, "POST", "/v1/boxes/"+A.box+"/tickets", A.tok, map[string]any{"purpose": "terminal"})
	if st != 201 || tk["ticket"] == nil {
		t.Fatalf("ticket %d %v", st, tk)
	}
}

func TestV200RolesInvites(t *testing.T) {
	mb := &mailbox{}
	e := setupWith(t, func(c *manager.Config) {
		c.Mailer = mb.send
		c.SignupMode = manager.SignupInvite
		c.DashboardURL = "https://app.example.test"
	}, 1)
	u := e.srv.URL
	_, root := call(t, u, "POST", "/v1/auth/signup", "", map[string]any{"email": "root@example.com", "password": "rootrootroot"})
	rootTok := root["token"].(string)
	// alice gets in with a platform invite code; her workspace is pro
	st, inv := call(t, u, "POST", "/v1/admin/invites", rootTok, map[string]any{"max_uses": 2, "email": "alice@example.com"})
	if st != 201 {
		t.Fatalf("invite %d %v", st, inv)
	}
	if !mb.has("alice@example.com", "https://app.example.test/signup?invite="+inv["code"].(string)) {
		t.Fatalf("invite mail link: %+v", mb.msgs)
	}
	st, a := call(t, u, "POST", "/v1/auth/signup", "", map[string]any{"email": "alice@example.com", "password": "password123", "invite_code": inv["code"]})
	if st != 201 {
		t.Fatalf("alice %d %v", st, a)
	}
	aTok, wsA := a["token"].(string), a["workspace"].(map[string]any)["id"].(string)
	call(t, u, "PATCH", "/v1/admin/workspaces/"+wsA, rootTok, map[string]any{"plan": "pro"})

	// invite carol (no account yet) as viewer: mailed /invite/<token>
	st, wi := call(t, u, "POST", "/v1/workspaces/current/invites", aTok, map[string]any{"email": "carol@example.com", "role": "viewer"})
	if st != 201 || wi["status"] != "pending" || !strings.HasPrefix(wi["invite_url"].(string), "https://app.example.test/invite/bxi_") || wi["token_hash"] != nil {
		t.Fatalf("ws invite %d %v", st, wi)
	}
	tok := mb.match(t, "carol@example.com", inviteLinkRe)
	st, pv := call(t, u, "GET", "/v1/workspace-invites/"+tok, "", nil)
	if st != 200 || pv["role"] != "viewer" || pv["account_exists"] != false || pv["workspace"].(map[string]any)["id"] != wsA || pv["status"] != "pending" {
		t.Fatalf("preview %d %v", st, pv)
	}
	if st, _ := call(t, u, "GET", "/v1/workspace-invites/bxi_nope", "", nil); st != 404 {
		t.Fatalf("bad invite preview %d", st)
	}
	// wrong email cannot use it
	if st, v := call(t, u, "POST", "/v1/auth/signup", "", map[string]any{"email": "mallory@example.com", "password": "password123", "workspace_invite": tok}); st != 400 && st != 403 {
		t.Fatalf("invite with other email %d %v", st, v)
	}
	if _, err := e.m.Store.GetUserByEmail(e.ctx, "mallory@example.com"); err == nil {
		t.Fatal("orphan user after failed sign-up")
	}
	st, c := call(t, u, "POST", "/v1/auth/signup", "", map[string]any{"email": "carol@example.com", "password": "password123", "workspace_invite": tok})
	if st != 201 || c["role"] != "viewer" || c["workspace"].(map[string]any)["id"] != wsA {
		t.Fatalf("carol signup via invite %d %v", st, c)
	}
	cTok := c["token"].(string)
	_, me := call(t, u, "GET", "/v1/auth/me", cTok, nil)
	if len(me["workspaces"].([]any)) != 1 || me["role"] != "viewer" || strings.Contains(fmt.Sprint(me["permissions"]), "boxes:write") {
		t.Fatalf("carol me %v", me)
	}
	if st, _ := call(t, u, "GET", "/v1/workspace-invites/"+tok, "", nil); st != 200 {
		t.Fatal("preview after accept")
	}

	// viewer: reads yes, writes no (POLICY_DENIED 403)
	st, box := call(t, u, "POST", "/v1/boxes", aTok, map[string]any{"template": "base", "spec": map[string]any{"cpu": 1, "ram_mb": 512, "disk_gb": 5}})
	if st != 201 {
		t.Fatalf("alice box %d %v", st, box)
	}
	boxID := box["id"].(string)
	if st, _ := call(t, u, "GET", "/v1/boxes/"+boxID, cTok, nil); st != 200 {
		t.Fatalf("viewer get %d", st)
	}
	for _, rq := range []struct {
		m, p string
		b    any
	}{
		{"POST", "/v1/boxes", map[string]any{"template": "base"}}, {"POST", "/v1/boxes/" + boxID + "/exec", map[string]any{"command": "id"}},
		{"DELETE", "/v1/boxes/" + boxID, nil}, {"POST", "/v1/boxes/" + boxID + "/sleep", nil}, {"POST", "/v1/secrets", map[string]any{"name": "S", "value": "v"}},
		{"POST", "/v1/agents", map[string]any{"name": "x"}}, {"POST", "/v1/keys", map[string]any{}}, {"POST", "/v1/workspaces/current/invites", map[string]any{"email": "z@example.com"}},
		{"POST", "/v1/emergency-stop", map[string]any{"confirm": "DUNG"}}, {"GET", "/v1/workspaces/current/audit", nil},
	} {
		if st, v := call(t, u, rq.m, rq.p, cTok, rq.b); st != 403 || errCode(v) != "POLICY_DENIED" {
			t.Fatalf("viewer %s %s → %d %v, want 403", rq.m, rq.p, st, v)
		}
	}
	_, ml := call(t, u, "GET", "/v1/workspaces/current/members", cTok, nil)
	if len(items(t, ml)) != 2 {
		t.Fatalf("members %v", ml)
	}
	carolID := c["user"].(map[string]any)["id"].(string)
	aliceID := a["user"].(map[string]any)["id"].(string)

	// promote to developer → can create/exec; can manage only own agents
	if st, v := call(t, u, "PATCH", "/v1/workspaces/current/members/"+carolID, aTok, map[string]any{"role": "developer"}); st != 200 || v["role"] != "developer" {
		t.Fatalf("promote %d %v", st, v)
	}
	if st, v := call(t, u, "POST", "/v1/boxes/"+boxID+"/exec", cTok, map[string]any{"command": "echo dev"}); st != 200 {
		t.Fatalf("developer exec %d %v", st, v)
	}
	_, aliceAgent := call(t, u, "POST", "/v1/agents", aTok, map[string]any{"name": "alice-agent"})
	if st, v := call(t, u, "DELETE", "/v1/agents/"+aliceAgent["id"].(string), cTok, nil); st != 403 {
		t.Fatalf("developer deleted someone else's agent %d %v", st, v)
	}
	if st, _ := call(t, u, "POST", "/v1/agents", cTok, map[string]any{"name": "carol-agent"}); st != 201 {
		t.Fatal("developer own agent")
	}
	if st, v := call(t, u, "POST", "/v1/keys", cTok, map[string]any{"scopes": []string{"admin"}}); st != 403 {
		t.Fatalf("developer created admin key %d %v", st, v)
	}
	if st, _ := call(t, u, "POST", "/v1/secrets", cTok, map[string]any{"name": "S", "value": "v"}); st != 403 {
		t.Fatal("developer wrote a secret")
	}

	// admin cannot grant owner; last owner cannot be removed or demoted
	call(t, u, "PATCH", "/v1/workspaces/current/members/"+carolID, aTok, map[string]any{"role": "admin"})
	if st, v := call(t, u, "PATCH", "/v1/workspaces/current/members/"+carolID, cTok, map[string]any{"role": "owner"}); st != 403 {
		t.Fatalf("admin self-promote to owner %d %v", st, v)
	}
	if st, v := call(t, u, "PATCH", "/v1/workspaces/current/members/"+aliceID, cTok, map[string]any{"role": "viewer"}); st != 403 {
		t.Fatalf("admin demoted owner %d %v", st, v)
	}
	if st, v := call(t, u, "PATCH", "/v1/workspaces/current/members/"+aliceID, aTok, map[string]any{"role": "admin"}); st != 400 || !strings.Contains(fmt.Sprint(v), "last owner") {
		t.Fatalf("demote last owner %d %v", st, v)
	}
	if st, _ := call(t, u, "DELETE", "/v1/workspaces/current/members/"+aliceID, aTok, nil); st != 400 {
		t.Fatalf("last owner left %d", st)
	}
	if st, v := call(t, u, "PATCH", "/v1/workspaces/current/members/"+carolID, aTok, map[string]any{"role": "nope"}); st != 400 {
		t.Fatalf("bad role %d %v", st, v)
	}
	// two owners → alice may step down
	call(t, u, "PATCH", "/v1/workspaces/current/members/"+carolID, aTok, map[string]any{"role": "owner"})
	if st, v := call(t, u, "PATCH", "/v1/workspaces/current/members/"+aliceID, aTok, map[string]any{"role": "admin"}); st != 200 {
		t.Fatalf("owner step down %d %v", st, v)
	}

	// existing user joins via accept: bob signs up (2nd code use), is invited as billing
	_, bres := call(t, u, "POST", "/v1/auth/signup", "", map[string]any{"email": "bob@example.com", "password": "password123", "invite_code": inv["code"]})
	bTok := bres["token"].(string)
	if st, _ := call(t, u, "POST", "/v1/workspaces/current/invites", cTok, map[string]any{"email": "bob@example.com", "role": "billing"}); st != 201 {
		t.Fatal("invite bob")
	}
	btok := mb.match(t, "bob@example.com", inviteLinkRe)
	if st, v := call(t, u, "POST", "/v1/workspace-invites/"+btok+"/accept", cTok, nil); st != 403 {
		t.Fatalf("accept by another user %d %v", st, v)
	}
	st, bme := call(t, u, "POST", "/v1/workspace-invites/"+btok+"/accept", bTok, nil)
	if st != 200 || bme["workspace"].(map[string]any)["id"] != wsA || bme["role"] != "billing" || len(bme["workspaces"].([]any)) != 2 {
		t.Fatalf("bob accept %d %v", st, bme)
	}
	if st, _ := call(t, u, "POST", "/v1/workspace-invites/"+btok+"/accept", bTok, nil); st != 400 {
		t.Fatal("accepted twice")
	}
	_, il := call(t, u, "GET", "/v1/workspaces/current/invites", cTok, nil)
	for _, x := range items(t, il) {
		if x["status"] != "accepted" {
			t.Fatalf("invite status %v", x)
		}
	}
	// billing sees usage, cannot exec
	if st, us := call(t, u, "GET", "/v1/workspaces/current/usage", bTok, nil); st != 200 || us["plan"] != "pro" {
		t.Fatalf("billing usage %d %v", st, us)
	}
	if st, _ := call(t, u, "POST", "/v1/boxes/"+boxID+"/exec", bTok, map[string]any{"command": "id"}); st != 403 {
		t.Fatal("billing exec")
	}
	// bob switches back to his own workspace: wsA box is invisible again
	ownWS := bres["workspace"].(map[string]any)["id"].(string)
	call(t, u, "POST", "/v1/auth/workspace", bTok, map[string]any{"workspace_id": ownWS})
	if st, _ := call(t, u, "GET", "/v1/boxes/"+boxID, bTok, nil); st != 404 {
		t.Fatalf("after switch %d", st)
	}
	// new workspace: caller becomes owner
	st, nw := call(t, u, "POST", "/v1/workspaces", bTok, map[string]any{"name": "Side project"})
	if st != 201 || nw["role"] != "owner" || nw["plan"] != "free" {
		t.Fatalf("create ws %d %v", st, nw)
	}
	// revoke a pending invite
	// pro allows 3 members (alice, carol, bob): a 4th invite is over quota
	if st, v := call(t, u, "POST", "/v1/workspaces/current/invites", cTok, map[string]any{"email": "dave@example.com", "role": "auditor"}); st != 403 || errCode(v) != "QUOTA_EXCEEDED" {
		t.Fatalf("max_members %d %v", st, v)
	}
	call(t, u, "PATCH", "/v1/admin/workspaces/"+wsA, rootTok, map[string]any{"plan": "team"})
	_, pi := call(t, u, "POST", "/v1/workspaces/current/invites", cTok, map[string]any{"email": "dave@example.com", "role": "auditor"})
	if st, _ := call(t, u, "DELETE", "/v1/workspaces/current/invites/"+pi["id"].(string), cTok, nil); st != 200 {
		t.Fatal("revoke invite")
	}
	if st, _ := call(t, u, "GET", "/v1/workspace-invites/"+mb.match(t, "dave@example.com", inviteLinkRe), "", nil); st != 404 {
		t.Fatal("revoked invite still previews")
	}
	// member removal + self leave
	if st, _ := call(t, u, "DELETE", "/v1/workspaces/current/members/"+bres["user"].(map[string]any)["id"].(string), cTok, nil); st != 200 {
		t.Fatal("remove bob")
	}
	if st, _ := call(t, u, "DELETE", "/v1/workspaces/current/members/"+aliceID, aTok, nil); st != 200 {
		t.Fatal("alice leaves")
	}
}

func TestV200Quotas(t *testing.T) {
	e := setupWith(t, func(c *manager.Config) { c.SignupMode = manager.SignupOpen }, 1)
	u := e.srv.URL
	call(t, u, "POST", "/v1/auth/signup", "", map[string]any{"email": "root@example.com", "password": "rootrootroot"})
	_, f := call(t, u, "POST", "/v1/auth/signup", "", map[string]any{"email": "free@example.com", "password": "password123"})
	tok, ws := f["token"].(string), f["workspace"].(map[string]any)["id"].(string)
	_, us := call(t, u, "GET", "/v1/workspaces/current/usage", tok, nil)
	q := us["quota"].(map[string]any)
	if us["plan"] != "free" || q["max_boxes"].(float64) != 1 || q["max_cpu_per_box"].(float64) != 1 || q["max_ttl_hours"].(float64) != 2 || q["public_url"] != false || q["egress"] != "restricted" {
		t.Fatalf("free quota %v", us)
	}
	spec := map[string]any{"cpu": 1, "ram_mb": 512, "disk_gb": 5}
	qe := func(st int, v map[string]any, quota string) {
		t.Helper()
		e, _ := v["error"].(map[string]any)
		if st != 403 || errCode(v) != "QUOTA_EXCEEDED" || e["hint"] == "" || len(e["next"].([]any)) == 0 || e["details"].(map[string]any)["quota"] != quota {
			t.Fatalf("want QUOTA_EXCEEDED %s, got %d %v", quota, st, v)
		}
	}
	st, v := call(t, u, "POST", "/v1/boxes", tok, map[string]any{"template": "base", "spec": map[string]any{"cpu": 2, "ram_mb": 512}})
	qe(st, v, "max_cpu_per_box")
	st, v = call(t, u, "POST", "/v1/boxes", tok, map[string]any{"template": "base", "spec": map[string]any{"cpu": 1, "ram_mb": 4096}})
	qe(st, v, "max_ram_mb_per_box")
	st, v = call(t, u, "POST", "/v1/boxes", tok, map[string]any{"template": "base", "ttl_seconds": 3 * 3600, "spec": spec})
	qe(st, v, "max_ttl_hours")
	// localized
	st, v = call(t, u, "POST", "/v1/boxes?lang=vi", tok, map[string]any{"template": "base", "spec": map[string]any{"cpu": 4}})
	if st != 403 || strings.Contains(fmt.Sprint(v["error"].(map[string]any)["message"]), "is limited to") {
		t.Fatalf("vi message %v", v)
	}
	st, b := call(t, u, "POST", "/v1/boxes", tok, map[string]any{"template": "base", "lifecycle": "session", "ports": []int{8000}, "spec": spec})
	if st != 201 || b["ttl_seconds"].(float64) != 7200 || b["egress"].(map[string]any)["source"] != "plan" || b["port_access"].(map[string]any)["8000"] != "token" {
		t.Fatalf("free box %d %v", st, b)
	}
	box := b["id"].(string)
	st, v = call(t, u, "POST", "/v1/boxes", tok, map[string]any{"template": "base", "spec": spec})
	qe(st, v, "max_boxes")
	if v["error"].(map[string]any)["details"].(map[string]any)["used"].(float64) != 1 {
		t.Fatalf("used %v", v)
	}
	st, v = call(t, u, "PUT", "/v1/boxes/"+box+"/ports/8000", tok, map[string]any{"access": "public"})
	qe(st, v, "public_url")
	st, v = call(t, u, "PUT", "/v1/boxes/"+box+"/egress", tok, map[string]any{"allow": []string{"evil.example.com"}})
	qe(st, v, "egress")
	// sleeping frees the slot; waking with another running box is refused
	call(t, u, "POST", "/v1/boxes/"+box+"/sleep", tok, nil)
	st, b2 := call(t, u, "POST", "/v1/boxes", tok, map[string]any{"template": "base", "spec": spec})
	if st != 201 {
		t.Fatalf("second box after sleep %d %v", st, b2)
	}
	st, v = call(t, u, "POST", "/v1/boxes/"+box+"/wake", tok, nil)
	qe(st, v, "max_boxes")
	// platform admin raises the limit with an override
	call(t, u, "PATCH", "/v1/admin/workspaces/"+ws, "bx_admin_e2e", map[string]any{"quota_overrides": map[string]any{"max_boxes": 3}})
	if st, v := call(t, u, "POST", "/v1/boxes/"+box+"/wake", tok, nil); st != 200 {
		t.Fatalf("wake after override %d %v", st, v)
	}
	// countable limits: free allows 3 agents
	for i := 0; i < 3; i++ {
		call(t, u, "POST", "/v1/agents", tok, map[string]any{"name": fmt.Sprintf("a%d", i)})
	}
	st, v = call(t, u, "POST", "/v1/agents", tok, map[string]any{"name": "a3"})
	qe(st, v, "max_agents")
	// suspended workspace: reads only
	call(t, u, "PATCH", "/v1/admin/workspaces/"+ws, "bx_admin_e2e", map[string]any{"status": "suspended"})
	if st, _ := call(t, u, "GET", "/v1/boxes", tok, nil); st != 200 {
		t.Fatal("suspended read")
	}
	if st, v := call(t, u, "POST", "/v1/boxes", tok, map[string]any{"template": "base", "spec": spec}); st != 403 || errCode(v) != "POLICY_DENIED" {
		t.Fatalf("suspended create %d %v", st, v)
	}
	// plans are public
	if st, pl := call(t, u, "GET", "/v1/plans", "", nil); st != 200 || len(items(t, pl)) != 4 {
		t.Fatalf("plans %d %v", st, pl)
	}
	// operator workspace stays unlimited
	_, du := call(t, u, "GET", "/v1/workspaces/current/usage", "bx_admin_e2e", nil)
	if du["plan"] != "unlimited" || du["workspace_id"] != model.DefaultWorkspaceID {
		t.Fatalf("default ws usage %v", du)
	}
}

func TestV200Locations(t *testing.T) {
	e := setupWith(t, func(c *manager.Config) { c.SignupMode = manager.SignupOpen }, 1)
	u, ctx := e.srv.URL, e.ctx
	call(t, u, "POST", "/v1/auth/signup", "", map[string]any{"email": "root@example.com", "password": "rootrootroot"})
	_, a := call(t, u, "POST", "/v1/auth/signup", "", map[string]any{"email": "a@example.com", "password": "password123"})
	_, b := call(t, u, "POST", "/v1/auth/signup", "", map[string]any{"email": "b@example.com", "password": "password123"})
	aTok, wsA := a["token"].(string), a["workspace"].(map[string]any)["id"].(string)
	bTok, wsB := b["token"].(string), b["workspace"].(map[string]any)["id"].(string)
	call(t, u, "PATCH", "/v1/admin/workspaces/"+wsA, "bx_admin_e2e", map[string]any{"plan": "team"})
	// a real local box + placed records on Hetzner Falkenstein, Vultr Tokyo and E2B
	st, lb := call(t, u, "POST", "/v1/boxes", aTok, map[string]any{"template": "base", "spec": map[string]any{"cpu": 1, "ram_mb": 512, "disk_gb": 5}})
	if st != 201 || lb["location"].(map[string]any)["region"] != "local" || lb["location"].(map[string]any)["site"] == "" {
		t.Fatalf("local box location %d %v", st, lb["location"])
	}
	_, ag := call(t, u, "GET", "/v1/agents", aTok, nil)
	agentID := items(t, ag)[0]["id"].(string)
	now := time.Now().UTC()
	put := func(id, ws, prov, region, billing, status string) {
		e.m.Store.PutBox(ctx, &model.Box{ID: id, WorkspaceID: ws, Slug: id, AgentID: agentID, Status: status, Lifecycle: "session", Provider: prov, Region: region, HostID: "h_" + prov, Billing: billing, CreatedAt: now, LastActiveAt: now})
	}
	put("box_hz1", wsA, "hetzner", "fsn1", "burst", model.StatusRunning)
	put("box_hz2", wsA, "hetzner", "fsn1", "burst", model.StatusSleeping)
	put("box_vt1", wsA, "vultr", "nrt", "burst", model.StatusRunning)
	put("box_e2b", wsA, "e2b", "", "sandbox", model.StatusRunning)
	put("box_bhz", wsB, "hetzner", "nbg1", "burst", model.StatusRunning)
	e.m.Store.AddLedger(ctx, &model.LedgerEntry{ID: secret.ID("led"), TS: now, AgentID: agentID, BoxID: "box_hz1", Provider: "hetzner", Kind: "compute", AmountUSD: 0.25, WorkspaceID: wsA})
	e.m.Store.AddLedger(ctx, &model.LedgerEntry{ID: secret.ID("led"), TS: now, AgentID: agentID, BoxID: "box_bhz", Provider: "hetzner", Kind: "compute", AmountUSD: 9, WorkspaceID: wsB})

	q := func(tok, qs string) map[string]bool {
		_, l := call(t, u, "GET", "/v1/boxes"+qs, tok, nil)
		return ids(items(t, l), "id")
	}
	if got := q(aTok, "?region=eu"); len(got) != 2 || !got["box_hz1"] || !got["box_hz2"] {
		t.Fatalf("region=eu %v", got)
	}
	if got := q(aTok, "?region=apac"); len(got) != 1 || !got["box_vt1"] {
		t.Fatalf("region=apac %v", got)
	}
	if got := q(aTok, "?region=sandbox"); len(got) != 1 || !got["box_e2b"] {
		t.Fatalf("region=sandbox %v", got)
	}
	if got := q(aTok, "?location=hetzner-fsn"); len(got) != 2 {
		t.Fatalf("location %v", got)
	}
	if got := q(aTok, "?provider=hetzner&region=eu"); got["box_bhz"] || len(got) != 2 {
		t.Fatalf("provider in workspace %v", got)
	}
	if got := q(bTok, "?region=eu"); len(got) != 1 || !got["box_bhz"] {
		t.Fatalf("B region=eu %v", got)
	}
	_, hb := call(t, u, "GET", "/v1/boxes/box_hz1", aTok, nil)
	loc := hb["location"].(map[string]any)
	if loc["region"] != "eu" || loc["country"] != "DE" || loc["city"] != "Falkenstein" || loc["provider"] != "hetzner" || loc["site"] != "hetzner-fsn" || loc["host"] != "h_hetzner" {
		t.Fatalf("location object %v", loc)
	}
	// MCP box_list passes the filter
	if txt := mcpTool(t, u, aTok, "box_list", map[string]any{"region": "sandbox"}); !strings.Contains(txt, "box_e2b") || strings.Contains(txt, "box_hz1") {
		t.Fatalf("mcp region filter %s", txt)
	}
	// dashboard summary
	st, lv := call(t, u, "GET", "/v1/boxes/locations", aTok, nil)
	if st != 200 {
		t.Fatalf("locations %d %v", st, lv)
	}
	rows := map[string]map[string]any{}
	for _, r := range items(t, lv) {
		rows[r["site"].(string)] = r
	}
	fsn := rows["hetzner-fsn"]
	if fsn == nil || fsn["running"].(float64) != 1 || fsn["sleeping"].(float64) != 1 || fsn["total"].(float64) != 2 || fsn["cost_today_usd"].(float64) != 0.25 || fsn["region"] != "eu" {
		t.Fatalf("fsn row %v", fsn)
	}
	if rows["hetzner-nbg"] != nil || rows["e2b"] == nil || rows["e2b"]["region"] != "sandbox" {
		t.Fatalf("rows %v", rows)
	}
	if tot := lv["total"].(map[string]any); tot["total"].(float64) != 5 || tot["cost_today_usd"].(float64) != 0.25 {
		t.Fatalf("total %v", tot)
	}
	// platform: every workspace, or one with ?workspace=
	_, pl := call(t, u, "GET", "/v1/boxes/locations?workspace="+wsB, "bx_admin_e2e", nil)
	if r := items(t, pl); len(r) != 1 || r[0]["site"] != "hetzner-nbg" {
		t.Fatalf("admin locations %v", r)
	}
}

// TestV200SignupRace: concurrent first sign-ups produce exactly one platform admin.
func TestV200SignupRace(t *testing.T) {
	e := setupWith(t, func(c *manager.Config) { c.SignupMode = manager.SignupOpen }, 0)
	u := e.srv.URL
	var wg sync.WaitGroup
	var mu sync.Mutex
	admins, created := 0, 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			st, v := call(t, u, "POST", "/v1/auth/signup", "", map[string]any{"email": fmt.Sprintf("u%d@example.com", i), "password": "password123"})
			mu.Lock()
			defer mu.Unlock()
			if st == 201 {
				created++
				if v["user"].(map[string]any)["platform_admin"] == true {
					admins++
				}
			}
		}(i)
	}
	wg.Wait()
	if created != 8 || admins != 1 {
		t.Fatalf("created %d, platform admins %d", created, admins)
	}
	us, _ := e.m.Store.ListUsers(e.ctx)
	n := 0
	for _, x := range us {
		if x.PlatformAdmin {
			n++
		}
	}
	ws, _ := e.m.Store.GetWorkspace(e.ctx, model.DefaultWorkspaceID)
	if n != 1 || ws.OwnerUserID == "" {
		t.Fatalf("stored admins %d, default owner %q", n, ws.OwnerUserID)
	}
}
