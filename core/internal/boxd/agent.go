package boxd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"

	"boxa/core/internal/hostlink"
	"boxa/core/internal/proto"
	"boxa/core/internal/secret"
)

// Config for the host agent.
type Config struct {
	CoreURL        string // ws(s)://core/v1/hosts/connect
	BootstrapToken string
	StateDir       string // persists host_id + host token, logs
	Provider       string
	Region         string
	IP             string
	MetricsEvery   time.Duration
	Version        string
}

type state struct {
	HostID    string `json:"host_id"`
	HostToken string `json:"host_token"`
}

// Agent is the boxd runtime.
type Agent struct {
	cfg     Config
	drv     Driver
	logsDir string

	mu      sync.Mutex
	peer    *hostlink.Peer
	ptys    map[string]*ptySession
	observe map[string]time.Time
	cpuPrev [2]uint64
}

type ptySession struct {
	f   *os.File
	cmd *exec.Cmd
}

func NewAgent(cfg Config, drv Driver) (*Agent, error) {
	if cfg.MetricsEvery == 0 {
		cfg.MetricsEvery = 10 * time.Second
	}
	logs := filepath.Join(cfg.StateDir, "logs")
	if err := os.MkdirAll(logs, 0o755); err != nil {
		return nil, err
	}
	return &Agent{cfg: cfg, drv: drv, logsDir: logs, ptys: map[string]*ptySession{}, observe: map[string]time.Time{}}, nil
}

func (a *Agent) statePath() string { return filepath.Join(a.cfg.StateDir, "host.json") }

func (a *Agent) loadState() state {
	var s state
	if b, err := os.ReadFile(a.statePath()); err == nil {
		json.Unmarshal(b, &s)
	}
	return s
}

func (a *Agent) saveState(s state) error {
	b, _ := json.MarshalIndent(s, "", "  ")
	return os.WriteFile(a.statePath(), b, 0o600)
}

// Run connects and reconnects forever until ctx is done.
func (a *Agent) Run(ctx context.Context) error {
	backoff := time.Second
	for ctx.Err() == nil {
		start := time.Now()
		err := a.connectOnce(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if time.Since(start) > 30*time.Second {
			backoff = time.Second
		}
		log.Printf("boxd: tunnel ended: %v (retry in %s)", err, backoff)
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return nil
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
	return nil
}

func (a *Agent) dial(ctx context.Context, token string) (*websocket.Conn, error) {
	h := http.Header{}
	h.Set("Authorization", "Bearer "+token)
	d := websocket.Dialer{HandshakeTimeout: 15 * time.Second}
	ws, resp, err := d.DialContext(ctx, a.cfg.CoreURL, h)
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusUnauthorized {
			return nil, errUnauthorized
		}
		return nil, err
	}
	return ws, nil
}

var errUnauthorized = errors.New("unauthorized")

func (a *Agent) connectOnce(ctx context.Context) error {
	st := a.loadState()
	token := st.HostToken
	if token == "" {
		token = a.cfg.BootstrapToken
	}
	ws, err := a.dial(ctx, token)
	if errors.Is(err, errUnauthorized) && st.HostToken != "" && a.cfg.BootstrapToken != "" {
		ws, err = a.dial(ctx, a.cfg.BootstrapToken)
	}
	if err != nil {
		return err
	}
	p := hostlink.NewPeer(ws)
	boxes, _ := a.drv.List(ctx)
	host, _ := os.Hostname()
	cpu, ram, disk := hostCapacity(a.cfg.StateDir)
	hb, _ := json.Marshal(proto.Hello{
		HostID: st.HostID, Hostname: host, Driver: a.drv.Name(), Version: a.cfg.Version,
		Provider: a.cfg.Provider, Region: a.cfg.Region, IP: a.cfg.IP,
		CPU: cpu, RAMMB: ram, DiskGB: disk, Boxes: boxes,
	})
	if err := p.Send(proto.Frame{Type: proto.THello, Params: hb}); err != nil {
		p.Close()
		return err
	}
	var wf proto.Frame
	ws.SetReadDeadline(time.Now().Add(20 * time.Second))
	if err := ws.ReadJSON(&wf); err != nil {
		p.Close()
		return err
	}
	ws.SetReadDeadline(time.Time{})
	if wf.Error != nil {
		p.Close()
		return wf.Error
	}
	var w proto.Welcome
	json.Unmarshal(wf.Result, &w)
	if w.HostToken != "" || w.HostID != st.HostID {
		if w.HostToken != "" {
			st.HostToken = w.HostToken
		}
		st.HostID = w.HostID
		if err := a.saveState(st); err != nil {
			log.Printf("boxd: cannot persist host token: %v", err)
		}
	}
	log.Printf("boxd: connected to core as host %s (driver=%s)", w.HostID, a.drv.Name())
	a.mu.Lock()
	a.peer = p
	a.mu.Unlock()
	ws.SetPingHandler(func(m string) error {
		return ws.WriteControl(websocket.PongMessage, []byte(m), time.Now().Add(5*time.Second))
	})
	p.OnFrame = func(f proto.Frame) { a.onFrame(p, f) }
	mctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go a.metricsLoop(mctx, p)
	go func() {
		select {
		case <-ctx.Done():
			p.Close()
		case <-p.Done():
		}
	}()
	err = p.ReadLoop()
	a.closeAllPTY()
	return err
}

func (a *Agent) metricsLoop(ctx context.Context, p *hostlink.Peer) {
	send := func() {
		m := a.collectMetrics(ctx)
		b, _ := json.Marshal(m)
		p.Send(proto.Frame{Type: proto.TMetrics, Params: b})
	}
	send()
	t := time.NewTicker(a.cfg.MetricsEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-p.Done():
			return
		case <-t.C:
			send()
		}
	}
}

func (a *Agent) onFrame(p *hostlink.Peer, f proto.Frame) {
	switch f.Type {
	case proto.TReq:
		res, err := a.handle(context.Background(), f)
		p.Reply(f, res, err)
	case proto.TPtyData:
		a.mu.Lock()
		s := a.ptys[f.Stream]
		a.mu.Unlock()
		if s != nil {
			s.f.Write(f.Data)
		}
	}
}

func decode[T any](f proto.Frame) (T, error) {
	var v T
	if err := json.Unmarshal(f.Params, &v); err != nil {
		return v, boxErr("INVALID", "bad params: "+err.Error())
	}
	return v, nil
}

func (a *Agent) handle(ctx context.Context, f proto.Frame) (any, error) {
	switch f.Method {
	case proto.MCreate:
		p, err := decode[proto.CreateParams](f)
		if err != nil {
			return nil, err
		}
		return nil, a.drv.Create(ctx, p)
	case proto.MStart, proto.MStop, proto.MDelete:
		p, err := decode[proto.BoxRef](f)
		if err != nil {
			return nil, err
		}
		switch f.Method {
		case proto.MStart:
			return nil, a.drv.Start(ctx, p.BoxID)
		case proto.MStop:
			return nil, a.drv.Stop(ctx, p.BoxID)
		default:
			return nil, a.drv.Delete(ctx, p.BoxID)
		}
	case proto.MList:
		ids, err := a.drv.List(ctx)
		return proto.ListResult{Boxes: ids}, err
	case proto.MExec:
		p, err := decode[proto.ExecParams](f)
		if err != nil {
			return nil, err
		}
		return a.exec(ctx, p)
	case proto.MRead:
		p, err := decode[proto.ReadParams](f)
		if err != nil {
			return nil, err
		}
		return a.read(ctx, p)
	case proto.MWrite:
		p, err := decode[proto.WriteParams](f)
		if err != nil {
			return nil, err
		}
		return map[string]int{"bytes": len(p.Content)}, a.drv.Write(ctx, p.BoxID, p.Path, []byte(p.Content))
	case proto.MGrep:
		p, err := decode[proto.GrepParams](f)
		if err != nil {
			return nil, err
		}
		return a.drv.Grep(ctx, p.BoxID, p.Pattern, p.Path)
	case proto.MObserve:
		p, err := decode[proto.BoxRef](f)
		if err != nil {
			return nil, err
		}
		return a.observeBox(ctx, p.BoxID)
	case proto.MSnapshot:
		p, err := decode[proto.SnapshotParams](f)
		if err != nil {
			return nil, err
		}
		loc, size, err := a.drv.Snapshot(ctx, p.BoxID, p.SnapshotID)
		if err != nil {
			return nil, err
		}
		return proto.SnapshotResult{Location: loc, SizeMB: float64(size) / (1 << 20)}, nil
	case proto.MLogRead:
		p, err := decode[proto.LogReadParams](f)
		if err != nil {
			return nil, err
		}
		b, err := os.ReadFile(filepath.Join(a.logsDir, filepath.Base(p.LogID)+".log"))
		if err != nil {
			return nil, boxErr("NOT_FOUND", "log not found")
		}
		return proto.LogReadResult{Content: string(b)}, nil
	case proto.MPtyOpen:
		p, err := decode[proto.PtyOpenParams](f)
		if err != nil {
			return nil, err
		}
		return nil, a.openPTY(ctx, p)
	case proto.MPtyResize:
		p, err := decode[proto.PtyResizeParams](f)
		if err != nil {
			return nil, err
		}
		a.mu.Lock()
		s := a.ptys[p.Stream]
		a.mu.Unlock()
		if s != nil {
			pty.Setsize(s.f, &pty.Winsize{Cols: uint16(p.Cols), Rows: uint16(p.Rows)})
		}
		return nil, nil
	case proto.MPtyClose:
		p, err := decode[proto.PtyResizeParams](f)
		if err != nil {
			return nil, err
		}
		a.closePTY(p.Stream)
		return nil, nil
	}
	return nil, boxErr("INVALID", "unknown method "+f.Method)
}

// exec runs a command and shapes its output (M7).
func (a *Agent) exec(ctx context.Context, p proto.ExecParams) (*ExecOut, error) {
	logID := secret.ID("log")
	outPath := filepath.Join(a.logsDir, logID+".out")
	errPath := filepath.Join(a.logsDir, logID+".err")
	of, err := os.Create(outPath)
	if err != nil {
		return nil, err
	}
	ef, err := os.Create(errPath)
	if err != nil {
		of.Close()
		return nil, err
	}
	defer os.Remove(outPath)
	defer os.Remove(errPath)
	ow, ew := &capWriter{f: of}, &capWriter{f: ef}
	start := time.Now()
	code, runErr := a.drv.Run(ctx, p.BoxID, p, ow, ew)
	dur := time.Since(start)
	of.Close()
	ef.Close()
	var pe *proto.Error
	if runErr != nil && !(errors.As(runErr, &pe) && pe.Code == "TIMEOUT") {
		return nil, runErr
	}
	tail := p.TailLines
	if tail <= 0 {
		tail = DefaultTailLines
	}
	so, st, _ := tailFile(outPath, tail)
	se, et, _ := tailFile(errPath, tail)
	total := ow.n + ew.n + ow.dropped + ew.dropped
	res := &ExecOut{
		ExitCode: code, DurationMS: dur.Milliseconds(), StdoutTail: so, StderrTail: se,
		Truncated: st || et || ow.dropped > 0 || ew.dropped > 0,
	}
	if runErr != nil {
		res.TimedOut = true
		if res.StderrTail != "" {
			res.StderrTail += "\n"
		}
		res.StderrTail += "[boxa] " + runErr.Error()
	}
	if res.Truncated || total > KeepLogBytes {
		if err := combineLog(filepath.Join(a.logsDir, logID+".log"), p.Command, outPath, errPath); err == nil {
			res.LogID = logID
		}
	}
	res.TokensSaved = tokensSaved(total, len(so)+len(se))
	files, _ := a.drv.ChangedFiles(ctx, p.BoxID, start.Add(-time.Second))
	if files == nil {
		files = []string{}
	}
	res.FilesChanged = files
	return res, nil
}

// ExecOut is what boxd returns; core maps it into model.ExecResult.
type ExecOut struct {
	ExitCode     int      `json:"exit_code"`
	DurationMS   int64    `json:"duration_ms"`
	StdoutTail   string   `json:"stdout_tail"`
	StderrTail   string   `json:"stderr_tail"`
	Truncated    bool     `json:"truncated"`
	LogID        string   `json:"log_id,omitempty"`
	FilesChanged []string `json:"files_changed"`
	TokensSaved  int      `json:"tokens_saved"`
	TimedOut     bool     `json:"timed_out,omitempty"`
}

func combineLog(dst, command, outPath, errPath string) error {
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	fmt.Fprintf(w, "$ %s\n--- stdout ---\n", command)
	if b, err := os.Open(outPath); err == nil {
		bufio.NewReader(b).WriteTo(w)
		b.Close()
	}
	fmt.Fprintf(w, "\n--- stderr ---\n")
	if b, err := os.Open(errPath); err == nil {
		bufio.NewReader(b).WriteTo(w)
		b.Close()
	}
	return w.Flush()
}

func (a *Agent) read(ctx context.Context, p proto.ReadParams) (*proto.ReadResult, error) {
	data, isDir, ents, err := a.drv.Read(ctx, p.BoxID, p.Path)
	if err != nil {
		return nil, err
	}
	if isDir {
		return &proto.ReadResult{IsDir: true, Entries: ents}, nil
	}
	s := strings.ToValidUTF8(string(data), "\uFFFD")
	lines := strings.Split(s, "\n")
	if strings.HasSuffix(s, "\n") {
		lines = lines[:len(lines)-1]
	}
	total := len(lines)
	if p.From > 0 || p.To > 0 {
		from, to := p.From, p.To
		if from < 1 {
			from = 1
		}
		if to <= 0 || to > total {
			to = total
		}
		if from > total {
			return &proto.ReadResult{Content: "", TotalLines: total}, nil
		}
		s = strings.Join(lines[from-1:to], "\n")
		if to > from-1 {
			s += "\n"
		}
	}
	return &proto.ReadResult{Content: s, TotalLines: total}, nil
}

func (a *Agent) observeBox(ctx context.Context, id string) (*proto.ObserveResult, error) {
	a.mu.Lock()
	since, ok := a.observe[id]
	now := time.Now()
	a.observe[id] = now
	a.mu.Unlock()
	if !ok {
		since = now.Add(-10 * time.Minute)
	}
	files, err := a.drv.ChangedFiles(ctx, id, since)
	if err != nil {
		return nil, err
	}
	procs, ports, err := a.drv.Processes(ctx, id)
	if err != nil {
		return nil, err
	}
	if files == nil {
		files = []string{}
	}
	if procs == nil {
		procs = []proto.Proc{}
	}
	if ports == nil {
		ports = []int{}
	}
	return &proto.ObserveResult{FilesChanged: files, Processes: procs, Ports: ports, Since: since.UTC().Format(time.RFC3339)}, nil
}

func (a *Agent) openPTY(ctx context.Context, p proto.PtyOpenParams) error {
	cmd, err := a.drv.ShellCmd(ctx, p.BoxID)
	if err != nil {
		return err
	}
	cols, rows := p.Cols, p.Rows
	if cols <= 0 {
		cols = 120
	}
	if rows <= 0 {
		rows = 32
	}
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	if err != nil {
		return err
	}
	s := &ptySession{f: f, cmd: cmd}
	a.mu.Lock()
	a.ptys[p.Stream] = s
	peer := a.peer
	a.mu.Unlock()
	go func() {
		buf := make([]byte, 16<<10)
		for {
			n, err := f.Read(buf)
			if n > 0 && peer != nil {
				peer.Send(proto.Frame{Type: proto.TPtyData, Stream: p.Stream, Data: append([]byte(nil), buf[:n]...)})
			}
			if err != nil {
				break
			}
		}
		cmd.Wait()
		if peer != nil {
			peer.Send(proto.Frame{Type: proto.TPtyExit, Stream: p.Stream})
		}
		a.mu.Lock()
		delete(a.ptys, p.Stream)
		a.mu.Unlock()
	}()
	return nil
}

func (a *Agent) closePTY(stream string) {
	a.mu.Lock()
	s := a.ptys[stream]
	delete(a.ptys, stream)
	a.mu.Unlock()
	if s != nil {
		if s.cmd.Process != nil {
			s.cmd.Process.Signal(syscall.SIGHUP)
		}
		s.f.Close()
	}
}

func (a *Agent) closeAllPTY() {
	a.mu.Lock()
	ids := make([]string, 0, len(a.ptys))
	for id := range a.ptys {
		ids = append(ids, id)
	}
	a.mu.Unlock()
	for _, id := range ids {
		a.closePTY(id)
	}
}
