package sandbox

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"boxmes/core/internal/model"
)

// Render is the render.com driver (REST API https://api.render.com/v1,
// Bearer API key; reference https://api-docs.render.com, OpenAPI
// render-public-api-1.json). Render has no exec or file API for services,
// so a box is a web service running a stock image whose start command
// downloads boxd and runs `boxd --dial` in the foreground (Info.DialOnly):
// exec, files, terminal, TCP and snapshots all go through boxd. boxd also
// holds the service port ($PORT, health path below) so Render sees the
// deploy as live, and forwards other requests to the box's preview port,
// which makes https://<service>.onrender.com the box's public preview URL.
//
// Everything sent to Render is neutral: service name mgr-<host>, env names
// MGR_BOOT / BOXD_*, the stock image, the sandbox User-Agent.
type Render struct {
	h    *httpClient
	base string
	info Info
	mu   sync.Mutex
	own  map[string]string // api key → owner (workspace) id
	urls map[string]string // service id → https://<name>.onrender.com
}

// RenderDefaultImage is the stock image a box runs (bash, curl, git, ssh
// client, ca-certificates). Override with the credential field "image" or
// RENDER_AGENT_IMAGE (any public image with /bin/sh and curl or wget).
const RenderDefaultImage = "docker.io/library/buildpack-deps:bookworm-scm"

// RenderHealthPath is answered by boxd's port holder (cmd/boxd).
const RenderHealthPath = "/.well-known/mgr-health"

// renderDefaultPreviewPort is the in-box port published at onrender.com.
const renderDefaultPreviewPort = 8000

// renderEphemeralGB: Render does not document the ephemeral filesystem size
// (instances that exceed it are evicted); 20 GB is an estimate so the
// coding-agent profile (disk 20) fits. disk_gb attaches a real disk.
const renderEphemeralGB = 20

func renderSize(sku string, cpu float64, ram int, month float64, note string) Size {
	return Size{SKU: sku, CPU: cpu, RAMMB: ram, DiskGB: renderEphemeralGB, FixedPrice: true, PriceHour: model.Round6(month / 730), Note: note}
}

// NewRender returns the driver (base "" = https://api.render.com/v1).
func NewRender(c *http.Client, base string) *Render {
	r := &Render{h: newHTTP(c), base: strings.TrimRight(orStr(base, "https://api.render.com/v1"), "/"), own: map[string]string{}, urls: map[string]string{}}
	r.info = Info{ID: "render", Name: "Render", Tier: "T2", Adapter: "native", Verification: VerifiedDocs,
		VerifiedNote: "Routes and fields from the official OpenAPI spec (api-docs.render.com, render-public-api-1.json, 2026-11-19): GET /owners, POST/GET/DELETE /services, GET /services/{id}/deploys, POST /services/{id}/suspend|resume. Tested against a mock server only; no live run yet (no API key).",
		Docs:         "https://api-docs.render.com/reference/create-service", Website: "https://render.com", Initials: "RD", Color: "#4351e8",
		// No exec API: dial-in only. Container platform (shared kernel).
		Caps: Capabilities{Background: true, Ports: true, BootMS: 90000, MaxCPU: 8, MaxRAMMB: 32768, Isolation: "container"},
		Pricing: Pricing{Source: "https://render.com/pricing", Confidence: "docs",
			Note: "flat monthly price per instance type, prorated by the second; free = 750 instance hours per workspace per month"},
		Sizes: []Size{
			renderSize("free", 0.1, 512, 0, "free: spins down after 15 min without inbound traffic (files lost), ~1 min cold start, 750 h/month per workspace"),
			renderSize("0.5c-512mb", 0.5, 512, 7, "starter"),
			renderSize("1c-2g", 1, 2048, 25, "standard"),
			renderSize("2c-4g", 2, 4096, 85, "pro"),
			renderSize("2c-8g", 2, 8192, 135, ""),
			renderSize("4c-8g", 4, 8192, 175, "pro_plus"),
			renderSize("4c-16g", 4, 16384, 225, "pro_max"),
			renderSize("8c-16g", 8, 16384, 300, ""),
			renderSize("8c-32g", 8, 32768, 450, "pro_ultra"),
		},
		Regions: []Region{{ID: "oregon", Name: "Oregon (US West)"}, {ID: "ohio", Name: "Ohio (US East)"}, {ID: "virginia", Name: "Virginia (US East)"},
			{ID: "frankfurt", Name: "Frankfurt (EU)"}, {ID: "singapore", Name: "Singapore (Asia)"}},
		Templates: []string{RenderDefaultImage}, DefaultTemplate: RenderDefaultImage, WorkDir: "/srv/w",
		Credentials: []CredField{
			{Name: "token", Label: "API key", Secret: true, Hint: "rnd_…"},
			{Name: "owner_id", Label: "Workspace ID (optional: first workspace of the key)", Optional: true, Hint: "tea-… / usr-…"},
			{Name: "image", Label: "Agent image (optional)", Optional: true, Hint: RenderDefaultImage},
			{Name: "disk_gb", Label: "Persistent disk GB (optional, paid plans only)", Optional: true, Hint: "0"},
			{Name: "preview_port", Label: "In-box port served at onrender.com (optional, 0 = none)", Optional: true, Hint: "8000"},
			{Name: "keep_awake", Label: "Keep free services awake while the box exists (optional, 1 or 0)", Optional: true, Hint: "1"},
		},
		EnvVars: []string{"RENDER_API_KEY", "RENDER_OWNER_ID", "RENDER_AGENT_IMAGE", "RENDER_DISK_GB"},
		Notes: []string{
			"No exec API: each box is a Render web service running boxd, which dials out to core (needs BOXMES_PUBLIC_URL reachable from the internet).",
			"Public preview: https://<service>.onrender.com serves in-box port 8000 (credential field preview_port); other ports go through the Boxmes gateway.",
			"Free plan: 0.1 CPU / 512 MB, spins down after 15 min without inbound traffic (boxd pings its own URL to stay up while the box exists), ~1 min cold start, local files lost on spin-down or restart.",
			"Paid instance types need a payment method on the Render workspace (API answers 402). A persistent disk (disk_gb, $0.25/GB-month) keeps the box's files across restarts.",
			"The ephemeral disk size is not documented by Render; the catalog assumes 20 GB (estimate).",
			"Sleep keeps the service running (boxd stops the box's processes); delete removes the service. No provider snapshot or fork; Boxmes snapshots are boxd tarballs.",
		},
		DialOnly: true, EphemeralRestart: true,
	}
	return r
}

func (r *Render) Info() Info { return r.info }

func (r *Render) api(c Cred) string { return strings.TrimRight(c.Get("base_url", r.base), "/") }

func (r *Render) hdr(c Cred) map[string]string {
	return map[string]string{"Authorization": "Bearer " + c.Token(), "Accept": "application/json"}
}

type renderOwner struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

func (r *Render) owners(ctx context.Context, c Cred) ([]renderOwner, error) {
	var out []struct {
		Owner renderOwner `json:"owner"`
	}
	if err := r.h.do(ctx, "GET", r.api(c)+"/owners?limit=100", r.hdr(c), nil, &out); err != nil {
		return nil, err
	}
	l := []renderOwner{}
	for _, o := range out {
		if o.Owner.ID != "" {
			l = append(l, o.Owner)
		}
	}
	return l, nil
}

// owner is the workspace boxes are created in: the credential's owner_id,
// else the key's first workspace (cached per key).
func (r *Render) owner(ctx context.Context, c Cred) (string, error) {
	if id := strings.TrimSpace(c["owner_id"]); id != "" {
		return id, nil
	}
	r.mu.Lock()
	id := r.own[c.Token()]
	r.mu.Unlock()
	if id != "" {
		return id, nil
	}
	l, err := r.owners(ctx, c)
	if err != nil {
		return "", err
	}
	if len(l) == 0 {
		return "", model.EH(model.ErrInvalid, "The Render API key sees no workspace; create the key in the workspace that should run boxes.", "render: GET /owners returned no workspace")
	}
	r.mu.Lock()
	r.own[c.Token()] = l[0].ID
	r.mu.Unlock()
	return l[0].ID, nil
}

func (r *Render) Validate(ctx context.Context, c Cred) error {
	l, err := r.owners(ctx, c)
	if err != nil {
		return err
	}
	if len(l) == 0 {
		return model.EH(model.ErrInvalid, "The Render API key sees no workspace; create the key in the workspace that should run boxes.", "render: GET /owners returned no workspace")
	}
	if want := strings.TrimSpace(c["owner_id"]); want != "" {
		for _, o := range l {
			if o.ID == want {
				return nil
			}
		}
		return model.EH(model.ErrInvalid, "Use a workspace ID from Render → Workspace settings that this API key can access.", "render: workspace %s is not visible to this key", want)
	}
	return nil
}

// previewPort is the in-box port served at onrender.com; preview_port=0 (or
// "off") publishes nothing there (boxd answers only the health path).
func (r *Render) previewPort(c Cred) int {
	if v := strings.TrimSpace(c["preview_port"]); v == "0" || strings.EqualFold(v, "off") {
		return 0
	}
	if n, err := strconv.Atoi(strings.TrimSpace(c["preview_port"])); err == nil && n > 0 && n < 65536 {
		return n
	}
	return renderDefaultPreviewPort
}

type renderService struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Type           string `json:"type"`
	Suspended      string `json:"suspended"`
	DashboardURL   string `json:"dashboardUrl"`
	ServiceDetails struct {
		URL    string `json:"url"`
		Plan   string `json:"plan"`
		Region string `json:"region"`
	} `json:"serviceDetails"`
}

func (r *Render) remember(s renderService) {
	if s.ID != "" && s.ServiceDetails.URL != "" {
		r.mu.Lock()
		r.urls[s.ID] = s.ServiceDetails.URL
		r.mu.Unlock()
	}
}

// renderDeployStates maps the newest deploy's status.
var renderDeployStates = map[string]string{"live": StateRunning, "build_failed": StateError, "update_failed": StateError,
	"pre_deploy_failed": StateError, "canceled": StateError}

func (r *Render) Create(ctx context.Context, c Cred, s Spec) (*Sandbox, error) {
	if strings.TrimSpace(s.Boot) == "" {
		return nil, model.EH(model.ErrInvalid, "This provider has no exec API: boxes run only in dial mode (set BOXMES_PUBLIC_URL).", "render: create needs the boxd start command")
	}
	owner, err := r.owner(ctx, c)
	if err != nil {
		return nil, err
	}
	plan := orStr(s.SKU, "free")
	image := orStr(c["image"], orStr(s.Template, RenderDefaultImage))
	env := []map[string]string{
		{"key": "MGR_BOOT", "value": s.Boot},
		// boxd expands these itself (Render does not interpolate env values)
		{"key": "BOXD_LISTEN_PORT", "value": "${PORT}"},
		{"key": "BOXD_HEALTH_PATH", "value": RenderHealthPath},
		{"key": "BOXD_PREVIEW_PORT", "value": strconv.Itoa(r.previewPort(c))},
	}
	// A free web service spins down after 15 min without inbound traffic and
	// loses its files: boxd requests its own public URL every few minutes
	// while it runs (counts as inbound), unless keep_awake=0.
	if plan == "free" && c["keep_awake"] != "0" {
		env = append(env, map[string]string{"key": "BOXD_KEEPALIVE_URL", "value": "${RENDER_EXTERNAL_URL}" + RenderHealthPath})
	}
	for k, v := range s.Env {
		if validEnvName(k) {
			env = append(env, map[string]string{"key": k, "value": v})
		}
	}
	details := map[string]any{
		"runtime": "image", "plan": plan, "numInstances": 1, "healthCheckPath": RenderHealthPath,
		// Render passes everything after `-c` to the shell as one argument
		// (render.com/docs/docker: "/bin/sh -c cmd1 && cmd2").
		"envSpecificDetails": map[string]any{"dockerCommand": `/bin/sh -c eval "$MGR_BOOT"`},
	}
	if s.Region != "" {
		details["region"] = s.Region
	}
	if gb, _ := strconv.Atoi(strings.TrimSpace(c["disk_gb"])); gb > 0 && plan != "free" {
		details["disk"] = map[string]any{"name": "data", "mountPath": r.info.WorkDir, "sizeGB": gb}
	}
	body := map[string]any{"type": "web_service", "name": trim(s.Name, 60), "ownerId": owner,
		"image": map[string]any{"ownerId": owner, "imagePath": image}, "envVars": env, "serviceDetails": details}
	var out struct {
		Service  renderService `json:"service"`
		DeployID string        `json:"deployId"`
	}
	if err := r.h.do(ctx, "POST", r.api(c)+"/services", r.hdr(c), body, &out); err != nil {
		if he, ok := err.(*HTTPError); ok && he.Status == 402 {
			return nil, model.EH(model.ErrProviderFailed, "Add a payment method to the Render workspace (paid instance types need one) or use the free plan.", "render: payment information required (402): %s", trim(he.Body, 200))
		}
		return nil, err
	}
	if out.Service.ID == "" {
		return nil, fmt.Errorf("create answered without service.id")
	}
	r.remember(out.Service)
	return &Sandbox{ID: out.Service.ID, State: StateStarting, Region: out.Service.ServiceDetails.Region, Template: image,
		Meta: map[string]string{"url": out.Service.ServiceDetails.URL, "deploy": out.DeployID, "plan": plan}}, nil
}

func (r *Render) Get(ctx context.Context, c Cred, id string) (*Sandbox, error) {
	var svc renderService
	if err := r.h.do(ctx, "GET", r.api(c)+"/services/"+url.PathEscape(id), r.hdr(c), nil, &svc); err != nil {
		if he, ok := err.(*HTTPError); ok && (he.Status == 404 || he.Status == 410) {
			return &Sandbox{ID: id, State: StateDeleted}, nil
		}
		return nil, err
	}
	r.remember(svc)
	sb := &Sandbox{ID: id, Region: svc.ServiceDetails.Region, Meta: map[string]string{"url": svc.ServiceDetails.URL, "plan": svc.ServiceDetails.Plan, "suspended": svc.Suspended}}
	if svc.Suspended == "suspended" {
		sb.State = StateStopped
		return sb, nil
	}
	var deps []struct {
		Deploy struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"deploy"`
	}
	if err := r.h.do(ctx, "GET", r.api(c)+"/services/"+url.PathEscape(id)+"/deploys?limit=1", r.hdr(c), nil, &deps); err != nil {
		return nil, err
	}
	sb.State = StateStarting
	if len(deps) > 0 {
		sb.Meta["deploy_status"] = deps[0].Deploy.Status
		if st := renderDeployStates[deps[0].Deploy.Status]; st != "" {
			sb.State = st
		}
	}
	return sb, nil
}

func (r *Render) Delete(ctx context.Context, c Cred, id string) error {
	err := r.h.do(ctx, "DELETE", r.api(c)+"/services/"+url.PathEscape(id), r.hdr(c), nil, nil)
	r.mu.Lock()
	delete(r.urls, id)
	r.mu.Unlock()
	return err
}

// Exec: Render has no exec API for services (one-off jobs start a new
// instance; SSH needs a paid plan and an SSH key). Everything runs through
// boxd, which the provider starts itself.
func (r *Render) Exec(ctx context.Context, c Cred, id string, req ExecRequest) (*ExecResult, error) {
	return nil, model.EH(model.ErrInvalid, "This provider has no exec API: boxes run only in dial mode (set BOXMES_PUBLIC_URL).", "render has no exec API (service %s)", id)
}

// Stop suspends the service (instance stopped, files lost unless a disk is
// attached); Start resumes it (a fresh instance runs the start command).
func (r *Render) Stop(ctx context.Context, c Cred, id string) error {
	return r.h.do(ctx, "POST", r.api(c)+"/services/"+url.PathEscape(id)+"/suspend", r.hdr(c), nil, nil)
}

func (r *Render) Start(ctx context.Context, c Cred, id string) error {
	return r.h.do(ctx, "POST", r.api(c)+"/services/"+url.PathEscape(id)+"/resume", r.hdr(c), nil, nil)
}

// PreviewURL is the service's onrender.com URL for the published port (boxd
// forwards it to that in-box port); other ports have no provider URL.
func (r *Render) PreviewURL(ctx context.Context, c Cred, id string, port int) (string, error) {
	if pp := r.previewPort(c); pp == 0 || port != pp {
		return "", fmt.Errorf("render publishes only in-box port %d at onrender.com", pp)
	}
	r.mu.Lock()
	u := r.urls[id]
	r.mu.Unlock()
	if u == "" {
		sb, err := r.Get(ctx, c, id)
		if err != nil {
			return "", err
		}
		u = sb.Meta["url"]
	}
	if u == "" {
		return "", fmt.Errorf("render service %s has no URL yet", id)
	}
	return strings.TrimRight(u, "/"), nil
}

// List returns the services this adapter created (name prefix mgr-).
func (r *Render) List(ctx context.Context, c Cred) ([]Sandbox, error) {
	q := "?limit=100&type=web_service"
	if id := strings.TrimSpace(c["owner_id"]); id != "" {
		q += "&ownerId=" + url.QueryEscape(id)
	}
	var out []struct {
		Service renderService `json:"service"`
	}
	if err := r.h.do(ctx, "GET", r.api(c)+"/services"+q, r.hdr(c), nil, &out); err != nil {
		return nil, err
	}
	l := []Sandbox{}
	for _, it := range out {
		s := it.Service
		if !strings.HasPrefix(s.Name, NamePrefix) {
			continue
		}
		r.remember(s)
		st := StateRunning
		if s.Suspended == "suspended" {
			st = StateStopped
		}
		l = append(l, Sandbox{ID: s.ID, State: st, Region: s.ServiceDetails.Region, Template: s.ServiceDetails.Plan, Meta: map[string]string{"name": s.Name, "url": s.ServiceDetails.URL}})
	}
	return l, nil
}
