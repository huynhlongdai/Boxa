package sandbox

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"boxa/core/internal/boxd"
	"boxa/core/internal/proto"
	"boxa/core/internal/secret"
)

// Shim is the gateway-side "boxd-shim" for T3 (exec-only) sandboxes: it
// answers the boxd RPCs core sends to a host by translating them into the
// provider's exec + file API. One sandbox = one box. Degraded mode: no PTY,
// no snapshots/clones/egress rules, no async-job cancel; output shaping
// (tail lines) happens here at the gateway.
type Shim struct {
	D       Driver
	C       Cred
	Ref     string // provider sandbox ref
	WorkDir string // e.g. /home/user → box /work = /home/user/work
	mu      sync.Mutex
	boxes   map[string]map[string]string // box id → env
	last    time.Time
	logs    map[string]string // log id → full output of a truncated exec (in memory, newest shimLogKeep)
	logIDs  []string
}

// shimLogKeep bounds the full-output logs a shim keeps for box_logs: the
// provider answers exec with the whole output, so the gateway keeps it
// (lost when boxa-core restarts).
const shimLogKeep = 16

// NewShim builds a shim for one sandbox.
func NewShim(d Driver, c Cred, ref string) *Shim {
	wd := strings.TrimRight(orStr(d.Info().WorkDir, "/tmp/boxa"), "/")
	return &Shim{D: d, C: c, Ref: ref, WorkDir: wd, boxes: map[string]map[string]string{}, logs: map[string]string{}}
}

// Unsupported is returned for RPCs an exec-only sandbox cannot serve.
func unsupported(method string) error {
	return &proto.Error{Code: "UNSUPPORTED", Message: method + " is not available on an exec-only (T3) sandbox; pick a T2 provider or a pool host for this feature"}
}

func decodeInto(params any, v any) error {
	b, err := json.Marshal(params)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func encodeOut(res any, out any) error {
	if out == nil || res == nil {
		return nil
	}
	b, err := json.Marshal(res)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

// boxPath maps a Boxa path (/work/x, x, /memory/x, /AGENTS.md) into the sandbox.
func (s *Shim) boxPath(p string) string {
	p = strings.TrimSpace(p)
	switch {
	case p == "" || p == "/work" || p == ".":
		return s.WorkDir + "/work"
	case strings.HasPrefix(p, "/work/"), strings.HasPrefix(p, "/memory"), p == "/AGENTS.md":
		return s.WorkDir + path.Clean(p)
	case strings.HasPrefix(p, "/"):
		return path.Clean(p)
	}
	return s.WorkDir + "/work/" + path.Clean(p)
}

func (s *Shim) env(box string) map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := map[string]string{"BOX_WORK": s.WorkDir + "/work", "BOX_MEMORY": s.WorkDir + "/memory", "BOXA_TIER": "T3"}
	for k, v := range s.boxes[box] {
		e[k] = v
	}
	return e
}

func (s *Shim) exec(ctx context.Context, box, cmd, cwd string, env map[string]string, timeout int) (*ExecResult, error) {
	all := s.env(box)
	for k, v := range env {
		all[k] = v
	}
	if cwd == "" {
		cwd = s.WorkDir + "/work"
	} else {
		cwd = s.boxPath(cwd)
	}
	// cwd and env go inline (mkdir -p + cd + export), which works with every
	// provider's exec whether or not it takes cwd/env fields.
	r := ExecRequest{Command: ShellCommand(ExecRequest{Command: cmd, Cwd: cwd, Env: all}), TimeoutS: timeout}
	res, err := s.D.Exec(ctx, s.C, s.Ref, r)
	s.mu.Lock()
	s.last = time.Now()
	s.mu.Unlock()
	return res, err
}

func (s *Shim) write(ctx context.Context, box, p string, data []byte) error {
	full := s.boxPath(p)
	if f, ok := s.D.(Filer); ok {
		if _, err := s.exec(ctx, box, "mkdir -p "+shq(path.Dir(full)), "/", nil, 30); err != nil {
			return err
		}
		return f.PutFile(ctx, s.C, s.Ref, full, data)
	}
	b64 := base64.StdEncoding.EncodeToString(data)
	res, err := s.exec(ctx, box, "mkdir -p "+shq(path.Dir(full))+" && printf %s "+shq(b64)+" | base64 -d > "+shq(full), "/", nil, 60)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return &proto.Error{Code: "INVALID", Message: "write failed: " + trim(res.Stderr, 300)}
	}
	return nil
}

// Call implements hostlink.Virtual.
func (s *Shim) Call(ctx context.Context, method string, params, out any) error {
	switch method {
	case proto.MCreate:
		var p proto.CreateParams
		if err := decodeInto(params, &p); err != nil {
			return err
		}
		s.mu.Lock()
		s.boxes[p.BoxID] = p.Env
		s.mu.Unlock()
		res, err := s.exec(ctx, p.BoxID, "mkdir -p "+shq(s.WorkDir+"/work")+" "+shq(s.WorkDir+"/memory"), "/", nil, 60)
		if err != nil {
			return err
		}
		if res.ExitCode != 0 {
			return &proto.Error{Code: "SETUP_FAILED", Message: "mkdir: " + trim(res.Stderr, 300)}
		}
		if p.AgentsMD != "" {
			if err := s.write(ctx, p.BoxID, "/AGENTS.md", []byte(p.AgentsMD)); err != nil {
				return err
			}
		}
		for _, cmd := range p.Setup {
			res, err := s.exec(ctx, p.BoxID, cmd, "", nil, 900)
			if err != nil {
				return err
			}
			if res.ExitCode != 0 {
				return &proto.Error{Code: "SETUP_FAILED", Message: fmt.Sprintf("setup %q exited %d: %s", cmd, res.ExitCode, trim(res.Stderr+res.Stdout, 400))}
			}
		}
		return nil
	case proto.MStart, proto.MStop:
		if p, ok := s.D.(Pauser); ok {
			if method == proto.MStop {
				return p.Pause(ctx, s.C, s.Ref)
			}
			return p.Resume(ctx, s.C, s.Ref)
		}
		return nil
	case proto.MDelete:
		var p proto.BoxRef
		decodeInto(params, &p)
		s.mu.Lock()
		delete(s.boxes, p.BoxID)
		s.mu.Unlock()
		err := s.D.Delete(ctx, s.C, s.Ref)
		if he, ok := err.(*HTTPError); ok && (he.Status == 404 || he.Status == 410) {
			return nil
		}
		return err
	case proto.MList:
		s.mu.Lock()
		ids := []string{}
		for id := range s.boxes {
			ids = append(ids, id)
		}
		s.mu.Unlock()
		return encodeOut(proto.ListResult{Boxes: ids}, out)
	case proto.MExec:
		var p proto.ExecParams
		if err := decodeInto(params, &p); err != nil {
			return err
		}
		t := p.TimeoutS
		if t <= 0 {
			t = 300
		}
		res, err := s.exec(ctx, p.BoxID, p.Command, p.Cwd, p.Env, t)
		if err != nil {
			return err
		}
		n := p.TailLines
		if n <= 0 {
			n = 50
		}
		so, t1 := boxd.ShapeBytes([]byte(res.Stdout), n)
		se, t2 := boxd.ShapeBytes([]byte(res.Stderr), n)
		saved := (len(res.Stdout) + len(res.Stderr) - len(so) - len(se)) / 4
		if saved < 0 {
			saved = 0
		}
		eo := boxd.ExecOut{ExitCode: res.ExitCode, DurationMS: res.DurationMS, StdoutTail: so, StderrTail: se, Truncated: t1 || t2,
			FilesChanged: []string{}, TokensSaved: saved, TimedOut: res.TimedOut}
		if eo.Truncated {
			eo.LogID = s.keepLog("$ " + p.Command + "\n--- stdout ---\n" + res.Stdout + "\n--- stderr ---\n" + res.Stderr)
		}
		return encodeOut(eo, out)
	case proto.MLogRead:
		var p proto.LogReadParams
		if err := decodeInto(params, &p); err != nil {
			return err
		}
		s.mu.Lock()
		c, ok := s.logs[p.LogID]
		s.mu.Unlock()
		if !ok {
			return &proto.Error{Code: "NOT_FOUND", Message: "log not found (a T3 sandbox keeps the last 16 truncated outputs in memory)"}
		}
		return encodeOut(proto.LogReadResult{Content: c}, out)
	case proto.MExecCancel:
		return nil // the provider's request/response exec cannot be cancelled mid-flight
	case proto.MRead:
		var p proto.ReadParams
		if err := decodeInto(params, &p); err != nil {
			return err
		}
		return s.read(ctx, p, out)
	case proto.MWrite:
		var p proto.WriteParams
		if err := decodeInto(params, &p); err != nil {
			return err
		}
		if err := s.write(ctx, p.BoxID, p.Path, []byte(p.Content)); err != nil {
			return err
		}
		return encodeOut(map[string]int{"bytes": len(p.Content)}, out)
	case proto.MGrep:
		var p proto.GrepParams
		if err := decodeInto(params, &p); err != nil {
			return err
		}
		res, err := s.exec(ctx, p.BoxID, "grep -rnI -e "+shq(p.Pattern)+" "+shq(s.boxPath(p.Path))+" 2>/dev/null | head -201", "/", nil, 60)
		if err != nil {
			return err
		}
		gr := proto.GrepResult{Matches: []proto.GrepMatch{}}
		for _, line := range strings.Split(strings.TrimRight(res.Stdout, "\n"), "\n") {
			parts := strings.SplitN(line, ":", 3)
			if len(parts) < 3 {
				continue
			}
			ln, err := strconv.Atoi(parts[1])
			if err != nil {
				continue
			}
			if len(gr.Matches) == 200 {
				gr.Truncated = true
				break
			}
			gr.Matches = append(gr.Matches, proto.GrepMatch{Path: strings.TrimPrefix(parts[0], s.WorkDir), Line: ln, Text: trim(parts[2], 400)})
		}
		return encodeOut(gr, out)
	case proto.MObserve:
		var p proto.BoxRef
		decodeInto(params, &p)
		res, err := s.exec(ctx, p.BoxID, "ps -eo pid=,args= 2>/dev/null | head -40", "/", nil, 30)
		if err != nil {
			return err
		}
		or := proto.ObserveResult{FilesChanged: []string{}, Processes: []proto.Proc{}, Ports: []int{}, Since: time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)}
		for _, line := range strings.Split(res.Stdout, "\n") {
			f := strings.Fields(line)
			if len(f) < 2 {
				continue
			}
			if pid, err := strconv.Atoi(f[0]); err == nil {
				or.Processes = append(or.Processes, proto.Proc{PID: pid, Cmd: trim(strings.Join(f[1:], " "), 200)})
			}
		}
		return encodeOut(or, out)
	}
	return unsupported(method)
}

func (s *Shim) read(ctx context.Context, p proto.ReadParams, out any) error {
	full := s.boxPath(p.Path)
	script := "if [ -d " + shq(full) + " ]; then echo __BOXA_DIR__; find " + shq(full) + " -mindepth 1 -maxdepth 1 -printf '%f\\t%y\\t%s\\n' 2>/dev/null | head -500; " +
		"elif [ -f " + shq(full) + " ]; then cat " + shq(full) + "; else echo 'no such file' >&2; exit 2; fi"
	if f, ok := s.D.(Filer); ok {
		if data, err := f.GetFile(ctx, s.C, s.Ref, full); err == nil {
			return encodeOut(lineRange(string(data), p.From, p.To), out)
		}
	}
	res, err := s.exec(ctx, p.BoxID, script, "/", nil, 60)
	if err != nil {
		return err
	}
	if res.ExitCode == 2 {
		return &proto.Error{Code: "NOT_FOUND", Message: "no such file: " + p.Path}
	}
	if strings.HasPrefix(res.Stdout, "__BOXA_DIR__\n") || res.Stdout == "__BOXA_DIR__" {
		rr := proto.ReadResult{IsDir: true, Entries: []proto.Entry{}}
		for _, line := range strings.Split(strings.TrimPrefix(res.Stdout, "__BOXA_DIR__\n"), "\n") {
			f := strings.Split(line, "\t")
			if len(f) != 3 {
				continue
			}
			typ := map[string]string{"d": "dir", "l": "symlink"}[f[1]]
			if typ == "" {
				typ = "file"
			}
			size, _ := strconv.ParseInt(f[2], 10, 64)
			rr.Entries = append(rr.Entries, proto.Entry{Name: f[0], Type: typ, Size: size})
		}
		return encodeOut(rr, out)
	}
	return encodeOut(lineRange(res.Stdout, p.From, p.To), out)
}

// lineRange mirrors boxd's read semantics (1-based inclusive range).
func lineRange(s string, from, to int) proto.ReadResult {
	s = strings.ToValidUTF8(s, "\uFFFD")
	lines := strings.Split(s, "\n")
	if strings.HasSuffix(s, "\n") {
		lines = lines[:len(lines)-1]
	}
	total := len(lines)
	if from > 0 || to > 0 {
		if from < 1 {
			from = 1
		}
		if to <= 0 || to > total {
			to = total
		}
		if from > total {
			return proto.ReadResult{TotalLines: total}
		}
		s = strings.Join(lines[from-1:to], "\n")
		if to > from-1 {
			s += "\n"
		}
	}
	return proto.ReadResult{Content: s, TotalLines: total}
}

func (s *Shim) keepLog(content string) string {
	id := secret.ID("log")
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logs[id] = content
	s.logIDs = append(s.logIDs, id)
	for len(s.logIDs) > shimLogKeep {
		delete(s.logs, s.logIDs[0])
		s.logIDs = s.logIDs[1:]
	}
	return id
}
