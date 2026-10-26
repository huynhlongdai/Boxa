package hostlink

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"boxa/core/internal/proto"
)

// Registrar is implemented by the box manager: it authenticates host tokens
// and records host state.
type Registrar interface {
	// AuthHost validates a bearer token before upgrade. It returns an opaque
	// auth handle passed back to RegisterHost.
	AuthHost(ctx context.Context, token string) (any, error)
	RegisterHost(ctx context.Context, auth any, h proto.Hello) (proto.Welcome, error)
	HostMetrics(hostID string, m proto.Metrics)
	HostDisconnected(hostID string)
}

// StreamSink receives PTY output for one stream.
type StreamSink func(data []byte, exited bool)

// Hub keeps the live boxd connections.
type Hub struct {
	reg      Registrar
	mu       sync.RWMutex
	conns    map[string]*Peer
	streams  map[string]StreamSink
	upgrader websocket.Upgrader
}

func NewHub(reg Registrar) *Hub {
	return &Hub{
		reg:      reg,
		conns:    map[string]*Peer{},
		streams:  map[string]StreamSink{},
		upgrader: websocket.Upgrader{ReadBufferSize: 32 << 10, WriteBufferSize: 32 << 10},
	}
}

// Connected reports whether a host has a live tunnel.
func (h *Hub) Connected(hostID string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	_, ok := h.conns[hostID]
	return ok
}

func (h *Hub) Count() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.conns)
}

// Peer returns the connection for a host.
func (h *Hub) Peer(hostID string) (*Peer, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	p, ok := h.conns[hostID]
	return p, ok
}

// Call is a convenience RPC to a host.
func (h *Hub) Call(ctx context.Context, hostID, method string, params, out any) error {
	p, ok := h.Peer(hostID)
	if !ok {
		return ErrClosed
	}
	return p.Call(ctx, method, params, out)
}

// AddStream registers a PTY sink.
func (h *Hub) AddStream(id string, sink StreamSink) {
	h.mu.Lock()
	h.streams[id] = sink
	h.mu.Unlock()
}

func (h *Hub) RemoveStream(id string) {
	h.mu.Lock()
	delete(h.streams, id)
	h.mu.Unlock()
}

// SendStream writes PTY input to a host.
func (h *Hub) SendStream(hostID, stream string, data []byte) error {
	p, ok := h.Peer(hostID)
	if !ok {
		return ErrClosed
	}
	return p.Send(proto.Frame{Type: proto.TPtyData, Stream: stream, Data: data})
}

func bearer(r *http.Request) string {
	a := r.Header.Get("Authorization")
	if strings.HasPrefix(a, "Bearer ") {
		return strings.TrimSpace(a[7:])
	}
	return r.URL.Query().Get("token")
}

// ServeHTTP accepts a boxd dial-out connection.
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	auth, err := h.reg.AuthHost(r.Context(), bearer(r))
	if err != nil {
		http.Error(w, `{"error":{"code":"UNAUTHORIZED","message":"bad host token","hint":"check BOXD_TOKEN"}}`, http.StatusUnauthorized)
		return
	}
	ws, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	p := NewPeer(ws)
	ws.SetReadDeadline(time.Now().Add(20 * time.Second))
	var first proto.Frame
	if err := ws.ReadJSON(&first); err != nil || first.Type != proto.THello {
		ws.Close()
		return
	}
	var hello proto.Hello
	_ = json.Unmarshal(first.Params, &hello)
	welcome, err := h.reg.RegisterHost(context.Background(), auth, hello)
	if err != nil {
		_ = p.Send(proto.Frame{Type: proto.TWelcome, Error: &proto.Error{Code: "UNAUTHORIZED", Message: err.Error()}})
		ws.Close()
		return
	}
	wb, _ := json.Marshal(welcome)
	if err := p.Send(proto.Frame{Type: proto.TWelcome, Result: wb}); err != nil {
		ws.Close()
		return
	}
	ws.SetReadDeadline(time.Time{})
	ws.SetPongHandler(func(string) error { return nil })
	hostID := welcome.HostID

	h.mu.Lock()
	if old, ok := h.conns[hostID]; ok {
		old.Close()
	}
	h.conns[hostID] = p
	h.mu.Unlock()
	log.Printf("hostlink: host %s connected (%s, driver=%s)", hostID, hello.Hostname, hello.Driver)

	p.OnFrame = func(f proto.Frame) {
		switch f.Type {
		case proto.TMetrics:
			var m proto.Metrics
			if json.Unmarshal(f.Params, &m) == nil {
				h.reg.HostMetrics(hostID, m)
			}
		case proto.TPtyData, proto.TPtyExit:
			h.mu.RLock()
			sink := h.streams[f.Stream]
			h.mu.RUnlock()
			if sink != nil {
				sink(f.Data, f.Type == proto.TPtyExit)
			}
		case proto.TReq:
			p.Reply(f, nil, &proto.Error{Code: "INVALID", Message: "core accepts no requests"})
		}
	}
	go p.KeepAlive(25 * time.Second)
	err = p.ReadLoop()
	h.mu.Lock()
	if h.conns[hostID] == p {
		delete(h.conns, hostID)
		h.mu.Unlock()
		h.reg.HostDisconnected(hostID)
	} else {
		h.mu.Unlock()
	}
	log.Printf("hostlink: host %s disconnected: %v", hostID, err)
}
