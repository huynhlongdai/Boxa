#!/usr/bin/env bash
# Live test v1.11 (box access P1) on the real domain behind the Cloudflare
# Tunnel (deploy/cloudflare-tunnel/up.sh). Per provider (default: e2b prized),
# one sandbox at a time:
#   box sftp put/get + OpenSSH sftp/scp through <name>.<domain> → OpenSSH with
#   a short-lived certificate (box ssh-proxy --anon, no API key) → revoke →
#   CDP: box browser (first start incl. install, then warm) → Browser.getVersion
#   + navigate https://example.com + title + metadata blocked → desktop: RFB
#   handshake + framebuffer update → share link 200 → revoke → 410 → down →
#   provider list [].
#
#   ENV_FILE=…/gateway.env [PROVIDERS="e2b prized"] scripts/live-access-p1.sh
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
WORK=$(mktemp -d /tmp/sbx-access-p1.XXXXXX); mkdir -p "$WORK/home"
BIN=$(pwd)/bin
WSC="python3 $(pwd)/scripts/wsclient.py"
PROBE="python3 $(pwd)/scripts/accessprobe.py"

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
  kill ${WATCHDOG:-} 2>/dev/null || true
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
  AGENT=$(api POST /agents -d "$(jq -nc --arg n "p1-$PROV-$(date +%s)" --arg p "$PROV" '{name:$n,kind:"claude-code",budget_day_usd:1,policy:{providers:[$p]}}')" | jq -r .id)
  AKEY=$(api POST /keys -d "{\"agent_id\":\"$AGENT\",\"name\":\"p1-live\"}" | jq -r .key)
  export BOXAZ_URL=$BOXAZ_PUBLIC_URL BOXAZ_API_KEY=$AKEY
  ( sleep "${WATCHDOG_S:-340}"; echo "  WATCHDOG: deleting every managed $PROV sandbox"; pkillall $PROV ) & WATCHDOG=$!
  T0=$(ms)
  B=$(KEY=$AKEY api POST /boxes -d "{\"template\":\"base\",\"slug\":\"p1-$PROV\",\"lifecycle\":\"session\",\"requirements\":{\"isolation\":\"sandbox\",\"cpu\":${CPU:-1},\"ram_mb\":${RAM_MB:-512},\"disk_gb\":5,\"duration_hours\":0.1}}")
  BOX=$(echo "$B" | jq -r .id)
  for i in $(seq 240); do S=$(KEY=$AKEY api GET /boxes/$BOX | jq -r .status); [ "$S" = running ] || [ "$S" = failed ] && break; sleep 0.25; done
  if [ "$S" != running ]; then note "$PROV box $S: $(echo "$B" | jq -c .error 2>/dev/null)"; kill $WATCHDOG; continue; fi
  NAME=$(KEY=$AKEY api GET /boxes/$BOX | jq -r .subdomain)
  echo "    box up in $(($(ms)-T0)) ms: $BOX name $NAME provider $(KEY=$AKEY api GET /boxes/$BOX | jq -r .provider)"
  KEY=$AKEY api POST /boxes/$BOX/exec -d '{"command":"id; uname -m; . /etc/os-release 2>/dev/null; echo $PRETTY_NAME; free -m | sed -n 2p; for b in sftp-server node npx apt-get sudo chromium Xvfb x11vnc; do printf \"%s=%s \" $b $(command -v $b >/dev/null && echo y || echo n); done; ls /usr/lib/openssh/sftp-server 2>/dev/null","timeout_s":30}' | jq -r .stdout_tail | sed 's/^/    env: /'

  # 1. SFTP (box sftp through the box host, then OpenSSH sftp/scp via ProxyCommand)
  head -c 1000000 /dev/urandom > "$WORK/up.bin"
  T0=$(ms); O=$("$BIN/box" sftp "$BOX" put "$WORK/up.bin" /tmp/p1.bin 2>&1); T1=$(ms)
  "$BIN/box" sftp "$BOX" get /tmp/p1.bin "$WORK/down.bin" >/dev/null 2>&1; T2=$(ms)
  cmp -s "$WORK/up.bin" "$WORK/down.bin" && pass "box sftp put 1 MB in $((T1-T0)) ms, get in $((T2-T1)) ms (byte-identical)" || note "sftp: $O"
  if command -v sftp >/dev/null; then
    "$BIN/box" ssh-config "$NAME" >/dev/null 2>&1; HOME=$WORK/home "$BIN/box" ssh-config >/dev/null 2>&1
    KH=$WORK/home/.boxaz/known_hosts
    SSHO=(-o "ProxyCommand=$BIN/box ssh-proxy %h" -o HostKeyAlias=boxaz-ssh -o "UserKnownHostsFile=$KH" -o BatchMode=yes -o LogLevel=ERROR)
    echo "put $WORK/up.bin /tmp/p1-openssh.bin" > "$WORK/b"
    T0=$(ms); HOME=$WORK/home sftp "${SSHO[@]}" -b "$WORK/b" "boxaz@$NAME.$DOMAIN" >/dev/null 2>"$WORK/sftp.err"; T1=$(ms)
    HOME=$WORK/home scp "${SSHO[@]}" -q "boxaz@$NAME.$DOMAIN:/tmp/p1-openssh.bin" "$WORK/scp.bin" 2>>"$WORK/sftp.err"; T2=$(ms)
    cmp -s "$WORK/up.bin" "$WORK/scp.bin" && pass "OpenSSH sftp put ($((T1-T0)) ms) + scp get ($((T2-T1)) ms) through $NAME.$DOMAIN" || note "openssh sftp/scp: $(tail -3 "$WORK/sftp.err")"
    # 2. short-lived certificate, no API key in the ssh process
    HOME=$WORK/home "$BIN/box" ssh-cert "$BOX" --ttl 15m --json > "$WORK/cert.json" 2>&1
    CID=$(jq -r .id "$WORK/cert.json"); CUSER=$(jq -r .user "$WORK/cert.json")
    CERTO=(-i "$WORK/home/.boxaz/id_ed25519" -o "CertificateFile=$WORK/home/.boxaz/id_ed25519-cert.pub" -o IdentitiesOnly=yes -o "ProxyCommand=env -u BOXAZ_API_KEY $BIN/box ssh-proxy --anon %h" -o HostKeyAlias=boxaz-ssh -o "UserKnownHostsFile=$KH" -o BatchMode=yes -o LogLevel=ERROR)
    T0=$(ms); O=$(HOME=$WORK/home ssh "${CERTO[@]}" "$CUSER@$NAME.$DOMAIN" 'echo cert-$((6*7)); uname -sm' </dev/null 2>&1)
    [[ $O == *cert-42* ]] && pass "OpenSSH with a 15 min certificate through wss://$NAME.$DOMAIN/.boxaz/ssh (no ticket, no API key) in $(($(ms)-T0)) ms: $(echo "$O" | tail -1)" || note "cert ssh: $O"
    KEY=$AKEY api DELETE "/boxes/$BOX/tickets/$CID" >/dev/null
    if HOME=$WORK/home ssh "${CERTO[@]}" "$CUSER@$NAME.$DOMAIN" true </dev/null >/dev/null 2>&1; then note "revoked certificate still accepted"; else pass "revoked certificate refused"; fi
  fi

  # 3. CDP browser
  T0=$(ms); "$BIN/box" browser "$BOX" --ttl 10m --json > "$WORK/br.json" 2>&1; T1=$(ms)
  CDP=$(jq -r .cdp_url "$WORK/br.json" 2>/dev/null)
  if [[ $CDP == wss://* ]]; then
    pass "box browser (first start: $(jq -r '.source' "$WORK/br.json"), $(jq -r '.version.Browser' "$WORK/br.json"), headless=$(jq -r .headless "$WORK/br.json")) in $((T1-T0)) ms; endpoint ${CDP%%ticket=*}ticket=bxa_…"
    CDPH=$(jq -r .http_url "$WORK/br.json")
    WSD=$(curl -sS --max-time 30 "${CDPH%%\?*}/json/version/?${CDPH#*\?}" | jq -r .webSocketDebuggerUrl)
    [[ $WSD == wss://$NAME.$DOMAIN/.boxaz/cdp/devtools/browser/* ]] && pass "/json/version via https://$NAME.$DOMAIN rewritten to wss://$NAME.$DOMAIN/.boxaz/cdp/devtools/browser/…" || note "json/version: $WSD"
    O=$($PROBE cdp "$CDP" --navigate https://example.com/ --expect-blocked http://169.254.169.254/latest/meta-data/ 2>&1)
    if echo "$O" | jq -e '.title == "Example Domain" and .metadata_blocked' >/dev/null 2>&1; then pass "CDP Browser.getVersion ($(echo "$O" | jq -r .getversion_ms) ms) + navigate example.com → title \"$(echo "$O" | jq -r .title)\" ($(echo "$O" | jq -r .navigate_ms) ms); metadata blocked: $(echo "$O" | jq -c .blocked_nav)"
    else note "cdp: $O"; KEY=$AKEY api POST /boxes/$BOX/exec -d '{"command":"free -m | sed -n 2p; ps -eo rss,args | grep -i chrom | head -5 | cut -c1-150; tail -5 ${TMPDIR:-/tmp}/.bx-access/browser.log; sudo -n dmesg 2>/dev/null | grep -i -E \"oom|killed\" | tail -3","timeout_s":30}' | jq -r .stdout_tail | sed 's/^/    diag: /'; fi
    T0=$(ms); "$BIN/box" browser "$BOX" --json > "$WORK/br2.json" 2>&1; pass "box browser warm (already running) in $(($(ms)-T0)) ms"
  else note "browser: $(head -c 600 "$WORK/br.json")"; fi

  # 4. desktop + share link
  T0=$(ms); "$BIN/box" desktop "$BOX" --json > "$WORK/dt.json" 2>&1; T1=$(ms)
  VNC=$(jq -r .vnc_url "$WORK/dt.json" 2>/dev/null)
  if [[ $VNC == wss://* ]]; then
    pass "box desktop first start $(jq -r '"\(.width)x\(.height) \(.vnc_server) wm=\(.wm)"' "$WORK/dt.json") in $((T1-T0)) ms"
    O=$($PROBE vnc "$VNC" 2>&1)
    echo "$O" | jq -e '.version | startswith("RFB 003.00")' >/dev/null && pass "VNC through wss://$NAME.$DOMAIN/.boxaz/vnc: $(echo "$O" | jq -r .version) (handshake $(echo "$O" | jq -r .handshake_ms) ms) + framebuffer update $(echo "$O" | jq -c '{width,height,update_rects,update_pixels,update_ms}')" || note "vnc: $O"
    T0=$(ms); "$BIN/box" desktop "$BOX" --json > "$WORK/dt2.json" 2>&1; pass "box desktop warm in $(($(ms)-T0)) ms (browser on desktop: $(jq -r .browser_on_desktop "$WORK/dt2.json"))"
    "$BIN/box" desktop "$BOX" --share --ttl 5m --json > "$WORK/sh.json"
    SURL=$(jq -r .url "$WORK/sh.json"); SSOCK=$(echo "$SURL" | sed 's#^https://#wss://#; s#/.boxaz/desktop?#/.boxaz/vnc?#')
    [[ $SURL == https://$NAME.$DOMAIN/.boxaz/desktop?share=bxs_* ]] && pass "share link https://$NAME.$DOMAIN/.boxaz/desktop?share=bxs_… (view_only=$(jq -r .view_only "$WORK/sh.json"))" || note "share url $SURL"
    [ "$($PROBE http "$SURL" | jq -r .status)" = 200 ] && pass "share page 200" || note "share page $($PROBE http "$SURL")"
    O=$($PROBE vnc "$SSOCK" --input 2>&1); echo "$O" | jq -e .ok >/dev/null && pass "share viewer RFB + framebuffer update (view-only)" || note "share vnc: $O"
    T0=$(ms); "$BIN/box" desktop "$BOX" --revoke-shares >/dev/null
    S1=$($PROBE http "$SURL" | jq -r .status); S2=$($WSC "$SSOCK" --status --timeout 30)
    [ "$S1" = 410 ] && [ "$S2" = 410 ] && pass "after revoke: page $S1, socket $S2 ($(($(ms)-T0)) ms)" || note "after revoke: page $S1 socket $S2"
  else note "desktop: $(head -c 600 "$WORK/dt.json")"; fi
  "$BIN/box" access "$BOX" | sed -n '1,2p' | sed 's/^/    /'

  # 5. down
  T0=$(ms); KEY=$AKEY api DELETE /boxes/$BOX >/dev/null
  for i in $(seq 30); do [ "$(plist $PROV)" = "[]" ] && break; sleep 1; done
  L=$(plist $PROV); [ "$L" = "[]" ] && pass "$PROV list [] after down ($(($(ms)-T0)) ms)" || { pkillall $PROV; note "$PROV leftover $L (deleted)"; }
  kill $WATCHDOG 2>/dev/null
done
echo "== done"
