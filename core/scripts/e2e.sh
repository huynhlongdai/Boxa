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

cleanup() { kill ${CORE_PID:-} ${BOXD_PID:-} ${FSB_PID:-} 2>/dev/null || true; wait 2>/dev/null || true; [ -n "${KEEP:-}" ] || rm -rf "$WORK"; }
trap cleanup EXIT

if [ -z "${SKIP_BUILD:-}" ]; then
  echo "== build"
  go build -o "$BIN/" ./cmd/...
fi

# v1.4: a declarative (YAML) sandbox adapter pointed at scripts/fake-sandbox.py, an exec-only REST
# service on localhost — exercises the YAML engine + T3 shim end to end without any cloud account.
FSB_PORT=${FSB_PORT:-18766}
mkdir -p "$WORK/adapters" "$WORK/fsb" "$WORK/sbxhome"
cat >"$WORK/adapters/fakesbx.yaml" <<YAML
id: fakesbx
name: Fake Sandbox (e2e)
tier: T3
verification: live-verified
verified_note: "scripts/fake-sandbox.py, driven end to end by scripts/e2e.sh"
docs: http://127.0.0.1:$FSB_PORT/v1
initials: FS
color: "#334155"
base_url: http://127.0.0.1:$FSB_PORT/v1
auth: {Authorization: "Bearer {{cred.token}}"}
credentials: [{name: token, label: API key, secret: true}]
env_vars: [FAKESBX_KEY]
capabilities: {background: false, pause: false, fork: false, ports: false, files: false, boot_ms: 50, isolation: microvm}
pricing: {cpu_second: 0.000005, gb_second: 0.000001, source: "http://127.0.0.1:$FSB_PORT/pricing", confidence: docs}
sizes: [{sku: small, cpu: 2, ram_mb: 2048, disk_gb: 20}]
regions: [{id: local, name: Local}]
workdir: $WORK/sbxhome
states: {running: running, deleted: deleted}
validate: {method: GET, path: /sandboxes}
create: {method: POST, path: /sandboxes, body: {cpu: "{{int:spec.cpu}}"}, capture: {id: \$.id, state: \$.status}}
get: {method: GET, path: "/sandboxes/{{p:id}}", capture: {state: \$.status}}
delete: {method: DELETE, path: "/sandboxes/{{p:id}}"}
exec:
  - method: POST
    path: "/sandboxes/{{p:id}}/exec"
    body: {command: "{{cmd}}", timeout: "{{int:timeout_s}}"}
    capture: {stdout: \$.stdout, stderr: \$.stderr, exit_code: \$.exit_code}
YAML
python3 "$ROOT/scripts/fake-sandbox.py" "$FSB_PORT" "$WORK/fsb" >"$WORK/fsb.log" 2>&1 & FSB_PID=$!

echo "== start core + boxd (workdir $WORK)"
BOXA_DATA_DIR=$WORK/data BOXA_ADMIN_KEY=$ADMIN BOXA_BOOTSTRAP_TOKEN=boot-e2e BOXA_ADDR=127.0.0.1:$PORT \
BOXA_SANDBOX_ADAPTERS_DIR=$WORK/adapters BOXA_AUTO_SANDBOX=1 BOXA_PRICE_FEED=off BOXA_LEDGER_INTERVAL=1s BOXA_LOCAL_PRICE_MONTH=73 BOXA_WARM_INTERVAL=1s BOXA_CLIENT_LOCATION=VN \
  "$BIN/boxa-core" >"$WORK/core.log" 2>&1 & CORE_PID=$!
for i in $(seq 50); do curl -sf "$BOXA_URL/v1/health" >/dev/null && break; sleep 0.1; done
curl -sf "$BOXA_URL/v1/health" | jq -e '.ok == true' >/dev/null && pass "core healthy: $(curl -s $BOXA_URL/v1/health | jq -c .components)" || fail health
BOXD_LOCAL_CREATE_DELAY_MS=${COLD_MS:-1000} BOXD_CORE_URL=$BOXA_URL BOXD_TOKEN=boot-e2e BOXD_DRIVER=local BOXD_STATE_DIR=$WORK/boxd BOXD_IP=127.0.0.1 \
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
[ "$N" = 22 ] && pass "MCP HTTP tools/list → 22 tools" || fail "mcp http"
R=$(curl -s -X POST "$BOXA_URL/mcp" -H "Authorization: Bearer $AKEY" -d "{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/call\",\"params\":{\"name\":\"box_exec\",\"arguments\":{\"box_id\":\"$BOX\",\"command\":\"echo via-mcp\"}}}")
echo "$R" | jq -e '.result.isError==false and .result.structuredContent.stdout_tail=="via-mcp"' >/dev/null && pass "MCP tools/call box_exec" || fail "mcp call"
R=$(printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}' '{"jsonrpc":"2.0","method":"notifications/initialized"}' '{"jsonrpc":"2.0","id":2,"method":"tools/list"}' | "$BIN/box" mcp)
[ "$(echo "$R" | jq -s '.[] | select(.id==2) | .result.tools | length')" = 22 ] && pass "MCP stdio (box mcp) tools/list → 22 tools" || fail "mcp stdio"

echo "== v1.1 discovery (no auth)"
curl -sf "$BOXA_URL/v1/openapi.json" | jq -e '.openapi=="3.1.0" and (.paths|has("/v1/quote")) and (.paths|has("/v1/boxes/{id}/fork"))' >/dev/null \
  && pass "openapi.json 3.1 ($(curl -s $BOXA_URL/v1/openapi.json | jq '[.paths[]|keys[]]|length') operations)" || fail openapi
LT=$(curl -sf "$BOXA_URL/llms.txt"); [[ $LT == "# Boxa"* ]] && pass "llms.txt" || fail llms
LF=$(curl -sf -H 'Accept-Language: vi' "$BOXA_URL/llms-full.txt"); grep -q 'Bắt đầu nhanh' <<<"$LF" && grep -q '/a2a' <<<"$LF" && pass "llms-full.txt localized (vi)" || fail llms-full
curl -sf "$BOXA_URL/.well-known/mcp.json" | jq -e '.transport.url|endswith("/mcp")' >/dev/null && pass ".well-known/mcp.json" || fail mcpcard
curl -sf "$BOXA_URL/.well-known/agent.json" | jq -e '.protocolVersion=="0.3.0" and (.skills|length)==23' >/dev/null && pass ".well-known/agent.json (A2A card, exec + 22 tool skills)" || fail agentcard
for F in openai anthropic gemini mcp; do
  N=$(curl -sf "$BOXA_URL/v1/tools?format=$F" | jq 'if type=="array" then (if .[0].functionDeclarations then .[0].functionDeclarations|length else length end) else .tools|length end')
  [ "$N" = 22 ] && pass "tools?format=$F → $N" || fail "tools $F ($N)"
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

echo "== v1.3 providers / selection / speed / agents / mesh"
curl -sf "$BOXA_URL/v1/profiles" | jq -e '(.items|length)==7 and (.kinds|length)==22' >/dev/null && pass "GET /profiles: 7 workload profiles, 22 agent kinds" || fail profiles
R=$(KEY=$AKEY api POST /recommend -d '{"profile":"coding-agent"}')
echo "    recommend: $(echo "$R" | jq -c '{pick:(.pick|{kind,provider,score,eta_seconds,cost_per_hour}),rejected:(.rejected|length)}')"
echo "$R" | jq -e '.pick.kind=="pool" and (.pick.reasons|length)>0 and (.pick.breakdown|length)==6' >/dev/null && pass "POST /recommend ranks + explains (pool host for coding-agent)" || fail recommend
api GET /providers/health | jq -e '(.items|length)>=6 and (.items[0].stats.breaker.state=="closed")' >/dev/null && pass "GET /providers/health (breaker closed, stats)" || fail health13
api GET /catalog | jq -e 'has("items") and has("sources") and .stale_after_s>0' >/dev/null && pass "GET /catalog (merged feed + API, staleness)" || fail catalog
api PUT /providers/hetzner/prefs -d '{"priority":2,"max_hourly_usd":0.05}' | jq -e '.priority==2 and .enabled' >/dev/null && pass "provider prefs (priority, caps)" || fail prefs
api POST /providers/hetzner/validate -d '{"token":"x"}' >/dev/null 2>&1; pass "validate endpoint reachable (no network call asserted)"
api PUT /warm-pool -d '{"enabled":true,"pools":[{"template":"base","spec":{"cpu":1,"ram_mb":512,"disk_gb":2},"min":1,"max":2,"ttl_minutes":30}]}' >/dev/null
for i in $(seq 60); do [ "$(api GET /warm-pool | jq '.ready.base // 0')" -ge 1 ] && break; sleep 0.25; done
[ "$(api GET /warm-pool | jq '.ready.base // 0')" -ge 1 ] && pass "warm pool filled (base ×1)" || fail warmfill
C1=$(KEY=$AKEY api POST /boxes -d '{"template":"node","slug":"e2e-cold","spec":{"cpu":1,"ram_mb":256,"disk_gb":1}}')
W1=$(KEY=$AKEY api POST /boxes -d '{"template":"base","slug":"e2e-warm","spec":{"cpu":1,"ram_mb":256,"disk_gb":1}}')
CS=$(echo "$C1" | jq .estimate.actual_seconds); WS=$(echo "$W1" | jq .estimate.actual_seconds)
echo "    BENCH create→running: cold ${CS}s (simulated launch ${COLD_MS:-1000} ms) vs warm-pool ${WS}s"
echo "$W1" | jq -e '.estimate.warm_hit and .estimate.kind=="warm"' >/dev/null && python3 -c "import sys; sys.exit(0 if $WS*3 < $CS else 1)" && pass "warm-pool hit is >3x faster than cold" || fail warm
WB=$(echo "$W1" | jq -r .id)
KEY=$AKEY api POST /boxes/$WB/exec -d '{"command":"mkdir -p d && cd d && export E2E=1","session":true}' >/dev/null
KEY=$AKEY api POST /boxes/$WB/exec -d '{"command":"pwd; echo E2E=$E2E","session":true}' | jq -e '(.stdout_tail|test("/d")) and (.stdout_tail|test("E2E=1"))' >/dev/null && pass "exec session keeps cwd + exports" || fail session
CL=$(KEY=$AKEY api POST /boxes/$WB/clone -d '{"slug":"e2e-clone"}')
echo "$CL" | jq -e '.status=="running" and .estimate.kind=="clone"' >/dev/null && pass "copy-on-write clone in $(echo "$CL" | jq .estimate.actual_seconds)s" || fail clone
KEY=$AKEY api GET /boxes/$WB/observe | jq -e 'has("diff") and (.summary|length)>0' >/dev/null && pass "observe returns structured diff + summary" || fail observe
E1=$(KEY=$AKEY api POST /boxes/ensure -d '{"name":"e2e-ensure","template":"base","spec":{"cpu":0.5,"ram_mb":128,"disk_gb":1}}')
E2=$(KEY=$AKEY api POST /boxes/ensure -d '{"name":"e2e-ensure"}')
[ "$(echo "$E1" | jq -r .box.id)" = "$(echo "$E2" | jq -r .box.id)" ] && echo "$E2" | jq -e '.action=="reused" and .resume!=null' >/dev/null && pass "box_ensure is idempotent (created → reused with resume)" || fail ensure
KEY=$AKEY api GET /boxes/nope | jq -e '.error.code=="NOT_FOUND" and (.error.next|length)>0' >/dev/null && pass "errors carry next actions" || fail errnext
api GET /mesh | jq -e '.enabled==false and (.hint|length)>0' >/dev/null && pass "mesh disabled → graceful no-op with hint" || fail mesh
api GET /speed | jq -e '.placement=="binpack" and .warm.hits>=1' >/dev/null && pass "GET /speed (bin-packing, warm hits $(api GET /speed | jq .warm.hits))" || fail speed
for b in $(echo "$C1 $W1 $CL" | jq -r .id) $(echo "$E1" | jq -r .box.id); do "$BIN/box" down "$b" >/dev/null; done
api PUT /warm-pool -d '{"enabled":false,"pools":[]}' >/dev/null

echo "== v1.4 sandbox API providers (T2/T3) / YAML adapters / recommend"
SP=$(api GET /sandbox/providers)
echo "    sandbox providers: $(echo "$SP" | jq -c '[.items[]|"\(.id):\(.tier)/\(.mode)/\(.verification)"]')"
echo "$SP" | jq -e '[.items[].id]|contains(["e2b","novita","daytona","prized","boxdsh","runloop","blaxel","vercel","boat","agent37","upstash-box","fakesbx"])' >/dev/null && pass "GET /sandbox/providers: 11 built-in adapters + custom YAML from BOXA_SANDBOX_ADAPTERS_DIR" || fail sbxlist
echo "$SP" | jq -e '(.items[]|select(.id=="prized")|.verification=="live-verified") and (.items[]|select(.id=="upstash-box")|.tier=="T3" and .mode=="shim") and all(.items[]; .docs!="" and .initials!="" and .from_usd_h>0 and (.capabilities|has("boot_ms") and has("fork") and has("max_lifetime_s")))' >/dev/null && pass "adapter metadata: verification level, docs, initials, capabilities, per-second price" || fail sbxmeta
api GET /providers | jq -e '([.items[]|select(.group=="sandbox")]|length)>=12 and (.items[]|select(.id=="runloop")|.adapter=="yaml") and (.items[]|select(.id=="hetzner")|.group=="vps")' >/dev/null && pass "GET /providers groups vps / sandbox (native | yaml adapters)" || fail sbxgroups
CT=$(api GET "/catalog?provider=e2b")
echo "$CT" | jq -e '(.items|length)>0 and all(.items[]; .tier=="T2" and .group=="sandbox" and .min_billing_minutes==0)' >/dev/null && pass "catalog: E2B sizes as T2 per-second offers before connecting" || fail sbxcatalog
L1=$(api POST /sandbox/adapters/lint -d '{"yaml":"id: x\nname: X\n"}')
echo "$L1" | jq -e '.ok==false and (.error|test("missing"))' >/dev/null && pass "adapter lint reports missing calls: $(echo "$L1" | jq -r .error)" || fail lint1
L2=$(jq -Rs '{yaml:.}' "$WORK/adapters/fakesbx.yaml" | api POST /sandbox/adapters/lint -d @-)
echo "$L2" | jq -e '.ok and .info.id=="fakesbx" and .info.adapter=="yaml" and .info.tier=="T3"' >/dev/null && pass "adapter lint accepts a valid YAML adapter (info echoed)" || fail lint2
KEY=$AKEY api POST /sandbox/adapters/lint -d '{"yaml":"id: x"}' | jq -e '.error.code=="UNAUTHORIZED" or .error.code=="POLICY_DENIED"' >/dev/null && pass "adapter lint needs an admin key" || fail lintscope
[ "$(api PUT /providers/fakesbx/token -d '{"token":"wrong"}' | jq -r .error.code)" = UNAUTHORIZED ] && pass "sandbox token validated against the provider (wrong key refused)" || fail sbxbadtoken
api PUT /providers/fakesbx/token -d '{"token":"fsb_e2e_0123456789"}' | jq -e '.connected and .group=="sandbox" and .adapter=="yaml"' >/dev/null && pass "fakesbx connected (YAML adapter, validate call)" || fail sbxtoken
api PUT /providers/fakesbx/prefs -d '{"sandbox_mode":"shim"}' | jq -e '.sandbox_mode=="shim"' >/dev/null && pass "provider prefs sandbox_mode=shim" || fail sbxmode
SBXREQ='"requirements":{"isolation":"sandbox","untrusted":true,"cpu":1,"ram_mb":1024,"lifetime":"ephemeral","duration_hours":0.5}'
R=$(KEY=$AKEY api POST /recommend -d "{\"template\":\"base\",$SBXREQ}")
echo "    recommend(untrusted, sandbox): pick=$(echo "$R" | jq -c '.pick|{provider,tier,kind,requires_approval,strong_isolation,cost_per_hour}') rejected=$(echo "$R" | jq -c '[.rejected[]|select(.kind=="sandbox")|.provider]')"
echo "$R" | jq -e '.pick.provider=="fakesbx" and .pick.tier=="T3" and .pick.requires_approval==false and .pick.min_billing_h==0' >/dev/null && pass "recommend: connected sandbox is the pick for untrusted ephemeral work" || fail sbxrec
RA=$(api POST /recommend -d "{\"template\":\"base\",$SBXREQ}")  # admin view also ranks providers that are not connected yet
echo "$RA" | jq -e 'any(.rejected[]; .provider=="daytona" and (.reason|test("shared kernel"))) and any(.rejected[]; .provider=="upstash-box") and all(.options[]; .strong_isolation)' >/dev/null && pass "recommend: shared-kernel sandboxes (Daytona, Upstash Box) rejected for untrusted code, even with isolation=sandbox" || fail sbxrej
R2=$(api POST /recommend -d '{"template":"base","requirements":{"isolation":"sandbox","fork":true,"cpu":1,"ram_mb":1024}}')
echo "$R2" | jq -e '(.options|length)>0 and all(.options[]; .fork) and any(.rejected[]; .reason|test("fork"))' >/dev/null && pass "recommend fork=true keeps only fork-capable options" || fail sbxfork
SB=$(KEY=$AKEY api POST /boxes -d "{\"template\":\"base\",\"slug\":\"e2e-sbx\",$SBXREQ}")
SBID=$(echo "$SB" | jq -r .id)
for i in $(seq 40); do [ "$(KEY=$AKEY api GET /boxes/$SBID | jq -r .status)" = running ] && break; sleep 0.25; done
echo "    sandbox box: $(KEY=$AKEY api GET /boxes/$SBID | jq -c '{id,status,provider,host_id,est:.estimate.kind}')"
SHID=$(echo "$SB" | jq -r .host_id)
SH=$(api GET /hosts | jq -c --arg h "$SHID" '[.items[]|select(.id==$h)][0]|{billing,driver,provider}')
[ "$SH" = '{"billing":"sandbox","driver":"shim","provider":"fakesbx"}' ] && pass "box placed on a single-box sandbox host (T3 shim): $SH" || fail "sbxhost $SH"
J=$(KEY=$AKEY api POST /boxes/$SBID/exec -d '{"command":"echo sbx-ok > f.txt && cat f.txt && pwd"}')
echo "$J" | jq -e '.exit_code==0 and (.stdout_tail|test("sbx-ok")) and (.stdout_tail|test("sbxhome/work"))' >/dev/null && pass "exec through the YAML adapter + T3 shim (/work mapped into the sandbox)" || { echo "$J"; fail sbxexec; }
SN=$(KEY=$AKEY api POST /boxes/$SBID/snapshot -d '{}')
echo "$SN" | jq -e '.size_mb>0 and (.location|startswith("local://"))' >/dev/null && pass "T3 snapshot: tarball pulled to core ($(echo "$SN" | jq -c '{size_mb,location}'))" || { echo "$SN"; fail sbxsnap; }
KEY=$AKEY api POST /boxes/$SBID/exec -d '{"command":"echo broken > f.txt"}' >/dev/null
KEY=$AKEY api POST /boxes/$SBID/restore -d "{\"snapshot_id\":\"$(echo "$SN" | jq -r .id)\"}" >/dev/null
[ "$("$BIN/box" read "$SBID" f.txt)" = sbx-ok ] && pass "T3 restore puts /work back (f.txt = sbx-ok)" || fail sbxrestore
P=$("$BIN/box" url "$SBID" 8000); [[ $P == 8000*none* ]] && pass "box url on a provider without previews says so: $(echo $P | cut -c1-60)…" || fail "sbxurl $P"
"$BIN/box" down "$SBID" >/dev/null
for i in $(seq 40); do grep -q '^DELETE /v1/sandboxes/' "$WORK/fsb/calls.log" && break; sleep 0.25; done
grep -q '^DELETE /v1/sandboxes/' "$WORK/fsb/calls.log" && pass "box down → sandbox deleted at the provider (per-second billing stops)" || fail sbxdelete
echo "    fake provider calls: $(awk '{print $1" "$2}' "$WORK/fsb/calls.log" | sed 's/sbx-[0-9a-f]*/:id/' | sort | uniq -c | awk '{print $2" "$3"×"$1}' | paste -sd, -)"

echo "== ledger / overview"
sleep 2.5
C=$(KEY=$ADMIN api GET /costs); echo "    summary: $(echo "$C" | jq -c .summary) entries=$(echo "$C" | jq '.items|length')"
echo "$C" | jq -e '.summary.total > 0 and (.items|length) > 0 and .summary.pool_usd > 0' >/dev/null && pass "per-tick ledger entries" || fail ledger
O=$(KEY=$ADMIN api GET /overview); echo "    overview: $(echo "$O" | jq -c '{spent_today,budget_day,boxes_running,boxes_sleeping,saved_vs_247_usd,top_tasks}')"
echo "$O" | jq -e '.spent_today > 0 and .boxes_running >= 1 and (.cost_7d|length)==7' >/dev/null && pass "overview aggregates" || fail overview
CSV=$(KEY=$ADMIN api GET /costs.csv); [[ $CSV == "id,ts,agent_id"* ]] && pass "costs.csv" || fail csv

echo "== down"
[[ $("$BIN/box" down "$BOX") == *terminated* ]] && pass "down" || fail down
EV=$(KEY=$AKEY api GET /boxes/$BOX/events | jq -r '.items | reverse | map(.type) | join(" → ")')
echo "    timeline: $EV"
[[ $EV == *box.terminated* ]] && pass "event timeline recorded" || fail events

echo "== ALL PASSED"
