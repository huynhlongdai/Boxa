#!/usr/bin/env bash
# Smoke test: chạy bản build với BOXAZ_MOCK=1 rồi curl mọi route (ở cả 4 ngôn ngữ qua cookie boxaz_locale) + API chính,
# endpoint v1.1 (agent-kinds, quote, exec-batch, yaml, restore/fork, discovery) và lỗi được dịch theo ngôn ngữ.
# Dùng: pnpm build && pnpm smoke   (PORT mặc định 3199)
set -u
cd "$(dirname "$0")/.."
PORT="${PORT:-3199}"
BASE="http://localhost:$PORT"
LOCALES="vi en zh ja"

if [ -f .next/standalone/server.js ]; then
  mkdir -p .next/standalone/.next
  cp -r .next/static .next/standalone/.next/ 2>/dev/null || true
  cp -r public .next/standalone/ 2>/dev/null || true
  BOXAZ_MOCK=1 PORT=$PORT HOSTNAME=127.0.0.1 node .next/standalone/server.js > smoke-server.log 2>&1 &
else
  BOXAZ_MOCK=1 npx next start -p "$PORT" > smoke-server.log 2>&1 &
fi
PID=$!
trap 'kill $PID 2>/dev/null' EXIT

for i in $(seq 1 60); do
  curl -s -o /dev/null "$BASE/login" && break
  sleep 0.5
done

fail=0
pass=0
ok() { echo "  ok   $*"; pass=$((pass + 1)); }
bad() { echo "  FAIL $*"; fail=1; }

check() {
  local path="$1" want="${2:-200}" code
  code=$(curl -s -o /dev/null -w '%{http_code}' "$BASE$path")
  if [ "$code" = "$want" ]; then ok "$code $path"; else bad "$code (muốn $want) $path"; fi
}

# check_page <locale> <path>: 200 + <html lang="<locale>"> + có nhãn điều hướng đúng ngôn ngữ
declare -A NAVWORD=([vi]="Tổng quan" [en]="Overview" [zh]="总览" [ja]="概要")
check_page() {
  local l="$1" path="$2" body code
  body=$(curl -s -w '\n__CODE__%{http_code}' -b "boxaz_locale=$l" "$BASE$path")
  code="${body##*__CODE__}"
  if [ "$code" != "200" ]; then bad "[$l] $code $path"; return; fi
  if ! grep -q "<html lang=\"$l\"" <<<"$body"; then bad "[$l] thiếu <html lang=\"$l\"> $path"; return; fi
  if [[ "$path" != /login && "$path" != /onboarding ]] && ! grep -qF "${NAVWORD[$l]}" <<<"$body"; then bad "[$l] không thấy nhãn '${NAVWORD[$l]}' $path"; return; fi
  ok "[$l] 200 $path"
}

ROUTES="/ /boxes /boxes/box_7f3a91 /boxes/box_7f3a91?tab=logs /boxes/box_a9d054?tab=ports /boxes/box_5e2b70?tab=ports /recommend /infra?s=health /infra?s=catalog /infra?s=speed /infra?s=mesh /infra?s=sandbox /infra/adapters /boxes/box_5e2b70 /agents /agents/agt_kecon /costs /infra /templates /secrets /settings /admin /developers /onboarding /login"

echo "== Trang × ngôn ngữ (cookie boxaz_locale)"
for l in $LOCALES; do
  for r in $ROUTES; do check_page "$l" "$r"; done
done
check /khong-ton-tai 404

echo "== Chọn ngôn ngữ theo Accept-Language khi không có cookie"
for pair in "en-US,en;q=0.9:en" "zh-CN,zh;q=0.9:zh" "ja-JP:ja" "fr-FR,fr;q=0.9:vi" "de;q=1,ja;q=0.5:ja"; do
  al="${pair%:*}"; want="${pair##*:}"
  got=$(curl -s -H "Accept-Language: $al" "$BASE/boxes" | grep -o '<html lang="[a-z]*"' | head -1)
  [ "$got" = "<html lang=\"$want\"" ] && ok "Accept-Language '$al' → $want" || bad "Accept-Language '$al' → $got (muốn $want)"
done

echo "== API (qua proxy, mock)"
for a in health overview boxes boxes/box_7f3a91 boxes/box_7f3a91/events boxes/box_7f3a91/snapshots "boxes/box_7f3a91/files?path=/work" boxes/box_7f3a91/observe \
  agents agents/agt_kecon "agents/agt_kecon/snippet?kind=n8n" keys costs costs.csv costs/suggestions providers hosts offers templates secrets approvals settings storage notify \
  admin/system admin/jobs admin/audit admin/quotas admin/users logs/log_0002 jobs/job_0001 api/session; do
  case "$a" in api/*) check "/$a" ;; *) check "/api/boxaz/$a" ;; esac
done
check /api/boxaz/boxes/khong-co 404

echo "== v1.1: agent kinds, snippet theo transport, yaml, job wait"
n=$(curl -s "$BASE/api/boxaz/agent-kinds" | node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{const j=JSON.parse(s);const c=new Set(j.items.map(k=>k.category));console.log(j.items.length+" "+[...c].sort().join(","))})')
[ "$n" = "22 coding,custom,framework,platform,workflow" ] && ok "agent-kinds: $n" || bad "agent-kinds: $n"
for tr in mcp-http mcp-stdio rest sdk-python sdk-ts cli function-calling; do
  got=$(curl -s "$BASE/api/boxaz/agents/agt_kecon/snippet?kind=cursor&lang=$tr" | node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{const j=JSON.parse(s);console.log(j.transport+"|"+(j.snippet.length>20))})')
  [ "$got" = "$tr|true" ] && ok "snippet cursor/$tr" || bad "snippet cursor/$tr → $got"
done
curl -s "$BASE/api/boxaz/boxes/box_7f3a91/yaml" | grep -q '"yaml":"' && ok "GET boxes/:id/yaml" || bad "boxes/:id/yaml"
check "/api/boxaz/jobs/job_0001?wait=1"

echo "== v1.1: quote, exec-batch (tokens_saved + log_id + next), restore, fork"
q=$(curl -s -X POST "$BASE/api/boxaz/quote" -H 'content-type: application/json' -b boxaz_locale=en \
  -d '{"agent_id":"agt_shopee","template":"python","lifecycle":"task","spec":{"cpu":2,"ram_mb":2048,"disk_gb":20}}')
echo "$q" | node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{const j=JSON.parse(s);process.exit(j.provider&&j.cost_per_hour>=0&&j.placement_reason&&Array.isArray(j.alternatives)?0:1)})' \
  && ok "POST /quote → $(echo "$q" | head -c 90)…" || bad "quote: $q"
b=$(curl -s -X POST "$BASE/api/boxaz/boxes/box_7f3a91/exec-batch" -H 'content-type: application/json' -d '{"commands":["echo hello world","python -m pytest -q tests"],"stop_on_error":true}')
echo "$b" | node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{const j=JSON.parse(s);process.exit(j.results?.length===2&&j.results.every(r=>r.log_id&&typeof r.tokens_saved==="number")&&j.next?.length?0:1)})' \
  && ok "POST exec-batch (2 kết quả, log_id, tokens_saved, next)" || bad "exec-batch: $(echo "$b" | head -c 200)"
ev=$(curl -s "$BASE/api/boxaz/boxes/box_7f3a91/events" | node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{const j=JSON.parse(s);const e=j.items.find(x=>x.type==="box.exec"&&x.data);console.log(e&&e.data.log_id&&e.tokens_saved!==undefined?"ok":"no")})')
[ "$ev" = "ok" ] && ok "event box.exec có data.tokens_saved + log_id" || bad "event box.exec thiếu data"
code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/api/boxaz/boxes/box_7f3a91/exec-batch" -H 'content-type: application/json' -d '{"commands":[]}')
[ "$code" = "400" ] && ok "400 exec-batch rỗng" || bad "exec-batch rỗng $code"
snap=$(curl -s -X POST "$BASE/api/boxaz/boxes/box_7f3a91/snapshot" | node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>console.log(JSON.parse(s).id))')
fk=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/api/boxaz/boxes/box_7f3a91/fork" -H 'content-type: application/json' -d "{\"snapshot_id\":\"$snap\"}")
[ "$fk" = "201" ] && ok "201 fork từ $snap" || bad "fork $fk"
rs=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/api/boxaz/boxes/box_7f3a91/restore" -H 'content-type: application/json' -d "{\"snapshot_id\":\"$snap\"}")
[ "$rs" = "200" ] && ok "200 restore từ $snap" || bad "restore $rs"

echo "== v1.1: discovery (không cần key)"
check /api/boxaz/openapi.json
ov=$(curl -s "$BASE/api/boxaz/openapi.json" | node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{const j=JSON.parse(s);console.log(j.openapi+" "+Object.keys(j.paths).length)})')
[[ "$ov" == 3.1.0* ]] && ok "openapi $ov paths" || bad "openapi: $ov"
for f in openai anthropic gemini mcp; do
  t=$(curl -s "$BASE/api/boxaz/tools?format=$f" | node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{const j=JSON.parse(s);const n=j.format==="gemini"?j.tools[0].functionDeclarations.length:j.tools.length;console.log(j.format+":"+n)})')
  [[ "$t" == "$f:"* && "${t#*:}" -ge 12 ]] && ok "tools?format=$f → ${t#*:} tool" || bad "tools $f → $t"
done
for p in _root/llms.txt _root/llms-full.txt _root/.well-known/mcp.json _root/.well-known/agent.json; do check "/api/boxaz/$p"; done
curl -s "$BASE/api/boxaz/_root/llms.txt" | grep -q "^# Boxaz" && ok "llms.txt có tiêu đề" || bad "llms.txt"

echo "== v1.2: providers (chuỗi dự phòng), egress, lưu trữ snapshot, thông báo, A2A card"
pc=$(curl -s "$BASE/api/boxaz/providers" | node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{const j=JSON.parse(s);console.log(j.items.filter(p=>p.chain_rank).sort((a,b)=>a.chain_rank-b.chain_rank).map(p=>p.id).join(">"))})')
[[ "$pc" == "hetzner>vultr>digitalocean>contabo>netcup>"* ]] && ok "chuỗi provider $pc" || bad "chuỗi provider: $pc"
n=$(curl -s "$BASE/api/boxaz/providers/contabo/regions" | node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>console.log(JSON.parse(s).items.length))')
[ "$n" = "9" ] && ok "providers/contabo/regions → 9" || bad "regions: $n"
eg=$(curl -s -X PUT "$BASE/api/boxaz/boxes/box_7f3a91/egress" -H 'content-type: application/json' -d '{"allow":["pypi.org","*.github.com"],"deny_by_default":true}' | node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{const e=JSON.parse(s).egress;console.log(e&&e.source+"|"+e.allow.length+"|"+e.mode)})')
[[ "$eg" == box\|2\|* ]] && ok "PUT boxes/:id/egress → $eg" || bad "egress: $eg"
code=$(curl -s -o /dev/null -w '%{http_code}' -X PUT "$BASE/api/boxaz/boxes/box_7f3a91/egress" -H 'content-type: application/json' -d '{"allow":["không hợp lệ"]}')
[ "$code" = "400" ] && ok "400 egress sai định dạng" || bad "egress sai $code"
st=$(curl -s "$BASE/api/boxaz/storage" | node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{const j=JSON.parse(s);console.log(j.provider+"|"+j.configured+"|"+("secret_access_key" in j))})')
[ "$st" = "r2|true|false" ] && ok "GET /storage (secret không lộ) $st" || bad "storage: $st"
curl -s -X POST "$BASE/api/boxaz/storage/test" | grep -q '"ok":true' && ok "POST /storage/test" || bad "storage/test"
nv=$(curl -s "$BASE/api/boxaz/notify" | node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{const j=JSON.parse(s);console.log(j.events_available.length+"|"+j.telegram_configured+"|"+("telegram_bot_token" in j))})')
[ "$nv" = "6|true|false" ] && ok "GET /notify $nv" || bad "notify: $nv"
tr=$(curl -s -X POST "$BASE/api/boxaz/notify/test" -H 'content-type: application/json' -d '{"channel":"all"}' | node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{const j=JSON.parse(s);console.log(j.results.map(r=>r.channel+":"+r.ok).join(","))})')
[ "$tr" = "telegram:true,email:false" ] && ok "POST /notify/test → $tr" || bad "notify/test: $tr"
ac=$(curl -s "$BASE/api/boxaz/_root/.well-known/agent.json" | node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{const j=JSON.parse(s);console.log(j.url.endsWith("/a2a")+"|"+j.capabilities.streaming+"|"+j.preferredTransport)})')
[ "$ac" = "true|true|JSONRPC" ] && ok "A2A agent card $ac" || bad "agent card: $ac"

echo "== v1.3: provider manager, recommend, right-sizing, warm pool, speed, clone/ensure/resume, mesh, error.next"
# jq <method> <path> <body|-> <node-expr trên biến j, in ra chuỗi>
jx() {
  local m="$1" p="$2" b="$3" e="$4"
  if [ "$b" = "-" ]; then curl -s -X "$m" "$BASE/api/boxaz/$p"; else curl -s -X "$m" "$BASE/api/boxaz/$p" -H 'content-type: application/json' -d "$b"; fi \
    | node -e "let s='';process.stdin.on('data',d=>s+=d).on('end',()=>{let j;try{j=JSON.parse(s)}catch{console.log('NOJSON '+s.slice(0,80));return};try{console.log($e)}catch(x){console.log('ERR '+x.message)}})"
}
expect() { local name="$1" got="$2" want="$3"; if [[ "$got" == $want ]]; then ok "$name → $got"; else bad "$name → $got (muốn $want)"; fi; }
expect "GET /providers/health" "$(jx GET providers/health - 'j.items.length+"|"+j.items.find(x=>x.provider.id==="contabo").stats.breaker.state+"|"+Object.keys(j.items.find(x=>x.provider.id==="hetzner").score.breakdown).length')" "17|open|6"
expect "GET /providers/hetzner/health" "$(jx GET providers/hetzner/health - 'j.provider.id+"|"+(j.recent.length>0)+"|"+(j.stats.api_latency_p95_ms>=j.stats.api_latency_p50_ms)')" "hetzner|true|true"
expect "POST validate (token sai)" "$(jx POST providers/digitalocean/validate '{"token":"sai"}' 'j.ok+"|"+j.error_class')" "false|auth"
expect "POST validate (token đúng)" "$(jx POST providers/digitalocean/validate '{"token":"dop_v1_0123456789abcdef0123"}' 'j.ok+"|"+(j.regions.length>0)+"|"+(j.sizes.length>0)')" "true|true|true"
expect "GET /profiles" "$(jx GET profiles - 'j.items.length+"|"+j.kinds.length+"|"+j.optimize.length')" "7|22|5"
expect "POST /recommend coding-agent" "$(jx POST recommend '{"profile":"coding-agent","agent_id":"agt_hark"}' 'j.profile.id+"|"+(j.options.length>0)+"|"+j.options.every(o=>Object.keys(o.breakdown).length===6&&o.reasons.length>0&&o.eta_seconds>0)+"|"+(j.pick?j.pick.rank:0)+"|"+Array.isArray(j.rejected)')" "coding-agent|true|true|1|true"
expect "POST /recommend untrusted → không container" "$(jx POST recommend '{"profile":"sandbox-untrusted"}' 'j.options.every(o=>o.isolation!=="container")+"|"+j.rejected.some(r=>/kernel|VM/i.test(r.reason))')" "true|true"
expect "POST /recommend có lý do loại contabo" "$(jx POST recommend '{"profile":"build"}' 'j.rejected.some(r=>r.provider==="contabo")')" "true"
expect "POST /recommend profile lạ → 400 + next" "$(jx POST recommend '{"requirements":{"profile":"khong-co"}}' 'j.error.code+"|"+(j.error.next.length>0)')" "INVALID|true"
expect "POST /providers/contabo/reset" "$(jx POST providers/contabo/reset - 'j.stats.breaker.state')" "closed"
expect "POST /providers/vultr/check" "$(jx POST providers/vultr/check - 'j.provider.id+"|"+j.stats.breaker.state')" "vultr|closed"
expect "PUT /providers/hetzner/prefs" "$(jx PUT providers/hetzner/prefs '{"max_servers":6}' 'j.max_servers+"|"+j.priority+"|"+j.regions.length')" "6|1|3"
expect "PUT prefs âm → 400" "$(jx PUT providers/hetzner/prefs '{"max_hourly_usd":-1}' 'j.error.code')" "INVALID"
expect "GET /catalog" "$(jx GET catalog - '(j.items.length>0)+"|"+j.sources.some(s=>s.stale)+"|"+j.items.some(i=>i.price_source.endsWith("-api"))+"|"+(j.total===j.items.length)')" "true|true|true|true"
expect "POST /boxes với profile → estimate" "$(jx POST boxes '{"agent_id":"agt_hark","profile":"browser-automation"}' 'j.profile+"|"+(j.estimate?j.estimate.kind:"none")+"|"+(j.estimate&&j.estimate.eta_seconds>0)')" "browser-automation|*|true"
expect "POST /boxes thường cũng có estimate" "$(jx POST boxes '{"agent_id":"agt_kecon","template":"python","spec":{"cpu":1,"ram_mb":1024,"disk_gb":10}}' '(j.estimate&&j.estimate.cost_per_hour>=0)+"|"+j.estimate.kind')" "true|pool"
expect "GET /rightsize" "$(jx GET rightsize - '(j.items.length>0)+"|"+j.items.some(r=>r.suggestion==="downsize"&&r.savings_per_month>0)')" "true|true"
expect "GET /boxes/:id/rightsize" "$(jx GET boxes/box_a9d054/rightsize - 'j.suggestion+"|"+j.suggested_spec.cpu')" "downsize|2"
expect "GET /warm-pool" "$(jx GET warm-pool - 'j.enabled+"|"+j.pools.length+"|"+(j.entries.length>0)')" "true|2|true"
expect "PUT /warm-pool" "$(jx PUT warm-pool '{"enabled":true,"pools":[{"template":"python","spec":{"cpu":2,"ram_mb":4096,"disk_gb":20},"min":2,"max":4,"ttl_minutes":45}]}' 'j.pools.length+"|"+j.pools[0].min+"|"+j.pools[0].ttl_minutes')" "1|2|45"
expect "PUT /warm-pool sai → 400" "$(jx PUT warm-pool '{"enabled":true,"pools":[{"template":"python","min":5,"max":2,"ttl_minutes":30}]}' 'j.error.code')" "INVALID"
expect "GET /speed" "$(jx GET speed - 'j.placement+"|"+(j.speedup>1)+"|"+(j.hosts.length>0)')" "binpack|true|true"
expect "POST /boxes/:id/clone" "$(jx POST boxes/box_a9d054/clone '{"slug":"kecon-story-gen-thu"}' 'j.slug+"|"+j.cloned_from+"|"+j.estimate.kind')" "kecon-story-gen-thu|box_a9d054|clone"
expect "clone box đã hỏng → 409" "$(jx POST boxes/box_b18f6e/clone '{}' 'j.error.code')" "INVALID"
expect "GET /boxes/:id/resume" "$(jx GET boxes/box_7f3a91/resume - 'j.woke+"|"+Array.isArray(j.recent_execs)+"|"+(j.next.length>0)')" "false|true|true"
expect "POST /boxes/ensure (tạo)" "$(jx POST boxes/ensure '{"name":"smoke-ensure","agent_id":"agt_hark","template":"base"}' 'j.action+"|"+j.created')" "created|true"
expect "POST /boxes/ensure (dùng lại)" "$(jx POST boxes/ensure '{"name":"smoke-ensure","agent_id":"agt_hark"}' 'j.action+"|"+j.created')" "reused|false"
expect "GET /mesh" "$(jx GET mesh - 'j.enabled+"|"+(j.nodes.length>0)+"|"+(j.acl_rules>0)')" "true|true|true"
expect "GET /mesh/config" "$(jx GET mesh/config - '/server_url/.test(j.config)+"|"+(JSON.parse(j.acl).acls.length>0)')" "true|true"
expect "GET /mesh/acl" "$(jx GET mesh/acl - 'Array.isArray(j.notes)+"|"+(j.policy.acls.length>0)')" "true|true"
expect "POST /mesh/keys" "$(jx POST mesh/keys '{"box_id":"box_9a6d13","reusable":false,"ttl_minutes":30}' 'j.key.startsWith("hskey-")+"|"+/tailscale up --login-server/.test(j.command)+"|"+j.ephemeral')" "true|true|true"
expect "error.next khi NOT_FOUND" "$(jx GET boxes/khong-co - 'j.error.code+"|"+(j.error.next.length>0)')" "NOT_FOUND|true"

echo "== v1.4: Sandbox API providers (T2/T3), catalog theo tier, recommend sandbox, lint adapter YAML"
expect "GET /sandbox/providers" "$(jx GET sandbox/providers - 'j.items.length+"|"+j.items.every(p=>p.initials&&p.docs&&p.capabilities&&p.pricing.source&&["live-verified","docs-verified","sdk-source"].includes(p.verification))+"|"+j.items.find(p=>p.id==="prized").verification+"|"+j.items.find(p=>p.id==="upstash-box").mode')" "11|true|live-verified|shim"
expect "sandbox mode: E2B ép dial, T2 auto → dial" "$(jx GET sandbox/providers - 'j.items.find(p=>p.id==="e2b").mode+"|"+j.items.find(p=>p.id==="boat").mode+"|"+j.items.filter(p=>p.connected).length')" "dial|dial|4"
expect "GET /providers có group sandbox" "$(jx GET providers - 'j.items.filter(p=>p.group==="sandbox").length+"|"+j.items.filter(p=>p.group==="vps").length+"|"+j.items.find(p=>p.id==="novita").verification')" "11|6|live-verified"
expect "GET /catalog có tier T2/T3 theo giây" "$(jx GET catalog - 'j.items.some(i=>i.tier==="T2"&&i.group==="sandbox"&&i.min_billing_minutes===0)+"|"+j.items.some(i=>i.tier==="T3")+"|"+j.items.filter(i=>i.tier==="T1").every(i=>i.group==="vps")')" "true|true|true"
expect "GET /catalog?tier=T3" "$(jx GET 'catalog?tier=T3' - 'j.items.length>0&&j.items.every(i=>i.tier==="T3")')" "true"
expect "POST /recommend untrusted+fork → sandbox T2, loại theo capability" "$(jx POST recommend '{"requirements":{"untrusted":true,"fork":true,"lifetime":"ephemeral","cpu":2,"ram_mb":2048}}' 'j.options.some(o=>o.kind==="sandbox"&&o.tier&&o.fork&&o.strong_isolation)+"|"+j.rejected.some(r=>r.provider==="daytona"&&/kernel/i.test(r.reason))+"|"+j.rejected.some(r=>r.provider==="runloop"&&/fork/i.test(r.reason))')" "true|true|true"
expect "POST /recommend persistent+public_url → loại vercel/prized" "$(jx POST recommend '{"requirements":{"lifetime":"persistent","public_url":true,"duration_hours":30}}' 'j.rejected.some(r=>r.provider==="vercel")+"|"+j.rejected.some(r=>r.provider==="prized")')" "true|true"
expect "POST /recommend isolation=sandbox" "$(jx POST recommend '{"requirements":{"isolation":"sandbox"}}' 'j.options.length>0&&j.options.every(o=>o.kind==="sandbox")')" "true"
expect "POST /boxes isolation=sandbox → host sandbox" "$(jx POST boxes '{"agent_id":"agt_hark","requirements":{"isolation":"sandbox","untrusted":true}}' 'j.estimate.kind+"|"+(j.host_id||"").startsWith("hst")')" "sandbox|true"
expect "GET /hosts có host sandbox T2 + T3" "$(jx GET hosts - 'j.items.filter(h=>h.billing==="sandbox").map(h=>h.driver).sort().slice(0,2).join(",")')" "sandbox,s*"
expect "PUT prefs sandbox_mode=shim" "$(jx PUT providers/e2b/prefs '{"sandbox_mode":"shim"}' 'j.sandbox_mode')" "shim"
expect "PUT prefs sandbox_mode sai → 400" "$(jx PUT providers/e2b/prefs '{"sandbox_mode":"turbo"}' 'j.error.code')" "INVALID"
jx PUT providers/e2b/prefs '{"sandbox_mode":"dial"}' 'j.sandbox_mode' >/dev/null
expect "POST validate sandbox thiếu trường" "$(jx POST providers/vercel/validate '{"token":"vcl_0123456789abcdef"}' 'j.ok+"|"+j.error_class')" "false|auth"
expect "POST validate sandbox nhiều trường" "$(jx POST providers/vercel/validate '{"token":"{\"token\":\"vcl_0123456789abcdef\",\"project_id\":\"prj_1\"}"}' 'j.ok+"|"+(j.sizes.length>0)+"|"+j.sizes[0].min_billing_minutes')" "true|true|0"
expect "POST /sandbox/adapters/lint thiếu trường" "$(jx POST sandbox/adapters/lint '{"yaml":"id: x\n"}' 'j.ok+"|"+/missing base_url/.test(j.error)')" "false|true"
expect "POST /sandbox/adapters/lint YAML hỏng" "$(jx POST sandbox/adapters/lint '{"yaml":"id: [x"}' 'j.ok')" "false"
expect "POST /sandbox/adapters/lint hợp lệ" "$(jx POST sandbox/adapters/lint '{"yaml":"id: acme\nname: Acme\nbase_url: https://api.acme.dev\nvalidate: {method: GET, path: /x}\ncreate: {method: POST, path: /x}\nget: {method: GET, path: /x}\ndelete: {method: DELETE, path: /x}\nexec: [{method: POST, path: /x}]\nsizes: [{sku: s, cpu: 1, ram_mb: 1024, disk_gb: 5}]\npricing: {cpu_second: 0.00001, gb_second: 0.000003}\n"}' 'j.ok+"|"+j.info.id+"|"+j.info.adapter+"|"+(j.info.from_usd_h>0)')" "true|acme|yaml|true"
expect "POST /sandbox/adapters/lint rỗng → 400" "$(jx POST sandbox/adapters/lint '{"yaml":""}' 'j.error.code')" "INVALID"

echo "== v1.7: ports + Boxaz URL, access, URL token, wake_on_request, app supervisor + log SSE, capabilities"
expect "GET /capabilities" "$(jx GET capabilities - 'j.items.length+"|"+j.items.every(c=>c.wake_via_gateway)+"|"+j.items.find(c=>c.provider==="e2b").wake_on_http_native+"|"+j.items.find(c=>c.provider==="prized").inbound_http')" "17|true|true|false"
expect "GET /boxes/:id/ports (gateway_url + access)" "$(jx GET boxes/box_a9d054/ports - 'j.ports.length+"|"+j.ports[0].port+"|"+j.ports[0].gateway_url+"|"+j.ports[0].access')" "1|3000|https://kecon-story-gen.boxaz.dev/|public"
expect "port token-only → kind gateway, không lộ URL provider" "$(jx GET boxes/box_5e2b70/ports - 'j.ports[0].kind+"|"+j.ports[0].public+"|"+(j.ports[0].url===j.ports[0].gateway_url)')" "gateway|false|true"
expect "PUT ports/:port access=token" "$(jx PUT boxes/box_a9d054/ports/3000 '{"access":"token"}' 'j.access+"|"+j.kind')" "token|gateway"
expect "PUT ports/:port access sai → 400" "$(jx PUT boxes/box_a9d054/ports/3000 '{"access":"open"}' 'j.error.code')" "INVALID"
expect "PUT ports/:port chưa mở → 404" "$(jx PUT boxes/box_a9d054/ports/4321 '{"access":"token"}' 'j.error.code')" "NOT_FOUND"
expect "PUT ports/:port access=public" "$(jx PUT boxes/box_a9d054/ports/3000 '{"access":"public"}' 'j.access')" "public"
TOKJ=$(curl -s -X POST "$BASE/api/boxaz/boxes/box_a9d054/ports/3000/tokens" -H 'content-type: application/json' -d '{"ttl_s":3600,"label":"smoke"}')
TID=$(echo "$TOKJ" | node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{const j=JSON.parse(s);console.log(j.id)})')
expect "POST tokens → 201 bxt_ + url ?token= (một lần)" "$(echo "$TOKJ" | node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{const j=JSON.parse(s);console.log(j.id.startsWith("utok_")+"|"+j.token.startsWith("bxt_")+"|"+j.url.endsWith("?token="+j.token)+"|"+j.label)})')" "true|true|true|smoke"
expect "GET tokens: port chuyển sang token, không trả giá trị" "$(jx GET boxes/box_a9d054/ports/3000/tokens - 'j.access+"|"+j.items.length+"|"+j.items.every(t=>!("token" in t)&&!("url" in t))')" "token|1|true"
expect "POST tokens ttl sai → 400" "$(jx POST boxes/box_a9d054/ports/3000/tokens '{"ttl_s":5}' 'j.error.code')" "INVALID"
expect "DELETE tokens/:id → revoked_at" "$(jx DELETE "boxes/box_a9d054/ports/3000/tokens/$TID" - '!!j.revoked_at+"|"+j.id')" "true|$TID"
jx PUT boxes/box_a9d054/ports/3000 '{"access":"public"}' 'j.access' >/dev/null
expect "GET tokens (seed: 1 hết hạn + 1 đang dùng)" "$(jx GET boxes/box_c4410f/ports/8080/tokens - 'j.items.length+"|"+j.items.filter(t=>t.expired).length')" "2|1"
expect "POST ports (mở port)" "$(jx POST boxes/box_3f0c88/ports '{"port":8501}' 'j.ports.some(p=>p.port===8501&&p.gateway_url)')" "true"
expect "PATCH box wake_on_request=false" "$(jx PATCH boxes/box_a9d054 '{"wake_on_request":false}' 'j.wake_on_request')" "false"
expect "GET box có wake_on_request + app" "$(jx GET boxes/box_a9d054 - 'j.wake_on_request+"|"+j.app.start')" "false|npm start"
jx PATCH boxes/box_a9d054 '{"wake_on_request":true}' 'j.wake_on_request' >/dev/null
expect "GET /boxes/:id/app" "$(jx GET boxes/box_a9d054/app - 'j.spec.port+"|"+j.status.state+"|"+j.status.ready+"|"+(j.status.restarts>=1)+"|"+j.status.supervisor')" "3000|ready|true|true|boxd"
expect "GET app (box không có app)" "$(jx GET boxes/box_7f3a91/app - 'j.spec+"|"+j.status.state')" "null|none"
expect "POST app/restart → restarts tăng" "$(jx POST boxes/box_a9d054/app/restart - 'j.status.restarts>=2&&j.status.ready')" "true"
expect "POST app/restart không có app → 404" "$(jx POST boxes/box_7f3a91/app/restart - 'j.error.code')" "NOT_FOUND"
expect "GET app/logs (offset)" "$(jx GET boxes/box_a9d054/app/logs - '/\[boxd\]/.test(j.data)+"|"+(j.offset>0)')" "true|true"
OFF=$(jx GET boxes/box_a9d054/app/logs - 'j.offset')
expect "GET app/logs?offset=<cuối> → rỗng" "$(jx GET "boxes/box_a9d054/app/logs?offset=$OFF" - 'JSON.stringify(j.data)+"|"+j.offset')" "\"\"|$OFF"
expect "PUT app thiếu start → 400" "$(jx PUT boxes/box_3f0c88/app '{"port":8501}' 'j.error.code')" "INVALID"
expect "PUT app → starting" "$(jx PUT boxes/box_3f0c88/app '{"start":"streamlit run app.py","port":8501}' 'j.spec.start+"|"+j.status.state+"|"+j.spec.workdir')" "streamlit run app.py|starting|/work"
sse=$(timeout 4 curl -sN "$BASE/api/boxaz/boxes/box_a9d054/app/logs?follow=1" | head -c 400)
echo "$sse" | grep -q "^event: log" && echo "$sse" | grep -q '^data: {"data":' && ok "SSE app/logs?follow=1 (event log)" || bad "SSE app logs: $(echo "$sse" | head -c 120)"
declare -A PORTSWORD=([vi]="Bắt buộc token" [en]="Require token" [zh]="需要令牌" [ja]="トークン必須")
declare -A TABWORD=([vi]='Port \u0026 app' [en]='Ports \u0026 app' [zh]="端口与应用" [ja]="ポートとアプリ")
for l in $LOCALES; do
  body=$(curl -s -b "boxaz_locale=$l" "$BASE/boxes/box_a9d054?tab=ports")
  grep -qF "${TABWORD[$l]}" <<<"$body" && ok "[$l] tab Ports & app có nhãn '${TABWORD[$l]}'" || bad "[$l] thiếu nhãn tab '${TABWORD[$l]}'"
  node -e 'const m=require("./messages/'$l'.json");process.exit(m.ports&&m.ports.requireToken==="'"${PORTSWORD[$l]}"'"&&m.ports.previewCaveat&&m.ports.app.logs&&m.boxDetail.tabs.ports?0:1)' && ok "[$l] chuỗi ports.* đủ" || bad "[$l] thiếu chuỗi ports.*"
done
grep -rqs "preview-caveat" .next/static/chunks && grep -rqs "logs-follow" .next/static/chunks && grep -rqs "issue-token-" .next/static/chunks && ok "bundle có UI ports (preview-caveat, issue-token, logs-follow)" || bad "bundle thiếu UI ports"

echo "== v1.8: gateway domain + private provider previews"
expect "GET /gateway?check=1" "$(jx GET 'gateway?check=1' - 'j.mode+"|"+/^https:\/\/<name>\./.test(j.url_pattern)+"|"+j.reserved.includes("hark")+"|"+j.providers.find(p=>p.provider==="e2b").private_preview+"|"+j.providers.find(p=>p.provider==="prized").preview_host+"|"+j.check.ok')" "host|true|true|true|false|true"
expect "GET /capabilities private_preview" "$(jx GET capabilities - 'j.items.find(c=>c.provider==="e2b").private_preview+"|"+j.items.find(c=>c.provider==="prized").private_preview')" "true|false"
expect "port e2b (token) → provider_preview private" "$(jx GET boxes/box_5e2b70/ports - 'j.ports[0].provider_preview')" "private"
expect "PUT settings gateway.domain sai → 400" "$(jx PUT settings '{"gateway":{"domain":"not a domain","provider_preview":""}}' 'j.error.code')" "INVALID"
expect "PUT settings gateway → lưu + /gateway theo" "$(jx PUT settings '{"gateway":{"domain":"Example.COM","provider_preview":"public"}}' 'j.gateway.domain+"|"+j.gateway.provider_preview')" "example.com|public"
expect "GET /gateway sau khi đổi" "$(jx GET gateway - 'j.domain+"|"+j.domain_source+"|"+j.private_previews')" "example.com|settings|false"
jx PUT settings '{"gateway":{"domain":"","provider_preview":""}}' 'j.gateway.domain' >/dev/null

echo "== v1.9: URL dễ đọc (tên box + nhãn port, alias <key>-<port>)"
expect "ports: name + primary + label + alias" "$(jx GET boxes/box_a9d054/ports - 'j.name+"|"+j.primary_port+"|"+j.ports[0].primary+"|"+j.ports[0].label+"|"+/^https:\/\/[0-9a-f]{20}-3000\./.test(j.ports[0].alias_url)')" "kecon-story-gen|3000|true|web|true"
expect "POST ports {port,label} → <name>-<label>" "$(jx POST boxes/box_a9d054/ports '{"port":5432,"label":"pg"}' 'j.ports[0].port+"|"+j.ports[0].gateway_url+"|"+/[0-9]/.test(j.ports[0].gateway_url.split("//")[1].split(".")[0])')" "5432|https://kecon-story-gen-pg.boxaz.dev/|false"
expect "PUT ports/:port {label}" "$(jx PUT boxes/box_a9d054/ports/5432 '{"label":"db"}' 'j.label+"|"+j.url')" "db|https://kecon-story-gen-db.boxaz.dev/"
expect "PUT label trùng port khác → 400" "$(jx PUT boxes/box_a9d054/ports/5432 '{"label":"web"}' 'j.error.code')" "INVALID"
expect "PUT label là số → 400" "$(jx PUT boxes/box_a9d054/ports/5432 '{"label":"5432"}' 'j.error.code')" "INVALID"
expect "PATCH subdomain dành riêng → 400" "$(jx PATCH boxes/box_a9d054 '{"subdomain":"hark"}' 'j.error.code')" "INVALID"
expect "PATCH subdomain đã có người dùng → 400" "$(jx PATCH boxes/box_a9d054 '{"subdomain":"hark-research"}' 'j.error.code+"|"+/hark-research-blue/.test(j.error.hint)')" "INVALID|true"
expect "PATCH subdomain → đổi tên" "$(jx PATCH boxes/box_a9d054 '{"subdomain":"Kể Chuyện"}' 'j.subdomain')" "ke-chuyen"
expect "ports theo tên mới" "$(jx GET boxes/box_a9d054/ports - 'j.ports.find(p=>p.port===3000).gateway_url')" "https://ke-chuyen.boxaz.dev/"
jx PATCH boxes/box_a9d054 '{"subdomain":"kecon-story-gen"}' 'j.subdomain' >/dev/null
grep -rqs "box-name-input" .next/static/chunks && grep -rqs "edit-label-" .next/static/chunks && ok "bundle có UI tên box + nhãn port" || bad "bundle thiếu UI tên box / nhãn port"
declare -A GWWORD=([vi]="Gateway domain" [en]="Gateway domain" [zh]="网关域名" [ja]="ゲートウェイドメイン")
for l in $LOCALES; do
  node -e 'const m=require("./messages/'$l'.json");process.exit(m.gateway&&m.gateway.title==="'"${GWWORD[$l]}"'"&&m.gateway.previewHint&&m.gateway.reserved&&m.gateway.reachable.includes("{ms}")&&m.ports.previewPrivate?0:1)' && ok "[$l] chuỗi gateway.* đủ" || bad "[$l] thiếu chuỗi gateway.*"
done

echo "== Lỗi được dịch theo ngôn ngữ (code không đổi)"
declare -A NF=([vi]="Không tìm thấy" [en]="not found" [zh]="未找到" [ja]="見つかりません")
for l in $LOCALES; do
  r=$(curl -s -b "boxaz_locale=$l" "$BASE/api/boxaz/boxes/khong-co")
  if grep -q '"code":"NOT_FOUND"' <<<"$r" && grep -qF "${NF[$l]}" <<<"$r"; then ok "[$l] NOT_FOUND: $(grep -o '"message":"[^"]*"' <<<"$r")"; else bad "[$l] lỗi chưa dịch: $r"; fi
done
r=$(curl -s -H 'Accept-Language: ja' "$BASE/api/boxaz/boxes/khong-co")
grep -qF "見つかりません" <<<"$r" && ok "Accept-Language: ja (không cookie) → lỗi tiếng Nhật" || bad "Accept-Language ja: $r"
r=$(curl -s -X POST -b boxaz_locale=zh "$BASE/api/boxaz/emergency-stop" -H 'content-type: application/json' -d '{"confirm":"sai"}')
grep -q '"docs_url"' <<<"$r" && grep -qF "缺少确认" <<<"$r" && ok "[zh] 400 emergency-stop có docs_url" || bad "emergency-stop zh: $r"

echo "== Mutation cũ"
code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/api/boxaz/emergency-stop" -H 'content-type: application/json' -d '{"confirm":"sai"}')
[ "$code" = "400" ] && ok "400 emergency-stop thiếu DUNG" || bad "emergency-stop $code"
code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/api/boxaz/templates/validate" -H 'content-type: application/json' -d '{"yaml":"name: x\nspec: {cpu: 2, ram_mb: 2048, disk_gb: 20}\n"}')
[ "$code" = "200" ] && ok "200 templates/validate" || bad "validate $code"
sse=$(timeout 4 curl -sN "$BASE/api/boxaz/stream" | head -c 200)
echo "$sse" | grep -q "overview.updated" && ok "SSE /stream" || bad "SSE"

echo "== Log server: không có MISSING_MESSAGE / lỗi i18n"
if grep -q "MISSING_MESSAGE\|INVALID_MESSAGE\|FORMATTING_ERROR\|\[i18n\]" smoke-server.log; then bad "server log có lỗi i18n:"; grep -m5 "i18n\|MESSAGE" smoke-server.log; else ok "không có lỗi i18n trong log"; fi

echo "PASS: $pass"
[ $fail = 0 ] && echo "SMOKE: TẤT CẢ OK" || echo "SMOKE: CÓ LỖI"
exit $fail
