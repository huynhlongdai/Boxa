# boxmes — Python SDK

Typed sync + async client for [Boxmes](../../core/README.md): cheap, disposable Linux boxes for AI agents.
Works with zero dependencies (stdlib `urllib`); uses `httpx` automatically when installed.

```bash
pip install ./sdk/python            # or: pip install "./sdk/python[httpx]"
export BOXMES_URL=http://127.0.0.1:8080 BOXMES_API_KEY=bx_agent_... BOXMES_LANG=vi   # BOXMES_LANG optional: vi|en|zh|ja
```

```python
from boxmes import Boxmes, BoxmesError

bx = Boxmes()                                      # or Boxmes(api_key, base_url, lang="ja")
print(bx.quote("python", "task", ram_mb=1024, ttl_seconds=1800))   # dry-run price, creates nothing
box = bx.create_box("python", "task", ttl_seconds=1800, idempotency_key="job-42")
r = bx.exec(box["id"], "python3 -c 'print(42)'")
print(r["stdout_tail"], r["next"] if "next" in r else "")
res = bx.exec_batch(box["id"], ["pip install -q requests", "python3 main.py"])   # stop_on_error=True
job = bx.run(box["id"], "make test")             # async exec + long-poll ?wait=30
snap = bx.snapshot(box["id"]); alt = bx.fork(box["id"], snap["id"]); bx.restore(box["id"], snap["id"])
print(bx.box_yaml(box["id"])["yaml"])
bx.down(box["id"])

try:
    bx.get_box("box_nope")
except BoxmesError as e:                           # e.code is stable; message/hint follow lang
    print(e.code, e.message, e.hint, e.retry_after_s, e.docs_url)
```

Async: `AsyncBoxmes` has the same methods (`await bx.exec(...)`, `async with AsyncBoxmes() as bx:`).
Retryable codes (`NO_CAPACITY`, `PROVIDER_FAILED`, `RATE_LIMITED`, `INTERNAL`) are retried `max_retries`
times (default 2), honouring `retry_after_s`. Loopback URLs bypass `HTTP(S)_PROXY`.

### v1.4 — sandbox API providers (T2/T3)

```python
for p in bx.sandbox_providers()["items"]:          # E2B, Daytona, Prized, Runloop, Vercel Sandbox, Upstash Box…
    print(p["id"], p["tier"], p["mode"], p["verification"], p["from_usd_h"], p["capabilities"]["fork"])
rec = bx.recommend(template="base", untrusted=True, fork=True, isolation="sandbox", lifetime="ephemeral")
print(rec["pick"]["provider"], rec["pick"]["tier"], [r["reason"] for r in rec["rejected"]])
admin = Boxmes(admin_key, base_url)
print(admin.lint_adapter(open("my-sandbox.yaml").read()))   # {ok, error?, info?} — admin key
```

### v2.0 — workspaces, roles, quotas, workspace keys, box locations

```python
me = Boxmes(session_token, base_url)                 # bxs_… session (POST /v1/auth/login) or a workspace key
print(me.me()["role"], me.get_workspace()["quota"])  # owner | admin | developer | billing | viewer | auditor
me.switch_workspace("ws_…"); me.create_workspace("Side project"); me.rename_workspace("Team A")
print(me.workspace_usage()["used"], me.workspace_audit(type="member.invited", limit=50)["items"])
inv = me.create_invite("dev@example.com", "developer")     # inv["invite_url"] shown once (…/invite/bxi_…)
Boxmes(None, base_url).get_invite(token)                    # preview, no auth; invitee: accept_invite(token)
me.list_members(); me.update_member_role(user_id, "viewer"); me.remove_member(user_id)
me.list_invites(); me.revoke_invite(invite_id)
key = me.create_key(None, "ci", ["boxes:read", "boxes:write", "exec"], expires_in_days=30)["key"]
me.list_boxes(region="eu", location="hetzner-fsn"); me.box_locations()
try:
    me.create_box("base")
except BoxmesError as e:
    if e.code == "QUOTA_EXCEEDED": print(e.details)          # {quota, limit, used, plan, upgrade_url}
# platform admin: admin_workspaces(), admin_update_workspace(ws_id, plan="pro", quota_overrides={...})
# v2.2 provider accounts: admin_providers(), list_provider_accounts("hetzner"), create_provider_account("hetzner", "EU #2", token, priority=2),
#   update_provider_account(p, a, balance=150), rotate_provider_account(p, a, token, external_id=...), set_provider_strategy("e2b", "drain_first"), infra_alerts()
# v2.2 M29-S sandbox accounts: provider_sandbox_accounts("e2b")  # concurrency gauges + template matrix
#   sync_account_templates("e2b", "pa_1"); update_provider_account("e2b", "pa_1", limit_group="prj-a", billing_group="ws-1", templates=["agent"])
```

## Function calling with any LLM

Tool schemas come from the server (`GET /v1/tools`, the same source as the MCP server); tool calls are
executed through `/mcp` so argument mapping is identical to MCP.

```python
from openai import OpenAI
from boxmes import Boxmes, as_openai_tools, handle_tool_call

bx, llm = Boxmes(), OpenAI()
messages = [{"role": "user", "content": "Run python3 -c 'print(6*7)' in a Boxmes box and tell me the result."}]
while True:
    resp = llm.chat.completions.create(model="gpt-4.1", messages=messages, tools=as_openai_tools(bx))
    msg = resp.choices[0].message
    messages.append(msg)
    if not msg.tool_calls:
        print(msg.content); break
    for call in msg.tool_calls:
        messages.append({"role": "tool", "tool_call_id": call.id,
                         "content": handle_tool_call(call.function.name, call.function.arguments, bx)})
```

Also: `as_openai_tools(bx, responses_api=True)`, `as_anthropic_tools(bx)`, `as_gemini_tools(bx)`,
`bx.call_tool("box_exec", {...})`, `ahandle_tool_call(...)` for async.

## Tests

```bash
(cd ../../core && go build -o bin/ ./cmd/...)    # tests start core + boxd (local driver) from core/bin
python3 -m pytest -q tests                        # or BOXMES_TEST_URL=… BOXMES_TEST_ADMIN_KEY=… against a running core
```
Tests skip when no core binary / URL is available.
