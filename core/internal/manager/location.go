package manager

// v2.0 box locations: every box is classified by where it runs, derived
// from its placement (provider + region / host). Sandbox API providers are
// region "sandbox" (their data centres are the provider's business).

import (
	"context"
	"sort"
	"strings"
	"time"

	"boxmes/core/internal/model"
	"boxmes/core/internal/sandbox"
	"boxmes/core/internal/store"
)

type siteInfo struct{ region, country, city string }

// sites maps a provider region code without trailing digits (fsn1 → fsn)
// to its continent region, country and city. Codes are shared by providers
// that use IATA-like names (Vultr, DigitalOcean, Hetzner).
var sites = map[string]siteInfo{
	// Hetzner
	"fsn": {"eu", "DE", "Falkenstein"}, "nbg": {"eu", "DE", "Nuremberg"}, "hel": {"eu", "FI", "Helsinki"},
	"ash": {"us", "US", "Ashburn"}, "hil": {"us", "US", "Hillsboro"}, "sin": {"apac", "SG", "Singapore"},
	// Vultr / DigitalOcean / common IATA codes
	"ams": {"eu", "NL", "Amsterdam"}, "fra": {"eu", "DE", "Frankfurt"}, "lhr": {"eu", "GB", "London"}, "lon": {"eu", "GB", "London"},
	"cdg": {"eu", "FR", "Paris"}, "par": {"eu", "FR", "Paris"}, "waw": {"eu", "PL", "Warsaw"}, "mad": {"eu", "ES", "Madrid"},
	"sto": {"eu", "SE", "Stockholm"}, "man": {"eu", "GB", "Manchester"}, "mil": {"eu", "IT", "Milan"}, "vie": {"eu", "AT", "Vienna"},
	"nue": {"eu", "DE", "Nuremberg"}, "dus": {"eu", "DE", "Düsseldorf"}, "muc": {"eu", "DE", "Munich"},
	"ewr": {"us", "US", "New Jersey"}, "nyc": {"us", "US", "New York"}, "ord": {"us", "US", "Chicago"}, "dfw": {"us", "US", "Dallas"},
	"sea": {"us", "US", "Seattle"}, "lax": {"us", "US", "Los Angeles"}, "atl": {"us", "US", "Atlanta"}, "mia": {"us", "US", "Miami"},
	"sjc": {"us", "US", "Silicon Valley"}, "sfo": {"us", "US", "San Francisco"}, "hnl": {"us", "US", "Honolulu"},
	"yto": {"us", "CA", "Toronto"}, "tor": {"us", "CA", "Toronto"}, "mex": {"us", "MX", "Mexico City"}, "iad": {"us", "US", "Washington"},
	"sao": {"us", "BR", "São Paulo"}, "scl": {"us", "CL", "Santiago"},
	"nrt": {"apac", "JP", "Tokyo"}, "itm": {"apac", "JP", "Osaka"}, "icn": {"apac", "KR", "Seoul"}, "sgp": {"apac", "SG", "Singapore"},
	"syd": {"apac", "AU", "Sydney"}, "mel": {"apac", "AU", "Melbourne"}, "bom": {"apac", "IN", "Mumbai"}, "del": {"apac", "IN", "Delhi"},
	"blr": {"apac", "IN", "Bangalore"}, "hkg": {"apac", "HK", "Hong Kong"}, "tyo": {"apac", "JP", "Tokyo"},
	"jnb": {"other", "ZA", "Johannesburg"}, "tlv": {"other", "IL", "Tel Aviv"},
	// Contabo region names
	"eu": {"eu", "DE", ""}, "uk": {"eu", "GB", ""}, "us-central": {"us", "US", ""}, "us-east": {"us", "US", ""},
	"us-west": {"us", "US", ""}, "aus": {"apac", "AU", ""}, "jpn": {"apac", "JP", ""}, "ind": {"apac", "IN", ""},
	// generic
	"us": {"us", "US", ""}, "apac": {"apac", "", ""}, "asia": {"apac", "", ""},
}

// siteCode lowercases a region and drops trailing digits (fsn1 → fsn, nyc3 → nyc).
func siteCode(region string) string {
	c := strings.ToLower(strings.TrimSpace(region))
	return strings.TrimRight(c, "0123456789")
}

func (m *Manager) isSandboxProvider(id string) bool {
	if pr, ok := m.Providers.Get(id); ok {
		_, sb := pr.(sandbox.Provider)
		return sb
	}
	return false
}

// boxLocation derives the location of a box from its placement.
func (m *Manager) boxLocation(ctx context.Context, b *model.Box) *model.BoxLocation {
	if b.Provider == "" && b.HostID == "" {
		return nil // not placed yet
	}
	region := b.Region
	if region == "" && b.HostID != "" {
		if h, err := m.Store.GetHost(ctx, b.HostID); err == nil {
			region = h.Region
		}
	}
	loc := &model.BoxLocation{Provider: b.Provider, Host: b.HostID}
	code := siteCode(region)
	si, known := sites[code]
	switch {
	case b.Billing == "sandbox" || m.isSandboxProvider(b.Provider):
		loc.Region = "sandbox"
		if known {
			loc.Country, loc.City = si.country, si.city
		}
	case b.Provider == "local":
		loc.Region = "local"
	case known:
		loc.Region, loc.Country, loc.City = si.region, si.country, si.city
	default:
		switch {
		case strings.HasPrefix(code, "eu"):
			loc.Region = "eu"
		case strings.HasPrefix(code, "us"), strings.HasPrefix(code, "na"):
			loc.Region = "us"
		case strings.HasPrefix(code, "ap"), strings.HasPrefix(code, "asia"):
			loc.Region = "apac"
		default:
			loc.Region = "other"
		}
	}
	loc.Site = b.Provider
	if code != "" {
		loc.Site = b.Provider + "-" + code
	}
	return loc
}

// LocationRow is one row of GET /v1/boxes/locations.
type LocationRow struct {
	model.BoxLocation
	Running      int     `json:"running"`
	Sleeping     int     `json:"sleeping"`
	Total        int     `json:"total"` // active boxes (running, sleeping, starting, snapshotting)
	CostTodayUSD float64 `json:"cost_today_usd"`
}

// RegionRow sums LocationRows per region.
type RegionRow struct {
	Region       string  `json:"region"`
	Running      int     `json:"running"`
	Sleeping     int     `json:"sleeping"`
	Total        int     `json:"total"`
	CostTodayUSD float64 `json:"cost_today_usd"`
}

// LocationsView is GET /v1/boxes/locations.
type LocationsView struct {
	Items   []*LocationRow `json:"items"`
	Regions []*RegionRow   `json:"regions"`
	Total   RegionRow      `json:"total"`
}

// BoxLocations counts the caller's boxes per location (site) with today's
// cost (UTC day) of each, for the dashboard map.
func (m *Manager) BoxLocations(ctx context.Context, p *Principal) (*LocationsView, error) {
	if err := p.Can(PermView); err != nil && p.Admin {
		return nil, err
	}
	f := store.BoxFilter{WorkspaceID: p.ScopeWS(), IncludeTerminated: true}
	if !p.Admin {
		f.AgentID = p.AgentID
	}
	boxes, err := m.Store.ListBoxes(ctx, f)
	if err != nil {
		return nil, err
	}
	lf := store.LedgerFilter{WorkspaceID: p.ScopeWS(), From: dayStart(time.Now())}
	if !p.Admin {
		lf.AgentID = p.AgentID
	}
	costs, _ := m.Store.SumLedgerBy(ctx, lf, "box_id")
	rows := map[string]*LocationRow{}
	for _, b := range boxes {
		cost := costs[b.ID]
		if !b.Active() && cost == 0 {
			continue
		}
		loc := m.boxLocation(ctx, b)
		if loc == nil {
			continue
		}
		r := rows[loc.Site]
		if r == nil {
			r = &LocationRow{BoxLocation: *loc}
			r.Host = "" // a site can span hosts
			rows[loc.Site] = r
		}
		if b.Active() {
			r.Total++
			switch b.Status {
			case model.StatusRunning:
				r.Running++
			case model.StatusSleeping:
				r.Sleeping++
			}
		}
		r.CostTodayUSD += cost
	}
	v := &LocationsView{Items: []*LocationRow{}, Regions: []*RegionRow{}, Total: RegionRow{Region: "all"}}
	regs := map[string]*RegionRow{}
	for _, r := range rows {
		r.CostTodayUSD = model.Round4(r.CostTodayUSD)
		v.Items = append(v.Items, r)
		g := regs[r.Region]
		if g == nil {
			g = &RegionRow{Region: r.Region}
			regs[r.Region] = g
			v.Regions = append(v.Regions, g)
		}
		g.Running, g.Sleeping, g.Total, g.CostTodayUSD = g.Running+r.Running, g.Sleeping+r.Sleeping, g.Total+r.Total, model.Round4(g.CostTodayUSD+r.CostTodayUSD)
		v.Total.Running, v.Total.Sleeping, v.Total.Total = v.Total.Running+r.Running, v.Total.Sleeping+r.Sleeping, v.Total.Total+r.Total
		v.Total.CostTodayUSD = model.Round4(v.Total.CostTodayUSD + r.CostTodayUSD)
	}
	sort.Slice(v.Items, func(i, j int) bool {
		if v.Items[i].Total != v.Items[j].Total {
			return v.Items[i].Total > v.Items[j].Total
		}
		return v.Items[i].Site < v.Items[j].Site
	})
	sort.Slice(v.Regions, func(i, j int) bool { return v.Regions[i].Region < v.Regions[j].Region })
	return v, nil
}
