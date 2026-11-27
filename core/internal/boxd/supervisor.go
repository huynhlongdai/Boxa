package boxd

// v1.7 process supervisor: runs the app spec of a box (clone → install once →
// start), captures its output (ring buffer + rotating file), restarts it on
// crash with exponential backoff, stops it while the box sleeps and starts it
// again on wake, after a boxd restart (cold resume) and after a restore.
// Specs persist in <state>/apps/<box>.json.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"boxmes/core/internal/model"
	"boxmes/core/internal/proto"
)

const (
	appRingBytes  = 256 << 10 // in-memory output kept per app
	appLogRotate  = 8 << 20   // log file rotates to .1 past this size
	appRunForever = 10 * 365 * 24 * 3600
)

// ring is a byte ring buffer addressed by absolute offsets.
type ring struct {
	buf  []byte
	end  int64 // absolute offset after the last byte written
	size int
}

func (r *ring) write(p []byte) {
	r.buf = append(r.buf, p...)
	if len(r.buf) > r.size {
		r.buf = append([]byte(nil), r.buf[len(r.buf)-r.size:]...)
	}
	r.end += int64(len(p))
}

// read returns bytes from off (clamped to what is kept), at most max.
func (r *ring) read(off int64, max int) ([]byte, int64, bool) {
	start := r.end - int64(len(r.buf))
	trunc := false
	if off < 0 {
		off = r.end - int64(max)
	}
	if off < start {
		if off >= 0 && off != r.end-int64(max) {
			trunc = true
		}
		off = start
	}
	if off > r.end {
		off = r.end
	}
	b := r.buf[off-start:]
	if len(b) > max {
		b = b[:max]
	}
	return append([]byte(nil), b...), off + int64(len(b)), trunc
}

type appSup struct {
	a    *Agent
	box  string
	spec model.AppSpec

	mu      sync.Mutex
	st      model.AppStatus
	out     ring
	logf    *os.File
	logPath string
	cancel  context.CancelFunc
	done    chan struct{}
	kick    chan struct{} // restart now (skips backoff)
}

func (a *Agent) appsDir() string { return filepath.Join(a.cfg.StateDir, "apps") }

func (a *Agent) appSpecPath(box string) string {
	return filepath.Join(a.appsDir(), filepath.Base(box)+".json")
}

func (a *Agent) getSup(box string) *appSup {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.apps == nil {
		return nil
	}
	return a.apps[box]
}

// appSet installs (or replaces / removes) the app of a box and starts it.
func (a *Agent) appSet(ctx context.Context, p proto.AppParams) (*model.AppStatus, error) {
	if old := a.getSup(p.BoxID); old != nil && p.Spec != nil {
		ns := specOf(p.Spec)
		if old.spec.FullHash() == ns.FullHash() {
			old.ensure()
			st := old.status()
			return &st, nil
		}
		old.close()
		a.mu.Lock()
		delete(a.apps, p.BoxID)
		a.mu.Unlock()
	}
	if p.Spec == nil {
		if old := a.getSup(p.BoxID); old != nil {
			old.close()
			a.mu.Lock()
			delete(a.apps, p.BoxID)
			a.mu.Unlock()
		}
		os.Remove(a.appSpecPath(p.BoxID))
		return &model.AppStatus{State: model.AppNone, Supervisor: "boxd"}, nil
	}
	spec := specOf(p.Spec)
	if msg := spec.Validate(); msg != "" {
		return nil, boxErr("INVALID", msg)
	}
	os.MkdirAll(a.appsDir(), 0o755)
	b, _ := json.MarshalIndent(spec, "", "  ")
	if err := os.WriteFile(a.appSpecPath(p.BoxID), b, 0o600); err != nil {
		return nil, err
	}
	s := a.newSup(p.BoxID, spec)
	s.start()
	st := s.status()
	return &st, nil
}

func specOf(in *model.AppSpec) model.AppSpec {
	s := *in
	s.Normalize()
	return s
}

func (a *Agent) newSup(box string, spec model.AppSpec) *appSup {
	s := &appSup{a: a, box: box, spec: spec, out: ring{size: appRingBytes}, kick: make(chan struct{}, 1),
		logPath: filepath.Join(a.appsDir(), filepath.Base(box)+".log")}
	s.st = model.AppStatus{State: model.AppStopped, Supervisor: "boxd", SpecHash: spec.FullHash()}
	if f, err := os.OpenFile(s.logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
		s.logf = f
	}
	a.mu.Lock()
	if a.apps == nil {
		a.apps = map[string]*appSup{}
	}
	a.apps[box] = s
	a.mu.Unlock()
	return s
}

// loadApps restarts the supervisors of persisted specs (boxd start = after a
// reboot / cold resume the app processes are gone).
func (a *Agent) loadApps(ctx context.Context) {
	ents, err := os.ReadDir(a.appsDir())
	if err != nil {
		return
	}
	boxes, _ := a.drv.List(ctx)
	have := map[string]bool{}
	for _, b := range boxes {
		have[b] = true
	}
	for _, e := range ents {
		name := e.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		box := strings.TrimSuffix(name, ".json")
		if !have[box] {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(a.appsDir(), name))
		if err != nil {
			continue
		}
		var spec model.AppSpec
		if json.Unmarshal(raw, &spec) != nil {
			continue
		}
		if a.getSup(box) != nil {
			continue
		}
		s := a.newSup(box, spec)
		s.logLine("boxd started: (re)starting the app")
		s.start()
	}
}

func (s *appSup) status() model.AppStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.st
	return st
}

func (s *appSup) set(f func(st *model.AppStatus)) {
	s.mu.Lock()
	f(&s.st)
	s.mu.Unlock()
}

func (s *appSup) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.out.write(p)
	if s.logf != nil {
		s.logf.Write(p)
		if st, err := s.logf.Stat(); err == nil && st.Size() > appLogRotate {
			s.logf.Close()
			os.Rename(s.logPath, s.logPath+".1")
			s.logf, _ = os.OpenFile(s.logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		}
	}
	return len(p), nil
}

func (s *appSup) logLine(format string, args ...any) {
	fmt.Fprintf(s, "[supervisor %s] %s\n", time.Now().UTC().Format("15:04:05"), fmt.Sprintf(format, args...))
}

func (s *appSup) running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cancel != nil
}

// start launches the supervise loop unless it runs already.
func (s *appSup) start() {
	s.mu.Lock()
	if s.cancel != nil {
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.done = make(chan struct{})
	done := s.done
	s.mu.Unlock()
	go func() {
		defer close(done)
		s.loop(ctx)
	}()
}

// stop kills the app and ends the loop (sleep, delete, spec change).
func (s *appSup) stop() {
	s.mu.Lock()
	cancel, done := s.cancel, s.done
	s.cancel = nil
	s.mu.Unlock()
	if cancel != nil {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
		}
	}
	s.set(func(st *model.AppStatus) { st.State, st.Ready, st.Message = model.AppStopped, false, "" })
}

// ensure starts the loop if it is not running (wake, restore, clone).
func (s *appSup) ensure() {
	if !s.running() {
		s.logLine("app process gone: starting it again")
		s.start()
	}
}

// restart kills the current process and starts it again immediately.
func (s *appSup) restart() {
	s.logLine("restart requested")
	s.stop()
	s.start()
}

func (s *appSup) close() {
	s.stop()
	s.mu.Lock()
	if s.logf != nil {
		s.logf.Close()
		s.logf = nil
	}
	s.mu.Unlock()
}

func (s *appSup) run(ctx context.Context, cmd, cwd string, timeoutS int) (int, error) {
	env := map[string]string{}
	for k, v := range s.spec.Env {
		env[k] = v
	}
	if s.spec.Port > 0 {
		if _, ok := env["PORT"]; !ok {
			env["PORT"] = strconv.Itoa(s.spec.Port)
		}
	}
	return s.a.drv.Run(ctx, s.box, proto.ExecParams{BoxID: s.box, Command: cmd, Cwd: cwd, Env: env, TimeoutS: timeoutS}, s, s)
}

func (s *appSup) sleep(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-s.kick:
		return true
	case <-time.After(d):
		return true
	}
}

func (s *appSup) markerPath() string {
	return filepath.Join(s.a.appsDir(), filepath.Base(s.box)+".installed")
}

// prepare clones the repo and runs install (once per spec hash).
func (s *appSup) prepare(ctx context.Context) error {
	sp := s.spec
	// paths under /work are passed relative to /work so that every driver
	// (local dirs, incus, sandboxes) maps them the same way
	tgt := sp.Workdir
	if tgt == "/work" {
		tgt = "."
	} else if strings.HasPrefix(tgt, "/work/") {
		tgt = strings.TrimPrefix(tgt, "/work/")
	}
	if sp.Repo != "" {
		s.set(func(st *model.AppStatus) { st.State, st.Message = model.AppCloning, "" })
		check, _ := s.run(ctx, "test -d "+shellQuote(tgt+"/.git"), "/work", 30)
		if check != 0 {
			ref := ""
			if sp.Ref != "" {
				ref = "--branch " + shellQuote(sp.Ref) + " "
			}
			s.logLine("git clone %s", sp.Repo)
			cmd := "mkdir -p " + shellQuote(parentDir(tgt)) + " && rm -rf " + shellQuote(tgt) + " && git clone -q --depth 1 " + ref + shellQuote(sp.Repo) + " " + shellQuote(tgt)
			if tgt == "." {
				cmd = "git clone -q --depth 1 " + ref + shellQuote(sp.Repo) + " ."
			}
			if code, err := s.run(ctx, cmd, "/work", 900); err != nil || code != 0 {
				return fmt.Errorf("git clone failed (exit %d)", code)
			}
		}
	} else {
		s.run(ctx, "mkdir -p "+shellQuote(tgt), "/work", 30)
	}
	if sp.Install != "" {
		h := sp.Hash()
		if b, _ := os.ReadFile(s.markerPath()); strings.TrimSpace(string(b)) != h {
			s.set(func(st *model.AppStatus) { st.State, st.Message = model.AppInstalling, "" })
			s.logLine("install: %s", sp.Install)
			if code, err := s.run(ctx, sp.Install, sp.Workdir, 1800); err != nil || code != 0 {
				return fmt.Errorf("install failed (exit %d)", code)
			}
			os.WriteFile(s.markerPath(), []byte(h), 0o600)
		}
	}
	return nil
}

func parentDir(p string) string {
	if i := strings.LastIndexByte(p, '/'); i > 0 {
		return p[:i]
	}
	if strings.HasPrefix(p, "/") {
		return "/"
	}
	return "."
}

func (s *appSup) loop(ctx context.Context) {
	delay := time.Second
	maxDelay := time.Duration(s.spec.BackoffMaxS) * time.Second
	bump := func() {
		delay *= 2
		if delay > maxDelay {
			delay = maxDelay
		}
	}
	for {
		if err := s.prepare(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			s.logLine("%v; retry in %s", err, delay)
			s.set(func(st *model.AppStatus) { st.State, st.Message = model.AppBackoff, err.Error() })
			if !s.sleep(ctx, delay) {
				return
			}
			bump()
			continue
		}
		break
	}
	delay = time.Second
	for ctx.Err() == nil {
		now := time.Now().UTC()
		s.set(func(st *model.AppStatus) {
			st.State, st.Ready, st.Message, st.StartedAt, st.ReadyAt = model.AppStarting, false, "", &now, nil
		})
		s.logLine("start: %s", s.spec.Start)
		pctx, pcancel := context.WithCancel(ctx)
		go s.watchReady(pctx)
		code, err := s.run(pctx, s.spec.Start, s.spec.Workdir, appRunForever)
		pcancel()
		if ctx.Err() != nil {
			return
		}
		ran := time.Since(now)
		msg := fmt.Sprintf("exited with code %d after %s", code, ran.Round(time.Millisecond))
		if err != nil {
			msg += ": " + err.Error()
		}
		s.logLine("%s", msg)
		c := code
		s.set(func(st *model.AppStatus) { st.LastExit, st.Ready, st.ReadyAt = &c, false, nil })
		if s.spec.Restart == model.RestartNever || (s.spec.Restart == model.RestartOnFailure && code == 0) {
			s.set(func(st *model.AppStatus) { st.State, st.Message = model.AppExited, msg })
			s.mu.Lock()
			s.cancel = nil
			s.mu.Unlock()
			return
		}
		if ran > time.Minute {
			delay = time.Second
		}
		s.set(func(st *model.AppStatus) {
			st.Restarts++
			st.State, st.Message = model.AppBackoff, fmt.Sprintf("%s; restarting in %s", msg, delay)
		})
		if !s.sleep(ctx, delay) {
			return
		}
		bump()
	}
}

// watchReady polls the readiness check until it passes or ctx ends.
func (s *appSup) watchReady(ctx context.Context) {
	r := s.spec.Ready
	ok := func() bool { return true }
	if r != nil && (r.HTTP != "" || r.TCP) && s.spec.Port > 0 {
		ok = func() bool {
			addr, err := s.a.drv.Addr(ctx, s.box, s.spec.Port)
			if err != nil {
				return false
			}
			if r.HTTP != "" {
				req, _ := http.NewRequestWithContext(ctx, "GET", "http://"+addr+r.HTTP, nil)
				resp, err := readyClient.Do(req)
				if err != nil {
					return false
				}
				io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
				resp.Body.Close()
				return resp.StatusCode < 500
			}
			c, err := net.DialTimeout("tcp", addr, 2*time.Second)
			if err != nil {
				return false
			}
			c.Close()
			return true
		}
	}
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if ok() {
			now := time.Now().UTC()
			s.set(func(st *model.AppStatus) {
				if st.State == model.AppStarting {
					st.State, st.Ready, st.ReadyAt = model.AppReadyState, true, &now
				}
			})
			s.logLine("ready")
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(300 * time.Millisecond):
		}
	}
}

var readyClient = &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func (s *appSup) logs(off int64, max int) proto.AppLogsResult {
	if max <= 0 || max > appRingBytes {
		max = 64 << 10
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b, next, trunc := s.out.read(off, max)
	return proto.AppLogsResult{Data: string(b), Offset: next, Truncated: trunc}
}

// ---- RPC glue ----

func (a *Agent) handleApp(ctx context.Context, method string, raw json.RawMessage) (any, error) {
	switch method {
	case proto.MAppSet:
		var p proto.AppParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, boxErr("INVALID", "bad params: "+err.Error())
		}
		return a.appSet(ctx, p)
	case proto.MAppLogs:
		var p proto.AppLogsParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, boxErr("INVALID", "bad params: "+err.Error())
		}
		s := a.getSup(p.BoxID)
		if s == nil {
			return proto.AppLogsResult{}, nil
		}
		return s.logs(p.Offset, p.Max), nil
	}
	var p proto.BoxRef
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, boxErr("INVALID", "bad params: "+err.Error())
	}
	s := a.getSup(p.BoxID)
	if s == nil {
		if method == proto.MAppGet || method == proto.MAppEnsure {
			return &model.AppStatus{State: model.AppNone, Supervisor: "boxd"}, nil
		}
		return nil, boxErr("NOT_FOUND", "this box has no app spec (PUT /v1/boxes/{id}/app first)")
	}
	switch method {
	case proto.MAppRestart:
		s.restart()
	case proto.MAppEnsure:
		s.ensure()
	}
	st := s.status()
	return &st, nil
}

// appBoxStopped / appBoxStarted / appBoxGone follow the box lifecycle.
func (a *Agent) appBoxStopped(box string) {
	if s := a.getSup(box); s != nil {
		s.logLine("box sleeping: app stopped")
		s.stop()
	}
}

func (a *Agent) appBoxStarted(box string) {
	if s := a.getSup(box); s != nil {
		s.ensure()
	}
}

func (a *Agent) appBoxRestored(box string) {
	if s := a.getSup(box); s != nil {
		s.logLine("box restored from a snapshot: restarting the app")
		os.Remove(s.markerPath())
		s.restart()
	}
}

func (a *Agent) appBoxGone(box string) {
	a.mu.Lock()
	s := a.apps[box]
	delete(a.apps, box)
	a.mu.Unlock()
	if s != nil {
		s.close()
		os.Remove(s.markerPath())
		os.Remove(s.logPath)
		os.Remove(s.logPath + ".1")
	}
	os.Remove(a.appSpecPath(box))
}

func (a *Agent) stopAllApps() {
	a.mu.Lock()
	ss := make([]*appSup, 0, len(a.apps))
	for _, s := range a.apps {
		ss = append(ss, s)
	}
	a.mu.Unlock()
	for _, s := range ss {
		s.stop()
	}
	if len(ss) > 0 {
		log.Printf("boxd: stopped %d app(s)", len(ss))
	}
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'" }
