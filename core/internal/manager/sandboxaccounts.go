package manager

// M29-S: sandbox providers with several API keys (accounts). What differs
// from VPS accounts (accounts.go):
//
//   - templates / snapshots are account-scoped (E2B project, Novita team,
//     Daytona project): a create from template X only goes to an account
//     that has X (manual list, synced list, credential "templates" field,
//     or not yet known); a "template not found" answer is learned on the
//     account and the create moves on to the next account;
//   - concurrency and create rate are per account (or per limit group:
//     keys of one E2B project / projects of one Daytona organization share
//     them) and enforced at selection with live counts: running sandboxes
//     (paused ones do not count, as at E2B and Daytona) + creates in flight
//     against concurrent_sandboxes, starts in the last second / minute
//     against create_per_sec / create_per_min;
//   - a box stays on its account for its whole life: pause/resume, native
//     fork, preview headers and destroy use the host's account; a resume is
//     refused with a clear error when that account is full, a native fork
//     falls back to snapshot → new sandbox on another account when the
//     source account cannot take one more sandbox;
//   - accounts sharing a wallet (billing_group) run out of credit together
//     (E2B blocks every project of a workspace when its credit is gone);
//   - a rotated key reaches the live T3 shims of the account at once.

import (
	"context"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"time"

	"boxmes/core/internal/model"
	"boxmes/core/internal/provider"
	"boxmes/core/internal/sandbox"
	"boxmes/core/internal/store"
)

// sbxState holds the live slots of sandbox accounts (in memory: a restart
// only loses creates that were in flight anyway).
type sbxState struct {
	mu       sync.Mutex
	inflight map[string]map[string]bool // limit key → slot (host id, fork:<box>, wake:<host>)
	starts   map[string][]time.Time     // limit key → create starts of the last minute
}

func (s *sbxState) init() {
	if s.inflight == nil {
		s.inflight, s.starts = map[string]map[string]bool{}, map[string][]time.Time{}
	}
}

// limitKey: accounts of one limit group share their slots.
func limitKey(a *model.ProviderAccount) string {
	if a.LimitGroup != "" {
		return a.Provider + "|g:" + a.LimitGroup
	}
	return a.ID
}

// limitMembers is the account plus the other live accounts of its limit group.
func (m *Manager) limitMembers(ctx context.Context, a *model.ProviderAccount) []*model.ProviderAccount {
	if a.LimitGroup == "" {
		return []*model.ProviderAccount{a}
	}
	out := []*model.ProviderAccount{a}
	for _, x := range m.listAccounts(ctx, a.Provider) {
		if x.ID != a.ID && x.LimitGroup == a.LimitGroup && x.Status != model.AccountArchived {
			out = append(out, x)
		}
	}
	return out
}

// groupLimit is the tightest positive limit k over the members.
func groupLimit(members []*model.ProviderAccount, k string) (float64, string) {
	lim, src := 0.0, ""
	for _, a := range members {
		if l, ok := a.Limits[k]; ok && l.Limit > 0 && (lim == 0 || l.Limit < lim) {
			lim, src = l.Limit, l.Source
		}
	}
	return lim, src
}

// pausedHost: every active box of the host sleeps (a paused sandbox does
// not count toward concurrency at E2B / Daytona).
func pausedHost(boxesOf map[string][]*model.Box, hostID string) bool {
	bs := boxesOf[hostID]
	if len(bs) == 0 {
		return false // provisioning / box not registered yet: counts
	}
	for _, b := range bs {
		if b.Status != model.StatusSleeping {
			return false
		}
	}
	return true
}

func boxesByHost(boxes []*model.Box) map[string][]*model.Box {
	out := map[string][]*model.Box{}
	for _, b := range boxes {
		if b.HostID != "" && b.Active() {
			out[b.HostID] = append(out[b.HostID], b)
		}
	}
	return out
}

// runningOn counts the running sandboxes of the given accounts (paused and
// in-flight slots excluded; slots are counted separately).
func runningOn(hosts []*model.Host, boxesOf map[string][]*model.Box, ids map[string]bool, skip map[string]bool) int {
	n := 0
	for _, h := range hosts {
		if !ids[h.AccountID] || !liveHost(h) || skip[h.ID] || pausedHost(boxesOf, h.ID) {
			continue
		}
		n++
	}
	return n
}

// fillSandboxStats adds the live sandbox figures to an account's stats.
func (m *Manager) fillSandboxStats(ctx context.Context, a *model.ProviderAccount, st *model.AccountStats, hosts []*model.Host, boxes []*model.Box, now time.Time) {
	if boxes == nil {
		boxes, _ = m.Store.ListBoxes(ctx, store.BoxFilter{})
	}
	by := boxesByHost(boxes)
	key := limitKey(a)
	m.sbx.mu.Lock()
	m.sbx.init()
	skip := map[string]bool{}
	for s := range m.sbx.inflight[key] {
		skip[s] = true
	}
	st.InFlight = len(m.sbx.inflight[key])
	for _, t := range m.sbx.starts[key] {
		if now.Sub(t) < time.Second {
			st.CreatesLastSec++
		}
		if now.Sub(t) < time.Minute {
			st.CreatesLastMin++
		}
	}
	m.sbx.mu.Unlock()
	st.SandboxStats = true
	st.SandboxesRunning = runningOn(hosts, by, map[string]bool{a.ID: true}, skip)
	ids := map[string]bool{}
	for _, x := range m.limitMembers(ctx, a) {
		ids[x.ID] = true
	}
	st.GroupRunning = runningOn(hosts, by, ids, skip)
}

// slotKind: a create (concurrency + create rate) or a resume (concurrency only).
const (
	slotCreate = "create"
	slotResume = "resume"
)

// reserveSandbox takes a slot on the account (its limit group) for one more
// running sandbox. why != "" refuses it; wait > 0 says when a create-rate
// slot frees. release must be called once the create / fork / resume ends.
func (m *Manager) reserveSandbox(ctx context.Context, a *model.ProviderAccount, slot, kind string) (release func(), why string, wait time.Duration) {
	members := m.limitMembers(ctx, a)
	hosts, _ := m.Store.ListHosts(ctx)
	boxes, _ := m.Store.ListBoxes(ctx, store.BoxFilter{})
	by := boxesByHost(boxes)
	ids := map[string]bool{}
	for _, x := range members {
		ids[x.ID] = true
	}
	key := limitKey(a)
	now := time.Now()
	m.sbx.mu.Lock()
	defer m.sbx.mu.Unlock()
	m.sbx.init()
	var keep []time.Time
	for _, t := range m.sbx.starts[key] {
		if now.Sub(t) < time.Minute {
			keep = append(keep, t)
		}
	}
	m.sbx.starts[key] = keep
	in := m.sbx.inflight[key]
	if in == nil {
		in = map[string]bool{}
		m.sbx.inflight[key] = in
	}
	running := runningOn(hosts, by, ids, in)
	scope := "account"
	if a.LimitGroup != "" {
		scope = "limit group " + a.LimitGroup
	}
	if l, src := groupLimit(members, "concurrent_sandboxes"); l > 0 && float64(running+len(in)+1) > l+1e-9 {
		return nil, fmt.Sprintf("concurrent_sandboxes limit %.0f of the %s reached (%d running, %d starting; %s)", l, scope, running, len(in), orStr(src, "manual")), 0
	}
	if kind == slotCreate {
		for _, w := range []struct {
			k   string
			win time.Duration
		}{{"create_per_sec", time.Second}, {"create_per_min", time.Minute}} {
			l, _ := groupLimit(members, w.k)
			if l <= 0 {
				continue
			}
			var inWin []time.Time
			for _, t := range keep {
				if now.Sub(t) < w.win {
					inWin = append(inWin, t)
				}
			}
			if float64(len(inWin)) >= l {
				// the window has room again when the start that keeps it at
				// the limit leaves it
				free := inWin[len(inWin)-int(l)].Add(w.win).Sub(now)
				return nil, fmt.Sprintf("%s limit %.0f of the %s reached", w.k, l, scope), free
			}
		}
		m.sbx.starts[key] = append(m.sbx.starts[key], now)
	}
	in[slot] = true
	var once sync.Once
	return func() {
		once.Do(func() {
			m.sbx.mu.Lock()
			delete(m.sbx.inflight[key], slot)
			m.sbx.mu.Unlock()
		})
	}, "", 0
}

// ---- templates ----

// accountTemplateSet: templates known to exist in the account (manual or
// synced list + the credential's own "templates" field, E2B / Novita).
func accountTemplateSet(sp sandbox.Provider, a *model.ProviderAccount, tok string) map[string]bool {
	set := map[string]bool{}
	for _, t := range a.Templates {
		set[t] = true
	}
	if sz, ok := sp.Driver().(sandbox.Sizer); ok && tok != "" {
		c := sandbox.ParseCred(tok)
		if strings.TrimSpace(c["templates"]) != "" {
			for _, z := range sz.SizesFor(c) {
				set[z.SKU] = true
			}
		}
	}
	return set
}

// sandboxTemplate is the provider template a create of sku needs ("" when
// the SKU is a plain size and the template is the provider default).
func sandboxTemplate(sp sandbox.Provider, sku string) string {
	if _, ok := sp.Driver().(sandbox.Sizer); ok {
		return sku // E2B / Novita: SKU = template id
	}
	return ""
}

// templateState of one template on one account: available | missing |
// not_listed | unknown (the account has no list: tried, a 404 teaches).
func templateState(sp sandbox.Provider, a *model.ProviderAccount, tok, tpl string) string {
	for _, x := range a.TemplatesMissing {
		if x == tpl {
			return "missing"
		}
	}
	if sandbox.GlobalTemplate(sp.Info(), tpl) {
		return "available"
	}
	set := accountTemplateSet(sp, a, tok)
	switch {
	case set[tpl]:
		return "available"
	case len(set) == 0:
		return "unknown"
	}
	return "not_listed"
}

// sandboxScopes: scopes a sandbox key must hold when the account lists its
// scopes (Daytona API keys carry permissions; read is always granted).
var sandboxScopes = map[string][]string{"daytona": {"write:sandboxes"}}

// sandboxScopesWarn: scopes whose absence does not block creates but
// leaves sandboxes Boxmes cannot clean up.
var sandboxScopesWarn = map[string][]string{"daytona": {"delete:sandboxes"}}

func missingScopes(a *model.ProviderAccount, need []string) []string {
	if len(a.Scopes) == 0 {
		return nil // not recorded: unknown
	}
	has := map[string]bool{}
	for _, s := range a.Scopes {
		has[s] = true
	}
	var out []string
	for _, s := range need {
		if !has[s] {
			out = append(out, s)
		}
	}
	return out
}

// sandboxIneligible adds the sandbox checks to the generic ones (spec
// M29-S §3): template availability and key scopes.
func (m *Manager) sandboxIneligible(ctx context.Context, sp sandbox.Provider, a *model.ProviderAccount, tpl string) (why string, isTemplate bool) {
	if miss := missingScopes(a, sandboxScopes[a.Provider]); len(miss) > 0 {
		return "key lacks scope " + strings.Join(miss, ", "), false
	}
	if tpl == "" {
		return "", false
	}
	tok, _ := m.credentialFor(ctx, a)
	switch templateState(sp, a, tok, tpl) {
	case "missing":
		return fmt.Sprintf("template %s missing in this account", tpl), true
	case "not_listed":
		return fmt.Sprintf("template %s not in this account's templates", tpl), true
	}
	return "", false
}

// learnTemplateMissing records that the account has no template tpl.
func (m *Manager) learnTemplateMissing(ctx context.Context, a *model.ProviderAccount, tpl string, err error) {
	if tpl == "" {
		return
	}
	l := m.acctLock(a.ID)
	l.Lock()
	defer l.Unlock()
	cur, gerr := m.Store.GetProviderAccount(ctx, a.ID)
	if gerr != nil {
		return
	}
	normAccount(cur)
	for _, x := range cur.TemplatesMissing {
		if x == tpl {
			return
		}
	}
	cur.TemplatesMissing = append(cur.TemplatesMissing, tpl)
	var keep []string
	for _, x := range cur.Templates { // a synced / manual entry was wrong
		if x != tpl {
			keep = append(keep, x)
		}
	}
	cur.Templates = nonNilStrs(keep)
	m.putAccount(ctx, cur)
	m.EmitData(ctx, "system", "", "", "provider_account.template_missing", fmt.Sprintf("%s %s: template %s not found with this key (%v)", cur.Provider, cur.Label, tpl, err), nil,
		map[string]any{"provider": cur.Provider, "account_id": cur.ID, "template": tpl})
}

// ---- billing group ----

// syncBillingGroup: accounts paying from one wallet share out_of_credit
// (E2B: "every project in your workspace is blocked" when the credit runs
// out). out = true marks the whole group out of credit (a 402 on one key);
// out = false lifts it on every member (a top-up / manual balance on one).
func (m *Manager) syncBillingGroup(ctx context.Context, a *model.ProviderAccount, out bool) {
	if a.BillingGroup == "" {
		return
	}
	for _, x := range m.listAccounts(ctx, a.Provider) {
		if x.ID == a.ID || x.BillingGroup != a.BillingGroup || x.Status == model.AccountArchived {
			continue
		}
		l := m.acctLock(x.ID)
		l.Lock()
		cur, err := m.Store.GetProviderAccount(ctx, x.ID)
		if err != nil {
			l.Unlock()
			continue
		}
		normAccount(cur)
		old, now := cur.Status, time.Now().UTC()
		switch {
		case out && cur.Status != model.AccountOutOfCredit && cur.Status != model.AccountDisabled:
			cur.Status, cur.StatusReason = model.AccountOutOfCredit, "billing group "+a.BillingGroup+": "+a.Label+" is out of credit"
		case !out && cur.Status == model.AccountOutOfCredit:
			cur.Status, cur.StatusReason = model.AccountActive, ""
		default:
			l.Unlock()
			continue
		}
		m.setAccountStatus(ctx, cur, old, now)
		m.putAccount(ctx, cur)
		l.Unlock()
	}
}

// ---- selection + create across accounts ----

// pickReq is one create request for account selection.
type pickReq struct {
	prov, region string
	priceHour    float64
	prefer, what string
	sp           sandbox.Provider // set for sandbox creates
	template     string           // provider template the create needs
	slot         string           // reservation slot (host id)
}

// acrossSandboxAccounts is acrossAccounts for a sandbox create: eligible
// accounts are also filtered by template availability and key scopes, a
// slot (concurrency + create rate, live counts) is reserved on the account
// before the create, and a "template not found" answer is learned and
// tried on the next account.
func (m *Manager) acrossSandboxAccounts(ctx context.Context, rq pickReq, fn func(a *model.ProviderAccount, tok string) error) (*model.ProviderAccount, error) {
	accts, reasons := m.pickAccounts(ctx, rq.prov, rq.region, rq.priceHour, rq.prefer)
	var ok []*model.ProviderAccount
	tplOnly := len(reasons) == 0 // every account was refused for the template alone
	for _, a := range accts {
		why, isTpl := m.sandboxIneligible(ctx, rq.sp, a, rq.template)
		if why != "" {
			reasons = append(reasons, a.Label+" ("+a.ID+"): "+why)
			tplOnly = tplOnly && isTpl
			continue
		}
		ok = append(ok, a)
	}
	if len(ok) == 0 {
		if len(reasons) == 0 {
			return nil, model.EH(model.ErrProviderFailed, "Connect the provider first: PUT /providers/"+rq.prov+"/token.", "provider %s not connected", rq.prov)
		}
		if tplOnly && rq.template != "" {
			return nil, model.EH(model.ErrProviderFailed, "Build or publish the template in one of the accounts, then list it on that account (PATCH /v1/admin/providers/{p}/accounts/{a} templates, or POST …/accounts/{a}/templates/sync); Boxmes fails over to the next provider.",
				"%s: template %s is not available in any account (%s)", rq.prov, rq.template, strings.Join(reasons, "; "))
		}
		return nil, model.EH(model.ErrProviderFailed, "Check Infra → Accounts: enable an account, raise its caps or top it up; Boxmes fails over to the next provider.", "%s: no eligible account (%s)", rq.prov, strings.Join(reasons, "; "))
	}
	pp := m.ProviderPrefs(ctx, rq.prov)
	if !failoverAcross(pp) {
		ok = ok[:1]
	}
	if len(ok) > maxAccountsPerCreate {
		ok = ok[:maxAccountsPerCreate]
	}
	var lastErr error
	var skipped []string
	attempts := 0
	for i := 0; i < len(ok); i++ {
		a := ok[i]
		if attempts >= maxAttemptsPerCreate {
			break
		}
		release, why, wait := m.reserveSandbox(ctx, a, rq.slot, slotCreate)
		if why != "" {
			// a create-rate slot frees within seconds: on the last candidate
			// queue for it (up to 6 s in all) instead of failing the create
			waited := time.Duration(0)
			for why != "" && wait > 0 && wait <= 2*time.Second && waited+wait <= 6*time.Second && i == len(ok)-1 {
				d := wait + time.Duration(10+rand.Intn(40))*time.Millisecond
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(d):
				}
				waited += d
				release, why, wait = m.reserveSandbox(ctx, a, rq.slot, slotCreate)
			}
			if why != "" {
				skipped = append(skipped, a.Label+" ("+a.ID+"): "+why)
				continue
			}
		}
		attempts++
		tok, err := m.credentialFor(ctx, a)
		if err != nil {
			release()
			lastErr = err
			continue
		}
		err = fn(a, tok)
		release()
		if err == nil {
			m.rememberSticky(ctx, rq.prov, a, pp)
			return a, nil
		}
		lastErr = err
		cls := "template_missing"
		if sandbox.IsTemplateMissing(err) {
			m.learnTemplateMissing(ctx, a, rq.template, err)
		} else if stop, c := m.failoverStops(rq.prov, rq.region, err); stop {
			return a, err
		} else {
			cls = c
		}
		if i+1 < len(ok) {
			next := ok[i+1]
			m.EmitData(ctx, "system", "", "", "provider_account.failover", fmt.Sprintf("%s: %s failed (%s), trying %s", rq.prov, a.Label, cls, next.Label), nil,
				map[string]any{"provider": rq.prov, "from": a.ID, "to": next.ID, "class": cls, "request": rq.what})
			m.raiseAlert(ctx, alertInput{Type: "failover", Level: "info", Provider: rq.prov, AccountID: a.ID,
				Args: []any{rq.prov, a.Label, next.Label, cls}})
		}
	}
	if lastErr == nil && len(skipped) > 0 {
		return nil, model.EH(model.ErrProviderFailed, "Every account is at its concurrency or create-rate limit: wait for sandboxes to end, raise the limits (PATCH …/accounts/{a} limits) or add an account; Boxmes fails over to the next provider.",
			"%s: every account is full (%s)", rq.prov, strings.Join(skipped, "; ")).WithRetry(1)
	}
	return nil, lastErr
}

// ---- lifecycle on the box's own account ----

// sandboxWakeCheck reserves a concurrency slot on the account of a sleeping
// sandbox box before it resumes: a paused sandbox can only resume in its
// own account, so a full account is a clear error rather than a provider
// 429 (release must be called after the resume).
func (m *Manager) sandboxWakeCheck(ctx context.Context, b *model.Box) (func(), error) {
	nop := func() {}
	h, err := m.Store.GetHost(ctx, b.HostID)
	if err != nil || h == nil || h.Billing != "sandbox" || h.AccountID == "" {
		return nop, nil
	}
	a, err := m.Store.GetProviderAccount(ctx, h.AccountID)
	if err != nil {
		return nop, nil
	}
	normAccount(a)
	rel, why, _ := m.reserveSandbox(ctx, a, "wake:"+h.ID, slotResume)
	if why != "" {
		return nop, model.EH(model.ErrProviderFailed, "A paused sandbox resumes only in its own account: sleep or stop another box on that account, raise its concurrent_sandboxes limit, or clone this box onto another account (POST /boxes/{id}/clone).",
			"%s account %s cannot resume this box: %s", h.Provider, a.Label, why)
	}
	return rel, nil
}

// reserveFork checks that the source account of a native fork can take one
// more sandbox (a fork lives in the source's account). why != "" means:
// use snapshot → new sandbox on an eligible account instead.
func (m *Manager) reserveFork(ctx context.Context, srcHost *model.Host, boxID string) (string, func()) {
	nop := func() {}
	if srcHost.AccountID == "" {
		return "", nop
	}
	a, err := m.Store.GetProviderAccount(ctx, srcHost.AccountID)
	if err != nil {
		return "", nop
	}
	normAccount(a)
	now := time.Now().UTC()
	st := m.computeStats(ctx, a, nil, nil, now)
	if why := m.ineligible(a, st, "", srcHost.PriceHour, now); why != "" {
		return a.Label + ": " + why, nop
	}
	rel, why, _ := m.reserveSandbox(ctx, a, "fork:"+boxID, slotCreate)
	if why != "" {
		return a.Label + ": " + why, nop
	}
	return "", rel
}

// refreshShimCreds hands a rotated key to the live T3 shims of an account
// (and, for the default account, to legacy hosts without an account).
func (m *Manager) refreshShimCreds(ctx context.Context, prov, accountID string) int {
	hosts, _ := m.Store.ListHosts(ctx)
	n := 0
	for _, h := range hosts {
		if h.Provider != prov || h.Driver != "shim" || h.Status == "deleted" {
			continue
		}
		if h.AccountID != accountID && !(h.AccountID == "" && accountID == model.DefaultAccountID(prov)) {
			continue
		}
		sh, ok := m.Hub.VirtualHost(h.ID).(*sandbox.Shim)
		if !ok {
			continue
		}
		tok, _, err := m.hostToken(ctx, h)
		if err != nil {
			continue
		}
		sh.SetCred(sandbox.ParseCred(tok))
		n++
	}
	return n
}

// unionSandboxOffers adds the sizes (templates) of the provider's other
// accounts to the catalog: the primary key's templates are not the only
// ones Boxmes can place on.
func (m *Manager) unionSandboxOffers(ctx context.Context, prov provider.Provider, primary string, offers []model.Offer) []model.Offer {
	sp, ok := prov.(sandbox.Provider)
	if !ok {
		return offers
	}
	if _, ok := sp.Driver().(sandbox.Sizer); !ok {
		return offers
	}
	seen := map[string]bool{}
	for _, o := range offers {
		seen[o.SKU+"|"+o.Region] = true
	}
	for _, a := range m.listAccounts(ctx, prov.ID()) {
		if !a.Enabled || a.Status == model.AccountArchived {
			continue
		}
		tok, err := m.credentialFor(ctx, a)
		if err != nil || tok == primary {
			continue
		}
		more, err := prov.Offers(ctx, tok)
		if err != nil {
			continue
		}
		for _, o := range more {
			if k := o.SKU + "|" + o.Region; !seen[k] {
				seen[k] = true
				offers = append(offers, o)
			}
		}
	}
	return offers
}

// ---- admin API ----

// SandboxAccountRow is one account of GET /v1/admin/providers/{p}/sandbox.
type SandboxAccountRow struct {
	ID                string            `json:"id"`
	Label             string            `json:"label"`
	Status            string            `json:"status"`
	Eligible          bool              `json:"eligible"`
	Ineligible        string            `json:"ineligible,omitempty"`
	LimitGroup        string            `json:"limit_group,omitempty"`
	BillingGroup      string            `json:"billing_group,omitempty"`
	Running           int               `json:"running"`       // this account
	GroupRunning      int               `json:"group_running"` // limit group (= running without a group)
	InFlight          int               `json:"in_flight"`
	ConcurrentLimit   float64           `json:"concurrent_limit,omitempty"` // tightest over the limit group (0 = unknown)
	ConcurrentSource  string            `json:"concurrent_source,omitempty"`
	ConcurrencyPct    float64           `json:"concurrency_pct,omitempty"`
	CreatePerSec      float64           `json:"create_per_sec,omitempty"`
	CreatePerMin      float64           `json:"create_per_min,omitempty"`
	CreatesLastSec    int               `json:"creates_last_sec"`
	CreatesLastMin    int               `json:"creates_last_min"`
	CooldownUntil     *time.Time        `json:"cooldown_until,omitempty"`
	Templates         map[string]string `json:"templates"` // template → available | missing | not_listed | unknown
	TemplatesSyncedAt *time.Time        `json:"templates_synced_at,omitempty"`
	Scopes            []string          `json:"scopes"`
	ScopeProblems     []string          `json:"scope_problems,omitempty"`
	BalanceEffective  *float64          `json:"balance_effective,omitempty"`
	CreditExpiresAt   *time.Time        `json:"credit_expires_at,omitempty"`
}

// SandboxAccountsView is GET /v1/admin/providers/{p}/sandbox: the Sandbox
// API tab (accounts, concurrency gauges, template availability matrix).
type SandboxAccountsView struct {
	Provider  string              `json:"provider"`
	Strategy  string              `json:"strategy"`
	Templates []string            `json:"templates"` // every template known on any account (+ the provider's built-ins)
	Accounts  []SandboxAccountRow `json:"accounts"`
}

func (m *Manager) sandboxProvAdmin(p *Principal, prov string) (sandbox.Provider, error) {
	if err := p.Require(model.ScopeAdmin); err != nil {
		return nil, err
	}
	sp, ok := m.sandboxProv(prov)
	if !ok {
		return nil, model.EH(model.ErrInvalid, "Use a sandbox provider id (GET /v1/sandbox/providers).", "provider %s is not a sandbox provider", prov)
	}
	return sp, nil
}

// SandboxAccounts is GET /v1/admin/providers/{p}/sandbox.
func (m *Manager) SandboxAccounts(ctx context.Context, p *Principal, prov string) (*SandboxAccountsView, error) {
	sp, err := m.sandboxProvAdmin(p, prov)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	hosts, _ := m.Store.ListHosts(ctx)
	boxes, _ := m.Store.ListBoxes(ctx, store.BoxFilter{})
	out := &SandboxAccountsView{Provider: prov, Strategy: m.effectiveStrategy(ctx, prov), Templates: []string{}, Accounts: []SandboxAccountRow{}}
	all := map[string]bool{}
	for _, z := range sp.Info().Sizes {
		all[z.SKU] = true
	}
	accts := m.listAccounts(ctx, prov)
	toks := map[string]string{}
	for _, a := range accts {
		if a.Status == model.AccountArchived {
			continue
		}
		tok, _ := m.credentialFor(ctx, a)
		toks[a.ID] = tok
		if _, ok := sp.Driver().(sandbox.Sizer); ok {
			for t := range accountTemplateSet(sp, a, tok) {
				all[t] = true
			}
		}
		for _, t := range a.Templates {
			all[t] = true
		}
		for _, t := range a.TemplatesMissing {
			all[t] = true
		}
	}
	for t := range all {
		out.Templates = append(out.Templates, t)
	}
	sort.Strings(out.Templates)
	for _, a := range accts {
		if a.Status == model.AccountArchived {
			continue
		}
		st := m.computeStats(ctx, a, hosts, boxes, now)
		row := SandboxAccountRow{ID: a.ID, Label: a.Label, Status: a.Status, Eligible: st.Eligible, Ineligible: st.Ineligible, LimitGroup: a.LimitGroup, BillingGroup: a.BillingGroup,
			Running: st.SandboxesRunning, GroupRunning: st.GroupRunning, InFlight: st.InFlight, CreatesLastSec: st.CreatesLastSec, CreatesLastMin: st.CreatesLastMin,
			CooldownUntil: a.CooldownUntil, TemplatesSyncedAt: a.TemplatesSyncedAt, Scopes: nonNilStrs(a.Scopes), Templates: map[string]string{},
			BalanceEffective: st.BalanceEffective, CreditExpiresAt: a.CreditExpiresAt}
		if why, _ := m.sandboxIneligible(ctx, sp, a, ""); why != "" && row.Ineligible == "" {
			row.Eligible, row.Ineligible = false, why
		}
		members := m.limitMembers(ctx, a)
		row.ConcurrentLimit, row.ConcurrentSource = groupLimit(members, "concurrent_sandboxes")
		if row.ConcurrentLimit > 0 {
			row.ConcurrencyPct = r4(float64(st.GroupRunning+st.InFlight) / row.ConcurrentLimit * 100)
		}
		row.CreatePerSec, _ = groupLimit(members, "create_per_sec")
		row.CreatePerMin, _ = groupLimit(members, "create_per_min")
		row.ScopeProblems = append(missingScopes(a, sandboxScopes[prov]), missingScopes(a, sandboxScopesWarn[prov])...)
		for _, t := range out.Templates {
			row.Templates[t] = templateState(sp, a, toks[a.ID], t)
		}
		out.Accounts = append(out.Accounts, row)
	}
	return out, nil
}

// TemplateSyncResult is POST …/accounts/{a}/templates/sync.
type TemplateSyncResult struct {
	Templates []string     `json:"templates"`
	Added     []string     `json:"added"`
	Removed   []string     `json:"removed"`
	Account   *AccountView `json:"account"`
}

// SyncAccountTemplates reads the account's templates (snapshots) from the
// provider API with the account's own key and stores them.
func (m *Manager) SyncAccountTemplates(ctx context.Context, p *Principal, prov, id string) (*TemplateSyncResult, error) {
	sp, err := m.sandboxProvAdmin(p, prov)
	if err != nil {
		return nil, err
	}
	tl, ok := sp.Driver().(sandbox.TemplateLister)
	if !ok {
		return nil, model.EH(model.ErrInvalid, "Set the account's templates by hand: PATCH /v1/admin/providers/{p}/accounts/{a} with {\"templates\": [...]}.", "provider %s cannot list templates", prov)
	}
	a, err := m.getAccount(ctx, prov, id)
	if err != nil {
		return nil, err
	}
	tok, err := m.credentialFor(ctx, a)
	if err != nil {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	list, err := tl.ListTemplates(cctx, sandbox.ParseCred(tok))
	cancel()
	if err != nil {
		return nil, err
	}
	l := m.acctLock(id)
	l.Lock()
	cur, err := m.getAccount(ctx, prov, id)
	if err != nil {
		l.Unlock()
		return nil, err
	}
	old := map[string]bool{}
	for _, t := range cur.Templates {
		old[t] = true
	}
	now := map[string]bool{}
	res := &TemplateSyncResult{Templates: nonNilStrs(list), Added: []string{}, Removed: []string{}}
	for _, t := range list {
		now[t] = true
		if !old[t] {
			res.Added = append(res.Added, t)
		}
	}
	for t := range old {
		if !now[t] {
			res.Removed = append(res.Removed, t)
		}
	}
	sort.Strings(res.Removed)
	var missing []string
	for _, t := range cur.TemplatesMissing {
		if !now[t] {
			missing = append(missing, t)
		}
	}
	at := time.Now().UTC()
	cur.Templates, cur.TemplatesMissing, cur.TemplatesSyncedAt, cur.UpdatedBy = nonNilStrs(list), nonNilStrs(missing), &at, p.Actor()
	err = m.putAccount(ctx, cur)
	l.Unlock()
	if err != nil {
		return nil, err
	}
	m.EmitData(ctx, p.Actor(), "", "", "provider_account.templates_synced", fmt.Sprintf("%s %s: %d templates (+%d −%d)", prov, cur.Label, len(list), len(res.Added), len(res.Removed)), nil,
		map[string]any{"provider": prov, "account_id": id, "templates": len(list), "added": res.Added, "removed": res.Removed})
	res.Account = m.viewAccount(ctx, cur, nil)
	return res, nil
}

// sandboxAccountWarnings: write warnings of a sandbox account (scopes that
// leave sandboxes behind, keys of one project that add no capacity).
func (m *Manager) sandboxAccountWarnings(ctx context.Context, a *model.ProviderAccount) []string {
	if _, ok := m.sandboxProv(a.Provider); !ok {
		return nil
	}
	var w []string
	if miss := missingScopes(a, sandboxScopes[a.Provider]); len(miss) > 0 {
		w = append(w, "key lacks "+strings.Join(miss, ", ")+": Boxmes cannot create sandboxes with it")
	}
	if miss := missingScopes(a, sandboxScopesWarn[a.Provider]); len(miss) > 0 {
		w = append(w, "key lacks "+strings.Join(miss, ", ")+": Boxmes could not delete its sandboxes (they would keep billing)")
	}
	if a.ExternalID != "" && a.LimitGroup == "" {
		for _, x := range m.listAccounts(ctx, a.Provider) {
			if x.ID != a.ID && x.ExternalID == a.ExternalID && x.Status != model.AccountArchived {
				w = append(w, "same external_id as "+x.Label+": keys of one project share its concurrency and rate limits; set the same limit_group on both")
				break
			}
		}
	}
	return w
}
