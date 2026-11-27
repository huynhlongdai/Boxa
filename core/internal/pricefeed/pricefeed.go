// Package pricefeed pulls open price + capability data from battleships.dev
// (spec v0.3 §6). It never fails hard: network errors are reported per
// provider and the previous data stays in the store.
package pricefeed

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"boxmes/core/internal/model"
)

const DefaultBaseURL = "https://battleships.dev"

// DefaultAllowlist is the set of battleships provider ids we import.
var DefaultAllowlist = []string{"hetzner-cloud", "vultr", "contabo", "netcup", "digitalocean"}

// BoxmesID maps a battleships id to the Boxmes provider id.
func BoxmesID(bsID string) string {
	switch bsID {
	case "hetzner-cloud":
		return "hetzner"
	}
	return bsID
}

type Fetcher struct {
	BaseURL   string
	Allowlist []string
	HTTP      *http.Client
}

// Result for one provider.
type Result struct {
	BattleshipsID string
	Provider      string
	Capabilities  map[string]any
	Offers        []model.Offer
	Err           error
}

type indexFile struct {
	Generated string `json:"generated"`
	Count     int    `json:"count"`
	Providers []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		JSON string `json:"json"`
	} `json:"providers"`
}

type providerFile struct {
	ID               string         `json:"id"`
	Name             string         `json:"name"`
	Category         string         `json:"category"`
	Isolation        string         `json:"isolation"`
	FeaturesResolved map[string]any `json:"features_resolved"`
	Modes            []struct {
		Key              string   `json:"key"`
		Label            string   `json:"label"`
		Pricing          string   `json:"pricing"`
		Regions          []string `json:"regions"`
		Flags            []string `json:"flags"`
		GPUOnly          bool     `json:"gpu_only"`
		MinBilledSeconds float64  `json:"min_billed_seconds"`
		StartFee         float64  `json:"start_fee"`
		Sizes            []struct {
			Name     string  `json:"name"`
			VCPU     float64 `json:"vcpu"`
			RAMGiB   float64 `json:"ram_gib"`
			DiskGiB  float64 `json:"disk_gib"`
			Hour     float64 `json:"hour"`
			MonthCap float64 `json:"month_cap"`
			Month    float64 `json:"month"`
		} `json:"sizes"`
	} `json:"modes"`
}

func (f *Fetcher) get(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "boxmes-core/0.1 (+price feed)")
	c := f.HTTP
	if c == nil {
		c = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

// Fetch downloads the index and every allowlisted provider file.
func (f *Fetcher) Fetch(ctx context.Context) ([]Result, error) {
	base := strings.TrimRight(f.BaseURL, "/")
	if base == "" {
		base = DefaultBaseURL
	}
	allow := f.Allowlist
	if len(allow) == 0 {
		allow = DefaultAllowlist
	}
	urls := map[string]string{}
	var idx indexFile
	idxErr := f.get(ctx, base+"/data/index.json", &idx)
	if idxErr == nil {
		for _, p := range idx.Providers {
			urls[p.ID] = p.JSON
		}
	}
	var out []Result
	for _, id := range allow {
		u := base + "/data/providers/" + id + ".json"
		if idxErr == nil {
			if _, ok := urls[id]; !ok {
				out = append(out, Result{BattleshipsID: id, Provider: BoxmesID(id), Err: fmt.Errorf("provider %q not in battleships index", id)})
				continue
			}
		}
		var pf providerFile
		if err := f.get(ctx, u, &pf); err != nil {
			out = append(out, Result{BattleshipsID: id, Provider: BoxmesID(id), Err: err})
			continue
		}
		out = append(out, Convert(id, &pf, time.Now().UTC()))
	}
	if idxErr != nil && allErr(out) {
		return out, fmt.Errorf("price feed unreachable: %w", idxErr)
	}
	return out, nil
}

func allErr(rs []Result) bool {
	for _, r := range rs {
		if r.Err == nil {
			return false
		}
	}
	return true
}

// ParseProvider converts raw provider JSON (exported for tests).
func ParseProvider(bsID string, raw []byte, now time.Time) (Result, error) {
	var pf providerFile
	if err := json.Unmarshal(raw, &pf); err != nil {
		return Result{}, err
	}
	return Convert(bsID, &pf, now), nil
}

// Convert maps battleships modes × sizes × regions to offers.
func Convert(bsID string, pf *providerFile, now time.Time) Result {
	r := Result{BattleshipsID: bsID, Provider: BoxmesID(bsID), Capabilities: pf.FeaturesResolved}
	if r.Capabilities == nil {
		r.Capabilities = map[string]any{}
	}
	r.Capabilities["_isolation"] = pf.Isolation
	r.Capabilities["_category"] = pf.Category
	for _, m := range pf.Modes {
		if m.GPUOnly || hasFlag(m.Flags, "alt") || hasFlag(m.Flags, "legacy") {
			continue
		}
		conf := "unverified"
		if hasFlag(m.Flags, "stock") || hasFlag(m.Flags, "stock_limited") || hasFlag(m.Flags, "limited") {
			conf = "stock_limited"
		}
		minMin := int(m.MinBilledSeconds / 60)
		regions := m.Regions
		if len(regions) == 0 {
			regions = []string{"any"}
		}
		for _, s := range m.Sizes {
			if s.Hour <= 0 && s.Month <= 0 && s.MonthCap <= 0 {
				continue
			}
			ph, pm := s.Hour, s.MonthCap
			if pm == 0 {
				pm = s.Month
			}
			mins := minMin
			// Monthly-only providers (netcup, contabo terms) are encoded with
			// hour == month_cap: treat the value as a month price billed per month.
			if ph > 0 && ph == pm {
				ph = pm / 730
				mins = 30 * 24 * 60
			}
			if ph == 0 && pm > 0 {
				ph = pm / 730
			}
			if pm == 0 {
				pm = ph * 730
			}
			for _, reg := range regions {
				r.Offers = append(r.Offers, model.Offer{
					Provider: r.Provider, SKU: strings.ToLower(s.Name), Region: reg,
					CPU: s.VCPU, RAMMB: int(s.RAMGiB * 1024), DiskGB: int(s.DiskGiB),
					PriceHour: round6(ph), PriceMonth: round6(pm), MinBillingMinutes: mins,
					Source: "battleships", FetchedAt: now, Confidence: conf,
				})
			}
		}
	}
	return r
}

func hasFlag(fs []string, f string) bool {
	for _, x := range fs {
		if x == f {
			return true
		}
	}
	return false
}

func round6(v float64) float64 { return float64(int64(v*1e6+0.5)) / 1e6 }
