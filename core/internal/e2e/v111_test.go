package e2e

// v1.11 box access P1: SFTP served by core (box without sftp-server forced
// with SFTPMode=core), short-lived SSH user certificates (WebSocket without a
// ticket + direct listener, revocation), the CDP endpoint (fake DevTools
// server: /json/version rewrite, Browser.getVersion over the proxied socket,
// metadata navigation rewritten, reusable ticket, revoke), the desktop (fake
// RFB server: handshake, view-only input dropped, shared flag forced), share
// links (page 200 → 410 after revoke, viewers disconnected) and the noVNC
// static files.

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"boxmes/core/internal/manager"
	"boxmes/core/internal/model"
	"boxmes/core/internal/wsconn"
)

func freePort(t *testing.T) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return p
}

// fakeCDP serves /json/version + a browser WebSocket answering
// Browser.getVersion and echoing Page.navigate's url.
func fakeCDP(t *testing.T, port int) {
	mux := http.NewServeMux()
	up := websocket.Upgrader{}
	mux.HandleFunc("/json/version", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Host, "127.0.0.1") {
			http.Error(w, "Host header is specified and is not an IP address or localhost.", 500)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"Browser": "FakeChrome/1.0", "Protocol-Version": "1.3", "User-Agent": "Mozilla/5.0 HeadlessChrome/1.0",
			"webSocketDebuggerUrl": fmt.Sprintf("ws://127.0.0.1:%d/devtools/browser/abc-123", port)})
	})
	mux.HandleFunc("/json/list", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]any{{"id": "P1", "type": "page", "url": "about:blank", "devtoolsFrontendUrl": "/devtools/inspector.html?ws=127.0.0.1/devtools/page/P1",
			"webSocketDebuggerUrl": fmt.Sprintf("ws://127.0.0.1:%d/devtools/page/P1", port)}})
	})
	mux.HandleFunc("/devtools/", func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		for {
			_, msg, err := c.ReadMessage()
			if err != nil {
				return
			}
			var m struct {
				ID     int            `json:"id"`
				Method string         `json:"method"`
				Params map[string]any `json:"params"`
			}
			json.Unmarshal(msg, &m)
			res := map[string]any{}
			switch m.Method {
			case "Browser.getVersion":
				res = map[string]any{"product": "FakeChrome/1.0", "protocolVersion": "1.3", "path": r.URL.Path}
			case "Page.navigate":
				res = map[string]any{"frameId": "F", "url": m.Params["url"]}
			}
			c.WriteJSON(map[string]any{"id": m.ID, "result": res})
		}
	})
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: mux}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
}

// fakeRFB is a minimal RFB 3.8 server that records what clients send after
// the ClientInit.
type fakeRFB struct {
	mu     sync.Mutex
	got    [][]byte
	shared []byte
}

func (f *fakeRFB) serve(t *testing.T, port int) {
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
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
				defer c.Close()
				c.Write([]byte("RFB 003.008\n"))
				v := make([]byte, 12)
				if _, err := io.ReadFull(c, v); err != nil {
					return
				}
				c.Write([]byte{1, 1}) // one security type: None
				sec := make([]byte, 1)
				if _, err := io.ReadFull(c, sec); err != nil {
					return
				}
				c.Write([]byte{0, 0, 0, 0}) // SecurityResult OK
				ci := make([]byte, 1)
				if _, err := io.ReadFull(c, ci); err != nil {
					return
				}
				f.mu.Lock()
				f.shared = append(f.shared, ci[0])
				f.mu.Unlock()
				// ServerInit: 64x48, 32bpp pixel format, name "fake"
				si := []byte{0, 64, 0, 48, 32, 24, 0, 1, 0, 255, 0, 255, 0, 255, 16, 8, 0, 0, 0, 0}
				si = binary.BigEndian.AppendUint32(si, 4)
				si = append(si, "fake"...)
				c.Write(si)
				buf := make([]byte, 4096)
				for {
					n, err := c.Read(buf)
					if n > 0 {
						f.mu.Lock()
						f.got = append(f.got, append([]byte(nil), buf[:n]...))
						f.mu.Unlock()
						if buf[0] == 3 { // FramebufferUpdateRequest → one 1x1 raw rect
							c.Write([]byte{0, 0, 0, 1, 0, 0, 0, 0, 0, 1, 0, 1, 0, 0, 0, 0, 1, 2, 3, 4})
						}
					}
					if err != nil {
						return
					}
				}
			}()
		}
	}()
}

func (f *fakeRFB) received() []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return bytes.Join(f.got, nil)
}

// rfbClient does the client handshake over a WebSocket and returns the conn.
func rfbClient(t *testing.T, u string, hdr http.Header) (*wsconn.Conn, string) {
	t.Helper()
	ws, st, body := dialStatus(u, hdr)
	if st != 101 {
		t.Fatalf("vnc socket %s → %d %s", u, st, body)
	}
	c := wsconn.New(ws)
	c.SetReadDeadline(time.Now().Add(10 * time.Second))
	ver := make([]byte, 12)
	if _, err := io.ReadFull(c, ver); err != nil {
		t.Fatalf("rfb version: %v", err)
	}
	c.Write([]byte("RFB 003.008\n"))
	types := make([]byte, 2)
	io.ReadFull(c, types)
	c.Write([]byte{1})
	res := make([]byte, 4)
	io.ReadFull(c, res)
	c.Write([]byte{0}) // ClientInit shared=0 (core forces 1)
	si := make([]byte, 24)
	if _, err := io.ReadFull(c, si); err != nil {
		t.Fatalf("server init: %v", err)
	}
	name := make([]byte, binary.BigEndian.Uint32(si[20:24]))
	io.ReadFull(c, name)
	return c, string(ver)
}

func TestAccessP1(t *testing.T) {
	cdpPort, vncPort := freePort(t), freePort(t)
	e := setupWith(t, func(c *manager.Config) {
		c.DashboardURL = "https://dash.example"
		c.SSHAddr = "127.0.0.1:0"
		c.SFTPMode = "core"
		c.BrowserPort, c.VNCPort = cdpPort, vncPort
	}, 1)
	if err := e.m.StartSSH(e.ctx); err != nil {
		t.Fatal(err)
	}
	ag, _ := e.agentClient("p1-agent", 0)
	var b model.Box
	e.must(ag, "POST", "/boxes", map[string]any{"template": "base", "slug": "p1", "lifecycle": "session"}, &b)
	waitStatus(t, e.m, b.ID, model.StatusRunning, 20*time.Second)
	var info struct {
		KnownHosts string `json:"known_hosts"`
		UserCA     string `json:"user_ca"`
	}
	e.must(ag, "GET", "/ssh", nil, &info)
	hostKey, _, _, _, _ := ssh.ParseAuthorizedKey([]byte(strings.TrimPrefix(info.KnownHosts, manager.SSHHostKeyAlias+" ")))
	if !strings.HasPrefix(info.UserCA, "ssh-ed25519 ") {
		t.Fatalf("user_ca %q", info.UserCA)
	}

	// ---- SFTP served by core ----
	tk := ticket(t, e, ag, b.ID, map[string]any{"purpose": "ssh"})
	sws, st, body := dialStatus(wsBase(e)+"/v1/boxes/"+b.ID+"/ssh?ticket="+url.QueryEscape(tk.Ticket), nil)
	if st != 101 {
		t.Fatalf("ssh ws %d %s", st, body)
	}
	sc, chans, reqs, err := ssh.NewClientConn(wsconn.New(sws), "p1", &ssh.ClientConfig{User: "boxmes", HostKeyCallback: ssh.FixedHostKey(hostKey), Timeout: 20 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	cl := ssh.NewClient(sc, chans, reqs)
	sf, err := sftp.NewClient(cl)
	if err != nil {
		t.Fatalf("sftp subsystem: %v", err)
	}
	dir := t.TempDir()
	payload := bytes.Repeat([]byte("boxmes-sftp-0123456789\n"), 20000) // ~440 KB
	f, err := sf.Create(dir + "/up.bin")
	if err != nil {
		t.Fatalf("sftp create: %v", err)
	}
	if _, err := f.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("sftp close (upload): %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "up.bin")); !bytes.Equal(got, payload) {
		t.Fatalf("uploaded %d bytes, box has %d", len(payload), len(got))
	}
	rf, err := sf.Open(dir + "/up.bin")
	if err != nil {
		t.Fatal(err)
	}
	back, _ := io.ReadAll(rf)
	rf.Close()
	if !bytes.Equal(back, payload) {
		t.Fatalf("download %d bytes", len(back))
	}
	if st, err := sf.Stat(dir + "/up.bin"); err != nil || st.Size() != int64(len(payload)) || st.IsDir() {
		t.Fatalf("stat %v %v", st, err)
	}
	if err := sf.Mkdir(dir + "/sub"); err != nil {
		t.Fatalf("mkdir %v", err)
	}
	if err := sf.Rename(dir+"/up.bin", dir+"/sub/moved.bin"); err != nil {
		t.Fatalf("rename %v", err)
	}
	if err := sf.Chmod(dir+"/sub/moved.bin", 0o600); err != nil {
		t.Fatalf("chmod %v", err)
	}
	items, err := sf.ReadDir(dir + "/sub")
	if err != nil || len(items) != 1 || items[0].Name() != "moved.bin" || items[0].Mode().Perm() != 0o600 {
		t.Fatalf("readdir %v %v", items, err)
	}
	if err := sf.Symlink(dir+"/sub/moved.bin", dir+"/link"); err != nil {
		t.Fatalf("symlink %v", err)
	}
	if l, err := sf.ReadLink(dir + "/link"); err != nil || l != dir+"/sub/moved.bin" {
		t.Fatalf("readlink %q %v", l, err)
	}
	if _, err := sf.Stat(dir + "/nope"); err == nil || !os.IsNotExist(err) {
		t.Fatalf("stat missing: %v", err)
	}
	if err := sf.Remove(dir + "/sub/moved.bin"); err != nil {
		t.Fatal(err)
	}
	if err := sf.RemoveDirectory(dir + "/sub"); err != nil {
		t.Fatal(err)
	}
	sf.Close()
	cl.Close()

	// ---- SSH user certificates ----
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	pub := string(ssh.MarshalAuthorizedKey(signer.PublicKey()))
	if err := ag.JSON(e.ctx, "POST", "/ssh/certs", map[string]any{"box": b.ID, "public_key": pub, "ttl_s": 50000}, nil); err == nil {
		t.Fatal("ttl over 12 h accepted")
	}
	var cert manager.SSHCert
	e.must(ag, "POST", "/ssh/certs", map[string]any{"box": b.Subdomain, "public_key": pub, "ttl_s": 600}, &cert)
	if cert.BoxID != b.ID || !strings.HasPrefix(cert.Certificate, "ssh-ed25519-cert-v01@openssh.com ") || cert.ValidBefore.Sub(time.Now()) > 601*time.Second {
		t.Fatalf("cert %+v", cert)
	}
	pk, _, _, _, err := ssh.ParseAuthorizedKey([]byte(cert.Certificate))
	if err != nil {
		t.Fatal(err)
	}
	certSigner, err := ssh.NewCertSigner(pk.(*ssh.Certificate), signer)
	if err != nil {
		t.Fatal(err)
	}
	anonSSH := func(s ssh.Signer) (*ssh.Client, error) {
		ws, st, body := dialStatus(wsBase(e)+"/v1/boxes/"+b.ID+"/ssh", nil) // no ticket, no key
		if st != 101 {
			t.Fatalf("anon ssh ws %d %s", st, body)
		}
		sc, chans, reqs, err := ssh.NewClientConn(wsconn.New(ws), "p1", &ssh.ClientConfig{User: b.Subdomain, Auth: []ssh.AuthMethod{ssh.PublicKeys(s)}, HostKeyCallback: ssh.FixedHostKey(hostKey), Timeout: 10 * time.Second})
		if err != nil {
			return nil, err
		}
		return ssh.NewClient(sc, chans, reqs), nil
	}
	if _, err := anonSSH(signer); err == nil {
		t.Fatal("plain unregistered key accepted on the anonymous socket")
	}
	ccl, err := anonSSH(certSigner)
	if err != nil {
		t.Fatalf("cert auth over WebSocket: %v", err)
	}
	s1, _ := ccl.NewSession()
	if o, err := s1.Output("echo cert-ok"); err != nil || string(o) != "cert-ok\n" {
		t.Fatalf("cert exec %q %v", o, err)
	}
	// direct listener: user = box name (a principal)
	dcl, err := ssh.Dial("tcp", e.m.SSHListenAddr(), &ssh.ClientConfig{User: b.Subdomain, Auth: []ssh.AuthMethod{ssh.PublicKeys(certSigner)}, HostKeyCallback: ssh.FixedHostKey(hostKey), Timeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("cert on the direct listener: %v", err)
	}
	dcl.Close()
	if _, err := ssh.Dial("tcp", e.m.SSHListenAddr(), &ssh.ClientConfig{User: "someone-else", Auth: []ssh.AuthMethod{ssh.PublicKeys(certSigner)}, HostKeyCallback: ssh.FixedHostKey(hostKey), Timeout: 10 * time.Second}); err == nil {
		t.Fatal("cert accepted for a principal it does not name")
	}
	// revoke: the open session closes, new logins fail
	var rv map[string]any
	e.must(ag, "DELETE", "/boxes/"+b.ID+"/tickets/"+cert.ID, nil, &rv)
	waitDone := make(chan struct{})
	go func() { ccl.Wait(); close(waitDone) }()
	select {
	case <-waitDone:
	case <-time.After(5 * time.Second):
		t.Fatal("revoking the certificate did not close its session")
	}
	if _, err := anonSSH(certSigner); err == nil {
		t.Fatal("revoked certificate accepted")
	}

	// ---- CDP ----
	fakeCDP(t, cdpPort)
	var bs manager.BrowserSession
	e.must(ag, "POST", "/boxes/"+b.ID+"/browser", map[string]any{"ttl_s": 600}, &bs)
	if !bs.Running || bs.Version["Browser"] != "FakeChrome/1.0" || !strings.Contains(bs.CDPURL, "ticket=bxa_") || !strings.Contains(bs.CoreURL, "/v1/boxes/"+b.ID+"/cdp?ticket=") {
		t.Fatalf("browser session %+v", bs)
	}
	tkt := strings.SplitN(bs.CoreURL, "ticket=", 2)[1]
	httpBase := e.srv.URL + "/v1/boxes/" + b.ID + "/cdp"
	resp, err := http.Get(httpBase + "/json/version/?ticket=" + tkt) // what connectOverCDP(http) fetches
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("json/version %v %v", resp, err)
	}
	var ver map[string]any
	json.NewDecoder(resp.Body).Decode(&ver)
	resp.Body.Close()
	wsu, _ := ver["webSocketDebuggerUrl"].(string)
	if !strings.HasPrefix(wsu, wsBase(e)+"/v1/boxes/"+b.ID+"/cdp/devtools/browser/abc-123?ticket=") {
		t.Fatalf("rewritten webSocketDebuggerUrl %q", wsu)
	}
	if resp, _ := http.Get(httpBase + "/json/version"); resp.StatusCode != 401 {
		t.Fatalf("json/version without ticket → %d", resp.StatusCode)
	}
	resp, _ = http.Get(httpBase + "/json/list?ticket=" + tkt)
	var list []map[string]any
	json.NewDecoder(resp.Body).Decode(&list)
	if len(list) != 1 || !strings.Contains(list[0]["webSocketDebuggerUrl"].(string), "/cdp/devtools/page/P1?ticket=") || list[0]["devtoolsFrontendUrl"] != nil {
		t.Fatalf("json/list %v", list)
	}
	cdpCall := func(u string, msgs ...string) []map[string]any {
		t.Helper()
		ws, st, body := dialStatus(u, nil)
		if st != 101 {
			t.Fatalf("cdp ws %s → %d %s", u, st, body)
		}
		defer ws.Close()
		var out []map[string]any
		for _, m := range msgs {
			ws.WriteMessage(websocket.TextMessage, []byte(m))
			ws.SetReadDeadline(time.Now().Add(5 * time.Second))
			var r map[string]any
			if err := ws.ReadJSON(&r); err != nil {
				t.Fatalf("cdp read: %v", err)
			}
			out = append(out, r)
		}
		return out
	}
	// the endpoint itself (connectOverCDP(wss://…) / puppeteer.connect) and the rewritten URL; the ticket is reusable
	r1 := cdpCall(bs.CoreURL, `{"id":1,"method":"Browser.getVersion"}`, `{"id":2,"method":"Page.navigate","params":{"url":"http://169.254.169.254/latest/meta-data/"}}`, `{"id":3,"method":"Page.navigate","params":{"url":"https://example.com/"}}`)
	if r1[0]["result"].(map[string]any)["product"] != "FakeChrome/1.0" || r1[0]["result"].(map[string]any)["path"] != "/devtools/browser/abc-123" {
		t.Fatalf("Browser.getVersion %v", r1[0])
	}
	if u := r1[1]["result"].(map[string]any)["url"]; u != "http://cloud-metadata.blocked.invalid/" {
		t.Fatalf("metadata navigation not blocked: %v", u)
	}
	if u := r1[2]["result"].(map[string]any)["url"]; u != "https://example.com/" {
		t.Fatalf("normal navigation changed: %v", u)
	}
	r2 := cdpCall(wsu, `{"id":7,"method":"Target.createTarget","params":{"url":"http://2852039166/"}}`)
	if r2[0]["id"].(float64) != 7 {
		t.Fatalf("page socket %v", r2)
	}
	if resp, _ := http.Get(httpBase + "/json/new?http://169.254.169.254/&ticket=" + tkt); resp.StatusCode != 403 {
		t.Fatalf("/json/new metadata → %d", resp.StatusCode)
	}
	var bst manager.BrowserState
	e.must(ag, "GET", "/boxes/"+b.ID+"/browser", nil, &bst)
	if !bst.Running || bst.Source != "existing" {
		t.Fatalf("browser status %+v", bst)
	}
	// revoke the cdp ticket: the endpoint stops working, live clients close
	ws, st, _ := dialStatus(bs.CoreURL, nil)
	if st != 101 {
		t.Fatal("cdp reconnect")
	}
	e.must(ag, "DELETE", "/boxes/"+b.ID+"/tickets/"+bs.TicketID, nil, &rv)
	ws.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, _, err := ws.ReadMessage(); err == nil {
		t.Fatal("cdp socket stayed open after revoke")
	}
	if _, st, _ := dialStatus(bs.CoreURL, nil); st != 401 {
		t.Fatalf("revoked cdp ticket → %d", st)
	}

	// ---- desktop (VNC) ----
	rfb := &fakeRFB{}
	rfb.serve(t, vncPort)
	var ds manager.DesktopSession
	e.must(ag, "POST", "/boxes/"+b.ID+"/desktop", map[string]any{"view_only": true}, &ds)
	if !ds.Running || !strings.Contains(ds.ViewerURL, "/v1/boxes/"+b.ID+"/desktop/view?ticket=bxa_") || !ds.ViewOnly {
		t.Fatalf("desktop %+v", ds)
	}
	page, _ := http.Get(ds.ViewerURL)
	pb, _ := io.ReadAll(page.Body)
	if page.StatusCode != 200 || !bytes.Contains(pb, []byte("novnc/core/rfb.js")) || !strings.Contains(page.Header.Get("Content-Security-Policy"), "frame-ancestors 'self' https://dash.example") {
		t.Fatalf("viewer page %d %s %s", page.StatusCode, page.Header.Get("Content-Security-Policy"), pb)
	}
	if js, _ := http.Get(e.srv.URL + "/v1/novnc/core/rfb.js"); js.StatusCode != 200 || !strings.HasPrefix(js.Header.Get("Content-Type"), "text/javascript") {
		t.Fatalf("novnc static %d", js.StatusCode)
	}
	c, rver := rfbClient(t, ds.CoreURL, nil)
	if rver != "RFB 003.008\n" {
		t.Fatalf("rfb version %q", rver)
	}
	// FramebufferUpdateRequest passes, key + pointer + cut text are dropped (view-only ticket)
	c.Write([]byte{4, 1, 0, 0, 0, 0, 0, 0x61})             // KeyEvent 'a'
	c.Write([]byte{5, 1, 0, 10, 0, 10})                     // PointerEvent
	c.Write([]byte{6, 0, 0, 0, 0, 0, 0, 3, 'x', 'y', 'z'}) // ClientCutText
	c.Write([]byte{3, 0, 0, 0, 0, 0, 0, 64, 0, 48})         // FramebufferUpdateRequest
	upd := make([]byte, 20)
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(c, upd); err != nil || upd[0] != 0 {
		t.Fatalf("framebuffer update %v %v", upd, err)
	}
	time.Sleep(100 * time.Millisecond)
	if got := rfb.received(); !bytes.Equal(got, []byte{3, 0, 0, 0, 0, 0, 0, 64, 0, 48}) {
		t.Fatalf("view-only: server received %v", got)
	}
	if rfb.shared[0] != 1 {
		t.Fatalf("ClientInit shared flag %v", rfb.shared)
	}
	c.Close()
	if _, st, _ := dialStatus(ds.CoreURL, nil); st != 401 {
		t.Fatalf("vnc ticket reused → %d", st)
	}
	// interactive ticket passes input
	var ds2 manager.DesktopSession
	e.must(ag, "POST", "/boxes/"+b.ID+"/desktop", map[string]any{}, &ds2)
	c2, _ := rfbClient(t, ds2.CoreURL, nil)
	before := len(rfb.received())
	c2.Write([]byte{4, 1, 0, 0, 0, 0, 0, 0x62})
	time.Sleep(200 * time.Millisecond)
	if got := rfb.received()[before:]; !bytes.Equal(got, []byte{4, 1, 0, 0, 0, 0, 0, 0x62}) {
		t.Fatalf("interactive: server received %v", got)
	}
	c2.Close()

	// ---- share links ----
	if err := ag.JSON(e.ctx, "POST", "/boxes/"+b.ID+"/desktop/shares", map[string]any{"ttl_s": 30}, nil); err == nil {
		t.Fatal("share ttl 30 s accepted")
	}
	var sh manager.DesktopShare
	e.must(ag, "POST", "/boxes/"+b.ID+"/desktop/shares", map[string]any{"ttl_s": 600}, &sh)
	if !sh.ViewOnly || !strings.Contains(sh.URL, "/desktop/view?share=bxs_") {
		t.Fatalf("share %+v", sh)
	}
	page, _ = http.Get(sh.URL)
	if page.StatusCode != 200 || !strings.Contains(page.Header.Get("Content-Security-Policy"), "frame-ancestors *") || page.Header.Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("share page %d %v", page.StatusCode, page.Header)
	}
	shareTok := strings.SplitN(sh.URL, "share=", 2)[1]
	sockURL := wsBase(e) + "/v1/boxes/" + b.ID + "/vnc?share=" + shareTok
	v1, _ := rfbClient(t, sockURL, nil)
	v2, _ := rfbClient(t, sockURL, nil) // reusable: a second viewer
	before = len(rfb.received())
	v1.Write([]byte{5, 1, 0, 1, 0, 1})
	time.Sleep(150 * time.Millisecond)
	if len(rfb.received()) != before {
		t.Fatal("share link viewer could send input")
	}
	var shares struct {
		Items []manager.DesktopShare `json:"items"`
	}
	e.must(ag, "GET", "/boxes/"+b.ID+"/desktop/shares", nil, &shares)
	if len(shares.Items) != 1 || shares.Items[0].Viewers != 2 || shares.Items[0].URL != "" {
		t.Fatalf("shares %+v", shares.Items)
	}
	t0 := time.Now()
	e.must(ag, "DELETE", "/boxes/"+b.ID+"/desktop/shares/all", nil, &rv)
	if rv["revoked"].(float64) != 1 || rv["viewers_closed"].(float64) != 2 {
		t.Fatalf("revoke %v", rv)
	}
	for _, v := range []*wsconn.Conn{v1, v2} {
		v.SetReadDeadline(time.Now().Add(3 * time.Second))
		if _, err := io.ReadAll(v); err != nil && !strings.Contains(err.Error(), "close") && !strings.Contains(err.Error(), "EOF") {
			t.Fatalf("viewer after revoke: %v", err)
		}
	}
	if time.Since(t0) > 2*time.Second {
		t.Fatalf("viewers closed after %s", time.Since(t0))
	}
	if page, _ := http.Get(sh.URL); page.StatusCode != 410 {
		t.Fatalf("revoked share page → %d", page.StatusCode)
	}
	if _, st, _ := dialStatus(sockURL, nil); st != 410 {
		t.Fatalf("revoked share socket → %d", st)
	}
	if page, _ := http.Get(e.srv.URL + "/v1/boxes/" + b.ID + "/desktop/view?share=bxs_" + b.ID + "_" + strings.Repeat("0", 40)); page.StatusCode != 401 {
		t.Fatalf("unknown share → %d", page.StatusCode)
	}

	// ---- MCP ----
	if out := mcpCall(t, e, "box_ssh_info", map[string]any{"box_id": b.ID, "public_key": pub, "ttl_s": 300}); !strings.Contains(out, "ssh-ed25519-cert-v01@openssh.com") {
		t.Fatalf("box_ssh_info cert %s", out)
	}
	if out := mcpCall(t, e, "box_browser", map[string]any{"box_id": b.ID}); !strings.Contains(out, "cdp_url") || !strings.Contains(out, "FakeChrome") {
		t.Fatalf("box_browser %s", out)
	}
	if out := mcpCall(t, e, "box_desktop", map[string]any{"box_id": b.ID, "action": "share", "ttl_s": 120}); !strings.Contains(out, "share=bxs_") {
		t.Fatalf("box_desktop share %s", out)
	}
	var acc manager.AccessInfo
	e.must(ag, "GET", "/boxes/"+b.ID+"/access", nil, &acc)
	if !strings.Contains(acc.SSH.Features, "sftp") || acc.Commands["browser"] == "" || acc.Commands["desktop"] == "" {
		t.Fatalf("access summary %+v", acc.Commands)
	}
	_ = bufio.NewReader
}

// v1.11 control-origin hardening: only BOXMES_PUBLIC_URL's host is core; box
// hosts serve box traffic + /.boxmes/*; a box cannot set cookies for the whole
// gateway domain; box-host origins never open access sockets.
func TestControlOrigin(t *testing.T) {
	appPort := freePort(t)
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", appPort))
	if err != nil {
		t.Fatal(err)
	}
	app := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "sid", Value: "1", Domain: "example.test", Path: "/"})
		w.Header().Add("Set-Cookie", "x=2; Path=/; domain=.example.test; HttpOnly")
		io.WriteString(w, "app:"+r.URL.Path)
	})}
	go app.Serve(l)
	t.Cleanup(func() { app.Close() })
	e := setupWith(t, func(c *manager.Config) { c.GatewayDomain = "example.test" }, 1)
	e.m.Cfg.PublicURL = "https://gw.example.test"
	ag, _ := e.agentClient("co-agent", 0)
	var b model.Box
	e.must(ag, "POST", "/boxes", map[string]any{"template": "base", "slug": "co-app", "lifecycle": "session", "ports": []int{appPort}}, &b)
	b = *waitStatus(t, e.m, b.ID, model.StatusRunning, 20*time.Second)
	do := func(host, path string, hdr http.Header) *http.Response {
		req, _ := http.NewRequest("GET", e.srv.URL+path, nil)
		req.Host = host
		for k, v := range hdr {
			req.Header[k] = v
		}
		resp, err := http.DefaultTransport.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	if r := do("example.test", "/v1/boxes", nil); r.StatusCode != 404 {
		t.Fatalf("bare gateway domain served core: %d", r.StatusCode)
	}
	if r := do("gw.example.test", "/v1/boxes", nil); r.StatusCode != 401 {
		t.Fatalf("control host /v1/boxes → %d", r.StatusCode)
	}
	host := b.Subdomain + ".example.test"
	r := do(host, "/v1/boxes", nil)
	body, _ := io.ReadAll(r.Body)
	if r.StatusCode != 200 || string(body) != "app:/v1/boxes" {
		t.Fatalf("box host must proxy to the app, never core: %d %s", r.StatusCode, body)
	}
	for _, c := range r.Header.Values("Set-Cookie") {
		if strings.Contains(strings.ToLower(c), "domain=") {
			t.Fatalf("Set-Cookie with Domain reached the client: %q", c)
		}
	}
	if len(r.Header.Values("Set-Cookie")) != 2 {
		t.Fatalf("cookies %v", r.Header.Values("Set-Cookie"))
	}
	if r := do(host, "/.boxmes/nope", nil); r.StatusCode != 404 {
		t.Fatalf("/.boxmes/nope → %d", r.StatusCode)
	}
	tk := ticket(t, e, ag, b.ID, map[string]any{"purpose": "terminal"})
	if r := do(host, "/.boxmes/terminal?ticket="+url.QueryEscape(tk.Ticket), http.Header{"Origin": {"https://other-box.example.test"}, "Connection": {"Upgrade"}, "Upgrade": {"websocket"},
		"Sec-Websocket-Version": {"13"}, "Sec-Websocket-Key": {"dGhlIHNhbXBsZSBub25jZQ=="}}); r.StatusCode != 403 {
		t.Fatalf("box-host origin opened an access socket: %d", r.StatusCode)
	}
}
