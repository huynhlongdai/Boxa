#!/usr/bin/env python3
"""Minimal WebSocket client for scripts/e2e.sh and the live scripts (stdlib only).

usage: wsclient.py URL [-H 'Name: value']... [--send TEXT]... [--until SUBSTR] [--timeout S] [--status]

Opens the socket, sends each --send as a binary frame (PTY input, "\\n" escapes
honoured), prints received binary data to stdout and JSON text frames as
"CTL <json>" lines on stderr, and exits 0 once --until appears in the output
(or the socket closes without --until). --status prints only the HTTP status
of the upgrade (101, 401, 403, …) and exits.
"""
import base64, os, socket, ssl, struct, sys, time, urllib.parse


def main():
    a = sys.argv[1:]
    url, hdrs, sends, until, timeout, status_only = a[0], [], [], None, 10.0, False
    i = 1
    while i < len(a):
        if a[i] == "-H": hdrs.append(a[i + 1]); i += 2
        elif a[i] == "--send": sends.append(a[i + 1].encode().decode("unicode_escape").encode()); i += 2
        elif a[i] == "--until": until = a[i + 1]; i += 2
        elif a[i] == "--timeout": timeout = float(a[i + 1]); i += 2
        elif a[i] == "--status": status_only = True; i += 1
        else: sys.exit("bad arg " + a[i])
    u = urllib.parse.urlparse(url)
    port = u.port or (443 if u.scheme == "wss" else 80)
    s = socket.create_connection((u.hostname, port), timeout=timeout)
    if u.scheme == "wss":
        s = ssl.create_default_context().wrap_socket(s, server_hostname=u.hostname)
    key = base64.b64encode(os.urandom(16)).decode()
    path = u.path + ("?" + u.query if u.query else "")
    req = f"GET {path} HTTP/1.1\r\nHost: {u.netloc}\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: {key}\r\nSec-WebSocket-Version: 13\r\nUser-Agent: wsclient\r\n"
    for h in hdrs:
        req += h + "\r\n"
    s.sendall((req + "\r\n").encode())
    buf = b""
    while b"\r\n\r\n" not in buf:
        c = s.recv(4096)
        if not c: break
        buf += c
    head, _, rest = buf.partition(b"\r\n\r\n")
    code = int(head.split(b" ")[1]) if head else 0
    if status_only:
        print(code); return
    if code != 101:
        sys.stderr.write(head.decode(errors="replace") + "\n" + rest.decode(errors="replace") + "\n"); sys.exit(2)

    def send(op, data):
        mask = os.urandom(4)
        n = len(data)
        hdr = bytes([0x80 | op])
        if n < 126: hdr += bytes([0x80 | n])
        elif n < 65536: hdr += bytes([0x80 | 126]) + struct.pack(">H", n)
        else: hdr += bytes([0x80 | 127]) + struct.pack(">Q", n)
        s.sendall(hdr + mask + bytes(b ^ mask[j % 4] for j, b in enumerate(data)))

    pending = rest
    def need(n):
        nonlocal pending
        while len(pending) < n:
            c = s.recv(65536)
            if not c: raise EOFError
            pending += c
        out, pending = pending[:n], pending[n:]
        return out

    for d in sends:
        send(2, d)
    out = b""
    deadline = time.time() + timeout
    try:
        while time.time() < deadline:
            s.settimeout(max(0.1, deadline - time.time()))
            b0, b1 = need(2)
            op, n = b0 & 0x0F, b1 & 0x7F
            if n == 126: n = struct.unpack(">H", need(2))[0]
            elif n == 127: n = struct.unpack(">Q", need(8))[0]
            data = need(n)
            if op == 8: break
            if op == 9: send(10, data); continue
            if op == 1:
                sys.stderr.write("CTL " + data.decode(errors="replace") + "\n"); sys.stderr.flush(); continue
            if op in (0, 2):
                out += data; sys.stdout.write(data.decode(errors="replace")); sys.stdout.flush()
                if until and until in out.decode(errors="replace"):
                    s.close(); return
    except (EOFError, socket.timeout, OSError):
        pass
    s.close()
    if until:
        sys.exit(1)


if __name__ == "__main__":
    main()
