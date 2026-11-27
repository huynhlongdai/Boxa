"""Spins up a real local Boxmes core + boxd (local driver) for the SDK tests.

Uses BOXMES_TEST_URL + BOXMES_TEST_ADMIN_KEY when set; otherwise starts
../../core/bin/{boxmes-core,boxd} (build them with `go build -o bin/ ./cmd/...`).
Tests are skipped when neither is available.
"""

import json
import os
import shutil
import socket
import subprocess
import tempfile
import time
import urllib.request
from pathlib import Path

import pytest

CORE_BIN = Path(__file__).resolve().parents[3] / "core" / "bin"


def _free_port() -> int:
    s = socket.socket()
    s.bind(("127.0.0.1", 0))
    port = s.getsockname()[1]
    s.close()
    return port


def _req(url, key, method="GET", body=None):
    data = json.dumps(body).encode() if body is not None else None
    r = urllib.request.Request(url, data=data, method=method, headers={"Authorization": f"Bearer {key}", "Content-Type": "application/json"})
    with urllib.request.urlopen(r, timeout=30) as resp:
        return json.loads(resp.read())


os.environ.setdefault("NO_PROXY", "127.0.0.1,localhost")
os.environ["NO_PROXY"] += ",127.0.0.1,localhost"
os.environ["no_proxy"] = os.environ["NO_PROXY"]


@pytest.fixture(scope="session")
def core():
    url, admin = os.environ.get("BOXMES_TEST_URL"), os.environ.get("BOXMES_TEST_ADMIN_KEY")
    procs, work = [], None
    try:
        yield from _core(url, admin, procs, work)
    finally:
        for p in procs:
            p.terminate()
            p.wait(timeout=10)


def _core(url, admin, procs, work):
    if not url:
        if not (CORE_BIN / "boxmes-core").exists() or not (CORE_BIN / "boxd").exists():
            pytest.skip("no local core: build core/bin or set BOXMES_TEST_URL")
        work = tempfile.mkdtemp(prefix="boxmes-sdk-py-")
        port, admin = _free_port(), "bx_admin_sdk_py"
        url = f"http://127.0.0.1:{port}"
        env = dict(os.environ, BOXMES_DATA_DIR=f"{work}/data", BOXMES_ADMIN_KEY=admin, BOXMES_BOOTSTRAP_TOKEN="boot-sdk",
                   BOXMES_ADDR=f"127.0.0.1:{port}", BOXMES_PRICE_FEED="off", BOXMES_LOCAL_PRICE_MONTH="73")
        procs.append(subprocess.Popen([str(CORE_BIN / "boxmes-core")], env=env, stdout=open(f"{work}/core.log", "w"), stderr=subprocess.STDOUT))
        for _ in range(100):
            try:
                urllib.request.urlopen(url + "/v1/health", timeout=1)
                break
            except Exception:
                time.sleep(0.1)
        denv = dict(os.environ, BOXD_CORE_URL=url, BOXD_TOKEN="boot-sdk", BOXD_DRIVER="local", BOXD_STATE_DIR=f"{work}/boxd", BOXD_IP="127.0.0.1")
        procs.append(subprocess.Popen([str(CORE_BIN / "boxd")], env=denv, stdout=open(f"{work}/boxd.log", "w"), stderr=subprocess.STDOUT))
        for _ in range(100):
            hosts = _req(url + "/v1/hosts", admin)["items"]
            if any(h.get("connected") for h in hosts):
                break
            time.sleep(0.1)
    agent = _req(url + "/v1/agents", admin, "POST", {"name": f"sdk-py-{int(time.time()*1000)}", "kind": "custom", "budget_day_usd": 5})
    key = _req(url + "/v1/keys", admin, "POST", {"agent_id": agent["id"], "name": "sdk"})["key"]
    yield {"url": url, "admin": admin, "key": key, "agent_id": agent["id"]}
    if work:
        shutil.rmtree(work, ignore_errors=True)
