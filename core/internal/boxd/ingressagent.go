package boxd

// Ingress agent (`boxd ingress`): a tiny dial-out helper started inside a T3
// sandbox box whose provider has no preview URL. It keeps one WebSocket to
// core (/v1/ingress/connect) and answers http.do requests by proxying them to
// 127.0.0.1:<port> inside the sandbox, so core can publish the port at
// <public url>/x/<key>/ through its existing ingress Proxy. It reconnects on
// its own (sleep/wake, core restarts) with a short backoff.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/gorilla/websocket"

	"boxaz/core/internal/hostlink"
	"boxaz/core/internal/proto"
)

// IngressAgentConfig configures RunIngressAgent.
type IngressAgentConfig struct {
	CoreURL string // ws(s)://core/v1/ingress/connect
	BoxID   string
	Token   string
	// Target overrides the upstream (default 127.0.0.1:<port> of each request).
	Target string
}

// ErrIngressRejected means core refused the token (box gone or core rekeyed).
var ErrIngressRejected = errors.New("ingress token rejected")

// RunIngressAgent connects and reconnects until ctx ends or core rejects the
// token three times in a row.
func RunIngressAgent(ctx context.Context, cfg IngressAgentConfig) error {
	backoff := 500 * time.Millisecond
	rejected := 0
	for ctx.Err() == nil {
		start := time.Now()
		err := ingressOnce(ctx, cfg)
		if ctx.Err() != nil {
			return nil
		}
		if errors.Is(err, ErrIngressRejected) {
			rejected++
			if rejected >= 3 {
				return err
			}
		} else {
			rejected = 0
		}
		if time.Since(start) > 20*time.Second {
			backoff = 500 * time.Millisecond
		}
		log.Printf("ingress: tunnel ended: %v (retry in %s)", err, backoff)
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return nil
		}
		if backoff < 5*time.Second {
			backoff *= 2
		}
	}
	return nil
}

func ingressOnce(ctx context.Context, cfg IngressAgentConfig) error {
	u, err := url.Parse(cfg.CoreURL)
	if err != nil {
		return err
	}
	q := u.Query()
	q.Set("box", cfg.BoxID)
	u.RawQuery = q.Encode()
	h := http.Header{}
	h.Set("Authorization", "Bearer "+cfg.Token)
	d := websocket.Dialer{HandshakeTimeout: 15 * time.Second, Proxy: http.ProxyFromEnvironment}
	ws, resp, err := d.DialContext(ctx, u.String(), h)
	if err != nil {
		if resp != nil && (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusNotFound) {
			return ErrIngressRejected
		}
		return err
	}
	p := hostlink.NewPeer(ws)
	ws.SetPingHandler(func(m string) error {
		return ws.WriteControl(websocket.PongMessage, []byte(m), time.Now().Add(5*time.Second))
	})
	log.Printf("ingress: connected to core for box %s", cfg.BoxID)
	p.OnFrame = func(f proto.Frame) {
		if f.Type != proto.TReq {
			return
		}
		if f.Method != proto.MHTTP {
			p.Reply(f, nil, &proto.Error{Code: "INVALID", Message: "ingress agent only proxies http.do"})
			return
		}
		var hp proto.HTTPParams
		if err := json.Unmarshal(f.Params, &hp); err != nil {
			p.Reply(f, nil, &proto.Error{Code: "INVALID", Message: err.Error()})
			return
		}
		res := ingressDo(ctx, cfg.Target, hp)
		p.Reply(f, res, nil)
	}
	go func() {
		select {
		case <-ctx.Done():
			p.Close()
		case <-p.Done():
		}
	}()
	return p.ReadLoop()
}

var ingressAgentClient = &http.Client{
	Timeout: 60 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

func ingressDo(ctx context.Context, target string, p proto.HTTPParams) *proto.HTTPResult {
	addr := target
	if addr == "" {
		addr = "127.0.0.1:" + strconv.Itoa(p.Port)
	}
	req, err := http.NewRequestWithContext(ctx, p.Method, "http://"+addr+p.Path, bytes.NewReader(p.Body))
	if err != nil {
		return &proto.HTTPResult{Status: 400, Headers: map[string][]string{"Content-Type": {"text/plain"}}, Body: []byte(err.Error())}
	}
	for k, vs := range p.Headers {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := ingressAgentClient.Do(req)
	if err != nil {
		return &proto.HTTPResult{Status: 502, Headers: map[string][]string{"Content-Type": {"text/plain"}}, Body: []byte("upstream error: " + err.Error())}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	return &proto.HTTPResult{Status: resp.StatusCode, Headers: resp.Header, Body: body}
}
