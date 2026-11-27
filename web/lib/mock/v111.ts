// MOCK MODE v1.11: truy cập P1 — browser (CDP), desktop (VNC) + share link, SSH certificate.
// Bám theo core/internal/api/access_p1.go + manager/{browser,desktop,sshcert}.go.
// WebSocket CDP / RFB không giả lập được qua route Next: UI hiện màn hình mô phỏng + ghi chú mock,
// còn trạng thái, ticket, share link, chứng chỉ là mock thật (giữ trong bộ nhớ).
import type { AccessTicket, Box, BoxmesEvent, BrowserSession, BrowserState, DesktopSession, DesktopShare, DesktopState, ErrorCode, SshCert } from "../types";
import { MOCK_GATEWAY_DOMAIN } from "./v18";
import { mockRecordTicket, mockRevokeTicketRecord } from "./v110";
import type { MockState } from "./seed";

type Res = { status: number; body: unknown };
export interface V111Ctx {
  ok: (body: unknown, status?: number) => Res;
  err: (status: number, code: ErrorCode, key: string, params?: Record<string, string | number>) => Res;
  hydrateBox: (b: MockState["boxes"][number]) => Box;
  pushEvent: (e: Partial<BoxmesEvent>) => unknown;
}

const CDP_PORT = 9222;
const VNC_PORT = 5900;
const BROWSER_IDLE_S = 600;
const DESKTOP_IDLE_S = 900;
const MAX_CERT_TTL_S = 43200;
const CORE = "api.boxmes.dev";
const USER_CA = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIMockBoxmesUserCertificateAuthorityForDashboardOnly boxmes-user-ca";
const KNOWN = "boxmes-ssh ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIBoxmesMockGatewayHostKeyOnlyForTheDashboardDemo";
const KEY_RE = /^(ssh-ed25519|ssh-rsa|ecdsa-sha2-nistp(256|384|521)|sk-ssh-ed25519@openssh\.com|sk-ecdsa-sha2-nistp256@openssh\.com)\s+([A-Za-z0-9+/]{20,}={0,3})(\s+.*)?$/;

interface Addon { browser: boolean; headless: boolean; browserEver: boolean; desktop: boolean; desktopEver: boolean; width: number; height: number }
interface V111State { addons: Record<string, Addon>; shares: Record<string, (DesktopShare & { _raw: string })[]>; seq: number }
type G = typeof globalThis & { __boxmesV111?: V111State };

const iso = (ms: number) => new Date(ms).toISOString();
const hex = (n: number) => Array.from({ length: n }, () => Math.floor(Math.random() * 16).toString(16)).join("");

function seed(): V111State {
  const now = Date.now();
  return {
    seq: 0,
    addons: { box_a9d054: { browser: true, headless: false, browserEver: true, desktop: true, desktopEver: true, width: 1280, height: 800 } },
    shares: {
      box_a9d054: [
        { id: "shr_seed01", box_id: "box_a9d054", prefix: "bxs_box_a9d054_3f9a1", view_only: true, expires_at: iso(now + 42 * 60_000), created_at: iso(now - 18 * 60_000), viewers: 1, uses: 3, actor: "user:anh", _raw: "" },
        { id: "shr_seed02", box_id: "box_a9d054", prefix: "bxs_box_a9d054_88c0e", view_only: true, expires_at: iso(now - 2 * 3600_000), created_at: iso(now - 3 * 3600_000), viewers: 0, uses: 1, actor: "user:anh", revoked_at: iso(now - 150 * 60_000), _raw: "" },
      ],
    },
  };
}
function st(): V111State {
  const g = globalThis as G;
  if (!g.__boxmesV111) g.__boxmesV111 = seed();
  return g.__boxmesV111;
}
function addon(s: V111State, id: string): Addon {
  return (s.addons[id] ??= { browser: false, headless: true, browserEver: false, desktop: false, desktopEver: false, width: 1280, height: 800 });
}

const nameOf = (ctx: V111Ctx, b: MockState["boxes"][number]) => ctx.hydrateBox(b).subdomain || b.slug;
const hostOf = (ctx: V111Ctx, b: MockState["boxes"][number]) => `${nameOf(ctx, b)}.${MOCK_GATEWAY_DOMAIN}`;
const version = { Browser: "Chrome/131.0.6778.85", "Protocol-Version": "1.3", "User-Agent": "Mozilla/5.0 (X11; Linux x86_64) HeadlessChrome/131.0.6778.85", "V8-Version": "13.1.201.9" };

function activeShares(s: V111State, boxId: string) {
  const now = Date.now();
  return (s.shares[boxId] ?? []).filter((x) => !x.revoked_at && Date.parse(x.expires_at) > now);
}
const pubShare = (x: DesktopShare & { _raw: string }): DesktopShare => {
  // eslint-disable-next-line @typescript-eslint/no-unused-vars
  const { _raw, url, ...rest } = x;
  return rest;
};

function browserState(s: V111State, b: MockState["boxes"][number]): BrowserState {
  const a = addon(s, b.id);
  const st: BrowserState = { box_id: b.id, running: false, headless: a.headless, started: false, clients: a.browser ? 1 : 0, idle_stop_s: BROWSER_IDLE_S, port: CDP_PORT };
  if (b.status !== "running") return { ...st, clients: 0, note: `box is ${b.status}; POST /v1/boxes/{id}/browser wakes it and starts the browser` };
  if (!a.browser) return { ...st, clients: 0 };
  return { ...st, running: true, source: a.headless ? "installed" : "found", binary: a.headless ? "/root/.cache/ms-playwright/chromium_headless_shell-1148/chrome-linux/headless_shell" : "/usr/bin/chromium", version };
}

function desktopState(s: V111State, b: MockState["boxes"][number]): DesktopState {
  const a = addon(s, b.id);
  const st: DesktopState = { box_id: b.id, running: false, started: false, viewers: 0, shares: activeShares(s, b.id).length, idle_stop_s: DESKTOP_IDLE_S, port: VNC_PORT, browser_on_desktop: false };
  if (b.status !== "running") return { ...st, note: `box is ${b.status}; POST /v1/boxes/{id}/desktop wakes it and starts the desktop` };
  if (!a.desktop) return st;
  const viewers = activeShares(s, b.id).reduce((n, x) => n + x.viewers, 0);
  return { ...st, running: true, display: ":99", width: a.width, height: a.height, vnc_server: "x11vnc", wm: "openbox", viewers, browser_on_desktop: a.browser && !a.headless };
}

function ticketRec(b: MockState["boxes"][number], purpose: AccessTicket["purpose"], ttlS: number, extra: Partial<AccessTicket> = {}): { raw: string; t: AccessTicket } {
  const now = Date.now();
  const raw = `bxa_${b.id}_${hex(40)}`;
  const t: AccessTicket = { id: `tkt_${hex(8)}`, box_id: b.id, purpose, prefix: raw.slice(0, 4 + b.id.length + 5), expires_at: iso(now + ttlS * 1000), created_at: iso(now), actor: "user:anh", ...extra };
  mockRecordTicket(t);
  return { raw, t };
}

function wake(ctx: V111Ctx, b: MockState["boxes"][number]) {
  if (b.status === "sleeping") {
    b.status = "running";
    b.last_active_at = iso(Date.now());
    ctx.pushEvent({ actor: "user:anh", box_id: b.id, agent_id: b.agent_id, type: "box.waking", message: `${b.slug} woken for access`, cost_usd: null });
  }
}

export function handleV111(ms: MockState, ctx: V111Ctx, M: string, segs: string[], body: Record<string, unknown>): Res | null {
  const s = st();
  const [r0, r1, r2, r3, r4] = segs;

  // ---- POST /ssh/certs ----
  if (r0 === "ssh" && r1 === "certs" && !r2 && M === "POST") {
    const ref = String(body.box ?? "").trim();
    if (!ref) return ctx.err(400, "INVALID", "v111_box_required");
    const b = ms.boxes.find((x) => x.id === ref || x.slug === ref || ctx.hydrateBox(x).subdomain === ref);
    if (!b) return ctx.err(404, "NOT_FOUND", "not_found", { what: `box ${ref}` });
    if (["terminated", "failed"].includes(b.status)) return ctx.err(404, "NOT_FOUND", "v110_gone", { id: b.id });
    const m = String(body.public_key ?? "").trim().replace(/\s+/g, " ").match(KEY_RE);
    if (!m) return ctx.err(400, "INVALID", "v110_ssh_key");
    const ttl = body.ttl_s === undefined || body.ttl_s === 0 ? 3600 : Number(body.ttl_s);
    if (!(ttl >= 60 && ttl <= MAX_CERT_TTL_S)) return ctx.err(400, "INVALID", "v111_cert_ttl");
    const now = Date.now();
    const name = nameOf(ctx, b);
    const host = hostOf(ctx, b);
    const fp = `SHA256:${btoa(m[3].slice(-32)).replace(/=+$/, "").slice(0, 43)}`;
    const principals = [b.id, ...new Set([ctx.hydrateBox(b).subdomain, b.slug].filter((x): x is string => !!x && x !== b.id))];
    const { t } = ticketRec(b, "ssh_cert", ttl);
    t.prefix = `cert ${fp.slice(0, 19)}…`;
    const certType = `${m[1].replace(/@openssh\.com$/, "")}-cert-v01@openssh.com`;
    const cert: SshCert = {
      id: t.id, box_id: b.id, certificate: `${certType} AAAAIHNzaC1lZDI1NTE5LWNlcnQtdjAxQG9wZW5zc2guY29tMockCertificate${hex(48)} ${name}`,
      serial: Math.floor(Math.random() * 2 ** 52), principals, valid_after: iso(now - 60_000), valid_before: iso(now + ttl * 1000), fingerprint: fp, host, user: name,
      proxy_command: "box ssh-proxy --anon %h", known_hosts: KNOWN, user_ca: USER_CA,
      command: `ssh -o CertificateFile=<key>-cert.pub -i <key> -o ProxyCommand='box ssh-proxy --anon %h' -o HostKeyAlias=boxmes-ssh ${name}@${host}`,
      direct_command: `ssh -p 2222 -o CertificateFile=<key>-cert.pub -i <key> ${name}@ssh.boxmes.dev`,
      config: `Host ${host}\n  HostName ${host}\n  User ${name}\n  ProxyCommand box ssh-proxy --anon %h\n  CertificateFile ~/.ssh/boxmes-${name}-cert.pub\n  IdentityFile ~/.ssh/id_ed25519\n  HostKeyAlias boxmes-ssh\n  UserKnownHostsFile ~/.boxmes/known_hosts\n`,
    };
    ctx.pushEvent({ actor: "user:anh", box_id: b.id, agent_id: b.agent_id, type: "box.ticket", message: `SSH certificate ${t.id} issued for ${fp} (valid until ${cert.valid_before})`, cost_usd: null });
    return ctx.ok(cert, 201);
  }

  if (r0 !== "boxes" || !r1 || !["browser", "desktop"].includes(r2 ?? "")) return null;
  const b = ms.boxes.find((x) => x.id === r1 || x.slug === r1);
  if (!b) return ctx.err(404, "NOT_FOUND", "not_found", { what: `box ${r1}` });
  const gone = ["terminated", "failed"].includes(b.status);
  const a = addon(s, b.id);
  const host = hostOf(ctx, b);

  // ---- browser ----
  if (r2 === "browser" && !r3) {
    if (M === "GET") return ctx.ok(browserState(s, b));
    if (M === "DELETE") {
      if (b.status !== "running") return ctx.ok({ box_id: b.id, stopped: false, note: "box is not running" });
      a.browser = false;
      ctx.pushEvent({ actor: "user:anh", box_id: b.id, agent_id: b.agent_id, type: "box.browser", message: "browser stopped (requested)", cost_usd: null });
      return ctx.ok({ box_id: b.id, stopped: true });
    }
    if (M === "POST") {
      if (gone) return ctx.err(404, "NOT_FOUND", "v110_gone", { id: b.id });
      const ttl = body.ttl_s === undefined || body.ttl_s === 0 ? 3600 : Number(body.ttl_s);
      if (!(ttl >= 10 && ttl <= 86400)) return ctx.err(400, "INVALID", "v110_ttl");
      wake(ctx, b);
      const started = !a.browser;
      const first = !a.browserEver;
      a.browser = true;
      a.browserEver = true;
      a.headless = Boolean(body.headless) || !a.desktop;
      const { raw, t } = ticketRec(b, "cdp", ttl);
      const q = `/.boxmes/cdp?ticket=${raw}`;
      const out: BrowserSession = {
        ...browserState(s, b), started, ...(started ? { start_ms: first ? 17400 : 2100 + Math.round(Math.random() * 600) } : {}), clients: 0,
        cdp_url: `wss://${host}${q}`, http_url: `https://${host}${q}`, core_url: `wss://${CORE}/v1/boxes/${b.id}/cdp?ticket=${raw}`, ticket_id: t.id, expires_at: t.expires_at,
        ...(a.headless ? {} : { live_view: "desktop" }),
      };
      if (started) ctx.pushEvent({ actor: "user:anh", box_id: b.id, agent_id: b.agent_id, type: "box.browser", message: `browser started (${a.headless ? "headless" : "on desktop"}) in ${out.start_ms} ms`, cost_usd: null });
      return ctx.ok(out, 201);
    }
    return null;
  }

  // ---- desktop ----
  if (r2 === "desktop" && !r3) {
    if (M === "GET") return ctx.ok(desktopState(s, b));
    if (M === "DELETE") {
      if (b.status !== "running") return ctx.ok({ box_id: b.id, stopped: false, note: "box is not running" });
      a.desktop = false;
      if (a.browser && !a.headless) a.headless = true;
      for (const x of s.shares[b.id] ?? []) x.viewers = 0;
      ctx.pushEvent({ actor: "user:anh", box_id: b.id, agent_id: b.agent_id, type: "box.desktop", message: "desktop stopped (requested)", cost_usd: null });
      return ctx.ok({ box_id: b.id, stopped: true });
    }
    if (M === "POST") {
      if (gone) return ctx.err(404, "NOT_FOUND", "v110_gone", { id: b.id });
      const ttl = body.ttl_s === undefined || body.ttl_s === 0 ? 60 : Number(body.ttl_s);
      if (!(ttl >= 10 && ttl <= 86400)) return ctx.err(400, "INVALID", "v110_ttl");
      wake(ctx, b);
      const started = !a.desktop;
      const first = !a.desktopEver;
      a.desktop = true;
      a.desktopEver = true;
      if (Number(body.width) >= 640 && Number(body.height) >= 480) { a.width = Number(body.width); a.height = Number(body.height); }
      const viewOnly = Boolean(body.view_only);
      const { raw, t } = ticketRec(b, "vnc", ttl, { redeemed_at: iso(Date.now() + 400), live: 1 });
      const out: DesktopSession = {
        ...desktopState(s, b), started, ...(started ? { start_ms: first ? 6600 : 900 + Math.round(Math.random() * 400) } : {}),
        vnc_url: `wss://${host}/.boxmes/vnc?ticket=${raw}${viewOnly ? "&view_only=1" : ""}`, viewer_url: `https://${host}/.boxmes/desktop?ticket=${raw}${viewOnly ? "&view_only=1" : ""}`,
        core_url: `wss://${CORE}/v1/boxes/${b.id}/vnc?ticket=${raw}`, ticket_id: t.id, view_only: viewOnly, expires_at: t.expires_at,
      };
      if (started) ctx.pushEvent({ actor: "user:anh", box_id: b.id, agent_id: b.agent_id, type: "box.desktop", message: `desktop started ${a.width}x${a.height} in ${out.start_ms} ms`, cost_usd: null });
      return ctx.ok(out, 201);
    }
    return null;
  }

  // ---- desktop share links ----
  if (r2 === "desktop" && r3 === "shares") {
    const list = (s.shares[b.id] ??= []);
    if (!r4 && M === "GET") return ctx.ok({ items: list.map(pubShare) });
    if (!r4 && M === "POST") {
      if (gone) return ctx.err(404, "NOT_FOUND", "v110_gone", { id: b.id });
      const ttl = body.ttl_s === undefined || body.ttl_s === 0 ? 3600 : Number(body.ttl_s);
      if (!(ttl >= 60 && ttl <= 86400)) return ctx.err(400, "INVALID", "v111_share_ttl");
      const viewOnly = body.view_only === undefined ? true : Boolean(body.view_only);
      const now = Date.now();
      const raw = `bxs_${b.id}_${hex(40)}`;
      s.seq += 1;
      const sh = { id: `shr_${s.seq.toString(16)}${hex(5)}`, box_id: b.id, prefix: raw.slice(0, 4 + b.id.length + 5), view_only: viewOnly, expires_at: iso(now + ttl * 1000), created_at: iso(now), viewers: 0, uses: 0, actor: "user:anh", _raw: raw };
      list.push(sh);
      mockRecordTicket({ id: sh.id, box_id: b.id, purpose: "vnc_share", prefix: sh.prefix, expires_at: sh.expires_at, created_at: sh.created_at, actor: "user:anh" });
      ctx.pushEvent({ actor: "user:anh", box_id: b.id, agent_id: b.agent_id, type: "box.ticket", message: `desktop share ${sh.prefix}… (${viewOnly ? "view-only" : "interactive"}) until ${sh.expires_at}`, cost_usd: null });
      return ctx.ok({ ...pubShare(sh), url: `https://${host}/.boxmes/desktop?share=${raw}` }, 201);
    }
    if (r4 && M === "DELETE") {
      const hit = list.filter((x) => r4 === "all" || x.id === r4);
      if (!hit.length && r4 !== "all") return ctx.err(404, "NOT_FOUND", "not_found", { what: `share ${r4}` });
      const now = iso(Date.now());
      let revoked = 0, closed = 0;
      for (const x of hit) if (!x.revoked_at) { x.revoked_at = now; revoked++; closed += x.viewers; x.viewers = 0; }
      mockRevokeTicketRecord(b.id, hit.map((x) => x.id));
      if (revoked) ctx.pushEvent({ actor: "user:anh", box_id: b.id, agent_id: b.agent_id, type: "box.ticket", message: `${revoked} desktop share link(s) revoked, ${closed} viewer(s) closed`, cost_usd: null });
      return ctx.ok({ revoked, viewers_closed: closed });
    }
  }
  return null;
}

/** GET /boxes/{id}/access ở mock: thêm cdp / desktop + lệnh v1.11. */
export function accessP1(ctx: V111Ctx, b: MockState["boxes"][number], info: Record<string, unknown>) {
  const s = st();
  const a = addon(s, b.id);
  const name = nameOf(ctx, b);
  const host = hostOf(ctx, b);
  const live = (info.live ?? {}) as Record<string, number>;
  if (a.browser) live.cdp = 1;
  if (a.desktop) { const v = activeShares(s, b.id).reduce((n, x) => n + x.viewers, 0); if (v) live.vnc = v; }
  info.live = live;
  info.cdp = { url: `wss://${host}/.boxmes/cdp?ticket={ticket}`, core_url: `wss://${CORE}/v1/boxes/${b.id}/cdp?ticket={ticket}`, start: `POST /v1/boxes/${b.id}/browser`, running: a.browser && b.status === "running", clients: a.browser ? 1 : 0 };
  info.desktop = { url: `wss://${host}/.boxmes/vnc?ticket={ticket}`, core_url: `wss://${CORE}/v1/boxes/${b.id}/vnc?ticket={ticket}`, page: `https://${host}/.boxmes/desktop?ticket={ticket}`, start: `POST /v1/boxes/${b.id}/desktop`, running: a.desktop && b.status === "running", clients: live.vnc ?? 0 };
  const c = (info.commands ?? {}) as Record<string, string>;
  Object.assign(c, {
    ssh_cert: `box ssh-cert ${name} --ttl 12h`, sftp: `box sftp ${name} put ./file.txt /tmp/`,
    scp: `scp -o ProxyCommand='box ssh-proxy %h' -o HostKeyAlias=boxmes-ssh ./file.txt boxmes@${host}:/tmp/`,
    browser: `box browser ${name}`, desktop: `box desktop ${name}`, desktop_share: `box desktop ${name} --share --ttl 1h`,
  });
  info.commands = c;
  return info;
}
