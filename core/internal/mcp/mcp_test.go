package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"boxa/core/internal/i18n"
	"boxa/core/internal/manager"
)

type fakeCaller struct{ last string }

func (f *fakeCaller) Do(ctx context.Context, method, path string, body any) (int, []byte, error) {
	b, _ := json.Marshal(body)
	f.last = method + " " + path + " " + string(b)
	return 200, []byte(`{"exit_code":0,"stdout_tail":"hi"}`), nil
}

func TestStdio(t *testing.T) {
	s := &Server{Name: "boxa", Version: "t"}
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}
{"jsonrpc":"2.0","method":"notifications/initialized"}
{"jsonrpc":"2.0","id":2,"method":"tools/list"}
`)
	var out bytes.Buffer
	if err := s.ServeStdio(context.Background(), &fakeCaller{}, in, &out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 responses (notification has none), got %d: %s", len(lines), out.String())
	}
	if !strings.Contains(out.String(), `"protocolVersion":"2025-03-26"`) || !strings.Contains(out.String(), "box_cost") {
		t.Fatal(out.String())
	}
}

func TestToolMapping(t *testing.T) {
	s := &Server{Name: "boxa"}
	fc := &fakeCaller{}
	r := s.Handle(context.Background(), fc, []byte(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"box_exec","arguments":{"box_id":"b1","command":"ls","async":true}}}`))
	if r == nil || r.Error != nil {
		t.Fatalf("%+v", r)
	}
	if !strings.HasPrefix(fc.last, "POST /boxes/b1/exec ") || !strings.Contains(fc.last, `"async":true`) {
		t.Fatal(fc.last)
	}
	r = s.Handle(context.Background(), fc, []byte(`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"box_read","arguments":{"box_id":"b1","path":"/work/a b","from":2}}}`))
	if !strings.Contains(fc.last, "GET /boxes/b1/files?from=2&path=%2Fwork%2Fa+b") {
		t.Fatal(fc.last)
	}
	r = s.Handle(context.Background(), fc, []byte(`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"box_exec","arguments":{}}}`))
	res := r.Result.(map[string]any)
	if res["isError"] != true {
		t.Fatalf("missing args should be a tool error: %+v", res)
	}
	r = s.Handle(context.Background(), fc, []byte(`{"jsonrpc":"2.0","id":6,"method":"nope"}`))
	if r.Error == nil || r.Error.Code != -32601 {
		t.Fatalf("%+v", r)
	}
	if len(Tools) != 21 {
		t.Fatalf("contract v1.3 requires 21 tools, have %d", len(Tools))
	}
}

func TestToolsSingleSource(t *testing.T) {
	if strings.Join(ToolNames(), ",") != strings.Join(manager.ToolNames, ",") {
		t.Fatalf("manager.ToolNames (AGENTS.md) out of sync:\n%v\n%v", ToolNames(), manager.ToolNames)
	}
	for _, f := range Formats {
		v, ok := ToolsAs(f)
		if !ok {
			t.Fatal(f)
		}
		b, _ := json.Marshal(v)
		for _, tl := range Tools {
			if !strings.Contains(string(b), `"`+tl.Name+`"`) {
				t.Fatalf("%s lacks %s", f, tl.Name)
			}
		}
		if f == "gemini" && strings.Contains(string(b), "additionalProperties") {
			t.Fatal("gemini schema must not contain additionalProperties")
		}
	}
	if _, ok := ToolsAs("nope"); ok {
		t.Fatal("unknown format accepted")
	}
}

type recCaller struct{ calls []string }

func (f *recCaller) Do(ctx context.Context, method, path string, body any) (int, []byte, error) {
	b, _ := json.Marshal(body)
	f.calls = append(f.calls, method+" "+path+" "+string(b))
	switch {
	case strings.HasPrefix(path, "/logs/"):
		return 200, []byte("l1\nerror: boom\nl3\nl4\n"), nil
	case strings.HasPrefix(path, "/boxes?") || path == "/boxes":
		return 200, []byte(`{"items":[{"id":"box_1","slug":"demo","status":"running","template":"python"}]}`), nil
	case strings.Contains(path, "/files?"):
		return 200, []byte(`{"type":"file","content":"print(1)"}`), nil
	case strings.HasSuffix(path, "/events"):
		return 200, []byte(`{"items":[{"type":"box.exec","message":"exit=1","data":{"exit_code":1,"log_id":"log_9","tokens_saved":12}},{"type":"box.running"}]}`), nil
	case path == "/templates":
		return 200, []byte(`{"items":[{"name":"python","description":"Python 3","builtin":true,"yaml":"x"}]}`), nil
	}
	return 200, []byte(`{}`), nil
}

func call(t *testing.T, s *Server, c Caller, method, params string) map[string]any {
	t.Helper()
	r := s.Handle(context.Background(), c, []byte(`{"jsonrpc":"2.0","id":1,"method":"`+method+`","params":`+params+`}`))
	if r == nil || r.Error != nil {
		t.Fatalf("%s: %+v", method, r)
	}
	b, _ := json.Marshal(r.Result)
	var m map[string]any
	json.Unmarshal(b, &m)
	return m
}

func TestV11Tools(t *testing.T) {
	s := &Server{Name: "boxa"}
	c := &recCaller{}
	call(t, s, c, "tools/call", `{"name":"box_exec_batch","arguments":{"box_id":"b1","commands":["ls","pwd"],"stop_on_error":false}}`)
	call(t, s, c, "tools/call", `{"name":"box_quote","arguments":{"template":"python","ram_mb":2048}}`)
	call(t, s, c, "tools/call", `{"name":"box_fork","arguments":{"box_id":"b1","slug":"alt"}}`)
	call(t, s, c, "tools/call", `{"name":"box_restore","arguments":{"box_id":"b1","snapshot_id":"snap_1"}}`)
	want := []string{
		`POST /boxes/b1/exec-batch {"commands":["ls","pwd"],"stop_on_error":false}`,
		`POST /quote {"spec":{"ram_mb":2048},"template":"python"}`,
		`POST /boxes/b1/fork {"slug":"alt"}`,
		`POST /boxes/b1/restore {"snapshot_id":"snap_1"}`,
	}
	for i, w := range want {
		if c.calls[i] != w {
			t.Fatalf("call %d: %s\nwant %s", i, c.calls[i], w)
		}
	}
	m := call(t, s, c, "tools/call", `{"name":"box_logs","arguments":{"log_id":"log_1","grep":"error"}}`)
	sc := m["structuredContent"].(map[string]any)
	if sc["content"] != "error: boom" || sc["total_lines"].(float64) != 4 {
		t.Fatalf("%+v", sc)
	}
	m = call(t, s, c, "tools/call", `{"name":"box_logs","arguments":{"box_id":"b1"}}`)
	execs := m["structuredContent"].(map[string]any)["execs"].([]any)
	if len(execs) != 1 || execs[0].(map[string]any)["log_id"] != "log_9" {
		t.Fatalf("%+v", m)
	}
	m = call(t, s, c, "tools/call", `{"name":"box_templates","arguments":{}}`)
	if strings.Contains(m["content"].([]any)[0].(map[string]any)["text"].(string), `"yaml"`) {
		t.Fatal("yaml included without include_yaml")
	}
}

func TestResourcesAndPrompts(t *testing.T) {
	s := &Server{Name: "boxa"}
	c := &recCaller{}
	m := call(t, s, c, "initialize", `{"protocolVersion":"2025-06-18"}`)
	caps := m["capabilities"].(map[string]any)
	if caps["resources"] == nil || caps["prompts"] == nil {
		t.Fatalf("%+v", caps)
	}
	m = call(t, s, c, "resources/list", `{}`)
	if len(m["resources"].([]any)) != 3 {
		t.Fatalf("%+v", m)
	}
	m = call(t, s, c, "resources/templates/list", `{}`)
	if len(m["resourceTemplates"].([]any)) != 4 {
		t.Fatal(m)
	}
	m = call(t, s, c, "resources/read", `{"uri":"boxa://box/box_1/file/work/a%20b.py"}`)
	if m["contents"].([]any)[0].(map[string]any)["text"] != "print(1)" || !strings.Contains(c.calls[len(c.calls)-1], "path=%2Fwork%2Fa+b.py") {
		t.Fatalf("%+v %v", m, c.calls)
	}
	m = call(t, s, c, "resources/read", `{"uri":"boxa://log/log_1"}`)
	if !strings.Contains(m["contents"].([]any)[0].(map[string]any)["text"].(string), "boom") {
		t.Fatal(m)
	}
	r := s.Handle(context.Background(), c, []byte(`{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{"uri":"http://x"}}`))
	if r.Error == nil {
		t.Fatal("bad uri accepted")
	}
	m = call(t, s, c, "prompts/list", `{}`)
	if len(m["prompts"].([]any)) != 3 {
		t.Fatal(m)
	}
	ctx := i18n.WithLocale(context.Background(), "vi")
	r = s.Handle(ctx, c, []byte(`{"jsonrpc":"2.0","id":1,"method":"prompts/get","params":{"name":"debug-failing-command","arguments":{"box_id":"box_1","command":"pytest"}}}`))
	b, _ := json.Marshal(r.Result)
	if !strings.Contains(string(b), "box_1") || !strings.Contains(string(b), "pytest") || !strings.Contains(string(b), "gỡ lỗi") {
		t.Fatal(string(b))
	}
	r = s.Handle(ctx, c, []byte(`{"jsonrpc":"2.0","id":1,"method":"prompts/get","params":{"name":"debug-failing-command","arguments":{}}}`))
	if r.Error == nil {
		t.Fatal("missing args accepted")
	}
}
