package provider

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"boxmes/core/internal/model"
	"boxmes/core/internal/secret"
)

// netcup adapter for the SCP REST API (https://www.servercontrolpanel.de/scp-core/api/v1,
// OpenID Connect, client "scp"). The Boxmes token is an SCP refresh token
// (env NETCUP_REFRESH_TOKEN), exchanged for short-lived access tokens.
//
// netcup cannot order servers over the API (orders go through the CCP and
// bill monthly). Create therefore CLAIMS a pre-ordered spare server — one
// whose nickname starts with "mgr-spare" (legacy "boxa-spare" is still claimed) — reinstalls Ubuntu with the Boxmes
// cloud-init and renames it; Destroy powers it off and returns it to the
// spare pool (cancel the contract in the CCP to stop paying).
// Prices come from a static catalog (confidence "unverified").
type Netcup struct {
	BaseURL     string
	AuthURL     string
	SparePrefix string
	ImageName   string
	c           *http.Client

	mu     sync.Mutex
	tokens map[string]cachedToken
}

func NewNetcup(c *http.Client) *Netcup {
	return &Netcup{
		BaseURL:     "https://www.servercontrolpanel.de/scp-core/api/v1",
		AuthURL:     "https://www.servercontrolpanel.de/realms/scp/protocol/openid-connect/token",
		SparePrefix: "mgr-spare", ImageName: "Ubuntu 24.04", c: c, tokens: map[string]cachedToken{},
	}
}

func (n *Netcup) ID() string        { return "netcup" }
func (n *Netcup) Name() string      { return "netcup" }
func (n *Netcup) Tier() string      { return "T1" }
func (n *Netcup) Implemented() bool { return true }
func (n *Netcup) Caps() Caps        { return Caps{Sleep: true} }

func (n *Netcup) access(ctx context.Context, refresh string) (string, error) {
	refresh = strings.TrimSpace(refresh)
	if refresh == "" {
		return "", model.EH(model.ErrInvalid, "Create an SCP refresh token (device login for client 'scp') and paste it.", "netcup refresh token is empty")
	}
	key := secret.Hash(refresh)
	n.mu.Lock()
	if t, ok := n.tokens[key]; ok && time.Now().Before(t.exp) {
		n.mu.Unlock()
		return t.access, nil
	}
	n.mu.Unlock()
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	form := url.Values{"client_id": {"scp"}, "grant_type": {"refresh_token"}, "refresh_token": {refresh}}
	if err := postForm(ctx, n.c, n.AuthURL, form, &out); err != nil {
		return "", authErr(err, "netcup")
	}
	if out.AccessToken == "" {
		return "", model.EH(model.ErrUnauthorized, "Create an SCP refresh token (device login for client 'scp') and paste it.", "netcup returned no access token")
	}
	exp := time.Duration(out.ExpiresIn) * time.Second
	if exp <= 0 {
		exp = 5 * time.Minute
	}
	n.mu.Lock()
	n.tokens[key] = cachedToken{out.AccessToken, time.Now().Add(exp - 30*time.Second)}
	n.mu.Unlock()
	return out.AccessToken, nil
}

func (n *Netcup) do(ctx context.Context, token, method, path string, body, out any) error {
	at, err := n.access(ctx, token)
	if err != nil {
		return err
	}
	h := map[string]string{"Authorization": "Bearer " + at}
	if method == "PATCH" {
		h["Content-Type"] = "application/merge-patch+json"
	}
	if err := doJSONH(ctx, n.c, method, n.BaseURL+path, h, body, out); err != nil {
		if _, ok := err.(*apiErr); ok {
			return authErr(err, "netcup")
		}
		return err
	}
	return nil
}

func (n *Netcup) Validate(ctx context.Context, token string) error {
	return n.do(ctx, token, "GET", "/servers?limit=1", nil, nil)
}

// netcupCatalog: VPS / root server line, USD estimates as of 2026-10
// (monthly, 1-month term). Unverified.
var netcupCatalog = []struct {
	SKU        string
	CPU        float64
	RAMMB      int
	DiskGB     int
	PriceMonth float64
}{
	{"VPS 1000 G11", 4, 8192, 256, 7.40},
	{"VPS 2000 G11", 8, 16384, 512, 13.90},
	{"RS 1000 G12", 4, 8192, 256, 10.90},
	{"RS 2000 G12", 8, 16384, 512, 18.90},
}

var netcupRegions = []Region{
	{"nue", "Nuremberg (DE)", true}, {"vie", "Vienna (AT)", true}, {"ams", "Amsterdam (NL)", true}, {"man", "Manassas (US)", true},
}

func (n *Netcup) Offers(ctx context.Context, token string) ([]model.Offer, error) {
	now := time.Now().UTC()
	var out []model.Offer
	for _, p := range netcupCatalog {
		for _, r := range netcupRegions {
			out = append(out, model.Offer{Provider: "netcup", SKU: p.SKU, Region: r.ID, CPU: p.CPU, RAMMB: p.RAMMB, DiskGB: p.DiskGB,
				PriceHour: model.Round6(p.PriceMonth / 730), PriceMonth: p.PriceMonth, MinBillingMinutes: 730 * 60,
				Source: "netcup-catalog", FetchedAt: now, Confidence: "unverified"})
		}
	}
	return out, nil
}

func (n *Netcup) Regions(ctx context.Context, token string) ([]Region, error) {
	return append([]Region(nil), netcupRegions...), nil
}

type ncServer struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Nickname string `json:"nickname"`
	Hostname string `json:"hostname"`
	Template struct {
		Name string `json:"name"`
	} `json:"template"`
	Site struct {
		City string `json:"city"`
	} `json:"site"`
	ServerLiveInfo struct {
		State string `json:"state"`
	} `json:"serverLiveInfo"`
	IPv4Addresses []struct {
		IP string `json:"ip"`
	} `json:"ipv4Addresses"`
}

func (s ncServer) ip() string {
	if len(s.IPv4Addresses) > 0 {
		return s.IPv4Addresses[0].IP
	}
	return ""
}

func ncState(s string) string {
	switch strings.ToUpper(s) {
	case "RUNNING", "ON":
		return "ready"
	case "":
		return "unknown"
	case "INSTALLING", "PENDING":
		return "provisioning"
	default:
		return "down"
	}
}

func (n *Netcup) servers(ctx context.Context, token string) ([]ncServer, error) {
	var out []ncServer
	if err := n.do(ctx, token, "GET", "/servers?limit=200", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (n *Netcup) List(ctx context.Context, token string) ([]Server, error) {
	ss, err := n.servers(ctx, token)
	if err != nil {
		return nil, err
	}
	var out []Server
	for _, s := range ss {
		out = append(out, Server{Ref: strconv.FormatInt(s.ID, 10), Name: firstNonEmpty(s.Nickname, s.Name), IP: s.ip(), SKU: s.Template.Name, Region: s.Site.City, Status: ncState(s.ServerLiveInfo.State)})
	}
	return out, nil
}

func (n *Netcup) imageFlavour(ctx context.Context, token, ref string) (int64, error) {
	var out []struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	}
	if err := n.do(ctx, token, "GET", "/servers/"+url.PathEscape(ref)+"/imageflavours", nil, &out); err != nil {
		return 0, err
	}
	for _, f := range out {
		if strings.Contains(strings.ToLower(f.Name), strings.ToLower(n.ImageName)) {
			return f.ID, nil
		}
	}
	return 0, model.E(model.ErrProviderFailed, "netcup image %q not offered for server %s", n.ImageName, ref)
}

// legacySparePrefix: spare servers nicknamed before the Boxmes rename (provider-visible,
// so new nicknames use the neutral "mgr-spare").
const legacySparePrefix = "boxa-spare"

// Create claims a spare server (nickname prefix mgr-spare or legacy boxa-spare, SKU match when the
// template name is known) and reinstalls it with the Boxmes cloud-init.
func (n *Netcup) Create(ctx context.Context, token string, req CreateRequest) (Handle, error) {
	ss, err := n.servers(ctx, token)
	if err != nil {
		return Handle{}, err
	}
	var pick *ncServer
	for i := range ss {
		s := ss[i]
		if !strings.HasPrefix(s.Nickname, n.SparePrefix) && !strings.HasPrefix(s.Nickname, legacySparePrefix) {
			continue
		}
		if req.SKU != "" && s.Template.Name != "" && !strings.EqualFold(s.Template.Name, req.SKU) {
			continue
		}
		pick = &ss[i]
		break
	}
	if pick == nil {
		return Handle{}, model.EH(model.ErrNoCapacity, "Order the server in the netcup CCP and set its nickname to mgr-spare-1; Boxmes claims spare servers.", "no spare netcup server (nickname %s*) matches %s", n.SparePrefix, req.SKU)
	}
	ref := strconv.FormatInt(pick.ID, 10)
	flavour, err := n.imageFlavour(ctx, token, ref)
	if err != nil {
		return Handle{}, err
	}
	if err := n.do(ctx, token, "PATCH", "/servers/"+ref, map[string]any{"nickname": req.Name}, nil); err != nil {
		return Handle{}, err
	}
	body := map[string]any{"imageFlavourId": flavour, "diskName": "vda", "rootPartitionFullDiskSize": true, "customScript": req.CloudInit, "emailToExecutingUser": false}
	if err := n.do(ctx, token, "POST", "/servers/"+ref+"/image", body, nil); err != nil {
		return Handle{}, err
	}
	return Handle{Ref: ref, IP: pick.ip()}, nil
}

// Destroy powers the server off and returns it to the spare pool.
func (n *Netcup) Destroy(ctx context.Context, token, ref string) error {
	if err := n.do(ctx, token, "PATCH", "/servers/"+url.PathEscape(ref), map[string]any{"state": "OFF"}, nil); err != nil {
		var ae *model.APIError
		if asAPI(err, &ae) && strings.Contains(ae.Message, "404") {
			return nil
		}
		return err
	}
	return n.do(ctx, token, "PATCH", "/servers/"+url.PathEscape(ref), map[string]any{"nickname": n.SparePrefix + "-" + ref}, nil)
}

func (n *Netcup) Status(ctx context.Context, token, ref string) (string, error) {
	var s ncServer
	if err := n.do(ctx, token, "GET", "/servers/"+url.PathEscape(ref), nil, &s); err != nil {
		var ae *model.APIError
		if asAPI(err, &ae) && strings.Contains(ae.Message, "404") {
			return "down", nil
		}
		return "unknown", err
	}
	return ncState(s.ServerLiveInfo.State), nil
}

func (n *Netcup) Sleep(ctx context.Context, token, ref string) error {
	return n.do(ctx, token, "PATCH", "/servers/"+url.PathEscape(ref), map[string]any{"state": "OFF"}, nil)
}
func (n *Netcup) Wake(ctx context.Context, token, ref string) error {
	return n.do(ctx, token, "PATCH", "/servers/"+url.PathEscape(ref), map[string]any{"state": "ON"}, nil)
}
