#!/usr/bin/env python3
"""Probes for the v1.11 access endpoints (stdlib only; used by scripts/e2e.sh
and scripts/live-access.sh).

  accessprobe.py cdp  WSS_URL [--navigate URL] [--expect-blocked URL]
      Browser.getVersion over the CDP WebSocket (Playwright/puppeteer-free);
      --navigate opens a tab, waits for the load and prints its title;
      --expect-blocked checks that a navigation to URL never reaches it.
  accessprobe.py vnc  WSS_URL [--input]
      RFB handshake ("RFB 003.00x"), ServerInit, one framebuffer update.
  accessprobe.py http URL      prints the HTTP status (no redirects).

Prints one JSON line with the results and timings (ms); exit 1 on failure.
"""
import base64, json, os, socket, ssl, struct, sys, time, urllib.parse, urllib.request


class WS:
    def __init__(self, url, timeout=60):
        u = urllib.parse.urlparse(url)
        port = u.port or (443 if u.scheme == "wss" else 80)
        s = socket.create_connection((u.hostname, port), timeout=timeout)
        if u.scheme == "wss":
            s = ssl.create_default_context().wrap_socket(s, server_hostname=u.hostname)
        key = base64.b64encode(os.urandom(16)).decode()
        path = u.path + ("?" + u.query if u.query else "")
        s.sendall((f"GET {path} HTTP/1.1\r\nHost: {u.netloc}\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"
                   f"Sec-WebSocket-Key: {key}\r\nSec-WebSocket-Version: 13\r\nUser-Agent: probe\r\n\r\n").encode())
        buf = b""
        while b"\r\n\r\n" not in buf:
            c = s.recv(4096)
            if not c:
                raise SystemExit("closed during handshake")
            buf += c
        head, self.rest = buf.split(b"\r\n\r\n", 1)
        self.status = int(head.split(b" ")[1])
        if self.status != 101:
            raise SystemExit(f"HTTP {self.status}: {head.decode(errors='replace')[:300]}")
        self.s = s
        self.stream = b""

    def send(self, data, op=1):
        if isinstance(data, str):
            data = data.encode()
        mask = os.urandom(4)
        n = len(data)
        h = bytes([0x80 | op])
        if n < 126:
            h += bytes([0x80 | n])
        elif n < 65536:
            h += bytes([0x80 | 126]) + struct.pack(">H", n)
        else:
            h += bytes([0x80 | 127]) + struct.pack(">Q", n)
        self.s.sendall(h + mask + bytes(b ^ mask[i % 4] for i, b in enumerate(data)))

    def _read(self, n):
        while len(self.rest) < n:
            c = self.s.recv(65536)
            if not c:
                raise EOFError("closed")
            self.rest += c
        out, self.rest = self.rest[:n], self.rest[n:]
        return out

    def recv(self):
        """one message: (opcode, payload); answers pings."""
        msg, first = b"", None
        while True:
            b0, b1 = self._read(2)
            op, n = b0 & 0x0F, b1 & 0x7F
            if n == 126:
                n = struct.unpack(">H", self._read(2))[0]
            elif n == 127:
                n = struct.unpack(">Q", self._read(8))[0]
            data = self._read(n)
            if op == 9:
                self.send(data, 10)
                continue
            if op == 8:
                raise EOFError("close frame")
            if op == 10:
                continue
            first = first if first is not None else op
            msg += data
            if b0 & 0x80:
                return first, msg

    def read_stream(self, n):
        """binary stream view (RFB over binary frames)."""
        while len(self.stream) < n:
            _, d = self.recv()
            self.stream += d
        out, self.stream = self.stream[:n], self.stream[n:]
        return out


def cdp(url, navigate=None, blocked=None):
    t0 = time.time()
    ws = WS(url)
    t_conn = time.time()
    seq = [0]
    out = {"ok": True}

    def call(method, params=None, session=None, timeout=60):
        seq[0] += 1
        m = {"id": seq[0], "method": method, "params": params or {}}
        if session:
            m["sessionId"] = session
        ws.send(json.dumps(m))
        end = time.time() + timeout
        while time.time() < end:
            try:
                _, d = ws.recv()
            except (TimeoutError, OSError, EOFError) as e:
                raise SystemExit(json.dumps({"ok": False, "error": f"{method}: {e!r}", **out}))
            r = json.loads(d)
            if r.get("id") == seq[0]:
                if "error" in r:
                    raise SystemExit(f"{method}: {r['error']}")
                return r.get("result", {})
        raise SystemExit(f"{method}: timeout")

    v = call("Browser.getVersion")
    out["product"], out["protocol"] = v.get("product"), v.get("protocolVersion")
    out["connect_ms"] = int((t_conn - t0) * 1000)
    out["getversion_ms"] = int((time.time() - t_conn) * 1000)

    def open_and_eval(u, expr, wait_s=30):
        tid = call("Target.createTarget", {"url": "about:blank"})["targetId"]
        sid = call("Target.attachToTarget", {"targetId": tid, "flatten": True})["sessionId"]
        nav = call("Page.navigate", {"url": u}, session=sid)
        val = None
        end = time.time() + wait_s
        while time.time() < end:
            r = call("Runtime.evaluate", {"expression": "document.readyState + '|' + location.href + '|' + " + expr, "returnByValue": True}, session=sid)
            val = r.get("result", {}).get("value", "")
            if val.startswith("complete|") and not val.split("|")[1].startswith("about:blank"):
                break
            if nav.get("errorText"):
                break
            time.sleep(0.25)
        call("Target.closeTarget", {"targetId": tid})
        return nav, val

    if navigate:
        t1 = time.time()
        nav, val = open_and_eval(navigate, "document.title")
        out["navigate_ms"] = int((time.time() - t1) * 1000)
        out["title"] = (val or "").split("|", 2)[-1]
        out["href"] = (val or "|").split("|")[1] if val and "|" in val else ""
        if nav.get("errorText"):
            out["ok"], out["nav_error"] = False, nav["errorText"]
    if blocked:
        nav, val = open_and_eval(blocked, "document.body ? document.body.innerText.slice(0, 80) : ''", wait_s=10)
        href = (val or "|").split("|")[1] if val and "|" in val else ""
        reached = bool(href) and urllib.parse.urlparse(href).hostname == urllib.parse.urlparse(blocked).hostname and not nav.get("errorText")
        out["blocked_nav"] = {"errorText": nav.get("errorText"), "href": href}
        out["metadata_blocked"] = not reached
        if reached:
            out["ok"] = False
    return out


def vnc(url, send_input=False):
    t0 = time.time()
    ws = WS(url)
    ver = ws.read_stream(12)
    out = {"ok": ver.startswith(b"RFB 003."), "version": ver.decode().strip(), "handshake_ms": int((time.time() - t0) * 1000)}
    ws.send(b"RFB 003.008\n", 2)
    n = ws.read_stream(1)[0]
    types = list(ws.read_stream(n)) if n else []
    if 1 not in types:
        raise SystemExit(f"no 'None' security type offered: {types}")
    ws.send(bytes([1]), 2)
    if struct.unpack(">I", ws.read_stream(4))[0] != 0:
        raise SystemExit("security failed")
    ws.send(bytes([1]), 2)  # ClientInit shared
    si = ws.read_stream(24)
    w, h = struct.unpack(">HH", si[:4])
    name = ws.read_stream(struct.unpack(">I", si[20:24])[0]).decode(errors="replace")
    out.update({"width": w, "height": h, "name": name})
    bpp = si[4] // 8
    t1 = time.time()
    ws.send(bytes([2, 0]) + struct.pack(">H", 1) + struct.pack(">i", 0), 2)  # SetEncodings: Raw
    ws.send(bytes([3, 0]) + struct.pack(">HHHH", 0, 0, min(w, 64), min(h, 64)), 2)  # small non-incremental update
    while True:
        t = ws.read_stream(1)[0]
        if t == 0:
            break
        if t == 2:  # bell
            continue
        if t == 1:  # colour map
            _, first, nc = struct.unpack(">BHH", ws.read_stream(5))
            ws.read_stream(6 * nc)
            continue
        if t == 3:
            ws.read_stream(3)
            ws.read_stream(struct.unpack(">I", ws.read_stream(4))[0])
            continue
        raise SystemExit(f"unexpected server message {t}")
    ws.read_stream(1)
    nrect = struct.unpack(">H", ws.read_stream(2))[0]
    pixels = 0
    for _ in range(nrect):
        x, y, rw, rh, enc = struct.unpack(">HHHHi", ws.read_stream(12))
        if enc == 0:
            ws.read_stream(rw * rh * bpp)
            pixels += rw * rh
        else:  # pseudo-encodings carry no payload here (we only asked for Raw)
            pass
    out.update({"update_rects": nrect, "update_pixels": pixels, "update_ms": int((time.time() - t1) * 1000)})
    if send_input:
        ws.send(bytes([5, 0]) + struct.pack(">HH", 5, 5), 2)
    return out


def http_status(url):
    class NoRedirect(urllib.request.HTTPRedirectHandler):
        def redirect_request(self, *a, **k):
            return None
    host = urllib.parse.urlparse(url).hostname or ""
    handlers = [NoRedirect]
    if host in ("127.0.0.1", "localhost", "::1"):
        handlers.append(urllib.request.ProxyHandler({}))  # never send loopback through a proxy
    op = urllib.request.build_opener(*handlers)
    try:
        r = op.open(urllib.request.Request(url, headers={"User-Agent": "probe"}), timeout=60)
        return {"status": r.status}
    except urllib.error.HTTPError as e:
        return {"status": e.code}


def main():
    a = sys.argv[1:]
    if len(a) < 2:
        sys.exit(__doc__)
    kind, url = a[0], a[1]
    opt = {a[i]: (a[i + 1] if i + 1 < len(a) and not a[i + 1].startswith("--") else True) for i in range(2, len(a)) if a[i].startswith("--")}
    if kind == "cdp":
        out = cdp(url, opt.get("--navigate"), opt.get("--expect-blocked"))
    elif kind == "vnc":
        out = vnc(url, bool(opt.get("--input")))
    elif kind == "http":
        out = http_status(url)
        print(json.dumps(out))
        return
    else:
        sys.exit(__doc__)
    print(json.dumps(out))
    sys.exit(0 if out.get("ok") else 1)


if __name__ == "__main__":
    main()
