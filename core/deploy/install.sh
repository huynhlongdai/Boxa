#!/usr/bin/env bash
# Boxa single-host install (Hetzner Cloud, Ubuntu 24.04): core + boxd + Incus + Caddy
# on the same machine. Run as root from an unpacked dist directory:
#
#   make dist && scp -r dist/ deploy/ root@<ip>:/root/boxa/
#   ssh root@<ip> 'cd /root/boxa && bash deploy/install.sh'
#
# Optional env: BASE_DOMAIN=example.com  ACME_EMAIL=you@example.com
#               HOST_PRICE_MONTH=4.59 (what you pay for this server; used for cost allocation)
set -euo pipefail
[ "$(id -u)" = 0 ] || { echo "run as root"; exit 1; }
HERE=$(cd "$(dirname "$0")/.." && pwd)
ARCH=$(dpkg --print-architecture)   # amd64 | arm64
DIST=${DIST:-$HERE/dist}

echo "== packages"
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq incus caddy curl jq ca-certificates >/dev/null

echo "== incus"
if ! incus profile show default 2>/dev/null | grep -q "root:"; then
  incus admin init --minimal
fi

echo "== binaries ($ARCH)"
for b in boxa-core boxd box; do
  src="$DIST/$b-linux-$ARCH"
  [ -f "$src" ] || src="$HERE/bin/$b"
  [ -f "$src" ] || { echo "missing $b binary (run 'make dist' and copy dist/)"; exit 1; }
  install -m 0755 "$src" /usr/local/bin/$b
done
id boxa >/dev/null 2>&1 || useradd --system --home /var/lib/boxa --shell /usr/sbin/nologin boxa
install -d -o boxa -g boxa -m 0700 /var/lib/boxa /var/lib/boxa/dist
install -d -m 0755 /etc/boxa /var/lib/boxd /var/lib/boxa/memory
# boxd binaries for future burst hosts (served at /dl/boxd-linux-<arch>)
cp -f "$DIST"/boxd-linux-* /var/lib/boxa/dist/ 2>/dev/null || true
chown -R boxa:boxa /var/lib/boxa/dist

echo "== config"
IP=$(curl -4 -fsS --max-time 5 https://api.ipify.org || hostname -I | awk '{print $1}')
ZONE=$(curl -fsS --max-time 2 http://169.254.169.254/hetzner/v1/metadata/availability-zone 2>/dev/null | cut -d- -f1 || true)
if [ -n "${BASE_DOMAIN:-}" ]; then CORE_HOST="core.$BASE_DOMAIN"; else CORE_HOST="core-${IP//./-}.sslip.io"; fi
if [ ! -f /etc/boxa/core.env ]; then
  MASTER=$(head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')
  BOOT="bxb_$(head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n')"
  ADMIN="bx_admin_$(head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n')"
  cat >/etc/boxa/core.env <<EOF
BOXA_ADDR=127.0.0.1:8080
BOXA_DATA_DIR=/var/lib/boxa
BOXA_PUBLIC_URL=https://$CORE_HOST
BASE_DOMAIN=${BASE_DOMAIN:-}
BOXA_MASTER_KEY=$MASTER
BOXA_ADMIN_KEY=$ADMIN
BOXA_BOOTSTRAP_TOKEN=$BOOT
BOXA_DIST_DIR=/var/lib/boxa/dist
BOXA_LOCAL_PRICE_MONTH=${HOST_PRICE_MONTH:-0}
BOXA_AUTO_BURST=false
# DATABASE_URL=postgres://boxa:pass@127.0.0.1:5432/boxa   (default: SQLite in BOXA_DATA_DIR)
EOF
  chmod 600 /etc/boxa/core.env
  cat >/etc/boxd.env <<EOF
BOXD_CORE_URL=http://127.0.0.1:8080
BOXD_TOKEN=$BOOT
BOXD_DRIVER=incus
BOXD_PROVIDER=local
BOXD_REGION=${ZONE:-hetzner}
BOXD_IP=$IP
BOXD_STATE_DIR=/var/lib/boxd
BOXD_MEMORY_ROOT=/var/lib/boxa/memory
EOF
  chmod 600 /etc/boxd.env
  NEW_ADMIN=$ADMIN
fi

echo "== caddy"
sed -e "s|{\$BOXA_CORE_HOST}|$CORE_HOST|g" -e "s|{\$ACME_EMAIL}|${ACME_EMAIL:-admin@$CORE_HOST}|g" \
    -e "s|{\$BOXA_WEB_PORT:3000}|${BOXA_WEB_PORT:-3000}|g" "$HERE/deploy/Caddyfile" >/etc/caddy/Caddyfile
caddy validate --config /etc/caddy/Caddyfile >/dev/null

echo "== services"
install -m 0644 "$HERE/deploy/systemd/boxa-core.service" /etc/systemd/system/
install -m 0644 "$HERE/deploy/systemd/boxd.service" /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now boxa-core
sleep 2
systemctl enable --now boxd
systemctl reload caddy || systemctl restart caddy

for i in $(seq 30); do curl -fsS http://127.0.0.1:8080/v1/health >/dev/null 2>&1 && break; sleep 1; done
echo
echo "Boxa is up:"
echo "  API      https://$CORE_HOST/v1   (MCP: https://$CORE_HOST/mcp)"
echo "  Health   $(curl -fsS http://127.0.0.1:8080/v1/health)"
[ -n "${NEW_ADMIN:-}" ] && echo "  ADMIN KEY (also in /etc/boxa/core.env): $NEW_ADMIN"
echo "  Firewall: allow only 22, 80, 443 inbound. boxd talks to core on localhost."
