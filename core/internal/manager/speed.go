package manager

import (
	"context"
	"sort"
	"strings"

	"boxaz/core/internal/model"
	"boxaz/core/internal/store"
)

// SpeedHost summarises one host's speed features.
type SpeedHost struct {
	HostID   string   `json:"host_id"`
	Provider string   `json:"provider"`
	Region   string   `json:"region"`
	Driver   string   `json:"driver"`
	Arch     string   `json:"arch"`
	CoW      string   `json:"cow"` // zfs | btrfs | lvm | reflink | copy
	VM       bool     `json:"vm"`
	Cache    bool     `json:"cache"`
	Mesh     bool     `json:"mesh"`
	LoadPct  float64  `json:"load_pct"` // allocated RAM share (bin-packing view)
	Boxes    int      `json:"boxes"`
	ColdETA  float64  `json:"cold_eta_s"`
	Features []string `json:"features"`
}

// SpeedView is GET /speed: what makes boxes start fast and how fast they start.
type SpeedView struct {
	Placement   string      `json:"placement"` // binpack | spread
	Warm        WarmStats   `json:"warm"`
	WarmEnabled bool        `json:"warm_enabled"`
	WarmReady   int         `json:"warm_ready"`
	AvgClaimMS  float64     `json:"avg_claim_ms"`
	AvgColdMS   float64     `json:"avg_cold_ms"`
	Speedup     float64     `json:"speedup"` // cold / claim
	Hosts       []SpeedHost `json:"hosts"`
	CacheEnv    []string    `json:"cache_env"`
	ExecCompact bool        `json:"exec_compact"`
}

// Speed reports warm-pool, clone, cache and bin-packing state.
func (m *Manager) Speed(ctx context.Context, p *Principal) (*SpeedView, error) {
	wv, err := m.GetWarmPool(ctx, p)
	if err != nil {
		return nil, err
	}
	v := &SpeedView{Placement: m.Cfg.Placement, Warm: wv.Stats, WarmEnabled: wv.Enabled, AvgClaimMS: wv.Stats.AvgClaimMS, AvgColdMS: wv.Stats.AvgColdMS, Hosts: []SpeedHost{}, ExecCompact: true,
		CacheEnv: []string{"PIP_INDEX_URL", "UV_INDEX_URL", "npm_config_registry", "GOPROXY", "apt Acquire::http::Proxy"}}
	for _, n := range wv.Ready {
		v.WarmReady += n
	}
	if v.AvgClaimMS > 0 && v.AvgColdMS > 0 {
		v.Speedup = round1(v.AvgColdMS / v.AvgClaimMS)
	}
	hosts, _ := m.ListHosts(ctx)
	alloc := map[string]int{}
	boxes, _ := m.Store.ListBoxes(ctx, store.BoxFilter{})
	for _, b := range boxes {
		if b.Active() {
			alloc[b.HostID] += b.Spec.RAMMB
		}
	}
	for hid, w := range m.warmAlloc() {
		alloc[hid] += w.RAMMB
	}
	for _, h := range hosts {
		sh := SpeedHost{HostID: h.ID, Provider: h.Provider, Region: h.Region, Driver: h.Driver, Arch: h.Arch, Boxes: h.BoxCount, Features: h.Features,
			ColdETA: round1(m.containerETA(h, "base")), CoW: "copy"}
		if sh.Features == nil {
			sh.Features = []string{}
		}
		for _, f := range h.Features {
			switch {
			case strings.HasPrefix(f, "cow:"):
				sh.CoW = strings.TrimPrefix(f, "cow:")
			case f == "vm":
				sh.VM = true
			case f == "cache":
				sh.Cache = true
			case f == "mesh":
				sh.Mesh = true
			}
		}
		if h.RAMMB > 0 {
			sh.LoadPct = round1(float64(alloc[h.ID]) / (float64(h.RAMMB) * ramReserve) * 100)
		}
		v.Hosts = append(v.Hosts, sh)
	}
	sort.Slice(v.Hosts, func(i, j int) bool { return v.Hosts[i].LoadPct > v.Hosts[j].LoadPct })
	return v, nil
}

var _ = model.StatusRunning
