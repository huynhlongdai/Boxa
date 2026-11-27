// Package notify delivers Boxmes reports and alerts over Telegram (Bot API
// sendMessage) and email (SMTP with STARTTLS / implicit TLS).
package notify

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// Telegram sends messages through the Bot API.
type Telegram struct {
	API  string // default https://api.telegram.org
	HTTP *http.Client
}

// Send posts text to chatID using bot token. Text is plain (no parse mode) so
// box names and commands never break formatting.
func (t Telegram) Send(ctx context.Context, token, chatID, text string) error {
	if token == "" || chatID == "" {
		return fmt.Errorf("telegram bot token and chat id are required")
	}
	api := strings.TrimRight(t.API, "/")
	if api == "" {
		api = "https://api.telegram.org"
	}
	hc := t.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 20 * time.Second}
	}
	if len(text) > 4000 { // Telegram limit is 4096 chars
		text = text[:4000] + "…"
	}
	body, _ := json.Marshal(map[string]any{"chat_id": chatID, "text": text, "disable_web_page_preview": true})
	req, err := http.NewRequestWithContext(ctx, "POST", api+"/bot"+token+"/sendMessage", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		// never leak the token embedded in the URL
		return fmt.Errorf("telegram request failed: %s", strings.ReplaceAll(err.Error(), token, "***"))
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var out struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	json.Unmarshal(b, &out)
	if resp.StatusCode >= 300 || !out.OK {
		d := out.Description
		if d == "" {
			d = strings.TrimSpace(string(b))
		}
		return fmt.Errorf("telegram HTTP %d: %s", resp.StatusCode, d)
	}
	return nil
}

// SMTP is an outgoing mail server.
type SMTP struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"-"`
	From     string `json:"from"`
	TLS      string `json:"tls"` // starttls (default, port 587) | tls (465) | none (local relays only)
	// InsecureSkipVerify is for tests only.
	InsecureSkipVerify bool `json:"-"`
}

func (s SMTP) Configured() bool { return s.Host != "" && s.From != "" }

// Recipients splits a comma/semicolon/space separated list.
func Recipients(list string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(list, func(r rune) bool { return r == ',' || r == ';' || r == ' ' || r == '\n' }) {
		if strings.Contains(f, "@") {
			out = append(out, strings.TrimSpace(f))
		}
	}
	return out
}

// BuildMessage renders an RFC 5322 UTF-8 text message.
func BuildMessage(from string, to []string, subject, body string, now time.Time) []byte {
	var b strings.Builder
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + strings.Join(to, ", ") + "\r\n")
	b.WriteString("Subject: " + mime.QEncoding.Encode("utf-8", subject) + "\r\n")
	b.WriteString("Date: " + now.Format(time.RFC1123Z) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("Content-Transfer-Encoding: base64\r\n\r\n")
	enc := base64.StdEncoding.EncodeToString([]byte(body))
	for len(enc) > 76 {
		b.WriteString(enc[:76] + "\r\n")
		enc = enc[76:]
	}
	b.WriteString(enc + "\r\n")
	return []byte(b.String())
}

// Send delivers one message.
func (s SMTP) Send(ctx context.Context, to []string, subject, body string) error {
	if !s.Configured() {
		return fmt.Errorf("smtp host and from address are required")
	}
	if len(to) == 0 {
		return fmt.Errorf("no email recipients configured")
	}
	port := s.Port
	mode := strings.ToLower(s.TLS)
	if mode == "" {
		mode = "starttls"
	}
	if port == 0 {
		port = 587
		if mode == "tls" {
			port = 465
		}
	}
	addr := net.JoinHostPort(s.Host, strconv.Itoa(port))
	tcfg := &tls.Config{ServerName: s.Host, InsecureSkipVerify: s.InsecureSkipVerify, MinVersion: tls.VersionTLS12}
	d := &net.Dialer{Timeout: 20 * time.Second}
	var conn net.Conn
	var err error
	if mode == "tls" {
		conn, err = tls.DialWithDialer(d, "tcp", addr, tcfg)
	} else {
		conn, err = d.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return err
	}
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	} else {
		conn.SetDeadline(time.Now().Add(60 * time.Second))
	}
	c, err := smtp.NewClient(conn, s.Host)
	if err != nil {
		conn.Close()
		return err
	}
	defer c.Close()
	if err := c.Hello("boxmes"); err != nil {
		return err
	}
	if mode == "starttls" {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return fmt.Errorf("smtp server does not offer STARTTLS (set tls to \"tls\" or \"none\")")
		}
		if err := c.StartTLS(tcfg); err != nil {
			return err
		}
	}
	if s.Username != "" {
		if ok, _ := c.Extension("AUTH"); ok {
			if err := c.Auth(plainAuth{s.Username, s.Password, s.Host, mode != "none"}); err != nil {
				return fmt.Errorf("smtp auth: %w", err)
			}
		}
	}
	if err := c.Mail(addrOnly(s.From)); err != nil {
		return err
	}
	for _, r := range to {
		if err := c.Rcpt(r); err != nil {
			return err
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(BuildMessage(s.From, to, subject, body, time.Now())); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

func addrOnly(from string) string {
	if i := strings.LastIndex(from, "<"); i >= 0 {
		return strings.Trim(from[i:], "<> ")
	}
	return strings.TrimSpace(from)
}

// plainAuth is smtp.PlainAuth without its "unencrypted connection" refusal
// for TLS-wrapped (implicit TLS) connections, which net/smtp cannot detect.
type plainAuth struct {
	user, pass, host string
	encrypted        bool
}

func (a plainAuth) Start(server *smtp.ServerInfo) (string, []byte, error) {
	if !a.encrypted && !server.TLS && server.Name != "localhost" && server.Name != "127.0.0.1" && server.Name != "::1" {
		return "", nil, fmt.Errorf("refusing to send the SMTP password over an unencrypted connection")
	}
	return "PLAIN", []byte("\x00" + a.user + "\x00" + a.pass), nil
}

func (a plainAuth) Next(fromServer []byte, more bool) ([]byte, error) {
	if more {
		return nil, fmt.Errorf("unexpected SMTP auth challenge")
	}
	return nil, nil
}
