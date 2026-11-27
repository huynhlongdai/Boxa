package manager

// v2.0 tenancy: who may see and do what.
//
//   - Platform callers (admin key, platform admin account) see every
//     workspace; ?workspace=<id> narrows reads, and creates land in that
//     workspace (else the session's current workspace, else ws_default).
//   - Workspace callers (a session member, a workspace API key) see only
//     their workspace; Can() checks their role (session) or scopes (key).
//   - Agent keys see only their agent's records inside its workspace.
//
// Records of another workspace are reported as NOT_FOUND, never as denied.

import (
	"context"
	"sort"

	"boxmes/core/internal/model"
)

// Workspace permissions (role matrix: docs/boxmes-ui-spec-v2.md §19).
const (
	PermView         = "view"             // boxes, agents, costs, events, approvals, templates
	PermBoxWrite     = "boxes:write"      // create/delete/sleep/wake/snapshot/fork boxes, ports, apps
	PermExec         = "exec"             // exec, jobs/logs, files, terminal, ssh, browser, desktop
	PermAgents       = "agents:write"     // agents and keys (developer: only those they created)
	PermSecretsRead  = "secrets:read"     // list secret names
	PermSecretsWrite = "secrets:write"    // create/rotate/delete secrets
	PermSecretUse    = "secrets:use"      // inject secrets into boxes
	PermTemplates    = "templates:write"  // create/update/delete workspace templates
	PermBilling      = "billing:read"     // plan, usage, (v2.1) wallet and invoices
	PermMembers      = "members:write"    // members and invites
	PermAudit        = "audit:read"       // workspace audit log
	PermEmergency    = "emergency_stop"   // sleep every running box of the workspace
	PermWorkspace    = "workspace:write"  // rename the workspace
	PermApprovals    = "approvals:decide" // approve / reject "Cần anh" items
)

// AllPerms in display order.
var AllPerms = []string{PermView, PermBoxWrite, PermExec, PermAgents, PermSecretsRead, PermSecretsWrite, PermSecretUse,
	PermTemplates, PermBilling, PermMembers, PermAudit, PermEmergency, PermWorkspace, PermApprovals}

// RolePerms is the implemented role → permission table.
var RolePerms = map[string][]string{
	model.RoleOwner: AllPerms,
	model.RoleAdmin: {PermView, PermBoxWrite, PermExec, PermAgents, PermSecretsRead, PermSecretsWrite, PermSecretUse,
		PermTemplates, PermMembers, PermAudit, PermEmergency, PermWorkspace, PermApprovals},
	model.RoleDeveloper: {PermView, PermBoxWrite, PermExec, PermAgents, PermSecretsRead, PermSecretUse, PermTemplates},
	model.RoleBilling:   {PermView, PermBilling},
	model.RoleViewer:    {PermView, PermSecretsRead},
	model.RoleAuditor:   {PermView, PermSecretsRead, PermAudit},
}

// Workspace API key scopes (v2.0) and what they grant.
const (
	KeyScopeBoxesRead    = "boxes:read"
	KeyScopeBoxesWrite   = "boxes:write"
	KeyScopeExec         = "exec"
	KeyScopeSecretsRead  = "secrets:read"
	KeyScopeSecretsWrite = "secrets:write"
	KeyScopeBillingRead  = "billing:read"
)

// WorkspaceKeyScopes lists the scopes a workspace key may carry.
var WorkspaceKeyScopes = []string{KeyScopeBoxesRead, KeyScopeBoxesWrite, KeyScopeExec, KeyScopeSecretsRead, KeyScopeSecretsWrite, KeyScopeBillingRead, model.ScopeAdmin}

// DefaultWorkspaceKeyScopes is used when a workspace key is created without scopes.
var DefaultWorkspaceKeyScopes = []string{KeyScopeBoxesRead, KeyScopeBoxesWrite, KeyScopeExec}

var scopePerms = map[string][]string{
	KeyScopeBoxesRead:    {PermView},
	KeyScopeBoxesWrite:   {PermView, PermBoxWrite, PermTemplates},
	KeyScopeExec:         {PermView, PermExec},
	KeyScopeSecretsRead:  {PermSecretsRead, PermSecretUse},
	KeyScopeSecretsWrite: {PermSecretsRead, PermSecretsWrite, PermSecretUse},
	KeyScopeBillingRead:  {PermView, PermBilling},
	model.ScopeAdmin:     AllPerms, // a workspace key's admin scope: everything inside its workspace
	// pre-v2.0 agent key scopes
	model.ScopeBoxCreate: {PermView, PermBoxWrite},
	model.ScopeBoxExec:   {PermView, PermExec},
	model.ScopeBoxFile:   {PermView, PermExec},
	model.ScopeSnapshot:  {PermView, PermBoxWrite},
	model.ScopeSecretUse: {PermSecretUse},
}

// legacyScopePerm maps Require(<agent scope>) onto a permission.
var legacyScopePerm = map[string]string{
	model.ScopeBoxCreate: PermBoxWrite,
	model.ScopeBoxExec:   PermExec,
	model.ScopeBoxFile:   PermExec,
	model.ScopeSnapshot:  PermBoxWrite,
	model.ScopeSecretUse: PermSecretUse,
}

func permSet(list []string) map[string]bool {
	out := map[string]bool{}
	for _, x := range list {
		out[x] = true
	}
	return out
}

func scopesPerms(scopes []string) map[string]bool {
	out := map[string]bool{}
	for _, s := range scopes {
		for _, p := range scopePerms[s] {
			out[p] = true
		}
	}
	return out
}

// PermsOf lists the permissions of a principal (sorted; platform = all).
func (p *Principal) PermsOf() []string {
	if p == nil {
		return []string{}
	}
	if p.Platform {
		return append([]string{}, AllPerms...)
	}
	out := []string{}
	for _, x := range AllPerms {
		if p.perms[x] {
			out = append(out, x)
		}
	}
	sort.Strings(out)
	return out
}

// Can checks one workspace permission.
func (p *Principal) Can(perm string) error {
	if p == nil || p.Key == nil {
		return model.E(model.ErrUnauthorized, "missing API key")
	}
	if p.Platform {
		return nil
	}
	if p.WorkspaceID == "" {
		return model.EH(model.ErrPolicyDenied, "Create a workspace (POST /v1/workspaces) or accept an invite.", "you are not a member of any workspace")
	}
	if p.perms[perm] {
		return nil
	}
	if p.UserID != "" {
		return model.EH(model.ErrPolicyDenied, "Ask a workspace owner or admin to change your role.", "your role %q does not allow %s", p.Role, perm)
	}
	return model.EH(model.ErrUnauthorized, "Create a key with the '"+perm+"' scope.", "key lacks scope %q", perm)
}

// CanManage checks PermAgents on an agent or key created by createdBy:
// developers may only change their own.
func (p *Principal) CanManage(createdBy string) error {
	if err := p.Can(PermAgents); err != nil {
		return err
	}
	if !p.Platform && p.UserID != "" && p.Role == model.RoleDeveloper && createdBy != p.Actor() {
		return model.EH(model.ErrPolicyDenied, "Ask a workspace owner or admin.", "developers can only change agents and keys they created")
	}
	return nil
}

// noWorkspace never matches a stored workspace id.
const noWorkspace = "\x00none"

// ScopeWS is the workspace filter for reads: "" = every workspace
// (platform without ?workspace=).
func (p *Principal) ScopeWS() string {
	if p == nil {
		return noWorkspace
	}
	if p.Platform {
		return p.FilterWS
	}
	if p.WorkspaceID == "" {
		return noWorkspace
	}
	return p.WorkspaceID
}

// InScope reports whether a record of workspace ws is visible.
func (p *Principal) InScope(ws string) bool {
	if p == nil {
		return false
	}
	if ws == "" { // pre-v2.0 rows are backfilled; "" means platform-wide
		return p.Platform && p.FilterWS == ""
	}
	sc := p.ScopeWS()
	return sc == "" || sc == ws
}

// TargetWS is the workspace new records are created in.
func (p *Principal) TargetWS() (string, error) {
	if p == nil {
		return "", model.E(model.ErrUnauthorized, "missing API key")
	}
	if p.Platform {
		switch {
		case p.FilterWS != "":
			return p.FilterWS, nil
		case p.WorkspaceID != "":
			return p.WorkspaceID, nil
		}
		return model.DefaultWorkspaceID, nil
	}
	if p.WorkspaceID == "" {
		return "", model.EH(model.ErrPolicyDenied, "Create a workspace (POST /v1/workspaces) or accept an invite.", "you are not a member of any workspace")
	}
	return p.WorkspaceID, nil
}

// SeesAgent: agent keys only see their own agent's records.
func (p *Principal) SeesAgent(agentID string) bool {
	return p != nil && (p.Admin || agentID == p.AgentID)
}

// Sees combines workspace scope and agent restriction.
func (p *Principal) Sees(ws, agentID string) bool {
	return p.InScope(ws) && p.SeesAgent(agentID)
}

// canMutateBox: sleep/wake/delete. Agent keys keep the pre-v2.0 rule (their
// own boxes, whatever their scopes); everyone else needs boxes:write.
func (p *Principal) canMutateBox() error {
	if p != nil && !p.Admin && p.AgentID != "" {
		return nil
	}
	return p.Can(PermBoxWrite)
}

// principalFromKey builds the principal of an API key.
func (m *Manager) principalFromKey(ctx context.Context, k *model.APIKey) (*Principal, error) {
	p := &Principal{Key: k, WorkspaceID: k.WorkspaceID}
	switch k.EffectiveKind() {
	case model.KeyKindPlatform:
		p.Platform, p.Admin, p.WorkspaceID = true, true, ""
	case model.KeyKindWorkspace:
		if k.WorkspaceID == "" {
			return nil, model.E(model.ErrUnauthorized, "invalid API key")
		}
		p.Admin = true
		p.perms = scopesPerms(k.Scopes)
	default:
		if k.AgentID == nil {
			return nil, model.E(model.ErrUnauthorized, "invalid API key")
		}
		p.AgentID = *k.AgentID
		if p.WorkspaceID == "" {
			if a, err := m.Store.GetAgent(ctx, p.AgentID); err == nil {
				p.WorkspaceID = a.WorkspaceID
			}
		}
		p.perms = scopesPerms(k.Scopes)
		if k.HasScope(model.ScopeAdmin) { // pre-v2.0 agent key with admin scope: workspace-wide
			p.Admin = true
		}
	}
	return p, nil
}

// ---- request context ----

type ctxKeyT int

const (
	ctxClientIP ctxKeyT = iota + 1
	ctxPrincipal
	ctxQuote
)

// quoteOnly: normalizeRequest runs for a quote (no create-rate accounting).
func quoteOnly(ctx context.Context) bool {
	v, _ := ctx.Value(ctxQuote).(bool)
	return v
}

// WithClientIP records the caller's IP (API keys remember last_used_ip).
func WithClientIP(ctx context.Context, ip string) context.Context {
	return context.WithValue(ctx, ctxClientIP, ip)
}

// ClientIPFrom returns the IP recorded by WithClientIP.
func ClientIPFrom(ctx context.Context) string {
	s, _ := ctx.Value(ctxClientIP).(string)
	return s
}

// WithPrincipal stores the caller so events emitted during the request
// carry its workspace.
func WithPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, ctxPrincipal, p)
}

func principalFrom(ctx context.Context) *Principal {
	p, _ := ctx.Value(ctxPrincipal).(*Principal)
	return p
}

// sysPrincipal is core acting on its own (reaper, gateway wake, notifications).
func sysPrincipal() *Principal {
	return &Principal{Key: &model.APIKey{ID: "system", Scopes: []string{model.ScopeAdmin}, Kind: model.KeyKindPlatform}, Admin: true, Platform: true}
}
