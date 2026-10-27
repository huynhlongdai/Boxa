package e2e

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"boxaz/core/internal/api"
	"boxaz/core/internal/client"
	"boxaz/core/internal/model"
)

func get(t *testing.T, url string, hdr map[string]string) (int, string, http.Header) {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header
}

// Every chi route is documented in the OpenAPI document.
func TestOpenAPICoversRoutes(t *testing.T) {
	e := setup(t)
	srv := api.New(e.m, "", "*")
	doc := map[string]bool{}
	for _, o := range api.OpenAPIOps() {
		doc[o[0]+" "+o[1]] = true
	}
	n := 0
	chi.Walk(srv.Routes(), func(method, route string, h http.Handler, mw ...func(http.Handler) http.Handler) error {
		route = strings.TrimSuffix(strings.ReplaceAll(route, "/*/", "/"), "/*")
		if route == "" {
			route = "/"
		}
		if route == "/mcp" && method != "POST" && method != "DELETE" {
			return nil
		}
		n++
		if !doc[method+" "+route] {
			t.Errorf("route %s %s missing from OpenAPI ops", method, route)
		}
		return nil
	})
	if n < 80 {
		t.Fatalf("walked only %d routes", n)
	}
	st, body, _ := get(t, e.srv.URL+"/v1/openapi.json", nil)
	var d map[string]any
	if st != 200 || json.Unmarshal([]byte(body), &d) != nil || d["openapi"] != "3.1.0" {
		t.Fatalf("openapi: %d %.200s", st, body)
	}
	schemas := d["components"].(map[string]any)["schemas"].(map[string]any)
	for _, s := range []string{"Box", "ExecResult", "QuoteResult", "BatchResult", "CreateBoxRequest", "Error", "AgentKind", "AgentSnippet"} {
		if schemas[s] == nil {
			t.Errorf("schema %s missing", s)
		}
	}
	// every $ref resolves
	for _, ref := range strings.Split(body, `"$ref":"#/components/schemas/`)[1:] {
		name := ref[:strings.Index(ref, `"`)]
		if schemas[name] == nil {
			t.Errorf("dangling $ref %s", name)
		}
	}
}

func TestDiscoveryAndI18n(t *testing.T) {
	e := setup(t)
	u := e.srv.URL
	st, en, _ := get(t, u+"/llms.txt", nil)
	if st != 200 || !strings.HasPrefix(en, "# Boxaz\n\n> ") || !strings.Contains(en, "box_exec_batch") {
		t.Fatalf("llms.txt: %d %.300s", st, en)
	}
	_, vi, _ := get(t, u+"/llms.txt", map[string]string{"Accept-Language": "vi-VN,vi;q=0.9"})
	if vi == en || !strings.Contains(vi, "Bắt đầu nhanh") {
		t.Fatalf("llms.txt vi not localized: %.300s", vi)
	}
	_, ja, _ := get(t, u+"/llms-full.txt?lang=ja", nil)
	if !strings.Contains(ja, "クイックスタート") || !strings.Contains(ja, "error-BUDGET_EXCEEDED") || !strings.Contains(ja, "POST /v1/quote") {
		t.Fatalf("llms-full ja: %.300s", ja)
	}
	for _, p := range []string{"/.well-known/mcp.json", "/.well-known/agent.json", "/v1/agent-kinds", "/v1/errors"} {
		st, body, _ := get(t, u+p, nil)
		var v map[string]any
		if st != 200 || json.Unmarshal([]byte(body), &v) != nil {
			t.Fatalf("%s: %d %.200s", p, st, body)
		}
		switch p {
		case "/v1/agent-kinds":
			if len(v["items"].([]any)) != 22 {
				t.Fatalf("kinds: %d", len(v["items"].([]any)))
			}
		case "/.well-known/agent.json":
			if v["protocolVersion"] != "0.3.0" || len(v["skills"].([]any)) != 27 || !strings.HasSuffix(v["url"].(string), "/a2a") || v["capabilities"].(map[string]any)["streaming"] != true {
				t.Fatalf("agent card %+v", v)
			}
		case "/.well-known/mcp.json":
			if !strings.HasSuffix(v["transport"].(map[string]any)["url"].(string), "/mcp") {
				t.Fatal(v)
			}
		}
	}
	for _, f := range []string{"openai", "anthropic", "gemini", "mcp"} {
		st, body, _ := get(t, u+"/v1/tools?format="+f, nil)
		if st != 200 || !strings.Contains(body, "box_fork") {
			t.Fatalf("tools %s: %d", f, st)
		}
	}
	st, body, _ := get(t, u+"/v1/tools?format=x", map[string]string{"Accept-Language": "zh"})
	if st != 400 || !strings.Contains(body, "未知的工具格式") {
		t.Fatalf("tools bad format zh: %d %s", st, body)
	}

	// Localized error envelope; code stays.
	ag, _ := e.agentClient("i18n", 0)
	ag.Lang = "vi"
	err := ag.JSON(e.ctx, "GET", "/boxes/box_nope", nil, nil)
	ae, ok := err.(*client.APIError)
	if !ok || ae.Code != "NOT_FOUND" || !strings.Contains(ae.Message, "không tìm thấy") || !strings.Contains(ae.Hint, "Kiểm tra lại id") || !strings.HasSuffix(ae.DocsURL, "/v1/errors#NOT_FOUND") {
		t.Fatalf("vi error: %+v", err)
	}
	ag.Lang = "ja"
	err = ag.JSON(e.ctx, "POST", "/boxes", map[string]any{"template": "nope"}, nil)
	if ae, ok := err.(*client.APIError); !ok || ae.Code != "INVALID" || !strings.Contains(ae.Message, "テンプレート") {
		t.Fatalf("ja error: %+v", err)
	}
	ag.Lang = ""
	err = ag.JSON(e.ctx, "GET", "/boxes/box_nope", nil, nil)
	if ae := err.(*client.APIError); ae.Message != `box "box_nope" not found` {
		t.Fatalf("en default: %+v", ae)
	}
	// Settings locale.
	var s model.Settings
	e.must(e.admin, "GET", "/settings", nil, &s)
	if s.Locale != "vi" {
		t.Fatalf("default settings locale %q", s.Locale)
	}
	if err := e.admin.JSON(e.ctx, "PUT", "/settings", map[string]any{"locale": "fr"}, nil); err == nil {
		t.Fatal("invalid locale accepted")
	}
	e.must(e.admin, "PUT", "/settings", map[string]any{"locale": "ja"}, &s)
	if s.Locale != "ja" {
		t.Fatal(s.Locale)
	}
}

func TestV11Boxes(t *testing.T) {
	e := setup(t)
	ag, agentID := e.agentClient("v11", 0)
	e.must(e.admin, "PATCH", "/agents/"+agentID, map[string]any{"locale": "vi", "kind": "langgraph"}, nil)

	// Quote: dry run, nothing created.
	var q map[string]any
	e.must(ag, "POST", "/quote", map[string]any{"template": "python", "lifecycle": "task", "ttl_seconds": 7200, "spec": map[string]any{"ram_mb": 512}}, &q)
	if q["available"] != true || q["provider"] != "local" || q["expected_hours"].(float64) != 2 || q["within_budget"] != true || q["cost_per_hour"].(float64) <= 0 {
		t.Fatalf("quote: %+v", q)
	}
	if exp := q["cost_per_hour"].(float64) * 2; abs(q["expected_total_usd"].(float64)-exp) > 1e-3 {
		t.Fatalf("expected_total %v vs %v", q["expected_total_usd"], exp)
	}
	var bl struct{ Items []model.Box }
	e.must(ag, "GET", "/boxes", nil, &bl)
	if len(bl.Items) != 0 {
		t.Fatal("quote created a box")
	}
	if err := ag.JSON(e.ctx, "POST", "/quote", map[string]any{"spec": map[string]any{"ram_mb": 900000}}, nil); err == nil || err.(*client.APIError).Code != "POLICY_DENIED" {
		t.Fatalf("quota quote: %v", err)
	}
	e.must(e.admin, "PUT", "/admin/quotas", map[string]any{"max_ram_mb_per_box": 4 << 20}, nil)
	e.must(ag, "POST", "/quote", map[string]any{"spec": map[string]any{"ram_mb": 2 << 20}}, &q)
	if q["available"] != false || !strings.Contains(q["placement_reason"].(string), "no host fits") {
		t.Fatalf("unavailable quote: %+v", q)
	}

	// Create from yaml; yaml stored verbatim; next hints.
	yml := "name: v11\ntemplate: python\nlifecycle: session\nspec:\n  cpu: 1\n  ram: 512M\n  disk: 5G\n"
	var created map[string]any
	e.must(ag, "POST", "/boxes", map[string]any{"yaml": yml}, &created)
	id := created["id"].(string)
	if created["status"] != "running" || len(created["next"].([]any)) == 0 || !strings.Contains(created["next"].([]any)[0].(string), id) {
		t.Fatalf("create: %+v", created)
	}
	var y map[string]any
	e.must(ag, "GET", "/boxes/"+id+"/yaml", nil, &y)
	if y["yaml"] != yml || y["source"] != "request" {
		t.Fatalf("yaml: %+v", y)
	}

	// AGENTS.md in the agent's locale (vi).
	var f map[string]any
	e.must(ag, "GET", "/boxes/"+id+"/files?path=/AGENTS.md", nil, &f)
	if c := f["content"].(string); !strings.Contains(c, "Bạn đang làm việc trong một **box Boxaz**") || !strings.Contains(c, "box_exec_batch") {
		t.Fatalf("AGENTS.md: %.400s", c)
	}

	// Exec: next hints + event data.
	var ex map[string]any
	e.must(ag, "POST", "/boxes/"+id+"/exec", map[string]any{"command": "seq 1 400; exit 2", "tail_lines": 5}, &ex)
	if ex["exit_code"].(float64) != 2 || ex["log_id"] == nil || !strings.Contains(strings.Join(toStrs(ex["next"]), " "), "box_logs(") {
		t.Fatalf("exec: %+v", ex)
	}
	var evs struct{ Items []model.Event }
	e.must(ag, "GET", "/boxes/"+id+"/events", nil, &evs)
	found := false
	for _, ev := range evs.Items {
		if ev.Type == "box.exec" && ev.Data["log_id"] == ex["log_id"] && ev.Data["tokens_saved"].(float64) > 0 {
			found = true
		}
	}
	if !found {
		t.Fatalf("exec event lacks tokens_saved/log_id: %+v", evs.Items)
	}

	// Batch: stop_on_error default true, and keep-going.
	var br struct {
		OK        bool
		Completed int
		StoppedAt *int `json:"stopped_at"`
		Results   []struct {
			Command    string
			ExitCode   int    `json:"exit_code"`
			StdoutTail string `json:"stdout_tail"`
		}
		Next []string
	}
	e.must(ag, "POST", "/boxes/"+id+"/exec-batch", map[string]any{"commands": []any{"echo one", map[string]any{"command": "false"}, "echo three"}}, &br)
	if br.OK || br.Completed != 2 || br.StoppedAt == nil || *br.StoppedAt != 1 || br.Results[0].StdoutTail != "one" || len(br.Next) == 0 {
		t.Fatalf("batch stop: %+v", br)
	}
	br.StoppedAt = nil
	e.must(ag, "POST", "/boxes/"+id+"/exec-batch", map[string]any{"commands": []string{"echo one", "false", "echo three"}, "stop_on_error": false}, &br)
	if br.OK || br.Completed != 3 || br.StoppedAt != nil || br.Results[2].StdoutTail != "three" {
		t.Fatalf("batch keep-going: %+v", br)
	}

	// Long-poll job.
	var jr map[string]any
	e.must(ag, "POST", "/boxes/"+id+"/exec", map[string]any{"command": "sleep 0.5; echo lp", "async": true}, &jr)
	if !strings.Contains(strings.Join(toStrs(jr["next"]), ""), "box_job(") {
		t.Fatalf("async next: %+v", jr)
	}
	var job model.Job
	e.must(ag, "GET", "/jobs/"+jr["job_id"].(string)+"?wait=10", nil, &job)
	if job.Status != "done" || job.Result.StdoutTail != "lp" {
		t.Fatalf("long-poll: %+v", job)
	}

	// Snapshot → modify → restore.
	e.must(ag, "PUT", "/boxes/"+id+"/files", map[string]any{"path": "/work/state.txt", "content": "v1\n"}, nil)
	var sn model.Snapshot
	e.must(ag, "POST", "/boxes/"+id+"/snapshot", nil, &sn)
	e.must(ag, "PUT", "/boxes/"+id+"/files", map[string]any{"path": "/work/state.txt", "content": "v2\n"}, nil)
	e.must(ag, "PUT", "/boxes/"+id+"/files", map[string]any{"path": "/work/junk.txt", "content": "x"}, nil)
	var rb model.Box
	e.must(ag, "POST", "/boxes/"+id+"/restore", map[string]any{}, &rb)
	e.must(ag, "GET", "/boxes/"+id+"/files?path=/work/state.txt", nil, &f)
	if f["content"] != "v1\n" {
		t.Fatalf("restore: %+v", f)
	}
	if err := ag.JSON(e.ctx, "GET", "/boxes/"+id+"/files?path=/work/junk.txt", nil, &f); err == nil {
		t.Fatal("restore kept a file created after the snapshot")
	}

	// Fork from that snapshot.
	var fork map[string]any
	e.must(ag, "POST", "/boxes/"+id+"/fork", map[string]any{"snapshot_id": sn.ID, "slug": "v11-alt"}, &fork)
	fid := fork["id"].(string)
	if fork["status"] != "running" || fid == id || fork["forked_from"] != sn.ID || fork["slug"] != "v11-alt" {
		t.Fatalf("fork: %+v", fork)
	}
	e.must(ag, "GET", "/boxes/"+fid+"/files?path=/work/state.txt", nil, &f)
	if f["content"] != "v1\n" {
		t.Fatalf("fork content: %+v", f)
	}
	e.must(ag, "GET", "/boxes/"+fid+"/files?path=/AGENTS.md", nil, &f)
	if !strings.Contains(f["content"].(string), sn.ID) || !strings.Contains(f["content"].(string), fid) {
		t.Fatalf("fork AGENTS.md: %.300s", f["content"])
	}
	e.must(ag, "GET", "/boxes/"+fid+"/yaml", nil, &y)
	if y["yaml"] != yml {
		t.Fatalf("fork yaml: %+v", y)
	}
	// Forked box is independent.
	e.must(ag, "PUT", "/boxes/"+fid+"/files", map[string]any{"path": "/work/state.txt", "content": "fork\n"}, nil)
	e.must(ag, "GET", "/boxes/"+id+"/files?path=/work/state.txt", nil, &f)
	if f["content"] != "v1\n" {
		t.Fatal("fork shares files with source")
	}
	// Fork without snapshot_id uses the latest snapshot; restore of a foreign-host/unknown snapshot fails.
	if err := ag.JSON(e.ctx, "POST", "/boxes/"+id+"/restore", map[string]any{"snapshot_id": "snap_nope"}, nil); err == nil {
		t.Fatal("unknown snapshot accepted")
	}

	// Generated yaml for JSON-created boxes.
	var b2 model.Box
	e.must(ag, "POST", "/boxes", map[string]any{"template": "node", "lifecycle": "task", "ttl_seconds": 600, "spec": map[string]any{"ram_mb": 256}}, &b2)
	e.must(ag, "GET", "/boxes/"+b2.ID+"/yaml", nil, &y)
	if y["source"] != "generated" || !strings.Contains(y["yaml"].(string), "template: node") || !strings.Contains(y["yaml"].(string), "ttl_seconds: 600") {
		t.Fatalf("generated yaml: %+v", y)
	}

	// Snippets for all 22 kinds via API (lang=ja comments).
	var kinds struct{ Items []struct{ ID string } }
	e.must(ag, "GET", "/agent-kinds", nil, &kinds)
	for _, k := range kinds.Items {
		var sn map[string]any
		e.must(e.admin, "GET", "/agents/"+agentID+"/snippet?kind="+k.ID+"&lang=ja", nil, &sn)
		if sn["kind"] != k.ID || sn["snippet"] == "" || sn["locale"] != "ja" {
			t.Fatalf("snippet %s: %+v", k.ID, sn)
		}
	}
	var sn2 map[string]any
	e.must(e.admin, "GET", "/agents/"+agentID+"/snippet", nil, &sn2)
	if sn2["kind"] != "langgraph" || sn2["locale"] != "vi" || !strings.Contains(sn2["snippet"].(string), "Chạy agent") {
		t.Fatalf("default snippet: %+v", sn2)
	}

	// MCP over HTTP: 21 tools, localized prompt, resources.
	mcpCall := func(lang, body string) map[string]any {
		req, _ := http.NewRequestWithContext(context.Background(), "POST", e.srv.URL+"/mcp", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+ag.Key)
		if lang != "" {
			req.Header.Set("Accept-Language", lang)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var m map[string]any
		json.NewDecoder(resp.Body).Decode(&m)
		return m
	}
	m := mcpCall("", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if len(m["result"].(map[string]any)["tools"].([]any)) != 26 {
		t.Fatalf("mcp tools: %+v", m)
	}
	m = mcpCall("zh", `{"jsonrpc":"2.0","id":2,"method":"prompts/get","params":{"name":"cost-review"}}`)
	if b, _ := json.Marshal(m); !strings.Contains(string(b), "检查我的 Boxaz 花费") {
		t.Fatalf("mcp prompt zh: %s", b)
	}
	m = mcpCall("", `{"jsonrpc":"2.0","id":3,"method":"resources/read","params":{"uri":"boxaz://box/`+fid+`/file/work/state.txt"}}`)
	if b, _ := json.Marshal(m); !strings.Contains(string(b), `"text":"fork\n"`) {
		t.Fatalf("mcp resource: %s", b)
	}
	m = mcpCall("vi", `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"box_exec","arguments":{"box_id":"box_nope","command":"ls"}}}`)
	if b, _ := json.Marshal(m); !strings.Contains(string(b), "không tìm thấy") {
		t.Fatalf("mcp error vi: %s", b)
	}
	m = mcpCall("", `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"box_exec_batch","arguments":{"box_id":"`+id+`","commands":["echo a","echo b"]}}}`)
	if sc := m["result"].(map[string]any)["structuredContent"].(map[string]any); sc["ok"] != true || sc["completed"].(float64) != 2 {
		t.Fatalf("mcp batch: %+v", m)
	}
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

func toStrs(v any) []string {
	var out []string
	if a, ok := v.([]any); ok {
		for _, x := range a {
			out = append(out, x.(string))
		}
	}
	return out
}
