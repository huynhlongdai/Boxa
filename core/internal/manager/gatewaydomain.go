package manager

// v1.8 gateway on a real domain: host-based box URLs <key>-<port>.<domain>
// behind a wildcard DNS record (e.g. a Cloudflare Tunnel), a reserved
// subdomain list so box labels can never shadow other services of the zone,
// and private provider previews (the provider refuses traffic that does not
// come through the gateway).

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"boxaz/core/internal/model"
	"boxaz/core/internal/sandbox"
)

// ReservedSubdomains can never be box labels at the gateway domain (and are
// never routed to a box): service names commonly given explicit DNS records.
// BOXAZ_GATEWAY_RESERVED adds more (comma separated).
var ReservedSubdomains = []string{
	"admin", "api", "app", "assets", "auth", "autoconfig", "autodiscover", "blog", "cdn", "console", "core", "dashboard",
	"cdp", "dl", "docs", "ftp", "git", "grafana", "gw", "hark", "help", "ide", "imap", "login", "mail", "mcp", "metrics", "mx", "ns", "ns1",
	"ns2", "pop", "pop3", "portal", "smtp", "ssh", "sso", "static", "status", "support", "tcp", "term", "vnc", "webmail", "ws", "www",
}

// boxLabelRE is the only label shape the gateway routes to a box:
// <20-hex HMAC key>-<port>. No reserved name (letters outside a-f, no
// "-<digits>" suffix) can match it.
var boxLabelRE = regexp.MustCompile(`^[0-9a-f]{20}-[0-9]{1,5}$`)

// IsBoxLabel reports whether a DNS label is a legacy box gateway label
// (<key>-<port>, kept as an alias since v1.9).
func IsBoxLabel(label string) bool { return boxLabelRE.MatchString(label) }

// nameLabelRE is the shape of a readable gateway label: <name> or
// <name>-<label> (one DNS label, at most 63 characters).
var nameLabelRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// IsNameLabel reports whether label can be a readable box address.
func IsNameLabel(label string) bool {
	return nameLabelRE.MatchString(label) && !strings.Contains(label, "--")
}

// IsReservedSubdomain reports whether label is reserved at the gateway domain.
func IsReservedSubdomain(label string) bool {
	label = strings.ToLower(strings.TrimSpace(label))
	for _, r := range reservedList() {
		if r == label {
			return true
		}
	}
	return false
}

func reservedList() []string {
	out := append([]string{}, ReservedSubdomains...)
	for _, x := range strings.Split(os.Getenv("BOXAZ_GATEWAY_RESERVED"), ",") {
		if x = strings.ToLower(strings.TrimSpace(x)); x != "" {
			out = append(out, x)
		}
	}
	sort.Strings(out)
	return out
}

// GatewayDomain is the effective host-based gateway domain: env
// BOXAZ_GATEWAY_DOMAIN, else settings.gateway.domain, else "".
func (m *Manager) GatewayDomain(ctx context.Context) string {
	if d := normDomain(m.Cfg.GatewayDomain); d != "" {
		return d
	}
	return normDomain(m.Settings(ctx).Gateway.Domain)
}

var domainRE = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z][a-z0-9-]{0,61}[a-z0-9]$`)

func validDomain(d string) bool { return len(d) <= 200 && domainRE.MatchString(d) }

func normDomain(d string) string {
	return strings.Trim(strings.ToLower(strings.TrimSpace(d)), ".")
}

// GatewayHost classifies a request host against the gateway domain:
// "box" (seg = "<key>-<port>"), "core" (the BOXAZ_PUBLIC_URL host or the bare
// domain: API + dashboard), "reserved" / "other" (a subdomain that is not a
// box label) or "" (not under the gateway domain).
func (m *Manager) GatewayHost(ctx context.Context, host string) (kind, seg string) {
	d := m.GatewayDomain(ctx)
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if d == "" {
		return "", ""
	}
	if pu, err := url.Parse(m.Cfg.PublicURL); err == nil && pu.Hostname() != "" && strings.EqualFold(pu.Hostname(), host) {
		return "core", ""
	}
	if host == d { // v1.11: the bare gateway domain is not a control origin (only BOXAZ_PUBLIC_URL's host is)
		return "reserved", ""
	}
	if !strings.HasSuffix(host, "."+d) {
		return "", ""
	}
	label := strings.TrimSuffix(host, "."+d)
	switch {
	case IsBoxLabel(label):
		return "box", label
	case IsReservedSubdomain(label):
		return "reserved", ""
	case IsNameLabel(label):
		return "box", label // v1.9 readable <name> / <name>-<label>; 404 when no box has it
	}
	return "other", ""
}

// ProviderPreviewMode is the effective provider-preview policy (auto |
// private | public): env BOXAZ_PROVIDER_PREVIEW, else settings, else auto.
func (m *Manager) ProviderPreviewMode(ctx context.Context) string {
	v := strings.ToLower(strings.TrimSpace(m.Cfg.ProviderPreview))
	if v == "" {
		v = strings.ToLower(strings.TrimSpace(m.Settings(ctx).Gateway.ProviderPreview))
	}
	switch v {
	case "private", "public":
		return v
	}
	return "auto"
}

// gatewayInternetFacing: the gateway URLs work from the internet (a gateway
// domain, or an internet-facing BOXAZ_PUBLIC_URL).
func (m *Manager) gatewayInternetFacing(ctx context.Context) bool {
	if ok, _ := CoreLooksPublic(m.Cfg.PublicURL); ok || m.Cfg.AssumePublicURL && m.Cfg.PublicURL != "" {
		return true
	}
	return m.GatewayDomain(ctx) != "" && m.Cfg.PublicURL != ""
}

// wantPrivatePreview decides whether a new sandbox should refuse public
// preview traffic: "private" always, "auto" when the gateway is reachable from
// the internet (otherwise nobody could reach the app at all).
func (m *Manager) wantPrivatePreview(ctx context.Context) bool {
	switch m.ProviderPreviewMode(ctx) {
	case "private":
		return true
	case "public":
		return false
	}
	return m.gatewayInternetFacing(ctx)
}

// privatePreviewSupported reports whether a sandbox provider can refuse
// public preview traffic (implements sandbox.TrafficGuard and says so).
func (m *Manager) privatePreviewSupported(provider string) bool {
	sp, ok := m.sandboxProv(provider)
	if !ok {
		return false
	}
	g, ok := sp.Driver().(sandbox.TrafficGuard)
	return ok && g.PrivateTrafficSupported()
}

// previewHeaders returns the headers the gateway adds when proxying to a
// private preview host (E2B-Traffic-Access-Token).
func (m *Manager) previewHeaders(ctx context.Context, h *model.Host) (map[string]string, error) {
	if h == nil || !h.PrivateTraffic {
		return nil, nil
	}
	sp, ok := m.sandboxProv(h.Provider)
	if !ok {
		return nil, model.E(model.ErrProviderFailed, "provider %s is not configured", h.Provider)
	}
	g, ok := sp.Driver().(sandbox.TrafficGuard)
	if !ok {
		return nil, nil
	}
	tok, err := m.providerToken(ctx, h.Provider)
	if err != nil {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return g.PreviewHeaders(cctx, sandbox.ParseCred(tok), h.ProviderRef)
}

// GatewayInfo is GET /v1/gateway: how box URLs are formed and whether the
// gateway answers from the internet.
type GatewayInfo struct {
	Mode         string `json:"mode"` // host | path | relative
	Domain       string `json:"domain"`
	DomainSource string `json:"domain_source"` // env | settings | none
	PublicURL    string `json:"public_url"`
	URLPattern   string `json:"url_pattern"`
	// v1.9: other ports by label, and the legacy <key>-<port> alias
	LabelPattern    string            `json:"label_pattern"`
	AliasPattern    string            `json:"alias_pattern"`
	InternetFacing  bool              `json:"internet_facing"`
	Reserved        []string          `json:"reserved"`
	ProviderPreview string            `json:"provider_preview"`        // effective mode
	PreviewSource   string            `json:"provider_preview_source"` // env | settings | default
	PrivatePreviews bool              `json:"private_previews"`        // new sandboxes are created private
	Providers       []GatewayProvider `json:"providers"`
	Check           *GatewayCheck     `json:"check,omitempty"`
}

// GatewayProvider says how one sandbox provider's preview host is treated.
type GatewayProvider struct {
	Provider       string `json:"provider"`
	PreviewHost    bool   `json:"preview_host"`    // the provider has its own public preview URLs
	PrivatePreview bool   `json:"private_preview"` // it can refuse traffic that bypasses the gateway
	Note           string `json:"note"`
}

// GatewayCheck is a self-check of the public URL (GET <public>/v1/health).
type GatewayCheck struct {
	OK        bool      `json:"ok"`
	Status    int       `json:"status,omitempty"`
	LatencyMS int64     `json:"latency_ms"`
	Error     string    `json:"error,omitempty"`
	CheckedAt time.Time `json:"checked_at"`
}

var gwCheckCache struct {
	sync.Mutex
	url string
	c   *GatewayCheck
}

// Gateway describes the gateway; check=true probes BOXAZ_PUBLIC_URL (cached 60 s).
func (m *Manager) Gateway(ctx context.Context, check bool) *GatewayInfo {
	s := m.Settings(ctx)
	g := &GatewayInfo{Domain: m.GatewayDomain(ctx), PublicURL: m.Cfg.PublicURL, Reserved: reservedList(), ProviderPreview: m.ProviderPreviewMode(ctx)}
	switch {
	case normDomain(m.Cfg.GatewayDomain) != "":
		g.DomainSource = "env"
	case normDomain(s.Gateway.Domain) != "":
		g.DomainSource = "settings"
	default:
		g.DomainSource = "none"
	}
	switch {
	case strings.TrimSpace(m.Cfg.ProviderPreview) != "":
		g.PreviewSource = "env"
	case strings.TrimSpace(s.Gateway.ProviderPreview) != "":
		g.PreviewSource = "settings"
	default:
		g.PreviewSource = "default"
	}
	switch {
	case g.Domain != "":
		g.Mode, g.URLPattern = "host", "https://<name>."+g.Domain+"/"
		g.LabelPattern, g.AliasPattern = "https://<name>-<label>."+g.Domain+"/", "https://<key>-<port>."+g.Domain+"/"
	case m.Cfg.PublicURL != "":
		pu := strings.TrimRight(m.Cfg.PublicURL, "/")
		g.Mode, g.URLPattern = "path", pu+"/x/<name>/"
		g.LabelPattern, g.AliasPattern = pu+"/x/<name>-<label>/", pu+"/x/<key>-<port>/"
	default:
		g.Mode, g.URLPattern = "relative", "/x/<name>/"
		g.LabelPattern, g.AliasPattern = "/x/<name>-<label>/", "/x/<key>-<port>/"
	}
	g.InternetFacing = m.gatewayInternetFacing(ctx)
	g.PrivatePreviews = m.wantPrivatePreview(ctx)
	for _, sp := range m.gatewayProviders(ctx) {
		info := sp.Info()
		gp := GatewayProvider{Provider: info.ID, PreviewHost: info.Caps.Ports}
		if g2, ok := sp.Driver().(sandbox.TrafficGuard); ok && g2.PrivateTrafficSupported() {
			gp.PrivatePreview = true
		}
		switch {
		case !gp.PreviewHost:
			gp.Note = "no provider preview host: traffic reaches the box only through the Boxaz gateway (boxd / ingress agent)"
		case gp.PrivatePreview:
			gp.Note = "preview host refuses requests without the sandbox traffic token when the sandbox is created private; the gateway adds it upstream"
		default:
			gp.Note = "preview host is public at the provider: anyone with the sandbox id can reach a port directly, even when its Boxaz URL needs a token"
		}
		g.Providers = append(g.Providers, gp)
	}
	sort.Slice(g.Providers, func(i, j int) bool { return g.Providers[i].Provider < g.Providers[j].Provider })
	if check && m.Cfg.PublicURL != "" {
		g.Check = m.gatewayCheck(ctx)
	}
	return g
}

func (m *Manager) gatewayCheck(ctx context.Context) *GatewayCheck {
	u := strings.TrimRight(m.Cfg.PublicURL, "/") + "/v1/health"
	gwCheckCache.Lock()
	defer gwCheckCache.Unlock()
	if c := gwCheckCache.c; c != nil && gwCheckCache.url == u && time.Since(c.CheckedAt) < time.Minute {
		return c
	}
	c := &GatewayCheck{CheckedAt: time.Now().UTC()}
	cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(cctx, http.MethodGet, u, nil)
	t0 := time.Now()
	res, err := http.DefaultClient.Do(req)
	c.LatencyMS = time.Since(t0).Milliseconds()
	if err != nil {
		c.Error = err.Error()
	} else {
		res.Body.Close()
		c.Status, c.OK = res.StatusCode, res.StatusCode == http.StatusOK
	}
	gwCheckCache.url, gwCheckCache.c = u, c
	return c
}

// gatewayProviders: the native preview-capable sandbox providers plus every
// connected sandbox provider.
func (m *Manager) gatewayProviders(ctx context.Context) []sandbox.Provider {
	featured := map[string]bool{"e2b": true, "novita": true, "prized": true, "daytona": true}
	var out []sandbox.Provider
	for _, p := range m.Providers.All() {
		sp, ok := p.(sandbox.Provider)
		if !ok {
			continue
		}
		if featured[sp.Info().ID] {
			out = append(out, sp)
			continue
		}
		if _, err := m.providerToken(ctx, sp.Info().ID); err == nil {
			out = append(out, sp)
		}
	}
	return out
}
