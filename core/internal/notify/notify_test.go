package notify

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestTelegram(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/botT0K/sendMessage" {
			w.WriteHeader(404)
			w.Write([]byte(`{"ok":false,"description":"Not Found"}`))
			return
		}
		json.NewDecoder(r.Body).Decode(&got)
		if got["chat_id"] == "bad" {
			w.WriteHeader(400)
			w.Write([]byte(`{"ok":false,"description":"Bad Request: chat not found"}`))
			return
		}
		w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	defer srv.Close()
	tg := Telegram{API: srv.URL, HTTP: srv.Client()}
	if err := tg.Send(context.Background(), "T0K", "123", "Chi phí hôm nay: $0.42"); err != nil {
		t.Fatal(err)
	}
	if got["chat_id"] != "123" || got["text"] != "Chi phí hôm nay: $0.42" {
		t.Fatalf("payload %+v", got)
	}
	err := tg.Send(context.Background(), "T0K", "bad", "x")
	if err == nil || !strings.Contains(err.Error(), "chat not found") {
		t.Fatalf("bad chat: %v", err)
	}
	err = tg.Send(context.Background(), "WRONG", "1", "x")
	if err == nil || strings.Contains(err.Error(), "WRONG") {
		t.Fatalf("error must not leak token: %v", err)
	}
	if err := tg.Send(context.Background(), "", "1", "x"); err == nil {
		t.Fatal("empty token")
	}
}

// fakeSMTP is a tiny SMTP server (no TLS) recording one message.
type fakeSMTP struct {
	ln   net.Listener
	mu   sync.Mutex
	auth string
	from string
	rcpt []string
	data string
}

func startFakeSMTP(t *testing.T) *fakeSMTP {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSMTP{ln: ln}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return f
}

func (f *fakeSMTP) serve(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	w := func(s string) { c.Write([]byte(s + "\r\n")) }
	w("220 fake ESMTP")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		up := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(up, "EHLO"), strings.HasPrefix(up, "HELO"):
			w("250-fake")
			w("250 AUTH PLAIN")
		case strings.HasPrefix(up, "AUTH PLAIN"):
			f.mu.Lock()
			f.auth = strings.TrimSpace(line[len("AUTH PLAIN"):])
			f.mu.Unlock()
			w("235 ok")
		case strings.HasPrefix(up, "MAIL FROM:"):
			f.mu.Lock()
			f.from = line[10:]
			f.mu.Unlock()
			w("250 ok")
		case strings.HasPrefix(up, "RCPT TO:"):
			f.mu.Lock()
			f.rcpt = append(f.rcpt, line[8:])
			f.mu.Unlock()
			w("250 ok")
		case up == "DATA":
			w("354 go")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil || l == ".\r\n" {
					break
				}
				b.WriteString(l)
			}
			f.mu.Lock()
			f.data = b.String()
			f.mu.Unlock()
			w("250 queued")
		case up == "QUIT":
			w("221 bye")
			return
		default:
			w("250 ok")
		}
	}
}

func TestSMTP(t *testing.T) {
	f := startFakeSMTP(t)
	_, port, _ := net.SplitHostPort(f.ln.Addr().String())
	var p int
	for _, ch := range port {
		p = p*10 + int(ch-'0')
	}
	s := SMTP{Host: "127.0.0.1", Port: p, Username: "u", Password: "pw", From: "Boxa <boxa@example.com>", TLS: "none"}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Send(ctx, Recipients("anh@example.com; ops@example.com, nope"), "[Boxa] Báo cáo", "Hôm qua: $1.23"); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	dec, _ := base64.StdEncoding.DecodeString(f.auth)
	if string(dec) != "\x00u\x00pw" || f.from != "<boxa@example.com>" || len(f.rcpt) != 2 {
		t.Fatalf("auth=%q from=%q rcpt=%v", dec, f.from, f.rcpt)
	}
	if !strings.Contains(f.data, "Subject: =?utf-8?q?") || !strings.Contains(f.data, base64.StdEncoding.EncodeToString([]byte("Hôm qua: $1.23"))) {
		t.Fatalf("data %q", f.data)
	}
	// starttls required but not offered → clear error
	s.TLS = "starttls"
	if err := s.Send(ctx, []string{"a@b.c"}, "x", "y"); err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("starttls: %v", err)
	}
	if err := (SMTP{}).Send(ctx, []string{"a@b.c"}, "x", "y"); err == nil {
		t.Fatal("unconfigured")
	}
}
