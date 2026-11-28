// Runs against a real local core (core/bin) or BOXMES_TEST_URL + BOXMES_TEST_ADMIN_KEY; skips otherwise.
// node --experimental-strip-types --test test/sdk.test.ts
import { test, before, after } from "node:test";
import assert from "node:assert/strict";
import { spawn, type ChildProcess } from "node:child_process";
import { existsSync, mkdtempSync, openSync, rmSync } from "node:fs";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";
import { Boxa, BoxaError, Boxaz, BoxazError, Boxmes, BoxmesError, asOpenAITools, asAnthropicTools, asGeminiTools, asToolMap, handleToolCall } from "../src/index.ts";

const here = dirname(fileURLToPath(import.meta.url));
const bin = join(here, "..", "..", "..", "core", "bin");
const procs: ChildProcess[] = [];
let work = "";
let url = process.env.BOXMES_TEST_URL ?? "";
let admin = process.env.BOXMES_TEST_ADMIN_KEY ?? "";
let key = "";
let agentId = "";
const skip = !url && !(existsSync(join(bin, "boxmes-core")) && existsSync(join(bin, "boxd")));

const freePort = () => new Promise<number>((res) => { const s = createServer(); s.listen(0, "127.0.0.1", () => { const p = (s.address() as { port: number }).port; s.close(() => res(p)); }); });
const wait = (ms: number) => new Promise((r) => setTimeout(r, ms));

before(async () => {
  if (skip) return;
  if (!url) {
    work = mkdtempSync(join(tmpdir(), "boxmes-sdk-ts-"));
    const port = await freePort();
    url = `http://127.0.0.1:${port}`;
    admin = "bx_admin_sdk_ts";
    procs.push(spawn(join(bin, "boxmes-core"), [], { env: { ...process.env, BOXMES_DATA_DIR: `${work}/data`, BOXMES_ADMIN_KEY: admin, BOXMES_BOOTSTRAP_TOKEN: "boot-ts", BOXMES_ADDR: `127.0.0.1:${port}`, BOXMES_PRICE_FEED: "off", BOXMES_LOCAL_PRICE_MONTH: "73" }, stdio: ["ignore", "ignore", openSync(`${work}/core.log`, "w")] }));
    for (let i = 0; i < 100; i++) { try { if ((await fetch(url + "/v1/health")).ok) break; } catch { /* starting */ } await wait(100); }
    procs.push(spawn(join(bin, "boxd"), [], { env: { ...process.env, BOXD_CORE_URL: url, BOXD_TOKEN: "boot-ts", BOXD_DRIVER: "local", BOXD_STATE_DIR: `${work}/boxd`, BOXD_IP: "127.0.0.1" }, stdio: ["ignore", "ignore", openSync(`${work}/boxd.log`, "w")] }));
    const adm = new Boxmes({ apiKey: admin, baseUrl: url });
    for (let i = 0; i < 100; i++) {
      const hs = await adm.request<{ items: { connected: boolean }[] }>("GET", "/hosts");
      if (hs.items.some((h) => h.connected)) break;
      await wait(100);
    }
  }
  const adm = new Boxmes({ apiKey: admin, baseUrl: url });
  const agent = await adm.createAgent({ name: `sdk-ts-${Date.now()}`, kind: "vercel-ai-sdk", budget_day_usd: 5 });
  agentId = agent.id;
  key = (await adm.createKey(agent.id, "sdk-ts")).key!;
});

after(() => {
  for (const p of procs) p.kill();
  if (work) rmSync(work, { recursive: true, force: true });
});

test("env defaults and url building", () => {
  process.env.BOXMES_URL = "http://example.test:9/v1/";
  process.env.BOXMES_API_KEY = "bx_agent_x";
  process.env.BOXMES_LANG = "vi";
  const bx = new Boxmes();
  delete process.env.BOXMES_URL; delete process.env.BOXMES_API_KEY; delete process.env.BOXMES_LANG;
  assert.equal(bx.baseUrl, "http://example.test:9");
  assert.equal(bx.headers().Authorization, "Bearer bx_agent_x");
  assert.equal(bx.headers()["Accept-Language"], "vi");
  assert.equal(bx.url("/llms.txt"), "http://example.test:9/llms.txt");
  assert.equal(bx.url("/boxes", { all: true, status: undefined }), "http://example.test:9/v1/boxes?all=true");
});

test("legacy BOXA_* env + deprecated aliases", () => {
  const saved = { ...process.env };
  try {
    delete process.env.BOXMES_URL; delete process.env.BOXMES_API_KEY;
    process.env.BOXA_URL = "http://legacy.test:9";
    process.env.BOXA_API_KEY = "bx_agent_old";
    const bx = new Boxmes();
    assert.equal(bx.baseUrl, "http://legacy.test:9");
    assert.equal(bx.headers().Authorization, "Bearer bx_agent_old");
    process.env.BOXMES_URL = "http://new.test:9";
    assert.equal(new Boxmes().baseUrl, "http://new.test:9");
    assert.equal(Boxa, Boxmes);
    assert.equal(BoxaError, BoxmesError);
  } finally {
    process.env = saved;
  }
});

test("legacy BOXAZ_* env wins over BOXA_* + Boxaz aliases", () => {
  const saved = { ...process.env };
  try {
    delete process.env.BOXMES_URL; delete process.env.BOXMES_API_KEY; delete process.env.BOXMES_LANG;
    process.env.BOXAZ_URL = "http://boxaz.test:9";
    process.env.BOXA_URL = "http://boxa.test:9";
    process.env.BOXAZ_API_KEY = "bx_agent_boxaz";
    process.env.BOXA_LANG = "ja";
    const bx = new Boxmes();
    assert.equal(bx.baseUrl, "http://boxaz.test:9");
    assert.equal(bx.headers().Authorization, "Bearer bx_agent_boxaz");
    assert.equal(bx.headers()["Accept-Language"], "ja");
    process.env.BOXMES_URL = "http://new.test:9";
    assert.equal(new Boxmes().baseUrl, "http://new.test:9");
    assert.equal(Boxaz, Boxmes);
    assert.equal(BoxazError, BoxmesError);
    const typed: Boxaz = new Boxaz({ baseUrl: "http://t.test:9" });
    assert.ok(typed instanceof Boxmes);
  } finally {
    process.env = saved;
  }
});

test("discovery + tool formats", { skip }, async () => {
  const bx = new Boxmes({ apiKey: key, baseUrl: url });
  assert.equal((await bx.health()).ok, true);
  assert.equal((await bx.agentKinds()).items.length, 22);
  const oa = await asOpenAITools(bx);
  assert.equal(oa.length, 32);
  assert.ok(oa.some((t) => t.function.name === "box_exec_batch"));
  assert.equal((await asAnthropicTools(bx))[0].input_schema.type, "object");
  assert.equal((await asGeminiTools(bx))[0].functionDeclarations.length, 32);
  assert.ok((await bx.llmsTxt()).startsWith("# Boxmes"));
  assert.equal((await bx.openapi()).openapi, "3.1.0");
  const sn = await new Boxmes({ apiKey: admin, baseUrl: url }).snippet(agentId, undefined, "zh");
  assert.equal(sn.kind, "vercel-ai-sdk");
  assert.equal(sn.language, "typescript");
  assert.equal(sn.locale, "zh");
});

test("localized errors", { skip }, async () => {
  const bx = new Boxmes({ apiKey: key, baseUrl: url, lang: "ja" });
  await assert.rejects(bx.getBox("box_nope"), (e: unknown) => {
    assert.ok(e instanceof BoxmesError);
    assert.equal(e.code, "NOT_FOUND");
    assert.equal(e.status, 404);
    assert.match(e.apiMessage, /見つかりません/);
    assert.match(e.docsUrl ?? "", /#NOT_FOUND$/);
    assert.equal(e.retryable, false);
    return true;
  });
});

test("box lifecycle: quote, create, exec, batch, run, restore, fork, tools", { skip }, async () => {
  const bx = new Boxmes({ apiKey: key, baseUrl: url });
  const q = await bx.quote({ template: "node", lifecycle: "task", ttl_seconds: 3600, spec: { ram_mb: 256 } });
  assert.equal(q.available, true);
  assert.equal(q.expected_hours, 1);
  const box = await bx.createBox({ template: "node", lifecycle: "session", slug: "sdk-ts", spec: { ram_mb: 256 } }, { idempotencyKey: "sdk-ts-1" });
  assert.equal(box.status, "running");
  assert.ok(box.next && box.next.length > 0);
  assert.equal((await bx.createBox({ template: "node" }, { idempotencyKey: "sdk-ts-1" })).id, box.id);
  const r = await bx.exec(box.id, "seq 1 200; exit 3", { tail_lines: 2 });
  assert.equal(r.exit_code, 3);
  assert.ok(r.log_id && r.truncated);
  assert.ok(r.next?.some((n) => n.startsWith("box_logs(")));
  assert.match(await bx.logs(r.log_id!), /\n100\n/);
  const b = await bx.execBatch(box.id, ["echo a", { command: "false" }, "echo c"]);
  assert.equal(b.ok, false);
  assert.equal(b.stopped_at, 1);
  assert.equal((await bx.execBatch(box.id, ["echo a", "false", "echo c"], { stop_on_error: false })).completed, 3);
  const j = await bx.run(box.id, "sleep 0.3; echo ts-async");
  assert.equal(j.status, "done");
  assert.equal(j.result?.stdout_tail, "ts-async");
  await bx.write(box.id, "/work/s.txt", "v1\n");
  const snap = await bx.snapshot(box.id);
  await bx.write(box.id, "/work/s.txt", "v2\n");
  await bx.restore(box.id, snap.id);
  assert.equal((await bx.read(box.id, "/work/s.txt")).content, "v1\n");
  const fork = await bx.fork(box.id, { snapshot_id: snap.id, slug: "sdk-ts-fork" });
  assert.equal(fork.forked_from, snap.id);
  assert.equal((await bx.read(fork.id, "/work/s.txt")).content, "v1\n");
  assert.match((await bx.boxYaml(box.id)).yaml, /template: node/);
  const ev = await bx.events(box.id);
  assert.ok(ev.items.some((e) => e.type === "box.exec" && typeof e.data?.tokens_saved === "number"));
  // tool bridge
  const out = JSON.parse(await handleToolCall("box_exec", JSON.stringify({ box_id: box.id, command: "echo via-tool" }), bx));
  assert.equal(out.stdout_tail, "via-tool");
  const err = JSON.parse(await handleToolCall("box_exec", { box_id: "box_nope", command: "ls" }, bx));
  assert.equal(err.error.code, "NOT_FOUND");
  const map = await asToolMap(bx);
  const listed = (await map.box_list.execute({})) as { items: { id: string }[] };
  assert.ok(listed.items.some((x) => x.id === box.id));
  await bx.down(fork.id);
  assert.equal((await bx.deleteBox(box.id)).status, "terminated");
});

test("v1.3 recommend / ensure / clone / session exec", { skip }, async () => {
  const bx = new Boxmes({ apiKey: key, baseUrl: url });
  const pr = await bx.profiles();
  assert.equal(pr.items.length, 7);
  assert.equal(pr.kinds.length, 22);
  const rec = await bx.recommend({ profile: "general", requirements: { ram_mb: 256, disk_gb: 1, max_price_hour: 1 } });
  assert.ok(rec.pick && rec.pick.reasons.length > 0 && Object.keys(rec.pick.breakdown).length === 6);
  const e1 = await bx.ensure("sdk-ts-ensure", { template: "base", spec: { cpu: 0.5, ram_mb: 128, disk_gb: 1 } });
  assert.equal(e1.action, "created");
  await bx.exec(e1.box.id, "mkdir -p t && cd t && export K=7", { session: true });
  const r = await bx.exec(e1.box.id, "pwd; echo K=$K", { session: true });
  assert.ok(r.stdout_tail.includes("/t") && r.stdout_tail.includes("K=7"));
  const e2 = await bx.ensure("sdk-ts-ensure");
  assert.equal(e2.action, "reused");
  assert.equal(e2.box.id, e1.box.id);
  const c = await bx.clone(e1.box.id, { slug: "sdk-ts-clone" });
  assert.equal(c.cloned_from, e1.box.id);
  assert.equal(c.estimate?.kind, "clone");
  assert.equal((await bx.resume(e1.box.id)).box.id, e1.box.id);
  await bx.down(c.id);
  await bx.down(e1.box.id);
});

test("v1.4 sandbox providers / adapter lint / recommend untrusted+fork", { skip }, async () => {
  const bx = new Boxmes({ apiKey: key, baseUrl: url });
  const sp = await bx.sandboxProviders();
  const ids = new Map(sp.items.map((p) => [p.id, p]));
  for (const id of ["e2b", "novita", "daytona", "prized", "boxdsh", "runloop", "blaxel", "vercel", "boat", "agent37", "upstash-box", "render"]) assert.ok(ids.has(id), id);
  assert.equal(ids.get("prized")?.verification, "live-verified");
  assert.equal(ids.get("upstash-box")?.mode, "shim");
  assert.ok(sp.items.every((p) => p.docs && p.initials && (p.from_usd_h > 0 || p.id === "render") && typeof p.capabilities.fork === "boolean"));
  await assert.rejects(bx.lintAdapter("id: x"), (e: unknown) => e instanceof BoxmesError && ["UNAUTHORIZED", "POLICY_DENIED"].includes(e.code));
  const adm = new Boxmes({ apiKey: admin, baseUrl: url });
  const bad = await adm.lintAdapter("id: x\nname: X\n");
  assert.equal(bad.ok, false);
  assert.match(bad.error ?? "", /missing/);
  const good = await adm.lintAdapter("id: acme\nname: Acme\nbase_url: https://api.acme.test\nvalidate: {method: GET, path: /x}\ncreate: {method: POST, path: /x, capture: {id: $.id}}\nget: {method: GET, path: /x}\ndelete: {method: DELETE, path: /x}\nexec: [{method: POST, path: /x}]\n");
  assert.equal(good.ok, true);
  assert.equal(good.info?.adapter, "yaml");
  const rec = await adm.recommend({ template: "base", untrusted: true, fork: true, requirements: { isolation: "sandbox", cpu: 1, ram_mb: 1024, lifetime: "ephemeral" } });
  assert.equal(rec.requirements.untrusted, true);
  assert.equal(rec.requirements.fork, true);
  assert.ok(rec.options.length > 0 && rec.options.every((o) => o.kind === "sandbox" && o.fork && o.strong_isolation && (o.tier === "T2" || o.tier === "T3")));
  assert.ok(rec.rejected.some((r) => r.provider === "daytona" && /shared kernel/.test(r.reason)));
  assert.ok(rec.rejected.some((r) => /fork/.test(r.reason)));
});

test("v1.7 supervised app / gateway wake / URL tokens / capabilities", { skip }, async () => {
  const bx = new Boxmes({ apiKey: key, baseUrl: url });
  const port = await freePort();
  const box = await bx.createBox({ template: "base", lifecycle: "session", slug: "sdk-ts-app" });
  try {
    const v = await bx.setApp(box.id, { start: "exec python3 -m http.server $PORT --bind 127.0.0.1", install: "echo ts-sdk > index.html", port, ready: { http: "/" }, workdir: "/work/site" });
    assert.equal(v.spec?.restart, "always");
    let st = v.status;
    for (let i = 0; i < 80 && st.state !== "ready"; i++) { await wait(250); st = (await bx.app(box.id)).status; }
    assert.equal(st.state, "ready");
    const p = (await bx.ports(box.id)).ports.find((x) => x.port === port)!;
    const gw = p.gateway_url!.startsWith("/") ? url + p.gateway_url : p.gateway_url!;
    assert.equal(await (await fetch(gw)).text(), "ts-sdk\n");
    const t = await bx.createUrlToken(box.id, port, { ttl_s: 600 });
    assert.ok(t.token?.startsWith("bxt_"));
    assert.equal((await fetch(gw)).status, 401);
    assert.equal((await fetch(gw, { headers: { Authorization: `Bearer ${t.token}` } })).status, 200);
    await bx.revokeUrlToken(box.id, port, t.id);
    assert.equal((await fetch(gw, { headers: { "X-Boxmes-Token": t.token! } })).status, 401);
    await bx.setPortAccess(box.id, port, "public");
    await bx.sleep(box.id);
    assert.equal(await (await fetch(gw)).text(), "ts-sdk\n"); // wake on request
    assert.equal((await bx.setWakeOnRequest(box.id, false)).wake_on_request, false);
    const caps = new Map((await bx.capabilities()).items.map((c) => [c.provider, c]));
    assert.ok(caps.get("e2b")?.wake_on_http_native && !caps.get("prized")?.inbound_http);
    assert.ok(caps.get("e2b")?.private_preview && !caps.get("prized")?.private_preview);
    const gi = await bx.gateway();
    assert.ok(["host", "path", "relative"].includes(gi.mode) && gi.reserved.includes("hark"));
    // v1.9 readable names
    assert.ok(p.gateway_url!.endsWith("/x/sdk-ts-app/") && gi.url_pattern.endsWith("<name>/"));
    assert.equal((await bx.renameUrl(box.id, "TS Shop")).subdomain, "ts-shop");
    const pv = await bx.ports(box.id);
    assert.ok(pv.name === "ts-shop" && pv.ports[0].gateway_url!.endsWith("/x/ts-shop/") && pv.ports[0].primary && pv.ports[0].alias_url!.endsWith(`-${port}/`));
    assert.equal((await bx.setPortLabel(box.id, port, "site")).label, "site");
    await assert.rejects(bx.renameUrl(box.id, "hark"), /reserved/);
  } finally {
    await bx.deleteBox(box.id);
  }
});

test("v1.10 access: tickets, forward allowlist, terminals, ssh keys", { skip }, async () => {
  const bx = new Boxmes({ apiKey: key, baseUrl: url });
  const box = await bx.createBox({ template: "base", lifecycle: "session", slug: "sdk-ts-acc" });
  try {
    const t = await bx.ticket(box.id, "terminal");
    assert.ok(t.ticket!.startsWith("bxa_") && t.url!.includes("ticket=bxa_") && t.url!.includes("/terminal"));
    await assert.rejects(bx.ticket(box.id, "tcp", { port: 6392 }), /allowlist/);
    const tt = await bx.ticket(box.id, "tcp", { port: 6392, allow: true });
    assert.ok(tt.url!.includes("port=6392"));
    const items = (await bx.tickets(box.id)).items;
    assert.ok(items.length === 2 && items.every((i) => !i.ticket));
    assert.equal((await bx.revokeTicket(box.id)).revoked, 2);
    assert.ok((await bx.allowForwardPort(box.id, 6393)).allowed_ports.includes(6392));
    const a = await bx.access(box.id);
    assert.ok(a.commands.forward.startsWith("box forward ") && a.tcp.allowed_ports.includes(6393) && a.ssh.config.includes("ssh-proxy"));
    assert.deepEqual((await bx.terminals(box.id)).items, []);
    assert.equal((await bx.sshInfo()).host_key_alias, "boxmes-ssh");
    const k = await bx.addSshKey("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHriFbNRs2ilXN0Y7F+viZSJ0GcP4uLy8TuLjh/ttcww sdk-test");
    assert.ok(k.fingerprint.startsWith("SHA256:") && k.name === "sdk-test");
    await bx.deleteSshKey(k.id);
  } finally {
    await bx.deleteBox(box.id);
  }
});

test("v1.11 access: ssh certificates, desktop share links, browser/desktop status", { skip }, async () => {
  const bx = new Boxmes({ apiKey: key, baseUrl: url });
  const box = await bx.createBox({ template: "base", lifecycle: "session", slug: "sdk-ts-p1" });
  try {
    const c = await bx.sshCert(box.id, "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHriFbNRs2ilXN0Y7F+viZSJ0GcP4uLy8TuLjh/ttcww sdk-test", 600);
    assert.ok(c.certificate.startsWith("ssh-ed25519-cert-v01@openssh.com ") && c.principals.includes(box.id));
    await assert.rejects(bx.sshCert(box.id, "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHriFbNRs2ilXN0Y7F+viZSJ0GcP4uLy8TuLjh/ttcww", 50000), /ttl_s/);
    assert.equal((await bx.revokeTicket(box.id, c.id)).revoked, 1);
    assert.ok((await bx.sshInfo()).user_ca!.startsWith("ssh-ed25519 "));
    const s = await bx.shareDesktop(box.id, { ttl_s: 120 });
    assert.ok(s.view_only && s.url!.includes("share=bxs_"));
    assert.equal((await bx.desktopShares(box.id)).items.length, 1);
    assert.equal((await bx.revokeDesktopShares(box.id)).revoked, 1);
    assert.equal(typeof (await bx.browserStatus(box.id)).running, "boolean");
    assert.equal(typeof (await bx.desktopStatus(box.id)).running, "boolean");
    const a = await bx.access(box.id);
    assert.ok(a.commands.browser.startsWith("box browser ") && a.cdp!.url.includes("/cdp"));
  } finally {
    await bx.deleteBox(box.id);
  }
});

// ---- v2.0 tenancy ----

type Rec = { method: string; path: string; query: Record<string, string>; body: unknown; auth?: string };
function mockFetch(answers: Record<string, [number, unknown]> = {}) {
  const calls: Rec[] = [];
  const f = (async (input: string | URL | Request, init?: RequestInit) => {
    const u = new URL(String(input));
    const method = init?.method ?? "GET";
    const h = (init?.headers ?? {}) as Record<string, string>;
    calls.push({ method, path: u.pathname, query: Object.fromEntries(u.searchParams), body: init?.body ? JSON.parse(String(init.body)) : undefined, auth: h.Authorization });
    const [st, ans] = answers[`${method} ${u.pathname}`] ?? [200, { ok: true }];
    return new Response(JSON.stringify(ans), { status: st, headers: { "content-type": "application/json" } });
  }) as typeof fetch;
  return { calls, f };
}

test("v2.0 tenancy routes (mocked fetch)", async () => {
  const { calls, f } = mockFetch();
  const bx = new Boxmes({ apiKey: "bxs_session", baseUrl: "http://core.test:9", fetch: f, maxRetries: 0 });
  await bx.me();
  await bx.switchWorkspace("ws_b");
  await bx.listWorkspaces();
  await bx.createWorkspace("Team B");
  await bx.getWorkspace();
  await bx.renameWorkspace("Team B2");
  await bx.workspaceUsage();
  await bx.workspaceAudit({ type: "member.invited", limit: 50 });
  await bx.plans();
  await bx.listMembers();
  await bx.updateMemberRole("usr_1", "viewer");
  await bx.removeMember("usr_1");
  await bx.createInvite("a@example.com", "auditor");
  await bx.listInvites();
  await bx.revokeInvite("wsi_1");
  await bx.getInvite("bxi_abc");
  await bx.acceptInvite("bxi_abc");
  await bx.boxLocations();
  await bx.adminWorkspaces();
  await bx.adminUpdateWorkspace("ws_b", { plan: "pro", quota_overrides: { max_boxes: 3 } });
  assert.deepEqual(calls.map((c) => `${c.method} ${c.path}`), [
    "GET /v1/auth/me", "POST /v1/auth/workspace", "GET /v1/workspaces", "POST /v1/workspaces", "GET /v1/workspaces/current",
    "PATCH /v1/workspaces/current", "GET /v1/workspaces/current/usage", "GET /v1/workspaces/current/audit", "GET /v1/plans",
    "GET /v1/workspaces/current/members", "PATCH /v1/workspaces/current/members/usr_1", "DELETE /v1/workspaces/current/members/usr_1",
    "POST /v1/workspaces/current/invites", "GET /v1/workspaces/current/invites", "DELETE /v1/workspaces/current/invites/wsi_1",
    "GET /v1/workspace-invites/bxi_abc", "POST /v1/workspace-invites/bxi_abc/accept", "GET /v1/boxes/locations",
    "GET /v1/admin/workspaces", "PATCH /v1/admin/workspaces/ws_b",
  ]);
  assert.deepEqual(calls[1].body, { workspace_id: "ws_b" });
  assert.deepEqual(calls[3].body, { name: "Team B" });
  assert.deepEqual(calls[7].query, { type: "member.invited", limit: "50" });
  assert.deepEqual(calls[10].body, { role: "viewer" });
  assert.deepEqual(calls[12].body, { email: "a@example.com", role: "auditor" });
  assert.deepEqual(calls[19].body, { plan: "pro", quota_overrides: { max_boxes: 3 } });
  assert.ok(calls.every((c) => c.auth === "Bearer bxs_session"));
});

test("v2.0 keys with scopes/expiry, box filters, error details (mocked fetch)", async () => {
  const quota = { error: { code: "QUOTA_EXCEEDED", message: "workspace quota max_boxes exceeded (limit 1, used 1)", hint: "upgrade", details: { quota: "max_boxes", limit: 1, used: 1, plan: "free" } } };
  const { calls, f } = mockFetch({
    "POST /v1/boxes": [403, quota],
    "POST /v1/boxes/box_1/exec": [401, { error: { code: "UNAUTHORIZED", message: 'key lacks scope "exec"' } }],
    "GET /v1/boxes/box_other": [404, { error: { code: "NOT_FOUND", message: "box not found" } }],
  });
  const bx = new Boxmes({ apiKey: "bx_ws_x", baseUrl: "http://core.test:9", fetch: f, maxRetries: 2 });
  await bx.createKey(null, "ci", ["boxes:read", "exec"], { expires_in_days: 30 });
  await bx.createKey(null, "until", undefined, { expires_at: "2030-01-01T00:00:00Z" });
  await bx.listKeys();
  await bx.revokeKey("key_1");
  await bx.listBoxes({ region: "eu", location: "hetzner-fsn", provider: "hetzner", all: true });
  assert.deepEqual(calls[0].body, { agent_id: null, name: "ci", scopes: ["boxes:read", "exec"], expires_in_days: 30 });
  assert.deepEqual(calls[1].body, { agent_id: null, name: "until", expires_at: "2030-01-01T00:00:00Z" });
  assert.equal(`${calls[2].method} ${calls[2].path}`, "GET /v1/keys");
  assert.equal(`${calls[3].method} ${calls[3].path}`, "DELETE /v1/keys/key_1");
  assert.deepEqual(calls[4].query, { region: "eu", location: "hetzner-fsn", provider: "hetzner", all: "true" });
  const n = calls.length;
  await assert.rejects(bx.createBox({ template: "base" }), (e: BoxmesError) => e.status === 403 && e.code === "QUOTA_EXCEEDED" && !e.retryable && e.details.quota === "max_boxes" && e.details.limit === 1);
  assert.equal(calls.length, n + 1); // not retried
  await assert.rejects(bx.exec("box_1", "id"), (e: BoxmesError) => e.status === 401 && e.code === "UNAUTHORIZED");
  await assert.rejects(bx.getBox("box_other"), (e: BoxmesError) => e.code === "NOT_FOUND" && Object.keys(e.details).length === 0);
});

test("v2.0 tenancy live: workspace, plan quota, scoped key, invite flow, roles, switch", { skip }, async () => {
  const pub = new Boxmes({ apiKey: "", baseUrl: url });
  const adm = new Boxmes({ apiKey: admin, baseUrl: url });
  const tag = String(Date.now());
  type Auth = { token: string; user: { id: string } };
  const signup = (body: Record<string, unknown>) => pub.request<Auth>("POST", "/auth/signup", { body: { password: "password123", ...body } });
  await signup({ email: `root-${tag}@example.com` }).catch(() => undefined); // first account = platform admin
  const { code } = await adm.request<{ code: string }>("POST", "/admin/invites", { body: { max_uses: 1 } });
  const a = await signup({ email: `owner-${tag}@example.com`, invite_code: code, workspace: "TS A" });
  const A = new Boxmes({ apiKey: a.token, baseUrl: url });
  const me = await A.me();
  const ws = me.workspace!.id;
  assert.ok(me.role === "owner" && me.permissions.includes("members:write") && !me.platform_admin);
  const cur = await A.getWorkspace();
  assert.ok(cur.plan === "free" && cur.quota!.max_members === 1 && cur.member_count === 1);
  assert.equal((await A.renameWorkspace("TS A2")).name, "TS A2");
  const plans = Object.fromEntries((await A.plans()).items.map((p) => [p.id, p.quota]));
  assert.ok(plans.pro.max_boxes === 10 && plans.team.max_members === 0 && plans.free.egress === "restricted");
  await assert.rejects(A.request("PATCH", "/workspaces/current", { body: { plan: "team" } }), (e: BoxmesError) => e.status === 403 && e.code === "POLICY_DENIED");
  await assert.rejects(A.createInvite(`dev-${tag}@example.com`), (e: BoxmesError) => e.code === "QUOTA_EXCEEDED" && e.details.quota === "max_members");
  assert.equal((await adm.adminUpdateWorkspace(ws, { plan: "pro" })).plan, "pro");
  assert.ok((await adm.adminWorkspaces()).items.some((w) => w.id === ws));

  const k = await A.createKey(null, "ci", ["boxes:read"], { expires_in_days: 7 });
  assert.ok(k.key && k.kind === "workspace" && k.expires_at);
  const ro = new Boxmes({ apiKey: k.key!, baseUrl: url });
  assert.deepEqual((await ro.listBoxes()).items, []);
  await assert.rejects(ro.createInvite("x@example.com"), (e: BoxmesError) => e.status === 401 && e.code === "UNAUTHORIZED");
  await A.revokeKey(k.id);
  await assert.rejects(A.createKey(null, "old", undefined, { expires_at: "2001-01-01T00:00:00Z" }), (e: BoxmesError) => e.code === "INVALID");

  const inv = await A.createInvite(`dev-${tag}@example.com`, "developer");
  const tok = inv.invite_url!.split("/").pop()!;
  assert.ok(tok.startsWith("bxi_") && inv.status === "pending");
  const pv = await pub.getInvite(tok);
  assert.ok(pv.workspace.id === ws && pv.role === "developer" && pv.account_exists === false);
  const d = await signup({ email: `dev-${tag}@example.com`, workspace_invite: tok });
  const D = new Boxmes({ apiKey: d.token, baseUrl: url });
  assert.equal((await D.me()).role, "developer");
  await assert.rejects(D.createInvite("y@example.com"), (e: BoxmesError) => e.status === 403 && e.code === "POLICY_DENIED");
  assert.equal((await A.updateMemberRole(d.user.id, "auditor")).role, "auditor");
  const ws2 = await A.createWorkspace("TS side");
  assert.equal((await A.switchWorkspace(ws2.id)).workspace!.id, ws2.id);
  assert.equal((await A.switchWorkspace(ws)).workspace!.id, ws);
  await assert.rejects(D.switchWorkspace(ws2.id), (e: BoxmesError) => e.status === 404);
  const inv2 = await A.createInvite(`later-${tag}@example.com`, "viewer");
  await A.revokeInvite(inv2.id);
  assert.ok((await A.listInvites()).items.every((i) => i.id !== inv2.id));
  await A.removeMember(d.user.id);
  assert.deepEqual((await A.listMembers()).items.map((m) => m.user_id), [a.user.id]);
  const u = await A.workspaceUsage();
  assert.ok(u.plan === "pro" && u.used.members === 1 && u.quota.max_boxes === 10);
  const types = new Set((await A.workspaceAudit({ limit: 200 })).items.map((e) => e.type));
  for (const t of ["member.invited", "member.joined", "member.role_changed", "member.removed"]) assert.ok(types.has(t), t);
});

test("v2.0 box locations + list filters (live)", { skip }, async () => {
  const bx = new Boxmes({ apiKey: key, baseUrl: url });
  const box = await bx.createBox({ template: "base", lifecycle: "session", slug: "sdk-ts-loc" });
  try {
    assert.equal(box.location!.region, "local");
    assert.ok((await bx.listBoxes({ region: "local" })).items.some((b) => b.id === box.id));
    assert.ok(!(await bx.listBoxes({ region: "eu" })).items.some((b) => b.id === box.id));
    assert.ok((await bx.listBoxes({ location: box.location!.site })).items.some((b) => b.id === box.id));
    const loc = await bx.boxLocations();
    assert.ok(loc.total.total >= 1 && loc.regions.some((r) => r.region === "local"));
  } finally {
    await bx.deleteBox(box.id);
  }
});

test("v2.2 provider account routes (mocked fetch)", async () => {
  const { calls, f } = mockFetch();
  const bx = new Boxmes({ apiKey: "bx_admin_x", baseUrl: "http://core.test:9", fetch: f, maxRetries: 0 });
  await bx.adminProviders();
  await bx.adminProvider("hetzner");
  await bx.listProviderAccounts("hetzner", { archived: true });
  await bx.validateProviderAccount("hetzner", "tok", "p2");
  await bx.createProviderAccount("hetzner", { label: "Hetzner EU #2", token: "tok", priority: 2, currency: "EUR" });
  await bx.getProviderAccount("hetzner", "pa_1");
  await bx.updateProviderAccount("hetzner", "pa_1", { balance: 150, limits: { servers: 25 } });
  await bx.rotateProviderAccount("hetzner", "pa_1", "tok2", "p2");
  await bx.testProviderAccount("hetzner", "pa_1");
  await bx.setProviderAccountState("hetzner", "pa_1", "drain");
  await bx.deleteProviderAccount("hetzner", "pa_1", { force: true, reason: "moved" });
  await bx.providerStrategy("e2b");
  await bx.setProviderStrategy("e2b", { strategy: "drain_first", sticky_minutes: 30 });
  await bx.infraAlerts("all");
  await bx.ackInfraAlert("low_balance|pa_1");
  const b = "/v1/admin/providers/hetzner";
  assert.deepEqual(calls.map((c) => `${c.method} ${c.path}`), [
    "GET /v1/admin/providers", `GET ${b}`, `GET ${b}/accounts`, `POST ${b}/accounts/validate`, `POST ${b}/accounts`, `GET ${b}/accounts/pa_1`,
    `PATCH ${b}/accounts/pa_1`, `POST ${b}/accounts/pa_1/rotate`, `POST ${b}/accounts/pa_1/test`, `POST ${b}/accounts/pa_1/drain`, `DELETE ${b}/accounts/pa_1`,
    "GET /v1/admin/providers/e2b/strategy", "PUT /v1/admin/providers/e2b/strategy", "GET /v1/admin/infra/alerts", "POST /v1/admin/infra/alerts/low_balance%7Cpa_1/ack",
  ]);
  assert.deepEqual(calls[2].query, { archived: "1" });
  assert.deepEqual(calls[4].body, { label: "Hetzner EU #2", token: "tok", priority: 2, currency: "EUR" });
  assert.deepEqual(calls[7].body, { token: "tok2", external_id: "p2" });
  assert.deepEqual(calls[10].query, { force: "1", reason: "moved" });
  assert.deepEqual(calls[13].query, { status: "all" });
});

test("v2.2 M29-S sandbox account routes (mocked fetch)", async () => {
  const { calls, f } = mockFetch();
  const bx = new Boxmes({ apiKey: "bx_admin_x", baseUrl: "http://core.test:9", fetch: f, maxRetries: 0 });
  await bx.providerSandboxAccounts("e2b");
  await bx.syncAccountTemplates("e2b", "pa_1");
  await bx.updateProviderAccount("e2b", "pa_1", { templates: ["agent"], limit_group: "prj-a", billing_group: "ws-1", limits: { concurrent_sandboxes: 20 } });
  assert.deepEqual(calls.map((c) => `${c.method} ${c.path}`), [
    "GET /v1/admin/providers/e2b/sandbox", "POST /v1/admin/providers/e2b/accounts/pa_1/templates/sync", "PATCH /v1/admin/providers/e2b/accounts/pa_1",
  ]);
  assert.deepEqual(calls[2].body, { templates: ["agent"], limit_group: "prj-a", billing_group: "ws-1", limits: { concurrent_sandboxes: 20 } });
});
