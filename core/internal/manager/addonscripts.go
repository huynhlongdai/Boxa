package manager

// v1.11 in-box scripts for the on-demand browser (CDP) and desktop (VNC).
// They run over proc.open with /bin/sh (POSIX; bash not required), keep their
// state in $TMPDIR/.bx-access (pid files, logs, the desktop's DISPLAY), bind
// every service to 127.0.0.1 only and detach with setsid so closing the
// proc.open stream does not kill them. Output is key=value lines.
//
// Environment passed by core: PORT (CDP port), VPORT (VNC port), DISP (X
// display number), W/H (screen), HEADLESS (1/0), INSTALL (auto/off).

const addonCommon = `
D="${TMPDIR:-/tmp}/.bx-access"; mkdir -p "$D" 2>/dev/null; chmod 700 "$D" 2>/dev/null
SUDO=""; if [ "$(id -u)" != 0 ]; then if command -v sudo >/dev/null 2>&1 && sudo -n true 2>/dev/null; then SUDO="sudo -n"; else SUDO="NO"; fi; fi
alive() { [ -f "$1" ] && kill -0 "$(cat "$1" 2>/dev/null)" 2>/dev/null; }
listening() { # port is listening on loopback/any (/proc/net/tcp[6], LISTEN = 0A)
  hp=$(printf '%04X' "$1"); grep -qi ":$hp [0-9A-F:]* 0A " /proc/net/tcp /proc/net/tcp6 2>/dev/null
}
detach() { # run "$@" in its own session, output to $LOG
  if command -v setsid >/dev/null 2>&1; then setsid "$@" >>"$LOG" 2>&1 </dev/null &
  elif command -v python3 >/dev/null 2>&1; then python3 -c 'import os,sys; os.setsid(); os.execvp(sys.argv[1], sys.argv[1:])' "$@" >>"$LOG" 2>&1 </dev/null &
  else nohup "$@" >>"$LOG" 2>&1 </dev/null & fi
  echo $!
}
is_debian() { [ -f /etc/debian_version ] && ! grep -qi ubuntu /etc/os-release 2>/dev/null; }
can_apt() { command -v apt-get >/dev/null 2>&1 && [ "$SUDO" != NO ]; }
apt_install() { # quiet, no recommends, retries the index once
  $SUDO env DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends "$@" >>"$D/install.log" 2>&1 && return 0
  $SUDO env DEBIAN_FRONTEND=noninteractive apt-get update -qq >>"$D/install.log" 2>&1
  $SUDO env DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends "$@" >>"$D/install.log" 2>&1
}
`

// browserStart finds (or installs) Chromium/Chrome and starts it with CDP on
// 127.0.0.1:$PORT. Prints browser=<path> pid=<pid> headless=<0|1> source=<...>
// or error=<reason>.
const browserStart = addonCommon + `
PORT="${PORT:-9222}"; LOG="$D/browser.log"
if listening "$PORT"; then echo "state=running"; if alive "$D/browser.pid"; then echo "pid=$(cat "$D/browser.pid")"; echo "headless=$(cat "$D/browser.headless" 2>/dev/null || echo 1)"; else echo "source=existing"; fi; exit 0; fi
not_snap() { r=$(readlink -f "$1" 2>/dev/null || echo "$1"); case "$r" in /snap/*) return 1;; esac; if head -c 4096 "$r" 2>/dev/null | grep -q 'snap'; then return 1; fi; return 0; }
find_browser() {
  if [ "$SUDO" = NO ] && [ "${HEADLESS:-1}" = 1 ]; then for p in "$HOME"/.cache/ms-playwright/chromium_headless_shell-*/chrome-*/*headless_shell; do [ -x "$p" ] && { echo "$p"; return 0; }; done; fi
  for b in ${BROWSER_BIN:-} chromium chromium-browser google-chrome-stable google-chrome chrome chrome-headless-shell headless_shell; do
    p=$(command -v "$b" 2>/dev/null) || continue; not_snap "$p" && { echo "$p"; return 0; }
  done
  for p in "$HOME"/.cache/ms-playwright/chromium-*/chrome-linux*/chrome "$HOME"/.cache/ms-playwright/chromium_headless_shell-*/chrome-*/*headless_shell /ms-playwright/chromium-*/chrome-linux*/chrome "$D"/cft/chrome-linux64/chrome "$D"/cft/chrome-headless-shell-linux64/chrome-headless-shell /opt/google/chrome/chrome /usr/lib/chromium/chromium; do
    [ -x "$p" ] && { echo "$p"; return 0; }
  done
  return 1
}
fix_libs() { # rootless: fetch the missing shared libraries as .debs (apt-get download) and unpack them under $D/libs
  ldd "$B" 2>/dev/null | grep -q "not found" || return 0
  command -v apt-get >/dev/null 2>&1 && command -v dpkg-deb >/dev/null 2>&1 || return 1
  mkdir -p "$D/debs" "$D/libs"
  L="$D/libs/usr/lib/$(uname -m)-linux-gnu:$D/libs/lib/$(uname -m)-linux-gnu"
  PKGS="libnspr4 libnss3 libatk1.0-0t64 libatk1.0-0 libatk-bridge2.0-0t64 libatk-bridge2.0-0 libcups2t64 libcups2 libdrm2 libxkbcommon0 libxcomposite1 libxdamage1 libxfixes3 libxrandr2 libgbm1 libpango-1.0-0 libcairo2 libasound2t64 libasound2 libatspi2.0-0t64 libatspi2.0-0 libxshmfence1 libwayland-server0 libavahi-common3 libavahi-client3"
  fetch() { (cd "$D/debs" && for p in $PKGS; do apt-get "$@" download "$p" >>"$D/install.log" 2>&1 || true; done
    for f in *.deb; do [ -f "$f" ] && dpkg-deb -x "$f" "$D/libs"; done) 2>>"$D/install.log"; }
  fetch
  if LD_LIBRARY_PATH="$L" ldd "$B" 2>/dev/null | grep -q "not found"; then
    # images often ship without apt lists (/var/lib/apt/lists emptied): fetch a private index without root
    mkdir -p "$D/apt/lists/partial" "$D/apt/cache/archives/partial"
    set -- -o "Dir::State::Lists=$D/apt/lists" -o "Dir::Cache=$D/apt/cache" -o "Dir::Cache::archives=$D/apt/cache/archives" -o Debug::NoLocking=1 -o "APT::Sandbox::User=$(id -un)"
    timeout 180 apt-get "$@" -qq update >>"$D/install.log" 2>&1
    fetch "$@"
  fi
  LD_LIBRARY_PATH="$L" ldd "$B" 2>/dev/null | grep -q "not found" && return 1
  echo "$L" > "$D/browser.libs"
}
SRC=found; B=$(find_browser)
if [ -z "$B" ]; then
  [ "${INSTALL:-auto}" = off ] && { echo "error=no Chromium/Chrome in the box (install chromium, or allow installs)"; exit 3; }
  SRC=installed; echo "installing=1" >&2
  if is_debian && can_apt; then apt_install chromium fonts-liberation; B=$(find_browser); fi
  if [ -z "$B" ] && command -v npx >/dev/null 2>&1; then
    if [ "$SUDO" != NO ]; then (cd "$D" && timeout 300 npx -y playwright@1 install --with-deps chromium >>"$D/install.log" 2>&1); else (cd "$D" && timeout 300 npx -y playwright@1 install chromium >>"$D/install.log" 2>&1); fi
    B=$(find_browser)
  fi
  if [ -z "$B" ] && [ "$(uname -m)" = x86_64 ]; then
    dl() { if command -v curl >/dev/null 2>&1; then curl -fsSL --retry 2 -o "$2" "$1"; elif command -v wget >/dev/null 2>&1; then wget -q -O "$2" "$1"; else python3 -c 'import sys,urllib.request; urllib.request.urlretrieve(sys.argv[1], sys.argv[2])' "$1" "$2"; fi; }
    V=$(dl https://googlechromelabs.github.io/chrome-for-testing/LATEST_RELEASE_STABLE /dev/stdout 2>/dev/null | tr -d '[:space:]')
    if [ -n "$V" ] && dl "https://storage.googleapis.com/chrome-for-testing-public/$V/linux64/chrome-linux64.zip" "$D/cft.zip" >>"$D/install.log" 2>&1; then
      mkdir -p "$D/cft"; (cd "$D/cft" && { unzip -qo ../cft.zip 2>/dev/null || python3 -c 'import zipfile,sys; zipfile.ZipFile(sys.argv[1]).extractall(".")' ../cft.zip; }); rm -f "$D/cft.zip"
      chmod +x "$D"/cft/chrome-linux64/chrome "$D"/cft/chrome-linux64/chrome_crashpad_handler 2>/dev/null
      if can_apt; then apt_install libnss3 libatk1.0-0 libatk-bridge2.0-0 libcups2 libdrm2 libxkbcommon0 libxcomposite1 libxdamage1 libxfixes3 libxrandr2 libgbm1 libpango-1.0-0 libcairo2 fonts-liberation; apt_install libasound2 || apt_install libasound2t64; fi
      B=$(find_browser)
    fi
  fi
  if [ -n "$B" ] && [ "$SUDO" = NO ]; then # no root: system libraries Chromium needs may be missing
    fix_libs || { echo "error=Chromium installed but system libraries are missing ($(LD_LIBRARY_PATH="${L:-}" ldd "$B" 2>/dev/null | awk '/not found/ {print $1}' | head -4 | tr '\n' ' ')) and there is no root/sudo to install them"; tail -2 "$D/install.log" 2>/dev/null | tr "\n" " " | sed "s/^/detail=/"; echo; exit 3; }
  fi
  [ -z "$B" ] && { echo "error=could not install Chromium (see $D/install.log: needs apt with root/sudo, npx, or x86_64 + internet)"; tail -3 "$D/install.log" 2>/dev/null | tr '\n' ' ' | sed 's/^/detail=/'; echo; exit 3; }
fi
PROFILE="${PROFILE:-$HOME/.local/share/bx-browser}"; mkdir -p "$PROFILE" 2>/dev/null || PROFILE="$D/profile"
HL="${HEADLESS:-1}"; DISP_ENV=""
if [ "$HL" != 1 ]; then if [ -f "$D/display" ] && alive "$D/xvfb.pid"; then DISP_ENV="DISPLAY=$(cat "$D/display")"; else HL=1; fi; fi
case "$B" in *headless_shell*|*headless-shell*) HL=1; DISP_ENV="";; esac
RULES="MAP metadata.google.internal ~NOTFOUND, MAP metadata.goog ~NOTFOUND, MAP metadata.azure.com ~NOTFOUND, MAP metadata.tencentyun.com ~NOTFOUND, MAP 169.254.169.254 ~NOTFOUND, MAP 169.254.170.2 ~NOTFOUND, MAP 169.254.0.23 ~NOTFOUND, MAP 100.100.100.200 ~NOTFOUND, MAP [fd00:ec2::254] ~NOTFOUND"
start_it() { # $1 = extra flag
  set -- "$B" --remote-debugging-address=127.0.0.1 --remote-debugging-port="$PORT" --user-data-dir="$PROFILE" \
    --no-first-run --no-default-browser-check --disable-dev-shm-usage --password-store=basic --use-mock-keychain \
    --disable-background-networking --disable-component-update --disable-sync --metrics-recording-only \
    "--host-resolver-rules=$RULES" --window-size="${W:-1280},${H:-800}" $1
  [ "$HL" = 1 ] && set -- "$@" --headless=new --disable-gpu
  [ "$(id -u)" = 0 ] && set -- "$@" --no-sandbox
  # no unprivileged user namespaces (most containers / microVM images): Chromium's sandbox cannot start renderers
  if [ "$(id -u)" != 0 ] && ! { command -v unshare >/dev/null 2>&1 && unshare --user --map-root-user true 2>/dev/null; }; then set -- "$@" --no-sandbox; fi
  MEM=$(awk '/MemTotal/ {print int($2/1024)}' /proc/meminfo 2>/dev/null)
  if [ -n "$MEM" ] && [ "$MEM" -lt 1500 ]; then set -- "$@" --renderer-process-limit=2 --disable-features=Translate,MediaRouter,OptimizationHints --disable-extensions; fi
  # tiny boxes (≈512 MB, strict overcommit): V8 cannot reserve its JIT code range ("V8 process OOM … CodeRange") — run JS without JIT
  if [ -n "$MEM" ] && [ "$MEM" -lt 900 ]; then set -- "$@" --js-flags=--jitless --no-zygote --no-sandbox; fi
  if [ -n "$DISP_ENV" ]; then set -- env "$DISP_ENV" "$@"; fi
  if [ -f "$D/browser.libs" ]; then set -- env "LD_LIBRARY_PATH=$(cat "$D/browser.libs")" "$@"; fi
  PID=$(detach "$@" about:blank)
  echo "$PID" > "$D/browser.pid"; echo "$HL" > "$D/browser.headless"
  i=0; while [ $i -lt 100 ]; do listening "$PORT" && return 0; kill -0 "$PID" 2>/dev/null || return 1; sleep 0.15; i=$((i+1)); done; return 1
}
: > "$LOG"
if ! start_it ""; then
  kill "$(cat "$D/browser.pid")" 2>/dev/null; sleep 0.3
  start_it "--no-sandbox" || { echo "error=browser did not start"; tail -5 "$LOG" | tr '\n' ' ' | sed 's/^/detail=/'; echo; exit 4; }
fi
echo "state=started"; echo "browser=$B"; echo "pid=$(cat "$D/browser.pid")"; echo "headless=$HL"; echo "source=$SRC"
`

// browserStop stops the browser started by browserStart.
const browserStop = addonCommon + `
if alive "$D/browser.pid"; then P=$(cat "$D/browser.pid"); kill -TERM -"$P" 2>/dev/null || kill "$P" 2>/dev/null; sleep 0.5; kill -0 "$P" 2>/dev/null && { kill -KILL -"$P" 2>/dev/null || kill -9 "$P"; }; echo "stopped=1"; else echo "stopped=0"; fi
rm -f "$D/browser.pid" "$D/browser.headless"
`

// browserStatus reports without starting anything.
const browserStatus = addonCommon + `
PORT="${PORT:-9222}"
if listening "$PORT"; then echo "running=1"; else echo "running=0"; fi
alive "$D/browser.pid" && { echo "pid=$(cat "$D/browser.pid")"; echo "headless=$(cat "$D/browser.headless" 2>/dev/null)"; }
true
`

// desktopStart starts Xvfb + a light window manager + x11vnc (or Xvnc) with
// RFB on 127.0.0.1:$VPORT. Prints display=:N width= height= vnc=... wm=...
const desktopStart = addonCommon + `
VPORT="${VPORT:-5900}"; DISP="${DISP:-99}"; W="${W:-1280}"; H="${H:-800}"; LOG="$D/desktop.log"
if alive "$D/xvfb.pid" && listening "$VPORT"; then echo "state=running"; echo "display=$(cat "$D/display")"; cat "$D/desktop.size" 2>/dev/null; exit 0; fi
have() { command -v "$1" >/dev/null 2>&1; }
if ! have Xvnc && { ! have Xvfb || ! have x11vnc; }; then
  [ "${INSTALL:-auto}" = off ] && { echo "error=no Xvfb/x11vnc in the box"; exit 3; }
  echo "installing=1" >&2
  if can_apt; then apt_install xvfb x11vnc openbox xauth x11-utils fonts-dejavu-core; 
  elif have apk && [ "$SUDO" != NO ]; then $SUDO apk add --no-cache xvfb x11vnc openbox >>"$D/install.log" 2>&1;
  elif have dnf && [ "$SUDO" != NO ]; then $SUDO dnf install -y -q xorg-x11-server-Xvfb x11vnc openbox >>"$D/install.log" 2>&1; fi
  if ! have Xvnc && { ! have Xvfb || ! have x11vnc; }; then echo "error=could not install Xvfb + x11vnc (needs apt/apk/dnf with root or sudo)"; exit 3; fi
fi
: > "$LOG"
for f in xvfb.pid wm.pid vnc.pid; do alive "$D/$f" && kill "$(cat "$D/$f")" 2>/dev/null; done
rm -f "/tmp/.X$DISP-lock" "/tmp/.X11-unix/X$DISP" 2>/dev/null
if have Xvfb && have x11vnc; then
  detach Xvfb ":$DISP" -screen 0 "${W}x${H}x24" -nolisten tcp -ac +extension RANDR > "$D/xvfb.pid"
  i=0; while [ $i -lt 60 ] && [ ! -e "/tmp/.X11-unix/X$DISP" ]; do sleep 0.1; i=$((i+1)); done
  [ -e "/tmp/.X11-unix/X$DISP" ] || { echo "error=Xvfb did not start"; tail -3 "$LOG" | tr '\n' ' ' | sed 's/^/detail=/'; echo; exit 4; }
  VNC=x11vnc
  detach x11vnc -display ":$DISP" -localhost -rfbport "$VPORT" -shared -forever -nopw -noxdamage -quiet -noipv6 > "$D/vnc.pid"
else
  VNC=Xvnc
  detach Xvnc ":$DISP" -geometry "${W}x${H}" -depth 24 -SecurityTypes None -localhost -rfbport "$VPORT" -nolisten tcp -AlwaysShared > "$D/xvfb.pid"
  cp "$D/xvfb.pid" "$D/vnc.pid"
fi
WM=none
for wm in openbox fluxbox xfwm4 matchbox-window-manager twm; do if have "$wm"; then WM=$wm; LOG="$D/wm.log"; detach env DISPLAY=":$DISP" "$wm" > "$D/wm.pid"; LOG="$D/desktop.log"; break; fi; done
i=0; while [ $i -lt 80 ]; do listening "$VPORT" && break; sleep 0.1; i=$((i+1)); done
listening "$VPORT" || { echo "error=VNC server did not start"; tail -3 "$LOG" | tr '\n' ' ' | sed 's/^/detail=/'; echo; exit 4; }
have xsetroot && DISPLAY=":$DISP" xsetroot -solid '#1f2937' 2>/dev/null
echo ":$DISP" > "$D/display"; printf 'width=%s\nheight=%s\n' "$W" "$H" > "$D/desktop.size"
echo "state=started"; echo "display=:$DISP"; echo "width=$W"; echo "height=$H"; echo "vnc=$VNC"; echo "wm=$WM"
`

// desktopStop stops the desktop processes.
const desktopStop = addonCommon + `
n=0; for f in vnc.pid wm.pid xvfb.pid; do if alive "$D/$f"; then P=$(cat "$D/$f"); kill -TERM -"$P" 2>/dev/null || kill "$P" 2>/dev/null; n=$((n+1)); fi; rm -f "$D/$f"; done
rm -f "$D/display" "$D/desktop.size"; echo "stopped=$n"
`

// desktopStatus reports without starting anything.
const desktopStatus = addonCommon + `
VPORT="${VPORT:-5900}"
if alive "$D/xvfb.pid" && listening "$VPORT"; then echo "running=1"; echo "display=$(cat "$D/display" 2>/dev/null)"; cat "$D/desktop.size" 2>/dev/null; else echo "running=0"; fi
true
`
