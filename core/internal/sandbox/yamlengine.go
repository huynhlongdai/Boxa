package sandbox

import (
	"bufio"
	"bytes"
	"context"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// Declarative YAML adapter (spec v0.3 §3 "Adapter khai báo"): a provider is
// one YAML file of HTTP templates — create/get/delete/exec (+ optional
// pause/resume/stop/start/fork/files/preview) — with auth, JSON paths,
// capability and price metadata. Built-ins are embedded; more can be dropped
// into BOXAZ_SANDBOX_ADAPTERS_DIR without rebuilding core.
//
// Templates: {{cred.token}} {{id}} {{vars.x}} {{spec.cpu}} {{spec.ram_mb}}
// {{spec.ram_gb}} {{spec.disk_gb}} {{spec.template}} {{spec.region}}
// {{spec.name}} {{spec.timeout_s}} {{spec.timeout_min}} {{spec.env}}
// {{cmd}} (shell string incl. cwd/env) {{raw_cmd}} {{cwd}} {{timeout_s}}
// {{timeout_ms}} {{path}} {{content}} {{content_b64}} {{port}}.
// Prefixes: json: int: num: bool: (typed when the whole value is the
// placeholder), q: (query-escape), p: (path-escape), b64:, sh: (shell quote).
// A body key ending in "?" is dropped when its value renders empty.

//go:embed builtin/*.yaml
var builtinFS embed.FS

// YCall is one templated HTTP call.
type YCall struct {
	Method  string            `yaml:"method"`
	Path    string            `yaml:"path"`
	Base    string            `yaml:"base"` // overrides base_url (e.g. "{{vars.url}}")
	Headers map[string]string `yaml:"headers"`
	Body    any               `yaml:"body"`
	RawBody string            `yaml:"raw_body"` // sent as bytes ("{{content}}")
	Capture map[string]string `yaml:"capture"`  // name → JSON path ($.a.b[0]); "body" for raw responses
	Raw     bool              `yaml:"raw"`      // response is not JSON (captured as "body")
	NDJSON  *YNDJSON          `yaml:"ndjson"`
	OK      []int             `yaml:"ok"` // extra statuses treated as success (e.g. 404 on get)
}

// YNDJSON collects newline-delimited JSON output lines.
type YNDJSON struct {
	Stream string `yaml:"stream"` // JSON path of the stream name ("stdout"/"stderr")
	Data   string `yaml:"data"`   // JSON path of the text
	Exit   string `yaml:"exit"`   // JSON path of an exit code on any line
}

// YSpec is one YAML adapter file.
type YSpec struct {
	ID              string            `yaml:"id"`
	Name            string            `yaml:"name"`
	Tier            string            `yaml:"tier"`
	Verification    string            `yaml:"verification"`
	VerifiedNote    string            `yaml:"verified_note"`
	Docs            string            `yaml:"docs"`
	Website         string            `yaml:"website"`
	Initials        string            `yaml:"initials"`
	Color           string            `yaml:"color"`
	BaseURL         string            `yaml:"base_url"`
	Auth            map[string]string `yaml:"auth"` // header → value template
	Headers         map[string]string `yaml:"headers"`
	Credentials     []CredField       `yaml:"credentials"`
	EnvVars         []string          `yaml:"env_vars"`
	Capabilities    Capabilities      `yaml:"capabilities"`
	Pricing         Pricing           `yaml:"pricing"`
	Sizes           []Size            `yaml:"sizes"`
	Regions         []Region          `yaml:"regions"`
	Templates       []string          `yaml:"templates"`
	DefaultTemplate string            `yaml:"default_template"`
	WorkDir         string            `yaml:"workdir"`
	Notes           []string          `yaml:"notes"`
	States          map[string]string `yaml:"states"`   // provider state → running|starting|paused|stopped|error|deleted
	RefVars         []string          `yaml:"ref_vars"` // captured vars kept in the sandbox ref (survive restarts)
	Validate        *YCall            `yaml:"validate"`
	Create          *YCall            `yaml:"create"`
	Get             *YCall            `yaml:"get"`
	Delete          *YCall            `yaml:"delete"`
	Pause           *YCall            `yaml:"pause"`
	Resume          *YCall            `yaml:"resume"`
	Stop            *YCall            `yaml:"stop"`
	Start           *YCall            `yaml:"start"`
	Fork            *YCall            `yaml:"fork"`
	Preview         *YCall            `yaml:"preview"`
	Exec            []YCall           `yaml:"exec"`
	PutFile         []YCall           `yaml:"put_file"`
	GetFile         []YCall           `yaml:"get_file"`
}

// YAMLDriver runs a YSpec.
type YAMLDriver struct {
	S    YSpec
	h    *httpClient
	base string // test override of base_url
	mu   sync.Mutex
	vars map[string]map[string]string // sandbox id → captured vars
}

// ParseYAML parses and checks one adapter.
func ParseYAML(b []byte) (*YSpec, error) {
	var s YSpec
	if err := yaml.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	var miss []string
	for k, ok := range map[string]bool{"id": s.ID != "", "name": s.Name != "", "base_url": s.BaseURL != "", "create": s.Create != nil,
		"get": s.Get != nil, "delete": s.Delete != nil, "exec": len(s.Exec) > 0, "validate": s.Validate != nil} {
		if !ok {
			miss = append(miss, k)
		}
	}
	if len(miss) > 0 {
		sort.Strings(miss)
		return nil, fmt.Errorf("adapter %q: missing %s", s.ID, strings.Join(miss, ", "))
	}
	if s.Tier == "" {
		s.Tier = "T3"
		if s.Capabilities.Background {
			s.Tier = "T2"
		}
	}
	if len(s.Credentials) == 0 {
		s.Credentials = []CredField{{Name: "token", Label: "API key", Secret: true}}
	}
	return &s, nil
}

// NewYAMLDriver builds a driver (base overrides base_url; "" = as declared).
func NewYAMLDriver(s YSpec, c *http.Client, base string) *YAMLDriver {
	return &YAMLDriver{S: s, h: newHTTP(c), base: base, vars: map[string]map[string]string{}}
}

// BuiltinYAML loads the embedded adapters plus BOXAZ_SANDBOX_ADAPTERS_DIR.
func BuiltinYAML(c *http.Client) ([]*YAMLDriver, error) {
	var out []*YAMLDriver
	var errs []string
	ents, _ := builtinFS.ReadDir("builtin")
	for _, e := range ents {
		b, _ := builtinFS.ReadFile("builtin/" + e.Name())
		s, err := ParseYAML(b)
		if err != nil {
			errs = append(errs, e.Name()+": "+err.Error())
			continue
		}
		out = append(out, NewYAMLDriver(*s, c, ""))
	}
	if dir := os.Getenv("BOXAZ_SANDBOX_ADAPTERS_DIR"); dir != "" {
		files, _ := filepath.Glob(filepath.Join(dir, "*.yaml"))
		for _, f := range files {
			b, err := os.ReadFile(f)
			if err == nil {
				var s *YSpec
				if s, err = ParseYAML(b); err == nil {
					out = append(out, NewYAMLDriver(*s, c, ""))
					continue
				}
			}
			errs = append(errs, f+": "+err.Error())
		}
	}
	if len(errs) > 0 {
		return out, fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return out, nil
}

func (y *YAMLDriver) Info() Info {
	s := y.S
	return Info{ID: s.ID, Name: s.Name, Tier: s.Tier, Adapter: "yaml", Verification: orStr(s.Verification, VerifiedDocs), VerifiedNote: s.VerifiedNote,
		Docs: s.Docs, Website: s.Website, Initials: s.Initials, Color: s.Color, Caps: s.Capabilities, Pricing: s.Pricing, Sizes: s.Sizes,
		Regions: s.Regions, Templates: s.Templates, DefaultTemplate: s.DefaultTemplate, WorkDir: s.WorkDir, Credentials: s.Credentials, EnvVars: s.EnvVars, Notes: s.Notes}
}

// ---- refs ----

// splitRef parses "id#k=v&k2=v2".
func splitRef(ref string) (string, map[string]string) {
	vars := map[string]string{}
	id, q, ok := strings.Cut(ref, "#")
	if ok {
		if vs, err := url.ParseQuery(q); err == nil {
			for k := range vs {
				vars[k] = vs.Get(k)
			}
		}
	}
	return id, vars
}

func (y *YAMLDriver) refFor(id string, vars map[string]string) string {
	if len(y.S.RefVars) == 0 {
		return id
	}
	q := url.Values{}
	for _, k := range y.S.RefVars {
		if v := vars[k]; v != "" {
			q.Set(k, v)
		}
	}
	if len(q) == 0 {
		return id
	}
	return id + "#" + q.Encode()
}

func (y *YAMLDriver) ctxFor(c Cred, ref string) map[string]string {
	id, rv := splitRef(ref)
	v := map[string]string{"id": id, "ref": ref}
	for k, x := range c {
		v["cred."+k] = x
	}
	for _, f := range y.S.Credentials {
		if _, ok := v["cred."+f.Name]; !ok && f.Default != "" {
			v["cred."+f.Name] = f.Default
		}
	}
	y.mu.Lock()
	for k, x := range y.vars[id] {
		v["vars."+k] = x
	}
	y.mu.Unlock()
	for k, x := range rv {
		v["vars."+k] = x
	}
	return v
}

func (y *YAMLDriver) remember(id string, vars map[string]string) {
	y.mu.Lock()
	defer y.mu.Unlock()
	m := y.vars[id]
	if m == nil {
		m = map[string]string{}
		y.vars[id] = m
	}
	for k, x := range vars {
		m[k] = x
	}
}

// ---- templating ----

var phRe = regexp.MustCompile(`\{\{\s*([a-zA-Z0-9_.:\-]+)\s*\}\}`)

func lookup(v map[string]string, key string) (string, string) {
	format := ""
	if i := strings.Index(key, ":"); i > 0 {
		format, key = key[:i], key[i+1:]
	}
	return v[key], format
}

func applyFormat(val, format string) string {
	switch format {
	case "q":
		return url.QueryEscape(val)
	case "p":
		return url.PathEscape(val)
	case "b64":
		return base64.StdEncoding.EncodeToString([]byte(val))
	case "sh":
		return shq(val)
	}
	return val
}

func render(s string, v map[string]string) string {
	return phRe.ReplaceAllStringFunc(s, func(m string) string {
		val, f := lookup(v, phRe.FindStringSubmatch(m)[1])
		return applyFormat(val, f)
	})
}

// renderValue renders a YAML body tree into JSON-ready values.
func renderValue(x any, v map[string]string) (any, bool) {
	switch t := x.(type) {
	case string:
		if m := phRe.FindStringSubmatch(t); m != nil && strings.TrimSpace(t) == m[0] {
			val, f := lookup(v, m[1])
			switch f {
			case "json":
				if val == "" {
					return nil, false
				}
				var j any
				if json.Unmarshal([]byte(val), &j) == nil {
					return j, true
				}
				return val, true
			case "int":
				fv, err := strconv.ParseFloat(val, 64)
				if err != nil {
					return nil, false
				}
				return int64(fv + 0.5), true
			case "num":
				fv, err := strconv.ParseFloat(val, 64)
				if err != nil {
					return nil, false
				}
				return fv, true
			case "bool":
				return val == "true" || val == "1", val != ""
			}
			r := applyFormat(val, f)
			return r, r != ""
		}
		r := render(t, v)
		return r, r != ""
	case map[string]any:
		out := map[string]any{}
		for k, sub := range t {
			opt := strings.HasSuffix(k, "?")
			key := strings.TrimSuffix(k, "?")
			rv, ok := renderValue(sub, v)
			if opt && (!ok || rv == nil || rv == int64(0) || rv == float64(0)) {
				continue
			}
			out[key] = rv
		}
		return out, true
	case []any:
		out := []any{}
		for _, sub := range t {
			rv, _ := renderValue(sub, v)
			out = append(out, rv)
		}
		return out, true
	}
	return x, x != nil
}

// JSONPath evaluates $.a.b[0].c (or "$") against decoded JSON; alternatives
// separated by "|" are tried in order.
func JSONPath(doc any, path string) (any, bool) {
	for _, alt := range strings.Split(path, "|") {
		if v, ok := jsonPath1(doc, strings.TrimSpace(alt)); ok {
			return v, true
		}
	}
	return nil, false
}

func jsonPath1(doc any, p string) (any, bool) {
	p = strings.TrimPrefix(strings.TrimPrefix(p, "$"), ".")
	cur := doc
	if p == "" {
		return cur, cur != nil
	}
	for _, seg := range strings.Split(p, ".") {
		name := seg
		var idx []int
		for {
			i := strings.Index(name, "[")
			if i < 0 {
				break
			}
			j := strings.Index(name[i:], "]")
			if j < 0 {
				return nil, false
			}
			n, err := strconv.Atoi(name[i+1 : i+j])
			if err != nil {
				return nil, false
			}
			idx = append(idx, n)
			name = name[:i] + name[i+j+1:]
		}
		if name != "" {
			m, ok := cur.(map[string]any)
			if !ok {
				return nil, false
			}
			if cur, ok = m[name]; !ok {
				return nil, false
			}
		}
		for _, n := range idx {
			a, ok := cur.([]any)
			if !ok || n < 0 || n >= len(a) {
				return nil, false
			}
			cur = a[n]
		}
	}
	return cur, cur != nil
}

func asString(x any) string {
	switch t := x.(type) {
	case nil:
		return ""
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	}
	b, _ := json.Marshal(x)
	return string(b)
}

// ---- HTTP ----

// run executes one call and returns the captured values.
func (y *YAMLDriver) run(ctx context.Context, call *YCall, v map[string]string) (map[string]string, error) {
	base := y.S.BaseURL
	if y.base != "" {
		base = y.base
	}
	if b := v["cred.base_url"]; b != "" {
		base = b
	}
	if call.Base != "" {
		base = render(call.Base, v)
	}
	u := strings.TrimRight(render(base, v), "/") + render(call.Path, v)
	var body io.Reader
	ct := ""
	if call.RawBody != "" {
		raw := render(call.RawBody, v)
		if strings.HasPrefix(call.RawBody, "{{content}}") && v["content_raw"] != "" {
			raw = v["content_raw"]
		}
		body, ct = strings.NewReader(raw), "application/octet-stream"
	} else if call.Body != nil {
		rv, _ := renderValue(call.Body, v)
		jb, err := json.Marshal(rv)
		if err != nil {
			return nil, err
		}
		body, ct = bytes.NewReader(jb), "application/json"
	}
	method := strings.ToUpper(orStr(call.Method, "GET"))
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	if ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	req.Header.Set("User-Agent", y.h.ua)
	for k, t := range y.S.Headers {
		req.Header.Set(k, render(t, v))
	}
	for k, t := range y.S.Auth {
		req.Header.Set(k, render(t, v))
	}
	for k, t := range call.Headers {
		req.Header.Set(k, render(t, v))
	}
	resp, err := y.h.c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	out := map[string]string{"_status": strconv.Itoa(resp.StatusCode)}
	okExtra := false
	for _, s := range call.OK {
		okExtra = okExtra || s == resp.StatusCode
	}
	if resp.StatusCode >= 300 && !okExtra {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, &HTTPError{Status: resp.StatusCode, Body: trim(string(b), 600)}
	}
	if call.NDJSON != nil {
		var so, se strings.Builder
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 1<<20), 16<<20)
		for sc.Scan() {
			line := bytes.TrimSpace(sc.Bytes())
			if len(line) == 0 {
				continue
			}
			var doc any
			if json.Unmarshal(line, &doc) != nil {
				continue
			}
			if call.NDJSON.Exit != "" {
				if x, ok := JSONPath(doc, call.NDJSON.Exit); ok {
					out["exit_code"] = asString(x)
				}
			}
			st, _ := JSONPath(doc, call.NDJSON.Stream)
			dt, ok := JSONPath(doc, call.NDJSON.Data)
			if !ok {
				continue
			}
			if s, isStr := dt.(string); isStr {
				if asString(st) == "stderr" {
					se.WriteString(s)
				} else {
					so.WriteString(s)
				}
			}
		}
		out["stdout"], out["stderr"] = so.String(), se.String()
		return out, nil
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if call.Raw {
		out["body"] = string(b)
		return out, nil
	}
	if len(call.Capture) == 0 || len(bytes.TrimSpace(b)) == 0 {
		return out, nil
	}
	var doc any
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("%s %s: response is not JSON: %s", method, call.Path, trim(string(b), 200))
	}
	for name, path := range call.Capture {
		if x, ok := JSONPath(doc, path); ok {
			out[name] = asString(x)
		}
	}
	return out, nil
}

func (y *YAMLDriver) runSteps(ctx context.Context, steps []YCall, v map[string]string) (map[string]string, error) {
	acc := map[string]string{}
	for i := range steps {
		got, err := y.run(ctx, &steps[i], v)
		if err != nil {
			return nil, err
		}
		for k, x := range got {
			acc[k] = x
			v["vars."+k] = x
			if k == "stdout" || k == "stderr" || k == "exit_code" || k == "body" {
				v[k] = x
			}
		}
	}
	return acc, nil
}

// ---- Driver ----

func (y *YAMLDriver) Validate(ctx context.Context, c Cred) error {
	_, err := y.run(ctx, y.S.Validate, y.ctxFor(c, ""))
	return err
}

func (y *YAMLDriver) specVars(v map[string]string, s Spec) {
	tpl := orStr(s.Template, y.S.DefaultTemplate)
	env, _ := json.Marshal(nonNilMap(s.Env))
	labels, _ := json.Marshal(nonNilMap(s.Labels))
	t := s.TimeoutS
	if t <= 0 {
		t = 3600
	}
	if m := y.S.Capabilities.MaxLifetimeS; m > 0 && t > m {
		t = m
	}
	for k, x := range map[string]string{"spec.name": s.Name, "spec.template": tpl, "spec.region": s.Region, "spec.sku": s.SKU,
		"spec.cpu": strconv.FormatFloat(s.CPU, 'f', -1, 64), "spec.ram_mb": strconv.Itoa(s.RAMMB), "spec.ram_gb": strconv.Itoa((s.RAMMB + 1023) / 1024),
		"spec.disk_gb": strconv.Itoa(s.DiskGB), "spec.env": string(env), "spec.labels": string(labels),
		"spec.timeout_s": strconv.Itoa(t), "spec.timeout_ms": strconv.Itoa(t * 1000), "spec.timeout_min": strconv.Itoa((t + 59) / 60)} {
		v[k] = x
	}
}

func (y *YAMLDriver) Create(ctx context.Context, c Cred, s Spec) (*Sandbox, error) {
	v := y.ctxFor(c, "")
	y.specVars(v, s)
	got, err := y.run(ctx, y.S.Create, v)
	if err != nil {
		return nil, err
	}
	id := got["id"]
	if id == "" {
		return nil, fmt.Errorf("%s create: id not found at %s", y.S.Name, y.S.Create.Capture["id"])
	}
	vars := map[string]string{}
	for k, x := range got {
		if k != "id" && k != "state" && !strings.HasPrefix(k, "_") {
			vars[k] = x
		}
	}
	y.remember(id, vars)
	st := y.state(got["state"])
	if got["state"] == "" {
		st = StateStarting
	}
	return &Sandbox{ID: y.refFor(id, vars), State: st, Region: s.Region, Template: v["spec.template"], Meta: vars}, nil
}

func (y *YAMLDriver) state(s string) string {
	if s == "" {
		return StateUnknown
	}
	if m, ok := y.S.States[strings.ToLower(s)]; ok {
		return m
	}
	if m, ok := y.S.States[s]; ok {
		return m
	}
	switch strings.ToLower(s) {
	case StateRunning, StateStarting, StatePaused, StateStopped, StateError, StateDeleted:
		return strings.ToLower(s)
	}
	return StateUnknown
}

func (y *YAMLDriver) Get(ctx context.Context, c Cred, ref string) (*Sandbox, error) {
	v := y.ctxFor(c, ref)
	got, err := y.run(ctx, y.S.Get, v)
	if err != nil {
		if he, ok := err.(*HTTPError); ok && he.Status == 404 {
			return &Sandbox{ID: ref, State: StateDeleted}, nil
		}
		return nil, err
	}
	if got["_status"] == "404" {
		return &Sandbox{ID: ref, State: StateDeleted}, nil
	}
	id, _ := splitRef(ref)
	vars := map[string]string{}
	for k, x := range got {
		if k != "state" && !strings.HasPrefix(k, "_") {
			vars[k] = x
		}
	}
	y.remember(id, vars)
	return &Sandbox{ID: ref, State: y.state(got["state"]), Meta: vars}, nil
}

func (y *YAMLDriver) Delete(ctx context.Context, c Cred, ref string) error {
	_, err := y.run(ctx, y.S.Delete, y.ctxFor(c, ref))
	if err == nil {
		id, _ := splitRef(ref)
		y.mu.Lock()
		delete(y.vars, id)
		y.mu.Unlock()
	}
	return err
}

func (y *YAMLDriver) Exec(ctx context.Context, c Cred, ref string, r ExecRequest) (*ExecResult, error) {
	v := y.ctxFor(c, ref)
	t := r.TimeoutS
	if t <= 0 {
		t = 300
	}
	env, _ := json.Marshal(nonNilMap(r.Env))
	v["cmd"], v["raw_cmd"], v["cwd"], v["env"] = ShellCommand(r), r.Command, r.Cwd, string(env)
	v["timeout_s"], v["timeout_ms"] = strconv.Itoa(t), strconv.Itoa(t*1000)
	start := time.Now()
	got, err := y.runSteps(ctx, y.S.Exec, v)
	if err != nil {
		return nil, err
	}
	res := &ExecResult{Stdout: orStr(got["stdout"], got["output"]), Stderr: got["stderr"], DurationMS: time.Since(start).Milliseconds()}
	if s := got["exit_code"]; s != "" {
		f, _ := strconv.ParseFloat(s, 64)
		res.ExitCode = int(f)
	}
	if got["timed_out"] == "true" {
		res.TimedOut = true
	}
	return res, nil
}

func (y *YAMLDriver) opt(ctx context.Context, call *YCall, c Cred, ref string) error {
	if call == nil {
		return fmt.Errorf("%s: not supported", y.S.Name)
	}
	_, err := y.run(ctx, call, y.ctxFor(c, ref))
	return err
}

// yamlPauser etc. are only exposed when the YAML declares the verbs (see Wrap).
type yamlPauser struct{ *YAMLDriver }

func (y yamlPauser) Pause(ctx context.Context, c Cred, id string) error {
	return y.opt(ctx, y.S.Pause, c, id)
}
func (y yamlPauser) Resume(ctx context.Context, c Cred, id string) error {
	return y.opt(ctx, y.S.Resume, c, id)
}

func (y *YAMLDriver) putFile(ctx context.Context, c Cred, ref, path string, data []byte) error {
	v := y.ctxFor(c, ref)
	v["path"], v["content"], v["content_raw"], v["content_b64"] = path, string(data), string(data), base64.StdEncoding.EncodeToString(data)
	_, err := y.runSteps(ctx, y.S.PutFile, v)
	return err
}

func (y *YAMLDriver) getFile(ctx context.Context, c Cred, ref, path string) ([]byte, error) {
	v := y.ctxFor(c, ref)
	v["path"] = path
	got, err := y.runSteps(ctx, y.S.GetFile, v)
	if err != nil {
		return nil, err
	}
	if b64 := got["content_b64"]; b64 != "" {
		return base64.StdEncoding.DecodeString(b64)
	}
	return []byte(orStr(got["body"], got["content"])), nil
}

type yamlFiler struct{ *YAMLDriver }

func (y yamlFiler) PutFile(ctx context.Context, c Cred, id, path string, data []byte) error {
	return y.putFile(ctx, c, id, path, data)
}
func (y yamlFiler) GetFile(ctx context.Context, c Cred, id, path string) ([]byte, error) {
	return y.getFile(ctx, c, id, path)
}

// Wrap returns the YAML driver with exactly the optional interfaces its
// file declares (so capability checks by type assertion stay honest).
func (y *YAMLDriver) Wrap() Driver {
	hasPause := y.S.Pause != nil && y.S.Resume != nil
	hasFiles := len(y.S.PutFile) > 0 && len(y.S.GetFile) > 0
	switch {
	case hasPause && hasFiles:
		return &struct {
			*YAMLDriver
			yamlPauser
			yamlFiler
		}{y, yamlPauser{y}, yamlFiler{y}}
	case hasPause:
		return &struct {
			*YAMLDriver
			yamlPauser
		}{y, yamlPauser{y}}
	case hasFiles:
		return &struct {
			*YAMLDriver
			yamlFiler
		}{y, yamlFiler{y}}
	}
	return y
}
