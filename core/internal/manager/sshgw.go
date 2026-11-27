package manager

// v1.10 SSH gateway: an SSH server inside core (golang.org/x/crypto/ssh), so
// no sshd is needed in any box. Two ways in:
//
//   - over WebSocket (Cloudflare-friendly): wss://<name>.<domain>/.boxmes/ssh
//     or /v1/boxes/{id}/ssh, authenticated by a ticket (purpose ssh) or an
//     API key; `box ssh-proxy %h` is the ProxyCommand. The SSH layer then
//     accepts any client auth (the transport is already authenticated).
//   - direct TCP listener (BOXMES_SSH_ADDR, needs a public IP / DNS-only
//     record): `ssh <ticket>@ssh.<domain>` (ticket as the username) or
//     `ssh <box>@ssh.<domain>` with a public key registered via /v1/ssh/keys.
//
// Sessions hand off to the box over proc.open (shell, exec, pty, env,
// window-change, signals, the sftp subsystem when the box has sftp-server)
// and direct-tcpip channels to the TCP stream (ssh -L, VS Code / Cursor
// Remote-SSH). Remote forwarding, agent and X11 forwarding are refused.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"boxmes/core/internal/hostlink"
	"boxmes/core/internal/model"
	"boxmes/core/internal/proto"
	"boxmes/core/internal/secret"
)

type sshState struct {
	mu     sync.Mutex
	signer ssh.Signer
	ca     ssh.Signer // v1.11 user CA
	ln     net.Listener
	fails  map[string][]time.Time // client ip → recent auth failures
}

const sshHostKeyKV = "ssh:hostkey"
const sshKeysKV = "ssh:keys"

// sshSigner loads (or creates once) the gateway host key, stored sealed.
func (m *Manager) sshSigner() (ssh.Signer, error) {
	m.acc.ssh.mu.Lock()
	defer m.acc.ssh.mu.Unlock()
	if m.acc.ssh.signer != nil {
		return m.acc.ssh.signer, nil
	}
	ctx := context.Background()
	var sealed string
	m.Store.GetKV(ctx, sshHostKeyKV, &sealed)
	if sealed != "" {
		if pemText, err := m.Crypt.Open(sealed); err == nil {
			if s, err := ssh.ParsePrivateKey([]byte(pemText)); err == nil {
				m.acc.ssh.signer = s
				return s, nil
			}
		}
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	blk, err := ssh.MarshalPrivateKey(priv, "boxmes ssh gateway")
	if err != nil {
		return nil, err
	}
	pemText := string(pem.EncodeToMemory(blk))
	if sealed, err = m.Crypt.Seal(pemText); err != nil {
		return nil, err
	}
	if err := m.Store.PutKV(ctx, sshHostKeyKV, sealed); err != nil {
		return nil, err
	}
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return nil, err
	}
	m.acc.ssh.signer = s
	return s, nil
}

// SSHKnownHosts returns the known_hosts line (under SSHHostKeyAlias) and the
// SHA256 fingerprint of the gateway host key.
func (m *Manager) SSHKnownHosts() (string, string) {
	s, err := m.sshSigner()
	if err != nil {
		return "", ""
	}
	pk := s.PublicKey()
	return SSHHostKeyAlias + " " + strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pk))), ssh.FingerprintSHA256(pk)
}

// SSHDirect is the host:port of the direct SSH listener users dial ("" = off).
func (m *Manager) SSHDirect() string {
	if m.Cfg.SSHPublic != "" {
		return m.Cfg.SSHPublic
	}
	if m.Cfg.SSHAddr == "" {
		return ""
	}
	_, port, err := net.SplitHostPort(m.Cfg.SSHAddr)
	if err != nil {
		return ""
	}
	host := "ssh." + m.GatewayDomain(context.Background())
	if m.GatewayDomain(context.Background()) == "" {
		host = "localhost"
	}
	return net.JoinHostPort(host, port)
}

// ---- registered public keys ----

// AddSSHKeyInput is POST /v1/ssh/keys.
type AddSSHKeyInput struct {
	Name      string `json:"name"`
	PublicKey string `json:"public_key"`
}

func (m *Manager) loadSSHKeys(ctx context.Context) []*model.SSHKey {
	var list []*model.SSHKey
	m.Store.GetKV(ctx, sshKeysKV, &list)
	return list
}

// sshKeyWS is the workspace of a registered key (pre-v2.0 keys: default).
func sshKeyWS(k *model.SSHKey) string {
	if k.WorkspaceID == "" {
		return model.DefaultWorkspaceID
	}
	return k.WorkspaceID
}

// ListSSHKeys lists the caller's keys (admin: all).
func (m *Manager) ListSSHKeys(ctx context.Context, p *Principal) []*model.SSHKey {
	out := []*model.SSHKey{}
	for _, k := range m.loadSSHKeys(ctx) {
		if p.Sees(sshKeyWS(k), k.AgentID) {
			out = append(out, k)
		}
	}
	return out
}

// AddSSHKey registers a public key for SSH to the caller's boxes.
func (m *Manager) AddSSHKey(ctx context.Context, p *Principal, in AddSSHKeyInput) (*model.SSHKey, error) {
	if err := p.Require(model.ScopeBoxExec); err != nil {
		return nil, err
	}
	pk, comment, _, _, err := ssh.ParseAuthorizedKey([]byte(strings.TrimSpace(in.PublicKey)))
	if err != nil {
		return nil, model.EH(model.ErrInvalid, "Paste one line of ~/.ssh/id_ed25519.pub (or id_rsa.pub).", "public_key is not an SSH public key")
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = comment
	}
	if len(name) > 80 {
		name = name[:80]
	}
	k := &model.SSHKey{ID: secret.ID("sshk"), Name: name, PublicKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pk))),
		Fingerprint: ssh.FingerprintSHA256(pk), AgentID: p.AgentID, CreatedAt: time.Now().UTC()}
	if ws, err := p.TargetWS(); err == nil {
		k.WorkspaceID = ws
	}
	if p.Key != nil {
		k.KeyID = p.Key.ID
	}
	lock := m.boxLock("ssh-keys")
	lock.Lock()
	defer lock.Unlock()
	list := m.loadSSHKeys(ctx)
	for _, x := range list {
		if x.Fingerprint == k.Fingerprint && x.AgentID == k.AgentID && sshKeyWS(x) == k.WorkspaceID && x.KeyID == k.KeyID {
			return x, nil
		}
	}
	if len(list) >= 500 {
		return nil, model.EH(model.ErrPolicyDenied, "Delete unused keys first.", "too many SSH keys")
	}
	list = append(list, k)
	if err := m.Store.PutKV(ctx, sshKeysKV, list); err != nil {
		return nil, err
	}
	m.Emit(ctx, p.Actor(), "", p.AgentID, "ssh.key", "SSH key "+k.Fingerprint+" added", nil)
	return k, nil
}

// DeleteSSHKey removes one of the caller's keys.
func (m *Manager) DeleteSSHKey(ctx context.Context, p *Principal, id string) error {
	if err := p.Require(model.ScopeBoxExec); err != nil {
		return err
	}
	lock := m.boxLock("ssh-keys")
	lock.Lock()
	defer lock.Unlock()
	list := m.loadSSHKeys(ctx)
	for i, x := range list {
		if x.ID == id && p.Sees(sshKeyWS(x), x.AgentID) {
			list = append(list[:i], list[i+1:]...)
			return m.Store.PutKV(ctx, sshKeysKV, list)
		}
	}
	return model.E(model.ErrNotFound, "SSH key %s not found", id)
}

// ---- serving ----

// SSHTarget is a pre-authenticated SSH connection (the WebSocket path).
type SSHTarget struct {
	Box      *model.Box
	P        *Principal
	TicketID string
}

func (m *Manager) sshAuthFailed(ip string) bool {
	m.acc.ssh.mu.Lock()
	defer m.acc.ssh.mu.Unlock()
	if m.acc.ssh.fails == nil {
		m.acc.ssh.fails = map[string][]time.Time{}
	}
	now := time.Now()
	keep := m.acc.ssh.fails[ip][:0]
	for _, t := range m.acc.ssh.fails[ip] {
		if now.Sub(t) < time.Minute {
			keep = append(keep, t)
		}
	}
	keep = append(keep, now)
	m.acc.ssh.fails[ip] = keep
	return len(keep) > 20
}

func (m *Manager) sshThrottled(ip string) bool {
	m.acc.ssh.mu.Lock()
	defer m.acc.ssh.mu.Unlock()
	n := 0
	for _, t := range m.acc.ssh.fails[ip] {
		if time.Since(t) < time.Minute {
			n++
		}
	}
	return n > 20
}

// direct-listener auth: the username is a ticket (none / password / any key)
// or a box ref with a registered public key.
func (m *Manager) sshDirectConfig(signer ssh.Signer) *ssh.ServerConfig {
	ipOf := func(c ssh.ConnMetadata) string { return hostOnlyAddr(c.RemoteAddr().String()) }
	byTicket := func(c ssh.ConnMetadata) (*ssh.Permissions, error) {
		if !strings.HasPrefix(c.User(), AccessTicketPrefix) {
			return nil, errors.New("not a ticket")
		}
		t, _, b, err := m.RedeemTicket(context.Background(), c.User(), model.PurposeSSH, "", 0)
		if err != nil {
			m.sshAuthFailed(ipOf(c))
			return nil, err
		}
		return &ssh.Permissions{Extensions: map[string]string{"box": b.ID, "key": t.KeyID, "ticket": t.ID}}, nil
	}
	cfg := &ssh.ServerConfig{
		ServerVersion:        "SSH-2.0-boxmes",
		MaxAuthTries:         6,
		NoClientAuth:         true,
		NoClientAuthCallback: byTicket,
		PasswordCallback: func(c ssh.ConnMetadata, _ []byte) (*ssh.Permissions, error) {
			return byTicket(c)
		},
		PublicKeyCallback: func(c ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if strings.HasPrefix(c.User(), AccessTicketPrefix) {
				return byTicket(c)
			}
			if _, isCert := key.(*ssh.Certificate); isCert { // v1.11: core-signed user certificate
				perms, err := m.sshCertAuth(key, "", c.User())
				if err != nil {
					m.sshAuthFailed(ipOf(c))
				}
				return perms, err
			}
			if perms, err := m.sshRegisteredKeyAuth(key, c.User()); err == nil {
				return perms, nil
			}
			m.sshAuthFailed(ipOf(c))
			return nil, errors.New("no box for this user and key")
		},
	}
	cfg.AddHostKey(signer)
	return cfg
}

func hostOnlyAddr(a string) string {
	if h, _, err := net.SplitHostPort(a); err == nil {
		return h
	}
	return a
}

// StartSSH starts the direct SSH listener (BOXMES_SSH_ADDR) until ctx ends.
func (m *Manager) StartSSH(ctx context.Context) error {
	if m.Cfg.SSHAddr == "" {
		return nil
	}
	signer, err := m.sshSigner()
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", m.Cfg.SSHAddr)
	if err != nil {
		return err
	}
	m.acc.ssh.mu.Lock()
	m.acc.ssh.ln = ln
	m.acc.ssh.mu.Unlock()
	cfg := m.sshDirectConfig(signer)
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			if m.sshThrottled(hostOnlyAddr(c.RemoteAddr().String())) {
				c.Close()
				continue
			}
			go m.serveSSHConn(ctx, c, cfg, nil)
		}
	}()
	log.Printf("ssh gateway listening on %s", ln.Addr())
	return nil
}

// SSHListenAddr is the bound address of the direct listener ("" = off).
func (m *Manager) SSHListenAddr() string {
	m.acc.ssh.mu.Lock()
	defer m.acc.ssh.mu.Unlock()
	if m.acc.ssh.ln == nil {
		return ""
	}
	return m.acc.ssh.ln.Addr().String()
}

// ServeSSH runs the SSH protocol on an already-authenticated transport (the
// WebSocket endpoint).
func (m *Manager) ServeSSH(ctx context.Context, conn net.Conn, t *SSHTarget) {
	signer, err := m.sshSigner()
	if err != nil {
		conn.Close()
		return
	}
	cfg := &ssh.ServerConfig{ServerVersion: "SSH-2.0-boxmes", NoClientAuth: true,
		PasswordCallback:  func(ssh.ConnMetadata, []byte) (*ssh.Permissions, error) { return nil, nil },
		PublicKeyCallback: func(ssh.ConnMetadata, ssh.PublicKey) (*ssh.Permissions, error) { return nil, nil }}
	cfg.AddHostKey(signer)
	m.serveSSHConn(ctx, conn, cfg, t)
}

func (m *Manager) serveSSHConn(ctx context.Context, conn net.Conn, cfg *ssh.ServerConfig, t *SSHTarget) {
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(30 * time.Second))
	sconn, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		return
	}
	conn.SetDeadline(time.Time{})
	defer sconn.Close()
	if t == nil { // direct listener: the auth callbacks put the box in Permissions
		ext := sconn.Permissions.Extensions
		p, err := m.principalForKey(ctx, ext["key"])
		if err != nil {
			return
		}
		b, err := m.Store.GetBox(ctx, ext["box"])
		if err != nil {
			return
		}
		t = &SSHTarget{Box: b, P: p, TicketID: ext["ticket"]}
	}
	b := t.Box
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	unbind := m.BindAccess(b.ID, t.TicketID, "ssh", func(string) { sconn.Close() })
	defer unbind()
	go m.KeepActive(cctx, b.ID)
	go ssh.DiscardRequests(reqs) // tcpip-forward (-R) and others: refused
	go func() {                  // server keepalive (Cloudflare / NAT idle timeouts)
		tk := time.NewTicker(25 * time.Second)
		defer tk.Stop()
		for {
			select {
			case <-cctx.Done():
				return
			case <-tk.C:
				if _, _, err := sconn.SendRequest("keepalive@openssh.com", true, nil); err != nil {
					sconn.Close()
					return
				}
			}
		}
	}()
	start := time.Now()
	m.Emit(ctx, t.P.Actor(), b.ID, b.AgentID, "box.ssh", "SSH session opened from "+hostOnlyAddr(sconn.RemoteAddr().String()), nil)
	var linkMu sync.Mutex
	var peer *hostlink.Peer
	link := func() (*hostlink.Peer, error) { // wake + link once, on the first channel
		linkMu.Lock()
		defer linkMu.Unlock()
		if peer != nil {
			select {
			case <-peer.Done():
			default:
				return peer, nil
			}
		}
		cur, err := m.Store.GetBox(cctx, b.ID)
		if err != nil {
			return nil, err
		}
		_, p, err := m.AccessLink(cctx, cur)
		if err != nil {
			return nil, err
		}
		peer = p
		return p, nil
	}
	nch := 0
	for nc := range chans {
		nch++
		switch nc.ChannelType() {
		case "session":
			go m.sshSession(cctx, b, nc, link)
		case "direct-tcpip":
			go m.sshDirectTCPIP(cctx, b, nc, link)
		default:
			nc.Reject(ssh.Prohibited, "boxmes: only session and direct-tcpip channels are supported")
		}
	}
	m.Emit(context.Background(), t.P.Actor(), b.ID, b.AgentID, "box.ssh", fmt.Sprintf("SSH session closed after %s (%d channels)", time.Since(start).Round(time.Second), nch), nil)
}

type ptyReqMsg struct {
	Term     string
	Cols     uint32
	Rows     uint32
	Width    uint32
	Height   uint32
	Modelist string
}

func (m *Manager) sshSession(ctx context.Context, b *model.Box, nc ssh.NewChannel, link func() (*hostlink.Peer, error)) {
	ch, reqs, err := nc.Accept()
	if err != nil {
		return
	}
	defer ch.Close()
	var env []string
	var pty *ptyReqMsg
	var proc *hostlink.Proc
	done := make(chan struct{})
	for {
		var req *ssh.Request
		select {
		case req = <-reqs:
		case <-done:
			return
		case <-ctx.Done():
			return
		}
		if req == nil {
			if proc != nil && proc.Stdio != nil {
				proc.Close()
			}
			return
		}
		switch req.Type {
		case "pty-req":
			var p ptyReqMsg
			if ssh.Unmarshal(req.Payload, &p) != nil {
				req.Reply(false, nil)
				continue
			}
			pty = &p
			req.Reply(true, nil)
		case "env":
			var kv struct{ Name, Value string }
			if ssh.Unmarshal(req.Payload, &kv) == nil && validEnvName(kv.Name) && len(env) < 64 {
				env = append(env, kv.Name+"="+kv.Value)
				req.Reply(true, nil)
			} else {
				req.Reply(false, nil)
			}
		case "shell", "exec", "subsystem":
			if proc != nil {
				req.Reply(false, nil)
				continue
			}
			sp := proto.ProcOpenParams{BoxID: b.ID, Env: env}
			switch req.Type {
			case "exec":
				var c struct{ Command string }
				if ssh.Unmarshal(req.Payload, &c) != nil {
					req.Reply(false, nil)
					continue
				}
				sp.Command = c.Command
			case "subsystem":
				var s struct{ Name string }
				if ssh.Unmarshal(req.Payload, &s) != nil || s.Name != "sftp" {
					req.Reply(false, nil)
					continue
				}
				// v1.11: the box's sftp-server, else core's own SFTP server
				peer, err := link()
				if err != nil {
					req.Reply(false, nil)
					fmt.Fprintf(ch.Stderr(), "boxmes: %s\r\n", model.AsAPIError(err).Message)
					ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Code uint32 }{255}))
					return
				}
				req.Reply(true, nil)
				go func() {
					code := m.serveSFTP(ctx, b.ID, peer, ch)
					ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Code uint32 }{uint32(code)}))
					ch.CloseWrite()
					close(done)
				}()
				proc = &hostlink.Proc{} // marks the channel as busy
				continue
			}
			if pty != nil && req.Type != "subsystem" {
				sp.TTY, sp.Term, sp.Cols, sp.Rows = true, pty.Term, int(pty.Cols), int(pty.Rows)
			}
			peer, err := link()
			if err == nil {
				cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
				proc, err = peer.OpenProc(cctx, sp)
				cancel()
				if errors.Is(err, hostlink.ErrStreamUnsupported) {
					err = model.EH(model.ErrProviderFailed, "Restart the box's agent (sleep and wake) so it runs the current boxd.", "the box agent does not support SSH sessions yet")
				}
			}
			if err != nil {
				req.Reply(false, nil)
				fmt.Fprintf(ch.Stderr(), "boxmes: %s\r\n", model.AsAPIError(err).Message)
				ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Code uint32 }{255}))
				return
			}
			req.Reply(true, nil)
			go func(pr *hostlink.Proc) {
				go func() {
					io.Copy(pr.Stdio, ch)
					pr.CloseStdin()
				}()
				var wg sync.WaitGroup
				wg.Add(2)
				go func() { io.Copy(ch, pr.Stdio); wg.Done() }()
				go func() { io.Copy(ch.Stderr(), pr.Stderr); wg.Done() }()
				wg.Wait()
				code := pr.Wait()
				if code < 0 {
					code = 255
				}
				ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Code uint32 }{uint32(code)}))
				ch.CloseWrite()
				close(done)
			}(proc)
		case "window-change":
			var w struct{ Cols, Rows, Width, Height uint32 }
			if ssh.Unmarshal(req.Payload, &w) == nil && proc != nil && proc.Stdio != nil {
				proc.Resize(int(w.Cols), int(w.Rows))
			}
			if req.WantReply {
				req.Reply(true, nil)
			}
		case "signal":
			var s struct{ Name string }
			if ssh.Unmarshal(req.Payload, &s) == nil && proc != nil && proc.Stdio != nil {
				proc.Signal(s.Name)
			}
			if req.WantReply {
				req.Reply(true, nil)
			}
		default: // auth-agent-req@openssh.com, x11-req, …
			req.Reply(false, nil)
		}
	}
}

func validEnvName(n string) bool {
	if n == "" || len(n) > 64 || strings.HasPrefix(n, "BOXD_") || n == "LD_PRELOAD" || n == "LD_LIBRARY_PATH" {
		return false
	}
	for _, r := range n {
		if !(r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

func (m *Manager) sshDirectTCPIP(ctx context.Context, b *model.Box, nc ssh.NewChannel, link func() (*hostlink.Peer, error)) {
	var d struct {
		Host       string
		Port       uint32
		OriginHost string
		OriginPort uint32
	}
	if ssh.Unmarshal(nc.ExtraData(), &d) != nil {
		nc.Reject(ssh.ConnectionFailed, "bad direct-tcpip request")
		return
	}
	h := strings.Trim(strings.ToLower(d.Host), "[]")
	if !(h == "localhost" || h == "127.0.0.1" || h == "::1" || h == "0.0.0.0" || h == b.Subdomain) || d.Port < 1 || d.Port > 65535 {
		nc.Reject(ssh.Prohibited, "boxmes: only ports inside the box (localhost:<port>) can be forwarded")
		return
	}
	peer, err := link()
	if err != nil {
		nc.Reject(ssh.ConnectionFailed, model.AsAPIError(err).Message)
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	sc, err := peer.OpenStream(cctx, b.ID, int(d.Port))
	cancel()
	if err != nil {
		nc.Reject(ssh.ConnectionFailed, "boxmes: "+strconv.Itoa(int(d.Port))+": "+model.AsAPIError(err).Message)
		return
	}
	ch, reqs, err := nc.Accept()
	if err != nil {
		sc.Close()
		return
	}
	go ssh.DiscardRequests(reqs)
	go func() {
		io.Copy(sc, ch)
		sc.CloseWrite()
	}()
	io.Copy(ch, sc)
	ch.Close()
	sc.Close()
}
