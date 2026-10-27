# boxaz — Python SDK

Typed sync + async client for [Boxaz](../../core/README.md): cheap, disposable Linux boxes for AI agents.
Works with zero dependencies (stdlib `urllib`); uses `httpx` automatically when installed.

```bash
pip install ./sdk/python            # or: pip install "./sdk/python[httpx]"
export BOXAZ_URL=http://127.0.0.1:8080 BOXAZ_API_KEY=bx_agent_... BOXAZ_LANG=vi   # BOXAZ_LANG optional: vi|en|zh|ja
```

```python
from boxaz import Boxaz, BoxazError

bx = Boxaz()                                      # or Boxaz(api_key, base_url, lang="ja")
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
except BoxazError as e:                           # e.code is stable; message/hint follow lang
    print(e.code, e.message, e.hint, e.retry_after_s, e.docs_url)
```

Async: `AsyncBoxaz` has the same methods (`await bx.exec(...)`, `async with AsyncBoxaz() as bx:`).
Retryable codes (`NO_CAPACITY`, `PROVIDER_FAILED`, `RATE_LIMITED`, `INTERNAL`) are retried `max_retries`
times (default 2), honouring `retry_after_s`. Loopback URLs bypass `HTTP(S)_PROXY`.

### v1.4 — sandbox API providers (T2/T3)

```python
for p in bx.sandbox_providers()["items"]:          # E2B, Daytona, Prized, Runloop, Vercel Sandbox, Upstash Box…
    print(p["id"], p["tier"], p["mode"], p["verification"], p["from_usd_h"], p["capabilities"]["fork"])
rec = bx.recommend(template="base", untrusted=True, fork=True, isolation="sandbox", lifetime="ephemeral")
print(rec["pick"]["provider"], rec["pick"]["tier"], [r["reason"] for r in rec["rejected"]])
admin = Boxaz(admin_key, base_url)
print(admin.lint_adapter(open("my-sandbox.yaml").read()))   # {ok, error?, info?} — admin key
```

## Function calling with any LLM

Tool schemas come from the server (`GET /v1/tools`, the same source as the MCP server); tool calls are
executed through `/mcp` so argument mapping is identical to MCP.

```python
from openai import OpenAI
from boxaz import Boxaz, as_openai_tools, handle_tool_call

bx, llm = Boxaz(), OpenAI()
messages = [{"role": "user", "content": "Run python3 -c 'print(6*7)' in a Boxaz box and tell me the result."}]
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
python3 -m pytest -q tests                        # or BOXAZ_TEST_URL=… BOXAZ_TEST_ADMIN_KEY=… against a running core
```
Tests skip when no core binary / URL is available.
