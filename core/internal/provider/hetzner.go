package provider

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"boxmes/core/internal/model"
)

// Hetzner Cloud API v1 adapter (https://docs.hetzner.cloud).
type Hetzner struct {
	BaseURL string
	Image   string
	c       *http.Client
}

func NewHetzner(c *http.Client) *Hetzner {
	return &Hetzner{BaseURL: "https://api.hetzner.cloud/v1", Image: "ubuntu-24.04", c: c}
}

func (h *Hetzner) ID() string        { return "hetzner" }
func (h *Hetzner) Name() string      { return "Hetzner Cloud" }
func (h *Hetzner) Tier() string      { return "T1" }
func (h *Hetzner) Implemented() bool { return true }
func (h *Hetzner) Caps() Caps        { return Caps{Sleep: true, Snapshot: true} }

// Validate checks the token can read the project. (Hetzner exposes no
// permission introspection; a read-only token passes here and fails on create.)
func (h *Hetzner) Validate(ctx context.Context, token string) error {
	var out struct {
		Locations []struct{ Name string } `json:"locations"`
	}
	if err := doJSON(ctx, h.c, "GET", h.BaseURL+"/locations", token, nil, &out); err != nil {
		return authErr(err, "Hetzner")
	}
	return nil
}

type hzPrice struct {
	Location     string                      `json:"location"`
	PriceHourly  struct{ Net, Gross string } `json:"price_hourly"`
	PriceMonthly struct{ Net, Gross string } `json:"price_monthly"`
}

type hzServerType struct {
	ID           int       `json:"id"`
	Name         string    `json:"name"`
	Cores        float64   `json:"cores"`
	Memory       float64   `json:"memory"`
	Disk         float64   `json:"disk"`
	Architecture string    `json:"architecture"`
	Deprecated   bool      `json:"deprecated"`
	Deprecation  any       `json:"deprecation"`
	Prices       []hzPrice `json:"prices"`
}

// Offers lists server types × locations with live prices (confidence verified).
func (h *Hetzner) Offers(ctx context.Context, token string) ([]model.Offer, error) {
	var all []model.Offer
	now := time.Now().UTC()
	for page := 1; page <= 10; page++ {
		var out struct {
			ServerTypes []hzServerType `json:"server_types"`
			Meta        struct {
				Pagination struct {
					NextPage *int `json:"next_page"`
				} `json:"pagination"`
			} `json:"meta"`
		}
		if err := doJSON(ctx, h.c, "GET", fmt.Sprintf("%s/server_types?per_page=50&page=%d", h.BaseURL, page), token, nil, &out); err != nil {
			return nil, authErr(err, "Hetzner")
		}
		for _, st := range out.ServerTypes {
			if st.Deprecated || st.Deprecation != nil {
				continue
			}
			for _, p := range st.Prices {
				ph, _ := strconv.ParseFloat(p.PriceHourly.Net, 64)
				pm, _ := strconv.ParseFloat(p.PriceMonthly.Net, 64)
				all = append(all, model.Offer{
					Provider: "hetzner", SKU: st.Name, Region: p.Location,
					CPU: st.Cores, RAMMB: int(st.Memory * 1024), DiskGB: int(st.Disk),
					PriceHour: ph, PriceMonth: pm, MinBillingMinutes: 60,
					Source: "hetzner-api", FetchedAt: now, Confidence: "verified",
				})
			}
		}
		if out.Meta.Pagination.NextPage == nil {
			break
		}
	}
	return all, nil
}

func (h *Hetzner) Create(ctx context.Context, token string, req CreateRequest) (Handle, error) {
	body := map[string]any{
		"name":               req.Name,
		"server_type":        req.SKU,
		"location":           req.Region,
		"image":              h.Image,
		"user_data":          req.CloudInit,
		"labels":             req.Labels,
		"start_after_create": true,
		"public_net":         map[string]bool{"enable_ipv4": true, "enable_ipv6": true},
	}
	var out struct {
		Server struct {
			ID        int64 `json:"id"`
			PublicNet struct {
				IPv4 struct {
					IP string `json:"ip"`
				} `json:"ipv4"`
			} `json:"public_net"`
		} `json:"server"`
	}
	if err := doJSON(ctx, h.c, "POST", h.BaseURL+"/servers", token, body, &out); err != nil {
		return Handle{}, authErr(err, "Hetzner")
	}
	return Handle{Ref: strconv.FormatInt(out.Server.ID, 10), IP: out.Server.PublicNet.IPv4.IP}, nil
}

func (h *Hetzner) Destroy(ctx context.Context, token, ref string) error {
	err := doJSON(ctx, h.c, "DELETE", h.BaseURL+"/servers/"+ref, token, nil, nil)
	if ae, ok := err.(*apiErr); ok && ae.Status == 404 {
		return nil
	}
	if err != nil {
		return authErr(err, "Hetzner")
	}
	return nil
}

func (h *Hetzner) Status(ctx context.Context, token, ref string) (string, error) {
	var out struct {
		Server struct {
			Status string `json:"status"`
		} `json:"server"`
	}
	if err := doJSON(ctx, h.c, "GET", h.BaseURL+"/servers/"+ref, token, nil, &out); err != nil {
		if ae, ok := err.(*apiErr); ok && ae.Status == 404 {
			return "down", nil
		}
		return "unknown", authErr(err, "Hetzner")
	}
	switch out.Server.Status {
	case "running":
		return "ready", nil
	case "initializing", "starting":
		return "provisioning", nil
	default:
		return "down", nil
	}
}

func (h *Hetzner) action(ctx context.Context, token, ref, act string) error {
	if err := doJSON(ctx, h.c, "POST", h.BaseURL+"/servers/"+ref+"/actions/"+act, token, map[string]any{}, nil); err != nil {
		return authErr(err, "Hetzner")
	}
	return nil
}

// Sleep powers the server off (Hetzner still bills stopped servers).
func (h *Hetzner) Sleep(ctx context.Context, token, ref string) error {
	return h.action(ctx, token, ref, "shutdown")
}
func (h *Hetzner) Wake(ctx context.Context, token, ref string) error {
	return h.action(ctx, token, ref, "poweron")
}

func (h *Hetzner) Snapshot(ctx context.Context, token, ref, name string) (string, error) {
	var out struct {
		Image struct {
			ID int64 `json:"id"`
		} `json:"image"`
	}
	if err := doJSON(ctx, h.c, "POST", h.BaseURL+"/servers/"+ref+"/actions/create_image", token, map[string]any{"type": "snapshot", "description": name}, &out); err != nil {
		return "", authErr(err, "Hetzner")
	}
	return "hetzner-image:" + strconv.FormatInt(out.Image.ID, 10), nil
}

func (h *Hetzner) Regions(ctx context.Context, token string) ([]Region, error) {
	var out struct {
		Locations []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"locations"`
	}
	if err := doJSON(ctx, h.c, "GET", h.BaseURL+"/locations", token, nil, &out); err != nil {
		return nil, authErr(err, "Hetzner")
	}
	var rs []Region
	for _, l := range out.Locations {
		rs = append(rs, Region{ID: l.Name, Name: l.Description, Available: true})
	}
	return rs, nil
}

func (h *Hetzner) List(ctx context.Context, token string) ([]Server, error) {
	var out struct {
		Servers []struct {
			ID         int64  `json:"id"`
			Name       string `json:"name"`
			Status     string `json:"status"`
			ServerType struct {
				Name string `json:"name"`
			} `json:"server_type"`
			Datacenter struct {
				Location struct {
					Name string `json:"name"`
				} `json:"location"`
			} `json:"datacenter"`
			PublicNet struct {
				IPv4 struct {
					IP string `json:"ip"`
				} `json:"ipv4"`
			} `json:"public_net"`
		} `json:"servers"`
	}
	if err := doJSON(ctx, h.c, "GET", h.BaseURL+"/servers?per_page=50&label_selector=managed", token, nil, &out); err != nil {
		return nil, authErr(err, "Hetzner")
	}
	var ss []Server
	for _, s := range out.Servers {
		st := "down"
		switch s.Status {
		case "running":
			st = "ready"
		case "initializing", "starting":
			st = "provisioning"
		}
		ss = append(ss, Server{Ref: strconv.FormatInt(s.ID, 10), Name: s.Name, IP: s.PublicNet.IPv4.IP, SKU: s.ServerType.Name, Region: s.Datacenter.Location.Name, Status: st})
	}
	return ss, nil
}
