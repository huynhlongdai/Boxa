// Package provider implements the Box Provider Interface (spec v0.3 §3).
// T1 providers (VPS) create hosts that run boxd + Incus; boxd then dials
// out to core, so adapters only map lifecycle verbs.
package provider

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"text/template"
	"time"

	"boxa/core/internal/model"
)

// CreateRequest asks a provider for a new host.
type CreateRequest struct {
	Name      string
	SKU       string
	Region    string
	CloudInit string
	Labels    map[string]string
}

// Handle identifies a created host at the provider.
type Handle struct {
	Ref string `json:"ref"`
	IP  string `json:"ip"`
}

// Caps declares which optional verbs a provider supports.
type Caps struct {
	Sleep    bool `json:"sleep"`
	Snapshot bool `json:"snapshot"`
	Fork     bool `json:"fork"`
}

// Provider is the BPI. Optional verbs are separate interfaces below.
type Provider interface {
	ID() string
	Name() string
	Tier() string
	Implemented() bool
	Caps() Caps
	Validate(ctx context.Context, token string) error
	Offers(ctx context.Context, token string) ([]model.Offer, error)
	Create(ctx context.Context, token string, req CreateRequest) (Handle, error)
	Destroy(ctx context.Context, token, ref string) error
	Status(ctx context.Context, token, ref string) (string, error) // provisioning|ready|down|unknown
}

// Sleeper is implemented by providers that can power hosts off/on.
type Sleeper interface {
	Sleep(ctx context.Context, token, ref string) error
	Wake(ctx context.Context, token, ref string) error
}

// Snapshotter is implemented by providers with host-level snapshots.
type Snapshotter interface {
	Snapshot(ctx context.Context, token, ref, name string) (string, error)
}

// Quote estimates the cost of running an offer for `hours` (spec M4):
// price_hour × max(hours, min_billing) + setup.
func Quote(o model.Offer, hours, setupUSD float64) float64 {
	minH := float64(o.MinBillingMinutes) / 60
	h := math.Max(hours, minH)
	cost := o.PriceHour * h
	if o.PriceMonth > 0 && cost > o.PriceMonth && hours <= 730 {
		cost = o.PriceMonth // monthly cap (Hetzner/Vultr bill hourly up to the monthly price)
	}
	return cost + setupUSD
}

// Registry of known providers.
type Registry struct {
	m     map[string]Provider
	order []string
}

func NewRegistry(ps ...Provider) *Registry {
	r := &Registry{m: map[string]Provider{}}
	for _, p := range ps {
		r.m[p.ID()] = p
		r.order = append(r.order, p.ID())
	}
	return r
}

func (r *Registry) Get(id string) (Provider, bool) { p, ok := r.m[id]; return p, ok }
func (r *Registry) All() []Provider {
	out := make([]Provider, 0, len(r.order))
	for _, id := range r.order {
		out = append(out, r.m[id])
	}
	return out
}

// Default returns the standard registry. Unimplemented providers appear in
// the UI (with offers from the price feed) but cannot create hosts yet.
func Default(httpc *http.Client) *Registry {
	if httpc == nil {
		httpc = &http.Client{Timeout: 30 * time.Second}
	}
	return NewRegistry(
		&Local{},
		NewHetzner(httpc),
		NewVultr(httpc),
		&Stub{id: "digitalocean", name: "DigitalOcean"},
		&Stub{id: "contabo", name: "Contabo"},
		&Stub{id: "netcup", name: "netcup"},
	)
}

// ---- local ----

// Local is the provider for hosts that register themselves (dev machine, or
// the single Hetzner box from deploy/install.sh). It never creates servers.
type Local struct{}

func (*Local) ID() string                                    { return "local" }
func (*Local) Name() string                                  { return "Local / self-registered" }
func (*Local) Tier() string                                  { return "T1" }
func (*Local) Implemented() bool                             { return true }
func (*Local) Caps() Caps                                    { return Caps{Sleep: false, Snapshot: false} }
func (*Local) Validate(ctx context.Context, token string) error { return nil }
func (*Local) Offers(ctx context.Context, token string) ([]model.Offer, error) { return nil, nil }
func (*Local) Create(ctx context.Context, token string, req CreateRequest) (Handle, error) {
	return Handle{}, model.EH(model.ErrInvalid, "Run boxd with BOXD_TOKEN=<bootstrap token> on the machine; it registers itself.", "local hosts cannot be created by core")
}
func (*Local) Destroy(ctx context.Context, token, ref string) error { return nil }
func (*Local) Status(ctx context.Context, token, ref string) (string, error) { return "unknown", nil }

// ---- stub ----

// Stub is a provider known to the price feed but without an adapter yet (P3).
type Stub struct{ id, name string }

func (s *Stub) ID() string        { return s.id }
func (s *Stub) Name() string      { return s.name }
func (s *Stub) Tier() string      { return "T1" }
func (s *Stub) Implemented() bool { return false }
func (s *Stub) Caps() Caps        { return Caps{} }
func (s *Stub) err() error {
	return model.EH(model.ErrProviderFailed, "This provider adapter ships in P3; use hetzner or vultr for now.", "%s adapter not implemented yet", s.name)
}
func (s *Stub) Validate(ctx context.Context, token string) error                { return s.err() }
func (s *Stub) Offers(ctx context.Context, token string) ([]model.Offer, error) { return nil, s.err() }
func (s *Stub) Create(ctx context.Context, token string, req CreateRequest) (Handle, error) {
	return Handle{}, s.err()
}
func (s *Stub) Destroy(ctx context.Context, token, ref string) error           { return s.err() }
func (s *Stub) Status(ctx context.Context, token, ref string) (string, error) { return "unknown", s.err() }

// ---- shared HTTP helper ----

type apiErr struct {
	Status int
	Body   string
}

func (e *apiErr) Error() string { return fmt.Sprintf("HTTP %d: %s", e.Status, e.Body) }

func doJSON(ctx context.Context, c *http.Client, method, url, token string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "boxa-core/0.1")
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode >= 300 {
		s := string(b)
		if len(s) > 500 {
			s = s[:500]
		}
		return &apiErr{Status: resp.StatusCode, Body: strings.TrimSpace(s)}
	}
	if out != nil && len(b) > 0 {
		return json.Unmarshal(b, out)
	}
	return nil
}

func authErr(err error, provider string) error {
	if ae, ok := err.(*apiErr); ok && (ae.Status == 401 || ae.Status == 403) {
		return model.EH(model.ErrUnauthorized, "Create a read+write API token in the "+provider+" console and paste it again.", "%s rejected the token (%d)", provider, ae.Status)
	}
	return model.EH(model.ErrProviderFailed, "Check network access to the provider API and retry.", "%s API error: %v", provider, err)
}

// ---- cloud-init ----

//go:embed cloudinit.yaml.tmpl
var cloudInitTmpl string

// CloudInitParams fills the host bootstrap template.
type CloudInitParams struct {
	CoreURL        string // https://core.example (no trailing slash)
	BootstrapToken string // one-time token, exchanged for a host token on first connect
	Provider       string
	Region         string
	BoxdURL        string // where to download the boxd binary
}

// RenderCloudInit renders the user-data for a new T1 host.
func RenderCloudInit(p CloudInitParams) (string, error) {
	t, err := template.New("ci").Parse(cloudInitTmpl)
	if err != nil {
		return "", err
	}
	var b bytes.Buffer
	err = t.Execute(&b, p)
	return b.String(), err
}
