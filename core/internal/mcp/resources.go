package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"boxaz/core/internal/i18n"
)

// ---- resources ----
//
//	boxaz://box/{box_id}              Box JSON
//	boxaz://box/{box_id}/file/{path}  file content or directory listing (path relative to /, e.g. work/main.py)
//	boxaz://box/{box_id}/logs         recent exec calls (exit code, tokens_saved, log_id)
//	boxaz://log/{log_id}              full output of one exec

// ResourceTemplate is an MCP resource template.
type ResourceTemplate struct {
	URITemplate string `json:"uriTemplate"`
	Name        string `json:"name"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description"`
	MimeType    string `json:"mimeType"`
}

var ResourceTemplates = []ResourceTemplate{
	{"boxaz://box/{box_id}", "box", "Box", "Box object (status, spec, cost, url).", "application/json"},
	{"boxaz://box/{box_id}/file/{path}", "box-file", "Box file", "A file or directory inside the box; path is relative to / (work/main.py, memory/notes.md, AGENTS.md).", "text/plain"},
	{"boxaz://box/{box_id}/logs", "box-logs", "Box exec log", "Recent exec calls of the box with exit codes, tokens_saved and log ids.", "application/json"},
	{"boxaz://log/{log_id}", "log", "Full exec output", "Full stdout+stderr of one exec (log_id from an exec result).", "text/plain"},
}

func listResources(ctx context.Context, c Caller) map[string]any {
	res := []map[string]any{}
	st, b, err := c.Do(ctx, "GET", "/boxes", nil)
	if err == nil && st < 300 {
		var l struct {
			Items []struct {
				ID, Slug, Status, Template string
			} `json:"items"`
		}
		json.Unmarshal(b, &l)
		for _, bx := range l.Items {
			res = append(res,
				map[string]any{"uri": "boxaz://box/" + bx.ID, "name": bx.Slug, "title": "Box " + bx.Slug,
					"description": fmt.Sprintf("%s box (%s)", bx.Template, bx.Status), "mimeType": "application/json"},
				map[string]any{"uri": "boxaz://box/" + bx.ID + "/file/AGENTS.md", "name": bx.Slug + "/AGENTS.md",
					"description": "AGENTS.md of " + bx.Slug, "mimeType": "text/markdown"},
				map[string]any{"uri": "boxaz://box/" + bx.ID + "/logs", "name": bx.Slug + "/logs",
					"description": "Recent exec calls of " + bx.Slug, "mimeType": "application/json"},
			)
		}
	}
	return map[string]any{"resources": res}
}

func readResource(ctx context.Context, c Caller, uri string) (map[string]any, error) {
	const pfx = "boxaz://"
	if !strings.HasPrefix(uri, pfx) {
		return nil, fmt.Errorf("unsupported resource uri %q (want boxaz://…)", uri)
	}
	rest := strings.TrimPrefix(uri, pfx)
	text := func(mime, body string) map[string]any {
		return map[string]any{"contents": []map[string]any{{"uri": uri, "mimeType": mime, "text": body}}}
	}
	call := func(method, path string) (string, error) {
		st, b, err := c.Do(ctx, method, path, nil)
		if err != nil {
			return "", err
		}
		if st >= 300 {
			return "", fmt.Errorf("%s", strings.TrimSpace(string(b)))
		}
		return string(b), nil
	}
	switch {
	case strings.HasPrefix(rest, "log/"):
		body, err := call("GET", "/logs/"+esc(strings.TrimPrefix(rest, "log/")))
		if err != nil {
			return nil, err
		}
		return text("text/plain", body), nil
	case strings.HasPrefix(rest, "box/"):
		parts := strings.SplitN(strings.TrimPrefix(rest, "box/"), "/", 3)
		id := parts[0]
		if id == "" {
			return nil, fmt.Errorf("missing box id in %q", uri)
		}
		if len(parts) == 1 {
			body, err := call("GET", "/boxes/"+esc(id))
			if err != nil {
				return nil, err
			}
			return text("application/json", body), nil
		}
		switch parts[1] {
		case "logs":
			st, b, err := toolLogs(ctx, c, args{"box_id": id})
			if err != nil {
				return nil, err
			}
			if st >= 300 {
				return nil, fmt.Errorf("%s", b)
			}
			return text("application/json", string(b)), nil
		case "file":
			p := "/"
			if len(parts) == 3 {
				dec, err := url.PathUnescape(parts[2])
				if err != nil {
					dec = parts[2]
				}
				p = "/" + strings.TrimPrefix(dec, "/")
			}
			body, err := call("GET", "/boxes/"+esc(id)+"/files?"+url.Values{"path": {p}}.Encode())
			if err != nil {
				return nil, err
			}
			var f struct {
				Type    string `json:"type"`
				Content string `json:"content"`
			}
			json.Unmarshal([]byte(body), &f)
			if f.Type == "file" {
				mime := "text/plain"
				if strings.HasSuffix(p, ".md") {
					mime = "text/markdown"
				}
				return text(mime, f.Content), nil
			}
			return text("application/json", body), nil
		}
	}
	return nil, fmt.Errorf("unknown resource %q", uri)
}

// ---- prompts ----

type promptArg struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Required    bool   `json:"required"`
}

type promptDef struct {
	Name  string
	Args  []string // names; required unless prefixed with "?"
	Title string   // i18n keys prompt.<name>.title / .desc / .text / .arg.<arg>
}

var promptDefs = []promptDef{
	{Name: "debug-failing-command", Args: []string{"box_id", "command"}},
	{Name: "start-project", Args: []string{"goal", "?template"}},
	{Name: "cost-review", Args: nil},
}

func listPrompts(loc string) []map[string]any {
	out := []map[string]any{}
	for _, p := range promptDefs {
		var as []promptArg
		for _, a := range p.Args {
			req := !strings.HasPrefix(a, "?")
			a = strings.TrimPrefix(a, "?")
			as = append(as, promptArg{Name: a, Description: i18n.T(loc, "prompt."+p.Name+".arg."+a), Required: req})
		}
		if as == nil {
			as = []promptArg{}
		}
		out = append(out, map[string]any{"name": p.Name, "title": i18n.T(loc, "prompt."+p.Name+".title"),
			"description": i18n.T(loc, "prompt."+p.Name+".desc"), "arguments": as})
	}
	return out
}

func getPrompt(loc, name string, a map[string]string) (map[string]any, error) {
	for _, p := range promptDefs {
		if p.Name != name {
			continue
		}
		var vals []any
		for _, an := range p.Args {
			req := !strings.HasPrefix(an, "?")
			an = strings.TrimPrefix(an, "?")
			v := a[an]
			if v == "" && req {
				return nil, fmt.Errorf("missing required argument %q", an)
			}
			if v == "" {
				v = "python"
			}
			vals = append(vals, v)
		}
		txt := i18n.T(loc, "prompt."+p.Name+".text", vals...)
		return map[string]any{
			"description": i18n.T(loc, "prompt."+p.Name+".desc"),
			"messages":    []map[string]any{{"role": "user", "content": map[string]any{"type": "text", "text": txt}}},
		}, nil
	}
	return nil, fmt.Errorf("unknown prompt %q", name)
}
