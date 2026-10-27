// MOCK MODE v1.7: gateway URL theo box+port (wake on request), URL token, app supervisor, capability lifecycle.
// Bám theo core/internal/api/v17.go + manager/{urltokens,app,capabilities,sandboxdeploy}.go.
import type { AppLogsResult, AppSpec, AppStatus, AppView, Box, CapabilityRow, PortView, URLToken } from "../types";
import type { MockState } from "./seed";
import { SANDBOX_INFO } from "./sandbox-data";
import type { V13Ctx } from "./v13";

type Res = { status: number; body: unknown; contentType?: string; headers?: Record<string, string> };
type MBox = MockState["boxes"][number];

interface V17Box {
  key: string;
  tokens: (URLToken & { _secret: string })[];
  app: AppSpec | null;
  status: AppStatus;
  log: string;
  /** offset tuyệt đối của byte đầu tiên còn giữ trong `log` (ring buffer). */
  base: number;
}
interface V17State {
  boxes: Record<string, V17Box>;
  seq: number;
}

const GW_DOMAIN = "boxaz.dev";
const MAX_LOG = 24 << 10;
const enc = new TextEncoder();
const bytes = (s: string) => enc.encode(s).length;

type G = typeof globalThis & { __boxazV17?: V17State };
function st(): V17State {
  const g = globalThis as G;
  if (!g.__boxazV17) g.__boxazV17 = { boxes: {}, seq: 0 };
  return g.__boxazV17;
}

const iso = (t: number) => new Date(t).toISOString();
const rnd = (n: number) => Array.from({ length: n }, () => "abcdefghijkmnpqrstuvwxyz23456789"[Math.floor(Math.random() * 32)]).join("");

/** Capability lifecycle (giống knownLifecycle trong core/internal/manager/capabilities.go). */
const KNOWN: Record<string, Omit<CapabilityRow, "provider" | "group" | "wake_via_gateway">> = {
  e2b: { memory_pause: true, wake_on_http_native: true, fork: true, inbound_http: true, max_runtime: 86400, note: "autoResume on the preview host (set at create); runtime cap 24 h Pro / 1 h Hobby, reset by a resume" },
  novita: { memory_pause: true, wake_on_http_native: true, fork: false, inbound_http: true, max_runtime: 14400, note: "E2B-compatible autoResume; clone = snapshot → new sandbox" },
  prized: { memory_pause: true, wake_on_http_native: false, fork: true, inbound_http: false, max_runtime: 604800, note: "no inbound HTTP: the gateway reaches the app through the in-box ingress agent (or a tunnel)" },
};

export function capabilities(s: MockState): CapabilityRow[] {
  const rows = s.providers.map((p): CapabilityRow => {
    const group = (p as { group?: string }).group === "sandbox" ? "sandbox" : "vps";
    const known = KNOWN[p.id];
    if (known) return { provider: p.id, group, wake_via_gateway: true, ...known };
    if (group === "sandbox") {
      const c = SANDBOX_INFO.find((x) => x.id === p.id)?.capabilities;
      return { provider: p.id, group, memory_pause: !!c?.pause, wake_on_http_native: false, wake_via_gateway: true, fork: !!c?.fork, inbound_http: !!c?.ports, max_runtime: c?.max_lifetime_s ?? 0, note: "derived from the adapter's capabilities" };
    }
    return { provider: p.id, group, memory_pause: false, wake_on_http_native: false, wake_via_gateway: true, fork: true, inbound_http: true, max_runtime: 0, note: "pool host: sleep stops the container (the supervised app restarts on wake); clone is copy-on-write on the host" };
  });
  return rows.sort((a, b) => (a.group < b.group ? -1 : a.group > b.group ? 1 : 0));
}

function appendLog(v: V17Box, text: string) {
  v.log += text;
  const over = bytes(v.log) - MAX_LOG;
  if (over > 0) {
    // cắt theo ký tự (log mock là ASCII) cho gọn
    v.log = v.log.slice(over);
    v.base += over;
  }
}

function ensure(b: MBox): V17Box {
  const S = st();
  let v = S.boxes[b.id];
  if (v) return v;
  v = { key: rnd(10), tokens: [], app: null, status: { state: "none", ready: false, restarts: 0 }, log: "", base: 0 };
  S.boxes[b.id] = v;
  const now = Date.now();
  // Dữ liệu mẫu: một box Node có app sẵn sàng, một box sandbox có port token + app, một service có port token + 2 token.
  if (b.id === "box_a9d054") {
    if (!b.ports.includes(3000)) b.ports.push(3000);
    v.app = { repo: "https://github.com/example/kecon-app.git", ref: "main", install: "npm ci", start: "npm start", port: 3000, ready: { http: "/healthz" }, restart: "always", workdir: "/work/kecon-app", env: { NODE_ENV: "production" } };
    v.status = { state: "ready", ready: true, pid: 412, restarts: 1, last_exit: 137, started_at: iso(now - 47 * 60e3), ready_at: iso(now - 47 * 60e3 + 1700), supervisor: "boxd", spec_hash: "9c1e2a4b", message: "listening on :3000" };
    appendLog(v, [
      "[boxd] clone https://github.com/example/kecon-app.git (main) → /work/kecon-app",
      "[boxd] install: npm ci",
      "added 412 packages in 18s",
      "[boxd] start: npm start (PORT=3000)",
      "> kecon-app@1.4.0 start",
      "> next start -p 3000",
      "  ▲ Next.js 15.5 - Local: http://localhost:3000",
      "[boxd] exited with 137 (OOM?) — restart in 1s (restarts=1)",
      "[boxd] start: npm start (PORT=3000)",
      "  ▲ Next.js 15.5 - Local: http://localhost:3000",
      "[boxd] ready: GET /healthz → 200 in 1.7s",
      "",
    ].join("\n"));
  } else if (b.id === "box_5e2b70") {
    if (!b.ports.includes(8000)) b.ports.push(8000);
    b.port_access = { ...(b.port_access ?? {}), "8000": "token" };
    v.app = { start: "python -m http.server 8000", port: 8000, restart: "on-failure", workdir: "/work" };
    v.status = { state: "ready", ready: true, pid: 77, restarts: 0, started_at: iso(now - 9 * 60e3), ready_at: iso(now - 9 * 60e3 + 430), supervisor: "script", spec_hash: "3f7d10aa" };
    appendLog(v, "[supervisor] start: python -m http.server 8000\nServing HTTP on 0.0.0.0 port 8000 (http://0.0.0.0:8000/) ...\n[supervisor] ready: tcp :8000 in 0.4s\n");
    v.tokens.push(mkToken(b, v, 8000, "fuzz-dashboard", 86400, now - 3 * 3600e3));
  } else if (b.id === "box_c4410f") {
    b.port_access = { ...(b.port_access ?? {}), "8080": "token" };
    v.tokens.push(mkToken(b, v, 8080, "qa-team", 7 * 86400, now - 2 * 86400e3));
    const old = mkToken(b, v, 8080, "demo", 3600, now - 5 * 3600e3);
    v.tokens.push(old);
  }
  return v;
}

function mkToken(b: MBox, v: V17Box, port: number, label: string, ttl: number, created = Date.now()): URLToken & { _secret: string } {
  const S = st();
  S.seq += 1;
  const secret = `bxt_${rnd(32)}`;
  return { id: `utok_${S.seq.toString(16)}${rnd(6)}`, box_id: b.id, port, label: label || undefined, prefix: secret.slice(0, 10), expires_at: iso(created + ttl * 1000), created_at: iso(created), _secret: secret };
}

function publicTok(t: URLToken & { _secret: string }): URLToken {
  // eslint-disable-next-line @typescript-eslint/no-unused-vars
  const { _secret, token, url, ...rest } = t;
  const expired = !rest.revoked_at && Date.parse(rest.expires_at) <= Date.now();
  return { ...rest, ...(expired ? { expired: true } : {}) };
}

const accessFor = (b: MBox, port: number) => (b.port_access?.[String(port)] === "token" ? "token" : "public");
const gatewayURL = (v: V17Box, port: number) => `https://${v.key}-${port}.${GW_DOMAIN}/`;

function portView(b: MBox, v: V17Box, port: number): PortView {
  const access = accessFor(b, port);
  const gw = gatewayURL(v, port);
  const running = b.status === "running";
  if (access === "token") {
    return { port, url: gw, kind: "gateway", public: false, gateway_url: gw, access,
      note: "token required: open with ?token=<bxt_…> (exchanged for a cookie), Authorization: Bearer <token> or X-Boxaz-Token; issue one with POST /v1/boxes/{id}/ports/{port}/tokens (box url --token)",
      caveats: ["Boxaz gateway URL: wakes a sleeping box on the first request and passes WebSocket/SSE through"] };
  }
  const caps = KNOWN[b.provider];
  if (caps?.inbound_http && b.provider !== "prized") {
    const host = b.provider === "e2b" ? "e2b.app" : "sandbox.novita.ai";
    return { port, url: running ? `https://${port}-i${b.id.slice(4)}.${host}` : "", kind: "provider-preview", public: true, gateway_url: gw, access,
      caveats: ["provider preview host: public to anyone with the URL", ...(running ? [] : ["box is not running: the provider URL answers only while it runs; the gateway URL wakes it"])] };
  }
  return { port, url: running ? `https://${GW_DOMAIN}/x/${v.key}-${port}/` : "", kind: running ? "ingress" : "none", public: running, gateway_url: gw, access,
    caveats: running ? ["Boxaz ingress through core (in-box boxd ingress agent)"] : ["box is not running"] };
}

function appView(b: MBox, v: V17Box): AppView {
  const status = { ...v.status };
  if (v.app && b.status === "sleeping") Object.assign(status, { state: "sleeping", ready: false, pid: undefined });
  return { box_id: b.id, spec: v.app, status };
}

export interface V17Ctx extends Pick<V13Ctx, "ok" | "list" | "hydrateBox" | "pushEvent"> {
  err: (status: number, code: "INVALID" | "NOT_FOUND", key: string, params?: Record<string, string | number>) => Res;
}

/** Ghép trường v1.7 vào Box (wake_on_request, port_access, app). */
export function hydrateV17(b: MBox, box: Box): Box {
  const v = ensure(b);
  return { ...box, wake_on_request: b.wake_on_request ?? true, ...(b.port_access ? { port_access: b.port_access } : {}), app: v.app };
}

/** Log app cho luồng SSE ?follow=1 (mock): đọc từ offset, thỉnh thoảng sinh dòng mới. */
export function appLogsFrom(s: MockState, boxId: string, offset: number, tick = false): AppLogsResult | null {
  const b = s.boxes.find((x) => x.id === boxId || x.slug === boxId);
  if (!b) return null;
  const v = ensure(b);
  if (tick && v.app && b.status === "running") {
    const ts = new Date().toISOString().slice(11, 19);
    const lines = v.app.port ? [`${ts} GET / 200 ${(4 + Math.random() * 30).toFixed(1)}ms`, `${ts} GET /healthz 200 0.${Math.floor(Math.random() * 9)}ms`] : [`${ts} tick`];
    appendLog(v, lines[Math.floor(Math.random() * lines.length)] + "\n");
  }
  return readLog(v, offset, 64 << 10);
}

function readLog(v: V17Box, offset: number, max: number): AppLogsResult {
  const end = v.base + bytes(v.log);
  let from = offset < 0 ? Math.max(v.base, end - max) : offset;
  let truncated = false;
  if (from < v.base) {
    from = v.base;
    truncated = true;
  }
  if (from > end) from = end;
  const data = v.log.slice(from - v.base, from - v.base + max);
  return { data, offset: from + bytes(data), ...(truncated ? { truncated } : {}) };
}

export function handleV17(s: MockState, ctx: V17Ctx, M: string, segs: string[], q: URLSearchParams, body: Record<string, unknown>): Res | null {
  const { ok, list, err } = ctx;
  const [r0, r1, r2, r3, r4, r5] = segs;
  if (r0 === "capabilities" && M === "GET") return list(capabilities(s));
  if (r0 !== "boxes" || !r1 || (!r2 && M !== "PATCH")) return null;
  if (!r2 && M === "PATCH") {
    const b = s.boxes.find((x) => x.id === r1 || x.slug === r1);
    if (!b) return err(404, "NOT_FOUND", "not_found", { what: `box ${r1}` });
    if (typeof body.wake_on_request === "boolean") {
      b.wake_on_request = body.wake_on_request;
      ctx.pushEvent({ actor: "user:anh", box_id: b.id, agent_id: b.agent_id, type: "box.settings", message: `wake_on_request=${body.wake_on_request}`, cost_usd: null });
    }
    return ok(ctx.hydrateBox(b));
  }
  if (r2 !== "ports" && r2 !== "app") return null;
  const b = s.boxes.find((x) => x.id === r1 || x.slug === r1);
  if (!b) return err(404, "NOT_FOUND", "not_found", { what: `box ${r1}` });
  const v = ensure(b);

  if (r2 === "ports") {
    if (!r3) {
      if (M === "GET") return ok({ box_id: b.id, url: b.ports.length ? portView(b, v, b.ports[0]).url : "", ports: b.ports.map((p) => portView(b, v, p)) });
      if (M === "POST") {
        const p = Number(body.port);
        if (!Number.isInteger(p) || p < 1 || p > 65535) return err(400, "INVALID", "v17_bad_port");
        if (!b.ports.includes(p)) {
          b.ports.push(p);
          ctx.pushEvent({ actor: "user:anh", box_id: b.id, agent_id: b.agent_id, type: "box.port_exposed", message: `port ${p}`, cost_usd: null });
        }
        return ok({ box_id: b.id, url: portView(b, v, b.ports[0]).url, ports: b.ports.map((x) => portView(b, v, x)) });
      }
      return null;
    }
    const port = Number(r3);
    if (!Number.isInteger(port) || port < 1 || port > 65535) return err(400, "INVALID", "v17_bad_port");
    if (!b.ports.includes(port)) return err(404, "NOT_FOUND", "v17_port_not_exposed", { port, box: b.id });
    if (!r4 && M === "PUT") {
      const a = String(body.access ?? "");
      if (a !== "public" && a !== "token") return err(400, "INVALID", "v17_bad_access");
      b.port_access = { ...(b.port_access ?? {}), [String(port)]: a };
      ctx.pushEvent({ actor: "user:anh", box_id: b.id, agent_id: b.agent_id, type: "box.port_access", message: `port ${port} access=${a}`, cost_usd: null });
      return ok(portView(b, v, port));
    }
    if (r4 === "tokens") {
      if (!r5 && M === "GET") return ok({ box_id: b.id, port, access: accessFor(b, port), gateway_url: gatewayURL(v, port), items: v.tokens.filter((t) => t.port === port).map(publicTok).reverse() });
      if (!r5 && M === "POST") {
        const ttl = body.ttl_s === undefined || body.ttl_s === null || body.ttl_s === 0 ? 86400 : Number(body.ttl_s);
        if (!Number.isFinite(ttl) || ttl < 60 || ttl > 2592000) return err(400, "INVALID", "v17_bad_ttl");
        const t = mkToken(b, v, port, String(body.label ?? "").slice(0, 80), ttl);
        v.tokens.push(t);
        if (body.private !== false) b.port_access = { ...(b.port_access ?? {}), [String(port)]: "token" };
        ctx.pushEvent({ actor: "user:anh", box_id: b.id, agent_id: b.agent_id, type: "url_token.created", message: `port ${port} ${t.prefix}… ttl=${ttl}s`, cost_usd: null });
        return ok({ ...publicTok(t), token: t._secret, url: `${gatewayURL(v, port)}?token=${t._secret}` }, 201);
      }
      if (r5 && M === "DELETE") {
        const t = v.tokens.find((x) => x.id === r5 && x.port === port);
        if (!t) return err(404, "NOT_FOUND", "not_found", { what: `token ${r5}` });
        if (!t.revoked_at) {
          t.revoked_at = iso(Date.now());
          ctx.pushEvent({ actor: "user:anh", box_id: b.id, agent_id: b.agent_id, type: "url_token.revoked", message: `port ${port} ${t.prefix}…`, cost_usd: null });
        }
        return ok(publicTok(t));
      }
    }
    return null;
  }

  // ---- app ----
  if (!r3) {
    if (M === "GET") return ok(appView(b, v));
    if (M === "PUT") {
      const start = String(body.start ?? "").trim();
      if (!start) return err(400, "INVALID", "v17_no_start");
      const spec: AppSpec = { ...(body as unknown as AppSpec), start, restart: (body.restart as string) || "always" };
      if (!spec.workdir) spec.workdir = spec.repo ? `/work/${spec.repo.replace(/\.git$/, "").split("/").pop()}` : "/work";
      v.app = spec;
      if (spec.port && !b.ports.includes(spec.port)) b.ports.push(spec.port);
      v.status = { state: b.status === "running" ? "starting" : "stopped", ready: false, restarts: 0, started_at: iso(Date.now()), supervisor: b.host_id?.startsWith("hst_sbx") ? "script" : "boxd", spec_hash: rnd(8) };
      appendLog(v, `[boxd] spec updated — start: ${start}\n`);
      ctx.pushEvent({ actor: "user:anh", box_id: b.id, agent_id: b.agent_id, type: "app.set", message: start, cost_usd: null });
      return ok(appView(b, v));
    }
    if (M === "DELETE") {
      v.app = null;
      v.status = { state: "none", ready: false, restarts: 0 };
      appendLog(v, "[boxd] app removed\n");
      return ok(appView(b, v));
    }
    return null;
  }
  if (r3 === "restart" && M === "POST") {
    if (!v.app) return err(404, "NOT_FOUND", "v17_no_app", { box: b.id });
    v.status = { ...v.status, state: "ready", ready: true, restarts: v.status.restarts + 1, started_at: iso(Date.now()), ready_at: iso(Date.now() + 1200), pid: (v.status.pid ?? 100) + 7, message: "restarted by user" };
    appendLog(v, `[boxd] restart requested — start: ${v.app.start}\n[boxd] ready in 1.2s\n`);
    ctx.pushEvent({ actor: "user:anh", box_id: b.id, agent_id: b.agent_id, type: "app.restart", message: v.app.start, cost_usd: null });
    return ok(appView(b, v));
  }
  if (r3 === "logs" && M === "GET") {
    const off = q.get("offset");
    const max = Number(q.get("max")) || 16 << 10;
    return ok(readLog(v, off === null || off === "" ? -1 : Number(off), max));
  }
  return null;
}
