#!/usr/bin/env bash
# End-to-end smoke test with real binaries: boxa-core + boxd (local driver) + box CLI.
# Requires: go (or prebuilt bin/), curl, jq, python3. No Docker/Incus/Postgres needed.
set -euo pipefail
cd "$(dirname "$0")/.."
ROOT=$(pwd)
BIN=${BIN:-$ROOT/bin}
PORT=${PORT:-18765}
WORK=$(mktemp -d /tmp/boxa-e2e.XXXXXX)
ADMIN=bx_admin_e2e_$(date +%s)
export BOXA_URL=http://127.0.0.1:$PORT

pass() { printf '  \033[32mPASS\033[0m %s\n' "$*"; }
fail() { printf '  \033[31mFAIL\033[0m %s\n' "$*"; echo "--- core.log"; tail -30 "$WORK/core.log"; echo "--- boxd.log"; tail -30 "$WORK/boxd.log"; exit 1; }
api() { local m=$1 p=$2; shift 2; curl -sS -X "$m" "$BOXA_URL/v1$p" -H "Authorization: Bearer ${KEY:-$ADMIN}" -H 'Content-Type: application/json' "$@"; }

cleanup() { kill ${CORE_PID:-} ${BOXD_PID:-} 2>/dev/null || true; wait 2>/dev/null || true; [ -n "${KEEP:-}" ] || rm -rf "$WORK"; }
trap cleanup EXIT

if [ -z "${SKIP_BUILD:-}" ]; then
  echo "== build"
  go build -o "$BIN/" ./cmd/...
fi

echo "== start core + boxd (workdir $WORK)"
BOXA_DATA_DIR=$WORK/data BOXA_ADMIN_KEY=$ADMIN BOXA_BOOTSTRAP_TOKEN=boot-e2e BOXA_ADDR=127.0.0.1:$PORT \
BOXA_PRICE_FEED=off BOXA_LEDGER_INTERVAL=1s BOXA_LOCAL_PRICE_MONTH=73 \
  "$BIN/boxa-core" >"$WORK/core.log" 2>&1 & CORE_PID=$!
for i in $(seq 50); do curl -sf "$BOXA_URL/v1/health" >/dev/null && break; sleep 0.1; done
curl -sf "$BOXA_URL/v1/health" | jq -e '.ok == true' >/dev/null && pass "core healthy: $(curl -s $BOXA_URL/v1/health | jq -c .components)" || fail health
BOXD_CORE_URL=$BOXA_URL BOXD_TOKEN=boot-e2e BOXD_DRIVER=local BOXD_STATE_DIR=$WORK/boxd BOXD_IP=127.0.0.1 \
  "$BIN/boxd" >"$WORK/boxd.log" 2>&1 & BOXD_PID=$!
for i in $(seq 50); do [ "$(api GET /hosts | jq '[.items[]|select(.connected)]|length')" = 1 ] && break; sleep 0.1; done
H=$(api GET /hosts | jq -c '.items[0] | {id,provider,status,connected,cpu,ram_mb,price_hour}')
echo "$H" | jq -e '.connected and .status=="ready"' >/dev/null && pass "boxd dialed out and registered: $H" || fail "host not connected"

echo "== agent + key"
AGENT=$(api POST /agents -d '{"name":"e2e-agent","kind":"claude-code","budget_day_usd":5}' | jq -r .id)
KEYJSON=$(api POST /keys -d "{\"agent_id\":\"$AGENT\",\"name\":\"e2e\"}")
AKEY=$(echo "$KEYJSON" | jq -r .key)
[[ $AKEY == bx_agent_* ]] && pass "agent $AGENT key $(echo "$KEYJSON" | jq -r .prefix)… scopes=$(echo "$KEYJSON" | jq -c .scopes)" || fail "key"
export BOXA_API_KEY=$AKEY

echo "== box up"
OUT=$("$BIN/box" up --template python --lifecycle session --slug e2e --ram 512M --idempotency-key e2e-1)
echo "$OUT" | sed 's/^/    /'
BOX=$(echo "$OUT" | head -1 | awk '{print $1}')
[[ $OUT == *running* ]] && pass "box up → $BOX running" || fail "box up"
AGAIN=$("$BIN/box" up --template python --idempotency-key e2e-1 | head -1 | awk '{print $1}')
[ "$AGAIN" = "$BOX" ] && pass "idempotency: same key returned same box" || fail "idempotency $AGAIN != $BOX"

echo "== exec"
OUT=$("$BIN/box" exec "$BOX" -- 'echo hi && ls'); echo "$OUT" | sed 's/^/    /'
[[ $(echo "$OUT" | head -1) == hi ]] && pass "exec 'echo hi && ls'" || fail exec
J=$(KEY=$AKEY api POST /boxes/$BOX/exec -d '{"command":"seq 1 1000; echo warn >&2; exit 7","tail_lines":5}')
echo "    $(echo "$J" | jq -c '{exit_code,truncated,log_id,tokens_saved,stdout_tail,stderr_tail}')"
echo "$J" | jq -e '.exit_code==7 and .truncated and .log_id!=null and .tokens_saved>0' >/dev/null && pass "output shaper (tail 5, truncated, log_id, tokens_saved)" || fail shaper
LOGID=$(echo "$J" | jq -r .log_id)
[ "$("$BIN/box" log "$LOGID" | grep -c '^[0-9]*$')" -ge 1000 ] && pass "full log via /logs/$LOGID" || fail "log"
set +e; "$BIN/box" exec "$BOX" -- 'exit 3' >/dev/null 2>&1; RC=$?; set -e
[ $RC = 3 ] && pass "CLI propagates exit code" || fail "exit code $RC"

echo "== files"
printf 'line one\nline two\nneedle here\n' | "$BIN/box" write "$BOX" /work/notes/a.txt >/dev/null
[ "$("$BIN/box" read "$BOX" /work/notes/a.txt --from 2 --to 3)" = $'line two\nneedle here' ] && pass "write + read line range" || fail read
[[ $("$BIN/box" grep "$BOX" needle) == "/work/notes/a.txt:3:needle here" ]] && pass "grep" || fail grep
J=$(KEY=$AKEY api POST /boxes/$BOX/exec -d '{"command":"touch made.txt"}')
echo "$J" | jq -e '.files_changed | index("/work/made.txt")' >/dev/null && pass "files_changed reported" || fail files_changed

echo "== async job"
JOB=$("$BIN/box" exec "$BOX" --async -- 'sleep 1; echo async-done' | jq -r .job_id)
R=$("$BIN/box" job "$JOB" --wait 10)
echo "$R" | jq -e '.status=="done" and .result.stdout_tail=="async-done"' >/dev/null && pass "async job $JOB done" || { echo "$R"; fail job; }

echo "== sleep / wake / snapshot"
[[ $("$BIN/box" sleep "$BOX") == *sleeping* ]] && pass "sleep" || fail sleep
[ "$("$BIN/box" exec "$BOX" -- 'cat notes/a.txt | wc -l' | tr -d ' ')" = 3 ] && pass "exec auto-wakes sleeping box, /work preserved" || fail autowake
"$BIN/box" sleep "$BOX" >/dev/null
[[ $("$BIN/box" wake "$BOX") == *running* ]] && pass "wake" || fail wake
SNAP=$("$BIN/box" snapshot "$BOX"); echo "    $SNAP"
SNAPFILE=$(echo "$SNAP" | awk '{print $NF}' | sed 's#local://##')
tar tzf "$SNAPFILE" | grep -q 'work/notes/a.txt' && pass "snapshot = tar.gz containing work/notes/a.txt" || fail snapshot

echo "== MCP"
R=$(curl -s -X POST "$BOXA_URL/mcp" -H "Authorization: Bearer $AKEY" -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}')
N=$(echo "$R" | jq '.result.tools | length'); echo "    tools: $(echo "$R" | jq -c '[.result.tools[].name]')"
[ "$N" = 12 ] && pass "MCP HTTP tools/list → 12 tools" || fail "mcp http"
R=$(curl -s -X POST "$BOXA_URL/mcp" -H "Authorization: Bearer $AKEY" -d "{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/call\",\"params\":{\"name\":\"box_exec\",\"arguments\":{\"box_id\":\"$BOX\",\"command\":\"echo via-mcp\"}}}")
echo "$R" | jq -e '.result.isError==false and .result.structuredContent.stdout_tail=="via-mcp"' >/dev/null && pass "MCP tools/call box_exec" || fail "mcp call"
R=$(printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}' '{"jsonrpc":"2.0","method":"notifications/initialized"}' '{"jsonrpc":"2.0","id":2,"method":"tools/list"}' | "$BIN/box" mcp)
[ "$(echo "$R" | jq -s '.[] | select(.id==2) | .result.tools | length')" = 12 ] && pass "MCP stdio (box mcp) tools/list → 12 tools" || fail "mcp stdio"

echo "== ledger / overview"
sleep 2.5
C=$(KEY=$ADMIN api GET /costs); echo "    summary: $(echo "$C" | jq -c .summary) entries=$(echo "$C" | jq '.items|length')"
echo "$C" | jq -e '.summary.total > 0 and (.items|length) > 0 and .summary.pool_usd > 0' >/dev/null && pass "per-tick ledger entries" || fail ledger
O=$(KEY=$ADMIN api GET /overview); echo "    overview: $(echo "$O" | jq -c '{spent_today,budget_day,boxes_running,boxes_sleeping,saved_vs_247_usd,top_tasks}')"
echo "$O" | jq -e '.spent_today > 0 and .boxes_running >= 1 and (.cost_7d|length)==7' >/dev/null && pass "overview aggregates" || fail overview
KEY=$ADMIN api GET /costs.csv | head -1 | grep -q '^id,ts,agent_id' && pass "costs.csv" || fail csv

echo "== down"
[[ $("$BIN/box" down "$BOX") == *terminated* ]] && pass "down" || fail down
EV=$(KEY=$AKEY api GET /boxes/$BOX/events | jq -r '.items | reverse | map(.type) | join(" → ")')
echo "    timeline: $EV"
[[ $EV == *box.terminated* ]] && pass "event timeline recorded" || fail events

echo "== ALL PASSED"
