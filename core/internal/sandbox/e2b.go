package sandbox

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// E2B is the E2B-compatible driver, shared by E2B and Novita Sandbox (a fork
// of the E2B stack). Control plane: REST (X-API-Key) per the E2B OpenAPI
// spec (github.com/e2b-dev/infra spec/openapi.yml). In-sandbox daemon
// "envd" on port 49983: files over REST (/files), processes over Connect RPC
// (process.Process/Start, JSON codec, server stream).
type E2B struct {
	h          *httpClient
	info       Info
	apiBase    string // https://api.<domain>
	domain     string // default sandbox domain
	keyHeader  string // X-API-Key | X-API-KEY
	idHeader   string // E2b-Sandbox-Id | Novita-Sandbox-Id
	portHeader string
	regionDom  map[string]string // region id → domain (Novita)
	// EnvdBase overrides the envd URL for every sandbox (tests).
	EnvdBase string
	fork     bool
	mu       sync.Mutex
	meta     map[string]e2bSandbox
}

type e2bSandbox struct {
	SandboxID       string `json:"sandboxID"`
	TemplateID      string `json:"templateID"` // build id (e.g. rki5dems9wqfm4r03t7g)
	Alias           string `json:"alias"`      // template name as created ("base")
	EnvdAccessToken string `json:"envdAccessToken"`
	Domain          string `json:"domain"`
	State           string `json:"state"`
	CPUCount        int    `json:"cpuCount"`
	MemoryMB        int    `json:"memoryMB"`
	EnvdVersion     string `json:"envdVersion"`
	apiBase         string
	known           bool // full record seen (Novita's envd has no access token)
}

// template is the name the sandbox was created from: the alias, not the
// build id the API also reports.
func (s e2bSandbox) template() string { return orStr(s.Alias, s.TemplateID) }

// apiFor is the control-plane base a sandbox was created on (region-aware:
// a Novita us-virginia-1 sandbox must be paused/resumed/deleted there).
func (d *E2B) apiFor(c Cred, id string) string {
	d.mu.Lock()
	m := d.meta[id]
	d.mu.Unlock()
	return orStr(m.apiBase, d.api(c, ""))
}

// remember stores a sandbox record answered by create/connect/fork.
func (d *E2B) remember(sb e2bSandbox, api, domain string) {
	if sb.Domain == "" {
		sb.Domain = domain
	}
	sb.apiBase, sb.known = api, true
	d.mu.Lock()
	if old, ok := d.meta[sb.SandboxID]; ok && sb.EnvdAccessToken == "" {
		sb.EnvdAccessToken = old.EnvdAccessToken
	}
	d.meta[sb.SandboxID] = sb
	d.mu.Unlock()
}

// NewE2B returns the E2B driver (apiBase "" = https://api.e2b.app).
func NewE2B(c *http.Client, apiBase string) *E2B {
	d := &E2B{h: newHTTP(c), apiBase: orStr(apiBase, "https://api.e2b.app"), domain: "e2b.app", keyHeader: "X-API-Key",
		idHeader: "E2b-Sandbox-Id", portHeader: "E2b-Sandbox-Port", fork: true, meta: map[string]e2bSandbox{}}
	d.info = Info{ID: "e2b", Name: "E2B", Tier: "T2", Adapter: "native", Verification: VerifiedLive,
		VerifiedNote: "Full lifecycle verified live on the Hobby plan on 2026-10-27 (template base = 2 vCPU / 512 MB / 22 GB root fs, 20 GB free, envd 0.10.0): create ~0.2 s, create→first exec ~0.35 s, exec ~30-90 ms, 200k-line exec ~0.14 s, file put/get ~25-45 ms, pause ~0.15 s, resume ~0.18 s, fork ~0.5-0.7 s, delete ~55 ms; also boxa-core box up/exec/down end to end. Control plane per e2b-dev/infra spec/openapi.yml, exec via envd Connect RPC process.Process/Start.",
		Docs:         "https://e2b.dev/docs", Website: "https://e2b.dev", Initials: "E2", Color: "#ff8800",
		Caps:    Capabilities{Background: true, Pause: true, Fork: true, Ports: true, PTY: true, Files: true, BootMS: 400, MaxLifetimeS: 24 * 3600, MaxCPU: 8, MaxRAMMB: 8192, Isolation: "microvm"},
		Pricing: Pricing{CPUSecond: 0.000014, GBSecond: 0.0000045, Source: "https://e2b.dev/pricing", Confidence: "docs", Note: "per second while running; Hobby/Pro plans; sandbox resources come from the template"},
		Sizes:   []Size{{SKU: "base", CPU: 2, RAMMB: 512, DiskGB: 22}}, // measured: df / = 22G (20G free), API diskSizeMB 23668
		Regions: []Region{{ID: "global", Name: "E2B Cloud"}}, Templates: []string{"base"}, DefaultTemplate: "base", WorkDir: "/home/user",
		Credentials: []CredField{{Name: "token", Label: "API key", Secret: true, Hint: "e2b_…"},
			{Name: "templates", Label: "Templates (id=CPUxMB, comma separated)", Optional: true, Hint: "base=2x512,my-agent=4x4096"}},
		EnvVars: []string{"E2B_API_KEY", "E2B_TEMPLATES"},
		Notes: []string{"CPU/RAM are fixed per template: build bigger templates in E2B and list them in the Templates field.",
			"Hobby (free credit) caps a sandbox at 1 h and 20 running at once; Pro allows 24 h."}}
	return d
}

// NewNovita returns the Novita Sandbox driver (E2B-compatible).
func NewNovita(c *http.Client, apiBase string) *E2B {
	d := NewE2B(c, "")
	d.apiBase, d.domain, d.keyHeader, d.idHeader, d.portHeader, d.fork = apiBase, "us-phx-1.sandbox.novita.ai", "X-API-KEY", "Novita-Sandbox-Id", "Novita-Sandbox-Port", false
	d.regionDom = map[string]string{"us-phx-1": "us-phx-1.sandbox.novita.ai", "us-virginia-1": "sandbox.novita.ai"}
	d.info = Info{ID: "novita", Name: "Novita Sandbox", Tier: "T2", Adapter: "native", Verification: VerifiedLive,
		VerifiedNote: "E2B-compatible. Verified live in us-phx-1 on 2026-10-27 (template base = 2 vCPU / 512 MB / 22 GB root fs, 20 GB free, envd 0.5.12, no envd access token): create ~0.3 s, create→first exec ~0.27 s, exec ~150-170 ms, 200k-line exec ~0.47 s, file put/get ~150 ms, delete ~0.1 s. Pause ~0.3 s / resume ~0.15 s / exec after resume ~80 ms (state reaches paused). Through boxa-core (scripts/live-novita.sh, shim): box up ~0.9 s, exec 84-133 ms, MCP 83 ms, 200k-line compaction keeps exit 7, box down ~0.1 s. No fork API. Paths/headers (X-API-KEY, api.<domain>, envd 49983) from the novita-sandbox SDK 2.1.1.",
		Docs:         "https://docs.novita.ai/guides/sandbox-overview", Website: "https://novita.ai", Initials: "NV", Color: "#23d57c",
		Caps:    Capabilities{Background: true, Pause: true, Ports: true, PTY: true, Files: true, BootMS: 600, MaxLifetimeS: 4 * 3600, MaxCPU: 8, MaxRAMMB: 8192, Isolation: "microvm"},
		Pricing: Pricing{CPUSecond: 0.0000098, GBSecond: 0.0000032, Source: "https://docs.novita.ai/guides/sandbox-pricing", Confidence: "docs", Note: "per second for CPU and RAM while running; 20 GB ephemeral disk included"},
		Sizes:   []Size{{SKU: "base", CPU: 2, RAMMB: 512, DiskGB: 22}}, // measured: API diskSizeMB 23709 (docs: 20 GB included)
		Regions: []Region{{ID: "us-phx-1", Name: "US Phoenix (v2, default)"}, {ID: "us-virginia-1", Name: "US Virginia (v1)"}}, Templates: []string{"base"}, DefaultTemplate: "base", WorkDir: "/home/user",
		Credentials: []CredField{{Name: "token", Label: "API key", Secret: true, Hint: "sk_…"},
			{Name: "templates", Label: "Templates (id=CPUxMB, comma separated)", Optional: true, Hint: "base=2x512"}},
		EnvVars: []string{"NOVITA_API_KEY", "NOVITA_TEMPLATES"},
		Notes:   []string{"Max session 1 h (free) to 4 h (default paid); CPU/RAM come from the template."}}
	return d
}

func (d *E2B) Info() Info { return d.info }

// SizesFor parses the optional "templates" credential field.
func (d *E2B) SizesFor(c Cred) []Size {
	raw := strings.TrimSpace(c["templates"])
	if raw == "" {
		return d.info.Sizes
	}
	var out []Size
	for _, part := range strings.Split(raw, ",") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) != 2 {
			continue
		}
		xy := strings.SplitN(strings.ToLower(kv[1]), "x", 2)
		if len(xy) != 2 {
			continue
		}
		cpu, e1 := strconv.ParseFloat(xy[0], 64)
		ram, e2 := strconv.Atoi(xy[1])
		if e1 != nil || e2 != nil || cpu <= 0 || ram <= 0 {
			continue
		}
		out = append(out, Size{SKU: strings.TrimSpace(kv[0]), CPU: cpu, RAMMB: ram, DiskGB: d.info.Sizes[0].DiskGB})
	}
	if len(out) == 0 {
		return d.info.Sizes
	}
	return out
}

func (d *E2B) api(c Cred, region string) string {
	if b := c["base_url"]; b != "" {
		return strings.TrimRight(b, "/")
	}
	if d.apiBase != "" && (d.regionDom == nil || region == "") {
		return strings.TrimRight(d.apiBase, "/")
	}
	return "https://api." + d.domainFor(c, region)
}

func (d *E2B) domainFor(c Cred, region string) string {
	if v := c["domain"]; v != "" {
		return v
	}
	if dom, ok := d.regionDom[region]; ok {
		return dom
	}
	return d.domain
}

func (d *E2B) hdr(c Cred) map[string]string { return map[string]string{d.keyHeader: c.Token()} }

func (d *E2B) Validate(ctx context.Context, c Cred) error {
	var out []json.RawMessage
	return d.h.do(ctx, "GET", d.api(c, "")+"/v2/sandboxes?limit=1", d.hdr(c), nil, &out)
}

func (d *E2B) Create(ctx context.Context, c Cred, s Spec) (*Sandbox, error) {
	tpl := orStr(s.SKU, orStr(s.Template, d.info.DefaultTemplate)) // SKU = template id
	timeout := s.TimeoutS
	if timeout <= 0 {
		timeout = 3600
	}
	if d.info.Caps.MaxLifetimeS > 0 && timeout > d.info.Caps.MaxLifetimeS {
		timeout = d.info.Caps.MaxLifetimeS
	}
	body := map[string]any{"templateID": tpl, "timeout": timeout, "autoPause": false, "metadata": s.Labels}
	if len(s.Env) > 0 {
		body["envVars"] = s.Env
	}
	api := d.api(c, s.Region)
	var sb e2bSandbox
	if err := d.h.do(ctx, "POST", api+"/sandboxes", d.hdr(c), body, &sb); err != nil {
		return nil, err
	}
	if sb.SandboxID == "" {
		return nil, fmt.Errorf("create answered without sandboxID")
	}
	d.remember(sb, api, d.domainFor(c, s.Region))
	return &Sandbox{ID: sb.SandboxID, State: StateRunning, Region: s.Region, Template: tpl, Meta: map[string]string{"domain": orStr(sb.Domain, d.domainFor(c, s.Region))}}, nil
}

func (d *E2B) lookup(ctx context.Context, c Cred, id string) (e2bSandbox, error) {
	d.mu.Lock()
	m, ok := d.meta[id]
	d.mu.Unlock()
	if ok && (m.known || m.EnvdAccessToken != "") {
		return m, nil
	}
	var sb e2bSandbox
	api := d.api(c, "")
	if ok && m.apiBase != "" {
		api = m.apiBase
	}
	if err := d.h.do(ctx, "GET", api+"/sandboxes/"+url.PathEscape(id), d.hdr(c), nil, &sb); err != nil {
		return sb, err
	}
	if sb.Domain == "" {
		sb.Domain = orStr(m.Domain, d.domainFor(c, ""))
	}
	sb.apiBase, sb.known = api, true
	d.mu.Lock()
	d.meta[id] = sb
	d.mu.Unlock()
	return sb, nil
}

func (d *E2B) Get(ctx context.Context, c Cred, id string) (*Sandbox, error) {
	d.mu.Lock()
	m := d.meta[id]
	d.mu.Unlock()
	api := orStr(m.apiBase, d.api(c, ""))
	var sb e2bSandbox
	if err := d.h.do(ctx, "GET", api+"/sandboxes/"+url.PathEscape(id), d.hdr(c), nil, &sb); err != nil {
		if he, ok := err.(*HTTPError); ok && he.Status == 404 {
			return &Sandbox{ID: id, State: StateDeleted}, nil
		}
		return nil, err
	}
	st := map[string]string{"running": StateRunning, "paused": StatePaused, "pausing": StatePaused}[sb.State]
	if st == "" {
		st = StateUnknown
	}
	return &Sandbox{ID: id, State: st, Template: sb.template(), Meta: map[string]string{"domain": orStr(sb.Domain, orStr(m.Domain, d.domainFor(c, "")))}}, nil
}

func (d *E2B) Delete(ctx context.Context, c Cred, id string) error {
	d.mu.Lock()
	m := d.meta[id]
	delete(d.meta, id)
	d.mu.Unlock()
	return d.h.do(ctx, "DELETE", orStr(m.apiBase, d.api(c, ""))+"/sandboxes/"+url.PathEscape(id), d.hdr(c), nil, nil)
}

func (d *E2B) Pause(ctx context.Context, c Cred, id string) error {
	return d.h.do(ctx, "POST", d.apiFor(c, id)+"/sandboxes/"+url.PathEscape(id)+"/pause", d.hdr(c), map[string]any{}, nil)
}

// Resume reconnects a paused sandbox (POST /sandboxes/{id}/connect, which
// answers the sandbox record incl. its envd token) with a fresh timeout.
func (d *E2B) Resume(ctx context.Context, c Cred, id string) error {
	api := d.apiFor(c, id)
	var sb e2bSandbox
	if err := d.h.do(ctx, "POST", api+"/sandboxes/"+url.PathEscape(id)+"/connect", d.hdr(c), map[string]any{"timeout": d.resumeTimeout()}, &sb); err != nil {
		return err
	}
	if sb.SandboxID == id {
		d.mu.Lock()
		dom := d.meta[id].Domain
		d.mu.Unlock()
		d.remember(sb, api, orStr(dom, d.domainFor(c, "")))
	}
	return nil
}

// resumeTimeout is the lifetime given on resume/fork: an hour, capped by the
// provider's max lifetime (the box's own TTL reaper deletes it earlier).
func (d *E2B) resumeTimeout() int {
	if m := d.info.Caps.MaxLifetimeS; m > 0 && m < 3600 {
		return m
	}
	return 3600
}

// Fork is E2B's checkpoint-and-fork (POST /sandboxes/{id}/fork).
func (d *E2B) Fork(ctx context.Context, c Cred, id string) (*Sandbox, error) {
	if !d.fork {
		return nil, fmt.Errorf("%s has no fork API", d.info.Name)
	}
	var res []struct {
		Sandbox *e2bSandbox `json:"sandbox"`
		Error   *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	api := d.apiFor(c, id)
	if err := d.h.do(ctx, "POST", api+"/sandboxes/"+url.PathEscape(id)+"/fork", d.hdr(c), map[string]any{"count": 1, "timeout": d.resumeTimeout()}, &res); err != nil {
		return nil, err
	}
	if len(res) == 0 || res[0].Sandbox == nil {
		msg := "no fork returned"
		if len(res) > 0 && res[0].Error != nil {
			msg = res[0].Error.Message
		}
		return nil, fmt.Errorf("fork failed: %s", msg)
	}
	ch := *res[0].Sandbox
	if ch.SandboxID == "" {
		return nil, fmt.Errorf("fork answered without sandboxID")
	}
	d.mu.Lock()
	dom := d.meta[id].Domain
	d.mu.Unlock()
	d.remember(ch, api, orStr(dom, d.domainFor(c, "")))
	return &Sandbox{ID: ch.SandboxID, State: StateRunning, Template: ch.template(), Meta: map[string]string{"domain": orStr(ch.Domain, orStr(dom, d.domain))}}, nil
}

func (d *E2B) PreviewURL(ctx context.Context, c Cred, id string, port int) (string, error) {
	m, err := d.lookup(ctx, c, id)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("https://%d-%s.%s", port, id, orStr(m.Domain, d.domain)), nil
}

func (d *E2B) List(ctx context.Context, c Cred) ([]Sandbox, error) {
	var l []e2bSandbox
	if err := d.h.do(ctx, "GET", d.api(c, "")+"/v2/sandboxes", d.hdr(c), nil, &l); err != nil {
		return nil, err
	}
	out := []Sandbox{}
	for _, s := range l {
		st := map[string]string{"running": StateRunning, "paused": StatePaused}[s.State]
		out = append(out, Sandbox{ID: s.SandboxID, State: orStr(st, StateUnknown), Template: s.template()})
	}
	return out, nil
}

// ---- envd ----

func (d *E2B) envd(ctx context.Context, c Cred, id string) (string, map[string]string, error) {
	m, err := d.lookup(ctx, c, id)
	if err != nil {
		return "", nil, err
	}
	base := d.EnvdBase
	if base == "" {
		base = fmt.Sprintf("https://49983-%s.%s", id, orStr(m.Domain, d.domain))
	}
	h := map[string]string{d.idHeader: id, d.portHeader: "49983",
		"Authorization": "Basic " + base64.StdEncoding.EncodeToString([]byte("user:"))}
	if m.EnvdAccessToken != "" {
		h["X-Access-Token"] = m.EnvdAccessToken
	}
	return strings.TrimRight(base, "/"), h, nil
}

// Exec runs `bash -l -c <cmd>` through envd's Connect RPC
// process.Process/Start (server streaming, JSON codec) and collects output.
func (d *E2B) Exec(ctx context.Context, c Cred, id string, r ExecRequest) (*ExecResult, error) {
	base, h, err := d.envd(ctx, c, id)
	if err != nil {
		return nil, err
	}
	timeout := r.TimeoutS
	if timeout <= 0 {
		timeout = 300
	}
	cctx, cancel := context.WithTimeout(ctx, time.Duration(timeout+15)*time.Second)
	defer cancel()
	proc := map[string]any{"cmd": "/bin/bash", "args": []string{"-l", "-c", r.Command}, "envs": nonNilMap(r.Env)}
	if r.Cwd != "" {
		proc["cwd"] = r.Cwd
	}
	msg, _ := json.Marshal(map[string]any{"process": proc})
	var body bytes.Buffer
	body.WriteByte(0)
	binary.Write(&body, binary.BigEndian, uint32(len(msg)))
	body.Write(msg)
	req, err := http.NewRequestWithContext(cctx, "POST", base+"/process.Process/Start", &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/connect+json")
	req.Header.Set("Connect-Protocol-Version", "1")
	req.Header.Set("Connect-Timeout-Ms", strconv.Itoa(timeout*1000))
	for k, v := range h {
		req.Header.Set(k, v)
	}
	start := time.Now()
	resp, err := d.h.c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, &HTTPError{Status: resp.StatusCode, Body: string(b)}
	}
	res := &ExecResult{ExitCode: -1}
	var so, se bytes.Buffer
	rd := bufio.NewReader(resp.Body)
	for {
		var hdr [5]byte
		if _, err := io.ReadFull(rd, hdr[:]); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				break
			}
			if cctx.Err() != nil {
				res.TimedOut = true
				break
			}
			return nil, err
		}
		n := binary.BigEndian.Uint32(hdr[1:])
		if n > 64<<20 {
			return nil, fmt.Errorf("envd frame too large")
		}
		payload := make([]byte, n)
		if _, err := io.ReadFull(rd, payload); err != nil {
			return nil, err
		}
		if hdr[0]&0x02 != 0 { // end of stream
			var end struct {
				Error *struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			json.Unmarshal(payload, &end)
			if end.Error != nil {
				if end.Error.Code == "deadline_exceeded" {
					res.TimedOut = true
					break
				}
				return nil, fmt.Errorf("envd %s: %s", end.Error.Code, end.Error.Message)
			}
			break
		}
		var ev struct {
			Event struct {
				Data *struct {
					Stdout string `json:"stdout"`
					Stderr string `json:"stderr"`
					Pty    string `json:"pty"`
				} `json:"data"`
				End *struct {
					ExitCode int    `json:"exitCode"`
					Exited   bool   `json:"exited"`
					Status   string `json:"status"`
					Error    string `json:"error"`
				} `json:"end"`
			} `json:"event"`
		}
		if err := json.Unmarshal(payload, &ev); err != nil {
			continue
		}
		if dt := ev.Event.Data; dt != nil {
			if b, err := base64.StdEncoding.DecodeString(dt.Stdout); err == nil {
				so.Write(b)
			}
			if b, err := base64.StdEncoding.DecodeString(dt.Stderr); err == nil {
				se.Write(b)
			}
			if b, err := base64.StdEncoding.DecodeString(dt.Pty); err == nil {
				so.Write(b)
			}
		}
		if e := ev.Event.End; e != nil {
			res.ExitCode = e.ExitCode
			if e.Error != "" && se.Len() == 0 {
				se.WriteString(e.Error)
			}
		}
	}
	if res.TimedOut {
		res.ExitCode = 124
	}
	res.Stdout, res.Stderr, res.DurationMS = so.String(), se.String(), time.Since(start).Milliseconds()
	return res, nil
}

func (d *E2B) PutFile(ctx context.Context, c Cred, id, path string, data []byte) error {
	base, h, err := d.envd(ctx, c, id)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", path)
	fw.Write(data)
	mw.Close()
	req, err := http.NewRequestWithContext(ctx, "POST", base+"/files?path="+url.QueryEscape(path)+"&username=user", &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	for k, v := range h {
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

func (d *E2B) GetFile(ctx context.Context, c Cred, id, path string) ([]byte, error) {
	base, h, err := d.envd(ctx, c, id)
	if err != nil {
		return nil, err
	}
	var out []byte
	err = d.h.do(ctx, "GET", base+"/files?path="+url.QueryEscape(path)+"&username=user", h, nil, &out)
	return out, err
}

func nonNilMap(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}
