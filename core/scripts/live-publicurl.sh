#!/usr/bin/env bash
# Live public-URL fallback test on Prized (no provider preview host), through
# boxaz-core as an agent would use it:
#   box up (T3 shim, port 8000) → clone heroku/node-js-sample, npm install,
#   start on :8000 → GET /ports → curl the public URL from outside (200 +
#   "Hello World!") → sleep → wake → restart the app if the process died →
#   re-check the URL → down → Prized lists no managed sandbox.
#   PRIZED_API_KEY=… scripts/live-publicurl.sh                 (MODE=tunnel)
#   MODE=ingress PRIZED_API_KEY=… scripts/live-publicurl.sh
# tunnel:  BOXAZ_PUBLIC_URL unset → ingress skipped → in-box cloudflared quick tunnel.
# ingress: core itself is exposed through a local cloudflared quick tunnel and
#          that URL becomes BOXAZ_PUBLIC_URL → in-box `boxd ingress` agent dials
#          core → URL <public>/x/<key>-<port>/. Needs cloudflared on PATH (or
#          CLOUDFLARED=…) and linux boxd binaries for the sandbox (built here).
# Budget: one sandbox, watchdog deletes every managed sandbox after WATCHDOG_S (280 s).
set -euo pipefail
cd "$(dirname "$0")/.."
ROOT=$(pwd)
BIN=${BIN:-$ROOT/bin}
PORT=${PORT:-18791}
MODE=${MODE:-tunnel}
WORK=$(mktemp -d /tmp/sbx-purl.XXXXXX)
ADMIN=bx_admin_live_$(date +%s)
REPO=${DEPLOY_REPO:-https://github.com/heroku/node-js-sample}
RESULTS=${RESULTS:-$WORK/results.json}
PAPI=https://api.prized.dev/api/v1
CLOUDFLARED=${CLOUDFLARED:-cloudflared}
export BOXAZ_URL=http://127.0.0.1:$PORT
: "${PRIZED_API_KEY:?set PRIZED_API_KEY}"

pass() { printf '  PASS %s\n' "$*"; }
note() { printf '  NOTE %s\n' "$*"; }
fail() { printf '  FAIL %s\n' "$*"; echo "--- core.log"; tail -40 "$WORK/core.log"; exit 1; }
api() { local m=$1 p=$2; shift 2; curl -sS -X "$m" "$BOXAZ_URL/v1$p" -H "Authorization: Bearer ${KEY:-$ADMIN}" -H 'Content-Type: application/json' "$@"; }
papi() { curl -sS -H "Authorization: Bearer $PRIZED_API_KEY" "$@"; }
live() { papi "$PAPI/sandboxes" | jq -c '[.sandboxes[]|select(.state!="deleting" and .state!="deleted")]'; }
ms() { date +%s%3N; }
declare -A T
rec() { T[$1]=$2; printf '    ⏱ %-16s %6s ms\n' "$1" "$2"; }
x() { KEY=$AKEY api POST /boxes/$1/exec -d "$(jq -n --arg c "$2" --argjson t "${3:-300}" '{command:$c,timeout_s:$t,tail_lines:20}')"; }
get() { curl -s -o "$WORK/body" -w '%{http_code}' --max-time 20 "$1" || true; }
wait200() { local C=""; for i in $(seq ${2:-40}); do C=$(get "$1"); [ "$C" = 200 ] && grep -q "Hello World" "$WORK/body" && break; sleep 1; done; echo "$C"; }

cleanup() {
  kill ${WATCHDOG:-} 2>/dev/null || true
  papi -G -X DELETE "$PAPI/sandboxes" --data-urlencode "label=managed=1" >/dev/null 2>&1 || true
  kill ${CORE_PID:-} ${CF_PID:-} 2>/dev/null || true; wait 2>/dev/null || true
  [ -n "${KEEP:-}" ] || rm -rf "$WORK/data"
}
trap cleanup EXIT
( sleep "${WATCHDOG_S:-280}"; echo "  WATCHDOG: deleting every managed sandbox"
  papi -G -X DELETE "$PAPI/sandboxes" --data-urlencode "label=managed=1" >/dev/null ) & WATCHDOG=$!

if [ -z "${SKIP_BUILD:-}" ]; then
  go build -o "$BIN/" ./cmd/...
  mkdir -p "$WORK/dist"
  for a in amd64 arm64; do GOOS=linux GOARCH=$a CGO_ENABLED=0 go build -o "$WORK/dist/boxd-linux-$a" ./cmd/boxd; done
fi
[ "$(live | jq length)" = 0 ] || fail "Prized already has live sandboxes: $(live)"

PUB=""
if [ "$MODE" = ingress ]; then
  T0=$(ms)
  "$CLOUDFLARED" tunnel --no-autoupdate --url "http://127.0.0.1:$PORT" >"$WORK/cf.log" 2>&1 & CF_PID=$!
  for i in $(seq 60); do PUB=$(grep -o 'https://[a-z0-9-]*\.trycloudflare\.com' "$WORK/cf.log" | head -1 || true); [ -n "$PUB" ] && break; sleep 0.5; done
  [ -n "$PUB" ] || fail "local cloudflared gave no URL: $(tail -5 "$WORK/cf.log")"
  rec core_tunnel $(($(ms)-T0)); echo "    BOXAZ_PUBLIC_URL=$PUB"
fi

env -u E2B_API_KEY -u NOVITA_API_KEY BOXAZ_DATA_DIR=$WORK/data BOXAZ_ADMIN_KEY=$ADMIN BOXAZ_ADDR=127.0.0.1:$PORT \
BOXAZ_AUTO_SANDBOX=1 BOXAZ_PRICE_FEED=off BOXAZ_CLIENT_LOCATION=VN BOXAZ_PUBLIC_URL=$PUB BOXAZ_DIST_DIR=$WORK/dist \
  "$BIN/boxaz-core" >"$WORK/core.log" 2>&1 & CORE_PID=$!
for i in $(seq 50); do curl -sf "$BOXAZ_URL/v1/health" >/dev/null && break; sleep 0.1; done
curl -sf "$BOXAZ_URL/v1/health" >/dev/null || fail "core did not start"
if [ -n "$PUB" ]; then for i in $(seq 40); do [ "$(get "$PUB/v1/health")" = 200 ] && break; sleep 1; done
  [ "$(get "$PUB/v1/health")" = 200 ] && pass "core reachable from the internet at $PUB" || fail "core not reachable via $PUB"; fi
AGENT=$(api POST /agents -d '{"name":"purl","kind":"claude-code","budget_day_usd":1}' | jq -r .id)
export BOXAZ_API_KEY=$(api POST /keys -d "{\"agent_id\":\"$AGENT\",\"name\":\"purl\"}" | jq -r .key)
AKEY=$BOXAZ_API_KEY
api PUT /providers/prized/token -d "{\"token\":\"$PRIZED_API_KEY\"}" | jq -e .connected >/dev/null && pass "prized connected" || fail connect
api PUT /providers/prized/prefs -d '{"sandbox_mode":"shim"}' | jq -e '.sandbox_mode=="shim"' >/dev/null || fail prefs

# 1. box up
REQ='"requirements":{"isolation":"sandbox","cpu":1,"ram_mb":1024,"disk_gb":5,"duration_hours":0.1}'
T0=$(ms)
B=$(KEY=$AKEY api POST /boxes -d "{\"template\":\"base\",\"slug\":\"web-purl\",\"lifecycle\":\"session\",\"ports\":[8000],$REQ}")
BOX=$(echo "$B" | jq -r .id); [ "$BOX" != null ] || { echo "$B"; fail "box create"; }
for i in $(seq 480); do S=$(KEY=$AKEY api GET /boxes/$BOX | jq -r .status); [ "$S" = running ] || [ "$S" = failed ] && break; sleep 0.25; done
rec up $(($(ms)-T0)); [ "$S" = running ] || fail "box not running ($S)"
[ "$(KEY=$AKEY api GET /boxes/$BOX | jq -r .provider)" = prized ] || fail "box not on prized"

# 2. deploy
T0=$(ms)
J=$(x $BOX "git clone -q --depth 1 $REPO app && cd app && npm install --omit=dev --no-audit --no-fund --loglevel=error 2>&1 | tail -3" 600)
echo "$J" | jq -e '.exit_code==0' >/dev/null || { echo "$J" | jq -c .; fail "clone/install"; }; rec install $(($(ms)-T0))
START='cd app && (PORT=8000 nohup setsid node index.js > /tmp/app.log 2>&1 < /dev/null &) ; for i in $(seq 40); do curl -sf localhost:8000 >/dev/null && break; sleep 0.25; done; curl -s localhost:8000'
J=$(x $BOX "$START" 60); echo "$J" | jq -e '.stdout_tail|contains("Hello World")' >/dev/null && pass "app answers inside the box" || { echo "$J" | jq -c .; fail "app start"; }

# 3. public URL through the fallback chain
T0=$(ms); PR=$(KEY=$AKEY api GET /boxes/$BOX/ports); rec ports_first $(($(ms)-T0))
echo "    ports: $(echo "$PR" | jq -c '.ports[0]')"
KIND=$(echo "$PR" | jq -r '.ports[0].kind'); URL=$(echo "$PR" | jq -r '.ports[0].url')
[ "$KIND" = "$MODE" ] && [ "$URL" != "" ] && [ "$URL" != null ] && pass "GET /ports → kind=$KIND $URL" || fail "expected kind=$MODE, got $KIND"
[ "$MODE" = ingress ] && { [[ $URL == "$PUB"/x/*-8000/ ]] || fail "ingress URL shape $URL"; }
CLI=$("$BIN/box" url $BOX 8000); [[ $CLI == *"$URL"* ]] && pass "box url agrees" || fail "cli url: $CLI"
T0=$(ms); C=$(wait200 "$URL"); rec url_first_200 $(($(ms)-T0))
[ "$C" = 200 ] && pass "curl $URL → 200 Hello World!" || fail "public URL answered $C: $(head -c 200 "$WORK/body")"
T0=$(ms); C=$(get "$URL"); rec url_warm $(($(ms)-T0))

# 4. sleep → wake → re-check
T0=$(ms); S=$("$BIN/box" sleep $BOX | awk '{print $2}'); rec sleep $(($(ms)-T0)); [ "$S" = sleeping ] || fail "sleep → $S"
C=$(get "$URL"); note "public URL while sleeping → HTTP $C"; T[url_sleeping]=$C
T0=$(ms); S=$("$BIN/box" wake $BOX | awk '{print $2}'); rec wake $(($(ms)-T0)); [ "$S" = running ] || fail "wake → $S"
J=$(x $BOX 'pgrep -f "[n]ode index.js" >/dev/null && echo proc=alive || echo proc=gone'); P=$(echo "$J" | jq -r .stdout_tail | grep -o 'proc=[a-z]*'); T[proc_after_wake]=$P
echo "    after wake: $P"
[ "$P" = proc=alive ] || { x $BOX "$START" 60 >/dev/null; note "app restarted after wake"; }
T0=$(ms); PR=$(KEY=$AKEY api GET /boxes/$BOX/ports); rec ports_after_wake $(($(ms)-T0))
URL2=$(echo "$PR" | jq -r '.ports[0].url'); KIND2=$(echo "$PR" | jq -r '.ports[0].kind')
[ "$URL2" = "$URL" ] && T[url_changed]=no || T[url_changed]=yes
echo "    after wake: kind=$KIND2 url=$URL2 (changed: ${T[url_changed]})"
T0=$(ms); C=$(wait200 "$URL2"); rec wake_to_200 $(($(ms)-T0))
[ "$C" = 200 ] && pass "URL serves again after sleep → wake" || fail "URL after wake HTTP $C"

# 5. down + provider empty
T0=$(ms); "$BIN/box" down $BOX >/dev/null; rec down $(($(ms)-T0))
T0=$(ms); for i in $(seq 30); do C=$(get "$URL2"); [ "$C" != 200 ] && break; sleep 1; done; rec down_to_dead $(($(ms)-T0))
T[url_after_down]=$C; note "URL after down → HTTP $C (the sandbox may answer for a moment while Prized deletes it)"
for i in $(seq 40); do LEFT=$(live | jq length); [ "$LEFT" = 0 ] && break; sleep 1; done
[ "$LEFT" = 0 ] && pass "no live sandboxes left at Prized" || fail "$LEFT sandbox(es) left"
{ printf '{"provider":"prized","mode":"%s","url":"%s"' "$MODE" "$URL"; for k in "${!T[@]}"; do printf ',"%s":"%s"' "$k" "${T[$k]}"; done; echo '}'; } > "$RESULTS"
echo "    results: $(cat "$RESULTS")"
echo "== LIVE PUBLIC URL PASSED (prized, $MODE)"
