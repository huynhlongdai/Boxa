// Package wsconn adapts a WebSocket to a net.Conn (binary frames = bytes) for
// the v1.10 TCP stream and SSH-over-WebSocket endpoints, on both ends: core
// (server) and the box CLI (box forward / box ssh-proxy). A ping every 25 s
// keeps Cloudflare and other proxies from closing an idle tunnel.
package wsconn

import (
	"io"
	"net"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// PingEvery is the heartbeat interval (Cloudflare closes idle WebSockets after ~100 s).
var PingEvery = 25 * time.Second

// Conn is a net.Conn over a WebSocket.
type Conn struct {
	ws   *websocket.Conn
	wmu  sync.Mutex
	r    io.Reader
	once sync.Once
	done chan struct{}
}

// New wraps ws and starts the heartbeat.
func New(ws *websocket.Conn) *Conn {
	c := &Conn{ws: ws, done: make(chan struct{})}
	ws.SetPingHandler(func(m string) error {
		c.wmu.Lock()
		defer c.wmu.Unlock()
		return ws.WriteControl(websocket.PongMessage, []byte(m), time.Now().Add(5*time.Second))
	})
	go c.heartbeat()
	return c
}

func (c *Conn) heartbeat() {
	t := time.NewTicker(PingEvery)
	defer t.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-t.C:
			c.wmu.Lock()
			err := c.ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second))
			c.wmu.Unlock()
			if err != nil {
				c.Close()
				return
			}
		}
	}
}

func (c *Conn) Read(b []byte) (int, error) {
	for {
		if c.r == nil {
			mt, r, err := c.ws.NextReader()
			if err != nil {
				if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.CloseNoStatusReceived) {
					return 0, io.EOF
				}
				return 0, err
			}
			if mt != websocket.BinaryMessage && mt != websocket.TextMessage {
				continue
			}
			c.r = r
		}
		n, err := c.r.Read(b)
		if err == io.EOF {
			c.r = nil
			if n > 0 {
				return n, nil
			}
			continue
		}
		return n, err
	}
}

func (c *Conn) Write(b []byte) (int, error) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	c.ws.SetWriteDeadline(time.Now().Add(60 * time.Second))
	if err := c.ws.WriteMessage(websocket.BinaryMessage, b); err != nil {
		return 0, err
	}
	return len(b), nil
}

// CloseWithReason sends a close frame with a reason, then closes.
func (c *Conn) CloseWithReason(code int, reason string) error {
	c.wmu.Lock()
	c.ws.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(code, reason), time.Now().Add(2*time.Second))
	c.wmu.Unlock()
	return c.Close()
}

func (c *Conn) Close() error {
	c.once.Do(func() {
		close(c.done)
		c.wmu.Lock()
		c.ws.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
		c.wmu.Unlock()
		c.ws.Close()
	})
	return nil
}

func (c *Conn) LocalAddr() net.Addr                { return c.ws.LocalAddr() }
func (c *Conn) RemoteAddr() net.Addr               { return c.ws.RemoteAddr() }
func (c *Conn) SetDeadline(t time.Time) error      { return c.ws.SetReadDeadline(t) }
func (c *Conn) SetReadDeadline(t time.Time) error  { return c.ws.SetReadDeadline(t) }
func (c *Conn) SetWriteDeadline(t time.Time) error { return nil }
