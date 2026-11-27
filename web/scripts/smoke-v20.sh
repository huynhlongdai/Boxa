#!/usr/bin/env bash
# Smoke v2.0 với Core THẬT, đi qua proxy Next (không gọi Core trực tiếp trừ khi cần đọc link mời):
#   đăng ký (người đầu = quản trị nền tảng) → cookie HttpOnly/Secure/SameSite=Lax, không lộ token
#   → CSRF (Origin lạ, thiếu header double-submit) bị chặn → tạo workspace → mời → xem lời mời (không đăng nhập)
#   → người được mời đăng ký bằng workspace_invite → /auth/me → đổi workspace → thành viên/lời mời/hạn mức
#   → QUOTA_EXCEEDED có details → vị trí box → chế độ admin key vẫn chạy.
# Dùng: BASE=http://localhost:3199 bash scripts/smoke-v20.sh   (Next chạy với BOXMES_MOCK=0 BOXMES_API=<core>/v1)
# Core phải là instance mới (chưa có user) để tài khoản đầu tiên thành quản trị nền tảng.
set -u
BASE="${BASE:-http://localhost:3199}"
ADMIN_KEY="${ADMIN_KEY:-}"
T=$(mktemp -d)
trap 'rm -rf "$T"' EXIT
fail=0
ok() { echo "  ok   $*"; }
bad() { echo "  FAIL $*"; fail=1; }
jqr() { node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{try{const j=JSON.parse(s);console.log(eval(process.argv[1]))}catch(e){console.log("ERR:"+s.slice(0,200))}})' "$1"; }
csrf() { awk '$6=="boxmes_csrf"{print $7}' "$1" | tail -1; }
# post <jar> <path> <json> [extra curl args...]: POST same-origin (Origin + double-submit header)
post() { local jar=$1 p=$2 d=$3; shift 3; curl -s -b "$jar" -c "$jar" -X POST "$BASE$p" -H "Origin: $BASE" -H "X-Boxmes-CSRF: $(csrf "$jar")" -H 'content-type: application/json' -d "$d" "$@"; }
req() { local jar=$1 m=$2 p=$3 d=${4:-}; if [ -n "$d" ]; then curl -s -b "$jar" -c "$jar" -X "$m" "$BASE$p" -H "Origin: $BASE" -H "X-Boxmes-CSRF: $(csrf "$jar")" -H 'content-type: application/json' -d "$d"; else curl -s -b "$jar" -c "$jar" -X "$m" "$BASE$p" -H "Origin: $BASE" -H "X-Boxmes-CSRF: $(csrf "$jar")"; fi; }
get() { curl -s -b "$1" -c "$1" "$BASE$2"; }

A=$T/a.jar; B=$T/b.jar; K=$T/k.jar
echo "== Đăng ký người đầu tiên (owner + quản trị nền tảng)"
get "$A" /api/session > /dev/null   # cấp cookie CSRF
[ -n "$(csrf "$A")" ] && ok "cookie boxmes_csrf được cấp" || bad "không có cookie CSRF"
HDR=$(curl -s -D - -o "$T/su.json" -b "$A" -c "$A" -X POST "$BASE/api/auth/signup" -H "Origin: $BASE" -H "X-Boxmes-CSRF: $(csrf "$A")" -H 'content-type: application/json' \
  -d '{"email":"root@example.com","password":"rootrootroot","name":"Root","workspace":"Root Co"}')
echo "$HDR" | grep -i '^set-cookie: boxmes_key=' | grep -qi 'httponly' && echo "$HDR" | grep -i '^set-cookie: boxmes_key=' | grep -qi 'secure' && echo "$HDR" | grep -i '^set-cookie: boxmes_key=' | grep -qi 'samesite=lax' \
  && ok "Set-Cookie boxmes_key: HttpOnly; Secure; SameSite=Lax" || bad "cookie phiên thiếu cờ: $(echo "$HDR" | grep -i 'set-cookie: boxmes_key')"
grep -q '"token"' "$T/su.json" && bad "body đăng ký lộ token" || ok "body đăng ký không có token: $(head -c 120 "$T/su.json")"
grep -q 'bxs_' "$T/su.json" && bad "body có bxs_" || true
ME=$(get "$A" /api/session)
[ "$(echo "$ME" | jqr 'j.mode+"|"+j.me.platform_admin+"|"+j.me.role+"|"+j.me.permissions.includes("members:write")')" = "session|true|owner|true" ] && ok "/api/session: phiên, PA, owner, có members:write" || bad "session: $ME"
echo "$ME" | grep -q 'bxs_' && bad "/api/session lộ token" || ok "/api/session không lộ token"

echo "== CSRF"
c=$(curl -s -o /dev/null -w '%{http_code}' -b "$A" -X POST "$BASE/api/boxmes/workspaces" -H "Origin: https://evil.example" -H "X-Boxmes-CSRF: $(csrf "$A")" -H 'content-type: application/json' -d '{"name":"evil"}')
[ "$c" = 403 ] && ok "Origin lạ → 403" || bad "Origin lạ → $c"
c=$(curl -s -o /dev/null -w '%{http_code}' -b "$A" -X POST "$BASE/api/boxmes/workspaces" -H "Origin: $BASE" -H 'content-type: application/json' -d '{"name":"evil"}')
[ "$c" = 403 ] && ok "thiếu header double-submit (có cookie phiên) → 403" || bad "thiếu CSRF header → $c"
c=$(curl -s -o /dev/null -w '%{http_code}' -b "$A" -X POST "$BASE/api/boxmes/workspaces" -H 'content-type: application/json' -d '{"name":"evil"}')
[ "$c" = 403 ] && ok "không Origin, không header → 403" || bad "không Origin → $c"
c=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/api/auth/login" -H "Origin: https://evil.example" -H 'content-type: application/json' -d '{"email":"root@example.com","password":"rootrootroot"}')
[ "$c" = 403 ] && ok "login CSRF (Origin lạ) → 403" || bad "login Origin lạ → $c"
c=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/api/boxmes/auth/login" -H "Origin: $BASE" -H 'content-type: application/json' -d '{"email":"root@example.com","password":"rootrootroot"}')
[ "$c" = 404 ] && ok "proxy không cho gọi /auth/login (token không tới trình duyệt) → 404" || bad "proxy auth/login → $c"

echo "== Workspace"
W2=$(post "$A" /api/boxmes/workspaces '{"name":"Team Two"}')
WS2=$(echo "$W2" | jqr 'j.id'); [ "${WS2#ws_}" != "$WS2" ] && ok "tạo workspace $WS2 (role $(echo "$W2" | jqr 'j.role'))" || bad "create ws: $W2"
SW=$(post "$A" /api/boxmes/auth/workspace "{\"workspace_id\":\"$WS2\"}")
[ "$(echo "$SW" | jqr 'j.workspace.id')" = "$WS2" ] && ok "đổi workspace → $WS2" || bad "switch: $SW"
[ "$(get "$A" /api/session | jqr 'j.me.workspace.id')" = "$WS2" ] && ok "/api/session theo workspace mới" || bad "session không đổi ws"
U=$(get "$A" /api/boxmes/workspaces/current/usage)
ok "usage: plan=$(echo "$U" | jqr 'j.plan') max_members=$(echo "$U" | jqr 'j.quota.max_members') members=$(echo "$U" | jqr 'j.used.members')"
P=$(get "$A" /api/boxmes/plans); [ "$(echo "$P" | jqr 'j.items.map(x=>x.id).join(",")')" = "free,pro,team,unlimited" ] && ok "GET /plans (không cần đăng nhập vẫn qua proxy)" || bad "plans: $P"

echo "== Mời + nhận lời mời"
Q=$(post "$A" /api/boxmes/workspaces/current/invites '{"email":"carol@example.com","role":"viewer"}')
QC=$(echo "$Q" | jqr 'j.error.code+"|"+j.error.details.quota+"|"+j.error.details.limit+"|"+j.error.details.used+"|"+j.error.details.plan+"|"+(j.error.next.length>0)')
[ "$QC" = "QUOTA_EXCEEDED|max_members|1|1|free|true" ] && ok "gói free (1 thành viên) → QUOTA_EXCEEDED details{quota,limit,used,plan} + next: $QC" || bad "quota: $Q"
R=$(req "$A" PATCH "/api/boxmes/admin/workspaces/$WS2" '{"plan":"pro"}'); [ "$(echo "$R" | jqr 'j.plan')" = pro ] && ok "quản trị nền tảng nâng $WS2 lên pro (qua proxy)" || bad "set plan: $R"
INV=$(post "$A" /api/boxmes/workspaces/current/invites '{"email":"bob@example.com","role":"developer"}')
URL=$(echo "$INV" | jqr 'j.invite_url'); TOK=${URL##*/invite/}
[ "${TOK#bxi_}" != "$TOK" ] && ok "tạo lời mời (status $(echo "$INV" | jqr 'j.status'), link /invite/bxi_…)" || bad "invite: $INV"
PV=$(curl -s "$BASE/api/boxmes/workspace-invites/$TOK")
[ "$(echo "$PV" | jqr 'j.workspace.name+"|"+j.email+"|"+j.status+"|"+j.account_exists')" = "Team Two|bob@example.com|pending|false" ] && ok "xem lời mời không cần đăng nhập" || bad "preview: $PV"
c=$(curl -s -o /dev/null -w '%{http_code}' "$BASE/invite/$TOK"); [ "$c" = 200 ] && ok "trang /invite/<token> 200" || bad "/invite page $c"
get "$B" /api/session > /dev/null
post "$B" /api/auth/signup "{\"email\":\"bob@example.com\",\"password\":\"bobbobbobbob\",\"workspace_invite\":\"$TOK\"}" > "$T/b.json"
BM=$(get "$B" /api/session)
[ "$(echo "$BM" | jqr 'j.me.workspace.id+"|"+j.me.role+"|"+j.me.platform_admin+"|"+j.me.permissions.includes("members:write")+"|"+j.me.permissions.includes("boxes:write")')" = "$WS2|developer|false|false|true" ] \
  && ok "bob đăng ký bằng workspace_invite → vào $WS2 với vai trò developer (không PA, không members:write)" || bad "bob: $(cat "$T/b.json") $BM"
L=$(get "$A" /api/boxmes/workspaces/current/invites)
ok "danh sách lời mời: $(echo "$L" | jqr 'j.items.map(i=>i.email+":"+i.status).join(", ")')"
c=$(curl -s -o "$T/bi.json" -w '%{http_code}' -b "$B" "$BASE/api/boxmes/workspaces/current/invites")
[ "$c" = 403 ] && ok "developer xem lời mời → 403 $(jqr 'j.error.code' < "$T/bi.json")" || bad "developer invites → $c $(cat "$T/bi.json")"
c=$(curl -s -o /dev/null -w '%{http_code}' -b "$B" "$BASE/api/boxmes/admin/system")
[ "$c" = 401 ] || [ "$c" = 403 ] && ok "developer gọi /admin/system → $c (Nền tảng chỉ PA)" || bad "admin/system cho bob → $c"

echo "== Thành viên, owner cuối cùng"
MB=$(get "$A" /api/boxmes/workspaces/current/members); ok "thành viên: $(echo "$MB" | jqr 'j.items.map(m=>m.email+":"+m.role).join(", ")')"
ROOTID=$(echo "$MB" | jqr 'j.items.find(m=>m.email==="root@example.com").user_id'); BOBID=$(echo "$MB" | jqr 'j.items.find(m=>m.email==="bob@example.com").user_id')
R=$(req "$A" DELETE "/api/boxmes/workspaces/current/members/$ROOTID"); [ "$(echo "$R" | jqr 'j.error.code')" = INVALID ] && ok "owner cuối không rời được: $(echo "$R" | jqr 'j.error.message')" || bad "last owner leave: $R"
R=$(req "$A" PATCH "/api/boxmes/workspaces/current/members/$BOBID" '{"role":"viewer"}'); [ "$(echo "$R" | jqr 'j.role')" = viewer ] && ok "đổi vai trò bob → viewer" || bad "patch: $R"
R=$(req "$B" DELETE "/api/boxmes/workspaces/current/members/$BOBID"); [ "$(echo "$R" | jqr 'j.ok')" = true ] && ok "bob tự rời workspace" || bad "bob leave: $R"
[ "$(get "$B" /api/session | jqr 'j.authed+"|"+(j.me.workspace===null)')" = "true|true" ] && ok "bob vẫn đăng nhập, không còn workspace (banner tạo workspace)" || bad "bob after leave"

echo "== Tài khoản có sẵn nhận lời mời (accept) rồi đổi qua lại"
INV2=$(post "$A" /api/boxmes/workspaces/current/invites '{"email":"bob@example.com","role":"billing"}'); TOK2=$(echo "$INV2" | jqr 'j.invite_url'); TOK2=${TOK2##*/invite/}
[ "$(curl -s "$BASE/api/boxmes/workspace-invites/$TOK2" | jqr 'j.account_exists')" = true ] && ok "preview: account_exists=true → trang mời hiện nút Đăng nhập" || bad "preview2"
R=$(post "$B" "/api/boxmes/workspace-invites/$TOK2/accept" '{}')
[ "$(echo "$R" | jqr 'j.workspace.id+"|"+j.role')" = "$WS2|billing" ] && ok "bob POST accept → vào $WS2 (billing), phiên chuyển sang workspace đó" || bad "accept: $R"
BW=$(post "$B" /api/boxmes/workspaces '{"name":"Bob Solo"}' | jqr 'j.id')
R=$(post "$B" /api/boxmes/auth/workspace "{\"workspace_id\":\"$BW\"}"); [ "$(echo "$R" | jqr 'j.workspace.id+"|"+j.role+"|"+j.workspaces.length')" = "$BW|owner|2" ] && ok "bob đổi sang workspace riêng (owner, 2 workspace)" || bad "bob switch: $R"
R=$(post "$B" /api/boxmes/auth/workspace "{\"workspace_id\":\"$WS2\"}"); [ "$(echo "$R" | jqr 'j.role')" = billing ] && ok "bob đổi lại $WS2 (billing)" || bad "bob switch back: $R"
c=$(curl -s -o /dev/null -w '%{http_code}' -b "$B" "$BASE/api/boxmes/boxes/$BW-khong-co"); [ "$c" = 404 ] && ok "box không thuộc workspace → 404" || bad "cross-ws $c"

echo "== Vị trí box + lọc"
LOC=$(get "$A" /api/boxmes/boxes/locations); [ "$(echo "$LOC" | jqr 'Array.isArray(j.items)&&Array.isArray(j.regions)&&j.total.region')" = all ] && ok "GET /boxes/locations: $(echo "$LOC" | head -c 100)" || bad "locations: $LOC"
c=$(curl -s -o /dev/null -w '%{http_code}' -b "$A" "$BASE/api/boxmes/boxes?region=eu&location=hetzner-fsn"); [ "$c" = 200 ] && ok "GET /boxes?region=&location= 200" || bad "boxes filter $c"

echo "== Trang (đã đăng nhập)"
for p in / /boxes "/boxes?group=location" /settings "/settings?tab=members" /settings/billing /approvals /admin; do
  c=$(curl -s -o /dev/null -w '%{http_code}' -b "$A" "$BASE$p"); [ "$c" = 200 ] && ok "200 $p" || bad "$c $p"
done

echo "== Đăng xuất"
post "$A" /api/auth/logout '{}' > /dev/null
[ "$(get "$A" /api/session | jqr 'j.authed')" = false ] && ok "đăng xuất xoá phiên" || bad "logout"
c=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/api/auth/login" -H "Origin: $BASE" -H 'content-type: application/json' -d '{"email":"root@example.com","password":"rootrootroot"}' -c "$A")
[ "$c" = 200 ] && ok "đăng nhập lại bằng email/mật khẩu" || bad "login $c"

if [ -n "$ADMIN_KEY" ]; then
  echo "== Chế độ admin key (cũ) vẫn chạy"
  get "$K" /api/session > /dev/null
  c=$(post "$K" /api/auth/login "{\"key\":\"$ADMIN_KEY\"}" -o /dev/null -w '%{http_code}'); [ "$c" = 200 ] && ok "đăng nhập bằng admin key" || bad "key login $c"
  S=$(get "$K" /api/session); [ "$(echo "$S" | jqr 'j.mode+"|"+j.me.platform_admin')" = "key|true" ] && ok "/api/session: mode=key, PA (key_prefix $(echo "$S" | jqr 'j.key_prefix'))" || bad "key session: $S"
  c=$(curl -s -o /dev/null -w '%{http_code}' -b "$K" "$BASE/api/boxmes/overview"); [ "$c" = 200 ] && ok "proxy /overview với admin key" || bad "overview $c"
  c=$(curl -s -o /dev/null -w '%{http_code}' -b "$K" "$BASE/api/boxmes/admin/workspaces"); [ "$c" = 200 ] && ok "proxy /admin/workspaces với admin key" || bad "admin/workspaces $c"
fi

[ $fail = 0 ] && echo "SMOKE v2.0: OK" || echo "SMOKE v2.0: CÓ LỖI"
exit $fail
