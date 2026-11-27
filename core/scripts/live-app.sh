#!/usr/bin/env bash
# Live v1.7 test: supervised app + Boxmes gateway (wake on request) + scoped URL
# tokens on real sandbox providers, through boxmes-core exposed to the internet
# by a local cloudflared quick tunnel (BOXMES_PUBLIC_URL), as an agent would:
#   box up → PUT /app (repo heroku/node-js-sample, npm install, node index.js,
#   port 8000, ready http /) → gateway URL → 200 from outside → kill the app →
#   supervisor restarts it → sleep → request to the gateway URL wakes the box
#   (latency measured) → URL token: 401 without, 200 with, 401 after revoke →
#   snapshot + restore → app restarted → clone (native fork) → clone's app
#   serves through its own gateway URL → down everything → provider lists [].
#   PROVIDERS="prized e2b" E2B_API_KEY=… PRIZED_API_KEY=… scripts/live-app.sh
# Budget per provider: 2 sandboxes (box + clone), never more than 2 at once,
# watchdog deletes every managed sandbox after WATCHDOG_S (280 s).
set -euo pipefail
cd "$(dirname "$0")/.."
ROOT=$(pwd)
BIN=${BIN:-$ROOT/bin}
PORT=${PORT:-18795}
PROVIDERS=${PROVIDERS:-prized e2b}
WORK=$(mktemp -d /tmp/sbx-app.XXXXXX)
ADMIN=bx_admin_live_$(date +%s)
REPO=${DEPLOY_REPO:-https://github.com/heroku/node-js-sample}
CLOUDFLARED=${CLOUDFLARED:-cloudflared}
RESULTS=${RESULTS:-$WORK/results.json}
export BOXMES_URL=http://127.0.0.1:$PORT

pass() { printf '  PASS %s\n' "$*"; }
note() { printf '  NOTE %s\n' "$*"; }
fail() { printf '  FAIL %s\n' "$*"; echo "--- core.log"; tail -60 "$WORK/core.log"; exit 1; }
api() { local m=$1 p=$2; shift 2; curl -sS -X "$m" "$BOXMES_URL/v1$p" -H "Authorization: Bearer ${KEY:-$ADMIN}" -H 'Content-Type: application/json' "$@"; }
ms() { date +%s%3N; }
declare -A T
rec() { T[$PROV.$1]=$2; printf '    ⏱ %-22s %7s\n' "$1" "$2"; }
get() { curl -s -o "$WORK/body" -w '%{http_code}' --max-time ${3:-30} ${2:+-H "$2"} "$1" || true; }
x() { KEY=$AKEY api POST /boxes/$1/exec -d "$(jq -n --arg c "$2" --argjson t "${3:-120}" '{command:$c,timeout_s:$t,tail_lines:20}')"; }
app_state() { KEY=$AKEY api GET /boxes/$1/app; }
wait_ready() { # wait_ready <box> <seconds> → prints app json
  local j=""; for i in $(seq $(($2*2))); do j=$(app_state $1); [ "$(echo "$j" | jq -r .status.state)" = ready ] && break; sleep 0.5; done; echo "$j"
}
wait200() { local c=""; for i in $(seq ${2:-30}); do c=$(get "$1"); [ "$c" = 200 ] && grep -q "Hello World" "$WORK/body" && break; sleep 1; done; echo "$c"; }

# provider helpers: list live managed sandboxes, delete them all
plist() {
  case $1 in
    prized) curl -sS -H "Authorization: Bearer $PRIZED_API_KEY" https://api.prized.dev/api/v1/sandboxes | jq -c '[.sandboxes[]|select(.state!="deleting" and .state!="deleted")|.id]' ;;
    e2b) curl -sS -H "X-API-Key: $E2B_API_KEY" https://api.e2b.app/v2/sandboxes | jq -c '[.[]|select(.state!="killed")|.sandboxID]' ;;
    novita-phx) curl -sS -H "X-API-KEY: $NOVITA_API_KEY" https://api.us-phx-1.sandbox.novita.ai/v2/sandboxes | jq -c '[.[]?|select(.state!="killed")|.sandboxID]' ;;
    novita-va) curl -sS -H "X-API-KEY: $NOVITA_API_KEY" https://api.sandbox.novita.ai/v2/sandboxes | jq -c '[.[]?|select(.state!="killed")|.sandboxID]' ;;
  esac
}
pkillall() {
  case $1 in
    prized) curl -sS -G -X DELETE -H "Authorization: Bearer $PRIZED_API_KEY" https://api.prized.dev/api/v1/sandboxes --data-urlencode "label=managed=1" >/dev/null 2>&1 || true ;;
    e2b) for id in $(curl -sS -H "X-API-Key: $E2B_API_KEY" https://api.e2b.app/v2/sandboxes | jq -r '.[]?|select(.metadata.managed=="1")|.sandboxID'); do
           curl -sS -X DELETE -H "X-API-Key: $E2B_API_KEY" https://api.e2b.app/sandboxes/$id >/dev/null || true; done ;;
  esac
}
cleanup() {
  kill ${WATCHDOG:-} 2>/dev/null || true
  for p in $PROVIDERS; do pkillall $p; done
  kill ${CORE_PID:-} ${CF_PID:-} 2>/dev/null || true; wait 2>/dev/null || true
  [ -n "${KEEP:-}" ] || rm -rf "$WORK/data"
}
trap cleanup EXIT

if [ -z "${SKIP_BUILD:-}" ]; then
  go build -o "$BIN/" ./cmd/...
fi
mkdir -p "$WORK/dist"
for a in amd64 arm64; do GOOS=linux GOARCH=$a CGO_ENABLED=0 go build -o "$WORK/dist/boxd-linux-$a" ./cmd/boxd; done
for p in $PROVIDERS; do [ "$(plist $p)" = "[]" ] || fail "$p already has live sandboxes: $(plist $p)"; done

start_tunnel() {
  "$CLOUDFLARED" tunnel --no-autoupdate --url "http://127.0.0.1:$PORT" >"$WORK/cf.log" 2>&1 & CF_PID=$!
  PUB=""; for i in $(seq 60); do PUB=$(grep -o 'https://[a-z0-9-]*\.trycloudflare\.com' "$WORK/cf.log" | head -1 || true); [ -n "$PUB" ] && break; sleep 0.5; done
  [ -n "$PUB" ] || fail "cloudflared gave no URL: $(tail -5 "$WORK/cf.log")"
  for i in $(seq 60); do grep -q "Registered tunnel connection" "$WORK/cf.log" && break; sleep 0.5; done
  sleep 3 # let the quick-tunnel DNS record exist before the first lookup (avoids a cached NXDOMAIN)
}
start_tunnel
echo "    BOXMES_PUBLIC_URL=$PUB"
start_core() {
env -u NOVITA_API_KEY -u E2B_API_KEY -u PRIZED_API_KEY BOXMES_DATA_DIR=$WORK/data BOXMES_ADMIN_KEY=$ADMIN BOXMES_ADDR=127.0.0.1:$PORT BOXMES_AUTO_SANDBOX=1 BOXMES_PRICE_FEED=off \
BOXMES_CLIENT_LOCATION=VN BOXMES_PUBLIC_URL=$PUB BOXMES_DIST_DIR=$WORK/dist BOXMES_DISABLE_TUNNEL=1 \
  "$BIN/boxmes-core" >>"$WORK/core.log" 2>&1 & CORE_PID=$!
for i in $(seq 50); do curl -sf "$BOXMES_URL/v1/health" >/dev/null && break; sleep 0.1; done
}
start_core
for try in 1 2 3; do # quick tunnels sometimes never resolve: start a fresh one
  for i in $(seq 60); do [ "$(get "$PUB/v1/health" "" 10)" = 200 ] && break 2; sleep 1; done
  echo "    tunnel $PUB unreachable, starting a new one"; kill $CF_PID 2>/dev/null || true; start_tunnel
  echo "    BOXMES_PUBLIC_URL=$PUB (core restarted with it)"; kill $CORE_PID; wait $CORE_PID 2>/dev/null || true; start_core
done
[ "$(get "$PUB/v1/health" "" 10)" = 200 ] && pass "core reachable from the internet at $PUB" || fail "core not reachable via $PUB"
AGENT=$(api POST /agents -d '{"name":"app-live","kind":"claude-code","budget_day_usd":1}' | jq -r .id)
export BOXMES_API_KEY=$(api POST /keys -d "{\"agent_id\":\"$AGENT\",\"name\":\"app-live\"}" | jq -r .key)
AKEY=$BOXMES_API_KEY

SPEC=$(jq -n --arg r "$REPO" '{repo:$r, install:"npm install --omit=dev --no-audit --no-fund --loglevel=error", start:"node index.js", port:8000, ready:{http:"/"}, restart:"always", backoff_max_s:5}')

for PROV in $PROVIDERS; do
  echo "== $PROV"
  case $PROV in prized) PKEY=$PRIZED_API_KEY ;; e2b) PKEY=$E2B_API_KEY ;; esac
  ( sleep "${WATCHDOG_S:-280}"; echo "  WATCHDOG: deleting every managed $PROV sandbox"; pkillall $PROV ) & WATCHDOG=$!
  api PUT /providers/$PROV/token -d "{\"token\":\"$PKEY\"}" | jq -e .connected >/dev/null && pass "$PROV connected" || fail "connect $PROV"
  api PUT /providers/$PROV/prefs -d '{"sandbox_mode":"shim"}' | jq -e '.sandbox_mode=="shim"' >/dev/null || fail prefs
  api GET /sandbox/providers | jq -c --arg p $PROV '.items[]|select(.id==$p)|{id,lifecycle}' | sed 's/^/    caps: /'
  # 1. box up + app spec
  T0=$(ms)
  B=$(KEY=$AKEY api POST /boxes -d "{\"template\":\"base\",\"slug\":\"app-$PROV\",\"lifecycle\":\"session\",\"requirements\":{\"isolation\":\"sandbox\",\"cpu\":1,\"ram_mb\":512,\"disk_gb\":5,\"duration_hours\":0.1}}")
  BOX=$(echo "$B" | jq -r .id); [ "$BOX" != null ] || { echo "$B"; fail "box create"; }
  for i in $(seq 240); do S=$(KEY=$AKEY api GET /boxes/$BOX | jq -r .status); [ "$S" = running ] || [ "$S" = failed ] && break; sleep 0.25; done
  [ "$S" = running ] || fail "box $S"; rec up "$(($(ms)-T0)) ms"
  [ "$(KEY=$AKEY api GET /boxes/$BOX | jq -r .provider)" = $PROV ] || fail "box not on $PROV: $(KEY=$AKEY api GET /boxes/$BOX | jq -c '{provider,placement_reason}')"
  T0=$(ms); KEY=$AKEY api PUT /boxes/$BOX/app -d "$SPEC" | jq -c '.status|{state,supervisor}' | sed 's/^/    app set: /'
  J=$(wait_ready $BOX 150); echo "$J" | jq -e '.status.ready' >/dev/null || { echo "$J" | jq -c .status; KEY=$AKEY api GET "/boxes/$BOX/app/logs?max=2000" | jq -r .data | tail -15; fail "app not ready"; }
  rec app_ready "$(($(ms)-T0)) ms"; echo "    supervisor: $(echo "$J" | jq -r .status.supervisor)"
  GW=$(KEY=$AKEY api GET /boxes/$BOX/ports | jq -r '.ports[]|select(.port==8000)|.gateway_url')
  [[ $GW == "$PUB"/x/*-8000/ ]] || fail "gateway url $GW"
  T0=$(ms); C=$(wait200 "$GW" 30); rec gateway_first_200 "$(($(ms)-T0)) ms"
  [ "$C" = 200 ] && pass "gateway URL → 200 Hello World! ($GW)" || fail "gateway $C $(head -c 200 "$WORK/body")"
  T0=$(ms); C=$(get "$GW"); rec gateway_warm "$(($(ms)-T0)) ms"
  # 2. kill → supervisor restarts
  x $BOX 'pkill -f "node index.js"; echo killed' >/dev/null
  T0=$(ms); sleep 1; J=$(wait_ready $BOX 60)
  echo "$J" | jq -e '.status.ready and .status.restarts>=1' >/dev/null && pass "app killed → restarted by the supervisor (restarts=$(echo "$J" | jq .status.restarts), last_exit=$(echo "$J" | jq .status.last_exit))" || fail "no restart: $(echo "$J" | jq -c .status)"
  rec restart_to_ready "$(($(ms)-T0)) ms"
  [ "$(wait200 "$GW" 20)" = 200 ] || fail "gateway after restart"
  # 3. sleep → request wakes it
  T0=$(ms); S=$("$BIN/box" sleep $BOX | awk '{print $2}'); rec sleep "$(($(ms)-T0)) ms"; [ "$S" = sleeping ] || fail "sleep → $S"
  C=$(get "$GW" "User-Agent: Googlebot/2.1" 15); [ "$C" = 503 ] && [ "$(KEY=$AKEY api GET /boxes/$BOX | jq -r .status)" = sleeping ] && pass "bot request → 503, box stays asleep" || note "bot request → $C"
  T0=$(ms); C=$(get "$GW" "Accept: text/html" 40); DT=$(($(ms)-T0))
  if [ "$C" = 200 ]; then rec wake_request_200 "$DT ms"; pass "request to the sleeping box woke it → 200 in $DT ms"
  else note "first request → $C after $DT ms (hold exceeded), retrying"; T1=$(ms); C=$(wait200 "$GW" 30); rec wake_request_200 "$(( $(ms)-T0 )) ms (first $C/$DT ms)"; [ "$C" = 200 ] || fail "wake on request"; fi
  KEY=$AKEY api GET /boxes/$BOX | jq -r .status | grep -q running || fail "box not running after wake"
  # 4. URL token
  TJ=$(KEY=$AKEY api POST /boxes/$BOX/ports/8000/tokens -d '{"ttl_s":600,"label":"live"}'); TOK=$(echo "$TJ" | jq -r .token); TID=$(echo "$TJ" | jq -r .id)
  C1=$(get "$GW"); C2=$(get "$GW" "Authorization: Bearer $TOK"); C3=$(curl -s -o /dev/null -w '%{http_code}' --max-time 20 "$(echo "$TJ" | jq -r .url)")
  KEY=$AKEY api DELETE /boxes/$BOX/ports/8000/tokens/$TID >/dev/null; C4=$(get "$GW" "Authorization: Bearer $TOK")
  T[$PROV.token]="$C1/$C2/$C3/$C4"; echo "    token: none=$C1 bearer=$C2 ?token=$C3 revoked=$C4"
  [ "$C1" = 401 ] && [ "$C2" = 200 ] && [ "$C3" = 302 ] && [ "$C4" = 401 ] && pass "URL token: 401 without, 200 with (302 → cookie for ?token=), 401 after revoke" || fail "token $C1/$C2/$C3/$C4"
  KEY=$AKEY api PUT /boxes/$BOX/ports/8000 -d '{"access":"public"}' >/dev/null
  # 5. snapshot + restore → app restarted
  SA=$(app_state $BOX | jq -r .status.started_at); SN=$(KEY=$AKEY api POST /boxes/$BOX/snapshot -d '{}' | jq -r .id); sleep 1.2
  T0=$(ms); KEY=$AKEY api POST /boxes/$BOX/restore -d "{\"snapshot_id\":\"$SN\"}" >/dev/null
  for i in $(seq 120); do J=$(app_state $BOX); [ "$(echo "$J" | jq -r .status.ready)" = true ] && [ "$(echo "$J" | jq -r .status.started_at)" != "$SA" ] && break; sleep 0.5; done
  [ "$(echo "$J" | jq -r .status.started_at)" != "$SA" ] && [ "$(echo "$J" | jq -r .status.ready)" = true ] && pass "restore → app restarted automatically" || fail "restore: $(echo "$J" | jq -c .status)"
  rec restore_to_ready "$(($(ms)-T0)) ms"; [ "$(wait200 "$GW" 20)" = 200 ] || fail "gateway after restore"
  # 6. clone (native fork) → clone's app serves through its own gateway URL
  T0=$(ms); CL=$(KEY=$AKEY api POST /boxes/$BOX/clone -d "{\"slug\":\"app-$PROV-clone\"}"); CLID=$(echo "$CL" | jq -r .id)
  [ "$CLID" != null ] || { echo "$CL"; fail clone; }; rec clone "$(($(ms)-T0)) ms ($(echo "$CL" | jq -r .placement_reason | grep -o 'via [^(]*'))"
  J=$(wait_ready $CLID 120); echo "$J" | jq -e '.status.ready' >/dev/null && pass "clone's app running (state=$(echo "$J" | jq -r .status.state), supervisor=$(echo "$J" | jq -r .status.supervisor))" || fail "clone app $(echo "$J" | jq -c .status)"
  rec clone_app_ready "$(($(ms)-T0)) ms"
  CGW=$(KEY=$AKEY api GET /boxes/$CLID/ports | jq -r '.ports[]|select(.port==8000)|.gateway_url')
  T0=$(ms); C=$(wait200 "$CGW" 30); rec clone_gateway_200 "$(($(ms)-T0)) ms"; [ "$C" = 200 ] && [ "$CGW" != "$GW" ] && pass "clone gateway URL → 200 ($CGW)" || fail "clone gateway $C"
  [ "$(get "$GW")" = 200 ] && pass "source still serves its own URL" || fail "source URL after clone"
  if [ $PROV = e2b ]; then # native wake: the provider's own URL resumes a paused sandbox (autoResume)
    PREV=$(KEY=$AKEY api GET /boxes/$CLID/ports | jq -r '.ports[]|select(.port==8000)|.url'); "$BIN/box" sleep $CLID >/dev/null
    T0=$(ms); C=$(get "$PREV" "" 30); T[$PROV.native_autoresume]="$C in $(($(ms)-T0)) ms"; echo "    E2B preview URL of the paused clone (native autoResume) → $C in $(($(ms)-T0)) ms"
  fi
  # 7. down
  T0=$(ms); "$BIN/box" down $CLID >/dev/null; "$BIN/box" down $BOX >/dev/null; rec down "$(($(ms)-T0)) ms"
  [ "$(get "$GW")" = 404 ] && pass "gateway URL dead after down" || note "gateway after down → $(get "$GW")"
  for i in $(seq 40); do L=$(plist $PROV); [ "$L" = "[]" ] && break; sleep 1; done
  [ "$L" = "[]" ] && pass "no live sandboxes left at $PROV" || fail "$PROV left $L"
  kill $WATCHDOG 2>/dev/null || true
  api PUT /providers/$PROV/prefs -d '{"enabled":false}' >/dev/null # next provider's boxes go there
done

echo "== final provider lists"
for p in e2b novita-phx novita-va prized; do
  case $p in novita-*) [ -n "${NOVITA_API_KEY:-}" ] || continue ;; e2b) [ -n "${E2B_API_KEY:-}" ] || continue ;; prized) [ -n "${PRIZED_API_KEY:-}" ] || continue ;; esac
  echo "    $p: $(plist $p)"
done
{ echo "{"; first=1; for k in "${!T[@]}"; do [ $first = 1 ] || echo ","; first=0; printf '"%s":"%s"' "$k" "${T[$k]}"; done; echo "}"; } > "$RESULTS"
echo "    results: $(jq -c -S . "$RESULTS")"
echo "== LIVE APP PASSED ($PROVIDERS)"
