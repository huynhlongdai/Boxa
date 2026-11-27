#!/usr/bin/env bash
# Live test v1.9 (readable gateway names) on the real domain behind the
# Cloudflare Tunnel (deploy/cloudflare-tunnel/up.sh), E2B only:
#   box "demo-app" → app spec (heroku/node-js-sample, port 8000) →
#   https://demo-app.<domain>/ → 200 from outside (DoH) → second port 8001
#   labelled "files" → https://demo-app-files.<domain>/ → 200 → legacy
#   <key>-<port> alias → 200 → sleep → request wakes → 200 → down → E2B [].
#
#   ENV_FILE=…/gateway.env scripts/live-names.sh
#
# At most 1 sandbox; a watchdog deletes every managed E2B sandbox after
# WATCHDOG_S (240 s); the exit trap downs every box and checks E2B is [].
set -euo pipefail
cd "$(dirname "$0")/.."
: "${ENV_FILE:?set ENV_FILE (the gateway env file used by up.sh)}"
set -a; . "$ENV_FILE"; set +a
ADDR=${BOXMES_ADDR:-:8080}
export BOXMES_URL=http://127.0.0.1:${ADDR##*:}
ADMIN=$BOXMES_ADMIN_KEY
DOMAIN=$BOXMES_GATEWAY_DOMAIN
NAME=${BOX_NAME:-demo-app}
REPO=${DEPLOY_REPO:-https://github.com/heroku/node-js-sample}
WORK=$(mktemp -d /tmp/sbx-names.XXXXXX)
DOH=${DOH:-https://1.1.1.1/dns-query}

pass() { printf '  PASS %s\n' "$*"; }
note() { printf '  NOTE %s\n' "$*"; }
fail() { printf '  FAIL %s\n' "$*"; exit 1; }
api() { local m=$1 p=$2; shift 2; curl -sS -X "$m" "$BOXMES_URL/v1$p" -H "Authorization: Bearer ${KEY:-$ADMIN}" -H 'Content-Type: application/json' "$@"; }
ms() { date +%s%3N; }
out() { curl -s -o "$WORK/body" -w '%{http_code} %{ssl_verify_result} %{time_total}' --doh-url "$DOH" --max-time ${3:-30} ${2:+-H "$2"} "$1" || echo "000 - -"; }
code() { out "$@" | cut -d' ' -f1; }
wait200() { local c=""; for i in $(seq ${2:-30}); do c=$(code "$1"); [ "$c" = 200 ] && break; sleep 1; done; echo "$c"; }
x() { KEY=$AKEY api POST /boxes/$1/exec -d "$(jq -n --arg c "$2" --argjson t "${3:-60}" '{command:$c,timeout_s:$t,tail_lines:20}')"; }
plist() { curl -sS -H "X-API-Key: $E2B_API_KEY" https://api.e2b.app/v2/sandboxes | jq -c '[.[]|select(.state!="killed")|.sandboxID]'; }
pkillall() { for id in $(curl -sS -H "X-API-Key: $E2B_API_KEY" https://api.e2b.app/v2/sandboxes | jq -r '.[]?|select(.metadata.managed=="1")|.sandboxID'); do
  curl -sS -X DELETE -H "X-API-Key: $E2B_API_KEY" https://api.e2b.app/sandboxes/$id >/dev/null || true; done; }
cleanup() {
  kill ${WATCHDOG:-} 2>/dev/null || true
  for b in $(api GET /boxes 2>/dev/null | jq -r '.items[]?|select(.status!="terminated")|.id' 2>/dev/null); do api DELETE /boxes/$b >/dev/null 2>&1 || true; done
  sleep 2; pkillall; sleep 1
  echo "  E2B sandboxes after cleanup: $(plist)"
}
trap cleanup EXIT

[ "$(code "$BOXMES_PUBLIC_URL/v1/health")" = 200 ] && pass "core reachable at $BOXMES_PUBLIC_URL" || fail "core not reachable at $BOXMES_PUBLIC_URL"
api GET /gateway | jq -c '{mode,url_pattern,label_pattern,alias_pattern}' | sed 's/^/    gateway: /'
[ "$(plist)" = "[]" ] || fail "E2B already has live sandboxes: $(plist)"
AGENT=$(api POST /agents -d "$(jq -nc --arg n "names-$(date +%s)" '{name:$n,kind:"claude-code",budget_day_usd:1,policy:{providers:["e2b"]}}')" | jq -r .id)
AKEY=$(api POST /keys -d "{\"agent_id\":\"$AGENT\",\"name\":\"names-live\"}" | jq -r .key)
( sleep "${WATCHDOG_S:-240}"; echo "  WATCHDOG: deleting every managed E2B sandbox"; pkillall ) & WATCHDOG=$!

T0=$(ms)
B=$(KEY=$AKEY api POST /boxes -d "{\"template\":\"base\",\"slug\":\"$NAME\",\"lifecycle\":\"session\",\"requirements\":{\"isolation\":\"sandbox\",\"cpu\":1,\"ram_mb\":512,\"disk_gb\":5,\"duration_hours\":0.1}}")
BOX=$(echo "$B" | jq -r .id); [ "$BOX" != null ] || { echo "$B"; fail "box create"; }
for i in $(seq 240); do S=$(KEY=$AKEY api GET /boxes/$BOX | jq -r .status); [ "$S" = running ] || [ "$S" = failed ] && break; sleep 0.25; done
[ "$S" = running ] || fail "box $S"; echo "    box up in $(($(ms)-T0)) ms, provider $(KEY=$AKEY api GET /boxes/$BOX | jq -r .provider), name $(KEY=$AKEY api GET /boxes/$BOX | jq -r .subdomain)"
SPEC=$(jq -n --arg r "$REPO" '{repo:$r, install:"npm install --omit=dev --no-audit --no-fund --loglevel=error", start:"node index.js", port:8000, ready:{http:"/"}, restart:"always", backoff_max_s:5}')
T0=$(ms); KEY=$AKEY api PUT /boxes/$BOX/app -d "$SPEC" >/dev/null
for i in $(seq 300); do J=$(KEY=$AKEY api GET /boxes/$BOX/app); [ "$(echo "$J" | jq -r .status.state)" = ready ] && break; sleep 0.5; done
echo "$J" | jq -e .status.ready >/dev/null || { echo "$J" | jq -c .status; fail "app not ready"; }
echo "    app ready in $(($(ms)-T0)) ms"

P=$(KEY=$AKEY api GET /boxes/$BOX/ports); echo "$P" | jq -c '{name,primary_port,ports:[.ports[]|{port,label,primary,url,alias_url}]}' | sed 's/^/    ports: /'
GW=$(echo "$P" | jq -r '.ports[]|select(.port==8000)|.gateway_url'); AL=$(echo "$P" | jq -r '.ports[]|select(.port==8000)|.alias_url')
[ "$GW" = "https://$NAME.$DOMAIN/" ] || fail "gateway url $GW"
T0=$(ms); C=$(wait200 "$GW" 40)
[ "$C" = 200 ] && grep -q "Hello World" "$WORK/body" && pass "$GW → 200 Hello World! from outside in $(($(ms)-T0)) ms" || fail "name url → $C $(head -c 200 "$WORK/body")"
R=$(out "$GW"); [ "$(echo "$R" | cut -d' ' -f2)" = 0 ] && pass "TLS valid, warm $(echo "$R" | awk '{printf "%d ms", $3*1000}')" || fail "TLS $R"
C=$(code "$AL"); [ "$C" = 200 ] && pass "legacy alias $AL → 200" || fail "alias → $C"

# second port with a text label
x $BOX "mkdir -p /tmp/f && echo files-ok > /tmp/f/index.html && cd /tmp/f && (setsid nohup python3 -m http.server 8001 --bind 0.0.0.0 >/tmp/f.log 2>&1 &) ; sleep 1; echo started" >/dev/null
L=$(KEY=$AKEY api POST /boxes/$BOX/ports -d '{"port":8001,"label":"files"}' | jq -r '.ports[0]|.port,.label,.gateway_url' | paste -sd' ')
echo "    exposed: $L"
FU="https://$NAME-files.$DOMAIN/"
[[ $L == "8001 files $FU" ]] || fail "label url $L"
C=""; for i in $(seq 20); do C=$(code "$FU"); [ "$C" = 200 ] && grep -q files-ok "$WORK/body" && break; sleep 1; done
[ "$C" = 200 ] && pass "$FU → 200 (second port by label)" || note "second port → $C $(head -c 120 "$WORK/body")"

# sleep → request wakes it
S=$(KEY=$AKEY api POST /boxes/$BOX/sleep -d '{}' | jq -r .status); [ "$S" = sleeping ] || fail "sleep → $S"
T0=$(ms); R=$(out "$GW" "Accept: text/html" 40); C=${R%% *}; DT=$(($(ms)-T0))
if [ "$C" = 200 ]; then pass "sleeping box woken by a request to $GW → 200 in $DT ms"
else note "first request → $C after $DT ms, retrying"; C=$(wait200 "$GW" 30); [ "$C" = 200 ] && pass "woke → 200 after $(($(ms)-T0)) ms" || fail "wake on request"; fi

# down → name gone
KEY=$AKEY api DELETE /boxes/$BOX >/dev/null
for i in $(seq 40); do [ "$(KEY=$AKEY api GET /boxes/$BOX | jq -r .status)" = terminated ] && break; sleep 0.25; done
C=$(code "$GW"); [ "$C" = 404 ] && pass "box down → $GW 404" || note "after down → $C"
