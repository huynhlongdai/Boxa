/**
 * @boxaz/sdk — TypeScript client for Boxaz (cheap disposable Linux boxes for AI agents).
 * Zero dependencies: uses the global fetch (Node ≥ 18, Deno, Bun, browsers, edge runtimes).
 *
 *   import { Boxaz } from "@boxaz/sdk";
 *   const bx = new Boxaz();                // BOXAZ_URL / BOXAZ_API_KEY / BOXAZ_LANG from env
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
export interface BoxazEvent {
  id: string; ts: string; actor: string; box_id: string | null; agent_id: string | null; type: string;
  message: string; cost_usd: number | null; data?: Record<string, unknown>;
}
export interface Agent { id: string; name: string; kind: string; budget_day_usd: number; budget_month_usd: number; locale?: string; [k: string]: unknown }
export interface ApiKey { id: string; agent_id: string | null; name: string; prefix: string; scopes: string[]; key?: string }
export interface List<T> { items: T[]; next_cursor: string | null }
export interface FileRead { path: string; type: "file" | "dir"; content?: string; total_lines?: number; entries?: { name: string; type: string; size: number }[] }

export interface ApiErrorBody { code: string; message: string; hint: string; retry_after_s?: number; docs_url?: string }

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
  sizes: { sku: string; cpu: number; ram_mb: number; disk_gb: number }[]; regions: { id: string; name: string }[];
  templates: string[]; default_template: string; workdir: string;
  credentials: { name: string; label: string; secret: boolean; optional: boolean; default?: string; hint?: string }[];
  env_vars: string[]; notes?: string[] | null;
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
  /** v1.3: declarative needs; Boxaz scores options and auto-picks */
  requirements?: Requirements;
  /** full box.yaml instead of the fields above */
  yaml?: string;
}

export type BatchCommand = string | { command: string; cwd?: string; timeout_s?: number };

export interface BoxazOptions {
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
export class BoxazError extends Error {
  readonly status: number;
  readonly code: string;
  /** localized message without the code prefix */
  readonly apiMessage: string;
  readonly hint: string;
  readonly retryAfterS?: number;
  readonly docsUrl?: string;
  readonly body?: unknown;
  constructor(status: number, e: Partial<ApiErrorBody>, body?: unknown) {
    super(`${e.code ?? status}: ${e.message ?? ""}${e.hint ? ` (hint: ${e.hint})` : ""}`);
    this.name = "BoxazError";
    this.status = status;
    this.code = e.code ?? String(status);
    this.apiMessage = e.message ?? "";
    this.hint = e.hint ?? "";
    this.retryAfterS = e.retry_after_s;
    this.docsUrl = e.docs_url;
    this.body = body;
  }
  get retryable(): boolean { return RETRYABLE.has(this.code); }
}

function env(name: string): string | undefined {
  const p = (globalThis as { process?: { env?: Record<string, string | undefined> } }).process;
  const v = p?.env?.[name];
  if (v || !name.startsWith("BOXAZ_")) return v;
  // Compat: pre-rename BOXA_* variables still work (deprecated).
  const legacy = "BOXA_" + name.slice("BOXAZ_".length);
  const old = p?.env?.[legacy];
  if (old && !warnedLegacy.has(legacy)) {
    warnedLegacy.add(legacy);
    console.warn(`[boxaz] ${legacy} is deprecated; rename it to ${name}`);
  }
  return old;
}
const warnedLegacy = new Set<string>();

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));
const enc = encodeURIComponent;

type Query = Record<string, string | number | boolean | undefined | null>;

export class Boxaz {
  readonly baseUrl: string;
  apiKey: string;
  lang?: string;
  maxRetries: number;
  timeoutMs: number;
  private readonly fetchImpl: typeof fetch;

  constructor(opts: BoxazOptions = {}) {
    this.apiKey = opts.apiKey ?? env("BOXAZ_API_KEY") ?? "";
    let url = (opts.baseUrl ?? env("BOXAZ_URL") ?? "http://localhost:8080").replace(/\/+$/, "");
    if (url.endsWith("/v1")) url = url.slice(0, -3);
    this.baseUrl = url;
    this.lang = opts.lang ?? env("BOXAZ_LANG") ?? undefined;
    this.maxRetries = opts.maxRetries ?? 2;
    this.timeoutMs = opts.timeoutMs ?? 600_000;
    const f = opts.fetch ?? globalThis.fetch;
    if (!f) throw new Error("@boxaz/sdk needs a global fetch (Node >= 18) or opts.fetch");
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
      const err = new BoxazError(res.status, env, parsed);
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
  listBoxes(filter: { status?: string; lifecycle?: string; agent?: string; provider?: string; all?: boolean } = {}) {
    return this.request<List<Box>>("GET", "/boxes", { query: filter });
  }
  getBox(id: string) { return this.request<Box>("GET", `/boxes/${enc(id)}`); }
  deleteBox(id: string) { return this.request<Box>("DELETE", `/boxes/${enc(id)}`); }
  /** alias of deleteBox */
  down(id: string) { return this.deleteBox(id); }
  sleep(id: string) { return this.request<Box>("POST", `/boxes/${enc(id)}/sleep`, { body: {} }); }
  wake(id: string) { return this.request<Box>("POST", `/boxes/${enc(id)}/wake`, { body: {} }); }
  boxYaml(id: string) { return this.request<{ box_id: string; yaml: string; source: string }>("GET", `/boxes/${enc(id)}/yaml`); }
  events(id: string) { return this.request<List<BoxazEvent>>("GET", `/boxes/${enc(id)}/events`); }

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

  // ---- misc ----
  templates() { return this.request<List<Template>>("GET", "/templates"); }
  overview() { return this.request<Record<string, unknown>>("GET", "/overview"); }
  snippet(agentId: string, kind?: string, lang?: string) { return this.request<Snippet>("GET", `/agents/${enc(agentId)}/snippet`, { query: { kind, lang } }); }
  createAgent(input: { name: string; kind?: string; budget_day_usd?: number; budget_month_usd?: number; locale?: string }) {
    return this.request<Agent>("POST", "/agents", { body: input });
  }
  createKey(agentId: string | null, name = "sdk", scopes?: string[]) {
    return this.request<ApiKey>("POST", "/keys", { body: { agent_id: agentId, name, scopes } });
  }

  // ---- tool bridge ----
  /** Execute a Boxaz tool by name through the MCP endpoint (same argument mapping as MCP). */
  async callTool<T = unknown>(name: string, args: Record<string, unknown> = {}): Promise<T> {
    const resp = await this.request<{ result?: { structuredContent?: unknown; content?: { text?: string }[]; isError?: boolean }; error?: { message: string } }>(
      "POST", "/mcp", { body: { jsonrpc: "2.0", id: Date.now(), method: "tools/call", params: { name, arguments: args } } });
    if (resp.error) throw new BoxazError(400, { code: "INVALID", message: resp.error.message });
    const r = resp.result ?? {};
    let out: unknown = r.structuredContent;
    if (out === undefined) {
      const text = (r.content ?? []).map((c) => c.text ?? "").join("");
      try { out = JSON.parse(text); } catch { out = text; }
    }
    if (r.isError && out && typeof out === "object" && "error" in out) {
      const e = (out as { error: ApiErrorBody }).error;
      throw new BoxazError(400, e, out);
    }
    return out as T;
  }
}

// ---- function-calling helpers ----

export interface OpenAITool { type: "function"; function: { name: string; description: string; parameters: Record<string, unknown>; strict?: boolean } }

/** Boxaz tools in OpenAI Chat Completions format (fetched from the server). */
export async function asOpenAITools(client: Boxaz = new Boxaz()): Promise<OpenAITool[]> {
  return (await client.tools("openai")) as OpenAITool[];
}
/** Boxaz tools in Anthropic Messages format. */
export async function asAnthropicTools(client: Boxaz = new Boxaz()) {
  return (await client.tools("anthropic")) as { name: string; description: string; input_schema: Record<string, unknown> }[];
}
/** Boxaz tools as Gemini Tool objects. */
export async function asGeminiTools(client: Boxaz = new Boxaz()) {
  return (await client.tools("gemini")) as { functionDeclarations: { name: string; description: string; parameters: Record<string, unknown> }[] }[];
}
/** Vercel AI SDK-style tool map: { box_exec: { description, parameters(JSON Schema), execute } }. */
export async function asToolMap(client: Boxaz = new Boxaz()) {
  const tools = await asOpenAITools(client);
  const map: Record<string, { description: string; parameters: Record<string, unknown>; execute: (args: Record<string, unknown>) => Promise<unknown> }> = {};
  for (const t of tools) {
    map[t.function.name] = { description: t.function.description, parameters: t.function.parameters, execute: (args) => client.callTool(t.function.name, args) };
  }
  return map;
}

/** Execute one tool call and return a JSON string for the tool-result message (errors are returned, not thrown). */
export async function handleToolCall(name: string, args: string | Record<string, unknown> | undefined, client: Boxaz = new Boxaz()): Promise<string> {
  const parsed = typeof args === "string" ? (args ? JSON.parse(args) : {}) : args ?? {};
  try {
    const out = await client.callTool(name, parsed);
    return typeof out === "string" ? out : JSON.stringify(out);
  } catch (e) {
    if (e instanceof BoxazError) return JSON.stringify({ error: { code: e.code, message: e.apiMessage, hint: e.hint, retry_after_s: e.retryAfterS } });
    throw e;
  }
}

/** @deprecated Renamed to {@link Boxaz} (Boxa → Boxaz). */
export const Boxa = Boxaz;
/** @deprecated Renamed to {@link Boxaz}. */
export type Boxa = Boxaz;
/** @deprecated Renamed to {@link BoxazError}. */
export const BoxaError = BoxazError;
/** @deprecated Renamed to {@link BoxazError}. */
export type BoxaError = BoxazError;
/** @deprecated Renamed to {@link BoxazOptions}. */
export type BoxaOptions = BoxazOptions;
/** @deprecated Renamed to {@link BoxazEvent}. */
export type BoxaEvent = BoxazEvent;

export default Boxaz;
