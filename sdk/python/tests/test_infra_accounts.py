"""v2.2 provider-account admin helpers: offline request shapes (mocked transport)."""

import json
import urllib.parse

from boxmes import Boxmes


class _Recorder:
    def __init__(self):
        self.calls = []

    def __call__(self, method, url, data, headers):
        u = urllib.parse.urlparse(url)
        self.calls.append({"method": method, "path": u.path, "query": dict(urllib.parse.parse_qsl(u.query)),
                           "body": json.loads(data) if data else None})
        return 200, "application/json", b'{"ok": true}'


def test_provider_account_routes_offline(monkeypatch):
    rec = _Recorder()
    bx = Boxmes("bx_admin_x", "http://core.test:9", max_retries=0)
    monkeypatch.setattr(bx, "_send", rec)
    bx.admin_providers()
    bx.admin_provider("hetzner")
    bx.list_provider_accounts("hetzner", archived=True)
    bx.validate_provider_account("hetzner", "tok", external_id="p2")
    bx.create_provider_account("hetzner", "Hetzner EU #2", "tok", priority=2, currency="EUR")
    bx.get_provider_account("hetzner", "pa_1")
    bx.update_provider_account("hetzner", "pa_1", balance=150, limits={"servers": 25})
    bx.rotate_provider_account("hetzner", "pa_1", "tok2", external_id="p2")
    bx.test_provider_account("hetzner", "pa_1")
    bx.set_provider_account_state("hetzner", "pa_1", "drain")
    bx.delete_provider_account("hetzner", "pa_1", force=True, reason="moved")
    bx.provider_strategy("e2b")
    bx.set_provider_strategy("e2b", "drain_first", sticky_minutes=30)
    bx.infra_alerts("all")
    bx.ack_infra_alert("low_balance|pa_1")
    got = [(c["method"], c["path"]) for c in rec.calls]
    base = "/v1/admin/providers/hetzner"
    assert got == [
        ("GET", "/v1/admin/providers"), ("GET", base), ("GET", base + "/accounts"), ("POST", base + "/accounts/validate"),
        ("POST", base + "/accounts"), ("GET", base + "/accounts/pa_1"), ("PATCH", base + "/accounts/pa_1"),
        ("POST", base + "/accounts/pa_1/rotate"), ("POST", base + "/accounts/pa_1/test"), ("POST", base + "/accounts/pa_1/drain"),
        ("DELETE", base + "/accounts/pa_1"), ("GET", "/v1/admin/providers/e2b/strategy"), ("PUT", "/v1/admin/providers/e2b/strategy"),
        ("GET", "/v1/admin/infra/alerts"), ("POST", "/v1/admin/infra/alerts/low_balance%7Cpa_1/ack"),
    ]
    assert rec.calls[2]["query"] == {"archived": "1"}
    assert rec.calls[4]["body"] == {"label": "Hetzner EU #2", "token": "tok", "priority": 2, "currency": "EUR"}
    assert rec.calls[6]["body"] == {"balance": 150, "limits": {"servers": 25}}
    assert rec.calls[7]["body"] == {"token": "tok2", "external_id": "p2"}
    assert rec.calls[10]["query"] == {"force": "1", "reason": "moved"}
    assert rec.calls[12]["body"] == {"strategy": "drain_first", "sticky_minutes": 30}
    assert rec.calls[13]["query"] == {"status": "all"}


def test_sandbox_account_routes_offline(monkeypatch):
    rec = _Recorder()
    bx = Boxmes("bx_admin_x", "http://core.test:9", max_retries=0)
    monkeypatch.setattr(bx, "_send", rec)
    bx.provider_sandbox_accounts("e2b")
    bx.sync_account_templates("e2b", "pa_1")
    bx.update_provider_account("e2b", "pa_1", templates=["agent"], limit_group="prj-a", billing_group="ws-1",
                               clear_templates_missing=True, limits={"concurrent_sandboxes": 20, "create_per_sec": 1})
    got = [(c["method"], c["path"]) for c in rec.calls]
    assert got == [("GET", "/v1/admin/providers/e2b/sandbox"), ("POST", "/v1/admin/providers/e2b/accounts/pa_1/templates/sync"),
                   ("PATCH", "/v1/admin/providers/e2b/accounts/pa_1")]
    assert rec.calls[1]["body"] is None
    assert rec.calls[2]["body"]["limit_group"] == "prj-a"


def test_balance_history_routes_offline(monkeypatch):
    rec = _Recorder()
    bx = Boxmes("bx_admin_x", "http://core.test:9", max_retries=0)
    monkeypatch.setattr(bx, "_send", rec)
    bx.account_balance_history("runpod", "pa_1", days=30)
    bx.provider_balance_history("runpod")
    got = [(c["method"], c["path"]) for c in rec.calls]
    assert got == [("GET", "/v1/admin/providers/runpod/accounts/pa_1/balance/history"),
                   ("GET", "/v1/admin/providers/runpod/balance/history")]
    assert rec.calls[0]["query"] == {"days": "30"}
    assert rec.calls[1]["query"] == {"days": "7"}
    assert rec.calls[0]["body"] is None
