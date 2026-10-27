#!/usr/bin/env bash
# Boxaz gateway on your own domain through a remotely-managed Cloudflare
# Tunnel. Idempotent: re-running reuses the tunnel and records by name.
#
#   CF_ACCOUNT_ID=… CF_ZONE_ID=… GATEWAY_DOMAIN=example.com \
#   CF_API_TOKEN=… ./setup.sh
#
# Creates / updates:
#   - tunnel  $TUNNEL_NAME (config_src=cloudflare, i.e. ingress lives in the API)
#   - ingress gw.<domain> and *.<domain> → $CORE_URL, everything else 404
#   - DNS     CNAME gw.<domain> and *.<domain> → <tunnel-id>.cfargotunnel.com
#             (proxied). Explicit records (api., www., …) keep priority over
#             the wildcard, so other services on the zone are untouched.
#   - token   written to $TOKEN_FILE (mode 600) for run.sh / cloudflared.
# Never touches other tunnels or records; refuses to repoint a record that
# targets something else unless FORCE=1.
#
# Token scopes: Account → Cloudflare Tunnel: Edit; Zone → DNS: Edit (zone).
# CF_API_TOKEN may be empty when a proxy injects the Authorization header.
set -euo pipefail
: "${CF_ACCOUNT_ID:?set CF_ACCOUNT_ID}" "${CF_ZONE_ID:?set CF_ZONE_ID}" "${GATEWAY_DOMAIN:?set GATEWAY_DOMAIN (e.g. example.com)}"
TUNNEL_NAME=${TUNNEL_NAME:-boxaz-gw}
PUBLIC_HOST=${PUBLIC_HOST:-gw.$GATEWAY_DOMAIN}
CORE_URL=${CORE_URL:-http://localhost:8080}
TOKEN_FILE=${TOKEN_FILE:-$HOME/.config/boxaz/tunnel-token}
CF_API=${CF_API:-https://api.cloudflare.com/client/v4}
FORCE=${FORCE:-0}

cf() { # cf METHOD PATH [JSON] — retries Cloudflare rate limits (code 971 / 429)
  local m=$1 p=$2 body=${3:-} out i auth=()
  [ -n "${CF_API_TOKEN:-}" ] && [ "${CF_API_TOKEN}" != proxy ] && auth=(-H "Authorization: Bearer $CF_API_TOKEN")
  for i in 1 2 3 4 5 6; do
    if [ -n "$body" ]; then
      out=$(curl -sS -X "$m" "${auth[@]}" -H 'Content-Type: application/json' --data "$body" "$CF_API$p")
    else
      out=$(curl -sS -X "$m" "${auth[@]}" "$CF_API$p")
    fi
    if echo "$out" | jq -e '.success == true' >/dev/null 2>&1; then echo "$out"; return 0; fi
    if echo "$out" | jq -e '[.errors[]?.code] | any(. == 971 or . == 10429)' >/dev/null 2>&1; then sleep $((i * 10)); continue; fi
    echo "cloudflare $m $p failed: $(echo "$out" | jq -c '.errors' 2>/dev/null || echo "$out")" >&2; return 1
  done
  echo "cloudflare $m $p: still rate limited" >&2; return 1
}

echo "== tunnel $TUNNEL_NAME"
TID=$(cf GET "/accounts/$CF_ACCOUNT_ID/cfd_tunnel?is_deleted=false&name=$TUNNEL_NAME" | jq -r '.result[0].id // empty')
if [ -z "$TID" ]; then
  TID=$(cf POST "/accounts/$CF_ACCOUNT_ID/cfd_tunnel" "$(jq -nc --arg n "$TUNNEL_NAME" '{name:$n, config_src:"cloudflare"}')" | jq -r .result.id)
  echo "   created $TID"
else
  echo "   exists  $TID"
fi

echo "== ingress $PUBLIC_HOST, *.$GATEWAY_DOMAIN → $CORE_URL"
CFG=$(jq -nc --arg gw "$PUBLIC_HOST" --arg wc "*.$GATEWAY_DOMAIN" --arg svc "$CORE_URL" \
  '{config:{ingress:[{hostname:$gw, service:$svc, originRequest:{}}, {hostname:$wc, service:$svc, originRequest:{}}, {service:"http_status:404"}]}}')
cf PUT "/accounts/$CF_ACCOUNT_ID/cfd_tunnel/$TID/configurations" "$CFG" >/dev/null

TARGET="$TID.cfargotunnel.com"
upsert() { # upsert NAME
  local name=$1 rec id content
  rec=$(cf GET "/zones/$CF_ZONE_ID/dns_records?name=$(printf %s "$name" | jq -sRr @uri)&per_page=50")
  id=$(echo "$rec" | jq -r '.result[0].id // empty'); content=$(echo "$rec" | jq -r '.result[0].content // empty')
  local body; body=$(jq -nc --arg n "$name" --arg c "$TARGET" '{type:"CNAME", name:$n, content:$c, proxied:true, ttl:1, comment:"gateway tunnel"}')
  if [ -z "$id" ]; then
    cf POST "/zones/$CF_ZONE_ID/dns_records" "$body" >/dev/null; echo "   $name → $TARGET (created)"
  elif [ "$content" = "$TARGET" ]; then
    echo "   $name → $TARGET (ok)"
  elif [ "$FORCE" = 1 ]; then
    cf PUT "/zones/$CF_ZONE_ID/dns_records/$id" "$body" >/dev/null; echo "   $name → $TARGET (repointed from $content)"
  else
    echo "   $name points to $content — not touching it (FORCE=1 repoints)" >&2; return 1
  fi
}
echo "== DNS"
upsert "$PUBLIC_HOST"
upsert "*.$GATEWAY_DOMAIN"

echo "== token → $TOKEN_FILE"
mkdir -p "$(dirname "$TOKEN_FILE")"; umask 077
cf GET "/accounts/$CF_ACCOUNT_ID/cfd_tunnel/$TID/token" | jq -r .result > "$TOKEN_FILE"
chmod 600 "$TOKEN_FILE"

cat <<MSG

Tunnel id: $TID
Core env:  BOXAZ_PUBLIC_URL=https://$PUBLIC_HOST  BOXAZ_GATEWAY_DOMAIN=$GATEWAY_DOMAIN
Run:       TOKEN_FILE=$TOKEN_FILE $(dirname "$0")/run.sh
MSG
