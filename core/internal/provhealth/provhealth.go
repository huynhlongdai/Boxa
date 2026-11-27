// Package provhealth tracks the health of every provider (and region) Boxmes
// talks to: API latency, create success rate, provisioning time p50/p95,
// error classes, a circuit breaker per provider and rate-limit / quota
// awareness. It is pure in-memory logic (the manager persists a snapshot in
// the KV store) so it can be unit tested without any network.
package provhealth

import (
	"errors"
	"math"
	"net"
	"sort"
	"strings"
	"sync"
	"time"
)

// Error classes (stable strings, used in API responses and the UI).
const (
	ClassOK        = "ok"
	ClassAuth      = "auth"       // 401/403: token wrong or missing scope
	ClassRateLimit = "rate_limit" // 429 / "too many requests"
	ClassQuota     = "quota"      // account limit (server count, resource limit)
	ClassCapacity  = "capacity"   // sold out / unavailable in region
	ClassTimeout   = "timeout"
	ClassNetwork   = "network" // DNS / connect refused / TLS
	ClassServer    = "server"  // 5xx
	ClassInvalid   = "invalid" // other 4xx (bad sku/region/params)
	ClassUnknown   = "unknown"
)

// Classes lists every class in display order.
var Classes = []string{ClassAuth, ClassRateLimit, ClassQuota, ClassCapacity, ClassTimeout, ClassNetwork, ClassServer, ClassInvalid, ClassUnknown}

// Classify maps an adapter error to a class by inspecting its text (adapters
// format provider HTTP errors as "HTTP <status>: <body>").
func Classify(err error) string {
	if err == nil {
		return ClassOK
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return ClassTimeout
	}
	s := strings.ToLower(err.Error())
	has := func(subs ...string) bool {
		for _, x := range subs {
			if strings.Contains(s, x) {
				return true
			}
		}
		return false
	}
	switch {
	case has("http 429", "too many requests", "rate limit", "ratelimit", "rate_limit", "throttl"):
		return ClassRateLimit
	case has("http 401", "http 403", "unauthorized", "rejected the token", "forbidden", "invalid token", "authentication"):
		return ClassAuth
	case has("quota", "limit exceeded", "limit reached", "resource_limit", "server_limit", "maximum number of"):
		return ClassQuota
	case has("no capacity", "out of stock", "sold out", "unavailable", "not available", "resource_unavailable", "no_capacity", "insufficient capacity"):
		return ClassCapacity
	case has("deadline exceeded", "timeout", "timed out"):
		return ClassTimeout
	case has("no such host", "connection refused", "connection reset", "dial tcp", "tls:", "network is unreachable", "eof"):
		return ClassNetwork
	case has("http 500", "http 502", "http 503", "http 504", "internal server error", "bad gateway", "service unavailable"):
		return ClassServer
	case has("http 400", "http 404", "http 409", "http 422", "invalid", "not found"):
		return ClassInvalid
	}
	return ClassUnknown
}

// Transient reports whether a class should count against the breaker and
// is worth retrying elsewhere (auth/invalid are configuration problems).
func Transient(class string) bool {
	switch class {
	case ClassRateLimit, ClassCapacity, ClassTimeout, ClassNetwork, ClassServer, ClassUnknown, ClassQuota:
		return true
	}
	return false
}

// Sample kinds.
const (
	KindAPI       = "api"       // health probe / validate / offers call
	KindCreate    = "create"    // provider Create call
	KindProvision = "provision" // host created → boxd connected (seconds)
)

// Sample is one observation.
type Sample struct {
	TS        time.Time `json:"ts"`
	Kind      string    `json:"kind"`
	Region    string    `json:"region,omitempty"`
	OK        bool      `json:"ok"`
	LatencyMS float64   `json:"latency_ms"`
	Class     string    `json:"class"`
	Error     string    `json:"error,omitempty"`
}

// Breaker states.
const (
	Closed   = "closed"
	Open     = "open"
	HalfOpen = "half_open"
)

// BreakerConfig tunes the circuit breaker.
type BreakerConfig struct {
	ConsecutiveFailures int           // open after N consecutive transient failures (default 3)
	FailureRate         float64       // or failure rate ≥ this over the window (default 0.5)
	MinSamples          int           // …with at least this many samples (default 6)
	Cooldown            time.Duration // first open period (default 60s), doubled per re-open up to MaxCooldown
	MaxCooldown         time.Duration // default 15m
}

func (c *BreakerConfig) defaults() {
	if c.ConsecutiveFailures <= 0 {
		c.ConsecutiveFailures = 3
	}
	if c.FailureRate <= 0 {
		c.FailureRate = 0.5
	}
	if c.MinSamples <= 0 {
		c.MinSamples = 6
	}
	if c.Cooldown <= 0 {
		c.Cooldown = time.Minute
	}
	if c.MaxCooldown <= 0 {
		c.MaxCooldown = 15 * time.Minute
	}
}

// Breaker is the per-provider circuit breaker state (exported for persistence).
type Breaker struct {
	State        string     `json:"state"`
	Consecutive  int        `json:"consecutive_failures"`
	OpenedAt     *time.Time `json:"opened_at,omitempty"`
	OpenUntil    *time.Time `json:"open_until,omitempty"`
	Opens        int        `json:"opens"` // re-open count (backoff exponent); reset on close
	LastReason   string     `json:"last_reason,omitempty"`
	ProbeRunning bool       `json:"-"`
}

// RateLimit is the rate-limit / quota awareness state of a provider.
type RateLimit struct {
	BlockedUntil *time.Time `json:"blocked_until,omitempty"` // after a 429: no calls until then
	Hits         int        `json:"hits"`                    // 429s in the window
	QuotaHit     *time.Time `json:"quota_hit_at,omitempty"`  // last account-quota error
	Remaining    int        `json:"remaining"`               // from X-RateLimit-Remaining when known (-1 unknown)
}

// Tracker is safe for concurrent use.
type Tracker struct {
	mu      sync.Mutex
	window  time.Duration
	maxPer  int
	cfg     BreakerConfig
	now     func() time.Time
	samples map[string][]Sample // provider → samples (newest last)
	brk     map[string]*Breaker
	rl      map[string]*RateLimit
}

// New creates a tracker keeping samples for window (default 24h).
func New(window time.Duration, cfg BreakerConfig) *Tracker {
	if window <= 0 {
		window = 24 * time.Hour
	}
	cfg.defaults()
	return &Tracker{window: window, maxPer: 2000, cfg: cfg, now: time.Now,
		samples: map[string][]Sample{}, brk: map[string]*Breaker{}, rl: map[string]*RateLimit{}}
}

// SetClock overrides time (tests).
func (t *Tracker) SetClock(f func() time.Time) { t.mu.Lock(); t.now = f; t.mu.Unlock() }

func (t *Tracker) breaker(p string) *Breaker {
	b := t.brk[p]
	if b == nil {
		b = &Breaker{State: Closed}
		t.brk[p] = b
	}
	return b
}

func (t *Tracker) ratelimit(p string) *RateLimit {
	r := t.rl[p]
	if r == nil {
		r = &RateLimit{Remaining: -1}
		t.rl[p] = r
	}
	return r
}

// Record adds a sample and updates breaker + rate-limit state. err may be
// nil (success). retryAfter (from a Retry-After header) may be 0.
func (t *Tracker) Record(provider string, s Sample, err error, retryAfter time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	if s.TS.IsZero() {
		s.TS = now
	}
	s.OK = err == nil
	s.Class = Classify(err)
	if err != nil {
		s.Error = trim(err.Error(), 240)
	}
	list := append(t.samples[provider], s)
	cut := now.Add(-t.window)
	i := 0
	for i < len(list) && list[i].TS.Before(cut) {
		i++
	}
	list = list[i:]
	if len(list) > t.maxPer {
		list = list[len(list)-t.maxPer:]
	}
	t.samples[provider] = list

	rl := t.ratelimit(provider)
	switch s.Class {
	case ClassRateLimit:
		rl.Hits++
		if retryAfter <= 0 {
			retryAfter = time.Duration(math.Min(float64(30*time.Second)*math.Pow(2, float64(min(rl.Hits-1, 5))), float64(15*time.Minute)))
		}
		u := now.Add(retryAfter)
		rl.BlockedUntil = &u
	case ClassQuota:
		n := now
		rl.QuotaHit = &n
	}
	if s.OK && rl.BlockedUntil != nil && now.After(*rl.BlockedUntil) {
		rl.BlockedUntil = nil
		rl.Hits = 0
	}

	// Breaker: only create + api samples, only transient failures count.
	if s.Kind == KindProvision && s.OK {
		return
	}
	b := t.breaker(provider)
	if s.OK {
		if b.State != Closed {
			b.State, b.OpenedAt, b.OpenUntil, b.Opens = Closed, nil, nil, 0
		}
		b.Consecutive, b.ProbeRunning = 0, false
		return
	}
	if !Transient(s.Class) {
		return
	}
	b.Consecutive++
	b.LastReason = s.Class
	trip := b.State == HalfOpen || b.Consecutive >= t.cfg.ConsecutiveFailures
	if !trip {
		total, fails := 0, 0
		for _, x := range list {
			if x.Kind == KindProvision {
				continue
			}
			total++
			if !x.OK && Transient(x.Class) {
				fails++
			}
		}
		trip = total >= t.cfg.MinSamples && float64(fails)/float64(total) >= t.cfg.FailureRate
	}
	if trip {
		cool := time.Duration(math.Min(float64(t.cfg.Cooldown)*math.Pow(2, float64(b.Opens)), float64(t.cfg.MaxCooldown)))
		if s.Class == ClassRateLimit && rl.BlockedUntil != nil && rl.BlockedUntil.Sub(now) > cool {
			cool = rl.BlockedUntil.Sub(now)
		}
		n := now
		u := now.Add(cool)
		b.State, b.OpenedAt, b.OpenUntil, b.ProbeRunning = Open, &n, &u, false
		b.Opens++
	}
}

// Allow reports whether a call to the provider may proceed now (breaker not
// open and not rate-limited). In half-open state exactly one probe passes.
// The returned reason explains a refusal.
func (t *Tracker) Allow(provider string) (bool, string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	if rl := t.rl[provider]; rl != nil && rl.BlockedUntil != nil && now.Before(*rl.BlockedUntil) {
		return false, "rate limited until " + rl.BlockedUntil.UTC().Format(time.RFC3339)
	}
	b := t.breaker(provider)
	switch b.State {
	case Open:
		if b.OpenUntil != nil && now.Before(*b.OpenUntil) {
			return false, "circuit open until " + b.OpenUntil.UTC().Format(time.RFC3339) + " (" + b.LastReason + ")"
		}
		b.State = HalfOpen
		b.ProbeRunning = true
		return true, "half-open probe"
	case HalfOpen:
		if b.ProbeRunning {
			return false, "circuit half-open, probe in flight"
		}
		b.ProbeRunning = true
		return true, "half-open probe"
	}
	return true, ""
}

// Peek is Allow without side effects (for scoring / display).
func (t *Tracker) Peek(provider string) (bool, string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	if rl := t.rl[provider]; rl != nil && rl.BlockedUntil != nil && now.Before(*rl.BlockedUntil) {
		return false, "rate limited"
	}
	if b := t.brk[provider]; b != nil {
		if b.State == Open && b.OpenUntil != nil && now.Before(*b.OpenUntil) {
			return false, "circuit open (" + b.LastReason + ")"
		}
		if b.State == HalfOpen && b.ProbeRunning {
			return false, "circuit half-open"
		}
	}
	return true, ""
}

// Reset closes the breaker and clears rate-limit state (operator action or new token).
func (t *Tracker) Reset(provider string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.brk[provider] = &Breaker{State: Closed}
	t.rl[provider] = &RateLimit{Remaining: -1}
}

// Stats summarises a provider (region "" = all regions).
type Stats struct {
	Provider          string         `json:"provider"`
	Region            string         `json:"region,omitempty"`
	Samples           int            `json:"samples"`
	APILatencyP50MS   float64        `json:"api_latency_p50_ms"`
	APILatencyP95MS   float64        `json:"api_latency_p95_ms"`
	APISuccessRate    float64        `json:"api_success_rate"` // 0..1 (1 when no samples)
	CreateAttempts    int            `json:"create_attempts"`
	CreateSuccessRate float64        `json:"create_success_rate"` // 0..1 (1 when no attempts)
	ProvisionP50S     float64        `json:"provision_p50_s"`
	ProvisionP95S     float64        `json:"provision_p95_s"`
	ErrorClasses      map[string]int `json:"error_classes"`
	LastOKAt          *time.Time     `json:"last_ok_at"`
	LastErrorAt       *time.Time     `json:"last_error_at"`
	LastError         string         `json:"last_error,omitempty"`
	LastClass         string         `json:"last_error_class,omitempty"`
	Breaker           Breaker        `json:"breaker"`
	RateLimit         RateLimit      `json:"rate_limit"`
	Status            string         `json:"status"` // healthy | degraded | down | unknown
	Reliability       float64        `json:"reliability"`
}

// Stats computes current statistics.
func (t *Tracker) Stats(provider, region string) Stats {
	t.mu.Lock()
	defer t.mu.Unlock()
	st := Stats{Provider: provider, Region: region, ErrorClasses: map[string]int{}, APISuccessRate: 1, CreateSuccessRate: 1}
	var api, prov []float64
	apiOK, apiN, crOK := 0, 0, 0
	for _, s := range t.samples[provider] {
		if region != "" && s.Region != "" && s.Region != region {
			continue
		}
		st.Samples++
		ts := s.TS
		if s.OK {
			if st.LastOKAt == nil || ts.After(*st.LastOKAt) {
				st.LastOKAt = &ts
			}
		} else {
			st.ErrorClasses[s.Class]++
			if st.LastErrorAt == nil || ts.After(*st.LastErrorAt) {
				st.LastErrorAt, st.LastError, st.LastClass = &ts, s.Error, s.Class
			}
		}
		switch s.Kind {
		case KindAPI:
			apiN++
			if s.OK {
				apiOK++
				api = append(api, s.LatencyMS)
			}
		case KindCreate:
			st.CreateAttempts++
			if s.OK {
				crOK++
			}
		case KindProvision:
			if s.OK {
				prov = append(prov, s.LatencyMS/1000)
			}
		}
	}
	if apiN > 0 {
		st.APISuccessRate = round(float64(apiOK)/float64(apiN), 3)
	}
	if st.CreateAttempts > 0 {
		st.CreateSuccessRate = round(float64(crOK)/float64(st.CreateAttempts), 3)
	}
	st.APILatencyP50MS, st.APILatencyP95MS = round(Percentile(api, 50), 1), round(Percentile(api, 95), 1)
	st.ProvisionP50S, st.ProvisionP95S = round(Percentile(prov, 50), 1), round(Percentile(prov, 95), 1)
	if b := t.brk[provider]; b != nil {
		st.Breaker = *b
	} else {
		st.Breaker = Breaker{State: Closed}
	}
	if r := t.rl[provider]; r != nil {
		st.RateLimit = *r
	} else {
		st.RateLimit = RateLimit{Remaining: -1}
	}
	// Reliability blends create success (weighted by attempts) with API success.
	w := math.Min(float64(st.CreateAttempts)/5, 1)
	st.Reliability = round(w*st.CreateSuccessRate+(1-w)*st.APISuccessRate, 3)
	now := t.now()
	switch {
	case st.Samples == 0:
		st.Status = "unknown"
	case st.Breaker.State == Open && st.Breaker.OpenUntil != nil && now.Before(*st.Breaker.OpenUntil):
		st.Status = "down"
	case st.Reliability < 0.9 || st.Breaker.State == HalfOpen || (st.RateLimit.BlockedUntil != nil && now.Before(*st.RateLimit.BlockedUntil)):
		st.Status = "degraded"
	default:
		st.Status = "healthy"
	}
	return st
}

// Regions returns the regions seen in samples for a provider.
func (t *Tracker) Regions(provider string) []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	seen := map[string]bool{}
	for _, s := range t.samples[provider] {
		if s.Region != "" {
			seen[s.Region] = true
		}
	}
	out := make([]string, 0, len(seen))
	for r := range seen {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

// Recent returns the newest n samples of a provider (newest first).
func (t *Tracker) Recent(provider string, n int) []Sample {
	t.mu.Lock()
	defer t.mu.Unlock()
	l := t.samples[provider]
	out := []Sample{}
	for i := len(l) - 1; i >= 0 && len(out) < n; i-- {
		out = append(out, l[i])
	}
	return out
}

// Snapshot is the persisted form.
type Snapshot struct {
	Samples  map[string][]Sample   `json:"samples"`
	Breakers map[string]*Breaker   `json:"breakers"`
	Limits   map[string]*RateLimit `json:"limits"`
}

// Export returns a copy for persistence.
func (t *Tracker) Export() Snapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := Snapshot{Samples: map[string][]Sample{}, Breakers: map[string]*Breaker{}, Limits: map[string]*RateLimit{}}
	for k, v := range t.samples {
		s.Samples[k] = append([]Sample(nil), v...)
	}
	for k, v := range t.brk {
		c := *v
		s.Breakers[k] = &c
	}
	for k, v := range t.rl {
		c := *v
		s.Limits[k] = &c
	}
	return s
}

// Import restores a snapshot.
func (t *Tracker) Import(s Snapshot) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for k, v := range s.Samples {
		t.samples[k] = v
	}
	for k, v := range s.Breakers {
		if v != nil {
			v.ProbeRunning = false
			t.brk[k] = v
		}
	}
	for k, v := range s.Limits {
		if v != nil {
			t.rl[k] = v
		}
	}
}

// Percentile (nearest-rank with linear interpolation); 0 for empty input.
func Percentile(v []float64, p float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	if len(s) == 1 {
		return s[0]
	}
	r := p / 100 * float64(len(s)-1)
	lo := int(math.Floor(r))
	hi := int(math.Ceil(r))
	return s[lo] + (s[hi]-s[lo])*(r-float64(lo))
}

func round(v float64, d int) float64 {
	p := math.Pow(10, float64(d))
	return math.Round(v*p) / p
}

func trim(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
