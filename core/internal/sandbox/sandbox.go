// Package sandbox implements the T2 "sandbox API" and T3 "exec-only" provider
// tiers (spec v0.3 §3). A Driver talks to one sandbox provider (E2B, Novita,
// Daytona, Prized, boxd.sh natively, or any provider described by a built-in
// declarative YAML adapter). AsProvider wraps a Driver into the generic
// provider.Provider so the provider manager (credentials, health, breaker,
// catalog, selector, fallback chain) treats sandboxes like any other source
// of boxes.
//
// T2: the sandbox can keep a background process alive, so core starts
// `boxd --dial` inside it and the sandbox joins Boxaz like any host.
// T3: the provider only offers request/response exec + files, so a
// gateway-side Shim answers boxd's RPCs by translating them to the API.
package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"boxaz/core/internal/model"
)

// Normalised sandbox states.
const (
	StateStarting = "starting"
	StateRunning  = "running"
	StatePaused   = "paused"
	StateStopped  = "stopped"
	StateError    = "error"
	StateDeleted  = "deleted"
	StateUnknown  = "unknown"
)

// Verification levels of an adapter (shown as a badge in the UI).
const (
	VerifiedLive = "live-verified" // read-only calls against the real API succeeded
	VerifiedDocs = "docs-verified" // endpoints taken from the provider's official API reference
	VerifiedSDK  = "sdk-source"    // endpoints read from the provider's official SDK source (no public REST reference)
)

// Spec asks a provider for a new sandbox.
type Spec struct {
	Name     string            `json:"name"`
	Template string            `json:"template"`
	Region   string            `json:"region"`
	SKU      string            `json:"sku"`
	CPU      float64           `json:"cpu"`
	RAMMB    int               `json:"ram_mb"`
	DiskGB   int               `json:"disk_gb"`
	Env      map[string]string `json:"env,omitempty"`
	Labels   map[string]string `json:"labels,omitempty"`
	TimeoutS int               `json:"timeout_s"` // max lifetime requested (0 = provider default)
	// PrivateTraffic asks the provider to refuse requests to the sandbox's
	// preview host that do not carry the sandbox's traffic access token
	// (E2B network.allowPublicTraffic=false). Only drivers implementing
	// TrafficGuard honour it.
	PrivateTraffic bool `json:"private_traffic,omitempty"`
}

// Sandbox is a created sandbox as seen by the provider.
type Sandbox struct {
	ID       string            `json:"id"`
	State    string            `json:"state"`
	Region   string            `json:"region,omitempty"`
	Template string            `json:"template,omitempty"`
	Meta     map[string]string `json:"meta,omitempty"` // toolbox base, access tokens, domains…
}

// ExecRequest runs one shell command (bash -c semantics).
type ExecRequest struct {
	Command  string            `json:"command"`
	Cwd      string            `json:"cwd,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
	TimeoutS int               `json:"timeout_s,omitempty"`
}

// ExecResult is the outcome of an exec.
type ExecResult struct {
	ExitCode   int    `json:"exit_code"`
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	DurationMS int64  `json:"duration_ms"`
	TimedOut   bool   `json:"timed_out,omitempty"`
}

// Capabilities is the capability vector of a provider (spec §2.4).
type Capabilities struct {
	Background   bool    `json:"background" yaml:"background"` // a detached process survives the exec call (T2)
	Pause        bool    `json:"pause" yaml:"pause"`
	Snapshot     bool    `json:"snapshot" yaml:"snapshot"`
	Fork         bool    `json:"fork" yaml:"fork"`
	Ports        bool    `json:"ports" yaml:"ports"` // preview URL / public port
	PTY          bool    `json:"pty" yaml:"pty"`
	Files        bool    `json:"files" yaml:"files"` // native file put/get (else via exec)
	GPU          bool    `json:"gpu" yaml:"gpu"`
	BootMS       int     `json:"boot_ms" yaml:"boot_ms"`               // typical create→running
	MaxLifetimeS int     `json:"max_lifetime_s" yaml:"max_lifetime_s"` // 0 = unlimited / not documented
	MaxCPU       float64 `json:"max_cpu" yaml:"max_cpu"`
	MaxRAMMB     int     `json:"max_ram_mb" yaml:"max_ram_mb"`
	Isolation    string  `json:"isolation" yaml:"isolation"` // microvm | gvisor | container | vm
}

// Pricing is per-second pricing of a running sandbox (USD).
type Pricing struct {
	CPUSecond    float64 `json:"cpu_second" yaml:"cpu_second"`         // per vCPU-second
	GBSecond     float64 `json:"gb_second" yaml:"gb_second"`           // per GiB RAM-second
	DiskGBSecond float64 `json:"disk_gb_second" yaml:"disk_gb_second"` // per GiB disk-second (0 = included)
	FlatSecond   float64 `json:"flat_second" yaml:"flat_second"`       // per sandbox-second (0 = none)
	Source       string  `json:"source" yaml:"source"`                 // pricing page URL
	Confidence   string  `json:"confidence" yaml:"confidence"`         // docs | estimated
	Note         string  `json:"note,omitempty" yaml:"note"`
}

// PerHour prices a running shape for one hour.
func (p Pricing) PerHour(cpu float64, ramMB, diskGB int) float64 {
	s := p.CPUSecond*cpu + p.GBSecond*float64(ramMB)/1024 + p.DiskGBSecond*float64(diskGB) + p.FlatSecond
	return model.Round6(s * 3600)
}

// Size is a sandbox shape offered in the catalog.
type Size struct {
	SKU    string  `json:"sku" yaml:"sku"`
	CPU    float64 `json:"cpu" yaml:"cpu"`
	RAMMB  int     `json:"ram_mb" yaml:"ram_mb"`
	DiskGB int     `json:"disk_gb" yaml:"disk_gb"`
}

// Region is a provider region.
type Region struct {
	ID   string `json:"id" yaml:"id"`
	Name string `json:"name" yaml:"name"`
}

// CredField is one credential input of the connect wizard.
type CredField struct {
	Name     string `json:"name" yaml:"name"`
	Label    string `json:"label" yaml:"label"`
	Secret   bool   `json:"secret" yaml:"secret"`
	Optional bool   `json:"optional" yaml:"optional"`
	Default  string `json:"default,omitempty" yaml:"default"`
	Hint     string `json:"hint,omitempty" yaml:"hint"`
}

// Info is the static description of a sandbox provider.
type Info struct {
	ID              string       `json:"id"`
	Name            string       `json:"name"`
	Tier            string       `json:"tier"`    // T2 | T3 (default execution path)
	Adapter         string       `json:"adapter"` // native | yaml
	Verification    string       `json:"verification"`
	VerifiedNote    string       `json:"verified_note,omitempty"`
	Docs            string       `json:"docs"`
	Website         string       `json:"website"`
	Initials        string       `json:"initials"`
	Color           string       `json:"color"`
	Caps            Capabilities `json:"capabilities"`
	Pricing         Pricing      `json:"pricing"`
	Sizes           []Size       `json:"sizes"`
	Regions         []Region     `json:"regions"`
	Templates       []string     `json:"templates"`
	DefaultTemplate string       `json:"default_template"`
	WorkDir         string       `json:"workdir"` // writable home for boxd / the shim's /work
	Credentials     []CredField  `json:"credentials"`
	EnvVars         []string     `json:"env_vars"` // env var(s) read at startup (joined like Credentials)
	Notes           []string     `json:"notes,omitempty"`
}

// Driver is the T2/T3 driver interface. Optional verbs are separate interfaces.
type Driver interface {
	Info() Info
	Validate(ctx context.Context, c Cred) error
	Create(ctx context.Context, c Cred, s Spec) (*Sandbox, error)
	Get(ctx context.Context, c Cred, id string) (*Sandbox, error)
	Delete(ctx context.Context, c Cred, id string) error
	Exec(ctx context.Context, c Cred, id string, r ExecRequest) (*ExecResult, error)
}

// Pauser can pause/resume (memory kept, compute billing stops).
type Pauser interface {
	Pause(ctx context.Context, c Cred, id string) error
	Resume(ctx context.Context, c Cred, id string) error
}

// Starter can stop/start (disk kept, memory lost).
type Starter interface {
	Stop(ctx context.Context, c Cred, id string) error
	Start(ctx context.Context, c Cred, id string) error
}

// Filer moves files natively (otherwise the shim uses exec).
type Filer interface {
	PutFile(ctx context.Context, c Cred, id, path string, data []byte) error
	GetFile(ctx context.Context, c Cred, id, path string) ([]byte, error)
}

// Forker forks a running sandbox into a new one.
type Forker interface {
	Fork(ctx context.Context, c Cred, id string) (*Sandbox, error)
}

// Snapshotter takes a provider-side snapshot.
type Snapshotter interface {
	Snapshot(ctx context.Context, c Cred, id, name string) (string, error)
}

// Previewer returns a public/preview URL for a port inside the sandbox.
type Previewer interface {
	PreviewURL(ctx context.Context, c Cred, id string, port int) (string, error)
}

// TrafficGuard is implemented by drivers whose preview host can refuse
// public traffic (v1.8): sandboxes created with Spec.PrivateTraffic answer
// 403 unless a request carries the headers PreviewHeaders returns, which only
// the Boxaz gateway adds upstream.
type TrafficGuard interface {
	PrivateTrafficSupported() bool
	PreviewHeaders(ctx context.Context, c Cred, id string) (map[string]string, error)
}

// MetaPrivateTraffic is set to "1" in Sandbox.Meta (and provider.Handle.Meta)
// when the provider confirmed the sandbox refuses public preview traffic.
const MetaPrivateTraffic = "private_traffic"

// Lister lists sandboxes at the provider.
type Lister interface {
	List(ctx context.Context, c Cred) ([]Sandbox, error)
}

// Cred is a provider credential: {"token": "...", other fields}. A plain
// string token is stored as-is; multi-field credentials as a JSON object.
type Cred map[string]string

// ParseCred decodes a stored token.
func ParseCred(s string) Cred {
	s = strings.TrimSpace(s)
	c := Cred{}
	if strings.HasPrefix(s, "{") {
		if err := json.Unmarshal([]byte(s), &c); err == nil {
			return c
		}
	}
	c["token"] = s
	return c
}

// Token returns the main API key.
func (c Cred) Token() string { return c["token"] }

// Get returns a field or a default.
func (c Cred) Get(k, def string) string {
	if v := strings.TrimSpace(c[k]); v != "" {
		return v
	}
	return def
}

// String encodes the credential for storage.
func (c Cred) String() string {
	if len(c) == 1 {
		if t, ok := c["token"]; ok {
			return t
		}
	}
	b, _ := json.Marshal(map[string]string(c))
	return string(b)
}

// ---- errors ----

// HTTPError is a non-2xx answer from a provider API.
type HTTPError struct {
	Status int
	Body   string
}

func (e *HTTPError) Error() string { return fmt.Sprintf("HTTP %d: %s", e.Status, e.Body) }

// wrapErr turns provider errors into Boxaz API errors (classified by provhealth).
func wrapErr(name string, err error) error {
	if err == nil {
		return nil
	}
	if _, ok := err.(*model.APIError); ok {
		return err
	}
	if he, ok := err.(*HTTPError); ok {
		switch {
		case he.Status == 401 || he.Status == 403:
			return model.EH(model.ErrUnauthorized, "Create an API key in the "+name+" console and paste it again.", "%s rejected the key (%d): %s", name, he.Status, trim(he.Body, 200))
		case he.Status == 429:
			return model.EH(model.ErrProviderFailed, "The provider is rate limiting or a quota is full; Boxaz backs off and fails over.", "%s rate limited (429): %s", name, trim(he.Body, 200))
		case he.Status == 402 || strings.Contains(he.Body, "balance") || strings.Contains(he.Body, "quota"):
			return model.EH(model.ErrProviderFailed, "Top up or raise the quota in the provider console.", "%s quota/balance (%d): %s", name, he.Status, trim(he.Body, 200))
		}
	}
	return model.EH(model.ErrProviderFailed, "Check network access to the provider API and retry.", "%s API error: %v", name, err)
}

func trim(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n]
	}
	return s
}

// ---- HTTP helper ----

// Provider-visible identifiers. Everything a provider stores or logs about
// our sandboxes (names, labels/metadata, user agent) stays neutral: the
// product name never goes to a third party.
const (
	NamePrefix   = "mgr-"             // sandbox / VM names
	LabelManaged = "managed"          // label/metadata key on every sandbox we create ("1")
	LabelHost    = "mgr-host"         // label/metadata key: host id
	UserAgent    = "sandbox-client/1" // HTTP User-Agent toward providers
)

// ManagedLabels are the labels/metadata put on a sandbox for host hostID.
func ManagedLabels(hostID string) map[string]string {
	return map[string]string{LabelManaged: "1", LabelHost: hostID}
}

// httpClient is shared by the native drivers.
type httpClient struct {
	c  *http.Client
	ua string
}

func newHTTP(c *http.Client) *httpClient {
	if c == nil {
		c = &http.Client{Timeout: 60 * time.Second}
	}
	return &httpClient{c: c, ua: UserAgent}
}

// do sends a request. body may be nil, []byte (raw) or any (JSON). out may be
// nil, *[]byte (raw) or any (JSON).
func (h *httpClient) do(ctx context.Context, method, url string, headers map[string]string, body, out any) error {
	var rd io.Reader
	ct := ""
	switch b := body.(type) {
	case nil:
	case []byte:
		rd = bytes.NewReader(b)
		ct = "application/octet-stream"
	case io.Reader:
		rd = b
	default:
		jb, err := json.Marshal(b)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(jb)
		ct = "application/json"
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		return err
	}
	if ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	req.Header.Set("User-Agent", h.ua)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := h.c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if resp.StatusCode >= 300 {
		return &HTTPError{Status: resp.StatusCode, Body: trim(string(b), 600)}
	}
	switch o := out.(type) {
	case nil:
	case *[]byte:
		*o = b
	default:
		if len(bytes.TrimSpace(b)) > 0 {
			return json.Unmarshal(b, out)
		}
	}
	return nil
}

// ---- waiting ----

// WaitRunning polls Get until the sandbox runs (or fails / times out).
func WaitRunning(ctx context.Context, d Driver, c Cred, id string, timeout time.Duration) (*Sandbox, error) {
	deadline := time.Now().Add(timeout)
	delay := 300 * time.Millisecond
	for {
		sb, err := d.Get(ctx, c, id)
		if err == nil {
			switch sb.State {
			case StateRunning:
				return sb, nil
			case StateError, StateDeleted:
				return sb, model.EH(model.ErrProviderFailed, "The provider reported a failed sandbox; Boxaz fails over to the next provider.", "%s sandbox %s is %s", d.Info().Name, id, sb.State)
			}
		}
		if time.Now().After(deadline) {
			if err != nil {
				return nil, err
			}
			return sb, model.EH(model.ErrProviderFailed, "The sandbox did not start in time; retry or pick another provider.", "%s sandbox %s still %s after %s", d.Info().Name, id, sb.State, timeout)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
		if delay < 3*time.Second {
			delay = delay * 3 / 2
		}
	}
}

// ---- bootstrap (T2) ----

// The in-sandbox dirs (.boxd here, .box for exec session state) are neutral on
// purpose: bootstrap and exec commands travel through the provider API, and
// the product name never goes to a provider.

// BootstrapParams fill the in-sandbox boxd start command.
type BootstrapParams struct {
	CoreURL        string // ws(s)://core/v1/hosts/connect
	BootstrapToken string
	BoxdURL        string // download base; "-linux-<arch>" is appended
	Provider       string
	Region         string
	WorkDir        string // writable dir (default $HOME)
}

// shq single-quotes s for sh.
func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'" }

// BootstrapScript is the shell command run once inside a T2 sandbox: it
// downloads boxd and starts `boxd --dial <core>` detached (setsid + nohup),
// so the sandbox dials out to core and registers as a single-box host.
func BootstrapScript(p BootstrapParams) string {
	dir := `"$HOME/.boxd"`
	if p.WorkDir != "" {
		dir = shq(strings.TrimRight(p.WorkDir, "/") + "/.boxd")
	}
	var b strings.Builder
	b.WriteString("set -e; D=" + dir + "; mkdir -p \"$D/state\"; ")
	b.WriteString(`A=$(uname -m); case "$A" in x86_64|amd64) A=amd64;; aarch64|arm64) A=arm64;; esac; `)
	b.WriteString("U=" + shq(p.BoxdURL) + "-linux-$A; ")
	b.WriteString(`if command -v curl >/dev/null 2>&1; then curl -fsSL "$U" -o "$D/boxd"; else wget -qO "$D/boxd" "$U"; fi; chmod +x "$D/boxd"; `)
	b.WriteString("cd \"$D\"; ")
	b.WriteString("BOXD_TOKEN=" + shq(p.BootstrapToken) + " BOXD_DRIVER=sandbox BOXD_PROVIDER=" + shq(p.Provider) + " BOXD_REGION=" + shq(p.Region) + ` BOXD_STATE_DIR="$D/state" BOXD_ROOT="$D/root" `)
	b.WriteString(`nohup setsid "$D/boxd" --dial ` + shq(p.CoreURL) + ` >"$D/boxd.log" 2>&1 </dev/null & `)
	b.WriteString("echo boxd-started")
	return b.String()
}

// ShellCommand wraps a command with cwd and env for providers whose exec API
// takes a single string.
func ShellCommand(r ExecRequest) string {
	var b strings.Builder
	if r.Cwd != "" {
		b.WriteString("mkdir -p " + shq(r.Cwd) + " && cd " + shq(r.Cwd) + " && ")
	}
	keys := make([]string, 0, len(r.Env))
	for k := range r.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if validEnvName(k) {
			b.WriteString("export " + k + "=" + shq(r.Env[k]) + "; ")
		}
	}
	b.WriteString(r.Command)
	return b.String()
}

func validEnvName(k string) bool {
	if k == "" {
		return false
	}
	for i, ch := range k {
		if !(ch == '_' || ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z' || i > 0 && ch >= '0' && ch <= '9') {
			return false
		}
	}
	return true
}

// pickSize maps a spec onto the nearest catalog size (SKU first).
func pickSize(info Info, s Spec) Size {
	for _, z := range info.Sizes {
		if z.SKU == s.SKU && s.SKU != "" {
			return z
		}
	}
	best := Size{CPU: s.CPU, RAMMB: s.RAMMB, DiskGB: s.DiskGB}
	found := false
	for _, z := range info.Sizes {
		if z.CPU >= s.CPU && z.RAMMB >= s.RAMMB && (!found || z.CPU*1000+float64(z.RAMMB) < best.CPU*1000+float64(best.RAMMB)) {
			best, found = z, true
		}
	}
	if best.CPU == 0 {
		best.CPU = 1
	}
	if best.RAMMB == 0 {
		best.RAMMB = 1024
	}
	return best
}
