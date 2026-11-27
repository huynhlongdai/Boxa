// Package boxyaml parses and validates box.yaml (spec M14) with line-numbered errors.
package boxyaml

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"boxmes/core/internal/egress"
	"boxmes/core/internal/model"
)

// Request is the normalized create-box request (same shape as POST /boxes body).
type Request struct {
	AgentID     string   `json:"agent_id,omitempty"`
	Slug        string   `json:"slug,omitempty"`
	Template    string   `json:"template"`
	Spec        Spec     `json:"spec"`
	Lifecycle   string   `json:"lifecycle"`
	TTLSeconds  int      `json:"ttl_seconds,omitempty"`
	IdleMinutes int      `json:"idle_minutes,omitempty"`
	BudgetUSD   float64  `json:"budget_usd,omitempty"`
	Ports       []int    `json:"ports,omitempty"`
	Secrets     []string `json:"secrets,omitempty"`
	Setup       []string `json:"setup,omitempty"`
	Region      string   `json:"region,omitempty"`
	YAML        string   `json:"yaml,omitempty"`
	Wait        *bool    `json:"wait,omitempty"`
	// Egress restricts outbound traffic (enforced by the incus driver).
	Egress *egress.Policy `json:"egress,omitempty"`
	// v1.3: need-based selection. Profile = workload profile id; Requirements
	// = declarative needs. Either one makes core auto-pick template, size and
	// placement (warm pool / pool container / VM / burst) by score.
	Profile      string              `json:"profile,omitempty"`
	Requirements *model.Requirements `json:"requirements,omitempty"`
	Name         string              `json:"name,omitempty"` // box_ensure key
	// v1.7: supervised app (process supervisor) and wake-on-request (default on).
	App           *model.AppSpec `json:"app,omitempty"`
	WakeOnRequest *bool          `json:"wake_on_request,omitempty"`
	// v1.9: the box's readable gateway name (https://<subdomain>.<domain>/);
	// default: name, else slug (a taken derived name gets a word suffix).
	Subdomain string `json:"subdomain,omitempty"`
}

type Spec struct {
	CPU    float64 `json:"cpu"`
	RAMMB  int     `json:"ram_mb"`
	DiskGB int     `json:"disk_gb"`
}

// LineError is one validation error.
type LineError struct {
	Line    int    `json:"line"`
	Message string `json:"message"`
}

type Errors []LineError

func (e Errors) Error() string {
	var parts []string
	for _, x := range e {
		parts = append(parts, fmt.Sprintf("line %d: %s", x.Line, x.Message))
	}
	return strings.Join(parts, "; ")
}

var (
	slugRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,40}$`)
	secretRe = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,63}$`)
	tmplRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9.\-_]{0,63}$`)
)

var knownKeys = map[string]bool{
	"name": true, "template": true, "spec": true, "lifecycle": true, "idle_minutes": true,
	"ttl": true, "ttl_seconds": true, "budget_usd": true, "ports": true, "secrets": true,
	"setup": true, "region": true, "description": true, "egress": true,
	"profile": true, "requirements": true, "app": true, "wake_on_request": true,
}

// ParseSize parses "4G", "512M", "4GB", "4096" (MB for ram) into MB.
func ParseSize(s string, defaultUnitMB int) (int, error) {
	s = strings.TrimSpace(strings.ToUpper(s))
	s = strings.TrimSuffix(s, "B")
	s = strings.TrimSuffix(s, "I")
	mult := defaultUnitMB
	switch {
	case strings.HasSuffix(s, "T"):
		mult, s = 1024*1024, strings.TrimSuffix(s, "T")
	case strings.HasSuffix(s, "G"):
		mult, s = 1024, strings.TrimSuffix(s, "G")
	case strings.HasSuffix(s, "M"):
		mult, s = 1, strings.TrimSuffix(s, "M")
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || v <= 0 {
		return 0, fmt.Errorf("bad size %q", s)
	}
	return int(v * float64(mult)), nil
}

// ParseDuration accepts Go durations plus "d" days, or plain seconds.
func ParseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if n, err := strconv.Atoi(s); err == nil {
		return time.Duration(n) * time.Second, nil
	}
	if strings.HasSuffix(s, "d") {
		n, err := strconv.ParseFloat(strings.TrimSuffix(s, "d"), 64)
		if err != nil {
			return 0, err
		}
		return time.Duration(n * float64(24*time.Hour)), nil
	}
	return time.ParseDuration(s)
}

// Parse validates YAML and returns the request; errors carry line numbers.
func Parse(src string) (*Request, Errors) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
		line := 1
		var te *yaml.TypeError
		msg := err.Error()
		if m := regexp.MustCompile(`line (\d+)`).FindStringSubmatch(msg); m != nil {
			line, _ = strconv.Atoi(m[1])
		}
		_ = te
		return nil, Errors{{Line: line, Message: msg}}
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, Errors{{Line: 1, Message: "box.yaml must be a mapping (key: value)"}}
	}
	root := doc.Content[0]
	r := &Request{Lifecycle: "session"}
	var errs Errors
	add := func(n *yaml.Node, f string, a ...any) {
		errs = append(errs, LineError{Line: n.Line, Message: fmt.Sprintf(f, a...)})
	}
	seen := map[string]bool{}
	for i := 0; i+1 < len(root.Content); i += 2 {
		k, v := root.Content[i], root.Content[i+1]
		key := k.Value
		if seen[key] {
			add(k, "duplicate key %q", key)
		}
		seen[key] = true
		if !knownKeys[key] {
			add(k, "unknown key %q (allowed: %s)", key, strings.Join(sortedKeys(), ", "))
			continue
		}
		switch key {
		case "name":
			if !slugRe.MatchString(v.Value) {
				add(v, "name must be lowercase letters, digits, '-' (max 41 chars)")
			}
			r.Slug = v.Value
		case "description":
		case "app":
			// app: { repo: …, install: …, start: …, port: 8000, ready: { http: /healthz }, restart: always, env: {…} }
			var raw map[string]any
			var sp model.AppSpec
			if v.Kind != yaml.MappingNode || v.Decode(&raw) != nil {
				add(v, "app must be a mapping like { start: npm start, port: 8000 }")
				continue
			}
			if b, err := json.Marshal(raw); err != nil || json.Unmarshal(b, &sp) != nil {
				add(v, "app: bad field types (start/install/repo are strings, port is a number, env is a mapping)")
				continue
			}
			sp.Normalize()
			if msg := sp.Validate(); msg != "" {
				add(v, "app: %s", msg)
				continue
			}
			r.App = &sp
		case "wake_on_request":
			b, err := strconv.ParseBool(v.Value)
			if err != nil {
				add(v, "wake_on_request must be true or false")
				continue
			}
			r.WakeOnRequest = &b
		case "profile":
			r.Profile = v.Value
		case "requirements":
			var rq model.Requirements
			if v.Kind != yaml.MappingNode || v.Decode(&rq) != nil {
				add(v, "requirements must be a mapping like { cpu: 2, ram_mb: 4096, max_price_hour: 0.05 }")
				continue
			}
			r.Requirements = &rq
		case "template":
			if !tmplRe.MatchString(v.Value) {
				add(v, "invalid template name %q", v.Value)
			}
			r.Template = v.Value
		case "region":
			r.Region = v.Value
		case "lifecycle":
			switch v.Value {
			case "task", "session", "service":
				r.Lifecycle = v.Value
			default:
				add(v, "lifecycle must be task, session or service (got %q)", v.Value)
			}
		case "idle_minutes":
			n, err := strconv.Atoi(v.Value)
			if err != nil || n < 1 || n > 1440 {
				add(v, "idle_minutes must be an integer 1..1440")
			}
			r.IdleMinutes = n
		case "ttl", "ttl_seconds":
			d, err := ParseDuration(v.Value)
			if err != nil || d <= 0 {
				add(v, "ttl must be a duration like 30m, 6h, 2d or seconds")
			} else if d > 90*24*time.Hour {
				add(v, "ttl must be at most 90d")
			}
			r.TTLSeconds = int(d.Seconds())
		case "budget_usd":
			f, err := strconv.ParseFloat(v.Value, 64)
			if err != nil || f < 0 {
				add(v, "budget_usd must be a non-negative number")
			}
			r.BudgetUSD = f
		case "spec":
			if v.Kind != yaml.MappingNode {
				add(v, "spec must be a mapping like { cpu: 2, ram: 4G, disk: 20G }")
				continue
			}
			for j := 0; j+1 < len(v.Content); j += 2 {
				sk, sv := v.Content[j], v.Content[j+1]
				switch sk.Value {
				case "cpu":
					f, err := strconv.ParseFloat(sv.Value, 64)
					if err != nil || f <= 0 || f > 64 {
						add(sv, "spec.cpu must be a number in (0, 64]")
					}
					r.Spec.CPU = f
				case "ram", "ram_mb", "memory":
					mb, err := ParseSize(sv.Value, 1)
					if sk.Value == "ram" && err == nil && !strings.ContainsAny(strings.ToUpper(sv.Value), "GMT") {
						mb, err = ParseSize(sv.Value, 1024) // bare number for "ram" = GB
					}
					if err != nil || mb < 128 {
						add(sv, "spec.ram must be a size like 512M or 4G (min 128M)")
					}
					r.Spec.RAMMB = mb
				case "disk", "disk_gb":
					mb, err := ParseSize(sv.Value, 1024)
					if err != nil || mb < 1024 {
						add(sv, "spec.disk must be a size like 20G (min 1G)")
					}
					r.Spec.DiskGB = mb / 1024
				case "gpu":
					add(sk, "gpu is not available in v1")
				default:
					add(sk, "unknown spec key %q (allowed: cpu, ram, disk)", sk.Value)
				}
			}
		case "ports":
			if v.Kind != yaml.SequenceNode {
				add(v, "ports must be a list like [8080]")
				continue
			}
			for _, p := range v.Content {
				n, err := strconv.Atoi(p.Value)
				if err != nil || n < 1 || n > 65535 {
					add(p, "port %q must be 1..65535", p.Value)
					continue
				}
				r.Ports = append(r.Ports, n)
			}
		case "secrets":
			if v.Kind != yaml.SequenceNode {
				add(v, "secrets must be a list of names like [OPENAI_KEY]")
				continue
			}
			for _, s := range v.Content {
				if !secretRe.MatchString(s.Value) {
					add(s, "secret name %q must be UPPER_SNAKE_CASE", s.Value)
					continue
				}
				r.Secrets = append(r.Secrets, s.Value)
			}
		case "egress":
			// egress: [pypi.org, "*.github.com"]        (allowlist, deny everything else)
			// egress: { allow: [...], deny_by_default: true }
			pol := &egress.Policy{}
			var list *yaml.Node
			switch v.Kind {
			case yaml.SequenceNode:
				list = v
			case yaml.MappingNode:
				for j := 0; j+1 < len(v.Content); j += 2 {
					ek, ev := v.Content[j], v.Content[j+1]
					switch ek.Value {
					case "allow":
						if ev.Kind != yaml.SequenceNode {
							add(ev, "egress.allow must be a list of domains/IPs/CIDRs")
							continue
						}
						list = ev
					case "deny_by_default":
						b, err := strconv.ParseBool(ev.Value)
						if err != nil {
							add(ev, "egress.deny_by_default must be true or false")
						}
						pol.DenyByDefault = b
					default:
						add(ek, "unknown egress key %q (allowed: allow, deny_by_default)", ek.Value)
					}
				}
			default:
				add(v, "egress must be a list or { allow: [...], deny_by_default: true }")
				continue
			}
			if list != nil {
				for _, e := range list.Content {
					r, err := egress.ParseEntry(e.Value)
					if err != nil {
						add(e, "egress entry: %s", err.Error())
						continue
					}
					pol.Allow = append(pol.Allow, r.String())
				}
			}
			r.Egress = pol
		case "setup":
			if v.Kind != yaml.SequenceNode {
				add(v, "setup must be a list of shell commands")
				continue
			}
			for _, s := range v.Content {
				if strings.TrimSpace(s.Value) == "" {
					add(s, "empty setup command")
					continue
				}
				r.Setup = append(r.Setup, s.Value)
			}
		}
	}
	if r.Template == "" && r.Profile == "" && r.Requirements == nil {
		errs = append(errs, LineError{Line: root.Line, Message: "template is required (base, python, node, python-browser or a custom template)"})
	}
	if (r.Profile != "" || r.Requirements != nil) && !seen["lifecycle"] {
		r.Lifecycle = "" // let the profile decide
	}
	if len(errs) > 0 {
		sort.SliceStable(errs, func(i, j int) bool { return errs[i].Line < errs[j].Line })
		return r, errs
	}
	r.YAML = src
	return r, nil
}

func sortedKeys() []string {
	ks := make([]string, 0, len(knownKeys))
	for k := range knownKeys {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// Builtin templates.
type Builtin struct {
	Name, Description, YAML string
}

var Builtins = []Builtin{
	{"base", "Ubuntu 24.04 with git, curl. Smallest and fastest.", "name: base\ntemplate: base\nspec: { cpu: 1, ram: 1G, disk: 10G }\nlifecycle: session\nidle_minutes: 5\n"},
	{"python", "Ubuntu 24.04 + Python 3, pip, venv.", "name: python\ntemplate: python\nspec: { cpu: 1, ram: 2G, disk: 10G }\nlifecycle: session\nidle_minutes: 5\nsetup: []\n"},
	{"node", "Ubuntu 24.04 + Node.js and npm.", "name: node\ntemplate: node\nspec: { cpu: 1, ram: 2G, disk: 10G }\nlifecycle: session\nidle_minutes: 5\n"},
	{"python-browser", "Python 3 + headless Chromium for scraping/automation.", "name: python-browser\ntemplate: python-browser\nspec: { cpu: 2, ram: 4G, disk: 20G }\nlifecycle: session\nidle_minutes: 5\n"},
}

// IsBuiltin reports whether a template name is built in.
func IsBuiltin(name string) bool {
	for _, b := range Builtins {
		if b.Name == name {
			return true
		}
	}
	return false
}
