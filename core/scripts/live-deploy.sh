#!/usr/bin/env bash
# Live "deploy an app" test on a real sandbox provider, through boxmes-core as
# an agent would use it (REST + CLI + MCP):
#   box up (T3 shim, port 8000) → git clone heroku/node-js-sample, npm install,
#   start it on :8000 → public URL from Boxmes (box url / ports / MCP box_url)
#   → curl from outside (200 + "Hello World!") → box sleep (= provider pause)
#   and wake (= resume): files/process/URL checked → idle sleep (idle_minutes=1,
#   fast reaper) → snapshot (gateway tarball) → break a file → restore →
#   fork the snapshot onto a fresh sandbox → clone (E2B: native fork) → down
#   everything and check the provider lists no sandbox.
#   E2B_API_KEY=… scripts/live-deploy.sh                    (E2B)
#   SANDBOX_PROVIDER=novita NOVITA_API_KEY=… scripts/live-deploy.sh
#   SKIP_FORK=1 / SKIP_IDLE=1 skip those steps (fewer sandboxes / faster).
# Budget: at most 2 sandboxes at once, each well under 5 minutes; a watchdog
# deletes every managed sandbox after WATCHDOG_S (default 280 s).
set -euo pipefail
cd "$(dirname "$0")/.."
ROOT=$(pwd)
BIN=${BIN:-$ROOT/bin}
PORT=${PORT:-18786}
WORK=$(mktemp -d /tmp/sbx-deploy.XXXXXX)
ADMIN=bx_admin_live_$(date +%s)
PROV=${SANDBOX_PROVIDER:-e2b}
REPO=${DEPLOY_REPO:-https://github.com/heroku/node-js-sample}
RESULTS=${RESULTS:-$WORK/results.json}
export BOXMES_URL=http://127.0.0.1:$PORT
case $PROV in
  e2b)    : "${E2B_API_KEY:?set E2B_API_KEY}"; PKEY=$E2B_API_KEY; PAPI=https://api.e2b.app; PHDR=X-API-Key ;;
  novita) : "${NOVITA_API_KEY:?set NOVITA_API_KEY}"; PKEY=$NOVITA_API_KEY; PAPI=https://api.us-phx-1.sandbox.novita.ai; PHDR=X-API-KEY ;;
  *) echo "SANDBOX_PROVIDER must be e2b or novita"; exit 2 ;;
esac

pass() { printf '  PASS %s\n' "$*"; }
note() { printf '  NOTE %s\n' "$*"; }
fail() { printf '  FAIL %s\n' "$*"; echo "--- core.log"; tail -40 "$WORK/core.log"; exit 1; }
api() { local m=$1 p=$2; shift 2; curl -sS -X "$m" "$BOXMES_URL/v1$p" -H "Authorization: Bearer ${KEY:-$ADMIN}" -H 'Content-Type: application/json' "$@"; }
papi() { curl -sS -H "$PHDR: $PKEY" "$@"; }
live() { papi "$PAPI/v2/sandboxes" | jq -c '[.[]|select(.state!="killed")]'; }
pstate() { papi "$PAPI/sandboxes/$1" | jq -r '.state // "gone"'; }
ms() { date +%s%3N; }
declare -A T
rec() { T[$1]=$2; printf '    ⏱ %-14s %6s ms\n' "$1" "$2"; }
x() { # exec through the API: x <box> <command> [timeout]
  KEY=$AKEY api POST /boxes/$1/exec -d "$(jq -n --arg c "$2" --argjson t "${3:-300}" '{command:$c,timeout_s:$t,tail_lines:20}')"
}
get() { curl -s -o "$WORK/body" -w '%{http_code}' --max-time 20 "$1" || true; }
wait_box() { # wait_box <box> <status> <seconds>
  local s=""
  for i in $(seq $(($3*4))); do s=$(KEY=$AKEY api GET /boxes/$1 | jq -r .status); [ "$s" = "$2" ] || [ "$s" = failed ] && break; sleep 0.25; done
  echo "$s"
}

cleanup() {
  kill ${WATCHDOG:-} 2>/dev/null || true
  for id in $(papi "$PAPI/v2/sandboxes" 2>/dev/null | jq -r '.[]?|select(.metadata.managed=="1")|.sandboxID' 2>/dev/null); do
    papi -X DELETE "$PAPI/sandboxes/$id" >/dev/null 2>&1 || true
  done
  kill ${CORE_PID:-} 2>/dev/null || true; wait ${CORE_PID:-} 2>/dev/null || true
  [ -n "${KEEP:-}" ] || rm -rf "$WORK/data"
}
trap cleanup EXIT
( sleep "${WATCHDOG_S:-280}"; echo "  WATCHDOG: deleting every managed sandbox"
  for id in $(papi "$PAPI/v2/sandboxes" | jq -r '.[]?|select(.metadata.managed=="1")|.sandboxID'); do papi -X DELETE "$PAPI/sandboxes/$id" >/dev/null; done ) & WATCHDOG=$!

[ -n "${SKIP_BUILD:-}" ] || go build -o "$BIN/" ./cmd/...
[ "$(live | jq length)" = 0 ] || fail "provider already has live sandboxes: $(live)"

OTHER=E2B_API_KEY; [ "$PROV" = e2b ] && OTHER=NOVITA_API_KEY
env -u $OTHER -u PRIZED_API_KEY BOXMES_DATA_DIR=$WORK/data BOXMES_ADMIN_KEY=$ADMIN BOXMES_ADDR=127.0.0.1:$PORT \
BOXMES_AUTO_SANDBOX=1 BOXMES_PRICE_FEED=off BOXMES_CLIENT_LOCATION=VN BOXMES_REAPER_INTERVAL=${REAPER:-10s} \
  "$BIN/boxmes-core" >"$WORK/core.log" 2>&1 & CORE_PID=$!
for i in $(seq 50); do curl -sf "$BOXMES_URL/v1/health" >/dev/null && break; sleep 0.1; done
curl -sf "$BOXMES_URL/v1/health" >/dev/null || fail "core did not start"
AGENT=$(api POST /agents -d '{"name":"deploy","kind":"claude-code","budget_day_usd":1}' | jq -r .id)
export BOXMES_API_KEY=$(api POST /keys -d "{\"agent_id\":\"$AGENT\",\"name\":\"deploy\"}" | jq -r .key)
AKEY=$BOXMES_API_KEY
api PUT /providers/$PROV/token -d "{\"token\":\"$PKEY\"}" | jq -e .connected >/dev/null && pass "$PROV connected" || fail connect
api PUT /providers/$PROV/prefs -d '{"sandbox_mode":"shim"}' | jq -e '.sandbox_mode=="shim"' >/dev/null || fail prefs

# 1. box up (session box, port 8000, idle sleep after 1 min)
REQ='"requirements":{"isolation":"sandbox","cpu":1,"ram_mb":512,"disk_gb":5,"duration_hours":0.1}'
T0=$(ms)
B=$(KEY=$AKEY api POST /boxes -d "{\"template\":\"base\",\"slug\":\"web-$PROV\",\"lifecycle\":\"session\",\"idle_minutes\":1,\"ports\":[8000],$REQ}")
BOX=$(echo "$B" | jq -r .id); [ "$BOX" != null ] || { echo "$B"; fail "box create"; }
S=$(wait_box $BOX running 120); rec up $(($(ms)-T0))
[ "$S" = running ] || fail "box not running ($S)"
HID=$(KEY=$AKEY api GET /boxes/$BOX | jq -r .host_id)
REF=$(api GET /hosts | jq -r --arg h "$HID" '.items[]|select(.id==$h)|.provider_ref')
pass "box $BOX up on $PROV sandbox $REF"

# 2. deploy: clone a real repo, install, run on :8000
T0=$(ms)
J=$(x $BOX "git clone -q --depth 1 $REPO app && cd app && npm install --omit=dev --no-audit --no-fund --loglevel=error 2>&1 | tail -3 && git log --oneline -1" 600)
echo "$J" | jq -e '.exit_code==0' >/dev/null || { echo "$J" | jq -c .; fail "clone/install"; }
rec install $(($(ms)-T0)); echo "    $(echo "$J" | jq -r .stdout_tail | tail -2 | tr '\n' ' ')"
START='cd app && (PORT=8000 nohup setsid node index.js > /tmp/app.log 2>&1 < /dev/null &) ; for i in $(seq 40); do curl -sf localhost:8000 >/dev/null && break; sleep 0.25; done; curl -s localhost:8000'
T0=$(ms); J=$(x $BOX "$START" 60); rec start $(($(ms)-T0))
echo "$J" | jq -e '.stdout_tail|contains("Hello World")' >/dev/null && pass "app answers inside the box" || { echo "$J" | jq -c .; fail "app start"; }

# 3. public URL from Boxmes (box record, REST /ports, CLI, MCP) + curl from outside
URL=$(KEY=$AKEY api GET /boxes/$BOX | jq -r .url)
PR=$(KEY=$AKEY api GET /boxes/$BOX/ports); echo "    ports: $(echo "$PR" | jq -c '.ports')"
CLI=$("$BIN/box" url $BOX 8000)
MCPU=$(curl -s -X POST "$BOXMES_URL/mcp" -H "Authorization: Bearer $AKEY" -d "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/call\",\"params\":{\"name\":\"box_url\",\"arguments\":{\"box_id\":\"$BOX\",\"port\":8000}}}" | jq -r '.result.structuredContent.ports[0].url')
echo "    box.url=$URL  cli: $CLI  mcp: $MCPU"
[[ $URL == https://8000-$REF.* ]] && [ "$URL" = "$MCPU" ] && [[ $CLI == *"$URL"* ]] && pass "public URL wired into box url, /ports, CLI and MCP" || fail "url mismatch"
T0=$(ms); C=""; for i in $(seq 20); do C=$(get "$URL"); [ "$C" = 200 ] && break; sleep 0.5; done; rec url_first_200 $(($(ms)-T0))
[ "$C" = 200 ] && grep -q "Hello World" "$WORK/body" && pass "curl $URL → 200 Hello World!" || fail "public URL answered $C: $(head -c 200 "$WORK/body")"

# 4. sleep → provider pause; wake → resume
T0=$(ms); S=$("$BIN/box" sleep $BOX | awk '{print $2}'); rec sleep $(($(ms)-T0))
PS=$(pstate $REF); [ "$S" = sleeping ] && [ "$PS" = paused ] && pass "box sleep → $PROV sandbox $PS" || fail "sleep: box $S, provider $PS"
C=$(get "$URL"); note "public URL while sleeping → HTTP $C"; T[url_sleeping]=$C
T0=$(ms); S=$("$BIN/box" wake $BOX | awk '{print $2}'); rec wake $(($(ms)-T0))
PS=$(pstate $REF); [ "$S" = running ] && [ "$PS" = running ] && pass "box wake → $PROV sandbox $PS" || fail "wake: box $S, provider $PS"
T0=$(ms); C=""; for i in $(seq 20); do C=$(get "$URL"); [ "$C" = 200 ] && break; sleep 0.5; done; rec wake_to_200 $(($(ms)-T0))
J=$(x $BOX 'pgrep -f "[n]ode index.js" >/dev/null && echo proc=alive || echo proc=gone; test -f app/node_modules/express/package.json && echo files=kept')
echo "    after wake: $(echo "$J" | jq -r .stdout_tail | tr '\n' ' ')"
T[proc_after_wake]=$(echo "$J" | jq -r .stdout_tail | grep -o 'proc=[a-z]*'); T[url_after_wake]=$C
if [ "$C" = 200 ]; then pass "app process + files survived sleep; URL serves again (HTTP 200)"; else
  note "URL after wake HTTP $C → restarting the app"; x $BOX "$START" 60 >/dev/null; C=$(get "$URL"); [ "$C" = 200 ] || fail "URL after restart $C"; fi

# 4b. idle sleep: idle_minutes=1, reaper every 10 s → sleeping without a call
if [ -z "${SKIP_IDLE:-}" ]; then
  T0=$(ms); S=$(wait_box $BOX sleeping 110); rec idle_to_sleep $(($(ms)-T0))
  PS=$(pstate $REF); [ "$S" = sleeping ] && [ "$PS" = paused ] && pass "idle sleep (1 min) → $PROV sandbox $PS" || fail "idle sleep: box $S provider $PS"
  T0=$(ms); KEY=$AKEY api POST /boxes/$BOX/wake -d '{}' >/dev/null; rec wake2 $(($(ms)-T0))
  C=$(get "$URL"); T[url_after_idle_wake]=$C; [ "$C" = 200 ] && pass "URL serves again after idle sleep → wake" || note "URL after idle wake HTTP $C"
fi

# 5. snapshot → break → restore
T0=$(ms); SN=$(KEY=$AKEY api POST /boxes/$BOX/snapshot -d '{}'); rec snapshot $(($(ms)-T0))
SNID=$(echo "$SN" | jq -r .id); echo "    snapshot: $(echo "$SN" | jq -c '{id,size_mb,storage,location}')"
[ "$SNID" != null ] || { echo "$SN"; fail snapshot; }
x $BOX 'rm -rf app/public && echo broken > app/index.js' >/dev/null
T0=$(ms); R=$(KEY=$AKEY api POST /boxes/$BOX/restore -d "{\"snapshot_id\":\"$SNID\"}"); rec restore $(($(ms)-T0))
J=$(x $BOX 'grep -c express app/index.js; test -d app/public && echo public=ok')
echo "$J" | jq -e '.stdout_tail|contains("public=ok")' >/dev/null && pass "restore brought back the repo (index.js + public/)" || { echo "$R $J"; fail restore; }

# 6. fork the snapshot onto a fresh sandbox (2 sandboxes at once)
if [ -z "${SKIP_FORK:-}" ]; then
  T0=$(ms); F=$(KEY=$AKEY api POST /boxes/$BOX/fork -d "{\"snapshot_id\":\"$SNID\",\"slug\":\"web-fork\"}"); rec fork $(($(ms)-T0))
  FB=$(echo "$F" | jq -r .id); [ "$(echo "$F" | jq -r .status)" = running ] || { echo "$F"; fail fork; }
  J=$(x $FB 'test -f app/node_modules/express/package.json && grep -c "Hello World" app/index.js')
  echo "$J" | jq -e '.exit_code==0' >/dev/null && pass "fork $FB on a new $PROV sandbox has the repo + node_modules" || fail "fork files"
  T0=$(ms); "$BIN/box" down $FB >/dev/null; rec fork_down $(($(ms)-T0))
fi

# 7. clone: E2B native fork (memory + disk), Novita snapshot → new sandbox
for i in $(seq 20); do [ "$(live | jq length)" -le 1 ] && break; sleep 0.5; done
T0=$(ms); CL=$(KEY=$AKEY api POST /boxes/$BOX/clone -d '{"slug":"web-clone"}'); rec clone $(($(ms)-T0))
CB=$(echo "$CL" | jq -r .id); [ "$(echo "$CL" | jq -r .status)" = running ] || { echo "$CL"; fail clone; }
echo "    clone: $(echo "$CL" | jq -c '{id,url,placement_reason}')"
CURL=$(echo "$CL" | jq -r .url)
J=$(x $CB 'pgrep -f "[n]ode index.js" >/dev/null && echo proc=alive || echo proc=gone; test -f app/index.js && echo files=ok')
echo "    clone state: $(echo "$J" | jq -r .stdout_tail | tr '\n' ' ')"; T[clone_proc]=$(echo "$J" | jq -r .stdout_tail | grep -o 'proc=[a-z]*')
C=$(get "$CURL"); T[clone_url]=$C
if [ "$C" = 200 ]; then pass "clone URL $CURL serves without a restart (process cloned)"; else
  x $CB "$START" 60 >/dev/null; C=$(get "$CURL"); [ "$C" = 200 ] && pass "clone URL serves after restarting the app (files cloned, process not)" || fail "clone URL $C"; fi
T0=$(ms); "$BIN/box" down $CB >/dev/null; rec clone_down $(($(ms)-T0))

# 8. down + provider empty
T0=$(ms); "$BIN/box" down $BOX >/dev/null; rec down $(($(ms)-T0))
for i in $(seq 40); do LEFT=$(live | jq length); [ "$LEFT" = 0 ] && break; sleep 1; done
[ "$LEFT" = 0 ] && pass "no live sandboxes left at $PROV ($(live))" || fail "$LEFT sandbox(es) left"
{ printf '{"provider":"%s","repo":"%s","url":"%s"' "$PROV" "$REPO" "$URL"; for k in "${!T[@]}"; do printf ',"%s":"%s"' "$k" "${T[$k]}"; done; echo '}'; } > "$RESULTS"
echo "    results: $(cat "$RESULTS")"
echo "== LIVE DEPLOY PASSED ($PROV)"
