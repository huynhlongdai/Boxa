package manager

// Egress allowlist (spec M10 network policy). Effective policy per box:
//   1. the box's own `egress` (box.yaml / API), which must stay within the
//      agent's policy.egress_allowlist when that list is set;
//   2. else the agent's egress_allowlist (deny everything else);
//   3. else settings.egress_deny_by_default → offline box (no egress at all);
//   4. else unrestricted (nil).
// boxd enforces it (incus: network ACL / nftables; local: warning only).

import (
	"context"
	"fmt"
	"time"

	"boxaz/core/internal/egress"
	"boxaz/core/internal/model"
	"boxaz/core/internal/proto"
)

func (m *Manager) effectiveEgress(ctx context.Context, agent *model.Agent, req *egress.Policy) (*model.BoxEgress, error) {
	settings := m.Settings(ctx)
	ceiling := agent.Policy.EgressAllowlist
	if req != nil {
		allow, err := egress.Normalize(req.Allow)
		if err != nil {
			return nil, model.EH(model.ErrInvalid, "Use domains (pypi.org, *.github.com), IPs or CIDRs, optionally with :port.", "invalid egress entry: %s", err.Error())
		}
		if ok, bad := egress.Within(allow, ceiling); !ok {
			return nil, model.EH(model.ErrPolicyDenied, "Ask the operator to add it to the agent's egress_allowlist.", "egress %q is outside the agent's allowlist", bad)
		}
		deny := req.DenyByDefault || len(allow) > 0
		if !deny && len(ceiling) > 0 {
			// an agent allowlist cannot be widened by an empty box policy
			return &model.BoxEgress{Allow: nonNilStrs(ceiling), DenyByDefault: true, Source: "agent"}, nil
		}
		if !deny && !settings.EgressDenyByDefault {
			return nil, nil
		}
		return &model.BoxEgress{Allow: allow, DenyByDefault: true, Source: "box"}, nil
	}
	if len(ceiling) > 0 {
		allow, err := egress.Normalize(ceiling)
		if err != nil {
			return nil, model.EH(model.ErrInvalid, "Fix the agent's policy.egress_allowlist.", "invalid agent egress_allowlist: %s", err.Error())
		}
		return &model.BoxEgress{Allow: allow, DenyByDefault: true, Source: "agent"}, nil
	}
	if settings.EgressDenyByDefault {
		return &model.BoxEgress{Allow: []string{}, DenyByDefault: true, Source: "settings"}, nil
	}
	return nil, nil
}

// applyEgress pushes the box policy to its host and records the result.
// Caller holds the box lock or owns the box (provisioning).
func (m *Manager) applyEgress(ctx context.Context, b *model.Box, clear bool) error {
	block := !m.Cfg.AllowMetadata
	if b.Egress == nil && !clear {
		if block && !m.Hub.IsVirtual(b.HostID) {
			m.applyMetadataBlock(ctx, b)
		}
		return nil
	}
	params := proto.EgressParams{BoxID: b.ID, BlockMetadata: block}
	if b.Egress != nil {
		params.Allow, params.DenyByDefault = b.Egress.Allow, b.Egress.DenyByDefault
	}
	var res proto.EgressResult
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	err := m.Hub.Call(cctx, b.HostID, proto.MEgress, params, &res)
	cancel()
	if err != nil {
		return mapHostErr(err)
	}
	if b.Egress != nil {
		now := time.Now().UTC()
		b.Egress.Enforced, b.Egress.Mode, b.Egress.Warning, b.Egress.Rules, b.Egress.AppliedAt = res.Enforced, res.Mode, res.Warning, res.Rules, &now
		m.Store.PutBox(ctx, b)
		typ := "box.egress_applied"
		msg := fmt.Sprintf("egress allowlist enforced via %s (%d rules)", res.Mode, res.Rules)
		if !res.Enforced {
			typ, msg = "box.egress_not_enforced", "egress allowlist NOT enforced: "+res.Warning
		}
		m.EmitData(ctx, "system", b.ID, b.AgentID, typ, msg, nil, map[string]any{"enforced": res.Enforced, "mode": res.Mode, "rules": res.Rules})
	}
	return nil
}

// applyMetadataBlock (v1.11) installs only the cloud-metadata drop rules on a
// box without an allowlist. Best effort: a host that cannot filter keeps the
// box (it is reported as an event).
func (m *Manager) applyMetadataBlock(ctx context.Context, b *model.Box) {
	var res proto.EgressResult
	cctx, cancel := context.WithTimeout(ctx, time.Minute)
	err := m.Hub.Call(cctx, b.HostID, proto.MEgress, proto.EgressParams{BoxID: b.ID, BlockMetadata: true}, &res)
	cancel()
	if err != nil {
		m.Emit(ctx, "system", b.ID, b.AgentID, "box.egress_not_enforced", "cloud metadata block not applied: "+err.Error(), nil)
	}
}

// EgressInput is PUT /boxes/:id/egress.
type EgressInput struct {
	Allow         []string `json:"allow"`
	DenyByDefault bool     `json:"deny_by_default"`
}

// SetEgress changes a running/sleeping box's egress policy. An empty input
// falls back to the agent/settings default.
func (m *Manager) SetEgress(ctx context.Context, p *Principal, id string, in EgressInput) (*model.Box, error) {
	if err := p.Require(model.ScopeBoxCreate); err != nil {
		return nil, err
	}
	b, err := m.GetBox(ctx, p, id)
	if err != nil {
		return nil, err
	}
	if b.Status != model.StatusRunning && b.Status != model.StatusSleeping {
		return nil, model.E(model.ErrInvalid, "cannot change egress of a box in status %s", b.Status)
	}
	agent, err := m.Store.GetAgent(ctx, b.AgentID)
	if err != nil {
		return nil, notFound("agent", b.AgentID)
	}
	var req *egress.Policy
	if in.DenyByDefault || len(in.Allow) > 0 {
		req = &egress.Policy{Allow: in.Allow, DenyByDefault: in.DenyByDefault}
	}
	eff, err := m.effectiveEgress(ctx, agent, req)
	if err != nil {
		return nil, err
	}
	lock := m.boxLock(b.ID)
	lock.Lock()
	defer lock.Unlock()
	b, _ = m.Store.GetBox(ctx, b.ID)
	b.Egress = eff
	m.Store.PutBox(ctx, b)
	if !m.Hub.Connected(b.HostID) {
		return nil, model.EH(model.ErrProviderFailed, "The host is offline; retry later.", "host %s offline", b.HostID)
	}
	if err := m.applyEgress(ctx, b, true); err != nil {
		return nil, err
	}
	m.saveBoxYAML(ctx, b, "")
	m.publishBox(b)
	return m.decorateBox(ctx, b), nil
}
