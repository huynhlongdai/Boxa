package manager

// v1.3 warm pool: pre-started containers per popular template/profile on
// pool hosts (never on burst hosts). A create that matches a warm entry is
// "claimed" (AGENTS.md + env + limits re-targeted, ~1 RPC) instead of a cold
// container launch. Refill runs in parallel; entries expire after a TTL.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"boxa/core/internal/boxd"
	"boxa/core/internal/boxyaml"
	"boxa/core/internal/model"
	"boxa/core/internal/proto"
	"boxa/core/internal/secret"
)

const kvWarmCfg = "warm_pool"
const kvWarmEntries = "warm_entries"

// WarmPoolSpec configures one pool.
type WarmPoolSpec struct {
	Template   string     `json:"template"`
	Profile    string     `json:"profile,omitempty"`
	Spec       model.Spec `json:"spec"`
	Min        int        `json:"min"`
	Max        int        `json:"max"`
	TTLMinutes int        `json:"ttl_minutes"`
}

// WarmConfig is GET/PUT /warm-pool config.
type WarmConfig struct {
	Enabled bool           `json:"enabled"`
	Pools   []WarmPoolSpec `json:"pools"`
}

// WarmEntry is one pre-started container.
type WarmEntry struct {
	ID        string     `json:"id"`
	HostID    string     `json:"host_id"`
	Template  string     `json:"template"`
	Spec      model.Spec `json:"spec"`
	Status    string     `json:"status"` // starting | ready
	CreatedAt time.Time  `json:"created_at"`
	ReadyAt   *time.Time `json:"ready_at,omitempty"`
	StartMS   int64      `json:"start_ms"`
}

// WarmStats is part of GET /warm-pool.
type WarmStats struct {
	Hits        int     `json:"hits"`
	Misses      int     `json:"misses"`
	HitRate     float64 `json:"hit_rate"`
	AvgClaimMS  float64 `json:"avg_claim_ms"`
	AvgColdMS   float64 `json:"avg_cold_ms"`
	Created     int     `json:"created"`
	Expired     int     `json:"expired"`
	LastRefill  string  `json:"last_refill,omitempty"`
	RefillError string  `json:"refill_error,omitempty"`
}

type warmState struct {
	mu      sync.Mutex
	entries map[string]*WarmEntry
	stats   WarmStats
	claimMS []float64
	coldMS  []float64
	filling sync.Mutex
}

// WarmView is GET /warm-pool.
type WarmView struct {
	WarmConfig
	Entries []WarmEntry    `json:"entries"`
	Stats   WarmStats      `json:"stats"`
	Ready   map[string]int `json:"ready"` // template → ready count
}

func (m *Manager) warmConfig(ctx context.Context) WarmConfig {
	c := WarmConfig{Pools: []WarmPoolSpec{}}
	_ = m.Store.GetKV(ctx, kvWarmCfg, &c)
	if c.Pools == nil {
		c.Pools = []WarmPoolSpec{}
	}
	return c
}

func (m *Manager) loadWarm(ctx context.Context) {
	m.warm.mu.Lock()
	defer m.warm.mu.Unlock()
	m.warm.entries = map[string]*WarmEntry{}
	var l []*WarmEntry
	if err := m.Store.GetKV(ctx, kvWarmEntries, &l); err == nil {
		for _, e := range l {
			if e.Status == "ready" {
				m.warm.entries[e.ID] = e
			}
		}
	}
}

func (m *Manager) saveWarmLocked(ctx context.Context) {
	l := make([]*WarmEntry, 0, len(m.warm.entries))
	for _, e := range m.warm.entries {
		l = append(l, e)
	}
	_ = m.Store.PutKV(ctx, kvWarmEntries, l)
}

// warmAlloc returns capacity used by warm entries per host.
func (m *Manager) warmAlloc() map[string]model.Spec {
	m.warm.mu.Lock()
	defer m.warm.mu.Unlock()
	out := map[string]model.Spec{}
	for _, e := range m.warm.entries {
		s := out[e.HostID]
		s.CPU += e.Spec.CPU
		s.RAMMB += e.Spec.RAMMB
		s.DiskGB += e.Spec.DiskGB
		out[e.HostID] = s
	}
	return out
}

// warmReady counts ready entries per template (optionally only on allowed hosts).
func (m *Manager) warmReady(template string, ok func(*WarmEntry) bool) []*WarmEntry {
	m.warm.mu.Lock()
	defer m.warm.mu.Unlock()
	var out []*WarmEntry
	for _, e := range m.warm.entries {
		if e.Status == "ready" && e.Template == template && (ok == nil || ok(e)) && m.Hub.Connected(e.HostID) {
			c := *e
			out = append(out, &c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// takeWarm atomically removes a ready entry (oldest first) for template.
func (m *Manager) takeWarm(ctx context.Context, template, id string, ok func(*WarmEntry) bool) *WarmEntry {
	m.warm.mu.Lock()
	defer m.warm.mu.Unlock()
	var best *WarmEntry
	for _, e := range m.warm.entries {
		if e.Status != "ready" || e.Template != template || !m.Hub.Connected(e.HostID) || (ok != nil && !ok(e)) || (id != "" && e.ID != id) {
			continue
		}
		if best == nil || e.CreatedAt.Before(best.CreatedAt) {
			best = e
		}
	}
	if best != nil {
		delete(m.warm.entries, best.ID)
		m.saveWarmLocked(ctx)
	}
	return best
}

func (m *Manager) warmRecord(hit bool, claimMS float64) {
	m.warm.mu.Lock()
	defer m.warm.mu.Unlock()
	if hit {
		m.warm.stats.Hits++
		m.warm.claimMS = lastN(append(m.warm.claimMS, claimMS), 100)
	} else {
		m.warm.stats.Misses++
	}
}

// recordColdCreate keeps cold create durations (for ETA and the hit/miss benchmark).
func (m *Manager) recordColdCreate(hostID, template string, d time.Duration) {
	m.warm.mu.Lock()
	m.warm.coldMS = lastN(append(m.warm.coldMS, float64(d.Milliseconds())), 100)
	m.warm.mu.Unlock()
	m.etaMu.Lock()
	k := hostID + "|" + template
	m.etas[k] = lastN(append(m.etas[k], d.Seconds()), 20)
	m.etaMu.Unlock()
}

func lastN(v []float64, n int) []float64 {
	if len(v) > n {
		return v[len(v)-n:]
	}
	return v
}

func avg(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := 0.0
	for _, x := range v {
		s += x
	}
	return s / float64(len(v))
}

// GetWarmPool returns config, entries and stats.
func (m *Manager) GetWarmPool(ctx context.Context, p *Principal) (*WarmView, error) {
	if err := p.Require(model.ScopeAdmin); err != nil {
		return nil, err
	}
	v := &WarmView{WarmConfig: m.warmConfig(ctx), Entries: []WarmEntry{}, Ready: map[string]int{}}
	m.warm.mu.Lock()
	for _, e := range m.warm.entries {
		v.Entries = append(v.Entries, *e)
		if e.Status == "ready" {
			v.Ready[e.Template]++
		}
	}
	v.Stats = m.warm.stats
	v.Stats.AvgClaimMS = round1(avg(m.warm.claimMS))
	v.Stats.AvgColdMS = round1(avg(m.warm.coldMS))
	if n := v.Stats.Hits + v.Stats.Misses; n > 0 {
		v.Stats.HitRate = round1(float64(v.Stats.Hits)/float64(n)*100) / 100
	}
	m.warm.mu.Unlock()
	sort.Slice(v.Entries, func(i, j int) bool { return v.Entries[i].CreatedAt.Before(v.Entries[j].CreatedAt) })
	return v, nil
}

// PutWarmPool validates and stores the warm pool config, then refills.
func (m *Manager) PutWarmPool(ctx context.Context, p *Principal, c WarmConfig) (*WarmView, error) {
	if err := p.Require(model.ScopeAdmin); err != nil {
		return nil, err
	}
	if c.Pools == nil {
		c.Pools = []WarmPoolSpec{}
	}
	if len(c.Pools) > 20 {
		return nil, model.E(model.ErrInvalid, "at most 20 warm pools")
	}
	for i := range c.Pools {
		ps := &c.Pools[i]
		if ps.Template == "" {
			return nil, model.EH(model.ErrInvalid, "Use a template name from GET /templates.", "warm pool %d: template is required", i+1)
		}
		if _, err := m.Store.GetTemplate(ctx, ps.Template); err != nil {
			return nil, model.EH(model.ErrInvalid, "Use one of GET /templates.", "template %q not found", ps.Template)
		}
		if ps.Spec.CPU <= 0 {
			ps.Spec.CPU = 1
		}
		if ps.Spec.RAMMB <= 0 {
			ps.Spec.RAMMB = 1024
		}
		if ps.Spec.DiskGB <= 0 {
			ps.Spec.DiskGB = 10
		}
		if ps.Min < 0 || ps.Min > 20 || ps.Max < ps.Min {
			return nil, model.EH(model.ErrInvalid, "min is 0..20 and max >= min.", "warm pool %d: invalid min/max", i+1)
		}
		if ps.Max == 0 {
			ps.Max = ps.Min
		}
		if ps.TTLMinutes <= 0 {
			ps.TTLMinutes = 60
		}
	}
	if err := m.Store.PutKV(ctx, kvWarmCfg, c); err != nil {
		return nil, err
	}
	m.Emit(ctx, p.Actor(), "", "", "warm_pool.updated", fmt.Sprintf("warm pool enabled=%v, %d pools", c.Enabled, len(c.Pools)), nil)
	go m.warmTick(context.Background())
	return m.GetWarmPool(ctx, p)
}

// warmTick expires old entries, trims to max and refills to min in parallel.
func (m *Manager) warmTick(ctx context.Context) error {
	if !m.warm.filling.TryLock() {
		return nil
	}
	defer m.warm.filling.Unlock()
	cfg := m.warmConfig(ctx)
	now := time.Now().UTC()
	byTpl := map[string]WarmPoolSpec{}
	for _, ps := range cfg.Pools {
		byTpl[ps.Template] = ps
	}
	// expire / drop
	var drop []*WarmEntry
	m.warm.mu.Lock()
	count := map[string]int{}
	for id, e := range m.warm.entries {
		ps, ok := byTpl[e.Template]
		switch {
		case !cfg.Enabled || !ok:
			drop = append(drop, e)
		case now.Sub(e.CreatedAt) > time.Duration(ps.TTLMinutes)*time.Minute:
			drop = append(drop, e)
			m.warm.stats.Expired++
		case !m.Hub.Connected(e.HostID):
			delete(m.warm.entries, id) // host gone: its container is gone with it
			continue
		default:
			count[e.Template]++
			if count[e.Template] > ps.Max {
				drop = append(drop, e)
				count[e.Template]--
			}
			continue
		}
		delete(m.warm.entries, id)
	}
	m.saveWarmLocked(ctx)
	m.warm.mu.Unlock()
	for _, e := range drop {
		dctx, cancel := context.WithTimeout(ctx, time.Minute)
		_ = m.Hub.Call(dctx, e.HostID, proto.MDelete, proto.BoxRef{BoxID: e.ID}, nil)
		cancel()
	}
	if !cfg.Enabled {
		return nil
	}
	// refill (parallel)
	var wg sync.WaitGroup
	var errMu sync.Mutex
	var errs []string
	for _, ps := range cfg.Pools {
		need := ps.Min - count[ps.Template]
		for i := 0; i < need; i++ {
			host := m.warmHost(ctx, ps.Spec)
			if host == "" {
				errMu.Lock()
				errs = append(errs, ps.Template+": no pool host has room")
				errMu.Unlock()
				break
			}
			e := &WarmEntry{ID: secret.ID("box"), HostID: host, Template: ps.Template, Spec: ps.Spec, Status: "starting", CreatedAt: time.Now().UTC()}
			m.warm.mu.Lock()
			m.warm.entries[e.ID] = e // reserve capacity while starting
			m.warm.mu.Unlock()
			wg.Add(1)
			go func(e *WarmEntry, tpl string) {
				defer wg.Done()
				start := time.Now()
				cctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
				err := m.Hub.Call(cctx, e.HostID, proto.MCreate, proto.CreateParams{BoxID: e.ID, Template: m.baseTemplateOf(cctx, tpl), Spec: e.Spec}, nil)
				cancel()
				m.warm.mu.Lock()
				defer m.warm.mu.Unlock()
				if err != nil {
					delete(m.warm.entries, e.ID)
					errMu.Lock()
					errs = append(errs, tpl+": "+err.Error())
					errMu.Unlock()
					return
				}
				if _, still := m.warm.entries[e.ID]; !still {
					return
				}
				t := time.Now().UTC()
				e.Status, e.ReadyAt, e.StartMS = "ready", &t, time.Since(start).Milliseconds()
				m.warm.stats.Created++
				m.warm.coldMS = lastN(append(m.warm.coldMS, float64(e.StartMS)), 100)
				m.saveWarmLocked(context.Background())
			}(e, ps.Template)
		}
	}
	wg.Wait()
	m.warm.mu.Lock()
	m.warm.stats.LastRefill = time.Now().UTC().Format(time.RFC3339)
	m.warm.stats.RefillError = strings.Join(errs, "; ")
	m.warm.mu.Unlock()
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// warmHost picks a pool (not burst) host with room using the bin-packing scheduler.
func (m *Manager) warmHost(ctx context.Context, spec model.Spec) string {
	probe := &model.Box{ID: "warm-probe", Lifecycle: model.LifecycleSession, Spec: spec}
	cands, _, _, err := m.candidates(ctx, probe, &model.Agent{}, nil)
	if err != nil {
		return ""
	}
	for _, c := range cands {
		if c.host.Billing != "burst" && c.loadAfter <= loadSoftLimit {
			return c.host.ID
		}
	}
	return ""
}

// baseTemplateOf resolves a custom template to its built-in base.
func (m *Manager) baseTemplateOf(ctx context.Context, tpl string) string {
	if t, err := m.Store.GetTemplate(ctx, tpl); err == nil && !t.Builtin {
		if def, errs := boxyaml.Parse(t.YAML); errs == nil && boxyaml.IsBuiltin(def.Template) {
			return def.Template
		}
		return "base"
	}
	return tpl
}

// claimWarm hands a warm container to box b (placement already decided).
func (m *Manager) claimWarm(ctx context.Context, b *model.Box, agent *model.Agent, e *WarmEntry) error {
	start := time.Now()
	cctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	err := m.Hub.Call(cctx, e.HostID, proto.MClaim, proto.ClaimParams{WarmID: e.ID, BoxID: b.ID, Spec: b.Spec, AgentID: b.AgentID, AgentsMD: m.agentsMD(ctx, b, agent)}, nil)
	if err != nil {
		dctx, dc := context.WithTimeout(ctx, time.Minute)
		_ = m.Hub.Call(dctx, e.HostID, proto.MDelete, proto.BoxRef{BoxID: e.ID}, nil)
		dc()
		return err
	}
	// user setup still runs (template setup was done when the entry started)
	for _, c := range b.Setup {
		var out boxd.ExecOut
		sctx, sc := context.WithTimeout(ctx, 15*time.Minute)
		err := m.Hub.Call(sctx, e.HostID, proto.MExec, proto.ExecParams{BoxID: b.ID, Command: c, TimeoutS: 900}, &out)
		sc()
		if err != nil {
			return err
		}
		if out.ExitCode != 0 {
			return model.E(model.ErrProviderFailed, "setup %q exited %d", c, out.ExitCode)
		}
	}
	m.warmRecord(true, float64(time.Since(start).Milliseconds()))
	go m.warmTick(context.Background()) // refill in the background
	return nil
}

func round1(v float64) float64 { return float64(int64(v*10+0.5)) / 10 }

func round3(v float64) float64 { return float64(int64(v*1000+0.5)) / 1000 }
