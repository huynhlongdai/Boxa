# @boxmes/sdk — TypeScript SDK

Typed, fetch-based, **zero-dependency** client for [Boxmes](../../core/README.md). Runs on Node ≥ 18, Bun,
Deno, browsers and edge runtimes.

```bash
npm i ./sdk/typescript     # build: npm run build (tsc → dist/)
export BOXMES_URL=http://127.0.0.1:8080 BOXMES_API_KEY=bx_agent_... BOXMES_LANG=vi
```

```ts
import { Boxmes, BoxmesError } from "@boxmes/sdk";

const bx = new Boxmes();                                   // or new Boxmes({ apiKey, baseUrl, lang: "ja" })
const q = await bx.quote({ template: "node", lifecycle: "task", ttl_seconds: 1800 });   // dry run
const box = await bx.createBox({ template: "node", lifecycle: "task", ttl_seconds: 1800 }, { idempotencyKey: "job-42" });
const r = await bx.exec(box.id, "node -e 'console.log(42)'");          // ExecResult (+ r.next hints)
const ref = await bx.exec(box.id, "npm test", { async: true });         // JobRef
const job = await bx.job(ref.job_id, 30);                               // long-poll
const batch = await bx.execBatch(box.id, ["npm i", "npm test"]);        // stop_on_error default true
const snap = await bx.snapshot(box.id);
const alt = await bx.fork(box.id, { snapshot_id: snap.id });
await bx.restore(box.id, snap.id);
console.log((await bx.boxYaml(box.id)).yaml);
await bx.down(box.id);

try { await bx.getBox("box_nope"); }
catch (e) { if (e instanceof BoxmesError) console.log(e.code, e.apiMessage, e.hint, e.retryAfterS, e.docsUrl); }
```

### v1.4 — sandbox API providers (T2/T3)

```ts
const { items } = await bx.sandboxProviders();          // tier, mode (dial|shim), capabilities, per-second pricing, verification
const rec = await bx.recommend({ template: "base", untrusted: true, fork: true, requirements: { isolation: "sandbox" } });
console.log(rec.pick?.provider, rec.pick?.tier, rec.rejected.map((r) => r.reason));
const lint = await new Boxmes({ apiKey: adminKey }).lintAdapter(yamlText);   // { ok, error?, info? } — admin key
```

### v2.0 — workspaces, roles, quotas, workspace keys, box locations

```ts
const me = new Boxmes({ apiKey: sessionToken });            // bxs_… session or a workspace key
const { role, permissions } = await me.me();                // owner | admin | developer | billing | viewer | auditor
await me.switchWorkspace("ws_…"); await me.createWorkspace("Side project"); await me.renameWorkspace("Team A");
const { quota, used } = await me.workspaceUsage(); await me.workspaceAudit({ type: "member.invited", limit: 50 });
const inv = await me.createInvite("dev@example.com", "developer");   // inv.invite_url shown once
await new Boxmes({ apiKey: "" }).getInvite(token); /* invitee: */ await me.acceptInvite(token);
await me.listMembers(); await me.updateMemberRole(userId, "viewer"); await me.removeMember(userId);
await me.listInvites(); await me.revokeInvite(inviteId);
const { key } = await me.createKey(null, "ci", ["boxes:read", "boxes:write", "exec"], { expires_in_days: 30 });
await me.listBoxes({ region: "eu", location: "hetzner-fsn" }); await me.boxLocations();
// QUOTA_EXCEEDED → BoxmesError.details = { quota, limit, used, plan, upgrade_url }
// platform admin: adminWorkspaces(), adminUpdateWorkspace(wsId, { plan: "pro", quota_overrides: { max_boxes: 20 } })
// v2.2 provider accounts: adminProviders(), listProviderAccounts("hetzner"), createProviderAccount("hetzner", { label, token, priority: 2 }),
//   updateProviderAccount(p, a, { balance: 150 }), rotateProviderAccount(p, a, token, externalId), setProviderStrategy("e2b", { strategy: "drain_first" }), infraAlerts()
```

## Function calling

```ts
import OpenAI from "openai";
import { Boxmes, asOpenAITools, handleToolCall } from "@boxmes/sdk";

const bx = new Boxmes(), llm = new OpenAI();
const tools = await asOpenAITools(bx);                    // from GET /v1/tools (same source as MCP)
const messages: any[] = [{ role: "user", content: "Run node -e 'console.log(6*7)' in a box." }];
for (;;) {
  const res = await llm.chat.completions.create({ model: "gpt-4.1", messages, tools });
  const msg = res.choices[0].message; messages.push(msg);
  if (!msg.tool_calls?.length) { console.log(msg.content); break; }
  for (const c of msg.tool_calls)
    messages.push({ role: "tool", tool_call_id: c.id, content: await handleToolCall(c.function.name, c.function.arguments, bx) });
}
```

Also `asAnthropicTools`, `asGeminiTools`, `asToolMap` (`{ name: { description, parameters, execute } }`),
and `bx.callTool(name, args)`.

## Tests

```bash
(cd ../../core && go build -o bin/ ./cmd/...)
npm test        # node --experimental-strip-types --test test/*.test.ts  (starts core + boxd from core/bin)
npm run typecheck
```
Integration tests skip when no core binary / `BOXMES_TEST_URL` is available.
