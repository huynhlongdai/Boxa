#!/usr/bin/env bash
# Smoke test: chạy bản build với BOXA_MOCK=1 rồi curl mọi route (ở cả 4 ngôn ngữ qua cookie boxa_locale) + API chính,
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
  BOXA_MOCK=1 PORT=$PORT HOSTNAME=127.0.0.1 node .next/standalone/server.js > smoke-server.log 2>&1 &
else
  BOXA_MOCK=1 npx next start -p "$PORT" > smoke-server.log 2>&1 &
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
  body=$(curl -s -w '\n__CODE__%{http_code}' -b "boxa_locale=$l" "$BASE$path")
  code="${body##*__CODE__}"
  if [ "$code" != "200" ]; then bad "[$l] $code $path"; return; fi
  if ! grep -q "<html lang=\"$l\"" <<<"$body"; then bad "[$l] thiếu <html lang=\"$l\"> $path"; return; fi
  if [[ "$path" != /login && "$path" != /onboarding ]] && ! grep -qF "${NAVWORD[$l]}" <<<"$body"; then bad "[$l] không thấy nhãn '${NAVWORD[$l]}' $path"; return; fi
  ok "[$l] 200 $path"
}

ROUTES="/ /boxes /boxes/box_7f3a91 /boxes/box_7f3a91?tab=logs /agents /agents/agt_kecon /costs /infra /templates /secrets /settings /admin /developers /onboarding /login"

echo "== Trang × ngôn ngữ (cookie boxa_locale)"
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
  agents agents/agt_kecon "agents/agt_kecon/snippet?kind=n8n" keys costs costs.csv costs/suggestions providers hosts offers templates secrets approvals settings \
  admin/system admin/jobs admin/audit admin/quotas admin/users logs/log_0002 jobs/job_0001 api/session; do
  case "$a" in api/*) check "/$a" ;; *) check "/api/boxa/$a" ;; esac
done
check /api/boxa/boxes/khong-co 404

echo "== v1.1: agent kinds, snippet theo transport, yaml, job wait"
n=$(curl -s "$BASE/api/boxa/agent-kinds" | node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{const j=JSON.parse(s);const c=new Set(j.items.map(k=>k.category));console.log(j.items.length+" "+[...c].sort().join(","))})')
[ "$n" = "22 coding,custom,framework,platform,workflow" ] && ok "agent-kinds: $n" || bad "agent-kinds: $n"
for tr in mcp-http mcp-stdio rest sdk-python sdk-ts cli function-calling; do
  got=$(curl -s "$BASE/api/boxa/agents/agt_kecon/snippet?kind=cursor&lang=$tr" | node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{const j=JSON.parse(s);console.log(j.transport+"|"+(j.snippet.length>20))})')
  [ "$got" = "$tr|true" ] && ok "snippet cursor/$tr" || bad "snippet cursor/$tr → $got"
done
curl -s "$BASE/api/boxa/boxes/box_7f3a91/yaml" | grep -q '"yaml":"' && ok "GET boxes/:id/yaml" || bad "boxes/:id/yaml"
check "/api/boxa/jobs/job_0001?wait=1"

echo "== v1.1: quote, exec-batch (tokens_saved + log_id + next), restore, fork"
q=$(curl -s -X POST "$BASE/api/boxa/quote" -H 'content-type: application/json' -b boxa_locale=en \
  -d '{"agent_id":"agt_shopee","template":"python","lifecycle":"task","spec":{"cpu":2,"ram_mb":2048,"disk_gb":20}}')
echo "$q" | node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{const j=JSON.parse(s);process.exit(j.provider&&j.cost_per_hour>=0&&j.placement_reason&&Array.isArray(j.alternatives)?0:1)})' \
  && ok "POST /quote → $(echo "$q" | head -c 90)…" || bad "quote: $q"
b=$(curl -s -X POST "$BASE/api/boxa/boxes/box_7f3a91/exec-batch" -H 'content-type: application/json' -d '{"commands":["echo hello world","python -m pytest -q tests"],"stop_on_error":true}')
echo "$b" | node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{const j=JSON.parse(s);process.exit(j.results?.length===2&&j.results.every(r=>r.log_id&&typeof r.tokens_saved==="number")&&j.next?.length?0:1)})' \
  && ok "POST exec-batch (2 kết quả, log_id, tokens_saved, next)" || bad "exec-batch: $(echo "$b" | head -c 200)"
ev=$(curl -s "$BASE/api/boxa/boxes/box_7f3a91/events" | node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{const j=JSON.parse(s);const e=j.items.find(x=>x.type==="box.exec"&&x.data);console.log(e&&e.data.log_id&&e.tokens_saved!==undefined?"ok":"no")})')
[ "$ev" = "ok" ] && ok "event box.exec có data.tokens_saved + log_id" || bad "event box.exec thiếu data"
code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/api/boxa/boxes/box_7f3a91/exec-batch" -H 'content-type: application/json' -d '{"commands":[]}')
[ "$code" = "400" ] && ok "400 exec-batch rỗng" || bad "exec-batch rỗng $code"
snap=$(curl -s -X POST "$BASE/api/boxa/boxes/box_7f3a91/snapshot" | node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>console.log(JSON.parse(s).id))')
fk=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/api/boxa/boxes/box_7f3a91/fork" -H 'content-type: application/json' -d "{\"snapshot_id\":\"$snap\"}")
[ "$fk" = "201" ] && ok "201 fork từ $snap" || bad "fork $fk"
rs=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/api/boxa/boxes/box_7f3a91/restore" -H 'content-type: application/json' -d "{\"snapshot_id\":\"$snap\"}")
[ "$rs" = "200" ] && ok "200 restore từ $snap" || bad "restore $rs"

echo "== v1.1: discovery (không cần key)"
check /api/boxa/openapi.json
ov=$(curl -s "$BASE/api/boxa/openapi.json" | node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{const j=JSON.parse(s);console.log(j.openapi+" "+Object.keys(j.paths).length)})')
[[ "$ov" == 3.1.0* ]] && ok "openapi $ov paths" || bad "openapi: $ov"
for f in openai anthropic gemini mcp; do
  t=$(curl -s "$BASE/api/boxa/tools?format=$f" | node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{const j=JSON.parse(s);const n=j.format==="gemini"?j.tools[0].functionDeclarations.length:j.tools.length;console.log(j.format+":"+n)})')
  [[ "$t" == "$f:"* && "${t#*:}" -ge 12 ]] && ok "tools?format=$f → ${t#*:} tool" || bad "tools $f → $t"
done
for p in _root/llms.txt _root/llms-full.txt _root/.well-known/mcp.json _root/.well-known/agent.json; do check "/api/boxa/$p"; done
curl -s "$BASE/api/boxa/_root/llms.txt" | grep -q "^# Boxa" && ok "llms.txt có tiêu đề" || bad "llms.txt"

echo "== Lỗi được dịch theo ngôn ngữ (code không đổi)"
declare -A NF=([vi]="Không tìm thấy" [en]="not found" [zh]="未找到" [ja]="見つかりません")
for l in $LOCALES; do
  r=$(curl -s -b "boxa_locale=$l" "$BASE/api/boxa/boxes/khong-co")
  if grep -q '"code":"NOT_FOUND"' <<<"$r" && grep -qF "${NF[$l]}" <<<"$r"; then ok "[$l] NOT_FOUND: $(grep -o '"message":"[^"]*"' <<<"$r")"; else bad "[$l] lỗi chưa dịch: $r"; fi
done
r=$(curl -s -H 'Accept-Language: ja' "$BASE/api/boxa/boxes/khong-co")
grep -qF "見つかりません" <<<"$r" && ok "Accept-Language: ja (không cookie) → lỗi tiếng Nhật" || bad "Accept-Language ja: $r"
r=$(curl -s -X POST -b boxa_locale=zh "$BASE/api/boxa/emergency-stop" -H 'content-type: application/json' -d '{"confirm":"sai"}')
grep -q '"docs_url"' <<<"$r" && grep -qF "缺少确认" <<<"$r" && ok "[zh] 400 emergency-stop có docs_url" || bad "emergency-stop zh: $r"

echo "== Mutation cũ"
code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/api/boxa/emergency-stop" -H 'content-type: application/json' -d '{"confirm":"sai"}')
[ "$code" = "400" ] && ok "400 emergency-stop thiếu DUNG" || bad "emergency-stop $code"
code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/api/boxa/templates/validate" -H 'content-type: application/json' -d '{"yaml":"name: x\nspec: {cpu: 2, ram_mb: 2048, disk_gb: 20}\n"}')
[ "$code" = "200" ] && ok "200 templates/validate" || bad "validate $code"
sse=$(timeout 4 curl -sN "$BASE/api/boxa/stream" | head -c 200)
echo "$sse" | grep -q "overview.updated" && ok "SSE /stream" || bad "SSE"

echo "== Log server: không có MISSING_MESSAGE / lỗi i18n"
if grep -q "MISSING_MESSAGE\|INVALID_MESSAGE\|FORMATTING_ERROR\|\[i18n\]" smoke-server.log; then bad "server log có lỗi i18n:"; grep -m5 "i18n\|MESSAGE" smoke-server.log; else ok "không có lỗi i18n trong log"; fi

echo "PASS: $pass"
[ $fail = 0 ] && echo "SMOKE: TẤT CẢ OK" || echo "SMOKE: CÓ LỖI"
exit $fail
