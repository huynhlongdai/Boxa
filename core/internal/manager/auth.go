package manager

import (
	"boxmes/core/internal/i18n"
	"context"
	"errors"
	"regexp"
	"sort"
	"strings"
	"time"

	"boxmes/core/internal/model"
	"boxmes/core/internal/secret"
	"boxmes/core/internal/store"
)

// Principal is the authenticated caller (v2.0 tenancy: see scope.go).
type Principal struct {
	Key     *model.APIKey
	AgentID string // set for agent keys: the caller only sees its own agent's records
	// Admin: not limited to one agent. True for the platform operator and for
	// every workspace-wide caller (session members, workspace keys); what
	// they may do is then decided by Can (role or key scopes).
	Admin bool
	// Platform: global operator (admin key, platform admin account). Sees
	// every workspace unless FilterWS (?workspace=) narrows it.
	Platform bool
	FilterWS string
	// v2 tenancy: set when the caller signed in with a session (bxs_ token).
	User        *model.User
	UserID      string
	WorkspaceID string // the caller's workspace (session: current, key: owner)
	Role        string // owner|admin|developer|billing|viewer|auditor in WorkspaceID
	SessionID   string
	session     *model.Session
	perms       map[string]bool // workspace permissions of non-platform callers
}

func (p *Principal) Actor() string {
	if p == nil || p.Key == nil {
		return "system"
	}
	if p.UserID != "" {
		return "user:" + p.UserID
	}
	if p.Platform {
		return "admin"
	}
	if p.AgentID == "" {
		return "key:" + p.Key.ID
	}
	return "agent:" + p.AgentID
}

// Require checks a scope. ScopeAdmin means "platform operator"; the other
// (legacy agent) scopes map onto workspace permissions (scope.go).
func (p *Principal) Require(scope string) error {
	if p == nil || p.Key == nil {
		return model.E(model.ErrUnauthorized, "missing API key")
	}
	if p.Platform {
		return nil
	}
	if scope == model.ScopeAdmin {
		if p.UserID != "" {
			return model.EH(model.ErrUnauthorized, "Use an admin key or a platform admin account.", "platform admin only")
		}
		return model.EH(model.ErrUnauthorized, "Create a key with the '"+scope+"' scope.", "key lacks scope %q", scope)
	}
	if perm, ok := legacyScopePerm[scope]; ok {
		return p.Can(perm)
	}
	return p.Can(scope)
}

// Authenticate resolves a bearer token.
func (m *Manager) Authenticate(ctx context.Context, token string) (*Principal, error) {
	if token == "" {
		return nil, model.E(model.ErrUnauthorized, "missing API key")
	}
	if strings.HasPrefix(token, SessionPrefix) {
		return m.authenticateSession(ctx, token)
	}
	k, err := m.Store.GetKeyByHash(ctx, secret.Hash(token))
	if err != nil {
		return nil, model.E(model.ErrUnauthorized, "invalid API key")
	}
	if k.RevokedAt != nil {
		return nil, model.EH(model.ErrUnauthorized, "This key was revoked; ask the operator for a new key.", "API key revoked")
	}
	now := time.Now().UTC()
	if k.ExpiresAt != nil && !now.Before(*k.ExpiresAt) {
		return nil, model.EH(model.ErrUnauthorized, "Create a new key (POST /v1/keys).", "API key expired")
	}
	ip := ClientIPFrom(ctx)
	if k.LastUsedAt == nil || now.Sub(*k.LastUsedAt) > time.Minute || (ip != "" && ip != k.LastUsedIP) {
		k.LastUsedAt = &now
		if ip != "" {
			k.LastUsedIP = ip
		}
		m.Store.PutKey(ctx, k)
	}
	p, err := m.principalFromKey(ctx, k)
	if err != nil {
		return nil, err
	}
	if !p.Platform {
		bucket := p.AgentID
		if bucket == "" {
			bucket = "key:" + k.ID
		}
		if err := m.allow(ctx, bucket); err != nil {
			return nil, err
		}
		m.touchAgent(ctx, p.AgentID)
	}
	return p, nil
}

// ---- rate limiting (token bucket per agent) ----

type bucket struct {
	tokens float64
	last   time.Time
}

func (m *Manager) allow(ctx context.Context, agentID string) error {
	limit := float64(m.Quotas(ctx).RateLimitPerMinute)
	m.mu.Lock()
	defer m.mu.Unlock()
	b := m.rate[agentID]
	now := time.Now()
	if b == nil {
		b = &bucket{tokens: limit, last: now}
		m.rate[agentID] = b
	}
	b.tokens += now.Sub(b.last).Minutes() * limit
	if b.tokens > limit {
		b.tokens = limit
	}
	b.last = now
	if b.tokens < 1 {
		wait := int((1-b.tokens)/limit*60) + 1
		return model.E(model.ErrRateLimited, "too many requests (limit %d/minute)", int(limit)).WithRetry(wait)
	}
	b.tokens--
	return nil
}

var agentLastTouch = map[string]time.Time{}

func (m *Manager) touchAgent(ctx context.Context, agentID string) {
	if agentID == "" {
		return
	}
	m.mu.Lock()
	last := agentLastTouch[agentID]
	now := time.Now().UTC()
	if now.Sub(last) < 30*time.Second {
		m.mu.Unlock()
		return
	}
	agentLastTouch[agentID] = now
	m.mu.Unlock()
	if a, err := m.Store.GetAgent(ctx, agentID); err == nil {
		a.LastCallAt = &now
		m.Store.PutAgent(ctx, a)
	}
}

// bootstrapAdmin creates the first admin key.
func (m *Manager) bootstrapAdmin(ctx context.Context, envKey string) (string, error) {
	if envKey != "" {
		if _, err := m.Store.GetKeyByHash(ctx, secret.Hash(envKey)); err == nil {
			return "", nil
		}
		k := &model.APIKey{ID: secret.ID("key"), Name: "admin (BOXMES_ADMIN_KEY)", Prefix: prefixOf(envKey), Scopes: []string{model.ScopeAdmin}, CreatedAt: time.Now().UTC(), Hash: secret.Hash(envKey)}
		return "", m.Store.PutKey(ctx, k)
	}
	keys, err := m.Store.ListKeys(ctx, "")
	if err != nil {
		return "", err
	}
	for _, k := range keys {
		if k.RevokedAt == nil && k.HasScope(model.ScopeAdmin) {
			return "", nil
		}
	}
	key, prefix := secret.NewAPIKey("admin")
	k := &model.APIKey{ID: secret.ID("key"), Name: "bootstrap admin", Prefix: prefix, Scopes: []string{model.ScopeAdmin}, CreatedAt: time.Now().UTC(), Hash: secret.Hash(key)}
	return key, m.Store.PutKey(ctx, k)
}

func prefixOf(k string) string {
	if len(k) > 12 {
		return k[:12]
	}
	return k
}

// ---- agents ----

var nameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9 ._-]{0,62}$`)

type AgentInput struct {
	Name           *string       `json:"name"`
	Kind           *string       `json:"kind"`
	BudgetDayUSD   *float64      `json:"budget_day_usd"`
	BudgetMonthUSD *float64      `json:"budget_month_usd"`
	Policy         *model.Policy `json:"policy"`
	MemoryMB       *int          `json:"memory_mb"`
	Locale         *string       `json:"locale"`
}

func (m *Manager) CreateAgent(ctx context.Context, p *Principal, in AgentInput) (*model.Agent, error) {
	if err := p.Can(PermAgents); err != nil {
		return nil, err
	}
	ws, err := p.TargetWS()
	if err != nil {
		return nil, err
	}
	if in.Name == nil || !nameRe.MatchString(*in.Name) {
		return nil, model.E(model.ErrInvalid, "name is required (letters, digits, space . _ -)")
	}
	if _, err := m.Store.GetAgentInWorkspace(ctx, ws, *in.Name); err == nil {
		return nil, model.E(model.ErrInvalid, "agent %q already exists", *in.Name)
	}
	if err := m.quotaCount(ctx, ws, "max_agents"); err != nil {
		return nil, err
	}
	a := &model.Agent{ID: secret.ID("agt"), WorkspaceID: ws, Name: *in.Name, Kind: "custom", CreatedAt: time.Now().UTC(), CreatedBy: p.Actor(), Policy: model.Policy{Providers: []string{}, Regions: []string{}}}
	if err := applyAgentInput(a, in); err != nil {
		return nil, err
	}
	if err := m.Store.PutAgent(ctx, a); err != nil {
		return nil, err
	}
	m.Emit(ctx, p.Actor(), "", a.ID, "agent.created", "agent "+a.Name+" created", nil)
	return m.decorateAgent(ctx, a), nil
}

func applyAgentInput(a *model.Agent, in AgentInput) error {
	if in.Name != nil {
		if !nameRe.MatchString(*in.Name) {
			return model.E(model.ErrInvalid, "invalid name")
		}
		a.Name = *in.Name
	}
	if in.Kind != nil {
		if !ValidKind(*in.Kind) {
			return model.EH(model.ErrInvalid, "List kinds with GET /agent-kinds.", "unknown agent kind %q", *in.Kind)
		}
		a.Kind = *in.Kind
	}
	if in.BudgetDayUSD != nil {
		if *in.BudgetDayUSD < 0 {
			return model.E(model.ErrInvalid, "budget_day_usd must be >= 0")
		}
		a.BudgetDayUSD = *in.BudgetDayUSD
	}
	if in.BudgetMonthUSD != nil {
		if *in.BudgetMonthUSD < 0 {
			return model.E(model.ErrInvalid, "budget_month_usd must be >= 0")
		}
		a.BudgetMonthUSD = *in.BudgetMonthUSD
	}
	if in.Policy != nil {
		a.Policy = *in.Policy
		if a.Policy.Providers == nil {
			a.Policy.Providers = []string{}
		}
		if a.Policy.Regions == nil {
			a.Policy.Regions = []string{}
		}
	}
	if in.MemoryMB != nil {
		a.MemoryMB = *in.MemoryMB
	}
	if in.Locale != nil {
		if *in.Locale != "" && !i18n.Supported(*in.Locale) {
			return model.E(model.ErrInvalid, "locale must be one of vi, en, zh, ja")
		}
		a.Locale = *in.Locale
	}
	return nil
}

func (m *Manager) decorateAgent(ctx context.Context, a *model.Agent) *model.Agent {
	now := time.Now()
	a.SpentTodayUSD, _ = m.Store.SumLedger(ctx, store.LedgerFilter{AgentID: a.ID, From: dayStart(now)})
	a.SpentMonthUSD, _ = m.Store.SumLedger(ctx, store.LedgerFilter{AgentID: a.ID, From: monthStart(now)})
	a.SpentTodayUSD, a.SpentMonthUSD = model.Round4(a.SpentTodayUSD), model.Round4(a.SpentMonthUSD)
	boxes, _ := m.Store.ListBoxes(ctx, store.BoxFilter{AgentID: a.ID})
	a.BoxCount = len(boxes)
	if a.Policy.Providers == nil {
		a.Policy.Providers = []string{}
	}
	if a.Policy.Regions == nil {
		a.Policy.Regions = []string{}
	}
	return a
}

// listAgentsScoped returns the agents visible to p's workspace scope.
func (m *Manager) listAgentsScoped(ctx context.Context, p *Principal) ([]*model.Agent, error) {
	if ws := p.ScopeWS(); ws != "" {
		return m.Store.ListAgentsInWorkspace(ctx, ws)
	}
	return m.Store.ListAgents(ctx)
}

func (m *Manager) ListAgents(ctx context.Context, p *Principal) ([]*model.Agent, error) {
	if !p.Admin {
		a, err := m.GetAgent(ctx, p, p.AgentID)
		if err != nil {
			return nil, err
		}
		return []*model.Agent{a}, nil
	}
	as, err := m.listAgentsScoped(ctx, p)
	if err != nil {
		return nil, err
	}
	for _, a := range as {
		m.decorateAgent(ctx, a)
	}
	return as, nil
}

// findAgent resolves an agent id or name visible to p (NOT_FOUND otherwise).
func (m *Manager) findAgent(ctx context.Context, p *Principal, ref string) (*model.Agent, error) {
	if a, err := m.Store.GetAgent(ctx, ref); err == nil && p.Sees(a.WorkspaceID, a.ID) {
		return a, nil
	}
	if ws := p.ScopeWS(); ws != "" {
		if a, err := m.Store.GetAgentInWorkspace(ctx, ws, ref); err == nil && p.SeesAgent(a.ID) {
			return a, nil
		}
	} else if p.Platform {
		// global platform caller: a name is looked up in the target workspace first
		if tw, err := p.TargetWS(); err == nil {
			if a, err := m.Store.GetAgentInWorkspace(ctx, tw, ref); err == nil {
				return a, nil
			}
		}
		if as, err := m.Store.ListAgents(ctx); err == nil {
			for _, a := range as {
				if a.Name == ref {
					return a, nil
				}
			}
		}
	}
	return nil, notFound("agent", ref)
}

func (m *Manager) GetAgent(ctx context.Context, p *Principal, id string) (*model.Agent, error) {
	a, err := m.findAgent(ctx, p, id)
	if err != nil {
		return nil, err
	}
	return m.decorateAgent(ctx, a), nil
}

func (m *Manager) UpdateAgent(ctx context.Context, p *Principal, id string, in AgentInput) (*model.Agent, error) {
	if err := p.Can(PermAgents); err != nil {
		return nil, err
	}
	a, err := m.findAgent(ctx, p, id)
	if err != nil || !p.Admin {
		return nil, notFound("agent", id)
	}
	if err := p.CanManage(a.CreatedBy); err != nil {
		return nil, err
	}
	if in.Name != nil && *in.Name != a.Name {
		if _, err := m.Store.GetAgentInWorkspace(ctx, a.WorkspaceID, *in.Name); err == nil {
			return nil, model.E(model.ErrInvalid, "agent %q already exists", *in.Name)
		}
	}
	if err := applyAgentInput(a, in); err != nil {
		return nil, err
	}
	if err := m.Store.PutAgent(ctx, a); err != nil {
		return nil, err
	}
	m.Emit(ctx, p.Actor(), "", a.ID, "agent.updated", "agent "+a.Name+" updated", nil)
	return m.decorateAgent(ctx, a), nil
}

// DeleteAgent terminates its boxes and revokes its keys.
func (m *Manager) DeleteAgent(ctx context.Context, p *Principal, id string) error {
	if err := p.Can(PermAgents); err != nil {
		return err
	}
	a, err := m.findAgent(ctx, p, id)
	if err != nil || !p.Admin {
		return notFound("agent", id)
	}
	if err := p.CanManage(a.CreatedBy); err != nil {
		return err
	}
	boxes, _ := m.Store.ListBoxes(ctx, store.BoxFilter{AgentID: a.ID})
	for _, b := range boxes {
		m.Terminate(ctx, p, b.ID)
	}
	keys, _ := m.Store.ListKeys(ctx, a.ID)
	now := time.Now().UTC()
	for _, k := range keys {
		if k.RevokedAt == nil {
			full, _ := m.Store.GetKey(ctx, k.ID)
			full.RevokedAt = &now
			m.Store.PutKey(ctx, full)
		}
	}
	m.Emit(ctx, p.Actor(), "", a.ID, "agent.deleted", "agent "+a.Name+" deleted", nil)
	return m.Store.DeleteAgent(ctx, a.ID)
}

// ensureOperatorAgent is used when a workspace-wide caller creates a box
// without agent_id: one "operator" agent per workspace.
func (m *Manager) ensureOperatorAgent(ctx context.Context, ws string) (*model.Agent, error) {
	if a, err := m.Store.GetAgentInWorkspace(ctx, ws, "operator"); err == nil {
		return a, nil
	}
	a := &model.Agent{ID: secret.ID("agt"), WorkspaceID: ws, Name: "operator", Kind: "custom", CreatedAt: time.Now().UTC(), Policy: model.Policy{Providers: []string{}, Regions: []string{}}}
	return a, m.Store.PutAgent(ctx, a)
}

// ---- keys ----

// KeyInput is POST /v1/keys. Without agent_id the key is a workspace key
// (v2.0) in the caller's workspace; a platform caller without a workspace
// (no ?workspace=, no workspace_id) creating an admin key gets a platform key.
type KeyInput struct {
	AgentID       *string    `json:"agent_id"`
	Name          string     `json:"name"`
	Scopes        []string   `json:"scopes"`
	ExpiresAt     *time.Time `json:"expires_at"`
	ExpiresInDays int        `json:"expires_in_days"`
	WorkspaceID   string     `json:"workspace_id"` // platform callers only
	Platform      bool       `json:"platform"`     // platform callers only: force a platform key
}

var validScopes = map[string]bool{model.ScopeAdmin: true, model.ScopeBoxCreate: true, model.ScopeBoxExec: true, model.ScopeBoxFile: true, model.ScopeSnapshot: true, model.ScopeSecretUse: true,
	KeyScopeBoxesRead: true, KeyScopeBoxesWrite: true, KeyScopeExec: true, KeyScopeSecretsRead: true, KeyScopeSecretsWrite: true, KeyScopeBillingRead: true}

func (m *Manager) CreateKey(ctx context.Context, p *Principal, in KeyInput) (*model.APIKey, error) {
	if err := p.Can(PermAgents); err != nil {
		return nil, err
	}
	if in.AgentID != nil && *in.AgentID == "" {
		in.AgentID = nil
	}
	ws, err := p.TargetWS()
	if err != nil {
		return nil, err
	}
	if in.WorkspaceID != "" && p.Platform {
		if _, err := m.Store.GetWorkspace(ctx, in.WorkspaceID); err != nil {
			return nil, notFound("workspace", in.WorkspaceID)
		}
		ws = in.WorkspaceID
	}
	var agent *model.Agent
	if in.AgentID != nil {
		agent, err = m.findAgent(ctx, p, *in.AgentID)
		if err != nil || !p.Admin {
			return nil, notFound("agent", *in.AgentID)
		}
		in.AgentID = &agent.ID
		ws = agent.WorkspaceID
	}
	// legacy platform key: platform caller, no workspace asked for, admin scope
	platformKey := p.Platform && agent == nil && (in.Platform || (in.WorkspaceID == "" && p.FilterWS == "" && p.WorkspaceID == "" && (len(in.Scopes) == 0 || (len(in.Scopes) == 1 && in.Scopes[0] == model.ScopeAdmin))))
	scopes := in.Scopes
	if len(scopes) == 0 {
		switch {
		case platformKey:
			scopes = []string{model.ScopeAdmin}
		case agent != nil:
			scopes = append([]string{}, model.AgentScopes...)
		default:
			scopes = append([]string{}, DefaultWorkspaceKeyScopes...)
		}
	}
	for _, s := range scopes {
		if !validScopes[s] {
			return nil, model.E(model.ErrInvalid, "unknown scope %q", s)
		}
	}
	if !p.Platform { // a key never grants more than its creator holds
		for perm := range scopesPerms(scopes) {
			if !p.perms[perm] {
				return nil, model.EH(model.ErrPolicyDenied, "Ask a workspace owner or admin.", "you cannot create a key with more rights than your own (%s)", perm)
			}
		}
	}
	if !platformKey {
		if err := m.quotaCount(ctx, ws, "max_keys"); err != nil {
			return nil, err
		}
	}
	sort.Strings(scopes)
	kind, prefixKind := model.KeyKindWorkspace, "ws"
	switch {
	case platformKey:
		kind, prefixKind, ws = model.KeyKindPlatform, "admin", ""
	case agent != nil:
		kind, prefixKind = model.KeyKindAgent, "agent"
	}
	key, prefix := secret.NewAPIKey(prefixKind)
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = kind + " key"
	}
	now := time.Now().UTC()
	k := &model.APIKey{ID: secret.ID("key"), WorkspaceID: ws, AgentID: in.AgentID, Name: clip(name, 80), Prefix: prefix, Scopes: scopes, CreatedAt: now,
		Hash: secret.Hash(key), Kind: kind, CreatedBy: p.Actor()}
	switch {
	case in.ExpiresAt != nil:
		if !in.ExpiresAt.After(now) {
			return nil, model.E(model.ErrInvalid, "expires_at must be in the future")
		}
		e := in.ExpiresAt.UTC()
		k.ExpiresAt = &e
	case in.ExpiresInDays < 0:
		return nil, model.E(model.ErrInvalid, "expires_in_days must be >= 0")
	case in.ExpiresInDays > 0:
		e := now.Add(time.Duration(in.ExpiresInDays) * 24 * time.Hour)
		k.ExpiresAt = &e
	}
	if err := m.Store.PutKey(ctx, k); err != nil {
		return nil, err
	}
	aid := ""
	if in.AgentID != nil {
		aid = *in.AgentID
	}
	m.EmitWS(ctx, ws, p.Actor(), "", aid, "key.created", "key "+prefix+"… created", nil, map[string]any{"kind": kind, "scopes": scopes})
	k.Key = key
	return k, nil
}

func (m *Manager) ListKeys(ctx context.Context, p *Principal) ([]*model.APIKey, error) {
	if !p.Admin {
		return m.Store.ListKeys(ctx, p.AgentID)
	}
	if ws := p.ScopeWS(); ws != "" {
		return m.Store.ListKeysInWorkspace(ctx, ws)
	}
	return m.Store.ListKeys(ctx, "")
}

func (m *Manager) RevokeKey(ctx context.Context, p *Principal, id string) error {
	if err := p.Can(PermAgents); err != nil {
		return err
	}
	k, err := m.Store.GetKey(ctx, id)
	if err != nil || !p.Admin {
		return notFound("key", id)
	}
	if k.EffectiveKind() == model.KeyKindPlatform {
		if !p.Platform {
			return notFound("key", id)
		}
	} else if !p.InScope(k.WorkspaceID) {
		return notFound("key", id)
	}
	if err := p.CanManage(k.CreatedBy); err != nil {
		return err
	}
	if k.ID == p.Key.ID {
		return model.EH(model.ErrInvalid, "Use another admin key to revoke this one.", "cannot revoke the key used for this request")
	}
	if k.RevokedAt == nil {
		now := time.Now().UTC()
		k.RevokedAt = &now
		if err := m.Store.PutKey(ctx, k); err != nil {
			return err
		}
	}
	m.EmitWS(ctx, k.WorkspaceID, p.Actor(), "", "", "key.revoked", "key "+k.Prefix+"… revoked", nil, nil)
	return nil
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// agentFor resolves which agent a box request acts for.
func (m *Manager) agentFor(ctx context.Context, p *Principal, requested string) (*model.Agent, error) {
	if !p.Admin {
		if requested != "" && requested != p.AgentID {
			return nil, model.E(model.ErrPolicyDenied, "agent keys can only create boxes for their own agent")
		}
		a, err := m.Store.GetAgent(ctx, p.AgentID)
		if err != nil {
			return nil, model.E(model.ErrUnauthorized, "key's agent no longer exists")
		}
		return a, nil
	}
	if requested == "" {
		if p.AgentID != "" {
			return m.Store.GetAgent(ctx, p.AgentID)
		}
		ws, err := p.TargetWS()
		if err != nil {
			return nil, err
		}
		return m.ensureOperatorAgent(ctx, ws)
	}
	a, err := m.findAgent(ctx, p, requested)
	if errors.Is(err, model.ErrNoRows) {
		return nil, notFound("agent", requested)
	}
	return a, err
}
