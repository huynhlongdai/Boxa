package sandbox

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// BoxdSh is the boxd.sh driver (persistent KVM microVMs that fork and
// suspend). The public API is gRPC (boxd.api.v1.BoxdApi at boxd.sh:9443),
// also served as grpc-web over HTTP/1.1 on the same port; Boxmes speaks
// grpc-web with hand-encoded protobuf for the handful of RPCs it uses.
// Auth: API key (bxd_…) → POST https://app.boxd.sh/api/v1/auth/token → 1 h JWT.
// Source: docs.boxd.sh/reference/grpc-api and /reference/grpc-proto.
//
// Not to be confused with Boxmes's own host agent, which is also called boxd.
type BoxdSh struct {
	h       *httpClient
	rpc     string // https://boxd.sh:9443
	authURL string
	info    Info
	mu      sync.Mutex
	jwt     map[string]boxdJWT
}

type boxdJWT struct {
	token string
	exp   time.Time
}

// NewBoxdSh returns the driver (base "" = https://boxd.sh:9443; the auth
// exchange is <base host>/api/v1/auth/token on app.boxd.sh by default).
func NewBoxdSh(c *http.Client, base string) *BoxdSh {
	d := &BoxdSh{h: newHTTP(c), rpc: strings.TrimRight(orStr(base, "https://boxd.sh:9443"), "/"), authURL: "https://app.boxd.sh/api/v1/auth/token", jwt: map[string]boxdJWT{}}
	if base != "" {
		d.authURL = strings.TrimRight(base, "/") + "/api/v1/auth/token"
	}
	d.info = Info{ID: "boxdsh", Name: "boxd.sh", Tier: "T2", Adapter: "native", Verification: VerifiedDocs,
		VerifiedNote: "grpc-web framing and field numbers from docs.boxd.sh/reference/grpc-api and grpc-proto (CreateVm, GetVm, DestroyVm, Exec, UploadFile, DownloadFile, SuspendVm, ResumeVm, ForkVm, CreateSnapshot, Whoami).",
		Docs:         "https://docs.boxd.sh/reference/grpc-api", Website: "https://boxd.sh", Initials: "BX", Color: "#111827",
		Caps:    Capabilities{Background: true, Pause: true, Snapshot: true, Fork: true, Ports: true, PTY: true, Files: true, BootMS: 300, MaxCPU: 8, MaxRAMMB: 32768, Isolation: "microvm"},
		Pricing: Pricing{CPUSecond: 0.047 / 3600, GBSecond: 0.0036 / 3600, Source: "https://boxd.sh/pricing", Confidence: "docs", Note: "€0.039/vCPU-h (US$0.047 listed) + €0.003/GiB-h RAM while running; disk billed by GiB used"},
		Sizes: []Size{{SKU: "1x4", CPU: 1, RAMMB: 4096, DiskGB: 100}, {SKU: "2x8", CPU: 2, RAMMB: 8192, DiskGB: 100}, {SKU: "4x16", CPU: 4, RAMMB: 16384, DiskGB: 100},
			{SKU: "8x32", CPU: 8, RAMMB: 32768, DiskGB: 100}},
		Regions:   []Region{{ID: "global", Name: "boxd.sh"}},
		Templates: []string{"ubuntu:24.04"}, DefaultTemplate: "", WorkDir: "/home/boxd",
		Credentials: []CredField{{Name: "token", Label: "API key", Secret: true, Hint: "bxd_…"}, {Name: "org", Label: "Organization", Optional: true}},
		EnvVars:     []string{"BOXDSH_API_KEY", "BOXDSH_ORG"},
		Notes:       []string{"Persistent VMs with a public HTTPS domain; Boxmes creates them isolated (no peers) and deletes them when the box ends."}}
	return d
}

func (d *BoxdSh) Info() Info { return d.info }

func (d *BoxdSh) token(ctx context.Context, c Cred) (string, error) {
	key := c.Token()
	d.mu.Lock()
	j, ok := d.jwt[key]
	d.mu.Unlock()
	if ok && time.Until(j.exp) > time.Minute {
		return j.token, nil
	}
	authURL := d.authURL
	if b := c["base_url"]; b != "" {
		authURL = strings.TrimRight(b, "/") + "/api/v1/auth/token"
	}
	var out struct {
		Token     string `json:"token"`
		ExpiresAt int64  `json:"expires_at"`
	}
	if err := d.h.do(ctx, "POST", authURL, nil, map[string]string{"api_key": key}, &out); err != nil {
		return "", err
	}
	if out.Token == "" {
		return "", &HTTPError{Status: 401, Body: "token exchange returned no token"}
	}
	exp := time.Unix(out.ExpiresAt, 0)
	if out.ExpiresAt == 0 {
		exp = time.Now().Add(55 * time.Minute)
	}
	d.mu.Lock()
	d.jwt[key] = boxdJWT{token: out.Token, exp: exp}
	d.mu.Unlock()
	return out.Token, nil
}

// call sends one grpc-web request and returns the response messages.
func (d *BoxdSh) call(ctx context.Context, c Cred, method string, req pbMsg) ([][]byte, error) {
	jwt, err := d.token(ctx, c)
	if err != nil {
		return nil, err
	}
	rpc := d.rpc
	if b := c["base_url"]; b != "" {
		rpc = strings.TrimRight(b, "/")
	}
	var body bytes.Buffer
	body.WriteByte(0)
	binary.Write(&body, binary.BigEndian, uint32(len(req)))
	body.Write(req)
	hr, err := http.NewRequestWithContext(ctx, "POST", rpc+"/boxd.api.v1.BoxdApi/"+method, &body)
	if err != nil {
		return nil, err
	}
	hr.Header.Set("Content-Type", "application/grpc-web+proto")
	hr.Header.Set("X-Grpc-Web", "1")
	hr.Header.Set("Authorization", "Bearer "+jwt)
	hr.Header.Set("User-Agent", d.h.ua)
	resp, err := d.h.c.Do(hr)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, &HTTPError{Status: resp.StatusCode, Body: string(b)}
	}
	status, msg := resp.Header.Get("Grpc-Status"), resp.Header.Get("Grpc-Message")
	var msgs [][]byte
	rd := bufio.NewReader(resp.Body)
	for {
		var hdr [5]byte
		if _, err := io.ReadFull(rd, hdr[:]); err != nil {
			break
		}
		n := binary.BigEndian.Uint32(hdr[1:])
		if n > 64<<20 {
			return nil, fmt.Errorf("grpc-web frame too large")
		}
		p := make([]byte, n)
		if _, err := io.ReadFull(rd, p); err != nil {
			return nil, err
		}
		if hdr[0]&0x80 != 0 {
			for _, line := range strings.Split(string(p), "\r\n") {
				kv := strings.SplitN(line, ":", 2)
				if len(kv) != 2 {
					continue
				}
				switch strings.ToLower(strings.TrimSpace(kv[0])) {
				case "grpc-status":
					status = strings.TrimSpace(kv[1])
				case "grpc-message":
					msg = strings.TrimSpace(kv[1])
				}
			}
			continue
		}
		msgs = append(msgs, p)
	}
	if status != "" && status != "0" {
		m, _ := url.PathUnescape(msg)
		return nil, grpcErr(status, m)
	}
	return msgs, nil
}

// grpcErr maps gRPC status codes onto HTTP-like errors (for classification).
func grpcErr(status, msg string) error {
	code, _ := strconv.Atoi(status)
	httpStatus := map[int]int{3: 400, 5: 404, 7: 403, 8: 429, 9: 409, 10: 409, 16: 401, 14: 503, 13: 500}[code]
	if httpStatus == 0 {
		httpStatus = 500
	}
	return &HTTPError{Status: httpStatus, Body: fmt.Sprintf("grpc-status %d: %s", code, msg)}
}

func (d *BoxdSh) one(ctx context.Context, c Cred, method string, req pbMsg) (map[int][]any, error) {
	msgs, err := d.call(ctx, c, method, req)
	if err != nil {
		return nil, err
	}
	if len(msgs) == 0 {
		return map[int][]any{}, nil
	}
	return pbFields(msgs[0])
}

func (d *BoxdSh) Validate(ctx context.Context, c Cred) error {
	_, err := d.one(ctx, c, "Whoami", pbMsg{})
	return err
}

func boxdState(s string) string {
	switch strings.ToLower(s) {
	case "running", "started", "ready":
		return StateRunning
	case "creating", "starting", "booting", "provisioning", "waking", "resuming":
		return StateStarting
	case "suspended", "standby", "hibernated", "paused":
		return StatePaused
	case "stopped", "stopping":
		return StateStopped
	case "error", "failed":
		return StateError
	case "deleted", "destroyed":
		return StateDeleted
	}
	return StateUnknown
}

func (d *BoxdSh) Create(ctx context.Context, c Cred, s Spec) (*Sandbox, error) {
	name := strings.ToLower(orStr(s.Name, NamePrefix+strconv.FormatInt(time.Now().UnixNano()%1e9, 36)))
	req := pbMsg{}.str(1, name)
	if s.Template != "" {
		req = req.str(2, s.Template)
	}
	for k, v := range s.Env {
		req = req.msg(5, pbMsg{}.str(1, k).str(2, v))
	}
	cfg := pbMsg{}.uint(1, uint64(s.CPU+0.5)).uint(2, uint64(s.RAMMB)<<20)
	req = req.msg(8, cfg).str(9, c["org"]).boolean(12, true)
	f, err := d.one(ctx, c, "CreateVm", req)
	if err != nil {
		return nil, err
	}
	id := orStr(pbStr(f, 1), pbStr(f, 2))
	if id == "" {
		return nil, fmt.Errorf("CreateVm answered without vm_id")
	}
	st := boxdState(pbStr(f, 6))
	if st == StateUnknown {
		st = StateStarting
	}
	return &Sandbox{ID: id, State: st, Template: pbStr(f, 5), Meta: map[string]string{"name": pbStr(f, 2), "url": pbStr(f, 4)}}, nil
}

func (d *BoxdSh) Get(ctx context.Context, c Cred, id string) (*Sandbox, error) {
	f, err := d.one(ctx, c, "GetVm", pbMsg{}.str(1, id))
	if err != nil {
		if he, ok := err.(*HTTPError); ok && he.Status == 404 {
			return &Sandbox{ID: id, State: StateDeleted}, nil
		}
		return nil, err
	}
	return &Sandbox{ID: id, State: boxdState(pbStr(f, 5)), Template: pbStr(f, 3), Meta: map[string]string{"name": pbStr(f, 2), "domain": pbStr(f, 12)}}, nil
}

func (d *BoxdSh) Delete(ctx context.Context, c Cred, id string) error {
	_, err := d.one(ctx, c, "DestroyVm", pbMsg{}.str(1, id))
	return err
}

func (d *BoxdSh) Pause(ctx context.Context, c Cred, id string) error {
	_, err := d.one(ctx, c, "SuspendVm", pbMsg{}.str(1, id))
	return err
}

func (d *BoxdSh) Resume(ctx context.Context, c Cred, id string) error {
	_, err := d.one(ctx, c, "ResumeVm", pbMsg{}.str(1, id))
	return err
}

func (d *BoxdSh) Fork(ctx context.Context, c Cred, id string) (*Sandbox, error) {
	name := "fork-" + strconv.FormatInt(time.Now().UnixNano()%1e9, 36)
	f, err := d.one(ctx, c, "ForkVm", pbMsg{}.str(1, id).str(2, name).boolean(6, true))
	if err != nil {
		return nil, err
	}
	return &Sandbox{ID: orStr(pbStr(f, 1), pbStr(f, 2)), State: StateRunning}, nil
}

func (d *BoxdSh) Snapshot(ctx context.Context, c Cred, id, name string) (string, error) {
	f, err := d.one(ctx, c, "CreateSnapshot", pbMsg{}.str(1, id).str(2, name))
	if err != nil {
		return "", err
	}
	return orStr(pbStr(f, 1), name), nil
}

// Exec runs the command over the bidirectional Exec stream in grpc-web
// half-duplex mode: one request message, then data chunks + the exit code.
func (d *BoxdSh) Exec(ctx context.Context, c Cred, id string, r ExecRequest) (*ExecResult, error) {
	start := time.Now()
	t := r.TimeoutS
	if t <= 0 {
		t = 300
	}
	cctx, cancel := context.WithTimeout(ctx, time.Duration(t+15)*time.Second)
	defer cancel()
	cmd := "bash -lc " + shq(ShellCommand(r))
	msgs, err := d.call(cctx, c, "Exec", pbMsg{}.str(4, cmd).str(5, id))
	if err != nil {
		if cctx.Err() != nil {
			return &ExecResult{ExitCode: 124, TimedOut: true, DurationMS: time.Since(start).Milliseconds()}, nil
		}
		return nil, err
	}
	res := &ExecResult{}
	var so, se bytes.Buffer
	for _, m := range msgs {
		f, err := pbFields(m)
		if err != nil {
			continue
		}
		if b := pbBytes(f, 1); len(b) > 0 {
			if pbUint(f, 10) == 1 {
				se.Write(b)
			} else {
				so.Write(b)
			}
		}
		if pbHas(f, 6) {
			res.ExitCode = int(int32(pbUint(f, 6)))
		}
	}
	res.Stdout, res.Stderr, res.DurationMS = so.String(), se.String(), time.Since(start).Milliseconds()
	return res, nil
}

func (d *BoxdSh) PutFile(ctx context.Context, c Cred, id, path string, data []byte) error {
	_, err := d.one(ctx, c, "UploadFile", pbMsg{}.str(1, id).str(2, path).bytes(3, data))
	return err
}

func (d *BoxdSh) GetFile(ctx context.Context, c Cred, id, path string) ([]byte, error) {
	f, err := d.one(ctx, c, "DownloadFile", pbMsg{}.str(1, id).str(2, path))
	if err != nil {
		return nil, err
	}
	return pbBytes(f, 1), nil
}
