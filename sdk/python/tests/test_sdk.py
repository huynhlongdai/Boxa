import asyncio
import json

import pytest

import boxa
from boxa import AsyncBoxa, Boxa, BoxaError, as_anthropic_tools, as_gemini_tools, as_openai_tools, handle_tool_call
from boxa import _client


def test_discovery(core):
    bx = Boxa(core["key"], core["url"])
    assert bx.health()["ok"] is True
    assert len(bx.agent_kinds()["items"]) == 22
    tools = as_openai_tools(bx)
    names = {t["function"]["name"] for t in tools}
    assert {"box_up", "box_exec_batch", "box_fork", "box_quote"} <= names and len(names) == 21
    assert as_openai_tools(bx, responses_api=True)[0]["name"] == "box_ensure"
    assert as_anthropic_tools(bx)[0]["input_schema"]["type"] == "object"
    assert len(as_gemini_tools(bx)[0]["functionDeclarations"]) == 21
    assert bx.llms_txt().startswith("# Boxa")
    assert bx.openapi()["openapi"] == "3.1.0"


def test_errors_localized(core):
    bx = Boxa(core["key"], core["url"], lang="vi")
    with pytest.raises(BoxaError) as ei:
        bx.get_box("box_khong_co")
    e = ei.value
    assert e.code == "NOT_FOUND" and e.status == 404 and "không tìm thấy" in e.message and e.docs_url.endswith("#NOT_FOUND")
    assert not e.retryable


def test_box_lifecycle_sync(core):
    with Boxa(core["key"], core["url"]) as bx:
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
        async with AsyncBoxa(core["key"], core["url"], lang="ja") as bx:
            box = await bx.up("base", "task", ttl_seconds=600, ram_mb=256)
            r = await bx.exec(box["id"], "echo async-hi")
            assert r["stdout_tail"] == "async-hi"
            res = await asyncio.gather(*[bx.exec(box["id"], f"echo {i}") for i in range(3)])
            assert sorted(x["stdout_tail"] for x in res) == ["0", "1", "2"]
            try:
                await bx.get_box("box_nope")
                raise AssertionError("expected error")
            except BoxaError as e:
                assert e.code == "NOT_FOUND" and "見つかりません" in e.message
            assert (await bx.call_tool("box_list", {}))["items"]
            await bx.down(box["id"])

    asyncio.run(main())


def test_urllib_fallback(core, monkeypatch):
    monkeypatch.setattr(_client, "httpx", None)
    bx = Boxa(core["key"], core["url"], lang="zh")
    assert bx._http is None
    assert bx.health()["ok"]
    with pytest.raises(BoxaError) as ei:
        bx.get_box("box_nope")
    assert "未找到" in ei.value.message

    async def main():
        abx = AsyncBoxa(core["key"], core["url"])
        assert abx._http is None
        assert len((await abx.agent_kinds())["items"]) == 22

    asyncio.run(main())


def test_env_defaults(monkeypatch):
    monkeypatch.setenv("BOXA_URL", "http://example.test:9/v1")
    monkeypatch.setenv("BOXA_API_KEY", "bx_agent_x")
    monkeypatch.setenv("BOXA_LANG", "vi")
    bx = Boxa()
    assert bx.base_url == "http://example.test:9" and bx.api_key == "bx_agent_x"
    h = bx._headers()
    assert h["Authorization"] == "Bearer bx_agent_x" and h["Accept-Language"] == "vi"
    assert bx._url("/llms.txt") == "http://example.test:9/llms.txt"
    assert bx._url("/boxes", {"all": True, "status": None}) == "http://example.test:9/v1/boxes?all=true"
    assert boxa.__version__ == "1.1.0"


def test_v13_selection_helpers(core):
    from boxa import Boxa
    url, akey = core["url"], core["key"]
    with Boxa(akey, url) as bx:
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
