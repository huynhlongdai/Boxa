package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
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
	if len(Tools) != 12 {
		t.Fatalf("contract requires 12 tools, have %d", len(Tools))
	}
}
