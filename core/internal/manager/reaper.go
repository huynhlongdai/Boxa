package manager

import (
	"context"
	"fmt"
	"time"

	"boxa/core/internal/model"
	"boxa/core/internal/provider"
	"boxa/core/internal/store"
)

// reap runs every 30s (spec M11):
//   - session box idle > idle_minutes        → sleep
//   - task box past TTL                      → terminate
//   - orphan (no agent call/activity > 24h)  → snapshot + terminate (service boxes exempt)
//   - burst host empty > 10 min              → destroy at provider
//   - host tunnel down > 2 min               → host down + "host_down" approval
func (m *Manager) reap(ctx context.Context) error {
	now := time.Now().UTC()
	// Snapshot retention (keep_snapshots / snapshot_retention_days), hourly.
	m.mu.Lock()
	due := now.Sub(m.lastRetention) > time.Hour
	if due {
		m.lastRetention = now
	}
	m.mu.Unlock()
	if due {
		m.applySnapshotRetention(ctx, "")
	}
	boxes, err := m.Store.ListBoxes(ctx, store.BoxFilter{Statuses: []string{model.StatusRunning, model.StatusSleeping}})
	if err != nil {
		return err
	}
	agents := map[string]*model.Agent{}
	if as, err := m.Store.ListAgents(ctx); err == nil {
		for _, a := range as {
			agents[a.ID] = a
		}
	}
	for _, b := range boxes {
		if b.TakenOver {
			continue
		}
		idleFor := now.Sub(b.LastActiveAt)
		// Task TTL.
		if b.Lifecycle == model.LifecycleTask && b.TTLSeconds > 0 && now.Sub(b.CreatedAt) > time.Duration(b.TTLSeconds)*time.Second {
			m.terminateBox(ctx, b, "reaper", fmt.Sprintf("task TTL %ds reached", b.TTLSeconds))
			continue
		}
		// Orphans.
		if b.Lifecycle != model.LifecycleService {
			lastCall := b.LastActiveAt
			if a := agents[b.AgentID]; a != nil && a.LastCallAt != nil && a.LastCallAt.After(lastCall) {
				lastCall = *a.LastCallAt
			}
			if now.Sub(lastCall) > m.Cfg.OrphanAfter {
				if m.Hub.Connected(b.HostID) {
					m.snapshotBox(ctx, b, "reaper")
				}
				m.terminateBox(ctx, b, "reaper", fmt.Sprintf("orphan: no agent activity for %s (snapshot kept)", now.Sub(lastCall).Round(time.Minute)))
				continue
			}
		}
		// Session idle → sleep.
		if b.Lifecycle == model.LifecycleSession && b.Status == model.StatusRunning && b.IdleMinutes > 0 && idleFor > time.Duration(b.IdleMinutes)*time.Minute {
			if m.Hub.Connected(b.HostID) {
				m.sleepBox(ctx, b, "reaper", fmt.Sprintf("idle %s > %d min", idleFor.Round(time.Second), b.IdleMinutes))
			}
		}
	}
	return m.reapHosts(ctx, now)
}

func (m *Manager) reapHosts(ctx context.Context, now time.Time) error {
	hosts, err := m.Store.ListHosts(ctx)
	if err != nil {
		return err
	}
	active, _ := m.Store.ListBoxes(ctx, store.BoxFilter{})
	count := map[string]int{}
	for _, b := range active {
		if b.Active() {
			count[b.HostID]++
		}
	}
	for _, h := range hosts {
		if h.Status == "deleted" {
			continue
		}
		connected := m.Hub.Connected(h.ID)
		// Host liveness.
		if !connected && (h.Status == "ready" || h.Status == "draining") {
			last := h.CreatedAt
			if h.LastSeenAt != nil {
				last = *h.LastSeenAt
			}
			if now.Sub(last) > m.Cfg.HostDownAfter {
				h.Status = "down"
				m.Store.PutHost(ctx, h)
				m.Emit(ctx, "reaper", "", "", "host.down", "host "+h.ID+" unreachable since "+last.Format(time.RFC3339), nil)
				m.createApproval(ctx, "host_down", "host_down:"+h.ID+":"+last.Format(time.RFC3339),
					"Host "+h.ID+" mất kết nối", fmt.Sprintf("%s %s (%s) không gửi tín hiệu từ %s; %d box bị ảnh hưởng. Kiểm tra máy hoặc xoá host.", h.Provider, h.SKU, h.IP, last.Format(time.RFC3339), count[h.ID]), nil, "", "")
			}
			continue
		}
		if h.Status == "provisioning" && !connected && now.Sub(h.CreatedAt) > 20*time.Minute && h.Provider != "local" {
			h.Status = "down"
			m.Store.PutHost(ctx, h)
			m.createApproval(ctx, "host_down", "host_never_up:"+h.ID, "Host "+h.ID+" không khởi động", "Host mới thuê chưa kết nối sau 20 phút. Kiểm tra cloud-init hoặc xoá host để ngừng tính tiền.", ptr(h.PriceHour), "", "")
			continue
		}
		// Empty burst hosts.
		if h.Billing != "burst" || h.Status != "ready" {
			continue
		}
		if count[h.ID] > 0 {
			if h.EmptySince != nil {
				h.EmptySince = nil
				m.Store.PutHost(ctx, h)
			}
			continue
		}
		if h.EmptySince == nil {
			t := now
			h.EmptySince = &t
			m.Store.PutHost(ctx, h)
			continue
		}
		if now.Sub(*h.EmptySince) > m.Cfg.BurstEmptyAfter {
			m.destroyHost(ctx, h, "reaper", fmt.Sprintf("burst host empty for %s", now.Sub(*h.EmptySince).Round(time.Minute)))
		}
	}
	return nil
}

var _ = provider.Quote
