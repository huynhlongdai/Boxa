package e2e

// v1.8: the gateway on a real domain (host-based <key>-<port>.<domain> URLs,
// the public URL host served as core, reserved / unknown subdomains 404) and
// private provider previews (the sandbox refuses traffic without its traffic
// token; only the gateway adds it upstream), with a fake T3 provider whose
// "preview host" is a local server enforcing the token.

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"boxaz/core/internal/manager"
	"boxaz/core/internal/model"
	"boxaz/core/internal/sandbox"
)

// privSandbox: deploySandbox + TrafficGuard; PreviewURL is the local preview server.
type privSandbox struct {
	*deploySandbox
	preview string
	mu      sync.Mutex
	private map[string]bool
}

func (p *privSandbox) Info() sandbox.Info {
	i := p.deploySandbox.Info()
	i.ID, i.Name = "fakepriv", "Fake private preview"
	return i
}
func (p *privSandbox) Create(ctx context.Context, c sandbox.Cred, s sandbox.Spec) (*sandbox.Sandbox, error) {
	sb, err := p.deploySandbox.Create(ctx, c, s)
	if err == nil && s.PrivateTraffic {
		p.mu.Lock()
		p.private[sb.ID] = true
		p.mu.Unlock()
		sb.Meta = map[string]string{sandbox.MetaPrivateTraffic: "1"}
	}
	return sb, err
}
func (p *privSandbox) PrivateTrafficSupported() bool { return true }
func (p *privSandbox) PreviewHeaders(ctx context.Context, c sandbox.Cred, id string) (map[string]string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.private[id] {
		return nil, nil
	}
	return map[string]string{"E2B-Traffic-Access-Token": "tat-" + id}, nil
}
func (p *privSandbox) PreviewURL(ctx context.Context, c sandbox.Cred, id string, port int) (string, error) {
	return p.preview, nil
}

func hostDo(t *testing.T, base, host, path string, hdr map[string]string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest("GET", base+path, nil)
	req.Host = host
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s (Host %s): %v", path, host, err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

func TestGatewayDomainPrivatePreview(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash required")
	}
	// the provider's preview host: 403 without the sandbox traffic token
	var seenMu sync.Mutex
	var seen []string
	prev := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// the gateway forwards to the preview origin (paths are the app's), so
		// the sandbox is told by its token; a real preview host is per sandbox
		tok := r.Header.Get("E2B-Traffic-Access-Token")
		seenMu.Lock()
		seen = append(seen, tok)
		seenMu.Unlock()
		if !strings.HasPrefix(tok, "tat-sb") {
			http.Error(w, "missing traffic access token", http.StatusForbidden)
			return
		}
		io.WriteString(w, "hello from "+strings.TrimPrefix(tok, "tat-"))
	}))
	t.Cleanup(prev.Close)

	dir := t.TempDir()
	fk := &privSandbox{deploySandbox: &deploySandbox{dir: dir, state: map[string]string{}}, preview: prev.URL, private: map[string]bool{}}
	reg := sandbox.DefaultRegistry(nil)
	reg.Add(sandbox.AsProvider(fk))
	e := setupReg(t, reg, func(c *manager.Config) {
		c.AutoSandbox = true
		c.ProviderChain = []string{"fakepriv"}
		c.HealthInterval = time.Hour
		c.SandboxEmptyAfter = 300 * time.Millisecond
		c.GatewayDomain = "Example.test."
	}, 0)
	e.m.Cfg.PublicURL = "https://gw.example.test" // core's public host (internet-facing name)
	ag, _ := e.agentClient("domain-agent", 0)
	e.must(e.admin, "PUT", "/providers/fakepriv/token", map[string]any{"token": "k"}, nil)

	// GET /v1/gateway
	var gi manager.GatewayInfo
	e.must(e.admin, "GET", "/gateway", nil, &gi)
	if gi.Mode != "host" || gi.Domain != "example.test" || gi.DomainSource != "env" || gi.URLPattern != "https://<key>-<port>.example.test/" ||
		!gi.InternetFacing || gi.ProviderPreview != "auto" || !gi.PrivatePreviews {
		t.Fatalf("gateway info %+v", gi)
	}
	foundRes, foundProv := false, false
	for _, r := range gi.Reserved {
		foundRes = foundRes || r == "hark"
	}
	for _, p := range gi.Providers {
		foundProv = foundProv || p.Provider == "fakepriv" && p.PrivatePreview && p.PreviewHost
	}
	if !foundRes || !foundProv {
		t.Fatalf("reserved/providers %+v", gi)
	}

	// box up → private sandbox, gateway URL on the domain
	var b model.Box
	e.must(ag, "POST", "/boxes", map[string]any{"template": "base", "slug": "web", "lifecycle": "session", "ports": []int{8000},
		"requirements": map[string]any{"cpu": 1, "ram_mb": 1024, "disk_gb": 5, "isolation": "sandbox"}}, &b)
	got := waitStatus(t, e.m, b.ID, model.StatusRunning, 20*time.Second)
	h, _ := e.m.Store.GetHost(e.ctx, got.HostID)
	if !h.PrivateTraffic || !fk.private[h.ProviderRef] {
		t.Fatalf("host not private: %+v", h)
	}
	re := regexp.MustCompile(`^https://([0-9a-f]{20}-8000)\.example\.test/$`)
	m := re.FindStringSubmatch(got.URL)
	if m == nil {
		t.Fatalf("box url %q", got.URL)
	}
	host := m[1] + ".example.test"
	var pr manager.PortsResult
	e.must(ag, "GET", "/boxes/"+b.ID+"/ports", nil, &pr)
	pv := pr.Ports[0]
	if pv.Kind != manager.KindGateway || pv.URL != got.URL || pv.GatewayURL != got.URL || !pv.Public || pv.ProviderPreview != "private" || strings.Contains(pv.URL, prev.URL) {
		t.Fatalf("port view %+v", pv)
	}

	// the gateway adds the traffic token upstream (and drops a client's own)
	st, body := hostDo(t, e.srv.URL, host, "/", map[string]string{"E2B-Traffic-Access-Token": "forged"})
	if st != 200 || body != "hello from "+h.ProviderRef {
		t.Fatalf("gateway → %d %q", st, body)
	}
	seenMu.Lock()
	last := seen[len(seen)-1]
	seenMu.Unlock()
	if last != "tat-"+h.ProviderRef {
		t.Fatalf("upstream saw token %q", last)
	}
	// direct access to the preview host is refused
	if res, err := http.Get(prev.URL + "/"); err != nil || res.StatusCode != 403 {
		t.Fatalf("direct preview: %v %v", res, err)
	}
	// token port: no "preview still public" caveat on a private sandbox
	e.must(ag, "PUT", "/boxes/"+b.ID+"/ports/8000", map[string]any{"access": "token"}, &pv)
	if pv.Kind != manager.KindGateway || pv.ProviderPreview != "private" || len(pv.Caveats) != 1 {
		t.Fatalf("token port %+v", pv)
	}
	if st, _ := hostDo(t, e.srv.URL, host, "/", nil); st != 401 {
		t.Fatalf("token port without token → %d", st)
	}
	e.must(ag, "PUT", "/boxes/"+b.ID+"/ports/8000", map[string]any{"access": "public"}, nil)

	// host classification: public URL host = core, reserved / unknown = 404
	if st, body := hostDo(t, e.srv.URL, "gw.example.test", "/v1/health", nil); st != 200 || !strings.Contains(body, `"ok":true`) {
		t.Fatalf("public host → %d %s", st, body)
	}
	for _, hh := range []string{"hark.example.test", "www.example.test", "random.example.test", "0123456789abcdef012-8000.example.test", m[1] + ".other.example.test"} {
		if st, body := hostDo(t, e.srv.URL, hh, "/v1/health", nil); st != 404 || !strings.Contains(body, "NOT_FOUND") {
			t.Fatalf("%s → %d %s", hh, st, body)
		}
	}
	for _, r := range manager.ReservedSubdomains {
		if manager.IsBoxLabel(r) {
			t.Fatalf("reserved %q matches the box label shape", r)
		}
	}

	// capabilities report private_preview
	var caps struct {
		Items []manager.CapabilityRow `json:"items"`
	}
	e.must(e.admin, "GET", "/capabilities", nil, &caps)
	ok := false
	for _, c := range caps.Items {
		ok = ok || c.Provider == "fakepriv" && c.PrivatePreview
	}
	if !ok {
		t.Fatalf("capabilities %+v", caps.Items)
	}

	// settings: invalid domain / mode rejected; provider_preview=public → a new
	// sandbox stays public and its token port warns about the provider URL
	var s model.Settings
	e.must(e.admin, "GET", "/settings", nil, &s)
	s.Gateway.Domain = "not a domain"
	if st, _, _ := e.admin.Do(e.ctx, "PUT", "/settings", s); st != 400 {
		t.Fatalf("bad domain → %d", st)
	}
	s.Gateway.Domain, s.Gateway.ProviderPreview = "", "sometimes"
	if st, _, _ := e.admin.Do(e.ctx, "PUT", "/settings", s); st != 400 {
		t.Fatalf("bad provider_preview → %d", st)
	}
	s.Gateway.ProviderPreview = "public"
	e.must(e.admin, "PUT", "/settings", s, nil)
	var b2 model.Box
	e.must(ag, "POST", "/boxes", map[string]any{"template": "base", "slug": "web2", "lifecycle": "session", "ports": []int{8000},
		"requirements": map[string]any{"cpu": 1, "ram_mb": 1024, "disk_gb": 5, "isolation": "sandbox"}}, &b2)
	got2 := waitStatus(t, e.m, b2.ID, model.StatusRunning, 20*time.Second)
	h2, _ := e.m.Store.GetHost(e.ctx, got2.HostID)
	if h2.PrivateTraffic || !strings.HasPrefix(got2.URL, prev.URL) {
		t.Fatalf("public mode: host %+v url %q", h2, got2.URL)
	}
	e.must(ag, "PUT", "/boxes/"+b2.ID+"/ports/8000", map[string]any{"access": "token"}, &pv)
	if pv.ProviderPreview != "public" || len(pv.Caveats) != 2 || !strings.Contains(pv.Caveats[1], "provider preview URL") {
		t.Fatalf("public-mode token port %+v", pv)
	}

	e.must(ag, "DELETE", "/boxes/"+b.ID, nil, nil)
	e.must(ag, "DELETE", "/boxes/"+b2.ID, nil, nil)
}
