"""v2.0 tenancy helpers: offline (mocked transport) request shapes + a live flow
against the local core from conftest (skipped when no core is available)."""

import asyncio
import json
import time
import urllib.parse

import pytest

from boxmes import AsyncBoxmes, Boxmes, BoxmesError
from boxmes import _client


class _Recorder:
    """Replaces Boxmes._send: records requests and answers canned JSON."""

    def __init__(self, answers=None):
        self.calls = []
        self.answers = answers or {}

    def __call__(self, method, url, data, headers):
        u = urllib.parse.urlparse(url)
        body = json.loads(data) if data else None
        self.calls.append({"method": method, "path": u.path, "query": dict(urllib.parse.parse_qsl(u.query)),
                           "body": body, "auth": headers.get("Authorization")})
        st, ans = self.answers.get((method, u.path), (200, {"ok": True}))
        return st, "application/json", json.dumps(ans).encode()


@pytest.fixture
def mocked(monkeypatch):
    rec = _Recorder()
    bx = Boxmes("bxs_session", "http://core.test:9", max_retries=0)
    monkeypatch.setattr(bx, "_send", rec)
    return bx, rec


def test_tenancy_routes_offline(mocked):
    bx, rec = mocked
    bx.me()
    bx.switch_workspace("ws_b")
    bx.list_workspaces()
    bx.create_workspace("Team B")
    bx.get_workspace()
    bx.rename_workspace("Team B2")
    bx.workspace_usage()
    bx.workspace_audit(type="member.invited", limit=50)
    bx.plans()
    bx.list_members()
    bx.update_member_role("usr_1", "viewer")
    bx.remove_member("usr_1")
    bx.create_invite("a@example.com", "auditor")
    bx.list_invites()
    bx.revoke_invite("wsi_1")
    bx.get_invite("bxi_abc")
    bx.accept_invite("bxi_abc")
    bx.box_locations()
    bx.admin_workspaces()
    bx.admin_update_workspace("ws_b", plan="pro", quota_overrides={"max_boxes": 3})
    got = [(c["method"], c["path"]) for c in rec.calls]
    assert got == [
        ("GET", "/v1/auth/me"), ("POST", "/v1/auth/workspace"), ("GET", "/v1/workspaces"), ("POST", "/v1/workspaces"),
        ("GET", "/v1/workspaces/current"), ("PATCH", "/v1/workspaces/current"), ("GET", "/v1/workspaces/current/usage"),
        ("GET", "/v1/workspaces/current/audit"), ("GET", "/v1/plans"), ("GET", "/v1/workspaces/current/members"),
        ("PATCH", "/v1/workspaces/current/members/usr_1"), ("DELETE", "/v1/workspaces/current/members/usr_1"),
        ("POST", "/v1/workspaces/current/invites"), ("GET", "/v1/workspaces/current/invites"),
        ("DELETE", "/v1/workspaces/current/invites/wsi_1"), ("GET", "/v1/workspace-invites/bxi_abc"),
        ("POST", "/v1/workspace-invites/bxi_abc/accept"), ("GET", "/v1/boxes/locations"), ("GET", "/v1/admin/workspaces"),
        ("PATCH", "/v1/admin/workspaces/ws_b"),
    ]
    b = {c["path"] + " " + c["method"]: c["body"] for c in rec.calls}
    assert b["/v1/auth/workspace POST"] == {"workspace_id": "ws_b"}
    assert b["/v1/workspaces POST"] == {"name": "Team B"}
    assert b["/v1/workspaces/current PATCH"] == {"name": "Team B2"}
    assert b["/v1/workspaces/current/members/usr_1 PATCH"] == {"role": "viewer"}
    assert b["/v1/workspaces/current/invites POST"] == {"email": "a@example.com", "role": "auditor"}
    assert b["/v1/admin/workspaces/ws_b PATCH"] == {"plan": "pro", "quota_overrides": {"max_boxes": 3}}
    assert rec.calls[7]["query"] == {"type": "member.invited", "limit": "50"}
    assert all(c["auth"] == "Bearer bxs_session" for c in rec.calls)


def test_keys_and_box_filters_offline(mocked):
    bx, rec = mocked
    bx.create_key(None, "ci", ["boxes:read", "exec"], expires_in_days=30)
    bx.create_key(None, "until", expires_at="2030-01-01T00:00:00Z")
    bx.create_key("agt_1", "agent")
    bx.list_keys()
    bx.revoke_key("key_1")
    bx.list_boxes(region="eu", location="hetzner-fsn", provider="hetzner", all=True)
    assert rec.calls[0]["body"] == {"name": "ci", "scopes": ["boxes:read", "exec"], "expires_in_days": 30}
    assert rec.calls[1]["body"] == {"name": "until", "expires_at": "2030-01-01T00:00:00Z"}
    assert rec.calls[2]["body"] == {"agent_id": "agt_1", "name": "agent"}
    assert (rec.calls[3]["method"], rec.calls[3]["path"]) == ("GET", "/v1/keys")
    assert (rec.calls[4]["method"], rec.calls[4]["path"]) == ("DELETE", "/v1/keys/key_1")
    assert rec.calls[5]["query"] == {"region": "eu", "location": "hetzner-fsn", "provider": "hetzner", "all": "true"}


def test_quota_and_scope_errors_offline(monkeypatch):
    bx = Boxmes("bx_ws_x", "http://core.test:9", max_retries=2)
    quota = {"error": {"code": "QUOTA_EXCEEDED", "message": "workspace quota max_boxes exceeded (limit 1, used 1)",
                       "hint": "Free capacity or upgrade.", "details": {"quota": "max_boxes", "limit": 1, "used": 1, "plan": "free"}}}
    rec = _Recorder({("POST", "/v1/boxes"): (403, quota),
                     ("POST", "/v1/boxes/box_1/exec"): (401, {"error": {"code": "UNAUTHORIZED", "message": "key lacks scope \"exec\""}}),
                     ("GET", "/v1/boxes/box_other"): (404, {"error": {"code": "NOT_FOUND", "message": "box not found"}})})
    monkeypatch.setattr(bx, "_send", rec)
    with pytest.raises(BoxmesError) as ei:
        bx.create_box("base")
    e = ei.value
    assert e.status == 403 and e.code == "QUOTA_EXCEEDED" and not e.retryable and len(rec.calls) == 1
    assert e.details == {"quota": "max_boxes", "limit": 1, "used": 1, "plan": "free"}
    with pytest.raises(BoxmesError) as ei:
        bx.exec("box_1", "id")
    assert ei.value.status == 401 and ei.value.code == "UNAUTHORIZED"
    with pytest.raises(BoxmesError) as ei:
        bx.get_box("box_other")
    assert ei.value.code == "NOT_FOUND"


def test_async_tenancy_offline(monkeypatch):
    monkeypatch.setattr(_client, "httpx", None)  # AsyncBoxmes falls back to a sync client in a thread
    rec = _Recorder({("GET", "/v1/workspaces/current/usage"): (200, {"plan": "free", "used": {"boxes": 0}})})

    async def main():
        abx = AsyncBoxmes("bxs_s", "http://core.test:9")
        monkeypatch.setattr(abx._sync, "_send", rec)
        u = await abx.workspace_usage()
        assert u["plan"] == "free"
        await abx.list_boxes(region="local")
        await abx.box_locations()

    asyncio.run(main())
    assert [c["path"] for c in rec.calls] == ["/v1/workspaces/current/usage", "/v1/boxes", "/v1/boxes/locations"]
    assert rec.calls[1]["query"] == {"region": "local"}


# ---------------------------------------------------------------- live -------

def _signup(url, email, password="password123", **extra):
    return Boxmes(None, url)._call("POST", "/auth/signup", {"email": email, "password": password, **extra})


def test_tenancy_live(core):
    url, admin = core["url"], core["admin"]
    adm = Boxmes(admin, url)
    tag = str(int(time.time() * 1000))
    try:  # the first account becomes platform admin; make sure it is not one of ours
        _signup(url, f"root-{tag}@example.com", "rootrootroot")
    except BoxmesError:
        pass
    code = adm._call("POST", "/admin/invites", {"max_uses": 1})["code"]
    a = _signup(url, f"owner-{tag}@example.com", invite_code=code, workspace="SDK A")
    A = Boxmes(a["token"], url)
    me = A.me()
    ws = me["workspace"]["id"]
    assert me["role"] == "owner" and "members:write" in me["permissions"] and not me["platform_admin"]
    cur = A.get_workspace()
    assert cur["id"] == ws and cur["plan"] == "free" and cur["quota"]["max_members"] == 1 and cur["member_count"] == 1
    assert A.rename_workspace("SDK A2")["name"] == "SDK A2"
    plans = {p["id"]: p["quota"] for p in A.plans()["items"]}
    assert plans["pro"]["max_boxes"] == 10 and plans["team"]["max_members"] == 0 and plans["free"]["egress"] == "restricted"

    # plan changes are platform-admin only
    with pytest.raises(BoxmesError) as ei:
        A._call("PATCH", "/workspaces/current", {"plan": "team"})
    assert ei.value.status == 403 and ei.value.code == "POLICY_DENIED"
    # free plan: 1 member → inviting is over quota
    with pytest.raises(BoxmesError) as ei:
        A.create_invite(f"dev-{tag}@example.com", "developer")
    assert ei.value.status == 403 and ei.value.code == "QUOTA_EXCEEDED" and ei.value.details["quota"] == "max_members" and ei.value.details["limit"] == 1
    assert adm.admin_update_workspace(ws, plan="pro")["plan"] == "pro"
    assert any(w["id"] == ws for w in adm.admin_workspaces()["items"])

    # workspace key with scopes + expiry
    k = A.create_key(None, "ci", ["boxes:read"], expires_in_days=7)
    assert k["key"] and k["kind"] == "workspace" and k["scopes"] == ["boxes:read"] and k["expires_at"]
    ro = Boxmes(k["key"], url)
    assert ro.list_boxes()["items"] == []
    with pytest.raises(BoxmesError) as ei:
        ro.create_invite("x@example.com")
    assert ei.value.status == 401 and ei.value.code == "UNAUTHORIZED"
    assert any(x["id"] == k["id"] for x in A.list_keys()["items"])
    A.revoke_key(k["id"])
    with pytest.raises(BoxmesError):
        A.create_key(None, "old", expires_at="2001-01-01T00:00:00Z")

    # invite → preview → sign up through the invite → accept
    inv = A.create_invite(f"dev-{tag}@example.com", "developer")
    tok = inv["invite_url"].rsplit("/", 1)[-1]
    assert tok.startswith("bxi_") and inv["status"] == "pending"
    assert [i["status"] for i in A.list_invites()["items"]] == ["pending"]
    pv = Boxmes(None, url).get_invite(tok)
    assert pv["workspace"]["id"] == ws and pv["role"] == "developer" and pv["account_exists"] is False
    d = _signup(url, f"dev-{tag}@example.com", workspace_invite=tok)
    D = Boxmes(d["token"], url)
    assert D.me()["workspace"]["id"] == ws and D.me()["role"] == "developer"
    with pytest.raises(BoxmesError) as ei:
        D.accept_invite(tok)  # already used
    assert ei.value.code in ("INVALID", "NOT_FOUND")
    # developer cannot manage members (role → 403 POLICY_DENIED)
    with pytest.raises(BoxmesError) as ei:
        D.create_invite("y@example.com")
    assert ei.value.status == 403 and ei.value.code == "POLICY_DENIED"
    dev_id = d["user"]["id"]
    assert A.update_member_role(dev_id, "auditor")["role"] == "auditor"
    assert {m["user_id"]: m["role"] for m in A.list_members()["items"]}[dev_id] == "auditor"
    # a second workspace for the owner, switch, and switching to a foreign one is 404
    ws2 = A.create_workspace("SDK side")
    assert ws2["role"] == "owner"
    assert A.switch_workspace(ws2["id"])["workspace"]["id"] == ws2["id"]
    assert A.switch_workspace(ws)["workspace"]["id"] == ws
    with pytest.raises(BoxmesError) as ei:
        D.switch_workspace(ws2["id"])
    assert ei.value.status == 404
    # revoke an open invite, remove the member
    inv2 = A.create_invite(f"later-{tag}@example.com", "viewer")
    A.revoke_invite(inv2["id"])
    assert all(i["id"] != inv2["id"] for i in A.list_invites()["items"])
    A.remove_member(dev_id)
    assert [m["user_id"] for m in A.list_members()["items"]] == [a["user"]["id"]]
    usage = A.workspace_usage()
    assert usage["plan"] == "pro" and usage["used"]["members"] == 1 and usage["quota"]["max_boxes"] == 10
    types = {e["type"] for e in A.workspace_audit(limit=200)["items"]}
    assert {"member.invited", "member.joined", "member.role_changed", "member.removed"} <= types


def test_box_locations_live(core):
    with Boxmes(core["key"], core["url"]) as bx:
        box = bx.up(template="base", lifecycle="session", slug="sdk-py-loc")
        try:
            assert box["location"]["region"] == "local" and box["location"]["site"].startswith("local")
            ids = [b["id"] for b in bx.list_boxes(region="local")["items"]]
            assert box["id"] in ids
            assert box["id"] not in [b["id"] for b in bx.list_boxes(region="eu")["items"]]
            assert box["id"] in [b["id"] for b in bx.list_boxes(location=box["location"]["site"])["items"]]
            loc = bx.box_locations()
            assert loc["total"]["total"] >= 1 and any(r["region"] == "local" for r in loc["regions"])
        finally:
            bx.down(box["id"])
