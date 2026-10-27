#!/usr/bin/env bash
# Runs cloudflared for the gateway tunnel created by setup.sh (token auth;
# ingress comes from the Cloudflare API). Foreground by default; DETACH=1
# starts it in the background (setsid nohup) and writes $PID_FILE.
set -euo pipefail
TOKEN_FILE=${TOKEN_FILE:-$HOME/.config/boxaz/tunnel-token}
CLOUDFLARED=${CLOUDFLARED:-$(command -v cloudflared || echo cloudflared)}
LOG=${LOG:-/tmp/boxaz-cloudflared.log}
PID_FILE=${PID_FILE:-/tmp/boxaz-cloudflared.pid}
[ -s "$TOKEN_FILE" ] || { echo "no tunnel token at $TOKEN_FILE (run setup.sh)" >&2; exit 1; }
export TUNNEL_TOKEN="$(cat "$TOKEN_FILE")" # env, not argv: keeps the token out of ps
args=(tunnel --no-autoupdate --metrics 127.0.0.1:0 run)
if [ "${DETACH:-0}" = 1 ]; then
  setsid nohup "$CLOUDFLARED" "${args[@]}" >"$LOG" 2>&1 < /dev/null &
  echo $! > "$PID_FILE"; echo "cloudflared pid $(cat "$PID_FILE"), log $LOG"
else
  exec "$CLOUDFLARED" "${args[@]}"
fi
