// Package mcp implements the Boxa MCP server (Model Context Protocol,
// JSON-RPC 2.0) with the 12 box_* tools. The same server runs over
// Streamable HTTP (core /mcp, JSON responses) and stdio (`box mcp`).
// Tools are thin wrappers that call the REST API through a Caller, so
// auth, scopes and behaviour are identical to REST.
//
// Implemented directly on the spec (protocol 2025-06-18, backwards compatible
// with 2025-03-26/2024-11-05) instead of an SDK: zero dependencies and one
// code path for both transports.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
)

const ProtocolVersion = "2025-06-18"

var supportedVersions = map[string]bool{"2025-06-18": true, "2025-03-26": true, "2024-11-05": true}

// Caller performs a REST call against /v1 and returns status + body.
type Caller interface {
	Do(ctx context.Context, method, path string, body any) (int, []byte, error)
}

type Server struct {
	Name    string
	Version string
}

type rpcReq struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResp struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcErr         `json:"error,omitempty"`
}

// Tool definition.
type Tool struct {
	Name        string         `json:"name"`
	Title       string         `json:"title,omitempty"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

func obj(props map[string]any, required ...string) map[string]any {
	s := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func str(desc string) map[string]any  { return map[string]any{"type": "string", "description": desc} }
func num(desc string) map[string]any  { return map[string]any{"type": "number", "description": desc} }
func intg(desc string) map[string]any { return map[string]any{"type": "integer", "description": desc} }
func boolean(desc string) map[string]any {
	return map[string]any{"type": "boolean", "description": desc}
}
func arr(desc, item string) map[string]any {
	return map[string]any{"type": "array", "description": desc, "items": map[string]any{"type": item}}
}

var boxID = str("Box id (box_…) or slug")

// Tools is the fixed tool list (contract: 12 tools).
var Tools = []Tool{
	{Name: "box_up", Title: "Create a box", Description: "Create (or reuse via idempotency_key) a Linux sandbox box and wait until it is running. Returns the Box object with id. Use lifecycle=task for one-off jobs (auto-terminated at TTL), session for interactive work (sleeps when idle, wakes on next call), service for long-running servers.",
		InputSchema: obj(map[string]any{
			"template":        str("base | python | node | python-browser | custom template name"),
			"lifecycle":       map[string]any{"type": "string", "enum": []string{"task", "session", "service"}},
			"cpu":             num("vCPUs (default 1)"),
			"ram_mb":          intg("RAM in MB (default 1024)"),
			"disk_gb":         intg("Disk in GB (default 10)"),
			"ttl_seconds":     intg("Task TTL in seconds"),
			"idle_minutes":    intg("Session idle minutes before sleep"),
			"budget_usd":      num("Max spend for this box"),
			"slug":            str("Readable name"),
			"ports":           arr("Ports to expose over HTTPS", "integer"),
			"secrets":         arr("Secret names to inject as env vars", "string"),
			"setup":           arr("Shell commands run once at creation", "string"),
			"yaml":            str("Full box.yaml instead of the fields above"),
			"idempotency_key": str("Same key returns the same box instead of creating a new one"),
		})},
	{Name: "box_exec", Title: "Run a command", Description: "Run a bash command in the box (cwd /work). Returns exit_code, the last tail_lines (default 60) of stdout/stderr, truncated flag, log_id for full output, files_changed. Set async=true for long commands and poll with box_job. Sleeping boxes are woken automatically.",
		InputSchema: obj(map[string]any{"box_id": boxID, "command": str("bash command"), "cwd": str("working directory (default /work)"), "timeout_s": intg("timeout in seconds (default 300)"), "async": boolean("run in background, returns job_id"), "tail_lines": intg("lines of output to return")}, "box_id", "command")},
	{Name: "box_job", Title: "Get job status", Description: "Get an async exec job (status queued|running|done|failed and its ExecResult). wait_s blocks up to that many seconds for completion.",
		InputSchema: obj(map[string]any{"job_id": str("job id"), "wait_s": intg("seconds to wait for completion (max 60)")}, "job_id")},
	{Name: "box_read", Title: "Read file or directory", Description: "Read a file (optionally a line range from..to, 1-based) or list a directory in the box.",
		InputSchema: obj(map[string]any{"box_id": boxID, "path": str("path, e.g. /work/main.py"), "from": intg("first line"), "to": intg("last line")}, "box_id", "path")},
	{Name: "box_write", Title: "Write file", Description: "Create or overwrite a text file in the box (parent dirs are created).",
		InputSchema: obj(map[string]any{"box_id": boxID, "path": str("path"), "content": str("full file content")}, "box_id", "path", "content")},
	{Name: "box_grep", Title: "Search files", Description: "Regex search (extended regexp) under a path in the box; returns path, line, text (max 500 matches).",
		InputSchema: obj(map[string]any{"box_id": boxID, "pattern": str("regular expression"), "path": str("directory or file (default /work)")}, "box_id", "pattern")},
	{Name: "box_observe", Title: "Observe changes", Description: "What changed since your last observe call: files changed, running processes, listening ports.",
		InputSchema: obj(map[string]any{"box_id": boxID}, "box_id")},
	{Name: "box_snapshot", Title: "Snapshot box", Description: "Snapshot the box filesystem (keeps the latest few).",
		InputSchema: obj(map[string]any{"box_id": boxID}, "box_id")},
	{Name: "box_sleep", Title: "Sleep or wake box", Description: "Put a box to sleep to stop compute billing (files are kept). Pass wake=true to wake it instead (exec also auto-wakes).",
		InputSchema: obj(map[string]any{"box_id": boxID, "wake": boolean("wake instead of sleep")}, "box_id")},
	{Name: "box_down", Title: "Terminate box", Description: "Terminate and delete the box. Its files are gone unless snapshotted.",
		InputSchema: obj(map[string]any{"box_id": boxID}, "box_id")},
	{Name: "box_list", Title: "List boxes", Description: "List your boxes (not terminated). Optional filters.",
		InputSchema: obj(map[string]any{"status": str("filter by status"), "lifecycle": str("filter by lifecycle")})},
	{Name: "box_cost", Title: "Cost", Description: "Spend summary: with box_id returns that box's spent_usd and cost_per_hour; without it returns today/month spend and budgets.",
		InputSchema: obj(map[string]any{"box_id": boxID})},
}

type args map[string]any

func (a args) s(k string) string {
	if v, ok := a[k]; ok && v != nil {
		switch t := v.(type) {
		case string:
			return t
		case float64:
			return strconv.FormatFloat(t, 'f', -1, 64)
		}
		return fmt.Sprint(v)
	}
	return ""
}

func (a args) has(k string) bool { _, ok := a[k]; return ok && a[k] != nil }

func (a args) num(k string) float64 {
	switch t := a[k].(type) {
	case float64:
		return t
	case string:
		f, _ := strconv.ParseFloat(t, 64)
		return f
	}
	return 0
}

func (a args) b(k string) bool {
	switch t := a[k].(type) {
	case bool:
		return t
	case string:
		return t == "true"
	}
	return false
}

func esc(s string) string { return url.PathEscape(s) }

// callTool maps a tool to a REST call.
func callTool(ctx context.Context, c Caller, name string, a args) (int, []byte, error) {
	need := func(keys ...string) error {
		for _, k := range keys {
			if a.s(k) == "" {
				return fmt.Errorf("missing required argument %q", k)
			}
		}
		return nil
	}
	switch name {
	case "box_up":
		body := map[string]any{}
		if a.s("yaml") != "" {
			body["yaml"] = a.s("yaml")
		} else {
			for _, k := range []string{"template", "lifecycle", "slug"} {
				if a.s(k) != "" {
					body[k] = a.s(k)
				}
			}
			spec := map[string]any{}
			if a.has("cpu") {
				spec["cpu"] = a.num("cpu")
			}
			if a.has("ram_mb") {
				spec["ram_mb"] = int(a.num("ram_mb"))
			}
			if a.has("disk_gb") {
				spec["disk_gb"] = int(a.num("disk_gb"))
			}
			body["spec"] = spec
			for _, k := range []string{"ttl_seconds", "idle_minutes"} {
				if a.has(k) {
					body[k] = int(a.num(k))
				}
			}
			if a.has("budget_usd") {
				body["budget_usd"] = a.num("budget_usd")
			}
			for _, k := range []string{"ports", "secrets", "setup"} {
				if a.has(k) {
					body[k] = a[k]
				}
			}
		}
		if k := a.s("idempotency_key"); k != "" {
			body["_idempotency_key"] = k
		}
		return c.Do(ctx, "POST", "/boxes", body)
	case "box_exec":
		if err := need("box_id", "command"); err != nil {
			return 0, nil, err
		}
		body := map[string]any{"command": a.s("command")}
		if a.s("cwd") != "" {
			body["cwd"] = a.s("cwd")
		}
		if a.has("timeout_s") {
			body["timeout_s"] = int(a.num("timeout_s"))
		}
		if a.has("tail_lines") {
			body["tail_lines"] = int(a.num("tail_lines"))
		}
		body["async"] = a.b("async")
		return c.Do(ctx, "POST", "/boxes/"+esc(a.s("box_id"))+"/exec", body)
	case "box_job":
		if err := need("job_id"); err != nil {
			return 0, nil, err
		}
		p := "/jobs/" + esc(a.s("job_id"))
		if w := int(a.num("wait_s")); w > 0 {
			p += "?wait=" + strconv.Itoa(w)
		}
		return c.Do(ctx, "GET", p, nil)
	case "box_read":
		if err := need("box_id"); err != nil {
			return 0, nil, err
		}
		q := url.Values{"path": {a.s("path")}}
		if a.has("from") {
			q.Set("from", strconv.Itoa(int(a.num("from"))))
		}
		if a.has("to") {
			q.Set("to", strconv.Itoa(int(a.num("to"))))
		}
		return c.Do(ctx, "GET", "/boxes/"+esc(a.s("box_id"))+"/files?"+q.Encode(), nil)
	case "box_write":
		if err := need("box_id", "path"); err != nil {
			return 0, nil, err
		}
		return c.Do(ctx, "PUT", "/boxes/"+esc(a.s("box_id"))+"/files", map[string]any{"path": a.s("path"), "content": a.s("content")})
	case "box_grep":
		if err := need("box_id", "pattern"); err != nil {
			return 0, nil, err
		}
		return c.Do(ctx, "POST", "/boxes/"+esc(a.s("box_id"))+"/grep", map[string]any{"pattern": a.s("pattern"), "path": a.s("path")})
	case "box_observe":
		if err := need("box_id"); err != nil {
			return 0, nil, err
		}
		return c.Do(ctx, "GET", "/boxes/"+esc(a.s("box_id"))+"/observe", nil)
	case "box_snapshot":
		if err := need("box_id"); err != nil {
			return 0, nil, err
		}
		return c.Do(ctx, "POST", "/boxes/"+esc(a.s("box_id"))+"/snapshot", map[string]any{})
	case "box_sleep":
		if err := need("box_id"); err != nil {
			return 0, nil, err
		}
		verb := "sleep"
		if a.b("wake") {
			verb = "wake"
		}
		return c.Do(ctx, "POST", "/boxes/"+esc(a.s("box_id"))+"/"+verb, map[string]any{})
	case "box_down":
		if err := need("box_id"); err != nil {
			return 0, nil, err
		}
		return c.Do(ctx, "DELETE", "/boxes/"+esc(a.s("box_id")), nil)
	case "box_list":
		q := url.Values{}
		if a.s("status") != "" {
			q.Set("status", a.s("status"))
		}
		if a.s("lifecycle") != "" {
			q.Set("lifecycle", a.s("lifecycle"))
		}
		return c.Do(ctx, "GET", "/boxes?"+q.Encode(), nil)
	case "box_cost":
		if id := a.s("box_id"); id != "" {
			st, b, err := c.Do(ctx, "GET", "/boxes/"+esc(id), nil)
			if err != nil || st >= 300 {
				return st, b, err
			}
			var box map[string]any
			json.Unmarshal(b, &box)
			out, _ := json.Marshal(map[string]any{"box_id": box["id"], "slug": box["slug"], "status": box["status"], "spent_usd": box["spent_usd"], "cost_per_hour": box["cost_per_hour"], "budget_usd": box["budget_usd"], "provider": box["provider"]})
			return st, out, nil
		}
		st, b, err := c.Do(ctx, "GET", "/overview", nil)
		if err != nil || st >= 300 {
			return st, b, err
		}
		var ov map[string]any
		json.Unmarshal(b, &ov)
		out, _ := json.Marshal(map[string]any{"spent_today": ov["spent_today"], "budget_day": ov["budget_day"], "spent_month": ov["spent_month"], "budget_month": ov["budget_month"], "boxes_running": ov["boxes_running"], "boxes_sleeping": ov["boxes_sleeping"]})
		return st, out, nil
	}
	return 0, nil, fmt.Errorf("unknown tool %q", name)
}

// Handle processes one JSON-RPC message; returns nil for notifications.
func (s *Server) Handle(ctx context.Context, c Caller, raw []byte) *rpcResp {
	var req rpcReq
	if err := json.Unmarshal(raw, &req); err != nil {
		return &rpcResp{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcErr{Code: -32700, Message: "parse error"}}
	}
	isNotif := len(req.ID) == 0 || string(req.ID) == "null"
	resp := &rpcResp{JSONRPC: "2.0", ID: req.ID}
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		json.Unmarshal(req.Params, &p)
		v := ProtocolVersion
		if supportedVersions[p.ProtocolVersion] {
			v = p.ProtocolVersion
		}
		resp.Result = map[string]any{
			"protocolVersion": v,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": s.Name, "version": s.Version},
			"instructions":    "Boxa gives you disposable Linux boxes. Typical flow: box_up → box_write/box_exec → box_down. Output is trimmed to save tokens; read full logs only when needed.",
		}
	case "notifications/initialized", "notifications/cancelled", "initialized":
		return nil
	case "ping":
		resp.Result = map[string]any{}
	case "tools/list":
		resp.Result = map[string]any{"tools": Tools}
	case "tools/call":
		var p struct {
			Name      string `json:"name"`
			Arguments args   `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			resp.Error = &rpcErr{Code: -32602, Message: "invalid params"}
			break
		}
		if p.Arguments == nil {
			p.Arguments = args{}
		}
		known := false
		for _, t := range Tools {
			if t.Name == p.Name {
				known = true
			}
		}
		if !known {
			resp.Error = &rpcErr{Code: -32602, Message: "unknown tool: " + p.Name}
			break
		}
		st, body, err := callTool(ctx, c, p.Name, p.Arguments)
		if err != nil {
			resp.Result = toolResult(fmt.Sprintf(`{"error":{"code":"INVALID","message":%q,"hint":"Check the tool arguments."}}`, err.Error()), true)
			break
		}
		resp.Result = toolResult(string(body), st >= 300)
	default:
		if isNotif {
			return nil
		}
		resp.Error = &rpcErr{Code: -32601, Message: "method not found: " + req.Method}
	}
	if isNotif {
		return nil
	}
	return resp
}

func toolResult(text string, isErr bool) map[string]any {
	r := map[string]any{"content": []map[string]any{{"type": "text", "text": text}}, "isError": isErr}
	var structured map[string]any
	if json.Unmarshal([]byte(text), &structured) == nil {
		r["structuredContent"] = structured
	}
	return r
}

// ServeHTTP implements MCP Streamable HTTP with plain JSON responses.
// CallerFor builds a Caller bound to the incoming request's credentials.
type HTTPHandler struct {
	Server    *Server
	CallerFor func(r *http.Request) Caller
	sessions  sync.Map
}

func (h *HTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
	case http.MethodGet:
		// No server-initiated stream offered.
		w.Header().Set("Allow", "POST, DELETE")
		http.Error(w, "SSE stream not offered; POST JSON-RPC messages", http.StatusMethodNotAllowed)
		return
	case http.MethodDelete:
		if sid := r.Header.Get("Mcp-Session-Id"); sid != "" {
			h.sessions.Delete(sid)
		}
		w.WriteHeader(http.StatusNoContent)
		return
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	if err != nil {
		http.Error(w, "read error", 400)
		return
	}
	c := h.CallerFor(r)
	trim := strings.TrimSpace(string(body))
	w.Header().Set("Content-Type", "application/json")
	if strings.HasPrefix(trim, "[") { // batch (2025-03-26)
		var msgs []json.RawMessage
		if err := json.Unmarshal(body, &msgs); err != nil {
			w.WriteHeader(400)
			json.NewEncoder(w).Encode(rpcResp{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcErr{Code: -32700, Message: "parse error"}})
			return
		}
		var out []*rpcResp
		for _, m := range msgs {
			if r := h.Server.Handle(r.Context(), c, m); r != nil {
				out = append(out, r)
			}
		}
		if len(out) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		json.NewEncoder(w).Encode(out)
		return
	}
	resp := h.Server.Handle(r.Context(), c, body)
	if resp == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if strings.Contains(trim, `"initialize"`) && resp.Error == nil {
		sid := newSessionID()
		h.sessions.Store(sid, true)
		w.Header().Set("Mcp-Session-Id", sid)
	}
	json.NewEncoder(w).Encode(resp)
}

// ServeStdio runs the server over newline-delimited JSON on stdin/stdout.
func (s *Server) ServeStdio(ctx context.Context, c Caller, in io.Reader, out io.Writer) error {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	enc := json.NewEncoder(out)
	var mu sync.Mutex
	var wg sync.WaitGroup
	for sc.Scan() {
		line := append([]byte(nil), sc.Bytes()...)
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if r := s.Handle(ctx, c, line); r != nil {
				mu.Lock()
				enc.Encode(r)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return sc.Err()
}
