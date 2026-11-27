package manager

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"boxmes/core/internal/model"
	"boxmes/core/internal/provider"
	"boxmes/core/internal/sandbox"
	"boxmes/core/internal/store"
)

// Scheduler v1 (spec M4 / v0.3 §5).
//
//   pool host  : marginal ≈ 0 while load after placement < 80%, then a penalty
//                proportional to the overload (so we spread before saturating).
//   burst offer: price_hour × max(expected_hours, min_billing) + setup cost
//                (monthly cap applied for long-running boxes).
//   pick min cost; ties → pool (lower latency, already booted).
//
// Ledger cost of a pool box = host price_hour × box.cpu / host.cpu (CPU-share
// allocation of the monthly host). Burst box = the whole host's price_hour.

const (
	cpuOvercommit = 4.0
	ramReserve    = 0.9 // usable fraction of host RAM
	loadSoftLimit = 0.8
)

type candidate struct {
	host        *model.Host
	offer       *model.Offer
	cost        float64
	costPerHour float64
	loadBefore  float64
	loadAfter   float64
	label       string
}

// Placement is the scheduler decision.
type Placement struct {
	Host        *model.Host
	Offer       *model.Offer
	CostPerHour float64
	Billing     string
	Reason      string
	// v1.3
	Kind      string  // warm | pool | vm | burst
	ETA       float64 // estimated seconds to running
	Score     float64 // selector score (smart placement)
	Isolation string  // vm when an Incus VM was chosen
}

func expectedHours(b *model.Box) float64 {
	switch b.Lifecycle {
	case model.LifecycleTask:
		if b.TTLSeconds > 0 {
			return float64(b.TTLSeconds) / 3600
		}
		return 1
	case model.LifecycleService:
		return 730
	default:
		return 4
	}
}

func allowed(list []string, v string) bool {
	if len(list) == 0 {
		return true
	}
	for _, x := range list {
		if x == v || x == "*" {
			return true
		}
	}
	return false
}

// candidates evaluates every pool host and burst offer for a box without side
// effects (shared by Schedule and the dry-run Quote).
func (m *Manager) candidates(ctx context.Context, b *model.Box, agent *model.Agent, exclude map[string]bool) (cands, offerCands []candidate, rejected []string, err error) {
	hosts, err := m.Store.ListHosts(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	active, _ := m.Store.ListBoxes(ctx, store.BoxFilter{})
	type alloc struct {
		cpu  float64
		ram  int
		disk int
	}
	used := map[string]*alloc{}
	for _, x := range active {
		if !x.Active() || x.ID == b.ID || x.HostID == "" {
			continue
		}
		a := used[x.HostID]
		if a == nil {
			a = &alloc{}
			used[x.HostID] = a
		}
		a.cpu += x.Spec.CPU
		a.ram += x.Spec.RAMMB
		a.disk += x.Spec.DiskGB
	}
	for hid, w := range m.warmAlloc() { // warm-pool containers occupy capacity too
		a := used[hid]
		if a == nil {
			a = &alloc{}
			used[hid] = a
		}
		a.cpu += w.CPU
		a.ram += w.RAMMB
		a.disk += w.DiskGB
	}
	hours := expectedHours(b)
	for _, h := range hosts {
		if exclude[h.ID] {
			continue
		}
		if h.Status != "ready" || !m.Hub.Connected(h.ID) {
			continue
		}
		if h.Billing == "sandbox" { // v1.4: one box per sandbox, never shared
			continue
		}
		if !allowed(agent.Policy.Providers, h.Provider) || !allowed(agent.Policy.Regions, h.Region) {
			rejected = append(rejected, h.ID+": policy")
			continue
		}
		if b.Region != "" && b.Region != h.Region {
			continue
		}
		a := used[h.ID]
		if a == nil {
			a = &alloc{}
		}
		capRAM := float64(h.RAMMB) * ramReserve
		capCPU := h.CPU * cpuOvercommit
		if capRAM <= 0 || capCPU <= 0 {
			continue
		}
		if float64(a.ram+b.Spec.RAMMB) > capRAM || a.cpu+b.Spec.CPU > capCPU {
			rejected = append(rejected, fmt.Sprintf("%s: full (ram %d/%d MB, cpu %.1f/%.0f)", h.ID, a.ram, int(capRAM), a.cpu, capCPU))
			continue
		}
		// Disk is thin-provisioned (Incus/ZFS/btrfs): allow 2x; local dev driver does not allocate disk.
		if h.Driver != "local" && h.DiskGB > 0 && float64(a.disk+b.Spec.DiskGB) > float64(h.DiskGB)*2 {
			rejected = append(rejected, fmt.Sprintf("%s: disk full", h.ID))
			continue
		}
		before := float64(a.ram) / capRAM
		after := float64(a.ram+b.Spec.RAMMB) / capRAM
		cost := 0.0
		if after > loadSoftLimit {
			cost = h.PriceHour * (after - loadSoftLimit) * 5 * math.Min(hours, 24)
		}
		cph := 0.0
		billing := h.Billing
		if billing == "burst" {
			cph = h.PriceHour // a burst host is billed whole, share it evenly with present boxes
			n := 1
			for _, x := range active {
				if x.Active() && x.HostID == h.ID && x.ID != b.ID {
					n++
				}
			}
			cph = h.PriceHour / float64(n)
		} else if h.CPU > 0 {
			cph = h.PriceHour * math.Min(1, b.Spec.CPU/h.CPU)
		}
		cands = append(cands, candidate{host: h, cost: cost, costPerHour: cph, loadBefore: before, loadAfter: after,
			label: fmt.Sprintf("%s host %s (%s/%s) load %.0f%%→%.0f%% marginal %s", billing, h.ID, h.Provider, h.Region, before*100, after*100, fmtUSD(cost))})
	}
	// Burst offers from the price feed / provider APIs.
	offers, _ := m.Store.ListOffers(ctx, "")
	verified := map[string]bool{}
	blockedSeen := map[string]bool{}
	for _, o := range offers {
		if o.Confidence == "verified" {
			verified[o.Provider] = true
		}
	}
	for i := range offers {
		o := offers[i]
		if verified[o.Provider] && o.Confidence != "verified" {
			continue // prefer live API prices when we have them
		}
		if o.CPU < b.Spec.CPU || o.RAMMB < b.Spec.RAMMB || (o.DiskGB > 0 && o.DiskGB < b.Spec.DiskGB) {
			continue
		}
		if !allowed(agent.Policy.Providers, o.Provider) || !allowed(agent.Policy.Regions, o.Region) {
			continue
		}
		if b.Region != "" && b.Region != o.Region {
			continue
		}
		prov, ok := m.Providers.Get(o.Provider)
		if !ok || !prov.Implemented() || o.Provider == "local" || exclude["provider:"+o.Provider] {
			continue
		}
		if why := m.offerBlocked(ctx, o); why != "" {
			if !blockedSeen[o.Provider] {
				blockedSeen[o.Provider] = true
				rejected = append(rejected, o.Provider+": "+why)
			}
			continue
		}
		cost := provider.Quote(o, hours, m.Cfg.SetupCostUSD)
		offerCands = append(offerCands, candidate{offer: &o, cost: cost, costPerHour: o.PriceHour,
			label: fmt.Sprintf("burst %s %s %s %s/h×max(%.1fh,%dm)=%s", o.Provider, o.SKU, o.Region, fmtUSD(o.PriceHour), hours, o.MinBillingMinutes, fmtUSD(cost))})
	}
	binpack := m.Cfg.Placement != "spread"
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].cost != cands[j].cost {
			return cands[i].cost < cands[j].cost
		}
		if binpack {
			// best fit: fill the fullest host that still has room under the
			// soft limit, keeping other hosts empty (they can be drained/stopped)
			return cands[i].loadAfter > cands[j].loadAfter
		}
		return cands[i].loadAfter < cands[j].loadAfter
	})
	sort.SliceStable(offerCands, func(i, j int) bool { return offerCands[i].cost < offerCands[j].cost })
	return cands, offerCands, rejected, nil
}

// Schedule picks a host (or offer) for a box.
func (m *Manager) Schedule(ctx context.Context, b *model.Box, agent *model.Agent, exclude map[string]bool) (*Placement, error) {
	cands, offerCands, rejected, err := m.candidates(ctx, b, agent, exclude)
	if err != nil {
		return nil, err
	}
	hours := expectedHours(b)

	var best *candidate
	if len(cands) > 0 {
		best = &cands[0]
	}
	connected := func(pid string) bool {
		mp, err := m.Store.GetProvider(ctx, pid)
		return err == nil && mp.Connected && mp.TokenEnc != ""
	}
	// Fallback chain (Hetzner pool → Vultr burst → DO / Contabo / netcup):
	// among CONNECTED providers the first one in the chain with a fitting
	// offer wins (cheapest within that provider). With nothing connected the
	// cheapest offer overall is reported (pool_expand proposal / NO_CAPACITY).
	bestOffer := m.chainOffer(offerCands, connected)
	if bestOffer != nil && (best == nil || bestOffer.cost < best.cost) && m.Cfg.AutoBurst && connected(bestOffer.offer.Provider) {
		bestOffer.label += fmt.Sprintf(" [chain #%d %s]", m.chainRank(bestOffer.offer.Provider)+1, strings.Join(m.chain(), "→"))
		best = bestOffer
	}
	if best == nil {
		hint := "No pool host has room. Ask the operator to add a host (POST /hosts) or retry with a smaller spec."
		msg := fmt.Sprintf("no host fits cpu=%.1f ram=%dMB disk=%dGB", b.Spec.CPU, b.Spec.RAMMB, b.Spec.DiskGB)
		if bestOffer != nil {
			msg += "; cheapest burst option: " + bestOffer.label
			if !m.Cfg.AutoBurst {
				m.proposePoolExpand(ctx, b, bestOffer)
			}
		}
		if len(rejected) > 0 {
			msg += " [" + strings.Join(rejected, "; ") + "]"
		}
		return nil, model.EH(model.ErrNoCapacity, hint, "%s", msg)
	}
	reason := "chose " + best.label
	var alts []string
	for i := 0; i < len(cands) && len(alts) < 2; i++ {
		if &cands[i] != best && cands[i].host != best.host {
			alts = append(alts, cands[i].label)
		}
	}
	if bestOffer != nil && best != bestOffer {
		alts = append(alts, bestOffer.label)
	}
	if len(alts) > 0 {
		reason += " | alternatives: " + strings.Join(alts, "; ")
	}
	reason += fmt.Sprintf(" | expected %.1fh (%s)", hours, b.Lifecycle)
	pl := &Placement{CostPerHour: model.Round6(best.costPerHour), Reason: reason}
	if best.host != nil {
		pl.Host = best.host
		pl.Billing = best.host.Billing
	} else {
		pl.Offer = best.offer
		pl.Billing = "burst"
	}
	return pl, nil
}

// proposePoolExpand files a "pool_expand" approval instead of renting automatically.
func (m *Manager) proposePoolExpand(ctx context.Context, b *model.Box, c *candidate) {
	key := "pool_expand:" + c.offer.Provider + ":" + c.offer.SKU + ":" + time.Now().UTC().Format("2006-01-02")
	amt := c.offer.PriceMonth
	m.createApproval(ctx, "pool_expand", key,
		"Thêm host "+c.offer.Provider+" "+c.offer.SKU,
		fmt.Sprintf("Box %s (%s) không có chỗ. Gợi ý thuê %s %s ở %s (%s/giờ, %s/tháng). Duyệt = đồng ý, sau đó tạo host trong mục Hạ tầng.", b.Slug, b.ID, c.offer.Provider, c.offer.SKU, c.offer.Region, fmtUSD(c.offer.PriceHour), fmtUSD(c.offer.PriceMonth)),
		&amt, b.ID, b.AgentID)
}

// chain returns the configured provider fallback chain.
// Providers with a preference priority (1 = first) come before the rest,
// which keep their configured chain order (v1.3).
func (m *Manager) chain() []string {
	base := append([]string{}, provider.DefaultChain...)
	for _, id := range sandbox.DefaultChain { // v1.4: sandbox providers after the T1 chain
		if _, ok := m.Providers.Get(id); ok {
			base = append(base, id)
		}
	}
	if len(m.Cfg.ProviderChain) > 0 {
		base = m.Cfg.ProviderChain
	}
	ctx := context.Background()
	type pr struct {
		id   string
		rank int
	}
	l := make([]pr, len(base))
	for i, id := range base {
		r := 1000 + i
		if pp := m.ProviderPrefs(ctx, id); pp.Priority > 0 {
			r = pp.Priority*10 + i%10
		}
		l[i] = pr{id, r}
	}
	sort.SliceStable(l, func(i, j int) bool { return l[i].rank < l[j].rank })
	out := make([]string, len(l))
	for i, x := range l {
		out[i] = x.id
	}
	return out
}

// chainRank is the provider's position in the fallback chain (unknown = last).
func (m *Manager) chainRank(id string) int {
	for i, p := range m.chain() {
		if p == id {
			return i
		}
	}
	return len(m.chain())
}

// chainOffer picks the burst offer following the fallback chain: the first
// connected provider (by chain rank) with a fitting offer, cheapest within it.
// offerCands must be sorted by cost. Falls back to the cheapest offer.
func (m *Manager) chainOffer(offerCands []candidate, connected func(string) bool) *candidate {
	if len(offerCands) == 0 {
		return nil
	}
	var best *candidate
	for i := range offerCands {
		c := &offerCands[i]
		if !connected(c.offer.Provider) {
			continue
		}
		if best == nil || m.chainRank(c.offer.Provider) < m.chainRank(best.offer.Provider) {
			best = c
		}
	}
	if best == nil {
		best = &offerCands[0]
	}
	return best
}
