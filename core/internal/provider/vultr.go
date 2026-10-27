package provider

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"boxaz/core/internal/model"
)

// Vultr API v2 adapter (https://www.vultr.com/api/).
type Vultr struct {
	BaseURL string
	OSName  string // matched against /v2/os names
	c       *http.Client
}

func NewVultr(c *http.Client) *Vultr {
	return &Vultr{BaseURL: "https://api.vultr.com/v2", OSName: "Ubuntu 24.04", c: c}
}

func (v *Vultr) ID() string        { return "vultr" }
func (v *Vultr) Name() string      { return "Vultr" }
func (v *Vultr) Tier() string      { return "T1" }
func (v *Vultr) Implemented() bool { return true }
func (v *Vultr) Caps() Caps        { return Caps{Sleep: true, Snapshot: true} }

func (v *Vultr) Validate(ctx context.Context, token string) error {
	var out struct {
		Account struct {
			Email string   `json:"email"`
			ACLs  []string `json:"acls"`
		} `json:"account"`
	}
	if err := doJSON(ctx, v.c, "GET", v.BaseURL+"/account", token, nil, &out); err != nil {
		return authErr(err, "Vultr")
	}
	// ACLs empty = full access (owner). Otherwise require manage_servers.
	if len(out.Account.ACLs) > 0 {
		ok := false
		for _, a := range out.Account.ACLs {
			if a == "manage_servers" {
				ok = true
			}
		}
		if !ok {
			return model.EH(model.ErrUnauthorized, "Give the API user the 'manage_servers' permission in Vultr.", "Vultr token lacks manage_servers")
		}
	}
	return nil
}

type vultrPlan struct {
	ID          string   `json:"id"`
	VCPU        float64  `json:"vcpu_count"`
	RAM         int      `json:"ram"`
	Disk        int      `json:"disk"`
	MonthlyCost float64  `json:"monthly_cost"`
	HourlyCost  float64  `json:"hourly_cost"`
	Type        string   `json:"type"`
	Locations   []string `json:"locations"`
}

func (v *Vultr) Offers(ctx context.Context, token string) ([]model.Offer, error) {
	var all []model.Offer
	now := time.Now().UTC()
	cursor := ""
	for i := 0; i < 10; i++ {
		u := v.BaseURL + "/plans?per_page=500&type=all"
		if cursor != "" {
			u += "&cursor=" + url.QueryEscape(cursor)
		}
		var out struct {
			Plans []vultrPlan `json:"plans"`
			Meta  struct {
				Links struct {
					Next string `json:"next"`
				} `json:"links"`
			} `json:"meta"`
		}
		if err := doJSON(ctx, v.c, "GET", u, token, nil, &out); err != nil {
			return nil, authErr(err, "Vultr")
		}
		for _, p := range out.Plans {
			if strings.HasPrefix(p.Type, "vcg") || strings.Contains(p.ID, "gpu") {
				continue // GPU out of scope for v1
			}
			ph := p.HourlyCost
			if ph == 0 && p.MonthlyCost > 0 {
				ph = p.MonthlyCost / 730
			}
			for _, loc := range p.Locations {
				all = append(all, model.Offer{
					Provider: "vultr", SKU: p.ID, Region: loc, CPU: p.VCPU, RAMMB: p.RAM, DiskGB: p.Disk,
					PriceHour: ph, PriceMonth: p.MonthlyCost, MinBillingMinutes: 60,
					Source: "vultr-api", FetchedAt: now, Confidence: "verified",
				})
			}
		}
		if out.Meta.Links.Next == "" {
			break
		}
		cursor = out.Meta.Links.Next
	}
	return all, nil
}

func (v *Vultr) osID(ctx context.Context, token string) (int, error) {
	var out struct {
		OS []struct {
			ID     int    `json:"id"`
			Name   string `json:"name"`
			Arch   string `json:"arch"`
			Family string `json:"family"`
		} `json:"os"`
	}
	if err := doJSON(ctx, v.c, "GET", v.BaseURL+"/os?per_page=500", token, nil, &out); err != nil {
		return 0, authErr(err, "Vultr")
	}
	for _, o := range out.OS {
		if strings.Contains(o.Name, v.OSName) && o.Arch == "x64" {
			return o.ID, nil
		}
	}
	return 0, model.E(model.ErrProviderFailed, "Vultr OS %q not found", v.OSName)
}

func (v *Vultr) Create(ctx context.Context, token string, req CreateRequest) (Handle, error) {
	osID, err := v.osID(ctx, token)
	if err != nil {
		return Handle{}, err
	}
	tags := []string{"managed"}
	for k, val := range req.Labels {
		tags = append(tags, fmt.Sprintf("%s=%s", k, val))
	}
	body := map[string]any{
		"region":    req.Region,
		"plan":      req.SKU,
		"os_id":     osID,
		"label":     req.Name,
		"hostname":  req.Name,
		"user_data": base64.StdEncoding.EncodeToString([]byte(req.CloudInit)),
		"tags":      tags,
	}
	var out struct {
		Instance struct {
			ID     string `json:"id"`
			MainIP string `json:"main_ip"`
		} `json:"instance"`
	}
	if err := doJSON(ctx, v.c, "POST", v.BaseURL+"/instances", token, body, &out); err != nil {
		return Handle{}, authErr(err, "Vultr")
	}
	return Handle{Ref: out.Instance.ID, IP: out.Instance.MainIP}, nil
}

func (v *Vultr) Destroy(ctx context.Context, token, ref string) error {
	err := doJSON(ctx, v.c, "DELETE", v.BaseURL+"/instances/"+url.PathEscape(ref), token, nil, nil)
	if ae, ok := err.(*apiErr); ok && ae.Status == 404 {
		return nil
	}
	if err != nil {
		return authErr(err, "Vultr")
	}
	return nil
}

func (v *Vultr) Status(ctx context.Context, token, ref string) (string, error) {
	var out struct {
		Instance struct {
			Status       string `json:"status"`
			PowerStatus  string `json:"power_status"`
			ServerStatus string `json:"server_status"`
		} `json:"instance"`
	}
	if err := doJSON(ctx, v.c, "GET", v.BaseURL+"/instances/"+url.PathEscape(ref), token, nil, &out); err != nil {
		if ae, ok := err.(*apiErr); ok && ae.Status == 404 {
			return "down", nil
		}
		return "unknown", authErr(err, "Vultr")
	}
	if out.Instance.Status == "active" && out.Instance.PowerStatus == "running" {
		return "ready", nil
	}
	if out.Instance.Status == "pending" {
		return "provisioning", nil
	}
	return "down", nil
}

func (v *Vultr) Sleep(ctx context.Context, token, ref string) error {
	if err := doJSON(ctx, v.c, "POST", v.BaseURL+"/instances/"+url.PathEscape(ref)+"/halt", token, map[string]any{}, nil); err != nil {
		return authErr(err, "Vultr")
	}
	return nil
}

func (v *Vultr) Wake(ctx context.Context, token, ref string) error {
	if err := doJSON(ctx, v.c, "POST", v.BaseURL+"/instances/"+url.PathEscape(ref)+"/start", token, map[string]any{}, nil); err != nil {
		return authErr(err, "Vultr")
	}
	return nil
}

func (v *Vultr) Regions(ctx context.Context, token string) ([]Region, error) {
	var out struct {
		Regions []struct {
			ID      string `json:"id"`
			City    string `json:"city"`
			Country string `json:"country"`
		} `json:"regions"`
	}
	if err := doJSON(ctx, v.c, "GET", v.BaseURL+"/regions?per_page=500", token, nil, &out); err != nil {
		return nil, authErr(err, "Vultr")
	}
	var rs []Region
	for _, r := range out.Regions {
		rs = append(rs, Region{ID: r.ID, Name: r.City + " (" + r.Country + ")", Available: true})
	}
	return rs, nil
}

func (v *Vultr) List(ctx context.Context, token string) ([]Server, error) {
	var out struct {
		Instances []struct {
			ID          string `json:"id"`
			Label       string `json:"label"`
			MainIP      string `json:"main_ip"`
			Plan        string `json:"plan"`
			Region      string `json:"region"`
			Status      string `json:"status"`
			PowerStatus string `json:"power_status"`
		} `json:"instances"`
	}
	if err := doJSON(ctx, v.c, "GET", v.BaseURL+"/instances?per_page=500&tag=managed", token, nil, &out); err != nil {
		return nil, authErr(err, "Vultr")
	}
	var ss []Server
	for _, in := range out.Instances {
		st := "down"
		if in.Status == "active" && in.PowerStatus == "running" {
			st = "ready"
		} else if in.Status == "pending" {
			st = "provisioning"
		}
		ss = append(ss, Server{Ref: in.ID, Name: in.Label, IP: in.MainIP, SKU: in.Plan, Region: in.Region, Status: st})
	}
	return ss, nil
}
