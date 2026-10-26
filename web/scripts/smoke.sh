#!/usr/bin/env bash
# Smoke test: chạy bản build với BOXA_MOCK=1 rồi curl mọi route + API chính, kỳ vọng HTTP 200.
# Dùng: pnpm build && pnpm smoke   (PORT mặc định 3199)
set -u
cd "$(dirname "$0")/.."
PORT="${PORT:-3199}"
BASE="http://localhost:$PORT"

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

for i in $(seq 1 40); do
  curl -s -o /dev/null "$BASE/login" && break
  sleep 0.5
done

fail=0
check() {
  local path="$1" want="${2:-200}"
  local code
  code=$(curl -s -o /dev/null -w '%{http_code}' "$BASE$path")
  if [ "$code" = "$want" ]; then echo "  ok   $code $path"; else echo "  FAIL $code (muốn $want) $path"; fail=1; fi
}

echo "== Trang"
for r in / /boxes /boxes/box_7f3a91 /agents /agents/agt_kecon /costs /infra /templates /secrets /settings /admin /onboarding /login; do check "$r"; done
check /khong-ton-tai 404

echo "== API (qua proxy, mock)"
for a in health overview boxes boxes/box_7f3a91 boxes/box_7f3a91/events boxes/box_7f3a91/snapshots "boxes/box_7f3a91/files?path=/work" boxes/box_7f3a91/observe \
  agents agents/agt_kecon "agents/agt_kecon/snippet?kind=n8n" keys costs costs.csv costs/suggestions providers hosts offers templates secrets approvals settings \
  admin/system admin/jobs admin/audit admin/quotas admin/users logs/log_0002 jobs/job_0001 api/session; do
  case "$a" in api/*) check "/$a" ;; *) check "/api/boxa/$a" ;; esac
done
check /api/boxa/boxes/khong-co 404

echo "== Mutation"
code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/api/boxa/emergency-stop" -H 'content-type: application/json' -d '{"confirm":"sai"}')
[ "$code" = "400" ] && echo "  ok   400 emergency-stop thiếu DUNG" || { echo "  FAIL emergency-stop $code"; fail=1; }
code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/api/boxa/templates/validate" -H 'content-type: application/json' -d '{"yaml":"name: x\nspec: {cpu: 2, ram_mb: 2048, disk_gb: 20}\n"}')
[ "$code" = "200" ] && echo "  ok   200 templates/validate" || { echo "  FAIL validate $code"; fail=1; }
sse=$(timeout 4 curl -sN "$BASE/api/boxa/stream" | head -c 200)
echo "$sse" | grep -q "overview.updated" && echo "  ok   SSE /stream" || { echo "  FAIL SSE"; fail=1; }

[ $fail = 0 ] && echo "SMOKE: TẤT CẢ OK" || echo "SMOKE: CÓ LỖI"
exit $fail
