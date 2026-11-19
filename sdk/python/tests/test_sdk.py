import asyncio
import json
import time

import pytest

import boxaz
from boxaz import AsyncBoxaz, Boxaz, BoxazError, as_anthropic_tools, as_gemini_tools, as_openai_tools, handle_tool_call
from boxaz import _client


def test_discovery(core):
    bx = Boxaz(core["key"], core["url"])
    assert bx.health()["ok"] is True
    assert len(bx.agent_kinds()["items"]) == 22
    tools = as_openai_tools(bx)
    names = {t["function"]["name"] for t in tools}
    assert {"box_up", "box_exec_batch", "box_fork", "box_quote"} <= names and len(names) == 32
    assert as_openai_tools(bx, responses_api=True)[0]["name"] == "box_ensure"
    assert as_anthropic_tools(bx)[0]["input_schema"]["type"] == "object"
    assert len(as_gemini_tools(bx)[0]["functionDeclarations"]) == 32
    assert bx.llms_txt().startswith("# Boxaz")
    assert bx.openapi()["openapi"] == "3.1.0"


def test_errors_localized(core):
    bx = Boxaz(core["key"], core["url"], lang="vi")
    with pytest.raises(BoxazError) as ei:
        bx.get_box("box_khong_co")
    e = ei.value
    assert e.code == "NOT_FOUND" and e.status == 404 and "không tìm thấy" in e.message and e.docs_url.endswith("#NOT_FOUND")
    assert not e.retryable


def test_box_lifecycle_sync(core):
    with Boxaz(core["key"], core["url"]) as bx:
        q = bx.quote("python", "task", ram_mb=512, ttl_seconds=1800)
        assert q["available"] and q["expected_hours"] == 0.5
        box = bx.create_box("python", "session", ram_mb=512, slug="sdk-py", idempotency_key="sdk-py-1")
        assert box["status"] == "running" and box["next"]
        assert bx.create_box("python", "session", idempotency_key="sdk-py-1")["id"] == box["id"]
        bid = box["id"]
        r = bx.exec(bid, "echo hi && seq 1 300", tail_lines=3)
        assert r["exit_code"] == 0 and r["stdout_tail"].endswith("300") and r["truncated"]
        assert "\n150\n" in bx.logs(r["log_id"])
        b = bx.exec_batch(bid, ["echo a", {"command": "false"}, "echo c"])
        assert b["ok"] is False and b["stopped_at"] == 1 and b["completed"] == 2
        assert bx.exec_batch(bid, ["echo a", "false", "echo c"], stop_on_error=False)["completed"] == 3
        j = bx.run(bid, "sleep 0.3; echo done-async")
        assert j["status"] == "done" and j["result"]["stdout_tail"] == "done-async"
        bx.write(bid, "/work/s.txt", "v1\n")
        snap = bx.snapshot(bid)
        bx.write(bid, "/work/s.txt", "v2\n")
        bx.restore(bid, snap["id"])
        assert bx.read(bid, "/work/s.txt")["content"] == "v1\n"
        fork = bx.fork(bid, snap["id"], slug="sdk-py-fork")
        assert fork["forked_from"] == snap["id"] and bx.read(fork["id"], "/work/s.txt")["content"] == "v1\n"
        assert "template: python" in bx.box_yaml(bid)["yaml"]
        assert any(t["name"] == "python" for t in bx.templates()["items"])
        # tool bridge (MCP tools/call)
        out = json.loads(handle_tool_call("box_exec", json.dumps({"box_id": bid, "command": "echo via-tool"}), bx))
        assert out["stdout_tail"] == "via-tool"
        err = json.loads(handle_tool_call("box_exec", {"box_id": "box_nope", "command": "ls"}, bx))
        assert err["error"]["code"] == "NOT_FOUND"
        assert bx.call_tool("box_cost", {"box_id": bid})["box_id"] == bid
        bx.delete_box(fork["id"])
        assert bx.delete_box(bid)["status"] == "terminated"


def test_async_client(core):
    async def main():
        async with AsyncBoxaz(core["key"], core["url"], lang="ja") as bx:
            box = await bx.up("base", "task", ttl_seconds=600, ram_mb=256)
            r = await bx.exec(box["id"], "echo async-hi")
            assert r["stdout_tail"] == "async-hi"
            res = await asyncio.gather(*[bx.exec(box["id"], f"echo {i}") for i in range(3)])
            assert sorted(x["stdout_tail"] for x in res) == ["0", "1", "2"]
            try:
                await bx.get_box("box_nope")
                raise AssertionError("expected error")
            except BoxazError as e:
                assert e.code == "NOT_FOUND" and "見つかりません" in e.message
            assert (await bx.call_tool("box_list", {}))["items"]
            await bx.down(box["id"])

    asyncio.run(main())


def test_urllib_fallback(core, monkeypatch):
    monkeypatch.setattr(_client, "httpx", None)
    bx = Boxaz(core["key"], core["url"], lang="zh")
    assert bx._http is None
    assert bx.health()["ok"]
    with pytest.raises(BoxazError) as ei:
        bx.get_box("box_nope")
    assert "未找到" in ei.value.message

    async def main():
        abx = AsyncBoxaz(core["key"], core["url"])
        assert abx._http is None
        assert len((await abx.agent_kinds())["items"]) == 22

    asyncio.run(main())


def test_env_defaults(monkeypatch):
    monkeypatch.setenv("BOXAZ_URL", "http://example.test:9/v1")
    monkeypatch.setenv("BOXAZ_API_KEY", "bx_agent_x")
    monkeypatch.setenv("BOXAZ_LANG", "vi")
    bx = Boxaz()
    assert bx.base_url == "http://example.test:9" and bx.api_key == "bx_agent_x"
    h = bx._headers()
    assert h["Authorization"] == "Bearer bx_agent_x" and h["Accept-Language"] == "vi"
    assert bx._url("/llms.txt") == "http://example.test:9/llms.txt"
    assert bx._url("/boxes", {"all": True, "status": None}) == "http://example.test:9/v1/boxes?all=true"
    assert boxaz.__version__ == "1.1.0"


def test_legacy_env_and_package(monkeypatch):
    # Pre-rename BOXA_* env vars and the old `boxa` package keep working.
    for k in ("BOXAZ_URL", "BOXAZ_API_KEY", "BOXAZ_LANG"):
        monkeypatch.delenv(k, raising=False)
    monkeypatch.setenv("BOXA_URL", "http://legacy.test:9")
    monkeypatch.setenv("BOXA_API_KEY", "bx_agent_old")
    with pytest.warns(DeprecationWarning):
        bx = Boxaz()
    assert bx.base_url == "http://legacy.test:9" and bx.api_key == "bx_agent_old"
    monkeypatch.setenv("BOXAZ_URL", "http://new.test:9")
    assert Boxaz().base_url == "http://new.test:9"
    import importlib
    import sys

    sys.modules.pop("boxa", None)
    with pytest.warns(DeprecationWarning):
        boxa = importlib.import_module("boxa")
    assert boxa.Boxa is Boxaz and boxa.BoxaError is BoxazError and boxa.AsyncBoxa is AsyncBoxaz


def test_v13_selection_helpers(core):
    from boxaz import Boxaz
    url, akey = core["url"], core["key"]
    with Boxaz(akey, url) as bx:
        pr = bx.profiles()
        assert len(pr["items"]) == 7 and len(pr["kinds"]) == 22
        rec = bx.recommend("general", ram_mb=256, disk_gb=1, max_price_hour=1)
        assert rec["pick"]["kind"] in ("pool", "warm") and rec["pick"]["reasons"]
        e1 = bx.ensure("sdk-py-ensure", template="base", cpu=0.5, ram_mb=128, disk_gb=1)
        assert e1["action"] == "created" and e1["box"]["status"] == "running"
        bid = e1["box"]["id"]
        r = bx.exec(bid, "mkdir -p q && cd q && export Z=9", session=True)
        r = bx.exec(bid, "pwd; echo Z=$Z", session=True)
        assert "/q" in r["stdout_tail"] and "Z=9" in r["stdout_tail"]
        e2 = bx.ensure("sdk-py-ensure")
        assert e2["action"] == "reused" and e2["box"]["id"] == bid and e2["resume"]["recent_execs"]
        c = bx.clone(bid, slug="sdk-py-clone")
        assert c["status"] == "running" and c["cloned_from"] == bid and c["estimate"]["kind"] == "clone"
        assert bx.resume(bid)["box"]["id"] == bid
        assert bx.rightsize(bid)["box_id"] == bid
        bx.down(c["id"])
        bx.down(bid)


def test_v14_sandbox_helpers(core):
    from boxaz import Boxaz, BoxazError
    url, admin, akey = core["url"], core["admin"], core["key"]
    with Boxaz(akey, url) as bx:
        sp = bx.sandbox_providers()["items"]
        ids = {p["id"]: p for p in sp}
        assert {"e2b", "novita", "daytona", "prized", "boxdsh", "runloop", "blaxel", "vercel", "boat", "agent37", "upstash-box", "render"} <= set(ids)
        assert ids["prized"]["verification"] == "live-verified" and ids["upstash-box"]["tier"] == "T3" and ids["upstash-box"]["mode"] == "shim"
        assert all(p["docs"] and p["initials"] and (p["from_usd_h"] > 0 or p["id"] == "render") and "fork" in p["capabilities"] for p in sp)
        # Render: dial-only (no exec API), free plan from $0/h
        assert ids["render"]["dial_only"] and ids["render"]["mode"] == "dial" and ids["render"]["from_usd_h"] == 0
        # agents cannot lint adapters (admin scope)
        try:
            bx.lint_adapter("id: x")
            raise AssertionError("agent key must not lint adapters")
        except BoxazError as e:
            assert e.code in ("UNAUTHORIZED", "POLICY_DENIED")
    with Boxaz(admin, url) as adm:
        bad = adm.lint_adapter("id: x\nname: X\n")
        assert bad["ok"] is False and "missing" in bad["error"]
        good = adm.lint_adapter(
            "id: acme\nname: Acme\nbase_url: https://api.acme.test\nvalidate: {method: GET, path: /x}\n"
            "create: {method: POST, path: /x, capture: {id: $.id}}\nget: {method: GET, path: /x}\n"
            "delete: {method: DELETE, path: /x}\nexec: [{method: POST, path: /x}]\n")
        assert good["ok"] is True and good["info"]["id"] == "acme" and good["info"]["adapter"] == "yaml"
        # untrusted + fork, sandbox only: every option is own-kernel and fork-capable; shared-kernel ones are explained
        rec = adm.recommend(template="base", untrusted=True, fork=True, isolation="sandbox", cpu=1, ram_mb=1024, lifetime="ephemeral")
        assert rec["requirements"]["untrusted"] is True and rec["requirements"]["fork"] is True
        assert rec["options"] and all(o["kind"] == "sandbox" and o["fork"] and o["strong_isolation"] and o["tier"] in ("T2", "T3") for o in rec["options"])
        assert any(r.get("provider") == "daytona" and "shared kernel" in r["reason"] for r in rec["rejected"])
        assert any("fork" in r["reason"] for r in rec["rejected"])


def test_v17_app_gateway_tokens(core):
    import shutil
    import socket
    import urllib.error
    import urllib.request
    if not shutil.which("python3"):
        pytest.skip("python3 required in the box")
    from boxaz import Boxaz
    url, akey = core["url"], core["key"]
    s = socket.socket(); s.bind(("127.0.0.1", 0)); port = s.getsockname()[1]; s.close()
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))

    def get(u, headers=None):
        try:
            with opener.open(urllib.request.Request(u, headers=headers or {}), timeout=30) as r:
                return r.status, r.read().decode()
        except urllib.error.HTTPError as e:
            return e.code, e.read().decode()

    with Boxaz(akey, url) as bx:
        box = bx.up(template="base", lifecycle="session", slug="sdk-py-app")
        try:
            v = bx.set_app(box["id"], "exec python3 -m http.server $PORT --bind 127.0.0.1", install="echo py-sdk > index.html",
                           port=port, ready_http="/", workdir="/work/site")
            assert v["spec"]["restart"] == "always"
            for _ in range(80):
                st = bx.app(box["id"])["status"]
                if st["state"] == "ready":
                    break
                time.sleep(0.25)
            assert st["ready"] and st["supervisor"] == "boxd"
            gw = [p for p in bx.ports(box["id"])["ports"] if p["port"] == port][0]["gateway_url"]
            full = url + gw if gw.startswith("/") else gw
            assert get(full) == (200, "py-sdk\n")
            t = bx.create_url_token(box["id"], port, ttl_s=600, label="sdk")
            assert t["token"].startswith("bxt_")
            assert get(full)[0] == 401
            assert get(full, {"Authorization": "Bearer " + t["token"]})[0] == 200
            assert bx.url_tokens(box["id"], port)["access"] == "token"
            bx.revoke_url_token(box["id"], port, t["id"])
            assert get(full, {"X-Boxaz-Token": t["token"]})[0] == 401
            bx.set_port_access(box["id"], port, "public")
            bx.sleep(box["id"])
            assert get(full) == (200, "py-sdk\n")  # wake on request
            assert bx.set_wake_on_request(box["id"], False)["wake_on_request"] is False
            assert "data" in bx.app_logs(box["id"])
            caps = {c["provider"]: c for c in bx.capabilities()["items"]}
            assert caps["e2b"]["wake_on_http_native"] and not caps["prized"]["inbound_http"]
            assert caps["e2b"]["private_preview"] and not caps["prized"]["private_preview"]
            gw_info = bx.gateway()
            assert gw_info["mode"] in ("host", "path", "relative") and "hark" in gw_info["reserved"]
            # v1.9 readable names
            assert gw.endswith("/x/sdk-py-app/") and gw_info["url_pattern"].endswith("<name>/")
            r = bx.rename_url(box["id"], "Py Shop")
            assert r["subdomain"] == "py-shop"
            pv = bx.ports(box["id"])
            assert pv["name"] == "py-shop" and pv["ports"][0]["gateway_url"].endswith("/x/py-shop/") and pv["ports"][0]["primary"]
            assert pv["ports"][0]["alias_url"].endswith("-%d/" % port)
            assert bx.set_port_label(box["id"], port, "site")["label"] == "site"
            with pytest.raises(Exception) as ei:
                bx.rename_url(box["id"], "hark")
            assert "reserved" in str(ei.value)
        finally:
            bx.down(box["id"])


SSH_PUB = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHriFbNRs2ilXN0Y7F+viZSJ0GcP4uLy8TuLjh/ttcww sdk-test"


def test_v110_access(core):
    from boxaz import Boxaz, BoxazError
    url, akey = core["url"], core["key"]
    with Boxaz(akey, url) as bx:
        box = bx.up(template="base", lifecycle="session", slug="sdk-py-acc")
        try:
            t = bx.ticket(box["id"], "terminal")
            assert t["ticket"].startswith("bxa_") and "ticket=bxa_" in t["url"] and "/terminal" in t["url"]
            with pytest.raises(BoxazError):
                bx.ticket(box["id"], "tcp")  # needs a port
            with pytest.raises(BoxazError):
                bx.ticket(box["id"], "tcp", port=6390)  # not in the allowlist
            tt = bx.ticket(box["id"], "tcp", port=6390, allow=True)
            assert "port=6390" in tt["url"]
            items = bx.tickets(box["id"])["items"]
            assert len(items) == 2 and all("ticket" not in i and "hash" not in i for i in items)
            assert bx.revoke_ticket(box["id"])["revoked"] == 2
            assert 6390 in bx.allow_forward_port(box["id"], 6391)["allowed_ports"]
            a = bx.access(box["id"])
            assert a["commands"]["forward"].startswith("box forward ") and 6391 in a["tcp"]["allowed_ports"]
            assert "ssh-proxy" in a["ssh"]["config"] and a["ssh"]["known_hosts"].startswith("boxaz-ssh ssh-ed25519 ")
            assert bx.terminals(box["id"])["items"] == []
            assert bx.ssh_info()["host_key_alias"] == "boxaz-ssh"
            k = bx.add_ssh_key(SSH_PUB)
            assert k["fingerprint"].startswith("SHA256:") and k["name"] == "sdk-test"
            assert any(x["id"] == k["id"] for x in bx.ssh_keys()["items"])
            bx.delete_ssh_key(k["id"])
        finally:
            bx.down(box["id"])


def test_v111_access(core):
    from boxaz import Boxaz, BoxazError
    url, akey = core["url"], core["key"]
    with Boxaz(akey, url) as bx:
        box = bx.up(template="base", lifecycle="session", slug="sdk-py-p1")
        try:
            c = bx.ssh_cert(box["id"], SSH_PUB, ttl_s=600)
            assert c["certificate"].startswith("ssh-ed25519-cert-v01@openssh.com ") and box["id"] in c["principals"]
            with pytest.raises(BoxazError):
                bx.ssh_cert(box["id"], SSH_PUB, ttl_s=50000)
            assert bx.revoke_ticket(box["id"], c["id"])["revoked"] == 1
            assert bx.ssh_info()["user_ca"].startswith("ssh-ed25519 ")
            s = bx.share_desktop(box["id"], ttl_s=120)
            assert s["view_only"] and "share=bxs_" in s["url"]
            assert len(bx.desktop_shares(box["id"])["items"]) == 1
            assert bx.revoke_desktop_shares(box["id"])["revoked"] == 1
            assert "running" in bx.browser_status(box["id"]) and "running" in bx.desktop_status(box["id"])
            a = bx.access(box["id"])
            assert a["commands"]["browser"].startswith("box browser ") and "/cdp" in a["cdp"]["url"]
        finally:
            bx.down(box["id"])
