package hostlink

// v1.10: processes in a box whose I/O rides multiplexed streams (proc.open).
// Used by the durable web terminal and the SSH gateway. The far end (boxd or
// the in-box ingress agent) runs the process under a PTY or with pipes and
// pumps: <id> = stdin / stdout (or PTY), <id>.e = stderr, <id>.x = exit code.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"

	"boxmes/core/internal/proto"
)

// Proc is the core end of a process opened with proc.open.
type Proc struct {
	p      *Peer
	ID     string
	Stdio  *StreamConn // write = stdin, read = stdout (PTY output in TTY mode)
	Stderr *StreamConn // pipe mode only (empty in TTY mode)
	exit   *StreamConn
}

func isUnsupported(err error) bool {
	var pe *proto.Error
	if errors.As(err, &pe) && pe.Code == "INVALID" && (strings.HasPrefix(pe.Message, "unknown method") || pe.Message == "ingress agent only proxies http.do") {
		return true
	}
	return false
}

// OpenProc starts a process in the box (params.Stream is set here).
func (p *Peer) OpenProc(ctx context.Context, params proto.ProcOpenParams) (*Proc, error) {
	id := nextStreamID()
	params.Stream = id
	pr := &Proc{p: p, ID: id, Stdio: newStreamConn(p, id), Stderr: newStreamConn(p, id+".e"), exit: newStreamConn(p, id+".x")}
	p.addStream(pr.Stdio)
	p.addStream(pr.Stderr)
	p.addStream(pr.exit)
	if err := p.Call(ctx, proto.MProcOpen, params, nil); err != nil {
		p.dropStream(id)
		p.dropStream(id + ".e")
		p.dropStream(id + ".x")
		if isUnsupported(err) {
			return nil, ErrStreamUnsupported
		}
		return nil, err
	}
	return pr, nil
}

// Wait blocks until the process exits and returns its exit code (-1 when the
// link died before the code arrived).
func (pr *Proc) Wait() int {
	b, _ := io.ReadAll(pr.exit)
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return -1
	}
	return n
}

// Resize changes the PTY window size.
func (pr *Proc) Resize(cols, rows int) error {
	return pr.p.Send(proto.Frame{Type: proto.TReq, ID: "", Method: proto.MProcResize, Params: mustJSON(proto.PtyResizeParams{Stream: pr.ID, Cols: cols, Rows: rows})})
}

// Signal sends a signal (INT, TERM, KILL, HUP…) to the process.
func (pr *Proc) Signal(sig string) error {
	return pr.p.Send(proto.Frame{Type: proto.TReq, ID: "", Method: proto.MProcSignal, Params: mustJSON(proto.ProcSignalParams{Stream: pr.ID, Signal: sig})})
}

// CloseStdin half-closes stdin (the process reads EOF).
func (pr *Proc) CloseStdin() error { return pr.Stdio.CloseWrite() }

// Close kills the process (HUP) and drops its streams.
func (pr *Proc) Close() {
	pr.Signal("HUP")
	pr.Stdio.Close()
	pr.Stderr.Close()
	pr.exit.Close()
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
