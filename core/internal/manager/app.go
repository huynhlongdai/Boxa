package manager

// v1.7 app spec + process supervisor (control plane side). The spec lives on
// the box; boxd (T1/T2) or the T3 shim's supervisor script runs it. Core
// re-sends the spec whenever the box (re)starts running — create, wake,
// clone, fork — which is idempotent on the box side (same spec = make sure
// it runs), and restarts the app after a restore.

import (
	"context"
	"fmt"
	"sync"
	"time"

	"boxaz/core/internal/model"
	"boxaz/core/internal/proto"
)

type appState struct {
	ready sync.Map // box id → time.Time (ready verified until)
}

// SetAppInput is PUT /v1/boxes/{id}/app.
type SetAppInput = model.AppSpec

// SetApp stores the app spec of a box and starts it when the box runs.
func (m *Manager) SetApp(ctx context.Context, p *Principal, id string, in model.AppSpec) (*model.AppView, error) {
	if err := p.Require(model.ScopeBoxExec); err != nil {
		return nil, err
	}
	in.Normalize()
	if msg := in.Validate(); msg != "" {
		return nil, model.EH(model.ErrInvalid, "Example: {\"repo\":\"https://github.com/heroku/node-js-sample\",\"install\":\"npm install\",\"start\":\"npm start\",\"port\":5000}.", "invalid app spec: %s", msg)
	}
	b, err := m.GetBox(ctx, p, id)
	if err != nil {
		return nil, err
	}
	lock := m.boxLock(b.ID)
	lock.Lock()
	b, err = m.Store.GetBox(ctx, b.ID)
	if err != nil {
		lock.Unlock()
		return nil, err
	}
	if !b.Active() {
		lock.Unlock()
		return nil, model.E(model.ErrInvalid, "cannot set the app of a box in status %s", b.Status)
	}
	spec := in
	b.App = &spec
	newPort := false
	if spec.Port > 0 {
		found := false
		for _, x := range b.Ports {
			found = found || x == spec.Port
		}
		if !found {
			b.Ports = append(nonNilInts(b.Ports), spec.Port)
			newPort = true
		}
		if spec.Label != "" && spec.Label != b.PortLabels[spec.Port] {
			before := namesCopy(b)
			if err := m.setPortLabel(ctx, b, spec.Port, spec.Label); err != nil {
				lock.Unlock()
				return nil, err
			}
			m.refreshNamedURLs(b, before)
			newPort = true
		}
	}
	m.ensureNames(ctx, b)
	m.Store.PutBox(ctx, b)
	lock.Unlock()
	m.app.ready.Delete(b.ID)
	m.Emit(ctx, p.Actor(), b.ID, b.AgentID, "box.app", fmt.Sprintf("app set: %s (port %d, restart %s)", trunc(spec.Start, 80), spec.Port, spec.Restart), nil)
	if newPort {
		m.publishBox(m.decorateBox(ctx, b))
	}
	view := &model.AppView{BoxID: b.ID, Spec: b.App}
	if b.Status != model.StatusRunning {
		view.Status = model.AppStatus{State: model.AppSleeping, Message: "the app starts when the box wakes"}
		return view, nil
	}
	var st model.AppStatus
	cctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	if err := m.Hub.Call(cctx, b.HostID, proto.MAppSet, proto.AppParams{BoxID: b.ID, Spec: b.App}, &st); err != nil {
		return nil, mapHostErr(err)
	}
	view.Status = st
	return view, nil
}

// DeleteApp stops and forgets the app of a box.
func (m *Manager) DeleteApp(ctx context.Context, p *Principal, id string) (*model.AppView, error) {
	if err := p.Require(model.ScopeBoxExec); err != nil {
		return nil, err
	}
	b, err := m.GetBox(ctx, p, id)
	if err != nil {
		return nil, err
	}
	lock := m.boxLock(b.ID)
	lock.Lock()
	b, _ = m.Store.GetBox(ctx, b.ID)
	b.App = nil
	m.Store.PutBox(ctx, b)
	lock.Unlock()
	m.app.ready.Delete(b.ID)
	if b.Status == model.StatusRunning {
		cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		if err := m.Hub.Call(cctx, b.HostID, proto.MAppSet, proto.AppParams{BoxID: b.ID}, nil); err != nil {
			return nil, mapHostErr(err)
		}
	}
	m.Emit(ctx, p.Actor(), b.ID, b.AgentID, "box.app", "app removed", nil)
	return &model.AppView{BoxID: b.ID, Status: model.AppStatus{State: model.AppNone}}, nil
}

// GetApp returns the spec and live status of a box's app.
func (m *Manager) GetApp(ctx context.Context, p *Principal, id string) (*model.AppView, error) {
	b, err := m.GetBox(ctx, p, id)
	if err != nil {
		return nil, err
	}
	return m.appView(ctx, b)
}

func (m *Manager) appView(ctx context.Context, b *model.Box) (*model.AppView, error) {
	view := &model.AppView{BoxID: b.ID, Spec: b.App, Status: model.AppStatus{State: model.AppNone}}
	if b.App == nil {
		return view, nil
	}
	if b.Status != model.StatusRunning {
		view.Status = model.AppStatus{State: model.AppSleeping, Message: "box is " + b.Status}
		if b.Status == model.StatusSleeping {
			view.Status.Message = "box is sleeping: the app starts again when it wakes (a request to its gateway URL wakes it)"
		}
		return view, nil
	}
	var st model.AppStatus
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := m.Hub.Call(cctx, b.HostID, proto.MAppGet, proto.BoxRef{BoxID: b.ID}, &st); err != nil {
		return nil, mapHostErr(err)
	}
	if st.State == model.AppNone {
		// the box lost the spec (e.g. a host that never got it): re-send it
		if err := m.Hub.Call(cctx, b.HostID, proto.MAppSet, proto.AppParams{BoxID: b.ID, Spec: b.App}, &st); err != nil {
			return nil, mapHostErr(err)
		}
	}
	view.Status = st
	if st.Ready {
		m.app.ready.Store(b.ID, time.Now().Add(5*time.Second))
	}
	return view, nil
}

// RestartApp restarts the app now.
func (m *Manager) RestartApp(ctx context.Context, p *Principal, id string) (*model.AppView, error) {
	if err := p.Require(model.ScopeBoxExec); err != nil {
		return nil, err
	}
	b, err := m.usable(ctx, p, id)
	if err != nil {
		return nil, err
	}
	if b.App == nil {
		return nil, model.EH(model.ErrNotFound, "Set it first: PUT /v1/boxes/{id}/app (box app set).", "box %s has no app", b.ID)
	}
	m.app.ready.Delete(b.ID)
	var st model.AppStatus
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if err := m.Hub.Call(cctx, b.HostID, proto.MAppRestart, proto.BoxRef{BoxID: b.ID}, &st); err != nil {
		var pe *proto.Error
		if !asProtoErr(err, &pe) || pe.Code != "NOT_FOUND" {
			return nil, mapHostErr(err)
		}
		if err := m.Hub.Call(cctx, b.HostID, proto.MAppSet, proto.AppParams{BoxID: b.ID, Spec: b.App}, &st); err != nil {
			return nil, mapHostErr(err)
		}
	}
	m.Emit(ctx, p.Actor(), b.ID, b.AgentID, "box.app", "app restarted", nil)
	return &model.AppView{BoxID: b.ID, Spec: b.App, Status: st}, nil
}

// AppLogs reads app output from offset (-1 = the last max bytes).
func (m *Manager) AppLogs(ctx context.Context, p *Principal, id string, offset int64, max int) (*proto.AppLogsResult, error) {
	b, err := m.GetBox(ctx, p, id)
	if err != nil {
		return nil, err
	}
	if b.App == nil {
		return nil, model.EH(model.ErrNotFound, "Set it first: PUT /v1/boxes/{id}/app (box app set).", "box %s has no app", b.ID)
	}
	if b.Status != model.StatusRunning {
		return &proto.AppLogsResult{Offset: offset}, nil
	}
	var r proto.AppLogsResult
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := m.Hub.Call(cctx, b.HostID, proto.MAppLogs, proto.AppLogsParams{BoxID: b.ID, Offset: offset, Max: max}, &r); err != nil {
		return nil, mapHostErr(err)
	}
	return &r, nil
}

// appEnsure re-sends the spec after the box (re)started running: the box side
// starts the app when its process is gone and leaves a running one alone.
func (m *Manager) appEnsure(boxID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	b, err := m.Store.GetBox(ctx, boxID)
	if err != nil || b.App == nil || b.Status != model.StatusRunning {
		return
	}
	m.app.ready.Delete(b.ID)
	var st model.AppStatus
	if err := m.Hub.Call(ctx, b.HostID, proto.MAppSet, proto.AppParams{BoxID: b.ID, Spec: b.App}, &st); err != nil {
		m.Emit(ctx, "system", b.ID, b.AgentID, "box.app", "app could not be started: "+err.Error(), nil)
		return
	}
}

// appAfterRestore restarts the app on the restored files.
func (m *Manager) appAfterRestore(boxID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	b, err := m.Store.GetBox(ctx, boxID)
	if err != nil || b.App == nil || b.Status != model.StatusRunning {
		return
	}
	m.app.ready.Delete(b.ID)
	if m.Hub.IsVirtual(b.HostID) {
		// boxd restarts by itself on box.restore; the T3 script needs a nudge
		m.Hub.Call(ctx, b.HostID, proto.MAppSet, proto.AppParams{BoxID: b.ID, Spec: b.App}, nil)
		if err := m.Hub.Call(ctx, b.HostID, proto.MAppRestart, proto.BoxRef{BoxID: b.ID}, nil); err == nil {
			m.Emit(ctx, "system", b.ID, b.AgentID, "box.app", "app restarted after restore", nil)
		}
		return
	}
	m.Hub.Call(ctx, b.HostID, proto.MAppEnsure, proto.BoxRef{BoxID: b.ID}, nil)
	m.Emit(ctx, "system", b.ID, b.AgentID, "box.app", "app restarted after restore", nil)
}

// AppGate reports whether gateway traffic to port may flow: true when the box
// has no app on that port or its app is ready. Readiness is cached briefly.
func (m *Manager) AppGate(ctx context.Context, b *model.Box, port int) (bool, string) {
	if b.App == nil || b.App.Port != port {
		return true, ""
	}
	if until, ok := m.app.ready.Load(b.ID); ok && time.Now().Before(until.(time.Time)) {
		return true, ""
	}
	var st model.AppStatus
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := m.Hub.Call(cctx, b.HostID, proto.MAppGet, proto.BoxRef{BoxID: b.ID}, &st); err != nil {
		return false, "app status unavailable: " + err.Error()
	}
	if st.State == model.AppNone || st.State == model.AppStopped {
		go m.appEnsure(b.ID)
		return false, "app is starting"
	}
	if st.Ready {
		m.app.ready.Store(b.ID, time.Now().Add(5*time.Second))
		return true, ""
	}
	msg := "app is " + st.State
	if st.Message != "" {
		msg += ": " + st.Message
	}
	return false, msg
}

// inheritV17 copies the v1.7 settings of src onto a clone / fork.
func inheritV17(dst, src *model.Box) {
	if src.App != nil {
		a := *src.App
		dst.App = &a
	}
	dst.WakeOnRequest = src.WakeOnRequest
	// v1.9: the copy starts from the source's name (made unique by
	// ensureNames, e.g. demo-app → demo-app-blue) and keeps its port labels
	dst.Subdomain = src.Subdomain
	if src.PortLabels != nil {
		dst.PortLabels = map[int]string{}
		for k, v := range src.PortLabels {
			dst.PortLabels[k] = v
		}
	}
	if src.PortAccess != nil {
		dst.PortAccess = map[int]string{}
		for k, v := range src.PortAccess {
			dst.PortAccess[k] = v
		}
	}
}

func asProtoErr(err error, pe **proto.Error) bool {
	if e, ok := err.(*proto.Error); ok {
		*pe = e
		return true
	}
	return false
}

// PatchBoxInput is PATCH /v1/boxes/{id} (v1.7).
type PatchBoxInput struct {
	WakeOnRequest *bool `json:"wake_on_request,omitempty"`
	// Subdomain (v1.9) renames the box's gateway address
	// (https://<subdomain>.<domain>/); the old name stops answering at once.
	Subdomain *string `json:"subdomain,omitempty"`
	// ForwardPorts (v1.10) replaces the extra in-box ports the TCP stream /
	// box forward may reach (exposed ports and 22 always are).
	ForwardPorts *[]int `json:"forward_ports,omitempty"`
}

// PatchBox changes per-box settings.
func (m *Manager) PatchBox(ctx context.Context, p *Principal, id string, in PatchBoxInput) (*model.Box, error) {
	if err := p.Require(model.ScopeBoxExec); err != nil {
		return nil, err
	}
	b, err := m.GetBox(ctx, p, id)
	if err != nil {
		return nil, err
	}
	lock := m.boxLock(b.ID)
	lock.Lock()
	b, err = m.Store.GetBox(ctx, b.ID)
	if err != nil {
		lock.Unlock()
		return nil, err
	}
	if in.WakeOnRequest != nil {
		v := *in.WakeOnRequest
		b.WakeOnRequest = &v
	}
	if in.ForwardPorts != nil {
		if err := setForwardPorts(b, *in.ForwardPorts); err != nil {
			lock.Unlock()
			return nil, err
		}
	}
	oldName := b.Subdomain
	if in.Subdomain != nil && model.Slugify(*in.Subdomain, 0) != b.Subdomain {
		before := namesCopy(b)
		if err := m.claimExplicitName(ctx, b, *in.Subdomain); err != nil {
			lock.Unlock()
			return nil, err
		}
		m.refreshNamedURLs(b, before)
	}
	m.Store.PutBox(ctx, b)
	lock.Unlock()
	if b.Subdomain != oldName {
		m.Emit(ctx, p.Actor(), b.ID, b.AgentID, "box.settings", fmt.Sprintf("gateway name %s → %s", oldName, b.Subdomain), nil)
	}
	if in.WakeOnRequest != nil {
		m.Emit(ctx, p.Actor(), b.ID, b.AgentID, "box.settings", fmt.Sprintf("wake_on_request → %v", *in.WakeOnRequest), nil)
	}
	if in.ForwardPorts != nil {
		m.Emit(ctx, p.Actor(), b.ID, b.AgentID, "box.settings", fmt.Sprintf("forward ports → %v", b.ForwardPorts), nil)
	}
	b = m.decorateBox(ctx, b)
	m.publishBox(b)
	return b, nil
}
