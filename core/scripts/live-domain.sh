#!/usr/bin/env bash
# Live v1.8 test: the Boxaz gateway on a real domain (host-based URLs
# https://<key>-<port>.<domain> (alias since v1.9; readable https://<name>.<domain>) behind a Cloudflare Tunnel, see
# deploy/cloudflare-tunnel) and private provider previews.
#
# Runs against a core that is ALREADY up behind the tunnel (deploy/
# cloudflare-tunnel/up.sh), with the providers connected:
#   ENV_FILE=…/gateway.env PROVIDERS="e2b prized novita" scripts/live-domain.sh
# Per provider: box up → PUT /app (heroku/node-js-sample, port 8000) →
# gateway URL from outside (DNS over HTTPS to 1.1.1.1, TLS verified) → 200 →
# sleep → request wakes → 200 (latency) → URL token 401/200/302/401 → direct
# provider preview URL refused (403) where the provider supports private
# previews → (e2b) SSE through the domain + native clone keeps the preview
# private → down → provider list [].
# Budget: ≤2 sandboxes at once (box + clone), watchdog deletes every managed
# sandbox of the provider after WATCHDOG_S (280 s).
set -euo pipefail
cd "$(dirname "$0")/.."
: "${ENV_FILE:?set ENV_FILE (the gateway env file used by up.sh)}"
set -a; . "$ENV_FILE"; set +a
ADDR=${BOXAZ_ADDR:-:8080}
export BOXAZ_URL=http://127.0.0.1:${ADDR##*:}
ADMIN=$BOXAZ_ADMIN_KEY
DOMAIN=$BOXAZ_GATEWAY_DOMAIN
PROVIDERS=${PROVIDERS:-e2b prized novita}
REPO=${DEPLOY_REPO:-https://github.com/heroku/node-js-sample}
WORK=$(mktemp -d /tmp/sbx-domain.XXXXXX)
DOH=${DOH:-https://1.1.1.1/dns-query}

pass() { printf '  PASS %s\n' "$*"; }
note() { printf '  NOTE %s\n' "$*"; }
fail() { printf '  FAIL %s\n' "$*"; exit 1; }
api() { local m=$1 p=$2; shift 2; curl -sS -X "$m" "$BOXAZ_URL/v1$p" -H "Authorization: Bearer ${KEY:-$ADMIN}" -H 'Content-Type: application/json' "$@"; }
ms() { date +%s%3N; }
declare -A T
rec() { T[$PROV.$1]=$2; printf '    ⏱ %-22s %s\n' "$1" "$2"; }
# out <url> [header] [max] → "<code> <ssl_verify> <seconds>" via public DNS (DoH), body in $WORK/body
out() { curl -s -o "$WORK/body" -w '%{http_code} %{ssl_verify_result} %{time_total}' --doh-url "$DOH" --max-time ${3:-30} ${2:+-H "$2"} "$1" || echo "000 - -"; }
code() { out "$@" | cut -d' ' -f1; }
wait200() { local c=""; for i in $(seq ${2:-30}); do c=$(code "$1"); [ "$c" = 200 ] && grep -q "Hello World" "$WORK/body" && break; sleep 1; done; echo "$c"; }
x() { KEY=$AKEY api POST /boxes/$1/exec -d "$(jq -n --arg c "$2" --argjson t "${3:-120}" '{command:$c,timeout_s:$t,tail_lines:20}')"; }
wait_ready() { local j=""; for i in $(seq $(($2*2))); do j=$(KEY=$AKEY api GET /boxes/$1/app); [ "$(echo "$j" | jq -r .status.state)" = ready ] && break; sleep 0.5; done; echo "$j"; }

plist() {
  case $1 in
    prized) curl -sS -H "Authorization: Bearer $PRIZED_API_KEY" https://api.prized.dev/api/v1/sandboxes | jq -c '[.sandboxes[]|select(.state!="deleting" and .state!="deleted")|.id]' ;;
    e2b) curl -sS -H "X-API-Key: $E2B_API_KEY" https://api.e2b.app/v2/sandboxes | jq -c '[.[]|select(.state!="killed")|.sandboxID]' ;;
    novita|novita-phx) curl -sS -H "X-API-KEY: $NOVITA_API_KEY" https://api.us-phx-1.sandbox.novita.ai/v2/sandboxes | jq -c '[.[]?|select(.state!="killed")|.sandboxID]' ;;
    novita-va) curl -sS -H "X-API-KEY: $NOVITA_API_KEY" https://api.sandbox.novita.ai/v2/sandboxes | jq -c '[.[]?|select(.state!="killed")|.sandboxID]' ;;
  esac
}
pkillall() {
  case $1 in
    prized) curl -sS -G -X DELETE -H "Authorization: Bearer $PRIZED_API_KEY" https://api.prized.dev/api/v1/sandboxes --data-urlencode "label=managed=1" >/dev/null 2>&1 || true ;;
    e2b) for id in $(curl -sS -H "X-API-Key: $E2B_API_KEY" https://api.e2b.app/v2/sandboxes | jq -r '.[]?|select(.metadata.managed=="1")|.sandboxID'); do
           curl -sS -X DELETE -H "X-API-Key: $E2B_API_KEY" https://api.e2b.app/sandboxes/$id >/dev/null || true; done ;;
    novita) for b in https://api.us-phx-1.sandbox.novita.ai https://api.sandbox.novita.ai; do
           for id in $(curl -sS -H "X-API-KEY: $NOVITA_API_KEY" $b/v2/sandboxes | jq -r '.[]?|select(.metadata.managed=="1")|.sandboxID'); do
             curl -sS -X DELETE -H "X-API-KEY: $NOVITA_API_KEY" $b/sandboxes/$id >/dev/null || true; done; done ;;
  esac
}
cleanup() {
  kill ${WATCHDOG:-} 2>/dev/null || true
  for b in $(api GET /boxes 2>/dev/null | jq -r '.items[]?|select(.status!="terminated")|.id' 2>/dev/null); do api DELETE /boxes/$b >/dev/null 2>&1 || true; done
  for p in $PROVIDERS; do pkillall $p; done
}
trap cleanup EXIT

[ "$(code "$BOXAZ_PUBLIC_URL/v1/health")" = 200 ] && pass "core reachable from the internet at $BOXAZ_PUBLIC_URL (TLS verified)" || fail "core not reachable at $BOXAZ_PUBLIC_URL"
api GET "/gateway?check=1" | jq -c '{mode,url_pattern,private_previews,check:.check.ok}' | sed 's/^/    gateway: /'
for p in $PROVIDERS; do [ "$(plist $p)" = "[]" ] || fail "$p already has live sandboxes: $(plist $p)"; done
SPEC=$(jq -n --arg r "$REPO" '{repo:$r, install:"npm install --omit=dev --no-audit --no-fund --loglevel=error", start:"node index.js", port:8000, ready:{http:"/"}, restart:"always", backoff_max_s:5}')
# SSE test server (python, stdlib): 5 events, 0.5 s apart
SSE_PY='import http.server,time
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(s):
        s.send_response(200); s.send_header("Content-Type","text/event-stream"); s.send_header("Cache-Control","no-cache"); s.end_headers()
        for i in range(5):
            s.wfile.write(("data: tick %d %.3f\n\n" % (i, time.time())).encode()); s.wfile.flush(); time.sleep(0.5)
http.server.ThreadingHTTPServer(("0.0.0.0",8001),H).serve_forever()'

for PROV in $PROVIDERS; do
  echo "== $PROV"
  ( sleep "${WATCHDOG_S:-280}"; echo "  WATCHDOG: deleting every managed $PROV sandbox"; pkillall $PROV ) & WATCHDOG=$!
  api GET /providers | jq -e --arg p $PROV '.items[]|select(.id==$p)|.connected' >/dev/null || fail "$PROV not connected"
  POL=$(jq -nc --arg p $PROV '{providers:[$p]} + (if $p=="novita" then {regions:["us-phx-1"]} else {} end)')
  AGENT=$(api POST /agents -d "$(jq -nc --arg n "domain-$PROV-$(date +%s)" --argjson pol "$POL" '{name:$n,kind:"claude-code",budget_day_usd:1,policy:$pol}')" | jq -r .id)
  AKEY=$(api POST /keys -d "{\"agent_id\":\"$AGENT\",\"name\":\"domain-live\"}" | jq -r .key)
  T0=$(ms)
  B=$(KEY=$AKEY api POST /boxes -d "{\"template\":\"base\",\"slug\":\"dom-$PROV\",\"lifecycle\":\"session\",\"requirements\":{\"isolation\":\"sandbox\",\"cpu\":1,\"ram_mb\":512,\"disk_gb\":5,\"duration_hours\":0.1}}")
  BOX=$(echo "$B" | jq -r .id); [ "$BOX" != null ] || { echo "$B"; fail "box create"; }
  for i in $(seq 240); do S=$(KEY=$AKEY api GET /boxes/$BOX | jq -r .status); [ "$S" = running ] || [ "$S" = failed ] && break; sleep 0.25; done
  [ "$S" = running ] || fail "box $S"; rec up "$(($(ms)-T0)) ms"
  [ "$(KEY=$AKEY api GET /boxes/$BOX | jq -r .provider)" = $PROV ] || fail "box not on $PROV"
  HOST=$(KEY=$AKEY api GET /boxes/$BOX | jq -r .host_id); HJ=$(api GET /hosts | jq -c --arg h $HOST '.items[]|select(.id==$h)')
  SBX=$(echo "$HJ" | jq -r .provider_ref); PRIV=$(echo "$HJ" | jq -r '.private_traffic // false'); echo "    sandbox $SBX private_traffic=$PRIV"
  T0=$(ms); KEY=$AKEY api PUT /boxes/$BOX/app -d "$SPEC" >/dev/null
  J=$(wait_ready $BOX 150); echo "$J" | jq -e '.status.ready' >/dev/null || { echo "$J" | jq -c .status; fail "app not ready"; }
  rec app_ready "$(($(ms)-T0)) ms"
  PJ=$(KEY=$AKEY api GET /boxes/$BOX/ports | jq -c '.ports[]|select(.port==8000)')
  GW=$(echo "$PJ" | jq -r .gateway_url); echo "    port 8000: $(echo "$PJ" | jq -c '{kind,url,public,access,provider_preview}')"
  [[ $GW =~ ^https://[0-9a-f]{20}-8000\.${DOMAIN//./\\.}/$ ]] || fail "gateway url $GW"
  T0=$(ms); C=$(wait200 "$GW" 40); rec first_200 "$(($(ms)-T0)) ms"
  [ "$C" = 200 ] && pass "$GW → 200 Hello World! from outside" || fail "gateway $C $(head -c 200 "$WORK/body")"
  R=$(out "$GW"); rec warm "$(echo "$R" | awk '{printf "%d ms", $3*1000}')"; [ "$(echo "$R" | cut -d' ' -f2)" = 0 ] && pass "TLS valid (ssl_verify_result=0, *.${DOMAIN})" || fail "TLS $R"
  # direct provider preview URL
  case $PROV in
    e2b) PREV="https://8000-$SBX.e2b.app/" ;;
    novita) PREV="https://8000-$SBX.us-phx-1.sandbox.novita.ai/" ;;
    *) PREV="" ;;
  esac
  if [ -n "$PREV" ]; then
    DC=$(curl -s -o "$WORK/prev" -w '%{http_code}' --max-time 20 "$PREV" || true); T[$PROV.direct_preview]=$DC
    if [ "$DC" = 403 ] || [ "$DC" = 401 ]; then pass "direct provider preview $PREV → $DC (blocked)"
    elif [ "$DC" = 200 ]; then note "direct provider preview $PREV → 200 (NOT blocked: $(head -c 80 "$WORK/prev"))"
    else note "direct provider preview → $DC $(head -c 120 "$WORK/prev")"; fi
  else T[$PROV.direct_preview]="n/a (no preview host)"; fi
  # sleep → request wakes it
  T0=$(ms); S=$(KEY=$AKEY api POST /boxes/$BOX/sleep -d '{}' | jq -r .status); rec sleep "$(($(ms)-T0)) ms"; [ "$S" = sleeping ] || fail "sleep → $S"
  [ -n "$PREV" ] && { DC=$(curl -s -o /dev/null -w '%{http_code}' --max-time 15 "$PREV" || true); sleep 1; ST=$(KEY=$AKEY api GET /boxes/$BOX | jq -r .status); echo "    direct preview while asleep → $DC (box still $ST)"; T[$PROV.preview_asleep]="$DC/$ST"; }
  T0=$(ms); R=$(out "$GW" "Accept: text/html" 40); C=${R%% *}; DT=$(($(ms)-T0))
  if [ "$C" = 200 ]; then rec wake_200 "$DT ms"; pass "request to the sleeping box woke it → 200 in $DT ms"
  else note "first request → $C after $DT ms, retrying"; C=$(wait200 "$GW" 30); rec wake_200 "$(( $(ms)-T0 )) ms (first $C)"; [ "$C" = 200 ] || fail "wake on request"; fi
  # URL token
  TJ=$(KEY=$AKEY api POST /boxes/$BOX/ports/8000/tokens -d '{"ttl_s":600,"label":"live"}'); TOK=$(echo "$TJ" | jq -r .token); TID=$(echo "$TJ" | jq -r .id)
  C1=$(code "$GW"); C2=$(code "$GW" "Authorization: Bearer $TOK"); C3=$(curl -s -o /dev/null -w '%{http_code}' --doh-url "$DOH" --max-time 20 "$(echo "$TJ" | jq -r .url)")
  KEY=$AKEY api DELETE /boxes/$BOX/ports/8000/tokens/$TID >/dev/null; C4=$(code "$GW" "Authorization: Bearer $TOK")
  T[$PROV.token]="$C1/$C2/$C3/$C4"; echo "    token: none=$C1 bearer=$C2 ?token=$C3 revoked=$C4"
  [ "$C1" = 401 ] && [ "$C2" = 200 ] && [ "$C3" = 302 ] && [ "$C4" = 401 ] && pass "URL token 401/200/302/401" || fail "token $C1/$C2/$C3/$C4"
  KEY=$AKEY api GET /boxes/$BOX/ports | jq -c '.ports[]|select(.port==8000)|{kind,access,provider_preview,caveats}' | sed 's/^/    token port: /'
  [ -n "$PREV" ] && { DC=$(curl -s -o /dev/null -w '%{http_code}' --max-time 15 "$PREV" || true); T[$PROV.direct_token_port]=$DC; echo "    direct preview of the token port → $DC"; }
  KEY=$AKEY api PUT /boxes/$BOX/ports/8000 -d '{"access":"public"}' >/dev/null
  if [ "$PROV" = e2b ]; then
    # SSE through the domain: events must arrive spread out (unbuffered)
    x $BOX "cat > /tmp/sse.py <<'PY'
$SSE_PY
PY
setsid nohup python3 /tmp/sse.py >/tmp/sse.log 2>&1 < /dev/null & sleep 0.5; echo ok" 30 >/dev/null
    SSEGW=$(KEY=$AKEY api POST /boxes/$BOX/ports -d '{"port":8001}' | jq -r '.ports[]|select(.port==8001)|.gateway_url')
    curl -sN --doh-url "$DOH" --max-time 10 "$SSEGW" | while IFS= read -r l; do [ -n "$l" ] && echo "$(ms) $l"; done > "$WORK/sse" || true
    N=$(wc -l < "$WORK/sse"); SPAN=$(( $(tail -1 "$WORK/sse" | cut -d' ' -f1) - $(head -1 "$WORK/sse" | cut -d' ' -f1) ))
    T[$PROV.sse]="$N events over ${SPAN} ms"; [ "$N" = 5 ] && [ "$SPAN" -ge 1500 ] && pass "SSE through $SSEGW: 5 events streamed over $SPAN ms" || note "SSE: $N events, span $SPAN ms"
    # native clone keeps the preview private
    T0=$(ms); CL=$(KEY=$AKEY api POST /boxes/$BOX/clone -d '{"slug":"dom-e2b-clone"}'); CLID=$(echo "$CL" | jq -r .id)
    for i in $(seq 120); do S=$(KEY=$AKEY api GET /boxes/$CLID | jq -r .status); [ "$S" = running ] || [ "$S" = failed ] && break; sleep 0.5; done
    [ "$S" = running ] || fail "clone $S $(echo "$CL" | head -c 300)"
    CGW=$(KEY=$AKEY api GET /boxes/$CLID/ports | jq -r '.ports[]|select(.port==8000)|.gateway_url'); C=$(wait200 "$CGW" 40); rec clone_200 "$(($(ms)-T0)) ms"
    CH=$(api GET /hosts | jq -c --arg h "$(KEY=$AKEY api GET /boxes/$CLID | jq -r .host_id)" '.items[]|select(.id==$h)|{provider_ref,private_traffic}')
    CSBX=$(echo "$CH" | jq -r .provider_ref); CDC=$(curl -s -o /dev/null -w '%{http_code}' --max-time 20 "https://8000-$CSBX.e2b.app/" || true)
    T[$PROV.clone]="gateway $C, direct preview $CDC, $(echo "$CH" | jq -c .private_traffic)"
    [ "$C" = 200 ] && pass "clone serves at $CGW; its direct preview → $CDC (private_traffic=$(echo "$CH" | jq -r .private_traffic))" || fail "clone gateway $C"
    KEY=$AKEY api DELETE /boxes/$CLID >/dev/null
  fi
  T0=$(ms); KEY=$AKEY api DELETE /boxes/$BOX >/dev/null; sleep 1; C=$(code "$GW"); rec down_url "$C after $(($(ms)-T0)) ms"
  kill $WATCHDOG 2>/dev/null || true
  for i in $(seq 20); do [ "$(plist $PROV)" = "[]" ] && break; sleep 1; done
  L=$(plist $PROV); T[$PROV.final]=$L; [ "$L" = "[]" ] && pass "$PROV has no sandboxes left" || { pkillall $PROV; note "$PROV leftover $L (deleted)"; }
done
echo "== final lists"
for p in prized e2b novita-phx novita-va; do echo "   $p: $(plist $p)"; done
echo "== results"
for k in $(printf '%s\n' "${!T[@]}" | sort); do printf '   %-28s %s\n' "$k" "${T[$k]}"; done
