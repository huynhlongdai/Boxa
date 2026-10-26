package manager

// Reports & alerts (Telegram + email). Non-secret settings live in
// settings.notify; the Telegram bot token and SMTP password are system
// secrets in the encrypted secrets store (BOXA_SYS_*), never returned.

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"boxa/core/internal/i18n"
	"boxa/core/internal/model"
	"boxa/core/internal/notify"
	"boxa/core/internal/store"
)

const (
	sysSecretTelegram = model.SystemSecretPrefix + "TELEGRAM_BOT_TOKEN"
	sysSecretSMTP     = model.SystemSecretPrefix + "SMTP_PASSWORD"
)

// NotifyEvents are the event kinds a user can subscribe to.
var NotifyEvents = []string{"daily_report", "budget", "approval", "emergency_stop", "box_failed", "host_down"}

// Delivery is one notification attempt (kept in memory, newest first).
type Delivery struct {
	TS      time.Time `json:"ts"`
	Kind    string    `json:"kind"`
	Channel string    `json:"channel"`
	OK      bool      `json:"ok"`
	Error   string    `json:"error,omitempty"`
	Title   string    `json:"title"`
}

// NotifyView is GET /notify.
type NotifyView struct {
	model.NotifySettings
	HasTelegramToken   bool       `json:"has_telegram_token"`
	HasSMTPPassword    bool       `json:"has_smtp_password"`
	TelegramConfigured bool       `json:"telegram_configured"`
	EmailConfigured    bool       `json:"email_configured"`
	EventsAvailable    []string   `json:"events_available"`
	Recent             []Delivery `json:"recent"`
}

// NotifyInput is PUT /notify. Secrets are write-only: empty keeps the stored
// value, clear_* removes it.
type NotifyInput struct {
	model.NotifySettings
	TelegramBotToken   string `json:"telegram_bot_token,omitempty"`
	SMTPPassword       string `json:"smtp_password,omitempty"`
	ClearTelegramToken bool   `json:"clear_telegram_token,omitempty"`
	ClearSMTPPassword  bool   `json:"clear_smtp_password,omitempty"`
}

// NotifyTestInput is POST /notify/test.
type NotifyTestInput struct {
	Channel string `json:"channel"` // telegram | email | all (default)
	Kind    string `json:"kind"`    // test (default) | daily_report
}

type NotifyTestResult struct {
	Results []Delivery `json:"results"`
}

type notifier struct {
	mu     sync.Mutex
	recent []Delivery
}

func (n *notifier) add(d Delivery) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.recent = append([]Delivery{d}, n.recent...)
	if len(n.recent) > 30 {
		n.recent = n.recent[:30]
	}
}

func (n *notifier) list() []Delivery {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]Delivery{}, n.recent...)
}

func (m *Manager) NotifyView(ctx context.Context, p *Principal) (*NotifyView, error) {
	if err := p.Require(model.ScopeAdmin); err != nil {
		return nil, err
	}
	s := m.Settings(ctx).Notify
	v := &NotifyView{NotifySettings: s, EventsAvailable: NotifyEvents, Recent: m.notif.list()}
	_, v.HasTelegramToken = m.systemSecret(ctx, sysSecretTelegram)
	_, v.HasSMTPPassword = m.systemSecret(ctx, sysSecretSMTP)
	v.TelegramConfigured = v.HasTelegramToken && s.TelegramChatID != ""
	v.EmailConfigured = s.SMTP.Host != "" && s.SMTP.From != "" && len(notify.Recipients(s.Email)) > 0
	return v, nil
}

func (m *Manager) PutNotify(ctx context.Context, p *Principal, in NotifyInput) (*NotifyView, error) {
	if err := p.Require(model.ScopeAdmin); err != nil {
		return nil, err
	}
	n := in.NotifySettings
	if n.DailyReportHour < 0 || n.DailyReportHour > 23 {
		return nil, model.E(model.ErrInvalid, "notify.daily_report_hour must be 0..23")
	}
	if n.Timezone != "" {
		if _, err := time.LoadLocation(n.Timezone); err != nil {
			return nil, model.E(model.ErrInvalid, "unknown timezone %q", n.Timezone)
		}
	}
	switch strings.ToLower(n.SMTP.TLS) {
	case "", "starttls", "tls", "none":
		n.SMTP.TLS = strings.ToLower(n.SMTP.TLS)
	default:
		return nil, model.E(model.ErrInvalid, "smtp.tls must be starttls, tls or none")
	}
	if n.SMTP.Port < 0 || n.SMTP.Port > 65535 {
		return nil, model.E(model.ErrInvalid, "smtp.port must be 1..65535")
	}
	evs := []string{}
	for _, e := range n.Events {
		if !contains(NotifyEvents, e) {
			return nil, model.E(model.ErrInvalid, "unknown notify event %q", e)
		}
		if !contains(evs, e) {
			evs = append(evs, e)
		}
	}
	n.Events = evs
	n.TelegramChatID = strings.TrimSpace(n.TelegramChatID)
	if in.ClearTelegramToken {
		m.deleteSystemSecret(ctx, sysSecretTelegram)
	} else if t := strings.TrimSpace(in.TelegramBotToken); t != "" {
		if err := m.putSystemSecret(ctx, sysSecretTelegram, t); err != nil {
			return nil, err
		}
	}
	if in.ClearSMTPPassword {
		m.deleteSystemSecret(ctx, sysSecretSMTP)
	} else if in.SMTPPassword != "" {
		if err := m.putSystemSecret(ctx, sysSecretSMTP, in.SMTPPassword); err != nil {
			return nil, err
		}
	}
	s := m.Settings(ctx)
	s.Notify = n
	if err := m.Store.PutKV(ctx, "settings", s); err != nil {
		return nil, err
	}
	m.Emit(ctx, p.Actor(), "", "", "notify.updated", "notification settings changed", nil)
	return m.NotifyView(ctx, p)
}

// TestNotify sends a test message (or the daily report) right now and
// returns per-channel results synchronously.
func (m *Manager) TestNotify(ctx context.Context, p *Principal, in NotifyTestInput) (*NotifyTestResult, error) {
	if err := p.Require(model.ScopeAdmin); err != nil {
		return nil, err
	}
	ch := in.Channel
	if ch == "" {
		ch = "all"
	}
	if ch != "all" && ch != "telegram" && ch != "email" {
		return nil, model.E(model.ErrInvalid, "channel must be telegram, email or all")
	}
	s := m.Settings(ctx)
	loc := s.Locale
	title, body := i18n.T(loc, "notify.test_title"), i18n.T(loc, "notify.test_body", m.publicName())
	kind := "test"
	if in.Kind == "daily_report" {
		kind = "daily_report"
		title, body = m.dailyReport(ctx, time.Now())
	}
	tctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	res := &NotifyTestResult{Results: m.deliver(tctx, kind, title, body, ch, true)}
	return res, nil
}

func (m *Manager) publicName() string {
	if m.Cfg.PublicURL != "" {
		return m.Cfg.PublicURL
	}
	return "Boxa"
}

// deliver sends to the selected channels. force=true reports unconfigured
// channels as errors (test button); otherwise they are skipped silently.
func (m *Manager) deliver(ctx context.Context, kind, title, body, channel string, force bool) []Delivery {
	s := m.Settings(ctx).Notify
	var out []Delivery
	now := time.Now().UTC()
	if channel == "all" || channel == "telegram" {
		tok, ok := m.systemSecret(ctx, sysSecretTelegram)
		if ok && s.TelegramChatID != "" {
			err := notify.Telegram{API: m.Cfg.TelegramAPI}.Send(ctx, tok, s.TelegramChatID, title+"\n\n"+body)
			out = append(out, result(now, kind, "telegram", title, err))
		} else if force {
			out = append(out, Delivery{TS: now, Kind: kind, Channel: "telegram", Title: title, Error: "telegram not configured (bot token + chat id)"})
		}
	}
	if channel == "all" || channel == "email" {
		cfg := notify.SMTP{Host: s.SMTP.Host, Port: s.SMTP.Port, Username: s.SMTP.Username, From: s.SMTP.From, TLS: s.SMTP.TLS}
		cfg.Password, _ = m.systemSecret(ctx, sysSecretSMTP)
		to := notify.Recipients(s.Email)
		if cfg.Configured() && len(to) > 0 {
			err := cfg.Send(ctx, to, "[Boxa] "+title, body)
			out = append(out, result(now, kind, "email", title, err))
		} else if force {
			out = append(out, Delivery{TS: now, Kind: kind, Channel: "email", Title: title, Error: "email not configured (smtp host, from, recipients)"})
		}
	}
	for _, d := range out {
		m.notif.add(d)
		if !d.OK {
			log.Printf("notify %s via %s failed: %s", kind, d.Channel, d.Error)
		}
	}
	return out
}

func result(ts time.Time, kind, ch, title string, err error) Delivery {
	d := Delivery{TS: ts, Kind: kind, Channel: ch, Title: title, OK: err == nil}
	if err != nil {
		d.Error = err.Error()
	}
	return d
}

// notify sends an alert asynchronously when the user subscribed to kind.
func (m *Manager) notify(kind, title, body string) {
	ctx := context.Background()
	s := m.Settings(ctx).Notify
	if !contains(s.Events, kind) {
		return
	}
	go func() {
		dctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		res := m.deliver(dctx, kind, title, body, "all", false)
		for _, d := range res {
			if !d.OK {
				m.Emit(dctx, "system", "", "", "notify.failed", fmt.Sprintf("%s via %s: %s", kind, d.Channel, d.Error), nil)
			}
		}
	}()
}

// notifyApproval maps a new approval to its notification kind.
func (m *Manager) notifyApproval(a *model.Approval) {
	loc := m.Settings(context.Background()).Locale
	kind := "approval"
	switch a.Type {
	case "budget":
		kind = "budget"
	case "host_down":
		kind = "host_down"
	}
	body := a.Detail
	if a.AmountUSD != nil {
		body += "\n" + i18n.T(loc, "notify.amount", *a.AmountUSD)
	}
	body += "\n\n" + i18n.T(loc, "notify.approve_hint")
	m.notify(kind, a.Title, body)
}

func (m *Manager) notifyLocation(s model.NotifySettings) *time.Location {
	if s.Timezone != "" {
		if l, err := time.LoadLocation(s.Timezone); err == nil {
			return l
		}
	}
	return time.UTC
}

// notifyTick runs every minute: sends the daily cost report once a day at
// daily_report_hour in the configured timezone.
func (m *Manager) notifyTick(ctx context.Context) error {
	s := m.Settings(ctx).Notify
	if !contains(s.Events, "daily_report") {
		return nil
	}
	now := time.Now().In(m.notifyLocation(s))
	if now.Hour() != s.DailyReportHour {
		return nil
	}
	day := now.Format("2006-01-02")
	var last string
	_ = m.Store.GetKV(ctx, "notify_last_daily", &last)
	if last == day {
		return nil
	}
	if err := m.Store.PutKV(ctx, "notify_last_daily", day); err != nil {
		return err
	}
	title, body := m.dailyReport(ctx, now)
	res := m.deliver(ctx, "daily_report", title, body, "all", false)
	for _, d := range res {
		if !d.OK {
			return fmt.Errorf("daily report via %s: %s", d.Channel, d.Error)
		}
	}
	return nil
}

// dailyReport builds the localized daily cost report (yesterday + month to date).
func (m *Manager) dailyReport(ctx context.Context, now time.Time) (string, string) {
	st := m.Settings(ctx)
	loc := st.Locale
	tz := m.notifyLocation(st.Notify)
	local := now.In(tz)
	todayStart := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, tz)
	yStart := todayStart.AddDate(0, 0, -1)
	mStart := time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, tz)
	yesterday, _ := m.Store.SumLedger(ctx, store.LedgerFilter{From: yStart.UTC(), To: todayStart.UTC()})
	month, _ := m.Store.SumLedger(ctx, store.LedgerFilter{From: mStart.UTC()})
	byProv, _ := m.Store.SumLedgerBy(ctx, store.LedgerFilter{From: yStart.UTC(), To: todayStart.UTC()}, "provider")
	sys := &Principal{Key: &model.APIKey{Scopes: []string{model.ScopeAdmin}}, Admin: true}
	o, _ := m.Overview(ctx, sys)
	var b strings.Builder
	b.WriteString(i18n.T(loc, "notify.daily_yesterday", yStart.Format("2006-01-02"), yesterday) + "\n")
	budget := st.BudgetMonthUSD
	if budget > 0 {
		b.WriteString(i18n.T(loc, "notify.daily_month_budget", month, budget, 100*month/budget) + "\n")
	} else {
		b.WriteString(i18n.T(loc, "notify.daily_month", month) + "\n")
	}
	for p, v := range byProv {
		if p == "" {
			p = "local"
		}
		b.WriteString(fmt.Sprintf("  · %s: $%.4f\n", p, v))
	}
	if o != nil {
		b.WriteString(i18n.T(loc, "notify.daily_boxes", o.BoxesRunning, o.BoxesSleeping) + "\n")
		b.WriteString(i18n.T(loc, "notify.daily_saved", o.SavedVs247USD) + "\n")
		if o.ApprovalsPending > 0 {
			b.WriteString(i18n.T(loc, "notify.daily_approvals", o.ApprovalsPending) + "\n")
		}
		for i, t := range o.TopTasks {
			if i >= 3 {
				break
			}
			b.WriteString(fmt.Sprintf("  %d. %s (%s) $%.4f\n", i+1, t.Slug, t.AgentName, t.CostUSD))
		}
	}
	return i18n.T(loc, "notify.daily_title", local.Format("2006-01-02")), strings.TrimRight(b.String(), "\n")
}
