package e2e

// v1.10 box access: tickets (required / single-use / expired / revoked),
// Origin allowlist, TCP stream (echo), durable terminal (detach, reattach,
// replay, viewers, kill), SSH gateway over WebSocket (exec, exit code, stdin
// EOF, pty, local port forwarding) and the direct SSH listener (ticket as
// username, registered public key).

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/crypto/ssh"

	"boxmes/core/internal/manager"
	"boxmes/core/internal/model"
	"boxmes/core/internal/wsconn"
)

func wsBase(e *env) string { return "ws" + strings.TrimPrefix(e.srv.URL, "http") }

func ticket(t *testing.T, e *env, c interface {
	JSON(context.Context, string, string, any, any) error
}, box string, in map[string]any) model.AccessTicket {
	t.Helper()
	var tk model.AccessTicket
	if err := c.JSON(e.ctx, "POST", "/boxes/"+box+"/tickets", in, &tk); err != nil {
		t.Fatalf("ticket %v: %v", in, err)
	}
	if !strings.HasPrefix(tk.Ticket, manager.AccessTicketPrefix) || tk.Hash != "" {
		t.Fatalf("ticket %+v", tk)
	}
	return tk
}

func dialStatus(u string, hdr http.Header) (*websocket.Conn, int, string) {
	ws, resp, err := websocket.DefaultDialer.Dial(u, hdr)
	if err == nil {
		return ws, 101, ""
	}
	if resp == nil {
		return nil, 0, err.Error()
	}
	b, _ := io.ReadAll(resp.Body)
	return nil, resp.StatusCode, string(b)
}

// echo server on a free local port (local driver boxes share the host network)
func echoServer(t *testing.T) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				io.Copy(c, c)
				c.Close()
			}()
		}
	}()
	return l.Addr().(*net.TCPAddr).Port
}

type termClient struct {
	t   *testing.T
	ws  *websocket.Conn
	out bytes.Buffer
	ctl []map[string]any
}

func (c *termClient) readUntil(sub string, d time.Duration) {
	c.t.Helper()
	c.ws.SetReadDeadline(time.Now().Add(d))
	for !strings.Contains(c.out.String(), sub) {
		mt, msg, err := c.ws.ReadMessage()
		if err != nil {
			c.t.Fatalf("terminal read waiting for %q: %v (got %q)", sub, err, c.out.String())
		}
		if mt == websocket.BinaryMessage {
			c.out.Write(msg)
		} else {
			var m map[string]any
			json.Unmarshal(msg, &m)
			c.ctl = append(c.ctl, m)
		}
	}
}

func (c *termClient) ctlOf(typ string) map[string]any {
	for _, m := range c.ctl {
		if m["type"] == typ {
			return m
		}
	}
	return nil
}

func TestAccessP0(t *testing.T) {
	e := setupWith(t, func(c *manager.Config) {
		c.TerminalDetachTTL = 3 * time.Second
		c.DashboardURL = "https://dash.example"
		c.SSHAddr = "127.0.0.1:0"
	}, 1)
	if err := e.m.StartSSH(e.ctx); err != nil {
		t.Fatal(err)
	}
	ag, _ := e.agentClient("acc-agent", 0)
	other, _ := e.agentClient("other-agent", 0)
	var b model.Box
	e.must(ag, "POST", "/boxes", map[string]any{"template": "base", "slug": "acc", "lifecycle": "session"}, &b)
	waitStatus(t, e.m, b.ID, model.StatusRunning, 20*time.Second)

	// ---- tickets ----
	if err := other.JSON(e.ctx, "POST", "/boxes/"+b.ID+"/tickets", map[string]any{"purpose": "terminal"}, nil); err == nil {
		t.Fatal("another agent got a ticket for this box")
	}
	for _, bad := range []map[string]any{{"purpose": "nope"}, {"purpose": "tcp"}, {"purpose": "terminal", "ttl_s": 5}, {"purpose": "terminal", "ttl_s": 100000}} {
		if err := ag.JSON(e.ctx, "POST", "/boxes/"+b.ID+"/tickets", bad, nil); err == nil {
			t.Fatalf("bad ticket input accepted: %v", bad)
		}
	}
	termURL := wsBase(e) + "/v1/boxes/" + b.ID + "/terminal?proto=2&cols=100&rows=30"
	if _, st, _ := dialStatus(termURL, nil); st != 401 {
		t.Fatalf("no auth → %d", st)
	}
	if _, st, body := dialStatus(termURL+"&token=bx_whatever", nil); st != 401 || !strings.Contains(body, "not accepted in URLs") {
		t.Fatalf("API key in URL → %d %s", st, body)
	}
	tk := ticket(t, e, ag, b.ID, map[string]any{"purpose": "terminal"})
	if tk.ExpiresAt.Sub(tk.CreatedAt) != 60*time.Second || !strings.Contains(tk.CoreURL, "ticket=") {
		t.Fatalf("ticket defaults %+v", tk)
	}
	// Origin allowlist (before the ticket is consumed)
	if _, st, _ := dialStatus(termURL+"&ticket="+url.QueryEscape(tk.Ticket), http.Header{"Origin": {"https://evil.example"}}); st != 403 {
		t.Fatalf("evil origin → %d", st)
	}
	// wrong purpose
	if _, st, _ := dialStatus(wsBase(e)+"/v1/boxes/"+b.ID+"/ssh?ticket="+url.QueryEscape(tk.Ticket), nil); st != 401 {
		t.Fatalf("terminal ticket on ssh → %d", st)
	}
	// that consumed nothing? purpose mismatch is refused before redemption
	ws, st, body := dialStatus(termURL+"&ticket="+url.QueryEscape(tk.Ticket), http.Header{"Origin": {"https://dash.example"}})
	if st != 101 {
		t.Fatalf("ticket + dashboard origin → %d %s", st, body)
	}
	tc := &termClient{t: t, ws: ws}
	tc.readUntil("$", 10*time.Second)
	sess := tc.ctlOf("session")
	if sess == nil || sess["created"] != true || sess["id"] == "" {
		t.Fatalf("session frame %v", tc.ctl)
	}
	sid := sess["id"].(string)
	// reused ticket → 401
	if _, st, _ := dialStatus(termURL+"&ticket="+url.QueryEscape(tk.Ticket), nil); st != 401 {
		t.Fatalf("reused ticket → %d", st)
	}
	// expired ticket → 401
	tk2 := ticket(t, e, ag, b.ID, map[string]any{"purpose": "terminal", "ttl_s": 10})
	list := []*model.AccessTicket{}
	e.m.Store.GetKV(e.ctx, "tickets:"+b.ID, &list)
	for _, x := range list {
		if x.ID == tk2.ID {
			x.ExpiresAt = time.Now().Add(-time.Second)
		}
	}
	e.m.Store.PutKV(e.ctx, "tickets:"+b.ID, list)
	if _, st, _ := dialStatus(termURL+"&ticket="+url.QueryEscape(tk2.Ticket), nil); st != 401 {
		t.Fatalf("expired ticket → %d", st)
	}
	// ticket of another box id in the path → 401
	tk3 := ticket(t, e, ag, b.ID, map[string]any{"purpose": "terminal"})
	if _, st, _ := dialStatus(wsBase(e)+"/v1/boxes/box_other/terminal?ticket="+url.QueryEscape(tk3.Ticket), nil); st != 401 {
		t.Fatalf("ticket for another box → %d", st)
	}

	// ---- durable terminal ----
	ws.WriteMessage(websocket.TextMessage, []byte(`{"type":"resize","cols":90,"rows":25}`))
	ws.WriteMessage(websocket.BinaryMessage, []byte("export MARK=replay-$((6*7)); echo $MARK\n"))
	tc.readUntil("replay-42", 10*time.Second)
	ws.Close() // drop: the PTY stays
	time.Sleep(300 * time.Millisecond)
	var terms struct {
		Items []model.TerminalSession `json:"items"`
	}
	e.must(ag, "GET", "/boxes/"+b.ID+"/terminals", nil, &terms)
	if len(terms.Items) != 1 || terms.Items[0].ID != sid || terms.Items[0].Viewers != 0 || terms.Items[0].ExpiresAt == nil || terms.Items[0].Cols != 90 {
		t.Fatalf("terminals after drop %+v", terms.Items)
	}
	// reattach with the API key header: replay contains the old output, env survived
	ws, st, body = dialStatus(termURL+"&session="+sid, http.Header{"Authorization": {"Bearer " + ag.Key}})
	if st != 101 {
		t.Fatalf("reattach %d %s", st, body)
	}
	tc = &termClient{t: t, ws: ws}
	tc.readUntil("replay-42", 5*time.Second)
	if s := tc.ctlOf("session"); s == nil || s["created"] != false || s["id"] != sid || s["replay"].(float64) <= 0 {
		t.Fatalf("reattach session frame %v", tc.ctl)
	}
	ws.WriteMessage(websocket.BinaryMessage, []byte("echo again-$MARK\n"))
	tc.readUntil("again-replay-42", 5*time.Second)
	// second viewer in view mode: sees output, cannot type
	tkv := ticket(t, e, ag, b.ID, map[string]any{"purpose": "terminal", "session_id": sid})
	vws, st, _ := dialStatus(wsBase(e)+"/v1/boxes/"+b.ID+"/terminal?proto=2&mode=view&ticket="+url.QueryEscape(tkv.Ticket), nil)
	if st != 101 {
		t.Fatalf("viewer %d", st)
	}
	vc := &termClient{t: t, ws: vws}
	vc.readUntil("again-replay-42", 5*time.Second)
	vws.WriteMessage(websocket.BinaryMessage, []byte("echo viewer-typed\n"))
	ws.WriteMessage(websocket.BinaryMessage, []byte("echo ctl-$((1+1))\n"))
	vc.readUntil("ctl-2", 5*time.Second)
	if strings.Contains(vc.out.String(), "viewer-typed\r\n") && strings.Contains(tc.out.String(), "viewer-typed") {
		t.Fatal("view-mode input reached the PTY")
	}
	vws.Close()
	ws.Close()
	// detach TTL (3 s) kills it
	time.Sleep(3800 * time.Millisecond)
	e.must(ag, "GET", "/boxes/"+b.ID+"/terminals", nil, &terms)
	if len(terms.Items) != 0 {
		t.Fatalf("session not reaped after detach ttl: %+v", terms.Items)
	}
	if _, st, _ := dialStatus(termURL+"&session="+sid, http.Header{"Authorization": {"Bearer " + ag.Key}}); st != 404 {
		t.Fatalf("attach to reaped session → %d", st)
	}
	// kill API + revoke closes the live socket
	tk4 := ticket(t, e, ag, b.ID, map[string]any{"purpose": "terminal"})
	ws, _, _ = dialStatus(termURL+"&ticket="+url.QueryEscape(tk4.Ticket), nil)
	tc = &termClient{t: t, ws: ws}
	tc.readUntil("$", 5*time.Second)
	var rv struct {
		Revoked int `json:"revoked"`
	}
	e.must(ag, "DELETE", "/boxes/"+b.ID+"/tickets/"+tk4.ID, nil, &rv)
	ws.SetReadDeadline(time.Now().Add(3 * time.Second))
	closed := false
	for {
		if _, _, err := ws.ReadMessage(); err != nil {
			closed = true
			break
		}
	}
	if rv.Revoked != 1 || !closed {
		t.Fatalf("revoke: %+v closed=%v", rv, closed)
	}
	var killed struct {
		Killed int `json:"killed"`
	}
	e.must(ag, "DELETE", "/boxes/"+b.ID+"/terminals/all", nil, &killed)

	// ---- TCP stream ----
	echo := echoServer(t)
	if err := ag.JSON(e.ctx, "POST", "/boxes/"+b.ID+"/tickets", map[string]any{"purpose": "tcp", "port": echo}, nil); err == nil {
		t.Fatal("tcp ticket for a non-allowlisted port")
	}
	tt := ticket(t, e, ag, b.ID, map[string]any{"purpose": "tcp", "port": echo, "allow": true})
	var bx model.Box
	e.must(ag, "GET", "/boxes/"+b.ID, nil, &bx)
	if len(bx.ForwardPorts) != 1 || bx.ForwardPorts[0] != echo {
		t.Fatalf("forward ports %v", bx.ForwardPorts)
	}
	tws, st, body := dialStatus(fmt.Sprintf("%s/v1/boxes/%s/tcp?port=%d&ticket=%s", wsBase(e), b.ID, echo, url.QueryEscape(tt.Ticket)), nil)
	if st != 101 {
		t.Fatalf("tcp dial %d %s", st, body)
	}
	tconn := wsconn.New(tws)
	fmt.Fprintf(tconn, "ping-over-ws\n")
	line, err := bufio.NewReader(tconn).ReadString('\n')
	if err != nil || line != "ping-over-ws\n" {
		t.Fatalf("echo: %q %v", line, err)
	}
	tconn.Close()
	// API key header path + port mismatch on a ticket
	tws, st, _ = dialStatus(fmt.Sprintf("%s/v1/boxes/%s/tcp?port=%d", wsBase(e), b.ID, echo), http.Header{"Authorization": {"Bearer " + ag.Key}})
	if st != 101 {
		t.Fatalf("tcp with key %d", st)
	}
	tws.Close()
	tt2 := ticket(t, e, ag, b.ID, map[string]any{"purpose": "tcp", "port": echo})
	if _, st, _ := dialStatus(fmt.Sprintf("%s/v1/boxes/%s/tcp?port=%d&ticket=%s", wsBase(e), b.ID, echo+1, url.QueryEscape(tt2.Ticket)), nil); st != 401 {
		t.Fatalf("port mismatch → %d", st)
	}
	if _, st, _ := dialStatus(fmt.Sprintf("%s/v1/boxes/%s/tcp?port=1", wsBase(e), b.ID), http.Header{"Authorization": {"Bearer " + ag.Key}}); st != 403 {
		t.Fatalf("non-allowlisted port with key → %d", st)
	}

	// ---- SSH over WebSocket ----
	var info struct {
		KnownHosts string `json:"known_hosts"`
		Direct     string `json:"direct"`
	}
	e.must(ag, "GET", "/ssh", nil, &info)
	hostKey, _, _, _, err := ssh.ParseAuthorizedKey([]byte(strings.TrimPrefix(info.KnownHosts, manager.SSHHostKeyAlias+" ")))
	if err != nil {
		t.Fatalf("known_hosts %q: %v", info.KnownHosts, err)
	}
	sshOverWS := func() *ssh.Client {
		tk := ticket(t, e, ag, b.ID, map[string]any{"purpose": "ssh"})
		sws, st, body := dialStatus(wsBase(e)+"/v1/boxes/"+b.ID+"/ssh?ticket="+url.QueryEscape(tk.Ticket), nil)
		if st != 101 {
			t.Fatalf("ssh ws %d %s", st, body)
		}
		sc, chans, reqs, err := ssh.NewClientConn(wsconn.New(sws), "acc", &ssh.ClientConfig{User: "boxmes", HostKeyCallback: ssh.FixedHostKey(hostKey), Timeout: 20 * time.Second})
		if err != nil {
			t.Fatalf("ssh handshake: %v", err)
		}
		return ssh.NewClient(sc, chans, reqs)
	}
	cl := sshOverWS()
	run := func(cl *ssh.Client, cmd, stdin string) (string, string, int) {
		t.Helper()
		s, err := cl.NewSession()
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		var o, eb bytes.Buffer
		s.Stdout, s.Stderr = &o, &eb
		if stdin != "" {
			s.Stdin = strings.NewReader(stdin)
		}
		s.Setenv("BOXMES_TEST", "env-ok")
		code := 0
		if err := s.Run(cmd); err != nil {
			ee, ok := err.(*ssh.ExitError)
			if !ok {
				t.Fatalf("run %q: %v", cmd, err)
			}
			code = ee.ExitStatus()
		}
		return o.String(), eb.String(), code
	}
	if o, _, c := run(cl, "uname -s; echo $BOXMES_TEST", ""); c != 0 || !strings.Contains(o, "Linux") || !strings.Contains(o, "env-ok") {
		t.Fatalf("ssh exec: %q %d", o, c)
	}
	if o, se, c := run(cl, "echo out; echo err >&2; exit 7", ""); c != 7 || o != "out\n" || se != "err\n" {
		t.Fatalf("ssh exit/stderr: %q %q %d", o, se, c)
	}
	if o, _, c := run(cl, "wc -l", "a\nb\nc\n"); c != 0 || strings.TrimSpace(o) != "3" {
		t.Fatalf("ssh stdin EOF: %q %d", o, c)
	}
	// script on stdin (what VS Code Remote-SSH does)
	if o, _, c := run(cl, "bash", "echo from-script-$((2+3))\n"); c != 0 || o != "from-script-5\n" {
		t.Fatalf("ssh bash stdin: %q %d", o, c)
	}
	// pty
	ps, _ := cl.NewSession()
	var po bytes.Buffer
	ps.Stdout = &po
	if err := ps.RequestPty("xterm", 30, 100, ssh.TerminalModes{}); err != nil {
		t.Fatal(err)
	}
	if err := ps.Run("tty; stty size"); err != nil {
		t.Fatalf("pty run: %v", err)
	}
	if !strings.Contains(po.String(), "/dev/") || !strings.Contains(po.String(), "30 100") {
		t.Fatalf("pty output %q", po.String())
	}
	// local port forwarding (ssh -L)
	fc, err := cl.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", echo))
	if err != nil {
		t.Fatalf("direct-tcpip: %v", err)
	}
	fmt.Fprintf(fc, "via-ssh-L\n")
	if l, _ := bufio.NewReader(fc).ReadString('\n'); l != "via-ssh-L\n" {
		t.Fatalf("forwarded echo %q", l)
	}
	fc.Close()
	if _, err := cl.Dial("tcp", "10.0.0.1:80"); err == nil {
		t.Fatal("forward to a non-box host allowed")
	}
	if _, _, err := cl.SendRequest("tcpip-forward", true, ssh.Marshal(struct {
		A string
		P uint32
	}{"0.0.0.0", 9999})); err != nil {
		t.Fatal(err)
	}
	cl.Close()

	// ---- direct SSH listener: ticket as username, registered public key ----
	addr := e.m.SSHListenAddr()
	if addr == "" {
		t.Fatal("ssh listener not started")
	}
	tks := ticket(t, e, ag, b.ID, map[string]any{"purpose": "ssh"})
	if !strings.Contains(tks.Command, tks.Ticket+"@") {
		t.Fatalf("ssh ticket command %q", tks.Command)
	}
	dcl, err := ssh.Dial("tcp", addr, &ssh.ClientConfig{User: tks.Ticket, HostKeyCallback: ssh.FixedHostKey(hostKey), Timeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("direct ssh with ticket: %v", err)
	}
	if o, _, c := run(dcl, "echo direct-ok", ""); c != 0 || o != "direct-ok\n" {
		t.Fatalf("direct exec %q", o)
	}
	dcl.Close()
	if _, err := ssh.Dial("tcp", addr, &ssh.ClientConfig{User: tks.Ticket, HostKeyCallback: ssh.FixedHostKey(hostKey), Timeout: 10 * time.Second}); err == nil {
		t.Fatal("ticket reused on the direct listener")
	}
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	var key model.SSHKey
	e.must(ag, "POST", "/ssh/keys", map[string]any{"name": "laptop", "public_key": string(ssh.MarshalAuthorizedKey(signer.PublicKey()))}, &key)
	kcfg := &ssh.ClientConfig{User: b.Subdomain, Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: ssh.FixedHostKey(hostKey), Timeout: 10 * time.Second}
	kcl, err := ssh.Dial("tcp", addr, kcfg)
	if err != nil {
		t.Fatalf("direct ssh with key as %s: %v", b.Subdomain, err)
	}
	if o, _, _ := run(kcl, "echo key-ok", ""); o != "key-ok\n" {
		t.Fatalf("key exec %q", o)
	}
	kcl.Close()
	// the other agent's key cannot reach this box
	var ok2 model.SSHKey
	_, priv2, _ := ed25519.GenerateKey(rand.Reader)
	signer2, _ := ssh.NewSignerFromKey(priv2)
	e.must(other, "POST", "/ssh/keys", map[string]any{"public_key": string(ssh.MarshalAuthorizedKey(signer2.PublicKey()))}, &ok2)
	if _, err := ssh.Dial("tcp", addr, &ssh.ClientConfig{User: b.Subdomain, Auth: []ssh.AuthMethod{ssh.PublicKeys(signer2)}, HostKeyCallback: ssh.FixedHostKey(hostKey), Timeout: 10 * time.Second}); err == nil {
		t.Fatal("another agent's key reached the box")
	}

	// ---- access summary + reserved names ----
	var acc manager.AccessInfo
	e.must(ag, "GET", "/boxes/"+b.ID+"/access", nil, &acc)
	if acc.Link != "boxd" || !strings.Contains(acc.Commands["forward"], "box forward") || !strings.Contains(acc.SSH.Config, "ProxyCommand box ssh-proxy %h") || acc.SSH.KnownHosts == "" {
		t.Fatalf("access %+v", acc)
	}
	for _, r := range []string{"ssh", "tcp", "term", "cdp", "vnc", "ide"} {
		if !manager.IsReservedSubdomain(r) {
			t.Fatalf("%s not reserved", r)
		}
	}
	// MCP tools
	if out := mcpCall(t, e, "box_ticket", map[string]any{"box_id": b.ID, "purpose": "ssh"}); !strings.Contains(out, "bxa_") {
		t.Fatalf("box_ticket %s", out)
	}
	if out := mcpCall(t, e, "box_forward", map[string]any{"box_id": b.ID, "port": 6380}); !strings.Contains(out, "box forward") || !strings.Contains(out, "6380") {
		t.Fatalf("box_forward %s", out)
	}
	if out := mcpCall(t, e, "box_ssh_info", map[string]any{"box_id": b.ID}); !strings.Contains(out, "ssh-proxy") || !strings.Contains(out, manager.SSHHostKeyAlias) {
		t.Fatalf("box_ssh_info %s", out)
	}
	if out := mcpCall(t, e, "box_terminal_sessions", map[string]any{"box_id": b.ID}); !strings.Contains(out, "items") {
		t.Fatalf("box_terminal_sessions %s", out)
	}
	// sleep closes sessions; terminal open wakes the box
	e.must(ag, "POST", "/boxes/"+b.ID+"/sleep", map[string]any{}, &bx)
	tk5 := ticket(t, e, ag, b.ID, map[string]any{"purpose": "terminal"})
	ws, st, body = dialStatus(termURL+"&ticket="+url.QueryEscape(tk5.Ticket), nil)
	if st != 101 {
		t.Fatalf("terminal on a sleeping box %d %s", st, body)
	}
	tc = &termClient{t: t, ws: ws}
	tc.readUntil("$", 10*time.Second)
	e.must(ag, "GET", "/boxes/"+b.ID, nil, &bx)
	if bx.Status != model.StatusRunning {
		t.Fatalf("terminal did not wake the box: %s", bx.Status)
	}
	ws.Close()
}
