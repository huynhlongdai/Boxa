package manager

// v1.7 lifecycle capability matrix per provider (research item 5): what sleep
// keeps, whether the provider's own URL wakes a paused box, native fork,
// inbound HTTP and the continuous runtime cap. The Boxaz gateway wakes boxes
// on every provider, so wake_via_gateway is always true.

import (
	"context"
	"sort"

	"boxaz/core/internal/model"
	"boxaz/core/internal/sandbox"
)

var knownLifecycle = map[string]model.LifecycleCaps{
	"e2b":     {MemoryPause: true, WakeOnHTTPNative: true, Fork: true, InboundHTTP: true, MaxRuntimeS: 86400, Note: "autoResume on the preview host (set at create); runtime cap 24 h Pro / 1 h Hobby, reset by a resume"},
	"novita":  {MemoryPause: true, WakeOnHTTPNative: true, InboundHTTP: true, MaxRuntimeS: 14400, Note: "E2B-compatible autoResume; clone = snapshot → new sandbox"},
	"prized":  {MemoryPause: true, Fork: true, MaxRuntimeS: 604800, Note: "no inbound HTTP: the gateway reaches the app through the in-box ingress agent (or a tunnel)"},
	"daytona": {Fork: true, InboundHTTP: true, Note: "containers: stop keeps the disk only, the app restarts on wake"},
	"boxdsh":  {MemoryPause: true, WakeOnHTTPNative: true, Fork: true, InboundHTTP: true, Note: "wakes on the first inbound packet"},
	"runloop": {WakeOnHTTPNative: true, InboundHTTP: true, Note: "suspend keeps the disk only; service winding down (Nov 2026)"},
	"blaxel":  {MemoryPause: true, WakeOnHTTPNative: true, InboundHTTP: true, Note: "standby wakes on connection"},
	"vercel":  {Fork: true, InboundHTTP: true, MaxRuntimeS: 18000, Note: "sessions persist the filesystem only: the app restarts on wake"},
}

// LifecycleFor returns the capability flags of a provider id.
func (m *Manager) LifecycleFor(id string) model.LifecycleCaps {
	c, ok := knownLifecycle[id]
	if !ok {
		if pr, found := m.Providers.Get(id); found {
			if sp, isSandbox := pr.(sandbox.Provider); isSandbox {
				caps := sp.Info().Caps
				c = model.LifecycleCaps{MemoryPause: caps.Pause, Fork: caps.Fork, InboundHTTP: caps.Ports, MaxRuntimeS: caps.MaxLifetimeS, Note: "derived from the adapter's capabilities"}
			} else {
				// VPS / local hosts: incus containers stop on sleep (processes
				// restart on wake), CoW clone on the host, ports via Boxaz ingress
				c = model.LifecycleCaps{Fork: true, InboundHTTP: true, Note: "pool host: sleep stops the container (the supervised app restarts on wake); clone is copy-on-write on the host"}
			}
		}
	} else if pr, found := m.Providers.Get(id); found {
		if sp, isSandbox := pr.(sandbox.Provider); isSandbox && c.MaxRuntimeS == 0 {
			c.MaxRuntimeS = sp.Info().Caps.MaxLifetimeS
		}
	}
	c.WakeViaGateway = true
	c.PrivatePreview = c.InboundHTTP && m.privatePreviewSupported(id)
	return c
}

// CapabilityRow is one row of GET /v1/capabilities.
type CapabilityRow struct {
	Provider string `json:"provider"`
	Group    string `json:"group"` // vps | sandbox
	model.LifecycleCaps
}

// Capabilities lists the lifecycle flags of every registered provider.
func (m *Manager) Capabilities(ctx context.Context) []CapabilityRow {
	out := []CapabilityRow{}
	for _, pr := range m.Providers.All() {
		g := "vps"
		if _, ok := pr.(sandbox.Provider); ok {
			g = "sandbox"
		}
		out = append(out, CapabilityRow{Provider: pr.ID(), Group: g, LifecycleCaps: m.LifecycleFor(pr.ID())})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Group < out[j].Group })
	return out
}
