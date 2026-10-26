package manager

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"time"

	"boxa/core/internal/events"
	"boxa/core/internal/model"
	"boxa/core/internal/secret"
	"boxa/core/internal/store"
)

// ---- ledger (spec M12) ----

// ledgerTick writes one compute entry per billable box for the elapsed interval.
// Pool boxes are billed while running (CPU-share of the monthly host);
// burst boxes are billed while their host exists (running or sleeping).
func (m *Manager) ledgerTick(ctx context.Context) error {
	now := time.Now().UTC()
	m.mu.Lock()
	last := m.lastTick
	m.lastTick = now
	m.mu.Unlock()
	if last.IsZero() {
		return nil // first tick only sets the clock
	}
	dt := now.Sub(last)
	if dt > 10*m.Cfg.LedgerInterval {
		dt = m.Cfg.LedgerInterval // core was down; don't bill the gap
	}
	boxes, err := m.Store.ListBoxes(ctx, store.BoxFilter{Statuses: []string{model.StatusRunning, model.StatusSleeping, model.StatusSnapshotting}})
	if err != nil {
		return err
	}
	for _, b := range boxes {
		billable := b.Status == model.StatusRunning || b.Status == model.StatusSnapshotting || (b.Billing == "burst" && b.Status == model.StatusSleeping)
		if !billable {
			continue
		}
		amt := model.Round6(b.CostPerHour * dt.Hours())
		e := &model.LedgerEntry{ID: secret.ID("led"), TS: now, AgentID: b.AgentID, BoxID: b.ID, Provider: b.Provider, Kind: "compute", AmountUSD: amt, Billing: b.Billing,
			Note: fmt.Sprintf("%s %.0fs × %s/h (%s)", b.Status, dt.Seconds(), fmtUSD(b.CostPerHour), b.Billing)}
		if err := m.Store.AddLedger(ctx, e); err != nil {
			return err
		}
		// re-read to avoid clobbering concurrent status changes
		lock := m.boxLock(b.ID)
		if lock.TryLock() {
			if fresh, err := m.Store.GetBox(ctx, b.ID); err == nil {
				fresh.SpentUSD = model.Round6(fresh.SpentUSD + amt)
				m.Store.PutBox(ctx, fresh)
			}
			lock.Unlock()
		} else {
			// box busy (exec/snapshot); fold amount in next time via ledger sum
			if sum, err := m.Store.SumLedger(ctx, store.LedgerFilter{BoxID: b.ID}); err == nil {
				go func(id string, s float64) {
					l := m.boxLock(id)
					l.Lock()
					defer l.Unlock()
					if fb, err := m.Store.GetBox(context.Background(), id); err == nil {
						fb.SpentUSD = model.Round6(s)
						m.Store.PutBox(context.Background(), fb)
					}
				}(b.ID, sum)
			}
		}
	}
	m.enforceBudgets(ctx)
	m.Broker.Publish(events.Msg{Type: "overview.updated", Data: map[string]any{"ts": now}, Public: true})
	return nil
}

func (m *Manager) hasOverride(ctx context.Context, key string) bool {
	var v bool
	return m.Store.GetKV(ctx, "override:"+key, &v) == nil && v
}

func (m *Manager) agentPeriodKeys(agentID string, now time.Time) (day, month string) {
	return "agent-day:" + agentID + ":" + now.Format("2006-01-02"), "agent-month:" + agentID + ":" + now.Format("2006-01")
}

// checkBudget is called before creating/waking boxes.
func (m *Manager) checkBudget(ctx context.Context, a *model.Agent) error {
	now := time.Now().UTC()
	dk, mk := m.agentPeriodKeys(a.ID, now)
	if a.BudgetDayUSD > 0 && !m.hasOverride(ctx, dk) {
		spent, _ := m.Store.SumLedger(ctx, store.LedgerFilter{AgentID: a.ID, From: dayStart(now)})
		if spent >= a.BudgetDayUSD {
			return model.EH(model.ErrBudgetExceeded, "Daily budget used; wait until 00:00 UTC or ask the operator to approve more.", "agent %s spent %s of %s today", a.Name, fmtUSD(spent), fmtUSD(a.BudgetDayUSD))
		}
	}
	if a.BudgetMonthUSD > 0 && !m.hasOverride(ctx, mk) {
		spent, _ := m.Store.SumLedger(ctx, store.LedgerFilter{AgentID: a.ID, From: monthStart(now)})
		if spent >= a.BudgetMonthUSD {
			return model.EH(model.ErrBudgetExceeded, "Monthly budget used; ask the operator to raise budget_month_usd.", "agent %s spent %s of %s this month", a.Name, fmtUSD(spent), fmtUSD(a.BudgetMonthUSD))
		}
	}
	s := m.Settings(ctx)
	if s.BudgetMonthUSD > 0 && !m.hasOverride(ctx, "global-month:"+now.Format("2006-01")) {
		total, _ := m.Store.SumLedger(ctx, store.LedgerFilter{From: monthStart(now)})
		if total >= s.BudgetMonthUSD {
			return model.EH(model.ErrBudgetExceeded, "The global monthly budget is used; ask the operator.", "global monthly budget %s reached", fmtUSD(s.BudgetMonthUSD))
		}
	}
	return nil
}

// enforceBudgets applies the 80% warning / 100% enforcement rules.
func (m *Manager) enforceBudgets(ctx context.Context) {
	now := time.Now().UTC()
	agents, _ := m.Store.ListAgents(ctx)
	for _, a := range agents {
		dk, mk := m.agentPeriodKeys(a.ID, now)
		checks := []struct {
			limit float64
			from  time.Time
			key   string
			label string
		}{
			{a.BudgetDayUSD, dayStart(now), dk, "ngày"},
			{a.BudgetMonthUSD, monthStart(now), mk, "tháng"},
		}
		for _, c := range checks {
			if c.limit <= 0 || m.hasOverride(ctx, c.key) {
				continue
			}
			spent, _ := m.Store.SumLedger(ctx, store.LedgerFilter{AgentID: a.ID, From: c.from})
			ratio := spent / c.limit
			if ratio >= 0.8 {
				amt := model.Round4(spent)
				m.createApproval(ctx, "budget", "budget80:"+c.key,
					fmt.Sprintf("Agent %s đã dùng %.0f%% ngân sách %s", a.Name, math.Min(ratio, 9.99)*100, c.label),
					fmt.Sprintf("Đã chi %s / %s. Duyệt = cho phép vượt ngân sách %s này; từ chối = giữ giới hạn (100%% sẽ tự dừng box).", fmtUSD(spent), fmtUSD(c.limit), c.label),
					&amt, "", a.ID)
			}
			if ratio >= 1 {
				m.enforceAgent(ctx, a, fmt.Sprintf("ngân sách %s %s đã hết", c.label, fmtUSD(c.limit)))
			}
		}
	}
	// per-box budget
	boxes, _ := m.Store.ListBoxes(ctx, store.BoxFilter{Statuses: []string{model.StatusRunning}})
	for _, b := range boxes {
		if b.BudgetUSD > 0 && b.SpentUSD >= b.BudgetUSD && !m.hasOverride(ctx, "box:"+b.ID) {
			m.enforceBox(ctx, b, fmt.Sprintf("box budget %s used", fmtUSD(b.BudgetUSD)))
		}
	}
	// global monthly
	s := m.Settings(ctx)
	gk := "global-month:" + now.Format("2006-01")
	if s.BudgetMonthUSD > 0 && !m.hasOverride(ctx, gk) {
		total, _ := m.Store.SumLedger(ctx, store.LedgerFilter{From: monthStart(now)})
		ratio := total / s.BudgetMonthUSD
		if ratio >= 0.8 {
			amt := model.Round4(total)
			m.createApproval(ctx, "budget", "budget80:"+gk, fmt.Sprintf("Đã dùng %.0f%% ngân sách tháng toàn hệ thống", math.Min(ratio, 9.99)*100),
				fmt.Sprintf("Đã chi %s / %s tháng này.", fmtUSD(total), fmtUSD(s.BudgetMonthUSD)), &amt, "", "")
		}
		if ratio >= 1 {
			all, _ := m.Store.ListBoxes(ctx, store.BoxFilter{Statuses: []string{model.StatusRunning}})
			for _, b := range all {
				m.enforceBox(ctx, b, "global monthly budget reached")
			}
		}
	}
}

func (m *Manager) enforceAgent(ctx context.Context, a *model.Agent, why string) {
	boxes, _ := m.Store.ListBoxes(ctx, store.BoxFilter{AgentID: a.ID, Status: model.StatusRunning})
	for _, b := range boxes {
		m.enforceBox(ctx, b, why)
	}
}

// enforceBox: session → sleep; task → snapshot then stop; service → ask first.
func (m *Manager) enforceBox(ctx context.Context, b *model.Box, why string) {
	switch b.Lifecycle {
	case model.LifecycleService:
		amt := model.Round4(b.SpentUSD)
		m.createApproval(ctx, "service_stop", "service_stop:"+b.ID+":"+time.Now().UTC().Format("2006-01-02"),
			"Dừng service "+b.Slug+"?", "Box service "+b.Slug+" ("+b.ID+") vượt ngân sách: "+why+". Duyệt = cho box ngủ; từ chối = tiếp tục chạy.", &amt, b.ID, b.AgentID)
	case model.LifecycleTask:
		m.snapshotBox(ctx, b, "budget")
		m.sleepBox(ctx, b, "budget", why)
	default:
		m.sleepBox(ctx, b, "budget", why)
	}
}

// ---- approvals ----

func (m *Manager) createApproval(ctx context.Context, typ, dedupe, title, detail string, amount *float64, boxID, agentID string) *model.Approval {
	if dedupe != "" {
		if a, err := m.Store.FindApprovalByDedupe(ctx, dedupe); err == nil {
			return a
		}
	}
	a := &model.Approval{ID: secret.ID("apr"), Type: typ, Title: title, Detail: detail, AmountUSD: amount, CreatedAt: time.Now().UTC(), Status: "pending", DedupeKey: dedupe, BoxID: boxID, AgentID: agentID}
	if err := m.Store.PutApproval(ctx, a); err != nil {
		return nil
	}
	m.Emit(ctx, "system", boxID, agentID, "approval.created", title, amount)
	m.Broker.Publish(events.Msg{Type: "approval.created", Data: a, AgentID: ""})
	return a
}

func (m *Manager) ListApprovals(ctx context.Context, p *Principal, status string) ([]*model.Approval, error) {
	if err := p.Require(model.ScopeAdmin); err != nil {
		return nil, err
	}
	return m.Store.ListApprovals(ctx, status)
}

// Decide approves or rejects an approval and applies its effect.
func (m *Manager) Decide(ctx context.Context, p *Principal, id string, approve bool) (*model.Approval, error) {
	if err := p.Require(model.ScopeAdmin); err != nil {
		return nil, err
	}
	a, err := m.Store.GetApproval(ctx, id)
	if err != nil {
		return nil, notFound("approval", id)
	}
	if a.Status != "pending" {
		return nil, model.E(model.ErrInvalid, "approval already %s", a.Status)
	}
	if approve {
		a.Status = "approved"
		switch a.Type {
		case "budget":
			// allow overrun for the period this approval was raised for
			if len(a.DedupeKey) > len("budget80:") {
				m.Store.PutKV(ctx, "override:"+a.DedupeKey[len("budget80:"):], true)
			}
		case "service_stop":
			if b, err := m.Store.GetBox(ctx, a.BoxID); err == nil && b.Status == model.StatusRunning {
				m.sleepBox(ctx, b, p.Actor(), "service stop approved")
			}
		}
	} else {
		a.Status = "rejected"
		if a.Type == "service_stop" && a.BoxID != "" {
			m.Store.PutKV(ctx, "override:box:"+a.BoxID, true)
		}
	}
	if err := m.Store.PutApproval(ctx, a); err != nil {
		return nil, err
	}
	m.Emit(ctx, p.Actor(), a.BoxID, a.AgentID, "approval."+a.Status, a.Title, a.AmountUSD)
	return a, nil
}

// ---- overview / costs ----

type Overview struct {
	SpentToday       float64   `json:"spent_today"`
	BudgetDay        float64   `json:"budget_day"`
	SpentMonth       float64   `json:"spent_month"`
	BudgetMonth      float64   `json:"budget_month"`
	BoxesRunning     int       `json:"boxes_running"`
	BoxesSleeping    int       `json:"boxes_sleeping"`
	SavedVs247USD    float64   `json:"saved_vs_247_usd"`
	Cost7d           []DayCost `json:"cost_7d"`
	TopTasks         []TopTask `json:"top_tasks"`
	ApprovalsPending int       `json:"approvals_pending"`
}

type DayCost struct {
	Date    string             `json:"date"`
	ByAgent map[string]float64 `json:"by_agent"`
}

type TopTask struct {
	BoxID     string  `json:"box_id"`
	Slug      string  `json:"slug"`
	AgentName string  `json:"agent_name"`
	CostUSD   float64 `json:"cost_usd"`
}

func (m *Manager) Overview(ctx context.Context, p *Principal) (*Overview, error) {
	now := time.Now().UTC()
	agentFilter := ""
	if !p.Admin {
		agentFilter = p.AgentID
	}
	o := &Overview{Cost7d: []DayCost{}, TopTasks: []TopTask{}}
	o.SpentToday, _ = m.Store.SumLedger(ctx, store.LedgerFilter{AgentID: agentFilter, From: dayStart(now)})
	o.SpentMonth, _ = m.Store.SumLedger(ctx, store.LedgerFilter{AgentID: agentFilter, From: monthStart(now)})
	agents, _ := m.Store.ListAgents(ctx)
	names := map[string]string{}
	for _, a := range agents {
		names[a.ID] = a.Name
		if agentFilter == "" || a.ID == agentFilter {
			o.BudgetDay += a.BudgetDayUSD
			o.BudgetMonth += a.BudgetMonthUSD
		}
	}
	if s := m.Settings(ctx); s.BudgetMonthUSD > 0 && agentFilter == "" {
		o.BudgetMonth = s.BudgetMonthUSD
	}
	boxes, _ := m.Store.ListBoxes(ctx, store.BoxFilter{AgentID: agentFilter, IncludeTerminated: true})
	ms := monthStart(now)
	for _, b := range boxes {
		switch b.Status {
		case model.StatusRunning:
			o.BoxesRunning++
		case model.StatusSleeping:
			o.BoxesSleeping++
		}
		// What this box would cost running 24/7 on an hourly VM since it was created (this month).
		start := b.CreatedAt
		if start.Before(ms) {
			start = ms
		}
		end := now
		if b.TerminatedAt != nil {
			end = *b.TerminatedAt
		}
		if end.After(start) && b.Status != model.StatusFailed {
			ref := b.CostPerHour
			if ref == 0 {
				ref = referenceHourly(b.Spec)
			}
			if b.Billing != "burst" {
				ref = math.Max(ref, referenceHourly(b.Spec))
			}
			hyp := ref * end.Sub(start).Hours()
			o.SavedVs247USD += math.Max(0, hyp-b.SpentUSD)
		}
	}
	rows, _ := m.Store.SumLedgerByDateAgent(ctx, dayStart(now).AddDate(0, 0, -6))
	byDate := map[string]map[string]float64{}
	for _, r := range rows {
		if agentFilter != "" && r.AgentID != agentFilter {
			continue
		}
		if byDate[r.Date] == nil {
			byDate[r.Date] = map[string]float64{}
		}
		n := names[r.AgentID]
		if n == "" {
			n = r.AgentID
		}
		byDate[r.Date][n] = model.Round4(byDate[r.Date][n] + r.Amount)
	}
	for i := 6; i >= 0; i-- {
		d := dayStart(now).AddDate(0, 0, -i).Format("2006-01-02")
		ba := byDate[d]
		if ba == nil {
			ba = map[string]float64{}
		}
		o.Cost7d = append(o.Cost7d, DayCost{Date: d, ByAgent: ba})
	}
	sums, _ := m.Store.SumLedgerBy(ctx, store.LedgerFilter{AgentID: agentFilter, From: dayStart(now).AddDate(0, 0, -6)}, "box_id")
	type kv struct {
		id string
		v  float64
	}
	var list []kv
	for k, v := range sums {
		list = append(list, kv{k, v})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].v > list[j].v })
	for i := 0; i < len(list) && i < 5; i++ {
		t := TopTask{BoxID: list[i].id, CostUSD: model.Round4(list[i].v)}
		if b, err := m.Store.GetBox(ctx, list[i].id); err == nil {
			t.Slug, t.AgentName = b.Slug, b.AgentName
		}
		o.TopTasks = append(o.TopTasks, t)
	}
	if p.Admin {
		pend, _ := m.Store.ListApprovals(ctx, "pending")
		o.ApprovalsPending = len(pend)
	}
	o.SpentToday, o.SpentMonth = model.Round4(o.SpentToday), model.Round4(o.SpentMonth)
	o.BudgetDay, o.BudgetMonth = model.Round4(o.BudgetDay), model.Round4(o.BudgetMonth)
	o.SavedVs247USD = model.Round4(o.SavedVs247USD)
	return o, nil
}

// referenceHourly is a conservative on-demand reference price used for
// "saved vs 24/7" (≈ $0.006/vCPU-h + $0.003/GB-h, typical hourly VPS).
func referenceHourly(s model.Spec) float64 {
	return 0.006*s.CPU + 0.003*float64(s.RAMMB)/1024
}

type CostsResult struct {
	Items      []*model.LedgerEntry `json:"items"`
	NextCursor *string              `json:"next_cursor"`
	Summary    CostSummary          `json:"summary"`
}

type CostSummary struct {
	Total      float64            `json:"total"`
	ByProvider map[string]float64 `json:"by_provider"`
	ByAgent    map[string]float64 `json:"by_agent"`
	PoolUSD    float64            `json:"pool_usd"`
	BurstUSD   float64            `json:"burst_usd"`
}

func (m *Manager) Costs(ctx context.Context, p *Principal, f store.LedgerFilter) (*CostsResult, error) {
	if !p.Admin {
		f.AgentID = p.AgentID
	} else if f.AgentID != "" {
		if a, err := m.Store.GetAgentByName(ctx, f.AgentID); err == nil {
			f.AgentID = a.ID
		}
	}
	items, err := m.Store.ListLedger(ctx, f)
	if err != nil {
		return nil, err
	}
	for _, it := range items {
		it.AmountUSD = model.Round6(it.AmountUSD)
	}
	r := &CostsResult{Items: items, Summary: CostSummary{ByProvider: map[string]float64{}, ByAgent: map[string]float64{}}}
	sf := f
	sf.Limit = 0
	r.Summary.Total, _ = m.Store.SumLedger(ctx, sf)
	bp, _ := m.Store.SumLedgerBy(ctx, sf, "provider")
	for k, v := range bp {
		r.Summary.ByProvider[k] = model.Round4(v)
	}
	ba, _ := m.Store.SumLedgerBy(ctx, sf, "agent_id")
	agents, _ := m.Store.ListAgents(ctx)
	names := map[string]string{}
	for _, a := range agents {
		names[a.ID] = a.Name
	}
	for k, v := range ba {
		n := names[k]
		if n == "" {
			n = k
		}
		r.Summary.ByAgent[n] = model.Round4(v)
	}
	bb, _ := m.Store.SumLedgerBy(ctx, sf, "billing")
	r.Summary.PoolUSD = model.Round4(bb["pool"])
	r.Summary.BurstUSD = model.Round4(bb["burst"])
	r.Summary.Total = model.Round4(r.Summary.Total)
	return r, nil
}

func (m *Manager) CostsCSV(ctx context.Context, p *Principal, f store.LedgerFilter, w io.Writer) error {
	f.Limit = 100000
	r, err := m.Costs(ctx, p, f)
	if err != nil {
		return err
	}
	cw := csv.NewWriter(w)
	cw.Write([]string{"id", "ts", "agent_id", "box_id", "provider", "kind", "amount_usd", "note"})
	for _, e := range r.Items {
		cw.Write([]string{e.ID, e.TS.Format(time.RFC3339), e.AgentID, e.BoxID, e.Provider, e.Kind, strconv.FormatFloat(e.AmountUSD, 'f', 6, 64), e.Note})
	}
	cw.Flush()
	return cw.Error()
}

type Suggestion struct {
	Kind      string  `json:"kind"`
	Title     string  `json:"title"`
	Detail    string  `json:"detail"`
	SavingUSD float64 `json:"saving_usd"`
	BoxID     string  `json:"box_id,omitempty"`
	AgentID   string  `json:"agent_id,omitempty"`
}

// Suggestions returns simple, rule-based savings ideas.
func (m *Manager) Suggestions(ctx context.Context, p *Principal) ([]Suggestion, error) {
	now := time.Now().UTC()
	out := []Suggestion{}
	agentFilter := ""
	if !p.Admin {
		agentFilter = p.AgentID
	}
	// 1. Burst spend vs a monthly host (spec M4 pool-expansion rule).
	burst7, _ := m.Store.SumLedger(ctx, store.LedgerFilter{Billing: "burst", From: now.AddDate(0, 0, -7)})
	if burst7 > 0 {
		offers, _ := m.Store.ListOffers(ctx, "")
		cheapest := 0.0
		var sku string
		for _, o := range offers {
			if o.CPU >= 2 && o.RAMMB >= 4096 && o.PriceMonth > 0 && (cheapest == 0 || o.PriceMonth < cheapest) {
				cheapest, sku = o.PriceMonth, o.Provider+" "+o.SKU
			}
		}
		monthlyBurst := burst7 / 7 * 30
		if cheapest > 0 && monthlyBurst > 0.6*cheapest {
			out = append(out, Suggestion{Kind: "pool_expand", Title: "Thuê thêm 1 host tháng",
				Detail:    fmt.Sprintf("Chi burst 7 ngày %s (~%s/tháng) > 60%% giá %s (%s/tháng).", fmtUSD(burst7), fmtUSD(monthlyBurst), sku, fmtUSD(cheapest)),
				SavingUSD: model.Round4(math.Max(0, monthlyBurst-cheapest))})
		}
	}
	boxes, _ := m.Store.ListBoxes(ctx, store.BoxFilter{AgentID: agentFilter})
	for _, b := range boxes {
		// 2. Long idle thresholds on session boxes.
		if b.Lifecycle == model.LifecycleSession && b.IdleMinutes > 15 && b.CostPerHour > 0 {
			waste := b.CostPerHour * float64(b.IdleMinutes-5) / 60 * 30
			out = append(out, Suggestion{Kind: "idle_minutes", BoxID: b.ID, AgentID: b.AgentID, Title: "Giảm idle_minutes của " + b.Slug,
				Detail: fmt.Sprintf("Box ngủ sau %d phút rảnh; đặt 5 phút tiết kiệm khoảng %s/tháng nếu mỗi ngày rảnh một lần.", b.IdleMinutes, fmtUSD(waste)), SavingUSD: model.Round4(waste)})
		}
		// 3. Running boxes idle > 2h.
		if b.Status == model.StatusRunning && now.Sub(b.LastActiveAt) > 2*time.Hour && b.Lifecycle != model.LifecycleService {
			out = append(out, Suggestion{Kind: "idle_box", BoxID: b.ID, AgentID: b.AgentID, Title: "Box " + b.Slug + " đang rảnh",
				Detail: fmt.Sprintf("Không có lệnh trong %s. Cho ngủ hoặc xoá.", now.Sub(b.LastActiveAt).Round(time.Minute)), SavingUSD: model.Round4(b.CostPerHour * 24)})
		}
		// 4. Oversized spec on task boxes that ran less than budget? (simple: cpu>=4 task)
		if b.Lifecycle == model.LifecycleTask && b.Spec.CPU >= 4 {
			out = append(out, Suggestion{Kind: "right_size", BoxID: b.ID, AgentID: b.AgentID, Title: "Kiểm tra cấu hình " + b.Slug,
				Detail: "Task dùng ≥4 vCPU; nếu lệnh chủ yếu chờ mạng, 1–2 vCPU thường đủ.", SavingUSD: model.Round4(b.CostPerHour / 2 * 8)})
		}
	}
	// 5. Agents with no budget.
	if p.Admin {
		agents, _ := m.Store.ListAgents(ctx)
		for _, a := range agents {
			if a.BudgetDayUSD == 0 && a.BudgetMonthUSD == 0 && a.Name != "operator" {
				out = append(out, Suggestion{Kind: "set_budget", AgentID: a.ID, Title: "Đặt ngân sách cho " + a.Name, Detail: "Agent chưa có giới hạn ngày/tháng; đặt budget để Boxa tự dừng khi chạy quá tay."})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].SavingUSD > out[j].SavingUSD })
	return out, nil
}
