#!/usr/bin/env bash
# Live end-to-end test against the real E2B API, or Novita Sandbox (the same
# E2B-compatible driver): creates ONE base-template sandbox for well under a
# minute, then deletes it.
#   E2B_API_KEY=e2b_… scripts/live-e2b.sh                 (shim / T3 mode)
#   NOVITA_API_KEY=sk_… scripts/live-novita.sh           (same flow, Novita)
#   E2B_MODE=dial BOXAZ_PUBLIC_URL=https://… …             (boxd dial-in / T2)
# Starts boxaz-core + a local boxd, connects the provider, asks /recommend for
# untrusted short-lived work (and with fork=true), creates a box, runs exec
# (API, MCP, CLI, a 200k-line command to see compaction), files, then box
# down and checks that no managed sandbox is left at the provider.
set -euo pipefail
cd "$(dirname "$0")/.."
ROOT=$(pwd)
BIN=${BIN:-$ROOT/bin}
PORT=${PORT:-18776}
WORK=$(mktemp -d /tmp/sbx-live.XXXXXX)
ADMIN=bx_admin_live_$(date +%s)
PROV=${SANDBOX_PROVIDER:-e2b}
MODE=${E2B_MODE:-shim}
export BOXAZ_URL=http://127.0.0.1:$PORT
case $PROV in
  e2b)    : "${E2B_API_KEY:?set E2B_API_KEY}"; PKEY=$E2B_API_KEY; PAPI=https://api.e2b.app; PHDR=X-API-Key ;;
  novita) : "${NOVITA_API_KEY:?set NOVITA_API_KEY}"; PKEY=$NOVITA_API_KEY; PAPI=https://api.us-phx-1.sandbox.novita.ai; PHDR=X-API-KEY ;;
  *) echo "SANDBOX_PROVIDER must be e2b or novita"; exit 2 ;;
esac

pass() { printf '  PASS %s\n' "$*"; }
fail() { printf '  FAIL %s\n' "$*"; echo "--- core.log"; tail -40 "$WORK/core.log"; exit 1; }
api() { local m=$1 p=$2; shift 2; curl -sS -X "$m" "$BOXAZ_URL/v1$p" -H "Authorization: Bearer ${KEY:-$ADMIN}" -H 'Content-Type: application/json' "$@"; }
papi() { curl -sS -H "$PHDR: $PKEY" "$@"; }
# The list endpoint lags a few seconds behind create/kill (verified live
# 2026-10-27); GET /sandboxes/{id} is authoritative (404 once killed).
live() { papi "$PAPI/v2/sandboxes" | jq -c '[.[]|select(.state!="killed")]'; }
one() { papi -w '\n%{http_code}' "$PAPI/sandboxes/$1"; }
ms() { date +%s%3N; }

cleanup() {
  # safety net: delete every sandbox this run left behind (metadata managed=1)
  for id in $(papi "$PAPI/v2/sandboxes" 2>/dev/null | jq -r '.[]?|select(.metadata.managed=="1")|.sandboxID' 2>/dev/null); do
    papi -X DELETE "$PAPI/sandboxes/$id" >/dev/null 2>&1 || true
  done
  [ -z "${REF:-}" ] || papi -X DELETE "$PAPI/sandboxes/$REF" >/dev/null 2>&1 || true
  kill ${CORE_PID:-} ${BOXD_PID:-} 2>/dev/null || true; wait 2>/dev/null || true
  [ -n "${KEEP:-}" ] || rm -rf "$WORK"
}
trap cleanup EXIT

[ -n "${SKIP_BUILD:-}" ] || go build -o "$BIN/" ./cmd/...

# only the provider under test is connected (other keys in the env would
# compete in /recommend)
OTHER=E2B_API_KEY; [ "$PROV" = e2b ] && OTHER=NOVITA_API_KEY
env -u $OTHER -u PRIZED_API_KEY BOXAZ_DATA_DIR=$WORK/data BOXAZ_ADMIN_KEY=$ADMIN BOXAZ_BOOTSTRAP_TOKEN=boot-live BOXAZ_ADDR=127.0.0.1:$PORT \
BOXAZ_AUTO_SANDBOX=1 BOXAZ_PRICE_FEED=off BOXAZ_LOCAL_PRICE_MONTH=73 BOXAZ_CLIENT_LOCATION=VN \
  "$BIN/boxaz-core" >"$WORK/core.log" 2>&1 & CORE_PID=$!
for i in $(seq 50); do curl -sf "$BOXAZ_URL/v1/health" >/dev/null && break; sleep 0.1; done
curl -sf "$BOXAZ_URL/v1/health" >/dev/null || fail "core did not start"
BOXD_CORE_URL=$BOXAZ_URL BOXD_TOKEN=boot-live BOXD_DRIVER=local BOXD_STATE_DIR=$WORK/boxd BOXD_IP=127.0.0.1 \
  "$BIN/boxd" >"$WORK/boxd.log" 2>&1 & BOXD_PID=$!
for i in $(seq 50); do [ "$(api GET /hosts | jq '[.items[]|select(.connected)]|length')" = 1 ] && break; sleep 0.1; done
pass "core + local boxd up"

AGENT=$(api POST /agents -d '{"name":"live","kind":"claude-code","budget_day_usd":1}' | jq -r .id)
export BOXAZ_API_KEY=$(api POST /keys -d "{\"agent_id\":\"$AGENT\",\"name\":\"live\"}" | jq -r .key)
AKEY=$BOXAZ_API_KEY

R=$(api PUT /providers/$PROV/token -d "{\"token\":\"$PKEY\"}")
echo "$R" | jq -e '.connected' >/dev/null && pass "$PROV connected (token validated live): $(echo "$R" | jq -c '{id,group,adapter,connected}')" || { echo "$R"; fail connect; }
api PUT /providers/$PROV/prefs -d "{\"sandbox_mode\":\"$MODE\"}" | jq -e ".sandbox_mode==\"$MODE\"" >/dev/null && pass "sandbox_mode=$MODE" || fail prefs

REQ='"requirements":{"isolation":"sandbox","untrusted":true,"cpu":1,"ram_mb":512,"lifetime":"ephemeral","duration_hours":0.25}'
R=$(KEY=$AKEY api POST /recommend -d "{\"template\":\"base\",$REQ}")
echo "    recommend pick: $(echo "$R" | jq -c '.pick|{provider,tier,kind,sku,cost_per_hour,strong_isolation,requires_approval}')"
echo "    options: $(echo "$R" | jq -c '[.options[]|"\(.provider)/\(.sku) $\(.cost_per_hour)"]|.[0:8]')"
echo "$R" | jq -c '.options[]|select(.provider=="'"$PROV"'")|{provider,sku,cost_per_hour,score,requires_approval,notes}'
echo "    rejected: $(echo "$R" | jq -c '[.rejected[]|"\(.provider): \(.reason)"]')"
echo "    auto_pick: $(echo "$R" | jq -r .auto_pick)"
echo "$R" | jq -e --arg p "$PROV" '.pick.provider==$p and .pick.requires_approval==false' >/dev/null && pass "recommend picks $PROV (connected, no approval) for untrusted ephemeral work" || fail "recommend pick"
REQF=${REQ%\}}',"fork":true}'
R=$(KEY=$AKEY api POST /recommend -d "{\"template\":\"base\",$REQF}")
echo "    recommend fork=true pick: $(echo "$R" | jq -c '.pick|{provider,tier,kind,sku,fork,cost_per_hour}')"
echo "$R" | jq -e --arg p "$PROV" '.pick.provider==$p' >/dev/null && pass "recommend fork=true picks $PROV (fork=$(echo "$R" | jq -r .pick.fork))" || echo "    (fork=true picked $(echo "$R" | jq -r .pick.provider))"

T0=$(ms)
B=$(KEY=$AKEY api POST /boxes -d "{\"template\":\"base\",\"slug\":\"live-$PROV\",$REQ}")
BOX=$(echo "$B" | jq -r .id)
[ "$BOX" != null ] || { echo "$B"; fail "box create"; }
for i in $(seq 240); do S=$(KEY=$AKEY api GET /boxes/$BOX | jq -r .status); [ "$S" = running ] || [ "$S" = failed ] && break; sleep 0.25; done
T1=$(ms)
INFO=$(KEY=$AKEY api GET /boxes/$BOX | jq -c '{id,status,provider,host_id}')
echo "    box: $INFO"
[ "$S" = running ] && pass "box up on $PROV in $((T1-T0)) ms" || { KEY=$AKEY api GET /boxes/$BOX/events | jq -c '.items[]|{type,msg}' | tail -8; fail "box not running ($S)"; }
HID=$(echo "$B" | jq -r .host_id)
H=$(api GET /hosts | jq -c --arg h "$HID" '[.items[]|select(.id==$h)][0]|{provider,driver,billing,provider_ref}')
echo "    host: $H"
REF=$(echo "$H" | jq -r .provider_ref)
REC=$(one "$REF" | head -1)
echo "$REC" | jq -e '.state=="running" and .metadata.managed=="1"' >/dev/null && pass "sandbox $REF running at $PROV (metadata: $(echo "$REC" | jq -c '.metadata|with_entries(select(.key|startswith("platform")|not))'), $(echo "$REC" | jq -c '{cpuCount,memoryMB,diskSizeMB,envdVersion}'))" || fail "sandbox $REF not running at $PROV: $REC"
echo "$REC" | jq -e '[.. | strings | ascii_downcase | select(contains("boxaz"))] | length == 0' >/dev/null && pass "no product name in the provider-visible sandbox record" || fail "product name leaked to $PROV"

for c in 'uname -a' 'echo hi' "python3 -c 'print(sum(range(10**6)))'"; do
  T0=$(ms); J=$(KEY=$AKEY api POST /boxes/$BOX/exec -d "$(jq -n --arg c "$c" '{command:$c}')"); T1=$(ms)
  echo "    exec [$c] $((T1-T0)) ms → $(echo "$J" | jq -c '{exit_code,stdout_tail,duration_ms}')"
  echo "$J" | jq -e '.exit_code==0' >/dev/null || fail "exec $c"
done
pass "exec via API"
J=$(KEY=$AKEY api POST /boxes/$BOX/exec -d '{"command":"seq 1 200000; echo warn >&2; exit 7","tail_lines":5}')
echo "    long output: $(echo "$J" | jq -c '{exit_code,truncated,log_id,tokens_saved,stdout_tail,stderr_tail}')"
echo "$J" | jq -e '.exit_code==7 and .truncated and .tokens_saved>0' >/dev/null && pass "output compaction (tail 5 of 200k lines, exit 7)" || fail compaction
LOGID=$(echo "$J" | jq -r .log_id)
[ "$LOGID" != null ] && [ "$("$BIN/box" log "$LOGID" | grep -c '^[0-9]*$')" -ge 200000 ] && pass "full output kept, read back via /logs/$LOGID" || fail "full log"
T0=$(ms); R=$(curl -s -X POST "$BOXAZ_URL/mcp" -H "Authorization: Bearer $AKEY" -d "{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/call\",\"params\":{\"name\":\"box_exec\",\"arguments\":{\"box_id\":\"$BOX\",\"command\":\"echo via-mcp\"}}}"); T1=$(ms)
echo "$R" | jq -e '.result.isError==false and .result.structuredContent.stdout_tail=="via-mcp"' >/dev/null && pass "MCP tools/call box_exec ($((T1-T0)) ms)" || { echo "$R"; fail "mcp exec"; }
OUT=$("$BIN/box" exec "$BOX" -- 'echo cli-ok && pwd'); echo "    box exec: $(echo $OUT)"
[[ $OUT == cli-ok* ]] && pass "box CLI exec" || fail "cli exec"
printf 'line one\nneedle here\n' | "$BIN/box" write "$BOX" /work/live/a.txt >/dev/null
[ "$("$BIN/box" read "$BOX" /work/live/a.txt --from 2 --to 2)" = "needle here" ] && pass "box write + read" || echo "    (write/read failed)"

T0=$(ms); "$BIN/box" down "$BOX" >/dev/null; T1=$(ms)
gone=""
for i in $(seq 40); do
  [ "$(one "$REF" | tail -1)" = 404 ] && { gone=1; break; }
  sleep 0.5
done
[ -n "$gone" ] && pass "box down ($((T1-T0)) ms) → $PROV sandbox gone (GET 404)" || fail "sandbox still alive at $PROV"
for i in $(seq 30); do LEFT=$(live | jq length); [ "$LEFT" = 0 ] && break; sleep 1; done
[ "$LEFT" = 0 ] && pass "no live sandboxes left at $PROV" || fail "$LEFT sandbox(es) left"
echo "== LIVE SANDBOX PASSED ($PROV, $MODE)"
