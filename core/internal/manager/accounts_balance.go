package manager

// v2.3 (M29 phase B): real balance reading + balance history.
//
// Providers implementing provider.BalanceReader (Runpod first, owner
// decision 2026-11-27) are polled every BOXMES_ACCOUNT_POLL_BALANCE (15m)
// with the account's read-only billing key when one is set, else its main
// key. A reading replaces the account's balance (balance_source=api) and its
// provider-reported burn rate feeds runway, so the existing runway alerts
// (7 / 2 days) and the automatic drain under 2 days apply to real money.
// Hosts are never deleted.
//
// Fail-soft: a failed read only marks the balance stale (balance_stale,
// balance_error) and keeps the last good reading; it never changes the
// account status, its breaker or failover.
//
// Every poll writes one provider_account_snapshots row (accounts without a
// reader get one per interval too, from their manual / estimated balance and
// ledger spend) for the balance / spend chart and runway trend; rows older
// than BOXMES_ACCOUNT_SNAPSHOT_DAYS (90) are pruned once a day.

import (
	"context"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"boxmes/core/internal/model"
	"boxmes/core/internal/provider"
)

const (
	defaultBalancePoll    = 15 * time.Minute
	minBalancePoll        = time.Minute
	defaultSnapshotDays   = 90
	balanceReadTimeout    = 20 * time.Second
	balanceReadConcurrent = 4
	maxHistoryDays        = 90
)

// balancePollInterval is BOXMES_ACCOUNT_POLL_BALANCE (Go duration or
// minutes), default 15m, at least 1m.
func balancePollInterval() time.Duration {
	v := strings.TrimSpace(os.Getenv("BOXMES_ACCOUNT_POLL_BALANCE"))
	if v == "" {
		return defaultBalancePoll
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		if n, err2 := strconv.Atoi(v); err2 == nil {
			d = time.Duration(n) * time.Minute
		} else {
			return defaultBalancePoll
		}
	}
	if d < minBalancePoll {
		d = minBalancePoll
	}
	return d
}

func snapshotRetention() time.Duration {
	if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv("BOXMES_ACCOUNT_SNAPSHOT_DAYS"))); err == nil && n > 0 {
		return time.Duration(n) * 24 * time.Hour
	}
	return defaultSnapshotDays * 24 * time.Hour
}

// balanceReader is the provider's BalanceReader, if it has one.
func (m *Manager) balanceReader(prov string) (provider.BalanceReader, bool) {
	pv, ok := m.Providers.Get(prov)
	if !ok {
		return nil, false
	}
	br, ok := pv.(provider.BalanceReader)
	return br, ok
}

// balanceCredential is the read-only billing key when set, else the main key.
func (m *Manager) balanceCredential(ctx context.Context, a *model.ProviderAccount) (string, bool, error) {
	if a.BillingCredentialEnc != "" {
		tok, err := m.Crypt.Open(a.BillingCredentialEnc)
		if err == nil && tok != "" {
			return tok, true, nil
		}
	}
	tok, err := m.credentialFor(ctx, a)
	return tok, false, err
}

// balanceDue: the account's provider reads balances and the last attempt
// is older than the poll interval.
func balanceDue(a *model.ProviderAccount, every time.Duration, now time.Time) bool {
	return a.BalancePolledAt == nil || now.Sub(*a.BalancePolledAt) >= every
}

// pollBalances reads every due account of providers with a BalanceReader.
// Returns the ids of the accounts read this tick (ok or not).
func (m *Manager) pollBalances(ctx context.Context, now time.Time) map[string]bool {
	every := balancePollInterval()
	polled := map[string]bool{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, balanceReadConcurrent)
	for _, p := range m.Providers.All() {
		br, ok := m.balanceReader(p.ID())
		if !ok {
			continue
		}
		for _, a := range m.listAccounts(ctx, p.ID()) {
			if a.Status == model.AccountArchived || !balanceDue(a, every, now) || !m.hasCredential(ctx, a) && a.BillingCredentialEnc == "" {
				continue
			}
			wg.Add(1)
			sem <- struct{}{}
			go func(a *model.ProviderAccount) {
				defer wg.Done()
				defer func() { <-sem }()
				m.readBalance(ctx, br, a, now)
				mu.Lock()
				polled[a.ID] = true
				mu.Unlock()
			}(a)
		}
	}
	wg.Wait()
	return polled
}

// readBalance reads one account and stores the outcome (fail-soft).
func (m *Manager) readBalance(ctx context.Context, br provider.BalanceReader, a *model.ProviderAccount, now time.Time) error {
	tok, billingKey, err := m.balanceCredential(ctx, a)
	var b provider.Balance
	if err == nil {
		c, cancel := context.WithTimeout(ctx, balanceReadTimeout)
		b, err = br.ReadBalance(c, tok)
		cancel()
	}
	if err == nil && a.ExternalID != "" && b.ExternalID != "" && b.ExternalID != a.ExternalID {
		which := "key"
		if billingKey {
			which = "billing key"
		}
		err = fmt.Errorf("the %s reads provider account %s, not this account's %s", which, b.ExternalID, a.ExternalID)
	}
	l := m.acctLock(a.ID)
	l.Lock()
	cur, gerr := m.Store.GetProviderAccount(ctx, a.ID)
	if gerr != nil {
		l.Unlock()
		return gerr
	}
	normAccount(cur)
	wasStale, oldSource := cur.BalanceStale, cur.BalanceSource
	var old *float64
	if cur.Balance != nil {
		v := *cur.Balance
		old = &v
	}
	cur.BalancePolledAt = &now
	if err != nil {
		msg := err.Error()
		if ae, ok := err.(*model.APIError); ok {
			msg = ae.Message
		}
		cur.BalanceStale, cur.BalanceError, cur.BalanceErrorAt = true, msg, &now
	} else {
		at := now
		cur.Balance, cur.BalanceSource, cur.BalanceAt = b.Balance, "api", &at
		if b.Currency != "" {
			cur.Currency = strings.ToUpper(b.Currency)
		}
		cur.SpendPerHour = b.SpendPerHour
		if b.PendingCharges != nil {
			cur.PendingCharges = b.PendingCharges
		}
		if b.MonthToDate != nil {
			cur.MonthToDate = b.MonthToDate
		}
		if b.SpendLimitPerHour != nil {
			used := 0.0
			if b.SpendPerHour != nil {
				used = *b.SpendPerHour
			}
			cur.Limits["spend_per_hour"] = model.AccountLimit{Limit: *b.SpendLimitPerHour, Used: used, Source: "api", At: &at}
		}
		cur.BalanceStale, cur.BalanceError, cur.BalanceErrorAt = false, "", nil
		if cur.BillingModel == "" {
			cur.BillingModel = "prepaid"
		}
	}
	perr := m.putAccount(ctx, cur)
	l.Unlock()
	if perr != nil {
		return perr
	}
	switch {
	case err != nil && !wasStale:
		m.EmitData(ctx, "system", "", "", "provider_account.balance_read_failed", fmt.Sprintf("%s %s: balance read failed (kept the last reading): %s", cur.Provider, cur.Label, cur.BalanceError), nil,
			map[string]any{"provider": cur.Provider, "account_id": cur.ID, "error": cur.BalanceError, "billing_key": billingKey})
	case err == nil && (oldSource != "api" || wasStale):
		data := map[string]any{"provider": cur.Provider, "account_id": cur.ID, "source": "api", "new": cur.Balance, "billing_key": billingKey}
		if old != nil {
			data["old"] = *old
		}
		m.EmitData(ctx, "system", "", "", "provider_account.balance_updated", fmt.Sprintf("%s %s: balance read from the provider API", cur.Provider, cur.Label), nil, data)
	}
	return err
}

// writeSnapshot stores the account's money at now (after stats).
func (m *Manager) writeSnapshot(ctx context.Context, a *model.ProviderAccount, st *model.AccountStats, now time.Time) {
	src := a.BalanceSource
	if a.Balance == nil && a.CreditRemaining != nil {
		src = "estimated"
	}
	if st.BalanceEffective == nil {
		src = "none"
	} else if src == "" {
		src = "estimated"
	}
	sn := &model.AccountSnapshot{AccountID: a.ID, Provider: a.Provider, TS: now.UTC().Truncate(time.Millisecond), Source: src, Currency: a.Currency,
		Balance: a.Balance, BalanceEffective: st.BalanceEffective, BalanceUSD: st.BalanceEffectiveUSD, SpendPerHour: a.SpendPerHour,
		BurnPerHourUSD: st.BurnPerHourUSD, RunwayDays: st.RunwayDays, SpendTodayUSD: st.SpendTodayUSD, SpendMonthUSD: st.SpendMonthUSD}
	if l, ok := a.Limits["spend_per_hour"]; ok && l.Source == "api" && l.Limit > 0 {
		v := l.Limit
		sn.SpendLimitPerHour = &v
	}
	if a.BalanceStale {
		sn.Stale, sn.Error = true, a.BalanceError
	}
	if err := m.Store.PutAccountSnapshot(ctx, sn); err == nil {
		m.accts.mu.Lock()
		m.accts.init()
		m.accts.snapAt[a.ID] = now
		m.accts.mu.Unlock()
	}
}

// snapshotDue: accounts polled this tick always; others once per interval
// when they have money or spend to show.
func (m *Manager) snapshotDue(a *model.ProviderAccount, st *model.AccountStats, polled bool, now time.Time) bool {
	if polled {
		return true
	}
	if _, ok := m.balanceReader(a.Provider); ok && m.hasCredentialCached(a) {
		return false // written when polled
	}
	if st.BalanceEffective == nil && st.SpendMonthUSD == 0 && st.BurnPerHourUSD == 0 {
		return false
	}
	m.accts.mu.Lock()
	m.accts.init()
	last, ok := m.accts.snapAt[a.ID]
	m.accts.mu.Unlock()
	return !ok || now.Sub(last) >= balancePollInterval()
}

// hasCredentialCached avoids a provider row read for accounts with their
// own sealed key (legacy provider: refs fall back to a store read).
func (m *Manager) hasCredentialCached(a *model.ProviderAccount) bool {
	if a.CredentialEnc != "" || a.BillingCredentialEnc != "" {
		return true
	}
	return m.hasCredential(context.Background(), a)
}

// pruneSnapshots runs at most once a day.
func (m *Manager) pruneSnapshots(ctx context.Context, now time.Time) {
	m.accts.mu.Lock()
	m.accts.init()
	due := now.Sub(m.accts.prunedAt) >= 24*time.Hour
	if due {
		m.accts.prunedAt = now
	}
	m.accts.mu.Unlock()
	if due {
		m.Store.PruneAccountSnapshots(ctx, now.Add(-snapshotRetention()))
	}
}

// ---- history API ----

// BalancePoint is one point of an account's balance history.
type BalancePoint = model.AccountSnapshot

// BalanceHistory is GET …/accounts/{a}/balance/history.
type BalanceHistory struct {
	Provider     string          `json:"provider"`
	AccountID    string          `json:"account_id"`
	Label        string          `json:"label"`
	Currency     string          `json:"currency"`
	Days         int             `json:"days"`
	IntervalS    float64         `json:"interval_s"`  // poll interval
	Source       string          `json:"source"`      // api (polled) | manual | estimated | none
	BalanceAPI   bool            `json:"balance_api"` // the provider reports balances
	Stale        bool            `json:"stale"`       // the last read failed
	BalanceError string          `json:"balance_error,omitempty"`
	BalanceAt    *time.Time      `json:"balance_at,omitempty"`
	PolledAt     *time.Time      `json:"polled_at,omitempty"`
	Points       []*BalancePoint `json:"points"`
	// Trend: runway change per day over the window (negative = shrinking),
	// from a least-squares fit of runway_days; nil with < 2 points.
	RunwayTrendPerDay *float64 `json:"runway_trend_per_day,omitempty"`
	// Spent is the money used over the window by the balance readings (sum
	// of the drops between points); TopUps the sum of the rises.
	Spent  *float64 `json:"spent,omitempty"`
	TopUps *float64 `json:"top_ups,omitempty"`
}

// ProviderBalancePoint is one bucket of the provider-level aggregate.
type ProviderBalancePoint struct {
	TS             time.Time `json:"ts"`
	BalanceUSD     *float64  `json:"balance_usd,omitempty"` // sum of the accounts' last known effective balance (USD)
	BurnPerHourUSD float64   `json:"burn_per_hour_usd"`
	RunwayDays     *float64  `json:"runway_days,omitempty"` // balance_usd / burn
	SpendTodayUSD  float64   `json:"spend_today_usd"`
	Accounts       int       `json:"accounts"`       // accounts with a value in this bucket (carried forward)
	StaleAccounts  int       `json:"stale_accounts"` // of those, last read failed
}

// ProviderBalanceHistory is GET /v1/admin/providers/{p}/balance/history.
type ProviderBalanceHistory struct {
	Provider          string                  `json:"provider"`
	Days              int                     `json:"days"`
	BucketS           float64                 `json:"bucket_s"`
	Accounts          []BalanceHistoryAccount `json:"accounts"`
	Points            []*ProviderBalancePoint `json:"points"`
	RunwayTrendPerDay *float64                `json:"runway_trend_per_day,omitempty"`
}

// BalanceHistoryAccount lists an account in the aggregate.
type BalanceHistoryAccount struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Source string `json:"source"`
	Stale  bool   `json:"stale"`
}

func historyDays(days int) int {
	if days <= 0 {
		return 7
	}
	if days > maxHistoryDays {
		return maxHistoryDays
	}
	return days
}

// slopePerDay fits y over x (hours) and returns the slope per day.
func slopePerDay(xs []time.Time, ys []float64) *float64 {
	if len(xs) < 2 {
		return nil
	}
	t0 := xs[0]
	n := float64(len(xs))
	var sx, sy, sxx, sxy float64
	for i := range xs {
		x := xs[i].Sub(t0).Hours() / 24
		sx, sy, sxx, sxy = sx+x, sy+ys[i], sxx+x*x, sxy+x*ys[i]
	}
	den := n*sxx - sx*sx
	if den == 0 {
		return nil
	}
	v := math.Round((n*sxy-sx*sy)/den*100) / 100
	return &v
}

// AccountBalanceHistory is GET …/accounts/{a}/balance/history?days=N.
func (m *Manager) AccountBalanceHistory(ctx context.Context, p *Principal, prov, id string, days int) (*BalanceHistory, error) {
	if err := p.Require(model.ScopeAdmin); err != nil {
		return nil, err
	}
	a, err := m.getAccount(ctx, prov, id)
	if err != nil {
		return nil, err
	}
	days = historyDays(days)
	now := time.Now().UTC()
	pts, err := m.Store.ListAccountSnapshots(ctx, []string{a.ID}, now.Add(-time.Duration(days)*24*time.Hour), time.Time{})
	if err != nil {
		return nil, err
	}
	_, api := m.balanceReader(prov)
	src := a.BalanceSource
	if src == "" {
		src = "none"
	}
	h := &BalanceHistory{Provider: prov, AccountID: a.ID, Label: a.Label, Currency: a.Currency, Days: days, IntervalS: balancePollInterval().Seconds(),
		Source: src, BalanceAPI: api, Stale: a.BalanceStale, BalanceError: a.BalanceError, BalanceAt: a.BalanceAt, PolledAt: a.BalancePolledAt, Points: pts}
	var xs []time.Time
	var ys []float64
	var last *float64
	drop, rise, n := 0.0, 0.0, 0
	for _, pt := range pts {
		if pt.RunwayDays != nil {
			xs, ys = append(xs, pt.TS), append(ys, *pt.RunwayDays)
		}
		if pt.Balance != nil && !pt.Stale {
			v := *pt.Balance
			if last != nil {
				if v > *last {
					rise += v - *last
				} else {
					drop += *last - v
				}
			}
			last = &v
			n++
		}
	}
	h.RunwayTrendPerDay = slopePerDay(xs, ys)
	if n >= 2 {
		d, r := r4(drop), r4(rise)
		h.Spent, h.TopUps = &d, &r
	}
	return h, nil
}

// ProviderBalanceHistoryView is GET /v1/admin/providers/{p}/balance/history:
// the accounts' snapshots bucketed (1 h up to 7 days, 3 h up to 30, 6 h
// beyond), each account carried forward from its last point.
func (m *Manager) ProviderBalanceHistoryView(ctx context.Context, p *Principal, prov string, days int) (*ProviderBalanceHistory, error) {
	if err := p.Require(model.ScopeAdmin); err != nil {
		return nil, err
	}
	if err := m.requireProvider(prov); err != nil {
		return nil, err
	}
	days = historyDays(days)
	bucket := time.Hour
	switch {
	case days > 30:
		bucket = 6 * time.Hour
	case days > 7:
		bucket = 3 * time.Hour
	}
	now := time.Now().UTC()
	from := now.Add(-time.Duration(days) * 24 * time.Hour).Truncate(bucket)
	out := &ProviderBalanceHistory{Provider: prov, Days: days, BucketS: bucket.Seconds(), Accounts: []BalanceHistoryAccount{}, Points: []*ProviderBalancePoint{}}
	var ids []string
	for _, a := range m.listAccounts(ctx, prov) {
		if a.Status == model.AccountArchived {
			continue
		}
		ids = append(ids, a.ID)
		src := a.BalanceSource
		if src == "" {
			src = "none"
		}
		out.Accounts = append(out.Accounts, BalanceHistoryAccount{ID: a.ID, Label: a.Label, Source: src, Stale: a.BalanceStale})
	}
	sort.SliceStable(out.Accounts, func(i, j int) bool { return out.Accounts[i].ID < out.Accounts[j].ID })
	pts, err := m.Store.ListAccountSnapshots(ctx, ids, from, time.Time{})
	if err != nil {
		return nil, err
	}
	lastBy := map[string]*model.AccountSnapshot{}
	i := 0
	var xs []time.Time
	var ys []float64
	for t := from; !t.After(now); t = t.Add(bucket) {
		end := t.Add(bucket)
		for i < len(pts) && pts[i].TS.Before(end) {
			lastBy[pts[i].AccountID] = pts[i]
			i++
		}
		if len(lastBy) == 0 {
			continue
		}
		pt := &ProviderBalancePoint{TS: t}
		bal, haveBal := 0.0, false
		for _, s := range lastBy {
			pt.Accounts++
			if s.Stale {
				pt.StaleAccounts++
			}
			if s.BalanceUSD != nil {
				bal += math.Max(0, *s.BalanceUSD)
				haveBal = true
			}
			pt.BurnPerHourUSD += s.BurnPerHourUSD
			pt.SpendTodayUSD += s.SpendTodayUSD
		}
		pt.BurnPerHourUSD, pt.SpendTodayUSD = r4(pt.BurnPerHourUSD), r4(pt.SpendTodayUSD)
		if haveBal {
			b := r4(bal)
			pt.BalanceUSD = &b
			if pt.BurnPerHourUSD > 0 {
				d := math.Round(bal/pt.BurnPerHourUSD/24*100) / 100
				pt.RunwayDays = &d
				xs, ys = append(xs, t), append(ys, d)
			}
		}
		out.Points = append(out.Points, pt)
	}
	out.RunwayTrendPerDay = slopePerDay(xs, ys)
	return out, nil
}

// PollAccountBalanceNow reads one account's balance now (POST …/test and
// tests). Providers without a reader are a no-op.
func (m *Manager) PollAccountBalanceNow(ctx context.Context, prov, id string) error {
	br, ok := m.balanceReader(prov)
	if !ok {
		return nil
	}
	a, err := m.getAccount(ctx, prov, id)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	rerr := m.readBalance(ctx, br, a, now)
	if a, err = m.getAccount(ctx, prov, id); err == nil {
		st := m.computeStats(ctx, a, nil, nil, now)
		m.writeSnapshot(ctx, a, st, now)
	}
	return rerr
}
