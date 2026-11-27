package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"boxmes/core/internal/model"
	"boxmes/core/internal/secret"
)

// Contabo API v1 adapter (https://api.contabo.com). Auth is OAuth2 password
// grant: client id/secret + API user/password (Customer Control Panel → API).
// The Boxmes token is JSON {"client_id","client_secret","api_user","api_password"}
// or the four values joined by ":" (env CONTABO_CLIENT_ID, CONTABO_CLIENT_SECRET,
// CONTABO_API_USER, CONTABO_API_PASSWORD).
//
// Contabo bills monthly and has no price API: Offers returns a static catalog
// (confidence "unverified"); the battleships feed overrides it when present.
// Destroy = cancel (the instance is removed at the end of the paid term).
type Contabo struct {
	BaseURL string
	AuthURL string
	Image   string // standard image name to look up
	c       *http.Client

	mu     sync.Mutex
	tokens map[string]cachedToken
}

type cachedToken struct {
	access string
	exp    time.Time
}

type contaboCreds struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	APIUser      string `json:"api_user"`
	APIPassword  string `json:"api_password"`
}

func encodeContaboToken(c contaboCreds) string {
	b, _ := json.Marshal(c)
	return string(b)
}

func parseContaboToken(tok string) (contaboCreds, error) {
	var c contaboCreds
	tok = strings.TrimSpace(tok)
	if strings.HasPrefix(tok, "{") {
		if err := json.Unmarshal([]byte(tok), &c); err != nil {
			return c, model.EH(model.ErrInvalid, "Paste {\"client_id\",\"client_secret\",\"api_user\",\"api_password\"} or client_id:client_secret:api_user:api_password.", "invalid Contabo credentials JSON")
		}
	} else if parts := strings.SplitN(tok, ":", 4); len(parts) == 4 {
		c = contaboCreds{parts[0], parts[1], parts[2], parts[3]}
	}
	if c.ClientID == "" || c.ClientSecret == "" || c.APIUser == "" || c.APIPassword == "" {
		return c, model.EH(model.ErrInvalid, "Paste {\"client_id\",\"client_secret\",\"api_user\",\"api_password\"} or client_id:client_secret:api_user:api_password.", "Contabo needs client_id, client_secret, api_user and api_password")
	}
	return c, nil
}

func NewContabo(c *http.Client) *Contabo {
	return &Contabo{
		BaseURL: "https://api.contabo.com/v1",
		AuthURL: "https://auth.contabo.com/auth/realms/contabo/protocol/openid-connect/token",
		Image:   "ubuntu-24.04", c: c, tokens: map[string]cachedToken{},
	}
}

func (k *Contabo) ID() string        { return "contabo" }
func (k *Contabo) Name() string      { return "Contabo" }
func (k *Contabo) Tier() string      { return "T1" }
func (k *Contabo) Implemented() bool { return true }
func (k *Contabo) Caps() Caps        { return Caps{Sleep: true, Snapshot: true} }

// access exchanges the credentials for a bearer token (cached until expiry).
func (k *Contabo) access(ctx context.Context, token string) (string, error) {
	cr, err := parseContaboToken(token)
	if err != nil {
		return "", err
	}
	key := secret.Hash(token)
	k.mu.Lock()
	if t, ok := k.tokens[key]; ok && time.Now().Before(t.exp) {
		k.mu.Unlock()
		return t.access, nil
	}
	k.mu.Unlock()
	form := url.Values{"client_id": {cr.ClientID}, "client_secret": {cr.ClientSecret}, "username": {cr.APIUser}, "password": {cr.APIPassword}, "grant_type": {"password"}}
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := postForm(ctx, k.c, k.AuthURL, form, &out); err != nil {
		return "", authErr(err, "Contabo")
	}
	if out.AccessToken == "" {
		return "", model.EH(model.ErrUnauthorized, "Create API credentials in the Contabo customer panel and paste them again.", "Contabo returned no access token")
	}
	exp := time.Duration(out.ExpiresIn) * time.Second
	if exp <= 0 {
		exp = 5 * time.Minute
	}
	k.mu.Lock()
	k.tokens[key] = cachedToken{out.AccessToken, time.Now().Add(exp - 30*time.Second)}
	k.mu.Unlock()
	return out.AccessToken, nil
}

func (k *Contabo) do(ctx context.Context, token, method, path string, body, out any) error {
	at, err := k.access(ctx, token)
	if err != nil {
		return err
	}
	h := map[string]string{"Authorization": "Bearer " + at, "x-request-id": newUUID()}
	if err := doJSONH(ctx, k.c, method, k.BaseURL+path, h, body, out); err != nil {
		if _, ok := err.(*apiErr); ok {
			return authErr(err, "Contabo")
		}
		return err
	}
	return nil
}

func (k *Contabo) Validate(ctx context.Context, token string) error {
	return k.do(ctx, token, "GET", "/compute/instances?size=1", nil, nil)
}

// contaboCatalog: Cloud VPS line (NVMe), USD estimates as of 2026-10; monthly
// billing. Unverified: confirm on contabo.com before relying on the price.
var contaboCatalog = []struct {
	SKU        string
	CPU        float64
	RAMMB      int
	DiskGB     int
	PriceMonth float64
}{
	{"V91", 4, 8192, 75, 4.95},
	{"V94", 6, 12288, 100, 7.95},
	{"V97", 8, 24576, 200, 15.50},
	{"V100", 12, 49152, 250, 27.50},
}

var contaboRegions = []Region{
	{"EU", "European Union (Germany)", true}, {"UK", "United Kingdom", true},
	{"US-central", "US Central (St. Louis)", true}, {"US-east", "US East (New York)", true}, {"US-west", "US West (Seattle)", true},
	{"SIN", "Singapore", true}, {"JPN", "Japan (Tokyo)", true}, {"AUS", "Australia (Sydney)", true}, {"IND", "India (Mumbai)", true},
}

func (k *Contabo) Offers(ctx context.Context, token string) ([]model.Offer, error) {
	now := time.Now().UTC()
	var out []model.Offer
	for _, p := range contaboCatalog {
		for _, r := range contaboRegions {
			out = append(out, model.Offer{Provider: "contabo", SKU: p.SKU, Region: r.ID, CPU: p.CPU, RAMMB: p.RAMMB, DiskGB: p.DiskGB,
				PriceHour: model.Round6(p.PriceMonth / 730), PriceMonth: p.PriceMonth, MinBillingMinutes: 730 * 60,
				Source: "contabo-catalog", FetchedAt: now, Confidence: "unverified"})
		}
	}
	return out, nil
}

func (k *Contabo) Regions(ctx context.Context, token string) ([]Region, error) {
	return append([]Region(nil), contaboRegions...), nil
}

func (k *Contabo) imageID(ctx context.Context, token string) (string, error) {
	var out struct {
		Data []struct {
			ImageID string `json:"imageId"`
			Name    string `json:"name"`
		} `json:"data"`
	}
	if err := k.do(ctx, token, "GET", "/compute/images?standardImage=true&size=100&name="+url.QueryEscape(k.Image), nil, &out); err != nil {
		return "", err
	}
	for _, im := range out.Data {
		if strings.Contains(strings.ToLower(im.Name), k.Image) {
			return im.ImageID, nil
		}
	}
	return "", model.E(model.ErrProviderFailed, "Contabo image %q not found", k.Image)
}

type ctInstance struct {
	InstanceID  int64  `json:"instanceId"`
	DisplayName string `json:"displayName"`
	Status      string `json:"status"`
	ProductID   string `json:"productId"`
	Region      string `json:"region"`
	IPConfig    struct {
		V4 struct {
			IP string `json:"ip"`
		} `json:"v4"`
	} `json:"ipConfig"`
}

func ctState(s string) string {
	switch strings.ToLower(s) {
	case "running":
		return "ready"
	case "provisioning", "installing", "pending_payment", "manual_provisioning", "verification_required", "reset_password":
		return "provisioning"
	case "":
		return "unknown"
	default:
		return "down"
	}
}

func (k *Contabo) Create(ctx context.Context, token string, req CreateRequest) (Handle, error) {
	img, err := k.imageID(ctx, token)
	if err != nil {
		return Handle{}, err
	}
	body := map[string]any{
		"imageId": img, "productId": req.SKU, "region": req.Region, "period": 1,
		"displayName": req.Name, "userData": req.CloudInit, "defaultUser": "root",
	}
	var out struct {
		Data []ctInstance `json:"data"`
	}
	if err := k.do(ctx, token, "POST", "/compute/instances", body, &out); err != nil {
		return Handle{}, err
	}
	if len(out.Data) == 0 {
		return Handle{}, model.E(model.ErrProviderFailed, "Contabo returned no instance")
	}
	return Handle{Ref: strconv.FormatInt(out.Data[0].InstanceID, 10), IP: out.Data[0].IPConfig.V4.IP}, nil
}

func (k *Contabo) List(ctx context.Context, token string) ([]Server, error) {
	var out struct {
		Data []ctInstance `json:"data"`
	}
	if err := k.do(ctx, token, "GET", "/compute/instances?size=100", nil, &out); err != nil {
		return nil, err
	}
	var ss []Server
	for _, in := range out.Data {
		ss = append(ss, Server{Ref: strconv.FormatInt(in.InstanceID, 10), Name: in.DisplayName, IP: in.IPConfig.V4.IP, SKU: in.ProductID, Region: in.Region, Status: ctState(in.Status)})
	}
	return ss, nil
}

// Destroy cancels the instance (Contabo removes it at the end of the term).
func (k *Contabo) Destroy(ctx context.Context, token, ref string) error {
	err := k.do(ctx, token, "POST", "/compute/instances/"+url.PathEscape(ref)+"/cancel", map[string]any{}, nil)
	var ae *model.APIError
	if err != nil && asAPI(err, &ae) && strings.Contains(ae.Message, "404") {
		return nil
	}
	return err
}

func (k *Contabo) Status(ctx context.Context, token, ref string) (string, error) {
	var out struct {
		Data []ctInstance `json:"data"`
	}
	if err := k.do(ctx, token, "GET", "/compute/instances/"+url.PathEscape(ref), nil, &out); err != nil {
		var ae *model.APIError
		if asAPI(err, &ae) && strings.Contains(ae.Message, "404") {
			return "down", nil
		}
		return "unknown", err
	}
	if len(out.Data) == 0 {
		return "down", nil
	}
	return ctState(out.Data[0].Status), nil
}

func (k *Contabo) Sleep(ctx context.Context, token, ref string) error {
	return k.do(ctx, token, "POST", "/compute/instances/"+url.PathEscape(ref)+"/actions/stop", map[string]any{}, nil)
}
func (k *Contabo) Wake(ctx context.Context, token, ref string) error {
	return k.do(ctx, token, "POST", "/compute/instances/"+url.PathEscape(ref)+"/actions/start", map[string]any{}, nil)
}
func (k *Contabo) Snapshot(ctx context.Context, token, ref, name string) (string, error) {
	var out struct {
		Data []struct {
			SnapshotID string `json:"snapshotId"`
		} `json:"data"`
	}
	if err := k.do(ctx, token, "POST", "/compute/instances/"+url.PathEscape(ref)+"/snapshots", map[string]any{"name": trimTo(name, 30)}, &out); err != nil {
		return "", err
	}
	id := name
	if len(out.Data) > 0 && out.Data[0].SnapshotID != "" {
		id = out.Data[0].SnapshotID
	}
	return "contabo-snapshot:" + id, nil
}
