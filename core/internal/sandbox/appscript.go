package sandbox

// v1.7 app supervisor for exec-only (T3) sandboxes, where boxd does not run:
// a small POSIX sh script launched with setsid through the provider's exec
// API. It clones + installs once, runs the start command, restarts it with
// exponential backoff per the restart policy, polls the readiness check and
// keeps its state in small files under $HOME/.boxd/app that the shim reads
// back with one exec. Core re-launches it after a wake, restore or clone when
// its process is gone. Names stay neutral (.boxd, supervisor).

import (
	"context"
	"encoding/base64"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"boxaz/core/internal/model"
	"boxaz/core/internal/proto"
)

const appDir = `"$HOME/.boxd/app"`

// SupervisorScript renders sup.sh for a normalized spec; workdir is the
// sandbox path of spec.Workdir.
func SupervisorScript(sp model.AppSpec, workdir string) string {
	var b strings.Builder
	w := func(s string) { b.WriteString(s); b.WriteByte('\n') }
	w(`#!/bin/sh`)
	// the workdir lives in its own file (written in clear by appSet) so
	// that path rewriting by an exec layer applies to it too
	w(`D=` + appDir + `; L="$D/app.log"; WD=$(cat "$D/workdir" 2>/dev/null); [ -n "$WD" ] || WD=` + shq(workdir))
	w(`st() { printf '%s' "$1" > "$D/state"; printf '%s' "$2" > "$D/msg"; }`)
	w(`lg() { echo "[supervisor $(date -u +%H:%M:%S)] $*" >> "$L"; }`)
	w(`rot() { s=$(wc -c < "$L" 2>/dev/null || echo 0); [ "$s" -gt 5000000 ] && mv "$L" "$L.1"; return 0; }`)
	w(`now() { date +%s; }`)
	keys := make([]string, 0, len(sp.Env))
	for k := range sp.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		w(`export ` + k + `=` + shq(sp.Env[k]))
	}
	if sp.Port > 0 {
		if _, ok := sp.Env["PORT"]; !ok {
			w(`export PORT=` + strconv.Itoa(sp.Port))
		}
	}
	// readiness check
	switch {
	case sp.Ready != nil && sp.Ready.HTTP != "" && sp.Port > 0:
		u := shq("http://127.0.0.1:" + strconv.Itoa(sp.Port) + sp.Ready.HTTP)
		w(`ready() { if command -v curl >/dev/null 2>&1; then c=$(curl -s -o /dev/null --max-time 2 -w '%{http_code}' ` + u + ` 2>/dev/null); case "$c" in [1234]??) return 0;; esac; return 1; fi; wget -q -O /dev/null -T 2 ` + u + ` 2>/dev/null; }`)
	case sp.Ready != nil && sp.Ready.TCP && sp.Port > 0:
		p := strconv.Itoa(sp.Port)
		w(`ready() { if command -v bash >/dev/null 2>&1; then bash -c 'exec 3<>/dev/tcp/127.0.0.1/` + p + `' 2>/dev/null; return $?; fi; if command -v nc >/dev/null 2>&1; then nc -z 127.0.0.1 ` + p + ` 2>/dev/null; return $?; fi; curl -s -o /dev/null --max-time 2 http://127.0.0.1:` + p + `/ 2>/dev/null; r=$?; [ $r -ne 7 ] && [ $r -ne 28 ]; }`)
	default:
		w(`ready() { return 0; }`)
	}
	maxd := sp.BackoffMaxS
	if maxd <= 0 {
		maxd = 30
	}
	w(`delay=1; MAXD=` + strconv.Itoa(maxd))
	w(`bump() { delay=$((delay*2)); [ $delay -gt $MAXD ] && delay=$MAXD; return 0; }`)
	// prepare
	w(`while :; do`)
	w(`  [ -f "$D/stop" ] && { st stopped ""; exit 0; }`)
	if sp.Repo != "" {
		ref := ""
		if sp.Ref != "" {
			ref = "--branch " + shq(sp.Ref) + " "
		}
		w(`  if [ ! -d "$WD/.git" ]; then st cloning ""; lg "git clone ` + strings.ReplaceAll(sp.Repo, `"`, ``) + `"; mkdir -p "$(dirname "$WD")"; if ! git clone -q --depth 1 ` + ref + shq(sp.Repo) + ` "$WD" >> "$L" 2>&1; then st backoff "git clone failed"; lg "git clone failed; retry in ${delay}s"; rm -rf "$WD"; sleep $delay; bump; continue; fi; fi`)
	} else {
		w(`  mkdir -p "$WD"`)
	}
	if sp.Install != "" {
		w(`  if [ "$(cat "$D/installed" 2>/dev/null)" != ` + shq(sp.Hash()) + ` ]; then st installing ""; lg "install: ` + strings.ReplaceAll(sp.Install, `"`, `'`) + `"; if ! (cd "$WD" && sh -c ` + shq(sp.Install) + `) >> "$L" 2>&1 < /dev/null; then st backoff "install failed"; lg "install failed; retry in ${delay}s"; sleep $delay; bump; continue; fi; printf '%s' ` + shq(sp.Hash()) + ` > "$D/installed"; fi`)
	}
	w(`  break`)
	w(`done`)
	w(`delay=1`)
	w(`while :; do`)
	w(`  [ -f "$D/stop" ] && { st stopped ""; exit 0; }`)
	w(`  rot; T0=$(now); now > "$D/started_at"; : > "$D/ready_at"; st starting ""; lg "start: ` + strings.ReplaceAll(sp.Start, `"`, `'`) + `"`)
	w(`  (cd "$WD" && exec setsid sh -c ` + shq(sp.Start) + `) >> "$L" 2>&1 < /dev/null &`)
	w(`  C=$!; echo $C > "$D/child.pid"`)
	w(`  ( while kill -0 $C 2>/dev/null; do if ready; then kill -0 $C 2>/dev/null && { st ready ""; now > "$D/ready_at"; lg ready; }; exit 0; fi; sleep 0.5; done ) &`)
	w(`  RP=$!; wait $C; code=$?; kill $RP 2>/dev/null; : > "$D/ready_at"`)
	w(`  printf '%s' $code > "$D/last_exit"; ran=$(( $(now) - T0 ))`)
	w(`  [ -f "$D/stop" ] && { st stopped ""; exit 0; }`)
	w(`  lg "exited with code $code after ${ran}s"`)
	switch sp.Restart {
	case model.RestartNever:
		w(`  st exited "exited with code $code"; exit 0`)
	case model.RestartOnFailure:
		w(`  [ "$code" = 0 ] && { st exited "exited with code 0"; exit 0; }`)
	}
	w(`  [ $ran -gt 60 ] && delay=1`)
	w(`  n=$(cat "$D/restarts" 2>/dev/null || echo 0); echo $((n+1)) > "$D/restarts"`)
	w(`  st backoff "exited with code $code; restarting in ${delay}s"; lg "restarting in ${delay}s"; sleep $delay; bump`)
	w(`done`)
	return b.String()
}

// killApp stops the supervisor and the app's process group.
const killApp = `D=` + appDir + `; for f in sup.pid child.pid; do P=$(cat "$D/$f" 2>/dev/null); [ -n "$P" ] && { kill -TERM -"$P" 2>/dev/null; kill -TERM "$P" 2>/dev/null; }; done; sleep 0.3; for f in sup.pid child.pid; do P=$(cat "$D/$f" 2>/dev/null); [ -n "$P" ] && { kill -KILL -"$P" 2>/dev/null; kill -KILL "$P" 2>/dev/null; }; rm -f "$D/$f"; done; `

// launchApp starts sup.sh (setsid, detached) unless it runs already.
const launchApp = `D=` + appDir + `; if [ -f "$D/sup.pid" ] && kill -0 "$(cat "$D/sup.pid")" 2>/dev/null; then echo sup=running; else rm -f "$D/stop"; nohup setsid sh "$D/sup.sh" > /dev/null 2>&1 < /dev/null & echo $! > "$D/sup.pid"; echo sup=started; fi; `

// statusApp prints the state files as key=value lines.
const statusApp = `D=` + appDir + `; [ -f "$D/sup.sh" ] || { echo none=1; exit 0; }; ` +
	`for k in state msg restarts last_exit started_at ready_at installed; do printf '%s=%s\n' "$k" "$(head -c 300 "$D/$k" 2>/dev/null | tr '\n' ' ')"; done; ` +
	`P=$(cat "$D/sup.pid" 2>/dev/null); [ -n "$P" ] && kill -0 "$P" 2>/dev/null && echo sup=alive || echo sup=dead; ` +
	`C=$(cat "$D/child.pid" 2>/dev/null); [ -n "$C" ] && kill -0 "$C" 2>/dev/null && echo child=$C || echo child=; [ -f "$D/stop" ] && echo stop=1; echo hash=$(cat "$D/spec.hash" 2>/dev/null)`

func (s *Shim) appExec(ctx context.Context, box, script string, timeout int) (*ExecResult, error) {
	res, err := s.exec(ctx, box, script, "/", nil, timeout)
	if err != nil {
		return nil, err
	}
	return res, nil
}

// appWorkdir maps the spec's box path into the sandbox.
func (s *Shim) appWorkdir(sp model.AppSpec) string { return s.boxPath(sp.Workdir) }

func (s *Shim) appSet(ctx context.Context, p proto.AppParams) (*model.AppStatus, error) {
	if p.Spec == nil {
		if _, err := s.appExec(ctx, p.BoxID, killApp+`rm -rf "$D"; echo removed`, 30); err != nil {
			return nil, err
		}
		return &model.AppStatus{State: model.AppNone, Supervisor: "script"}, nil
	}
	sp := *p.Spec
	sp.Normalize()
	if msg := sp.Validate(); msg != "" {
		return nil, &proto.Error{Code: "INVALID", Message: msg}
	}
	hash := sp.FullHash()
	// same spec and supervisor alive: nothing to do
	if st, err := s.appStatus(ctx, p.BoxID); err == nil && st.SpecHash == hash && st.State != model.AppStopped && st.State != model.AppNone {
		return st, nil
	}
	script := base64.StdEncoding.EncodeToString([]byte(SupervisorScript(sp, s.appWorkdir(sp))))
	cmd := killApp + `mkdir -p "$D" && printf %s ` + shq(script) + ` | base64 -d > "$D/sup.sh" && printf %s ` + shq(s.appWorkdir(sp)) + ` > "$D/workdir" && printf %s ` + shq(hash) + ` > "$D/spec.hash" && : > "$D/state" && rm -f "$D/stop" && ` + launchApp
	res, err := s.appExec(ctx, p.BoxID, cmd, 60)
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, &proto.Error{Code: "SETUP_FAILED", Message: "supervisor launch failed: " + trim(res.Stderr+res.Stdout, 300)}
	}
	time.Sleep(300 * time.Millisecond)
	return s.appStatus(ctx, p.BoxID)
}

func (s *Shim) appStatus(ctx context.Context, box string) (*model.AppStatus, error) {
	res, err := s.appExec(ctx, box, statusApp, 30)
	if err != nil {
		return nil, err
	}
	return parseAppStatus(res.Stdout), nil
}

// parseAppStatus reads statusApp's output.
func parseAppStatus(out string) *model.AppStatus {
	kv := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			kv[k] = strings.TrimSpace(v)
		}
	}
	st := &model.AppStatus{Supervisor: "script", State: model.AppNone}
	if kv["none"] == "1" {
		return st
	}
	st.State = kv["state"]
	st.Message = kv["msg"]
	st.SpecHash = kv["hash"]
	st.Restarts, _ = strconv.Atoi(kv["restarts"])
	if v, err := strconv.Atoi(kv["last_exit"]); err == nil {
		st.LastExit = &v
	}
	if v, err := strconv.ParseInt(kv["started_at"], 10, 64); err == nil && v > 0 {
		t := time.Unix(v, 0).UTC()
		st.StartedAt = &t
	}
	if v, err := strconv.ParseInt(kv["ready_at"], 10, 64); err == nil && v > 0 {
		t := time.Unix(v, 0).UTC()
		st.ReadyAt = &t
	}
	st.PID, _ = strconv.Atoi(kv["child"])
	if kv["sup"] != "alive" {
		// supervisor gone (cold boot, killed): the app is not supervised
		if kv["stop"] == "1" || st.State == model.AppExited {
			if st.State != model.AppExited {
				st.State = model.AppStopped
			}
		} else {
			st.State = model.AppStopped
			st.Message = "supervisor is not running"
		}
		st.PID = 0
	}
	if st.State == "" {
		st.State = model.AppStarting
	}
	st.Ready = st.State == model.AppReadyState
	return st
}

func (s *Shim) appLogs(ctx context.Context, p proto.AppLogsParams) (*proto.AppLogsResult, error) {
	max := p.Max
	if max <= 0 || max > 256<<10 {
		max = 64 << 10
	}
	var script string
	if p.Offset < 0 {
		script = fmt.Sprintf(`L=%s/app.log; S=$(wc -c < "$L" 2>/dev/null || echo 0); echo "SIZE=$S"; tail -c %d "$L" 2>/dev/null`, appDir, max)
	} else {
		script = fmt.Sprintf(`L=%s/app.log; S=$(wc -c < "$L" 2>/dev/null || echo 0); echo "SIZE=$S"; [ "$S" -ge %d ] && tail -c +%d "$L" 2>/dev/null | head -c %d`, appDir, p.Offset, p.Offset+1, max)
	}
	res, err := s.appExec(ctx, p.BoxID, script, 30)
	if err != nil {
		return nil, err
	}
	first, rest, _ := strings.Cut(res.Stdout, "\n")
	size, _ := strconv.ParseInt(strings.TrimPrefix(strings.TrimSpace(first), "SIZE="), 10, 64)
	out := &proto.AppLogsResult{Data: rest}
	switch {
	case p.Offset < 0:
		out.Offset = size
	case size < p.Offset: // rotated / truncated
		out.Offset, out.Truncated, out.Data = 0, true, ""
	default:
		out.Offset = p.Offset + int64(len(rest))
	}
	return out, nil
}

// appCall implements the app RPCs for the shim.
func (s *Shim) appCall(ctx context.Context, method string, params, out any) error {
	switch method {
	case proto.MAppSet:
		var p proto.AppParams
		if err := decodeInto(params, &p); err != nil {
			return err
		}
		st, err := s.appSet(ctx, p)
		if err != nil {
			return err
		}
		return encodeOut(st, out)
	case proto.MAppLogs:
		var p proto.AppLogsParams
		if err := decodeInto(params, &p); err != nil {
			return err
		}
		r, err := s.appLogs(ctx, p)
		if err != nil {
			return err
		}
		return encodeOut(r, out)
	}
	var p proto.BoxRef
	if err := decodeInto(params, &p); err != nil {
		return err
	}
	switch method {
	case proto.MAppRestart:
		res, err := s.appExec(ctx, p.BoxID, `[ -f `+appDir+`/sup.sh ] || { echo no-app; exit 3; }; `+killApp+launchApp, 60)
		if err != nil {
			return err
		}
		if res.ExitCode == 3 {
			return &proto.Error{Code: "NOT_FOUND", Message: "this box has no app spec (PUT /v1/boxes/{id}/app first)"}
		}
		time.Sleep(300 * time.Millisecond)
	case proto.MAppEnsure:
		if _, err := s.appExec(ctx, p.BoxID, `[ -f `+appDir+`/sup.sh ] || exit 0; [ -f `+appDir+`/stop ] && exit 0; `+launchApp, 60); err != nil {
			return err
		}
	}
	st, err := s.appStatus(ctx, p.BoxID)
	if err != nil {
		return err
	}
	return encodeOut(st, out)
}
