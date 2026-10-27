#!/usr/bin/env bash
# Live test v1.10 (box access P0) on the real domain behind the Cloudflare
# Tunnel (deploy/cloudflare-tunnel/up.sh). Per provider (default: e2b prized),
# one sandbox at a time:
#   web terminal through wss://<name>.<domain>/.boxaz/terminal with a ticket →
#   drop → reattach (ring-buffer replay) → `box forward` to python http.server
#   :9000 → curl via localhost → OpenSSH `box ssh` (ProxyCommand box ssh-proxy
#   %h through <name>.<domain>) running uname -a → ssh -L forward → down →
#   provider list [].
#
#   ENV_FILE=…/gateway.env [PROVIDERS="e2b prized"] scripts/live-access.sh
#
# The CLI talks to the public URL (BOXAZ_URL=$BOXAZ_PUBLIC_URL), so tickets
# and every socket go through Cloudflare. A watchdog deletes the provider's
# managed sandboxes after WATCHDOG_S (280 s); the exit trap downs every box.
set -uo pipefail
cd "$(dirname "$0")/.."
: "${ENV_FILE:?set ENV_FILE (the gateway env file used by up.sh)}"
set -a; . "$ENV_FILE"; set +a
ADDR=${BOXAZ_ADDR:-:8080}
LOCAL=http://127.0.0.1:${ADDR##*:}
ADMIN=$BOXAZ_ADMIN_KEY
DOMAIN=$BOXAZ_GATEWAY_DOMAIN
PROVIDERS=${PROVIDERS:-e2b prized}
WORK=$(mktemp -d /tmp/sbx-access.XXXXXX); mkdir -p "$WORK/home"
BIN=$(pwd)/bin
WSC="python3 $(pwd)/scripts/wsclient.py"

pass() { printf '  PASS %s\n' "$*"; }
note() { printf '  NOTE %s\n' "$*"; }
api() { local m=$1 p=$2; shift 2; curl -sS -X "$m" "$LOCAL/v1$p" -H "Authorization: Bearer ${KEY:-$ADMIN}" -H 'Content-Type: application/json' "$@"; }
ms() { date +%s%3N; }
plist() {
  case $1 in
    e2b) curl -sS -H "X-API-Key: $E2B_API_KEY" https://api.e2b.app/v2/sandboxes | jq -c '[.[]|select(.state!="killed")|.sandboxID]' ;;
    prized) curl -sS -H "Authorization: Bearer $PRIZED_API_KEY" https://api.prized.dev/api/v1/sandboxes | jq -c '[.sandboxes[]|select(.state!="deleting" and .state!="deleted")|.id]' ;;
  esac
}
pkillall() {
  case $1 in
    e2b) for id in $(curl -sS -H "X-API-Key: $E2B_API_KEY" https://api.e2b.app/v2/sandboxes | jq -r '.[]?|select(.metadata.managed=="1")|.sandboxID'); do
           curl -sS -X DELETE -H "X-API-Key: $E2B_API_KEY" https://api.e2b.app/sandboxes/$id >/dev/null || true; done ;;
    prized) curl -sS -G -X DELETE -H "Authorization: Bearer $PRIZED_API_KEY" https://api.prized.dev/api/v1/sandboxes --data-urlencode "label=managed=1" >/dev/null 2>&1 || true ;;
  esac
}
downall() { for b in $(api GET /boxes 2>/dev/null | jq -r '.items[]?|select(.status!="terminated")|.id' 2>/dev/null); do api DELETE /boxes/$b >/dev/null 2>&1 || true; done; }
cleanup() {
  kill ${WATCHDOG:-} ${FWD:-} ${SSHL:-} 2>/dev/null || true
  downall; sleep 2
  for p in $PROVIDERS; do pkillall $p; done; sleep 1
  for p in $PROVIDERS; do echo "  $p sandboxes after cleanup: $(plist $p)"; done
}
trap cleanup EXIT

[ "$(curl -s -o /dev/null -w '%{http_code}' "$BOXAZ_PUBLIC_URL/v1/health")" = 200 ] && pass "core reachable at $BOXAZ_PUBLIC_URL" || { echo "core not reachable"; exit 1; }
for p in $PROVIDERS; do [ "$(plist $p)" = "[]" ] || { echo "$p already has live sandboxes: $(plist $p)"; exit 1; }; done
which ssh >/dev/null && echo "    OpenSSH: $(ssh -V 2>&1)"

for PROV in $PROVIDERS; do
  echo "== $PROV"
  AGENT=$(api POST /agents -d "$(jq -nc --arg n "access-$PROV-$(date +%s)" --arg p "$PROV" '{name:$n,kind:"claude-code",budget_day_usd:1,policy:{providers:[$p]}}')" | jq -r .id)
  AKEY=$(api POST /keys -d "{\"agent_id\":\"$AGENT\",\"name\":\"access-live\"}" | jq -r .key)
  export BOXAZ_URL=$BOXAZ_PUBLIC_URL BOXAZ_API_KEY=$AKEY
  ( sleep "${WATCHDOG_S:-280}"; echo "  WATCHDOG: deleting every managed $PROV sandbox"; pkillall $PROV ) & WATCHDOG=$!
  T0=$(ms)
  B=$(KEY=$AKEY api POST /boxes -d "{\"template\":\"base\",\"slug\":\"acc-$PROV\",\"lifecycle\":\"session\",\"requirements\":{\"isolation\":\"sandbox\",\"cpu\":1,\"ram_mb\":512,\"disk_gb\":5,\"duration_hours\":0.1}}")
  BOX=$(echo "$B" | jq -r .id)
  for i in $(seq 240); do S=$(KEY=$AKEY api GET /boxes/$BOX | jq -r .status); [ "$S" = running ] || [ "$S" = failed ] && break; sleep 0.25; done
  if [ "$S" != running ]; then note "$PROV box $S: $(echo "$B" | jq -c .error 2>/dev/null)"; kill $WATCHDOG; continue; fi
  NAME=$(KEY=$AKEY api GET /boxes/$BOX | jq -r .subdomain)
  echo "    box up in $(($(ms)-T0)) ms: $BOX name $NAME provider $(KEY=$AKEY api GET /boxes/$BOX | jq -r .provider)"
  KEY=$AKEY api POST /boxes/$BOX/exec -d '{"command":"cd /tmp && (command -v python3 >/dev/null && nohup python3 -m http.server 9000 >/tmp/http.log 2>&1 &) ; sleep 1; echo started","timeout_s":30}' | jq -c '{exit_code,stdout_tail}' | sed 's/^/    http.server: /'

  # 1. web terminal through the domain with a ticket
  TK=$("$BIN/box" ticket "$BOX" terminal --json)
  URL=$(echo "$TK" | jq -r .url)
  [[ $URL == wss://$NAME.$DOMAIN/.boxaz/terminal* ]] && pass "ticket url on the box host: ${URL%%ticket=*}ticket=bxa_…" || note "ticket url $URL"
  T0=$(ms)
  if $WSC "$URL&proto=2&cols=100&rows=30" --send 'echo T-$((6*7)); uname -a\n' --until T-42 --timeout 90 >"$WORK/t1.out" 2>"$WORK/t1.ctl"; then
    pass "terminal via ticket through wss://$NAME.$DOMAIN: first output in $(($(ms)-T0)) ms (includes starting the in-box agent)"
  else note "terminal: $(tail -c 300 "$WORK/t1.out") $(tail -3 "$WORK/t1.ctl")"; fi
  SID=$(grep -o '"id":"term_[a-z0-9]*"' "$WORK/t1.ctl" | head -1 | cut -d'"' -f4)
  # 2. reattach after the drop
  if [ -n "$SID" ]; then
    URL2=$("$BIN/box" ticket "$BOX" terminal --session "$SID" --json | jq -r .url)
    T0=$(ms)
    if $WSC "$URL2&proto=2" --send 'echo R-$((5+5))\n' --until R-10 --timeout 30 >"$WORK/t2.out" 2>"$WORK/t2.ctl" && grep -q T-42 "$WORK/t2.out" && grep -q '"created":false' "$WORK/t2.ctl"; then
      pass "terminal reattach to $SID after the drop: replay has the old output, new command ran ($(($(ms)-T0)) ms)"
    else note "reattach: $(tail -c 300 "$WORK/t2.out")"; fi
  fi
  # 3. box forward → python http.server :9000
  "$BIN/box" forward "$BOX" 9000 19000 >"$WORK/fwd.log" 2>&1 & FWD=$!
  for i in $(seq 100); do grep -q Forwarding "$WORK/fwd.log" && break; sleep 0.1; done
  T0=$(ms); C=$(curl -s -o /dev/null -w '%{http_code}' --max-time 60 http://127.0.0.1:19000/); T1=$(ms)
  C2=$(curl -s -o /dev/null -w '%{http_code} %{time_total}' --max-time 30 http://127.0.0.1:19000/)
  [ "$C" = 200 ] && pass "box forward $NAME 9000 → curl http://127.0.0.1:19000/ → 200 (first $((T1-T0)) ms, warm ${C2#* }s)" || note "forward: $C / $(cat "$WORK/fwd.log" | tail -3)"
  kill $FWD 2>/dev/null; wait $FWD 2>/dev/null
  # 4. OpenSSH through ProxyCommand box ssh-proxy %h (wss://<name>.<domain>/.boxaz/ssh)
  T0=$(ms)
  O=$(HOME=$WORK/home "$BIN/box" ssh "$NAME" -o BatchMode=yes -- 'uname -a; echo ssh-ok-$((6*7))' </dev/null 2>&1)
  [[ $O == *ssh-ok-42* ]] && pass "ssh (OpenSSH, ProxyCommand through $NAME.$DOMAIN) uname -a in $(($(ms)-T0)) ms: $(echo "$O" | head -1 | cut -c1-90)" || note "ssh: $O"
  HOME=$WORK/home "$BIN/box" ssh "$NAME" -o BatchMode=yes -o ExitOnForwardFailure=yes -L 19001:127.0.0.1:9000 -- 'sleep 8' </dev/null >"$WORK/sshL.log" 2>&1 & SSHL=$!
  C=000; T0=$(ms); for i in $(seq 40); do C=$(curl -s -o /dev/null -w '%{http_code}' --max-time 10 http://127.0.0.1:19001/); [ "$C" = 200 ] && break; sleep 0.25; done
  [ "$C" = 200 ] && pass "ssh -L 19001:127.0.0.1:9000 → curl → 200 ($(($(ms)-T0)) ms incl. ssh setup)" || note "ssh -L: $C $(cat "$WORK/sshL.log")"
  wait $SSHL 2>/dev/null
  "$BIN/box" access "$BOX" | sed -n '1,3p' | sed 's/^/    /'
  # 5. down
  T0=$(ms); KEY=$AKEY api DELETE /boxes/$BOX >/dev/null
  for i in $(seq 30); do [ "$(plist $PROV)" = "[]" ] && break; sleep 1; done
  L=$(plist $PROV); [ "$L" = "[]" ] && pass "$PROV list [] after down ($(($(ms)-T0)) ms)" || { pkillall $PROV; note "$PROV leftover $L (deleted)"; }
  kill $WATCHDOG 2>/dev/null
done
echo "== done"
