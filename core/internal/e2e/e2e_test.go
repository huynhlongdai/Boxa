// Package e2e runs core + boxd (local driver) in-process and exercises the
// public REST/MCP/WebSocket surface end to end.
package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"boxa/core/internal/api"
	"boxa/core/internal/boxd"
	"boxa/core/internal/client"
	"boxa/core/internal/manager"
	"boxa/core/internal/model"
	"boxa/core/internal/provider"
	"boxa/core/internal/secret"
	"boxa/core/internal/store"
)

type env struct {
	t     *testing.T
	m     *manager.Manager
	srv   *httptest.Server
	admin *client.Client
	ctx   context.Context
}

func setup(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "boxa.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	crypt, _ := secret.New("test-master")
	m := manager.New(st, provider.Default(nil), crypt, manager.Config{
		BootstrapToken: "boot-token", DisablePriceFeed: true,
		LedgerInterval: 150 * time.Millisecond, ReaperInterval: 150 * time.Millisecond,
		LocalPriceMonth: 730, // $1/h host → visible ledger amounts
		CreateWait:      30 * time.Second,
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if _, err := m.Init(ctx, "bx_admin_e2e"); err != nil {
		t.Fatal(err)
	}
	m.Start(ctx)
	srv := httptest.NewServer(api.New(m, "", "*"))
	t.Cleanup(srv.Close)

	drv, err := boxd.NewLocalDriver(filepath.Join(dir, "boxd-root"))
	if err != nil {
		t.Fatal(err)
	}
	agent, err := boxd.NewAgent(boxd.Config{
		CoreURL: "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/hosts/connect", BootstrapToken: "boot-token",
		StateDir: filepath.Join(dir, "boxd-state"), Provider: "local", IP: "127.0.0.1", MetricsEvery: time.Second, Version: "test",
	}, drv)
	if err != nil {
		t.Fatal(err)
	}
	go agent.Run(ctx)
	e := &env{t: t, m: m, srv: srv, admin: client.New(srv.URL, "bx_admin_e2e"), ctx: ctx}
	deadline := time.Now().Add(10 * time.Second)
	for m.Hub.Count() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("boxd did not connect")
		}
		time.Sleep(50 * time.Millisecond)
	}
	return e
}

func (e *env) must(c *client.Client, method, path string, body, out any) {
	e.t.Helper()
	if err := c.JSON(e.ctx, method, path, body, out); err != nil {
		e.t.Fatalf("%s %s: %v", method, path, err)
	}
}

func (e *env) agentClient(name string, budgetDay float64) (*client.Client, string) {
	var a model.Agent
	e.must(e.admin, "POST", "/agents", map[string]any{"name": name, "kind": "claude-code", "budget_day_usd": budgetDay}, &a)
	var k model.APIKey
	e.must(e.admin, "POST", "/keys", map[string]any{"agent_id": a.ID, "name": name + "-key"}, &k)
	if k.Key == "" || !strings.HasPrefix(k.Key, "bx_agent_") {
		e.t.Fatalf("bad key %+v", k)
	}
	return client.New(e.srv.URL, k.Key), a.ID
}

func waitStatus(t *testing.T, m *manager.Manager, id, status string, d time.Duration) *model.Box {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		b, err := m.Store.GetBox(context.Background(), id)
		if err == nil && b.Status == status {
			return b
		}
		if time.Now().After(deadline) {
			t.Fatalf("box %s: want %s, have %+v", id, status, b)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestEndToEnd(t *testing.T) {
	e := setup(t)
	ag, agentID := e.agentClient("coder", 0)

	// Unauthorized + contract error envelope.
	bad := client.New(e.srv.URL, "nope")
	err := bad.JSON(e.ctx, "GET", "/boxes", nil, nil)
	if ae, ok := err.(*client.APIError); !ok || ae.Code != "UNAUTHORIZED" || ae.Hint == "" {
		t.Fatalf("want UNAUTHORIZED with hint, got %v", err)
	}

	// Create with idempotency.
	var box model.Box
	body := map[string]any{"template": "python", "lifecycle": "session", "slug": "e2e", "spec": map[string]any{"cpu": 1, "ram_mb": 512, "disk_gb": 5}}
	st, raw, _ := ag.Do(e.ctx, "POST", "/boxes", map[string]any{"template": "python", "lifecycle": "session", "slug": "e2e", "spec": body["spec"], "idempotency_key": "k1"})
	if st != 201 {
		t.Fatalf("create: %d %s", st, raw)
	}
	json.Unmarshal(raw, &box)
	if box.Status != model.StatusRunning || box.AgentID != agentID || box.PlacementReason == "" || box.HostID == "" {
		t.Fatalf("unexpected box %+v", box)
	}
	var again model.Box
	st, raw, _ = ag.Do(e.ctx, "POST", "/boxes", map[string]any{"template": "python", "spec": body["spec"], "idempotency_key": "k1"})
	json.Unmarshal(raw, &again)
	if st != 200 || again.ID != box.ID {
		t.Fatalf("idempotency: %d %s vs %s", st, again.ID, box.ID)
	}

	// Exec with shaping.
	var res model.ExecResult
	e.must(ag, "POST", "/boxes/"+box.ID+"/exec", map[string]any{"command": "echo hi && ls"}, &res)
	if res.ExitCode != 0 || !strings.HasPrefix(res.StdoutTail, "hi") {
		t.Fatalf("exec: %+v", res)
	}
	e.must(ag, "POST", "/boxes/"+box.ID+"/exec", map[string]any{"command": "seq 1 500; exit 3", "tail_lines": 10}, &res)
	if res.ExitCode != 3 || !res.Truncated || res.LogID == nil || res.TokensSaved <= 0 || strings.Count(res.StdoutTail, "\n") != 9 {
		t.Fatalf("shaping: %+v", res)
	}
	st, raw, _ = ag.Do(e.ctx, "GET", "/logs/"+*res.LogID, nil)
	if st != 200 || !strings.Contains(string(raw), "\n250\n") {
		t.Fatalf("log: %d", st)
	}

	// Files.
	e.must(ag, "PUT", "/boxes/"+box.ID+"/files", map[string]any{"path": "/work/src/a.txt", "content": "alpha\nbeta\ngamma\n"}, nil)
	var fr map[string]any
	e.must(ag, "GET", "/boxes/"+box.ID+"/files?path=/work/src/a.txt&from=2&to=3", nil, &fr)
	if fr["content"] != "beta\ngamma\n" || fr["total_lines"].(float64) != 3 {
		t.Fatalf("read: %+v", fr)
	}
	e.must(ag, "GET", "/boxes/"+box.ID+"/files?path=/work/src", nil, &fr)
	if ents := fr["entries"].([]any); len(ents) != 1 {
		t.Fatalf("dir: %+v", fr)
	}
	var gr struct {
		Matches []struct {
			Path string
			Line int
		}
	}
	e.must(ag, "POST", "/boxes/"+box.ID+"/grep", map[string]any{"pattern": "^gam", "path": "/work"}, &gr)
	if len(gr.Matches) != 1 || gr.Matches[0].Line != 3 {
		t.Fatalf("grep: %+v", gr)
	}
	if err := ag.JSON(e.ctx, "GET", "/boxes/"+box.ID+"/files?path=/../../etc/passwd", nil, &fr); err == nil {
		t.Fatalf("path escape not blocked: %+v", fr) // local driver maps it inside /work → NOT_FOUND
	}
	var obs map[string]any
	e.must(ag, "GET", "/boxes/"+box.ID+"/observe", nil, &obs)

	// Async job.
	var jr struct {
		JobID string   `json:"job_id"`
		Next  []string `json:"next"`
	}
	e.must(ag, "POST", "/boxes/"+box.ID+"/exec", map[string]any{"command": "sleep 0.3; echo async-ok", "async": true}, &jr)
	var job model.Job
	e.must(ag, "GET", "/jobs/"+jr.JobID+"?wait=10", nil, &job)
	if job.Status != "done" || job.Result == nil || job.Result.StdoutTail != "async-ok" {
		t.Fatalf("job: %+v", job)
	}

	// Terminal (WebSocket PTY).
	wsURL := "ws" + strings.TrimPrefix(e.srv.URL, "http") + "/v1/boxes/" + box.ID + "/terminal?token=" + ag.Key
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("ws: %v", err)
	}
	ws.WriteMessage(websocket.TextMessage, []byte(`{"type":"resize","cols":100,"rows":30}`))
	ws.WriteMessage(websocket.TextMessage, []byte("echo term-$((40+2))\n"))
	ws.SetReadDeadline(time.Now().Add(10 * time.Second))
	var got strings.Builder
	for !strings.Contains(got.String(), "term-42") {
		_, msg, err := ws.ReadMessage()
		if err != nil {
			t.Fatalf("ws read: %v (got %q)", err, got.String())
		}
		got.Write(msg)
	}
	ws.Close()

	// Sleep / auto-wake / wake / snapshot.
	e.must(ag, "POST", "/boxes/"+box.ID+"/sleep", map[string]any{}, &box)
	if box.Status != model.StatusSleeping {
		t.Fatalf("sleep: %s", box.Status)
	}
	e.must(ag, "POST", "/boxes/"+box.ID+"/exec", map[string]any{"command": "cat src/a.txt | wc -l"}, &res)
	if strings.TrimSpace(res.StdoutTail) != "3" {
		t.Fatalf("autowake exec: %+v", res)
	}
	var sn model.Snapshot
	e.must(ag, "POST", "/boxes/"+box.ID+"/snapshot", map[string]any{}, &sn)
	if !strings.HasPrefix(sn.Location, "local://") {
		t.Fatalf("snapshot: %+v", sn)
	}

	// Secrets injected as env, names-only API.
	var sec map[string]any
	e.must(e.admin, "POST", "/secrets", map[string]any{"name": "API_TOKEN", "value": "s3cr3t", "agents": []string{agentID}}, &sec)
	if _, leaked := sec["value"]; leaked {
		t.Fatal("secret value returned")
	}
	var sbox model.Box
	e.must(ag, "POST", "/boxes", map[string]any{"template": "base", "lifecycle": "task", "ttl_seconds": 600, "secrets": []string{"API_TOKEN"}, "spec": map[string]any{"ram_mb": 256}}, &sbox)
	e.must(ag, "POST", "/boxes/"+sbox.ID+"/exec", map[string]any{"command": "echo $API_TOKEN"}, &res)
	if res.StdoutTail != "s3cr3t" {
		t.Fatalf("secret env: %+v", res)
	}

	// Ledger accrues (host $1/h, box cpu 1/2 → $0.5/h).
	time.Sleep(600 * time.Millisecond)
	var costs manager.CostsResult
	e.must(e.admin, "GET", "/costs", nil, &costs)
	if len(costs.Items) == 0 || costs.Summary.Total <= 0 || costs.Summary.PoolUSD <= 0 {
		t.Fatalf("costs: %+v", costs.Summary)
	}
	var ov manager.Overview
	e.must(e.admin, "GET", "/overview", nil, &ov)
	if ov.BoxesRunning < 2 || len(ov.Cost7d) != 7 || ov.SpentToday <= 0 {
		t.Fatalf("overview: %+v", ov)
	}
	st, raw, _ = e.admin.Do(e.ctx, "GET", "/costs.csv", nil)
	if st != 200 || !strings.HasPrefix(string(raw), "id,ts,agent_id") {
		t.Fatalf("csv: %d", st)
	}

	// MCP over HTTP.
	for _, msg := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
	} {
		req, _ := http.NewRequest("POST", e.srv.URL+"/mcp", strings.NewReader(msg))
		req.Header.Set("Authorization", "Bearer "+ag.Key)
		resp, err := http.DefaultClient.Do(req)
		if err != nil || resp.StatusCode != 200 {
			t.Fatalf("mcp: %v %v", err, resp)
		}
		var out map[string]any
		json.NewDecoder(resp.Body).Decode(&out)
		resp.Body.Close()
		if r, ok := out["result"].(map[string]any); ok {
			if tools, ok := r["tools"].([]any); ok && len(tools) != 18 {
				t.Fatalf("want 18 tools, got %d", len(tools))
			}
		} else {
			t.Fatalf("mcp result: %+v", out)
		}
	}

	// Reaper: idle session → sleep; task past TTL → terminate.
	b, _ := e.m.Store.GetBox(e.ctx, box.ID)
	b.LastActiveAt = time.Now().Add(-time.Hour)
	e.m.Store.PutBox(e.ctx, b)
	waitStatus(t, e.m, box.ID, model.StatusSleeping, 5*time.Second)
	var tbox model.Box
	e.must(ag, "POST", "/boxes", map[string]any{"template": "base", "lifecycle": "task", "ttl_seconds": 1, "spec": map[string]any{"ram_mb": 256}}, &tbox)
	waitStatus(t, e.m, tbox.ID, model.StatusTerminated, 5*time.Second)

	// Agent isolation: another agent cannot see this box.
	other, _ := e.agentClient("other", 0)
	if err := other.JSON(e.ctx, "GET", "/boxes/"+box.ID, nil, nil); err == nil {
		t.Fatal("other agent can see box")
	}

	// Emergency stop requires DUNG.
	if err := e.admin.JSON(e.ctx, "POST", "/emergency-stop", map[string]any{"confirm": "no"}, nil); err == nil {
		t.Fatal("emergency stop without confirm")
	}
	var es map[string]any
	e.must(e.admin, "POST", "/emergency-stop", map[string]any{"confirm": "DUNG"}, &es)
	waitStatus(t, e.m, sbox.ID, model.StatusSleeping, 2*time.Second)

	// Down.
	e.must(ag, "DELETE", "/boxes/"+box.ID, nil, &box)
	if box.Status != model.StatusTerminated {
		t.Fatalf("down: %s", box.Status)
	}
	var evs model.List[model.Event]
	e.must(ag, "GET", "/boxes/"+box.ID+"/events", nil, &evs)
	if len(evs.Items) < 5 {
		t.Fatalf("events: %d", len(evs.Items))
	}
}

func TestBudgetEnforcement(t *testing.T) {
	e := setup(t)
	// $1/h host, box with 2 cpu on 2-cpu host? use cpu=host cpu share; tiny budget trips fast.
	ag, agentID := e.agentClient("spender", 0.0002)
	var box model.Box
	e.must(ag, "POST", "/boxes", map[string]any{"template": "base", "lifecycle": "session", "spec": map[string]any{"cpu": 1, "ram_mb": 256}}, &box)
	if box.Status != model.StatusRunning {
		t.Fatalf("box: %+v", box)
	}
	waitStatus(t, e.m, box.ID, model.StatusSleeping, 10*time.Second)
	var aps model.List[model.Approval]
	e.must(e.admin, "GET", "/approvals?status=pending", nil, &aps)
	if len(aps.Items) == 0 || aps.Items[0].Type != "budget" {
		t.Fatalf("approvals: %+v", aps.Items)
	}
	// New boxes are refused while over budget.
	err := ag.JSON(e.ctx, "POST", "/boxes", map[string]any{"template": "base"}, nil)
	if ae, ok := err.(*client.APIError); !ok || ae.Code != "BUDGET_EXCEEDED" {
		t.Fatalf("want BUDGET_EXCEEDED, got %v", err)
	}
	// Approving lets the agent continue today.
	e.must(e.admin, "POST", "/approvals/"+aps.Items[0].ID+"/approve", map[string]any{}, nil)
	e.must(ag, "POST", "/boxes/"+box.ID+"/wake", map[string]any{}, &box)
	if box.Status != model.StatusRunning {
		t.Fatalf("wake after approval: %s", box.Status)
	}
	_ = agentID
}

func TestTemplatesAndSettings(t *testing.T) {
	e := setup(t)
	var v struct {
		OK     bool `json:"ok"`
		Errors []struct {
			Line    int    `json:"line"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	e.must(e.admin, "POST", "/templates/validate", map[string]any{"yaml": "name: x\ntemplate: python\nspec: { cpu: 2, ram: 4G }\nlifecycle: forever\nbogus: 1\n"}, &v)
	if v.OK || len(v.Errors) != 2 || v.Errors[0].Line != 4 || v.Errors[1].Line != 5 {
		t.Fatalf("validate: %+v", v)
	}
	e.must(e.admin, "POST", "/templates", map[string]any{"name": "crawler", "description": "d", "yaml": "name: crawler\ntemplate: python\nspec: { cpu: 1, ram: 512M, disk: 5G }\nlifecycle: task\nttl: 10m\nsetup: [\"echo setup-ran > /dev/null\"]\n"}, nil)
	var ts model.List[model.Template]
	e.must(e.admin, "GET", "/templates", nil, &ts)
	if len(ts.Items) != 5 {
		t.Fatalf("templates: %d", len(ts.Items))
	}
	var b model.Box
	e.must(e.admin, "POST", "/boxes", map[string]any{"template": "crawler"}, &b)
	if b.Status != model.StatusRunning || b.Spec.RAMMB != 512 || b.Lifecycle != "session" && b.Lifecycle != "task" {
		t.Fatalf("custom template box: %+v", b)
	}
	var s model.Settings
	e.must(e.admin, "PUT", "/settings", map[string]any{"budget_month_usd": 25, "reaper": map[string]any{"idle_minutes": 7}}, &s)
	if s.BudgetMonthUSD != 25 || s.Reaper.IdleMinutes != 7 || s.Reaper.KeepSnapshots == 0 {
		t.Fatalf("settings: %+v", s)
	}
	var ps model.List[model.Provider]
	e.must(e.admin, "GET", "/providers", nil, &ps)
	if len(ps.Items) != 6 || ps.Items[0].ID != "local" {
		t.Fatalf("providers: %+v", ps.Items)
	}
	var sys map[string]any
	e.must(e.admin, "GET", "/admin/system", nil, &sys)
	var hs model.List[model.Host]
	e.must(e.admin, "GET", "/hosts", nil, &hs)
	if len(hs.Items) != 1 || !hs.Items[0].Connected || hs.Items[0].Status != "ready" {
		t.Fatalf("hosts: %+v", hs.Items)
	}
	var snip struct {
		Snippet string `json:"snippet"`
	}
	var agents model.List[model.Agent]
	e.must(e.admin, "GET", "/agents", nil, &agents)
	e.must(e.admin, "GET", "/agents/"+agents.Items[0].ID+"/snippet?kind=claude-code", nil, &snip)
	if !strings.Contains(snip.Snippet, "claude mcp add") {
		t.Fatalf("snippet: %+v", snip)
	}
}

func TestIngress(t *testing.T) {
	e := setup(t)
	var b model.Box
	e.must(e.admin, "POST", "/boxes", map[string]any{"template": "base", "slug": "web", "ports": []int{18931}, "spec": map[string]any{"ram_mb": 256}}, &b)
	if !strings.HasPrefix(b.URL, "https://web-127-0-0-1.sslip.io/?boxa_token=") {
		t.Fatalf("url: %q", b.URL)
	}
	var res model.ExecResult
	e.must(e.admin, "POST", "/boxes/"+b.ID+"/exec", map[string]any{"command": "echo hello-ingress > index.html; (python3 -m http.server 18931 >/dev/null 2>&1 &) ; sleep 0.7"}, &res)
	defer e.admin.JSON(e.ctx, "DELETE", "/boxes/"+b.ID, nil, nil)
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	get := func(path string, cookie *http.Cookie) *http.Response {
		req, _ := http.NewRequest("GET", e.srv.URL+path, nil)
		req.Host = "web-127-0-0-1.sslip.io"
		if cookie != nil {
			req.AddCookie(cookie)
		}
		resp, err := noRedirect.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	if r := get("/index.html", nil); r.StatusCode != 401 {
		t.Fatalf("unprotected: %d", r.StatusCode)
	}
	tok := strings.SplitN(b.URL, "boxa_token=", 2)[1]
	r := get("/?boxa_token="+tok, nil)
	if r.StatusCode != 302 || len(r.Cookies()) == 0 {
		t.Fatalf("token redirect: %d", r.StatusCode)
	}
	r = get("/index.html", r.Cookies()[0])
	body := make([]byte, 100)
	n, _ := r.Body.Read(body)
	if r.StatusCode != 200 || !strings.Contains(string(body[:n]), "hello-ingress") {
		t.Fatalf("proxy: %d %q", r.StatusCode, body[:n])
	}
	resp, _ := http.Get(e.srv.URL + "/v1/internal/tls-ask?domain=web-127-0-0-1.sslip.io")
	if resp.StatusCode != 200 {
		t.Fatalf("tls-ask: %d", resp.StatusCode)
	}
	resp, _ = http.Get(e.srv.URL + "/v1/internal/tls-ask?domain=evil.example.com")
	if resp.StatusCode != 404 {
		t.Fatalf("tls-ask evil: %d", resp.StatusCode)
	}
}
