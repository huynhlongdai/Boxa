#!/usr/bin/env bash
# One command to (re)start the gateway on a host: boxmes-core + cloudflared,
# both detached (setsid nohup), pid files next to the env file.
#
#   ENV_FILE=/path/gateway.env deploy/cloudflare-tunnel/up.sh
#
# gateway.env (KEY=VALUE lines, sourced) needs at least:
#   BOXMES_ADDR=127.0.0.1:8091            # the port the tunnel ingress points at
#   BOXMES_PUBLIC_URL=https://gw.example.com
#   BOXMES_GATEWAY_DOMAIN=example.com
#   BOXMES_DATA_DIR=/var/lib/boxmes        # BOXMES_DIST_DIR for the ingress agent
#   TOKEN_FILE=/path/tunnel-token        # written by setup.sh
# optional: BOXMES_CORE_BIN (default: bin/boxmes-core of this checkout),
#           CLOUDFLARED, BOXMES_ADMIN_KEY / BOXMES_ADMIN_KEY_FILE, provider keys.
# Stop with down.sh (same ENV_FILE).
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
: "${ENV_FILE:?set ENV_FILE to the gateway env file}"
set -a; . "$ENV_FILE"; set +a
RUN_DIR=${RUN_DIR:-$(dirname "$ENV_FILE")}
CORE_BIN=${BOXMES_CORE_BIN:-$HERE/../../bin/boxmes-core}
"$HERE/down.sh" >/dev/null 2>&1 || true
[ -x "$CORE_BIN" ] || { echo "no core binary at $CORE_BIN (go build -o bin/ ./cmd/...)" >&2; exit 1; }
setsid nohup "$CORE_BIN" >>"$RUN_DIR/core.log" 2>&1 < /dev/null &
echo $! > "$RUN_DIR/core.pid"
ADDR=${BOXMES_ADDR:-:8080}; LOCAL="http://127.0.0.1:${ADDR##*:}"
for i in $(seq 100); do curl -sf "$LOCAL/v1/health" >/dev/null && break; sleep 0.1; done
curl -sf "$LOCAL/v1/health" >/dev/null || { echo "core did not start; see $RUN_DIR/core.log" >&2; exit 1; }
echo "core pid $(cat "$RUN_DIR/core.pid") on $LOCAL (log $RUN_DIR/core.log)"
DETACH=1 LOG="$RUN_DIR/cloudflared.log" PID_FILE="$RUN_DIR/cloudflared.pid" TOKEN_FILE="$TOKEN_FILE" "$HERE/run.sh"
if [ -n "${BOXMES_PUBLIC_URL:-}" ]; then
  for i in $(seq 60); do curl -sf "$BOXMES_PUBLIC_URL/v1/health" >/dev/null && break; sleep 1; done
  if curl -sf "$BOXMES_PUBLIC_URL/v1/health" >/dev/null; then echo "gateway up: $BOXMES_PUBLIC_URL"; else echo "WARN: $BOXMES_PUBLIC_URL not answering yet (see $RUN_DIR/cloudflared.log)" >&2; fi
fi
