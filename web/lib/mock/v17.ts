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
  /** v1.9: tên gateway dễ đọc (https://<name>.<domain>/) + nhãn port (<name>-<label>). */
  name: string;
  labels: Record<number, string>;
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

const GW_DOMAIN = "boxmes.dev";
const MAX_LOG = 24 << 10;
const enc = new TextEncoder();
const bytes = (s: string) => enc.encode(s).length;

type G = typeof globalThis & { __boxmesV17?: V17State };
function st(): V17State {
  const g = globalThis as G;
  if (!g.__boxmesV17) g.__boxmesV17 = { boxes: {}, seq: 0 };
  return g.__boxmesV17;
}

const iso = (t: number) => new Date(t).toISOString();
const rnd = (n: number) => Array.from({ length: n }, () => "abcdefghijkmnpqrstuvwxyz23456789"[Math.floor(Math.random() * 32)]).join("");

/** Capability lifecycle (giống knownLifecycle trong core/internal/manager/capabilities.go). */
const KNOWN: Record<string, Omit<CapabilityRow, "provider" | "group" | "wake_via_gateway">> = {
  e2b: { memory_pause: true, wake_on_http_native: true, fork: true, inbound_http: true, max_runtime: 86400, private_preview: true, note: "autoResume on the preview host (set at create); runtime cap 24 h Pro / 1 h Hobby, reset by a resume" },
  novita: { memory_pause: true, wake_on_http_native: true, fork: false, inbound_http: true, max_runtime: 14400, private_preview: true, note: "E2B-compatible autoResume; clone = snapshot → new sandbox" },
  prized: { memory_pause: true, wake_on_http_native: false, fork: true, inbound_http: false, max_runtime: 604800, private_preview: false, note: "no inbound HTTP: the gateway reaches the app through the in-box ingress agent (or a tunnel)" },
  render: { memory_pause: false, wake_on_http_native: false, fork: false, inbound_http: true, max_runtime: 0, private_preview: false, note: "web service running boxd (dial-in only): onrender.com serves one in-box port; sleep stops the box's processes, the service keeps running; free plan spins down after 15 min without inbound traffic and loses local files" },
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

// ---- v1.9: tên gateway dễ đọc (giống core/internal/manager/gatewaynames.go) ----
const RESERVED_NAMES = new Set(["admin", "api", "app", "assets", "auth", "autoconfig", "autodiscover", "blog", "cdn", "console", "core", "dashboard", "dl", "docs", "ftp", "git", "grafana", "gw", "hark", "help", "imap", "login", "mail", "mcp", "metrics", "mx", "ns", "ns1", "ns2", "pop", "pop3", "portal", "smtp", "sso", "static", "status", "support", "webmail", "ws", "www"]);
const SUFFIXES = ["blue", "green", "amber", "coral", "jade", "ruby", "teal", "navy", "sky", "sun", "moon", "star", "leaf", "oak", "pine", "fox", "owl", "wolf", "bear", "hawk"];
const WORDS = ["alpha", "bravo", "charlie", "delta", "echo", "foxtrot", "golf", "hotel", "india", "juliet", "kilo", "lima", "mike", "november", "oscar", "papa"];
const WELL_KNOWN: Record<number, string> = { 80: "web", 3000: "web", 4200: "web", 5000: "web", 5173: "web", 8000: "web", 8080: "web", 3001: "api", 4000: "api", 8001: "api", 8081: "admin", 9000: "admin", 5432: "db", 3306: "mysql", 6379: "redis", 27017: "mongo", 8888: "jupyter", 7860: "gradio", 8501: "streamlit", 11434: "ollama", 9229: "debug", 9090: "metrics" };

export function slugifyName(s: string, max = 40): string {
  const out = s.replace(/đ/g, "d").replace(/Đ/g, "D").normalize("NFD").replace(/\p{M}/gu, "").toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-+|-+$/g, "");
  return max > 0 && out.length > max ? out.slice(0, max).replace(/-+$/, "") : out;
}
export const boxNameOk = (n: string) => n.length >= 1 && n.length <= 40 && /^[a-z0-9]([a-z0-9-]*[a-z0-9])?$/.test(n) && !n.includes("--") && /[a-z]/.test(n) && !/^[0-9a-f]{20}-[0-9]{1,5}$/.test(n);
export const portLabelOk = (l: string) => l.length <= 20 && /^[a-z]([a-z0-9-]*[a-z0-9])?$/.test(l) && !l.includes("--");

function primaryPort(b: MBox, v: V17Box): number {
  if (v.app?.port && b.ports.includes(v.app.port)) return v.app.port;
  return b.ports[0] ?? 0;
}
/** Mọi host đang dùng (trừ box `except`). */
function usedHosts(except: string): Set<string> {
  const S = st();
  const out = new Set<string>();
  for (const [id, v] of Object.entries(S.boxes)) {
    if (id === except || !v.name) continue;
    out.add(v.name);
    for (const l of Object.values(v.labels)) out.add(`${v.name}-${l}`);
  }
  return out;
}
function clash(used: Set<string>, name: string, labels: Record<number, string>): boolean {
  return RESERVED_NAMES.has(name) || used.has(name) || Object.values(labels).some((l) => used.has(`${name}-${l}`));
}
export function freeName(used: Set<string>, base: string, labels: Record<number, string>): string {
  if (!boxNameOk(base)) base = "box";
  if (!clash(used, base, labels)) return base;
  for (const w of SUFFIXES) {
    const n = `${base.slice(0, 39 - w.length).replace(/-+$/, "")}-${w}`;
    if (boxNameOk(n) && !clash(used, n, labels)) return n;
  }
  return `${base.slice(0, 30)}-${rnd(6).replace(/[0-9]/g, "x")}`;
}
function assignLabels(b: MBox, v: V17Box, used: Set<string>) {
  const taken = new Set(Object.values(v.labels));
  for (const p of b.ports) {
    if (v.labels[p]) continue;
    const cands = [...(v.app?.port === p && v.app?.label ? [v.app.label] : []), ...(WELL_KNOWN[p] ? [WELL_KNOWN[p]] : []), ...(WELL_KNOWN[p] === "web" ? ["app", "site", "ui"] : []), ...WORDS];
    const l = cands.find((c) => !taken.has(c) && !used.has(`${v.name}-${c}`));
    if (l) {
      v.labels[p] = l;
      taken.add(l);
    }
  }
}
function ensureNames(b: MBox, v: V17Box) {
  const used = usedHosts(b.id);
  if (!v.name) v.name = freeName(used, slugifyName(b.slug) || "box", v.labels);
  assignLabels(b, v, used);
}

function ensure(b: MBox): V17Box {
  const S = st();
  let v = S.boxes[b.id];
  if (v) {
    ensureNames(b, v);
    return v;
  }
  v = { key: rnd(10).replace(/[^0-9a-f]/g, "a").padEnd(20, "0").slice(0, 20), name: "", labels: {}, tokens: [], app: null, status: { state: "none", ready: false, restarts: 0 }, log: "", base: 0 };
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
  ensureNames(b, v);
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
const aliasURL = (v: V17Box, port: number) => `https://${v.key}-${port}.${GW_DOMAIN}/`;
/** v1.9: https://<name>.<domain>/ cho port chính, https://<name>-<label>.<domain>/ cho port khác. */
function gatewayURL(b: MBox, v: V17Box, port: number): string {
  if (port === primaryPort(b, v)) return `https://${v.name}.${GW_DOMAIN}/`;
  return v.labels[port] ? `https://${v.name}-${v.labels[port]}.${GW_DOMAIN}/` : aliasURL(v, port);
}

function portView(b: MBox, v: V17Box, port: number): PortView {
  return { ...portViewBase(b, v, port), label: v.labels[port], primary: port === primaryPort(b, v), alias_url: aliasURL(v, port) };
}

function portsResult(b: MBox, v: V17Box) {
  return { box_id: b.id, url: b.ports.length ? portView(b, v, primaryPort(b, v)).url : "", name: v.name, primary_port: primaryPort(b, v), ports: b.ports.map((p) => portView(b, v, p)) };
}

function portViewBase(b: MBox, v: V17Box, port: number): PortView {
  const access = accessFor(b, port);
  const gw = gatewayURL(b, v, port);
  const running = b.status === "running";
  const caps = KNOWN[b.provider];
  // v1.8: E2B / Novita sandboxes behind an internet-facing gateway are created private
  const priv = !!caps?.private_preview;
  const provider_preview: PortView["provider_preview"] = caps?.inbound_http ? (priv ? "private" : "public") : undefined;
  if (access === "token") {
    return { port, url: gw, kind: "gateway", public: false, gateway_url: gw, access, ...(provider_preview ? { provider_preview } : {}),
      note: "token required: open with ?token=<bxt_…> (exchanged for a cookie), Authorization: Bearer <token> or X-Boxmes-Token; issue one with POST /v1/boxes/{id}/ports/{port}/tokens (box url --token)",
      caveats: ["Boxmes gateway URL: wakes a sleeping box on the first request and passes WebSocket/SSE through"] };
  }
  if (priv) {
    return { port, url: gw, kind: "gateway", public: true, gateway_url: gw, access, provider_preview: "private",
      note: `${b.provider} preview host is private (refuses requests without the sandbox traffic token); served through the Boxmes gateway, which wakes a sleeping box`,
      caveats: ["Boxmes gateway URL: host-based <name>.<domain> / <name>-<label>.<domain>; WebSocket and SSE pass through; a sleeping box is woken by the first request"] };
  }
  if (caps?.inbound_http && b.provider !== "prized") {
    const host = b.provider === "e2b" ? "e2b.app" : "sandbox.novita.ai";
    return { port, url: running ? `https://${port}-i${b.id.slice(4)}.${host}` : "", kind: "provider-preview", public: true, gateway_url: gw, access, provider_preview: "public",
      caveats: ["provider preview host: public to anyone with the URL", ...(running ? [] : ["box is not running: the provider URL answers only while it runs; the gateway URL wakes it"])] };
  }
  return { port, url: running ? gw : "", kind: running ? "ingress" : "none", public: running, gateway_url: gw, access,
    caveats: running ? ["served through core at a host-based gateway URL (in-box boxd ingress agent)"] : ["box is not running"] };
}

function appView(b: MBox, v: V17Box): AppView {
  const status = { ...v.status };
  if (v.app && b.status === "sleeping") Object.assign(status, { state: "sleeping", ready: false, pid: undefined });
  return { box_id: b.id, spec: v.app, status };
}

function setLabel(b: MBox, v: V17Box, port: number, raw: string, err: V17Ctx["err"]): Res | null {
  const label = raw.trim().toLowerCase();
  if (!portLabelOk(label)) return err(400, "INVALID", "v19_bad_label", { label: raw });
  const other = Object.entries(v.labels).find(([p, l]) => Number(p) !== port && l === label);
  if (other) return err(400, "INVALID", "v19_label_used", { label, port: Number(other[0]) });
  if (usedHosts(b.id).has(`${v.name}-${label}`)) return err(400, "INVALID", "v19_name_taken", { name: `${v.name}-${label}`, next: `${v.name}-${label}-${SUFFIXES[0]}` });
  v.labels[port] = label;
  return null;
}

export interface V17Ctx extends Pick<V13Ctx, "ok" | "list" | "hydrateBox" | "pushEvent"> {
  err: (status: number, code: "INVALID" | "NOT_FOUND", key: string, params?: Record<string, string | number>) => Res;
}

/** Ghép trường v1.7 vào Box (wake_on_request, port_access, app). */
export function hydrateV17(b: MBox, box: Box): Box {
  const v = ensure(b);
  return { ...box, wake_on_request: b.wake_on_request ?? true, ...(b.port_access ? { port_access: b.port_access } : {}), app: v.app,
    subdomain: v.name, port_labels: Object.fromEntries(Object.entries(v.labels).map(([k, l]) => [k, l])) };
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
    if (typeof body.subdomain === "string") {
      const v = ensure(b);
      const raw = body.subdomain;
      const name = slugifyName(raw, 0) || raw.trim().toLowerCase();
      if (name !== v.name) {
        if (!boxNameOk(name)) return err(400, "INVALID", "v19_bad_name", { name: raw });
        if (RESERVED_NAMES.has(name)) return err(400, "INVALID", "v19_name_reserved", { name });
        const used = usedHosts(b.id);
        if (clash(used, name, v.labels)) return err(400, "INVALID", "v19_name_taken", { name, next: freeName(used, name, v.labels) });
        const old = v.name;
        v.name = name;
        ctx.pushEvent({ actor: "user:anh", box_id: b.id, agent_id: b.agent_id, type: "box.settings", message: `gateway name ${old} → ${name}`, cost_usd: null });
      }
    }
    return ok(ctx.hydrateBox(b));
  }
  if (r2 !== "ports" && r2 !== "app") return null;
  const b = s.boxes.find((x) => x.id === r1 || x.slug === r1);
  if (!b) return err(404, "NOT_FOUND", "not_found", { what: `box ${r1}` });
  const v = ensure(b);

  if (r2 === "ports") {
    if (!r3) {
      if (M === "GET") return ok(portsResult(b, v));
      if (M === "POST") {
        const p = Number(body.port);
        if (!Number.isInteger(p) || p < 1 || p > 65535) return err(400, "INVALID", "v17_bad_port");
        if (typeof body.label === "string" && body.label) {
          const e = setLabel(b, v, p, body.label, err);
          if (e) return e;
        }
        if (!b.ports.includes(p)) {
          b.ports.push(p);
          ensureNames(b, v);
          ctx.pushEvent({ actor: "user:anh", box_id: b.id, agent_id: b.agent_id, type: "box.port_exposed", message: `port ${p} → ${gatewayURL(b, v, p)}`, cost_usd: null });
        }
        const res = portsResult(b, v);
        res.ports.sort((x, y) => (x.port === p ? -1 : y.port === p ? 1 : 0));
        return ok(res);
      }
      return null;
    }
    const port = Number(r3);
    if (!Number.isInteger(port) || port < 1 || port > 65535) return err(400, "INVALID", "v17_bad_port");
    if (!b.ports.includes(port)) return err(404, "NOT_FOUND", "v17_port_not_exposed", { port, box: b.id });
    if (!r4 && M === "PUT") {
      const a = String(body.access ?? "");
      const label = typeof body.label === "string" ? body.label : "";
      if (!label && a !== "public" && a !== "token") return err(400, "INVALID", "v17_bad_access");
      if (a && a !== "public" && a !== "token") return err(400, "INVALID", "v17_bad_access");
      if (label) {
        const e = setLabel(b, v, port, label, err);
        if (e) return e;
        ctx.pushEvent({ actor: "user:anh", box_id: b.id, agent_id: b.agent_id, type: "box.port", message: `port ${port} label → ${v.labels[port]}`, cost_usd: null });
      }
      if (a) {
        b.port_access = { ...(b.port_access ?? {}), [String(port)]: a };
        ctx.pushEvent({ actor: "user:anh", box_id: b.id, agent_id: b.agent_id, type: "box.port_access", message: `port ${port} access=${a}`, cost_usd: null });
      }
      return ok(portView(b, v, port));
    }
    if (r4 === "tokens") {
      if (!r5 && M === "GET") return ok({ box_id: b.id, port, access: accessFor(b, port), gateway_url: gatewayURL(b, v, port), items: v.tokens.filter((t) => t.port === port).map(publicTok).reverse() });
      if (!r5 && M === "POST") {
        const ttl = body.ttl_s === undefined || body.ttl_s === null || body.ttl_s === 0 ? 86400 : Number(body.ttl_s);
        if (!Number.isFinite(ttl) || ttl < 60 || ttl > 2592000) return err(400, "INVALID", "v17_bad_ttl");
        const t = mkToken(b, v, port, String(body.label ?? "").slice(0, 80), ttl);
        v.tokens.push(t);
        if (body.private !== false) b.port_access = { ...(b.port_access ?? {}), [String(port)]: "token" };
        ctx.pushEvent({ actor: "user:anh", box_id: b.id, agent_id: b.agent_id, type: "url_token.created", message: `port ${port} ${t.prefix}… ttl=${ttl}s`, cost_usd: null });
        return ok({ ...publicTok(t), token: t._secret, url: `${gatewayURL(b, v, port)}?token=${t._secret}` }, 201);
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
