package manager

// v2.0 workspaces, members, roles and workspace invites.

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"boxmes/core/internal/model"
	"boxmes/core/internal/secret"
	"boxmes/core/internal/store"
)

const (
	workspaceInviteTTL   = 7 * 24 * time.Hour
	maxOwnedWorkspaces   = 10
	WorkspaceInvitePrefix = "bxi_"
)

// CurrentWorkspaceView is GET /v1/workspaces/current.
type CurrentWorkspaceView struct {
	*model.Workspace
	Role        string      `json:"role"`
	Plan        string      `json:"plan"`
	Quota       model.Quota `json:"quota"`
	Permissions []string    `json:"permissions"`
	MemberCount int         `json:"member_count"`
}

func (m *Manager) workspaceView(ctx context.Context, p *Principal, w *model.Workspace) *CurrentWorkspaceView {
	q, _ := m.WorkspaceQuota(ctx, w.ID)
	v := &CurrentWorkspaceView{Workspace: w, Plan: q.Plan, Quota: q, Permissions: p.PermsOf()}
	if v.Workspace.Status == "" {
		v.Workspace.Status = "active"
	}
	if p.UserID != "" {
		if mb, err := m.Store.GetMembership(ctx, w.ID, p.UserID); err == nil {
			v.Role = model.NormRole(mb.Role)
		}
	}
	if ms, err := m.Store.ListMemberships(ctx, w.ID); err == nil {
		v.MemberCount = len(ms)
	}
	return v
}

// CreateWorkspaceInput is POST /v1/workspaces.
type CreateWorkspaceInput struct {
	Name string `json:"name"`
}

// CreateWorkspace makes a new workspace owned by the signed-in caller (it
// does not switch the session; use POST /v1/auth/workspace).
func (m *Manager) CreateWorkspace(ctx context.Context, p *Principal, in CreateWorkspaceInput) (*CurrentWorkspaceView, error) {
	if err := requireUser(p); err != nil {
		return nil, err
	}
	name := clip(strings.TrimSpace(in.Name), 60)
	if name == "" {
		return nil, model.E(model.ErrInvalid, "name is required")
	}
	if !p.Platform {
		ms, _ := m.Store.ListUserMemberships(ctx, p.UserID)
		owned := 0
		for _, mb := range ms {
			if mb.Role == model.RoleOwner {
				owned++
			}
		}
		if owned >= maxOwnedWorkspaces {
			return nil, model.EH(model.ErrQuotaExceeded, "Delete or hand over a workspace you own first.", "you already own %d workspaces (limit %d)", owned, maxOwnedWorkspaces)
		}
	}
	now := time.Now().UTC()
	w := &model.Workspace{ID: secret.ID("ws"), Name: name, OwnerUserID: p.UserID, Plan: m.defaultPlan(),
		MaxBoxes: model.DefaultMaxBoxes, MaxTTLHours: model.DefaultMaxTTLHours, CreatedAt: now, CreatedBy: p.Actor()}
	if err := m.Store.PutWorkspace(ctx, w); err != nil {
		return nil, err
	}
	if err := m.Store.PutMembership(ctx, &model.Membership{WorkspaceID: w.ID, UserID: p.UserID, Role: model.RoleOwner, CreatedAt: now}); err != nil {
		m.Store.DeleteWorkspace(ctx, w.ID)
		return nil, err
	}
	m.EmitWS(ctx, w.ID, p.Actor(), "", "", "workspace.created", "workspace "+w.Name+" created", nil, nil)
	v := m.workspaceView(ctx, p, w)
	v.Role = model.RoleOwner
	v.Permissions = append([]string{}, RolePerms[model.RoleOwner]...)
	return v, nil
}

// currentWorkspace loads the caller's target workspace.
func (m *Manager) currentWorkspace(ctx context.Context, p *Principal) (*model.Workspace, error) {
	ws, err := p.TargetWS()
	if err != nil {
		return nil, err
	}
	w, err := m.Store.GetWorkspace(ctx, ws)
	if err != nil {
		return nil, notFound("workspace", ws)
	}
	return w, nil
}

func (m *Manager) GetCurrentWorkspace(ctx context.Context, p *Principal) (*CurrentWorkspaceView, error) {
	if err := p.Can(PermView); err != nil && p.Can(PermBilling) != nil {
		return nil, err
	}
	w, err := m.currentWorkspace(ctx, p)
	if err != nil {
		return nil, err
	}
	return m.workspaceView(ctx, p, w), nil
}

// PatchWorkspaceInput is PATCH /v1/workspaces/current (and
// PATCH /v1/admin/workspaces/{id}). Plan, overrides and status are
// platform-admin only.
type PatchWorkspaceInput struct {
	Name           *string               `json:"name"`
	Plan           *string               `json:"plan"`
	QuotaOverrides *model.QuotaOverrides `json:"quota_overrides"`
	Status         *string               `json:"status"`
}

func (m *Manager) PatchCurrentWorkspace(ctx context.Context, p *Principal, in PatchWorkspaceInput) (*CurrentWorkspaceView, error) {
	w, err := m.currentWorkspace(ctx, p)
	if err != nil {
		return nil, err
	}
	return m.patchWorkspace(ctx, p, w, in)
}

func (m *Manager) patchWorkspace(ctx context.Context, p *Principal, w *model.Workspace, in PatchWorkspaceInput) (*CurrentWorkspaceView, error) {
	if err := p.Can(PermWorkspace); err != nil {
		return nil, err
	}
	if (in.Plan != nil || in.QuotaOverrides != nil || in.Status != nil) && !p.Platform {
		return nil, model.EH(model.ErrPolicyDenied, "Plans and quotas are changed by the platform operator (billing arrives in v2.1).", "only a platform admin can change plan, quota_overrides or status")
	}
	if in.Name != nil {
		n := clip(strings.TrimSpace(*in.Name), 60)
		if n == "" {
			return nil, model.E(model.ErrInvalid, "name is required")
		}
		w.Name = n
	}
	if in.Plan != nil {
		if _, ok := model.Plans[*in.Plan]; !ok && *in.Plan != "" {
			return nil, model.EH(model.ErrInvalid, "List plans with GET /v1/plans.", "unknown plan %q", *in.Plan)
		}
		w.Plan = *in.Plan
	}
	if in.QuotaOverrides != nil {
		if o := in.QuotaOverrides; o.Egress != nil && *o.Egress != model.EgressOpen && *o.Egress != model.EgressRestricted {
			return nil, model.E(model.ErrInvalid, "egress must be open or restricted")
		}
		w.Overrides = in.QuotaOverrides
	}
	if in.Status != nil {
		switch *in.Status {
		case "active", "suspended":
			w.Status = *in.Status
		default:
			return nil, model.E(model.ErrInvalid, "status must be active or suspended")
		}
	}
	if err := m.Store.PutWorkspace(ctx, w); err != nil {
		return nil, err
	}
	m.EmitWS(ctx, w.ID, p.Actor(), "", "", "workspace.updated", "workspace "+w.Name+" updated", nil, nil)
	return m.workspaceView(ctx, p, w), nil
}

// ---- members ----

// MemberView is one row of GET /v1/workspaces/current/members.
type MemberView struct {
	UserID        string    `json:"user_id"`
	Email         string    `json:"email"`
	Name          string    `json:"name,omitempty"`
	Role          string    `json:"role"`
	EmailVerified bool      `json:"email_verified"`
	JoinedAt      time.Time `json:"joined_at"`
}

func (m *Manager) ListMembers(ctx context.Context, p *Principal) ([]*MemberView, error) {
	if err := p.Can(PermView); err != nil && p.Can(PermBilling) != nil {
		return nil, err
	}
	w, err := m.currentWorkspace(ctx, p)
	if err != nil {
		return nil, err
	}
	ms, err := m.Store.ListMemberships(ctx, w.ID)
	if err != nil {
		return nil, err
	}
	out := make([]*MemberView, 0, len(ms))
	for _, mb := range ms {
		v := &MemberView{UserID: mb.UserID, Role: model.NormRole(mb.Role), JoinedAt: mb.CreatedAt}
		if u, err := m.Store.GetUser(ctx, mb.UserID); err == nil {
			v.Email, v.Name, v.EmailVerified = u.Email, u.Name, u.EmailVerified
		}
		out = append(out, v)
	}
	return out, nil
}

func (m *Manager) countOwners(ctx context.Context, ws string) int {
	ms, _ := m.Store.ListMemberships(ctx, ws)
	n := 0
	for _, mb := range ms {
		if mb.Role == model.RoleOwner {
			n++
		}
	}
	return n
}

func lastOwnerErr() *model.APIError {
	return model.EH(model.ErrInvalid, "Make another member owner first.", "cannot remove or demote the last owner of the workspace")
}

func ownerOnlyErr() *model.APIError {
	return model.EH(model.ErrPolicyDenied, "Ask a workspace owner.", "only an owner can grant, change or remove the owner role")
}

// PatchMemberInput is PATCH /v1/workspaces/current/members/{user_id}.
type PatchMemberInput struct {
	Role string `json:"role"`
}

func (m *Manager) PatchMember(ctx context.Context, p *Principal, userID string, in PatchMemberInput) (*MemberView, error) {
	if err := p.Can(PermMembers); err != nil {
		return nil, err
	}
	w, err := m.currentWorkspace(ctx, p)
	if err != nil {
		return nil, err
	}
	role := model.NormRole(strings.ToLower(strings.TrimSpace(in.Role)))
	if !model.ValidRole(role) {
		return nil, model.EH(model.ErrInvalid, "Use owner, admin, developer, billing, viewer or auditor.", "unknown role %q", in.Role)
	}
	mb, err := m.Store.GetMembership(ctx, w.ID, userID)
	if err != nil {
		return nil, notFound("member", userID)
	}
	isOwner := p.Platform || p.Role == model.RoleOwner
	if (role == model.RoleOwner || mb.Role == model.RoleOwner) && !isOwner {
		return nil, ownerOnlyErr()
	}
	if mb.Role == model.RoleOwner && role != model.RoleOwner && m.countOwners(ctx, w.ID) <= 1 {
		return nil, lastOwnerErr()
	}
	old := mb.Role
	mb.Role = role
	if err := m.Store.PutMembership(ctx, mb); err != nil {
		return nil, err
	}
	if role == model.RoleOwner && w.OwnerUserID == "" {
		w.OwnerUserID = userID
		m.Store.PutWorkspace(ctx, w)
	}
	m.EmitWS(ctx, w.ID, p.Actor(), "", "", "member.role_changed", "member "+userID+" "+model.NormRole(old)+" → "+role, nil, map[string]any{"user_id": userID, "role": role, "previous": model.NormRole(old)})
	v := &MemberView{UserID: userID, Role: role, JoinedAt: mb.CreatedAt}
	if u, err := m.Store.GetUser(ctx, userID); err == nil {
		v.Email, v.Name, v.EmailVerified = u.Email, u.Name, u.EmailVerified
	}
	return v, nil
}

// RemoveMember removes a member (members:write) or lets a member leave
// (their own user_id). The last owner can neither leave nor be removed.
func (m *Manager) RemoveMember(ctx context.Context, p *Principal, userID string) error {
	self := p.UserID != "" && userID == p.UserID
	if !self {
		if err := p.Can(PermMembers); err != nil {
			return err
		}
	}
	w, err := m.currentWorkspace(ctx, p)
	if err != nil {
		return err
	}
	mb, err := m.Store.GetMembership(ctx, w.ID, userID)
	if err != nil {
		return notFound("member", userID)
	}
	if mb.Role == model.RoleOwner {
		if !self && !p.Platform && p.Role != model.RoleOwner {
			return ownerOnlyErr()
		}
		if m.countOwners(ctx, w.ID) <= 1 {
			return lastOwnerErr()
		}
	}
	if err := m.Store.DeleteMembership(ctx, w.ID, userID); err != nil {
		return err
	}
	if w.OwnerUserID == userID {
		if ms, err := m.Store.ListMemberships(ctx, w.ID); err == nil {
			for _, x := range ms {
				if x.Role == model.RoleOwner {
					w.OwnerUserID = x.UserID
					m.Store.PutWorkspace(ctx, w)
					break
				}
			}
		}
	}
	m.EmitWS(ctx, w.ID, p.Actor(), "", "", "member.removed", "member "+userID+" removed", nil, map[string]any{"user_id": userID, "self": self})
	return nil
}

// ---- invites ----

// WorkspaceInviteInput is POST /v1/workspaces/current/invites.
type WorkspaceInviteInput struct {
	Email string `json:"email"`
	Role  string `json:"role"`
}

// WorkspaceInviteCreated is the create answer: the link is shown once.
type WorkspaceInviteCreated struct {
	*model.WorkspaceInvite
	Status    string `json:"status"`
	InviteURL string `json:"invite_url"`
	Mailed    bool   `json:"mailed"`
}

// WorkspaceInviteRow is one row of GET /v1/workspaces/current/invites.
type WorkspaceInviteRow struct {
	*model.WorkspaceInvite
	Status string `json:"status"` // pending | accepted | expired
}

func inviteStatus(inv *model.WorkspaceInvite, now time.Time) string {
	switch {
	case inv.AcceptedAt != nil:
		return "accepted"
	case !now.Before(inv.ExpiresAt):
		return "expired"
	}
	return "pending"
}

func (m *Manager) CreateWorkspaceInvite(ctx context.Context, p *Principal, in WorkspaceInviteInput) (*WorkspaceInviteCreated, error) {
	if err := p.Can(PermMembers); err != nil {
		return nil, err
	}
	w, err := m.currentWorkspace(ctx, p)
	if err != nil {
		return nil, err
	}
	email, err := cleanEmail(in.Email)
	if err != nil {
		return nil, err
	}
	role := model.NormRole(strings.ToLower(strings.TrimSpace(in.Role)))
	if role == "" {
		role = model.RoleDeveloper
	}
	if !model.ValidRole(role) {
		return nil, model.EH(model.ErrInvalid, "Use owner, admin, developer, billing, viewer or auditor.", "unknown role %q", in.Role)
	}
	if role == model.RoleOwner && !p.Platform && p.Role != model.RoleOwner {
		return nil, ownerOnlyErr()
	}
	if u, err := m.Store.GetUserByEmail(ctx, email); err == nil {
		if _, err := m.Store.GetMembership(ctx, w.ID, u.ID); err == nil {
			return nil, model.EH(model.ErrInvalid, "Change their role in the members list instead.", "%s is already a member", email)
		}
	}
	now := time.Now().UTC()
	// one open invite per email: a new one replaces the old link
	if old, err := m.Store.ListWorkspaceInvites(ctx, w.ID); err == nil {
		for _, x := range old {
			if x.Email == email && x.AcceptedAt == nil {
				m.Store.DeleteWorkspaceInvite(ctx, x.ID)
			}
		}
	}
	if err := m.quotaCount(ctx, w.ID, "max_members"); err != nil {
		return nil, err
	}
	tok := WorkspaceInvitePrefix + secret.RandHex(24)
	inv := &model.WorkspaceInvite{ID: secret.ID("wsi"), WorkspaceID: w.ID, Email: email, Role: role, InvitedBy: p.Actor(),
		TokenHash: secret.Hash(tok), ExpiresAt: now.Add(workspaceInviteTTL), CreatedAt: now}
	if err := m.Store.PutWorkspaceInvite(ctx, inv); err != nil {
		return nil, err
	}
	link := m.dashboardURL("/invite/"+tok, nil)
	inviter := "A Boxmes user"
	if p.User != nil {
		inviter = p.User.Email
		if p.User.Name != "" {
			inviter = p.User.Name + " (" + p.User.Email + ")"
		}
	}
	m.sendMail(ctx, email, "You are invited to "+w.Name,
		inviter+" invited you to the Boxmes workspace \""+w.Name+"\" as "+role+".\n\nOpen this link within 7 days to accept (sign up with this email address if you have no account yet):\n\n"+link+"\n\nIf you do not know this workspace, ignore this email.\n")
	m.EmitWS(ctx, w.ID, p.Actor(), "", "", "member.invited", email+" invited as "+role, nil, map[string]any{"email": email, "role": role, "invite_id": inv.ID})
	return &WorkspaceInviteCreated{WorkspaceInvite: inv, Status: "pending", InviteURL: link, Mailed: m.mailConfigured(ctx)}, nil
}

func (m *Manager) ListWorkspaceInvites(ctx context.Context, p *Principal) ([]*WorkspaceInviteRow, error) {
	if err := p.Can(PermMembers); err != nil {
		return nil, err
	}
	w, err := m.currentWorkspace(ctx, p)
	if err != nil {
		return nil, err
	}
	l, err := m.Store.ListWorkspaceInvites(ctx, w.ID)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	out := make([]*WorkspaceInviteRow, 0, len(l))
	for _, inv := range l {
		out = append(out, &WorkspaceInviteRow{WorkspaceInvite: inv, Status: inviteStatus(inv, now)})
	}
	return out, nil
}

func (m *Manager) DeleteWorkspaceInvite(ctx context.Context, p *Principal, id string) error {
	if err := p.Can(PermMembers); err != nil {
		return err
	}
	w, err := m.currentWorkspace(ctx, p)
	if err != nil {
		return err
	}
	inv, err := m.Store.GetWorkspaceInvite(ctx, id)
	if err != nil || inv.WorkspaceID != w.ID {
		return notFound("invite", id)
	}
	m.EmitWS(ctx, w.ID, p.Actor(), "", "", "member.invite_revoked", "invite for "+inv.Email+" revoked", nil, map[string]any{"invite_id": id})
	return m.Store.DeleteWorkspaceInvite(ctx, id)
}

// InvitePreview is GET /v1/workspace-invites/{token} (no auth).
type InvitePreview struct {
	Workspace     map[string]string `json:"workspace"` // id, name
	Email         string            `json:"email"`
	Role          string            `json:"role"`
	InvitedBy     string            `json:"invited_by"` // inviter's name/email when a user
	ExpiresAt     time.Time         `json:"expires_at"`
	Status        string            `json:"status"`         // pending | accepted | expired
	AccountExists bool              `json:"account_exists"` // sign in (true) or sign up with workspace_invite (false)
}

func badInviteErr() *model.APIError {
	return model.EH(model.ErrNotFound, "Ask for a new invite.", "workspace invite not found")
}

func (m *Manager) inviteByToken(ctx context.Context, tok string) (*model.WorkspaceInvite, error) {
	tok = strings.TrimSpace(tok)
	if !strings.HasPrefix(tok, WorkspaceInvitePrefix) {
		return nil, badInviteErr()
	}
	inv, err := m.Store.GetWorkspaceInviteByHash(ctx, secret.Hash(tok))
	if err != nil {
		return nil, badInviteErr()
	}
	return inv, nil
}

func (m *Manager) PreviewWorkspaceInvite(ctx context.Context, tok string) (*InvitePreview, error) {
	inv, err := m.inviteByToken(ctx, tok)
	if err != nil {
		return nil, err
	}
	w, err := m.Store.GetWorkspace(ctx, inv.WorkspaceID)
	if err != nil {
		return nil, badInviteErr()
	}
	v := &InvitePreview{Workspace: map[string]string{"id": w.ID, "name": w.Name}, Email: inv.Email, Role: inv.Role,
		ExpiresAt: inv.ExpiresAt, Status: inviteStatus(inv, time.Now()), InvitedBy: inv.InvitedBy}
	if strings.HasPrefix(inv.InvitedBy, "user:") {
		if u, err := m.Store.GetUser(ctx, strings.TrimPrefix(inv.InvitedBy, "user:")); err == nil {
			v.InvitedBy = u.Email
			if u.Name != "" {
				v.InvitedBy = u.Name + " <" + u.Email + ">"
			}
		}
	}
	if _, err := m.Store.GetUserByEmail(ctx, inv.Email); err == nil {
		v.AccountExists = true
	}
	return v, nil
}

// AcceptWorkspaceInvite joins the invited workspace (signed-in user whose
// email is the invited one) and makes it the session's current workspace.
func (m *Manager) AcceptWorkspaceInvite(ctx context.Context, p *Principal, tok string) (*MeView, error) {
	if err := requireUser(p); err != nil {
		return nil, err
	}
	inv, err := m.inviteByToken(ctx, tok)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(inv.Email, p.User.Email) {
		return nil, model.EH(model.ErrPolicyDenied, "Sign in with the invited email address.", "this invite is for another email address")
	}
	if st := inviteStatus(inv, time.Now()); st != "pending" {
		return nil, model.EH(model.ErrInvalid, "Ask for a new invite.", "workspace invite is %s", st)
	}
	mb, err := m.Store.AcceptWorkspaceInvite(ctx, inv.ID, p.UserID, time.Now().UTC())
	if errors.Is(err, store.ErrWorkspaceInviteUnavailable) {
		return nil, model.EH(model.ErrInvalid, "Ask for a new invite.", "workspace invite is %s", "used")
	}
	if err != nil {
		return nil, err
	}
	m.EmitWS(ctx, inv.WorkspaceID, p.Actor(), "", "", "member.joined", p.User.Email+" joined as "+mb.Role, nil, map[string]any{"user_id": p.UserID, "role": mb.Role})
	return m.SwitchWorkspace(ctx, p, inv.WorkspaceID)
}

// ---- platform admin: workspaces ----

// AdminWorkspaceRow is one row of GET /v1/admin/workspaces.
type AdminWorkspaceRow struct {
	*model.Workspace
	Plan          string  `json:"plan"`
	Members       int     `json:"members"`
	Boxes         int     `json:"boxes"`
	SpentMonthUSD float64 `json:"spent_month_usd"`
}

func (m *Manager) ListWorkspacesAdmin(ctx context.Context, p *Principal) ([]*AdminWorkspaceRow, error) {
	if err := requirePlatformAdmin(p); err != nil {
		return nil, err
	}
	ws, err := m.Store.ListWorkspaces(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*AdminWorkspaceRow, 0, len(ws))
	for _, w := range ws {
		r := &AdminWorkspaceRow{Workspace: w, Plan: m.planOf(w)}
		if w.Status == "" {
			w.Status = "active"
		}
		if ms, err := m.Store.ListMemberships(ctx, w.ID); err == nil {
			r.Members = len(ms)
		}
		if bs, err := m.Store.ListBoxes(ctx, store.BoxFilter{WorkspaceID: w.ID}); err == nil {
			r.Boxes = len(bs)
		}
		r.SpentMonthUSD, _ = m.Store.SumLedger(ctx, store.LedgerFilter{WorkspaceID: w.ID, From: monthStart(time.Now())})
		r.SpentMonthUSD = model.Round4(r.SpentMonthUSD)
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (m *Manager) PatchWorkspaceAdmin(ctx context.Context, p *Principal, id string, in PatchWorkspaceInput) (*CurrentWorkspaceView, error) {
	if err := requirePlatformAdmin(p); err != nil {
		return nil, err
	}
	w, err := m.Store.GetWorkspace(ctx, id)
	if err != nil {
		return nil, notFound("workspace", id)
	}
	return m.patchWorkspace(ctx, p, w, in)
}
