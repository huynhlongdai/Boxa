package manager

// v1.10 durable web terminal: a session id per PTY, the PTY kept alive for
// BOXMES_TERMINAL_DETACH_TTL after the last WebSocket drops, an output ring
// buffer replayed on (re)attach, several viewers with one controller, and
// list / kill. Sessions live in core (the PTY runs in the box through
// proc.open, or the legacy pty.open of older boxd), so they survive a
// browser reload or a Cloudflare edge restart, not a core restart.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"
	"time"

	"boxmes/core/internal/hostlink"
	"boxmes/core/internal/model"
	"boxmes/core/internal/proto"
	"boxmes/core/internal/secret"
)

const (
	defaultTermDetachTTL = 10 * time.Minute
	defaultTermBuffer    = 256 << 10
	maxTermsPerBox       = 8
	viewerQueue          = 512
)

type termState struct {
	mu sync.Mutex
	m  map[string]*termSess
}

// termIO is a PTY in the box.
type termIO interface {
	io.ReadWriter
	Resize(cols, rows int)
	Close()
	Wait() int
}

type procTerm struct{ pr *hostlink.Proc }

func (t procTerm) Read(b []byte) (int, error)  { return t.pr.Stdio.Read(b) }
func (t procTerm) Write(b []byte) (int, error) { return t.pr.Stdio.Write(b) }
func (t procTerm) Resize(c, r int)             { t.pr.Resize(c, r) }
func (t procTerm) Close()                      { t.pr.Close() }
func (t procTerm) Wait() int                   { return t.pr.Wait() }

// legacyTerm is the pre-v1.10 pty.open path (boxd without proc.open).
type legacyTerm struct {
	m      *Manager
	hostID string
	stream string
	r      *io.PipeReader
	w      *io.PipeWriter
}

func (t *legacyTerm) Read(b []byte) (int, error) { return t.r.Read(b) }
func (t *legacyTerm) Write(b []byte) (int, error) {
	if err := t.m.Hub.SendStream(t.hostID, t.stream, b); err != nil {
		return 0, err
	}
	return len(b), nil
}
func (t *legacyTerm) Resize(c, r int) { t.m.ResizeTerminal(t.hostID, t.stream, c, r) }
func (t *legacyTerm) Close() {
	t.m.CloseTerminal(t.hostID, t.stream)
	t.w.Close()
}
func (t *legacyTerm) Wait() int { return -1 }

// TermViewer is one attached WebSocket.
type TermViewer struct {
	Out  chan []byte         // output after the replay
	Ctl  chan map[string]any // control messages (control, exit, viewers)
	Done chan struct{}       // closed when the session drops this viewer
	once sync.Once
	// Reason says why Done closed ("" = session ended normally).
	Reason string
}

func newViewer() *TermViewer {
	return &TermViewer{Out: make(chan []byte, viewerQueue), Ctl: make(chan map[string]any, 16), Done: make(chan struct{})}
}

func (v *TermViewer) kick(reason string) {
	v.once.Do(func() {
		v.Reason = reason
		close(v.Done)
	})
}

func (v *TermViewer) notify(msg map[string]any) {
	select {
	case v.Ctl <- msg:
	default:
	}
}

type termSess struct {
	m       *Manager
	mu      sync.Mutex
	info    model.TerminalSession
	agentID string
	io      termIO
	ring    []byte
	ringMax int
	viewers map[*TermViewer]struct{}
	ctrl    *TermViewer
	timer   *time.Timer
	ended   bool
}

// TerminalDetachTTL is how long a detached terminal session is kept.
func (m *Manager) TerminalDetachTTL() time.Duration { return m.termDetachTTL() }

func (m *Manager) termDetachTTL() time.Duration {
	if m.Cfg.TerminalDetachTTL > 0 {
		return m.Cfg.TerminalDetachTTL
	}
	return defaultTermDetachTTL
}

func (m *Manager) termBuffer() int {
	if m.Cfg.TerminalBuffer > 0 {
		return m.Cfg.TerminalBuffer
	}
	return defaultTermBuffer
}

func (s *termSess) snapshot() *model.TerminalSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.info
	c.Viewers = len(s.viewers)
	c.Controlled = s.ctrl != nil
	c.Buffered = len(s.ring)
	return &c
}

func (s *termSess) pump() {
	buf := make([]byte, 32<<10)
	for {
		n, err := s.io.Read(buf)
		if n > 0 {
			chunk := append([]byte(nil), buf[:n]...)
			s.mu.Lock()
			s.ring = append(s.ring, chunk...)
			if over := len(s.ring) - s.ringMax; over > 0 {
				s.ring = append([]byte(nil), s.ring[over:]...)
			}
			s.info.LastIO = time.Now().UTC()
			for v := range s.viewers {
				select {
				case v.Out <- chunk:
				default: // too slow: drop it, it reattaches and replays
					delete(s.viewers, v)
					if s.ctrl == v {
						s.ctrl = nil
					}
					v.kick("viewer too slow; reattach to resume")
				}
			}
			s.mu.Unlock()
		}
		if err != nil {
			break
		}
	}
	code := s.io.Wait()
	s.end(&code, "")
}

// end marks the session over and drops it (exit code nil = killed).
func (s *termSess) end(code *int, reason string) {
	s.mu.Lock()
	if s.ended {
		s.mu.Unlock()
		return
	}
	s.ended = true
	s.info.Exited = true
	if code != nil && *code >= 0 {
		s.info.ExitCode = code
	}
	if s.timer != nil {
		s.timer.Stop()
	}
	vs := s.viewers
	s.viewers = map[*TermViewer]struct{}{}
	s.ctrl = nil
	s.mu.Unlock()
	msg := map[string]any{"type": "exit"}
	if code != nil && *code >= 0 {
		msg["code"] = *code
	}
	if reason != "" {
		msg["reason"] = reason
	}
	for v := range vs {
		v.notify(msg)
		v.kick(reason)
	}
	s.io.Close()
	s.m.acc.terms.mu.Lock()
	delete(s.m.acc.terms.m, s.info.ID)
	s.m.acc.terms.mu.Unlock()
}

func (s *termSess) broadcastViewers() {
	n := len(s.viewers)
	for v := range s.viewers {
		v.notify(map[string]any{"type": "viewers", "n": n, "control": v == s.ctrl})
	}
}

// Attach adds a viewer and returns the output to replay first. control asks
// for input control (the newest controller wins; the previous one is told).
func (s *termSess) Attach(v *TermViewer, control bool) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return nil, model.EH(model.ErrNotFound, "Open a new terminal session.", "terminal session %s has ended", s.info.ID)
	}
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	s.info.DetachedAt, s.info.ExpiresAt = nil, nil
	s.viewers[v] = struct{}{}
	if control {
		if s.ctrl != nil && s.ctrl != v {
			s.ctrl.notify(map[string]any{"type": "control", "control": false})
		}
		s.ctrl = v
	}
	s.broadcastViewers()
	return append([]byte(nil), s.ring...), nil
}

// Detach removes a viewer; with none left the PTY is kept for the detach TTL.
func (s *termSess) Detach(v *TermViewer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.viewers[v]; !ok {
		return
	}
	delete(s.viewers, v)
	if s.ctrl == v {
		s.ctrl = nil
	}
	if len(s.viewers) > 0 || s.ended {
		s.broadcastViewers()
		return
	}
	ttl := s.m.termDetachTTL()
	now := time.Now().UTC()
	exp := now.Add(ttl)
	s.info.DetachedAt, s.info.ExpiresAt = &now, &exp
	s.timer = time.AfterFunc(ttl, func() { s.end(nil, "detached too long") })
}

// Input writes keystrokes from the controlling viewer.
func (s *termSess) Input(v *TermViewer, b []byte) bool {
	s.mu.Lock()
	ok := s.ctrl == v
	s.mu.Unlock()
	if ok {
		s.io.Write(b)
	}
	return ok
}

// Resize applies a window size from the controlling viewer.
func (s *termSess) Resize(v *TermViewer, cols, rows int) {
	if cols < 1 || rows < 1 || cols > 1000 || rows > 1000 {
		return
	}
	s.mu.Lock()
	ok := s.ctrl == v
	if ok {
		s.info.Cols, s.info.Rows = cols, rows
	}
	s.mu.Unlock()
	if ok {
		s.io.Resize(cols, rows)
	}
}

// TakeControl makes v the controller (control=false releases it).
func (s *termSess) TakeControl(v *TermViewer, control bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.viewers[v]; !ok {
		return
	}
	switch {
	case control && s.ctrl != v:
		if s.ctrl != nil {
			s.ctrl.notify(map[string]any{"type": "control", "control": false})
		}
		s.ctrl = v
	case !control && s.ctrl == v:
		s.ctrl = nil
	default:
		return
	}
	s.broadcastViewers()
}

// TermHandle is an attached viewer of a session.
type TermHandle struct {
	Sess    *termSess
	Viewer  *TermViewer
	Replay  []byte
	Created bool
	Info    *model.TerminalSession
}

// ID of the attached session.
func (h *TermHandle) ID() string { return h.Sess.info.ID }

// Input / Resize / Control / Detach forward to the session.
func (h *TermHandle) Input(b []byte) bool        { return h.Sess.Input(h.Viewer, b) }
func (h *TermHandle) Resize(cols, rows int)      { h.Sess.Resize(h.Viewer, cols, rows) }
func (h *TermHandle) Control(on bool)            { h.Sess.TakeControl(h.Viewer, on) }
func (h *TermHandle) Detach()                    { h.Sess.Detach(h.Viewer) }
func (h *TermHandle) Snapshot() *model.TerminalSession { return h.Sess.snapshot() }

func (m *Manager) termGet(id string) *termSess {
	m.acc.terms.mu.Lock()
	defer m.acc.terms.mu.Unlock()
	return m.acc.terms.m[id]
}

func (m *Manager) boxTerms(boxID string) []*termSess {
	m.acc.terms.mu.Lock()
	defer m.acc.terms.mu.Unlock()
	var out []*termSess
	for _, s := range m.acc.terms.m {
		if s.info.BoxID == boxID {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].info.CreatedAt.Before(out[j].info.CreatedAt) })
	return out
}

func (m *Manager) listTerms(boxID string) []*model.TerminalSession {
	out := []*model.TerminalSession{}
	for _, s := range m.boxTerms(boxID) {
		out = append(out, s.snapshot())
	}
	return out
}

func (m *Manager) killBoxTerminals(boxID, reason string) {
	for _, s := range m.boxTerms(boxID) {
		s.end(nil, reason)
	}
}

// openTermIO starts a PTY shell in the box over its link.
func (m *Manager) openTermIO(ctx context.Context, b *model.Box, peer *hostlink.Peer, cols, rows int) (termIO, error) {
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	pr, err := peer.OpenProc(cctx, proto.ProcOpenParams{BoxID: b.ID, TTY: true, Cols: cols, Rows: rows})
	if err == nil {
		return procTerm{pr}, nil
	}
	if !errors.Is(err, hostlink.ErrStreamUnsupported) {
		return nil, mapHostErr(err)
	}
	if m.Hub.IsVirtual(b.HostID) {
		return nil, model.EH(model.ErrProviderFailed, "Restart the in-box agent (sleep and wake the box) so it picks up the current boxd.", "the in-box agent of box %s is too old for terminals", b.ID)
	}
	r, w := io.Pipe()
	t := &legacyTerm{m: m, hostID: b.HostID, stream: secret.ID("pty"), r: r, w: w}
	m.Hub.AddStream(t.stream, func(data []byte, exited bool) {
		if len(data) > 0 {
			w.Write(data)
		}
		if exited {
			w.Close()
		}
	})
	if err := m.Hub.Call(cctx, b.HostID, proto.MPtyOpen, proto.PtyOpenParams{BoxID: b.ID, Stream: t.stream, Cols: cols, Rows: rows}, nil); err != nil {
		m.Hub.RemoveStream(t.stream)
		return nil, mapHostErr(err)
	}
	return t, nil
}

// TerminalAttach attaches to session sid of box b ("" or "new" = a new
// session; the box is woken first). control asks for input control.
func (m *Manager) TerminalAttach(ctx context.Context, p *Principal, b *model.Box, sid, name string, cols, rows int, control bool) (*TermHandle, error) {
	if err := p.Require(model.ScopeBoxExec); err != nil {
		return nil, err
	}
	if !p.Admin && b.AgentID != p.AgentID {
		return nil, notFound("box", b.ID)
	}
	v := newViewer()
	if sid != "" && sid != "new" {
		s := m.termGet(sid)
		if s == nil || s.info.BoxID != b.ID {
			return nil, model.EH(model.ErrNotFound, "Open a new terminal session.", "terminal session %s has ended", sid)
		}
		replay, err := s.Attach(v, control)
		if err != nil {
			return nil, err
		}
		return &TermHandle{Sess: s, Viewer: v, Replay: replay, Info: s.snapshot()}, nil
	}
	if len(m.boxTerms(b.ID)) >= maxTermsPerBox {
		return nil, model.EH(model.ErrPolicyDenied, "Kill an old session: DELETE /v1/boxes/{id}/terminals/{session_id}.", "box %s already has %d terminal sessions", b.ID, maxTermsPerBox)
	}
	b, peer, err := m.AccessLink(ctx, b)
	if err != nil {
		return nil, err
	}
	if cols <= 0 {
		cols = 120
	}
	if rows <= 0 {
		rows = 32
	}
	tio, err := m.openTermIO(ctx, b, peer, cols, rows)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	s := &termSess{m: m, io: tio, ringMax: m.termBuffer(), viewers: map[*TermViewer]struct{}{}, agentID: b.AgentID,
		info: model.TerminalSession{ID: secret.ID("term"), BoxID: b.ID, Name: name, Cols: cols, Rows: rows, CreatedAt: now, LastIO: now, Actor: p.Actor()}}
	m.acc.terms.mu.Lock()
	if m.acc.terms.m == nil {
		m.acc.terms.m = map[string]*termSess{}
	}
	m.acc.terms.m[s.info.ID] = s
	m.acc.terms.mu.Unlock()
	replay, _ := s.Attach(v, control)
	go s.pump()
	m.Emit(ctx, p.Actor(), b.ID, b.AgentID, "box.terminal", fmt.Sprintf("terminal session %s opened", s.info.ID), nil)
	return &TermHandle{Sess: s, Viewer: v, Replay: replay, Created: true, Info: s.snapshot()}, nil
}

// ListTerminals lists the box's terminal sessions.
func (m *Manager) ListTerminals(ctx context.Context, p *Principal, ref string) ([]*model.TerminalSession, error) {
	b, err := m.ResolveBox(ctx, p, ref)
	if err != nil {
		return nil, err
	}
	return m.listTerms(b.ID), nil
}

// KillTerminal ends a session ("all" = every session of the box).
func (m *Manager) KillTerminal(ctx context.Context, p *Principal, ref, sid string) (int, error) {
	if err := p.Require(model.ScopeBoxExec); err != nil {
		return 0, err
	}
	b, err := m.ResolveBox(ctx, p, ref)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, s := range m.boxTerms(b.ID) {
		if sid == "all" || s.info.ID == sid {
			s.end(nil, "killed")
			n++
		}
	}
	if n == 0 && sid != "all" {
		return 0, model.E(model.ErrNotFound, "terminal session %s not found", sid)
	}
	m.Emit(ctx, p.Actor(), b.ID, b.AgentID, "box.terminal", fmt.Sprintf("%d terminal session(s) killed", n), nil)
	return n, nil
}
