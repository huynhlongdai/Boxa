package api

// A2A (Agent2Agent) JSON-RPC task API at POST /a2a, mapped onto Boxmes:
//
//   message/send   (alias tasks/send)          → run a command (async job) or a tool
//   message/stream (alias tasks/sendSubscribe) → same, streamed as SSE
//   tasks/get, tasks/cancel, tasks/resubscribe
//
// A message is turned into work like this:
//   - a data part {"tool": "box_up", "arguments": {...}} calls that Boxmes tool
//     (any of /v1/tools) and completes immediately with its JSON result;
//   - a data part {"box_id": "...", "command": "...", "cwd"?, "timeout_s"?}, or
//     a text part (the command) with metadata.box_id, starts an async exec
//     job: the task id IS the job id, so tasks/get and GET /v1/jobs/{id} agree.
// Auth is the same Bearer API key as REST/MCP; an agent only sees its tasks.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"boxmes/core/internal/manager"
	"boxmes/core/internal/mcp"
	"boxmes/core/internal/model"
	"boxmes/core/internal/secret"
)

const (
	a2aParseError     = -32700
	a2aInvalidRequest = -32600
	a2aMethodNotFound = -32601
	a2aInvalidParams  = -32602
	a2aInternal       = -32603
	a2aTaskNotFound   = -32001
	a2aNotCancelable  = -32002
)

type a2aReq struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type a2aErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type a2aPart struct {
	Kind string         `json:"kind,omitempty"`
	Type string         `json:"type,omitempty"` // A2A ≤0.2 used "type"
	Text string         `json:"text,omitempty"`
	Data map[string]any `json:"data,omitempty"`
}

func (p a2aPart) kind() string {
	if p.Kind != "" {
		return p.Kind
	}
	return p.Type
}

type a2aMessage struct {
	Kind      string         `json:"kind,omitempty"`
	Role      string         `json:"role"`
	Parts     []a2aPart      `json:"parts"`
	MessageID string         `json:"messageId,omitempty"`
	ContextID string         `json:"contextId,omitempty"`
	TaskID    string         `json:"taskId,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

type a2aSendParams struct {
	ID            string         `json:"id,omitempty"`        // tasks/send (≤0.2)
	SessionID     string         `json:"sessionId,omitempty"` // tasks/send (≤0.2)
	Message       a2aMessage     `json:"message"`
	Metadata      map[string]any `json:"metadata,omitempty"`
	Configuration struct {
		Blocking *bool `json:"blocking,omitempty"`
	} `json:"configuration"`
}

type a2aTaskRec struct {
	ID        string          `json:"id"`
	ContextID string          `json:"context_id"`
	Kind      string          `json:"kind"` // exec | tool
	AgentID   string          `json:"agent_id"`
	Workspace string          `json:"workspace_id,omitempty"` // v2.0 (pre-v2.0 tasks: default)
	BoxID     string          `json:"box_id,omitempty"`
	JobID     string          `json:"job_id,omitempty"`
	Tool      string          `json:"tool,omitempty"`
	State     string          `json:"state"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     string          `json:"error,omitempty"`
	Message   a2aMessage      `json:"message"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

func a2aKey(id string) string { return "a2a_task:" + id }

func (s *Server) a2a(w http.ResponseWriter, r *http.Request) {
	var req a2aReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&req); err != nil {
		writeRPC(w, nil, nil, &a2aErr{Code: a2aParseError, Message: "invalid JSON"})
		return
	}
	if req.JSONRPC != "2.0" || req.Method == "" {
		writeRPC(w, req.ID, nil, &a2aErr{Code: a2aInvalidRequest, Message: "jsonrpc must be \"2.0\" with a method"})
		return
	}
	switch req.Method {
	case "message/send", "tasks/send":
		task, e := s.a2aSend(r, req)
		if e != nil {
			writeRPC(w, req.ID, nil, e)
			return
		}
		writeRPC(w, req.ID, task, nil)
	case "message/stream", "tasks/sendSubscribe":
		var p a2aSendParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			writeRPC(w, req.ID, nil, &a2aErr{Code: a2aInvalidParams, Message: err.Error()})
			return
		}
		f := false
		p.Configuration.Blocking = &f
		rec, e := s.a2aStart(r, p)
		if e != nil {
			writeRPC(w, req.ID, nil, e)
			return
		}
		s.a2aStream(w, r, req.ID, rec, req.Method == "tasks/sendSubscribe")
	case "tasks/resubscribe":
		rec, e := s.a2aLoad(r, req.Params)
		if e != nil {
			writeRPC(w, req.ID, nil, e)
			return
		}
		s.a2aStream(w, r, req.ID, rec, false)
	case "tasks/get":
		rec, e := s.a2aLoad(r, req.Params)
		if e != nil {
			writeRPC(w, req.ID, nil, e)
			return
		}
		writeRPC(w, req.ID, s.a2aTask(r.Context(), rec), nil)
	case "tasks/cancel":
		rec, e := s.a2aLoad(r, req.Params)
		if e != nil {
			writeRPC(w, req.ID, nil, e)
			return
		}
		if rec.Kind != "exec" || isFinal(rec.State) {
			writeRPC(w, req.ID, nil, &a2aErr{Code: a2aNotCancelable, Message: "task is not cancelable (state " + rec.State + ")"})
			return
		}
		if _, err := s.M.CancelJob(r.Context(), pr(r), rec.JobID); err != nil {
			writeRPC(w, req.ID, nil, &a2aErr{Code: a2aInternal, Message: localizeErr(r, model.AsAPIError(err)).Message})
			return
		}
		rec = s.a2aRefresh(r.Context(), rec)
		writeRPC(w, req.ID, s.a2aTask(r.Context(), rec), nil)
	case "tasks/pushNotificationConfig/set", "tasks/pushNotificationConfig/get", "tasks/pushNotification/set", "tasks/pushNotification/get":
		writeRPC(w, req.ID, nil, &a2aErr{Code: -32003, Message: "push notifications are not supported; use message/stream or tasks/get"})
	default:
		writeRPC(w, req.ID, nil, &a2aErr{Code: a2aMethodNotFound, Message: "method not found: " + req.Method})
	}
}

func writeRPC(w http.ResponseWriter, id json.RawMessage, result any, e *a2aErr) {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	resp := map[string]any{"jsonrpc": "2.0", "id": id}
	if e != nil {
		resp["error"] = e
	} else {
		resp["result"] = result
	}
	writeJSON(w, 200, resp)
}

func (s *Server) a2aSend(r *http.Request, req a2aReq) (any, *a2aErr) {
	var p a2aSendParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return nil, &a2aErr{Code: a2aInvalidParams, Message: err.Error()}
	}
	rec, e := s.a2aStart(r, p)
	if e != nil {
		return nil, e
	}
	// Blocking by default: wait up to 30s for short commands.
	if rec.Kind == "exec" && (p.Configuration.Blocking == nil || *p.Configuration.Blocking) {
		deadline := time.Now().Add(30 * time.Second)
		for !isFinal(rec.State) && time.Now().Before(deadline) {
			select {
			case <-r.Context().Done():
				return s.a2aTask(r.Context(), rec), nil
			case <-time.After(200 * time.Millisecond):
			}
			rec = s.a2aRefresh(r.Context(), rec)
		}
	}
	return s.a2aTask(r.Context(), rec), nil
}

// a2aStart validates the message and starts the work.
func (s *Server) a2aStart(r *http.Request, p a2aSendParams) (*a2aTaskRec, *a2aErr) {
	ctx := r.Context()
	msg := p.Message
	if len(msg.Parts) == 0 {
		return nil, &a2aErr{Code: a2aInvalidParams, Message: "message.parts is empty"}
	}
	meta := map[string]any{}
	for k, v := range p.Metadata {
		meta[k] = v
	}
	for k, v := range msg.Metadata {
		meta[k] = v
	}
	ctxID := msg.ContextID
	if ctxID == "" {
		ctxID = p.SessionID
	}
	if ctxID == "" {
		ctxID = secret.ID("ctx")
	}
	now := time.Now().UTC()
	rec := &a2aTaskRec{ContextID: ctxID, AgentID: pr(r).AgentID, Message: msg, CreatedAt: now, UpdatedAt: now}
	rec.Workspace, _ = pr(r).TargetWS()
	var command, boxID, cwd string
	timeout := 0
	if v, ok := meta["box_id"].(string); ok {
		boxID = v
	}
	if v, ok := meta["cwd"].(string); ok {
		cwd = v
	}
	if v, ok := meta["timeout_s"].(float64); ok {
		timeout = int(v)
	}
	for _, part := range msg.Parts {
		switch part.kind() {
		case "data":
			if tool, ok := part.Data["tool"].(string); ok && tool != "" {
				argsM, _ := part.Data["arguments"].(map[string]any)
				return s.a2aTool(r, rec, tool, argsM)
			}
			if v, ok := part.Data["box_id"].(string); ok {
				boxID = v
			}
			if v, ok := part.Data["command"].(string); ok {
				command = v
			}
			if v, ok := part.Data["cwd"].(string); ok {
				cwd = v
			}
			if v, ok := part.Data["timeout_s"].(float64); ok {
				timeout = int(v)
			}
		case "text":
			if command == "" {
				command = strings.TrimSpace(part.Text)
			}
		}
	}
	if command == "" {
		return nil, &a2aErr{Code: a2aInvalidParams, Message: "send a text part with the command (and metadata.box_id), a data part {box_id, command}, or {tool, arguments}"}
	}
	if boxID == "" {
		return nil, &a2aErr{Code: a2aInvalidParams, Message: "metadata.box_id is required for commands (create one with {\"tool\":\"box_up\"})"}
	}
	body := map[string]any{"command": command, "async": true}
	if cwd != "" {
		body["cwd"] = cwd
	}
	if timeout > 0 {
		body["timeout_s"] = timeout
	}
	res, err := s.M.Exec(ctx, pr(r), boxID, execInput(body))
	if err != nil {
		ae := localizeErr(r, model.AsAPIError(err))
		return nil, &a2aErr{Code: a2aInvalidParams, Message: ae.Code + ": " + ae.Message, Data: map[string]any{"code": ae.Code, "hint": ae.Hint}}
	}
	jid := ""
	if m, ok := res.(map[string]string); ok {
		jid = m["job_id"]
	}
	if jid == "" {
		return nil, &a2aErr{Code: a2aInternal, Message: "exec did not start a job"}
	}
	b, _ := s.M.GetBox(ctx, pr(r), boxID)
	rec.ID, rec.Kind, rec.JobID, rec.State = jid, "exec", jid, "submitted"
	if b != nil {
		rec.BoxID = b.ID
	}
	s.M.Store.PutKV(ctx, a2aKey(rec.ID), rec)
	return s.a2aRefresh(ctx, rec), nil
}

func execInput(body map[string]any) (in managerExecInput) {
	b, _ := json.Marshal(body)
	json.Unmarshal(b, &in)
	return in
}

func (s *Server) a2aTool(r *http.Request, rec *a2aTaskRec, tool string, argsM map[string]any) (*a2aTaskRec, *a2aErr) {
	if !mcp.HasTool(tool) {
		return nil, &a2aErr{Code: a2aInvalidParams, Message: "unknown tool " + tool + " (see GET /v1/tools)"}
	}
	c := &inProcCaller{h: s, auth: r.Header.Get("Authorization"), lang: reqLocale(r)}
	if c.auth == "" {
		c.auth = "Bearer " + bearer(r)
	}
	st, body, err := mcp.CallTool(r.Context(), c, tool, argsM)
	rec.ID, rec.Kind, rec.Tool = secret.ID("task"), "tool", tool
	switch {
	case err != nil:
		rec.State, rec.Error = "failed", err.Error()
	case st >= 400:
		rec.State, rec.Result = "failed", json.RawMessage(body)
		var env struct {
			Error struct {
				Code, Message string
			} `json:"error"`
		}
		json.Unmarshal(body, &env)
		rec.Error = env.Error.Code + ": " + env.Error.Message
	default:
		rec.State = "completed"
		if json.Valid(body) {
			rec.Result = json.RawMessage(body)
		} else {
			q, _ := json.Marshal(map[string]string{"text": string(body)})
			rec.Result = q
		}
	}
	rec.UpdatedAt = time.Now().UTC()
	s.M.Store.PutKV(r.Context(), a2aKey(rec.ID), rec)
	return rec, nil
}

func (s *Server) a2aLoad(r *http.Request, raw json.RawMessage) (*a2aTaskRec, *a2aErr) {
	var p struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &p); err != nil || p.ID == "" {
		return nil, &a2aErr{Code: a2aInvalidParams, Message: "params.id is required"}
	}
	var rec a2aTaskRec
	if err := s.M.Store.GetKV(r.Context(), a2aKey(p.ID), &rec); err != nil || rec.ID == "" {
		return nil, &a2aErr{Code: a2aTaskNotFound, Message: "task not found: " + p.ID}
	}
	ws := rec.Workspace
	if ws == "" {
		ws = model.DefaultWorkspaceID
	}
	if !pr(r).Sees(ws, rec.AgentID) {
		return nil, &a2aErr{Code: a2aTaskNotFound, Message: "task not found: " + p.ID}
	}
	return s.a2aRefresh(r.Context(), &rec), nil
}

func jobState(st string) string {
	switch st {
	case "queued":
		return "submitted"
	case "running":
		return "working"
	case "done":
		return "completed"
	case "canceled":
		return "canceled"
	default:
		return "failed"
	}
}

func isFinal(s string) bool {
	return s == "completed" || s == "failed" || s == "canceled" || s == "rejected"
}

// a2aRefresh syncs an exec task with its job.
func (s *Server) a2aRefresh(ctx context.Context, rec *a2aTaskRec) *a2aTaskRec {
	if rec.Kind != "exec" || isFinal(rec.State) {
		return rec
	}
	j, err := s.M.Store.GetJob(ctx, rec.JobID)
	if err != nil {
		return rec
	}
	st := jobState(j.Status)
	if st != rec.State {
		rec.State, rec.UpdatedAt = st, time.Now().UTC()
		if j.Result != nil {
			b, _ := json.Marshal(j.Result)
			rec.Result = b
		}
		s.M.Store.PutKV(ctx, a2aKey(rec.ID), rec)
	}
	return rec
}

func (s *Server) a2aStatus(rec *a2aTaskRec) map[string]any {
	st := map[string]any{"state": rec.State, "timestamp": rec.UpdatedAt.Format(time.RFC3339)}
	if rec.Error != "" {
		st["message"] = map[string]any{"kind": "message", "role": "agent", "messageId": rec.ID + "-err",
			"parts": []any{map[string]any{"kind": "text", "type": "text", "text": rec.Error}}}
	}
	return st
}

func (s *Server) a2aArtifacts(rec *a2aTaskRec) []any {
	if len(rec.Result) == 0 {
		return []any{}
	}
	var data map[string]any
	json.Unmarshal(rec.Result, &data)
	parts := []any{map[string]any{"kind": "data", "type": "data", "data": data}}
	name := "result"
	if rec.Kind == "exec" {
		name = "exec_result"
		var er model.ExecResult
		if json.Unmarshal(rec.Result, &er) == nil {
			txt := er.StdoutTail
			if er.StderrTail != "" {
				txt += "\n[stderr]\n" + er.StderrTail
			}
			parts = append(parts, map[string]any{"kind": "text", "type": "text", "text": fmt.Sprintf("exit %d\n%s", er.ExitCode, txt)})
		}
	} else if rec.Tool != "" {
		name = rec.Tool
	}
	return []any{map[string]any{"artifactId": rec.ID + "-" + name, "name": name, "parts": parts}}
}

// a2aTask renders the A2A Task object.
func (s *Server) a2aTask(ctx context.Context, rec *a2aTaskRec) map[string]any {
	meta := map[string]any{"kind": rec.Kind}
	if rec.BoxID != "" {
		meta["box_id"] = rec.BoxID
	}
	if rec.JobID != "" {
		meta["job_id"] = rec.JobID
	}
	if rec.Tool != "" {
		meta["tool"] = rec.Tool
	}
	hist := rec.Message
	hist.Kind = "message"
	if hist.Role == "" {
		hist.Role = "user"
	}
	return map[string]any{
		"kind": "task", "id": rec.ID, "contextId": rec.ContextID, "sessionId": rec.ContextID,
		"status": s.a2aStatus(rec), "artifacts": s.a2aArtifacts(rec), "history": []any{hist}, "metadata": meta,
	}
}

// a2aStream emits the task then status/artifact updates over SSE until final.
func (s *Server) a2aStream(w http.ResponseWriter, r *http.Request, id json.RawMessage, rec *a2aTaskRec, legacy bool) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeRPC(w, id, nil, &a2aErr{Code: a2aInternal, Message: "streaming unsupported"})
		return
	}
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	send := func(result any) {
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
		fmt.Fprintf(w, "data: %s\n\n", b)
		fl.Flush()
	}
	status := func(final bool) map[string]any {
		return map[string]any{"kind": "status-update", "taskId": rec.ID, "contextId": rec.ContextID, "status": s.a2aStatus(rec), "final": final}
	}
	if !legacy {
		send(s.a2aTask(r.Context(), rec))
	} else {
		send(status(false))
	}
	last := rec.State
	tick := time.NewTicker(300 * time.Millisecond)
	defer tick.Stop()
	timeout := time.After(6 * time.Hour)
	for !isFinal(rec.State) {
		select {
		case <-r.Context().Done():
			return
		case <-timeout:
			return
		case <-tick.C:
		}
		rec = s.a2aRefresh(r.Context(), rec)
		if rec.State != last && !isFinal(rec.State) {
			send(status(false))
			last = rec.State
		}
	}
	for _, a := range s.a2aArtifacts(rec) {
		send(map[string]any{"kind": "artifact-update", "taskId": rec.ID, "contextId": rec.ContextID, "artifact": a, "lastChunk": true})
	}
	send(status(true))
}

type managerExecInput = manager.ExecInput
