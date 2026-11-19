#!/bin/sh
# Render entrypoint: fix ownership of the data dir (a disk on paid plans,
# ephemeral on free), start cloudflared
# (restarted if it exits) when TUNNEL_TOKEN is set, then run core as user app.
set -eu
DATA=${BOXAZ_DATA_DIR:-/data}
mkdir -p "$DATA" && chown -R app:app "$DATA"
# Render routes to $PORT; the tunnel ingress points at localhost:<BOXAZ_ADDR port>.
export BOXAZ_ADDR="${BOXAZ_ADDR:-:${PORT:-8091}}"
if [ -n "${TUNNEL_TOKEN:-}" ]; then
  (
    while :; do
      su-exec app cloudflared tunnel --no-autoupdate --metrics 127.0.0.1:0 run || true
      echo "cloudflared exited; restarting in 5s" >&2; sleep 5
    done
  ) &
fi
# Free plan: Render spins a service down after 15 min without inbound traffic,
# and tunnel traffic does not count. Ping our own public URL every 10 min
# (KEEPALIVE_S=0 turns it off; RENDER_EXTERNAL_URL is set by Render).
if [ -n "${RENDER_EXTERNAL_URL:-}" ] && [ "${KEEPALIVE_S:-600}" != 0 ]; then
  ( sleep 60; while :; do curl -fsS -o /dev/null --max-time 20 "$RENDER_EXTERNAL_URL/v1/health" || true; sleep "${KEEPALIVE_S:-600}"; done ) &
fi
exec env -u TUNNEL_TOKEN su-exec app /usr/local/bin/boxaz-core
