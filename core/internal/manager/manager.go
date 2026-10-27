// Package manager is Boxaz core's business logic: box lifecycle (M3),
// scheduler (M4), provider orchestration (M5), reaper (M11), budget/ledger
// (M12), price feed (M13), secrets (M10), templates (M14), audit (M15).
package manager

import (
	"boxaz/core/internal/i18n"
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"boxaz/core/internal/boxyaml"
	"boxaz/core/internal/events"
	"boxaz/core/internal/hostlink"
	"boxaz/core/internal/model"
	"boxaz/core/internal/objstore"
	"boxaz/core/internal/pricefeed"
	"boxaz/core/internal/proto"
	"boxaz/core/internal/provhealth"
	"boxaz/core/internal/provider"
	"boxaz/core/internal/secret"
	"boxaz/core/internal/store"
)

const Version = "0.1.0"

// Config is read from env by cmd/boxaz-core.
type Config struct {
	PublicURL     string // external base URL of core, e.g. https://core-1-2-3-4.sslip.io
	DisableTunnel bool   // BOXAZ_DISABLE_TUNNEL: no in-box quick-tunnel fallback for public URLs
	// AssumePublicURL (BOXAZ_INGRESS_ASSUME_PUBLIC) tries the ingress fallback
	// even when PublicURL looks private (e.g. sandboxes reach it over a VPN).
	AssumePublicURL bool
	// PublicURLProbe checks a fresh tunnel URL from core (nil = real HTTP GET; tests stub it).
	PublicURLProbe     func(ctx context.Context, url string) bool
	BaseDomain         string // overrides settings.base_domain when set
	BootstrapToken     string // shared token letting a boxd self-register (install.sh / dev)
	AutoBurst          bool   // allow scheduler to rent hourly hosts automatically
	LocalPriceMonth    float64
	LedgerInterval     time.Duration
	ReaperInterval     time.Duration
	PriceFeedInterval  time.Duration
	PriceFeedURL       string
	PriceFeedAllowlist []string
	DisablePriceFeed   bool
	BoxdDownloadURL    string // base URL for boxd binaries used in cloud-init
	SetupCostUSD       float64
	OrphanAfter        time.Duration
	BurstEmptyAfter    time.Duration
	HostDownAfter      time.Duration
	CreateWait         time.Duration
	URLKey             string           // HMAC key for box URL tokens (defaults to a random per-process key)
	ProviderChain      []string         // burst fallback order (default hetzner, vultr, digitalocean, contabo, netcup, then the sandbox chain)
	AutoSandbox        bool             // v1.4: box_up may create sandboxes without approval (BOXAZ_AUTO_SANDBOX)
	SandboxMode        string           // v1.4: auto | dial (T2) | shim (T3) default for sandbox providers
	SandboxEmptyAfter  time.Duration    // v1.4: destroy an empty sandbox host after this (default 1 min)
	SnapshotDir        string           // T3 sandbox snapshots (gateway tarballs); default $BOXAZ_DATA_DIR/snapshots
	ObjectStore        *objstore.Config // snapshot storage from env (nil = settings/local)
	TelegramAPI        string           // override https://api.telegram.org (tests)
	// v1.3
	HealthInterval    time.Duration // provider health probes (default 60s)
	CatalogStaleAfter time.Duration // offers older than this are flagged stale (default 24h)
	WarmInterval      time.Duration // warm pool refill loop (default 10s)
	Placement         string        // binpack (default) | spread
	ClientLocation    string        // default agent location for latency estimates (region id, ISO country, lat,lon)
	Mesh              MeshConfig    // optional headscale mesh
	HostCacheIP       string        // BOXD_CACHE_HOST written into new hosts' cloud-init (deploy/caches.sh on each host)
}

func (c *Config) defaults() {
	if c.SandboxEmptyAfter == 0 {
		c.SandboxEmptyAfter = time.Minute
	}
	if c.LedgerInterval == 0 {
		c.LedgerInterval = time.Minute
	}
	if c.ReaperInterval == 0 {
		c.ReaperInterval = 30 * time.Second
	}
	if c.PriceFeedInterval == 0 {
		c.PriceFeedInterval = 6 * time.Hour
	}
	if c.OrphanAfter == 0 {
		c.OrphanAfter = 24 * time.Hour
	}
	if c.BurstEmptyAfter == 0 {
		c.BurstEmptyAfter = 10 * time.Minute
	}
	if c.HostDownAfter == 0 {
		c.HostDownAfter = 2 * time.Minute
	}
	if c.CreateWait == 0 {
		c.CreateWait = 120 * time.Second
	}
	if c.HealthInterval == 0 {
		c.HealthInterval = time.Minute
	}
	if c.CatalogStaleAfter == 0 {
		c.CatalogStaleAfter = 24 * time.Hour
	}
	if c.WarmInterval == 0 {
		c.WarmInterval = 10 * time.Second
	}
	if c.Placement == "" {
		c.Placement = "binpack"
	}
	c.PublicURL = strings.TrimRight(c.PublicURL, "/")
	if c.URLKey == "" {
		c.URLKey = secret.RandHex(16)
	}
}

// SysJob tracks a background loop for /admin/jobs and /health.
type SysJob struct {
	Name       string     `json:"name"`
	IntervalS  float64    `json:"interval_s"`
	LastRunAt  *time.Time `json:"last_run_at"`
	LastStatus string     `json:"last_status"`
	LastError  string     `json:"last_error"`
	DurationMS int64      `json:"duration_ms"`
	Runs       int        `json:"runs"`
}

type Manager struct {
	Store     store.Store
	Hub       *hostlink.Hub
	Providers *provider.Registry
	Crypt     *secret.Box
	Broker    *events.Broker
	Feed      *pricefeed.Fetcher
	Cfg       Config

	mu            sync.Mutex
	sysjobs       map[string]*SysJob
	lastTick      time.Time
	lastRetention time.Time
	rate          map[string]*bucket
	boxLocks      sync.Map     // box id → *sync.Mutex
	ing           ingressState // public-URL fallback: in-box ingress agents
	startedAt     time.Time
	ctx           context.Context
	objHTTP       *http.Client // object storage client (tests inject the fake's client)
	jobCancel     sync.Map     // job id → context.CancelFunc (A2A tasks/cancel)
	notif         notifier
	// v1.3
	Health  *provhealth.Tracker
	catMu   sync.Mutex
	catalog *Catalog
	catVer  int
	warm    warmState
	usage   usageState
	obsMu   sync.Mutex
	lastObs map[string]*proto.ObserveResult
	etaMu   sync.Mutex
	etas    map[string][]float64 // "<host>|<template>" → recent cold create seconds
}

// SetObjectStoreHTTP overrides the HTTP client used for object storage (tests).
func (m *Manager) SetObjectStoreHTTP(c *http.Client) { m.objHTTP = c }

func New(st store.Store, reg *provider.Registry, crypt *secret.Box, cfg Config) *Manager {
	cfg.defaults()
	m := &Manager{
		Store: st, Providers: reg, Crypt: crypt, Broker: events.NewBroker(), Cfg: cfg,
		sysjobs: map[string]*SysJob{}, rate: map[string]*bucket{}, startedAt: time.Now(),
		Feed:    &pricefeed.Fetcher{BaseURL: cfg.PriceFeedURL, Allowlist: cfg.PriceFeedAllowlist},
		ctx:     context.Background(),
		Health:  provhealth.New(24*time.Hour, provhealth.BreakerConfig{}),
		lastObs: map[string]*proto.ObserveResult{}, etas: map[string][]float64{},
	}
	m.Hub = hostlink.NewHub(m)
	return m
}

// Init seeds providers/templates, recovers interrupted state, bootstraps the admin key.
// It returns a freshly generated admin key (to print once) or "".
func (m *Manager) Init(ctx context.Context, envAdminKey string) (string, error) {
	for _, p := range m.Providers.All() {
		if _, err := m.Store.GetProvider(ctx, p.ID()); errors.Is(err, model.ErrNoRows) {
			mp := &model.Provider{ID: p.ID(), Name: p.Name(), Tier: p.Tier(), Implemented: p.Implemented(), Connected: p.ID() == "local"}
			if err := m.Store.PutProvider(ctx, mp); err != nil {
				return "", err
			}
		}
	}
	for _, b := range boxyaml.Builtins {
		if _, err := m.Store.GetTemplate(ctx, b.Name); errors.Is(err, model.ErrNoRows) {
			m.Store.PutTemplate(ctx, &model.Template{Name: b.Name, Description: b.Description, YAML: b.YAML, CreatedAt: time.Now().UTC(), Builtin: true})
		}
	}
	// Jobs interrupted by a restart cannot resume.
	jobs, _ := m.Store.ListJobs(ctx, "", 500)
	for _, j := range jobs {
		if j.Status == "queued" || j.Status == "running" {
			now := time.Now().UTC()
			j.Status, j.FinishedAt = "failed", &now
			m.Store.PutJob(ctx, j)
		}
	}
	// Boxes stuck mid-transition get re-driven.
	boxes, _ := m.Store.ListBoxes(ctx, store.BoxFilter{Statuses: []string{model.StatusRequested, model.StatusPlacing, model.StatusProvisioning, model.StatusSnapshotting}})
	for _, b := range boxes {
		if b.Status == model.StatusSnapshotting {
			b.Status = model.StatusRunning
			m.Store.PutBox(ctx, b)
			continue
		}
		b.Status = model.StatusRequested
		m.Store.PutBox(ctx, b)
		go m.provision(b.ID)
	}
	hosts, _ := m.Store.ListHosts(ctx)
	for _, h := range hosts {
		if h.Connected {
			h.Connected = false
			m.Store.PutHost(ctx, h)
		}
	}
	m.initV13(ctx)
	m.initV14(ctx)
	return m.bootstrapAdmin(ctx, envAdminKey)
}

// Start launches the background loops.
func (m *Manager) Start(ctx context.Context) {
	m.ctx = ctx
	go m.loop(ctx, "reaper", m.Cfg.ReaperInterval, m.reap)
	go m.loop(ctx, "ledger", m.Cfg.LedgerInterval, m.ledgerTick)
	go m.loop(ctx, "notify", time.Minute, m.notifyTick)
	go m.loop(ctx, "provider_health", m.Cfg.HealthInterval, m.healthTick)
	go m.loop(ctx, "warm_pool", m.Cfg.WarmInterval, m.warmTick)
	if !m.Cfg.DisablePriceFeed {
		go m.loop(ctx, "price_feed", m.Cfg.PriceFeedInterval, m.refreshPrices)
	} else {
		m.mu.Lock()
		m.sysjobs["price_feed"] = &SysJob{Name: "price_feed", LastStatus: "disabled"}
		m.mu.Unlock()
	}
}

func (m *Manager) loop(ctx context.Context, name string, every time.Duration, fn func(context.Context) error) {
	m.mu.Lock()
	m.sysjobs[name] = &SysJob{Name: name, IntervalS: every.Seconds(), LastStatus: "pending"}
	m.mu.Unlock()
	run := func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("%s panic: %v", name, r)
			}
		}()
		start := time.Now()
		rctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		err := fn(rctx)
		cancel()
		m.mu.Lock()
		j := m.sysjobs[name]
		now := time.Now().UTC()
		j.LastRunAt, j.DurationMS, j.Runs = &now, time.Since(start).Milliseconds(), j.Runs+1
		if err != nil {
			j.LastStatus, j.LastError = "error", err.Error()
			log.Printf("%s: %v", name, err)
		} else {
			j.LastStatus, j.LastError = "ok", ""
		}
		m.mu.Unlock()
	}
	if name == "price_feed" {
		time.Sleep(2 * time.Second) // let the server come up first
	}
	run()
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			run()
		}
	}
}

// SysJobs returns a snapshot of background job state.
func (m *Manager) SysJobs() []SysJob {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []SysJob
	for _, n := range []string{"reaper", "ledger", "price_feed", "notify", "provider_health", "warm_pool"} {
		if j, ok := m.sysjobs[n]; ok {
			out = append(out, *j)
		}
	}
	return out
}

func (m *Manager) sysJob(name string) *SysJob {
	m.mu.Lock()
	defer m.mu.Unlock()
	if j, ok := m.sysjobs[name]; ok {
		c := *j
		return &c
	}
	return nil
}

func (m *Manager) boxLock(id string) *sync.Mutex {
	l, _ := m.boxLocks.LoadOrStore(id, &sync.Mutex{})
	return l.(*sync.Mutex)
}

// ---- events / audit ----

// Emit records an audit event and publishes it on the stream.
func (m *Manager) Emit(ctx context.Context, actor, boxID, agentID, typ, msg string, cost *float64) {
	m.EmitData(ctx, actor, boxID, agentID, typ, msg, cost, nil)
}

// EmitData is Emit with structured details (Event.data).
func (m *Manager) EmitData(ctx context.Context, actor, boxID, agentID, typ, msg string, cost *float64, data map[string]any) {
	e := &model.Event{ID: secret.ID("evt"), TS: time.Now().UTC(), Actor: actor, Type: typ, Message: msg, CostUSD: cost, Data: data}
	if boxID != "" {
		e.BoxID = &boxID
	}
	if agentID != "" {
		e.AgentID = &agentID
	}
	if err := m.Store.AddEvent(ctx, e); err != nil {
		log.Printf("event: %v", err)
	}
	m.Broker.Publish(events.Msg{Type: "event.created", Data: e, AgentID: agentID})
}

func (m *Manager) publishBox(b *model.Box) {
	m.Broker.Publish(events.Msg{Type: "box.updated", Data: b, AgentID: b.AgentID})
}

// ---- settings / quotas ----

func (m *Manager) Settings(ctx context.Context) model.Settings {
	s := model.DefaultSettings()
	_ = m.Store.GetKV(ctx, "settings", &s)
	if m.Cfg.BaseDomain != "" && (s.BaseDomain == "" || s.BaseDomain == "sslip.io") {
		s.BaseDomain = m.Cfg.BaseDomain
	}
	return s
}

func (m *Manager) PutSettings(ctx context.Context, s model.Settings) (model.Settings, error) {
	if s.Reaper.IdleMinutes < 1 {
		s.Reaper.IdleMinutes = 5
	}
	if s.Reaper.KeepSnapshots < 1 {
		s.Reaper.KeepSnapshots = 4
	}
	if s.Reaper.DefaultTTLSeconds < 60 {
		s.Reaper.DefaultTTLSeconds = 3600
	}
	if s.BudgetMonthUSD < 0 {
		return s, model.E(model.ErrInvalid, "budget_month_usd must be >= 0")
	}
	if s.Notify.DailyReportHour < 0 || s.Notify.DailyReportHour > 23 {
		return s, model.E(model.ErrInvalid, "notify.daily_report_hour must be 0..23")
	}
	if s.Notify.Events == nil {
		s.Notify.Events = []string{}
	}
	// Older clients send notify without smtp/timezone: keep the stored ones.
	if prev := m.Settings(ctx).Notify; s.Notify.SMTP.Host == "" && s.Notify.SMTP.From == "" && s.Notify.Timezone == "" {
		s.Notify.SMTP, s.Notify.Timezone = prev.SMTP, prev.Timezone
	}
	if s.Locale == "" {
		s.Locale = "vi"
	}
	if !i18n.Supported(s.Locale) {
		return s, model.E(model.ErrInvalid, "locale must be one of vi, en, zh, ja")
	}
	if err := m.Store.PutKV(ctx, "settings", s); err != nil {
		return s, err
	}
	m.Emit(ctx, "admin", "", "", "settings.updated", "settings changed", nil)
	return s, nil
}

func (m *Manager) Quotas(ctx context.Context) model.Quotas {
	q := model.DefaultQuotas()
	_ = m.Store.GetKV(ctx, "quotas", &q)
	return q
}

func (m *Manager) PutQuotas(ctx context.Context, q model.Quotas) (model.Quotas, error) {
	if q.MaxBoxesPerAgent < 1 || q.MaxCPUPerBox <= 0 || q.MaxRAMMBPerBox < 128 || q.MaxDiskGBPerBox < 1 || q.RateLimitPerMinute < 1 {
		return q, model.E(model.ErrInvalid, "all quota values must be positive")
	}
	err := m.Store.PutKV(ctx, "quotas", q)
	return q, err
}

// ---- helpers ----

func notFound(what, id string) error { return model.E(model.ErrNotFound, "%s %q not found", what, id) }

func dayStart(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func monthStart(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

func ptr[T any](v T) *T { return &v }

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func fmtUSD(v float64) string { return fmt.Sprintf("$%.4f", v) }
