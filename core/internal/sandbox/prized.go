package sandbox

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

// Prized is the prized.dev Sandboxes driver (beta): control plane
// https://api.prized.dev/api/v1 (Bearer dcp_…), commands and files on the
// sandbox's edge `toolboxBase` with the same key. Source:
// https://prized.dev/docs/sandboxes-api.
type Prized struct {
	h    *httpClient
	base string
	info Info
	mu   sync.Mutex
	tb   map[string]string // sandbox id → toolboxBase
}

type prizedSandbox struct {
	ID          string `json:"id"`
	State       string `json:"state"`
	Template    string `json:"template"`
	ToolboxBase string `json:"toolboxBase"`
	ErrorReason any    `json:"errorReason"`
}

// NewPrized returns the driver (base "" = https://api.prized.dev/api/v1).
func NewPrized(c *http.Client, base string) *Prized {
	p := &Prized{h: newHTTP(c), base: strings.TrimRight(orStr(base, "https://api.prized.dev/api/v1"), "/"), tb: map[string]string{}}
	p.info = Info{ID: "prized", Name: "Prized Sandboxes", Tier: "T2", Adapter: "native", Verification: VerifiedLive,
		VerifiedNote: "Routes from prized.dev/docs/sandboxes-api; read-only calls (GET /me, /sandbox-templates, /sandboxes) succeeded with a real key on 2026-10-27. Create/exec not exercised (no spend).",
		Docs:         "https://prized.dev/docs/sandboxes-api", Website: "https://prized.dev/sandboxes", Initials: "PZ", Color: "#7c5cff",
		Caps:    Capabilities{Background: true, Pause: true, Fork: true, Files: true, BootMS: 3000, MaxLifetimeS: 7 * 24 * 3600, MaxCPU: 32, MaxRAMMB: 131072, Isolation: "microvm"},
		Pricing: Pricing{CPUSecond: 0.04 / 3600, GBSecond: 0.0125 / 3600, DiskGBSecond: 0.0001 / 3600, Source: "https://prized.dev/docs/billing#sandboxes", Confidence: "docs", Note: "per second, no minimum; paused = storage only"},
		Sizes: []Size{{SKU: "1x2", CPU: 1, RAMMB: 2048, DiskGB: 10}, {SKU: "2x4", CPU: 2, RAMMB: 4096, DiskGB: 10}, {SKU: "4x8", CPU: 4, RAMMB: 8192, DiskGB: 10},
			{SKU: "8x16", CPU: 8, RAMMB: 16384, DiskGB: 10}, {SKU: "16x32", CPU: 16, RAMMB: 32768, DiskGB: 20}},
		Regions: []Region{{ID: "global", Name: "Prized (auto)"}}, Templates: []string{"prized-base-v1", "prized-agent-v1"}, DefaultTemplate: "prized-base-v1", WorkDir: "/home/prized",
		Credentials: []CredField{{Name: "token", Label: "API key (Sandboxes: Write)", Secret: true, Hint: "dcp_…"}},
		EnvVars:     []string{"PRIZED_API_KEY"},
		Notes:       []string{"No inbound ports: boxd dials out, so this is fine for Boxa.", "Free tier: 2 vCPU / 4 GB and 60 min of running per sandbox."}}
	return p
}

func (p *Prized) Info() Info { return p.info }

func (p *Prized) api(c Cred) string {
	return strings.TrimRight(c.Get("base_url", p.base), "/")
}

func (p *Prized) hdr(c Cred) map[string]string {
	return map[string]string{"Authorization": "Bearer " + c.Token()}
}

func (p *Prized) Validate(ctx context.Context, c Cred) error {
	var out struct {
		Templates []struct {
			ID string `json:"id"`
		} `json:"templates"`
	}
	return p.h.do(ctx, "GET", p.api(c)+"/sandbox-templates", p.hdr(c), nil, &out)
}

var prizedStates = map[string]string{"starting": StateStarting, "running": StateRunning, "pausing": StatePaused, "paused": StatePaused,
	"resizing": StatePaused, "deleting": StateDeleted, "deleted": StateDeleted, "error": StateError}

func (p *Prized) remember(s prizedSandbox) *Sandbox {
	if s.ToolboxBase != "" {
		p.mu.Lock()
		p.tb[s.ID] = s.ToolboxBase
		p.mu.Unlock()
	}
	st := prizedStates[s.State]
	if st == "" {
		st = StateUnknown
	}
	return &Sandbox{ID: s.ID, State: st, Template: s.Template, Meta: map[string]string{"toolbox": s.ToolboxBase}}
}

func (p *Prized) Create(ctx context.Context, c Cred, s Spec) (*Sandbox, error) {
	res := map[string]any{"vcpu": int(s.CPU + 0.5), "memoryMb": s.RAMMB}
	if s.DiskGB >= 10 {
		res["diskGb"] = s.DiskGB
	}
	body := map[string]any{"template": orStr(s.Template, "prized-base-v1"), "resources": res}
	if len(s.Env) > 0 {
		body["envVars"] = s.Env
	}
	if len(s.Labels) > 0 {
		lb := map[string]string{}
		for k, v := range s.Labels {
			if len(lb) < 32 {
				lb[trim(k, 64)] = trim(v, 256)
			}
		}
		body["labels"] = lb
	}
	if s.TimeoutS > 0 {
		body["maxRuntimeMin"] = (s.TimeoutS + 59) / 60
	}
	h := p.hdr(c)
	if s.Name != "" {
		h["Idempotency-Key"] = trim(s.Name, 128)
	}
	var out struct {
		Sandbox prizedSandbox `json:"sandbox"`
	}
	if err := p.h.do(ctx, "POST", p.api(c)+"/sandboxes?wait=running", h, body, &out); err != nil {
		return nil, err
	}
	if out.Sandbox.ID == "" {
		return nil, fmt.Errorf("create answered without sandbox.id")
	}
	return p.remember(out.Sandbox), nil
}

func (p *Prized) Get(ctx context.Context, c Cred, id string) (*Sandbox, error) {
	var out struct {
		Sandbox prizedSandbox `json:"sandbox"`
	}
	if err := p.h.do(ctx, "GET", p.api(c)+"/sandboxes/"+url.PathEscape(id), p.hdr(c), nil, &out); err != nil {
		if he, ok := err.(*HTTPError); ok && he.Status == 404 {
			return &Sandbox{ID: id, State: StateDeleted}, nil
		}
		return nil, err
	}
	return p.remember(out.Sandbox), nil
}

func (p *Prized) Delete(ctx context.Context, c Cred, id string) error {
	return p.h.do(ctx, "DELETE", p.api(c)+"/sandboxes/"+url.PathEscape(id), p.hdr(c), nil, nil)
}

func (p *Prized) Pause(ctx context.Context, c Cred, id string) error {
	return p.h.do(ctx, "POST", p.api(c)+"/sandboxes/"+url.PathEscape(id)+"/pause", p.hdr(c), nil, nil)
}

func (p *Prized) Resume(ctx context.Context, c Cred, id string) error {
	return p.h.do(ctx, "POST", p.api(c)+"/sandboxes/"+url.PathEscape(id)+"/resume?wait=running", p.hdr(c), nil, nil)
}

func (p *Prized) Fork(ctx context.Context, c Cred, id string) (*Sandbox, error) {
	var out struct {
		Sandboxes []prizedSandbox `json:"sandboxes"`
	}
	if err := p.h.do(ctx, "POST", p.api(c)+"/sandboxes/"+url.PathEscape(id)+"/fork", p.hdr(c), map[string]any{"count": 1}, &out); err != nil {
		return nil, err
	}
	if len(out.Sandboxes) == 0 {
		return nil, fmt.Errorf("fork answered without sandboxes")
	}
	return p.remember(out.Sandboxes[0]), nil
}

func (p *Prized) List(ctx context.Context, c Cred) ([]Sandbox, error) {
	var out struct {
		Sandboxes []prizedSandbox `json:"sandboxes"`
	}
	if err := p.h.do(ctx, "GET", p.api(c)+"/sandboxes", p.hdr(c), nil, &out); err != nil {
		return nil, err
	}
	l := []Sandbox{}
	for _, s := range out.Sandboxes {
		l = append(l, *p.remember(s))
	}
	return l, nil
}

func (p *Prized) toolbox(ctx context.Context, c Cred, id string) (string, error) {
	p.mu.Lock()
	tb := p.tb[id]
	p.mu.Unlock()
	if tb != "" {
		return strings.TrimRight(tb, "/"), nil
	}
	sb, err := p.Get(ctx, c, id)
	if err != nil {
		return "", err
	}
	if sb.Meta["toolbox"] == "" {
		return "", fmt.Errorf("sandbox %s has no toolboxBase (state %s)", id, sb.State)
	}
	return strings.TrimRight(sb.Meta["toolbox"], "/"), nil
}

func (p *Prized) Exec(ctx context.Context, c Cred, id string, r ExecRequest) (*ExecResult, error) {
	tb, err := p.toolbox(ctx, c, id)
	if err != nil {
		return nil, err
	}
	t := r.TimeoutS
	if t <= 0 {
		t = 300
	}
	if t > 3600 {
		t = 3600
	}
	body := map[string]any{"command": r.Command, "timeout": t}
	if strings.HasPrefix(r.Cwd, "/") {
		body["cwd"] = r.Cwd
	}
	if len(r.Env) > 0 {
		body["env"] = r.Env
	}
	var out struct {
		Stdout     string `json:"stdout"`
		Stderr     string `json:"stderr"`
		ExitCode   int    `json:"exitCode"`
		DurationMS int64  `json:"durationMs"`
		TimedOut   bool   `json:"timedOut"`
	}
	if err := p.h.do(ctx, "POST", tb+"/process/execute", p.hdr(c), body, &out); err != nil {
		return nil, err
	}
	return &ExecResult{ExitCode: out.ExitCode, Stdout: out.Stdout, Stderr: out.Stderr, DurationMS: out.DurationMS, TimedOut: out.TimedOut}, nil
}

func (p *Prized) PutFile(ctx context.Context, c Cred, id, path string, data []byte) error {
	tb, err := p.toolbox(ctx, c, id)
	if err != nil {
		return err
	}
	return p.h.do(ctx, "POST", tb+"/files/upload?path="+url.QueryEscape(path), p.hdr(c), data, nil)
}

func (p *Prized) GetFile(ctx context.Context, c Cred, id, path string) ([]byte, error) {
	tb, err := p.toolbox(ctx, c, id)
	if err != nil {
		return nil, err
	}
	var out []byte
	err = p.h.do(ctx, "GET", tb+"/files/download?path="+url.QueryEscape(path), p.hdr(c), nil, &out)
	return out, err
}
