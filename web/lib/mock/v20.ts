// MOCK MODE v2.0: tenancy — /auth/me, đổi workspace, workspace, thành viên, lời mời, gói, hạn mức, vị trí box.
// Bám core/internal/manager/{tenancy,workspaces,quota,location}.go. Trạng thái giữ trong bộ nhớ (globalThis).
import type { Box, BoxLocation, ErrorCode, LocationRow, LocationsView, Me, Member, PlanInfo, Quota, RegionRow, Workspace, WorkspaceInvite, WorkspaceRole } from "../types";
import type { MockState } from "./seed";

type Res = { status: number; body: unknown };
interface Ctx {
  ok: (b: unknown, status?: number) => Res;
  list: <T>(items: T[]) => Res;
  err: (status: number, code: ErrorCode, key: string, params?: Record<string, string | number>) => Res;
}

const Q = (plan: string, o: Partial<Quota>): Quota => ({
  plan, max_boxes: 0, max_cpu_per_box: 0, max_ram_mb_per_box: 0, max_ttl_hours: 0, public_url: true, egress: "open",
  max_members: 0, max_agents: 0, max_keys: 0, max_secrets: 0, max_boxes_per_minute: 0, ...o,
});
const PLANS: Record<string, Quota> = {
  free: Q("free", { max_boxes: 1, max_cpu_per_box: 1, max_ram_mb_per_box: 2048, max_ttl_hours: 2, public_url: false, egress: "restricted", max_members: 1, max_agents: 3, max_keys: 5, max_secrets: 10, max_boxes_per_minute: 5 }),
  pro: Q("pro", { max_boxes: 10, max_cpu_per_box: 8, max_ram_mb_per_box: 16384, max_ttl_hours: 24, max_members: 3, max_agents: 25, max_keys: 50, max_secrets: 100, max_boxes_per_minute: 30 }),
  team: Q("team", { max_boxes: 50, max_cpu_per_box: 16, max_ram_mb_per_box: 32768, max_ttl_hours: 168, max_boxes_per_minute: 120 }),
  unlimited: Q("unlimited", {}),
};
const ALL_PERMS = ["view", "boxes:write", "exec", "agents:write", "secrets:read", "secrets:write", "secrets:use", "templates:write", "billing:read", "members:write", "audit:read", "emergency_stop", "workspace:write", "approvals:decide"];
const ROLE_PERMS: Record<string, string[]> = {
  owner: ALL_PERMS,
  admin: ALL_PERMS.filter((p) => p !== "billing:read"),
  developer: ["view", "boxes:write", "exec", "agents:write", "secrets:read", "secrets:use", "templates:write"],
  billing: ["view", "billing:read"],
  viewer: ["view", "secrets:read"],
  auditor: ["view", "secrets:read", "audit:read"],
};

interface WS { id: string; name: string; plan: string; status: string; created_at: string; owner_user_id: string }
interface MB { ws: string; user: string; role: WorkspaceRole; at: string }
interface Inv extends Omit<WorkspaceInvite, "status"> { token: string }
interface T20 {
  current: string;
  workspaces: WS[];
  members: MB[];
  users: Record<string, { email: string; name: string; email_verified: boolean }>;
  invites: Inv[];
  waitlist: string[];
  seq: number;
}

const ME = "usr_root";
const now = () => new Date().toISOString();
const ago = (ms: number) => new Date(Date.now() - ms).toISOString();
const DAY = 86_400_000;

type G = typeof globalThis & { __boxmesMock20?: T20 };
function st(): T20 {
  const g = globalThis as G;
  if (!g.__boxmesMock20) {
    g.__boxmesMock20 = {
      current: "ws_default",
      seq: 0,
      workspaces: [
        { id: "ws_default", name: "Boxmes", plan: "unlimited", status: "active", created_at: ago(40 * DAY), owner_user_id: ME },
        { id: "ws_kecon", name: "kecon-team", plan: "pro", status: "active", created_at: ago(12 * DAY), owner_user_id: "usr_lan" },
        { id: "ws_acme", name: "acme-labs", plan: "free", status: "active", created_at: ago(30 * DAY), owner_user_id: "usr_lan" },
      ],
      users: {
        [ME]: { email: "anh@boxmes.local", name: "Anh", email_verified: true },
        usr_lan: { email: "lan@kecon.vn", name: "Lan", email_verified: true },
        usr_minh: { email: "minh@kecon.vn", name: "Minh", email_verified: false },
      },
      members: [
        { ws: "ws_default", user: ME, role: "owner", at: ago(40 * DAY) },
        { ws: "ws_kecon", user: "usr_lan", role: "owner", at: ago(12 * DAY) },
        { ws: "ws_kecon", user: ME, role: "admin", at: ago(10 * DAY) },
        { ws: "ws_kecon", user: "usr_minh", role: "developer", at: ago(3 * DAY) },
        { ws: "ws_acme", user: "usr_lan", role: "owner", at: ago(30 * DAY) },
      ],
      invites: [
        { id: "wsi_p1", workspace_id: "ws_kecon", email: "hoa@kecon.vn", role: "viewer", invited_by: "user:usr_lan", expires_at: new Date(Date.now() + 5 * DAY).toISOString(), accepted_at: null, created_at: ago(2 * DAY), token: "bxi_demo_pending" },
        { id: "wsi_e1", workspace_id: "ws_kecon", email: "old@kecon.vn", role: "developer", invited_by: "user:usr_lan", expires_at: ago(DAY), accepted_at: null, created_at: ago(8 * DAY), token: "bxi_demo_expired" },
        { id: "wsi_a1", workspace_id: "ws_kecon", email: "minh@kecon.vn", role: "developer", invited_by: "user:usr_lan", expires_at: ago(-4 * DAY), accepted_at: ago(3 * DAY), accepted_by: "usr_minh", created_at: ago(4 * DAY), token: "bxi_demo_accepted" },
        { id: "wsi_p2", workspace_id: "ws_acme", email: "anh@boxmes.local", role: "developer", invited_by: "user:usr_lan", expires_at: new Date(Date.now() + 6 * DAY).toISOString(), accepted_at: null, created_at: ago(DAY), token: "bxi_demo_join" },
      ],
      waitlist: [],
    };
  }
  return g.__boxmesMock20;
}

const inviteStatus = (i: Inv) => (i.accepted_at ? "accepted" : Date.parse(i.expires_at) <= Date.now() ? "expired" : "pending");
const pub = (i: Inv): WorkspaceInvite => {
  const { token: _t, ...rest } = i;
  void _t;
  return { ...rest, status: inviteStatus(i) };
};
const roleIn = (t: T20, ws: string) => t.members.find((m) => m.ws === ws && m.user === ME)?.role ?? "";
const wsView = (t: T20, w: WS): Workspace => ({ ...w, role: roleIn(t, w.id) as WorkspaceRole });

export function mockMe(): Me {
  const t = st();
  const mine = t.workspaces.filter((w) => t.members.some((m) => m.ws === w.id && m.user === ME)).map((w) => wsView(t, w));
  const cur = mine.find((w) => w.id === t.current) ?? null;
  return {
    user: { id: ME, ...t.users[ME], platform_admin: true, created_at: ago(40 * DAY) },
    workspace: cur,
    workspaces: mine,
    role: cur?.role ?? "",
    permissions: ALL_PERMS,
    platform_admin: true,
  };
}

/** Vị trí suy từ provider + vùng (rút gọn bảng sites của core/internal/manager/location.go). */
const SITES: Record<string, [string, string, string]> = {
  fsn: ["eu", "DE", "Falkenstein"], nbg: ["eu", "DE", "Nuremberg"], hel: ["eu", "FI", "Helsinki"], "eu-de": ["eu", "DE", ""],
  sgp: ["apac", "SG", "Singapore"], nrt: ["apac", "JP", "Tokyo"], fra: ["eu", "DE", "Frankfurt"], ewr: ["us", "US", "New Jersey"],
  "us-east-": ["us", "US", ""], ash: ["us", "US", "Ashburn"], nyc: ["us", "US", "New York"],
};
const SANDBOX = ["e2b", "upstash-box", "daytona", "modal", "prized"];
export function boxLocation(b: Pick<Box, "provider" | "region" | "host_id">): BoxLocation | undefined {
  if (!b.provider) return undefined;
  const code = (b.region ?? "").toLowerCase().replace(/\d+$/, "");
  const si = SITES[code];
  const loc: BoxLocation = { provider: b.provider, region: "other", site: code ? `${b.provider}-${code}` : b.provider, host: b.host_id || undefined };
  if (SANDBOX.includes(b.provider)) loc.region = "sandbox";
  else if (b.provider === "local") loc.region = "local";
  else if (si) [loc.region, loc.country, loc.city] = si;
  else if (code.startsWith("eu")) loc.region = "eu";
  else if (code.startsWith("us")) loc.region = "us";
  if (loc.region === "sandbox" && si) [, loc.country, loc.city] = si;
  if (!loc.city) delete loc.city;
  if (!loc.country) delete loc.country;
  return loc;
}

function locations(s: MockState, spentToday: (boxId: string) => number): LocationsView {
  const rows = new Map<string, LocationRow>();
  for (const b of s.boxes) {
    const active = ["running", "sleeping", "provisioning", "placing", "requested", "snapshotting"].includes(b.status);
    const cost = spentToday(b.id);
    if (!active && cost === 0) continue;
    const loc = boxLocation(b);
    if (!loc) continue;
    const r = rows.get(loc.site) ?? { ...loc, host: undefined, running: 0, sleeping: 0, total: 0, cost_today_usd: 0 };
    if (active) {
      r.total++;
      if (b.status === "running") r.running++;
      if (b.status === "sleeping") r.sleeping++;
    }
    r.cost_today_usd = Math.round((r.cost_today_usd + cost) * 1e4) / 1e4;
    rows.set(loc.site, r);
  }
  const items = [...rows.values()].sort((a, b) => b.total - a.total || a.site.localeCompare(b.site));
  const regs = new Map<string, RegionRow>();
  const total: RegionRow = { region: "all", running: 0, sleeping: 0, total: 0, cost_today_usd: 0 };
  for (const r of items) {
    const g = regs.get(r.region) ?? { region: r.region, running: 0, sleeping: 0, total: 0, cost_today_usd: 0 };
    g.running += r.running; g.sleeping += r.sleeping; g.total += r.total; g.cost_today_usd = Math.round((g.cost_today_usd + r.cost_today_usd) * 1e4) / 1e4;
    regs.set(r.region, g);
    total.running += r.running; total.sleeping += r.sleeping; total.total += r.total; total.cost_today_usd = Math.round((total.cost_today_usd + r.cost_today_usd) * 1e4) / 1e4;
  }
  return { items, regions: [...regs.values()].sort((a, b) => a.region.localeCompare(b.region)), total };
}

export function handleV20(s: MockState, c: Ctx, M: string, segs: string[], q: URLSearchParams, body: Record<string, unknown>, spentToday: (boxId: string) => number): Res | null {
  const t = st();
  const [r0, r1, r2, r3] = segs;
  const p = segs.join("/");
  const cur = () => t.workspaces.find((w) => w.id === t.current)!;
  const curView = () => {
    const w = cur();
    const quota = PLANS[w.plan] ?? PLANS.free;
    return { ...wsView(t, w), plan: w.plan, quota, permissions: ALL_PERMS, member_count: t.members.filter((m) => m.ws === w.id).length };
  };
  const owners = () => t.members.filter((m) => m.ws === t.current && m.role === "owner").length;

  if (p === "auth/me" && M === "GET") return c.ok(mockMe());
  if (p === "auth/config" && M === "GET") return c.ok({ signup_mode: "open", first_user: false, min_password_len: 8, email_verification: true, email_configured: false });
  if (p === "auth/workspace" && M === "POST") {
    const id = String(body.workspace_id ?? "");
    if (!t.members.some((m) => m.ws === id && m.user === ME)) return c.err(404, "NOT_FOUND", "not_found", { what: `workspace ${id}` });
    t.current = id;
    return c.ok(mockMe());
  }
  if (p === "auth/verify-email/resend" && M === "POST") return c.ok({ ok: true }, 202);
  if (r0 === "plans" && M === "GET") return c.list<PlanInfo>(Object.entries(PLANS).map(([id, quota]) => ({ id, quota })));
  if (r0 === "waitlist" && M === "POST") {
    const e = String(body.email ?? "").toLowerCase();
    if (e && !t.waitlist.includes(e)) t.waitlist.push(e);
    return c.ok({ email: e, position: t.waitlist.indexOf(e) + 128 }, 201);
  }
  if (p === "workspaces" && M === "POST") {
    const name = String(body.name ?? "").trim().slice(0, 60);
    if (!name) return c.err(400, "INVALID", "v20_invalid", { what: "name" });
    const w: WS = { id: `ws_${(++t.seq).toString(16)}${Math.random().toString(16).slice(2, 6)}`, name, plan: "free", status: "active", created_at: now(), owner_user_id: ME };
    t.workspaces.push(w);
    t.members.push({ ws: w.id, user: ME, role: "owner", at: now() });
    return c.ok({ ...wsView(t, w), role: "owner", quota: PLANS.free, permissions: ALL_PERMS, member_count: 1 }, 201);
  }
  if (p === "workspaces" && M === "GET") return c.list(mockMe().workspaces);
  if (r0 === "workspaces" && r1 === "current") {
    if (!r2) {
      if (M === "GET") return c.ok(curView());
      if (M === "PATCH") {
        if (typeof body.name === "string" && body.name.trim()) cur().name = body.name.trim().slice(0, 60);
        return c.ok(curView());
      }
    }
    if (r2 === "usage" && M === "GET") {
      const w = cur();
      const quota = PLANS[w.plan] ?? PLANS.free;
      const active = s.boxes.filter((b) => !["terminated", "failed"].includes(b.status));
      const today = s.boxes.reduce((a, b) => a + spentToday(b.id), 0);
      return c.ok({
        workspace_id: w.id, plan: w.plan, quota,
        used: {
          boxes: active.filter((b) => b.status !== "sleeping").length, boxes_total: active.length, agents: s.agents.length,
          keys: s.keys?.length ?? 0, secrets: s.secrets?.length ?? 0, members: t.members.filter((m) => m.ws === w.id).length,
          spent_today_usd: Math.round(today * 1e4) / 1e4, spent_month_usd: Math.round(s.ledger.reduce((a, l) => a + l.amount_usd, 0) * 1e4) / 1e4,
        },
      });
    }
    if (r2 === "members") {
      if (!r3 && M === "GET") {
        return c.list<Member>(t.members.filter((m) => m.ws === t.current).map((m) => ({ user_id: m.user, email: t.users[m.user]?.email ?? m.user, name: t.users[m.user]?.name, role: m.role, email_verified: t.users[m.user]?.email_verified ?? false, joined_at: m.at })));
      }
      const mb = t.members.find((m) => m.ws === t.current && m.user === r3);
      if (!mb) return c.err(404, "NOT_FOUND", "not_found", { what: `member ${r3}` });
      if (M === "PATCH") {
        const role = String(body.role ?? "") as WorkspaceRole;
        if (!ROLE_PERMS[role]) return c.err(400, "INVALID", "v20_invalid", { what: "role" });
        if (mb.role === "owner" && role !== "owner" && owners() <= 1) return c.err(400, "INVALID", "v20_last_owner");
        mb.role = role;
        return c.ok({ user_id: mb.user, email: t.users[mb.user]?.email, name: t.users[mb.user]?.name, role, email_verified: true, joined_at: mb.at });
      }
      if (M === "DELETE") {
        if (mb.role === "owner" && owners() <= 1) return c.err(400, "INVALID", "v20_last_owner");
        t.members = t.members.filter((m) => m !== mb);
        return c.ok({ ok: true });
      }
    }
    if (r2 === "invites") {
      if (!r3 && M === "GET") return c.list(t.invites.filter((i) => i.workspace_id === t.current).map(pub));
      if (!r3 && M === "POST") {
        const email = String(body.email ?? "").trim().toLowerCase();
        if (!/^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(email)) return c.err(400, "INVALID", "v20_invalid", { what: "email" });
        const role = (String(body.role ?? "") || "developer") as WorkspaceRole;
        const w = cur();
        const quota = PLANS[w.plan] ?? PLANS.free;
        const count = t.members.filter((m) => m.ws === w.id).length;
        if (quota.max_members > 0 && count >= quota.max_members) {
          return { status: 403, body: { error: { code: "QUOTA_EXCEEDED", message: `workspace quota max_members exceeded (limit ${quota.max_members}, used ${count})`, hint: "Free capacity (sleep or delete boxes, remove unused items) or upgrade the workspace plan.", details: { quota: "max_members", limit: quota.max_members, used: count, plan: w.plan, upgrade_url: "/settings/billing" }, next: ["GET /v1/workspaces/current/usage", "GET /v1/plans"] } } };
        }
        t.invites = t.invites.filter((i) => !(i.workspace_id === w.id && i.email === email && !i.accepted_at));
        const token = `bxi_${Math.random().toString(16).slice(2)}${Math.random().toString(16).slice(2)}`;
        const inv: Inv = { id: `wsi_${(++t.seq).toString(16)}`, workspace_id: w.id, email, role, invited_by: `user:${ME}`, expires_at: new Date(Date.now() + 7 * DAY).toISOString(), accepted_at: null, created_at: now(), token };
        t.invites.push(inv);
        return c.ok({ ...pub(inv), invite_url: `/invite/${token}`, mailed: false }, 201);
      }
      if (r3 && M === "DELETE") {
        const n = t.invites.length;
        t.invites = t.invites.filter((i) => !(i.id === r3 && i.workspace_id === t.current));
        return n === t.invites.length ? c.err(404, "NOT_FOUND", "not_found", { what: `invite ${r3}` }) : c.ok({ ok: true });
      }
    }
  }
  if (r0 === "workspace-invites" && r1) {
    const inv = t.invites.find((i) => i.token === r1);
    if (!inv) return c.err(404, "NOT_FOUND", "not_found", { what: "workspace invite" });
    const w = t.workspaces.find((x) => x.id === inv.workspace_id)!;
    if (!r2 && M === "GET") {
      const by = t.users[inv.invited_by.replace(/^user:/, "")];
      return c.ok({ workspace: { id: w.id, name: w.name }, email: inv.email, role: inv.role, invited_by: by ? `${by.name} <${by.email}>` : inv.invited_by, expires_at: inv.expires_at, status: inviteStatus(inv), account_exists: Object.values(t.users).some((u) => u.email === inv.email) });
    }
    if (r2 === "accept" && M === "POST") {
      if (inviteStatus(inv) !== "pending") return c.err(400, "INVALID", "v20_invalid", { what: `invite ${inviteStatus(inv)}` });
      inv.accepted_at = now();
      inv.accepted_by = ME;
      if (!t.members.some((m) => m.ws === w.id && m.user === ME)) t.members.push({ ws: w.id, user: ME, role: inv.role, at: now() });
      t.current = w.id;
      return c.ok(mockMe());
    }
  }
  if (r0 === "boxes" && r1 === "locations" && M === "GET") return c.ok(locations(s, spentToday));
  void q;
  return null;
}
