package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"boxmes/core/internal/model"
)

// DigitalOcean API v2 adapter (https://docs.digitalocean.com/reference/api/).
// Token: a personal access token with read+write scope (env DIGITALOCEAN_TOKEN).
type DigitalOcean struct {
	BaseURL string
	Image   string
	c       *http.Client
}

func NewDigitalOcean(c *http.Client) *DigitalOcean {
	return &DigitalOcean{BaseURL: "https://api.digitalocean.com/v2", Image: "ubuntu-24-04-x64", c: c}
}

func (d *DigitalOcean) ID() string        { return "digitalocean" }
func (d *DigitalOcean) Name() string      { return "DigitalOcean" }
func (d *DigitalOcean) Tier() string      { return "T1" }
func (d *DigitalOcean) Implemented() bool { return true }
func (d *DigitalOcean) Caps() Caps        { return Caps{Sleep: true, Snapshot: true} }

func (d *DigitalOcean) Validate(ctx context.Context, token string) error {
	var out struct {
		Account struct {
			Status string `json:"status"`
		} `json:"account"`
	}
	if err := doJSON(ctx, d.c, "GET", d.BaseURL+"/account", token, nil, &out); err != nil {
		return authErr(err, "DigitalOcean")
	}
	if out.Account.Status != "" && out.Account.Status != "active" {
		return model.EH(model.ErrUnauthorized, "Activate the DigitalOcean account (billing/verification) and retry.", "DigitalOcean account status %s", out.Account.Status)
	}
	return nil
}

type doSize struct {
	Slug         string   `json:"slug"`
	VCPUs        float64  `json:"vcpus"`
	Memory       int      `json:"memory"`
	Disk         int      `json:"disk"`
	PriceHourly  float64  `json:"price_hourly"`
	PriceMonthly float64  `json:"price_monthly"`
	Regions      []string `json:"regions"`
	Available    bool     `json:"available"`
}

func (d *DigitalOcean) Offers(ctx context.Context, token string) ([]model.Offer, error) {
	var all []model.Offer
	now := time.Now().UTC()
	for page := 1; page <= 10; page++ {
		var out struct {
			Sizes []doSize `json:"sizes"`
			Links struct {
				Pages struct {
					Next string `json:"next"`
				} `json:"pages"`
			} `json:"links"`
		}
		if err := doJSON(ctx, d.c, "GET", fmt.Sprintf("%s/sizes?per_page=200&page=%d", d.BaseURL, page), token, nil, &out); err != nil {
			return nil, authErr(err, "DigitalOcean")
		}
		for _, s := range out.Sizes {
			if !s.Available || s.Slug == "" {
				continue
			}
			for _, r := range s.Regions {
				all = append(all, model.Offer{
					Provider: "digitalocean", SKU: s.Slug, Region: r, CPU: s.VCPUs, RAMMB: s.Memory, DiskGB: s.Disk,
					PriceHour: s.PriceHourly, PriceMonth: s.PriceMonthly, MinBillingMinutes: 1,
					Source: "digitalocean-api", FetchedAt: now, Confidence: "verified",
				})
			}
		}
		if out.Links.Pages.Next == "" {
			break
		}
	}
	return all, nil
}

func (d *DigitalOcean) Regions(ctx context.Context, token string) ([]Region, error) {
	var out struct {
		Regions []struct {
			Slug      string `json:"slug"`
			Name      string `json:"name"`
			Available bool   `json:"available"`
		} `json:"regions"`
	}
	if err := doJSON(ctx, d.c, "GET", d.BaseURL+"/regions?per_page=200", token, nil, &out); err != nil {
		return nil, authErr(err, "DigitalOcean")
	}
	var rs []Region
	for _, r := range out.Regions {
		rs = append(rs, Region{ID: r.Slug, Name: r.Name, Available: r.Available})
	}
	return rs, nil
}

type doDroplet struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Status   string `json:"status"`
	SizeSlug string `json:"size_slug"`
	Region   struct {
		Slug string `json:"slug"`
	} `json:"region"`
	Networks struct {
		V4 []struct {
			IP   string `json:"ip_address"`
			Type string `json:"type"`
		} `json:"v4"`
	} `json:"networks"`
}

func (dr doDroplet) publicIP() string {
	for _, n := range dr.Networks.V4 {
		if n.Type == "public" {
			return n.IP
		}
	}
	return ""
}

func doState(s string) string {
	switch s {
	case "active":
		return "ready"
	case "new":
		return "provisioning"
	default:
		return "down"
	}
}

func (d *DigitalOcean) Create(ctx context.Context, token string, req CreateRequest) (Handle, error) {
	tags := []string{"managed"}
	for k, v := range req.Labels {
		tags = append(tags, sanitizeTag(k+"-"+v))
	}
	body := map[string]any{
		"name": req.Name, "region": req.Region, "size": req.SKU, "image": d.Image,
		"user_data": req.CloudInit, "tags": tags, "ipv6": true, "monitoring": false,
	}
	var out struct {
		Droplet doDroplet `json:"droplet"`
	}
	if err := doJSON(ctx, d.c, "POST", d.BaseURL+"/droplets", token, body, &out); err != nil {
		return Handle{}, authErr(err, "DigitalOcean")
	}
	return Handle{Ref: strconv.FormatInt(out.Droplet.ID, 10), IP: out.Droplet.publicIP()}, nil
}

func (d *DigitalOcean) List(ctx context.Context, token string) ([]Server, error) {
	var out struct {
		Droplets []doDroplet `json:"droplets"`
	}
	if err := doJSON(ctx, d.c, "GET", d.BaseURL+"/droplets?per_page=200&tag_name=managed", token, nil, &out); err != nil {
		return nil, authErr(err, "DigitalOcean")
	}
	var ss []Server
	for _, dr := range out.Droplets {
		ss = append(ss, Server{Ref: strconv.FormatInt(dr.ID, 10), Name: dr.Name, IP: dr.publicIP(), SKU: dr.SizeSlug, Region: dr.Region.Slug, Status: doState(dr.Status)})
	}
	return ss, nil
}

func (d *DigitalOcean) Destroy(ctx context.Context, token, ref string) error {
	err := doJSON(ctx, d.c, "DELETE", d.BaseURL+"/droplets/"+url.PathEscape(ref), token, nil, nil)
	if ae, ok := err.(*apiErr); ok && ae.Status == 404 {
		return nil
	}
	if err != nil {
		return authErr(err, "DigitalOcean")
	}
	return nil
}

func (d *DigitalOcean) Status(ctx context.Context, token, ref string) (string, error) {
	var out struct {
		Droplet doDroplet `json:"droplet"`
	}
	if err := doJSON(ctx, d.c, "GET", d.BaseURL+"/droplets/"+url.PathEscape(ref), token, nil, &out); err != nil {
		if ae, ok := err.(*apiErr); ok && ae.Status == 404 {
			return "down", nil
		}
		return "unknown", authErr(err, "DigitalOcean")
	}
	return doState(out.Droplet.Status), nil
}

func (d *DigitalOcean) action(ctx context.Context, token, ref string, body map[string]any) error {
	if err := doJSON(ctx, d.c, "POST", d.BaseURL+"/droplets/"+url.PathEscape(ref)+"/actions", token, body, nil); err != nil {
		return authErr(err, "DigitalOcean")
	}
	return nil
}

// Sleep powers the droplet off (DigitalOcean still bills powered-off droplets).
func (d *DigitalOcean) Sleep(ctx context.Context, token, ref string) error {
	return d.action(ctx, token, ref, map[string]any{"type": "power_off"})
}
func (d *DigitalOcean) Wake(ctx context.Context, token, ref string) error {
	return d.action(ctx, token, ref, map[string]any{"type": "power_on"})
}
func (d *DigitalOcean) Snapshot(ctx context.Context, token, ref, name string) (string, error) {
	if err := d.action(ctx, token, ref, map[string]any{"type": "snapshot", "name": name}); err != nil {
		return "", err
	}
	return "digitalocean-snapshot:" + name, nil
}
