package boxd

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"time"
)

// Port holder (Render and other "run a web service from an image"
// providers): the platform only marks the service live once something
// listens on its port ($PORT) and only routes inbound HTTP there. boxd binds
// that port itself, answers the health path, and forwards every other
// request (WebSocket upgrades included) to the box's preview port on
// loopback, so the provider's own URL is the box's public preview URL.

// PortHolderConfig configures RunPortHolder.
type PortHolderConfig struct {
	Listen     string // ":10000"
	HealthPath string // answered 200 "ok" by boxd itself
	Target     int    // in-box port the provider URL serves (0 = none: 404)
}

// PortHolderHandler is the holder's HTTP handler (exported for tests).
func PortHolderHandler(cfg PortHolderConfig) http.Handler {
	target, _ := url.Parse("http://127.0.0.1:" + strconv.Itoa(cfg.Target))
	rp := httputil.NewSingleHostReverseProxy(target)
	rp.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprintf(w, "nothing is listening on port %d inside the box yet (start the app on 0.0.0.0:%d)\n", cfg.Target, cfg.Target)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cfg.HealthPath != "" && r.URL.Path == cfg.HealthPath {
			w.Header().Set("Content-Type", "text/plain")
			w.Write([]byte("ok"))
			return
		}
		if cfg.Target <= 0 {
			http.NotFound(w, r)
			return
		}
		rp.ServeHTTP(w, r)
	})
}

// RunPortHolder serves until ctx ends (errors are logged, not fatal: the box
// still works through core without the provider URL).
func RunPortHolder(ctx context.Context, cfg PortHolderConfig) {
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		log.Printf("port holder: listen %s: %v", cfg.Listen, err)
		return
	}
	srv := &http.Server{Handler: PortHolderHandler(cfg), ReadHeaderTimeout: 30 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(sctx)
	}()
	log.Printf("port holder on %s → 127.0.0.1:%d (health %s)", cfg.Listen, cfg.Target, cfg.HealthPath)
	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		log.Printf("port holder: %v", err)
	}
}

// RunKeepAlive requests u every interval until ctx ends. A free web service
// that gets no inbound HTTP for 15 minutes is spun down (and loses its
// files); a request to its own public URL counts as inbound traffic.
func RunKeepAlive(ctx context.Context, u string, every time.Duration) {
	if every <= 0 {
		every = 5 * time.Minute
	}
	c := &http.Client{Timeout: 30 * time.Second}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
		if err != nil {
			log.Printf("keepalive: %v", err)
			return
		}
		if resp, err := c.Do(req); err == nil {
			resp.Body.Close()
		}
	}
}
