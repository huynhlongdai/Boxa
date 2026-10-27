// Package mcp implements the Boxaz MCP server (Model Context Protocol,
// JSON-RPC 2.0): the box_* tools (tools.go), resources (box files, logs,
// AGENTS.md via boxaz:// URIs) and prompts (resources.go). The same server runs over
// Streamable HTTP (core /mcp, JSON responses) and stdio (`box mcp`).
// Tools are thin wrappers that call the REST API through a Caller, so
// auth, scopes and behaviour are identical to REST.
//
// Implemented directly on the spec (protocol 2025-06-18, backwards compatible
// with 2025-03-26/2024-11-05) instead of an SDK: zero dependencies and one
// code path for both transports.
package mcp

import (
	"boxaz/core/internal/i18n"
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
		body := specBody(a)
		if k := a.s("idempotency_key"); k != "" {
			body["_idempotency_key"] = k
		}
		return c.Do(ctx, "POST", "/boxes", body)
	case "box_quote":
		return c.Do(ctx, "POST", "/quote", specBody(a))
	case "box_ensure":
		if err := need("name"); err != nil {
			return 0, nil, err
		}
		body := specBody(a)
		body["name"] = a.s("name")
		return c.Do(ctx, "POST", "/boxes/ensure", body)
	case "box_recommend":
		body := map[string]any{"requirements": needsBody(a, true)}
		for _, k := range []string{"profile", "template", "lifecycle"} {
			if a.s(k) != "" {
				body[k] = a.s(k)
			}
		}
		return c.Do(ctx, "POST", "/recommend", body)
	case "box_clone":
		if err := need("box_id"); err != nil {
			return 0, nil, err
		}
		body := map[string]any{}
		if a.s("slug") != "" {
			body["slug"] = a.s("slug")
		}
		return c.Do(ctx, "POST", "/boxes/"+esc(a.s("box_id"))+"/clone", body)
	case "box_exec_batch":
		if err := need("box_id"); err != nil {
			return 0, nil, err
		}
		cmds, _ := a["commands"].([]any)
		if len(cmds) == 0 {
			return 0, nil, fmt.Errorf("missing required argument %q", "commands")
		}
		body := map[string]any{"commands": cmds}
		if a.has("stop_on_error") {
			body["stop_on_error"] = a.b("stop_on_error")
		}
		if a.s("cwd") != "" {
			body["cwd"] = a.s("cwd")
		}
		for _, k := range []string{"timeout_s", "tail_lines"} {
			if a.has(k) {
				body[k] = int(a.num(k))
			}
		}
		return c.Do(ctx, "POST", "/boxes/"+esc(a.s("box_id"))+"/exec-batch", body)
	case "box_fork", "box_restore":
		if err := need("box_id"); err != nil {
			return 0, nil, err
		}
		body := map[string]any{}
		if a.s("snapshot_id") != "" {
			body["snapshot_id"] = a.s("snapshot_id")
		}
		if a.s("slug") != "" && name == "box_fork" {
			body["slug"] = a.s("slug")
		}
		verb := strings.TrimPrefix(name, "box_")
		return c.Do(ctx, "POST", "/boxes/"+esc(a.s("box_id"))+"/"+verb, body)
	case "box_logs":
		return toolLogs(ctx, c, a)
	case "box_templates":
		st, b, err := c.Do(ctx, "GET", "/templates", nil)
		if err != nil || st >= 300 {
			return st, b, err
		}
		var l struct {
			Items []map[string]any `json:"items"`
		}
		json.Unmarshal(b, &l)
		out := []map[string]any{}
		for _, t := range l.Items {
			m := map[string]any{"name": t["name"], "description": t["description"], "builtin": t["builtin"] == true}
			if a.b("include_yaml") {
				m["yaml"] = t["yaml"]
			}
			out = append(out, m)
		}
		js, _ := json.Marshal(map[string]any{"items": out})
		return st, js, nil
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
		if a.b("session") {
			body["session"] = true
		}
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
	case "box_url":
		if err := need("box_id"); err != nil {
			return 0, nil, err
		}
		if a.has("port") {
			return c.Do(ctx, "POST", "/boxes/"+esc(a.s("box_id"))+"/ports", map[string]any{"port": int(a.num("port"))})
		}
		return c.Do(ctx, "GET", "/boxes/"+esc(a.s("box_id"))+"/ports", nil)
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

func specBody(a args) map[string]any {
	body := map[string]any{}
	if a.s("yaml") != "" {
		body["yaml"] = a.s("yaml")
		return body
	}
	for _, k := range []string{"template", "lifecycle", "slug", "region"} {
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
	if a.s("profile") != "" {
		body["profile"] = a.s("profile")
	}
	if rq := needsBody(a, false); len(rq) > 0 {
		body["requirements"] = rq
	}
	if a.has("egress_allow") || a.has("egress_deny_by_default") {
		eg := map[string]any{"allow": []any{}}
		if v, ok := a["egress_allow"].([]any); ok {
			eg["allow"] = v
		}
		if a.has("egress_deny_by_default") {
			eg["deny_by_default"] = a.b("egress_deny_by_default")
		}
		body["egress"] = eg
	}
	return body
}

// toolLogs: full log by id (tail/grep filtered) or the recent exec list of a box.
func toolLogs(ctx context.Context, c Caller, a args) (int, []byte, error) {
	if id := a.s("log_id"); id != "" {
		st, b, err := c.Do(ctx, "GET", "/logs/"+esc(id), nil)
		if err != nil || st >= 300 {
			return st, b, err
		}
		lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
		total := len(lines)
		if g := a.s("grep"); g != "" {
			var keep []string
			for _, l := range lines {
				if strings.Contains(l, g) {
					keep = append(keep, l)
				}
			}
			lines = keep
		}
		if n := int(a.num("tail_lines")); n > 0 && len(lines) > n {
			lines = lines[len(lines)-n:]
		}
		content := strings.Join(lines, "\n")
		const max = 200 << 10
		truncated := false
		if len(content) > max {
			content, truncated = content[len(content)-max:], true
		}
		out, _ := json.Marshal(map[string]any{"log_id": id, "total_lines": total, "returned_lines": len(lines), "truncated": truncated, "content": content})
		return st, out, nil
	}
	if a.s("box_id") == "" {
		return 0, nil, fmt.Errorf("pass log_id (from an exec result) or box_id")
	}
	st, b, err := c.Do(ctx, "GET", "/boxes/"+esc(a.s("box_id"))+"/events", nil)
	if err != nil || st >= 300 {
		return st, b, err
	}
	var l struct {
		Items []struct {
			TS      string         `json:"ts"`
			Type    string         `json:"type"`
			Message string         `json:"message"`
			Data    map[string]any `json:"data"`
		} `json:"items"`
	}
	json.Unmarshal(b, &l)
	out := []map[string]any{}
	for _, e := range l.Items {
		if e.Type != "box.exec" {
			continue
		}
		m := map[string]any{"ts": e.TS, "message": e.Message}
		for k, v := range e.Data {
			m[k] = v
		}
		out = append(out, m)
		if len(out) >= 30 {
			break
		}
	}
	js, _ := json.Marshal(map[string]any{"box_id": a.s("box_id"), "execs": out})
	return st, js, nil
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
			"capabilities": map[string]any{
				"tools":     map[string]any{"listChanged": false},
				"resources": map[string]any{"subscribe": false, "listChanged": false},
				"prompts":   map[string]any{"listChanged": false},
			},
			"serverInfo":   map[string]any{"name": s.Name, "title": "Boxaz", "version": s.Version},
			"instructions": i18n.T(i18n.FromContext(ctx), "mcp.instructions"),
		}
	case "notifications/initialized", "notifications/cancelled", "initialized":
		return nil
	case "ping":
		resp.Result = map[string]any{}
	case "tools/list":
		resp.Result = map[string]any{"tools": Tools}
	case "resources/list":
		resp.Result = listResources(ctx, c)
	case "resources/templates/list":
		resp.Result = map[string]any{"resourceTemplates": ResourceTemplates}
	case "resources/read":
		var p struct {
			URI string `json:"uri"`
		}
		json.Unmarshal(req.Params, &p)
		r, err := readResource(ctx, c, p.URI)
		if err != nil {
			resp.Error = &rpcErr{Code: -32002, Message: err.Error()}
			break
		}
		resp.Result = r
	case "prompts/list":
		resp.Result = map[string]any{"prompts": listPrompts(i18n.FromContext(ctx))}
	case "prompts/get":
		var p struct {
			Name      string            `json:"name"`
			Arguments map[string]string `json:"arguments"`
		}
		json.Unmarshal(req.Params, &p)
		r, err := getPrompt(i18n.FromContext(ctx), p.Name, p.Arguments)
		if err != nil {
			resp.Error = &rpcErr{Code: -32602, Message: err.Error()}
			break
		}
		resp.Result = r
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
			eb, _ := json.Marshal(map[string]any{"error": map[string]any{"code": "INVALID", "message": err.Error(), "hint": i18n.T(i18n.FromContext(ctx), "mcp.check_args")}})
			resp.Result = toolResult(string(eb), true)
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
	r = r.WithContext(i18n.WithLocale(r.Context(), i18n.FromRequest(r, i18n.Default)))
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

// needsBody collects requirement fields (withSpec adds cpu/ram/disk/region).
func needsBody(a args, withSpec bool) map[string]any {
	rq := map[string]any{}
	for _, k := range []string{"location", "optimize", "isolation", "arch", "lifetime"} {
		if a.s(k) != "" {
			rq[k] = a.s(k)
		}
	}
	for _, k := range []string{"max_price_hour", "max_latency_ms", "duration_hours"} {
		if a.has(k) {
			rq[k] = a.num(k)
		}
	}
	for _, k := range []string{"untrusted", "fork"} {
		if v, ok := a[k].(bool); ok && v {
			rq[k] = true
		}
	}
	if withSpec {
		if a.has("cpu") {
			rq["cpu"] = a.num("cpu")
		}
		for _, k := range []string{"ram_mb", "disk_gb"} {
			if a.has(k) {
				rq[k] = int(a.num(k))
			}
		}
		if a.s("region") != "" {
			rq["region"] = a.s("region")
		}
	}
	return rq
}
