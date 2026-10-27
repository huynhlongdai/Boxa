package sandbox

import (
	"context"
	"net/http"
	"strings"
	"time"

	"boxaz/core/internal/model"
	"boxaz/core/internal/provider"
)

// Provider is a provider.Provider backed by a sandbox Driver.
type Provider interface {
	provider.Provider
	Driver() Driver
	Info() Info
	TokenFromEnv(getenv func(string) string) string
}

// adapter wraps a Driver as a provider.Provider (+ Regioner, Sleeper,
// Snapshotter, Lister when the driver supports them).
type adapter struct {
	d    Driver
	info Info
	// WaitTimeout bounds create→running (tests shorten it).
	WaitTimeout time.Duration
}

// AsProvider wraps a driver.
func AsProvider(d Driver) Provider {
	return &adapter{d: d, info: d.Info(), WaitTimeout: 3 * time.Minute}
}

func (a *adapter) Driver() Driver    { return a.d }
func (a *adapter) Info() Info        { return a.info }
func (a *adapter) ID() string        { return a.info.ID }
func (a *adapter) Name() string      { return a.info.Name }
func (a *adapter) Tier() string      { return orStr(a.info.Tier, "T2") }
func (a *adapter) Implemented() bool { return true }
func (a *adapter) Caps() provider.Caps {
	return provider.Caps{Sleep: a.info.Caps.Pause, Snapshot: a.info.Caps.Snapshot, Fork: a.info.Caps.Fork}
}

func (a *adapter) Validate(ctx context.Context, token string) error {
	if strings.TrimSpace(token) == "" {
		return model.EH(model.ErrInvalid, "Paste the API key from the "+a.info.Name+" console.", "token is required")
	}
	c := ParseCred(token)
	for _, f := range a.info.Credentials {
		if !f.Optional && f.Default == "" && strings.TrimSpace(c[f.Name]) == "" {
			return model.EH(model.ErrInvalid, "Fill in every required field of the "+a.info.Name+" credential.", "%s: %s is required", a.info.Name, f.Label)
		}
	}
	return wrapErr(a.info.Name, a.d.Validate(ctx, c))
}

// Offers are the catalog sizes priced from the documented per-second rates
// (no API call: sandbox providers bill by the shape, not by a SKU list).
func (a *adapter) Offers(ctx context.Context, token string) ([]model.Offer, error) {
	info := a.info
	if s, ok := a.d.(Sizer); ok && token != "" {
		info.Sizes = s.SizesFor(ParseCred(token))
	}
	return StaticOffers(info, a.info.ID+"-pricing", time.Now().UTC()), nil
}

// Sizer derives the size list from the credential (E2B/Novita templates).
type Sizer interface {
	SizesFor(c Cred) []Size
}

// StaticOffers turns sizes × regions into offers.
func StaticOffers(info Info, source string, now time.Time) []model.Offer {
	conf := "verified"
	if info.Pricing.Confidence != "docs" {
		conf = "estimated"
	}
	regions := info.Regions
	if len(regions) == 0 {
		regions = []Region{{ID: "global", Name: "Global"}}
	}
	var out []model.Offer
	for _, r := range regions {
		for _, z := range info.Sizes {
			ph := info.Pricing.PerHour(z.CPU, z.RAMMB, z.DiskGB)
			out = append(out, model.Offer{Provider: info.ID, SKU: z.SKU, Region: r.ID, CPU: z.CPU, RAMMB: z.RAMMB, DiskGB: z.DiskGB,
				PriceHour: ph, PriceMonth: model.Round4(ph * 730), MinBillingMinutes: 0, Source: source, FetchedAt: now, Confidence: conf})
		}
	}
	return out
}

func (a *adapter) Regions(ctx context.Context, token string) ([]provider.Region, error) {
	out := []provider.Region{}
	for _, r := range a.info.Regions {
		out = append(out, provider.Region{ID: r.ID, Name: r.Name, Available: true})
	}
	return out, nil
}

// Create starts a sandbox, waits until it runs and (T2) runs the bootstrap
// that starts `boxd --dial`. A failed bootstrap deletes the sandbox.
func (a *adapter) Create(ctx context.Context, token string, req provider.CreateRequest) (provider.Handle, error) {
	c := ParseCred(token)
	info := a.info
	if s, ok := a.d.(Sizer); ok {
		info.Sizes = s.SizesFor(c)
	}
	z := pickSize(info, Spec{SKU: req.SKU})
	spec := Spec{Name: req.Name, SKU: z.SKU, CPU: z.CPU, RAMMB: z.RAMMB, DiskGB: z.DiskGB, Region: req.Region, Labels: req.Labels, Template: a.info.DefaultTemplate}
	if g, ok := a.d.(TrafficGuard); ok && req.PrivateTraffic && g.PrivateTrafficSupported() {
		spec.PrivateTraffic = true
	}
	if spec.Region == "global" {
		spec.Region = ""
	}
	sb, err := a.d.Create(ctx, c, spec)
	if err != nil {
		return provider.Handle{}, wrapErr(a.info.Name, err)
	}
	cleanup := func() {
		dctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		_ = a.d.Delete(dctx, c, sb.ID)
	}
	if sb.State != StateRunning {
		if _, err := WaitRunning(ctx, a.d, c, sb.ID, a.WaitTimeout); err != nil {
			cleanup()
			return provider.Handle{}, wrapErr(a.info.Name, err)
		}
	}
	if req.Bootstrap != "" {
		res, err := a.d.Exec(ctx, c, sb.ID, ExecRequest{Command: req.Bootstrap, TimeoutS: 180})
		if err == nil && res.ExitCode != 0 {
			err = model.EH(model.ErrProviderFailed, "The sandbox could not download or start boxd; check BOXAZ_PUBLIC_URL / BOXAZ_BOXD_URL are reachable from the internet.", "%s bootstrap exit %d: %s", a.info.Name, res.ExitCode, trim(res.Stderr+res.Stdout, 300))
		}
		if err != nil {
			cleanup()
			return provider.Handle{}, wrapErr(a.info.Name, err)
		}
	}
	hd := provider.Handle{Ref: sb.ID}
	if sb.Meta[MetaPrivateTraffic] == "1" {
		hd.Meta = map[string]string{MetaPrivateTraffic: "1"}
	}
	return hd, nil
}

func (a *adapter) Destroy(ctx context.Context, token, ref string) error {
	err := a.d.Delete(ctx, ParseCred(token), ref)
	if he, ok := err.(*HTTPError); ok && (he.Status == 404 || he.Status == 410) {
		return nil // already gone
	}
	return wrapErr(a.info.Name, err)
}

func (a *adapter) Status(ctx context.Context, token, ref string) (string, error) {
	sb, err := a.d.Get(ctx, ParseCred(token), ref)
	if err != nil {
		return "unknown", wrapErr(a.info.Name, err)
	}
	switch sb.State {
	case StateRunning:
		return "ready", nil
	case StateStarting:
		return "provisioning", nil
	case StatePaused, StateStopped, StateError, StateDeleted:
		return "down", nil
	}
	return "unknown", nil
}

func (a *adapter) Sleep(ctx context.Context, token, ref string) error {
	if p, ok := a.d.(Pauser); ok {
		return wrapErr(a.info.Name, p.Pause(ctx, ParseCred(token), ref))
	}
	if s, ok := a.d.(Starter); ok {
		return wrapErr(a.info.Name, s.Stop(ctx, ParseCred(token), ref))
	}
	return model.EH(model.ErrInvalid, "This provider cannot pause sandboxes.", "%s has no pause", a.info.Name)
}

func (a *adapter) Wake(ctx context.Context, token, ref string) error {
	if p, ok := a.d.(Pauser); ok {
		return wrapErr(a.info.Name, p.Resume(ctx, ParseCred(token), ref))
	}
	if s, ok := a.d.(Starter); ok {
		return wrapErr(a.info.Name, s.Start(ctx, ParseCred(token), ref))
	}
	return model.EH(model.ErrInvalid, "This provider cannot resume sandboxes.", "%s has no resume", a.info.Name)
}

func (a *adapter) Snapshot(ctx context.Context, token, ref, name string) (string, error) {
	if s, ok := a.d.(Snapshotter); ok {
		id, err := s.Snapshot(ctx, ParseCred(token), ref, name)
		return id, wrapErr(a.info.Name, err)
	}
	return "", model.EH(model.ErrInvalid, "This provider has no snapshots.", "%s has no snapshot API", a.info.Name)
}

func (a *adapter) List(ctx context.Context, token string) ([]provider.Server, error) {
	l, ok := a.d.(Lister)
	if !ok {
		return []provider.Server{}, nil
	}
	sbs, err := l.List(ctx, ParseCred(token))
	if err != nil {
		return nil, wrapErr(a.info.Name, err)
	}
	out := []provider.Server{}
	for _, s := range sbs {
		st := map[string]string{StateRunning: "ready", StateStarting: "provisioning"}[s.State]
		if st == "" {
			st = "down"
		}
		out = append(out, provider.Server{Ref: s.ID, Name: s.ID, Region: s.Region, SKU: s.Template, Status: st})
	}
	return out, nil
}

// TokenFromEnv joins the provider's env vars into a credential ("" when the
// first, required one is missing).
func (a *adapter) TokenFromEnv(getenv func(string) string) string {
	if len(a.info.EnvVars) == 0 {
		return ""
	}
	c := Cred{}
	for i, k := range a.info.EnvVars {
		v := strings.TrimSpace(getenv(k))
		if v == "" {
			if i == 0 {
				return ""
			}
			continue
		}
		name := "token"
		if i < len(a.info.Credentials) {
			name = a.info.Credentials[i].Name
		}
		c[name] = v
	}
	return c.String()
}

func orStr(s, d string) string {
	if strings.TrimSpace(s) == "" {
		return d
	}
	return s
}

// Drivers returns every built-in sandbox driver (native + YAML).
func Drivers(httpc *http.Client) []Driver {
	ds := []Driver{NewE2B(httpc, ""), NewNovita(httpc, ""), NewDaytona(httpc, ""), NewPrized(httpc, ""), NewBoxdSh(httpc, "")}
	ys, _ := BuiltinYAML(httpc)
	for _, y := range ys {
		ds = append(ds, y.Wrap())
	}
	return ds
}

// Providers wraps every built-in driver.
func Providers(httpc *http.Client) []provider.Provider {
	var out []provider.Provider
	for _, d := range Drivers(httpc) {
		out = append(out, AsProvider(d))
	}
	return out
}

// DefaultRegistry is provider.Default plus every sandbox provider (v1.4).
func DefaultRegistry(httpc *http.Client) *provider.Registry {
	if httpc == nil {
		httpc = &http.Client{Timeout: 60 * time.Second}
	}
	r := provider.Default(httpc)
	r.Add(Providers(httpc)...)
	return r
}

// DefaultChain is the sandbox part of the fallback chain (after the T1
// chain): cheapest documented per-second compute first.
var DefaultChain = []string{"e2b", "daytona", "novita", "prized", "boxdsh", "runloop", "blaxel", "boat", "agent37", "vercel", "upstash-box"}
