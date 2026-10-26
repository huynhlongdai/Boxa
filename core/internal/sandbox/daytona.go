package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

// Daytona is the Daytona driver: control plane https://app.daytona.io/api
// (Bearer API key; openapi.json), toolbox at {toolboxProxyUrl}/{sandboxId}
// (toolbox-openapi.json: /process/execute, /files/download, /files/upload-v2).
type Daytona struct {
	h    *httpClient
	base string
	info Info
	mu   sync.Mutex
	tb   map[string]string
}

type daytonaSandbox struct {
	ID              string  `json:"id"`
	State           string  `json:"state"`
	Target          string  `json:"target"`
	Snapshot        string  `json:"snapshot"`
	ToolboxProxyURL string  `json:"toolboxProxyUrl"`
	CPU             float64 `json:"cpu"`
	Memory          float64 `json:"memory"`
	ErrorReason     string  `json:"errorReason"`
}

// NewDaytona returns the driver (base "" = https://app.daytona.io/api).
func NewDaytona(c *http.Client, base string) *Daytona {
	d := &Daytona{h: newHTTP(c), base: strings.TrimRight(orStr(base, "https://app.daytona.io/api"), "/"), tb: map[string]string{}}
	d.info = Info{ID: "daytona", Name: "Daytona", Tier: "T2", Adapter: "native", Verification: VerifiedDocs,
		VerifiedNote: "From Daytona's published OpenAPI (docs/openapi.json, docs/toolbox-openapi.json); toolbox URL = toolboxProxyUrl + '/' + sandboxId as in the official TS SDK.",
		Docs:         "https://www.daytona.io/docs/en/tools/api/", Website: "https://www.daytona.io", Initials: "DT", Color: "#0f62fe",
		Caps:    Capabilities{Background: true, Pause: true, Snapshot: true, Fork: true, Ports: true, PTY: true, Files: true, GPU: true, BootMS: 1000, MaxCPU: 4, MaxRAMMB: 8192, Isolation: "container"},
		Pricing: Pricing{CPUSecond: 0.000014, GBSecond: 0.0000045, DiskGBSecond: 0.00000003, Source: "https://www.daytona.io/pricing", Confidence: "estimated", Note: "published per-second rates (pricing page renders client-side; not re-fetched)"},
		Sizes: []Size{{SKU: "1x1", CPU: 1, RAMMB: 1024, DiskGB: 3}, {SKU: "1x2", CPU: 1, RAMMB: 2048, DiskGB: 5}, {SKU: "2x4", CPU: 2, RAMMB: 4096, DiskGB: 10},
			{SKU: "4x8", CPU: 4, RAMMB: 8192, DiskGB: 10}},
		Regions:   []Region{{ID: "us", Name: "United States"}, {ID: "eu", Name: "Europe"}},
		Templates: []string{"(default snapshot)"}, WorkDir: "/home/daytona",
		Credentials: []CredField{{Name: "token", Label: "API key", Secret: true, Hint: "dtn_…"},
			{Name: "organization_id", Label: "Organization ID", Optional: true}},
		EnvVars: []string{"DAYTONA_API_KEY", "DAYTONA_ORGANIZATION_ID"}}
	return d
}

func (d *Daytona) Info() Info { return d.info }

func (d *Daytona) api(c Cred) string { return strings.TrimRight(c.Get("base_url", d.base), "/") }

func (d *Daytona) hdr(c Cred) map[string]string {
	h := map[string]string{"Authorization": "Bearer " + c.Token()}
	if o := c["organization_id"]; o != "" {
		h["X-Daytona-Organization-ID"] = o
	}
	return h
}

func (d *Daytona) Validate(ctx context.Context, c Cred) error {
	var out map[string]any
	return d.h.do(ctx, "GET", d.api(c)+"/sandbox?limit=1", d.hdr(c), nil, &out)
}

var daytonaStates = map[string]string{"started": StateRunning, "creating": StateStarting, "starting": StateStarting, "restoring": StateStarting,
	"pulling_snapshot": StateStarting, "pending_build": StateStarting, "building_snapshot": StateStarting, "resuming": StateStarting, "forking": StateStarting,
	"stopped": StateStopped, "stopping": StateStopped, "archived": StateStopped, "archiving": StateStopped, "paused": StatePaused, "pausing": StatePaused,
	"error": StateError, "build_failed": StateError, "destroyed": StateDeleted, "destroying": StateDeleted}

func (d *Daytona) remember(s daytonaSandbox) *Sandbox {
	if s.ToolboxProxyURL != "" {
		d.mu.Lock()
		d.tb[s.ID] = strings.TrimRight(s.ToolboxProxyURL, "/") + "/" + s.ID
		d.mu.Unlock()
	}
	st := daytonaStates[s.State]
	if st == "" {
		st = StateUnknown
	}
	return &Sandbox{ID: s.ID, State: st, Region: s.Target, Template: s.Snapshot}
}

func (d *Daytona) Create(ctx context.Context, c Cred, s Spec) (*Sandbox, error) {
	gb := (s.RAMMB + 1023) / 1024
	if gb < 1 {
		gb = 1
	}
	body := map[string]any{"cpu": int(s.CPU + 0.5), "memory": gb, "autoStopInterval": 0}
	if s.DiskGB > 0 {
		body["disk"] = s.DiskGB
	}
	if s.Region != "" {
		body["target"] = s.Region
	}
	if s.Template != "" {
		body["snapshot"] = s.Template
	}
	if len(s.Env) > 0 {
		body["env"] = s.Env
	}
	if len(s.Labels) > 0 {
		body["labels"] = s.Labels
	}
	if s.TimeoutS > 0 {
		body["ttlMinutes"] = (s.TimeoutS + 59) / 60
	}
	var out daytonaSandbox
	if err := d.h.do(ctx, "POST", d.api(c)+"/sandbox", d.hdr(c), body, &out); err != nil {
		return nil, err
	}
	if out.ID == "" {
		return nil, fmt.Errorf("create answered without id")
	}
	return d.remember(out), nil
}

func (d *Daytona) Get(ctx context.Context, c Cred, id string) (*Sandbox, error) {
	var out daytonaSandbox
	if err := d.h.do(ctx, "GET", d.api(c)+"/sandbox/"+url.PathEscape(id), d.hdr(c), nil, &out); err != nil {
		if he, ok := err.(*HTTPError); ok && he.Status == 404 {
			return &Sandbox{ID: id, State: StateDeleted}, nil
		}
		return nil, err
	}
	return d.remember(out), nil
}

func (d *Daytona) Delete(ctx context.Context, c Cred, id string) error {
	return d.h.do(ctx, "DELETE", d.api(c)+"/sandbox/"+url.PathEscape(id), d.hdr(c), nil, nil)
}

func (d *Daytona) post(ctx context.Context, c Cred, id, verb string, body, out any) error {
	return d.h.do(ctx, "POST", d.api(c)+"/sandbox/"+url.PathEscape(id)+"/"+verb, d.hdr(c), body, out)
}

func (d *Daytona) Pause(ctx context.Context, c Cred, id string) error {
	return d.post(ctx, c, id, "pause", nil, nil)
}
func (d *Daytona) Resume(ctx context.Context, c Cred, id string) error {
	return d.post(ctx, c, id, "start", nil, nil)
}
func (d *Daytona) Stop(ctx context.Context, c Cred, id string) error {
	return d.post(ctx, c, id, "stop", nil, nil)
}
func (d *Daytona) Start(ctx context.Context, c Cred, id string) error {
	return d.post(ctx, c, id, "start", nil, nil)
}

func (d *Daytona) Fork(ctx context.Context, c Cred, id string) (*Sandbox, error) {
	var out daytonaSandbox
	if err := d.post(ctx, c, id, "fork", map[string]any{}, &out); err != nil {
		return nil, err
	}
	return d.remember(out), nil
}

func (d *Daytona) Snapshot(ctx context.Context, c Cred, id, name string) (string, error) {
	var out map[string]any
	if err := d.post(ctx, c, id, "snapshot", map[string]any{"name": name}, &out); err != nil {
		return "", err
	}
	return name, nil
}

func (d *Daytona) PreviewURL(ctx context.Context, c Cred, id string, port int) (string, error) {
	var out struct {
		URL string `json:"url"`
	}
	err := d.h.do(ctx, "GET", fmt.Sprintf("%s/sandbox/%s/ports/%d/preview-url", d.api(c), url.PathEscape(id), port), d.hdr(c), nil, &out)
	return out.URL, err
}

func (d *Daytona) List(ctx context.Context, c Cred) ([]Sandbox, error) {
	var out struct {
		Items []daytonaSandbox `json:"items"`
	}
	if err := d.h.do(ctx, "GET", d.api(c)+"/sandbox", d.hdr(c), nil, &out); err != nil {
		return nil, err
	}
	l := []Sandbox{}
	for _, s := range out.Items {
		l = append(l, *d.remember(s))
	}
	return l, nil
}

func (d *Daytona) toolbox(ctx context.Context, c Cred, id string) (string, error) {
	d.mu.Lock()
	tb := d.tb[id]
	d.mu.Unlock()
	if tb != "" {
		return tb, nil
	}
	if _, err := d.Get(ctx, c, id); err != nil {
		return "", err
	}
	d.mu.Lock()
	tb = d.tb[id]
	d.mu.Unlock()
	if tb == "" {
		return "", fmt.Errorf("sandbox %s has no toolboxProxyUrl", id)
	}
	return tb, nil
}

// Exec uses the toolbox /process/execute (stdout and stderr come back merged
// in `result`).
func (d *Daytona) Exec(ctx context.Context, c Cred, id string, r ExecRequest) (*ExecResult, error) {
	tb, err := d.toolbox(ctx, c, id)
	if err != nil {
		return nil, err
	}
	t := r.TimeoutS
	if t <= 0 {
		t = 300
	}
	body := map[string]any{"command": "bash -lc " + shq(r.Command), "timeout": t}
	if r.Cwd != "" {
		body["cwd"] = r.Cwd
	}
	if len(r.Env) > 0 {
		body["envs"] = r.Env
	}
	var out struct {
		ExitCode int    `json:"exitCode"`
		Result   string `json:"result"`
	}
	if err := d.h.do(ctx, "POST", tb+"/process/execute", d.hdr(c), body, &out); err != nil {
		return nil, err
	}
	return &ExecResult{ExitCode: out.ExitCode, Stdout: out.Result}, nil
}

func (d *Daytona) PutFile(ctx context.Context, c Cred, id, path string, data []byte) error {
	tb, err := d.toolbox(ctx, c, id)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", path)
	fw.Write(data)
	mw.Close()
	req, err := http.NewRequestWithContext(ctx, "POST", tb+"/files/upload-v2?path="+url.QueryEscape(path), &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	for k, v := range d.hdr(c) {
		req.Header.Set(k, v)
	}
	resp, err := d.h.c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return &HTTPError{Status: resp.StatusCode, Body: string(b)}
	}
	return nil
}

func (d *Daytona) GetFile(ctx context.Context, c Cred, id, path string) ([]byte, error) {
	tb, err := d.toolbox(ctx, c, id)
	if err != nil {
		return nil, err
	}
	var out []byte
	err = d.h.do(ctx, "GET", tb+"/files/download?path="+url.QueryEscape(path), d.hdr(c), nil, &out)
	return out, err
}
