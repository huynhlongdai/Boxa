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

	"boxa/core/internal/agentkit"
	"boxa/core/internal/i18n"
	"boxa/core/internal/manager"
	"boxa/core/internal/mcp"
	"boxa/core/internal/model"
)

func (s *Server) llmsHeader(r *http.Request) (string, string, *strings.Builder) {
	loc := reqLocale(r)
	base := s.publicBase(r)
	t := func(k string, a ...any) string { return i18n.T(loc, k, a...) }
	var b strings.Builder
	fmt.Fprintf(&b, "# Boxa\n\n> %s\n\n%s\n\n", t("llms.summary"), t("llms.details"))
	fmt.Fprintf(&b, "## %s\n\n", t("llms.h_quickstart"))
	fmt.Fprintf(&b, "1. %s\n", t("llms.qs1"))
	fmt.Fprintf(&b, "2. %s `%s/mcp` (Streamable HTTP, header `Authorization: Bearer $BOXA_API_KEY`) — %s `box mcp`.\n", t("llms.qs2"), base, t("llms.or_stdio"))
	fmt.Fprintf(&b, "3. %s\n", t("llms.qs3"))
	fmt.Fprintf(&b, "4. %s\n\n", t("llms.qs4"))
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
	fmt.Fprintf(b, "- [A2A agent card](%s/.well-known/agent.json)\n", base)
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
		"name":            "dev.boxa/boxa",
		"title":           "Boxa",
		"description":     i18n.T(loc, "llms.summary"),
		"version":         manager.Version,
		"protocolVersion": mcp.ProtocolVersion,
		"serverInfo":      map[string]any{"name": "boxa", "title": "Boxa", "version": manager.Version},
		"transport":       map[string]any{"type": "streamable-http", "url": base + "/mcp"},
		"remotes": []any{map[string]any{"type": "streamable-http", "url": base + "/mcp", "headers": []any{
			map[string]any{"name": "Authorization", "description": "Bearer <BOXA_API_KEY>", "isRequired": true, "isSecret": true},
			map[string]any{"name": "Accept-Language", "description": "vi | en | zh | ja", "isRequired": false},
		}}},
		"packages": []any{map[string]any{"registryType": "binary", "identifier": "box", "transport": map[string]any{"type": "stdio"},
			"runtimeArguments": []string{"mcp"}, "environmentVariables": []any{
				map[string]any{"name": "BOXA_URL", "isRequired": true, "default": base},
				map[string]any{"name": "BOXA_API_KEY", "isRequired": true, "isSecret": true},
				map[string]any{"name": "BOXA_LANG", "isRequired": false, "description": "vi | en | zh | ja"},
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
// Boxa exposes its skills over REST/MCP; the A2A message/send task endpoint
// is not implemented, so the card advertises the HTTP+JSON interface.
func (s *Server) agentCard(w http.ResponseWriter, r *http.Request) {
	base := s.publicBase(r)
	loc := reqLocale(r)
	var skills []any
	for _, t := range mcp.Tools {
		skills = append(skills, map[string]any{
			"id": t.Name, "name": t.Title, "description": t.Description,
			"tags":       []string{"sandbox", "linux", "code-execution", strings.TrimPrefix(t.Name, "box_")},
			"inputModes": []string{"application/json"}, "outputModes": []string{"application/json"},
		})
	}
	writeJSON(w, 200, map[string]any{
		"protocolVersion":    "0.3.0",
		"name":               "Boxa",
		"description":        i18n.T(loc, "llms.summary"),
		"url":                base + "/v1",
		"preferredTransport": "HTTP+JSON",
		"additionalInterfaces": []any{
			map[string]any{"url": base + "/v1", "transport": "HTTP+JSON"},
		},
		"provider":                          map[string]any{"organization": "Boxa", "url": base},
		"version":                           manager.Version,
		"documentationUrl":                  base + "/llms-full.txt",
		"iconUrl":                           nil,
		"capabilities":                      map[string]any{"streaming": false, "pushNotifications": false, "stateTransitionHistory": false},
		"securitySchemes":                   map[string]any{"bearer": map[string]any{"type": "http", "scheme": "bearer", "description": "Boxa API key"}},
		"security":                          []any{map[string]any{"bearer": []string{}}},
		"defaultInputModes":                 []string{"application/json", "text/plain"},
		"defaultOutputModes":                []string{"application/json", "text/plain"},
		"skills":                            skills,
		"supportsAuthenticatedExtendedCard": false,
		"x-boxa": map[string]any{"mcp": base + "/mcp", "openapi": base + "/v1/openapi.json", "tools": base + "/v1/tools?format=openai",
			"note": "A2A JSON-RPC task methods (message/send) are not implemented; call the REST/MCP interfaces."},
	})
}
