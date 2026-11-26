package manager

import (
	"boxaz/core/internal/i18n"
	"context"
	"errors"
	"regexp"
	"sort"
	"strings"
	"time"

	"boxaz/core/internal/model"
	"boxaz/core/internal/secret"
	"boxaz/core/internal/store"
)

// Principal is the authenticated caller.
type Principal struct {
	Key     *model.APIKey
	AgentID string // "" for admin keys without agent binding
	Admin   bool
	// v2 tenancy: set when the caller signed in with a session (bxs_ token).
	User        *model.User
	UserID      string
	WorkspaceID string
	Role        string // owner|member in WorkspaceID
	SessionID   string
	session     *model.Session
}

func (p *Principal) Actor() string {
	if p == nil || p.Key == nil {
		return "system"
	}
	if p.UserID != "" {
		return "user:" + p.UserID
	}
	if p.Admin {
		return "admin"
	}
	return "agent:" + p.AgentID
}

// Require checks a scope.
func (p *Principal) Require(scope string) error {
	if p == nil || p.Key == nil {
		return model.E(model.ErrUnauthorized, "missing API key")
	}
	if p.Key.HasScope(scope) {
		return nil
	}
	return model.EH(model.ErrUnauthorized, "Create a key with the '"+scope+"' scope.", "key lacks scope %q", scope)
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
	if k.LastUsedAt == nil || now.Sub(*k.LastUsedAt) > time.Minute {
		k.LastUsedAt = &now
		m.Store.PutKey(ctx, k)
	}
	p := &Principal{Key: k, Admin: k.HasScope(model.ScopeAdmin)}
	if k.AgentID != nil {
		p.AgentID = *k.AgentID
	}
	if !p.Admin {
		if err := m.allow(ctx, p.AgentID); err != nil {
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
		k := &model.APIKey{ID: secret.ID("key"), Name: "admin (BOXAZ_ADMIN_KEY)", Prefix: prefixOf(envKey), Scopes: []string{model.ScopeAdmin}, CreatedAt: time.Now().UTC(), Hash: secret.Hash(envKey)}
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
	if err := p.Require(model.ScopeAdmin); err != nil {
		return nil, err
	}
	if in.Name == nil || !nameRe.MatchString(*in.Name) {
		return nil, model.E(model.ErrInvalid, "name is required (letters, digits, space . _ -)")
	}
	if _, err := m.Store.GetAgentByName(ctx, *in.Name); err == nil {
		return nil, model.E(model.ErrInvalid, "agent %q already exists", *in.Name)
	}
	a := &model.Agent{ID: secret.ID("agt"), Name: *in.Name, Kind: "custom", CreatedAt: time.Now().UTC(), Policy: model.Policy{Providers: []string{}, Regions: []string{}}}
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

func (m *Manager) ListAgents(ctx context.Context, p *Principal) ([]*model.Agent, error) {
	if !p.Admin {
		a, err := m.GetAgent(ctx, p, p.AgentID)
		if err != nil {
			return nil, err
		}
		return []*model.Agent{a}, nil
	}
	as, err := m.Store.ListAgents(ctx)
	if err != nil {
		return nil, err
	}
	for _, a := range as {
		m.decorateAgent(ctx, a)
	}
	return as, nil
}

func (m *Manager) GetAgent(ctx context.Context, p *Principal, id string) (*model.Agent, error) {
	if !p.Admin && id != p.AgentID {
		return nil, notFound("agent", id)
	}
	a, err := m.Store.GetAgent(ctx, id)
	if err != nil {
		if a2, err2 := m.Store.GetAgentByName(ctx, id); err2 == nil {
			return m.decorateAgent(ctx, a2), nil
		}
		return nil, notFound("agent", id)
	}
	return m.decorateAgent(ctx, a), nil
}

func (m *Manager) UpdateAgent(ctx context.Context, p *Principal, id string, in AgentInput) (*model.Agent, error) {
	if err := p.Require(model.ScopeAdmin); err != nil {
		return nil, err
	}
	a, err := m.Store.GetAgent(ctx, id)
	if err != nil {
		return nil, notFound("agent", id)
	}
	if in.Name != nil && *in.Name != a.Name {
		if _, err := m.Store.GetAgentByName(ctx, *in.Name); err == nil {
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
	if err := p.Require(model.ScopeAdmin); err != nil {
		return err
	}
	a, err := m.Store.GetAgent(ctx, id)
	if err != nil {
		return notFound("agent", id)
	}
	boxes, _ := m.Store.ListBoxes(ctx, store.BoxFilter{AgentID: id})
	for _, b := range boxes {
		m.Terminate(ctx, p, b.ID)
	}
	keys, _ := m.Store.ListKeys(ctx, id)
	now := time.Now().UTC()
	for _, k := range keys {
		if k.RevokedAt == nil {
			full, _ := m.Store.GetKey(ctx, k.ID)
			full.RevokedAt = &now
			m.Store.PutKey(ctx, full)
		}
	}
	if err := m.Store.DeleteAgent(ctx, id); err != nil {
		return err
	}
	m.Emit(ctx, p.Actor(), "", id, "agent.deleted", "agent "+a.Name+" deleted", nil)
	return nil
}

// ensureOperatorAgent is used when an admin creates a box without agent_id.
func (m *Manager) ensureOperatorAgent(ctx context.Context) (*model.Agent, error) {
	if a, err := m.Store.GetAgentByName(ctx, "operator"); err == nil {
		return a, nil
	}
	a := &model.Agent{ID: secret.ID("agt"), Name: "operator", Kind: "custom", CreatedAt: time.Now().UTC(), Policy: model.Policy{Providers: []string{}, Regions: []string{}}}
	return a, m.Store.PutAgent(ctx, a)
}

// ---- keys ----

type KeyInput struct {
	AgentID *string  `json:"agent_id"`
	Name    string   `json:"name"`
	Scopes  []string `json:"scopes"`
}

var validScopes = map[string]bool{model.ScopeAdmin: true, model.ScopeBoxCreate: true, model.ScopeBoxExec: true, model.ScopeBoxFile: true, model.ScopeSnapshot: true, model.ScopeSecretUse: true}

func (m *Manager) CreateKey(ctx context.Context, p *Principal, in KeyInput) (*model.APIKey, error) {
	if err := p.Require(model.ScopeAdmin); err != nil {
		return nil, err
	}
	if in.AgentID != nil && *in.AgentID == "" {
		in.AgentID = nil
	}
	if in.AgentID != nil {
		a, err := m.Store.GetAgent(ctx, *in.AgentID)
		if err != nil {
			a, err = m.Store.GetAgentByName(ctx, *in.AgentID)
			if err != nil {
				return nil, notFound("agent", *in.AgentID)
			}
		}
		in.AgentID = &a.ID
	}
	scopes := in.Scopes
	if len(scopes) == 0 {
		if in.AgentID == nil {
			scopes = []string{model.ScopeAdmin}
		} else {
			scopes = append([]string{}, model.AgentScopes...)
		}
	}
	for _, s := range scopes {
		if !validScopes[s] {
			return nil, model.E(model.ErrInvalid, "unknown scope %q", s)
		}
	}
	if in.AgentID == nil && !contains(scopes, model.ScopeAdmin) {
		return nil, model.E(model.ErrInvalid, "a key without agent_id must have the admin scope")
	}
	sort.Strings(scopes)
	kind := "agent"
	if contains(scopes, model.ScopeAdmin) {
		kind = "admin"
	}
	key, prefix := secret.NewAPIKey(kind)
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = kind + " key"
	}
	k := &model.APIKey{ID: secret.ID("key"), AgentID: in.AgentID, Name: name, Prefix: prefix, Scopes: scopes, CreatedAt: time.Now().UTC(), Hash: secret.Hash(key)}
	if err := m.Store.PutKey(ctx, k); err != nil {
		return nil, err
	}
	aid := ""
	if in.AgentID != nil {
		aid = *in.AgentID
	}
	m.Emit(ctx, p.Actor(), "", aid, "key.created", "key "+prefix+"… created", nil)
	k.Key = key
	return k, nil
}

func (m *Manager) ListKeys(ctx context.Context, p *Principal) ([]*model.APIKey, error) {
	if p.Admin {
		return m.Store.ListKeys(ctx, "")
	}
	return m.Store.ListKeys(ctx, p.AgentID)
}

func (m *Manager) RevokeKey(ctx context.Context, p *Principal, id string) error {
	if err := p.Require(model.ScopeAdmin); err != nil {
		return err
	}
	k, err := m.Store.GetKey(ctx, id)
	if err != nil {
		return notFound("key", id)
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
	m.Emit(ctx, p.Actor(), "", "", "key.revoked", "key "+k.Prefix+"… revoked", nil)
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
		return m.ensureOperatorAgent(ctx)
	}
	a, err := m.Store.GetAgent(ctx, requested)
	if errors.Is(err, model.ErrNoRows) {
		a, err = m.Store.GetAgentByName(ctx, requested)
	}
	if err != nil {
		return nil, notFound("agent", requested)
	}
	return a, nil
}
