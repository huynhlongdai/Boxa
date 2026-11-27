package manager

// v2.2 admin API for provider accounts (/v1/admin/providers/...).
// Platform admin only. Tokens are write-only; responses carry fingerprints.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"boxmes/core/internal/model"
	"boxmes/core/internal/provhealth"
	"boxmes/core/internal/secret"
	"boxmes/core/internal/store"
)

// AccountView is an account with its stats and (on writes) warnings.
type AccountView struct {
	*model.ProviderAccount
	Warnings []string `json:"warnings,omitempty"`
}

// AccountInput is POST /v1/admin/providers/{p}/accounts and the body of
// PATCH (merge-patch: absent fields keep their value; for dates "" clears).
// Dates are RFC 3339 or YYYY-MM-DD; money is in the account currency.
type AccountInput struct {
	Label        *string `json:"label,omitempty"`
	Token        string  `json:"token,omitempty"`         // create only (rotate uses /rotate)
	BillingToken *string `json:"billing_token,omitempty"` // optional read-only billing key ("" removes it)
	SkipValidate bool    `json:"skip_validate,omitempty"` // store without calling the provider (offline setup)

	ExternalID *string   `json:"external_id,omitempty"`
	Tags       *[]string `json:"tags,omitempty"`
	Owner      *string   `json:"owner,omitempty"`
	Notes      *string   `json:"notes,omitempty"`
	ConsoleURL *string   `json:"console_url,omitempty"`
	Scopes     *[]string `json:"scopes,omitempty"`

	Enabled  *bool     `json:"enabled,omitempty"`
	Priority *int      `json:"priority,omitempty"`
	Weight   *int      `json:"weight,omitempty"`
	Regions  *[]string `json:"regions,omitempty"`
	Pool     *string   `json:"pool,omitempty"`
	Drain    *bool     `json:"drain,omitempty"`

	BillingModel    *string  `json:"billing_model,omitempty"`
	Currency        *string  `json:"currency,omitempty"`
	FXRateUSD       *float64 `json:"fx_rate_usd,omitempty"`
	Balance         *float64 `json:"balance,omitempty"` // manual balance (sets balance_source=manual, balance_at=now)
	ClearBalance    bool     `json:"clear_balance,omitempty"`
	CreditTotal     *float64 `json:"credit_total,omitempty"`
	CreditRemaining *float64 `json:"credit_remaining,omitempty"`
	PendingCharges  *float64 `json:"pending_charges,omitempty"`
	MonthToDate     *float64 `json:"month_to_date,omitempty"`
	SpendCapDay     *float64 `json:"spend_cap_day,omitempty"`
	SpendCapMonth   *float64 `json:"spend_cap_month,omitempty"`
	MinBalance      *float64 `json:"min_balance,omitempty"`
	LowBalanceWarn  *float64 `json:"low_balance_warn,omitempty"`
	BillingCycle    *string  `json:"billing_cycle,omitempty"`
	InvoiceDay      *int     `json:"invoice_day,omitempty"`
	RotateEveryDays *int     `json:"rotate_every_days,omitempty"`

	TokenExpiresAt   *string `json:"token_expires_at,omitempty"`
	RefreshExpiresAt *string `json:"refresh_expires_at,omitempty"`
	CreditExpiresAt  *string `json:"credit_expires_at,omitempty"`
	TrialEndsAt      *string `json:"trial_ends_at,omitempty"`
	ContractEndsAt   *string `json:"contract_ends_at,omitempty"`
	NextInvoiceAt    *string `json:"next_invoice_at,omitempty"`

	// Limits: manual provider-side limits, e.g. {"servers": 25, "vcpu": 100,
	// "concurrent_sandboxes": 20, "create_per_sec": 1, "create_per_min": 300}
	// (0 removes one).
	Limits map[string]float64 `json:"limits,omitempty"`

	// M29-S sandbox accounts
	Templates             *[]string                `json:"templates,omitempty"`               // templates / snapshots that exist in this account (replaces the list)
	ClearTemplatesMissing bool                     `json:"clear_templates_missing,omitempty"` // forget the learned "template not found" entries
	BillingGroup          *string                  `json:"billing_group,omitempty"`           // accounts paying from one wallet ("" clears)
	LimitGroup            *string                  `json:"limit_group,omitempty"`             // accounts sharing provider-side limits ("" clears)
	Thresholds            *model.AccountThresholds `json:"thresholds,omitempty"`
}

// RotateInput is POST …/accounts/{a}/rotate.
type RotateInput struct {
	Token        string `json:"token"`
	ExternalID   string `json:"external_id,omitempty"` // must equal the account's external_id when that is set
	SkipValidate bool   `json:"skip_validate,omitempty"`
}

// AccountTestResult is POST …/accounts/{a}/test and …/accounts/validate.
type AccountTestResult struct {
	OK             bool         `json:"ok"`
	Error          string       `json:"error,omitempty"`
	ErrorClass     string       `json:"error_class,omitempty"`
	LatencyMS      float64      `json:"latency_ms"`
	KeyFingerprint string       `json:"key_fingerprint,omitempty"`
	Duplicates     []string     `json:"duplicates,omitempty"` // accounts with the same external_id or key
	Account        *AccountView `json:"account,omitempty"`
}

// ValidateAccountInput is POST …/accounts/validate.
type ValidateAccountInput struct {
	Token      string `json:"token"`
	ExternalID string `json:"external_id,omitempty"`
}

// StrategyInput is PUT /v1/admin/providers/{p}/strategy.
type StrategyInput struct {
	Strategy               string `json:"strategy"` // priority | weighted | round_robin | least_used | drain_first ("" = default)
	StickyMinutes          int    `json:"sticky_minutes"`
	FailoverAcrossAccounts *bool  `json:"failover_across_accounts,omitempty"`
}

// StrategyView is GET/PUT …/strategy.
type StrategyView struct {
	Provider               string   `json:"provider"`
	Strategy               string   `json:"strategy"`           // stored ("" = default)
	EffectiveStrategy      string   `json:"effective_strategy"` // what the scheduler uses
	StickyMinutes          int      `json:"sticky_minutes"`
	FailoverAcrossAccounts bool     `json:"failover_across_accounts"`
	Strategies             []string `json:"strategies"`
}

// AdminProviderRow is one row of GET /v1/admin/providers.
type AdminProviderRow struct {
	ID               string             `json:"id"`
	Name             string             `json:"name"`
	Tier             string             `json:"tier"`
	Group            string             `json:"group"` // vps | sandbox
	Connected        bool               `json:"connected"`
	Implemented      bool               `json:"implemented"`
	Health           string             `json:"health"` // provider-level breaker status
	Strategy         string             `json:"strategy"`
	AccountsTotal    int                `json:"accounts_total"`
	AccountsHealthy  int                `json:"accounts_healthy"` // eligible now
	AccountsDegraded int                `json:"accounts_degraded"`
	HostsLive        int                `json:"hosts_live"`
	BoxesLive        int                `json:"boxes_live"`
	BalanceUSD       *float64           `json:"balance_usd,omitempty"`         // Σ effective balances (USD)
	BalanceByCur     map[string]float64 `json:"balance_by_currency,omitempty"` // original currencies
	SpendTodayUSD    float64            `json:"spend_today_usd"`
	SpendMonthUSD    float64            `json:"spend_month_usd"`
	ForecastMonthUSD float64            `json:"forecast_month_usd"`
	RunwayDays       *float64           `json:"runway_days,omitempty"` // eligible accounts together (failover)
	MaxLimitPct      float64            `json:"max_limit_pct"`
	OpenAlerts       int                `json:"open_alerts"`
}

// AdminProviderDetail is GET /v1/admin/providers/{p}.
type AdminProviderDetail struct {
	AdminProviderRow
	Prefs    model.ProviderPrefs `json:"prefs"`
	Strategy StrategyView        `json:"strategy_config"`
	Accounts []*AccountView      `json:"accounts"`
	Alerts   []*InfraAlert       `json:"alerts"`
	Caps     map[string]any      `json:"capabilities,omitempty"`
}

// InfraSummary is the KPI strip of GET /v1/admin/providers (top level).
type InfraSummary struct {
	BalanceUSD       float64            `json:"balance_usd"`
	BalanceByCur     map[string]float64 `json:"balance_by_currency"`
	SpendTodayUSD    float64            `json:"spend_today_usd"`
	SpendMonthUSD    float64            `json:"spend_month_usd"`
	ForecastMonthUSD float64            `json:"forecast_month_usd"`
	ShortestRunway   *RunwayRef         `json:"shortest_runway,omitempty"`
	AccountsHealthy  int                `json:"accounts_healthy"`
	AccountsDegraded int                `json:"accounts_degraded"`
	AccountsBroken   int                `json:"accounts_broken"`
	OpenAlerts       int                `json:"open_alerts"`
}

// RunwayRef names the account with the shortest runway.
type RunwayRef struct {
	Provider  string  `json:"provider"`
	AccountID string  `json:"account_id"`
	Label     string  `json:"label"`
	Days      float64 `json:"days"`
}

// AdminProvidersView is GET /v1/admin/providers.
type AdminProvidersView struct {
	Summary InfraSummary        `json:"summary"`
	Items   []*AdminProviderRow `json:"items"`
}

func (m *Manager) requireProvider(id string) error {
	if _, ok := m.Providers.Get(id); !ok || id == "local" {
		return notFound("provider", id)
	}
	return nil
}

func (m *Manager) getAccount(ctx context.Context, prov, id string) (*model.ProviderAccount, error) {
	a, err := m.Store.GetProviderAccount(ctx, id)
	if err != nil || a.Provider != prov {
		return nil, notFound("provider account", id)
	}
	normAccount(a)
	return a, nil
}

func (m *Manager) viewAccount(ctx context.Context, a *model.ProviderAccount, hosts []*model.Host) *AccountView {
	a.Stats = m.computeStats(ctx, a, hosts, nil, time.Now().UTC())
	return &AccountView{ProviderAccount: a}
}

// ListProviderAccounts is GET /v1/admin/providers/{p}/accounts.
func (m *Manager) ListProviderAccounts(ctx context.Context, p *Principal, prov string, archived bool) ([]*AccountView, error) {
	if err := p.Require(model.ScopeAdmin); err != nil {
		return nil, err
	}
	if err := m.requireProvider(prov); err != nil {
		return nil, err
	}
	hosts, _ := m.Store.ListHosts(ctx)
	out := []*AccountView{}
	for _, a := range m.listAccounts(ctx, prov) {
		if a.Status == model.AccountArchived && !archived {
			continue
		}
		out = append(out, m.viewAccount(ctx, a, hosts))
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Priority != out[j].Priority {
			return out[i].Priority < out[j].Priority
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// GetProviderAccount is GET …/accounts/{a}.
func (m *Manager) GetProviderAccount(ctx context.Context, p *Principal, prov, id string) (*AccountView, error) {
	if err := p.Require(model.ScopeAdmin); err != nil {
		return nil, err
	}
	a, err := m.getAccount(ctx, prov, id)
	if err != nil {
		return nil, err
	}
	return m.viewAccount(ctx, a, nil), nil
}

func parseWhen(s string) (*time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	for _, f := range []string{time.RFC3339, "2006-01-02T15:04", "2006-01-02"} {
		if t, err := time.Parse(f, s); err == nil {
			t = t.UTC()
			return &t, nil
		}
	}
	return nil, model.E(model.ErrInvalid, "bad date %q (use RFC 3339 or YYYY-MM-DD)", s)
}

func validCur(s string) bool {
	if len(s) != 3 {
		return false
	}
	for _, c := range s {
		if c < 'A' || c > 'Z' {
			return false
		}
	}
	return true
}

// applyInput merges in into a (shared by create and patch).
func (m *Manager) applyInput(ctx context.Context, a *model.ProviderAccount, in AccountInput, actor string) ([]string, error) {
	var changed []string
	set := func(name string) { changed = append(changed, name) }
	bad := func(f string, args ...any) error {
		return model.EH(model.ErrInvalid, "See the provider accounts API in docs/api-contract-v1.md.", f, args...)
	}
	if in.Label != nil {
		l := strings.TrimSpace(*in.Label)
		if l == "" || len(l) > 80 {
			return nil, bad("label must be 1..80 characters")
		}
		a.Label = l
		set("label")
	}
	str := func(dst *string, v *string, name string) {
		if v != nil {
			*dst = strings.TrimSpace(*v)
			set(name)
		}
	}
	str(&a.ExternalID, in.ExternalID, "external_id")
	str(&a.Owner, in.Owner, "owner")
	str(&a.Notes, in.Notes, "notes")
	str(&a.ConsoleURL, in.ConsoleURL, "console_url")
	str(&a.Pool, in.Pool, "pool")
	if in.Tags != nil {
		a.Tags = append([]string{}, (*in.Tags)...)
		set("tags")
	}
	if in.Scopes != nil {
		a.Scopes = append([]string{}, (*in.Scopes)...)
		set("scopes")
	}
	if in.Regions != nil {
		a.Regions = append([]string{}, (*in.Regions)...)
		set("regions")
	}
	if in.Enabled != nil {
		a.Enabled = *in.Enabled
		set("enabled")
		if a.Enabled && a.Status == model.AccountDisabled {
			a.Status, a.StatusReason = model.AccountActive, ""
		}
		if !a.Enabled {
			a.Status, a.StatusReason = model.AccountDisabled, "disabled by "+actor
		}
	}
	if in.Priority != nil {
		if *in.Priority < 1 || *in.Priority > 99 {
			return nil, bad("priority must be 1..99 (1 = first)")
		}
		a.Priority = *in.Priority
		set("priority")
	}
	if in.Weight != nil {
		if *in.Weight < 1 || *in.Weight > 1000 {
			return nil, bad("weight must be 1..1000")
		}
		a.Weight = *in.Weight
		set("weight")
	}
	if in.Drain != nil {
		a.Drain = *in.Drain
		a.AutoDrained = false
		set("drain")
		if a.Drain {
			a.Status, a.StatusReason = model.AccountDraining, "drained by "+actor
		} else if a.Status == model.AccountDraining {
			a.Status, a.StatusReason = model.AccountActive, ""
		}
	}
	if in.BillingModel != nil {
		switch *in.BillingModel {
		case "", "prepaid", "postpaid", "contract", "free_tier":
		default:
			return nil, bad("billing_model must be prepaid, postpaid, contract or free_tier")
		}
		a.BillingModel = *in.BillingModel
		set("billing_model")
	}
	if in.BillingCycle != nil {
		switch *in.BillingCycle {
		case "", "monthly", "hourly-prepaid", "contract":
		default:
			return nil, bad("billing_cycle must be monthly, hourly-prepaid or contract")
		}
		a.BillingCycle = *in.BillingCycle
		set("billing_cycle")
	}
	if in.Currency != nil {
		c := strings.ToUpper(strings.TrimSpace(*in.Currency))
		if !validCur(c) {
			return nil, bad("currency must be a 3-letter ISO code such as USD or EUR")
		}
		a.Currency = c
		set("currency")
	}
	nonNeg := func(v *float64, name string, dst *float64) error {
		if v == nil {
			return nil
		}
		if *v < 0 {
			return bad("%s must be >= 0", name)
		}
		*dst = *v
		set(name)
		return nil
	}
	for _, f := range []struct {
		v    *float64
		name string
		dst  *float64
	}{{in.FXRateUSD, "fx_rate_usd", &a.FXRateUSD}, {in.SpendCapDay, "spend_cap_day", &a.SpendCapDay}, {in.SpendCapMonth, "spend_cap_month", &a.SpendCapMonth},
		{in.MinBalance, "min_balance", &a.MinBalance}, {in.LowBalanceWarn, "low_balance_warn", &a.LowBalanceWarn}} {
		if err := nonNeg(f.v, f.name, f.dst); err != nil {
			return nil, err
		}
	}
	optF := func(v *float64, dst **float64, name string) {
		if v != nil {
			x := *v
			*dst = &x
			set(name)
		}
	}
	optF(in.CreditTotal, &a.CreditTotal, "credit_total")
	optF(in.CreditRemaining, &a.CreditRemaining, "credit_remaining")
	if in.CreditRemaining != nil {
		at := time.Now().UTC()
		a.CreditAt = &at // effective credit = credit − ledger spend since now
		if a.Status == model.AccountOutOfCredit && a.Balance == nil && *in.CreditRemaining > a.MinBalance {
			a.Status, a.StatusReason = model.AccountActive, ""
		}
	}
	if in.Templates != nil {
		a.Templates = dedupeStrs(*in.Templates)
		set("templates")
	}
	if in.ClearTemplatesMissing {
		a.TemplatesMissing = []string{}
		set("templates_missing")
	}
	group := func(v *string, dst *string, name string) error {
		if v == nil {
			return nil
		}
		g := strings.TrimSpace(*v)
		if len(g) > 64 || strings.ContainsAny(g, " |/") {
			return bad("%s must be up to 64 characters without spaces, | or /", name)
		}
		*dst = g
		set(name)
		return nil
	}
	if err := group(in.BillingGroup, &a.BillingGroup, "billing_group"); err != nil {
		return nil, err
	}
	if err := group(in.LimitGroup, &a.LimitGroup, "limit_group"); err != nil {
		return nil, err
	}
	optF(in.PendingCharges, &a.PendingCharges, "pending_charges")
	optF(in.MonthToDate, &a.MonthToDate, "month_to_date")
	now := time.Now().UTC()
	if in.ClearBalance {
		a.Balance, a.BalanceAt, a.BalanceSource = nil, nil, ""
		set("balance")
	} else if in.Balance != nil {
		old := a.Balance
		x := *in.Balance
		a.Balance, a.BalanceAt, a.BalanceSource = &x, &now, "manual"
		set("balance")
		if a.Status == model.AccountOutOfCredit && x > a.MinBalance {
			a.Status, a.StatusReason = model.AccountActive, ""
		}
		data := map[string]any{"provider": a.Provider, "account_id": a.ID, "source": "manual", "new": x, "currency": a.Currency}
		if old != nil {
			data["old"] = *old
		}
		m.EmitData(ctx, actor, "", "", "provider_account.balance_updated", fmt.Sprintf("%s %s: balance %.2f %s (manual)", a.Provider, a.Label, x, a.Currency), nil, data)
	}
	if in.InvoiceDay != nil {
		if *in.InvoiceDay < 0 || *in.InvoiceDay > 31 {
			return nil, bad("invoice_day must be 1..31 (0 = unknown)")
		}
		a.InvoiceDay = *in.InvoiceDay
		set("invoice_day")
	}
	if in.RotateEveryDays != nil {
		if *in.RotateEveryDays < 0 {
			return nil, bad("%s must be >= 0", "rotate_every_days")
		}
		a.RotateEveryDays = *in.RotateEveryDays
		set("rotate_every_days")
	}
	for _, d := range []struct {
		v    *string
		dst  **time.Time
		name string
	}{{in.TokenExpiresAt, &a.TokenExpiresAt, "token_expires_at"}, {in.RefreshExpiresAt, &a.RefreshExpiresAt, "refresh_expires_at"},
		{in.CreditExpiresAt, &a.CreditExpiresAt, "credit_expires_at"}, {in.TrialEndsAt, &a.TrialEndsAt, "trial_ends_at"},
		{in.ContractEndsAt, &a.ContractEndsAt, "contract_ends_at"}, {in.NextInvoiceAt, &a.NextInvoiceAt, "next_invoice_at"}} {
		if d.v == nil {
			continue
		}
		t, err := parseWhen(*d.v)
		if err != nil {
			return nil, err
		}
		*d.dst = t
		set(d.name)
	}
	if in.Limits != nil {
		for k, v := range in.Limits {
			if v < 0 {
				return nil, bad("limit %s must be >= 0", k)
			}
			if v == 0 {
				delete(a.Limits, k)
				continue
			}
			a.Limits[k] = model.AccountLimit{Limit: v, Source: "manual", At: &now}
		}
		set("limits")
	}
	if in.Thresholds != nil {
		a.Thresholds = *in.Thresholds
		set("thresholds")
	}
	if in.BillingToken != nil {
		if t := strings.TrimSpace(*in.BillingToken); t == "" {
			a.BillingCredentialEnc, a.BillingKeyFingerprint = "", ""
		} else {
			enc, err := m.Crypt.Seal(t)
			if err != nil {
				return nil, err
			}
			a.BillingCredentialEnc, a.BillingKeyFingerprint = enc, Fingerprint(t)
		}
		set("billing_token")
	}
	return changed, nil
}

// duplicates lists other live accounts with the same external id or key.
func (m *Manager) duplicates(ctx context.Context, prov, self, externalID, fp string) []string {
	var out []string
	for _, a := range m.listAccounts(ctx, prov) {
		if a.ID == self || a.Status == model.AccountArchived {
			continue
		}
		if (externalID != "" && a.ExternalID == externalID) || (fp != "" && a.KeyFingerprint == fp) {
			out = append(out, a.ID)
		}
	}
	return out
}

func dupWarning(d []string) []string {
	if len(d) == 0 {
		return nil
	}
	return []string{"same provider account / key as " + strings.Join(d, ", ") + ": not a real backup (spec §8.4)"}
}

func (m *Manager) validateToken(ctx context.Context, prov, tok string) *AccountTestResult {
	pv, _ := m.Providers.Get(prov)
	res := &AccountTestResult{KeyFingerprint: Fingerprint(tok)}
	c, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	start := time.Now()
	err := pv.Validate(c, tok)
	res.LatencyMS = float64(time.Since(start).Microseconds()) / 1000
	if err != nil {
		res.Error = err.Error()
		if ae, ok := err.(*model.APIError); ok {
			res.Error = ae.Message
		}
		res.ErrorClass = provhealth.Classify(err)
		return res
	}
	res.OK = true
	return res
}

// ValidateProviderAccount is POST …/accounts/validate: checks a key without
// saving it and reports accounts it would duplicate.
func (m *Manager) ValidateProviderAccount(ctx context.Context, p *Principal, prov string, in ValidateAccountInput) (*AccountTestResult, error) {
	if err := p.Require(model.ScopeAdmin); err != nil {
		return nil, err
	}
	if err := m.requireProvider(prov); err != nil {
		return nil, err
	}
	tok := strings.TrimSpace(in.Token)
	if tok == "" {
		return nil, model.EH(model.ErrInvalid, "Paste the API token from the provider console.", "token is required")
	}
	res := m.validateToken(ctx, prov, tok)
	res.Duplicates = m.duplicates(ctx, prov, "", strings.TrimSpace(in.ExternalID), res.KeyFingerprint)
	return res, nil
}

// CreateProviderAccount is POST /v1/admin/providers/{p}/accounts.
func (m *Manager) CreateProviderAccount(ctx context.Context, p *Principal, prov string, in AccountInput) (*AccountView, error) {
	if err := p.Require(model.ScopeAdmin); err != nil {
		return nil, err
	}
	if err := m.requireProvider(prov); err != nil {
		return nil, err
	}
	tok := strings.TrimSpace(in.Token)
	if tok == "" {
		return nil, model.EH(model.ErrInvalid, "Paste the API token from the provider console.", "token is required")
	}
	if in.Label == nil || strings.TrimSpace(*in.Label) == "" {
		return nil, model.EH(model.ErrInvalid, "Give the account a label such as \"Hetzner EU #2\".", "label is required")
	}
	if !in.SkipValidate {
		if r := m.validateToken(ctx, prov, tok); !r.OK {
			return nil, model.EH(model.ErrInvalid, "Create a read+write API token in the provider console and paste it again.", "%s rejected the token (%s): %s", prov, r.ErrorClass, r.Error)
		}
	}
	enc, err := m.Crypt.Seal(tok)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	a := &model.ProviderAccount{ID: "pa_" + secret.RandHex(8), Provider: prov, Enabled: true, Priority: 1, Weight: 1, Status: model.AccountActive,
		CredentialRef: "account", CredentialSource: "api", CredentialEnc: enc, KeyFingerprint: Fingerprint(tok), CreatedAt: now, CreatedBy: p.Actor(), UpdatedBy: p.Actor()}
	normAccount(a)
	if in.Priority == nil { // after the existing accounts
		maxP := 0
		for _, x := range m.listAccounts(ctx, prov) {
			if x.Status != model.AccountArchived && x.Priority > maxP {
				maxP = x.Priority
			}
		}
		if maxP > 0 {
			a.Priority = min(maxP+1, 99)
		}
	}
	if _, err := m.applyInput(ctx, a, in, p.Actor()); err != nil {
		return nil, err
	}
	dups := m.duplicates(ctx, prov, a.ID, a.ExternalID, a.KeyFingerprint)
	if !in.SkipValidate {
		a.LastOKAt, a.LastCheckAt = &now, &now
	}
	if err := m.putAccount(ctx, a); err != nil {
		return nil, err
	}
	if mp, err := m.Store.GetProvider(ctx, prov); err == nil && !mp.Connected {
		mp.Connected, mp.Error = true, nil
		m.Store.PutProvider(ctx, mp)
	}
	m.Health.Reset(acctKey(a))
	m.EmitData(ctx, p.Actor(), "", "", "provider_account.created", fmt.Sprintf("%s: account %s added (key %s)", prov, a.Label, a.KeyFingerprint), nil,
		map[string]any{"provider": prov, "account_id": a.ID, "key_fingerprint": a.KeyFingerprint, "credential_source": "api", "label": a.Label})
	v := m.viewAccount(ctx, a, nil)
	v.Warnings = append(dupWarning(dups), m.sandboxAccountWarnings(ctx, a)...)
	return v, nil
}

// PatchProviderAccount is PATCH …/accounts/{a} (merge-patch, manual money
// and dates included). Env accounts only take enabled / drain / priority /
// weight / regions / money / dates (their key comes from env).
func (m *Manager) PatchProviderAccount(ctx context.Context, p *Principal, prov, id string, in AccountInput) (*AccountView, error) {
	if err := p.Require(model.ScopeAdmin); err != nil {
		return nil, err
	}
	l := m.acctLock(id)
	l.Lock()
	a, err := m.getAccount(ctx, prov, id)
	if err != nil {
		l.Unlock()
		return nil, err
	}
	if in.Token != "" {
		l.Unlock()
		return nil, model.EH(model.ErrInvalid, "Use POST /v1/admin/providers/{p}/accounts/{a}/rotate to change the key.", "token cannot be changed with PATCH")
	}
	if a.Status == model.AccountArchived && (in.Enabled == nil || !*in.Enabled) {
		l.Unlock()
		return nil, model.EH(model.ErrInvalid, "Send {\"enabled\": true} to restore an archived account.", "account %s is archived", id)
	}
	if a.Status == model.AccountArchived {
		a.Status = model.AccountActive
	}
	oldEnabled, oldDrain, oldStatus := a.Enabled, a.Drain, a.Status
	changed, err := m.applyInput(ctx, a, in, p.Actor())
	if err != nil {
		l.Unlock()
		return nil, err
	}
	a.UpdatedBy = p.Actor()
	err = m.putAccount(ctx, a)
	l.Unlock()
	if err != nil {
		return nil, err
	}
	data := map[string]any{"provider": prov, "account_id": id, "fields": changed, "key_fingerprint": a.KeyFingerprint}
	m.EmitData(ctx, p.Actor(), "", "", "provider_account.updated", fmt.Sprintf("%s %s updated: %s", prov, a.Label, strings.Join(changed, ", ")), nil, data)
	if oldEnabled != a.Enabled {
		typ := "provider_account.enabled"
		if !a.Enabled {
			typ = "provider_account.disabled"
		}
		m.EmitData(ctx, p.Actor(), "", "", typ, prov+" "+a.Label, nil, map[string]any{"provider": prov, "account_id": id, "key_fingerprint": a.KeyFingerprint})
	}
	if oldDrain != a.Drain {
		typ := "provider_account.drained"
		if !a.Drain {
			typ = "provider_account.undrained"
		}
		m.EmitData(ctx, p.Actor(), "", "", typ, prov+" "+a.Label, nil, map[string]any{"provider": prov, "account_id": id})
	}
	if oldStatus == model.AccountOutOfCredit && a.Status != model.AccountOutOfCredit {
		m.syncBillingGroup(ctx, a, false) // the shared wallet was topped up
	}
	v := m.viewAccount(ctx, a, nil)
	v.Warnings = append(dupWarning(m.duplicates(ctx, prov, id, a.ExternalID, "")), m.sandboxAccountWarnings(ctx, a)...)
	return v, nil
}

func dedupeStrs(l []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, x := range l {
		if x = strings.TrimSpace(x); x != "" && !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

// SetAccountState is POST …/accounts/{a}/{enable|disable|drain|undrain|reset}.
func (m *Manager) SetAccountState(ctx context.Context, p *Principal, prov, id, action string) (*AccountView, error) {
	t, f := true, false
	switch action {
	case "enable":
		return m.PatchProviderAccount(ctx, p, prov, id, AccountInput{Enabled: &t})
	case "disable":
		return m.PatchProviderAccount(ctx, p, prov, id, AccountInput{Enabled: &f})
	case "drain":
		return m.PatchProviderAccount(ctx, p, prov, id, AccountInput{Drain: &t})
	case "undrain":
		return m.PatchProviderAccount(ctx, p, prov, id, AccountInput{Drain: &f})
	case "reset":
		if err := p.Require(model.ScopeAdmin); err != nil {
			return nil, err
		}
		l := m.acctLock(id)
		l.Lock()
		a, err := m.getAccount(ctx, prov, id)
		if err != nil {
			l.Unlock()
			return nil, err
		}
		old := a.Status
		a.CooldownUntil, a.ErrorStreak, a.LastError, a.LastErrorClass = nil, 0, "", ""
		if a.Status == model.AccountAuthFailed || a.Status == model.AccountOutOfCredit || a.Status == model.AccountCooling || a.Status == model.AccountRateLimited || a.Status == model.AccountQuotaFull {
			a.Status, a.StatusReason = model.AccountActive, ""
		}
		for k, lim := range a.Limits {
			if lim.Source == "learned" {
				delete(a.Limits, k)
			}
		}
		m.Health.Reset(acctKey(a))
		m.setAccountStatus(ctx, a, old, time.Now().UTC())
		err = m.putAccount(ctx, a)
		l.Unlock()
		if err != nil {
			return nil, err
		}
		m.EmitData(ctx, p.Actor(), "", "", "provider_account.reset", prov+" "+a.Label+": breaker, cooldown and learned limits reset", nil, map[string]any{"provider": prov, "account_id": id})
		return m.viewAccount(ctx, a, nil), nil
	}
	return nil, model.E(model.ErrNotFound, "no route %s %s", "POST", action)
}

// RotateProviderAccount is POST …/accounts/{a}/rotate: validates the new
// key, refuses a key of another provider account (external_id must match),
// swaps it atomically and keeps every host on this account.
func (m *Manager) RotateProviderAccount(ctx context.Context, p *Principal, prov, id string, in RotateInput) (*AccountView, error) {
	if err := p.Require(model.ScopeAdmin); err != nil {
		return nil, err
	}
	tok := strings.TrimSpace(in.Token)
	if tok == "" {
		return nil, model.EH(model.ErrInvalid, "Paste the API token from the provider console.", "token is required")
	}
	a, err := m.getAccount(ctx, prov, id)
	if err != nil {
		return nil, err
	}
	if a.CredentialSource == "env" {
		return nil, model.EH(model.ErrInvalid, "Change the environment variable and restart core; env accounts can only be enabled, disabled or drained here.", "account %s takes its key from %s", id, strings.TrimPrefix(a.CredentialRef, "env:"))
	}
	ext := strings.TrimSpace(in.ExternalID)
	if a.ExternalID != "" && ext != a.ExternalID {
		return nil, model.EH(model.ErrInvalid, "A key of another provider account cannot replace this one (its hosts could no longer be deleted): add it as a new account.", "external_id %q does not match the account's %q", ext, a.ExternalID)
	}
	if !in.SkipValidate {
		if r := m.validateToken(ctx, prov, tok); !r.OK {
			return nil, model.EH(model.ErrInvalid, "Create a read+write API token in the provider console and paste it again.", "%s rejected the token (%s): %s", prov, r.ErrorClass, r.Error)
		}
	}
	enc, err := m.Crypt.Seal(tok)
	if err != nil {
		return nil, err
	}
	l := m.acctLock(id)
	l.Lock()
	a, err = m.getAccount(ctx, prov, id)
	if err != nil {
		l.Unlock()
		return nil, err
	}
	now := time.Now().UTC()
	oldFP, oldStatus := a.KeyFingerprint, a.Status
	if strings.HasPrefix(a.CredentialRef, "provider:") {
		mp, err := m.Store.GetProvider(ctx, prov)
		if err != nil {
			l.Unlock()
			return nil, err
		}
		mp.TokenEnc, mp.Connected, mp.Error, mp.TokenSource = enc, true, nil, "api"
		if err := m.Store.PutProvider(ctx, mp); err != nil {
			l.Unlock()
			return nil, err
		}
	} else {
		a.CredentialEnc = enc
	}
	a.KeyFingerprint, a.RotatedAt, a.UpdatedBy, a.CredentialSource = Fingerprint(tok), &now, p.Actor(), "api"
	if a.Status == model.AccountAuthFailed {
		a.Status, a.StatusReason = model.AccountActive, ""
	}
	a.CooldownUntil, a.ErrorStreak = nil, 0
	m.Health.Reset(acctKey(a))
	m.setAccountStatus(ctx, a, oldStatus, now)
	err = m.putAccount(ctx, a)
	l.Unlock()
	if err != nil {
		return nil, err
	}
	m.EmitData(ctx, p.Actor(), "", "", "provider_account.key_rotated", fmt.Sprintf("%s %s: key %s → %s; revoke the old key in the provider console", prov, a.Label, oldFP, a.KeyFingerprint), nil,
		map[string]any{"provider": prov, "account_id": id, "old_fingerprint": oldFP, "key_fingerprint": a.KeyFingerprint})
	m.refreshShimCreds(ctx, prov, id) // live T3 shims of the account use the new key at once
	v := m.viewAccount(ctx, a, nil)
	if a.ExternalID == "" {
		v.Warnings = []string{"external_id unknown: make sure the new key belongs to the same provider account, or its hosts cannot be deleted"}
	}
	return v, nil
}

// TestProviderAccount is POST …/accounts/{a}/test: probe the stored key now.
func (m *Manager) TestProviderAccount(ctx context.Context, p *Principal, prov, id string) (*AccountTestResult, error) {
	if err := p.Require(model.ScopeAdmin); err != nil {
		return nil, err
	}
	a, err := m.getAccount(ctx, prov, id)
	if err != nil {
		return nil, err
	}
	tok, err := m.credentialFor(ctx, a)
	if err != nil {
		return nil, err
	}
	res := m.validateToken(ctx, prov, tok)
	m.Health.Record(acctKey(a), provhealth.Sample{Kind: provhealth.KindAPI, LatencyMS: res.LatencyMS}, errIf(res), 0)
	m.saveHealth(ctx)
	if res.OK {
		m.accountOutcome(ctx, a, nil, 0)
		m.EmitData(ctx, p.Actor(), "", "", "provider_account.validated", prov+" "+a.Label+" key OK", nil, map[string]any{"provider": prov, "account_id": id, "key_fingerprint": a.KeyFingerprint})
	} else {
		m.accountOutcome(ctx, a, fmt.Errorf("%s", res.Error), 0)
		m.EmitData(ctx, p.Actor(), "", "", "provider_account.check_failed", fmt.Sprintf("%s %s: %s (%s)", prov, a.Label, res.Error, res.ErrorClass), nil,
			map[string]any{"provider": prov, "account_id": id, "error_class": res.ErrorClass, "key_fingerprint": a.KeyFingerprint})
	}
	a, _ = m.getAccount(ctx, prov, id)
	if a != nil {
		res.Account = m.viewAccount(ctx, a, nil)
	}
	return res, nil
}

func errIf(r *AccountTestResult) error {
	if r.OK {
		return nil
	}
	return fmt.Errorf("%s", r.Error)
}

// DeleteProviderAccount is DELETE …/accounts/{a}: archives the account
// (no new hosts, key kept so its hosts can still be deleted). Refused while
// hosts live on it unless force (reason required). purge removes the row
// once no host references it.
func (m *Manager) DeleteProviderAccount(ctx context.Context, p *Principal, prov, id string, force, purge bool, reason string) error {
	if err := p.Require(model.ScopeAdmin); err != nil {
		return err
	}
	a, err := m.getAccount(ctx, prov, id)
	if err != nil {
		return err
	}
	hosts, _ := m.Store.ListHosts(ctx)
	live, refs := 0, 0
	for _, h := range hosts {
		if h.AccountID == id {
			if liveHost(h) {
				live++
			}
			if h.Status != "deleted" {
				refs++
			}
		}
	}
	if live > 0 && !force {
		return model.EH(model.ErrInvalid, "Delete or move its hosts first, or pass ?force=1&reason=… (the hosts keep the archived key).", "account %s still has %d live hosts", id, live)
	}
	if force && live > 0 && strings.TrimSpace(reason) == "" {
		return model.EH(model.ErrInvalid, "Say why in ?reason=… (kept in the audit log).", "reason is required with force")
	}
	if purge && refs > 0 {
		return model.EH(model.ErrInvalid, "Archive it instead (DELETE without purge).", "account %s is still referenced by %d hosts", id, refs)
	}
	if purge {
		if strings.HasPrefix(a.CredentialRef, "provider:") {
			if mp, err := m.Store.GetProvider(ctx, prov); err == nil {
				mp.TokenEnc, mp.TokenSource = "", ""
				m.Store.PutProvider(ctx, mp)
			}
		}
		if err := m.Store.DeleteProviderAccount(ctx, id); err != nil {
			return err
		}
		m.invalidateAccounts(prov)
		m.clearAccountAlerts(ctx, id)
		m.EmitData(ctx, p.Actor(), "", "", "provider_account.deleted", prov+" "+a.Label+" deleted", nil, map[string]any{"provider": prov, "account_id": id, "key_fingerprint": a.KeyFingerprint})
		return nil
	}
	old := a.Status
	a.Status, a.StatusReason, a.Enabled, a.UpdatedBy = model.AccountArchived, strings.TrimSpace("archived by "+p.Actor()+" "+reason), false, p.Actor()
	m.setAccountStatus(ctx, a, old, time.Now().UTC())
	if err := m.putAccount(ctx, a); err != nil {
		return err
	}
	m.clearAccountAlerts(ctx, id)
	m.EmitData(ctx, p.Actor(), "", "", "provider_account.archived", fmt.Sprintf("%s %s archived (%d live hosts) %s", prov, a.Label, live, reason), nil,
		map[string]any{"provider": prov, "account_id": id, "live_hosts": live, "force": force, "reason": reason, "key_fingerprint": a.KeyFingerprint})
	return nil
}

// AccountTimeline is GET …/accounts/{a}/timeline (audit events of the account).
func (m *Manager) AccountTimeline(ctx context.Context, p *Principal, prov, id string, limit int) ([]*model.Event, error) {
	if err := p.Require(model.ScopeAdmin); err != nil {
		return nil, err
	}
	if _, err := m.getAccount(ctx, prov, id); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	evs, err := m.Store.ListEvents(ctx, store.EventFilter{Type: "provider_account.", Limit: 1000})
	if err != nil {
		return nil, err
	}
	out := []*model.Event{}
	for _, e := range evs {
		if e.Data == nil {
			continue
		}
		if e.Data["account_id"] == id || e.Data["from"] == id || e.Data["to"] == id {
			out = append(out, e)
			if len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

// ---- strategy & provider overview ----

func (m *Manager) strategyView(ctx context.Context, prov string) StrategyView {
	pp := m.ProviderPrefs(ctx, prov)
	return StrategyView{Provider: prov, Strategy: pp.Strategy, EffectiveStrategy: m.effectiveStrategy(ctx, prov), StickyMinutes: pp.StickyMinutes,
		FailoverAcrossAccounts: failoverAcross(pp), Strategies: model.Strategies}
}

// GetProviderStrategy is GET /v1/admin/providers/{p}/strategy.
func (m *Manager) GetProviderStrategy(ctx context.Context, p *Principal, prov string) (*StrategyView, error) {
	if err := p.Require(model.ScopeAdmin); err != nil {
		return nil, err
	}
	if err := m.requireProvider(prov); err != nil {
		return nil, err
	}
	v := m.strategyView(ctx, prov)
	return &v, nil
}

// PutProviderStrategy is PUT /v1/admin/providers/{p}/strategy.
func (m *Manager) PutProviderStrategy(ctx context.Context, p *Principal, prov string, in StrategyInput) (*StrategyView, error) {
	if err := p.Require(model.ScopeAdmin); err != nil {
		return nil, err
	}
	if err := m.requireProvider(prov); err != nil {
		return nil, err
	}
	if in.Strategy != "" && !contains(model.Strategies, in.Strategy) {
		return nil, model.EH(model.ErrInvalid, "Use priority, weighted, round_robin, least_used or drain_first.", "unknown strategy %q", in.Strategy)
	}
	if in.StickyMinutes < 0 || in.StickyMinutes > 24*60 {
		return nil, model.E(model.ErrInvalid, "sticky_minutes must be 0..1440")
	}
	pp := m.ProviderPrefs(ctx, prov)
	pp.Strategy, pp.StickyMinutes = in.Strategy, in.StickyMinutes
	if in.FailoverAcrossAccounts != nil {
		v := *in.FailoverAcrossAccounts
		pp.FailoverAcrossAccounts = &v
	}
	pp.UpdatedAt = time.Now().UTC()
	if err := m.Store.PutKV(ctx, prefsKey(prov), pp); err != nil {
		return nil, err
	}
	v := m.strategyView(ctx, prov)
	m.EmitData(ctx, p.Actor(), "", "", "provider.strategy_updated", fmt.Sprintf("%s: strategy %s, sticky %d min, failover across accounts %v", prov, v.EffectiveStrategy, v.StickyMinutes, v.FailoverAcrossAccounts), nil,
		map[string]any{"provider": prov, "strategy": v.Strategy, "sticky_minutes": v.StickyMinutes, "failover_across_accounts": v.FailoverAcrossAccounts})
	return &v, nil
}

func (m *Manager) providerRow(ctx context.Context, pid string, hosts []*model.Host, alerts []*InfraAlert) (*AdminProviderRow, []*AccountView) {
	pv, _ := m.Providers.Get(pid)
	row := &AdminProviderRow{ID: pid, Name: pv.Name(), Tier: pv.Tier(), Group: "vps", Implemented: pv.Implemented(), Strategy: m.effectiveStrategy(ctx, pid),
		Health: m.Health.Stats(pid, "").Status, BalanceByCur: map[string]float64{}}
	if _, ok := m.sandboxProv(pid); ok {
		row.Group = "sandbox"
	}
	if mp, err := m.Store.GetProvider(ctx, pid); err == nil {
		row.Connected = m.provConnected(ctx, mp)
	}
	var views []*AccountView
	balUSD, haveBal := 0.0, false
	burn := 0.0
	for _, a := range m.listAccounts(ctx, pid) {
		if a.Status == model.AccountArchived {
			continue
		}
		v := m.viewAccount(ctx, a, hosts)
		views = append(views, v)
		st := v.Stats
		row.AccountsTotal++
		if st.Eligible {
			row.AccountsHealthy++
			burn += st.BurnPerHourUSD
			if st.BalanceEffectiveUSD != nil {
				balUSD += math0(*st.BalanceEffectiveUSD - a.MinBalance*fxRate(a))
				haveBal = true
			}
		} else if a.Enabled {
			row.AccountsDegraded++
		}
		row.HostsLive += st.HostsLive
		row.BoxesLive += st.BoxesLive
		row.SpendTodayUSD += st.SpendTodayUSD
		row.SpendMonthUSD += st.SpendMonthUSD
		row.ForecastMonthUSD += st.ForecastMonthEnd * fxRate(a)
		if st.BalanceEffective != nil {
			row.BalanceByCur[a.Currency] = r4(row.BalanceByCur[a.Currency] + *st.BalanceEffective)
			b := 0.0
			if row.BalanceUSD != nil {
				b = *row.BalanceUSD
			}
			b = r4(b + *st.BalanceEffectiveUSD)
			row.BalanceUSD = &b
		}
		used := float64(st.HostsLive)
		for k, l := range a.Limits {
			u := l.Used
			if k == "servers" || k == "concurrent_sandboxes" {
				u = used
				if k == "concurrent_sandboxes" && st.SandboxStats {
					u = float64(st.GroupRunning + st.InFlight)
				}
			} else if k == "vcpu" {
				u = st.VCPULive
			}
			if l.Limit > 0 && 100*u/l.Limit > row.MaxLimitPct {
				row.MaxLimitPct = r4(100 * u / l.Limit)
			}
		}
	}
	if haveBal && burn > 0 {
		d := r4(balUSD / burn / 24)
		row.RunwayDays = &d
	}
	row.SpendTodayUSD, row.SpendMonthUSD, row.ForecastMonthUSD = r4(row.SpendTodayUSD), r4(row.SpendMonthUSD), r4(row.ForecastMonthUSD)
	for _, al := range alerts {
		if al.Provider == pid && al.Status != "closed" {
			row.OpenAlerts++
		}
	}
	if len(row.BalanceByCur) == 0 {
		row.BalanceByCur = nil
	}
	return row, views
}

func math0(v float64) float64 {
	if v < 0 {
		return 0
	}
	return v
}

// AdminProviders is GET /v1/admin/providers: KPI strip + one row per provider.
func (m *Manager) AdminProviders(ctx context.Context, p *Principal) (*AdminProvidersView, error) {
	if err := p.Require(model.ScopeAdmin); err != nil {
		return nil, err
	}
	hosts, _ := m.Store.ListHosts(ctx)
	alerts, _ := m.ListInfraAlerts(ctx, p, "")
	out := &AdminProvidersView{Items: []*AdminProviderRow{}, Summary: InfraSummary{BalanceByCur: map[string]float64{}, OpenAlerts: len(alerts)}}
	for _, pv := range m.Providers.All() {
		if pv.ID() == "local" {
			continue
		}
		row, views := m.providerRow(ctx, pv.ID(), hosts, alerts)
		out.Items = append(out.Items, row)
		s := &out.Summary
		s.SpendTodayUSD += row.SpendTodayUSD
		s.SpendMonthUSD += row.SpendMonthUSD
		s.ForecastMonthUSD += row.ForecastMonthUSD
		if row.BalanceUSD != nil {
			s.BalanceUSD += *row.BalanceUSD
		}
		for c, v := range row.BalanceByCur {
			s.BalanceByCur[c] = r4(s.BalanceByCur[c] + v)
		}
		for _, v := range views {
			st := v.Stats
			switch {
			case st.Eligible:
				s.AccountsHealthy++
			case v.Status == model.AccountAuthFailed || v.Status == model.AccountOutOfCredit || !st.HasCredential:
				s.AccountsBroken++
			case v.Enabled:
				s.AccountsDegraded++
			}
			if st.RunwayDays != nil && v.Enabled && (s.ShortestRunway == nil || *st.RunwayDays < s.ShortestRunway.Days) {
				s.ShortestRunway = &RunwayRef{Provider: v.Provider, AccountID: v.ID, Label: v.Label, Days: *st.RunwayDays}
			}
		}
	}
	s := &out.Summary
	s.BalanceUSD, s.SpendTodayUSD, s.SpendMonthUSD, s.ForecastMonthUSD = r4(s.BalanceUSD), r4(s.SpendTodayUSD), r4(s.SpendMonthUSD), r4(s.ForecastMonthUSD)
	return out, nil
}

// AdminProvider is GET /v1/admin/providers/{p}.
func (m *Manager) AdminProvider(ctx context.Context, p *Principal, prov string) (*AdminProviderDetail, error) {
	if err := p.Require(model.ScopeAdmin); err != nil {
		return nil, err
	}
	if err := m.requireProvider(prov); err != nil {
		return nil, err
	}
	hosts, _ := m.Store.ListHosts(ctx)
	alerts, _ := m.ListInfraAlerts(ctx, p, "")
	row, views := m.providerRow(ctx, prov, hosts, alerts)
	d := &AdminProviderDetail{AdminProviderRow: *row, Prefs: m.ProviderPrefs(ctx, prov), Strategy: m.strategyView(ctx, prov), Accounts: views, Alerts: []*InfraAlert{}}
	if d.Accounts == nil {
		d.Accounts = []*AccountView{}
	}
	for _, al := range alerts {
		if al.Provider == prov {
			d.Alerts = append(d.Alerts, al)
		}
	}
	if caps, err := m.Store.GetCapabilities(ctx, prov); err == nil && len(caps) > 0 {
		d.Caps = caps
	}
	return d, nil
}
