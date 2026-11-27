/**
 * @boxmes/sdk — TypeScript client for Boxmes (cheap disposable Linux boxes for AI agents).
 * Zero dependencies: uses the global fetch (Node ≥ 18, Deno, Bun, browsers, edge runtimes).
 *
 *   import { Boxmes } from "@boxmes/sdk";
 *   const bx = new Boxmes();                // BOXMES_URL / BOXMES_API_KEY / BOXMES_LANG from env
 *   const box = await bx.createBox({ template: "python", lifecycle: "task", ttl_seconds: 600 });
 *   const r = await bx.exec(box.id, "python3 -c 'print(42)'");
 */

export type Lifecycle = "task" | "session" | "service";
export type BoxStatus = "requested" | "placing" | "provisioning" | "running" | "sleeping" | "snapshotting" | "terminated" | "failed";
export type Locale = "en" | "vi" | "zh" | "ja";
export type ToolFormat = "openai" | "anthropic" | "gemini" | "mcp";

export interface Spec { cpu?: number; ram_mb?: number; disk_gb?: number }

export interface Box {
  id: string; slug: string; agent_id: string; agent_name: string; status: BoxStatus; lifecycle: Lifecycle;
  template: string; spec: Required<Spec>; provider: string; region: string; host_id: string; url: string;
  ttl_seconds: number; idle_minutes: number; budget_usd: number; spent_usd: number; cost_per_hour: number;
  placement_reason: string; ports: number[]; secrets: string[]; created_at: string; last_active_at: string;
  forked_from?: string; locale?: string; error?: string; next?: string[];
  /** v1.3 */
  profile?: string; requirements?: Requirements; isolation?: string; estimate?: BoxEstimate; cloned_from?: string; name?: string;
  /** v1.7 */
  app?: AppSpec; wake_on_request?: boolean; port_access?: Record<string, PortAccess>;
  /** v1.9: readable gateway name (https://<subdomain>.<domain>/) and port labels (<subdomain>-<label>) */
  subdomain?: string; port_labels?: Record<string, string>;
  /** v2.0: owning workspace and where the box runs (absent until placed) */
  workspace_id?: string; location?: BoxLocation;
}

export interface ExecResult {
  exit_code: number; duration_ms: number; stdout_tail: string; stderr_tail: string; truncated: boolean;
  log_id: string | null; files_changed: string[]; tokens_saved: number; next?: string[];
}

export interface JobRef { job_id: string; next?: string[] }

export interface Job {
  id: string; box_id: string; command: string; status: "queued" | "running" | "done" | "failed";
  result: ExecResult | null; created_at: string; finished_at: string | null;
}

export interface BatchItem extends ExecResult { index: number; command: string }
export interface BatchResult {
  ok: boolean; results: BatchItem[]; completed: number; stopped_at: number | null; duration_ms: number;
  tokens_saved: number; next?: string[];
}

export interface QuoteOption {
  kind: "pool" | "burst"; provider: string; region: string; host_id?: string; sku?: string;
  cost_per_hour: number; expected_total_usd: number; requires_approval: boolean; label: string;
}
export interface Quote {
  available: boolean; provider: string; region: string; host_id?: string; billing: string; cost_per_hour: number;
  expected_hours: number; expected_total_usd: number; placement_reason: string; alternatives: QuoteOption[];
  spec: Required<Spec>; lifecycle: Lifecycle; template: string; within_budget: boolean;
  budget_error?: ApiErrorBody; next?: string[];
}

export interface Snapshot { id: string; box_id: string; size_mb: number; created_at: string; location: string }
export interface Template { name: string; description: string; yaml: string; uses: number; created_at: string; builtin?: boolean }
export interface AgentKind {
  id: string; name: string; category: "coding" | "framework" | "workflow" | "platform" | "custom";
  transports: string[]; doc_url: string | null; language: string;
}
export interface Snippet { kind: string; language: string; snippet: string; file?: string; notes: string[]; locale: string }
export interface BoxmesEvent {
  id: string; ts: string; actor: string; box_id: string | null; agent_id: string | null; type: string;
  message: string; cost_usd: number | null; data?: Record<string, unknown>;
}
export interface Agent { id: string; name: string; kind: string; budget_day_usd: number; budget_month_usd: number; locale?: string; [k: string]: unknown }
export interface ApiKey {
  id: string; agent_id: string | null; name: string; prefix: string; scopes: string[]; key?: string;
  /** v2.0 */
  workspace_id?: string; kind?: "platform" | "workspace" | "agent"; expires_at?: string | null; created_by?: string;
  created_at?: string; last_used_at?: string | null; revoked_at?: string | null;
}
/** v2.0 workspace key scopes */
export type KeyScope = "boxes:read" | "boxes:write" | "exec" | "secrets:read" | "secrets:write" | "billing:read" | "admin" | (string & {});
export interface List<T> { items: T[]; next_cursor: string | null }

// ---- v2.2 infra: provider accounts ----
export type AccountStrategy = "priority" | "weighted" | "round_robin" | "least_used" | "drain_first";
export type AccountStatus = "active" | "cooling" | "rate_limited" | "quota_full" | "low_balance" | "out_of_credit" | "auth_failed" | "token_expiring" | "disabled" | "draining" | "archived";
export interface AccountLimit { limit: number; used: number; source?: "manual" | "learned" | "api"; at?: string }
export interface AccountStats {
  hosts_live: number; boxes_live: number; vcpu_live: number; spend_today: number; spend_month: number; spend_today_usd: number; spend_month_usd: number;
  burn_per_hour_usd: number; balance_effective?: number; balance_effective_usd?: number; runway_hours?: number; runway_days?: number;
  forecast_month_end: number; eligible: boolean; ineligible?: string; breaker: "closed" | "open" | "half_open"; has_credential: boolean;
}
export interface ProviderAccount {
  id: string; provider: string; label: string; external_id?: string; owner_workspace_id?: string; tags: string[]; owner?: string; notes?: string; console_url?: string;
  is_default: boolean; credential_ref: string; credential_source: "api" | "env"; key_fingerprint?: string; billing_key_fingerprint?: string; scopes: string[];
  token_expires_at?: string; refresh_expires_at?: string; rotated_at?: string; rotate_every_days?: number;
  enabled: boolean; priority: number; weight: number; regions: string[]; pool?: string; drain: boolean; auto_drained: boolean;
  status: AccountStatus; status_reason?: string; status_since?: string; cooldown_until?: string; error_streak: number; last_error_class?: string; last_error?: string; last_ok_at?: string; last_check_at?: string;
  billing_model?: "prepaid" | "postpaid" | "contract" | "free_tier"; currency: string; fx_rate_usd?: number; balance?: number; balance_source?: "manual" | "api" | "estimated"; balance_at?: string;
  credit_total?: number; credit_remaining?: number; credit_expires_at?: string; pending_charges?: number; month_to_date?: number;
  spend_cap_day: number; spend_cap_month: number; min_balance: number; low_balance_warn: number;
  billing_cycle?: string; invoice_day?: number; next_invoice_at?: string; trial_ends_at?: string; contract_ends_at?: string;
  limits: Record<string, AccountLimit>; thresholds: Partial<Record<"runway_warn_days" | "runway_critical_days" | "limit_warn_pct" | "limit_critical_pct" | "expiry_warn_days" | "expiry_critical_days", number>>;
  created_at: string; created_by?: string; updated_at: string; updated_by?: string; stats?: AccountStats; warnings?: string[];
}
/** POST / PATCH body (merge-patch; dates RFC 3339 or YYYY-MM-DD, "" clears; limits value 0 removes). */
export interface ProviderAccountInput {
  label?: string; billing_token?: string; skip_validate?: boolean; external_id?: string; tags?: string[]; owner?: string; notes?: string; console_url?: string; scopes?: string[];
  enabled?: boolean; priority?: number; weight?: number; regions?: string[]; pool?: string; drain?: boolean;
  billing_model?: string; currency?: string; fx_rate_usd?: number; balance?: number; clear_balance?: boolean; credit_total?: number; credit_remaining?: number;
  pending_charges?: number; month_to_date?: number; spend_cap_day?: number; spend_cap_month?: number; min_balance?: number; low_balance_warn?: number;
  billing_cycle?: string; invoice_day?: number; rotate_every_days?: number;
  token_expires_at?: string; refresh_expires_at?: string; credit_expires_at?: string; trial_ends_at?: string; contract_ends_at?: string; next_invoice_at?: string;
  limits?: Record<string, number>; thresholds?: ProviderAccount["thresholds"];
}
export interface AccountTestResult { ok: boolean; error?: string; error_class?: string; latency_ms: number; key_fingerprint?: string; duplicates?: string[]; account?: ProviderAccount }
export interface ProviderStrategy { provider: string; strategy: AccountStrategy | ""; effective_strategy: AccountStrategy; sticky_minutes: number; failover_across_accounts: boolean; strategies: AccountStrategy[] }
export interface InfraAlert {
  id: string; type: string; level: "info" | "warn" | "critical"; status: "open" | "acked" | "closed"; provider?: string; account_id?: string; host_id?: string;
  message: string; data?: { args?: unknown[] }; count: number; opened_at: string; updated_at: string; closed_at?: string; acked_at?: string; acked_by?: string; notified_at?: string; notified_level?: string;
}
export interface AdminProviderRow {
  id: string; name: string; tier: string; group: "vps" | "sandbox"; connected: boolean; implemented: boolean; health: string; strategy: AccountStrategy;
  accounts_total: number; accounts_healthy: number; accounts_degraded: number; hosts_live: number; boxes_live: number;
  balance_usd?: number; balance_by_currency?: Record<string, number>; spend_today_usd: number; spend_month_usd: number; forecast_month_usd: number;
  runway_days?: number; max_limit_pct: number; open_alerts: number;
}
export interface InfraSummary {
  balance_usd: number; balance_by_currency: Record<string, number>; spend_today_usd: number; spend_month_usd: number; forecast_month_usd: number;
  shortest_runway?: { provider: string; account_id: string; label: string; days: number }; accounts_healthy: number; accounts_degraded: number; accounts_broken: number; open_alerts: number;
}
export interface FileRead { path: string; type: "file" | "dir"; content?: string; total_lines?: number; entries?: { name: string; type: string; size: number }[] }

export interface ApiErrorBody { code: string; message: string; hint: string; retry_after_s?: number; docs_url?: string; /** v2.0, e.g. QUOTA_EXCEEDED {quota, limit, used, plan, upgrade_url} */ details?: Record<string, unknown>; next?: string[] }

/** v1.3 declarative needs (POST /recommend, POST /boxes {requirements}). */
export interface Requirements {
  cpu?: number; ram_mb?: number; disk_gb?: number; gpu?: number; arch?: "any" | "amd64" | "arm64"; region?: string;
  location?: string; max_latency_ms?: number; max_price_hour?: number; lifetime?: "ephemeral" | "persistent";
  duration_hours?: number; isolation?: "any" | "container" | "vm" | "sandbox"; profile?: string;
  optimize?: "balanced" | "cost" | "speed" | "reliability" | "latency"; public_url?: boolean; untrusted?: boolean;
  /** v1.4: the workload forks/clones the box (keeps only fork-capable options). */
  fork?: boolean;
}

/** v1.4 sandbox API provider (GET /sandbox/providers). */
export interface SandboxCapabilities {
  background: boolean; pause: boolean; snapshot: boolean; fork: boolean; ports: boolean; pty: boolean; files: boolean; gpu: boolean;
  boot_ms: number; max_lifetime_s: number; max_cpu: number; max_ram_mb: number; isolation: "microvm" | "gvisor" | "container" | "vm" | string;
}
export interface SandboxInfo {
  id: string; name: string; tier: "T2" | "T3"; adapter: "native" | "yaml"; verification: "live-verified" | "docs-verified" | "sdk-source" | string;
  verified_note?: string; docs: string; website: string; initials: string; color: string; capabilities: SandboxCapabilities;
  pricing: { cpu_second: number; gb_second: number; disk_gb_second: number; flat_second: number; source: string; confidence: "docs" | "estimated" | string; note?: string };
  /** fixed_price sizes (Render plans) cost price_hour USD/h flat, prorated per second, instead of the per-second rates. */
  sizes: { sku: string; cpu: number; ram_mb: number; disk_gb: number; fixed_price?: boolean; price_hour?: number; note?: string }[]; regions: { id: string; name: string }[];
  templates: string[]; default_template: string; workdir: string;
  credentials: { name: string; label: string; secret: boolean; optional: boolean; default?: string; hint?: string }[];
  env_vars: string[]; notes?: string[] | null;
  /** No exec API (Render): always T2 dial-in; boxd is the service's start command and core needs BOXMES_PUBLIC_URL. */
  dial_only?: boolean;
  /** The provider may restart the instance with a fresh filesystem; boxd rejoins with the host's token. */
  ephemeral_restart?: boolean;
}
export interface SandboxProvider extends SandboxInfo {
  /** dial = T2 (boxd dials in from the sandbox), shim = T3 (exec-only shim at the gateway). */
  mode: "dial" | "shim"; connected: boolean; usable: boolean; blocked?: string; health: string; from_usd_h: number; chain_rank?: number;
}
export interface AdapterLintResult { ok: boolean; error?: string | null; info?: SandboxInfo | null }

export interface BoxEstimate {
  kind: "warm" | "pool" | "vm" | "burst" | "clone"; cost_per_hour: number; expected_hours: number; expected_total_usd: number;
  eta_seconds: number; actual_seconds?: number; score?: number; warm_hit: boolean;
}

export interface ScoredOption {
  kind: string; provider: string; region: string; host_id?: string; sku?: string; isolation: string; cost_per_hour: number;
  expected_total_usd: number; eta_seconds: number; latency_ms: number; rank: number; score: number;
  breakdown: Record<string, { score: number; weight: number; value: string }>; reasons: string[]; requires_approval: boolean;
  /** v1.4 */
  tier?: "T1" | "T2" | "T3"; fork?: boolean; pause?: boolean; ports?: boolean; gpu?: boolean; max_lifetime_h?: number;
  boot_ms?: number; verification?: string; strong_isolation?: boolean; min_billing_h?: number;
}

export interface Recommendation {
  requirements: Requirements; profile: { id: string; name: string; template: string; lifecycle: Lifecycle };
  template: string; lifecycle: Lifecycle; expected_hours: number; options: ScoredOption[];
  rejected: { kind: string; provider?: string; region?: string; host_id?: string; reason: string }[];
  pick: ScoredOption | null; auto_pick: string; explain: string; box_up: Record<string, unknown>; next?: string[];
}

export interface EnsureResult {
  box: Box; action: "created" | "reused" | "woken" | "recreated"; created: boolean;
  resume?: { box: Box; woke: boolean; recent_execs: { ts: string; command: string; exit_code: unknown; log_id: unknown }[]; running_jobs: Job[]; files_changed: string[]; listening_ports: number[]; next: string[] };
}

export interface CreateBoxInput {
  template?: string; lifecycle?: Lifecycle; spec?: Spec; slug?: string; ttl_seconds?: number; idle_minutes?: number;
  budget_usd?: number; ports?: number[]; secrets?: string[]; setup?: string[]; region?: string; agent_id?: string;
  /** v1.3: workload profile (auto-picks template, size and placement) */
  profile?: string;
  /** v1.3: declarative needs; Boxmes scores options and auto-picks */
  requirements?: Requirements;
  /** v1.7: supervised app started with the box; wake on request (default true) */
  app?: AppSpec; wake_on_request?: boolean;
  /** v1.9: gateway name (default: name, else slug; a taken derived name gets a word suffix) */
  subdomain?: string;
  /** full box.yaml instead of the fields above */
  yaml?: string;
}

export type BatchCommand = string | { command: string; cwd?: string; timeout_s?: number };

export interface BoxmesOptions {
  apiKey?: string;
  baseUrl?: string;
  /** Accept-Language for localized error message/hint: vi | en | zh | ja */
  lang?: Locale | string;
  /** retries for retryable codes (NO_CAPACITY, PROVIDER_FAILED, RATE_LIMITED, INTERNAL); default 2 */
  maxRetries?: number;
  /** per-request timeout in ms (default 600000) */
  timeoutMs?: number;
  fetch?: typeof fetch;
}

const RETRYABLE = new Set(["NO_CAPACITY", "PROVIDER_FAILED", "RATE_LIMITED", "INTERNAL"]);
const ROOT_PATHS = new Set(["/llms.txt", "/llms-full.txt", "/.well-known/mcp.json", "/.well-known/agent.json", "/mcp"]);

/** Error envelope from the API. `code` is stable; `message`/`hint` follow `lang`. */
export class BoxmesError extends Error {
  readonly status: number;
  readonly code: string;
  /** localized message without the code prefix */
  readonly apiMessage: string;
  readonly hint: string;
  readonly retryAfterS?: number;
  readonly docsUrl?: string;
  readonly body?: unknown;
  /** v2.0 machine-readable context, e.g. QUOTA_EXCEEDED `{quota, limit, used, plan, upgrade_url}` ({} when absent) */
  readonly details: Record<string, unknown>;
  constructor(status: number, e: Partial<ApiErrorBody>, body?: unknown) {
    super(`${e.code ?? status}: ${e.message ?? ""}${e.hint ? ` (hint: ${e.hint})` : ""}`);
    this.name = "BoxmesError";
    this.status = status;
    this.code = e.code ?? String(status);
    this.apiMessage = e.message ?? "";
    this.hint = e.hint ?? "";
    this.retryAfterS = e.retry_after_s;
    this.docsUrl = e.docs_url;
    this.body = body;
    this.details = e.details ?? {};
  }
  get retryable(): boolean { return RETRYABLE.has(this.code); }
}

function env(name: string): string | undefined {
  const p = (globalThis as { process?: { env?: Record<string, string | undefined> } }).process;
  const v = p?.env?.[name];
  if (v || !name.startsWith("BOXMES_")) return v;
  // Compat: pre-rename BOXAZ_* (Boxaz → Boxmes), then BOXA_* (Boxa → Boxaz)
  // variables still work (deprecated). The newest legacy name wins.
  const suffix = name.slice("BOXMES_".length);
  for (const prefix of LEGACY_ENV_PREFIXES) {
    const legacy = prefix + suffix;
    const old = p?.env?.[legacy];
    if (!old) continue;
    if (!warnedLegacy.has(legacy)) {
      warnedLegacy.add(legacy);
      console.warn(`[boxmes] ${legacy} is deprecated; rename it to ${name}`);
    }
    return old;
  }
  return undefined;
}
const LEGACY_ENV_PREFIXES = ["BOXAZ_", "BOXA_"] as const;
const warnedLegacy = new Set<string>();

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));
const enc = encodeURIComponent;

type Query = Record<string, string | number | boolean | undefined | null>;

export class Boxmes {
  readonly baseUrl: string;
  apiKey: string;
  lang?: string;
  maxRetries: number;
  timeoutMs: number;
  private readonly fetchImpl: typeof fetch;

  constructor(opts: BoxmesOptions = {}) {
    this.apiKey = opts.apiKey ?? env("BOXMES_API_KEY") ?? "";
    let url = (opts.baseUrl ?? env("BOXMES_URL") ?? "http://localhost:8080").replace(/\/+$/, "");
    if (url.endsWith("/v1")) url = url.slice(0, -3);
    this.baseUrl = url;
    this.lang = opts.lang ?? env("BOXMES_LANG") ?? undefined;
    this.maxRetries = opts.maxRetries ?? 2;
    this.timeoutMs = opts.timeoutMs ?? 600_000;
    const f = opts.fetch ?? globalThis.fetch;
    if (!f) throw new Error("@boxmes/sdk needs a global fetch (Node >= 18) or opts.fetch");
    this.fetchImpl = f.bind(globalThis);
  }

  // ---- plumbing ----
  url(path: string, query?: Query): string {
    let u = this.baseUrl + (ROOT_PATHS.has(path) ? path : "/v1" + path);
    if (query) {
      const q = new URLSearchParams();
      for (const [k, v] of Object.entries(query)) if (v !== undefined && v !== null) q.set(k, String(v));
      const s = q.toString();
      if (s) u += "?" + s;
    }
    return u;
  }

  headers(extra?: Record<string, string>): Record<string, string> {
    const h: Record<string, string> = { Accept: "application/json", ...extra };
    if (this.apiKey) h.Authorization = `Bearer ${this.apiKey}`;
    if (this.lang) h["Accept-Language"] = this.lang;
    return h;
  }

  async request<T = unknown>(method: string, path: string, opts: { body?: unknown; query?: Query; headers?: Record<string, string>; raw?: boolean } = {}): Promise<T> {
    const headers = this.headers(opts.headers);
    let body: string | undefined;
    if (opts.body !== undefined) {
      body = JSON.stringify(opts.body);
      headers["Content-Type"] = "application/json";
    }
    for (let attempt = 0; ; attempt++) {
      const ctrl = new AbortController();
      const timer = setTimeout(() => ctrl.abort(), this.timeoutMs);
      let res: Response;
      try {
        res = await this.fetchImpl(this.url(path, opts.query), { method, headers, body, signal: ctrl.signal });
      } finally {
        clearTimeout(timer);
      }
      const text = await res.text();
      if (res.ok) {
        const ct = res.headers.get("content-type") ?? "";
        if (opts.raw || !ct.includes("json")) return text as unknown as T;
        return (text ? JSON.parse(text) : null) as T;
      }
      let env: Partial<ApiErrorBody> = {};
      let parsed: unknown;
      try { parsed = JSON.parse(text); env = (parsed as { error?: ApiErrorBody }).error ?? {}; } catch { env = { message: text.slice(0, 500) }; }
      const err = new BoxmesError(res.status, env, parsed);
      if (!err.retryable || attempt >= this.maxRetries) throw err;
      await sleep(Math.min((err.retryAfterS ?? 0.5 * 2 ** attempt) * 1000, 30_000));
    }
  }

  // ---- discovery (no auth) ----
  health() { return this.request<{ ok: boolean; version: string; components: Record<string, string> }>("GET", "/health"); }
  /** Tool schemas from the server (same source as the MCP server). */
  tools(format: ToolFormat = "openai") { return this.request<unknown>("GET", "/tools", { query: { format } }); }
  agentKinds() { return this.request<List<AgentKind>>("GET", "/agent-kinds"); }
  openapi() { return this.request<Record<string, unknown>>("GET", "/openapi.json"); }
  llmsTxt(full = false) { return this.request<string>("GET", full ? "/llms-full.txt" : "/llms.txt", { raw: true }); }
  errors() { return this.request<{ locale: string; items: unknown[] }>("GET", "/errors"); }

  // ---- boxes ----
  /** Dry-run placement + price; creates nothing. */
  quote(input: CreateBoxInput) { return this.request<Quote>("POST", "/quote", { body: input }); }
  createBox(input: CreateBoxInput = {}, opts: { idempotencyKey?: string; wait?: boolean } = {}) {
    return this.request<Box>("POST", "/boxes", {
      body: input, query: opts.wait === false ? { wait: false } : undefined,
      headers: opts.idempotencyKey ? { "Idempotency-Key": opts.idempotencyKey } : undefined,
    });
  }
  /** alias of createBox */
  up(input: CreateBoxInput = {}, opts: { idempotencyKey?: string; wait?: boolean } = {}) { return this.createBox(input, opts); }
  /** v2.0 filters: `region` (eu | us | apac | sandbox | local | other), `location` (site, e.g. hetzner-fsn), `workspace` (platform admin only). */
  listBoxes(filter: { status?: string; lifecycle?: string; agent?: string; provider?: string; all?: boolean; region?: LocationRegion | (string & {}); location?: string; workspace?: string } = {}) {
    return this.request<List<Box>>("GET", "/boxes", { query: filter });
  }
  /** v2.0: active boxes and today's cost per location (site) and region. */
  boxLocations() { return this.request<LocationsView>("GET", "/boxes/locations"); }
  getBox(id: string) { return this.request<Box>("GET", `/boxes/${enc(id)}`); }
  deleteBox(id: string) { return this.request<Box>("DELETE", `/boxes/${enc(id)}`); }
  /** alias of deleteBox */
  down(id: string) { return this.deleteBox(id); }
  sleep(id: string) { return this.request<Box>("POST", `/boxes/${enc(id)}/sleep`, { body: {} }); }
  wake(id: string) { return this.request<Box>("POST", `/boxes/${enc(id)}/wake`, { body: {} }); }
  boxYaml(id: string) { return this.request<{ box_id: string; yaml: string; source: string }>("GET", `/boxes/${enc(id)}/yaml`); }
  events(id: string) { return this.request<List<BoxmesEvent>>("GET", `/boxes/${enc(id)}/events`); }

  // ---- exec ----
  exec(id: string, command: string, opts?: { cwd?: string; timeout_s?: number; tail_lines?: number; async?: false; session?: boolean }): Promise<ExecResult>;
  exec(id: string, command: string, opts: { cwd?: string; timeout_s?: number; tail_lines?: number; async: true; session?: boolean }): Promise<JobRef>;
  exec(id: string, command: string, opts: { cwd?: string; timeout_s?: number; tail_lines?: number; async?: boolean; session?: boolean } = {}): Promise<ExecResult | JobRef> {
    return this.request("POST", `/boxes/${enc(id)}/exec`, { body: { command, ...opts } });
  }
  execBatch(id: string, commands: BatchCommand[], opts: { stop_on_error?: boolean; cwd?: string; timeout_s?: number; tail_lines?: number } = {}) {
    return this.request<BatchResult>("POST", `/boxes/${enc(id)}/exec-batch`, { body: { commands, ...opts } });
  }
  /** Get a job; `wait` long-polls up to that many seconds (max 60). */
  job(jobId: string, wait?: number) { return this.request<Job>("GET", `/jobs/${enc(jobId)}`, { query: { wait } }); }
  /** Start a command async and long-poll until it finishes. */
  async run(id: string, command: string, opts: { cwd?: string; timeout_s?: number; tail_lines?: number; pollS?: number } = {}): Promise<Job> {
    const { pollS = 30, ...rest } = opts;
    const ref = await this.exec(id, command, { ...rest, async: true });
    for (;;) {
      const j = await this.job(ref.job_id, pollS);
      if (j.status === "done" || j.status === "failed") return j;
    }
  }
  logs(logId: string) { return this.request<string>("GET", `/logs/${enc(logId)}`, { raw: true }); }

  // ---- files ----
  read(id: string, path: string, range: { from?: number; to?: number } = {}) {
    return this.request<FileRead>("GET", `/boxes/${enc(id)}/files`, { query: { path, ...range } });
  }
  write(id: string, path: string, content: string) {
    return this.request<{ ok: boolean; path: string; bytes: number }>("PUT", `/boxes/${enc(id)}/files`, { body: { path, content } });
  }
  grep(id: string, pattern: string, path?: string) {
    return this.request<{ matches: { path: string; line: number; text: string }[]; truncated: boolean }>("POST", `/boxes/${enc(id)}/grep`, { body: { pattern, path } });
  }
  observe(id: string) { return this.request<Record<string, unknown>>("GET", `/boxes/${enc(id)}/observe`); }

  // ---- snapshots ----
  snapshot(id: string) { return this.request<Snapshot>("POST", `/boxes/${enc(id)}/snapshot`, { body: {} }); }
  snapshots(id: string) { return this.request<List<Snapshot>>("GET", `/boxes/${enc(id)}/snapshots`); }
  restore(id: string, snapshotId?: string) { return this.request<Box>("POST", `/boxes/${enc(id)}/restore`, { body: snapshotId ? { snapshot_id: snapshotId } : {} }); }
  fork(id: string, opts: { snapshot_id?: string; slug?: string } = {}) { return this.request<Box>("POST", `/boxes/${enc(id)}/fork`, { body: opts }); }

  // ---- v1.3: need-based selection + agent helpers ----
  /** Workload profiles + per-agent-kind defaults (no auth needed). */
  profiles() { return this.request<{ items: { id: string; name: string; description: string; template: string; lifecycle: Lifecycle }[]; kinds: { kind: string; profile: string }[] }>("GET", "/profiles"); }
  /** Rank placement options with price, ETA, score and reasons. Creates nothing. */
  recommend(input: { profile?: string; template?: string; lifecycle?: Lifecycle; requirements?: Requirements; untrusted?: boolean; fork?: boolean } = {}) {
    const { untrusted, fork, ...rest } = input;
    const requirements = untrusted === undefined && fork === undefined ? rest.requirements
      : { ...rest.requirements, ...(untrusted === undefined ? {} : { untrusted }), ...(fork === undefined ? {} : { fork }) };
    return this.request<Recommendation>("POST", "/recommend", { body: { ...rest, ...(requirements ? { requirements } : {}) } });
  }

  // ---- v1.4: sandbox API providers (T2 dial-in / T3 exec-only shim) ----
  /** Sandbox providers with adapter, tier, effective mode, capabilities, per-second pricing and verification level. */
  sandboxProviders() { return this.request<List<SandboxProvider>>("GET", "/sandbox/providers"); }
  /** Check a declarative (YAML) sandbox adapter without loading it (admin key). */
  lintAdapter(yaml: string) { return this.request<AdapterLintResult>("POST", "/sandbox/adapters/lint", { body: { yaml } }); }
  /** Get-or-create a box by name (idempotent); a sleeping box is woken and returned with a resume summary. */
  ensure(name: string, input: CreateBoxInput = {}) { return this.request<EnsureResult>("POST", "/boxes/ensure", { body: { ...input, name } }); }
  /** Wake if needed and summarise recent execs, jobs, files and ports. */
  resume(id: string) { return this.request<NonNullable<EnsureResult["resume"]>>("GET", `/boxes/${enc(id)}/resume`); }
  /** Copy-on-write clone of a live box on the same host. */
  clone(id: string, opts: { slug?: string } = {}) { return this.request<Box>("POST", `/boxes/${enc(id)}/clone`, { body: opts }); }
  /** Right-sizing suggestion for one box (or all running boxes when id is omitted). */
  rightsize(id?: string) { return this.request<Record<string, unknown>>("GET", id ? `/boxes/${enc(id)}/rightsize` : "/rightsize"); }

  // ---- v1.7: supervised app, gateway URLs, scoped URL tokens, wake settings ----
  /** Declare the box's long-running app: clone repo, install once, start, restart on crash, start again after wake/restore/clone, gate gateway traffic until ready. */
  setApp(id: string, spec: AppSpec) { return this.request<AppView>("PUT", `/boxes/${enc(id)}/app`, { body: spec }); }
  /** App spec + live status (state, ready, restarts, last_exit, supervisor). */
  app(id: string) { return this.request<AppView>("GET", `/boxes/${enc(id)}/app`); }
  restartApp(id: string) { return this.request<AppView>("POST", `/boxes/${enc(id)}/app/restart`, { body: {} }); }
  deleteApp(id: string) { return this.request<AppView>("DELETE", `/boxes/${enc(id)}/app`); }
  /** App output; pass the returned offset back to read only what is new. */
  appLogs(id: string, opts: { offset?: number; max?: number } = {}) {
    return this.request<{ data: string; offset: number; truncated?: boolean }>("GET", `/boxes/${enc(id)}/app/logs`, { query: { offset: opts.offset ?? -1, max: opts.max ?? 16384 } });
  }
  /** Exposed ports with url, kind, access and the stable gateway_url. */
  ports(id: string) { return this.request<PortsResult>("GET", `/boxes/${enc(id)}/ports`); }
  /** Expose a port; label (e.g. "api") names it in its URL https://<name>-<label>.<domain>/ (v1.9). */
  expose(id: string, port: number, label?: string) { return this.request<PortsResult>("POST", `/boxes/${enc(id)}/ports`, { body: label ? { port, label } : { port } }); }
  /** Issue a scoped, expiring, revocable token for one box port (token + url shown once; private defaults to true). */
  createUrlToken(id: string, port: number, opts: { ttl_s?: number; label?: string; private?: boolean } = {}) {
    return this.request<UrlToken>("POST", `/boxes/${enc(id)}/ports/${port}/tokens`, { body: opts });
  }
  urlTokens(id: string, port: number) { return this.request<{ box_id: string; port: number; access: PortAccess; gateway_url: string; items: UrlToken[] }>("GET", `/boxes/${enc(id)}/ports/${port}/tokens`); }
  revokeUrlToken(id: string, port: number, tokenId: string) { return this.request<UrlToken>("DELETE", `/boxes/${enc(id)}/ports/${port}/tokens/${enc(tokenId)}`); }
  setPortAccess(id: string, port: number, access: PortAccess) { return this.request<PortView>("PUT", `/boxes/${enc(id)}/ports/${port}`, { body: { access } }); }
  /** Whether a request to the gateway URL wakes the sleeping box (default on). */
  /** v1.9: name a port in its gateway URL (https://<name>-<label>.<domain>/). */
  setPortLabel(id: string, port: number, label: string) { return this.request<PortView>("PUT", `/boxes/${enc(id)}/ports/${port}`, { body: { label } }); }
  /** v1.9: set the box's readable gateway name (https://<name>.<domain>/); taken / reserved names → BoxmesError with next. */
  renameUrl(id: string, name: string) { return this.request<Box>("PATCH", `/boxes/${enc(id)}`, { body: { subdomain: name } }); }
  setWakeOnRequest(id: string, on: boolean) { return this.request<Box>("PATCH", `/boxes/${enc(id)}`, { body: { wake_on_request: on } }); }
  /** Lifecycle capability flags per provider. */
  capabilities() { return this.request<List<LifecycleCaps & { provider: string; group: string }>>("GET", "/capabilities"); }
  /** Gateway domain, box URL pattern (v1.9 https://<name>.<domain>/ + label/alias patterns), reserved subdomains, provider-preview policy; check probes the public URL. */
  gateway(check = false) { return this.request<GatewayInfo>("GET", "/gateway", { query: check ? { check: "1" } : undefined }); }

  // ---- v1.10 access: tickets, TCP forward, SSH gateway, durable terminal ----
  /** How to reach the box beyond HTTP: commands (box forward / box ssh / ssh-config), WebSocket endpoints, allowed TCP ports, terminal sessions, SSH host key. */
  access(id: string) { return this.request<AccessInfo>("GET", `/boxes/${enc(id)}/access`); }
  /** Single-use access ticket (terminal | tcp | ssh): `ticket` is shown once, `url` is the WebSocket to open; redeem before expires_at (default 60 s). */
  ticket(id: string, purpose: TicketPurpose, opts: { port?: number; ttl_s?: number; session_id?: string; allow?: boolean } = {}) {
    return this.request<AccessTicket>("POST", `/boxes/${enc(id)}/tickets`, { body: { purpose, ...opts } });
  }
  tickets(id: string) { return this.request<List<AccessTicket>>("GET", `/boxes/${enc(id)}/tickets`); }
  /** Revoke one ticket (or "all"); the sessions it opened close. */
  revokeTicket(id: string, ticketId = "all") { return this.request<{ revoked: number }>("DELETE", `/boxes/${enc(id)}/tickets/${enc(ticketId)}`); }
  /** Add an in-box port to the TCP forward allowlist (exposed ports and 22 always are). */
  allowForwardPort(id: string, port: number) { return this.request<{ box_id: string; allowed_ports: number[] }>("POST", `/boxes/${enc(id)}/forward-ports`, { body: { port } }); }
  terminals(id: string) { return this.request<List<TerminalSession> & { detach_ttl_s: number }>("GET", `/boxes/${enc(id)}/terminals`); }
  killTerminal(id: string, sessionId = "all") { return this.request<{ killed: number }>("DELETE", `/boxes/${enc(id)}/terminals/${enc(sessionId)}`); }
  /** SSH gateway host key (known_hosts line + fingerprint), direct listener, ProxyCommand. */
  sshInfo() { return this.request<{ known_hosts: string; host_key_fingerprint: string; host_key_alias: string; direct: string; proxy_command: string; domain: string; /** v1.11 */ user_ca?: string; cert_max_ttl_s?: number; sftp?: string }>("GET", "/ssh"); }
  sshKeys() { return this.request<List<SshKey>>("GET", "/ssh/keys"); }
  addSshKey(publicKey: string, name?: string) { return this.request<SshKey>("POST", "/ssh/keys", { body: { public_key: publicKey, name } }); }
  deleteSshKey(keyId: string) { return this.request<{ deleted: boolean }>("DELETE", `/ssh/keys/${enc(keyId)}`); }

  // ---- v1.11 access P1: SSH certificates, browser (CDP), desktop (VNC) ----
  /** Short-lived SSH user certificate for one box (ttl_s ≤ 43200); revoke with revokeTicket(box, cert.id). */
  sshCert(boxId: string, publicKey: string, ttlS?: number) { return this.request<SshCert>("POST", "/ssh/certs", { body: { box: boxId, public_key: publicKey, ttl_s: ttlS } }); }
  /** Start (or reuse) Chromium in the box; cdp_url works with chromium.connectOverCDP / puppeteer.connect. */
  browser(boxId: string, opts: { ttl_s?: number; headless?: boolean } = {}) { return this.request<BrowserSession>("POST", `/boxes/${enc(boxId)}/browser`, { body: opts }); }
  /** Just the CDP WebSocket URL (starts the browser when needed). */
  async cdpUrl(boxId: string, ttlS?: number) { return (await this.browser(boxId, { ttl_s: ttlS })).cdp_url; }
  browserStatus(boxId: string) { return this.request<BrowserState>("GET", `/boxes/${enc(boxId)}/browser`); }
  stopBrowser(boxId: string) { return this.request<{ box_id: string; stopped: boolean }>("DELETE", `/boxes/${enc(boxId)}/browser`); }
  /** Start the desktop; viewer_url = single-use noVNC page, vnc_url = RFB over WebSocket. */
  desktop(boxId: string, opts: { width?: number; height?: number; view_only?: boolean; ttl_s?: number } = {}) { return this.request<DesktopSession>("POST", `/boxes/${enc(boxId)}/desktop`, { body: opts }); }
  desktopStatus(boxId: string) { return this.request<DesktopState>("GET", `/boxes/${enc(boxId)}/desktop`); }
  stopDesktop(boxId: string) { return this.request<{ box_id: string; stopped: boolean }>("DELETE", `/boxes/${enc(boxId)}/desktop`); }
  /** View-only (default) share link, iframe-able, TTL 60 s … 24 h. */
  shareDesktop(boxId: string, opts: { ttl_s?: number; view_only?: boolean } = {}) { return this.request<DesktopShare>("POST", `/boxes/${enc(boxId)}/desktop/shares`, { body: { ttl_s: opts.ttl_s ?? 3600, view_only: opts.view_only ?? true } }); }
  desktopShares(boxId: string) { return this.request<List<DesktopShare>>("GET", `/boxes/${enc(boxId)}/desktop/shares`); }
  /** Revoke one share link (or "all"): viewers disconnect, the link answers 410. */
  revokeDesktopShares(boxId: string, shareId = "all") { return this.request<{ revoked: number; viewers_closed: number }>("DELETE", `/boxes/${enc(boxId)}/desktop/shares/${enc(shareId)}`); }

  // ---- misc ----
  templates() { return this.request<List<Template>>("GET", "/templates"); }
  overview() { return this.request<Record<string, unknown>>("GET", "/overview"); }
  snippet(agentId: string, kind?: string, lang?: string) { return this.request<Snippet>("GET", `/agents/${enc(agentId)}/snippet`, { query: { kind, lang } }); }
  createAgent(input: { name: string; kind?: string; budget_day_usd?: number; budget_month_usd?: number; locale?: string }) {
    return this.request<Agent>("POST", "/agents", { body: input });
  }
  /**
   * Create an API key (`key` is returned once). `agentId = null` from a workspace caller makes a v2.0 workspace key
   * (scopes default to boxes:read + boxes:write + exec). Expiry via `expires_in_days` or `expires_at` (RFC3339, future).
   */
  createKey(agentId: string | null, name = "sdk", scopes?: KeyScope[], opts: { expires_in_days?: number; expires_at?: string; workspace_id?: string } = {}) {
    return this.request<ApiKey>("POST", "/keys", { body: { agent_id: agentId, name, scopes, ...opts } });
  }
  listKeys() { return this.request<List<ApiKey>>("GET", "/keys"); }
  revokeKey(keyId: string) { return this.request<{ ok: boolean }>("DELETE", `/keys/${enc(keyId)}`); }

  // ---- v2.0 tenancy: account, workspaces, members, invites ----
  // me / switchWorkspace / createWorkspace / acceptInvite need a session token (bxs_…) as apiKey.
  /** Signed-in user, current workspace, role and permissions. */
  me() { return this.request<Me>("GET", "/auth/me"); }
  /** Point the session at another workspace the user belongs to (404 when not a member). */
  switchWorkspace(workspaceId: string) { return this.request<Me>("POST", "/auth/workspace", { body: { workspace_id: workspaceId } }); }
  listWorkspaces() { return this.request<List<Workspace>>("GET", "/workspaces"); }
  /** New workspace owned by the caller (plan = instance default); does not switch the session. */
  createWorkspace(name: string) { return this.request<Workspace>("POST", "/workspaces", { body: { name } }); }
  /** Current workspace with role, plan, effective quota, permissions and member_count. */
  getWorkspace() { return this.request<Workspace>("GET", "/workspaces/current"); }
  renameWorkspace(name: string) { return this.request<Workspace>("PATCH", "/workspaces/current", { body: { name } }); }
  workspaceUsage() { return this.request<Usage>("GET", "/workspaces/current/usage"); }
  /** Workspace audit log (audit:read — owner, admin, auditor). */
  workspaceAudit(filter: { actor?: string; type?: string; box?: string; agent?: string; limit?: number } = {}) {
    return this.request<List<BoxmesEvent>>("GET", "/workspaces/current/audit", { query: filter });
  }
  /** Built-in plans and quotas (no auth). */
  plans() { return this.request<List<{ id: Plan; quota: Quota }>>("GET", "/plans"); }
  listMembers() { return this.request<List<Member>>("GET", "/workspaces/current/members"); }
  /** Owner role can only be granted / changed by owners; the last owner cannot be demoted. */
  updateMemberRole(userId: string, role: Role) { return this.request<Member>("PATCH", `/workspaces/current/members/${enc(userId)}`, { body: { role } }); }
  /** Remove a member (members:write), or leave with your own user id. */
  removeMember(userId: string) { return this.request<{ ok: boolean }>("DELETE", `/workspaces/current/members/${enc(userId)}`); }
  /** Invite an email (7 days). `invite_url` is returned once. */
  createInvite(email: string, role: Role = "developer") { return this.request<WorkspaceInvite>("POST", "/workspaces/current/invites", { body: { email, role } }); }
  listInvites() { return this.request<List<WorkspaceInvite>>("GET", "/workspaces/current/invites"); }
  revokeInvite(inviteId: string) { return this.request<{ ok: boolean }>("DELETE", `/workspaces/current/invites/${enc(inviteId)}`); }
  /** Preview an invite by its bxi_… token (no auth). */
  getInvite(token: string) { return this.request<InvitePreview>("GET", `/workspace-invites/${enc(token)}`); }
  /** Join the invited workspace (session of the invited email); it becomes the current workspace. */
  acceptInvite(token: string) { return this.request<Me>("POST", `/workspace-invites/${enc(token)}/accept`, { body: {} }); }
  /** Platform admin: every workspace with plan, members, boxes and month spend. */
  adminWorkspaces() { return this.request<List<Workspace & { members: number; boxes: number; spent_month_usd: number }>>("GET", "/admin/workspaces"); }
  /** Platform admin: set plan, quota_overrides, status or name. */
  adminUpdateWorkspace(workspaceId: string, patch: { plan?: Plan | ""; quota_overrides?: Partial<Omit<Quota, "plan">>; status?: "active" | "suspended"; name?: string }) {
    return this.request<Workspace>("PATCH", `/admin/workspaces/${enc(workspaceId)}`, { body: patch });
  }

  // ---- v2.2 infra: provider accounts (platform admin) ----
  /** KPI summary + one row per provider (accounts, balance, spend, runway, alerts). */
  adminProviders() { return this.request<{ summary: InfraSummary; items: AdminProviderRow[] }>("GET", "/admin/providers"); }
  adminProvider(provider: string) {
    return this.request<AdminProviderRow & { prefs: Record<string, unknown>; strategy_config: ProviderStrategy; accounts: ProviderAccount[]; alerts: InfraAlert[] }>("GET", `/admin/providers/${enc(provider)}`);
  }
  listProviderAccounts(provider: string, opts: { archived?: boolean } = {}) {
    return this.request<List<ProviderAccount>>("GET", `/admin/providers/${enc(provider)}/accounts`, { query: opts.archived ? { archived: "1" } : undefined });
  }
  getProviderAccount(provider: string, accountId: string) { return this.request<ProviderAccount>("GET", `/admin/providers/${enc(provider)}/accounts/${enc(accountId)}`); }
  /** Check a key without saving it. */
  validateProviderAccount(provider: string, token: string, externalId?: string) {
    return this.request<AccountTestResult>("POST", `/admin/providers/${enc(provider)}/accounts/validate`, { body: { token, external_id: externalId } });
  }
  createProviderAccount(provider: string, input: ProviderAccountInput & { label: string; token: string }) {
    return this.request<ProviderAccount>("POST", `/admin/providers/${enc(provider)}/accounts`, { body: input });
  }
  /** Merge-patch: scheduling, manual balance / credit / limits / dates, caps, thresholds. */
  updateProviderAccount(provider: string, accountId: string, patch: ProviderAccountInput) {
    return this.request<ProviderAccount>("PATCH", `/admin/providers/${enc(provider)}/accounts/${enc(accountId)}`, { body: patch });
  }
  rotateProviderAccount(provider: string, accountId: string, token: string, externalId?: string) {
    return this.request<ProviderAccount>("POST", `/admin/providers/${enc(provider)}/accounts/${enc(accountId)}/rotate`, { body: { token, external_id: externalId } });
  }
  testProviderAccount(provider: string, accountId: string) { return this.request<AccountTestResult>("POST", `/admin/providers/${enc(provider)}/accounts/${enc(accountId)}/test`); }
  setProviderAccountState(provider: string, accountId: string, action: "enable" | "disable" | "drain" | "undrain" | "reset") {
    return this.request<ProviderAccount>("POST", `/admin/providers/${enc(provider)}/accounts/${enc(accountId)}/${action}`);
  }
  deleteProviderAccount(provider: string, accountId: string, opts: { force?: boolean; reason?: string; purge?: boolean } = {}) {
    const query: Record<string, string> = {};
    if (opts.force) query.force = "1";
    if (opts.reason) query.reason = opts.reason;
    if (opts.purge) query.purge = "1";
    return this.request<unknown>("DELETE", `/admin/providers/${enc(provider)}/accounts/${enc(accountId)}`, { query });
  }
  providerStrategy(provider: string) { return this.request<ProviderStrategy>("GET", `/admin/providers/${enc(provider)}/strategy`); }
  setProviderStrategy(provider: string, input: { strategy: AccountStrategy | ""; sticky_minutes?: number; failover_across_accounts?: boolean }) {
    return this.request<ProviderStrategy>("PUT", `/admin/providers/${enc(provider)}/strategy`, { body: input });
  }
  infraAlerts(status?: "open" | "acked" | "closed" | "all") { return this.request<List<InfraAlert>>("GET", "/admin/infra/alerts", { query: status ? { status } : undefined }); }
  ackInfraAlert(alertId: string) { return this.request<InfraAlert>("POST", `/admin/infra/alerts/${enc(alertId)}/ack`); }

  // ---- tool bridge ----
  /** Execute a Boxmes tool by name through the MCP endpoint (same argument mapping as MCP). */
  async callTool<T = unknown>(name: string, args: Record<string, unknown> = {}): Promise<T> {
    const resp = await this.request<{ result?: { structuredContent?: unknown; content?: { text?: string }[]; isError?: boolean }; error?: { message: string } }>(
      "POST", "/mcp", { body: { jsonrpc: "2.0", id: Date.now(), method: "tools/call", params: { name, arguments: args } } });
    if (resp.error) throw new BoxmesError(400, { code: "INVALID", message: resp.error.message });
    const r = resp.result ?? {};
    let out: unknown = r.structuredContent;
    if (out === undefined) {
      const text = (r.content ?? []).map((c) => c.text ?? "").join("");
      try { out = JSON.parse(text); } catch { out = text; }
    }
    if (r.isError && out && typeof out === "object" && "error" in out) {
      const e = (out as { error: ApiErrorBody }).error;
      throw new BoxmesError(400, e, out);
    }
    return out as T;
  }
}

// ---- function-calling helpers ----

export interface OpenAITool { type: "function"; function: { name: string; description: string; parameters: Record<string, unknown>; strict?: boolean } }

/** Boxmes tools in OpenAI Chat Completions format (fetched from the server). */
export async function asOpenAITools(client: Boxmes = new Boxmes()): Promise<OpenAITool[]> {
  return (await client.tools("openai")) as OpenAITool[];
}
/** Boxmes tools in Anthropic Messages format. */
export async function asAnthropicTools(client: Boxmes = new Boxmes()) {
  return (await client.tools("anthropic")) as { name: string; description: string; input_schema: Record<string, unknown> }[];
}
/** Boxmes tools as Gemini Tool objects. */
export async function asGeminiTools(client: Boxmes = new Boxmes()) {
  return (await client.tools("gemini")) as { functionDeclarations: { name: string; description: string; parameters: Record<string, unknown> }[] }[];
}
/** Vercel AI SDK-style tool map: { box_exec: { description, parameters(JSON Schema), execute } }. */
export async function asToolMap(client: Boxmes = new Boxmes()) {
  const tools = await asOpenAITools(client);
  const map: Record<string, { description: string; parameters: Record<string, unknown>; execute: (args: Record<string, unknown>) => Promise<unknown> }> = {};
  for (const t of tools) {
    map[t.function.name] = { description: t.function.description, parameters: t.function.parameters, execute: (args) => client.callTool(t.function.name, args) };
  }
  return map;
}

/** Execute one tool call and return a JSON string for the tool-result message (errors are returned, not thrown). */
export async function handleToolCall(name: string, args: string | Record<string, unknown> | undefined, client: Boxmes = new Boxmes()): Promise<string> {
  const parsed = typeof args === "string" ? (args ? JSON.parse(args) : {}) : args ?? {};
  try {
    const out = await client.callTool(name, parsed);
    return typeof out === "string" ? out : JSON.stringify(out);
  } catch (e) {
    if (e instanceof BoxmesError) return JSON.stringify({ error: { code: e.code, message: e.apiMessage, hint: e.hint, retry_after_s: e.retryAfterS } });
    throw e;
  }
}

/** @deprecated Renamed to {@link Boxmes} (Boxa → Boxmes). */
export const Boxa = Boxmes;
/** @deprecated Renamed to {@link Boxmes}. */
export type Boxa = Boxmes;
/** @deprecated Renamed to {@link BoxmesError}. */
export const BoxaError = BoxmesError;
/** @deprecated Renamed to {@link BoxmesError}. */
export type BoxaError = BoxmesError;
/** @deprecated Renamed to {@link BoxmesOptions}. */
export type BoxaOptions = BoxmesOptions;
/** @deprecated Renamed to {@link BoxmesEvent}. */
export type BoxaEvent = BoxmesEvent;

/** @deprecated Renamed to {@link Boxmes} (Boxaz → Boxmes). */
export const Boxaz = Boxmes;
/** @deprecated Renamed to {@link Boxmes}. */
export type Boxaz = Boxmes;
/** @deprecated Renamed to {@link BoxmesError}. */
export const BoxazError = BoxmesError;
/** @deprecated Renamed to {@link BoxmesError}. */
export type BoxazError = BoxmesError;
/** @deprecated Renamed to {@link BoxmesOptions}. */
export type BoxazOptions = BoxmesOptions;
/** @deprecated Renamed to {@link BoxmesEvent}. */
export type BoxazEvent = BoxmesEvent;

export default Boxmes;

// ---- v1.7 types ----
export type RestartPolicy = "always" | "on-failure" | "never";
export interface AppSpec {
  start: string; repo?: string; ref?: string; install?: string; port?: number; /** v1.9: URL label of port */ label?: string;
  ready?: { http?: string; tcp?: boolean }; restart?: RestartPolicy; env?: Record<string, string>; workdir?: string; backoff_max_s?: number;
}
export interface AppStatus {
  state: "none" | "cloning" | "installing" | "starting" | "ready" | "backoff" | "exited" | "stopped" | "failed" | "sleeping";
  ready: boolean; pid?: number; restarts: number; last_exit?: number; started_at?: string; ready_at?: string; message?: string; supervisor?: "boxd" | "script"; spec_hash?: string;
}
export interface AppView { box_id: string; spec: AppSpec | null; status: AppStatus }
export type PortAccess = "public" | "token";
export interface PortView { port: number; url: string; kind: string; public: boolean; note?: string; caveats?: string[]; gateway_url?: string; access: PortAccess; provider_preview?: "private" | "public";
  /** v1.9: text label (web, api, …), primary = served at the bare box name, alias_url = legacy <key>-<port> URL */
  label?: string; primary?: boolean; alias_url?: string }
export interface PortsResult { box_id: string; url: string; /** v1.9 */ name?: string; primary_port?: number; ports: PortView[] }
export interface UrlToken { id: string; box_id: string; port: number; label?: string; prefix: string; token?: string; url?: string; expires_at: string; created_at: string; revoked_at?: string; expired?: boolean }
export interface LifecycleCaps { memory_pause: boolean; wake_on_http_native: boolean; wake_via_gateway: boolean; fork: boolean; inbound_http: boolean; max_runtime: number; private_preview?: boolean; note?: string }
// v1.10 access
export type TicketPurpose = "terminal" | "tcp" | "ssh" | "cdp" | "vnc" | "ssh_cert" | "vnc_share";
export interface AccessTicket {
  id: string; box_id: string; purpose: TicketPurpose; port?: number; session_id?: string; prefix: string;
  /** Only in the create response. */ ticket?: string; url?: string; core_url?: string; command?: string;
  expires_at: string; created_at: string; redeemed_at?: string; revoked_at?: string; live?: number;
}
export interface TerminalSession {
  id: string; box_id: string; name?: string; cols: number; rows: number; viewers: number; controlled: boolean;
  created_at: string; last_io: string; detached_at?: string; expires_at?: string; exited?: boolean; exit_code?: number; buffered_bytes: number;
}
export interface SshKey { id: string; name: string; public_key: string; fingerprint: string; agent_id?: string; created_at: string }
export interface AccessInfo {
  box_id: string; name?: string; host?: string; link: "boxd" | "agent" | "agent-on-demand" | "none"; live: Record<string, number>;
  tcp: { url: string; core_url: string; allowed_ports: number[]; auth: string };
  terminal: { url: string; core_url: string; detach_ttl_s: number; sessions: TerminalSession[] };
  ssh: { url: string; core_url: string; host: string; proxy_command: string; command: string; config: string; known_hosts: string; host_key_fingerprint: string; direct?: string; features: string };
  /** v1.11 */ cdp?: AccessAddon; desktop?: AccessAddon;
  commands: Record<string, string>; notes?: string[]; tickets: string;
}
/** v1.11: the on-demand browser / desktop in an access summary. */
export interface AccessAddon { url: string; core_url: string; page?: string; start: string; running: boolean; clients: number }
/** v1.11: short-lived SSH user certificate (POST /v1/ssh/certs). */
export interface SshCert {
  id: string; box_id: string; certificate: string; serial: number; principals: string[]; valid_after: string; valid_before: string; fingerprint: string;
  host: string; user: string; proxy_command: string; known_hosts: string; command: string; direct_command?: string; config: string; user_ca: string;
}
export interface BrowserState {
  box_id: string; running: boolean; headless: boolean; source?: "found" | "installed" | "existing"; binary?: string; version?: Record<string, string>;
  started: boolean; start_ms?: number; clients: number; idle_stop_s: number; port: number; note?: string;
}
/** CDP endpoint: pass cdp_url to Playwright chromium.connectOverCDP() or puppeteer.connect({ browserWSEndpoint }). */
export interface BrowserSession extends BrowserState { cdp_url: string; http_url?: string; core_url: string; ticket_id: string; expires_at: string; live_view?: string }
export interface DesktopState {
  box_id: string; running: boolean; display?: string; width?: number; height?: number; vnc_server?: string; wm?: string; started: boolean; start_ms?: number;
  viewers: number; shares: number; idle_stop_s: number; port: number; browser_on_desktop: boolean; note?: string;
}
export interface DesktopSession extends DesktopState { vnc_url: string; viewer_url: string; core_url: string; ticket_id: string; view_only: boolean; expires_at: string }
export interface DesktopShare { id: string; box_id: string; /** only when created */ url?: string; prefix: string; view_only: boolean; expires_at: string; created_at: string; revoked_at?: string; viewers: number; uses: number; actor?: string }

export interface GatewayInfo {
  mode: "host" | "path" | "relative"; domain: string; domain_source: "env" | "settings" | "none"; public_url: string; url_pattern: string; /** v1.9 */ label_pattern?: string; alias_pattern?: string; internet_facing: boolean;
  reserved: string[]; provider_preview: "auto" | "private" | "public"; provider_preview_source: "env" | "settings" | "default"; private_previews: boolean;
  providers: { provider: string; preview_host: boolean; private_preview: boolean; note: string }[];
  check?: { ok: boolean; status?: number; latency_ms: number; error?: string; checked_at: string };
}

// ---- v2.0 tenancy ----
export type Role = "owner" | "admin" | "developer" | "billing" | "viewer" | "auditor";
export type Plan = "free" | "pro" | "team" | "unlimited";
/** Effective workspace limits; 0 means unlimited. */
export interface Quota {
  plan: string; max_boxes: number; max_cpu_per_box: number; max_ram_mb_per_box: number; max_ttl_hours: number;
  public_url: boolean; egress: "restricted" | "open"; max_members: number; max_agents: number; max_keys: number;
  max_secrets: number; max_boxes_per_minute: number;
}
export interface Workspace {
  id: string; name: string; owner_user_id: string; plan?: string; quota_overrides?: Partial<Omit<Quota, "plan">>;
  status?: "active" | "suspended"; created_at: string; created_by?: string;
  /** caller's role (GET /workspaces, /workspaces/current) */
  role?: string;
  /** GET /workspaces/current */
  quota?: Quota; permissions?: string[]; member_count?: number;
}
export interface Me { user: { id: string; email: string; name?: string; [k: string]: unknown }; workspace: Workspace | null; workspaces: Workspace[]; role: string; permissions: string[]; platform_admin: boolean }
export interface Usage {
  workspace_id: string; plan: string; quota: Quota;
  used: { boxes: number; boxes_total: number; agents: number; keys: number; secrets: number; members: number; spent_today_usd: number; spent_month_usd: number };
}
export interface Member { user_id: string; email: string; name?: string; role: string; email_verified: boolean; joined_at: string }
export interface WorkspaceInvite {
  id: string; workspace_id: string; email: string; role: string; invited_by: string; expires_at: string;
  accepted_at: string | null; accepted_by?: string; created_at: string; status: "pending" | "accepted" | "expired";
  /** create response only */
  invite_url?: string; mailed?: boolean;
}
export interface InvitePreview { workspace: { id: string; name: string }; email: string; role: string; invited_by: string; expires_at: string; status: "pending" | "accepted" | "expired"; account_exists: boolean }
export type LocationRegion = "eu" | "us" | "apac" | "sandbox" | "local" | "other";
export interface BoxLocation { region: LocationRegion; country?: string; city?: string; provider: string; site: string; host?: string }
export interface LocationRow extends BoxLocation { running: number; sleeping: number; total: number; cost_today_usd: number }
export interface RegionRow { region: string; running: number; sleeping: number; total: number; cost_today_usd: number }
export interface LocationsView { items: LocationRow[]; regions: RegionRow[]; total: RegionRow }
