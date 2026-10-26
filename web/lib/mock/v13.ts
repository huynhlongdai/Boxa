// MOCK MODE v1.3: provider manager (health, breaker, prefs, validate, catalog), recommend + profiles,
// right-sizing, warm pool, speed, clone/ensure/resume, mesh. Shape bám theo boxa/core/internal (xem lib/types.ts).
import type {
  Box,
  BoxEstimate,
  CatalogItem,
  HealthStats,
  KindProfile,
  MeshNode,
  Offer,
  ProviderHealth,
  ProviderPrefs,
  ProviderRegion,
  RecommendOption,
  Rejection,
  Requirements,
  RightSize,
  ScoreComponent,
  ScoreFactor,
  WarmPoolSpec,
  WarmView,
  WorkloadProfile,
} from "../types";
import type { Locale } from "@/i18n/config";
import { mt } from "./i18n";
import { DAY, HOUR, MIN, ago, iso, r4, type MockState } from "./seed";

type Res = { status: number; body: unknown; contentType?: string; headers?: Record<string, string> };
export interface V13Ctx {
  L: Locale;
  ok: (body: unknown, status?: number) => Res;
  list: <T>(items: T[]) => Res;
  err: (status: number, code: "INVALID" | "NOT_FOUND" | "NO_CAPACITY" | "PROVIDER_FAILED" | "POLICY_DENIED", key: string, params?: Record<string, string | number>) => Res;
  hydrateBox: (b: MockState["boxes"][number]) => Box;
  createBox: (body: Record<string, unknown>) => Res;
  nextId: (prefix: string) => string;
  pushEvent: (e: { actor: string; box_id: string | null; agent_id: string | null; type: string; message: string; cost_usd: number | null }) => unknown;
  regions: Record<string, ProviderRegion[]>;
}

// ---------------- Profiles (giống selector/profiles.go) ----------------
type W = Record<ScoreFactor, number>;
const balanced: W = { cost: 0.3, health: 0.15, speed: 0.2, latency: 0.15, capacity: 0.1, reliability: 0.1 };
const prof = (p: Omit<WorkloadProfile, "egress_deny_by_default" | "egress_allow" | "warm" | "ttl_seconds" | "idle_minutes"> & Partial<WorkloadProfile>): WorkloadProfile => ({
  idle_minutes: 0, ttl_seconds: 0, egress_deny_by_default: false, egress_allow: null, warm: false, ...p,
});
export const PROFILES: WorkloadProfile[] = [
  prof({ id: "general", name: "General", description: "Small all-purpose Linux box for scripts and quick experiments.", template: "base", lifecycle: "session", idle_minutes: 5,
    defaults: { cpu: 1, ram_mb: 1024, disk_gb: 10, lifetime: "ephemeral" }, weights: balanced, warm: true, tips: ["Start here when unsure; right-sizing suggests a better size after a few minutes of use."] }),
  prof({ id: "coding-agent", name: "Coding agent", description: "Interactive repo work for coding agents (git, build tools, tests). Fast start and low latency matter most.", template: "python", lifecycle: "session", idle_minutes: 10,
    defaults: { cpu: 2, ram_mb: 4096, disk_gb: 20, lifetime: "persistent" }, weights: { cost: 0.25, health: 0.15, speed: 0.25, latency: 0.2, capacity: 0.05, reliability: 0.1 }, warm: true, tips: ["Snapshot before risky refactors; fork to try alternatives in parallel."] }),
  prof({ id: "browser-automation", name: "Browser automation", description: "Headless Chromium/Playwright scraping and web tasks; needs RAM and a public egress.", template: "python-browser", lifecycle: "session", idle_minutes: 5,
    defaults: { cpu: 2, ram_mb: 4096, disk_gb: 15, lifetime: "ephemeral" }, weights: { cost: 0.3, health: 0.15, speed: 0.25, latency: 0.1, capacity: 0.1, reliability: 0.1 }, warm: true, tips: ["Chromium needs ≥2 GB RAM per browser; one browser per vCPU is a good rule."] }),
  prof({ id: "build", name: "Build / CI", description: "Short CPU-heavy compiles and test runs; cheapest compute that finishes soon, then thrown away.", template: "base", lifecycle: "task", ttl_seconds: 3600,
    defaults: { cpu: 4, ram_mb: 8192, disk_gb: 30, lifetime: "ephemeral", duration_hours: 1 }, weights: { cost: 0.4, health: 0.1, speed: 0.2, latency: 0.05, capacity: 0.15, reliability: 0.1 }, tips: ["Host caches (apt/pip/npm/go proxies) make repeat builds much faster."] }),
  prof({ id: "data", name: "Data processing", description: "Pandas/DuckDB/ETL jobs: RAM and disk heavy, latency-insensitive.", template: "python", lifecycle: "task", ttl_seconds: 4 * 3600,
    defaults: { cpu: 2, ram_mb: 8192, disk_gb: 50, lifetime: "ephemeral", duration_hours: 4 }, weights: { cost: 0.45, health: 0.1, speed: 0.1, latency: 0.05, capacity: 0.2, reliability: 0.1 }, tips: ["Write results to /memory or snapshot to object storage before the TTL ends."] }),
  prof({ id: "long-running-service", name: "Long-running service", description: "Bots, webhooks, APIs that run 24/7 with a public URL; reliability and monthly cost dominate.", template: "node", lifecycle: "service",
    defaults: { cpu: 1, ram_mb: 1024, disk_gb: 10, lifetime: "persistent", duration_hours: 730, public_url: true }, weights: { cost: 0.35, health: 0.15, speed: 0.05, latency: 0.15, capacity: 0.1, reliability: 0.2 }, tips: ["Service boxes are never auto-stopped; Boxa asks you before stopping one over budget."] }),
  prof({ id: "sandbox-untrusted", name: "Untrusted code sandbox", description: "Run code you do not trust (user uploads, generated code): strongest isolation, offline by default, thrown away.", template: "python", lifecycle: "task", ttl_seconds: 900,
    defaults: { cpu: 1, ram_mb: 1024, disk_gb: 5, lifetime: "ephemeral", isolation: "vm", untrusted: true, duration_hours: 0.25 }, weights: { cost: 0.2, health: 0.15, speed: 0.2, latency: 0.05, capacity: 0.1, reliability: 0.3 },
    egress_deny_by_default: true, egress_allow: ["pypi.org", "files.pythonhosted.org"], warm: true, tips: ["Prefers an Incus VM or a T2 sandbox API; containers share the host kernel."] }),
];
const kp = (kind: string, profile: string, cpu: number, ram_mb: number, disk_gb: number, why: string): KindProfile => ({ kind, profile, spec: { cpu, ram_mb, disk_gb }, why });
export const KIND_PROFILES: KindProfile[] = [
  kp("claude-code", "coding-agent", 2, 4096, 20, "repo + tests; node and python toolchains"),
  kp("codex-cli", "coding-agent", 2, 4096, 20, "repo + tests"),
  kp("gemini-cli", "coding-agent", 2, 4096, 20, "repo + tests"),
  kp("cursor", "coding-agent", 2, 4096, 20, "remote workspace for the IDE agent"),
  kp("windsurf", "coding-agent", 2, 4096, 20, "remote workspace for the IDE agent"),
  kp("cline", "coding-agent", 2, 2048, 15, "lighter IDE agent workspace"),
  kp("openai-agents", "general", 1, 2048, 10, "tool-calling runtime; short code runs"),
  kp("langchain", "general", 1, 2048, 10, "tool execution"),
  kp("langgraph", "general", 1, 2048, 10, "graph nodes run code here"),
  kp("crewai", "general", 2, 2048, 10, "several agents share one box"),
  kp("autogen", "sandbox-untrusted", 1, 2048, 10, "executes LLM-generated code"),
  kp("vercel-ai-sdk", "general", 1, 1024, 10, "code-interpreter style tools"),
  kp("mastra", "general", 1, 1024, 10, "workflow steps"),
  kp("pydantic-ai", "general", 1, 1024, 10, "typed tool runs"),
  kp("llamaindex", "data", 2, 4096, 30, "indexing documents is RAM/disk heavy"),
  kp("n8n", "long-running-service", 1, 2048, 10, "always-on workflow server"),
  kp("dify", "long-running-service", 2, 4096, 20, "always-on app platform"),
  kp("flowise", "long-running-service", 1, 2048, 10, "always-on flow server"),
  kp("make", "build", 1, 1024, 10, "short scenario steps"),
  kp("zapier", "build", 1, 1024, 10, "short zap steps"),
  kp("hark", "browser-automation", 2, 4096, 15, "browses and automates the web"),
  kp("custom", "general", 1, 1024, 10, "safe small default"),
];

// ---------------- State ----------------
interface ProvState {
  stats: HealthStats;
  prefs: ProviderPrefs;
}
interface V13State {
  prov: Record<string, ProvState>;
  warm: { enabled: boolean; pools: WarmPoolSpec[]; hits: number; misses: number };
  mesh: { enabled: boolean; keys: number };
}
type S13 = MockState & { _v13?: V13State };

const emptyClasses = () => ({}) as Record<string, number>;
function stats(provider: string, p: Partial<HealthStats>): HealthStats {
  return {
    provider, samples: 0, api_latency_p50_ms: 0, api_latency_p95_ms: 0, api_success_rate: 1, create_attempts: 0, create_success_rate: 1, provision_p50_s: 0, provision_p95_s: 0,
    error_classes: emptyClasses(), last_ok_at: null, last_error_at: null, breaker: { state: "closed", consecutive_failures: 0, opens: 0 },
    rate_limit: { hits: 0, remaining: -1 }, status: "unknown", reliability: 1, ...p,
  };
}
const prefs = (p: Partial<ProviderPrefs> = {}): ProviderPrefs => ({ enabled: true, priority: 0, max_hourly_usd: 0, max_monthly_usd: 0, max_servers: 0, regions: [], default_region: "", ...p });

function state(s: MockState, L: Locale): V13State {
  const g = s as S13;
  if (g._v13) return g._v13;
  const now = Date.now();
  g._v13 = {
    prov: {
      hetzner: { stats: stats("hetzner", { samples: 288, api_latency_p50_ms: 182, api_latency_p95_ms: 431, api_success_rate: 0.993, create_attempts: 14, create_success_rate: 1, provision_p50_s: 47, provision_p95_s: 71, last_ok_at: ago(4 * MIN), last_error_at: ago(19 * HOUR), last_error: "HTTP 503 from api.hetzner.cloud", last_error_class: "server", error_classes: { server: 2 }, status: "healthy", reliability: 0.99 }), prefs: prefs({ priority: 1, max_hourly_usd: 0.08, max_monthly_usd: 60, max_servers: 4, regions: ["fsn1", "hel1", "nbg1"], default_region: "fsn1", updated_at: ago(6 * DAY) }) },
      vultr: { stats: stats("vultr", { samples: 276, api_latency_p50_ms: 305, api_latency_p95_ms: 1260, api_success_rate: 0.93, create_attempts: 7, create_success_rate: 0.857, provision_p50_s: 84, provision_p95_s: 142, last_ok_at: ago(5 * MIN), last_error_at: ago(38 * MIN), last_error: mt(L, "h_capacity", { region: "nrt" }), last_error_class: "capacity", error_classes: { capacity: 1, timeout: 3, rate_limit: 1 }, breaker: { state: "half_open", consecutive_failures: 2, opens: 1, opened_at: ago(40 * MIN), open_until: ago(10 * MIN), last_reason: "timeout" }, rate_limit: { hits: 1, remaining: 18 }, status: "degraded", reliability: 0.86 }), prefs: prefs({ priority: 2, max_hourly_usd: 0.06, max_servers: 2, regions: ["sgp", "nrt"], default_region: "sgp", updated_at: ago(3 * DAY) }) },
      digitalocean: { stats: stats("digitalocean", {}), prefs: prefs({ priority: 3 }) },
      contabo: { stats: stats("contabo", { samples: 140, api_latency_p50_ms: 640, api_latency_p95_ms: 2100, api_success_rate: 0.71, create_attempts: 3, create_success_rate: 0.33, provision_p50_s: 880, provision_p95_s: 1310, last_ok_at: ago(2 * HOUR), last_error_at: ago(7 * MIN), last_error: mt(L, "h_quota"), last_error_class: "quota", error_classes: { quota: 4, timeout: 2 }, breaker: { state: "open", consecutive_failures: 5, opens: 2, opened_at: ago(7 * MIN), open_until: iso(now + 8 * MIN), last_reason: "quota" }, rate_limit: { hits: 0, remaining: -1, quota_hit_at: ago(7 * MIN) }, status: "down", reliability: 0.33 }), prefs: prefs({ priority: 4, max_monthly_usd: 20, max_servers: 1, regions: ["EU", "SIN"], default_region: "EU", updated_at: ago(12 * DAY) }) },
      netcup: { stats: stats("netcup", { samples: 12, api_success_rate: 0, last_error_at: ago(2 * DAY), last_error: mt(L, "bad_token"), last_error_class: "auth", error_classes: { auth: 12 }, status: "down", reliability: 1 }), prefs: prefs({ priority: 5, enabled: false }) },
      local: { stats: stats("local", { samples: 0, status: "healthy" }), prefs: prefs() },
    },
    warm: {
      enabled: true,
      hits: 41,
      misses: 9,
      pools: [
        { template: "python", profile: "coding-agent", spec: { cpu: 2, ram_mb: 4096, disk_gb: 20 }, min: 1, max: 3, ttl_minutes: 30 },
        { template: "python-browser", profile: "browser-automation", spec: { cpu: 2, ram_mb: 4096, disk_gb: 15 }, min: 1, max: 2, ttl_minutes: 20 },
      ],
    },
    mesh: { enabled: true, keys: 0 },
  };
  // Seed: vài box có profile + estimate (đo thực tế sau khi chạy)
  const patch: Record<string, { profile: string; estimate: BoxEstimate }> = {
    box_7f3a91: { profile: "browser-automation", estimate: { kind: "warm", cost_per_hour: 0.0078, expected_hours: 8, expected_total_usd: 0.0624, eta_seconds: 1.2, actual_seconds: 1.4, score: 88.6, warm_hit: true } },
    box_a9d054: { profile: "coding-agent", estimate: { kind: "pool", cost_per_hour: 0.0221, expected_hours: 8, expected_total_usd: 0.1768, eta_seconds: 9.5, actual_seconds: 11.2, score: 81.3, warm_hit: false } },
    box_2c81e4: { profile: "browser-automation", estimate: { kind: "burst", cost_per_hour: 0.03, expected_hours: 3, expected_total_usd: 0.09, eta_seconds: 96, actual_seconds: 104, score: 63.2, warm_hit: false } },
  };
  for (const b of s.boxes) if (patch[b.id] && !b.estimate) Object.assign(b, patch[b.id]);
  return g._v13;
}

// ---------------- Scoring (giản lược selector/score.go) ----------------
const RTT: Record<string, number> = { sgp: 34, sin: 36, SIN: 38, sgp1: 35, nrt: 72, JPN: 74, blr1: 95, IND: 98, AUS: 160, fsn1: 192, nbg1: 196, nbg: 196, nue: 196, "eu-de": 198, EU: 198, hel1: 214, fra: 188, fra1: 188, ams: 186, vie: 190, UK: 205, ash: 232, ewr: 238, nyc3: 238, "US-east": 238, man: 236, "US-central": 250, hil: 205, "US-west": 200 };
const rtt = (region: string) => RTT[region] ?? 180;
const FACTORS: ScoreFactor[] = ["cost", "health", "speed", "latency", "capacity", "reliability"];
function weightsFor(p: WorkloadProfile, optimize?: string): W {
  const w = { ...p.weights };
  const boost: Partial<Record<string, ScoreFactor>> = { cost: "cost", speed: "speed", reliability: "reliability", latency: "latency" };
  const f = optimize ? boost[optimize] : undefined;
  if (f) {
    w[f] += 0.3;
    const sum = FACTORS.reduce((a, k) => a + w[k], 0);
    for (const k of FACTORS) w[k] = Math.round((w[k] / sum) * 1000) / 1000;
  }
  return w;
}
const healthScore = (h: string) => (h === "healthy" ? 100 : h === "degraded" ? 60 : h === "unknown" ? 70 : 0);

type Opt = Omit<RecommendOption, "rank" | "score" | "breakdown" | "reasons"> & { reasons: string[] };

export function resolveNeeds(rq: Requirements, profileId?: string, kind?: string): { req: Requirements; profile: WorkloadProfile } {
  const k = kind ? KIND_PROFILES.find((x) => x.kind === kind) : undefined;
  const pid = rq.profile || profileId || k?.profile || "general";
  const profile = PROFILES.find((p) => p.id === pid) ?? PROFILES[0];
  const base = { ...profile.defaults, ...(k && !rq.profile && !profileId ? k.spec : {}) };
  const req: Requirements = { ...base };
  for (const [key, v] of Object.entries(rq)) if (v !== undefined && v !== null && v !== "" && v !== 0) (req as Record<string, unknown>)[key] = v;
  req.profile = profile.id;
  req.location = req.location || "VN";
  req.optimize = req.optimize || "balanced";
  req.arch = req.arch || "any";
  return { req, profile };
}

export function recommend(s: MockState, L: Locale, body: { requirements?: Requirements; profile?: string; template?: string; lifecycle?: string; agent_id?: string; kind?: string; limit?: number }) {
  const st = state(s, L);
  const agent = s.agents.find((a) => a.id === body.agent_id);
  const { req, profile } = resolveNeeds(body.requirements ?? {}, body.profile, body.kind ?? agent?.kind);
  const lifecycle = body.lifecycle || profile.lifecycle;
  const template = body.template || profile.template;
  const hours = req.duration_hours || (lifecycle === "service" ? 730 : lifecycle === "session" ? 8 : 2);
  const cpu = req.cpu ?? 1, ram = req.ram_mb ?? 1024, disk = req.disk_gb ?? 10;
  const opts: Opt[] = [];
  const rej: Rejection[] = [];
  const allowed = agent?.policy.providers;
  const loc = req.location ?? "VN";

  // warm pool
  const wp = st.warm.enabled ? st.warm.pools.find((p) => p.template === template && p.spec.cpu >= cpu && p.spec.ram_mb >= ram) : undefined;
  const warmHost = s.hosts.find((h) => h.id === "hst_fsn1_01")!;
  if (wp && profile.warm && req.isolation !== "vm") {
    opts.push({ kind: "warm", provider: warmHost.provider, region: warmHost.region, host_id: warmHost.id, arch: "amd64", isolation: "container", cpu: wp.spec.cpu, ram_mb: wp.spec.ram_mb, disk_gb: wp.spec.disk_gb,
      cost_per_hour: r4((warmHost.price_hour / warmHost.cpu) * wp.spec.cpu), expected_total_usd: 0, eta_seconds: 1.2, reliability: 0.99, health: "healthy", headroom: 0.42, latency_ms: rtt(warmHost.region), min_billing_h: 0,
      requires_approval: false, price_stale: false, warm_id: "wrm_7c21", reasons: [mt(L, "r_warm", { host: warmHost.id })] });
  }
  // pool containers + VM
  for (const h of s.hosts) {
    if (h.status !== "ready" || h.billing !== "pool") continue;
    if (allowed && !allowed.includes(h.provider)) { rej.push({ kind: "pool", provider: h.provider, region: h.region, host_id: h.id, reason: mt(L, "j_policy", { provider: h.provider }) }); continue; }
    const freeCpu = Math.round(h.cpu * (1 - h.cpu_used_pct / 100) * 10) / 10;
    const freeRam = Math.round(h.ram_mb * (1 - h.ram_used_pct / 100));
    const cph = r4((h.price_hour / h.cpu) * cpu);
    const base = { provider: h.provider, region: h.region, host_id: h.id, arch: h.sku.startsWith("cax") ? "arm64" : "amd64", cpu, ram_mb: ram, disk_gb: disk, cost_per_hour: cph, expected_total_usd: 0,
      reliability: 0.995, health: "healthy", latency_ms: rtt(h.region), min_billing_h: 0, requires_approval: false, price_stale: false };
    if (freeCpu < cpu || freeRam < ram) { rej.push({ kind: "pool", provider: h.provider, region: h.region, host_id: h.id, reason: mt(L, "j_nocap", { host: h.id, cpu: freeCpu, ram: freeRam }) }); continue; }
    const headroom = Math.max(0, Math.round((1 - (h.cpu_used_pct / 100 + cpu / h.cpu)) * 100) / 100);
    opts.push({ ...base, kind: "pool", isolation: "container", eta_seconds: 8.5, headroom, reasons: [mt(L, "r_pool", { host: h.id, free: freeCpu })] });
    if (h.id === "hst_hel1_01") opts.push({ ...base, kind: "vm", isolation: "vm", eta_seconds: 24, headroom: Math.max(0, headroom - 0.05), reasons: [mt(L, "r_vm", { host: h.id })] });
  }
  // burst offers
  for (const o of s.offers) {
    const ps = st.prov[o.provider];
    const conn = s.providers.find((p) => p.id === o.provider)?.connected;
    const r = { kind: "burst", provider: o.provider, region: o.region, sku: o.sku };
    if (!conn) continue;
    if (allowed && !allowed.includes(o.provider)) { rej.push({ ...r, reason: mt(L, "j_policy", { provider: o.provider }) }); continue; }
    if (ps?.stats.breaker.state === "open") { rej.push({ ...r, reason: mt(L, "j_breaker", { provider: o.provider, reason: ps.stats.breaker.last_reason ?? "" }) }); continue; }
    if (ps && !ps.prefs.enabled) { rej.push({ ...r, reason: mt(L, "j_disabled", { provider: o.provider }) }); continue; }
    if (ps?.prefs.max_hourly_usd && o.price_hour > ps.prefs.max_hourly_usd) { rej.push({ ...r, reason: mt(L, "j_cap", { provider: o.provider, price: o.price_hour, max: ps.prefs.max_hourly_usd }) }); continue; }
    if (ps?.prefs.regions.length && !ps.prefs.regions.includes(o.region)) { rej.push({ ...r, reason: mt(L, "j_allowed", { region: o.region, provider: o.provider }) }); continue; }
    const minH = o.min_billing_minutes / 60;
    if (minH >= 700 && hours < 200) { rej.push({ ...r, reason: mt(L, "j_monthly", { hours }) }); continue; }
    opts.push({ kind: "burst", provider: o.provider, region: o.region, sku: o.sku, arch: o.sku.startsWith("cax") ? "arm64" : "amd64", isolation: "vm", cpu: o.cpu, ram_mb: o.ram_mb, disk_gb: o.disk_gb,
      cost_per_hour: o.price_hour, expected_total_usd: 0, eta_seconds: (ps?.stats.provision_p50_s || 70) + 12, reliability: ps?.stats.create_success_rate ?? 0.9, health: ps?.stats.status ?? "unknown",
      headroom: Math.max(0, 1 - cpu / o.cpu), latency_ms: rtt(o.region), min_billing_h: minH, requires_approval: !['hetzner', 'vultr'].includes(o.provider), price_stale: Date.now() - Date.parse(o.fetched_at) > 6 * HOUR,
      reasons: [mt(L, "r_burst", { sku: o.sku, provider: o.provider, region: o.region })] });
  }
  // filters
  const keep: Opt[] = [];
  for (const o of opts) {
    const r = { kind: o.kind, provider: o.provider, region: o.region, host_id: o.host_id, sku: o.sku };
    if ((req.gpu ?? 0) > 0) rej.push({ ...r, reason: mt(L, "j_gpu") });
    else if (o.cpu < cpu || o.ram_mb < ram) rej.push({ ...r, reason: mt(L, "j_small", { cpu: o.cpu, ram: o.ram_mb }) });
    else if (req.arch && req.arch !== "any" && o.arch && o.arch !== req.arch) rej.push({ ...r, reason: mt(L, "j_arch", { arch: o.arch, want: req.arch }) });
    else if (req.region && o.region !== req.region) rej.push({ ...r, reason: mt(L, "j_region", { region: o.region, want: req.region }) });
    else if (req.max_price_hour && o.cost_per_hour > req.max_price_hour) rej.push({ ...r, reason: mt(L, "j_price", { price: o.cost_per_hour, max: req.max_price_hour }) });
    else if (req.max_latency_ms && o.latency_ms > req.max_latency_ms) rej.push({ ...r, reason: mt(L, "j_latency", { ms: o.latency_ms, max: req.max_latency_ms }) });
    else if ((req.isolation === "vm" || req.untrusted) && o.isolation === "container") rej.push({ ...r, reason: mt(L, req.untrusted ? "j_untrusted" : "j_isolation", { iso: "vm" }) });
    else if (req.isolation === "container" && o.isolation !== "container") rej.push({ ...r, reason: mt(L, "j_isolation", { iso: "container" }) });
    else keep.push(o);
  }
  for (const o of keep) o.expected_total_usd = r4(o.cost_per_hour * Math.max(hours, o.min_billing_h));
  const w = weightsFor(profile, req.optimize);
  const minTotal = Math.min(...keep.map((o) => Math.max(o.expected_total_usd, 0.0001)));
  const usd = (n: number) => `$${n.toFixed(4)}`;
  const scored: RecommendOption[] = keep.map((o) => {
    const comp: Record<ScoreFactor, ScoreComponent> = {
      cost: { score: Math.round(100 * Math.min(1, minTotal / Math.max(o.expected_total_usd, 0.0001))), weight: w.cost, value: `${usd(o.cost_per_hour)}/h · Σ ${usd(o.expected_total_usd)}` },
      health: { score: healthScore(o.health), weight: w.health, value: o.health },
      speed: { score: Math.round(100 * Math.exp(-o.eta_seconds / 150)), weight: w.speed, value: `~${o.eta_seconds}s` },
      latency: { score: Math.round(100 * Math.max(0, 1 - o.latency_ms / 300)), weight: w.latency, value: `~${o.latency_ms} ms → ${loc}` },
      capacity: { score: Math.round(o.headroom * 100), weight: w.capacity, value: `${Math.round(o.headroom * 100)}%` },
      reliability: { score: Math.round(o.reliability * 100), weight: w.reliability, value: `${Math.round(o.reliability * 100)}%` },
    };
    const score = Math.round(FACTORS.reduce((a, f) => a + comp[f].score * comp[f].weight, 0) * 10) / 10;
    const reasons = [...o.reasons];
    if (o.latency_ms < 80) reasons.push(mt(L, "r_near", { ms: o.latency_ms, loc }));
    if (o.health === "degraded") reasons.push(mt(L, "r_degraded", { provider: o.provider, rate: Math.round(o.reliability * 100) }));
    if (o.requires_approval) reasons.push(mt(L, "r_approval"));
    return { ...o, rank: 0, score, breakdown: comp, reasons };
  });
  scored.sort((a, b) => b.score - a.score);
  if (scored.length) {
    const cheapest = [...scored].sort((a, b) => a.expected_total_usd - b.expected_total_usd)[0];
    cheapest.reasons.push(mt(L, "r_cheapest", { hours }));
    const fastest = [...scored].sort((a, b) => a.eta_seconds - b.eta_seconds)[0];
    if (fastest !== cheapest) fastest.reasons.push(mt(L, "r_fastest", { eta: fastest.eta_seconds }));
  }
  scored.forEach((o, i) => (o.rank = i + 1));
  const limit = body.limit && body.limit > 0 ? body.limit : 5;
  const options = scored.slice(0, limit);
  const pick: RecommendOption | null = options.find((o) => !o.requires_approval) ?? options[0] ?? null;
  const boxUp: Record<string, unknown> = { profile: profile.id, template, lifecycle, requirements: req };
  if (agent) boxUp.agent_id = agent.id;
  return {
    requirements: req,
    profile,
    template,
    lifecycle,
    expected_hours: hours,
    options,
    rejected: rej,
    pick,
    auto_pick: pick ? mt(L, "auto_pick", { kind: pick.kind, provider: pick.provider, region: pick.region }) : mt(L, "auto_none"),
    explain: mt(L, "explain", { profile: profile.id, opt: req.optimize ?? "balanced", n: scored.length, r: rej.length }),
    box_up: boxUp,
    next: pick ? [`box_up(profile="${profile.id}", template="${template}", ...)  # or box_ensure(name=..., same args)`] : [],
  };
}

/** Ước tính cho box mới tạo (mọi create đều có estimate). */
export function estimateFor(kind: string, cph: number, lifecycle: string, hours?: number): BoxEstimate {
  const h = hours || (lifecycle === "service" ? 730 : lifecycle === "session" ? 8 : 2);
  const eta = kind === "warm" ? 1.2 : kind === "clone" ? 2.1 : kind === "vm" ? 24 : kind === "burst" ? 96 : 8.5;
  return { kind, cost_per_hour: cph, expected_hours: h, expected_total_usd: r4(cph * h), eta_seconds: eta, warm_hit: kind === "warm" };
}

// ---------------- Health view ----------------
function healthRow(s: MockState, L: Locale, id: string, detail: boolean): ProviderHealth | null {
  const st = state(s, L);
  const p = s.providers.find((x) => x.id === id);
  const ps = st.prov[id];
  if (!p || !ps) return null;
  const offers = s.offers.filter((o) => o.provider === id);
  const ref = offers.sort((a, b) => a.price_hour - b.price_hour)[0];
  let score: ProviderHealth["score"] = null;
  if (ref && p.connected) {
    const w = balanced;
    const comp: Record<ScoreFactor, ScoreComponent> = {
      cost: { score: Math.round(100 * Math.min(1, 0.0071 / ref.price_hour)), weight: w.cost, value: `$${ref.price_hour.toFixed(4)}/h (${ref.sku})` },
      health: { score: healthScore(ps.stats.status), weight: w.health, value: ps.stats.status },
      speed: { score: Math.round(100 * Math.exp(-(ps.stats.provision_p50_s || 90) / 150)), weight: w.speed, value: `p50 ${ps.stats.provision_p50_s || "?"}s` },
      latency: { score: Math.round(100 * Math.max(0, 1 - rtt(ref.region) / 300)), weight: w.latency, value: `~${rtt(ref.region)} ms → VN` },
      capacity: { score: 100, weight: w.capacity, value: "new server" },
      reliability: { score: Math.round(ps.stats.create_success_rate * 100), weight: w.reliability, value: `${Math.round(ps.stats.create_success_rate * 100)}%` },
    };
    const total = Math.round(FACTORS.reduce((a, f) => a + comp[f].score * comp[f].weight, 0) * 10) / 10;
    score = { score: total, region: ref.region, sku: ref.sku, breakdown: comp, reasons: [mt(L, "r_burst", { sku: ref.sku, provider: id, region: ref.region }), ...(ps.stats.status === "degraded" ? [mt(L, "r_degraded", { provider: id, rate: Math.round(ps.stats.create_success_rate * 100) })] : [])] };
  }
  const blocked = !p.connected ? mt(L, "b_notconn") : ps.stats.breaker.state === "open" ? mt(L, "j_breaker", { provider: id, reason: ps.stats.breaker.last_reason ?? "" }) : !ps.prefs.enabled ? mt(L, "j_disabled", { provider: id }) : "";
  const regions = id === "hetzner" ? [stats(id, { ...ps.stats, region: "fsn1", samples: 9, provision_p50_s: 44 }), stats(id, { ...ps.stats, region: "hel1", samples: 5, provision_p50_s: 52 })]
    : id === "vultr" ? [stats(id, { ...ps.stats, region: "sgp", samples: 5, create_success_rate: 1, status: "healthy" }), stats(id, { ...ps.stats, region: "nrt", samples: 2, create_success_rate: 0.5 })] : [];
  const recent = detail && ps.stats.samples ? Array.from({ length: 8 }, (_, i) => ({ ts: ago((i + 1) * 5 * MIN), kind: i % 4 === 3 ? "create" : "api", ok: !(id === "contabo" && i < 3) && !(id === "vultr" && i === 2), latency_ms: Math.round(ps.stats.api_latency_p50_ms * (0.8 + ((i * 37) % 10) / 20)), class: id === "contabo" && i < 3 ? "quota" : id === "vultr" && i === 2 ? "timeout" : "" })) : undefined;
  return { provider: p, stats: ps.stats, regions, prefs: ps.prefs, score, recent, usable: !blocked, blocked: blocked || undefined, hosts: s.hosts.filter((h) => h.provider === id).length,
    offer_age_s: offers.length ? Math.round((Date.now() - Date.parse(offers[0].fetched_at)) / 1000) : -1 };
}

// ---------------- Catalog ----------------
function catalog(s: MockState, L: Locale, q: URLSearchParams) {
  state(s, L);
  const now = Date.now();
  const staleAfter = 6 * 3600;
  const apiFetched = ago(9 * MIN);
  const items: CatalogItem[] = s.offers.map((o: Offer) => {
    const live = ["hetzner", "vultr"].includes(o.provider);
    const fetched = live ? apiFetched : o.fetched_at;
    const ps = (s as S13)._v13!.prov[o.provider];
    const conn = !!s.providers.find((p) => p.id === o.provider)?.connected;
    const blocked = ps?.stats.breaker.state === "open" ? mt(L, "j_breaker", { provider: o.provider, reason: ps.stats.breaker.last_reason ?? "" }) : ps?.prefs.max_hourly_usd && o.price_hour > ps.prefs.max_hourly_usd ? mt(L, "j_cap", { provider: o.provider, price: o.price_hour, max: ps.prefs.max_hourly_usd }) : undefined;
    const age = Math.round((now - Date.parse(fetched)) / 1000);
    return {
      provider: o.provider, sku: o.sku, region: o.region, arch: o.sku.startsWith("cax") ? "arm64" : "amd64", cpu: o.cpu, ram_mb: o.ram_mb, disk_gb: o.disk_gb,
      price_hour: live && o.provider === "vultr" ? r4(o.price_hour * 1.04) : o.price_hour, price_month: o.price_month, min_billing_minutes: o.min_billing_minutes,
      price_source: live ? `${o.provider}-api` : "battleships", sources: live ? ["battleships", `${o.provider}-api`] : ["battleships"],
      feed_price_hour: live && o.provider === "vultr" ? o.price_hour : null, fetched_at: fetched, age_s: age, stale: age > staleAfter,
      confidence: live ? "verified" : o.confidence, health: ps?.stats.status ?? "unknown", usable: conn && !blocked, blocked,
    };
  });
  // netcup feed cũ (> 6 giờ)
  for (const it of items) if (it.provider === "netcup") { it.fetched_at = ago(9 * HOUR); it.age_s = 9 * 3600; it.stale = true; }
  const filt = items.filter((it) => (!q.get("provider") || it.provider === q.get("provider")) && (!q.get("region") || it.region === q.get("region")) && (!q.get("arch") || it.arch === q.get("arch"))
    && it.cpu >= Number(q.get("min_cpu") || 0) && it.ram_mb >= Number(q.get("min_ram_mb") || 0) && (!Number(q.get("max_price_hour")) || it.price_hour <= Number(q.get("max_price_hour"))));
  filt.sort((a, b) => a.price_hour - b.price_hour);
  const srcMap = new Map<string, { source: string; provider: string; count: number; fetched_at: string; age_s: number; stale: boolean }>();
  for (const it of items) for (const src of it.sources) {
    const k = `${src}|${it.provider}`;
    const fetched = src === "battleships" ? (it.provider === "netcup" ? ago(9 * HOUR) : ago(3 * HOUR)) : apiFetched;
    const age = Math.round((now - Date.parse(fetched)) / 1000);
    const e = srcMap.get(k) ?? { source: src, provider: it.provider, count: 0, fetched_at: fetched, age_s: age, stale: age > staleAfter };
    e.count++;
    srcMap.set(k, e);
  }
  return {
    items: filt, sources: [...srcMap.values()].sort((a, b) => (a.provider + a.source).localeCompare(b.provider + b.source)),
    capabilities: { hetzner: { arm64: true, ipv6: true, snapshots: true, hourly: true }, vultr: { arm64: false, ipv6: true, snapshots: true, hourly: true }, contabo: { hourly: false, monthly_only: true } },
    generated_at: iso(now), stale_after_s: staleAfter, cached: false, total: filt.length,
  };
}

// ---------------- Right-sizing ----------------
function rightsize(s: MockState, L: Locale, b: MockState["boxes"][number]): RightSize {
  const ageS = (Date.now() - Date.parse(b.created_at)) / 1000;
  const base = { box_id: b.id, slug: b.slug, spec: b.spec, savings_per_hour: 0, savings_per_month: 0 } as RightSize;
  const h = (n: number) => Math.round(n * 10) / 10;
  if (ageS < 15 * 60 || b.status === "provisioning") return { ...base, samples: Math.max(1, Math.floor(ageS / 60)), window_s: Math.round(ageS), cpu_p95_pct: 0, ram_p95_mb: 0, ram_max_mb: 0, idle_s: 0, suggestion: "insufficient_data", reasons: [mt(L, "rs_insufficient", { n: Math.max(1, Math.floor(ageS / 60)) })] };
  const win = Math.min(ageS, 24 * 3600);
  const samples = Math.round(win / 300);
  const cphPerCpu = b.cost_per_hour / b.spec.cpu;
  const presets: Record<string, Partial<RightSize>> = {
    box_7f3a91: { cpu_p95_pct: 142, ram_p95_mb: 3020, ram_max_mb: 3410, idle_s: 60, suggestion: "keep", reasons: [mt(L, "rs_keep")] },
    box_a9d054: { cpu_p95_pct: 61, ram_p95_mb: 2380, ram_max_mb: 2900, idle_s: 180, suggestion: "downsize", suggested_spec: { cpu: 2, ram_mb: 4096, disk_gb: b.spec.disk_gb } },
    box_c4410f: { cpu_p95_pct: 18, ram_p95_mb: 610, ram_max_mb: 820, idle_s: 30, suggestion: "downsize", suggested_spec: { cpu: 1, ram_mb: 2048, disk_gb: b.spec.disk_gb } },
    box_3f0c88: { cpu_p95_pct: 188, ram_p95_mb: 3880, ram_max_mb: 4060, idle_s: 20, suggestion: "upsize", suggested_spec: { cpu: b.spec.cpu, ram_mb: b.spec.ram_mb * 2, disk_gb: b.spec.disk_gb } },
    box_9a6d13: { cpu_p95_pct: 9, ram_p95_mb: 540, ram_max_mb: 700, idle_s: 300, suggestion: "keep", reasons: [mt(L, "rs_keep")] },
  };
  const p = presets[b.id] ?? (b.status === "sleeping" ? { cpu_p95_pct: 0, ram_p95_mb: 0, ram_max_mb: 0, idle_s: (Date.now() - Date.parse(b.last_active_at ?? b.created_at)) / 1000, suggestion: "stop" as const } : { cpu_p95_pct: 35, ram_p95_mb: b.spec.ram_mb * 0.5, ram_max_mb: b.spec.ram_mb * 0.6, idle_s: 900, suggestion: "sleep" as const });
  const r: RightSize = { ...base, samples, window_s: Math.round(win), cpu_p95_pct: 0, ram_p95_mb: 0, ram_max_mb: 0, idle_s: 0, suggestion: "keep", reasons: [], ...p } as RightSize;
  r.idle_s = Math.round(r.idle_s);
  if (r.suggested_spec) {
    const save = r4((b.spec.cpu - r.suggested_spec.cpu) * cphPerCpu);
    r.savings_per_hour = Math.max(0, save);
    r.savings_per_month = Math.round(r.savings_per_hour * 730 * 100) / 100;
    if (r.suggestion === "downsize") r.reasons = [mt(L, "rs_down", { cpu: r.cpu_p95_pct, ram: Math.round(r.ram_p95_mb), total: b.spec.ram_mb, h: h(win / 3600) })];
    if (r.suggestion === "upsize") r.reasons = [mt(L, "rs_up", { ram: Math.round(r.ram_max_mb), pct: Math.round((r.ram_max_mb / b.spec.ram_mb) * 100) })];
  }
  if (r.suggestion === "sleep") { r.savings_per_hour = r4(b.cost_per_hour * 0.6); r.savings_per_month = Math.round(r.savings_per_hour * 730 * 100) / 100; r.reasons = [mt(L, "rs_idle", { m: Math.round(r.idle_s / 60), h: h(win / 3600) })]; }
  if (r.suggestion === "stop") { r.savings_per_hour = r4(b.cost_per_hour * 0.1); r.savings_per_month = Math.round(r.savings_per_hour * 730 * 100) / 100; r.reasons = [mt(L, "rs_stop", { d: Math.max(1, Math.round(r.idle_s / 3600)) })]; }
  return r;
}

// ---------------- Warm pool + speed ----------------
function warmView(s: MockState, L: Locale): WarmView {
  const st = state(s, L).warm;
  const entries: WarmView["entries"] = [];
  const ready: Record<string, number> = {};
  if (st.enabled) {
    st.pools.forEach((p, i) => {
      const n = Math.min(p.max, Math.max(p.min, i === 0 ? 2 : 1));
      for (let k = 0; k < n; k++) {
        const starting = i === 0 && k === n - 1 && n > 1;
        entries.push({ id: `wrm_${i}${k}${p.template.length}`, host_id: k % 2 ? "hst_hel1_01" : "hst_fsn1_01", template: p.template, spec: p.spec, status: starting ? "starting" : "ready", created_at: ago((k + 2) * 4 * MIN), ready_at: starting ? null : ago((k + 2) * 4 * MIN - 9000), start_ms: 8200 + k * 600 });
        if (!starting) ready[p.template] = (ready[p.template] ?? 0) + 1;
      }
    });
  }
  const total = st.hits + st.misses;
  return { enabled: st.enabled, pools: st.pools, entries, ready, stats: { hits: st.hits, misses: st.misses, hit_rate: total ? Math.round((st.hits / total) * 1000) / 1000 : 0, avg_claim_ms: 1180, avg_cold_ms: 9400, created: 63, expired: 12, last_refill: ago(2 * MIN) } };
}
function speed(s: MockState, L: Locale) {
  const w = warmView(s, L);
  const drv: Record<string, { cow: string; vm: boolean; arch: string }> = { hst_fsn1_01: { cow: "zfs", vm: false, arch: "amd64" }, hst_hel1_01: { cow: "zfs", vm: true, arch: "amd64" }, hst_ctb_eu_01: { cow: "btrfs", vm: false, arch: "amd64" } };
  const hosts = s.hosts.filter((h) => h.status === "ready").map((h) => {
    const d = drv[h.id] ?? { cow: "copy", vm: false, arch: "amd64" };
    const features = [`cow:${d.cow}`, ...(d.vm ? ["vm"] : []), ...(d.cow !== "copy" ? ["cache"] : []), ...(h.billing === "pool" ? ["mesh"] : [])];
    return { host_id: h.id, provider: h.provider, region: h.region, driver: "incus", arch: d.arch, cow: d.cow, vm: d.vm, cache: d.cow !== "copy", mesh: h.billing === "pool", load_pct: h.ram_used_pct, boxes: s.boxes.filter((b) => b.host_id === h.id && b.status !== "terminated").length, cold_eta_s: d.cow === "copy" ? 21 : 8.4, features };
  });
  return { placement: "binpack", warm: w.stats, warm_enabled: w.enabled, warm_ready: Object.values(w.ready).reduce((a, b) => a + b, 0), avg_claim_ms: w.stats.avg_claim_ms, avg_cold_ms: w.stats.avg_cold_ms, speedup: Math.round((w.stats.avg_cold_ms / w.stats.avg_claim_ms) * 10) / 10, hosts,
    cache_env: ["PIP_INDEX_URL=http://10.10.0.1:3141/root/pypi/+simple/", "npm_config_registry=http://10.10.0.1:4873/", "GOPROXY=http://10.10.0.1:3000,direct", "APT proxy http://10.10.0.1:3142"], exec_compact: true };
}

// ---------------- Mesh ----------------
const MESH_URL = "https://mesh.boxa.example";
function meshNodes(s: MockState): MeshNode[] {
  const pool = s.hosts.filter((h) => h.billing === "pool");
  const nodes: MeshNode[] = pool.map((h, i) => ({ id: String(i + 1), name: h.id.replace(/_/g, "-"), givenName: h.id.replace(/_/g, "-"), ipAddresses: [`100.64.0.${i + 1}`, `fd7a:115c:a1e0::${i + 1}`], online: h.status === "ready", lastSeen: ago((i + 1) * 20 * 1000), forcedTags: ["tag:boxa-host"], validTags: [], user: { name: "boxa" } }));
  const svc = s.boxes.filter((b) => b.lifecycle === "service" && b.status === "running").slice(0, 2);
  svc.forEach((b, i) => nodes.push({ id: String(nodes.length + 1), name: b.slug, givenName: b.slug, ipAddresses: [`100.64.1.${i + 1}`], online: true, lastSeen: ago(40 * 1000), forcedTags: [`tag:box-${b.slug}`, `tag:agent-${b.agent_name}`], validTags: [], user: { name: "boxa" } }));
  nodes.push({ id: String(nodes.length + 1), name: "anh-macbook", givenName: "anh-macbook", ipAddresses: ["100.64.2.1"], online: false, lastSeen: ago(3 * HOUR), forcedTags: ["tag:admin"], validTags: [], user: { name: "boxa" } });
  return nodes;
}
function meshPolicy(s: MockState) {
  const svc = s.boxes.filter((b) => b.lifecycle === "service" && b.status !== "terminated");
  return {
    tagOwners: { "tag:boxa-host": ["boxa@"], "tag:admin": ["boxa@"], ...Object.fromEntries(svc.map((b) => [`tag:box-${b.slug}`, ["boxa@"]])) },
    acls: [
      { action: "accept", src: ["tag:admin"], dst: ["*:*"] },
      { action: "accept", src: ["tag:boxa-host"], dst: ["tag:boxa-host:*"] },
      ...svc.map((b) => ({ action: "accept", src: [`tag:agent-${b.agent_name}`], dst: [`tag:box-${b.slug}:${b.ports.length ? b.ports.join(",") : "*"}`] })),
    ],
  };
}

// ---------------- Router ----------------
export function handleV13(s: MockState, ctx: V13Ctx, M: string, segs: string[], q: URLSearchParams, body: Record<string, unknown>): Res | null {
  const { L, ok, list, err } = ctx;
  const [r0, r1, r2] = segs;
  const st = state(s, L);

  if (r0 === "profiles" && M === "GET") return ok({ items: PROFILES, kinds: KIND_PROFILES, optimize: ["balanced", "cost", "speed", "reliability", "latency"], isolation: ["any", "container", "vm", "sandbox"], lifetime: ["ephemeral", "persistent"] });
  if (r0 === "recommend" && M === "POST") {
    const rq = (body.requirements ?? {}) as Requirements;
    if ((rq.cpu ?? 0) < 0 || (rq.cpu ?? 0) > 64 || (rq.ram_mb ?? 0) < 0) return err(400, "INVALID", "bad_spec");
    if (rq.profile && !PROFILES.some((p) => p.id === rq.profile)) return err(400, "INVALID", "bad_profile", { profile: rq.profile });
    return ok(recommend(s, L, body as Parameters<typeof recommend>[2]));
  }
  if (r0 === "catalog" && M === "GET") return ok(catalog(s, L, q));
  if (r0 === "rightsize" && M === "GET") return list(s.boxes.filter((b) => ["running", "sleeping"].includes(b.status)).map((b) => rightsize(s, L, b)));
  if (r0 === "speed" && M === "GET") return ok(speed(s, L));
  if (r0 === "warm-pool") {
    if (M === "PUT") {
      const pools = (body.pools as WarmPoolSpec[] | undefined) ?? [];
      for (const p of pools) {
        if (!p.template || p.min < 0 || p.max < p.min || p.max > 10 || p.ttl_minutes < 5 || p.ttl_minutes > 1440) return err(400, "INVALID", "warm_bad", { template: p.template || "?" });
      }
      st.warm.enabled = !!body.enabled;
      st.warm.pools = pools.map((p) => ({ template: p.template, profile: p.profile || undefined, spec: p.spec ?? { cpu: 1, ram_mb: 1024, disk_gb: 10 }, min: Number(p.min), max: Number(p.max), ttl_minutes: Number(p.ttl_minutes) }));
      ctx.pushEvent({ actor: "user:anh", box_id: null, agent_id: null, type: "warm.updated", message: `Warm pool: ${st.warm.enabled ? "on" : "off"}, ${pools.length} pool`, cost_usd: null });
    }
    return ok(warmView(s, L));
  }
  if (r0 === "mesh") {
    if (!r1 && M === "GET") {
      if (!st.mesh.enabled) return ok({ enabled: false, nodes: [], online: 0, hosts_joined: 0, hosts: s.hosts.length, acl_rules: 0, derp: "", hint: mt(L, "mesh_off") });
      const nodes = meshNodes(s);
      return ok({ enabled: true, url: MESH_URL, user: "boxa", nodes, online: nodes.filter((n) => n.online).length, hosts_joined: nodes.filter((n) => n.forcedTags.includes("tag:boxa-host")).length, hosts: s.hosts.filter((h) => h.status !== "down").length, acl_rules: meshPolicy(s).acls.length, derp: "embedded (region 900 boxa)" });
    }
    if (r1 === "config" && M === "GET") {
      const config = `server_url: ${MESH_URL}\nlisten_addr: 0.0.0.0:8080\nmetrics_listen_addr: 127.0.0.1:9090\nprefixes:\n  v4: 100.64.0.0/10\n  v6: fd7a:115c:a1e0::/48\nderp:\n  server:\n    enabled: true\n    region_id: 900\n    region_code: boxa\n    stun_listen_addr: 0.0.0.0:3478\n  paths:\n    - /etc/headscale/derp.yaml\ndns:\n  magic_dns: true\n  base_domain: mesh.boxa.internal\npolicy:\n  mode: file\n  path: /etc/headscale/acl.json\n`;
      const derp = `regions:\n  900:\n    regionid: 900\n    regioncode: boxa\n    nodes:\n      - name: 900a\n        hostname: mesh.boxa.example\n        stunport: 3478\n`;
      return ok({ enabled: st.mesh.enabled, config, derp, acl: JSON.stringify(meshPolicy(s), null, 2), paths: { config: "/etc/headscale/config.yaml", derp: "/etc/headscale/derp.yaml", acl: "/etc/headscale/acl.json" } });
    }
    if (r1 === "acl" && M === "GET") return ok({ enabled: st.mesh.enabled, policy: meshPolicy(s), notes: s.boxes.some((b) => b.egress?.allow?.some((a) => a.startsWith("*."))) ? [mt(L, "mesh_note_wildcard")] : [] });
    if (r1 === "keys" && M === "POST") {
      if (!st.mesh.enabled) return err(400, "INVALID", "mesh_disabled");
      const ttl = Number(body.ttl_minutes) || 60;
      if (ttl < 1 || ttl > 10080) return err(400, "INVALID", "mesh_ttl");
      st.mesh.keys++;
      const box = body.box_id ? s.boxes.find((b) => b.id === body.box_id || b.slug === body.box_id) : undefined;
      if (body.box_id && !box) return err(404, "NOT_FOUND", "not_found", { what: `box ${body.box_id}` });
      const tags = box ? [`tag:box-${box.slug}`, `tag:agent-${box.agent_name}`] : ["tag:boxa-host"];
      const key = `hskey-auth-${Array.from(crypto.getRandomValues(new Uint8Array(12))).map((x) => x.toString(16).padStart(2, "0")).join("")}`;
      const hostname = box?.slug ?? (body.host_id as string) ?? "boxa-host";
      ctx.pushEvent({ actor: "user:anh", box_id: box?.id ?? null, agent_id: box?.agent_id ?? null, type: "mesh.key_created", message: `Mesh pre-auth key (${tags.join(",")})`, cost_usd: null });
      return ok({ key, tags, ephemeral: !body.reusable, expires_at: iso(Date.now() + ttl * MIN), login_server: MESH_URL, command: `tailscale up --login-server ${MESH_URL} --authkey ${key} --hostname ${hostname}` }, 201);
    }
  }

  if (r0 === "providers" && r1) {
    if (r1 === "health" && M === "GET") return list(s.providers.map((p) => healthRow(s, L, p.id, false)).filter(Boolean));
    const p = s.providers.find((x) => x.id === r1);
    const ps = st.prov[r1];
    if (!p || !ps) return null;
    if (r2 === "health" && M === "GET") return ok(healthRow(s, L, r1, true));
    if (r2 === "check" && M === "POST") {
      p.last_check_at = iso(Date.now());
      if (p.connected) {
        ps.stats.samples++;
        ps.stats.last_ok_at = iso(Date.now());
        if (ps.stats.breaker.state === "half_open") { ps.stats.breaker = { state: "closed", consecutive_failures: 0, opens: 0 }; ps.stats.status = "healthy"; }
      }
      return ok(healthRow(s, L, r1, true));
    }
    if (r2 === "reset" && M === "POST") {
      ps.stats.breaker = { state: "closed", consecutive_failures: 0, opens: 0 };
      if (ps.stats.status === "down" && p.connected) ps.stats.status = "degraded";
      ctx.pushEvent({ actor: "user:anh", box_id: null, agent_id: null, type: "provider.breaker_reset", message: `${r1} circuit breaker reset`, cost_usd: null });
      return ok(healthRow(s, L, r1, true));
    }
    if (r2 === "prefs") {
      if (M === "PUT") {
        const n = { ...ps.prefs, ...(body as Partial<ProviderPrefs>) };
        if (n.priority < 0 || n.max_hourly_usd < 0 || n.max_monthly_usd < 0 || n.max_servers < 0) return err(400, "INVALID", "prefs_bad");
        n.regions = (n.regions ?? []).filter(Boolean);
        if (n.default_region && n.regions.length && !n.regions.includes(n.default_region)) return err(400, "INVALID", "prefs_region", { region: n.default_region });
        n.updated_at = iso(Date.now());
        ps.prefs = n;
        ctx.pushEvent({ actor: "user:anh", box_id: null, agent_id: null, type: "provider.prefs", message: `${r1} prefs updated`, cost_usd: null });
      }
      return ok(ps.prefs);
    }
    if (r2 === "validate" && M === "POST") {
      const token = String(body.token ?? "").trim();
      if (!token && !p.connected && p.id !== "local") return err(400, "INVALID", "token_required");
      const t0 = 140 + (token.length % 7) * 31;
      if (token && (token.length < 16 || /sai|invalid|wrong/i.test(token)))
        return ok({ provider: p.id, ok: false, error: mt(L, "bad_token"), error_class: "auth", latency_ms: t0, regions: [], sizes: [], size_count: 0, hint: mt(L, "bad_token_hint") });
      const sizes = s.offers.filter((o) => o.provider === p.id).sort((a, b) => a.price_hour - b.price_hour);
      const extra: Offer[] = sizes.length ? [] : [{ provider: p.id, sku: "s-1vcpu-1gb", region: (ctx.regions[p.id] ?? [{ id: "sgp1" }])[0].id, cpu: 1, ram_mb: 1024, disk_gb: 25, price_hour: 0.00893, price_month: 6, min_billing_minutes: 60, source: `${p.id}-api`, fetched_at: iso(Date.now()), confidence: "verified" }, { provider: p.id, sku: "s-2vcpu-4gb", region: (ctx.regions[p.id] ?? [{ id: "sgp1" }])[0].id, cpu: 2, ram_mb: 4096, disk_gb: 80, price_hour: 0.03571, price_month: 24, min_billing_minutes: 60, source: `${p.id}-api`, fetched_at: iso(Date.now()), confidence: "verified" }];
      const all = [...sizes, ...extra];
      return ok({ provider: p.id, ok: true, latency_ms: t0, regions: ctx.regions[p.id] ?? [], sizes: all, size_count: all.length * 3 });
    }
    return null;
  }

  if (r0 === "boxes") {
    if (r1 === "ensure" && M === "POST") {
      const name = String(body.name ?? body.slug ?? "").trim();
      if (!name) return err(400, "INVALID", "ensure_name");
      const b = s.boxes.find((x) => (x.name === name || x.slug === name) && !["terminated", "failed"].includes(x.status));
      if (b) {
        let action = "reused";
        if (b.status === "sleeping") { b.status = "running"; b.last_active_at = iso(Date.now()); action = "woken"; }
        return ok({ box: ctx.hydrateBox(b), action, created: false, next: [`box_exec(box_id="${b.id}", command=...)`] });
      }
      const res = ctx.createBox({ ...body, slug: name });
      if (res.status >= 300) return res;
      const created = res.body as Box;
      const nb = s.boxes.find((x) => x.id === created.id);
      if (nb) nb.name = name;
      return ok({ box: { ...created, name }, action: "created", created: true, next: created.next }, 201);
    }
    const b = s.boxes.find((x) => x.id === r1 || x.slug === r1);
    if (!b) return null;
    if (r2 === "rightsize" && M === "GET") return ok(rightsize(s, L, b));
    if (r2 === "resume" && M === "GET") {
      let woke = false;
      if (b.status === "sleeping") { b.status = "running"; b.last_active_at = iso(Date.now()); woke = true; ctx.pushEvent({ actor: "user:anh", box_id: b.id, agent_id: b.agent_id, type: "box.woke", message: `Wake ${b.slug} (resume)`, cost_usd: null }); }
      const execs = s.events.filter((e) => e.box_id === b.id && e.type === "box.exec").sort((a, z) => Date.parse(z.ts) - Date.parse(a.ts)).slice(0, 5)
        .map((e) => ({ ts: e.ts, command: (e.data?.command as string) ?? e.message.replace(/^[^:]*: /, "").replace(/ tokens_saved=.*$/, ""), exit_code: (e.data?.exit_code as number) ?? (/exit=(\d+)/.test(e.message) ? Number(/exit=(\d+)/.exec(e.message)![1]) : null), log_id: (e.data?.log_id as string) ?? e.log_id ?? null }));
      const jobs = s.jobs.filter((j) => j.box_id === b.id && ["queued", "running"].includes(j.status));
      const snaps = s.snapshots.filter((x) => x.box_id === b.id).sort((a, z) => Date.parse(z.created_at) - Date.parse(a.created_at));
      const files = Object.keys(s.files[b.id] ?? {}).slice(0, 6);
      return ok({ box: ctx.hydrateBox(b), woke, idle_seconds: Math.round((Date.now() - Date.parse(b.last_active_at ?? b.created_at)) / 1000), recent_execs: execs, running_jobs: jobs, last_snapshot: snaps[0] ?? null, files_changed: files, listening_ports: b.ports,
        next: [`box_exec(box_id="${b.id}", command=...)`, ...(snaps[0] ? [`box_restore(box_id="${b.id}", snapshot_id="${snaps[0].id}")  # roll back`] : []), `box_observe(box_id="${b.id}")`] });
    }
    if (r2 === "clone" && M === "POST") {
      if (!["running", "sleeping"].includes(b.status)) return err(409, "INVALID", "clone_state", { status: b.status });
      const id = ctx.nextId("box");
      const slug = String(body.slug || `${b.slug}-clone-${Math.random().toString(36).slice(2, 5)}`).toLowerCase().replace(/[^a-z0-9-]/g, "-");
      if (s.boxes.some((x) => x.slug === slug && x.status !== "terminated")) return err(409, "INVALID", "slug_exists", { slug });
      const nb: MockState["boxes"][number] = { ...b, id, slug, status: "provisioning", url: null, created_at: iso(Date.now()), last_active_at: null, cloned_from: b.id, name: undefined, _ready_at: Date.now() + 2500,
        estimate: { ...estimateFor("clone", b.cost_per_hour, b.lifecycle), score: undefined }, placement_reason: mt(L, "clone_reason", { slug: b.slug, host: b.host_id ?? "?" }) };
      delete (nb as Partial<typeof nb>)._taken_over;
      s.boxes.push(nb);
      s.files[id] = { ...(s.files[b.id] ?? {}) };
      ctx.pushEvent({ actor: "user:anh", box_id: id, agent_id: b.agent_id, type: "box.cloned", message: `Clone ${b.slug} → ${slug} (copy-on-write)`, cost_usd: null });
      return ok({ ...ctx.hydrateBox(nb), next: [`box_exec(box_id="${id}", command=...)`, `box_down(box_id="${id}")`] }, 201);
    }
  }
  return null;
}

