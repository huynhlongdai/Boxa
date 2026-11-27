package main

// v1.11 CLI: access P1 — `box ssh-cert`, `box ssh-config --cert`, `box sftp`,
// `box ssh-proxy --anon`, `box browser` (CDP endpoint) and `box desktop`
// (VNC desktop + view-only share links).

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"boxmes/core/internal/client"
	"boxmes/core/internal/manager"
	"boxmes/core/internal/model"
	"boxmes/core/internal/wsconn"
)

// dialSSHAnon opens the SSH WebSocket without a ticket: wss://<host>/.boxmes/ssh
// for a gateway host, else core's /v1/boxes/<ref>/ssh.
func dialSSHAnon(ctx context.Context, c *client.Client, host string) (*wsconn.Conn, error) {
	host = strings.ToLower(strings.TrimSpace(host))
	ref := boxRefFromHost(host)
	var urls []string
	if strings.Contains(host, ".") && !strings.HasSuffix(host, ".boxmes") && !strings.HasSuffix(host, ".boxaz") && os.Getenv("BOXMES_ACCESS_VIA") != "core" {
		scheme := "wss://"
		if strings.HasPrefix(c.BaseURL, "http://") && strings.Contains(c.BaseURL, host) {
			scheme = "ws://"
		}
		urls = append(urls, scheme+host+"/.boxmes/ssh")
	}
	base := c.BaseURL
	switch {
	case strings.HasPrefix(base, "https://"):
		base = "wss://" + base[8:]
	case strings.HasPrefix(base, "http://"):
		base = "ws://" + base[7:]
	}
	urls = append(urls, strings.TrimRight(base, "/")+"/v1/boxes/"+url.PathEscape(ref)+"/ssh")
	var last error
	for _, u := range urls {
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

// ensureBoxmesKey returns ~/.boxmes/id_ed25519 (created on first use) and its public key line.
func ensureBoxmesKey() (string, string, error) {
	dir := boxmesDir()
	os.MkdirAll(dir, 0o700)
	p := filepath.Join(dir, "id_ed25519")
	if pub, err := os.ReadFile(p + ".pub"); err == nil {
		if _, err := os.Stat(p); err == nil {
			return p, strings.TrimSpace(string(pub)), nil
		}
	}
	pubk, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	blk, err := ssh.MarshalPrivateKey(priv, "box-cli")
	if err != nil {
		return "", "", err
	}
	if err := os.WriteFile(p, pem.EncodeToMemory(blk), 0o600); err != nil {
		return "", "", err
	}
	spk, _ := ssh.NewPublicKey(pubk)
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(spk))) + " box-cli"
	if err := os.WriteFile(p+".pub", []byte(line+"\n"), 0o644); err != nil {
		return "", "", err
	}
	return p, line, nil
}

func issueCert(ctx context.Context, c *client.Client, box, pub string, ttl time.Duration) (*manager.SSHCert, error) {
	var out manager.SSHCert
	err := c.JSON(ctx, "POST", "/ssh/certs", manager.SSHCertInput{Box: box, PublicKey: pub, TTLSeconds: int(ttl.Seconds())}, &out)
	return &out, err
}

// box ssh-cert <box> [--key file.pub] [--ttl 1h] [--out file] [--json]
func cmdSSHCert(ctx context.Context, c *client.Client, args []string) error {
	fs := flag.NewFlagSet("ssh-cert", flag.ContinueOnError)
	keyPath := fs.String("key", "", "public key to certify (default ~/.boxmes/id_ed25519.pub, created when missing)")
	ttl := fs.Duration("ttl", time.Hour, "certificate lifetime (≤ 12h)")
	out := fs.String("out", "", "where to write the certificate (default <key>-cert.pub)")
	asJSON := fs.Bool("json", false, "print the full response as JSON")
	if err := fs.Parse(reorder(fs, args)); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("usage: box ssh-cert <box> [--key ~/.ssh/id_ed25519.pub] [--ttl 1h]")
	}
	var priv, pub string
	if *keyPath == "" {
		p, line, err := ensureBoxmesKey()
		if err != nil {
			return err
		}
		priv, pub = p, line
	} else {
		b, err := os.ReadFile(*keyPath)
		if err != nil {
			return err
		}
		pub = strings.TrimSpace(string(b))
		priv = strings.TrimSuffix(*keyPath, ".pub")
	}
	cert, err := issueCert(ctx, c, fs.Arg(0), pub, *ttl)
	if err != nil {
		return err
	}
	dst := *out
	if dst == "" {
		dst = priv + "-cert.pub"
	}
	if err := os.WriteFile(dst, []byte(cert.Certificate+"\n"), 0o644); err != nil {
		return err
	}
	if _, kh, err := writeKnownHosts(ctx, c); err == nil {
		_ = kh
	}
	if *asJSON {
		printJSON(cert)
		return nil
	}
	fmt.Printf("certificate %s for %s (principals %s), valid until %s\n  saved to %s\n", cert.ID, cert.BoxID, strings.Join(cert.Principals, ","), cert.ValidBefore.Local().Format(time.RFC3339), dst)
	cmd := strings.NewReplacer("<key>-cert.pub", dst, "<key>", priv, "box ssh-proxy", selfPath()+" ssh-proxy").Replace(cert.Command)
	cmd = strings.Replace(cmd, "-o HostKeyAlias=", "-o UserKnownHostsFile="+filepath.Join(boxmesDir(), "known_hosts")+" -o HostKeyAlias=", 1)
	fmt.Printf("  ssh: %s\n", cmd)
	if cert.Direct != "" {
		fmt.Printf("  direct: %s\n", strings.NewReplacer("<key>-cert.pub", dst, "<key>", priv).Replace(cert.Direct))
	}
	fmt.Printf("  revoke: DELETE /v1/boxes/%s/tickets/%s\n", cert.BoxID, cert.ID)
	return nil
}

// certConfigBlock issues certificates for hosts and renders Host entries that
// authenticate with them (no API key needed by ssh: box ssh-proxy --anon).
func certConfigBlock(ctx context.Context, c *client.Client, names []string, domain string, ttl time.Duration) (string, error) {
	priv, pub, err := ensureBoxmesKey()
	if err != nil {
		return "", err
	}
	certDir := filepath.Join(boxmesDir(), "certs")
	os.MkdirAll(certDir, 0o700)
	var b strings.Builder
	for _, n := range names {
		cert, err := issueCert(ctx, c, n, pub, ttl)
		if err != nil {
			return "", fmt.Errorf("%s: %w", n, err)
		}
		host := n + ".boxmes"
		if domain != "" {
			host = n + "." + domain
		}
		cf := filepath.Join(certDir, host+"-cert.pub")
		if err := os.WriteFile(cf, []byte(cert.Certificate+"\n"), 0o644); err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "Host %s\n  HostName %s\n  User %s\n  IdentityFile %s\n  CertificateFile %s\n  IdentitiesOnly yes\n  ProxyCommand %s ssh-proxy --anon %%h\n  HostKeyAlias %s\n  UserKnownHostsFile ~/.boxmes/known_hosts\n  ServerAliveInterval 25\n  # certificate %s valid until %s\n\n",
			host, host, cert.User, priv, cf, selfPath(), manager.SSHHostKeyAlias, cert.ID, cert.ValidBefore.Local().Format(time.RFC3339))
	}
	return b.String(), nil
}

// ---- box sftp ----

// sftpClient connects the built-in SSH client and opens the sftp subsystem.
func sftpClient(ctx context.Context, c *client.Client, box string) (*sftp.Client, func(), error) {
	info, _, err := writeKnownHosts(ctx, c)
	if err != nil {
		return nil, nil, err
	}
	pk, _, _, _, err := ssh.ParseAuthorizedKey([]byte(strings.TrimPrefix(info.KnownHosts, info.Alias+" ")))
	if err != nil {
		return nil, nil, err
	}
	ws, err := dialAccess(ctx, c, box, manager.CreateTicketInput{Purpose: model.PurposeSSH}, "ssh", "")
	if err != nil {
		return nil, nil, err
	}
	sc, chans, reqs, err := ssh.NewClientConn(ws, box, &ssh.ClientConfig{User: "boxmes", HostKeyCallback: ssh.FixedHostKey(pk), Timeout: 100 * time.Second})
	if err != nil {
		return nil, nil, err
	}
	cl := ssh.NewClient(sc, chans, reqs)
	sf, err := sftp.NewClient(cl)
	if err != nil {
		cl.Close()
		return nil, nil, err
	}
	return sf, func() { sf.Close(); cl.Close() }, nil
}

// box sftp <box> put <local> <remote> | get <remote> <local> | ls [dir] | rm <path> | mkdir <dir>
func cmdSFTP(ctx context.Context, c *client.Client, args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: box sftp <box> put <local> <remote> | get <remote> <local> | ls [dir] | rm <path> | mkdir <dir>")
	}
	box, op, rest := args[0], args[1], args[2:]
	sf, done, err := sftpClient(ctx, c, box)
	if err != nil {
		return err
	}
	defer done()
	start := time.Now()
	switch op {
	case "put":
		if len(rest) < 2 {
			return fmt.Errorf("usage: box sftp <box> put <local> <remote>")
		}
		src, err := os.Open(rest[0])
		if err != nil {
			return err
		}
		defer src.Close()
		dstPath := rest[1]
		if st, err := sf.Stat(dstPath); err == nil && st.IsDir() {
			dstPath = path.Join(dstPath, filepath.Base(rest[0]))
		}
		dst, err := sf.OpenFile(dstPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
		if err != nil {
			return err
		}
		n, err := io.Copy(dst, src)
		if cerr := dst.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
		fmt.Printf("put %s → %s:%s (%d bytes, %s)\n", rest[0], box, dstPath, n, time.Since(start).Round(time.Millisecond))
	case "get":
		if len(rest) < 2 {
			return fmt.Errorf("usage: box sftp <box> get <remote> <local>")
		}
		src, err := sf.Open(rest[0])
		if err != nil {
			return err
		}
		defer src.Close()
		local := rest[1]
		if st, err := os.Stat(local); err == nil && st.IsDir() {
			local = filepath.Join(local, path.Base(rest[0]))
		}
		var dst io.Writer = os.Stdout
		if local != "-" {
			f, err := os.Create(local)
			if err != nil {
				return err
			}
			defer f.Close()
			dst = f
		}
		n, err := io.Copy(dst, src)
		if err != nil {
			return err
		}
		if local != "-" {
			fmt.Printf("get %s:%s → %s (%d bytes, %s)\n", box, rest[0], local, n, time.Since(start).Round(time.Millisecond))
		}
	case "ls":
		dir := "."
		if len(rest) > 0 {
			dir = rest[0]
		}
		items, err := sf.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, fi := range items {
			fmt.Printf("%s\t%d\t%s\t%s\n", fi.Mode(), fi.Size(), fi.ModTime().Local().Format("2006-01-02 15:04"), fi.Name())
		}
	case "rm":
		if len(rest) < 1 {
			return fmt.Errorf("usage: box sftp <box> rm <path>")
		}
		return sf.Remove(rest[0])
	case "mkdir":
		if len(rest) < 1 {
			return fmt.Errorf("usage: box sftp <box> mkdir <dir>")
		}
		return sf.MkdirAll(rest[0])
	default:
		return fmt.Errorf("unknown sftp command %q (put, get, ls, rm, mkdir)", op)
	}
	return nil
}

// ---- box browser ----

func cmdBrowser(ctx context.Context, c *client.Client, args []string) error {
	fs := flag.NewFlagSet("browser", flag.ContinueOnError)
	ttl := fs.Duration("ttl", time.Hour, "how long the endpoint URL works (reusable until then; ≤ 24h)")
	stop := fs.Bool("stop", false, "stop the box's browser")
	status := fs.Bool("status", false, "show whether the browser runs (does not start it)")
	headless := fs.Bool("headless", false, "force headless even when the desktop runs")
	asJSON := fs.Bool("json", false, "print the full response as JSON")
	if err := fs.Parse(reorder(fs, args)); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("usage: box browser <box> [--ttl 1h] [--stop] [--status] [--json]")
	}
	base := "/boxes/" + url.PathEscape(fs.Arg(0)) + "/browser"
	var out map[string]any
	var err error
	switch {
	case *stop:
		err = c.JSON(ctx, "DELETE", base, nil, &out)
	case *status:
		err = c.JSON(ctx, "GET", base, nil, &out)
	default:
		err = c.JSON(ctx, "POST", base, map[string]any{"ttl_s": int(ttl.Seconds()), "headless": *headless}, &out)
	}
	if err != nil {
		return err
	}
	if *asJSON || *stop || *status {
		printJSON(out)
		return nil
	}
	fmt.Printf("CDP endpoint (valid until %v):\n  %v\n", out["expires_at"], out["cdp_url"])
	if v, ok := out["version"].(map[string]any); ok {
		fmt.Printf("browser: %v (%v)", v["Browser"], map[bool]string{true: "headless", false: "on the desktop"}[out["headless"] == true])
	}
	if ms, ok := out["start_ms"].(float64); ok && out["started"] == true {
		fmt.Printf(", started in %.1fs", ms/1000)
	}
	fmt.Println()
	fmt.Printf("playwright: await chromium.connectOverCDP(%q)\npuppeteer:  await puppeteer.connect({browserWSEndpoint: %q})\n", out["cdp_url"], out["cdp_url"])
	return nil
}

// ---- box desktop ----

func cmdDesktop(ctx context.Context, c *client.Client, args []string) error {
	fs := flag.NewFlagSet("desktop", flag.ContinueOnError)
	share := fs.Bool("share", false, "create a share link (view-only unless --interactive)")
	interactive := fs.Bool("interactive", false, "share link may control the desktop")
	ttl := fs.Duration("ttl", time.Hour, "share link / viewer lifetime (60s … 24h)")
	revoke := fs.Bool("revoke-shares", false, "revoke every share link (viewers are disconnected)")
	list := fs.Bool("shares", false, "list share links")
	stop := fs.Bool("stop", false, "stop the desktop")
	status := fs.Bool("status", false, "show whether the desktop runs (does not start it)")
	size := fs.String("size", "", "screen size WxH when starting (default 1280x800)")
	asJSON := fs.Bool("json", false, "print the full response as JSON")
	if err := fs.Parse(reorder(fs, args)); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("usage: box desktop <box> [--share [--ttl 1h] [--interactive]] [--shares] [--revoke-shares] [--stop] [--status]")
	}
	base := "/boxes/" + url.PathEscape(fs.Arg(0)) + "/desktop"
	var out map[string]any
	var err error
	switch {
	case *stop:
		err = c.JSON(ctx, "DELETE", base, nil, &out)
	case *status:
		err = c.JSON(ctx, "GET", base, nil, &out)
	case *revoke:
		err = c.JSON(ctx, "DELETE", base+"/shares/all", nil, &out)
	case *list:
		err = c.JSON(ctx, "GET", base+"/shares", nil, &out)
	case *share:
		err = c.JSON(ctx, "POST", base+"/shares", map[string]any{"ttl_s": int(ttl.Seconds()), "view_only": !*interactive}, &out)
	default:
		in := map[string]any{"ttl_s": int(ttl.Seconds())}
		if *size != "" {
			var w, h int
			fmt.Sscanf(*size, "%dx%d", &w, &h)
			in["width"], in["height"] = w, h
		}
		err = c.JSON(ctx, "POST", base, in, &out)
	}
	if err != nil {
		return err
	}
	if *asJSON || *stop || *status || *revoke || *list {
		printJSON(out)
		return nil
	}
	if *share {
		mode := "view-only"
		if out["view_only"] == false {
			mode = "interactive"
		}
		fmt.Printf("share link (%s, until %v):\n  %v\nrevoke: box desktop %s --revoke-shares\n", mode, out["expires_at"], out["url"], fs.Arg(0))
		return nil
	}
	fmt.Printf("desktop %vx%v on display %v", out["width"], out["height"], out["display"])
	if ms, ok := out["start_ms"].(float64); ok && out["started"] == true {
		fmt.Printf(" (started in %.1fs)", ms/1000)
	}
	fmt.Printf("\nopen (single use, until %v):\n  %v\nVNC over WebSocket: %v\n", out["expires_at"], out["viewer_url"], out["vnc_url"])
	return nil
}
