package manager

// v2.2 (M29 phase A) provider accounts: several credentials per provider,
// account selection (eligibility filter + strategy + sticky), failover by
// error class, account-level breaker/cooldown, ledger spend, manual
// balance/limits/expiry, runway and automatic drain. Every action on an
// existing host uses the account that created it (hostToken).
//
// Storage: accounts live in provider_accounts. The legacy single token of a
// provider (providers.doc token_enc, PUT /providers/{id}/token, unlabeled
// env vars) is the provider's default account pa_<provider>_default whose
// credential_ref is provider:<id>; other accounts keep their credential
// AES-GCM sealed (master key) in their own row. Credentials never leave
// core: API responses and events carry key_fingerprint only.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"math/rand"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"boxmes/core/internal/model"
	"boxmes/core/internal/provhealth"
	"boxmes/core/internal/provider"
	"boxmes/core/internal/sandbox"
	"boxmes/core/internal/store"
)

const (
	maxAccountsPerCreate = 3 // spec §4.3: at most 3 accounts / provider per create request
	maxAttemptsPerCreate = 6
)

type stickyPick struct {
	account string
	until   time.Time
}

// acctState is the in-memory side of provider accounts.
type acctState struct {
	mu      sync.Mutex
	cache   map[string][]*model.ProviderAccount // provider → accounts (stored form)
	stats   map[string]*model.AccountStats      // last computed stats per account
	rr      map[string]int                      // round-robin cursor per provider
	sticky  map[string]stickyPick               // provider|workspace → account
	locks   sync.Map                            // account id → *sync.Mutex
	blocked map[string]time.Time                // capacity blocklist provider|region → until
	alertMu sync.Mutex
}

func (s *acctState) init() {
	if s.cache == nil {
		s.cache, s.stats, s.rr, s.sticky, s.blocked = map[string][]*model.ProviderAccount{}, map[string]*model.AccountStats{}, map[string]int{}, map[string]stickyPick{}, map[string]time.Time{}
	}
}

func (m *Manager) acctLock(id string) *sync.Mutex {
	l, _ := m.accts.locks.LoadOrStore(id, &sync.Mutex{})
	return l.(*sync.Mutex)
}

func (m *Manager) invalidateAccounts(provider string) {
	m.accts.mu.Lock()
	m.accts.init()
	delete(m.accts.cache, provider)
	m.accts.mu.Unlock()
}

func acctKey(a *model.ProviderAccount) string { return "acct:" + a.ID }

func cloneAccount(a *model.ProviderAccount) *model.ProviderAccount {
	c := *a
	c.Tags = append([]string{}, a.Tags...)
	c.Regions = append([]string{}, a.Regions...)
	c.Scopes = append([]string{}, a.Scopes...)
	c.Limits = map[string]model.AccountLimit{}
	for k, v := range a.Limits {
		c.Limits[k] = v
	}
	c.Stats = nil
	return &c
}

func normAccount(a *model.ProviderAccount) {
	if a.Tags == nil {
		a.Tags = []string{}
	}
	if a.Regions == nil {
		a.Regions = []string{}
	}
	if a.Scopes == nil {
		a.Scopes = []string{}
	}
	if a.Limits == nil {
		a.Limits = map[string]model.AccountLimit{}
	}
	if a.Currency == "" {
		a.Currency = "USD"
	}
	if a.Weight <= 0 {
		a.Weight = 1
	}
	if a.Priority <= 0 {
		a.Priority = 1
	}
	if a.Status == "" {
		a.Status = model.AccountActive
	}
}

// Fingerprint identifies a credential without revealing it: sha256 prefix
// plus the last 4 characters.
func Fingerprint(tok string) string {
	if tok == "" {
		return ""
	}
	h := sha256.Sum256([]byte(tok))
	tail := tok
	if len(tail) > 4 {
		tail = tail[len(tail)-4:]
	}
	if len(tok) < 12 {
		tail = ""
	}
	return hex.EncodeToString(h[:4]) + "…" + tail
}

// putAccount persists an account and refreshes the cache.
func (m *Manager) putAccount(ctx context.Context, a *model.ProviderAccount) error {
	normAccount(a)
	a.UpdatedAt = time.Now().UTC()
	if err := m.Store.PutProviderAccount(ctx, a); err != nil {
		return err
	}
	m.invalidateAccounts(a.Provider)
	return nil
}

// listAccounts returns a provider's accounts (copies, archived included).
// A legacy provider token (providers.doc token_enc) becomes the default
// account the first time it is seen.
func (m *Manager) listAccounts(ctx context.Context, prov string) []*model.ProviderAccount {
	m.accts.mu.Lock()
	m.accts.init()
	cached, ok := m.accts.cache[prov]
	m.accts.mu.Unlock()
	if !ok {
		list, err := m.Store.ListProviderAccounts(ctx, prov)
		if err != nil {
			return nil
		}
		for _, a := range list {
			normAccount(a)
		}
		m.accts.mu.Lock()
		m.accts.cache[prov] = list
		m.accts.mu.Unlock()
		cached = list
	}
	hasDefault := false
	for _, a := range cached {
		if a.IsDefault {
			hasDefault = true
		}
	}
	if !hasDefault {
		if mp, err := m.Store.GetProvider(ctx, prov); err == nil && mp.TokenEnc != "" {
			if a := m.ensureDefaultAccount(ctx, mp); a != nil {
				return m.listAccounts(ctx, prov)
			}
		}
	}
	out := make([]*model.ProviderAccount, 0, len(cached))
	for _, a := range cached {
		out = append(out, cloneAccount(a))
	}
	return out
}

// ensureDefaultAccount creates pa_<provider>_default for a legacy token.
func (m *Manager) ensureDefaultAccount(ctx context.Context, mp *model.Provider) *model.ProviderAccount {
	id := model.DefaultAccountID(mp.ID)
	if a, err := m.Store.GetProviderAccount(ctx, id); err == nil {
		normAccount(a)
		return a
	}
	src := mp.TokenSource
	if src == "" {
		src = "api"
	}
	now := time.Now().UTC()
	a := &model.ProviderAccount{ID: id, Provider: mp.ID, Label: "Mặc định", IsDefault: true, CredentialRef: "provider:" + mp.ID, CredentialSource: src,
		Enabled: true, Priority: 1, Weight: 1, Status: model.AccountActive, CreatedAt: now, CreatedBy: "migration"}
	if tok, err := m.Crypt.Open(mp.TokenEnc); err == nil {
		a.KeyFingerprint = Fingerprint(tok)
	}
	if err := m.putAccount(ctx, a); err != nil {
		return nil
	}
	m.EmitData(ctx, "system", "", "", "provider_account.created", mp.ID+": default account from the provider token", nil,
		map[string]any{"provider": mp.ID, "account_id": id, "key_fingerprint": a.KeyFingerprint, "credential_source": src})
	return a
}

// migrateAccounts (startup) gives every legacy token its default account,
// every live host an account and stamps this month's ledger rows.
func (m *Manager) migrateAccounts(ctx context.Context) {
	defaults := map[string]string{}
	for _, p := range m.Providers.All() {
		for _, a := range m.listAccounts(ctx, p.ID()) {
			if a.IsDefault {
				defaults[p.ID()] = a.ID
			}
		}
	}
	hosts, _ := m.Store.ListHosts(ctx)
	byHost := map[string]string{}
	for _, h := range hosts {
		if h.Provider == "local" {
			continue
		}
		if h.AccountID == "" && h.Status != "deleted" && defaults[h.Provider] != "" {
			h.AccountID = defaults[h.Provider]
			m.Store.PutHost(ctx, h)
		}
		if h.AccountID != "" {
			byHost[h.ID] = h.AccountID
		}
	}
	var done bool
	if m.Store.GetKV(ctx, "accounts_ledger_backfill", &done) == nil && done {
		return
	}
	now := time.Now().UTC()
	from := store.FmtTime(time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC))
	boxes, _ := m.Store.ListBoxes(ctx, store.BoxFilter{IncludeTerminated: true})
	for _, b := range boxes {
		if acc := byHost[b.HostID]; acc != "" {
			m.Store.SetLedgerAccount(ctx, b.ID, acc, from)
		}
	}
	m.Store.PutKV(ctx, "accounts_ledger_backfill", true)
}

// credentialFor opens an account's credential ("" + error when missing).
func (m *Manager) credentialFor(ctx context.Context, a *model.ProviderAccount) (string, error) {
	if a == nil {
		return "", model.E(model.ErrProviderFailed, "provider account not found")
	}
	enc := a.CredentialEnc
	if strings.HasPrefix(a.CredentialRef, "provider:") {
		mp, err := m.Store.GetProvider(ctx, a.Provider)
		if err == nil {
			enc = mp.TokenEnc
		}
	}
	if enc == "" {
		return "", model.EH(model.ErrProviderFailed, "Rotate the account key in Infra → Accounts (POST /v1/admin/providers/{p}/accounts/{a}/rotate).", "provider account %s has no credential", a.ID)
	}
	return m.Crypt.Open(enc)
}

func (m *Manager) hasCredential(ctx context.Context, a *model.ProviderAccount) bool {
	if strings.HasPrefix(a.CredentialRef, "provider:") {
		mp, err := m.Store.GetProvider(ctx, a.Provider)
		return err == nil && mp.TokenEnc != ""
	}
	return a.CredentialEnc != ""
}

// hostToken is the credential of the account that created h (pre-v2.2
// hosts without an account use the provider's primary credential).
func (m *Manager) hostToken(ctx context.Context, h *model.Host) (string, *model.ProviderAccount, error) {
	if h.AccountID == "" {
		tok, err := m.providerToken(ctx, h.Provider)
		return tok, nil, err
	}
	a, err := m.Store.GetProviderAccount(ctx, h.AccountID)
	if err != nil {
		return "", nil, model.EH(model.ErrProviderFailed, "Recreate the provider account or delete the server by hand in the provider console.", "provider account %s of host %s not found", h.AccountID, h.ID)
	}
	normAccount(a)
	tok, err := m.credentialFor(ctx, a)
	return tok, a, err
}

// providerToken is the provider's primary credential (legacy callers:
// regions, servers, offers, sandbox discovery). Primary = the usable account
// with the best priority; falls back to the legacy token.
func (m *Manager) providerToken(ctx context.Context, id string) (string, error) {
	accts := m.listAccounts(ctx, id)
	sort.SliceStable(accts, func(i, j int) bool { return accts[i].Priority < accts[j].Priority })
	for _, pass := range []int{0, 1} {
		for _, a := range accts {
			if pass == 0 && (!a.Enabled || a.Status == model.AccountAuthFailed || a.Status == model.AccountArchived) {
				continue
			}
			if a.Status == model.AccountArchived {
				continue
			}
			if tok, err := m.credentialFor(ctx, a); err == nil && tok != "" {
				return tok, nil
			}
		}
	}
	p, err := m.Store.GetProvider(ctx, id)
	if err != nil || p.TokenEnc == "" {
		return "", model.EH(model.ErrProviderFailed, "Connect the provider first: PUT /providers/"+id+"/token.", "provider %s not connected", id)
	}
	return m.Crypt.Open(p.TokenEnc)
}

// provConnected: the provider is connected and has a credential (legacy
// token or any non-archived account with one).
func (m *Manager) provConnected(ctx context.Context, mp *model.Provider) bool {
	if mp == nil || !mp.Connected {
		return false
	}
	if mp.TokenEnc != "" {
		return true
	}
	for _, a := range m.listAccounts(ctx, mp.ID) {
		if a.Enabled && a.Status != model.AccountArchived && m.hasCredential(ctx, a) {
			return true
		}
	}
	return false
}

// ---- money & stats ----

// fxDefaults: USD per unit (BOXMES_FX_RATES="EUR=1.08,GBP=1.27" overrides).
var fxDefaults = map[string]float64{"USD": 1, "EUR": 1.08, "GBP": 1.27}

func fxRate(a *model.ProviderAccount) float64 {
	if a.FXRateUSD > 0 {
		return a.FXRateUSD
	}
	cur := strings.ToUpper(a.Currency)
	if env := os.Getenv("BOXMES_FX_RATES"); env != "" {
		for _, kv := range strings.Split(env, ",") {
			k, v, ok := strings.Cut(strings.TrimSpace(kv), "=")
			if ok && strings.EqualFold(k, cur) {
				if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
					return f
				}
			}
		}
	}
	if f, ok := fxDefaults[cur]; ok {
		return f
	}
	return 1
}

type thresholds struct {
	runwayWarn, runwayCrit, limitWarn, limitCrit, expWarn, expCrit float64
}

// accountThresholds: owner decisions of 2026-11-27 (7/2 days runway, 80/95 %
// limits & spend caps, 14/3 days token/credit/contract expiry), overridable
// per account.
func accountThresholds(a *model.ProviderAccount) thresholds {
	t := thresholds{7, 2, 80, 95, 14, 3}
	o := a.Thresholds
	if o.RunwayWarnDays > 0 {
		t.runwayWarn = o.RunwayWarnDays
	}
	if o.RunwayCriticalDays > 0 {
		t.runwayCrit = o.RunwayCriticalDays
	}
	if o.LimitWarnPct > 0 {
		t.limitWarn = o.LimitWarnPct
	}
	if o.LimitCriticalPct > 0 {
		t.limitCrit = o.LimitCriticalPct
	}
	if o.ExpiryWarnDays > 0 {
		t.expWarn = o.ExpiryWarnDays
	}
	if o.ExpiryCriticalDays > 0 {
		t.expCrit = o.ExpiryCriticalDays
	}
	return t
}

func liveHost(h *model.Host) bool { return h.Status != "deleted" && h.Status != "down" }

// hostAccount is the account id a host is billed to ("" for local).
func hostAccount(h *model.Host) string { return h.AccountID }

func r4(v float64) float64 { return math.Round(v*1e4) / 1e4 }

// computeStats fills the figures of one account. hosts/boxes may be nil
// (loaded here).
func (m *Manager) computeStats(ctx context.Context, a *model.ProviderAccount, hosts []*model.Host, boxes []*model.Box, now time.Time) *model.AccountStats {
	st := &model.AccountStats{HasCredential: m.hasCredential(ctx, a), Breaker: provhealth.Closed}
	if hosts == nil {
		hosts, _ = m.Store.ListHosts(ctx)
	}
	priceHour := 0.0
	onAcct := map[string]bool{}
	for _, h := range hosts {
		if h.Provider != a.Provider || !liveHost(h) || hostAccount(h) != a.ID {
			continue
		}
		onAcct[h.ID] = true
		st.HostsLive++
		st.VCPULive += h.CPU
		priceHour += h.PriceHour
	}
	if boxes == nil && st.HostsLive > 0 {
		boxes, _ = m.Store.ListBoxes(ctx, store.BoxFilter{})
	}
	for _, b := range boxes {
		if onAcct[b.HostID] && b.Active() {
			st.BoxesLive++
		}
	}
	fx := fxRate(a)
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	st.SpendTodayUSD, _ = m.Store.SumLedger(ctx, store.LedgerFilter{AccountID: a.ID, From: day})
	st.SpendMonthUSD, _ = m.Store.SumLedger(ctx, store.LedgerFilter{AccountID: a.ID, From: month})
	last24, _ := m.Store.SumLedger(ctx, store.LedgerFilter{AccountID: a.ID, From: now.Add(-24 * time.Hour)})
	window := 24.0
	if age := now.Sub(a.CreatedAt).Hours(); age > 0 && age < window {
		window = math.Max(age, 1)
	}
	st.BurnPerHourUSD = r4(math.Max(last24/window, priceHour))
	st.SpendTodayUSD, st.SpendMonthUSD = r4(st.SpendTodayUSD), r4(st.SpendMonthUSD)
	st.SpendToday, st.SpendMonth = r4(st.SpendTodayUSD/fx), r4(st.SpendMonthUSD/fx)
	if a.Balance != nil {
		since := time.Time{}
		if a.BalanceAt != nil {
			since = *a.BalanceAt
		}
		spent, _ := m.Store.SumLedger(ctx, store.LedgerFilter{AccountID: a.ID, From: since})
		eff := r4(*a.Balance - spent/fx)
		usd := r4(eff * fx)
		st.BalanceEffective, st.BalanceEffectiveUSD = &eff, &usd
		if st.BurnPerHourUSD > 0 {
			h := math.Max(0, (usd-a.MinBalance*fx)/st.BurnPerHourUSD)
			h = math.Round(h*10) / 10
			d := math.Round(h/24*100) / 100
			st.RunwayHours, st.RunwayDays = &h, &d
		}
	}
	next := month.AddDate(0, 1, 0)
	st.ForecastMonthEnd = r4(st.SpendMonth + st.BurnPerHourUSD*next.Sub(now).Hours()/fx)
	if ok, _ := m.Health.Peek(acctKey(a)); !ok {
		st.Breaker = provhealth.Open
	} else if s := m.Health.Stats(acctKey(a), ""); s.Breaker.State != "" {
		st.Breaker = s.Breaker.State
	}
	why := m.ineligible(a, st, "", 0, now)
	st.Eligible, st.Ineligible = why == "", why
	m.accts.mu.Lock()
	m.accts.init()
	m.accts.stats[a.ID] = st
	m.accts.mu.Unlock()
	return st
}

// ineligible explains why the scheduler must not use the account for one
// more host of priceHour (USD) in region ("" = usable). Spec §4.1.
func (m *Manager) ineligible(a *model.ProviderAccount, st *model.AccountStats, region string, priceHour float64, now time.Time) string {
	switch {
	case a.Status == model.AccountArchived:
		return "archived"
	case !a.Enabled:
		return "disabled"
	case a.Status == model.AccountAuthFailed:
		return "auth failed: " + a.StatusReason
	case a.Status == model.AccountOutOfCredit:
		return "out of credit"
	case a.Drain:
		if a.AutoDrained {
			return "draining (runway below the critical threshold)"
		}
		return "draining"
	case st != nil && !st.HasCredential:
		return "no credential"
	case a.CooldownUntil != nil && now.Before(*a.CooldownUntil):
		return fmt.Sprintf("%s until %s", orStr(a.Status, "cooling"), a.CooldownUntil.UTC().Format(time.RFC3339))
	}
	if ok, why := m.Health.Peek(acctKey(a)); !ok {
		return "account " + why
	}
	if region != "" && !allowed(a.Regions, region) {
		return "region " + region + " not allowed"
	}
	used := 0.0
	if st != nil {
		used = float64(st.HostsLive)
	}
	for _, k := range []string{"servers", "concurrent_sandboxes"} {
		if l, ok := a.Limits[k]; ok && l.Limit > 0 && used+1 > l.Limit {
			return fmt.Sprintf("%s limit %.0f reached", k, l.Limit)
		}
	}
	if st == nil {
		return ""
	}
	fx := fxRate(a)
	if st.BalanceEffective != nil {
		if *st.BalanceEffective-priceHour*24/fx < a.MinBalance {
			return fmt.Sprintf("balance %.2f %s below the minimum %.2f", *st.BalanceEffective, a.Currency, a.MinBalance)
		}
		if st.RunwayDays != nil && *st.RunwayDays < accountThresholds(a).runwayCrit {
			return fmt.Sprintf("runway %.1f days below %.0f days", *st.RunwayDays, accountThresholds(a).runwayCrit)
		}
	}
	if a.SpendCapDay > 0 && st.SpendToday+priceHour/fx > a.SpendCapDay+1e-9 {
		return fmt.Sprintf("daily spend cap %.2f %s reached", a.SpendCapDay, a.Currency)
	}
	if a.SpendCapMonth > 0 && st.SpendMonth+priceHour/fx > a.SpendCapMonth+1e-9 {
		return fmt.Sprintf("monthly spend cap %.2f %s reached", a.SpendCapMonth, a.Currency)
	}
	if a.SpendCapMonth > 0 && a.BillingModel == "postpaid" && st.ForecastMonthEnd > a.SpendCapMonth+1e-9 {
		return fmt.Sprintf("month-end forecast %.2f over the monthly cap %.2f", st.ForecastMonthEnd, a.SpendCapMonth)
	}
	return ""
}

func orStr(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// ---- selection ----

func (m *Manager) effectiveStrategy(ctx context.Context, prov string) string {
	pp := m.ProviderPrefs(ctx, prov)
	if pp.Strategy != "" {
		return pp.Strategy
	}
	if _, ok := m.sandboxProv(prov); ok {
		return model.StrategyDrainFirst
	}
	return model.StrategyPriority
}

func failoverAcross(pp model.ProviderPrefs) bool {
	return pp.FailoverAcrossAccounts == nil || *pp.FailoverAcrossAccounts
}

// pickAccounts returns the eligible accounts of a provider in the order the
// strategy prefers (prefer = account to try first when eligible, e.g. the
// source of a clone) plus the reasons the others were skipped.
func (m *Manager) pickAccounts(ctx context.Context, prov, region string, priceHour float64, prefer string) ([]*model.ProviderAccount, []string) {
	now := time.Now().UTC()
	all := m.listAccounts(ctx, prov)
	hosts, _ := m.Store.ListHosts(ctx)
	var ok []*model.ProviderAccount
	var reasons []string
	for _, a := range all {
		if a.Status == model.AccountArchived {
			continue
		}
		st := m.computeStats(ctx, a, hosts, nil, now)
		m.applyAuto(ctx, a, st, now)
		if why := m.ineligible(a, st, region, priceHour, now); why != "" {
			reasons = append(reasons, a.Label+" ("+a.ID+"): "+why)
			continue
		}
		a.Stats = st
		ok = append(ok, a)
	}
	ok = m.rankAccounts(ctx, prov, ok)
	ws := ""
	if p := principalFrom(ctx); p != nil {
		ws = p.WorkspaceID
	}
	stick := ""
	m.accts.mu.Lock()
	if sp, found := m.accts.sticky[prov+"|"+ws]; found && now.Before(sp.until) {
		stick = sp.account
	}
	m.accts.mu.Unlock()
	for _, want := range []string{stick, prefer} { // prefer wins (applied last)
		if want == "" {
			continue
		}
		for i, a := range ok {
			if a.ID == want && i > 0 {
				ok = append([]*model.ProviderAccount{a}, append(ok[:i:i], ok[i+1:]...)...)
				break
			}
		}
	}
	return ok, reasons
}

func usageRatio(a *model.ProviderAccount) float64 {
	r := -1.0
	used := 0.0
	if a.Stats != nil {
		used = float64(a.Stats.HostsLive)
	}
	for k, l := range a.Limits {
		if l.Limit <= 0 {
			continue
		}
		u := l.Used
		if k == "servers" || k == "concurrent_sandboxes" {
			u = used
		} else if k == "vcpu" && a.Stats != nil {
			u = a.Stats.VCPULive
		}
		r = math.Max(r, u/l.Limit)
	}
	if r < 0 {
		return used / 1000 // no limits known: fewer hosts first
	}
	return r
}

func (m *Manager) rankAccounts(ctx context.Context, prov string, l []*model.ProviderAccount) []*model.ProviderAccount {
	if len(l) < 2 {
		return l
	}
	byPrio := func(i, j int) bool {
		if l[i].Priority != l[j].Priority {
			return l[i].Priority < l[j].Priority
		}
		hi, hj := 0, 0
		if l[i].Stats != nil {
			hi = l[i].Stats.HostsLive
		}
		if l[j].Stats != nil {
			hj = l[j].Stats.HostsLive
		}
		if hi != hj {
			return hi < hj
		}
		return l[i].ID < l[j].ID
	}
	switch m.effectiveStrategy(ctx, prov) {
	case model.StrategyWeighted: // weighted random order (LiteLLM simple-shuffle)
		rest := append([]*model.ProviderAccount{}, l...)
		var out []*model.ProviderAccount
		for len(rest) > 0 {
			tot := 0
			for _, a := range rest {
				tot += a.Weight
			}
			x := rand.Intn(tot)
			for i, a := range rest {
				if x < a.Weight {
					out = append(out, a)
					rest = append(rest[:i], rest[i+1:]...)
					break
				}
				x -= a.Weight
			}
		}
		return out
	case model.StrategyRoundRobin:
		sort.SliceStable(l, func(i, j int) bool { return l[i].ID < l[j].ID })
		m.accts.mu.Lock()
		n := m.accts.rr[prov]
		m.accts.rr[prov] = n + 1
		m.accts.mu.Unlock()
		k := n % len(l)
		return append(append([]*model.ProviderAccount{}, l[k:]...), l[:k]...)
	case model.StrategyLeastUsed:
		sort.SliceStable(l, func(i, j int) bool {
			ri, rj := usageRatio(l[i]), usageRatio(l[j])
			if ri != rj {
				return ri < rj
			}
			return byPrio(i, j)
		})
	case model.StrategyDrainFirst: // promo credit that expires first, then the emptiest wallet
		sort.SliceStable(l, func(i, j int) bool {
			ei, ej := l[i].CreditExpiresAt, l[j].CreditExpiresAt
			if (ei != nil) != (ej != nil) {
				return ei != nil
			}
			if ei != nil && !ei.Equal(*ej) {
				return ei.Before(*ej)
			}
			bi, bj := money(l[i]), money(l[j])
			if (bi != nil) != (bj != nil) {
				return bi != nil
			}
			if bi != nil && *bi != *bj {
				return *bi < *bj
			}
			return byPrio(i, j)
		})
	default:
		sort.SliceStable(l, byPrio)
	}
	return l
}

// money is what is left to drain: credit remaining, else effective balance (USD).
func money(a *model.ProviderAccount) *float64 {
	if a.CreditRemaining != nil {
		v := *a.CreditRemaining * fxRate(a)
		return &v
	}
	if a.Stats != nil && a.Stats.BalanceEffectiveUSD != nil {
		return a.Stats.BalanceEffectiveUSD
	}
	return nil
}

// accountsBlocked explains why no account of a provider can take a host now
// ("" = some account can, or the provider has no accounts at all). Cheap:
// uses the stats of the last tick / pick.
func (m *Manager) accountsBlocked(ctx context.Context, prov, region string) string {
	all := m.listAccounts(ctx, prov)
	if len(all) == 0 {
		return ""
	}
	now := time.Now().UTC()
	var why []string
	for _, a := range all {
		if a.Status == model.AccountArchived {
			continue
		}
		m.accts.mu.Lock()
		st := m.accts.stats[a.ID]
		m.accts.mu.Unlock()
		if st == nil {
			st = &model.AccountStats{HasCredential: m.hasCredential(ctx, a)}
		}
		w := m.ineligible(a, st, region, 0, now)
		if w == "" {
			return ""
		}
		why = append(why, a.Label+": "+w)
	}
	if len(why) == 0 {
		return ""
	}
	return "no eligible account: " + strings.Join(why, "; ")
}

func (m *Manager) capacityBlocked(prov, region string) bool {
	m.accts.mu.Lock()
	defer m.accts.mu.Unlock()
	m.accts.init()
	u, ok := m.accts.blocked[prov+"|"+region]
	return ok && time.Now().Before(u)
}

// ---- create with failover ----

// acrossAccounts runs fn on the provider's eligible accounts in strategy
// order, failing over to the next account on account-level errors (auth,
// payment, quota, rate limit, and transient server errors) — spec §4.3.
// Capacity and invalid-request errors stop at once (another account of the
// same provider would not help).
func (m *Manager) acrossAccounts(ctx context.Context, prov, region string, priceHour float64, prefer, what string, fn func(a *model.ProviderAccount, tok string) error) (*model.ProviderAccount, error) {
	accts, reasons := m.pickAccounts(ctx, prov, region, priceHour, prefer)
	if len(accts) == 0 {
		if len(reasons) == 0 {
			return nil, model.EH(model.ErrProviderFailed, "Connect the provider first: PUT /providers/"+prov+"/token.", "provider %s not connected", prov)
		}
		return nil, model.EH(model.ErrProviderFailed, "Check Infra → Accounts: enable an account, raise its caps or top it up; Boxmes fails over to the next provider.", "%s: no eligible account (%s)", prov, strings.Join(reasons, "; "))
	}
	pp := m.ProviderPrefs(ctx, prov)
	if !failoverAcross(pp) {
		accts = accts[:1]
	}
	if len(accts) > maxAccountsPerCreate {
		accts = accts[:maxAccountsPerCreate]
	}
	var lastErr error
	attempts := 0
	for i, a := range accts {
		if attempts >= maxAttemptsPerCreate {
			break
		}
		attempts++
		tok, err := m.credentialFor(ctx, a)
		if err != nil {
			lastErr = err
			continue
		}
		err = fn(a, tok)
		if err == nil {
			ws := ""
			if p := principalFrom(ctx); p != nil {
				ws = p.WorkspaceID
			}
			if mins := pp.StickyMinutes; mins > 0 {
				m.accts.mu.Lock()
				m.accts.sticky[prov+"|"+ws] = stickyPick{a.ID, time.Now().Add(time.Duration(mins) * time.Minute)}
				m.accts.mu.Unlock()
			}
			return a, nil
		}
		lastErr = err
		cls := provhealth.Classify(err)
		if cls == provhealth.ClassCapacity {
			m.accts.mu.Lock()
			m.accts.blocked[prov+"|"+region] = time.Now().Add(10 * time.Minute)
			m.accts.mu.Unlock()
			return a, err
		}
		if cls == provhealth.ClassInvalid {
			return a, err
		}
		if i+1 < len(accts) {
			next := accts[i+1]
			m.EmitData(ctx, "system", "", "", "provider_account.failover", fmt.Sprintf("%s: %s failed (%s), trying %s", prov, a.Label, cls, next.Label), nil,
				map[string]any{"provider": prov, "from": a.ID, "to": next.ID, "class": cls, "request": what})
			m.raiseAlert(ctx, alertInput{Type: "failover", Level: "info", Provider: prov, AccountID: a.ID,
				Args: []any{prov, a.Label, next.Label, cls}})
		}
	}
	return nil, lastErr
}

// accountOutcome applies the failover matrix (spec §4.3) to an account
// after a provider call (err == nil: success).
func (m *Manager) accountOutcome(ctx context.Context, a *model.ProviderAccount, err error, retryAfter time.Duration) {
	if a == nil {
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
	now := time.Now().UTC()
	old := cur.Status
	cur.LastCheckAt = &now
	if err == nil {
		cur.ErrorStreak, cur.LastError, cur.LastErrorClass = 0, "", ""
		cur.LastOKAt = &now
		if cur.Status == model.AccountAuthFailed || cur.Status == model.AccountCooling || cur.Status == model.AccountRateLimited {
			cur.Status, cur.StatusReason, cur.CooldownUntil = model.AccountActive, "", nil
		}
		m.setAccountStatus(ctx, cur, old, now)
		m.putAccount(ctx, cur)
		if old == model.AccountAuthFailed {
			m.clearAlert(ctx, "auth_failed|"+cur.ID)
		}
		return
	}
	cls := provhealth.Classify(err)
	msg := err.Error()
	if ae, ok := err.(*model.APIError); ok {
		msg = ae.Message
		if ae.RetryAfterS != nil && retryAfter == 0 {
			retryAfter = time.Duration(*ae.RetryAfterS) * time.Second
		}
	}
	if len(msg) > 300 {
		msg = msg[:300] + "…"
	}
	cur.LastError, cur.LastErrorClass = msg, cls
	cur.ErrorStreak++
	switch cls {
	case provhealth.ClassAuth:
		cur.Status, cur.StatusReason = model.AccountAuthFailed, msg
		m.raiseAlert(ctx, alertInput{Type: "auth_failed", Level: "critical", Provider: cur.Provider, AccountID: cur.ID, Args: []any{cur.Provider, cur.Label, msg}})
	case provhealth.ClassPayment:
		cur.Status, cur.StatusReason = model.AccountOutOfCredit, msg
		m.raiseAlert(ctx, alertInput{Type: "out_of_credit", Level: "critical", Provider: cur.Provider, AccountID: cur.ID, Args: []any{cur.Provider, cur.Label, msg}})
	case provhealth.ClassQuota:
		hosts, _ := m.Store.ListHosts(ctx)
		used := 0.0
		for _, h := range hosts {
			if h.Provider == cur.Provider && liveHost(h) && hostAccount(h) == cur.ID {
				used++
			}
		}
		key := "servers"
		if _, ok := m.sandboxProv(cur.Provider); ok {
			key = "concurrent_sandboxes"
		}
		// the failed host itself is already stored as provisioning: do not count it
		if used > 0 {
			used--
		}
		lim := cur.Limits[key]
		if lim.Source != "manual" || lim.Limit == 0 || used < lim.Limit {
			cur.Limits[key] = model.AccountLimit{Limit: math.Max(used, 0), Used: used, Source: "learned", At: &now}
			m.EmitData(ctx, "system", "", "", "provider_account.limit_learned", fmt.Sprintf("%s %s: %s limit learned = %.0f", cur.Provider, cur.Label, key, used), nil,
				map[string]any{"provider": cur.Provider, "account_id": cur.ID, "limit": key, "value": used})
		}
		u := now.Add(time.Hour)
		cur.Status, cur.StatusReason, cur.CooldownUntil = model.AccountQuotaFull, msg, &u
	case provhealth.ClassRateLimit:
		if retryAfter <= 0 {
			retryAfter = parseRetryAfter(msg)
		}
		if retryAfter < 10*time.Second {
			retryAfter = 10 * time.Second
		}
		if retryAfter > 15*time.Minute {
			retryAfter = 15 * time.Minute
		}
		u := now.Add(retryAfter)
		cur.Status, cur.StatusReason, cur.CooldownUntil = model.AccountRateLimited, msg, &u
	case provhealth.ClassServer, provhealth.ClassTimeout, provhealth.ClassNetwork, provhealth.ClassUnknown:
		if ok, why := m.Health.Peek(acctKey(cur)); !ok {
			if s := m.Health.Stats(acctKey(cur), ""); s.Breaker.OpenUntil != nil {
				u := *s.Breaker.OpenUntil
				cur.CooldownUntil = &u
			}
			cur.Status, cur.StatusReason = model.AccountCooling, why
		}
	}
	m.setAccountStatus(ctx, cur, old, now)
	m.putAccount(ctx, cur)
}

var retryRe = regexp.MustCompile(`(?i)retry[- _]after[":= ]+(\d+)`)

func parseRetryAfter(s string) time.Duration {
	if g := retryRe.FindStringSubmatch(s); g != nil {
		if n, err := strconv.Atoi(g[1]); err == nil {
			return time.Duration(n) * time.Second
		}
	}
	return 30 * time.Second
}

// setAccountStatus records a status change (event) when the status moved.
func (m *Manager) setAccountStatus(ctx context.Context, a *model.ProviderAccount, old string, now time.Time) {
	if a.Status == old {
		return
	}
	a.StatusSince = &now
	m.EmitData(ctx, "system", "", "", "provider_account.status_changed", fmt.Sprintf("%s %s: %s → %s (%s)", a.Provider, a.Label, old, a.Status, a.StatusReason), nil,
		map[string]any{"provider": a.Provider, "account_id": a.ID, "from": old, "to": a.Status, "reason": a.StatusReason})
}

// applyAuto is the automatic part of spec §6: runway below the critical
// threshold drains the account (never deletes hosts); recovered runway
// lifts an automatic drain; an expired cooldown returns to active.
func (m *Manager) applyAuto(ctx context.Context, a *model.ProviderAccount, st *model.AccountStats, now time.Time) {
	th := accountThresholds(a)
	change := false
	old := a.Status
	low := st.RunwayDays != nil && *st.RunwayDays < th.runwayCrit
	autodrain := os.Getenv("BOXMES_ACCOUNT_AUTODRAIN") != "0"
	switch {
	case low && !a.Drain && autodrain && a.Enabled && a.Status != model.AccountArchived:
		a.Drain, a.AutoDrained, change = true, true, true
		a.Status, a.StatusReason = model.AccountDraining, fmt.Sprintf("runway %.1f days < %.0f days", *st.RunwayDays, th.runwayCrit)
		m.EmitData(ctx, "system", "", "", "provider_account.drained", fmt.Sprintf("%s %s drained automatically: %s", a.Provider, a.Label, a.StatusReason), nil,
			map[string]any{"provider": a.Provider, "account_id": a.ID, "auto": true, "runway_days": *st.RunwayDays})
	case a.AutoDrained && !low:
		a.Drain, a.AutoDrained, change = false, false, true
		a.Status, a.StatusReason = model.AccountActive, ""
		m.EmitData(ctx, "system", "", "", "provider_account.undrained", fmt.Sprintf("%s %s: runway recovered, drain lifted", a.Provider, a.Label), nil,
			map[string]any{"provider": a.Provider, "account_id": a.ID, "auto": true})
	}
	if a.CooldownUntil != nil && !now.Before(*a.CooldownUntil) {
		a.CooldownUntil, change = nil, true
		if a.Status == model.AccountRateLimited || a.Status == model.AccountCooling || a.Status == model.AccountQuotaFull {
			a.Status, a.StatusReason = model.AccountActive, ""
		}
	}
	// soft statuses follow the figures
	if a.Status == model.AccountActive || a.Status == model.AccountLowBalance || a.Status == model.AccountExpiring || a.Status == model.AccountDraining || a.Status == model.AccountDisabled {
		ns, reason := model.AccountActive, ""
		switch {
		case !a.Enabled:
			ns, reason = model.AccountDisabled, a.StatusReason
		case a.Drain:
			ns, reason = model.AccountDraining, a.StatusReason
		case st.RunwayDays != nil && *st.RunwayDays < th.runwayWarn:
			ns, reason = model.AccountLowBalance, fmt.Sprintf("runway %.1f days", *st.RunwayDays)
		case a.TokenExpiresAt != nil && a.TokenExpiresAt.Sub(now).Hours()/24 < th.expCrit:
			ns, reason = model.AccountExpiring, "token expires "+a.TokenExpiresAt.UTC().Format("2006-01-02")
		}
		if ns != a.Status {
			a.Status, a.StatusReason, change = ns, reason, true
		}
	}
	if !change {
		return
	}
	m.setAccountStatus(ctx, a, old, now)
	l := m.acctLock(a.ID)
	l.Lock()
	defer l.Unlock()
	cur, err := m.Store.GetProviderAccount(ctx, a.ID)
	if err != nil {
		return
	}
	cur.Drain, cur.AutoDrained, cur.CooldownUntil, cur.Status, cur.StatusReason, cur.StatusSince = a.Drain, a.AutoDrained, a.CooldownUntil, a.Status, a.StatusReason, a.StatusSince
	m.putAccount(ctx, cur)
}

// ---- background ----

// accountsTick (every minute): stats, automatic drain/undrain and status,
// alerts with dedupe, orphan hosts, daily infra digest.
func (m *Manager) accountsTick(ctx context.Context) error {
	now := time.Now().UTC()
	hosts, _ := m.Store.ListHosts(ctx)
	boxes, _ := m.Store.ListBoxes(ctx, store.BoxFilter{})
	known := map[string]bool{}
	for _, p := range m.Providers.All() {
		all := m.listAccounts(ctx, p.ID())
		usable, enabled := 0, 0
		for _, a := range all {
			known[a.ID] = true
			if a.Status == model.AccountArchived {
				m.clearAccountAlerts(ctx, a.ID)
				continue
			}
			st := m.computeStats(ctx, a, hosts, boxes, now)
			m.applyAuto(ctx, a, st, now)
			st = m.computeStats(ctx, a, hosts, boxes, now)
			m.evaluateAlerts(ctx, a, st, now)
			if a.Enabled {
				enabled++
			}
			if st.Eligible {
				usable++
			}
		}
		key := "single_account|" + p.ID()
		if enabled >= 2 && usable == 1 {
			m.raiseAlert(ctx, alertInput{Key: key, Type: "single_account", Level: "warn", Provider: p.ID(), Args: []any{p.ID()}})
		} else {
			m.clearAlert(ctx, key)
		}
		// failover notices close 15 minutes after the last failover
		m.expireAlert(ctx, "failover|"+p.ID(), 15*time.Minute)
	}
	for _, h := range hosts {
		if h.Status == "deleted" || h.AccountID == "" {
			continue
		}
		if !known[h.AccountID] {
			if _, err := m.Store.GetProviderAccount(ctx, h.AccountID); err != nil {
				m.raiseAlert(ctx, alertInput{Key: "orphan_host|" + h.ID, Type: "orphan_host", Level: "critical", Provider: h.Provider, AccountID: h.AccountID, HostID: h.ID,
					Args: []any{h.ID, h.Provider, "account " + h.AccountID + " missing"}})
			}
		}
		if h.ContractEndsAt != nil {
			m.expiryAlert(ctx, "contract_ending|"+h.ID, "contract_ending", h.Provider, h.AccountID, h.ID, *h.ContractEndsAt, 14, 3, now, h.ID)
		}
	}
	m.infraDigestTick(ctx, now)
	return nil
}

// ---- env ----

var labelRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

// envVarsOf lists the env variables of a provider (first = required).
func envVarsOf(p provider.Provider) []string {
	if sp, ok := p.(sandbox.Provider); ok {
		return sp.Info().EnvVars
	}
	return provider.EnvTokens[p.ID()]
}

// ConfigureProviderAccountsFromEnv adds one account per labeled env
// credential: <VAR>__<LABEL> (two underscores), e.g. HETZNER_TOKEN__EU2 or
// E2B_API_KEY__BACKUP. Multi-part credentials use the same label on every
// part (CONTABO_CLIENT_ID__B, CONTABO_CLIENT_SECRET__B, …); a missing
// optional part falls back to the unlabeled variable. Env accounts cannot be
// edited or rotated from the API (only enabled/disabled/drained); an env
// account whose variable disappeared is disabled. Returns the account ids.
func (m *Manager) ConfigureProviderAccountsFromEnv(ctx context.Context, environ []string) []string {
	env := map[string]string{}
	for _, kv := range environ {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	var ids []string
	seen := map[string]bool{}
	for _, p := range m.Providers.All() {
		vars := envVarsOf(p)
		if len(vars) == 0 {
			continue
		}
		labels := map[string]bool{}
		for k, v := range env {
			if strings.HasPrefix(k, vars[0]+"__") && strings.TrimSpace(v) != "" {
				if l := strings.TrimPrefix(k, vars[0]+"__"); labelRe.MatchString(l) {
					labels[l] = true
				}
			}
		}
		for label := range labels {
			get := func(k string) string {
				if v := env[k+"__"+label]; v != "" {
					return v
				}
				if k == vars[0] {
					return ""
				}
				return env[k]
			}
			tok := provider.TokenFromEnv(p.ID(), get)
			if ef, ok := p.(interface {
				TokenFromEnv(func(string) string) string
			}); ok {
				tok = ef.TokenFromEnv(get)
			}
			if tok == "" {
				continue
			}
			id := "pa_" + p.ID() + "_env_" + strings.ToLower(label)
			seen[id] = true
			ids = append(ids, id)
			m.upsertEnvAccount(ctx, p, id, "env:"+label, vars[0]+"__"+label, tok)
		}
	}
	// env accounts whose variable is gone are disabled (hosts keep working
	// only while the sealed copy is valid; the admin decides what to do)
	for _, p := range m.Providers.All() {
		for _, a := range m.listAccounts(ctx, p.ID()) {
			if a.CredentialSource == "env" && !a.IsDefault && strings.HasPrefix(a.ID, "pa_"+p.ID()+"_env_") && !seen[a.ID] && a.Enabled {
				a.Enabled, a.Status, a.StatusReason = false, model.AccountDisabled, "env variable removed"
				m.putAccount(ctx, a)
				m.EmitData(ctx, "system", "", "", "provider_account.disabled", p.ID()+" "+a.Label+": env variable removed", nil, map[string]any{"provider": p.ID(), "account_id": a.ID})
			}
		}
	}
	sort.Strings(ids)
	return ids
}

func (m *Manager) upsertEnvAccount(ctx context.Context, p provider.Provider, id, label, ref, tok string) {
	enc, err := m.Crypt.Seal(tok)
	if err != nil {
		return
	}
	now := time.Now().UTC()
	a, err := m.Store.GetProviderAccount(ctx, id)
	created := err != nil
	if created {
		a = &model.ProviderAccount{ID: id, Provider: p.ID(), Label: label, Enabled: true, Priority: 2, Weight: 1, CreatedAt: now, CreatedBy: "env"}
	}
	fp := Fingerprint(tok)
	rotated := !created && a.KeyFingerprint != fp
	a.CredentialRef, a.CredentialSource, a.CredentialEnc, a.KeyFingerprint = "env:"+ref, "env", enc, fp
	if rotated {
		a.RotatedAt = &now
		if a.Status == model.AccountAuthFailed {
			a.Status, a.StatusReason = model.AccountActive, ""
		}
	}
	if !a.Enabled && a.StatusReason == "env variable removed" {
		a.Enabled, a.Status, a.StatusReason = true, model.AccountActive, ""
	}
	m.putAccount(ctx, a)
	if mp, err := m.Store.GetProvider(ctx, p.ID()); err == nil && !mp.Connected {
		mp.Connected, mp.Error = true, nil
		m.Store.PutProvider(ctx, mp)
	}
	switch {
	case created:
		m.EmitData(ctx, "system", "", "", "provider_account.created", p.ID()+" "+label+" from env "+ref, nil,
			map[string]any{"provider": p.ID(), "account_id": id, "key_fingerprint": fp, "credential_source": "env"})
	case rotated:
		m.EmitData(ctx, "system", "", "", "provider_account.key_rotated", p.ID()+" "+label+": env credential changed", nil,
			map[string]any{"provider": p.ID(), "account_id": id, "key_fingerprint": fp, "credential_source": "env"})
	}
	go func() {
		c, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		acc, err := m.Store.GetProviderAccount(c, id)
		if err != nil {
			return
		}
		verr := p.Validate(c, tok)
		m.accountOutcome(c, acc, verr, 0)
	}()
}

// orphanHost raises the critical alert for a host Boxmes can no longer
// delete with its account's credential (spec §4.4): manual cleanup needed.
func (m *Manager) orphanHost(ctx context.Context, h *model.Host, a *model.ProviderAccount, err error) {
	console := ""
	if a != nil {
		console = a.ConsoleURL
	}
	m.raiseAlert(ctx, alertInput{Key: "orphan_host|" + h.ID, Type: "orphan_host", Level: "critical", Provider: h.Provider, AccountID: h.AccountID, HostID: h.ID,
		Args: []any{h.ID, h.Provider, err.Error()}})
	m.EmitData(ctx, "system", "", "", "provider_account.orphan_host", fmt.Sprintf("host %s (%s %s) cannot be deleted with account %s: %v", h.ID, h.Provider, h.ProviderRef, h.AccountID, err), nil,
		map[string]any{"provider": h.Provider, "account_id": h.AccountID, "host_id": h.ID, "provider_ref": h.ProviderRef, "console_url": console})
}

// AccountsTickNow runs the provider-accounts job once (tests, admin tools).
func (m *Manager) AccountsTickNow(ctx context.Context) error { return m.accountsTick(ctx) }
