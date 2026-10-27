// Package hostlink implements both ends of the core⇄boxd dial-out tunnel.
package hostlink

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"boxaz/core/internal/proto"
)

// ErrClosed is returned for calls on a dead connection.
var ErrClosed = errors.New("host connection closed")

// Peer is a multiplexed JSON-frame connection. Both core and boxd use it.
type Peer struct {
	ws      *websocket.Conn
	wmu     sync.Mutex
	seq     atomic.Uint64
	mu      sync.Mutex
	pending map[string]chan proto.Frame
	closed  chan struct{}
	once    sync.Once
	// OnFrame handles frames that are not RPC responses (req, metrics, pty.*).
	OnFrame func(f proto.Frame)
}

func NewPeer(ws *websocket.Conn) *Peer {
	ws.SetReadLimit(64 << 20)
	return &Peer{ws: ws, pending: map[string]chan proto.Frame{}, closed: make(chan struct{})}
}

func (p *Peer) Done() <-chan struct{} { return p.closed }

func (p *Peer) Close() {
	p.once.Do(func() {
		close(p.closed)
		p.ws.Close()
		p.mu.Lock()
		for id, ch := range p.pending {
			close(ch)
			delete(p.pending, id)
		}
		p.mu.Unlock()
	})
}

// Send writes one frame.
func (p *Peer) Send(f proto.Frame) error {
	select {
	case <-p.closed:
		return ErrClosed
	default:
	}
	p.wmu.Lock()
	defer p.wmu.Unlock()
	p.ws.SetWriteDeadline(time.Now().Add(30 * time.Second))
	return p.ws.WriteJSON(f)
}

// Reply answers a request frame.
func (p *Peer) Reply(req proto.Frame, result any, err error) {
	f := proto.Frame{ID: req.ID, Type: proto.TRes}
	if err != nil {
		var pe *proto.Error
		if errors.As(err, &pe) {
			f.Error = pe
		} else {
			f.Error = &proto.Error{Code: "INTERNAL", Message: err.Error()}
		}
	} else if result != nil {
		b, merr := json.Marshal(result)
		if merr != nil {
			f.Error = &proto.Error{Code: "INTERNAL", Message: merr.Error()}
		} else {
			f.Result = b
		}
	}
	_ = p.Send(f)
}

// Call performs an RPC and decodes the result into out (may be nil).
func (p *Peer) Call(ctx context.Context, method string, params, out any) error {
	id := strconv.FormatUint(p.seq.Add(1), 10)
	b, err := json.Marshal(params)
	if err != nil {
		return err
	}
	ch := make(chan proto.Frame, 1)
	p.mu.Lock()
	select {
	case <-p.closed:
		p.mu.Unlock()
		return ErrClosed
	default:
	}
	p.pending[id] = ch
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		delete(p.pending, id)
		p.mu.Unlock()
	}()
	if err := p.Send(proto.Frame{ID: id, Type: proto.TReq, Method: method, Params: b}); err != nil {
		return err
	}
	select {
	case f, ok := <-ch:
		if !ok {
			return ErrClosed
		}
		if f.Error != nil {
			return f.Error
		}
		if out != nil && len(f.Result) > 0 {
			return json.Unmarshal(f.Result, out)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-p.closed:
		return ErrClosed
	}
}

// ReadLoop reads frames until the connection dies. Requests are dispatched
// to OnFrame in their own goroutine so long execs do not block the pipe.
func (p *Peer) ReadLoop() error {
	defer p.Close()
	for {
		var f proto.Frame
		if err := p.ws.ReadJSON(&f); err != nil {
			return fmt.Errorf("read: %w", err)
		}
		switch f.Type {
		case proto.TRes:
			p.mu.Lock()
			ch := p.pending[f.ID]
			p.mu.Unlock()
			if ch != nil {
				ch <- f
			}
		case proto.TReq:
			if p.OnFrame != nil {
				go p.OnFrame(f)
			}
		default:
			if p.OnFrame != nil {
				p.OnFrame(f) // ordered: pty data must stay in order
			}
		}
	}
}

// KeepAlive pings periodically so idle proxies do not cut the tunnel.
func (p *Peer) KeepAlive(every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-p.closed:
			return
		case <-t.C:
			p.wmu.Lock()
			err := p.ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second))
			p.wmu.Unlock()
			if err != nil {
				p.Close()
				return
			}
		}
	}
}
