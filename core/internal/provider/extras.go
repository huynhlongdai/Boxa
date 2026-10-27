package provider

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"boxaz/core/internal/model"
)

// Region is a provider location.
type Region struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Available bool   `json:"available"`
}

// Server is a host as seen by the provider API.
type Server struct {
	Ref    string `json:"ref"`
	Name   string `json:"name"`
	IP     string `json:"ip"`
	SKU    string `json:"sku"`
	Region string `json:"region"`
	Status string `json:"status"` // provisioning|ready|down|unknown
}

// Regioner lists provider regions.
type Regioner interface {
	Regions(ctx context.Context, token string) ([]Region, error)
}

// Lister lists the servers Boxaz created at the provider.
type Lister interface {
	List(ctx context.Context, token string) ([]Server, error)
}

// EnvTokens maps provider id → env variables read by boxaz-core at startup.
// Multi-part credentials (Contabo) are joined into the provider's token format.
var EnvTokens = map[string][]string{
	"hetzner":      {"HETZNER_TOKEN"},
	"vultr":        {"VULTR_API_KEY"},
	"digitalocean": {"DIGITALOCEAN_TOKEN"},
	"contabo":      {"CONTABO_CLIENT_ID", "CONTABO_CLIENT_SECRET", "CONTABO_API_USER", "CONTABO_API_PASSWORD"},
	"netcup":       {"NETCUP_REFRESH_TOKEN"},
}

// TokenFromEnv builds a provider token from env values (getenv injected for tests).
// Returns "" when any required variable is missing.
func TokenFromEnv(id string, getenv func(string) string) string {
	keys, ok := EnvTokens[id]
	if !ok {
		return ""
	}
	var parts []string
	for _, k := range keys {
		v := strings.TrimSpace(getenv(k))
		if v == "" {
			return ""
		}
		parts = append(parts, v)
	}
	if id == "contabo" {
		return encodeContaboToken(contaboCreds{ClientID: parts[0], ClientSecret: parts[1], APIUser: parts[2], APIPassword: parts[3]})
	}
	return parts[0]
}

var tagRe = regexp.MustCompile(`[^a-zA-Z0-9_:\-.]`)

func sanitizeTag(s string) string { return tagRe.ReplaceAllString(s, "-") }

// DefaultChain is the scheduler fallback chain (pool first, then burst
// providers in this order): Hetzner pool → Vultr burst → DO / Contabo / netcup.
var DefaultChain = []string{"hetzner", "vultr", "digitalocean", "contabo", "netcup"}

func postForm(ctx context.Context, c *http.Client, u string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, "POST", u, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "sandbox-client/1")
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		s := string(b)
		if len(s) > 300 {
			s = s[:300]
		}
		return &apiErr{Status: resp.StatusCode, Body: strings.TrimSpace(s)}
	}
	return json.Unmarshal(b, out)
}

func newUUID() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func asAPI(err error, target **model.APIError) bool { return errors.As(err, target) }

func trimTo(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}
