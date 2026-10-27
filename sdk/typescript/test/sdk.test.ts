// Runs against a real local core (core/bin) or BOXAZ_TEST_URL + BOXAZ_TEST_ADMIN_KEY; skips otherwise.
// node --experimental-strip-types --test test/sdk.test.ts
import { test, before, after } from "node:test";
import assert from "node:assert/strict";
import { spawn, type ChildProcess } from "node:child_process";
import { existsSync, mkdtempSync, openSync, rmSync } from "node:fs";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";
import { Boxa, BoxaError, Boxaz, BoxazError, asOpenAITools, asAnthropicTools, asGeminiTools, asToolMap, handleToolCall } from "../src/index.ts";

const here = dirname(fileURLToPath(import.meta.url));
const bin = join(here, "..", "..", "..", "core", "bin");
const procs: ChildProcess[] = [];
let work = "";
let url = process.env.BOXAZ_TEST_URL ?? "";
let admin = process.env.BOXAZ_TEST_ADMIN_KEY ?? "";
let key = "";
let agentId = "";
const skip = !url && !(existsSync(join(bin, "boxaz-core")) && existsSync(join(bin, "boxd")));

const freePort = () => new Promise<number>((res) => { const s = createServer(); s.listen(0, "127.0.0.1", () => { const p = (s.address() as { port: number }).port; s.close(() => res(p)); }); });
const wait = (ms: number) => new Promise((r) => setTimeout(r, ms));

before(async () => {
  if (skip) return;
  if (!url) {
    work = mkdtempSync(join(tmpdir(), "boxaz-sdk-ts-"));
    const port = await freePort();
    url = `http://127.0.0.1:${port}`;
    admin = "bx_admin_sdk_ts";
    procs.push(spawn(join(bin, "boxaz-core"), [], { env: { ...process.env, BOXAZ_DATA_DIR: `${work}/data`, BOXAZ_ADMIN_KEY: admin, BOXAZ_BOOTSTRAP_TOKEN: "boot-ts", BOXAZ_ADDR: `127.0.0.1:${port}`, BOXAZ_PRICE_FEED: "off", BOXAZ_LOCAL_PRICE_MONTH: "73" }, stdio: ["ignore", "ignore", openSync(`${work}/core.log`, "w")] }));
    for (let i = 0; i < 100; i++) { try { if ((await fetch(url + "/v1/health")).ok) break; } catch { /* starting */ } await wait(100); }
    procs.push(spawn(join(bin, "boxd"), [], { env: { ...process.env, BOXD_CORE_URL: url, BOXD_TOKEN: "boot-ts", BOXD_DRIVER: "local", BOXD_STATE_DIR: `${work}/boxd`, BOXD_IP: "127.0.0.1" }, stdio: ["ignore", "ignore", openSync(`${work}/boxd.log`, "w")] }));
    const adm = new Boxaz({ apiKey: admin, baseUrl: url });
    for (let i = 0; i < 100; i++) {
      const hs = await adm.request<{ items: { connected: boolean }[] }>("GET", "/hosts");
      if (hs.items.some((h) => h.connected)) break;
      await wait(100);
    }
  }
  const adm = new Boxaz({ apiKey: admin, baseUrl: url });
  const agent = await adm.createAgent({ name: `sdk-ts-${Date.now()}`, kind: "vercel-ai-sdk", budget_day_usd: 5 });
  agentId = agent.id;
  key = (await adm.createKey(agent.id, "sdk-ts")).key!;
});

after(() => {
  for (const p of procs) p.kill();
  if (work) rmSync(work, { recursive: true, force: true });
});

test("env defaults and url building", () => {
  process.env.BOXAZ_URL = "http://example.test:9/v1/";
  process.env.BOXAZ_API_KEY = "bx_agent_x";
  process.env.BOXAZ_LANG = "vi";
  const bx = new Boxaz();
  delete process.env.BOXAZ_URL; delete process.env.BOXAZ_API_KEY; delete process.env.BOXAZ_LANG;
  assert.equal(bx.baseUrl, "http://example.test:9");
  assert.equal(bx.headers().Authorization, "Bearer bx_agent_x");
  assert.equal(bx.headers()["Accept-Language"], "vi");
  assert.equal(bx.url("/llms.txt"), "http://example.test:9/llms.txt");
  assert.equal(bx.url("/boxes", { all: true, status: undefined }), "http://example.test:9/v1/boxes?all=true");
});

test("legacy BOXA_* env + deprecated aliases", () => {
  const saved = { ...process.env };
  try {
    delete process.env.BOXAZ_URL; delete process.env.BOXAZ_API_KEY;
    process.env.BOXA_URL = "http://legacy.test:9";
    process.env.BOXA_API_KEY = "bx_agent_old";
    const bx = new Boxaz();
    assert.equal(bx.baseUrl, "http://legacy.test:9");
    assert.equal(bx.headers().Authorization, "Bearer bx_agent_old");
    process.env.BOXAZ_URL = "http://new.test:9";
    assert.equal(new Boxaz().baseUrl, "http://new.test:9");
    assert.equal(Boxa, Boxaz);
    assert.equal(BoxaError, BoxazError);
  } finally {
    process.env = saved;
  }
});

test("discovery + tool formats", { skip }, async () => {
  const bx = new Boxaz({ apiKey: key, baseUrl: url });
  assert.equal((await bx.health()).ok, true);
  assert.equal((await bx.agentKinds()).items.length, 22);
  const oa = await asOpenAITools(bx);
  assert.equal(oa.length, 22);
  assert.ok(oa.some((t) => t.function.name === "box_exec_batch"));
  assert.equal((await asAnthropicTools(bx))[0].input_schema.type, "object");
  assert.equal((await asGeminiTools(bx))[0].functionDeclarations.length, 22);
  assert.ok((await bx.llmsTxt()).startsWith("# Boxaz"));
  assert.equal((await bx.openapi()).openapi, "3.1.0");
  const sn = await new Boxaz({ apiKey: admin, baseUrl: url }).snippet(agentId, undefined, "zh");
  assert.equal(sn.kind, "vercel-ai-sdk");
  assert.equal(sn.language, "typescript");
  assert.equal(sn.locale, "zh");
});

test("localized errors", { skip }, async () => {
  const bx = new Boxaz({ apiKey: key, baseUrl: url, lang: "ja" });
  await assert.rejects(bx.getBox("box_nope"), (e: unknown) => {
    assert.ok(e instanceof BoxazError);
    assert.equal(e.code, "NOT_FOUND");
    assert.equal(e.status, 404);
    assert.match(e.apiMessage, /見つかりません/);
    assert.match(e.docsUrl ?? "", /#NOT_FOUND$/);
    assert.equal(e.retryable, false);
    return true;
  });
});

test("box lifecycle: quote, create, exec, batch, run, restore, fork, tools", { skip }, async () => {
  const bx = new Boxaz({ apiKey: key, baseUrl: url });
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
  const bx = new Boxaz({ apiKey: key, baseUrl: url });
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
  const bx = new Boxaz({ apiKey: key, baseUrl: url });
  const sp = await bx.sandboxProviders();
  const ids = new Map(sp.items.map((p) => [p.id, p]));
  for (const id of ["e2b", "novita", "daytona", "prized", "boxdsh", "runloop", "blaxel", "vercel", "boat", "agent37", "upstash-box"]) assert.ok(ids.has(id), id);
  assert.equal(ids.get("prized")?.verification, "live-verified");
  assert.equal(ids.get("upstash-box")?.mode, "shim");
  assert.ok(sp.items.every((p) => p.docs && p.initials && p.from_usd_h > 0 && typeof p.capabilities.fork === "boolean"));
  await assert.rejects(bx.lintAdapter("id: x"), (e: unknown) => e instanceof BoxazError && ["UNAUTHORIZED", "POLICY_DENIED"].includes(e.code));
  const adm = new Boxaz({ apiKey: admin, baseUrl: url });
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
