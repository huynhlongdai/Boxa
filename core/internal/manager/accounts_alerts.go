package manager

// v2.2 infra alerts (spec §6, owner decisions 2026-11-27): one open alert
// per (type, account|host|provider) key; only the highest level crossed is
// kept; a critical alert goes to Telegram at once (notify event
// infra_alert) and is repeated at most once per 24 h while unacknowledged
// or when it escalates; warn/info alerts go into a daily email digest
// (notify event infra_digest). An alert closes by itself when its condition
// clears.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"boxmes/core/internal/i18n"
	"boxmes/core/internal/model"
)

const kvInfraAlerts = "infra_alerts"

// InfraAlert is one row of GET /v1/admin/infra/alerts.
type InfraAlert struct {
	ID         string         `json:"id"` // dedupe key: <type>|<account|host|provider>
	Type       string         `json:"type"`
	Level      string         `json:"level"`  // info | warn | critical
	Status     string         `json:"status"` // open | acked | closed
	Provider   string         `json:"provider,omitempty"`
	AccountID  string         `json:"account_id,omitempty"`
	HostID     string         `json:"host_id,omitempty"`
	Message    string         `json:"message"` // English; Type + Data let clients localize
	Data       map[string]any `json:"data,omitempty"`
	Count      int            `json:"count"` // times the condition was seen while open
	OpenedAt   time.Time      `json:"opened_at"`
	UpdatedAt  time.Time      `json:"updated_at"`
	ClosedAt   *time.Time     `json:"closed_at,omitempty"`
	AckedAt    *time.Time     `json:"acked_at,omitempty"`
	AckedBy    string         `json:"acked_by,omitempty"`
	NotifiedAt *time.Time     `json:"notified_at,omitempty"`
	Notified   string         `json:"notified_level,omitempty"`
}

type alertInput struct {
	Key, Type, Level, Provider, AccountID, HostID string
	Args                                          []any
}

var levelRank = map[string]int{"info": 1, "warn": 2, "critical": 3}

func (m *Manager) loadAlerts(ctx context.Context) map[string]*InfraAlert {
	out := map[string]*InfraAlert{}
	_ = m.Store.GetKV(ctx, kvInfraAlerts, &out)
	return out
}

func (m *Manager) saveAlerts(ctx context.Context, al map[string]*InfraAlert) {
	// keep closed alerts for 30 days
	cut := time.Now().Add(-30 * 24 * time.Hour)
	for k, a := range al {
		if a.Status == "closed" && a.ClosedAt != nil && a.ClosedAt.Before(cut) {
			delete(al, k)
		}
	}
	_ = m.Store.PutKV(ctx, kvInfraAlerts, al)
}

func alertText(locale, typ string, args []any) string {
	return i18n.T(locale, "infra.alert."+typ, args...)
}

// raiseAlert opens or updates an alert (dedupe by key).
func (m *Manager) raiseAlert(ctx context.Context, in alertInput) {
	if in.Key == "" {
		subj := in.AccountID
		if in.HostID != "" {
			subj = in.HostID
		}
		if in.Type == "failover" || subj == "" {
			subj = in.Provider
		}
		in.Key = in.Type + "|" + subj
	}
	now := time.Now().UTC()
	m.accts.alertMu.Lock()
	al := m.loadAlerts(ctx)
	a := al[in.Key]
	opened, escalate := false, false
	if a == nil || a.Status == "closed" {
		a = &InfraAlert{ID: in.Key, Type: in.Type, Status: "open", OpenedAt: now}
		al[in.Key] = a
		opened = true
	}
	if levelRank[in.Level] > levelRank[a.Level] {
		escalate = !opened
		if a.Status == "acked" {
			a.Status = "open"
		}
	}
	if opened || escalate || levelRank[in.Level] < levelRank[a.Level] {
		a.Level = in.Level // only the highest crossed level is kept; it moves down when the figure improves
	}
	a.Provider, a.AccountID, a.HostID = in.Provider, in.AccountID, in.HostID
	a.Message = alertText("en", in.Type, in.Args)
	a.Data = map[string]any{"args": in.Args}
	a.Count++
	a.UpdatedAt = now
	notify := a.Level == "critical" && a.Status == "open" && (opened || escalate || a.NotifiedAt == nil || now.Sub(*a.NotifiedAt) >= 24*time.Hour || a.Notified != a.Level)
	if notify {
		a.NotifiedAt, a.Notified = &now, a.Level
	}
	snapshot := *a
	m.saveAlerts(ctx, al)
	m.accts.alertMu.Unlock()
	if opened || escalate {
		m.EmitData(ctx, "system", "", "", "provider_account.alert_opened", fmt.Sprintf("[%s] %s", snapshot.Level, snapshot.Message), nil,
			map[string]any{"alert_id": snapshot.ID, "type": snapshot.Type, "level": snapshot.Level, "provider": snapshot.Provider, "account_id": snapshot.AccountID, "host_id": snapshot.HostID})
	}
	if notify {
		m.notifyInfraCritical(snapshot, in.Args)
	}
}

func (m *Manager) clearAlert(ctx context.Context, key string) {
	m.accts.alertMu.Lock()
	al := m.loadAlerts(ctx)
	a := al[key]
	if a == nil || a.Status == "closed" {
		m.accts.alertMu.Unlock()
		return
	}
	now := time.Now().UTC()
	a.Status, a.ClosedAt, a.UpdatedAt = "closed", &now, now
	snap := *a
	m.saveAlerts(ctx, al)
	m.accts.alertMu.Unlock()
	m.EmitData(ctx, "system", "", "", "provider_account.alert_closed", "resolved: "+snap.Message, nil,
		map[string]any{"alert_id": snap.ID, "type": snap.Type, "provider": snap.Provider, "account_id": snap.AccountID, "host_id": snap.HostID})
}

// expireAlert closes an alert not seen for d (failover notices).
func (m *Manager) expireAlert(ctx context.Context, key string, d time.Duration) {
	m.accts.alertMu.Lock()
	al := m.loadAlerts(ctx)
	a := al[key]
	stale := a != nil && a.Status != "closed" && time.Since(a.UpdatedAt) > d
	m.accts.alertMu.Unlock()
	if stale {
		m.clearAlert(ctx, key)
	}
}

func (m *Manager) clearAccountAlerts(ctx context.Context, accountID string) {
	m.accts.alertMu.Lock()
	al := m.loadAlerts(ctx)
	var keys []string
	for k, a := range al {
		if a.AccountID == accountID && a.Status != "closed" && a.Type != "orphan_host" {
			keys = append(keys, k)
		}
	}
	m.accts.alertMu.Unlock()
	for _, k := range keys {
		m.clearAlert(ctx, k)
	}
}

// expiryAlert raises warn/critical while t is within warn/crit days (or past).
func (m *Manager) expiryAlert(ctx context.Context, key, typ, prov, acct, host string, t time.Time, warn, crit float64, now time.Time, label string) {
	days := t.Sub(now).Hours() / 24
	switch {
	case days < crit:
		m.raiseAlert(ctx, alertInput{Key: key, Type: typ, Level: "critical", Provider: prov, AccountID: acct, HostID: host, Args: []any{prov, label, t.UTC().Format("2006-01-02"), days}})
	case days < warn:
		m.raiseAlert(ctx, alertInput{Key: key, Type: typ, Level: "warn", Provider: prov, AccountID: acct, HostID: host, Args: []any{prov, label, t.UTC().Format("2006-01-02"), days}})
	default:
		m.clearAlert(ctx, key)
	}
}

func pctLevel(pct, warn, crit float64) string {
	switch {
	case pct >= crit:
		return "critical"
	case pct >= warn:
		return "warn"
	}
	return ""
}

// evaluateAlerts checks one account against the thresholds.
func (m *Manager) evaluateAlerts(ctx context.Context, a *model.ProviderAccount, st *model.AccountStats, now time.Time) {
	th := accountThresholds(a)
	k := func(t string) string { return t + "|" + a.ID }
	// low balance / runway
	switch {
	case st.RunwayDays != nil && *st.RunwayDays < th.runwayCrit:
		m.raiseAlert(ctx, alertInput{Key: k("low_balance"), Type: "low_balance", Level: "critical", Provider: a.Provider, AccountID: a.ID, Args: []any{a.Provider, a.Label, *st.RunwayDays, *st.BalanceEffective, a.Currency}})
	case st.RunwayDays != nil && *st.RunwayDays < th.runwayWarn:
		m.raiseAlert(ctx, alertInput{Key: k("low_balance"), Type: "low_balance", Level: "warn", Provider: a.Provider, AccountID: a.ID, Args: []any{a.Provider, a.Label, *st.RunwayDays, *st.BalanceEffective, a.Currency}})
	case st.BalanceEffective != nil && a.LowBalanceWarn > 0 && *st.BalanceEffective < a.LowBalanceWarn:
		days := -1.0
		if st.RunwayDays != nil {
			days = *st.RunwayDays
		}
		m.raiseAlert(ctx, alertInput{Key: k("low_balance"), Type: "low_balance", Level: "warn", Provider: a.Provider, AccountID: a.ID, Args: []any{a.Provider, a.Label, days, *st.BalanceEffective, a.Currency}})
	default:
		m.clearAlert(ctx, k("low_balance"))
	}
	if a.Status != model.AccountOutOfCredit {
		m.clearAlert(ctx, k("out_of_credit"))
	}
	if a.Status != model.AccountAuthFailed {
		m.clearAlert(ctx, k("auth_failed"))
	}
	if a.TokenExpiresAt != nil {
		m.expiryAlert(ctx, k("token_expiring"), "token_expiring", a.Provider, a.ID, "", *a.TokenExpiresAt, th.expWarn, th.expCrit, now, a.Label)
	} else {
		m.clearAlert(ctx, k("token_expiring"))
	}
	if a.RefreshExpiresAt != nil {
		m.expiryAlert(ctx, k("refresh_expiring"), "token_expiring", a.Provider, a.ID, "", *a.RefreshExpiresAt, th.expWarn, th.expCrit, now, a.Label)
	}
	if a.CreditExpiresAt != nil && (a.CreditRemaining == nil || *a.CreditRemaining > 1) {
		m.expiryAlert(ctx, k("credit_expiring"), "credit_expiring", a.Provider, a.ID, "", *a.CreditExpiresAt, th.expWarn, th.expCrit, now, a.Label)
	} else {
		m.clearAlert(ctx, k("credit_expiring"))
	}
	if a.TrialEndsAt != nil {
		m.expiryAlert(ctx, k("trial_ending"), "trial_ending", a.Provider, a.ID, "", *a.TrialEndsAt, 7, 1, now, a.Label)
	} else {
		m.clearAlert(ctx, k("trial_ending"))
	}
	if a.ContractEndsAt != nil {
		m.expiryAlert(ctx, k("contract_ending"), "contract_ending", a.Provider, a.ID, "", *a.ContractEndsAt, th.expWarn, th.expCrit, now, a.Label)
	} else {
		m.clearAlert(ctx, k("contract_ending"))
	}
	if a.RotateEveryDays > 0 {
		base := a.CreatedAt
		if a.RotatedAt != nil {
			base = *a.RotatedAt
		}
		if now.After(base.Add(time.Duration(a.RotateEveryDays) * 24 * time.Hour)) {
			m.raiseAlert(ctx, alertInput{Key: k("rotation_due"), Type: "rotation_due", Level: "info", Provider: a.Provider, AccountID: a.ID, Args: []any{a.Provider, a.Label, a.RotateEveryDays}})
		} else {
			m.clearAlert(ctx, k("rotation_due"))
		}
	}
	// limits
	keys := make([]string, 0, len(a.Limits))
	for n := range a.Limits {
		keys = append(keys, n)
	}
	sort.Strings(keys)
	for _, n := range keys {
		l := a.Limits[n]
		used := l.Used
		switch n {
		case "servers", "concurrent_sandboxes":
			used = float64(st.HostsLive)
			if n == "concurrent_sandboxes" && st.SandboxStats {
				used = float64(st.GroupRunning + st.InFlight) // M29-S: paused sandboxes do not count
			}
		case "vcpu":
			used = st.VCPULive
		}
		key := "limit_near|" + a.ID + "|" + n
		if l.Limit <= 0 {
			m.clearAlert(ctx, key)
			continue
		}
		pct := 100 * used / l.Limit
		if lv := pctLevel(pct, th.limitWarn, th.limitCrit); lv != "" {
			m.raiseAlert(ctx, alertInput{Key: key, Type: "limit_near", Level: lv, Provider: a.Provider, AccountID: a.ID, Args: []any{a.Provider, a.Label, n, used, l.Limit}})
		} else {
			m.clearAlert(ctx, key)
		}
	}
	// spend caps (ledger)
	for _, c := range []struct {
		name       string
		spend, cap float64
	}{{"day", st.SpendToday, a.SpendCapDay}, {"month", st.SpendMonth, a.SpendCapMonth}} {
		key := "spend_cap_near|" + a.ID + "|" + c.name
		if c.cap <= 0 {
			m.clearAlert(ctx, key)
			continue
		}
		lv := pctLevel(100*c.spend/c.cap, th.limitWarn, th.limitCrit)
		if lv == "" && c.name == "month" && st.ForecastMonthEnd > c.cap {
			lv = "warn"
		}
		if lv != "" {
			m.raiseAlert(ctx, alertInput{Key: key, Type: "spend_cap_near", Level: lv, Provider: a.Provider, AccountID: a.ID, Args: []any{a.Provider, a.Label, c.name, c.spend, c.cap, a.Currency}})
		} else {
			m.clearAlert(ctx, key)
		}
	}
	if a.BalanceSource == "manual" && a.BalanceAt != nil && now.Sub(*a.BalanceAt) > 30*24*time.Hour {
		m.raiseAlert(ctx, alertInput{Key: k("balance_stale"), Type: "balance_stale", Level: "info", Provider: a.Provider, AccountID: a.ID, Args: []any{a.Provider, a.Label, a.BalanceAt.UTC().Format("2006-01-02")}})
	} else {
		m.clearAlert(ctx, k("balance_stale"))
	}
}

// notifyInfraCritical sends a critical alert to Telegram at once.
func (m *Manager) notifyInfraCritical(a InfraAlert, args []any) {
	ctx := context.Background()
	s := m.Settings(ctx)
	if !contains(s.Notify.Events, "infra_alert") {
		return
	}
	title := i18n.T(s.Locale, "infra.alert_title", a.Level)
	body := alertText(s.Locale, a.Type, args)
	go func() {
		dctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		for _, d := range m.deliver(dctx, "infra_alert", title, body, "telegram", false) {
			if !d.OK {
				m.Emit(dctx, "system", "", "", "notify.failed", fmt.Sprintf("infra_alert via %s: %s", d.Channel, d.Error), nil)
			}
		}
	}()
}

// infraDigestTick mails the open infra alerts once a day at
// daily_report_hour (notify event infra_digest).
func (m *Manager) infraDigestTick(ctx context.Context, now time.Time) {
	s := m.Settings(ctx)
	if !contains(s.Notify.Events, "infra_digest") {
		return
	}
	local := now.In(m.notifyLocation(s.Notify))
	if local.Hour() != s.Notify.DailyReportHour {
		return
	}
	day := local.Format("2006-01-02")
	var last string
	_ = m.Store.GetKV(ctx, "notify_last_infra_digest", &last)
	if last == day {
		return
	}
	m.Store.PutKV(ctx, "notify_last_infra_digest", day)
	title, body, n := m.infraDigest(ctx, s.Locale, day)
	if n == 0 {
		return
	}
	go func() {
		dctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		m.deliver(dctx, "infra_digest", title, body, "email", false)
	}()
}

func (m *Manager) infraDigest(ctx context.Context, locale, day string) (string, string, int) {
	m.accts.alertMu.Lock()
	al := m.loadAlerts(ctx)
	m.accts.alertMu.Unlock()
	var open []*InfraAlert
	for _, a := range al {
		if a.Status != "closed" {
			open = append(open, a)
		}
	}
	sort.Slice(open, func(i, j int) bool {
		if levelRank[open[i].Level] != levelRank[open[j].Level] {
			return levelRank[open[i].Level] > levelRank[open[j].Level]
		}
		return open[i].ID < open[j].ID
	})
	var b strings.Builder
	for _, a := range open {
		var args []any
		if x, ok := a.Data["args"].([]any); ok {
			args = x
		}
		fmt.Fprintf(&b, "[%s] %s\n", a.Level, alertText(locale, a.Type, args))
	}
	return i18n.T(locale, "infra.digest_title", day, len(open)), strings.TrimRight(b.String(), "\n"), len(open)
}

// ---- admin API ----

// ListInfraAlerts is GET /v1/admin/infra/alerts?status=open|acked|closed|all.
func (m *Manager) ListInfraAlerts(ctx context.Context, p *Principal, status string) ([]*InfraAlert, error) {
	if err := p.Require(model.ScopeAdmin); err != nil {
		return nil, err
	}
	m.accts.alertMu.Lock()
	al := m.loadAlerts(ctx)
	m.accts.alertMu.Unlock()
	out := []*InfraAlert{}
	for _, a := range al {
		switch status {
		case "", "active":
			if a.Status == "closed" {
				continue
			}
		case "all":
		default:
			if a.Status != status {
				continue
			}
		}
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool {
		if levelRank[out[i].Level] != levelRank[out[j].Level] {
			return levelRank[out[i].Level] > levelRank[out[j].Level]
		}
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	return out, nil
}

// AckInfraAlert is POST /v1/admin/infra/alerts/{id}/ack: no more repeats
// until the alert escalates or clears.
func (m *Manager) AckInfraAlert(ctx context.Context, p *Principal, id string) (*InfraAlert, error) {
	if err := p.Require(model.ScopeAdmin); err != nil {
		return nil, err
	}
	m.accts.alertMu.Lock()
	al := m.loadAlerts(ctx)
	a := al[id]
	if a == nil {
		m.accts.alertMu.Unlock()
		return nil, notFound("alert", id)
	}
	now := time.Now().UTC()
	if a.Status == "open" {
		a.Status, a.AckedAt, a.AckedBy = "acked", &now, p.Actor()
	}
	snap := *a
	m.saveAlerts(ctx, al)
	m.accts.alertMu.Unlock()
	m.EmitData(ctx, p.Actor(), "", "", "provider_account.alert_acked", "acknowledged: "+snap.Message, nil, map[string]any{"alert_id": id, "account_id": snap.AccountID, "provider": snap.Provider})
	return &snap, nil
}
