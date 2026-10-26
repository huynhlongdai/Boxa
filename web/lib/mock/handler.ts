// Bộ xử lý MOCK MODE: mô phỏng Boxa Core theo contract v1, trạng thái giữ trong bộ nhớ.
import { parseDocument } from "yaml";
import type { RecommendOption, Agent, AgentKind, ApiKey, Approval, Box, BoxaEvent, Delivery, ErrorCode, Host, LedgerEntry, NotifySettings, NotifyView, Overview, ProviderRegion, Quote, QuoteOption, Settings, Snapshot, StorageView, Template } from "../types";
import { NOTIFY_EVENTS } from "../types";
import type { Locale } from "@/i18n/config";
import { FALLBACK_KINDS } from "../agent-kinds";
import { mt, seedText } from "./i18n";
import { discovery } from "./discovery";
import { DAY, HOUR, MIN, iso, makeSeed, r4, type MockState } from "./seed";
import { makeSnippet } from "../snippets";
import { estimateFor, handleV13, recommend, resolveNeeds } from "./v13";

type G = typeof globalThis & { __boxaMock?: MockState };

export function store(): MockState {
  const g = globalThis as G;
  if (!g.__boxaMock) g.__boxaMock = makeSeed();
  return g.__boxaMock;
}

export interface MockResponse {
  status: number;
  body: unknown;
  contentType?: string;
  headers?: Record<string, string>;
}

const ok = (body: unknown, status = 200): MockResponse => ({ status, body });
// Ngôn ngữ của request đang xử lý (handleMock chạy đồng bộ nên an toàn).
let L: Locale = "vi";
const DOCS = "https://boxa.dev/docs/errors";
/** Lỗi theo contract v1.1: message + hint dịch theo Accept-Language, code không đổi, kèm docs_url (+ retry_after_s nếu có). */
/** v1.3: error.next — lệnh gợi ý thường gỡ được lỗi (giống nextForError ở core/internal/api/v13.go). */
const NEXT: Partial<Record<ErrorCode, string[]>> = {
  NO_CAPACITY: ["box_recommend(<same needs>)  # see ranked options and why others were rejected", "box_up(<smaller cpu/ram_mb>)"],
  BUDGET_EXCEEDED: ["box_cost()  # see spend vs budget", "box_list(status=\"running\")  # then box_sleep / box_down unused boxes"],
  BOX_SLEEPING: ["box_sleep(box_id=<id>, wake=true)", "box_ensure(name=<name>)  # wakes or recreates"],
  NOT_FOUND: ["box_list()  # find the right id or slug", "box_ensure(name=<name>)  # get-or-create by name"],
  PROVIDER_FAILED: ["box_recommend(<same needs>)  # Boxa already fails over; check other options", "retry after retry_after_s"],
  POLICY_DENIED: ["box_quote(<smaller spec>)", "ask the operator to change the agent policy or quotas"],
  TIMEOUT: ["box_exec(..., async=true) then box_job(job_id, wait_s=30)"],
  INVALID: ["GET /v1/openapi.json or box_templates() / GET /v1/profiles for valid values"],
  RATE_LIMITED: ["wait retry_after_s, then retry", "box_exec_batch(...)  # fewer round-trips"],
};
const err = (status: number, code: ErrorCode, key: string, params: Record<string, string | number> = {}, extra: { retry_after_s?: number } = {}): MockResponse => ({
  status,
  body: { error: { code, message: mt(L, key, params), hint: mt(L, `${key}_hint`, params), docs_url: `${DOCS}#${code.toLowerCase()}`, ...extra, ...(NEXT[code] ? { next: NEXT[code] } : {}) } },
  headers: extra.retry_after_s ? { "Retry-After": String(extra.retry_after_s) } : undefined,
});
const list = <T>(items: T[]) => ok({ items, next_cursor: null });

const reg = (id: string, name: string): ProviderRegion => ({ id, name, available: true });
const PROVIDER_REGIONS: Record<string, ProviderRegion[]> = {
  hetzner: [reg("fsn1", "Falkenstein DC Park 1"), reg("nbg1", "Nuremberg DC Park 1"), reg("hel1", "Helsinki DC Park 1"), reg("ash", "Ashburn, VA"), reg("hil", "Hillsboro, OR"), reg("sin", "Singapore")],
  vultr: [reg("sgp", "Singapore (SG)"), reg("nrt", "Tokyo (JP)"), reg("ewr", "New Jersey (US)"), reg("fra", "Frankfurt (DE)")],
  digitalocean: [reg("sgp1", "Singapore 1"), reg("fra1", "Frankfurt 1"), reg("nyc3", "New York 3"), reg("blr1", "Bangalore 1")],
  contabo: [reg("EU", "European Union (Germany)"), reg("UK", "United Kingdom"), reg("US-central", "US Central (St. Louis)"), reg("US-east", "US East (New York)"), reg("US-west", "US West (Seattle)"), reg("SIN", "Singapore"), reg("JPN", "Japan (Tokyo)"), reg("AUS", "Australia (Sydney)"), reg("IND", "India (Mumbai)")],
  netcup: [reg("nue", "Nuremberg (DE)"), reg("vie", "Vienna (AT)"), reg("ams", "Amsterdam (NL)"), reg("man", "Manassas (US)")],
};
const notFound = (what: string) => err(404, "NOT_FOUND", "not_found", { what });

function nextId(s: MockState, prefix: string) {
  s.seq += 1;
  return `${prefix}_${s.seq.toString(16)}${Math.random().toString(16).slice(2, 6)}`;
}

function pushEvent(s: MockState, e: Omit<BoxaEvent, "id" | "ts"> & { ts?: string }) {
  const evt: BoxaEvent = { id: nextId(s, "evt"), ts: e.ts ?? iso(Date.now()), ...e } as BoxaEvent;
  s.events.push(evt);
  return evt;
}

function startOfTodayUTC() {
  const d = new Date();
  d.setUTCHours(0, 0, 0, 0);
  return d.getTime();
}
function startOfMonthUTC() {
  const d = new Date();
  return Date.UTC(d.getUTCFullYear(), d.getUTCMonth(), 1);
}

/** Tiến trình theo thời gian: box/host đang dựng → sẵn sàng; agent onboarding nhận lệnh đầu tiên. */
function advance(s: MockState) {
  const now = Date.now();
  for (const b of s.boxes) {
    if (["requested", "placing", "provisioning"].includes(b.status) && b._ready_at && b._ready_at <= now) {
      b.status = "running";
      b.last_active_at = iso(now);
      const host = s.hosts.find((h) => h.id === b.host_id);
      b.url = `https://${b.slug}-${(host?.ip ?? "10.0.0.1").replace(/\./g, "-")}.${s.settings.base_domain}`;
      delete b._ready_at;
      pushEvent(s, { actor: "system", box_id: b.id, agent_id: b.agent_id, type: "box.running", message: `Box ${b.slug} sẵn sàng`, cost_usd: null });
    } else if (b.status === "placing" && !b._ready_at) {
      b.status = "provisioning";
    }
  }
  for (const h of s.hosts) {
    if (h.status === "provisioning" && h._ready_at && h._ready_at <= now) {
      h.status = "ready";
      delete h._ready_at;
      pushEvent(s, { actor: "system", box_id: null, agent_id: null, type: "host.ready", message: `Host ${h.id} (${h.provider}/${h.region}) sẵn sàng`, cost_usd: null });
    }
  }
  for (const [agentId, t] of Object.entries(s.first_call_at)) {
    const a = s.agents.find((x) => x.id === agentId);
    if (a && !a.last_call_at && t <= now) {
      a.last_call_at = iso(t);
      pushEvent(s, { actor: `agent:${a.name}`, box_id: null, agent_id: a.id, type: "agent.first_call", message: `${a.name} gọi lệnh đầu tiên: box_list`, cost_usd: null });
    }
  }
}

/** Cộng dồn chi phí cho box đang chạy (gọi từ luồng SSE). */
export function tickSpend(s: MockState, seconds: number) {
  const running = s.boxes.filter((b) => b.status === "running");
  for (const b of running) {
    const amt = r4((b.cost_per_hour * seconds) / 3600);
    if (amt <= 0) continue;
    const last = [...s.ledger].reverse().find((l) => l.box_id === b.id && l.kind === "compute" && Date.now() - Date.parse(l.ts) < HOUR);
    if (last) last.amount_usd = r4(last.amount_usd + amt);
    else
      s.ledger.push({ id: nextId(s, "led"), ts: iso(Date.now()), agent_id: b.agent_id, box_id: b.id, provider: b.provider, kind: "compute", amount_usd: amt, note: `${b.slug} · ${b.provider === "vultr" ? "burst theo giờ" : "phần pool phân bổ"}` });
  }
  return running;
}

function sum(entries: LedgerEntry[]) {
  return r4(entries.reduce((a, l) => a + l.amount_usd, 0));
}

function hydrateAgent(s: MockState, a: Agent): Agent {
  const today = startOfTodayUTC();
  const month = startOfMonthUTC();
  const mine = s.ledger.filter((l) => l.agent_id === a.id);
  return {
    ...a,
    spent_today_usd: sum(mine.filter((l) => Date.parse(l.ts) >= today)),
    spent_month_usd: sum(mine.filter((l) => Date.parse(l.ts) >= month)),
    box_count: s.boxes.filter((b) => b.agent_id === a.id && b.status !== "terminated").length,
  };
}

/** v1.2: egress hiệu lực — box tự khai báo, hoặc allowlist của agent, hoặc settings chặn mặc định. */
function egressFor(s: MockState, b: MockState["boxes"][number]): Box["egress"] {
  if (b.egress !== undefined) return b.egress;
  const agent = s.agents.find((a) => a.id === b.agent_id);
  const host = s.hosts.find((h) => h.id === b.host_id);
  const enforced = host?.provider !== "local";
  const base = { enforced, mode: enforced ? "incus-acl" : "none", warning: enforced ? undefined : "local driver has no network isolation: the egress allowlist is NOT enforced", applied_at: b.created_at };
  if (agent?.policy.egress_allowlist?.length) return { allow: [...agent.policy.egress_allowlist].sort(), deny_by_default: true, source: "agent", rules: agent.policy.egress_allowlist.length * 2, ...base };
  if (s.settings.egress_deny_by_default) return { allow: [], deny_by_default: true, source: "settings", rules: 0, ...base };
  return null;
}

const EGRESS_RE = /^(\*\.)?([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}(:\d{1,5})?$|^\d{1,3}(\.\d{1,3}){3}(\/\d{1,2})?(:\d{1,5})?$|^\[?[0-9a-f:]+\]?(\/\d{1,3})?(:\d{1,5})?$/i;

function hydrateBox(s: MockState, b: MockState["boxes"][number]): Box {
  // eslint-disable-next-line @typescript-eslint/no-unused-vars
  const { _ready_at, _taken_over, ...rest } = b;
  return { ...rest, egress: egressFor(s, b), spent_usd: sum(s.ledger.filter((l) => l.box_id === b.id)) };
}

function hydrateHost(s: MockState, h: MockState["hosts"][number]): Host {
  // eslint-disable-next-line @typescript-eslint/no-unused-vars
  const { _ready_at, ...rest } = h;
  return { ...rest, box_count: s.boxes.filter((b) => b.host_id === h.id && !["terminated", "failed"].includes(b.status)).length };
}

export function overview(s: MockState): Overview {
  const today = startOfTodayUTC();
  const month = startOfMonthUTC();
  const cost_7d: Overview["cost_7d"] = [];
  for (let d = 6; d >= 0; d--) {
    const from = today - d * DAY;
    const to = from + DAY;
    const by_agent: Record<string, number> = {};
    for (const a of s.agents) {
      const v = sum(s.ledger.filter((l) => l.agent_id === a.id && Date.parse(l.ts) >= from && Date.parse(l.ts) < to));
      if (v > 0) by_agent[a.name] = v;
    }
    cost_7d.push({ date: new Date(from).toISOString().slice(0, 10), by_agent });
  }
  const todayEntries = s.ledger.filter((l) => Date.parse(l.ts) >= today);
  const perBox = new Map<string, number>();
  for (const l of todayEntries) perBox.set(l.box_id, (perBox.get(l.box_id) ?? 0) + l.amount_usd);
  const top_tasks = [...perBox.entries()]
    .sort((a, b) => b[1] - a[1])
    .slice(0, 5)
    .map(([box_id, cost]) => {
      const b = s.boxes.find((x) => x.id === box_id);
      return { box_id, slug: b?.slug ?? box_id, agent_name: b?.agent_name ?? "?", cost_usd: r4(cost) };
    });
  const spent_month = sum(s.ledger.filter((l) => Date.parse(l.ts) >= month));
  // Ước tính chi nếu mọi box đã tạo trong tháng chạy 24/7 ở giá burst
  const hoursInMonthSoFar = (Date.now() - month) / HOUR;
  const full247 = s.boxes.filter((b) => b.status !== "terminated").reduce((a, b) => a + b.cost_per_hour * 2.4 * hoursInMonthSoFar, 0);
  return {
    spent_today: sum(todayEntries),
    budget_day: r4(s.settings.budget_month_usd / 30),
    spent_month,
    budget_month: s.settings.budget_month_usd,
    boxes_running: s.boxes.filter((b) => b.status === "running").length,
    boxes_sleeping: s.boxes.filter((b) => b.status === "sleeping").length,
    saved_vs_247_usd: r4(Math.max(0, full247 - spent_month)),
    cost_7d,
    top_tasks,
    approvals_pending: s.approvals.filter((a) => a.status === "pending").length,
  };
}

function toYaml(b: Box): string {
  return `# box.yaml (chỉ đọc) — ${b.id}
slug: ${b.slug}
agent: ${b.agent_name}
template: ${b.template}
lifecycle: ${b.lifecycle}
spec:
  cpu: ${b.spec.cpu}
  ram_mb: ${b.spec.ram_mb}
  disk_gb: ${b.spec.disk_gb}
${b.ttl_seconds ? `ttl_seconds: ${b.ttl_seconds}\n` : ""}${b.idle_minutes ? `idle_minutes: ${b.idle_minutes}\n` : ""}${b.budget_usd ? `budget_usd: ${b.budget_usd}\n` : ""}ports: [${b.ports.join(", ")}]
secrets: [${b.secrets.join(", ")}]
`;
}
export { toYaml as mockBoxYaml };

function validateYaml(src: string) {
  const errors: { line: number; message: string }[] = [];
  const doc = parseDocument(src, { prettyErrors: true });
  for (const e of doc.errors) errors.push({ line: (e.linePos?.[0]?.line ?? 1), message: e.message.split("\n")[0] });
  if (!errors.length) {
    const v = doc.toJS() as Record<string, unknown> | null;
    const lineOf = (key: string) => {
      const idx = src.split("\n").findIndex((l) => l.trimStart().startsWith(`${key}:`));
      return idx >= 0 ? idx + 1 : 1;
    };
    if (!v || typeof v !== "object") errors.push({ line: 1, message: mt(L, "v_object") });
    else {
      if (!v.name && !v.slug && !v.template) errors.push({ line: 1, message: mt(L, "v_name") });
      const spec = v.spec as Record<string, unknown> | undefined;
      if (!spec) errors.push({ line: 1, message: mt(L, "v_spec") });
      else {
        if (typeof spec.cpu !== "number" || spec.cpu < 1 || spec.cpu > 16) errors.push({ line: lineOf("cpu"), message: mt(L, "v_cpu") });
        if (typeof spec.ram_mb !== "number" || spec.ram_mb < 256) errors.push({ line: lineOf("ram_mb"), message: mt(L, "v_ram") });
        if (typeof spec.disk_gb !== "number" || spec.disk_gb < 5) errors.push({ line: lineOf("disk_gb"), message: mt(L, "v_disk") });
      }
      if (v.lifecycle && !["task", "session", "service"].includes(String(v.lifecycle)))
        errors.push({ line: lineOf("lifecycle"), message: mt(L, "v_lifecycle") });
      if (v.gpu) errors.push({ line: lineOf("gpu"), message: mt(L, "v_gpu") });
    }
  }
  return { ok: errors.length === 0, errors };
}

function createBox(s: MockState, body: Record<string, unknown>): MockResponse {
  if (s.settings.maintenance.enabled) return err(503, "INVALID", "maintenance", {}, { retry_after_s: 300 });
  let spec = body.spec as Box["spec"] | undefined;
  let template = (body.template as string) ?? "base";
  let lifecycle = (body.lifecycle as Box["lifecycle"]) ?? "task";
  let slug = body.slug as string | undefined;
  if (typeof body.yaml === "string") {
    const v = validateYaml(body.yaml);
    if (!v.ok) return err(400, "INVALID", "bad_yaml", { detail: v.errors[0].message });
    const js = parseDocument(body.yaml).toJS() as Record<string, unknown>;
    spec = js.spec as Box["spec"];
    template = (js.template as string) ?? (js.name as string) ?? template;
    lifecycle = (js.lifecycle as Box["lifecycle"]) ?? lifecycle;
    slug = (js.slug as string) ?? `${template}-thu-${Math.random().toString(36).slice(2, 6)}`;
  }
  const agent = s.agents.find((a) => a.id === body.agent_id) ?? s.agents.find((a) => a.id === "agt_hark")!;
  const hAgent = hydrateAgent(s, agent);
  if (hAgent.spent_today_usd >= agent.budget_day_usd * 1.2)
    return err(402, "BUDGET_EXCEEDED", "over_budget", { agent: agent.name });
  // v1.3: profile/requirements → tự chọn spec + chỗ đặt bằng cùng engine với POST /recommend
  let pick: RecommendOption | null = null;
  let profileId: string | undefined;
  let requirements: Box["requirements"];
  if (body.profile || body.requirements) {
    const rq = (body.requirements ?? {}) as NonNullable<Box["requirements"]>;
    const { req, profile } = resolveNeeds(rq, body.profile as string | undefined, agent.kind);
    profileId = profile.id;
    requirements = req;
    if (!body.spec && typeof body.yaml !== "string") spec = { cpu: req.cpu ?? 1, ram_mb: req.ram_mb ?? 1024, disk_gb: req.disk_gb ?? 10 };
    if (!body.template) template = profile.template;
    if (!body.lifecycle) lifecycle = profile.lifecycle;
    const rec = recommend(s, L, { requirements: rq, profile: profile.id, template, lifecycle, agent_id: agent.id });
    pick = rec.pick;
    if (!pick) return err(503, "NO_CAPACITY", "no_capacity", { cpu: spec?.cpu ?? 1, agent: agent.name, s: 60 }, { retry_after_s: 60 });
  }
  spec = spec ?? { cpu: 2, ram_mb: 2048, disk_gb: 20 };
  // v1.4: phương án sandbox → host một-box trên Sandbox API (T2 dial-in / T3 shim), xoá cùng box
  if (pick?.kind === "sandbox") {
    s.hosts.push({ id: nextId(s, "hst"), provider: pick.provider, region: pick.region, sku: pick.sku ?? "default", ip: "", status: "ready", cpu: pick.cpu, ram_mb: pick.ram_mb, disk_gb: pick.disk_gb,
      cpu_used_pct: 0, ram_used_pct: 0, disk_used_pct: 0, box_count: 0, price_hour: pick.cost_per_hour, price_month: r4(pick.cost_per_hour * 730), billing: "sandbox",
      driver: pick.tier === "T3" ? "shim" : "sandbox", provider_ref: Math.random().toString(36).slice(2, 12), created_at: iso(Date.now()) });
    pick = { ...pick, host_id: s.hosts[s.hosts.length - 1].id };
  }
  const host = (pick?.host_id && s.hosts.find((h) => h.id === pick!.host_id)) || (s.hosts.find((h) => h.status === "ready" && h.billing === "pool" && agent.policy.providers.includes(h.provider)) ?? s.hosts.find((h) => h.status === "ready")!);
  const id = nextId(s, "box");
  const b: MockState["boxes"][number] = {
    id,
    slug: slug ?? `${agent.name.split("-")[0]}-${Math.random().toString(36).slice(2, 6)}`,
    agent_id: agent.id,
    agent_name: agent.name,
    status: "provisioning",
    lifecycle,
    template,
    spec,
    provider: host.provider,
    region: host.region,
    host_id: host.id,
    url: null,
    ttl_seconds: (body.ttl_seconds as number) ?? (lifecycle === "task" ? s.settings.reaper.default_ttl_seconds : null),
    idle_minutes: (body.idle_minutes as number) ?? (lifecycle === "service" ? null : s.settings.reaper.idle_minutes),
    budget_usd: (body.budget_usd as number) ?? 1,
    spent_usd: 0,
    cost_per_hour: r4((host.price_hour / host.cpu) * spec.cpu),
    placement_reason: mt(L, host.billing === "pool" ? "q_pool" : "q_burst", { provider: host.provider, region: host.region, cpu: spec.cpu, ram: spec.ram_mb / 1024, agent: agent.name }),
    ports: (body.ports as number[]) ?? [],
    secrets: (body.secrets as string[]) ?? [],
    created_at: iso(Date.now()),
    last_active_at: null,
    _ready_at: Date.now() + (pick?.kind === "warm" ? 1400 : 8000),
  };
  b.estimate = { ...estimateFor(pick && pick.kind !== "burst" ? pick.kind : host.billing === "pool" ? "pool" : "burst", b.cost_per_hour, lifecycle), ...(pick ? { score: pick.score } : {}) };
  if (pick) {
    b.profile = profileId;
    b.requirements = requirements;
    b.placement_reason = pick.reasons.join("; ");
  }
  const eg = body.egress as { allow?: unknown[]; deny_by_default?: boolean } | undefined;
  if (eg && (eg.deny_by_default || (eg.allow?.length ?? 0) > 0)) {
    const allow = (eg.allow ?? []).map((x) => String(x).trim().toLowerCase());
    const bad = allow.find((x) => !EGRESS_RE.test(x));
    if (bad) return err(400, "INVALID", "bad_egress", { entry: bad });
    const enforced = host.provider !== "local";
    b.egress = { allow: [...new Set(allow)].sort(), deny_by_default: true, source: "box", enforced, mode: enforced ? "incus-acl" : "none", rules: allow.length * 2, applied_at: iso(Date.now()) };
  }
  s.boxes.push(b);
  s.files[id] = { "/work/README.md": `# ${b.slug}\n`, "/memory/notes.md": `# Ghi nhớ của ${agent.name}\n` };
  pushEvent(s, { actor: "user:anh", box_id: id, agent_id: agent.id, type: "box.created", message: `Tạo box ${b.slug} (${template}) tại ${host.provider}/${host.region}`, cost_usd: null });
  return ok({ ...hydrateBox(s, b), next: [mt(L, "n_exec", { id }), mt(L, "n_down", { id })] }, 201);
}

/** POST /quote: dry-run đặt chỗ + giá (không tạo box). */
function quote(s: MockState, body: Record<string, unknown>): MockResponse {
  const spec = body.spec as Box["spec"] | undefined;
  if (!spec?.cpu || !spec.ram_mb) return err(400, "INVALID", "bad_spec");
  const agent = s.agents.find((a) => a.id === body.agent_id) ?? s.agents.find((a) => a.id === "agt_hark")!;
  const lifecycle = (body.lifecycle as Box["lifecycle"]) ?? "task";
  const hours = typeof body.ttl_seconds === "number" ? body.ttl_seconds / 3600 : lifecycle === "service" ? 720 : lifecycle === "session" ? 8 : 2;
  const allowed = agent.policy.providers;
  const opts: (QuoteOption & { _pool: boolean })[] = [];
  for (const h of s.hosts) {
    if (h.status !== "ready" || h.billing !== "pool" || !allowed.includes(h.provider)) continue;
    const freeCpu = h.cpu * (1 - h.cpu_used_pct / 100);
    if (freeCpu < spec.cpu) continue;
    const cph = r4((h.price_hour / h.cpu) * spec.cpu);
    opts.push({ _pool: true, provider: h.provider, region: h.region, sku: h.sku, billing: "pool", cost_per_hour: cph, expected_total_usd: r4(cph * hours), reason: mt(L, "q_alt_pool", { provider: h.provider, region: h.region }) });
  }
  for (const o of s.offers) {
    if (!allowed.includes(o.provider) || o.cpu < spec.cpu || o.ram_mb < spec.ram_mb || o.min_billing_minutes >= 43200) continue;
    opts.push({ _pool: false, provider: o.provider, region: o.region, sku: o.sku, billing: "burst", cost_per_hour: o.price_hour, expected_total_usd: r4(o.price_hour * Math.max(hours, o.min_billing_minutes / 60)), reason: mt(L, "q_alt_burst", { provider: o.provider, region: o.region, min: o.min_billing_minutes }) });
  }
  if (!opts.length) return err(503, "NO_CAPACITY", "no_capacity", { cpu: spec.cpu, agent: agent.name, s: 60 }, { retry_after_s: 60 });
  // Scheduler ưu tiên pool tháng còn chỗ (chi phí biên ≈ $0), sau đó mới tới burst theo giờ rẻ nhất
  opts.sort((a, b) => Number(b._pool) - Number(a._pool) || a.cost_per_hour - b.cost_per_hour);
  const [best, ...rest] = opts;
  const q: Quote = {
    provider: best.provider,
    region: best.region,
    sku: best.sku,
    billing: best.billing,
    cost_per_hour: best.cost_per_hour,
    expected_total_usd: best.expected_total_usd ?? 0,
    expected_hours: Math.round(hours * 10) / 10,
    placement_reason: mt(L, best._pool ? "q_pool" : "q_burst", { provider: best.provider, region: best.region, cpu: spec.cpu, ram: spec.ram_mb / 1024, agent: agent.name }),
    // eslint-disable-next-line @typescript-eslint/no-unused-vars
    alternatives: rest.slice(0, 4).map(({ _pool, ...o }) => o),
  };
  return ok(q);
}

let tokenSeed = 0;
/** Chạy một lệnh mock, ghi job + log + event box.exec (kèm data có cấu trúc như Core v1.1). */
function runExec(s: MockState, b: MockState["boxes"][number], cmd: string) {
  const id = nextId(s, "job");
  const log_id = `log_${id.slice(4)}`;
  const fail = /\b(false|exit 1)\b/.test(cmd);
  const stdout = cmd.startsWith("echo ") ? cmd.slice(5) : `(mock) ok: ${cmd}`;
  tokenSeed = (tokenSeed + 1) % 7;
  const tokens_saved = cmd.length > 12 ? 180 + tokenSeed * 95 : 0;
  const duration_ms = 90 + tokenSeed * 40;
  const exit_code = fail ? 1 : 0;
  const result = { exit_code, duration_ms, stdout_tail: stdout, stderr_tail: fail ? "exit status 1" : "", truncated: tokens_saved > 0, log_id, files_changed: [] as string[], tokens_saved };
  s.jobs.push({ id, box_id: b.id, command: cmd, status: fail ? "failed" : "done", result, created_at: iso(Date.now()), finished_at: iso(Date.now() + duration_ms) });
  s.logs[log_id] = `$ ${cmd}\n${stdout}\n[exit ${exit_code}] ${(duration_ms / 1000).toFixed(2)}s\n`;
  const evt = pushEvent(s, { actor: "user:anh", box_id: b.id, agent_id: b.agent_id, type: "box.exec", message: `exit=${exit_code} ${duration_ms}ms: ${cmd} tokens_saved=${tokens_saved} log=${log_id}`, cost_usd: null });
  evt.tokens_saved = tokens_saved;
  evt.log_id = log_id;
  evt.data = { exit_code, duration_ms, command: cmd, tokens_saved, log_id };
  b.last_active_at = iso(Date.now());
  return { id, result };
}

function csvEscape(v: string | number) {
  const str = String(v);
  return /[",\n]/.test(str) ? `"${str.replace(/"/g, '""')}"` : str;
}

function filterLedger(s: MockState, q: URLSearchParams) {
  const agent = q.get("agent");
  const provider = q.get("provider");
  const from = q.get("from");
  const to = q.get("to");
  const task = q.get("task");
  return s.ledger
    .filter((l) => {
      if (agent && l.agent_id !== agent && s.agents.find((a) => a.id === l.agent_id)?.name !== agent) return false;
      if (provider && l.provider !== provider) return false;
      if (task && l.box_id !== task && s.boxes.find((b) => b.id === l.box_id)?.slug !== task) return false;
      if (from && Date.parse(l.ts) < Date.parse(from)) return false;
      if (to && Date.parse(l.ts) > Date.parse(to.length === 10 ? `${to}T23:59:59Z` : to)) return false;
      return true;
    })
    .sort((a, b) => Date.parse(b.ts) - Date.parse(a.ts));
}

export function handleMock(method: string, segs: string[], q: URLSearchParams, body: Record<string, unknown>, apiBase: string, locale: Locale = "vi"): MockResponse {
  L = locale;
  const s = store();
  advance(s);
  const [r0, r1, r2] = segs;
  const M = method.toUpperCase();

  // ---- health / overview ----
  if (r0 === "health") return ok({ ok: true, version: "0.1.0-mock", components: { db: "ok", scheduler: "ok", reaper: "ok", price_feed: "ok", gateway: "ok" } });
  if (r0 === "overview") return ok(overview(s));

  // ---- v1.1: discovery, agent kinds, quote ----
  const disc = discovery(segs, q, apiBase);
  if (disc) return disc;
  if (r0 === "agent-kinds") return list(FALLBACK_KINDS);
  if (r0 === "quote" && M === "POST") return quote(s, body);

  // ---- v1.3: provider manager, recommend, speed, mesh, clone/ensure/resume ----
  const v13 = handleV13(s, {
    L, ok, list, err: (st, code, key, params) => err(st, code, key, params),
    hydrateBox: (b) => hydrateBox(s, b), createBox: (b) => createBox(s, b), nextId: (p) => nextId(s, p),
    pushEvent: (e) => pushEvent(s, e as Omit<BoxaEvent, "id" | "ts">), regions: PROVIDER_REGIONS,
  }, M, segs, q, body);
  if (v13) return v13;

  // ---- boxes ----
  if (r0 === "boxes") {
    if (!r1) {
      if (M === "GET") {
        let items = s.boxes.filter((b) => b.status !== "terminated");
        const st = q.get("status"), ag = q.get("agent"), pv = q.get("provider"), lc = q.get("lifecycle");
        if (st) items = items.filter((b) => b.status === st);
        if (ag) items = items.filter((b) => b.agent_id === ag || b.agent_name === ag);
        if (pv) items = items.filter((b) => b.provider === pv);
        if (lc) items = items.filter((b) => b.lifecycle === lc);
        return list(items.map((b) => hydrateBox(s, b)));
      }
      if (M === "POST") return createBox(s, body);
    }
    const b = s.boxes.find((x) => x.id === r1 || x.slug === r1);
    if (!b) return notFound(`box ${r1}`);
    if (!r2) {
      if (M === "GET") return ok(hydrateBox(s, b));
      if (M === "DELETE") {
        b.status = "terminated";
        pushEvent(s, { actor: "user:anh", box_id: b.id, agent_id: b.agent_id, type: "box.terminated", message: `Xoá box ${b.slug}`, cost_usd: null });
        return ok(hydrateBox(s, b));
      }
    }
    switch (r2) {
      case "sleep":
        if (b.status !== "running") return err(409, "INVALID", "bad_state_sleep", { status: b.status });
        b.status = "sleeping";
        b.url = null;
        pushEvent(s, { actor: "user:anh", box_id: b.id, agent_id: b.agent_id, type: "box.sleeping", message: `Cho ${b.slug} ngủ`, cost_usd: null });
        return ok(hydrateBox(s, b));
      case "wake":
        if (!["sleeping", "failed"].includes(b.status)) return err(409, "INVALID", "bad_state_wake", { status: b.status });
        b.status = "provisioning";
        b._ready_at = Date.now() + 5000;
        pushEvent(s, { actor: "user:anh", box_id: b.id, agent_id: b.agent_id, type: "box.waking", message: `Đánh thức ${b.slug}`, cost_usd: null });
        return ok(hydrateBox(s, b));
      case "snapshot": {
        const sid = nextId(s, "snp");
        const remote = s.storage.configured;
        const snap: Snapshot = {
          id: sid, box_id: b.id, size_mb: Math.round(400 + Math.random() * 1500), created_at: iso(Date.now()), location: `local:///var/lib/boxd/snapshots/${sid}.tar.gz`,
          storage: remote ? "s3" : "local", ...(remote ? { remote_url: `s3://${s.storage.bucket}/${s.storage.prefix ? s.storage.prefix + "/" : ""}snapshots/${b.id}/${sid}.tar.gz` } : {}),
        };
        s.snapshots.push(snap);
        pushEvent(s, { actor: "user:anh", box_id: b.id, agent_id: b.agent_id, type: "box.snapshot", message: `Snapshot ${snap.id} (${snap.size_mb} MB)`, cost_usd: 0.001 });
        return ok(snap, 201);
      }
      case "snapshots":
        return list(s.snapshots.filter((x) => x.box_id === b.id).sort((a, c) => Date.parse(c.created_at) - Date.parse(a.created_at)));
      case "restore":
      case "fork": {
        const snap = s.snapshots.find((x) => x.id === body.snapshot_id && x.box_id === b.id);
        if (!snap) return notFound(`snapshot ${String(body.snapshot_id)}`);
        if (r2 === "restore") {
          b.status = "provisioning";
          b._ready_at = Date.now() + 6000;
          pushEvent(s, { actor: "user:anh", box_id: b.id, agent_id: b.agent_id, type: "box.restore", message: `Khôi phục ${b.slug} từ ${snap.id}`, cost_usd: null });
          return ok(hydrateBox(s, b));
        }
        const res = createBox(s, { agent_id: b.agent_id, slug: `${b.slug}-fork-${Math.random().toString(36).slice(2, 5)}`, template: b.template, spec: b.spec, lifecycle: b.lifecycle });
        if (res.status === 201) {
          const nb = res.body as Box;
          s.files[nb.id] = { ...s.files[b.id] };
        }
        return res;
      }
      case "yaml":
        return ok({ yaml: toYaml(hydrateBox(s, b)) });
      case "egress": {
        if (M !== "PUT") break;
        const allow = Array.isArray(body.allow) ? (body.allow as unknown[]).map((x) => String(x).trim().toLowerCase()).filter(Boolean) : [];
        const bad = allow.find((x) => !EGRESS_RE.test(x));
        if (bad) return err(400, "INVALID", "bad_egress", { entry: bad });
        const deny = Boolean(body.deny_by_default) || allow.length > 0;
        if (!deny) {
          delete b.egress;
          const eff = egressFor(s, b);
          b.egress = eff ?? null;
        } else {
          const host = s.hosts.find((h) => h.id === b.host_id);
          const enforced = host?.provider !== "local";
          b.egress = { allow: [...new Set(allow)].sort(), deny_by_default: true, source: "box", enforced, mode: enforced ? "incus-acl" : "none", rules: allow.length * 2, applied_at: iso(Date.now()),
            ...(enforced ? {} : { warning: "local driver has no network isolation: the egress allowlist is NOT enforced" }) };
        }
        pushEvent(s, { actor: "user:anh", box_id: b.id, agent_id: b.agent_id, type: b.egress?.enforced ? "box.egress_applied" : "box.egress_not_enforced", message: `egress: ${b.egress ? b.egress.allow.join(", ") || "deny all" : "unrestricted"}`, cost_usd: null });
        return ok(hydrateBox(s, b));
      }
      case "exec-batch": {
        if (b.status === "sleeping") return err(409, "BOX_SLEEPING", "sleeping", {}, { retry_after_s: 5 });
        const cmds = body.commands;
        if (!Array.isArray(cmds) || !cmds.length || cmds.some((c) => typeof c !== "string")) return err(400, "INVALID", "bad_commands");
        const stop = body.stop_on_error !== false;
        const results: unknown[] = [];
        let stopped_at: number | null = null;
        for (let i = 0; i < cmds.length; i++) {
          const { result } = runExec(s, b, String(cmds[i]));
          results.push({ command: cmds[i], ...result });
          if (stop && result.exit_code !== 0) { stopped_at = i; break; }
        }
        return ok({ results, stopped_at, next: [mt(L, "n_down", { id: b.id })] });
      }
      case "events":
        return list(s.events.filter((e) => e.box_id === b.id).sort((a, c) => Date.parse(c.ts) - Date.parse(a.ts)));
      case "observe": {
        const running = b.status === "running";
        const fs = Object.keys(s.files[b.id] ?? {});
        return ok({
          files_changed: running ? fs.filter((f) => f.startsWith("/work")).slice(0, 3) : [],
          processes: running ? [{ pid: 412, cmd: b.template.includes("browser") ? "chromium --headless=new" : b.template === "node" ? "node server.js" : "python main.py" }, { pid: 88, cmd: "boxd --dial gw.boxa" }] : [],
          ports: running ? b.ports : [],
          since: b.last_active_at ?? b.created_at,
        });
      }
      case "files": {
        const fs = s.files[b.id] ?? {};
        if (M === "PUT") {
          fs[String(body.path)] = String(body.content ?? "");
          s.files[b.id] = fs;
          return ok({ ok: true });
        }
        const p = (q.get("path") || "/work").replace(/\/+$/, "") || "/";
        if (fs[p] !== undefined) {
          const lines = fs[p].split("\n");
          const from = Number(q.get("from") || 1), to = Number(q.get("to") || lines.length);
          return ok({ content: lines.slice(from - 1, to).join("\n"), total_lines: lines.length });
        }
        const prefix = p === "/" ? "/" : `${p}/`;
        const entries = new Map<string, { name: string; type: "file" | "dir"; size: number }>();
        for (const [fp, content] of Object.entries(fs)) {
          if (!fp.startsWith(prefix)) continue;
          const rest = fp.slice(prefix.length);
          const [first, ...more] = rest.split("/");
          if (more.length) {
            const e = entries.get(first) ?? { name: first, type: "dir" as const, size: 0 };
            e.size += content.length;
            entries.set(first, e);
          } else entries.set(first, { name: first, type: "file", size: new TextEncoder().encode(content).length });
        }
        if (!entries.size && !["/work", "/memory"].includes(p)) return notFound(p);
        return ok({ entries: [...entries.values()].sort((a, c) => (a.type === c.type ? a.name.localeCompare(c.name) : a.type === "dir" ? -1 : 1)) });
      }
      case "grep":
        return ok({ matches: Object.entries(s.files[b.id] ?? {}).flatMap(([p, c]) => c.split("\n").map((l, i) => ({ path: p, line: i + 1, text: l })).filter((m) => m.text.includes(String(body.pattern ?? "")))) });
      case "exec": {
        if (b.status === "sleeping") return err(409, "BOX_SLEEPING", "sleeping", {}, { retry_after_s: 5 });
        const { id, result } = runExec(s, b, String(body.command ?? ""));
        if (body.async) return ok({ job_id: id, next: [mt(L, "n_wait", { job: id })] }, 202);
        return ok({ ...result, next: result.truncated ? [mt(L, "n_log", { log: result.log_id })] : [mt(L, "n_down", { id: b.id })] });
      }
      case "takeover":
        b._taken_over = true;
        pushEvent(s, { actor: "user:anh", box_id: b.id, agent_id: b.agent_id, type: "terminal.takeover", message: "Anh chiếm quyền terminal — agent tạm dừng exec", cost_usd: null });
        return ok(hydrateBox(s, b));
      case "release":
        b._taken_over = false;
        pushEvent(s, { actor: "user:anh", box_id: b.id, agent_id: b.agent_id, type: "terminal.release", message: "Trả quyền terminal cho agent", cost_usd: null });
        return ok(hydrateBox(s, b));
    }
  }

  if (r0 === "jobs" && r1) {
    const j = s.jobs.find((x) => x.id === r1);
    if (!j) return notFound(`job ${r1}`);
    if (r2 === "cancel" && M === "POST" && (j.status === "queued" || j.status === "running")) {
      j.status = "canceled";
      j.finished_at = iso(Date.now());
    }
    return ok(j);
  }
  if (r0 === "logs" && r1) {
    const l = s.logs[r1];
    return l !== undefined ? { status: 200, body: l, contentType: "text/plain; charset=utf-8" } : notFound(`log ${r1}`);
  }

  // ---- agents ----
  if (r0 === "agents") {
    if (!r1) {
      if (M === "GET") return list(s.agents.map((a) => hydrateAgent(s, a)));
      if (M === "POST") {
        const name = String(body.name ?? "").trim();
        if (!/^[a-z0-9][a-z0-9-]{1,38}$/.test(name)) return err(400, "INVALID", "agent_name");
        if (s.agents.some((a) => a.name === name)) return err(409, "INVALID", "agent_exists", { name });
        const a: Agent = {
          id: nextId(s, "agt"),
          name,
          kind: (body.kind as AgentKind) ?? "custom",
          budget_day_usd: Number(body.budget_day_usd ?? 0.5),
          budget_month_usd: Number(body.budget_month_usd ?? 10),
          spent_today_usd: 0,
          spent_month_usd: 0,
          policy: (body.policy as Agent["policy"]) ?? { providers: ["hetzner"], regions: [], egress_allowlist: null },
          box_count: 0,
          last_call_at: null,
          memory_mb: 0,
          created_at: iso(Date.now()),
        };
        s.agents.push(a);
        s.first_call_at[a.id] = Date.now() + 15000;
        pushEvent(s, { actor: "user:anh", box_id: null, agent_id: a.id, type: "agent.created", message: `Tạo agent ${a.name} (${a.kind})`, cost_usd: null });
        return ok(a, 201);
      }
    }
    const a = s.agents.find((x) => x.id === r1 || x.name === r1);
    if (!a) return notFound(`agent ${r1}`);
    if (r2 === "snippet") return ok(makeSnippet((q.get("kind") as AgentKind) || a.kind, a.name, apiBase, q.get("transport") ?? q.get("lang")));
    if (M === "GET") return ok(hydrateAgent(s, a));
    if (M === "PATCH") {
      for (const k of ["name", "kind", "budget_day_usd", "budget_month_usd", "policy"] as const) {
        if (body[k] !== undefined) (a as unknown as Record<string, unknown>)[k] = body[k];
      }
      pushEvent(s, { actor: "user:anh", box_id: null, agent_id: a.id, type: "agent.updated", message: `Cập nhật agent ${a.name}`, cost_usd: null });
      return ok(hydrateAgent(s, a));
    }
    if (M === "DELETE") {
      s.agents = s.agents.filter((x) => x.id !== a.id);
      return ok({ ok: true });
    }
  }

  // ---- keys ----
  if (r0 === "keys") {
    if (!r1 && M === "GET") return list(s.keys);
    if (!r1 && M === "POST") {
      const rand = Array.from(crypto.getRandomValues(new Uint8Array(18))).map((x) => "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"[x % 57]).join("");
      const key = `bx_live_${rand}`;
      const k: ApiKey = { id: nextId(s, "key"), agent_id: (body.agent_id as string) ?? null, name: String(body.name ?? "key"), prefix: key.slice(0, 12), scopes: (body.scopes as string[]) ?? ["exec"], created_at: iso(Date.now()), last_used_at: null, revoked_at: null };
      s.keys.push(k);
      return ok({ ...k, key }, 201);
    }
    if (r1 && M === "DELETE") {
      const k = s.keys.find((x) => x.id === r1);
      if (!k) return notFound(`key ${r1}`);
      k.revoked_at = iso(Date.now());
      pushEvent(s, { actor: "user:anh", box_id: null, agent_id: k.agent_id, type: "key.revoked", message: `Thu hồi key ${k.prefix}…`, cost_usd: null });
      return ok(k);
    }
  }

  // ---- costs ----
  if (r0 === "costs.csv") {
    const rows = filterLedger(s, q);
    const csv = ["id,ts,agent,box,provider,kind,amount_usd,note", ...rows.map((l) => [l.id, l.ts, s.agents.find((a) => a.id === l.agent_id)?.name ?? l.agent_id, s.boxes.find((b) => b.id === l.box_id)?.slug ?? l.box_id, l.provider, l.kind, l.amount_usd.toFixed(4), l.note].map(csvEscape).join(","))].join("\n");
    return { status: 200, body: csv + "\n", contentType: "text/csv; charset=utf-8", headers: { "Content-Disposition": `attachment; filename="boxa-costs-${new Date().toISOString().slice(0, 10)}.csv"` } };
  }
  if (r0 === "costs") {
    if (r1 === "suggestions")
      return list(([
        { kind: "idle_late", title: "kecon-story-gen ngủ trễ", detail: "Box rảnh trung bình 47 phút trước khi ngủ. Đặt idle_minutes = 10 để giảm ~18% chi phí box này.", saving_usd: 1.9, box_id: "box_a9d054", agent_id: "agt_kecon" },
        { kind: "move_to_pool", title: "shopee-crawl-02 nên chuyển sang pool", detail: "Task crawl chạy đều mỗi ngày 3 giờ ở burst Vultr sgp. Đặt vào pool Hetzner fsn1 (còn 4 vCPU trống) rẻ hơn ~5 lần.", saving_usd: 4.6, box_id: "box_2c81e4", agent_id: "agt_shopee" },
        { kind: "oversized", title: "replingo-api dư spec", detail: "CPU trung bình 6%, RAM 21% trong 7 ngày. Giảm từ 2 vCPU/4 GB xuống 1 vCPU/2 GB.", saving_usd: 1.1, box_id: "box_c4410f", agent_id: "agt_replingo" },
      ] as const).map((x) => seedText(L, x.kind, { ...x })));
    const items = filterLedger(s, q);
    const by_provider: Record<string, number> = {};
    for (const l of items) by_provider[l.provider] = r4((by_provider[l.provider] ?? 0) + l.amount_usd);
    const burst = sum(items.filter((l) => s.hosts.find((h) => h.id === s.boxes.find((b) => b.id === l.box_id)?.host_id)?.billing === "burst"));
    const total = sum(items);
    return ok({ items, next_cursor: null, summary: { total, by_provider, pool_usd: r4(total - burst), burst_usd: burst } });
  }

  // ---- infra ----
  if (r0 === "providers") {
    if (!r1) return list(s.providers);
    const p = s.providers.find((x) => x.id === r1);
    if (!p) return notFound(`provider ${r1}`);
    if (r2 === "regions") return list(PROVIDER_REGIONS[p.id] ?? []);
    if (r2 === "token" && M === "PUT") {
      const token = String(body.token ?? "");
      p.last_check_at = iso(Date.now());
      if (token.length < 16 || /sai|invalid|wrong/i.test(token)) {
        p.connected = false;
        p.error = mt(L, "bad_token");
        return err(400, "PROVIDER_FAILED", "bad_token");
      }
      p.connected = true;
      p.error = null;
      pushEvent(s, { actor: "user:anh", box_id: null, agent_id: null, type: "provider.connected", message: `Kết nối ${p.name} thành công`, cost_usd: null });
      return ok(p);
    }
  }
  if (r0 === "hosts") {
    if (!r1 && M === "GET") return list(s.hosts.map((h) => hydrateHost(s, h)));
    if (!r1 && M === "POST") {
      const offer = s.offers.find((o) => o.provider === body.provider && o.sku === body.sku && o.region === body.region) ?? s.offers.find((o) => o.provider === body.provider);
      const prov = s.providers.find((p) => p.id === body.provider);
      if (!prov?.connected) return err(400, "PROVIDER_FAILED", "provider_off", { provider: String(body.provider) });
      if (!offer) return err(400, "INVALID", "no_offer");
      const h: MockState["hosts"][number] = {
        id: nextId(s, "hst"), provider: String(body.provider), region: String(body.region ?? offer.region), sku: String(body.sku ?? offer.sku),
        ip: `95.217.${Math.floor(Math.random() * 250)}.${Math.floor(Math.random() * 250)}`, status: "provisioning",
        cpu: offer.cpu, ram_mb: offer.ram_mb, disk_gb: offer.disk_gb, cpu_used_pct: 2, ram_used_pct: 6, disk_used_pct: 4, box_count: 0,
        price_month: offer.price_month, price_hour: offer.price_hour, billing: "pool", created_at: iso(Date.now()), _ready_at: Date.now() + 9000,
      };
      s.hosts.push(h);
      pushEvent(s, { actor: "user:anh", box_id: null, agent_id: null, type: "host.created", message: `Tạo host ${h.sku} tại ${h.provider}/${h.region}`, cost_usd: null });
      return ok(hydrateHost(s, h), 201);
    }
    const h = s.hosts.find((x) => x.id === r1);
    if (!h) return notFound(`host ${r1}`);
    if (r2 === "drain") {
      h.status = "draining";
      pushEvent(s, { actor: "user:anh", box_id: null, agent_id: null, type: "host.draining", message: `Drain host ${h.id}: không nhận box mới`, cost_usd: null });
      return ok(hydrateHost(s, h));
    }
    if (M === "DELETE") {
      if (s.boxes.some((b) => b.host_id === h.id && ["running", "provisioning"].includes(b.status)))
        return err(409, "INVALID", "host_busy");
      s.hosts = s.hosts.filter((x) => x.id !== h.id);
      return ok({ ok: true });
    }
  }
  if (r0 === "offers") return list([...s.offers].sort((a, b) => a.price_hour / a.cpu - b.price_hour / b.cpu));

  // ---- templates ----
  if (r0 === "templates") {
    if (r1 === "validate" && M === "POST") return ok(validateYaml(String(body.yaml ?? "")));
    if (!r1 && M === "GET") return list(s.templates);
    if (!r1 && M === "POST") {
      const name = String(body.name ?? "").trim();
      if (!name) return err(400, "INVALID", "tpl_name");
      if (s.templates.some((t) => t.name === name)) return err(409, "INVALID", "tpl_exists", { name });
      const t: Template = { name, description: String(body.description ?? ""), yaml: String(body.yaml ?? ""), uses: 0, created_at: iso(Date.now()) };
      s.templates.push(t);
      return ok(t, 201);
    }
    const t = s.templates.find((x) => x.name === decodeURIComponent(r1 ?? ""));
    if (!t) return notFound(`template ${r1}`);
    if (M === "PUT") {
      const v = validateYaml(String(body.yaml ?? t.yaml));
      if (!v.ok) return err(400, "INVALID", "tpl_yaml", { line: v.errors[0].line, detail: v.errors[0].message });
      t.yaml = String(body.yaml ?? t.yaml);
      if (body.description !== undefined) t.description = String(body.description);
      return ok(t);
    }
    if (M === "DELETE") {
      s.templates = s.templates.filter((x) => x.name !== t.name);
      return ok({ ok: true });
    }
  }

  // ---- secrets ----
  if (r0 === "secrets") {
    if (!r1 && M === "GET") return list(s.secrets);
    if (!r1 && M === "POST") {
      const name = String(body.name ?? "").trim().toUpperCase();
      if (!/^[A-Z][A-Z0-9_]{1,63}$/.test(name)) return err(400, "INVALID", "secret_name");
      if (!body.value) return err(400, "INVALID", "secret_value");
      if (s.secrets.some((x) => x.name === name)) return err(409, "INVALID", "secret_exists", { name });
      const sec = { name, agents: (body.agents as string[]) ?? [], created_at: iso(Date.now()), rotated_at: null };
      s.secrets.push(sec);
      return ok(sec, 201);
    }
    const sec = s.secrets.find((x) => x.name === decodeURIComponent(r1 ?? ""));
    if (!sec) return notFound(`secret ${r1}`);
    if (r2 === "rotate") {
      sec.rotated_at = iso(Date.now());
      pushEvent(s, { actor: "user:anh", box_id: null, agent_id: null, type: "secret.rotated", message: `Xoay vòng ${sec.name}`, cost_usd: null });
      return ok(sec);
    }
    if (M === "DELETE") {
      s.secrets = s.secrets.filter((x) => x.name !== sec.name);
      return ok({ ok: true });
    }
  }

  // ---- approvals ----
  if (r0 === "approvals") {
    if (!r1) return list([...s.approvals].sort((a, b) => Date.parse(b.created_at) - Date.parse(a.created_at)).map((a) => seedText(L, a.id, a)));
    const a = s.approvals.find((x) => x.id === r1);
    if (!a) return notFound(`approval ${r1}`);
    if (r2 === "approve" || r2 === "reject") {
      a.status = r2 === "approve" ? "approved" : "rejected";
      if (r2 === "approve" && a.type === "budget") {
        const ag = s.agents.find((x) => x.id === "agt_shopee");
        if (ag) ag.budget_day_usd = r4(ag.budget_day_usd + (a.amount_usd ?? 0));
      }
      if (r2 === "approve" && a.type === "pool_expand") {
        s.hosts.push({ id: nextId(s, "hst"), provider: "hetzner", region: "fsn1", sku: "cx42", ip: "49.12.91.4", status: "provisioning", cpu: 8, ram_mb: 16384, disk_gb: 160, cpu_used_pct: 1, ram_used_pct: 4, disk_used_pct: 3, box_count: 0, price_month: 19.4, price_hour: 0.0311, billing: "pool", created_at: iso(Date.now()), _ready_at: Date.now() + 10000 });
      }
      pushEvent(s, { actor: "user:anh", box_id: null, agent_id: null, type: `approval.${a.status}`, message: `${a.status === "approved" ? "Duyệt" : "Từ chối"}: ${a.title}`, cost_usd: null });
      return ok(seedText(L, a.id, a) as Approval);
    }
  }

  if (r0 === "emergency-stop" && M === "POST") {
    if (body.confirm !== "DUNG") return err(400, "INVALID", "confirm");
    let n = 0;
    for (const b of s.boxes) if (["running", "provisioning", "placing"].includes(b.status)) { b.status = "sleeping"; b.url = null; delete b._ready_at; n++; }
    pushEvent(s, { actor: "user:anh", box_id: null, agent_id: null, type: "emergency.stop", message: `Dừng khẩn cấp: cho ngủ ${n} box`, cost_usd: null });
    if (s.settings.notify.events.includes("emergency_stop") && s.notify_secrets.telegram) {
      s.deliveries.unshift({ ts: iso(Date.now()), kind: "emergency_stop", channel: "telegram", ok: true, title: mt(L, "n_emergency_title") });
    }
    return ok({ ok: true, slept: n });
  }

  // ---- v1.2: lưu trữ snapshot ----
  if (r0 === "storage") {
    if (r1 === "test" && M === "POST") {
      if (!s.storage.configured) return ok({ ok: false, error: "object storage not configured (snapshots stay local)", latency_ms: 0, bucket: "" });
      return ok({ ok: true, latency_ms: 140 + Math.round(Math.random() * 60), bucket: s.storage.bucket });
    }
    if (M === "PUT") {
      const prov = String(body.provider ?? "local");
      if (prov === "local") {
        s.storage = { ...s.storage, provider: "local", endpoint: "", region: "", bucket: "", prefix: "", access_key_id: "", configured: false, source: "none", has_secret: false };
      } else {
        if (!["r2", "b2", "s3", "minio"].includes(prov)) return err(400, "INVALID", "bad_storage");
        if (!body.endpoint || !body.bucket || !body.access_key_id) return err(400, "INVALID", "storage_fields");
        const hasSecret = Boolean(body.secret_access_key) || s.storage.has_secret;
        if (!hasSecret) return err(400, "INVALID", "storage_secret");
        let endpoint = String(body.endpoint).trim().replace(/\/+$/, "");
        if (!endpoint.includes("://")) endpoint = `https://${endpoint}`;
        const region = String(body.region || (prov === "r2" ? "auto" : (endpoint.match(/s3\.([a-z0-9-]+)\.backblazeb2/)?.[1] ?? "us-east-1")));
        s.storage = { ...s.storage, provider: prov as StorageView["provider"], endpoint, region, bucket: String(body.bucket), prefix: String(body.prefix ?? "").replace(/^\/+|\/+$/g, ""), access_key_id: String(body.access_key_id), configured: true, source: "settings", has_secret: true };
      }
      pushEvent(s, { actor: "user:anh", box_id: null, agent_id: null, type: "storage.updated", message: `snapshot storage: ${s.storage.provider} ${s.storage.bucket}`, cost_usd: null });
    }
    s.storage.keep_snapshots = s.settings.reaper.keep_snapshots;
    s.storage.snapshot_retention_days = s.settings.reaper.snapshot_retention_days ?? 0;
    return ok(s.storage);
  }

  // ---- v1.2: thông báo ----
  if (r0 === "notify") {
    const view = (): NotifyView => {
      const n = s.settings.notify;
      return {
        ...n, has_telegram_token: s.notify_secrets.telegram, has_smtp_password: s.notify_secrets.smtp,
        telegram_configured: s.notify_secrets.telegram && !!n.telegram_chat_id,
        email_configured: !!n.smtp?.host && !!n.smtp?.from && /@/.test(n.email),
        events_available: [...NOTIFY_EVENTS], recent: s.deliveries.slice(0, 30),
      };
    };
    if (r1 === "test" && M === "POST") {
      const ch = String(body.channel ?? "all");
      const kind = body.kind === "daily_report" ? "daily_report" : "test";
      const v = view();
      const title = kind === "daily_report" ? mt(L, "n_daily_title") : mt(L, "n_test_title");
      const results: Delivery[] = [];
      const now = iso(Date.now());
      if (ch === "all" || ch === "telegram") results.push(v.telegram_configured ? { ts: now, kind, channel: "telegram", ok: true, title } : { ts: now, kind, channel: "telegram", ok: false, error: "telegram not configured (bot token + chat id)", title });
      if (ch === "all" || ch === "email") results.push(v.email_configured && s.notify_secrets.smtp ? { ts: now, kind, channel: "email", ok: true, title } : { ts: now, kind, channel: "email", ok: false, error: v.email_configured ? "smtp auth: 535 authentication failed" : "email not configured (smtp host, from, recipients)", title });
      s.deliveries = [...results, ...s.deliveries].slice(0, 30);
      return ok({ results });
    }
    if (M === "PUT") {
      const events = Array.isArray(body.events) ? (body.events as string[]) : s.settings.notify.events;
      const unknown = events.find((e) => !(NOTIFY_EVENTS as readonly string[]).includes(e));
      if (unknown) return err(400, "INVALID", "bad_event", { event: unknown });
      const hour = Number(body.daily_report_hour ?? s.settings.notify.daily_report_hour);
      if (!(hour >= 0 && hour <= 23)) return err(400, "INVALID", "bad_hour");
      const smtpIn = (body.smtp ?? s.settings.notify.smtp) as NotifySettings["smtp"];
      s.settings.notify = {
        telegram_chat_id: String(body.telegram_chat_id ?? "").trim(), email: String(body.email ?? ""), events: [...new Set(events)], daily_report_hour: hour,
        timezone: String(body.timezone ?? s.settings.notify.timezone ?? ""), smtp: smtpIn,
      };
      if (body.clear_telegram_token) s.notify_secrets.telegram = false;
      else if (String(body.telegram_bot_token ?? "").trim()) s.notify_secrets.telegram = true;
      if (body.clear_smtp_password) s.notify_secrets.smtp = false;
      else if (String(body.smtp_password ?? "")) s.notify_secrets.smtp = true;
      pushEvent(s, { actor: "user:anh", box_id: null, agent_id: null, type: "notify.updated", message: "notification settings changed", cost_usd: null });
    }
    return ok(view());
  }

  if (r0 === "settings") {
    if (M === "GET") return ok(s.settings);
    if (M === "PUT") {
      const next = { ...s.settings, ...(body as Partial<Settings>) } as Settings;
      next.features = { ...next.features, gpu: false }; // GPU khoá tới P5
      s.settings = next;
      pushEvent(s, { actor: "user:anh", box_id: null, agent_id: null, type: "settings.updated", message: "Cập nhật cài đặt", cost_usd: null });
      return ok(s.settings);
    }
  }

  // ---- admin ----
  if (r0 === "admin") {
    if (r1 === "system")
      return ok({
        version: "0.1.0-mock",
        db: "postgres",
        public_url: "https://boxa.example.com",
        auto_burst: true,
        uptime_s: Math.round((Date.now() - s.started_at) / 1000) + 3 * 86400,
        components: [
          { name: "db", status: "ok", latency_ms: 0.4, detail: "postgres" },
          { name: "scheduler", status: "ok", latency_ms: 0, detail: `${s.hosts.length} host đang kết nối, auto_burst=true` },
          { name: "reaper", status: "ok", latency_ms: 0, detail: "chạy 12 giây trước" },
          { name: "ledger", status: "ok", latency_ms: 0, detail: "chạy 20 giây trước" },
          { name: "price_feed", status: "degraded", latency_ms: 1701, detail: "battleships.dev phản hồi chậm" },
          { name: "gateway", status: "ok", latency_ms: 0, detail: `1 SSE subscriber, ${s.hosts.length} host tunnel` },
        ],
      });
    if (r1 === "jobs") {
      const now = Date.now();
      return list([
        { name: "reaper", interval_s: 30, last_run_at: iso(now - 12_000), last_status: "ok", last_error: "", duration_ms: 3, runs: 8640 },
        { name: "ledger", interval_s: 60, last_run_at: iso(now - 20_000), last_status: "ok", last_error: "", duration_ms: 11, runs: 4320 },
        { name: "price_feed", interval_s: 21600, last_run_at: iso(now - 3 * HOUR), last_status: "ok", last_error: "", duration_ms: 1701, runs: 12 },
        { name: "self_test", interval_s: 86400, last_run_at: iso(now - 9 * HOUR), last_status: "error", last_error: "netcup: token hết hạn (401)", duration_ms: 812, runs: 3 },
        { name: "daily_report", interval_s: 86400, last_run_at: iso(now - 13 * HOUR), last_status: "ok", last_error: "", duration_ms: 240, runs: 3 },
      ]);
    }
    if (r1 === "audit") {
      let items = [...s.events].sort((a, b) => Date.parse(b.ts) - Date.parse(a.ts));
      const actor = q.get("actor"), type = q.get("type");
      if (actor) items = items.filter((e) => e.actor.includes(actor));
      if (type) items = items.filter((e) => e.type.startsWith(type));
      return list(items);
    }
    if (r1 === "quotas") {
      if (M === "PUT") s.quotas = { ...s.quotas, ...(body as Record<string, number>) };
      return ok(s.quotas);
    }
    if (r1 === "users") return list(s.users);
  }

  return err(404, "NOT_FOUND", "no_endpoint", { method: M, path: `/${segs.join("/")}` });
}

export { MIN };
