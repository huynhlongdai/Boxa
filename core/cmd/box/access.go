package main

// v1.10 CLI: box access — tickets, `box forward` (TCP over WebSocket),
// `box ssh` / `box ssh-proxy` / `box ssh-config` (SSH gateway in core),
// `box terminals` and `box access`.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/crypto/ssh"

	"boxaz/core/internal/client"
	"boxaz/core/internal/manager"
	"boxaz/core/internal/model"
	"boxaz/core/internal/wsconn"
)

const accessUsage = `  box access <box>                             (how to reach the box: forward, ssh, terminal)
  box ticket <box> terminal|tcp|ssh [--port N] [--ttl 60s] [--json]   (single-use access ticket)
  box forward <box> <remote-port> [local-port] [--bind 127.0.0.1]     (local port → port in the box over WSS)
  box ssh <box> [ssh args…] [-- command]        (OpenSSH through box ssh-proxy; no sshd needed in the box)
  box ssh-proxy [--anon] <host> [port]          (ProxyCommand: ssh -o ProxyCommand='box ssh-proxy %h' …; --anon: no API key, certificate auth)
  box ssh-config [--write] [--cert [--ttl 12h]] [box…]   (Host entries for ~/.ssh/config: VS Code / Cursor Remote-SSH; --cert: short-lived certificates)
  box ssh-cert <box> [--key ~/.ssh/id_ed25519.pub] [--ttl 1h] [--json]   (short-lived SSH user certificate, ≤ 12 h)
  box sftp <box> put <local> <remote> | get <remote> <local> | ls [dir] | rm <path> | mkdir <dir>   (SFTP through the gateway; always works)
  box browser <box> [--ttl 1h] [--stop] [--status] [--json]   (CDP endpoint: chromium.connectOverCDP / puppeteer.connect)
  box desktop <box> [--share [--ttl 1h] [--interactive]] [--revoke-shares] [--stop] [--json]   (desktop over VNC; view-only share links)
  box ssh-key add <file.pub> [--name n] | ls | rm <id>
  box terminals <box> [--kill <session_id>|all]
`

func boxRefFromHost(h string) string {
	h = strings.TrimSpace(strings.ToLower(h))
	if i := strings.IndexByte(h, '.'); i > 0 {
		return h[:i]
	}
	return h
}

func createTicket(ctx context.Context, c *client.Client, box string, in manager.CreateTicketInput) (*model.AccessTicket, error) {
	var t model.AccessTicket
	if err := c.JSON(ctx, "POST", "/boxes/"+url.PathEscape(box)+"/tickets", in, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

// ticketURLs lists where to use a ticket: the box host, core, then BOXAZ_URL.
func ticketURLs(c *client.Client, t *model.AccessTicket, kind, query string) []string {
	var out []string
	add := func(u string) {
		if u == "" {
			return
		}
		for _, x := range out {
			if x == u {
				return
			}
		}
		out = append(out, u)
	}
	if os.Getenv("BOXAZ_ACCESS_VIA") != "core" {
		add(t.URL)
	}
	add(t.CoreURL)
	base := c.BaseURL
	switch {
	case strings.HasPrefix(base, "https://"):
		base = "wss://" + base[8:]
	case strings.HasPrefix(base, "http://"):
		base = "ws://" + base[7:]
	}
	u := base + "/v1/boxes/" + t.BoxID + "/" + kind + "?"
	if query != "" {
		u += query + "&"
	}
	add(u + "ticket=" + url.QueryEscape(t.Ticket))
	return out
}

var wsDialer = websocket.Dialer{Proxy: http.ProxyFromEnvironment, HandshakeTimeout: 100 * time.Second, ReadBufferSize: 32 << 10, WriteBufferSize: 32 << 10}

// dialAccess opens an access WebSocket with a fresh ticket, trying the box
// host, then core. A handshake refused by the server (ticket consumed or
// rejected) is final; a transport failure tries the next URL.
func dialAccess(ctx context.Context, c *client.Client, box string, in manager.CreateTicketInput, kind, query string) (*wsconn.Conn, error) {
	t, err := createTicket(ctx, c, box, in)
	if err != nil {
		return nil, err
	}
	var last error
	for _, u := range ticketURLs(c, t, kind, query) {
		h := http.Header{}
		h.Set("User-Agent", "box-cli/"+manager.Version)
		ws, resp, err := wsDialer.DialContext(ctx, u, h)
		if err == nil {
			return wsconn.New(ws), nil
		}
		if resp != nil {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			return nil, fmt.Errorf("%s: HTTP %d %s", hostOf(u), resp.StatusCode, strings.TrimSpace(string(b)))
		}
		last = fmt.Errorf("%s: %v", hostOf(u), err)
	}
	return nil, last
}

func hostOf(u string) string {
	if pu, err := url.Parse(u); err == nil {
		return pu.Host
	}
	return u
}

func cmdTicket(ctx context.Context, c *client.Client, args []string) error {
	fs := flag.NewFlagSet("ticket", flag.ContinueOnError)
	port := fs.Int("port", 0, "tcp: in-box port")
	ttl := fs.Duration("ttl", 60*time.Second, "time to redeem it (10s … 24h)")
	session := fs.String("session", "", "terminal: only this session")
	allow := fs.Bool("allow", false, "tcp: add the port to the forward allowlist")
	asJSON := fs.Bool("json", false, "print the full ticket as JSON")
	if err := fs.Parse(reorder(fs, args)); err != nil {
		return err
	}
	args = fs.Args()
	if len(args) < 2 {
		return fmt.Errorf("usage: box ticket <box> terminal|tcp|ssh [--port N] [--ttl 60s]")
	}
	t, err := createTicket(ctx, c, args[0], manager.CreateTicketInput{Purpose: args[1], Port: *port, TTLSeconds: int(ttl.Seconds()), SessionID: *session, Allow: *allow})
	if err != nil {
		return err
	}
	if *asJSON {
		printJSON(t)
		return nil
	}
	fmt.Println(t.Ticket)
	return nil
}

func cmdAccess(ctx context.Context, c *client.Client, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: box access <box>")
	}
	var a manager.AccessInfo
	if err := c.JSON(ctx, "GET", "/boxes/"+url.PathEscape(args[0])+"/access", nil, &a); err != nil {
		return err
	}
	fmt.Printf("box %s (%s) link=%s\n", a.Name, a.BoxID, a.Link)
	fmt.Printf("forward:   %s   (allowed ports %v)\n", a.Commands["forward"], a.TCP.AllowedPorts)
	fmt.Printf("ssh:       %s\n", a.Commands["ssh"])
	fmt.Printf("ssh exec:  %s\n", a.Commands["ssh_exec"])
	fmt.Printf("ssh-config: %s\n", a.Commands["ssh_config"])
	if d := a.Commands["ssh_direct"]; d != "" {
		fmt.Printf("ssh direct: %s\n", d)
	}
	fmt.Printf("tcp:       %s\n", a.TCP.URL)
	fmt.Printf("terminal:  %s (detached sessions kept %ds)\n", a.Terminal.URL, a.Terminal.DetachTTLS)
	for _, s := range a.Terminal.Sessions {
		fmt.Printf("  session %s viewers=%d %dx%d\n", s.ID, s.Viewers, s.Cols, s.Rows)
	}
	for _, n := range a.Notes {
		fmt.Println("note:", n)
	}
	return nil
}

// ---- box forward ----

func cmdForward(ctx context.Context, c *client.Client, args []string) error {
	fs := flag.NewFlagSet("forward", flag.ContinueOnError)
	bind := fs.String("bind", "127.0.0.1", "local address to listen on")
	allow := fs.Bool("allow", true, "add the port to the box's forward allowlist when needed")
	if err := fs.Parse(reorder(fs, args)); err != nil {
		return err
	}
	args = fs.Args()
	if len(args) < 2 {
		return fmt.Errorf("usage: box forward <box> <remote-port> [local-port]")
	}
	box := args[0]
	rport, err := strconv.Atoi(args[1])
	if err != nil || rport < 1 || rport > 65535 {
		return fmt.Errorf("remote port must be 1-65535: %q", args[1])
	}
	lport := rport
	if len(args) > 2 {
		if lport, err = strconv.Atoi(args[2]); err != nil {
			return fmt.Errorf("local port must be a number: %q", args[2])
		}
	}
	// fail fast (auth, allowlist) before listening
	t, err := createTicket(ctx, c, box, manager.CreateTicketInput{Purpose: model.PurposeTCP, Port: rport, Allow: *allow, TTLSeconds: 10})
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(*bind, strconv.Itoa(lport)))
	if err != nil && len(args) <= 2 {
		ln, err = net.Listen("tcp", net.JoinHostPort(*bind, "0"))
	}
	if err != nil {
		return err
	}
	defer ln.Close()
	fmt.Printf("Forwarding %s → %s:%d (via %s). Ctrl-C to stop.\n", ln.Addr(), box, rport, hostOf(firstNonEmpty(t.URL, t.CoreURL)))
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	var nconn atomic.Int64
	for {
		lc, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go func() {
			defer lc.Close()
			id := nconn.Add(1)
			var ws *wsconn.Conn
			var err error
			for attempt, wait := 0, 500*time.Millisecond; attempt < 4; attempt, wait = attempt+1, wait*2 {
				ws, err = dialAccess(ctx, c, box, manager.CreateTicketInput{Purpose: model.PurposeTCP, Port: rport, Allow: *allow}, "tcp", "port="+strconv.Itoa(rport))
				if err == nil || ctx.Err() != nil {
					break
				}
				var ae *client.APIError
				if errors.As(err, &ae) && (ae.Status == 401 || ae.Status == 403 || ae.Status == 404) {
					break
				}
				time.Sleep(wait)
			}
			if err != nil {
				fmt.Fprintf(os.Stderr, "box forward: connection %d: %v\n", id, err)
				return
			}
			defer ws.Close()
			done := make(chan struct{})
			go func() {
				io.Copy(ws, lc)
				ws.Close()
				close(done)
			}()
			io.Copy(lc, ws)
			if tc, ok := lc.(*net.TCPConn); ok {
				tc.CloseWrite()
			}
			select {
			case <-done:
			case <-time.After(2 * time.Second):
			}
		}()
	}
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}

// ---- ssh ----

type stdioConn struct {
	io.Reader
	io.Writer
}

func cmdSSHProxy(ctx context.Context, c *client.Client, args []string) error {
	anon := false
	if len(args) > 0 && (args[0] == "--anon" || args[0] == "-anon") {
		anon, args = true, args[1:]
	}
	if len(args) < 1 {
		return fmt.Errorf("usage: box ssh-proxy [--anon] <host> [port]  (ProxyCommand box ssh-proxy %%h)")
	}
	var ws *wsconn.Conn
	var err error
	if anon { // v1.11: no ticket / API key — the SSH layer authenticates (certificate or registered key)
		ws, err = dialSSHAnon(ctx, c, args[0])
	} else {
		ws, err = dialAccess(ctx, c, boxRefFromHost(args[0]), manager.CreateTicketInput{Purpose: model.PurposeSSH}, "ssh", "")
	}
	if err != nil {
		return err
	}
	defer ws.Close()
	done := make(chan struct{})
	go func() {
		io.Copy(os.Stdout, ws)
		close(done)
	}()
	go func() {
		io.Copy(ws, os.Stdin)
		ws.Close()
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
	return nil
}

func boxazDir() string {
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".boxaz")
}

type sshInfoResp struct {
	KnownHosts  string `json:"known_hosts"`
	Fingerprint string `json:"host_key_fingerprint"`
	Alias       string `json:"host_key_alias"`
	Direct      string `json:"direct"`
	Domain      string `json:"domain"`
}

// writeKnownHosts stores the gateway host key in ~/.boxaz/known_hosts.
func writeKnownHosts(ctx context.Context, c *client.Client) (*sshInfoResp, string, error) {
	var info sshInfoResp
	if err := c.JSON(ctx, "GET", "/ssh", nil, &info); err != nil {
		return nil, "", err
	}
	dir := boxazDir()
	os.MkdirAll(dir, 0o700)
	p := filepath.Join(dir, "known_hosts")
	if err := os.WriteFile(p, []byte(info.KnownHosts+"\n"), 0o600); err != nil {
		return nil, "", err
	}
	return &info, p, nil
}

func selfPath() string {
	if p, err := os.Executable(); err == nil {
		return p
	}
	return "box"
}

func cmdSSH(ctx context.Context, c *client.Client, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: box ssh <box> [ssh args…] [-- command]")
	}
	box := args[0]
	rest := args[1:]
	_, kh, err := writeKnownHosts(ctx, c)
	if err != nil {
		return err
	}
	var cmd []string
	for i, a := range rest {
		if a == "--" {
			cmd = rest[i+1:]
			rest = rest[:i]
			break
		}
	}
	if sshBin, err := exec.LookPath("ssh"); err == nil && os.Getenv("BOXAZ_SSH_BUILTIN") == "" {
		// user options first: for -o, OpenSSH keeps the first value it sees
		argv := append([]string{"ssh"}, rest...)
		argv = append(argv, "-o", "ProxyCommand="+selfPath()+" ssh-proxy %h", "-o", "HostKeyAlias="+manager.SSHHostKeyAlias,
			"-o", "UserKnownHostsFile="+kh, "-o", "ServerAliveInterval=25", "-o", "LogLevel=ERROR")
		argv = append(argv, "boxaz@"+box)
		argv = append(argv, cmd...)
		return syscall.Exec(sshBin, argv, os.Environ())
	}
	// no OpenSSH client (or BOXAZ_SSH_BUILTIN=1): built-in Go client — commands,
	// -L [bind:]lport:host:rport forwards and -N; no interactive shell.
	var fwds []string
	noCmd := false
	for i := 0; i < len(rest); i++ {
		switch {
		case rest[i] == "-L" && i+1 < len(rest):
			fwds = append(fwds, rest[i+1])
			i++
		case strings.HasPrefix(rest[i], "-L") && len(rest[i]) > 2:
			fwds = append(fwds, rest[i][2:])
		case rest[i] == "-N":
			noCmd = true
		}
	}
	if len(cmd) == 0 && !noCmd {
		return fmt.Errorf("no ssh client found: install OpenSSH for an interactive shell, or run a command: box ssh %s -- uname -a (built-in client: -L, -N)", box)
	}
	code, err := sshRunBuiltin(ctx, c, box, strings.Join(cmd, " "), fwds, os.Stdin, os.Stdout, os.Stderr)
	if err != nil {
		return err
	}
	os.Exit(code)
	return nil
}

// sshRunBuiltin runs one command (or, with an empty command, waits for
// Ctrl-C) over the SSH gateway with the Go client, serving -L forwards.
func sshRunBuiltin(ctx context.Context, c *client.Client, box, command string, fwds []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	info, _, err := writeKnownHosts(ctx, c)
	if err != nil {
		return 0, err
	}
	pk, _, _, _, err := ssh.ParseAuthorizedKey([]byte(strings.TrimPrefix(info.KnownHosts, info.Alias+" ")))
	if err != nil {
		return 0, err
	}
	ws, err := dialAccess(ctx, c, box, manager.CreateTicketInput{Purpose: model.PurposeSSH}, "ssh", "")
	if err != nil {
		return 0, err
	}
	sc, chans, reqs, err := ssh.NewClientConn(ws, box, &ssh.ClientConfig{User: "boxaz", HostKeyCallback: ssh.FixedHostKey(pk), Timeout: 100 * time.Second})
	if err != nil {
		return 0, err
	}
	cl := ssh.NewClient(sc, chans, reqs)
	defer cl.Close()
	for _, f := range fwds {
		parts := strings.Split(f, ":")
		bind := "127.0.0.1"
		if len(parts) == 4 {
			bind, parts = parts[0], parts[1:]
		}
		if len(parts) != 3 {
			return 0, fmt.Errorf("-L wants [bind:]lport:host:rport, got %q", f)
		}
		ln, err := net.Listen("tcp", net.JoinHostPort(bind, parts[0]))
		if err != nil {
			return 0, err
		}
		defer ln.Close()
		target := net.JoinHostPort(parts[1], parts[2])
		fmt.Fprintf(stderr, "box ssh: forwarding %s → %s in %s\n", ln.Addr(), target, box)
		go func() {
			for {
				lc, err := ln.Accept()
				if err != nil {
					return
				}
				go func() {
					defer lc.Close()
					rc, err := cl.Dial("tcp", target)
					if err != nil {
						fmt.Fprintf(stderr, "box ssh: -L %s: %v\n", target, err)
						return
					}
					defer rc.Close()
					go func() { io.Copy(rc, lc); rc.Close() }()
					io.Copy(lc, rc)
				}()
			}
		}()
	}
	if command == "" {
		done := make(chan struct{})
		go func() { cl.Wait(); close(done) }()
		select {
		case <-ctx.Done():
		case <-done:
		}
		return 0, nil
	}
	sess, err := cl.NewSession()
	if err != nil {
		return 0, err
	}
	sess.Stdin, sess.Stdout, sess.Stderr = stdin, stdout, stderr
	err = sess.Run(command)
	var ee *ssh.ExitError
	if errors.As(err, &ee) {
		return ee.ExitStatus(), nil
	}
	if err != nil {
		return 0, err
	}
	return 0, nil
}

const sshCfgBegin = "# >>> boxaz ssh-config (managed by `box ssh-config --write`) >>>"
const sshCfgEnd = "# <<< boxaz ssh-config <<<"

func cmdSSHConfig(ctx context.Context, c *client.Client, args []string) error {
	fs := flag.NewFlagSet("ssh-config", flag.ContinueOnError)
	write := fs.Bool("write", false, "write the block into ~/.ssh/config (replacing an older Boxaz block)")
	useCert := fs.Bool("cert", false, "authenticate with short-lived certificates (~/.boxaz/id_ed25519; no API key needed by ssh)")
	certTTL := fs.Duration("ttl", 12*time.Hour, "--cert: certificate lifetime (≤ 12h)")
	if err := fs.Parse(reorder(fs, args)); err != nil {
		return err
	}
	info, _, err := writeKnownHosts(ctx, c)
	if err != nil {
		return err
	}
	var hosts []string
	names := fs.Args()
	if len(names) == 0 {
		var env struct {
			Items []model.Box `json:"items"`
		}
		if err := c.JSON(ctx, "GET", "/boxes", nil, &env); err != nil {
			return err
		}
		for _, b := range env.Items {
			if b.Status == model.StatusRunning || b.Status == model.StatusSleeping {
				names = append(names, firstNonEmpty(b.Subdomain, b.Slug))
			}
		}
	}
	for _, n := range names {
		if info.Domain != "" {
			hosts = append(hosts, n+"."+info.Domain)
		} else {
			hosts = append(hosts, n+".boxaz")
		}
	}
	proxy := selfPath() + " ssh-proxy %h"
	var b strings.Builder
	b.WriteString(sshCfgBegin + "\n")
	if *useCert {
		blk, err := certConfigBlock(ctx, c, names, info.Domain, *certTTL)
		if err != nil {
			return err
		}
		b.WriteString(blk)
	} else {
		b.WriteString(manager.SSHConfigBlock(hosts, proxy))
	}
	if info.Domain != "" {
		b.WriteString(manager.SSHConfigBlock([]string{"*." + info.Domain}, proxy))
	}
	b.WriteString(sshCfgEnd + "\n")
	block := strings.ReplaceAll(b.String(), "HostName *."+info.Domain+"\n", "")
	if !*write {
		fmt.Print(block)
		return nil
	}
	h, _ := os.UserHomeDir()
	p := filepath.Join(h, ".ssh", "config")
	os.MkdirAll(filepath.Dir(p), 0o700)
	old, _ := os.ReadFile(p)
	s := string(old)
	if i := strings.Index(s, sshCfgBegin); i >= 0 {
		if j := strings.Index(s[i:], sshCfgEnd); j >= 0 {
			s = s[:i] + strings.TrimLeft(s[i+j+len(sshCfgEnd):], "\n")
		}
	}
	// Host blocks match first-wins: put ours on top
	if err := os.WriteFile(p, []byte(block+"\n"+s), 0o600); err != nil {
		return err
	}
	fmt.Printf("wrote %d host(s) to %s: %s\n", len(hosts), p, strings.Join(hosts, " "))
	return nil
}

func cmdSSHKey(ctx context.Context, c *client.Client, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: box ssh-key add <file.pub> [--name n] | ls | rm <id>")
	}
	switch args[0] {
	case "ls", "list":
		var env struct {
			Items []model.SSHKey `json:"items"`
		}
		if err := c.JSON(ctx, "GET", "/ssh/keys", nil, &env); err != nil {
			return err
		}
		for _, k := range env.Items {
			fmt.Printf("%s\t%s\t%s\n", k.ID, k.Fingerprint, k.Name)
		}
		return nil
	case "rm", "delete":
		if len(args) < 2 {
			return fmt.Errorf("usage: box ssh-key rm <id>")
		}
		return c.JSON(ctx, "DELETE", "/ssh/keys/"+url.PathEscape(args[1]), nil, nil)
	case "add":
		fs := flag.NewFlagSet("ssh-key add", flag.ContinueOnError)
		name := fs.String("name", "", "key name")
		if err := fs.Parse(reorder(fs, args[1:])); err != nil {
			return err
		}
		if fs.NArg() < 1 {
			return fmt.Errorf("usage: box ssh-key add <file.pub>")
		}
		pub, err := os.ReadFile(fs.Arg(0))
		if err != nil {
			return err
		}
		var k model.SSHKey
		if err := c.JSON(ctx, "POST", "/ssh/keys", map[string]any{"name": *name, "public_key": string(pub)}, &k); err != nil {
			return err
		}
		fmt.Printf("%s\t%s\t%s\n", k.ID, k.Fingerprint, k.Name)
		return nil
	}
	return fmt.Errorf("unknown ssh-key command %q", args[0])
}

func cmdTerminals(ctx context.Context, c *client.Client, args []string) error {
	fs := flag.NewFlagSet("terminals", flag.ContinueOnError)
	kill := fs.String("kill", "", "session id to kill (all = every session)")
	if err := fs.Parse(reorder(fs, args)); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("usage: box terminals <box> [--kill <session_id>|all]")
	}
	base := "/boxes/" + url.PathEscape(fs.Arg(0)) + "/terminals"
	if *kill != "" {
		var r struct {
			Killed int `json:"killed"`
		}
		if err := c.JSON(ctx, "DELETE", base+"/"+url.PathEscape(*kill), nil, &r); err != nil {
			return err
		}
		fmt.Printf("killed %d session(s)\n", r.Killed)
		return nil
	}
	var env struct {
		Items []model.TerminalSession `json:"items"`
	}
	if err := c.JSON(ctx, "GET", base, nil, &env); err != nil {
		return err
	}
	for _, s := range env.Items {
		state := "attached"
		if s.Viewers == 0 {
			state = "detached"
			if s.ExpiresAt != nil {
				state += " until " + s.ExpiresAt.Local().Format("15:04:05")
			}
		}
		fmt.Printf("%s\t%dx%d\tviewers=%d\t%s\t%d bytes buffered\n", s.ID, s.Cols, s.Rows, s.Viewers, state, s.Buffered)
	}
	return nil
}
