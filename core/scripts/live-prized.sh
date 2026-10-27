#!/usr/bin/env bash
# Live end-to-end test against the real Prized Sandboxes API (creates ONE
# small free-tier sandbox for well under a minute, then deletes it).
#   PRIZED_API_KEY=dcp_… scripts/live-prized.sh          (shim / T3 mode)
#   PRIZED_MODE=dial BOXA_PUBLIC_URL=https://… …          (boxd dial-in / T2)
# Starts boxa-core + a local boxd, connects prized, asks /recommend for
# untrusted short-lived work, creates a box on prized, runs exec (incl. a
# long-output command to see compaction), files, then box down and checks
# that no boxa sandbox is left at Prized.
set -euo pipefail
cd "$(dirname "$0")/.."
ROOT=$(pwd)
BIN=${BIN:-$ROOT/bin}
PORT=${PORT:-18775}
WORK=$(mktemp -d /tmp/boxa-live.XXXXXX)
ADMIN=bx_admin_live_$(date +%s)
MODE=${PRIZED_MODE:-shim}
PAPI=https://api.prized.dev/api/v1
export BOXA_URL=http://127.0.0.1:$PORT
: "${PRIZED_API_KEY:?set PRIZED_API_KEY}"

pass() { printf '  PASS %s\n' "$*"; }
fail() { printf '  FAIL %s\n' "$*"; echo "--- core.log"; tail -40 "$WORK/core.log"; exit 1; }
api() { local m=$1 p=$2; shift 2; curl -sS -X "$m" "$BOXA_URL/v1$p" -H "Authorization: Bearer ${KEY:-$ADMIN}" -H 'Content-Type: application/json' "$@"; }
papi() { curl -sS -H "Authorization: Bearer $PRIZED_API_KEY" "$@"; }
ms() { date +%s%3N; }

cleanup() {
  # safety net: delete any sandbox this run left behind (label boxa=1)
  papi -G -X DELETE "$PAPI/sandboxes" --data-urlencode "label=boxa=1" >/dev/null 2>&1 || true
  kill ${CORE_PID:-} ${BOXD_PID:-} 2>/dev/null || true; wait 2>/dev/null || true
  [ -n "${KEEP:-}" ] || rm -rf "$WORK"
}
trap cleanup EXIT

[ -n "${SKIP_BUILD:-}" ] || go build -o "$BIN/" ./cmd/...

BOXA_DATA_DIR=$WORK/data BOXA_ADMIN_KEY=$ADMIN BOXA_BOOTSTRAP_TOKEN=boot-live BOXA_ADDR=127.0.0.1:$PORT \
BOXA_AUTO_SANDBOX=1 BOXA_PRICE_FEED=off BOXA_LOCAL_PRICE_MONTH=73 BOXA_CLIENT_LOCATION=VN \
  "$BIN/boxa-core" >"$WORK/core.log" 2>&1 & CORE_PID=$!
for i in $(seq 50); do curl -sf "$BOXA_URL/v1/health" >/dev/null && break; sleep 0.1; done
curl -sf "$BOXA_URL/v1/health" >/dev/null || fail "core did not start"
BOXD_CORE_URL=$BOXA_URL BOXD_TOKEN=boot-live BOXD_DRIVER=local BOXD_STATE_DIR=$WORK/boxd BOXD_IP=127.0.0.1 \
  "$BIN/boxd" >"$WORK/boxd.log" 2>&1 & BOXD_PID=$!
for i in $(seq 50); do [ "$(api GET /hosts | jq '[.items[]|select(.connected)]|length')" = 1 ] && break; sleep 0.1; done
pass "core + local boxd up"

AGENT=$(api POST /agents -d '{"name":"live","kind":"claude-code","budget_day_usd":1}' | jq -r .id)
export BOXA_API_KEY=$(api POST /keys -d "{\"agent_id\":\"$AGENT\",\"name\":\"live\"}" | jq -r .key)
AKEY=$BOXA_API_KEY

R=$(api PUT /providers/prized/token -d "{\"token\":\"$PRIZED_API_KEY\"}")
echo "$R" | jq -e '.connected' >/dev/null && pass "prized connected (token validated live): $(echo "$R" | jq -c '{id,group,adapter,connected}')" || { echo "$R"; fail connect; }
api PUT /providers/prized/prefs -d "{\"sandbox_mode\":\"$MODE\"}" | jq -e ".sandbox_mode==\"$MODE\"" >/dev/null && pass "sandbox_mode=$MODE" || fail prefs

REQ='"requirements":{"isolation":"sandbox","untrusted":true,"cpu":1,"ram_mb":1024,"lifetime":"ephemeral","duration_hours":0.25}'
R=$(KEY=$AKEY api POST /recommend -d "{\"template\":\"base\",$REQ}")
echo "    recommend pick: $(echo "$R" | jq -c '.pick|{provider,tier,kind,sku,cost_per_hour,strong_isolation,requires_approval}')"
echo "    options: $(echo "$R" | jq -c '[.options[]|"\(.provider)/\(.sku) $\(.cost_per_hour)"]|.[0:5]')"
echo "    rejected: $(echo "$R" | jq -c '[.rejected[]|"\(.provider): \(.reason)"]|.[0:4]')"
echo "    auto_pick: $(echo "$R" | jq -r .auto_pick)"
echo "$R" | jq -e '.pick.provider=="prized" and .pick.requires_approval==false' >/dev/null && pass "recommend picks prized (connected, no approval) for untrusted ephemeral work" || fail "recommend pick"

T0=$(ms)
B=$(KEY=$AKEY api POST /boxes -d "{\"template\":\"base\",\"slug\":\"live-prized\",$REQ}")
BOX=$(echo "$B" | jq -r .id)
[ "$BOX" != null ] || { echo "$B"; fail "box create"; }
for i in $(seq 240); do S=$(KEY=$AKEY api GET /boxes/$BOX | jq -r .status); [ "$S" = running ] || [ "$S" = failed ] && break; sleep 0.25; done
T1=$(ms)
INFO=$(KEY=$AKEY api GET /boxes/$BOX | jq -c '{id,status,provider,host_id}')
echo "    box: $INFO"
[ "$S" = running ] && pass "box up on prized in $((T1-T0)) ms" || { KEY=$AKEY api GET /boxes/$BOX/events | jq -c '.items[]|{type,msg}' | tail -8; fail "box not running ($S)"; }
HID=$(echo "$B" | jq -r .host_id)
H=$(api GET /hosts | jq -c --arg h "$HID" '[.items[]|select(.id==$h)][0]|{provider,driver,billing,provider_ref}')
echo "    host: $H"
REF=$(echo "$H" | jq -r .provider_ref)
papi "$PAPI/sandboxes" | jq -e --arg r "$REF" 'any(.sandboxes[]; .id==$r and .state=="running")' >/dev/null && pass "sandbox $REF running at Prized" || echo "    (could not match provider_ref=$REF in Prized list)"

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
T0=$(ms); R=$(curl -s -X POST "$BOXA_URL/mcp" -H "Authorization: Bearer $AKEY" -d "{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/call\",\"params\":{\"name\":\"box_exec\",\"arguments\":{\"box_id\":\"$BOX\",\"command\":\"echo via-mcp\"}}}"); T1=$(ms)
echo "$R" | jq -e '.result.isError==false and .result.structuredContent.stdout_tail=="via-mcp"' >/dev/null && pass "MCP tools/call box_exec ($((T1-T0)) ms)" || { echo "$R"; fail "mcp exec"; }
OUT=$("$BIN/box" exec "$BOX" -- 'echo cli-ok && pwd'); echo "    box exec: $(echo $OUT)"
[[ $OUT == cli-ok* ]] && pass "box CLI exec" || fail "cli exec"
printf 'line one\nneedle here\n' | "$BIN/box" write "$BOX" /work/live/a.txt >/dev/null
[ "$("$BIN/box" read "$BOX" /work/live/a.txt --from 2 --to 2)" = "needle here" ] && pass "box write + read" || echo "    (write/read failed)"

T0=$(ms); "$BIN/box" down "$BOX" >/dev/null; T1=$(ms)
gone=""
for i in $(seq 40); do
  papi "$PAPI/sandboxes" | jq -e --arg r "$REF" 'all(.sandboxes[]; .id!=$r or .state=="deleting")' >/dev/null && { gone=1; break; }
  sleep 0.5
done
[ -n "$gone" ] && pass "box down ($((T1-T0)) ms) → Prized sandbox deleting/gone" || fail "sandbox still alive at Prized"
sleep 3
LEFT=$(papi "$PAPI/sandboxes" | jq '[.sandboxes[]|select(.state!="deleting")]|length')
[ "$LEFT" = 0 ] && pass "no live sandboxes left at Prized" || fail "$LEFT sandbox(es) left"
echo "== LIVE PRIZED PASSED ($MODE)"
