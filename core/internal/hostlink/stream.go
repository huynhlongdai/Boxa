package hostlink

// v1.7: raw byte streams multiplexed over a Peer (tcp.data / tcp.close
// frames). The gateway uses them to pass HTTP, WebSocket and SSE through to a
// port inside a box without buffering: core opens a stream with a tcp.open
// RPC, the far end (boxd or the in-box ingress agent) dials the port and pumps
// bytes both ways.

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"strconv"
	"sync"
	"time"

	"boxaz/core/internal/proto"
)

// streamChunk bounds one tcp.data frame.
const streamChunk = 32 << 10

// StreamConn is one end of a multiplexed stream; it implements net.Conn.
type StreamConn struct {
	p      *Peer
	id     string
	mu     sync.Mutex
	cond   *sync.Cond
	buf    [][]byte
	eof    bool // remote closed
	closed bool // local closed
	rdl    time.Time
	timer  *time.Timer
}

func newStreamConn(p *Peer, id string) *StreamConn {
	c := &StreamConn{p: p, id: id}
	c.cond = sync.NewCond(&c.mu)
	return c
}

func (p *Peer) addStream(c *StreamConn) {
	p.mu.Lock()
	if p.streams == nil {
		p.streams = map[string]*StreamConn{}
	}
	p.streams[c.id] = c
	p.mu.Unlock()
}

func (p *Peer) dropStream(id string) {
	p.mu.Lock()
	delete(p.streams, id)
	p.mu.Unlock()
}

// deliverStream routes a tcp.data / tcp.close frame (called from ReadLoop,
// never blocks: data is queued).
func (p *Peer) deliverStream(f proto.Frame) {
	p.mu.Lock()
	c := p.streams[f.Stream]
	p.mu.Unlock()
	if c == nil {
		if f.Type == proto.TTCPData {
			_ = p.Send(proto.Frame{Type: proto.TTCPClose, Stream: f.Stream})
		}
		return
	}
	c.mu.Lock()
	if f.Type == proto.TTCPClose || f.Type == proto.TTCPEOF {
		c.eof = true
	} else if len(f.Data) > 0 {
		c.buf = append(c.buf, f.Data)
	}
	c.cond.Broadcast()
	c.mu.Unlock()
	if f.Type == proto.TTCPClose {
		p.dropStream(f.Stream)
	}
}

// closeStreams ends every stream when the peer dies.
func (p *Peer) closeStreams() {
	p.mu.Lock()
	ss := p.streams
	p.streams = nil
	p.mu.Unlock()
	for _, c := range ss {
		c.mu.Lock()
		c.eof = true
		c.cond.Broadcast()
		c.mu.Unlock()
	}
}

var streamSeq struct {
	sync.Mutex
	n uint64
}

func nextStreamID() string {
	streamSeq.Lock()
	defer streamSeq.Unlock()
	streamSeq.n++
	return "s" + strconv.FormatInt(time.Now().UnixNano()%1e9, 36) + "-" + strconv.FormatUint(streamSeq.n, 36)
}

// ErrStreamUnsupported means the far end predates tcp.open (old boxd): use the
// buffered http.do path instead.
var ErrStreamUnsupported = errors.New("stream not supported by the remote agent")

// OpenStream asks the far end to connect to boxID:port and returns the stream.
func (p *Peer) OpenStream(ctx context.Context, boxID string, port int) (*StreamConn, error) {
	id := nextStreamID()
	c := newStreamConn(p, id)
	p.addStream(c)
	err := p.Call(ctx, proto.MTCPOpen, proto.TCPOpenParams{BoxID: boxID, Port: port, Stream: id}, nil)
	if err != nil {
		p.dropStream(id)
		var pe *proto.Error
		if errors.As(err, &pe) && pe.Code == "INVALID" && (len(pe.Message) > 14 && pe.Message[:14] == "unknown method" || pe.Message == "ingress agent only proxies http.do") {
			return nil, ErrStreamUnsupported
		}
		return nil, err
	}
	return c, nil
}

// ServeStream answers a tcp.open request: target is already dialled; bytes
// are pumped until either side closes.
func (p *Peer) ServeStream(stream string, target net.Conn) {
	c := newStreamConn(p, stream)
	p.addStream(c)
	go func() {
		io.Copy(target, c)
		if tc, ok := target.(*net.TCPConn); ok {
			tc.CloseWrite()
		} else {
			target.Close()
		}
	}()
	go func() {
		io.Copy(c, target)
		target.Close()
		c.Close()
	}()
}

func (c *StreamConn) Read(b []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for len(c.buf) == 0 {
		if c.eof || c.closed {
			return 0, io.EOF
		}
		if !c.rdl.IsZero() && !time.Now().Before(c.rdl) {
			return 0, os.ErrDeadlineExceeded
		}
		select {
		case <-c.p.closed:
			return 0, io.EOF
		default:
		}
		c.cond.Wait()
	}
	n := copy(b, c.buf[0])
	if n == len(c.buf[0]) {
		c.buf = c.buf[1:]
	} else {
		c.buf[0] = c.buf[0][n:]
	}
	return n, nil
}

func (c *StreamConn) Write(b []byte) (int, error) {
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return 0, net.ErrClosed
	}
	n := 0
	for len(b) > 0 {
		k := len(b)
		if k > streamChunk {
			k = streamChunk
		}
		if err := c.p.Send(proto.Frame{Type: proto.TTCPData, Stream: c.id, Data: append([]byte(nil), b[:k]...)}); err != nil {
			return n, err
		}
		n += k
		b = b[k:]
	}
	return n, nil
}

// CloseWrite half-closes the stream (v1.10): the far end reads EOF, and the
// stream still delivers what the far end writes.
func (c *StreamConn) CloseWrite() error {
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return net.ErrClosed
	}
	return c.p.Send(proto.Frame{Type: proto.TTCPEOF, Stream: c.id})
}

// AttachStream registers the far end of a stream opened by the other side
// (proc.open: the box end of a process's streams).
func (p *Peer) AttachStream(id string) *StreamConn {
	c := newStreamConn(p, id)
	p.addStream(c)
	return c
}

// Close ends the stream on both sides.
func (c *StreamConn) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	if c.timer != nil {
		c.timer.Stop()
	}
	c.cond.Broadcast()
	c.mu.Unlock()
	c.p.dropStream(c.id)
	return c.p.Send(proto.Frame{Type: proto.TTCPClose, Stream: c.id})
}

type streamAddr string

func (a streamAddr) Network() string { return "boxaz-stream" }
func (a streamAddr) String() string  { return string(a) }

func (c *StreamConn) LocalAddr() net.Addr  { return streamAddr("local:" + c.id) }
func (c *StreamConn) RemoteAddr() net.Addr { return streamAddr("box:" + c.id) }

func (c *StreamConn) SetDeadline(t time.Time) error { return c.SetReadDeadline(t) }

func (c *StreamConn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rdl = t
	if c.timer != nil {
		c.timer.Stop()
		c.timer = nil
	}
	if !t.IsZero() {
		c.timer = time.AfterFunc(time.Until(t), func() {
			c.mu.Lock()
			c.cond.Broadcast()
			c.mu.Unlock()
		})
	}
	c.cond.Broadcast()
	return nil
}

func (c *StreamConn) SetWriteDeadline(t time.Time) error { return nil }
