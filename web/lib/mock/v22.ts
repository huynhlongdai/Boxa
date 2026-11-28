// MOCK MODE v2.2: hạ tầng — tài khoản nhà cung cấp (M29) + sandbox nhiều khoá (M29-S).
// Bám docs/api-contract-v1.md "v2.2 additions" + "v2.2 M29-S" (core/internal/manager/accounts.go, sandboxaccounts.go).
// Trạng thái giữ trong bộ nhớ (globalThis). Không bao giờ trả token: chỉ key_fingerprint.
import type {
  AccountStats, AccountStatus, AccountStrategy, AdminProviderRow, ErrorCode, InfraAlert, InfraSummary, Provider, ProviderAccount,
  ProviderStrategyView, SandboxAccountRow, TemplateAvailability,
} from "../types";
import { ACCOUNT_STRATEGIES } from "../types";
import type { MockState } from "./seed";

type Res = { status: number; body: unknown };
interface Ctx {
  ok: (b: unknown, status?: number) => Res;
  list: <T>(items: T[]) => Res;
  err: (status: number, code: ErrorCode, key: string, params?: Record<string, string | number>) => Res;
}

const HOUR = 3_600_000;
const DAY = 24 * HOUR;
const now = () => new Date().toISOString();
const ago = (ms: number) => new Date(Date.now() - ms).toISOString();
const ahead = (ms: number) => new Date(Date.now() + ms).toISOString();
const FX: Record<string, number> = { USD: 1, EUR: 1.08, GBP: 1.27 };

interface Acc extends Omit<ProviderAccount, "stats" | "warnings"> {
  _burn: number; // USD / giờ
  _hosts: number;
  _spend_today: number; // tiền gốc
  _spend_month: number;
  _running?: number; // sandbox đang giữ slot
  _templates_api?: string[]; // template provider liệt kê khi sync
}
interface T22 {
  accounts: Acc[];
  strategies: Record<string, { strategy: AccountStrategy | ""; sticky_minutes: number; failover_across_accounts: boolean }>;
  alerts: InfraAlert[];
  timeline: { id: string; ts: string; type: string; account_id: string; provider: string; message: string; actor: string }[];
  seq: number;
}

const SANDBOX = new Set(["e2b", "daytona", "novita", "prized", "boxd", "render", "vercel", "modal", "blaxel", "upstash-box"]);
const BUILTIN: Record<string, string[]> = { e2b: ["base"], daytona: ["daytona-small"], novita: ["base"] };
const TEMPLATE_API = new Set(["e2b", "daytona", "novita"]);

function acc(p: Partial<Acc> & Pick<Acc, "id" | "provider" | "label">): Acc {
  return {
    tags: [], is_default: false, credential_ref: "account", credential_source: "api", scopes: [], enabled: true, priority: 1, weight: 1, regions: [],
    drain: false, auto_drained: false, status: "active", error_streak: 0, currency: "USD", spend_cap_day: 0, spend_cap_month: 0, min_balance: 0,
    low_balance_warn: 0, limits: {}, thresholds: {}, created_at: ago(20 * DAY), updated_at: ago(2 * DAY), last_check_at: ago(4 * 60_000),
    last_ok_at: ago(4 * 60_000), _burn: 0, _hosts: 0, _spend_today: 0, _spend_month: 0, ...p,
  };
}

type G = typeof globalThis & { __boxmesMock22?: T22 };
function st(): T22 {
  const g = globalThis as G;
  if (!g.__boxmesMock22) {
    const accounts: Acc[] = [
      acc({ id: "pa_hetzner_default", provider: "hetzner", label: "Hetzner EU #1", is_default: true, credential_ref: "provider:hetzner", credential_source: "env",
        key_fingerprint: "3f9a1c0e…k2Qp", external_id: "project-1", currency: "EUR", billing_model: "postpaid", balance: 182.4, balance_source: "manual", balance_at: ago(3 * DAY),
        spend_cap_month: 400, min_balance: 10, limits: { servers: { limit: 25, used: 3, source: "manual", at: ago(5 * DAY) } }, regions: ["fsn1", "hel1"],
        _burn: 0.094, _hosts: 3, _spend_today: 1.9, _spend_month: 51.2 }),
      acc({ id: "pa_8c2e41", provider: "hetzner", label: "Hetzner EU #2", priority: 2, key_fingerprint: "a07be9d2…Xn4f", external_id: "project-2", currency: "EUR",
        billing_model: "postpaid", balance: 60, balance_source: "manual", balance_at: ago(9 * DAY), token_expires_at: ahead(10 * DAY),
        limits: { servers: { limit: 10, used: 9, source: "learned", at: ago(HOUR) } }, status: "token_expiring", status_reason: "token expires in 10 days",
        _burn: 0.03, _hosts: 1, _spend_today: 0.6, _spend_month: 14.4 }),
      acc({ id: "pa_vultr_default", provider: "vultr", label: "Vultr SG", is_default: true, credential_ref: "provider:vultr", key_fingerprint: "77c0ab31…9Lme",
        billing_model: "prepaid", balance: 24.5, balance_source: "manual", balance_at: ago(2 * DAY), min_balance: 5, low_balance_warn: 20,
        status: "low_balance", status_reason: "runway 4.1 days < 7", _burn: 0.2, _hosts: 1, _spend_today: 4.8, _spend_month: 96 }),
      acc({ id: "pa_contabo_default", provider: "contabo", label: "Contabo EU", is_default: true, credential_ref: "provider:contabo", key_fingerprint: "c1d2e3f4…0a1b",
        currency: "EUR", billing_model: "contract", billing_cycle: "contract", contract_ends_at: ahead(12 * DAY), _burn: 0.012, _hosts: 1, _spend_today: 0.28, _spend_month: 8.5 }),
      acc({ id: "pa_e2b_default", provider: "e2b", label: "E2B prj-a key 1", is_default: true, credential_ref: "provider:e2b", key_fingerprint: "e2b_51aa…7Hq0",
        external_id: "prj-a", limit_group: "prj-a", billing_group: "ws-1", billing_model: "free_tier", credit_total: 100, credit_remaining: 38.2, credit_at: ago(DAY),
        credit_expires_at: ahead(21 * DAY), min_balance: 3, limits: { concurrent_sandboxes: { limit: 20, used: 7, source: "manual" }, create_per_sec: { limit: 1, used: 0, source: "manual" } },
        templates: ["base", "agent-py", "browser"], templates_synced_at: ago(6 * HOUR), _templates_api: ["base", "agent-py", "browser", "agent-node"],
        _burn: 0.41, _hosts: 5, _spend_today: 3.9, _spend_month: 61.8, _running: 5 }),
      acc({ id: "pa_3b19f0", provider: "e2b", label: "E2B prj-a key 2", priority: 2, key_fingerprint: "e2b_9c0d…2Wm1", external_id: "prj-a", limit_group: "prj-a", billing_group: "ws-1",
        billing_model: "free_tier", limits: { concurrent_sandboxes: { limit: 20, used: 7, source: "learned", at: ago(40 * 60_000) } }, templates_missing: ["browser"],
        _burn: 0.08, _hosts: 2, _spend_today: 0.7, _spend_month: 9.1, _running: 2, _templates_api: ["base", "agent-py"] }),
      acc({ id: "pa_d4e7a2", provider: "e2b", label: "E2B ws-2 backup", priority: 3, key_fingerprint: "e2b_0f1e…Lp9z", external_id: "prj-z", billing_group: "ws-2",
        billing_model: "prepaid", balance: 45, balance_source: "manual", balance_at: ago(DAY), min_balance: 3, drain: true, status: "draining",
        limits: { concurrent_sandboxes: { limit: 10, used: 0, source: "manual" } }, templates: ["base", "agent-py"], _templates_api: ["base", "agent-py"] }),
      acc({ id: "pa_daytona_default", provider: "daytona", label: "Daytona org-1", is_default: true, credential_ref: "provider:daytona", key_fingerprint: "dtn_44b2…c0Rt",
        external_id: "org-1", limit_group: "org-1", billing_group: "org-1", billing_model: "prepaid", credit_remaining: 72, credit_at: ago(2 * DAY), currency: "USD",
        scopes: ["write:sandboxes", "write:snapshots"], limits: { concurrent_sandboxes: { limit: 100, used: 2, source: "manual" }, create_per_min: { limit: 60, used: 0, source: "manual" } },
        _burn: 0.05, _hosts: 2, _spend_today: 1.1, _spend_month: 12.6, _running: 2, _templates_api: ["daytona-small", "agent-py"] }),
    ];
    const al = (id: string, type: string, level: InfraAlert["level"], provider: string, account_id: string, message: string, args: unknown[], openedAgo: number): InfraAlert => ({
      id, type, level, status: "open", provider, account_id, message, data: { args }, count: 1, opened_at: ago(openedAgo), updated_at: ago(openedAgo / 2),
    });
    g.__boxmesMock22 = {
      seq: 0,
      accounts,
      strategies: {},
      alerts: [
        al("low_balance|pa_vultr_default", "low_balance", "warn", "vultr", "pa_vultr_default", "Vultr SG: about 4.1 days of balance left", ["Vultr SG", 4.1], 5 * HOUR),
        al("token_expiring|pa_8c2e41", "token_expiring", "warn", "hetzner", "pa_8c2e41", "Hetzner EU #2: key expires in 10 days", ["Hetzner EU #2", 10], 2 * DAY),
        al("contract_ending|pa_contabo_default", "contract_ending", "warn", "contabo", "pa_contabo_default", "Contabo EU: contract ends in 12 days", ["Contabo EU", 12], DAY),
        al("single_account|vultr", "single_account", "info", "vultr", "", "vultr has only one usable account", ["vultr"], 3 * DAY),
        al("limit_near|pa_8c2e41|servers", "limit_near", "critical", "hetzner", "pa_8c2e41", "Hetzner EU #2: servers at 90% of the limit", ["Hetzner EU #2", "servers", 90], 3 * HOUR),
      ],
      timeline: [],
    };
    const t = g.__boxmesMock22;
    for (const a of accounts) ev(t, a, "provider_account.created", `account ${a.label} created`, a.created_at);
    ev(t, accounts[5], "provider_account.template_missing", "template browser not found in this account", ago(3 * HOUR));
    ev(t, accounts[5], "provider_account.limit_learned", "concurrent_sandboxes learned = 20 (429)", ago(40 * 60_000));
    ev(t, accounts[1], "provider_account.status_changed", "active → token_expiring", ago(2 * DAY));
  }
  return g.__boxmesMock22;
}

function ev(t: T22, a: Acc, type: string, message: string, ts = now(), actor = "system") {
  t.seq += 1;
  t.timeline.push({ id: `evt_pa${t.seq}`, ts, type, account_id: a.id, provider: a.provider, message, actor });
}

const fx = (a: Acc) => a.fx_rate_usd || FX[a.currency] || 1;
const r2 = (n: number) => Math.round(n * 100) / 100;

function balanceEff(a: Acc): number | undefined {
  const spentSince = (at?: string) => (at ? (a._burn / fx(a)) * ((Date.now() - Date.parse(at)) / HOUR) : 0);
  if (a.balance !== undefined) return r2(a.balance - spentSince(a.balance_at));
  if (a.credit_remaining !== undefined) return r2(a.credit_remaining - spentSince(a.credit_at));
  return undefined;
}

function ineligible(a: Acc): string | undefined {
  if (a.status === "archived") return "archived";
  if (!a.enabled) return "disabled";
  if (a.drain) return "draining";
  if (a.status === "auth_failed" || a.status === "out_of_credit") return a.status;
  if (a.cooldown_until && Date.parse(a.cooldown_until) > Date.now()) return "cooling down";
  const srv = a.limits.servers;
  if (srv && srv.used >= srv.limit) return `servers limit ${srv.used}/${srv.limit}`;
  return undefined;
}

function stats(t: T22, a: Acc): AccountStats {
  const be = balanceEff(a);
  const beUsd = be === undefined ? undefined : r2(be * fx(a));
  const runwayH = beUsd !== undefined && a._burn > 0 ? Math.max(0, (beUsd - a.min_balance * fx(a)) / a._burn) : undefined;
  const d = new Date();
  const hoursLeft = (Date.UTC(d.getUTCFullYear(), d.getUTCMonth() + 1, 1) - Date.now()) / HOUR;
  const why = ineligible(a);
  const sbx = SANDBOX.has(a.provider);
  const group = a.limit_group ? t.accounts.filter((x) => x.provider === a.provider && x.limit_group === a.limit_group).reduce((n, x) => n + (x._running ?? 0), 0) : (a._running ?? 0);
  return {
    hosts_live: a._hosts, boxes_live: a._hosts + (a._hosts > 1 ? 1 : 0), vcpu_live: a._hosts * 4,
    spend_today: r2(a._spend_today), spend_month: r2(a._spend_month), spend_today_usd: r2(a._spend_today * fx(a)), spend_month_usd: r2(a._spend_month * fx(a)),
    burn_per_hour_usd: a._burn, balance_effective: be, balance_effective_usd: beUsd,
    runway_hours: runwayH === undefined ? undefined : Math.round(runwayH), runway_days: runwayH === undefined ? undefined : Math.round((runwayH / 24) * 10) / 10,
    forecast_month_end: r2(a._spend_month * fx(a) + a._burn * hoursLeft), eligible: !why, ineligible: why,
    breaker: a.status === "cooling" ? "open" : "closed", has_credential: true,
    ...(sbx ? { sandboxes_running: a._running ?? 0, group_running: group, in_flight: 0, creates_last_sec: 0, creates_last_min: a._running ? 1 : 0 } : {}),
  };
}

function view(t: T22, a: Acc, warnings?: string[]): ProviderAccount {
  const out: Record<string, unknown> = { ...a, stats: stats(t, a) };
  for (const k of Object.keys(out)) if (k.startsWith("_")) delete out[k];
  if (warnings?.length) out.warnings = warnings;
  return out as unknown as ProviderAccount;
}

const isSandbox = (p: Provider) => p.group === "sandbox" || SANDBOX.has(p.id);
const defaultStrategy = (p: string): AccountStrategy => (SANDBOX.has(p) ? "drain_first" : "priority");

function strategyView(t: T22, p: string): ProviderStrategyView {
  const c = t.strategies[p] ?? { strategy: "", sticky_minutes: 0, failover_across_accounts: true };
  return { provider: p, strategy: c.strategy, effective_strategy: c.strategy || defaultStrategy(p), sticky_minutes: c.sticky_minutes, failover_across_accounts: c.failover_across_accounts, strategies: [...ACCOUNT_STRATEGIES] };
}

function providerRow(t: T22, p: Provider): AdminProviderRow {
  const accs = t.accounts.filter((a) => a.provider === p.id && a.status !== "archived");
  const st = accs.map((a) => ({ a, s: stats(t, a) }));
  const byCur: Record<string, number> = {};
  let usd: number | undefined;
  for (const { a, s } of st) {
    if (s.balance_effective === undefined) continue;
    usd = (usd ?? 0) + (s.balance_effective_usd ?? 0);
    if (a.currency !== "USD") byCur[a.currency] = r2((byCur[a.currency] ?? 0) + s.balance_effective);
  }
  const eligible = st.filter((x) => x.s.eligible);
  const burn = eligible.reduce((n, x) => n + x.a._burn, 0);
  const eligUsd = eligible.reduce((n, x) => n + (x.s.balance_effective_usd ?? 0), 0);
  const limitPct = Math.max(0, ...accs.flatMap((a) => Object.values(a.limits).map((l) => (l.limit ? Math.round((l.used / l.limit) * 100) : 0))));
  return {
    id: p.id, name: p.name, tier: p.tier, group: isSandbox(p) ? "sandbox" : "vps", connected: p.connected || accs.length > 0, implemented: p.implemented !== false,
    health: accs.some((a) => a.status === "auth_failed") ? "degraded" : "ok", strategy: strategyView(t, p.id).effective_strategy,
    accounts_total: accs.length, accounts_healthy: eligible.length, accounts_degraded: accs.length - eligible.length,
    hosts_live: accs.reduce((n, a) => n + a._hosts, 0), boxes_live: accs.reduce((n, a) => n + a._hosts, 0),
    balance_usd: usd === undefined ? undefined : r2(usd), balance_by_currency: Object.keys(byCur).length ? byCur : undefined,
    spend_today_usd: r2(st.reduce((n, x) => n + x.s.spend_today_usd, 0)), spend_month_usd: r2(st.reduce((n, x) => n + x.s.spend_month_usd, 0)),
    forecast_month_usd: r2(st.reduce((n, x) => n + x.s.forecast_month_end, 0)),
    runway_days: burn > 0 && eligible.some((x) => x.s.balance_effective_usd !== undefined) ? Math.round((eligUsd / burn / 24) * 10) / 10 : undefined,
    max_limit_pct: limitPct, open_alerts: t.alerts.filter((x) => x.provider === p.id && x.status === "open").length,
  };
}

function summary(t: T22, rows: AdminProviderRow[]): InfraSummary {
  const byCur: Record<string, number> = {};
  for (const r of rows) for (const [c, v] of Object.entries(r.balance_by_currency ?? {})) byCur[c] = r2((byCur[c] ?? 0) + v);
  let shortest: InfraSummary["shortest_runway"];
  for (const a of t.accounts) {
    if (a.status === "archived") continue;
    const s = stats(t, a);
    if (s.runway_days !== undefined && (!shortest || s.runway_days < shortest.days)) shortest = { provider: a.provider, account_id: a.id, label: a.label, days: s.runway_days };
  }
  const live = t.accounts.filter((a) => a.status !== "archived");
  const broken = live.filter((a) => ["auth_failed", "out_of_credit", "disabled"].includes(a.status)).length;
  const healthy = live.filter((a) => stats(t, a).eligible).length;
  return {
    balance_usd: r2(rows.reduce((n, r) => n + (r.balance_usd ?? 0), 0)), balance_by_currency: byCur,
    spend_today_usd: r2(rows.reduce((n, r) => n + r.spend_today_usd, 0)), spend_month_usd: r2(rows.reduce((n, r) => n + r.spend_month_usd, 0)),
    forecast_month_usd: r2(rows.reduce((n, r) => n + r.forecast_month_usd, 0)), shortest_runway: shortest,
    accounts_healthy: healthy, accounts_degraded: live.length - healthy - broken, accounts_broken: broken, open_alerts: t.alerts.filter((x) => x.status === "open").length,
  };
}

function fingerprint(tok: string) {
  let h = 0;
  for (const c of tok) h = (h * 31 + c.charCodeAt(0)) >>> 0;
  const hex = h.toString(16).padStart(8, "0");
  return tok.length >= 12 ? `${hex}…${tok.slice(-4)}` : hex;
}

const NUM_FIELDS = ["priority", "weight", "fx_rate_usd", "balance", "credit_total", "credit_remaining", "pending_charges", "month_to_date", "spend_cap_day", "spend_cap_month", "min_balance", "low_balance_warn", "invoice_day", "rotate_every_days"] as const;
const STR_FIELDS = ["label", "external_id", "owner", "notes", "console_url", "pool", "billing_model", "currency", "billing_cycle", "limit_group", "billing_group"] as const;
const DATE_FIELDS = ["token_expires_at", "refresh_expires_at", "credit_expires_at", "trial_ends_at", "contract_ends_at", "next_invoice_at"] as const;

function applyPatch(a: Acc, b: Record<string, unknown>): string | null {
  for (const k of STR_FIELDS) if (typeof b[k] === "string") {
    const v = (b[k] as string).trim();
    if ((k === "limit_group" || k === "billing_group") && (v.length > 64 || /[\s|/]/.test(v))) return k;
    if (k === "label" && !v) return k;
    (a as unknown as Record<string, unknown>)[k] = v || undefined;
  }
  for (const k of NUM_FIELDS) if (typeof b[k] === "number") (a as unknown as Record<string, unknown>)[k] = b[k];
  for (const k of DATE_FIELDS) if (typeof b[k] === "string") {
    const v = b[k] as string;
    if (v && Number.isNaN(Date.parse(v))) return k;
    (a as unknown as Record<string, unknown>)[k] = v ? new Date(v.length === 10 ? `${v}T00:00:00Z` : v).toISOString() : undefined;
  }
  if (typeof b.priority === "number" && (b.priority < 1 || b.priority > 99)) return "priority";
  if (typeof b.weight === "number" && (b.weight < 1 || b.weight > 1000)) return "weight";
  if (typeof b.enabled === "boolean") a.enabled = b.enabled;
  if (typeof b.drain === "boolean") a.drain = b.drain;
  if (Array.isArray(b.regions)) a.regions = b.regions.map(String).filter(Boolean);
  if (Array.isArray(b.tags)) a.tags = b.tags.map(String).filter(Boolean);
  if (Array.isArray(b.scopes)) a.scopes = b.scopes.map(String).filter(Boolean);
  if (Array.isArray(b.templates)) a.templates = b.templates.map(String).filter(Boolean);
  if (b.clear_templates_missing === true) a.templates_missing = [];
  if (b.clear_balance === true) { a.balance = undefined; a.balance_source = undefined; a.balance_at = undefined; }
  if (typeof b.balance === "number") { a.balance_source = "manual"; a.balance_at = now(); if (a.status === "out_of_credit" && b.balance > a.min_balance) a.status = "active"; }
  if (typeof b.credit_remaining === "number") a.credit_at = now();
  if (typeof b.billing_token === "string") a.billing_key_fingerprint = b.billing_token ? fingerprint(b.billing_token) : undefined;
  if (b.limits && typeof b.limits === "object") {
    for (const [k, v] of Object.entries(b.limits as Record<string, unknown>)) {
      if (typeof v !== "number") continue;
      if (v <= 0) delete a.limits[k];
      else a.limits[k] = { limit: v, used: a.limits[k]?.used ?? 0, source: "manual", at: now() };
    }
  }
  if (b.thresholds && typeof b.thresholds === "object") a.thresholds = { ...a.thresholds, ...(b.thresholds as Acc["thresholds"]) };
  a.updated_at = now();
  a.updated_by = "user:root";
  return null;
}

function writeWarnings(t: T22, a: Acc): string[] {
  const w: string[] = [];
  if (a.external_id) {
    const dup = t.accounts.filter((x) => x.id !== a.id && x.provider === a.provider && x.external_id === a.external_id && x.status !== "archived");
    if (dup.length && !a.limit_group) w.push(`same external_id as ${dup.map((x) => x.id).join(", ")}: not a real backup (set limit_group)`);
  }
  if (a.scopes.length && !a.scopes.includes("delete:sandboxes") && SANDBOX.has(a.provider)) w.push("key lacks delete:sandboxes: Boxmes cannot delete its sandboxes");
  return w;
}

function recomputeStatus(a: Acc) {
  if (a.status === "archived") return;
  if (!a.enabled) a.status = "disabled";
  else if (a.drain) a.status = "draining";
  else if (["disabled", "draining"].includes(a.status)) a.status = "active";
}

function sandboxRow(t: T22, a: Acc, all: string[]): SandboxAccountRow {
  const group = a.limit_group ? t.accounts.filter((x) => x.provider === a.provider && x.limit_group === a.limit_group && x.status !== "archived") : [a];
  const lims = group.map((x) => x.limits.concurrent_sandboxes).filter(Boolean);
  const lim = lims.length ? lims.reduce((m, l) => (l!.limit < m!.limit ? l : m)) : undefined;
  const running = group.reduce((n, x) => n + (x._running ?? 0), 0);
  const known = new Set([...(a.templates ?? []), ...(BUILTIN[a.provider] ?? [])]);
  const missing = new Set(a.templates_missing ?? []);
  const templates: Record<string, TemplateAvailability> = {};
  for (const tp of all) templates[tp] = missing.has(tp) ? "missing" : known.has(tp) ? "available" : (a.templates?.length ? "not_listed" : "unknown");
  const sp: string[] = [];
  if (a.scopes.length && !a.scopes.includes("write:sandboxes")) sp.push("missing write:sandboxes (not picked)");
  if (a.scopes.length && !a.scopes.includes("delete:sandboxes")) sp.push("missing delete:sandboxes (Boxmes cannot delete sandboxes)");
  const s = stats(t, a);
  return {
    id: a.id, label: a.label, status: a.status, eligible: s.eligible, ineligible: s.ineligible, limit_group: a.limit_group, billing_group: a.billing_group,
    running: a._running ?? 0, group_running: running, in_flight: 0, concurrent_limit: lim?.limit, concurrent_source: lim?.source,
    concurrency_pct: lim ? Math.round((running / lim.limit) * 100) : undefined, create_per_sec: a.limits.create_per_sec?.limit, create_per_min: a.limits.create_per_min?.limit,
    creates_last_sec: 0, creates_last_min: a._running ? 1 : 0, cooldown_until: a.cooldown_until, templates, templates_synced_at: a.templates_synced_at,
    scopes: a.scopes, scope_problems: sp.length ? sp : undefined, balance_effective: s.balance_effective, credit_expires_at: a.credit_expires_at,
  };
}

export function handleV22(s: MockState, c: Ctx, M: string, segs: string[], q: URLSearchParams, body: Record<string, unknown>): Res | null {
  if (segs[0] !== "admin" || !["providers", "infra"].includes(segs[1])) return null;
  const t = st();
  const [, r1, p, r3, a, r5, r6] = segs;
  const provs = s.providers.filter((x) => x.id !== "local");
  const bad = (what: string) => c.err(400, "INVALID", "v20_invalid", { what });

  // ---- cảnh báo ----
  if (r1 === "infra") {
    if (p === "alerts" && !r3 && M === "GET") {
      const f = q.get("status") ?? "";
      const lv = { critical: 0, warn: 1, info: 2 } as const;
      const items = t.alerts.filter((x) => (f === "all" ? true : f ? x.status === f : x.status !== "closed")).sort((x, y) => lv[x.level] - lv[y.level]);
      return c.list(items);
    }
    if (p === "alerts" && r3 && a === "ack" && M === "POST") {
      const al = t.alerts.find((x) => x.id === r3);
      if (!al) return c.err(404, "NOT_FOUND", "not_found", { what: `alert ${r3}` });
      al.status = "acked"; al.acked_at = now(); al.acked_by = "user:root"; al.updated_at = now();
      return c.ok(al);
    }
    return null;
  }

  if (!p && M === "GET") {
    const rows = provs.map((x) => providerRow(t, x));
    return c.ok({ summary: summary(t, rows), items: rows });
  }
  const prov = provs.find((x) => x.id === p);
  if (!prov) return c.err(404, "NOT_FOUND", "not_found", { what: `provider ${p}` });
  const mine = () => t.accounts.filter((x) => x.provider === p).sort((x, y) => x.priority - y.priority || x.created_at.localeCompare(y.created_at));

  if (!r3 && M === "GET") {
    return c.ok({
      ...providerRow(t, prov), prefs: {}, strategy_config: strategyView(t, p), accounts: mine().filter((x) => x.status !== "archived").map((x) => view(t, x)),
      alerts: t.alerts.filter((x) => x.provider === p && x.status !== "closed"),
      capabilities: { balance_api: false, limits_api: false, template_api: TEMPLATE_API.has(p), billing_model: SANDBOX.has(p) ? "prepaid" : "postpaid" },
    });
  }
  if (r3 === "strategy") {
    if (M === "PUT") {
      const sv = String(body.strategy ?? "");
      if (sv && !(ACCOUNT_STRATEGIES as readonly string[]).includes(sv)) return bad("strategy");
      const sticky = typeof body.sticky_minutes === "number" ? body.sticky_minutes : 0;
      if (sticky < 0 || sticky > 1440) return bad("sticky_minutes");
      t.strategies[p] = { strategy: sv as AccountStrategy | "", sticky_minutes: sticky, failover_across_accounts: body.failover_across_accounts !== false };
    }
    return c.ok(strategyView(t, p));
  }
  if (r3 === "sandbox" && M === "GET") {
    if (!isSandbox(prov)) return c.err(400, "INVALID", "v20_invalid", { what: `provider ${p} is not a sandbox provider` });
    const accs = mine().filter((x) => x.status !== "archived");
    const all = [...new Set([...(BUILTIN[p] ?? []), ...accs.flatMap((x) => [...(x.templates ?? []), ...(x.templates_missing ?? [])])])].sort();
    return c.ok({ provider: p, strategy: strategyView(t, p).effective_strategy, templates: all, accounts: accs.map((x) => sandboxRow(t, x, all)) });
  }
  if (r3 !== "accounts") return null;

  if (!a && M === "GET") return c.list(mine().filter((x) => q.get("archived") === "1" || x.status !== "archived").map((x) => view(t, x)));
  if (a === "validate" && M === "POST") {
    const tok = String(body.token ?? "");
    if (!tok) return bad("token");
    const okTok = tok.length >= 8 && !/bad|invalid/i.test(tok);
    const ext = typeof body.external_id === "string" ? body.external_id : "";
    const dups = mine().filter((x) => ext && x.external_id === ext).map((x) => x.id);
    return c.ok({ ok: okTok, latency_ms: 180 + Math.round(Math.random() * 200), key_fingerprint: fingerprint(tok), ...(okTok ? {} : { error: "401 Unauthorized: invalid token", error_class: "auth" }), ...(dups.length ? { duplicates: dups } : {}) });
  }
  if (!a && M === "POST") {
    const label = String(body.label ?? "").trim();
    const tok = String(body.token ?? "");
    if (!label) return bad("label");
    if (!tok) return bad("token");
    if (!body.skip_validate && (tok.length < 8 || /bad|invalid/i.test(tok))) return c.err(400, "INVALID", "v20_invalid", { what: "token (401 from provider)" });
    t.seq += 1;
    const n = acc({ id: `pa_${(t.seq * 7919).toString(16).padStart(6, "0")}`, provider: p, label, key_fingerprint: fingerprint(tok), created_at: now(), updated_at: now(),
      priority: Math.max(0, ...mine().map((x) => x.priority)) + 1, created_by: "user:root" });
    const e = applyPatch(n, { ...body, label });
    if (e) return bad(e);
    recomputeStatus(n);
    t.accounts.push(n);
    ev(t, n, "provider_account.created", `account ${n.label} created`, now(), "user:root");
    return c.ok(view(t, n, writeWarnings(t, n)), 201);
  }
  const acct = t.accounts.find((x) => x.provider === p && x.id === a);
  if (!acct) return c.err(404, "NOT_FOUND", "not_found", { what: `account ${a}` });

  if (!r5) {
    if (M === "GET") return c.ok(view(t, acct));
    if (M === "PATCH") {
      if (typeof body.token === "string") return bad("token (use /rotate)");
      if (acct.status === "archived") {
        if (body.enabled !== true) return bad("archived account: only {enabled: true}");
        acct.status = "active"; acct.enabled = true;
        ev(t, acct, "provider_account.enabled", "restored from archive", now(), "user:root");
        return c.ok(view(t, acct));
      }
      const e = applyPatch(acct, body);
      if (e) return bad(e);
      recomputeStatus(acct);
      ev(t, acct, "provider_account.updated", `fields: ${Object.keys(body).join(", ")}`, now(), "user:root");
      return c.ok(view(t, acct, writeWarnings(t, acct)));
    }
    if (M === "DELETE") {
      if (q.get("purge") === "1") {
        if (acct._hosts > 0) return c.err(400, "INVALID", "v20_invalid", { what: "purge: hosts still reference this account" });
        t.accounts = t.accounts.filter((x) => x !== acct);
        return { status: 204, body: null };
      }
      if (acct._hosts > 0 && !(q.get("force") === "1" && q.get("reason"))) {
        return c.err(400, "INVALID", "v20_invalid", { what: `${acct._hosts} live hosts: use force=1 with a reason` });
      }
      acct.status = "archived"; acct.enabled = false;
      ev(t, acct, "provider_account.archived", q.get("reason") ? `archived: ${q.get("reason")}` : "archived", now(), "user:root");
      return { status: 204, body: null };
    }
    return null;
  }
  if (r5 === "timeline" && M === "GET") {
    return c.list(t.timeline.filter((x) => x.account_id === acct.id).sort((x, y) => y.ts.localeCompare(x.ts)).slice(0, Number(q.get("limit") || 100)));
  }
  if (r5 === "templates" && r6 === "sync" && M === "POST") {
    if (!TEMPLATE_API.has(p)) return c.err(400, "INVALID", "v20_invalid", { what: `provider ${p} cannot list templates` });
    const listed = acct._templates_api ?? BUILTIN[p] ?? [];
    const before = new Set(acct.templates ?? []);
    const added = listed.filter((x) => !before.has(x));
    const removed = [...before].filter((x) => !listed.includes(x));
    acct.templates = [...listed];
    acct.templates_missing = (acct.templates_missing ?? []).filter((x) => !listed.includes(x));
    acct.templates_synced_at = now();
    ev(t, acct, "provider_account.templates_synced", `templates: +${added.length} −${removed.length}`, now(), "user:root");
    return c.ok({ templates: acct.templates, added, removed, account: view(t, acct) });
  }
  if (M !== "POST") return null;
  if (r5 === "rotate") {
    const tok = String(body.token ?? "");
    if (!tok) return bad("token");
    if (acct.credential_source === "env") return bad("env account: change the variable and restart");
    if (acct.external_id && body.external_id !== acct.external_id) return c.err(400, "INVALID", "v20_invalid", { what: `external_id must be ${acct.external_id}` });
    const old = acct.key_fingerprint;
    acct.key_fingerprint = fingerprint(tok); acct.rotated_at = now();
    if (acct.status === "auth_failed") acct.status = "active";
    ev(t, acct, "provider_account.key_rotated", `${old ?? "—"} → ${acct.key_fingerprint}`, now(), "user:root");
    return c.ok(view(t, acct, acct.external_id ? [] : ["external_id unknown: cannot prove the new key is the same account"]));
  }
  if (r5 === "test") {
    acct.last_check_at = now();
    const okk = acct.status !== "auth_failed";
    if (okk) acct.last_ok_at = now();
    ev(t, acct, okk ? "provider_account.validated" : "provider_account.check_failed", okk ? "key ok" : "401", now(), "user:root");
    return c.ok({ ok: okk, latency_ms: 140 + Math.round(Math.random() * 160), key_fingerprint: acct.key_fingerprint, account: view(t, acct), ...(okk ? {} : { error: "401 Unauthorized", error_class: "auth" }) });
  }
  const states: Record<string, () => void> = {
    enable: () => { acct.enabled = true; },
    disable: () => { acct.enabled = false; },
    drain: () => { acct.drain = true; },
    undrain: () => { acct.drain = false; acct.auto_drained = false; },
    reset: () => {
      acct.cooldown_until = undefined; acct.error_streak = 0;
      if (["auth_failed", "out_of_credit", "quota_full", "cooling", "rate_limited"].includes(acct.status)) acct.status = "active" as AccountStatus;
      for (const [k, l] of Object.entries(acct.limits)) if (l.source === "learned") delete acct.limits[k];
    },
  };
  const fn = states[r5];
  if (!fn) return null;
  if (acct.status === "archived") return bad("archived account");
  fn();
  recomputeStatus(acct);
  ev(t, acct, `provider_account.${r5 === "enable" ? "enabled" : r5 === "disable" ? "disabled" : r5 === "drain" ? "drained" : r5 === "undrain" ? "undrained" : "reset"}`, r5, now(), "user:root");
  return c.ok(view(t, acct));
}
