package i18n

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	cases := map[string]string{
		"":                        "en",
		"vi":                      "vi",
		"vi-VN,vi;q=0.9,en;q=0.8": "vi",
		"fr-FR, ja;q=0.5":         "ja",
		"zh-Hant-TW":              "zh",
		"en-US;q=0.2, zh;q=0.9":   "zh",
		"de, fr":                  "en",
		"JA_jp.UTF-8":             "ja",
		"vi;q=0":                  "en",
	}
	for in, want := range cases {
		if got := Parse(in, "en"); got != want {
			t.Errorf("Parse(%q) = %q, want %q", in, got, want)
		}
	}
}

var verbRe = regexp.MustCompile(`%[-+# 0-9.]*[a-zA-Z%]`)

// collect every literal format/hint passed to model.E / model.EH in core.
func collect(t *testing.T) (msgs, hints map[string]bool) {
	msgs, hints = map[string]bool{}, map[string]bool{}
	root := filepath.Join("..", "..")
	filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), p, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			c, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := c.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if x, ok := sel.X.(*ast.Ident); !ok || x.Name != "model" {
				return true
			}
			lit := func(i int) string {
				if i < len(c.Args) {
					if b, ok := c.Args[i].(*ast.BasicLit); ok && b.Kind == token.STRING {
						s, _ := strconv.Unquote(b.Value)
						return s
					}
				}
				return ""
			}
			switch sel.Sel.Name {
			case "E":
				if s := lit(1); s != "" {
					msgs[s] = true
				}
			case "EH":
				if s := lit(1); s != "" {
					hints[s] = true
				}
				if s := lit(2); s != "" {
					msgs[s] = true
				}
			}
			return true
		})
		return nil
	})
	if len(msgs) < 50 {
		t.Fatalf("extractor found only %d messages", len(msgs))
	}
	return
}

// Every error message and hint in the code base is translated in vi/zh/ja,
// with identical fmt verbs.
func TestCatalogComplete(t *testing.T) {
	msgs, hints := collect(t)
	for _, l := range []string{"vi", "zh", "ja"} {
		c := cats[l]
		for m := range msgs {
			tr, ok := c.Msg[m]
			if !ok {
				t.Errorf("%s: missing msg %q", l, m)
				continue
			}
			if a, b := verbRe.FindAllString(m, -1), verbRe.FindAllString(tr, -1); strings.Join(a, ",") != strings.Join(b, ",") {
				t.Errorf("%s: verbs differ for %q: %v vs %v", l, m, a, b)
			}
		}
		for h := range hints {
			if _, ok := c.Hint[h]; !ok {
				t.Errorf("%s: missing hint %q", l, h)
			}
		}
		for k, en := range cats["en"].Text {
			tr, ok := c.Text[k]
			if !ok {
				t.Errorf("%s: missing text %q", l, k)
				continue
			}
			if a, b := verbRe.FindAllString(en, -1), verbRe.FindAllString(tr, -1); strings.Join(a, ",") != strings.Join(b, ",") {
				t.Errorf("%s: verbs differ for text %q", l, k)
			}
		}
	}
}

func TestMsgAndHint(t *testing.T) {
	if got := Msg("vi", "template %q not found", "x"); got != `không tìm thấy template "x"` {
		t.Fatal(got)
	}
	if got := Msg("en", "template %q not found", "x"); got != `template "x" not found` {
		t.Fatal(got)
	}
	if got := Hint("ja", "Use one of GET /templates."); !strings.Contains(got, "GET /templates") || got == "Use one of GET /templates." {
		t.Fatal(got)
	}
	if got := T("zh", "no.such.key"); got != "no.such.key" {
		t.Fatal(got)
	}
}

func TestAgentsMD(t *testing.T) {
	for _, l := range Locales {
		s := AgentsMD(l, AgentsMDData{Slug: "demo", BoxID: "box_1", Template: "python", Lifecycle: "session", AgentName: "coder",
			AgentKind: "claude-code", CPU: "1", RAMMB: 1024, DiskGB: 10, Budget: "x", LifecycleRule: "y", Secrets: "none",
			Tools: []string{"box_exec"}, Base: "http://b"})
		for _, want := range []string{"/work", "/memory", "box_exec", "http://b/mcp", "box_1"} {
			if !strings.Contains(s, want) {
				t.Errorf("%s: AGENTS.md lacks %q", l, want)
			}
		}
	}
}
