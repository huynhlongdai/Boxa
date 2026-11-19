package boxd

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPortHolder(t *testing.T) {
	// a free port with nothing behind it yet
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	h := httptest.NewServer(PortHolderHandler(PortHolderConfig{HealthPath: "/.well-known/mgr-health", Target: port}))
	defer h.Close()
	get := func(path string) (int, string) {
		resp, err := http.Get(h.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if c, b := get("/.well-known/mgr-health"); c != 200 || b != "ok" {
		t.Fatalf("health %d %q", c, b)
	}
	if c, b := get("/"); c != 502 || !strings.Contains(b, strconv.Itoa(port)) {
		t.Fatalf("no app → 502 naming the port, got %d %q", c, b)
	}
	app, _ := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("app " + r.URL.Path)) })}
	go srv.Serve(app)
	defer srv.Close()
	if c, b := get("/hello"); c != 200 || b != "app /hello" {
		t.Fatalf("forward %d %q", c, b)
	}
}

func TestPortHolderNoTarget(t *testing.T) {
	h := httptest.NewServer(PortHolderHandler(PortHolderConfig{HealthPath: "/hz", Target: 0}))
	defer h.Close()
	for path, want := range map[string]int{"/hz": 200, "/": 404} {
		resp, err := http.Get(h.URL + path)
		if err != nil || resp.StatusCode != want {
			t.Fatalf("%s: %v %v", path, resp, err)
		}
		resp.Body.Close()
	}
}

func TestKeepAlive(t *testing.T) {
	var n int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { atomic.AddInt32(&n, 1) }))
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 350*time.Millisecond)
	defer cancel()
	RunKeepAlive(ctx, s.URL+"/.well-known/mgr-health", 100*time.Millisecond)
	if atomic.LoadInt32(&n) < 2 {
		t.Fatalf("keepalive requests: %d", n)
	}
}
