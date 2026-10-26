package i18n

import (
	"embed"
	"strings"
	"text/template"
)

//go:embed agentsmd/*.md.tmpl
var agentsFS embed.FS

var agentsTpl = map[string]*template.Template{}

func init() {
	for _, l := range Locales {
		b, err := agentsFS.ReadFile("agentsmd/" + l + ".md.tmpl")
		if err != nil {
			panic("i18n: missing AGENTS.md template " + l)
		}
		agentsTpl[l] = template.Must(template.New(l).Parse(string(b)))
	}
}

// AgentsMDData feeds the AGENTS.md template.
type AgentsMDData struct {
	Slug, BoxID, Template, Lifecycle, AgentName, AgentKind string
	CPU                                                    string
	RAMMB, DiskGB                                          int
	URL, Ports, ForkedFrom                                 string
	Budget, LifecycleRule, Secrets                         string
	Tools                                                  []string
	Base                                                   string
}

// AgentsMD renders /AGENTS.md for a box in locale l.
func AgentsMD(l string, d AgentsMDData) string {
	t, ok := agentsTpl[l]
	if !ok {
		t = agentsTpl[Default]
	}
	var sb strings.Builder
	if err := t.Execute(&sb, d); err != nil {
		return "# AGENTS.md\n\n" + err.Error()
	}
	return sb.String()
}
