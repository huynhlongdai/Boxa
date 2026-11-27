package manager

// v2.0 per-workspace quotas from the workspace plan (model.Plans) plus its
// overrides. Checked when boxes are created, woken, forked, cloned or
// restored, and when agents, keys, secrets and members are added.

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"boxmes/core/internal/egress"
	"boxmes/core/internal/model"
	"boxmes/core/internal/store"
)

// planOf returns the plan id of a workspace ("" → instance default; the
// operator's default workspace is unlimited).
func (m *Manager) planOf(w *model.Workspace) string {
	if w != nil && w.Plan != "" {
		if _, ok := model.Plans[w.Plan]; ok {
			return w.Plan
		}
	}
	if w != nil && w.ID == model.DefaultWorkspaceID {
		return model.PlanUnlimited
	}
	return m.defaultPlan()
}

func (m *Manager) defaultPlan() string {
	p := strings.ToLower(strings.TrimSpace(m.Cfg.DefaultPlan))
	if _, ok := model.Plans[p]; ok {
		return p
	}
	return model.PlanFree
}

// WorkspaceQuota is the effective quota of workspace ws.
func (m *Manager) WorkspaceQuota(ctx context.Context, ws string) (model.Quota, *model.Workspace) {
	w, err := m.Store.GetWorkspace(ctx, ws)
	if err != nil {
		w = nil
	}
	q := model.Plans[m.planOf(w)]
	if w != nil {
		q = w.Overrides.Apply(q)
	}
	return q, w
}

func (m *Manager) quotaErr(ctx context.Context, q model.Quota, name string, limit, used any) *model.APIError {
	e := model.EH(model.ErrQuotaExceeded, "Free capacity (sleep or delete boxes, remove unused items) or upgrade the workspace plan.",
		"workspace quota %s exceeded (limit %v, used %v)", name, limit, used)
	e.Details = map[string]any{"quota": name, "limit": limit, "used": used, "plan": q.Plan, "upgrade_url": m.upgradeURL()}
	e.Next = []string{"GET /v1/workspaces/current/usage", "GET /v1/plans"}
	return e
}

func (m *Manager) upgradeURL() string {
	base := strings.TrimRight(m.Cfg.DashboardURL, "/")
	if base == "" {
		base = strings.TrimRight(m.Cfg.PublicURL, "/")
	}
	return base + "/settings/billing"
}

func (m *Manager) limitErr(q model.Quota, what string, limit any) *model.APIError {
	e := model.EH(model.ErrQuotaExceeded, "Ask for less or upgrade the workspace plan.", "%s is limited to %v on the %s plan", what, limit, q.Plan)
	e.Details = map[string]any{"quota": what, "limit": limit, "plan": q.Plan, "upgrade_url": m.upgradeURL()}
	e.Next = []string{"GET /v1/workspaces/current/usage", "GET /v1/plans"}
	return e
}

func suspendedErr() *model.APIError {
	return model.EH(model.ErrPolicyDenied, "Contact the operator of this Boxmes instance.", "workspace is suspended")
}

// quotaCount checks a countable limit before adding one item.
func (m *Manager) quotaCount(ctx context.Context, ws, which string) error {
	q, w := m.WorkspaceQuota(ctx, ws)
	if w != nil && w.Status == "suspended" {
		return suspendedErr()
	}
	limit, used := 0, 0
	switch which {
	case "max_agents":
		limit = q.MaxAgents
		if limit > 0 {
			l, _ := m.Store.ListAgentsInWorkspace(ctx, ws)
			for _, a := range l {
				if a.Name != "operator" {
					used++
				}
			}
		}
	case "max_keys":
		limit = q.MaxKeys
		if limit > 0 {
			l, _ := m.Store.ListKeysInWorkspace(ctx, ws)
			for _, k := range l {
				if k.RevokedAt == nil && (k.ExpiresAt == nil || time.Now().Before(*k.ExpiresAt)) {
					used++
				}
			}
		}
	case "max_secrets":
		limit = q.MaxSecrets
		if limit > 0 {
			l, _ := m.Store.ListSecretsInWorkspace(ctx, ws)
			for _, s := range l {
				if !s.System {
					used++
				}
			}
		}
	case "max_members":
		limit = q.MaxMembers
		if limit > 0 {
			l, _ := m.Store.ListMemberships(ctx, ws)
			used = len(l)
			if inv, err := m.Store.ListWorkspaceInvites(ctx, ws); err == nil {
				for _, i := range inv {
					if i.AcceptedAt == nil && time.Now().Before(i.ExpiresAt) {
						used++
					}
				}
			}
		}
	}
	if limit > 0 && used >= limit {
		return m.quotaErr(ctx, q, which, limit, used)
	}
	return nil
}

// runningBoxes counts boxes of ws that hold capacity and are not sleeping.
func (m *Manager) runningBoxes(ctx context.Context, ws, exclude string) int {
	bs, _ := m.Store.ListBoxes(ctx, store.BoxFilter{WorkspaceID: ws})
	n := 0
	for _, b := range bs {
		if b.ID != exclude && b.Status != model.StatusSleeping {
			n++
		}
	}
	return n
}

// createRate remembers recent box creations per workspace.
var createRate = struct {
	sync.Mutex
	m map[string][]time.Time
}{m: map[string][]time.Time{}}

func rateCount(ws string, now time.Time, add bool) int {
	createRate.Lock()
	defer createRate.Unlock()
	keep := createRate.m[ws][:0]
	for _, t := range createRate.m[ws] {
		if now.Sub(t) < time.Minute {
			keep = append(keep, t)
		}
	}
	if add {
		keep = append(keep, now)
	}
	createRate.m[ws] = keep
	return len(keep)
}

// BoxQuotaInput is what a box create/fork/clone asks for.
type BoxQuotaInput struct {
	CPU        float64
	RAMMB      int
	TTLSeconds *int // capped in place when the plan has a TTL limit and none was asked
	Ports      []int
	Public     bool // an explicit public port was asked for
	New        bool // counts against the create rate
}

// checkBoxQuota enforces the per-box and concurrency limits of workspace ws.
func (m *Manager) checkBoxQuota(ctx context.Context, ws string, in BoxQuotaInput) error {
	q, w := m.WorkspaceQuota(ctx, ws)
	if w != nil && w.Status == "suspended" {
		return suspendedErr()
	}
	if q.MaxCPUPerBox > 0 && in.CPU > q.MaxCPUPerBox {
		return m.limitErr(q, "max_cpu_per_box", q.MaxCPUPerBox)
	}
	if q.MaxRAMMBPerBox > 0 && in.RAMMB > q.MaxRAMMBPerBox {
		return m.limitErr(q, "max_ram_mb_per_box", q.MaxRAMMBPerBox)
	}
	if q.MaxTTLHours > 0 && in.TTLSeconds != nil {
		max := q.MaxTTLHours * 3600
		if *in.TTLSeconds > max {
			return m.limitErr(q, "max_ttl_hours", q.MaxTTLHours)
		}
		if *in.TTLSeconds <= 0 {
			*in.TTLSeconds = max
		}
	}
	if in.Public && !q.PublicURL {
		return m.limitErr(q, "public_url", false)
	}
	if q.MaxBoxes > 0 {
		if n := m.runningBoxes(ctx, ws, ""); n >= q.MaxBoxes {
			return m.quotaErr(ctx, q, "max_boxes", q.MaxBoxes, n)
		}
	}
	if in.New && q.MaxBoxesPerMinute > 0 {
		now := time.Now()
		if n := rateCount(ws, now, false); n >= q.MaxBoxesPerMinute {
			e := m.quotaErr(ctx, q, "max_boxes_per_minute", q.MaxBoxesPerMinute, n)
			return e.WithRetry(60)
		}
		rateCount(ws, now, true)
	}
	return nil
}

// checkWakeQuota: waking a sleeping box takes a concurrency slot again.
func (m *Manager) checkWakeQuota(ctx context.Context, b *model.Box) error {
	q, w := m.WorkspaceQuota(ctx, b.WorkspaceID)
	if w != nil && w.Status == "suspended" {
		return suspendedErr()
	}
	if q.MaxBoxes > 0 {
		if n := m.runningBoxes(ctx, b.WorkspaceID, b.ID); n >= q.MaxBoxes {
			return m.quotaErr(ctx, q, "max_boxes", q.MaxBoxes, n)
		}
	}
	return nil
}

// planEgress applies a restricted plan to an effective egress policy:
// unrestricted becomes the plan allowlist; an explicit allowlist must stay
// inside it.
func (m *Manager) planEgress(ctx context.Context, ws string, eff *model.BoxEgress) (*model.BoxEgress, error) {
	q, _ := m.WorkspaceQuota(ctx, ws)
	if q.Egress != model.EgressRestricted {
		return eff, nil
	}
	if eff == nil || (!eff.DenyByDefault && len(eff.Allow) == 0) {
		return &model.BoxEgress{Allow: append([]string{}, model.EgressRestrictedAllow...), DenyByDefault: true, Source: "plan"}, nil
	}
	if ok, bad := egress.Within(eff.Allow, model.EgressRestrictedAllow); !ok {
		e := m.limitErr(q, "egress", model.EgressRestricted)
		e.Details["host"] = bad
		return nil, e
	}
	eff.DenyByDefault = true
	return eff, nil
}

// publicDefault: on plans without public URLs every port defaults to token access.
func (m *Manager) publicAllowed(ctx context.Context, ws string) bool {
	q, _ := m.WorkspaceQuota(ctx, ws)
	return q.PublicURL
}

// Usage is GET /v1/workspaces/current/usage.
type Usage struct {
	WorkspaceID string      `json:"workspace_id"`
	Plan        string      `json:"plan"`
	Quota       model.Quota `json:"quota"`
	Used        UsageCounts `json:"used"`
}

type UsageCounts struct {
	Boxes         int     `json:"boxes"`       // non-sleeping (counts against max_boxes)
	BoxesTotal    int     `json:"boxes_total"` // active incl. sleeping
	Agents        int     `json:"agents"`      // excluding the implicit "operator" agent
	Keys          int     `json:"keys"`        // active workspace + agent keys
	Secrets       int     `json:"secrets"`
	Members       int     `json:"members"`
	SpentTodayUSD float64 `json:"spent_today_usd"`
	SpentMonthUSD float64 `json:"spent_month_usd"`
}

func (m *Manager) WorkspaceUsage(ctx context.Context, p *Principal) (*Usage, error) {
	if err := p.Can(PermView); err != nil {
		return nil, err
	}
	ws, err := p.TargetWS()
	if err != nil {
		return nil, err
	}
	q, _ := m.WorkspaceQuota(ctx, ws)
	u := &Usage{WorkspaceID: ws, Plan: q.Plan, Quota: q}
	bs, _ := m.Store.ListBoxes(ctx, store.BoxFilter{WorkspaceID: ws})
	u.Used.BoxesTotal = len(bs)
	u.Used.Boxes = m.runningBoxes(ctx, ws, "")
	if l, err := m.Store.ListAgentsInWorkspace(ctx, ws); err == nil {
		for _, a := range l {
			if a.Name != "operator" {
				u.Used.Agents++
			}
		}
	}
	if l, err := m.Store.ListKeysInWorkspace(ctx, ws); err == nil {
		for _, k := range l {
			if k.RevokedAt == nil && (k.ExpiresAt == nil || time.Now().Before(*k.ExpiresAt)) {
				u.Used.Keys++
			}
		}
	}
	if l, err := m.Store.ListSecretsInWorkspace(ctx, ws); err == nil {
		for _, s := range l {
			if !s.System {
				u.Used.Secrets++
			}
		}
	}
	if l, err := m.Store.ListMemberships(ctx, ws); err == nil {
		u.Used.Members = len(l)
	}
	now := time.Now()
	u.Used.SpentTodayUSD, _ = m.Store.SumLedger(ctx, store.LedgerFilter{WorkspaceID: ws, From: dayStart(now)})
	u.Used.SpentMonthUSD, _ = m.Store.SumLedger(ctx, store.LedgerFilter{WorkspaceID: ws, From: monthStart(now)})
	u.Used.SpentTodayUSD, u.Used.SpentMonthUSD = model.Round4(u.Used.SpentTodayUSD), model.Round4(u.Used.SpentMonthUSD)
	return u, nil
}

// PlanView is one row of GET /v1/plans.
type PlanView struct {
	ID    string      `json:"id"`
	Quota model.Quota `json:"quota"`
}

// ListPlans lists the built-in plans (quota only; prices land with v2.1 billing).
func (m *Manager) ListPlans() []PlanView {
	out := []PlanView{}
	for _, id := range model.PlanIDs {
		out = append(out, PlanView{ID: id, Quota: model.Plans[id]})
	}
	return out
}

var _ = fmt.Sprintf
