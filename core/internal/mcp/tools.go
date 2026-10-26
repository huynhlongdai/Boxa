package mcp

// Tools is the single source of truth for Boxa's tool definitions. The MCP
// server (tools/list), GET /v1/tools?format=openai|anthropic|gemini|mcp, the
// llms-full.txt reference and the SDK helpers are all generated from it.

import "strings"

// Tool definition (MCP shape).
type Tool struct {
	Name        string           `json:"name"`
	Title       string           `json:"title,omitempty"`
	Description string           `json:"description"`
	InputSchema map[string]any   `json:"inputSchema"`
	Annotations *ToolAnnotations `json:"annotations,omitempty"`
	// REST is the endpoint the tool wraps (documentation only).
	REST string `json:"-"`
}

// ToolAnnotations are MCP 2025-06-18 behaviour hints.
type ToolAnnotations struct {
	Title           string `json:"title,omitempty"`
	ReadOnlyHint    bool   `json:"readOnlyHint"`
	DestructiveHint bool   `json:"destructiveHint"`
	IdempotentHint  bool   `json:"idempotentHint"`
	OpenWorldHint   bool   `json:"openWorldHint"`
}

func obj(props map[string]any, required ...string) map[string]any {
	s := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
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
func enum(desc string, vals ...string) map[string]any {
	return map[string]any{"type": "string", "description": desc, "enum": vals}
}

var boxID = str("Box id (box_…) or slug")

func ro() *ToolAnnotations   { return &ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true} }
func rw() *ToolAnnotations   { return &ToolAnnotations{OpenWorldHint: true} }
func idem() *ToolAnnotations { return &ToolAnnotations{IdempotentHint: true} }
func dest() *ToolAnnotations { return &ToolAnnotations{DestructiveHint: true, IdempotentHint: true} }

func specProps(withIdem bool) map[string]any {
	p := map[string]any{
		"template":     str("base | python | node | python-browser | custom template name (see box_templates)"),
		"lifecycle":    enum("task = one-off job, auto-terminated at TTL; session = interactive, sleeps when idle; service = long-running server", "task", "session", "service"),
		"cpu":          num("vCPUs (default 1)"),
		"ram_mb":       intg("RAM in MB (default 1024)"),
		"disk_gb":      intg("Disk in GB (default 10)"),
		"ttl_seconds":  intg("Task TTL in seconds"),
		"idle_minutes": intg("Session idle minutes before sleep"),
		"budget_usd":   num("Max spend for this box in USD"),
		"region":       str("Preferred region (optional)"),
		"slug":         str("Readable name (lowercase, digits, '-')"),
		"ports":        arr("Ports to expose over HTTPS", "integer"),
		"secrets":      arr("Secret names to inject as env vars", "string"),
		"setup":        arr("Shell commands run once at creation", "string"),
		"yaml":         str("Full box.yaml instead of the fields above"),
	}
	if withIdem {
		p["idempotency_key"] = str("Same key returns the same box instead of creating a new one")
	}
	return p
}

// Tools is the tool list (contract v1 12 tools + v1.1 additions).
var Tools = []Tool{
	{Name: "box_up", Title: "Create a box", REST: "POST /v1/boxes", Annotations: rw(),
		Description: "Create (or reuse via idempotency_key) a Linux sandbox box and wait until it is running. Returns the Box (id, status, url, cost_per_hour) plus next hints. Call box_quote first to see the price.",
		InputSchema: obj(specProps(true))},
	{Name: "box_quote", Title: "Price a box (dry run)", REST: "POST /v1/quote", Annotations: ro(),
		Description: "Dry-run placement for a spec: where the box would run, cost_per_hour, expected_total_usd for its lifecycle, alternatives, and whether it fits the budget. Creates nothing.",
		InputSchema: obj(specProps(false))},
	{Name: "box_exec", Title: "Run a command", REST: "POST /v1/boxes/{id}/exec", Annotations: rw(),
		Description: "Run a bash command in the box (cwd /work). Returns exit_code, the last tail_lines (default 60) of stdout/stderr, truncated, log_id for the full output, files_changed, tokens_saved and next hints. async=true returns job_id (poll with box_job). Sleeping boxes wake automatically.",
		InputSchema: obj(map[string]any{"box_id": boxID, "command": str("bash command"), "cwd": str("working directory (default /work)"), "timeout_s": intg("timeout in seconds (default 300, max 3600 sync)"), "async": boolean("run in background, returns job_id"), "tail_lines": intg("lines of output to return (default 60)")}, "box_id", "command")},
	{Name: "box_exec_batch", Title: "Run several commands", REST: "POST /v1/boxes/{id}/exec-batch", Annotations: rw(),
		Description: "Run up to 50 commands sequentially in one round-trip. stop_on_error (default true) stops at the first non-zero exit. Returns per-command results, ok, stopped_at, total tokens_saved.",
		InputSchema: obj(map[string]any{"box_id": boxID, "commands": arr("bash commands, run in order", "string"), "stop_on_error": boolean("stop at the first failing command (default true)"), "cwd": str("working directory for all commands"), "timeout_s": intg("timeout per command in seconds"), "tail_lines": intg("lines of output per command")}, "box_id", "commands")},
	{Name: "box_job", Title: "Get job status", REST: "GET /v1/jobs/{id}?wait=", Annotations: ro(),
		Description: "Get an async exec job (status queued|running|done|failed and its ExecResult). wait_s long-polls up to that many seconds (max 60) for completion.",
		InputSchema: obj(map[string]any{"job_id": str("job id"), "wait_s": intg("seconds to wait for completion (max 60)")}, "job_id")},
	{Name: "box_logs", Title: "Read full logs", REST: "GET /v1/logs/{id}", Annotations: ro(),
		Description: "Read the full output of an exec by log_id (optionally only the last tail_lines or lines matching grep). Without log_id, lists the recent exec calls of box_id with their exit codes and log ids.",
		InputSchema: obj(map[string]any{"log_id": str("log id from an exec result"), "box_id": boxID, "tail_lines": intg("return only the last N lines"), "grep": str("return only lines containing this text")})},
	{Name: "box_read", Title: "Read file or directory", REST: "GET /v1/boxes/{id}/files", Annotations: ro(),
		Description: "Read a file (optionally a line range from..to, 1-based) or list a directory in the box.",
		InputSchema: obj(map[string]any{"box_id": boxID, "path": str("path, e.g. /work/main.py"), "from": intg("first line"), "to": intg("last line")}, "box_id", "path")},
	{Name: "box_write", Title: "Write file", REST: "PUT /v1/boxes/{id}/files", Annotations: idem(),
		Description: "Create or overwrite a text file in the box (parent dirs are created).",
		InputSchema: obj(map[string]any{"box_id": boxID, "path": str("path"), "content": str("full file content")}, "box_id", "path", "content")},
	{Name: "box_grep", Title: "Search files", REST: "POST /v1/boxes/{id}/grep", Annotations: ro(),
		Description: "Regex search (extended regexp) under a path in the box; returns path, line, text (max 500 matches).",
		InputSchema: obj(map[string]any{"box_id": boxID, "pattern": str("regular expression"), "path": str("directory or file (default /work)")}, "box_id", "pattern")},
	{Name: "box_observe", Title: "Observe changes", REST: "GET /v1/boxes/{id}/observe", Annotations: ro(),
		Description: "What changed since your last observe call: files changed, running processes, listening ports.",
		InputSchema: obj(map[string]any{"box_id": boxID}, "box_id")},
	{Name: "box_snapshot", Title: "Snapshot box", REST: "POST /v1/boxes/{id}/snapshot", Annotations: rw(),
		Description: "Snapshot the box filesystem (/work and /memory; keeps the latest few). Use before risky changes; undo with box_restore or branch with box_fork.",
		InputSchema: obj(map[string]any{"box_id": boxID}, "box_id")},
	{Name: "box_restore", Title: "Restore snapshot", REST: "POST /v1/boxes/{id}/restore", Annotations: dest(),
		Description: "Replace the box's files with a snapshot (latest when snapshot_id is omitted). Changes since the snapshot are lost.",
		InputSchema: obj(map[string]any{"box_id": boxID, "snapshot_id": str("snapshot id (default: latest of this box)")}, "box_id")},
	{Name: "box_fork", Title: "Fork box", REST: "POST /v1/boxes/{id}/fork", Annotations: rw(),
		Description: "Create a new box from a snapshot of this box (latest; a fresh snapshot is taken if none exists). Use it to try alternatives in parallel.",
		InputSchema: obj(map[string]any{"box_id": boxID, "snapshot_id": str("snapshot id (default: latest)"), "slug": str("name for the new box")}, "box_id")},
	{Name: "box_sleep", Title: "Sleep or wake box", REST: "POST /v1/boxes/{id}/sleep|wake", Annotations: idem(),
		Description: "Put a box to sleep to stop compute billing (files are kept). Pass wake=true to wake it instead (exec also auto-wakes).",
		InputSchema: obj(map[string]any{"box_id": boxID, "wake": boolean("wake instead of sleep")}, "box_id")},
	{Name: "box_down", Title: "Terminate box", REST: "DELETE /v1/boxes/{id}", Annotations: dest(),
		Description: "Terminate and delete the box. Its files are gone unless snapshotted.",
		InputSchema: obj(map[string]any{"box_id": boxID}, "box_id")},
	{Name: "box_list", Title: "List boxes", REST: "GET /v1/boxes", Annotations: ro(),
		Description: "List your boxes (not terminated). Optional filters.",
		InputSchema: obj(map[string]any{"status": str("filter by status"), "lifecycle": str("filter by lifecycle")})},
	{Name: "box_cost", Title: "Cost", REST: "GET /v1/overview | GET /v1/boxes/{id}", Annotations: ro(),
		Description: "Spend summary: with box_id returns that box's spent_usd and cost_per_hour; without it returns today/month spend and budgets.",
		InputSchema: obj(map[string]any{"box_id": boxID})},
	{Name: "box_templates", Title: "List templates", REST: "GET /v1/templates", Annotations: ro(),
		Description: "List box templates (built-in and custom) with descriptions. include_yaml=true also returns each template's box.yaml.",
		InputSchema: obj(map[string]any{"include_yaml": boolean("include the yaml of each template")})},
}

func init() {
	for i := range Tools {
		if Tools[i].Annotations != nil {
			Tools[i].Annotations.Title = Tools[i].Title
		}
	}
}

// ToolNames returns tool names in order.
func ToolNames() []string {
	out := make([]string, len(Tools))
	for i, t := range Tools {
		out[i] = t.Name
	}
	return out
}

// Formats supported by ToolsAs.
var Formats = []string{"openai", "anthropic", "gemini", "mcp"}

// ToolsAs renders the tool list in a provider's function-calling format:
//
//	openai    → [{"type":"function","function":{name,description,parameters,strict:false}}]  (Chat Completions)
//	anthropic → [{name,description,input_schema}]                                            (Messages API)
//	gemini    → [{"functionDeclarations":[{name,description,parameters}]}]                   (Tool objects)
//	mcp       → {"tools":[...]}                                                              (tools/list result)
func ToolsAs(format string) (any, bool) {
	switch strings.ToLower(format) {
	case "openai", "":
		out := make([]map[string]any, len(Tools))
		for i, t := range Tools {
			out[i] = map[string]any{"type": "function", "function": map[string]any{
				"name": t.Name, "description": t.Description, "parameters": t.InputSchema, "strict": false}}
		}
		return out, true
	case "anthropic", "claude":
		out := make([]map[string]any, len(Tools))
		for i, t := range Tools {
			out[i] = map[string]any{"name": t.Name, "description": t.Description, "input_schema": t.InputSchema}
		}
		return out, true
	case "gemini", "google":
		decls := make([]map[string]any, len(Tools))
		for i, t := range Tools {
			decls[i] = map[string]any{"name": t.Name, "description": t.Description, "parameters": geminiSchema(t.InputSchema)}
		}
		return []map[string]any{{"functionDeclarations": decls}}, true
	case "mcp":
		return map[string]any{"tools": Tools}, true
	}
	return nil, false
}

// geminiSchema converts JSON Schema to the OpenAPI-3.0 subset Gemini accepts
// (no additionalProperties; property-less objects are omitted).
func geminiSchema(s map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range s {
		switch k {
		case "additionalProperties":
			continue
		case "properties":
			props := map[string]any{}
			for pk, pv := range v.(map[string]any) {
				props[pk] = geminiSchema(pv.(map[string]any))
			}
			out[k] = props
		case "items":
			out[k] = geminiSchema(v.(map[string]any))
		default:
			out[k] = v
		}
	}
	if out["type"] == "object" {
		if p, ok := out["properties"].(map[string]any); ok && len(p) == 0 {
			delete(out, "properties")
		}
	}
	return out
}
