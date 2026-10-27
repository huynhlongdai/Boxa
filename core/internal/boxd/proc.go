package boxd

// v1.10 proc.open: run a process in a box with its I/O on multiplexed streams
// (see hostlink/proc.go for the core end). Shared by the host agent (boxd,
// command built by the driver) and the in-box ingress agent (T3 sandboxes,
// command run locally). Used by the durable web terminal and the SSH gateway.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"

	"boxaz/core/internal/hostlink"
	"boxaz/core/internal/proto"
)

// ProcCmder is implemented by drivers that can build an arbitrary command
// (shell or exec, TTY or not) inside a box.
type ProcCmder interface {
	ProcCmd(ctx context.Context, id string, p proto.ProcOpenParams) (*exec.Cmd, error)
}

type procEntry struct {
	cmd *exec.Cmd
	pty *os.File
}

// procTable tracks live processes by stream id (resize / signal).
type procTable struct {
	mu sync.Mutex
	m  map[string]*procEntry
}

func (t *procTable) get(id string) *procEntry {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.m[id]
}

func (t *procTable) put(id string, e *procEntry) {
	t.mu.Lock()
	if t.m == nil {
		t.m = map[string]*procEntry{}
	}
	t.m[id] = e
	t.mu.Unlock()
}

func (t *procTable) del(id string) {
	t.mu.Lock()
	delete(t.m, id)
	t.mu.Unlock()
}

var sigNames = map[string]syscall.Signal{"INT": syscall.SIGINT, "TERM": syscall.SIGTERM, "KILL": syscall.SIGKILL, "HUP": syscall.SIGHUP,
	"QUIT": syscall.SIGQUIT, "USR1": syscall.SIGUSR1, "USR2": syscall.SIGUSR2, "ALRM": syscall.SIGALRM, "PIPE": syscall.SIGPIPE}

// handleCtl answers proc.resize / proc.signal (fire-and-forget from core).
func (t *procTable) handleCtl(f proto.Frame) error {
	switch f.Method {
	case proto.MProcResize:
		var p proto.PtyResizeParams
		if err := json.Unmarshal(f.Params, &p); err != nil {
			return &proto.Error{Code: "INVALID", Message: err.Error()}
		}
		if e := t.get(p.Stream); e != nil && e.pty != nil && p.Cols > 0 && p.Rows > 0 {
			pty.Setsize(e.pty, &pty.Winsize{Cols: uint16(p.Cols), Rows: uint16(p.Rows)})
		}
	case proto.MProcSignal:
		var p proto.ProcSignalParams
		if err := json.Unmarshal(f.Params, &p); err != nil {
			return &proto.Error{Code: "INVALID", Message: err.Error()}
		}
		e := t.get(p.Stream)
		sig, ok := sigNames[strings.TrimPrefix(strings.ToUpper(p.Signal), "SIG")]
		if e == nil || !ok || e.cmd.Process == nil {
			return nil
		}
		pid := e.cmd.Process.Pid
		if syscall.Kill(-pid, sig) != nil { // process group (setsid / setpgid)
			e.cmd.Process.Signal(sig)
		}
		if sig == syscall.SIGHUP {
			go func() { // a process that ignores HUP is killed after 5 s
				time.Sleep(5 * time.Second)
				if t.get(p.Stream) == e {
					syscall.Kill(-pid, syscall.SIGKILL)
				}
			}()
		}
	}
	return nil
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return 128 + int(ws.Signal())
		}
		return ee.ExitCode()
	}
	return 255
}

// serve starts cmd and wires it to the streams of sp.Stream.
func (t *procTable) serve(p *hostlink.Peer, sp proto.ProcOpenParams, cmd *exec.Cmd) error {
	if cmd.Env == nil {
		cmd.Env = os.Environ()
	}
	if sp.TTY {
		term := sp.Term
		if term == "" {
			term = "xterm-256color"
		}
		cmd.Env = append(cmd.Env, "TERM="+term)
	}
	cmd.Env = append(cmd.Env, sp.Env...)
	main := p.AttachStream(sp.Stream)
	errS := p.AttachStream(sp.Stream + ".e")
	ex := p.AttachStream(sp.Stream + ".x")
	fail := func(err error) error {
		main.Close()
		errS.Close()
		ex.Close()
		return &proto.Error{Code: "UNAVAILABLE", Message: "start: " + err.Error()}
	}
	finish := func(code int) {
		ex.Write([]byte(strconv.Itoa(code)))
		ex.Close()
		errS.Close()
		main.Close()
		t.del(sp.Stream)
	}
	if sp.TTY {
		cols, rows := sp.Cols, sp.Rows
		if cols <= 0 {
			cols = 120
		}
		if rows <= 0 {
			rows = 32
		}
		f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
		if err != nil {
			return fail(err)
		}
		t.put(sp.Stream, &procEntry{cmd: cmd, pty: f})
		go io.Copy(f, main)
		outDone := make(chan struct{})
		go func() {
			io.Copy(main, f)
			close(outDone)
		}()
		go func() {
			code := exitCode(cmd.Wait())
			select { // drain what is left, then unblock a read held by a background child
			case <-outDone:
			case <-time.After(300 * time.Millisecond):
				f.Close()
				<-outDone
			}
			f.Close()
			finish(code)
		}()
		return nil
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fail(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fail(err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fail(err)
	}
	if err := cmd.Start(); err != nil {
		return fail(err)
	}
	t.put(sp.Stream, &procEntry{cmd: cmd})
	go func() {
		io.Copy(stdin, main)
		stdin.Close()
	}()
	go func() {
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { io.Copy(main, stdout); wg.Done() }()
		go func() { io.Copy(errS, stderr); wg.Done() }()
		wg.Wait()
		finish(exitCode(cmd.Wait()))
	}()
	return nil
}

// localProcCmd builds the command for the ingress agent (runs in the box
// itself): the login shell, or `<shell> -c <command>`.
func localProcCmd(sp proto.ProcOpenParams) *exec.Cmd {
	sh := os.Getenv("SHELL")
	if sh == "" {
		for _, c := range []string{"/bin/bash", "/usr/bin/bash", "/bin/sh"} {
			if _, err := os.Stat(c); err == nil {
				sh = c
				break
			}
		}
	}
	var cmd *exec.Cmd
	if sp.Command == "" {
		cmd = exec.Command(sh, "-l")
	} else {
		cmd = exec.Command(sh, "-c", sp.Command)
	}
	cmd.Dir = sp.Cwd
	if cmd.Dir == "" {
		cmd.Dir, _ = os.UserHomeDir()
	}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "BOXD_") { // never leak the agent's token
			cmd.Env = append(cmd.Env, kv)
		}
	}
	return cmd
}
