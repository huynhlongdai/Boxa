#!/usr/bin/env bash
# Stops what up.sh started (core + cloudflared). The tunnel and DNS records
# stay in Cloudflare; up.sh starts serving them again.
set -uo pipefail
: "${ENV_FILE:?set ENV_FILE to the gateway env file}"
RUN_DIR=${RUN_DIR:-$(dirname "$ENV_FILE")}
for n in cloudflared core; do
  f="$RUN_DIR/$n.pid"
  if [ -s "$f" ] && kill -0 "$(cat "$f")" 2>/dev/null; then
    kill "$(cat "$f")"; for i in $(seq 50); do kill -0 "$(cat "$f")" 2>/dev/null || break; sleep 0.2; done
    kill -9 "$(cat "$f")" 2>/dev/null; echo "stopped $n ($(cat "$f"))"
  fi
  rm -f "$f"
done
