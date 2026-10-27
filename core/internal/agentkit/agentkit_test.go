package agentkit

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"

	"boxaz/core/internal/i18n"
)

var contractIDs = "claude-code, codex-cli, gemini-cli, cursor, windsurf, cline, openai-agents, langchain, langgraph, crewai, autogen, vercel-ai-sdk, mastra, pydantic-ai, llamaindex, n8n, dify, flowise, make, zapier, hark, custom"

func TestKindsMatchContract(t *testing.T) {
	if got := strings.Join(IDs(), ", "); got != contractIDs {
		t.Fatalf("kinds:\n%s\nwant\n%s", got, contractIDs)
	}
	cats := map[string]bool{"coding": true, "framework": true, "workflow": true, "platform": true, "custom": true}
	for _, k := range Kinds {
		if !cats[k.Category] || len(k.Transports) == 0 || k.Name == "" {
			t.Errorf("bad kind %+v", k)
		}
	}
}

func TestSnippets(t *testing.T) {
	py, _ := exec.LookPath("python3")
	for _, k := range Kinds {
		for _, l := range i18n.Locales {
			s := Build(k.ID, "https://core.example", "coder", l)
			if s.Kind != k.ID || s.Language != k.Language || s.Locale != l {
				t.Errorf("%s/%s: header %+v", k.ID, l, s)
			}
			if strings.Contains(s.Snippet, "{{C_") || strings.Contains(s.Snippet, "{{MCP}}") || strings.Contains(s.Snippet, "{{BASE}}") || strings.Contains(s.Snippet, "{{TASK}}") {
				t.Errorf("%s/%s: unreplaced placeholder:\n%s", k.ID, l, s.Snippet)
			}
			for _, n := range s.Notes {
				if strings.Contains(n, "snip.") || strings.Contains(n, "{{BASE}}") {
					t.Errorf("%s/%s: bad note %q", k.ID, l, n)
				}
			}
			if !strings.Contains(s.Snippet, "BOXAZ_API_KEY") {
				t.Errorf("%s: snippet does not use BOXAZ_API_KEY", k.ID)
			}
			switch k.Language {
			case "json":
				var v any
				if err := json.Unmarshal([]byte(s.Snippet), &v); err != nil {
					t.Errorf("%s/%s: invalid JSON: %v\n%s", k.ID, l, err, s.Snippet)
				}
			case "python":
				if py != "" {
					f, _ := os.CreateTemp(t.TempDir(), "*.py")
					f.WriteString(s.Snippet)
					f.Close()
					if out, err := exec.Command(py, "-c", "import ast,sys; ast.parse(open(sys.argv[1]).read())", f.Name()).CombinedOutput(); err != nil {
						t.Errorf("%s/%s: python syntax: %s\n%s", k.ID, l, out, s.Snippet)
					}
				}
			}
		}
	}
	if Build("nope", "http://x", "a", "en").Kind != "custom" {
		t.Fatal("unknown kind should fall back to custom")
	}
}
