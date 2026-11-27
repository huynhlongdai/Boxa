package manager

// v1.3 optional headscale mesh. Disabled (the default) = every call is a
// graceful no-op that says how to enable it.

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"

	"boxmes/core/internal/mesh"
	"boxmes/core/internal/model"
	"boxmes/core/internal/store"
)

// MeshConfig configures the optional headscale mesh (env BOXMES_MESH_*).
type MeshConfig struct {
	Enabled      bool
	HeadscaleURL string // control server URL boxd/tailscale log in to, e.g. https://mesh.example.com
	APIKey       string // headscale API key (headscale apikeys create)
	User         string // headscale user that owns Boxmes nodes (default "boxmes")
	BaseDomain   string // MagicDNS domain (default boxmes.mesh)
	DERPHost     string // optional extra self-hosted DERP relay hostname
	HTTP         *http.Client
}

func (m *Manager) meshCfg() mesh.Config {
	c := m.Cfg.Mesh
	return mesh.Config{ServerURL: c.HeadscaleURL, APIKey: c.APIKey, User: c.User, BaseDomain: c.BaseDomain, EmbeddedDERP: true, DERPHost: c.DERPHost}
}

func (m *Manager) meshClient() *mesh.Client {
	return &mesh.Client{BaseURL: m.Cfg.Mesh.HeadscaleURL, APIKey: m.Cfg.Mesh.APIKey, HTTP: m.Cfg.Mesh.HTTP}
}

func (m *Manager) meshEnabled() bool {
	return m.Cfg.Mesh.Enabled && m.Cfg.Mesh.HeadscaleURL != "" && m.Cfg.Mesh.APIKey != ""
}

func (m *Manager) meshUser() string {
	if m.Cfg.Mesh.User != "" {
		return m.Cfg.Mesh.User
	}
	return "boxmes"
}

// MeshView is GET /mesh.
type MeshView struct {
	Enabled     bool        `json:"enabled"`
	URL         string      `json:"url,omitempty"`
	User        string      `json:"user,omitempty"`
	Nodes       []mesh.Node `json:"nodes"`
	Online      int         `json:"online"`
	HostsJoined int         `json:"hosts_joined"`
	Hosts       int         `json:"hosts"`
	ACLRules    int         `json:"acl_rules"`
	DERP        string      `json:"derp"`
	Error       string      `json:"error,omitempty"`
	Hint        string      `json:"hint,omitempty"`
}

// MeshStatus reports the mesh (graceful when disabled).
func (m *Manager) MeshStatus(ctx context.Context, p *Principal) (*MeshView, error) {
	if err := p.Require(model.ScopeAdmin); err != nil {
		return nil, err
	}
	v := &MeshView{Nodes: []mesh.Node{}}
	hosts, _ := m.ListHosts(ctx)
	v.Hosts = len(hosts)
	for _, h := range hosts {
		if h.MeshIP != "" {
			v.HostsJoined++
		}
	}
	if !m.meshEnabled() {
		v.Hint = "Mesh is off. Set BOXMES_MESH_ENABLED=true, BOXMES_MESH_URL (headscale server_url) and BOXMES_MESH_API_KEY, then restart core; hosts join at their next connect."
		return v, nil
	}
	v.Enabled, v.URL, v.User = true, m.Cfg.Mesh.HeadscaleURL, m.meshUser()
	v.DERP = "embedded + Tailscale public DERP map"
	if m.Cfg.Mesh.DERPHost != "" {
		v.DERP += " + " + m.Cfg.Mesh.DERPHost
	}
	pol := mesh.GeneratePolicy(m.meshCfg(), m.meshBoxes(ctx))
	v.ACLRules = len(pol.ACLs)
	c, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	nodes, err := m.meshClient().Nodes(c, m.meshUser())
	if err != nil {
		v.Error = err.Error()
		return v, nil
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Name < nodes[j].Name })
	v.Nodes = nodes
	for _, n := range nodes {
		if n.Online {
			v.Online++
		}
	}
	return v, nil
}

func (m *Manager) meshBoxes(ctx context.Context) []mesh.Box {
	boxes, _ := m.Store.ListBoxes(ctx, store.BoxFilter{})
	var out []mesh.Box
	for _, b := range boxes {
		if !b.Active() {
			continue
		}
		mb := mesh.Box{ID: b.ID, AgentID: b.AgentID, Ports: b.Ports}
		if b.Egress != nil {
			mb.EgressAllow, mb.DenyByDefault = b.Egress.Allow, b.Egress.DenyByDefault
		}
		out = append(out, mb)
	}
	return out
}

// MeshConfigYAML returns generated headscale config.yaml, derp.yaml and acl.json.
func (m *Manager) MeshConfigYAML(ctx context.Context, p *Principal) (map[string]any, error) {
	if err := p.Require(model.ScopeAdmin); err != nil {
		return nil, err
	}
	cfg := m.meshCfg()
	if cfg.ServerURL == "" {
		cfg.ServerURL = "https://mesh.example.com"
	}
	y, err := mesh.RenderConfig(cfg)
	if err != nil {
		return nil, err
	}
	pol := mesh.GeneratePolicy(cfg, m.meshBoxes(ctx))
	return map[string]any{"enabled": m.meshEnabled(), "config": y, "derp": mesh.RenderDERP(cfg), "acl": pol.JSON(),
		"paths": map[string]string{"config": "/etc/headscale/config.yaml", "derp": "/etc/headscale/derp.yaml", "acl": "/etc/headscale/acl.json"}}, nil
}

// MeshACL returns the generated policy and notes about entries it cannot express.
func (m *Manager) MeshACL(ctx context.Context, p *Principal) (map[string]any, error) {
	if err := p.Require(model.ScopeAdmin); err != nil {
		return nil, err
	}
	pol := mesh.GeneratePolicy(m.meshCfg(), m.meshBoxes(ctx))
	notes := pol.Notes
	if notes == nil {
		notes = []string{}
	}
	return map[string]any{"enabled": m.meshEnabled(), "policy": pol, "notes": notes}, nil
}

// MeshKeyInput is POST /mesh/keys.
type MeshKeyInput struct {
	HostID     string `json:"host_id,omitempty"`
	BoxID      string `json:"box_id,omitempty"`
	Reusable   bool   `json:"reusable"`
	TTLMinutes int    `json:"ttl_minutes"`
}

// MeshKeyResult carries a pre-auth key (shown once) and the join command.
type MeshKeyResult struct {
	Key         string    `json:"key"`
	Tags        []string  `json:"tags"`
	Ephemeral   bool      `json:"ephemeral"`
	ExpiresAt   time.Time `json:"expires_at"`
	LoginServer string    `json:"login_server"`
	Command     string    `json:"command"`
}

// MeshKey creates a pre-auth key for a host (tag:boxmes-host) or a box
// (tag:agent-<agent>, tag:box-<id>, ephemeral).
func (m *Manager) MeshKey(ctx context.Context, p *Principal, in MeshKeyInput) (*MeshKeyResult, error) {
	if err := p.Require(model.ScopeAdmin); err != nil {
		return nil, err
	}
	if !m.meshEnabled() {
		return nil, model.EH(model.ErrInvalid, "Set BOXMES_MESH_ENABLED, BOXMES_MESH_URL and BOXMES_MESH_API_KEY first.", "mesh is disabled")
	}
	tags := []string{mesh.TagHost}
	ephemeral := false
	host := "boxmes-node"
	switch {
	case in.BoxID != "":
		b, err := m.GetBox(ctx, p, in.BoxID)
		if err != nil {
			return nil, err
		}
		tags, ephemeral, host = []string{mesh.AgentTag(b.AgentID), mesh.BoxTag(b.ID)}, true, "box-"+b.Slug
	case in.HostID != "":
		h, err := m.Store.GetHost(ctx, in.HostID)
		if err != nil {
			return nil, notFound("host", in.HostID)
		}
		host = "host-" + strings.TrimPrefix(h.ID, "host_")
	}
	ttl := time.Duration(in.TTLMinutes) * time.Minute
	k, err := m.meshKeyFor(ctx, tags, in.Reusable, ephemeral, ttl)
	if err != nil {
		return nil, err
	}
	m.Emit(ctx, p.Actor(), in.BoxID, "", "mesh.key_created", "pre-auth key for "+host+" ("+strings.Join(tags, ",")+")", nil)
	return &MeshKeyResult{Key: k.Key, Tags: tags, Ephemeral: ephemeral, ExpiresAt: k.Expiration, LoginServer: m.Cfg.Mesh.HeadscaleURL,
		Command: "tailscale " + strings.Join(mesh.TailscaleUpArgs(m.Cfg.Mesh.HeadscaleURL, k.Key, host, tags), " ")}, nil
}

func (m *Manager) meshKeyFor(ctx context.Context, tags []string, reusable, ephemeral bool, ttl time.Duration) (*mesh.PreAuthKey, error) {
	c, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cl := m.meshClient()
	if err := cl.EnsureUser(c, m.meshUser()); err != nil {
		return nil, model.EH(model.ErrProviderFailed, "Check BOXMES_MESH_URL / BOXMES_MESH_API_KEY and that headscale is running.", "headscale: %v", err)
	}
	k, err := cl.CreatePreAuthKey(c, m.meshUser(), tags, reusable, ephemeral, ttl)
	if err != nil {
		return nil, model.EH(model.ErrProviderFailed, "Check BOXMES_MESH_URL / BOXMES_MESH_API_KEY and that headscale is running.", "headscale: %v", err)
	}
	return k, nil
}

// meshJoin returns join parameters for a connecting host (nil when disabled
// or headscale is unreachable — the host simply runs without the mesh).
func (m *Manager) meshJoin(ctx context.Context, h *model.Host) *meshJoinInfo {
	if !m.meshEnabled() || h.MeshIP != "" {
		return nil
	}
	k, err := m.meshKeyFor(ctx, []string{mesh.TagHost}, false, false, 30*time.Minute)
	if err != nil {
		m.Emit(ctx, "system", "", "", "mesh.join_failed", "host "+h.ID+": "+err.Error(), nil)
		return nil
	}
	return &meshJoinInfo{LoginServer: m.Cfg.Mesh.HeadscaleURL, AuthKey: k.Key, Hostname: "host-" + strings.TrimPrefix(h.ID, "host_"), Tags: []string{mesh.TagHost}}
}

type meshJoinInfo struct {
	LoginServer string
	AuthKey     string
	Hostname    string
	Tags        []string
}
