package manager

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"boxmes/core/internal/boxyaml"
	"boxmes/core/internal/model"
	"boxmes/core/internal/store"
)

// ---- secrets (M10) ----

var secretNameRe = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,63}$`)

type SecretInput struct {
	Name   string   `json:"name"`
	Value  string   `json:"value"`
	Agents []string `json:"agents"`
}

func (m *Manager) ListSecrets(ctx context.Context, p *Principal) ([]*model.Secret, error) {
	var all []*model.Secret
	var err error
	if ws := p.ScopeWS(); ws != "" {
		all, err = m.Store.ListSecretsInWorkspace(ctx, ws)
	} else {
		all, err = m.Store.ListSecrets(ctx)
	}
	if err != nil {
		return nil, err
	}
	if p.Platform {
		return all, nil
	}
	out := []*model.Secret{}
	if p.Admin && p.Can(PermSecretsRead) != nil {
		return out, nil
	}
	for _, s := range all {
		if s.System {
			continue // only the platform operator sees Boxmes's own credentials
		}
		if p.Admin || len(s.Agents) == 0 || contains(s.Agents, p.AgentID) {
			out = append(out, s)
		}
	}
	return out, nil
}

// secretIn finds a non-system secret of p's target workspace.
func (m *Manager) secretIn(ctx context.Context, p *Principal, name string) (*model.Secret, string, error) {
	ws, err := p.TargetWS()
	if err != nil {
		return nil, "", err
	}
	s, err := m.Store.GetSecretIn(ctx, ws, name)
	if err != nil || (s.System && !p.Platform) {
		return nil, ws, notFound("secret", name)
	}
	return s, ws, nil
}

func (m *Manager) CreateSecret(ctx context.Context, p *Principal, in SecretInput) (*model.Secret, error) {
	if err := p.Can(PermSecretsWrite); err != nil {
		return nil, err
	}
	ws, err := p.TargetWS()
	if err != nil {
		return nil, err
	}
	if !secretNameRe.MatchString(in.Name) {
		return nil, model.E(model.ErrInvalid, "name must be UPPER_SNAKE_CASE")
	}
	if in.Value == "" {
		return nil, model.E(model.ErrInvalid, "value is required")
	}
	if model.IsSystemSecretName(in.Name) {
		return nil, model.EH(model.ErrInvalid, "Configure Boxmes credentials in Settings (notifications, storage) instead.", "secret names starting with BOXMES_SYS_ are reserved")
	}
	if _, err := m.Store.GetSecretIn(ctx, ws, in.Name); err == nil {
		return nil, model.EH(model.ErrInvalid, "Use POST /secrets/:name/rotate to change the value.", "secret %s already exists", in.Name)
	}
	if err := m.quotaCount(ctx, ws, "max_secrets"); err != nil {
		return nil, err
	}
	agents, err := m.resolveAgentIDs(ctx, p, ws, in.Agents)
	if err != nil {
		return nil, err
	}
	enc, err := m.Crypt.Seal(in.Value)
	if err != nil {
		return nil, err
	}
	s := &model.Secret{Name: in.Name, WorkspaceID: ws, Agents: agents, CreatedAt: time.Now().UTC(), ValueEnc: enc}
	if err := m.Store.PutSecret(ctx, s); err != nil {
		return nil, err
	}
	m.EmitWS(ctx, ws, p.Actor(), "", "", "secret.created", "secret "+in.Name+" created", nil, nil)
	return s, nil
}

// resolveAgentIDs maps agent ids/names of workspace ws to ids.
func (m *Manager) resolveAgentIDs(ctx context.Context, p *Principal, ws string, in []string) ([]string, error) {
	out := []string{}
	for _, a := range in {
		ag, err := m.Store.GetAgent(ctx, a)
		if err != nil || ag.WorkspaceID != ws {
			ag, err = m.Store.GetAgentInWorkspace(ctx, ws, a)
			if err != nil {
				return nil, notFound("agent", a)
			}
		}
		out = append(out, ag.ID)
	}
	return out, nil
}

func (m *Manager) RotateSecret(ctx context.Context, p *Principal, name, value string, agents []string) (*model.Secret, error) {
	if err := p.Can(PermSecretsWrite); err != nil {
		return nil, err
	}
	s, ws, err := m.secretIn(ctx, p, name)
	if err != nil {
		return nil, err
	}
	if value == "" {
		return nil, model.E(model.ErrInvalid, "value is required")
	}
	enc, err := m.Crypt.Seal(value)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	s.ValueEnc, s.RotatedAt = enc, &now
	if agents != nil {
		if s.Agents, err = m.resolveAgentIDs(ctx, p, ws, agents); err != nil {
			return nil, err
		}
	}
	if err := m.Store.PutSecret(ctx, s); err != nil {
		return nil, err
	}
	m.EmitWS(ctx, ws, p.Actor(), "", "", "secret.rotated", "secret "+name+" rotated", nil, nil)
	return s, nil
}

func (m *Manager) DeleteSecret(ctx context.Context, p *Principal, name string) error {
	if err := p.Can(PermSecretsWrite); err != nil {
		return err
	}
	_, ws, err := m.secretIn(ctx, p, name)
	if err != nil {
		return err
	}
	m.EmitWS(ctx, ws, p.Actor(), "", "", "secret.deleted", "secret "+name+" deleted", nil, nil)
	return m.Store.DeleteSecretIn(ctx, ws, name)
}

// ---- templates (M14) ----

type TemplateInput struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	YAML        string `json:"yaml"`
}

// ListTemplates: built-ins plus the caller's workspace templates.
func (m *Manager) ListTemplates(ctx context.Context, p *Principal) ([]*model.Template, error) {
	return m.Store.ListTemplatesIn(ctx, p.ScopeWS())
}

// templateFor resolves a template by name for box creation in workspace ws.
func (m *Manager) templateFor(ctx context.Context, ws, name string) (*model.Template, error) {
	return m.Store.GetTemplateIn(ctx, ws, name)
}

func (m *Manager) PutTemplate(ctx context.Context, p *Principal, in TemplateInput, create bool) (*model.Template, error) {
	if err := p.Can(PermTemplates); err != nil {
		return nil, err
	}
	if !p.Admin {
		return nil, p.Require(model.ScopeAdmin)
	}
	ws, err := p.TargetWS()
	if err != nil {
		return nil, err
	}
	if !regexp.MustCompile(`^[a-z0-9][a-z0-9.\-_]{0,63}$`).MatchString(in.Name) {
		return nil, model.E(model.ErrInvalid, "invalid template name")
	}
	if _, errs := boxyaml.Parse(in.YAML); errs != nil {
		return nil, model.EH(model.ErrInvalid, "Validate with POST /templates/validate.", "yaml: %s", errs.Error())
	}
	found, err := m.Store.GetTemplateIn(ctx, ws, in.Name)
	var mine *model.Template
	builtin := err == nil && found.Builtin
	if err == nil && !found.Builtin && found.WorkspaceID == ws {
		mine = found
	}
	// ws_default shares row ids with the built-ins (pre-v2.0 layout), so a
	// built-in name stays read-only there; other workspaces may shadow one.
	if builtin && mine == nil && (ws == model.DefaultWorkspaceID || !create) {
		return nil, model.EH(model.ErrInvalid, "Create a new template that extends the built-in one (template: <name>) instead.", "built-in templates are read-only")
	}
	if create && mine != nil {
		return nil, model.E(model.ErrInvalid, "template %s already exists", in.Name)
	}
	if !create && mine == nil {
		return nil, notFound("template", in.Name)
	}
	t := &model.Template{Name: in.Name, WorkspaceID: ws, Description: in.Description, YAML: in.YAML, CreatedAt: time.Now().UTC()}
	if mine != nil {
		t.CreatedAt, t.Uses = mine.CreatedAt, mine.Uses
	}
	if err := m.Store.PutTemplate(ctx, t); err != nil {
		return nil, err
	}
	m.EmitWS(ctx, ws, p.Actor(), "", "", "template.saved", "template "+in.Name+" saved", nil, nil)
	return t, nil
}

func (m *Manager) DeleteTemplate(ctx context.Context, p *Principal, name string) error {
	if err := p.Can(PermTemplates); err != nil {
		return err
	}
	if !p.Admin {
		return p.Require(model.ScopeAdmin)
	}
	ws, err := p.TargetWS()
	if err != nil {
		return err
	}
	t, err := m.Store.GetTemplateIn(ctx, ws, name)
	if err != nil {
		return notFound("template", name)
	}
	if t.Builtin {
		return model.E(model.ErrInvalid, "built-in templates cannot be deleted")
	}
	m.EmitWS(ctx, ws, p.Actor(), "", "", "template.deleted", "template "+name+" deleted", nil, nil)
	return m.Store.DeleteTemplateIn(ctx, ws, name)
}

// ---- admin ----

type Component struct {
	Name      string  `json:"name"`
	Status    string  `json:"status"` // ok|degraded|down|disabled
	LatencyMS float64 `json:"latency_ms"`
	Detail    string  `json:"detail"`
}

func (m *Manager) Components(ctx context.Context) []Component {
	var out []Component
	t := time.Now()
	err := m.Store.Ping(ctx)
	c := Component{Name: "db", Status: "ok", LatencyMS: float64(time.Since(t).Microseconds()) / 1000, Detail: m.Store.Dialect()}
	if err != nil {
		c.Status, c.Detail = "down", err.Error()
	}
	out = append(out, c)
	jobComp := func(name, comp string, every time.Duration) Component {
		j := m.sysJob(name)
		c := Component{Name: comp, Status: "ok"}
		if j == nil {
			c.Status, c.Detail = "down", "not started"
			return c
		}
		c.LatencyMS = float64(j.DurationMS)
		switch {
		case j.LastStatus == "disabled":
			c.Status, c.Detail = "disabled", "disabled by config"
		case j.LastStatus == "error":
			c.Status, c.Detail = "degraded", j.LastError
		case j.LastRunAt != nil && time.Since(*j.LastRunAt) > 3*every+time.Minute:
			c.Status, c.Detail = "degraded", "last run "+j.LastRunAt.Format(time.RFC3339)
		default:
			if j.LastRunAt != nil {
				c.Detail = "last run " + j.LastRunAt.Format(time.RFC3339)
			}
		}
		return c
	}
	out = append(out, Component{Name: "scheduler", Status: "ok", Detail: fmt.Sprintf("%d hosts connected, auto_burst=%v", m.Hub.Count(), m.Cfg.AutoBurst)})
	out = append(out, jobComp("reaper", "reaper", m.Cfg.ReaperInterval))
	out = append(out, jobComp("ledger", "ledger", m.Cfg.LedgerInterval))
	out = append(out, jobComp("price_feed", "price_feed", m.Cfg.PriceFeedInterval))
	out = append(out, Component{Name: "gateway", Status: "ok", Detail: fmt.Sprintf("%d SSE subscribers, %d host tunnels", m.Broker.Count(), m.Hub.Count())})
	return out
}

// Audit lists events: platform-wide for the operator, else the caller's
// workspace (agent keys: their agent only).
func (m *Manager) Audit(ctx context.Context, p *Principal, f store.EventFilter) ([]*model.Event, error) {
	if !p.Admin {
		f.AgentID = p.AgentID
	}
	f.WorkspaceID = p.ScopeWS()
	return m.Store.ListEvents(ctx, f)
}

// WorkspaceAudit is GET /v1/workspaces/current/audit (owner, admin, auditor).
func (m *Manager) WorkspaceAudit(ctx context.Context, p *Principal, f store.EventFilter) ([]*model.Event, error) {
	if err := p.Can(PermAudit); err != nil {
		return nil, err
	}
	ws, err := p.TargetWS()
	if err != nil {
		return nil, err
	}
	f.WorkspaceID = ws
	return m.Store.ListEvents(ctx, f)
}

func (m *Manager) Users(ctx context.Context) []map[string]any {
	keys, _ := m.Store.ListKeys(ctx, "")
	admin := 0
	for _, k := range keys {
		if k.RevokedAt == nil && k.HasScope(model.ScopeAdmin) {
			admin++
		}
	}
	return []map[string]any{{"id": "owner", "name": "owner", "role": "admin", "admin_keys": admin, "note": "Boxmes v1 is single-operator; agents authenticate with agent keys."}}
}

func (m *Manager) Uptime() time.Duration { return time.Since(m.startedAt) }

var _ = strings.TrimSpace
