package api

// Agent discovery documents (no auth): /llms.txt, /llms-full.txt (llmstxt.org
// format, intro/quickstart localized), /.well-known/mcp.json (MCP server
// card), /.well-known/agent.json (A2A agent card). All are generated from the
// same tool list (internal/mcp) and route table (openapi.go).

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"boxaz/core/internal/agentkit"
	"boxaz/core/internal/i18n"
	"boxaz/core/internal/manager"
	"boxaz/core/internal/mcp"
	"boxaz/core/internal/model"
	"boxaz/core/internal/sandbox"
	"boxaz/core/internal/selector"
)

func (s *Server) llmsHeader(r *http.Request) (string, string, *strings.Builder) {
	loc := reqLocale(r)
	base := s.publicBase(r)
	t := func(k string, a ...any) string { return i18n.T(loc, k, a...) }
	var b strings.Builder
	fmt.Fprintf(&b, "# Boxaz\n\n> %s\n\n%s\n\n", t("llms.summary"), t("llms.details"))
	fmt.Fprintf(&b, "## %s\n\n", t("llms.h_quickstart"))
	fmt.Fprintf(&b, "1. %s\n", t("llms.qs1"))
	fmt.Fprintf(&b, "2. %s `%s/mcp` (Streamable HTTP, header `Authorization: Bearer $BOXAZ_API_KEY`) — %s `box mcp`.\n", t("llms.qs2"), base, t("llms.or_stdio"))
	fmt.Fprintf(&b, "3. %s\n", t("llms.qs3"))
	fmt.Fprintf(&b, "4. %s\n\n", t("llms.qs4"))
	// v1.3: how to choose a box
	fmt.Fprintf(&b, "## %s\n\n%s\n\n", t("llms.h_choose"), t("llms.choose"))
	for _, p := range selector.Profiles {
		fmt.Fprintf(&b, "- `%s` (%s, %s, %.0f vCPU / %d MB): %s\n", p.ID, p.Template, p.Lifecycle, p.Defaults.CPU, p.Defaults.RAMMB, p.Description)
	}
	b.WriteString("\n")
	// v1.4: sandbox API providers
	fmt.Fprintf(&b, "## %s\n\n%s\n\n", t("llms.h_sandbox"), t("llms.sandbox"))
	for _, p := range s.M.Providers.All() {
		if sp, ok := p.(sandbox.Provider); ok {
			in := sp.Info()
			fmt.Fprintf(&b, "- `%s` %s (%s, %s, %s): %s\n", in.ID, in.Name, in.Tier, in.Adapter, in.Verification, in.Docs)
		}
	}
	b.WriteString("\n")
	// v1.7: supervised apps, gateway URLs (wake on request), URL tokens, capabilities
	fmt.Fprintf(&b, "## %s\n\n%s\n\n", t("llms.h_apps"), t("llms.apps"))
	// v1.10: box access (tickets, durable terminal, port forward, SSH)
	fmt.Fprintf(&b, "## %s\n\n%s\n\n", t("llms.h_access"), t("llms.access"))
	// v1.11: SFTP, SSH certificates, browser (CDP), desktop (VNC) + share links
	fmt.Fprintf(&b, "## %s\n\n%s\n\n", t("llms.h_access_p1"), t("llms.access_p1"))
	return loc, base, &b
}

func (s *Server) llmsTxt(w http.ResponseWriter, r *http.Request) {
	loc, base, b := s.llmsHeader(r)
	t := func(k string, a ...any) string { return i18n.T(loc, k, a...) }
	fmt.Fprintf(b, "## %s\n\n", t("llms.h_docs"))
	fmt.Fprintf(b, "- [llms-full.txt](%s/llms-full.txt): %s\n", base, t("llms.d_full"))
	fmt.Fprintf(b, "- [OpenAPI 3.1](%s/v1/openapi.json): %s\n", base, t("llms.d_openapi"))
	fmt.Fprintf(b, "- [Tools (OpenAI)](%s/v1/tools?format=openai): %s\n", base, t("llms.d_tools"))
	fmt.Fprintf(b, "- [Tools (Anthropic)](%s/v1/tools?format=anthropic)\n", base)
	fmt.Fprintf(b, "- [Tools (Gemini)](%s/v1/tools?format=gemini)\n", base)
	fmt.Fprintf(b, "- [MCP server card](%s/.well-known/mcp.json): %s\n", base, t("llms.d_mcp"))
	fmt.Fprintf(b, "- [A2A agent card](%s/.well-known/agent.json): %s\n", base, t("llms.d_a2a", base))
	fmt.Fprintf(b, "- [Agent kinds](%s/v1/agent-kinds): %s\n", base, t("llms.d_kinds"))
	fmt.Fprintf(b, "- [Errors](%s/v1/errors): %s\n\n", base, t("llms.d_errors"))
	fmt.Fprintf(b, "## %s\n\n", t("llms.h_tools"))
	for _, tl := range mcp.Tools {
		fmt.Fprintf(b, "- `%s`: %s\n", tl.Name, firstSentence(tl.Description))
	}
	fmt.Fprintf(b, "\n## Optional\n\n- [%s](%s/v1/agent-kinds)\n", t("llms.d_snippets"), base)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte(b.String()))
}

func firstSentence(s string) string {
	if i := strings.Index(s, ". "); i > 0 {
		return s[:i+1]
	}
	return s
}

func (s *Server) llmsFull(w http.ResponseWriter, r *http.Request) {
	loc, base, b := s.llmsHeader(r)
	t := func(k string, a ...any) string { return i18n.T(loc, k, a...) }
	fmt.Fprintf(b, "## %s\n\n%s\n\n", t("llms.h_auth"), t("llms.auth", base))
	fmt.Fprintf(b, "## %s\n\n%s\n\n", t("llms.h_concepts"), t("llms.concepts"))
	fmt.Fprintf(b, "## A2A\n\n%s\n\n", t("llms.a2a", base))

	fmt.Fprintf(b, "## %s\n\n", t("llms.h_tools"))
	for _, tl := range mcp.Tools {
		fmt.Fprintf(b, "### %s — %s\n\n%s\n\nREST: `%s`\n\n", tl.Name, tl.Title, tl.Description, tl.REST)
		props, _ := tl.InputSchema["properties"].(map[string]any)
		reqd := map[string]bool{}
		if rq, ok := tl.InputSchema["required"].([]string); ok {
			for _, x := range rq {
				reqd[x] = true
			}
		}
		keys := make([]string, 0, len(props))
		for k := range props {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			p, _ := props[k].(map[string]any)
			mark := ""
			if reqd[k] {
				mark = " (required)"
			}
			fmt.Fprintf(b, "- `%s` %v%s: %v\n", k, p["type"], mark, p["description"])
		}
		b.WriteString("\n")
	}

	fmt.Fprintf(b, "## %s\n\n", t("llms.h_endpoints"))
	for _, o := range ops {
		if o.Tag == "internal" {
			continue
		}
		auth := ""
		if o.NoAuth {
			auth = " (no auth)"
		}
		fmt.Fprintf(b, "- `%s %s`%s — %s\n", o.Method, o.Path, auth, o.Summary)
	}

	fmt.Fprintf(b, "\n## %s\n\n%s\n\n", t("llms.h_errors"), t("llms.errors_intro"))
	for _, c := range model.Codes {
		ra := ""
		if n := model.DefaultRetryAfter(c); n > 0 {
			ra = fmt.Sprintf(" retry_after_s≈%d.", n)
		}
		st := model.StatusFor(c)
		if c == model.ErrInternal {
			st = 500
		}
		fmt.Fprintf(b, "- <a id=\"error-%s\"></a>`%s` (HTTP %d): %s%s\n", c, c, st, i18n.Hint(loc, model.DefaultHint(c)), ra)
	}

	fmt.Fprintf(b, "\n## %s\n\n", t("llms.h_kinds"))
	for _, k := range agentkit.Kinds {
		fmt.Fprintf(b, "- `%s` %s (%s; %s)\n", k.ID, k.Name, k.Category, strings.Join(k.Transports, ", "))
	}
	fmt.Fprintf(b, "\n%s\n", t("llms.snippet_hint", base))
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte(b.String()))
}

// mcpCard serves /.well-known/mcp.json. The MCP server-card format is still a
// draft (SEP "server cards" + registry server.json); we include the fields of
// both: serverInfo/transport/capabilities and remotes with header metadata.
func (s *Server) mcpCard(w http.ResponseWriter, r *http.Request) {
	base := s.publicBase(r)
	loc := reqLocale(r)
	prompts := []string{"debug-failing-command", "start-project", "cost-review"}
	writeJSON(w, 200, map[string]any{
		"name":            "dev.boxaz/boxaz",
		"title":           "Boxaz",
		"description":     i18n.T(loc, "llms.summary"),
		"version":         manager.Version,
		"protocolVersion": mcp.ProtocolVersion,
		"serverInfo":      map[string]any{"name": "boxaz", "title": "Boxaz", "version": manager.Version},
		"transport":       map[string]any{"type": "streamable-http", "url": base + "/mcp"},
		"remotes": []any{map[string]any{"type": "streamable-http", "url": base + "/mcp", "headers": []any{
			map[string]any{"name": "Authorization", "description": "Bearer <BOXAZ_API_KEY>", "isRequired": true, "isSecret": true},
			map[string]any{"name": "Accept-Language", "description": "vi | en | zh | ja", "isRequired": false},
		}}},
		"packages": []any{map[string]any{"registryType": "binary", "identifier": "box", "transport": map[string]any{"type": "stdio"},
			"runtimeArguments": []string{"mcp"}, "environmentVariables": []any{
				map[string]any{"name": "BOXAZ_URL", "isRequired": true, "default": base},
				map[string]any{"name": "BOXAZ_API_KEY", "isRequired": true, "isSecret": true},
				map[string]any{"name": "BOXAZ_LANG", "isRequired": false, "description": "vi | en | zh | ja"},
			}}},
		"authentication": map[string]any{"required": true, "schemes": []string{"bearer"}},
		"capabilities":   map[string]any{"tools": map[string]any{"listChanged": false}, "resources": map[string]any{"subscribe": false, "listChanged": false}, "prompts": map[string]any{"listChanged": false}},
		"tools":          mcp.ToolNames(),
		"resources":      mcp.ResourceTemplates,
		"prompts":        prompts,
		"instructions":   i18n.T(loc, "mcp.instructions"),
		"documentation":  base + "/llms-full.txt",
		"openapi":        base + "/v1/openapi.json",
	})
}

// agentCard serves /.well-known/agent.json (A2A AgentCard, protocol 0.3).
// The JSON-RPC task API lives at POST /a2a (message/send, message/stream,
// tasks/get, tasks/cancel, tasks/resubscribe + the ≤0.2 aliases tasks/send
// and tasks/sendSubscribe). Every Boxaz tool is a skill: send a data part
// {"tool": "<name>", "arguments": {...}}; commands run as async jobs.
func (s *Server) agentCard(w http.ResponseWriter, r *http.Request) {
	base := s.publicBase(r)
	loc := reqLocale(r)
	skills := []any{map[string]any{
		"id": "exec", "name": "Run a command in a box",
		"description": "Send the shell command as a text part with metadata.box_id (or a data part {box_id, command, cwd?, timeout_s?}). Runs as an async job; the task id is the job id. Stream progress with message/stream, cancel with tasks/cancel.",
		"tags":        []string{"sandbox", "linux", "code-execution", "exec"},
		"examples":    []string{`{"role":"user","parts":[{"kind":"text","text":"pytest -q"}],"metadata":{"box_id":"box_123"}}`},
		"inputModes":  []string{"text/plain", "application/json"}, "outputModes": []string{"application/json", "text/plain"},
	}}
	for _, t := range mcp.Tools {
		skills = append(skills, map[string]any{
			"id": t.Name, "name": t.Title, "description": t.Description + ` A2A: data part {"tool":"` + t.Name + `","arguments":{…}}.`,
			"tags":       []string{"sandbox", "linux", "code-execution", strings.TrimPrefix(t.Name, "box_")},
			"inputModes": []string{"application/json"}, "outputModes": []string{"application/json"},
		})
	}
	writeJSON(w, 200, map[string]any{
		"protocolVersion":    "0.3.0",
		"name":               "Boxaz",
		"description":        i18n.T(loc, "llms.summary"),
		"url":                base + "/a2a",
		"preferredTransport": "JSONRPC",
		"additionalInterfaces": []any{
			map[string]any{"url": base + "/a2a", "transport": "JSONRPC"},
			map[string]any{"url": base + "/v1", "transport": "HTTP+JSON"},
		},
		"provider":                          map[string]any{"organization": "Boxaz", "url": base},
		"version":                           manager.Version,
		"documentationUrl":                  base + "/llms-full.txt",
		"iconUrl":                           nil,
		"capabilities":                      map[string]any{"streaming": true, "pushNotifications": false, "stateTransitionHistory": false},
		"securitySchemes":                   map[string]any{"bearer": map[string]any{"type": "http", "scheme": "bearer", "description": "Boxaz API key"}},
		"security":                          []any{map[string]any{"bearer": []string{}}},
		"defaultInputModes":                 []string{"application/json", "text/plain"},
		"defaultOutputModes":                []string{"application/json", "text/plain"},
		"skills":                            skills,
		"supportsAuthenticatedExtendedCard": false,
		"x-boxaz": map[string]any{"mcp": base + "/mcp", "openapi": base + "/v1/openapi.json", "tools": base + "/v1/tools?format=openai",
			"capabilities": base + "/v1/capabilities",
			"gateway_urls": "readable: https://<name>.<domain>/ → the box's primary port (app port, else first port); https://<name>-<label>.<domain>/ → other ports (label web, api, db, …; never a number); legacy <key>-<port> stays as alias_url; name URLs are guessable, use box_url_token for private apps",
			"url_tokens":   "box_url_token protects only the Boxaz gateway URL of a port; with a gateway domain / internet-facing public URL, E2B and Novita sandboxes are created private (preview host 403 without the sandbox traffic token, which only the gateway adds; ports report provider_preview=private); where provider_preview=public (Daytona, or BOXAZ_PROVIDER_PREVIEW=public) the provider preview URL stays reachable by anyone who knows the sandbox id",
			"access":       "v1.10: box_forward (box forward <box> <port>: TCP over wss://<name>.<domain>/.boxaz/tcp), box_ssh_info (SSH gateway in core: box ssh / ssh-config / ProxyCommand box ssh-proxy %h, no sshd in the box; v1.11: also signs a short-lived SSH user certificate, POST /v1/ssh/certs, ≤ 12 h, revocable), box_ticket (single-use, 60 s, per box + purpose; the terminal / tcp / ssh / cdp / vnc WebSockets never take an API key in the URL), box_terminal_sessions (durable terminal: reattach + replay). v1.11: SFTP always (box sftp, OpenSSH sftp / scp; core serves it when the box has no sftp-server), box_browser (Chromium over CDP: cdp_url wss://<name>.<domain>/.boxaz/cdp?ticket=… for connectOverCDP / puppeteer.connect, metadata addresses blocked), box_desktop (Xvfb + VNC: single-use noVNC viewer_url /.boxaz/desktop?ticket=…, view-only or interactive; share links /.boxaz/desktop?share=bxs_… view-only by default, revoked → 410)",
			"access_detail": i18n.T(loc, "llms.access") + "\n" + i18n.T(loc, "llms.access_p1"),
			"a2a_methods":  []string{"message/send", "message/stream", "tasks/get", "tasks/cancel", "tasks/resubscribe", "tasks/send", "tasks/sendSubscribe"}},
	})
}
