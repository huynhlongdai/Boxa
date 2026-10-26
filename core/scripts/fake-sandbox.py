#!/usr/bin/env python3
"""Fake Sandbox API for scripts/e2e.sh (v1.4): a tiny exec-only REST service that
the declarative YAML adapter in the e2e run drives (create/get/delete/exec).
Each sandbox is a directory; exec runs `bash -c` in it. Not for production."""
import json, os, shutil, subprocess, sys, threading, uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

ROOT = sys.argv[2]
TOKEN = os.environ.get("FAKE_SANDBOX_TOKEN", "fsb_e2e_0123456789")
boxes, lock = {}, threading.Lock()
log = open(os.path.join(ROOT, "calls.log"), "a", buffering=1)


class H(BaseHTTPRequestHandler):
    def log_message(self, *a):
        pass

    def reply(self, code, obj):
        b = json.dumps(obj).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(b)))
        self.end_headers()
        self.wfile.write(b)

    def route(self, method):
        log.write(f"{method} {self.path}\n")
        if self.headers.get("Authorization") != "Bearer " + TOKEN:
            return self.reply(401, {"error": "bad key"})
        n = int(self.headers.get("Content-Length") or 0)
        body = json.loads(self.rfile.read(n) or b"{}") if n else {}
        parts = [p for p in self.path.split("?")[0].split("/") if p]  # v1 sandboxes [id] [exec]
        if parts[:2] != ["v1", "sandboxes"]:
            return self.reply(404, {"error": "not found"})
        if len(parts) == 2 and method == "GET":
            with lock:
                return self.reply(200, {"sandboxes": [{"id": k, "status": "running"} for k in boxes]})
        if len(parts) == 2 and method == "POST":
            sid = "sbx-" + uuid.uuid4().hex[:10]
            d = os.path.join(ROOT, sid)
            os.makedirs(d, exist_ok=True)
            with lock:
                boxes[sid] = d
            return self.reply(201, {"id": sid, "status": "running", "cpu": body.get("cpu")})
        sid = parts[2] if len(parts) > 2 else ""
        with lock:
            d = boxes.get(sid)
        if not d:
            return self.reply(404, {"error": "no sandbox " + sid})
        if len(parts) == 3 and method == "GET":
            return self.reply(200, {"id": sid, "status": "running"})
        if len(parts) == 3 and method == "DELETE":
            with lock:
                boxes.pop(sid, None)
            shutil.rmtree(d, ignore_errors=True)
            return self.reply(200, {"id": sid, "status": "deleted"})
        if len(parts) == 4 and parts[3] == "exec" and method == "POST":
            try:
                p = subprocess.run(["bash", "-c", body.get("command", "")], cwd=d, capture_output=True, text=True, timeout=int(body.get("timeout") or 60))
                return self.reply(200, {"stdout": p.stdout, "stderr": p.stderr, "exit_code": p.returncode})
            except subprocess.TimeoutExpired:
                return self.reply(200, {"stdout": "", "stderr": "timeout", "exit_code": 124})
        return self.reply(404, {"error": "no route"})

    def do_GET(self):
        self.route("GET")

    def do_POST(self):
        self.route("POST")

    def do_DELETE(self):
        self.route("DELETE")


ThreadingHTTPServer(("127.0.0.1", int(sys.argv[1])), H).serve_forever()
