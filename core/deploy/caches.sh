#!/usr/bin/env bash
# Boxmes v1.3 — shared package caches on a host, used by every box on it.
#   apt-cacher-ng :3142 · devpi (pip) :3141 · verdaccio (npm) :4873 · athens (Go) :3000
# Bind address = the Incus bridge (default 10.10.0.1 / incusbr0) so only boxes reach them.
# Then set BOXD_CACHE_HOST=<bridge ip> in /etc/boxd.env and restart boxd: new boxes get
# PIP_INDEX_URL, UV_INDEX_URL, npm_config_registry, GOPROXY and an apt proxy file.
# NOTE: written for Ubuntu 24.04; not exercised in Boxmes CI (no Incus there).
set -euo pipefail
BR_IP="${BR_IP:-$(ip -4 addr show incusbr0 2>/dev/null | awk '/inet /{print $2}' | cut -d/ -f1)}"
BR_IP="${BR_IP:-10.10.0.1}"
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq apt-cacher-ng python3-venv nodejs npm curl >/dev/null
sed -i "s/^# *BindAddress:.*/BindAddress: ${BR_IP}/" /etc/apt-cacher-ng/acng.conf || true
systemctl restart apt-cacher-ng

python3 -m venv /opt/devpi && /opt/devpi/bin/pip install -q devpi-server devpi-web
[ -d /var/lib/devpi ] || /opt/devpi/bin/devpi-init --serverdir /var/lib/devpi
npm install -g --silent verdaccio
ARCH=$(dpkg --print-architecture)
curl -fsSL "https://github.com/gomods/athens/releases/download/v0.15.4/athens_0.15.4_linux_${ARCH}.tar.gz" | tar -xz -C /usr/local/bin athens

unit() { # name, exec
cat >/etc/systemd/system/boxmes-cache-$1.service <<UNIT
[Unit]
Description=Boxmes shared cache: $1
After=network-online.target
[Service]
ExecStart=$2
Restart=always
DynamicUser=yes
StateDirectory=boxmes-cache-$1
[Install]
WantedBy=multi-user.target
UNIT
}
unit pip "/opt/devpi/bin/devpi-server --serverdir /var/lib/devpi --host ${BR_IP} --port 3141"
unit npm "/usr/bin/env verdaccio --listen ${BR_IP}:4873"
unit go  "/usr/local/bin/athens -port ${BR_IP}:3000"
systemctl daemon-reload
systemctl enable --now boxmes-cache-pip boxmes-cache-npm boxmes-cache-go
grep -q '^BOXD_CACHE_HOST=' /etc/boxd.env 2>/dev/null || echo "BOXD_CACHE_HOST=${BR_IP}" >> /etc/boxd.env
systemctl restart boxd || true
echo "caches ready on ${BR_IP} (apt 3142, pip 3141, npm 4873, go 3000)"
