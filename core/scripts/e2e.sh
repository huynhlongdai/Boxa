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
[ "$N" = 18 ] && pass "MCP HTTP tools/list → 18 tools" || fail "mcp http"
R=$(curl -s -X POST "$BOXA_URL/mcp" -H "Authorization: Bearer $AKEY" -d "{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/call\",\"params\":{\"name\":\"box_exec\",\"arguments\":{\"box_id\":\"$BOX\",\"command\":\"echo via-mcp\"}}}")
echo "$R" | jq -e '.result.isError==false and .result.structuredContent.stdout_tail=="via-mcp"' >/dev/null && pass "MCP tools/call box_exec" || fail "mcp call"
R=$(printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}' '{"jsonrpc":"2.0","method":"notifications/initialized"}' '{"jsonrpc":"2.0","id":2,"method":"tools/list"}' | "$BIN/box" mcp)
[ "$(echo "$R" | jq -s '.[] | select(.id==2) | .result.tools | length')" = 18 ] && pass "MCP stdio (box mcp) tools/list → 18 tools" || fail "mcp stdio"

echo "== v1.1 discovery (no auth)"
curl -sf "$BOXA_URL/v1/openapi.json" | jq -e '.openapi=="3.1.0" and (.paths|has("/v1/quote")) and (.paths|has("/v1/boxes/{id}/fork"))' >/dev/null \
  && pass "openapi.json 3.1 ($(curl -s $BOXA_URL/v1/openapi.json | jq '[.paths[]|keys[]]|length') operations)" || fail openapi
curl -sf "$BOXA_URL/llms.txt" | head -1 | grep -q '^# Boxa' && pass "llms.txt" || fail llms
LF=$(curl -sf -H 'Accept-Language: vi' "$BOXA_URL/llms-full.txt"); grep -q 'Bắt đầu nhanh' <<<"$LF" && grep -q '/a2a' <<<"$LF" && pass "llms-full.txt localized (vi)" || fail llms-full
curl -sf "$BOXA_URL/.well-known/mcp.json" | jq -e '.transport.url|endswith("/mcp")' >/dev/null && pass ".well-known/mcp.json" || fail mcpcard
curl -sf "$BOXA_URL/.well-known/agent.json" | jq -e '.protocolVersion=="0.3.0" and (.skills|length)==19' >/dev/null && pass ".well-known/agent.json (A2A card, exec + 18 tool skills)" || fail agentcard
for F in openai anthropic gemini mcp; do
  N=$(curl -sf "$BOXA_URL/v1/tools?format=$F" | jq 'if type=="array" then (if .[0].functionDeclarations then .[0].functionDeclarations|length else length end) else .tools|length end')
  [ "$N" = 18 ] && pass "tools?format=$F → $N" || fail "tools $F ($N)"
done
[ "$(curl -sf $BOXA_URL/v1/agent-kinds | jq '.items|length')" = 22 ] && pass "agent-kinds → 22" || fail kinds

echo "== v1.1 i18n errors"
E=$(curl -s -H "Authorization: Bearer $AKEY" -H 'Accept-Language: vi-VN,vi;q=0.9' "$BOXA_URL/v1/boxes/box_khongco"); echo "    $(echo "$E" | jq -c .error)"
echo "$E" | jq -e '.error.code=="NOT_FOUND" and (.error.message|test("không tìm thấy")) and (.error.docs_url|endswith("#NOT_FOUND"))' >/dev/null && pass "Accept-Language vi → localized message+hint, code unchanged" || fail "i18n vi"
E=$(curl -s -H "Authorization: Bearer $AKEY" -H 'Accept-Language: ja' -X POST "$BOXA_URL/v1/boxes" -d '{"template":"nope"}')
echo "$E" | jq -e '.error.code=="INVALID" and (.error.message|test("テンプレート"))' >/dev/null && pass "Accept-Language ja" || fail "i18n ja"

echo "== v1.1 quote / batch / long-poll / yaml / restore / fork"
Q=$(KEY=$AKEY api POST /quote -d '{"template":"python","lifecycle":"task","ttl_seconds":3600,"spec":{"ram_mb":512}}'); echo "    $(echo "$Q" | jq -c '{available,provider,cost_per_hour,expected_total_usd,within_budget}')"
echo "$Q" | jq -e '.available and .provider=="local" and .expected_hours==1 and (.alternatives|type=="array")' >/dev/null && pass "quote (dry run)" || fail quote
B=$(KEY=$AKEY api POST /boxes/$BOX/exec-batch -d '{"commands":["echo one","false","echo three"]}')
echo "$B" | jq -e '.ok==false and .completed==2 and .stopped_at==1 and (.next|length)>0' >/dev/null && pass "exec-batch stop_on_error (default) stops at #1" || { echo "$B"; fail batch; }
B=$(KEY=$AKEY api POST /boxes/$BOX/exec-batch -d '{"commands":["echo one","false","echo three"],"stop_on_error":false}')
echo "$B" | jq -e '.completed==3 and .results[2].stdout_tail=="three"' >/dev/null && pass "exec-batch keep going" || fail batch2
OUT=$("$BIN/box" batch "$BOX" -- 'echo a' ';;' 'echo b'); [ "$(echo "$OUT" | grep -c '^[ab]$')" = 2 ] && pass "CLI box batch" || fail "cli batch"
J=$(KEY=$AKEY api POST /boxes/$BOX/exec -d '{"command":"sleep 1.5; echo lp","async":true}')
echo "$J" | jq -e '.next[0]|test("box_job")' >/dev/null || fail "async next"
T0=$(date +%s%N); R=$(KEY=$AKEY api GET "/jobs/$(echo "$J" | jq -r .job_id)?wait=20"); T1=$(date +%s%N)
echo "$R" | jq -e '.status=="done" and .result.stdout_tail=="lp"' >/dev/null && pass "job long-poll ?wait=20 returned on completion ($(( (T1-T0)/1000000 ))ms)" || fail longpoll
X=$(KEY=$AKEY api POST /boxes/$BOX/exec -d '{"command":"seq 1 300; exit 4","tail_lines":3}')
echo "$X" | jq -e '(.next|join(" "))|test("box_logs")' >/dev/null && pass "exec next hints: $(echo "$X" | jq -c .next)" || fail next
KEY=$AKEY api GET /boxes/$BOX/events | jq -e '[.items[]|select(.type=="box.exec" and .data.log_id!=null and .data.tokens_saved>0)]|length>0' >/dev/null && pass "exec events carry tokens_saved + log_id" || fail evdata
KEY=$AKEY api GET /boxes/$BOX/yaml | jq -e '.yaml|test("template: python")' >/dev/null && pass "GET /boxes/:id/yaml ($("$BIN/box" yaml "$BOX" | head -1))" || fail yaml
echo v1 | "$BIN/box" write "$BOX" /work/state.txt >/dev/null
SNAP2=$(KEY=$AKEY api POST /boxes/$BOX/snapshot | jq -r .id)
echo v2 | "$BIN/box" write "$BOX" /work/state.txt >/dev/null
KEY=$AKEY api POST /boxes/$BOX/restore -d "{\"snapshot_id\":\"$SNAP2\"}" >/dev/null
[ "$("$BIN/box" read "$BOX" /work/state.txt)" = v1 ] && pass "restore snapshot $SNAP2 (v2 → v1)" || fail restore
FORK=$("$BIN/box" fork "$BOX" "$SNAP2" --slug e2e-fork | awk '{print $1}')
[ "$("$BIN/box" read "$FORK" /work/state.txt)" = v1 ] && pass "fork → $FORK has the snapshot files" || fail fork
"$BIN/box" read "$FORK" /AGENTS.md | grep -q "$SNAP2" && pass "fork AGENTS.md mentions source snapshot" || fail forkmd
"$BIN/box" down "$FORK" >/dev/null
R=$(curl -s -X POST "$BOXA_URL/mcp" -H "Authorization: Bearer $AKEY" -H 'Accept-Language: zh' -d '{"jsonrpc":"2.0","id":9,"method":"prompts/get","params":{"name":"debug-failing-command","arguments":{"box_id":"x","command":"pytest"}}}')
echo "$R" | jq -e '.result.messages[0].content.text|test("调试")' >/dev/null && pass "MCP prompts/get localized (zh)" || fail prompt
R=$(curl -s -X POST "$BOXA_URL/mcp" -H "Authorization: Bearer $AKEY" -d "{\"jsonrpc\":\"2.0\",\"id\":10,\"method\":\"resources/read\",\"params\":{\"uri\":\"boxa://box/$BOX/file/work/state.txt\"}}")
echo "$R" | jq -e '.result.contents[0].text=="v1\n"' >/dev/null && pass "MCP resources/read boxa://box/…/file/work/state.txt" || fail resource

echo "== v1.2 providers / egress / A2A / notify / storage"
P=$(api GET /providers); echo "    providers: $(echo "$P" | jq -c '[.items[]|{id,implemented,chain_rank}]')"
echo "$P" | jq -e '[.items[]|select(.implemented and (.id=="digitalocean" or .id=="contabo" or .id=="netcup"))]|length==3' >/dev/null && pass "DigitalOcean / Contabo / netcup adapters registered (fallback chain)" || fail providers
[ "$(api GET /providers/netcup/regions | jq '.items|length')" = 4 ] && pass "provider regions" || fail regions
EB=$(KEY=$AKEY api POST /boxes -d '{"template":"base","slug":"e2e-egress","spec":{"ram_mb":256},"egress":{"allow":["pypi.org","*.github.com"],"deny_by_default":true}}')
echo "    egress: $(echo "$EB" | jq -c .egress)"
echo "$EB" | jq -e '.egress.enforced==false and .egress.mode=="none" and (.egress.warning|test("NOT enforced"))' >/dev/null && pass "egress allowlist on local driver → clear warning (not enforced)" || fail egress
"$BIN/box" down "$(echo "$EB" | jq -r .id)" >/dev/null
A=$(curl -s -X POST "$BOXA_URL/a2a" -H "Authorization: Bearer $AKEY" -d "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"message/send\",\"params\":{\"message\":{\"role\":\"user\",\"parts\":[{\"kind\":\"text\",\"text\":\"echo a2a-ok\"}]},\"metadata\":{\"box_id\":\"$BOX\"}}}")
echo "$A" | jq -e '.result.status.state=="completed" and (.result.artifacts[0].parts[1].text|test("a2a-ok"))' >/dev/null && pass "A2A message/send → task $(echo "$A" | jq -r .result.id) completed" || { echo "$A"; fail a2a; }
TID=$(echo "$A" | jq -r .result.id)
curl -s -X POST "$BOXA_URL/a2a" -H "Authorization: Bearer $AKEY" -d "{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tasks/get\",\"params\":{\"id\":\"$TID\"}}" | jq -e '.result.status.state=="completed"' >/dev/null && pass "A2A tasks/get" || fail a2aget
S=$(curl -sN -X POST "$BOXA_URL/a2a" -H "Authorization: Bearer $AKEY" -d "{\"jsonrpc\":\"2.0\",\"id\":3,\"method\":\"tasks/sendSubscribe\",\"params\":{\"message\":{\"role\":\"user\",\"parts\":[{\"type\":\"text\",\"text\":\"echo sse\"}]},\"metadata\":{\"box_id\":\"$BOX\"}}}")
echo "$S" | grep '^data: ' | tail -1 | sed 's/^data: //' | jq -e '.result.final==true and .result.status.state=="completed"' >/dev/null && pass "A2A tasks/sendSubscribe (SSE, $(echo "$S" | grep -c '^data: ') events)" || { echo "$S"; fail a2asse; }
curl -sf "$BOXA_URL/.well-known/agent.json" | jq -e '.capabilities.streaming and (.url|endswith("/a2a"))' >/dev/null && pass "agent card advertises /a2a + streaming" || fail card
api GET /notify | jq -e '(.events_available|length)==6 and .telegram_configured==false' >/dev/null && pass "GET /notify" || fail notify
api POST /notify/test -d '{"channel":"telegram"}' | jq -e '.results[0].ok==false' >/dev/null && pass "notify test reports unconfigured channel" || fail notifytest
api GET /storage | jq -e '.configured==false and .provider=="local"' >/dev/null && pass "storage: local fallback when unconfigured" || fail storage

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
