// MOCK MODE v1.4: provider Sandbox API (T2 dial-in / T3 shim). Bám theo boxa/core/internal/manager/sandbox14.go,
// selector/score.go (loại theo capability) và api/v14.go (lint adapter YAML).
import { parse as parseYaml } from "yaml";
import type { AdapterLintResult, CatalogItem, Offer, Provider, ProviderPrefs, RecommendOption, Rejection, Requirements, SandboxInfo, SandboxProvider } from "../types";
import type { Locale } from "@/i18n/config";
import { mt } from "./i18n";
import { ago, iso, r4, MIN, type MockState } from "./seed";
import { SANDBOX_INFO } from "./sandbox-data";

type Opt = Omit<RecommendOption, "rank" | "score" | "breakdown" | "reasons"> & { reasons: string[] };

export const sandboxInfo = (id: string) => SANDBOX_INFO.find((i) => i.id === id);
export const isSandbox = (id: string) => SANDBOX_INFO.some((i) => i.id === id);

/** Giá/giờ của một size (giống sandbox.Pricing.PerHour). */
export function perHour(info: Pick<SandboxInfo, "pricing">, cpu: number, ramMb: number, diskGb: number): number {
  const p = info.pricing;
  return r4((p.cpu_second * cpu + p.gb_second * (ramMb / 1024) + p.disk_gb_second * diskGb + p.flat_second) * 3600);
}

/** Chế độ hiệu lực: dial (T2, boxd dial-in) | shim (T3). Mock coi như BOXA_PUBLIC_URL đã đặt. */
export function sandboxMode(info: SandboxInfo, prefs?: Partial<ProviderPrefs>): "dial" | "shim" {
  const m = (prefs?.sandbox_mode ?? "auto").toLowerCase();
  if (m === "dial" || m === "shim") return m;
  return info.tier === "T2" && info.capabilities.background ? "dial" : "shim";
}

/** Provider row cho GET /providers (registry v1.4: group/adapter/verification/docs). */
export function sandboxProviderRows(): Provider[] {
  const conn: Record<string, string> = { e2b: "api", prized: "env", daytona: "api", "upstash-box": "api" };
  return SANDBOX_INFO.map((i, n) => ({
    id: i.id, name: i.name, connected: !!conn[i.id], tier: i.tier as Provider["tier"], last_check_at: conn[i.id] ? ago((8 + n) * MIN) : null, error: null, implemented: true,
    chain_rank: 6 + n, token_source: conn[i.id] as Provider["token_source"], group: "sandbox", adapter: i.adapter, verification: i.verification, docs: i.docs,
  }));
}

type PrefsOf = (id: string) => ProviderPrefs | undefined;
type HealthOf = (id: string) => string;

export function sandboxViews(s: MockState, prefsOf: PrefsOf, healthOf: HealthOf, L: Locale): SandboxProvider[] {
  const out = SANDBOX_INFO.map((info) => {
    const p = s.providers.find((x) => x.id === info.id);
    const pp = prefsOf(info.id);
    const connected = !!p?.connected;
    const blocked = !connected ? mt(L, "b_notconn") : pp && !pp.enabled ? mt(L, "j_disabled", { provider: info.id }) : healthOf(info.id) === "down" ? mt(L, "j_breaker", { provider: info.id, reason: "server" }) : "";
    return { ...info, mode: sandboxMode(info, pp), connected, usable: !blocked, blocked: blocked || undefined, health: healthOf(info.id), chain_rank: p?.chain_rank } as SandboxProvider;
  });
  return out.sort((a, b) => (a.adapter !== b.adapter ? (a.adapter === "native" ? -1 : 1) : a.from_usd_h - b.from_usd_h));
}

/** Catalog: mỗi size × region của provider sandbox, tính theo giây. */
export function sandboxCatalog(s: MockState, prefsOf: PrefsOf, healthOf: HealthOf, L: Locale): CatalogItem[] {
  const fetched = ago(2 * MIN);
  const items: CatalogItem[] = [];
  for (const info of SANDBOX_INFO) {
    const connected = !!s.providers.find((x) => x.id === info.id)?.connected;
    const pp = prefsOf(info.id);
    for (const z of info.sizes) for (const r of info.regions) {
      const ph = perHour(info, z.cpu, z.ram_mb, z.disk_gb);
      const blocked = pp && !pp.enabled ? mt(L, "j_disabled", { provider: info.id }) : pp?.max_hourly_usd && ph > pp.max_hourly_usd ? mt(L, "j_cap", { provider: info.id, price: ph, max: pp.max_hourly_usd }) : undefined;
      items.push({
        provider: info.id, sku: z.sku, region: r.id, arch: "amd64", cpu: z.cpu, ram_mb: z.ram_mb, disk_gb: z.disk_gb, price_hour: ph, price_month: r4(ph * 730), min_billing_minutes: 0,
        price_source: "sandbox-pricing", sources: ["sandbox-pricing"], feed_price_hour: null, fetched_at: fetched, age_s: 120, stale: false,
        confidence: info.pricing.confidence === "docs" ? "verified" : "estimated", health: healthOf(info.id), usable: connected && !blocked, blocked, tier: info.tier, group: "sandbox",
      });
    }
  }
  return items;
}

/** Phương án sandbox cho /recommend + phương án bị loại theo capability (selector/score.go). */
export function sandboxOptions(s: MockState, L: Locale, req: Requirements, hours: number, prefsOf: PrefsOf, healthOf: HealthOf, allowed?: string[]): { opts: Opt[]; rej: Rejection[] } {
  const opts: Opt[] = [];
  const rej: Rejection[] = [];
  const cpu = req.cpu ?? 1, ram = req.ram_mb ?? 1024;
  for (const info of SANDBOX_INFO) {
    const c = info.capabilities;
    const pp = prefsOf(info.id);
    const connected = !!s.providers.find((x) => x.id === info.id)?.connected;
    const z = [...info.sizes].sort((a, b) => perHour(info, a.cpu, a.ram_mb, a.disk_gb) - perHour(info, b.cpu, b.ram_mb, b.disk_gb)).find((x) => x.cpu >= cpu && x.ram_mb >= ram) ?? info.sizes[info.sizes.length - 1];
    const region = info.regions[0]?.id ?? "global";
    const r = { kind: "sandbox", provider: info.id, region, sku: z.sku };
    const strong = ["microvm", "vm", "gvisor"].includes(c.isolation);
    const maxH = c.max_lifetime_s > 0 ? r4(c.max_lifetime_s / 3600) : 0;
    const why =
      allowed && !allowed.includes(info.id) ? mt(L, "j_policy", { provider: info.id })
      : pp && !pp.enabled ? mt(L, "j_disabled", { provider: info.id })
      : (req.gpu ?? 0) > 0 ? mt(L, "j_gpu")
      : z.cpu < cpu || z.ram_mb < ram ? mt(L, "j_small", { cpu: z.cpu, ram: z.ram_mb })
      : req.untrusted && !strong ? mt(L, "j_sbx_shared")
      : req.isolation === "vm" ? mt(L, "j_isolation", { iso: "vm" })
      : req.isolation === "container" ? mt(L, "j_isolation", { iso: "container" })
      : req.lifetime === "persistent" && !c.pause ? mt(L, "j_sbx_pause")
      : maxH > 0 && (req.duration_hours ?? 0) > maxH ? mt(L, "j_sbx_life", { max: Math.round(maxH * 10) / 10, need: req.duration_hours ?? 0 })
      : req.public_url && !c.ports ? mt(L, "j_sbx_ports")
      : req.fork && !c.fork ? mt(L, "j_fork")
      : "";
    if (why) { rej.push({ ...r, reason: why }); continue; }
    const mode = sandboxMode(info, pp);
    const tier = mode === "shim" ? "T3" : info.tier;
    const eta = Math.round(((c.boot_ms || 2000) / 1000 + (mode === "dial" ? 6 : 1.5)) * 10) / 10;
    const reasons = [mt(L, "r_sandbox", { name: info.name, iso: c.isolation, tier }) + (tier === "T3" ? `; ${mt(L, "r_sandbox_t3")}` : "")];
    if (req.lifetime !== "persistent" && hours <= 24) reasons.push(mt(L, "r_persec"));
    if (req.untrusted && strong) reasons.push(mt(L, "r_strong", { iso: c.isolation }));
    if (info.pricing.confidence !== "docs") reasons.push(mt(L, "r_sbx_est"));
    opts.push({
      kind: "sandbox", provider: info.id, region, sku: z.sku, arch: "amd64", isolation: "sandbox", cpu: z.cpu, ram_mb: z.ram_mb, disk_gb: z.disk_gb,
      cost_per_hour: perHour(info, z.cpu, z.ram_mb, z.disk_gb), expected_total_usd: 0, eta_seconds: eta, reliability: connected ? 0.97 : 0.9, health: healthOf(info.id),
      headroom: 1, latency_ms: 180, min_billing_h: 0, requires_approval: !connected, price_stale: false, reasons,
      tier, fork: c.fork, pause: c.pause, ports: c.ports, gpu: c.gpu, max_lifetime_h: maxH || undefined, boot_ms: c.boot_ms, verification: info.verification, strong_isolation: strong,
    });
  }
  return { opts, rej };
}

/** Kiểm tra token sandbox (một hoặc nhiều trường, JSON {"token":…, "org":…}). */
export function sandboxValidate(info: SandboxInfo, token: string, L: Locale) {
  let cred: Record<string, string> = {};
  const t = token.trim();
  if (t.startsWith("{")) { try { cred = JSON.parse(t); } catch { cred = { token: t }; } } else cred = { token: t };
  const latency = 120 + (t.length % 9) * 23;
  const missing = info.credentials.filter((f) => !f.optional && !String(cred[f.name] ?? "").trim()).map((f) => f.label);
  if (missing.length) return { provider: info.id, ok: false, error: mt(L, "tok_fields", { fields: missing.join(", ") }), error_class: "auth", latency_ms: latency, regions: [], sizes: [], size_count: 0, hint: mt(L, "bad_token_hint") };
  const key = String(cred.token ?? "");
  if (key.length < 12 || /sai|invalid|wrong/i.test(key)) return { provider: info.id, ok: false, error: mt(L, "bad_token"), error_class: "auth", latency_ms: latency, regions: [], sizes: [], size_count: 0, hint: mt(L, "bad_token_hint") };
  const now = iso(Date.now());
  const sizes: Offer[] = info.sizes.flatMap((z) => info.regions.slice(0, 1).map((r) => {
    const ph = perHour(info, z.cpu, z.ram_mb, z.disk_gb);
    return { provider: info.id, sku: z.sku, region: r.id, cpu: z.cpu, ram_mb: z.ram_mb, disk_gb: z.disk_gb, price_hour: ph, price_month: r4(ph * 730), min_billing_minutes: 0, source: "sandbox-pricing", fetched_at: now, confidence: info.pricing.confidence === "docs" ? "verified" : "estimated" } as Offer;
  }));
  return { provider: info.id, ok: true, latency_ms: latency, regions: info.regions.map((r) => ({ id: r.id, name: r.name, available: true })), sizes, size_count: info.sizes.length * info.regions.length };
}

// ---------------- Lint adapter YAML (api/v14.go → sandbox.ParseYAML) ----------------
const REQUIRED = ["base_url", "create", "delete", "exec", "get", "name", "validate"];

export function lintAdapter(src: string): AdapterLintResult {
  let doc: unknown;
  try {
    doc = parseYaml(src);
  } catch (e) {
    return { ok: false, error: `yaml: ${String((e as Error).message).split("\n")[0]}` };
  }
  if (!doc || typeof doc !== "object" || Array.isArray(doc)) return { ok: false, error: "adapter: expected a YAML mapping" };
  const d = doc as Record<string, unknown>;
  const id = String(d.id ?? "").trim();
  if (!id) return { ok: false, error: "adapter: missing id" };
  if (!/^[a-z0-9][a-z0-9-]*$/.test(id)) return { ok: false, error: `adapter "${id}": id must be lowercase letters, digits and dashes` };
  const missing = REQUIRED.filter((k) => d[k] === undefined || d[k] === null || d[k] === "").sort();
  if (missing.length) return { ok: false, error: `adapter "${id}": missing ${missing.join(", ")}` };
  const tier = String(d.tier ?? "T2");
  if (!["T2", "T3"].includes(tier)) return { ok: false, error: `adapter "${id}": tier must be T2 or T3` };
  if (!/^https?:\/\//.test(String(d.base_url))) return { ok: false, error: `adapter "${id}": base_url must start with http:// or https://` };
  const caps = (d.capabilities ?? {}) as Partial<SandboxInfo["capabilities"]>;
  const pr = (d.pricing ?? {}) as Partial<SandboxInfo["pricing"]>;
  const sizes = (Array.isArray(d.sizes) ? d.sizes : []) as SandboxInfo["sizes"];
  const info: SandboxInfo = {
    id, name: String(d.name), tier, adapter: "yaml", verification: String(d.verification ?? "docs-verified"), verified_note: d.verified_note ? String(d.verified_note) : undefined,
    docs: String(d.docs ?? ""), website: String(d.website ?? ""), initials: String(d.initials ?? id.slice(0, 2).toUpperCase()), color: String(d.color ?? "#64748b"),
    capabilities: { background: false, pause: false, snapshot: false, fork: false, ports: false, pty: false, files: false, gpu: false, boot_ms: 0, max_lifetime_s: 0, max_cpu: 0, max_ram_mb: 0, isolation: "container", ...caps },
    pricing: { cpu_second: 0, gb_second: 0, disk_gb_second: 0, flat_second: 0, source: "", confidence: "estimated", ...pr },
    sizes, regions: (Array.isArray(d.regions) ? d.regions : [{ id: "global", name: String(d.name) }]) as SandboxInfo["regions"],
    templates: (Array.isArray(d.templates) ? d.templates : []) as string[], default_template: String(d.default_template ?? ""), workdir: String(d.workdir ?? "/tmp/boxa"),
    credentials: ((Array.isArray(d.credentials) ? d.credentials : [{ name: "token", label: "API key", secret: true }]) as SandboxInfo["credentials"]).map((c) => ({ ...c, optional: !!c.optional })),
    env_vars: (Array.isArray(d.env_vars) ? d.env_vars : []) as string[], notes: [], from_usd_h: 0,
  };
  info.from_usd_h = sizes.length ? Math.min(...sizes.map((z) => perHour(info, z.cpu, z.ram_mb, z.disk_gb))) : 0;
  return { ok: true, error: null, info };
}
