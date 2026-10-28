// MOCK MODE v1.10: truy cập box — ticket một lần (terminal|tcp|ssh, 60 s), terminal bền (session, detach TTL, replay),
// GET /boxes/{id}/access, forward-ports, GET /ssh + /ssh/keys. Bám theo core/internal/api/access.go + manager/access.go.
// WebSocket terminal không giả lập được qua route Next (không upgrade): UI dùng shell mô phỏng và hiện ghi chú mock.
// Ở mock, tạo ticket terminal coi như đã đổi (redeem) ngay: không có session_id → mở session mới; có → gắn lại session đó.
import type { AccessInfo, AccessTicket, Box, BoxazEvent, ErrorCode, SshKey, TerminalSession } from "../types";
import { MOCK_GATEWAY_DOMAIN } from "./v18";
import type { MockState } from "./seed";

type Res = { status: number; body: unknown };
export interface V110Ctx {
  ok: (body: unknown, status?: number) => Res;
  err: (status: number, code: ErrorCode, key: string, params?: Record<string, string | number>) => Res;
  hydrateBox: (b: MockState["boxes"][number]) => Box;
  pushEvent: (e: Partial<BoxazEvent>) => unknown;
}

const DETACH_TTL_S = 600;
const BUFFER = 256 << 10;
const SSH_INTERNAL_PORT = 2222;
const FP = "SHA256:q3Vb9yJx0mZr4Kd2T1pWcE8nH6sLaG5uF7oRiY0Bv2M";
const KNOWN = `boxaz-ssh ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIBoxazMockGatewayHostKeyOnlyForTheDashboardDemo`;
const CORE_WS = "wss://api.boxaz.dev";

interface V110State {
  tickets: Record<string, AccessTicket[]>;
  terms: Record<string, TerminalSession[]>;
  keys: SshKey[];
  seq: number;
}
type G = typeof globalThis & { __boxazV110?: V110State };

const iso = (ms: number) => new Date(ms).toISOString();
const hex = (n: number) => Array.from({ length: n }, () => Math.floor(Math.random() * 16).toString(16)).join("");

function seed(): V110State {
  const now = Date.now();
  const term = (id: string, box: string, o: Partial<TerminalSession>): TerminalSession => ({
    id, box_id: box, cols: 120, rows: 32, viewers: 0, controlled: false, created_at: iso(now - 40 * 60_000), last_io: iso(now - 60_000), buffered_bytes: 18_432, actor: "user:anh", ...o,
  });
  const tk = (id: string, box: string, o: Partial<AccessTicket>): AccessTicket => ({
    id, box_id: box, purpose: "terminal", prefix: `bxa_${box}_${hex(5)}`, expires_at: iso(now - 30 * 60_000), created_at: iso(now - 31 * 60_000), actor: "user:anh", ...o,
  });
  return {
    seq: 0,
    terms: {
      box_a9d054: [
        term("term_k7d2a1", "box_a9d054", { name: "dev-server", viewers: 1, controlled: true, buffered_bytes: 96_512, last_io: iso(now - 4_000) }),
        term("term_m3q8c5", "box_a9d054", { name: "", detached_at: iso(now - 3 * 60_000), expires_at: iso(now + (DETACH_TTL_S - 180) * 1000), buffered_bytes: 7_204, created_at: iso(now - 25 * 60_000), last_io: iso(now - 3 * 60_000) }),
      ],
      box_7f3a91: [term("term_s1x9f0", "box_7f3a91", { viewers: 2, controlled: true, actor: "agent:shopee-crawler", buffered_bytes: 41_000, last_io: iso(now - 9_000) })],
    },
    tickets: {
      box_a9d054: [
        tk("tkt_9a01", "box_a9d054", { purpose: "terminal", session_id: "term_k7d2a1", redeemed_at: iso(now - 31 * 60_000 + 900), live: 1, created_at: iso(now - 31 * 60_000), expires_at: iso(now - 30 * 60_000) }),
        tk("tkt_9a02", "box_a9d054", { purpose: "tcp", port: 3000, redeemed_at: iso(now - 12 * 60_000), live: 1, actor: "key:laptop", created_at: iso(now - 12 * 60_000 - 400), expires_at: iso(now - 11 * 60_000) }),
        tk("tkt_9a03", "box_a9d054", { purpose: "ssh", created_at: iso(now - 2 * 3600_000), expires_at: iso(now - 2 * 3600_000 + 60_000) }),
      ],
    },
    keys: [
      { id: "sshk_01", name: "anh@macbook", public_key: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIK3m0ckLaptopKeyForBoxazDashboardDemoOnly anh@macbook", fingerprint: "SHA256:Zt8mWq2LxP0vR6yN4cB1eH9sK3aD7fG5jU2oI8nM0Qw", created_at: iso(now - 6 * 86400_000) },
    ],
  };
}

function st(): V110State {
  const g = globalThis as G;
  if (!g.__boxazV110) g.__boxazV110 = seed();
  return g.__boxazV110;
}

function nameOf(ctx: V110Ctx, b: MockState["boxes"][number]) {
  return ctx.hydrateBox(b).subdomain || b.slug;
}

export function forwardPortsOf(b: Box): number[] {
  const set = new Set<number>([SSH_INTERNAL_PORT, ...(b.ports ?? []), ...(b.forward_ports ?? [])]);
  return [...set].sort((x, y) => x - y);
}

function urls(ctx: V110Ctx, b: MockState["boxes"][number], kind: string, q = "") {
  return { url: `wss://${nameOf(ctx, b)}.${MOCK_GATEWAY_DOMAIN}/.boxaz/${kind}${q}`, core_url: `${CORE_WS}/v1/boxes/${b.id}/${kind}${q}` };
}

function liveTerms(s: V110State, boxId: string) {
  const now = Date.now();
  const list = (s.terms[boxId] ?? []).filter((t) => !t.expires_at || Date.parse(t.expires_at) > now);
  s.terms[boxId] = list;
  return list;
}

const pub = (t: AccessTicket): AccessTicket => {
  // eslint-disable-next-line @typescript-eslint/no-unused-vars
  const { ticket, url, core_url, command, ...rest } = t;
  return rest;
};

function access(ctx: V110Ctx, s: V110State, b: MockState["boxes"][number]): AccessInfo {
  const name = nameOf(ctx, b);
  const host = `${name}.${MOCK_GATEWAY_DOMAIN}`;
  const hb = ctx.hydrateBox(b);
  const tcp = urls(ctx, b, "tcp", "?port={port}");
  const term = urls(ctx, b, "terminal");
  const ssh = urls(ctx, b, "ssh");
  const terms = liveTerms(s, b.id);
  const live: Record<string, number> = {};
  const nt = terms.reduce((a, x) => a + x.viewers, 0);
  if (nt) live.terminal = nt;
  const ntcp = (s.tickets[b.id] ?? []).filter((t) => t.purpose === "tcp" && t.live && !t.revoked_at).length;
  if (ntcp) live.tcp = ntcp;
  const exec = b.provider === "upstash-box";
  const config = `Host ${host}\n  HostName ${host}\n  User boxaz\n  ProxyCommand box ssh-proxy %h\n  HostKeyAlias boxaz-ssh\n  UserKnownHostsFile ~/.boxaz/known_hosts\n  ServerAliveInterval 25\n\n`;
  return {
    box_id: b.id, name, host, link: exec ? "none" : b.provider === "e2b" ? "boxd" : "boxd", live,
    tcp: { ...tcp, allowed_ports: forwardPortsOf(hb), auth: "?ticket=bxa_… (purpose tcp, same port) or Authorization: Bearer <API key>" },
    terminal: { ...term, detach_ttl_s: DETACH_TTL_S, sessions: terms },
    ssh: {
      ...ssh, host, proxy_command: "box ssh-proxy %h", command: `box ssh ${name}`, config, known_hosts: KNOWN, host_key_fingerprint: FP, direct: "ssh.boxaz.dev:2222",
      features: "shell, exec, pty, env, window-change, signals, local port forwarding (ssh -L, VS Code / Cursor Remote-SSH), sftp when the box has an sftp-server; no -R / agent / X11 forwarding",
    },
    commands: {
      forward: `box forward ${name} <remote-port> [local-port]`, ssh: `box ssh ${name}`, ssh_exec: `box ssh ${name} -- uname -a`, ssh_config: "box ssh-config --write",
      ssh_proxy: `ssh -o ProxyCommand='box ssh-proxy %h' -o HostKeyAlias=boxaz-ssh boxaz@${host}`, ssh_direct: `ssh -p 2222 $(box ticket ${name} ssh)@ssh.boxaz.dev`,
    },
    notes: exec ? ["exec-only sandbox and core is not reachable from it (BOXAZ_PUBLIC_URL): terminal, forward and ssh need the in-box agent to dial in"] : undefined,
    tickets: `POST /v1/boxes/${b.id}/tickets {purpose: terminal|tcp|ssh, port?, ttl_s?}`,
  };
}

const KEY_RE = /^(ssh-ed25519|ssh-rsa|ecdsa-sha2-nistp(256|384|521)|sk-ssh-ed25519@openssh\.com|sk-ecdsa-sha2-nistp256@openssh\.com)\s+([A-Za-z0-9+/]{20,}={0,3})(\s+(.*))?$/;

function fingerprint(b64: string) {
  let h = 2166136261;
  for (let i = 0; i < b64.length; i++) h = Math.imul(h ^ b64.charCodeAt(i), 16777619) >>> 0;
  const abc = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
  let out = "";
  for (let i = 0; i < 43; i++) {
    h = Math.imul(h ^ (h >>> 13), 2654435761) >>> 0;
    out += abc[h % 64];
  }
  return `SHA256:${out}`;
}

export function handleV110(ms: MockState, ctx: V110Ctx, M: string, segs: string[], body: Record<string, unknown>): Res | null {
  const s = st();
  const [r0, r1, r2, r3] = segs;

  // ---- /ssh, /ssh/keys ----
  if (r0 === "ssh") {
    if (!r1 && M === "GET") return ctx.ok({ known_hosts: KNOWN, host_key_fingerprint: FP, host_key_alias: "boxaz-ssh", direct: "ssh.boxaz.dev:2222", proxy_command: "box ssh-proxy %h", domain: MOCK_GATEWAY_DOMAIN });
    if (r1 !== "keys") return null;
    if (!r2 && M === "GET") return ctx.ok({ items: s.keys });
    if (!r2 && M === "POST") {
      const raw = String(body.public_key ?? "").trim().replace(/\s+/g, " ");
      const m = raw.match(KEY_RE);
      if (!m) return ctx.err(400, "INVALID", "v110_ssh_key");
      const fp = fingerprint(m[3]);
      const dup = s.keys.find((k) => k.fingerprint === fp);
      if (dup) return ctx.ok(dup, 201); // như core: thêm lại cùng key là idempotent
      s.seq += 1;
      const k: SshKey = { id: `sshk_${s.seq.toString(16)}${hex(4)}`, name: String(body.name ?? "").trim() || m[5] || fp.slice(7, 19), public_key: raw, fingerprint: fp, created_at: iso(Date.now()) };
      s.keys.push(k);
      ctx.pushEvent({ actor: "user:anh", box_id: null, agent_id: null, type: "ssh.key", message: `ssh key ${k.name} (${fp}) added`, cost_usd: null });
      return ctx.ok(k, 201);
    }
    if (r2 && !r3 && M === "DELETE") {
      const i = s.keys.findIndex((k) => k.id === r2);
      if (i < 0) return ctx.err(404, "NOT_FOUND", "not_found", { what: `ssh key ${r2}` });
      s.keys.splice(i, 1);
      return ctx.ok({ deleted: true });
    }
    return null;
  }

  if (r0 !== "boxes" || !r1) return null;
  const b = ms.boxes.find((x) => x.id === r1 || x.slug === r1);

  // PATCH /boxes/{id} {forward_ports}: ghi rồi để v1.7 trả box (cùng request có thể đổi wake_on_request / subdomain).
  if (!r2 && M === "PATCH") {
    if (b && Array.isArray(body.forward_ports)) {
      const ports = [...new Set((body.forward_ports as unknown[]).map(Number).filter((p) => Number.isInteger(p) && p >= 1 && p <= 65535))].sort((x, y) => x - y);
      if (ports.length > 32) return ctx.err(400, "INVALID", "v110_forward_max");
      b.forward_ports = ports;
      ctx.pushEvent({ actor: "user:anh", box_id: b.id, agent_id: b.agent_id, type: "box.settings", message: `forward ports → [${ports.join(" ")}]`, cost_usd: null });
    }
    return null;
  }
  if (!["access", "tickets", "terminals", "forward-ports"].includes(r2 ?? "")) return null;
  if (!b) return ctx.err(404, "NOT_FOUND", "not_found", { what: `box ${r1}` });

  if (r2 === "access" && M === "GET") return ctx.ok(access(ctx, s, b));

  if (r2 === "forward-ports" && M === "POST") {
    const port = Number(body.port);
    if (!Number.isInteger(port) || port < 1 || port > 65535) return ctx.err(400, "INVALID", "v110_port");
    const hb = ctx.hydrateBox(b);
    if (!forwardPortsOf(hb).includes(port)) {
      if ((b.forward_ports ?? []).length >= 32) return ctx.err(400, "INVALID", "v110_forward_max");
      b.forward_ports = [...(b.forward_ports ?? []), port].sort((x, y) => x - y);
      ctx.pushEvent({ actor: "user:anh", box_id: b.id, agent_id: b.agent_id, type: "box.settings", message: `forward port ${port} allowed`, cost_usd: null });
    }
    return ctx.ok({ box_id: b.id, allowed_ports: forwardPortsOf(ctx.hydrateBox(b)) });
  }

  if (r2 === "terminals") {
    if (!r3 && M === "GET") return ctx.ok({ items: liveTerms(s, b.id), detach_ttl_s: DETACH_TTL_S });
    if (r3 && M === "DELETE") {
      const list = liveTerms(s, b.id);
      const hit = list.filter((t) => r3 === "all" || t.id === r3);
      if (!hit.length && r3 !== "all") return ctx.err(404, "NOT_FOUND", "not_found", { what: `terminal session ${r3}` });
      s.terms[b.id] = list.filter((t) => !hit.includes(t));
      return ctx.ok({ killed: hit.length });
    }
    return null;
  }

  // ---- tickets ----
  const list = (s.tickets[b.id] ??= []);
  if (!r3 && M === "GET") return ctx.ok({ items: list.map(pub) });
  if (r3 && M === "DELETE") {
    const now = iso(Date.now());
    const hit = list.filter((t) => (r3 === "all" || t.id === r3) && !t.revoked_at);
    if (!hit.length && r3 !== "all" && !list.some((t) => t.id === r3)) return ctx.err(404, "NOT_FOUND", "not_found", { what: `ticket ${r3}` });
    for (const t of hit) {
      t.revoked_at = now;
      if (t.live && t.purpose === "terminal" && t.session_id) {
        const sess = (s.terms[b.id] ?? []).find((x) => x.id === t.session_id);
        if (sess) { sess.viewers = Math.max(0, sess.viewers - t.live); sess.controlled = sess.viewers > 0 && sess.controlled; }
      }
      t.live = 0;
    }
    if (hit.length) ctx.pushEvent({ actor: "user:anh", box_id: b.id, agent_id: b.agent_id, type: "box.ticket", message: `${hit.length} ticket(s) revoked`, cost_usd: null });
    return ctx.ok({ revoked: hit.length });
  }
  if (!r3 && M === "POST") {
    if (["terminated", "failed"].includes(b.status)) return ctx.err(404, "NOT_FOUND", "v110_gone", { id: b.id });
    const purpose = String(body.purpose ?? "").trim().toLowerCase();
    if (!["terminal", "tcp", "ssh"].includes(purpose)) return ctx.err(400, "INVALID", "v110_purpose", { purpose: String(body.purpose ?? "") });
    let port = 0;
    if (purpose === "tcp") {
      port = Number(body.port);
      if (!Number.isInteger(port) || port < 1 || port > 65535) return ctx.err(400, "INVALID", "v110_port");
      if (!forwardPortsOf(ctx.hydrateBox(b)).includes(port)) {
        if (body.allow !== true) return ctx.err(403, "POLICY_DENIED", "v110_port_denied", { port });
        b.forward_ports = [...(b.forward_ports ?? []), port].sort((x, y) => x - y);
      }
    }
    const ttl = body.ttl_s === undefined || body.ttl_s === 0 ? 60 : Number(body.ttl_s);
    if (!(ttl >= 10 && ttl <= 86400)) return ctx.err(400, "INVALID", "v110_ttl");
    const now = Date.now();
    const raw = `bxa_${b.id}_${hex(40)}`;
    s.seq += 1;
    let sessionId = purpose === "terminal" && typeof body.session_id === "string" ? body.session_id : "";
    const t: AccessTicket = {
      id: `tkt_${s.seq.toString(16)}${hex(4)}`, box_id: b.id, purpose: purpose as AccessTicket["purpose"], ...(port ? { port } : {}), ...(sessionId ? { session_id: sessionId } : {}),
      prefix: raw.slice(0, 4 + b.id.length + 5), expires_at: iso(now + ttl * 1000), created_at: iso(now), actor: "user:anh",
    };
    // Mock: ticket terminal được coi như đã đổi ngay (trình duyệt không mở được WS thật ở mock).
    if (purpose === "terminal") {
      const terms = liveTerms(s, b.id);
      let sess = sessionId ? terms.find((x) => x.id === sessionId) : undefined;
      if (!sess) {
        sess = { id: `term_${hex(6)}`, box_id: b.id, cols: 120, rows: 32, viewers: 0, controlled: false, created_at: iso(now), last_io: iso(now), buffered_bytes: 0, actor: "user:anh" };
        terms.push(sess);
        sessionId = sess.id;
      }
      sess.viewers += 1;
      sess.controlled = true;
      sess.detached_at = null;
      sess.expires_at = null;
      t.redeemed_at = iso(now + 300);
      t.session_id = sessionId;
      t.live = 1;
    }
    list.push(t);
    if (list.length > 200) list.splice(0, list.length - 200);
    ctx.pushEvent({ actor: "user:anh", box_id: b.id, agent_id: b.agent_id, type: "box.ticket", message: `${purpose} ticket ${t.prefix}… issued (redeem before ${t.expires_at})`, cost_usd: null });
    const q = purpose === "tcp" ? `?port=${port}` : purpose === "terminal" && body.session_id ? `?session=${encodeURIComponent(String(body.session_id))}` : "";
    const u = urls(ctx, b, purpose, q);
    const out: AccessTicket = { ...t, ticket: raw, url: `${u.url}${q ? "&" : "?"}ticket=${raw}`, core_url: u.core_url };
    if (purpose === "ssh") out.command = `ssh -p 2222 ${raw}@ssh.boxaz.dev`;
    return ctx.ok(out, 201);
  }
  return null;
}

/** Mock: thả viewer khi trang terminal đóng — không có route; session tự detach trong UI mock. */
export const MOCK_DETACH_TTL_S = DETACH_TTL_S;
export const MOCK_TERMINAL_BUFFER = BUFFER;

/** v1.11: ghi thêm bản ghi ticket (cdp / vnc / vnc_share / ssh_cert) để bảng ticket ở tab Truy cập thấy được. */
export function mockRecordTicket(t: AccessTicket) {
  const s = st();
  const list = (s.tickets[t.box_id] ??= []);
  list.push(t);
  if (list.length > 200) list.splice(0, list.length - 200);
}
/** v1.11: thu hồi bản ghi ticket theo id (share link). */
export function mockRevokeTicketRecord(boxId: string, ids: string[]) {
  const now = new Date().toISOString();
  for (const t of st().tickets[boxId] ?? []) if (ids.includes(t.id) && !t.revoked_at) { t.revoked_at = now; t.live = 0; }
}
