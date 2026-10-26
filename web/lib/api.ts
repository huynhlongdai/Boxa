// API client có kiểu, bám theo docs/api-contract-v1.md.
// Trình duyệt luôn gọi qua proxy Next (/api/boxa/...), proxy gắn API key từ cookie httpOnly,
// nên key không bao giờ nằm trong JS phía client.
import type {
  AdapterLintResult,
  AdminSystem,
  AdminUser,
  Agent,
  AgentKind,
  AgentKindInfo,
  ApiErrorBody,
  ApiKey,
  Approval,
  Box,
  BoxaEvent,
  CostsResponse,
  CreateBoxBody,
  ExecResult,
  FilesResponse,
  Health,
  Host,
  Job,
  List,
  Observe,
  Offer,
  ExecBatchResult,
  Overview,
  Provider,
  Quote,
  QuoteBody,
  Quotas,
  SchedJob,
  Secret,
  Settings,
  Snapshot,
  Snippet,
  Suggestion,
  Template,
  ToolFormat,
  NotifyInput,
  NotifyTestResult,
  NotifyView,
  ProviderRegion,
  StorageInput,
  StorageTestResult,
  StorageView,
  Transport,
  ValidateResult,
  Catalog,
  MeshACLView,
  MeshConfigView,
  MeshKeyResult,
  MeshView,
  ProfilesResponse,
  ProviderHealth,
  ProviderPrefs,
  RecommendBody,
  RecommendResult,
  SandboxProvider,
  ResumeInfo,
  RightSize,
  SpeedView,
  ValidateTokenResult,
  WarmPoolSpec,
  WarmView,
} from "./types";

export const PROXY_BASE = "/api/boxa";

export class ApiError extends Error {
  code: string;
  hint?: string;
  status: number;
  retryAfter?: number;
  docsUrl?: string;
  /** v1.3: các lệnh gợi ý tiếp theo thường gỡ được lỗi (error.next). */
  next: string[];
  constructor(status: number, body: Partial<ApiErrorBody> | null, fallback: string) {
    super(body?.error?.message ?? fallback);
    this.status = status;
    this.code = body?.error?.code ?? "INVALID";
    this.hint = body?.error?.hint;
    this.retryAfter = body?.error?.retry_after_s;
    this.docsUrl = body?.error?.docs_url;
    this.next = Array.isArray(body?.error?.next) ? body!.error!.next! : [];
  }
}

type Query = Record<string, string | number | boolean | undefined | null>;

export function qs(q?: Query): string {
  if (!q) return "";
  const p = new URLSearchParams();
  for (const [k, v] of Object.entries(q)) {
    if (v === undefined || v === null || v === "") continue;
    p.set(k, String(v));
  }
  const s = p.toString();
  return s ? `?${s}` : "";
}

async function request<T>(method: string, path: string, body?: unknown, headers?: Record<string, string>): Promise<T> {
  const res = await fetch(`${PROXY_BASE}${path}`, {
    method,
    headers: {
      ...(body !== undefined ? { "Content-Type": "application/json" } : {}),
      ...headers,
    },
    body: body !== undefined ? JSON.stringify(body) : undefined,
    cache: "no-store",
    credentials: "same-origin",
  });
  if (res.status === 401 && typeof window !== "undefined" && !window.location.pathname.startsWith("/login")) {
    window.location.href = `/login?next=${encodeURIComponent(window.location.pathname)}`;
  }
  const text = await res.text();
  let data: unknown = null;
  if (text) {
    try {
      data = JSON.parse(text);
    } catch {
      data = text;
    }
  }
  if (!res.ok) {
    throw new ApiError(res.status, (data as ApiErrorBody) ?? null, `HTTP ${res.status}`);
  }
  return data as T;
}

export const http = {
  get: <T>(path: string) => request<T>("GET", path),
  post: <T>(path: string, body?: unknown, headers?: Record<string, string>) => request<T>("POST", path, body ?? {}, headers),
  put: <T>(path: string, body?: unknown) => request<T>("PUT", path, body ?? {}),
  patch: <T>(path: string, body?: unknown) => request<T>("PATCH", path, body ?? {}),
  del: <T>(path: string) => request<T>("DELETE", path),
};

/** fetcher cho SWR: key chính là đường dẫn API (vd "/boxes?status=running"). */
export const fetcher = <T>(path: string) => http.get<T>(path);

export interface BoxFilters {
  status?: string;
  agent?: string;
  provider?: string;
  lifecycle?: string;
}
export interface CostFilters {
  agent?: string;
  provider?: string;
  from?: string;
  to?: string;
  task?: string;
}

/** Đường dẫn (khóa SWR) cho từng resource. */
export const paths = {
  health: "/health",
  overview: "/overview",
  boxes: (f?: BoxFilters) => `/boxes${qs(f as Query)}`,
  box: (id: string) => `/boxes/${id}`,
  boxFiles: (id: string, path: string, from?: number, to?: number) => `/boxes/${id}/files${qs({ path, from, to })}`,
  boxEvents: (id: string) => `/boxes/${id}/events`,
  boxSnapshots: (id: string) => `/boxes/${id}/snapshots`,
  boxObserve: (id: string) => `/boxes/${id}/observe`,
  boxYaml: (id: string) => `/boxes/${id}/yaml`,
  job: (id: string) => `/jobs/${id}`,
  log: (id: string) => `/logs/${id}`,
  agents: "/agents",
  agent: (id: string) => `/agents/${id}`,
  // v1.1: `lang` = biến thể transport của snippet (mcp-http, rest, sdk-python…); ngôn ngữ người đọc đi qua Accept-Language.
  snippet: (id: string, kind: AgentKind, transport?: Transport) => `/agents/${id}/snippet${qs({ kind, lang: transport, transport })}`,
  agentKinds: "/agent-kinds",
  keys: "/keys",
  costs: (f?: CostFilters) => `/costs${qs(f as Query)}`,
  costsCsv: (f?: CostFilters) => `${PROXY_BASE}/costs.csv${qs(f as Query)}`,
  suggestions: "/costs/suggestions",
  providers: "/providers",
  hosts: "/hosts",
  offers: "/offers",
  templates: "/templates",
  secrets: "/secrets",
  approvals: "/approvals",
  settings: "/settings",
  storage: "/storage",
  notify: "/notify",
  providerRegions: (id: string) => `/providers/${id}/regions`,
  // v1.3
  providersHealth: "/providers/health",
  providerHealth: (id: string) => `/providers/${id}/health`,
  providerPrefs: (id: string) => `/providers/${id}/prefs`,
  catalog: (f?: { provider?: string; region?: string; arch?: string; min_cpu?: number; min_ram_mb?: number; max_price_hour?: number; limit?: number }) => `/catalog${qs(f)}`,
  profiles: "/profiles",
  // v1.4
  sandboxProviders: "/sandbox/providers",
  rightsize: "/rightsize",
  boxRightsize: (id: string) => `/boxes/${id}/rightsize`,
  boxResume: (id: string) => `/boxes/${id}/resume`,
  warmPool: "/warm-pool",
  speed: "/speed",
  mesh: "/mesh",
  meshConfig: "/mesh/config",
  meshAcl: "/mesh/acl",
  adminSystem: "/admin/system",
  adminJobs: "/admin/jobs",
  adminAudit: (f?: { actor?: string; type?: string }) => `/admin/audit${qs(f)}`,
  adminQuotas: "/admin/quotas",
  adminUsers: "/admin/users",
  // v1.1 discovery (không cần auth). Proxy: /api/boxa/<x> → <BOXA_API>/<x>; tiền tố "_root/" → gốc Core (bỏ /v1).
  openapi: "/openapi.json",
  tools: (format: ToolFormat) => `/tools${qs({ format })}`,
  llmsTxt: "/_root/llms.txt",
  llmsFullTxt: "/_root/llms-full.txt",
  mcpCard: "/_root/.well-known/mcp.json",
  a2aCard: "/_root/.well-known/agent.json",
};

export const api = {
  health: () => http.get<Health>(paths.health),
  overview: () => http.get<Overview>(paths.overview),

  // Boxes
  listBoxes: (f?: BoxFilters) => http.get<List<Box>>(paths.boxes(f)),
  getBox: (id: string) => http.get<Box>(paths.box(id)),
  createBox: (body: CreateBoxBody, idempotencyKey?: string) =>
    http.post<Box>("/boxes", body, idempotencyKey ? { "Idempotency-Key": idempotencyKey } : undefined),
  deleteBox: (id: string) => http.del<Box>(paths.box(id)),
  sleepBox: (id: string) => http.post<Box>(`/boxes/${id}/sleep`),
  wakeBox: (id: string) => http.post<Box>(`/boxes/${id}/wake`),
  snapshotBox: (id: string) => http.post<Snapshot>(`/boxes/${id}/snapshot`),
  exec: (id: string, body: { command: string; cwd?: string; timeout_s?: number; async?: boolean; tail_lines?: number }) =>
    http.post<ExecResult | { job_id: string }>(`/boxes/${id}/exec`, body),
  files: (id: string, path: string, from?: number, to?: number) => http.get<FilesResponse>(paths.boxFiles(id, path, from, to)),
  writeFile: (id: string, path: string, content: string) => http.put<{ ok: boolean }>(`/boxes/${id}/files`, { path, content }),
  grep: (id: string, pattern: string, path: string) => http.post<unknown>(`/boxes/${id}/grep`, { pattern, path }),
  observe: (id: string) => http.get<Observe>(paths.boxObserve(id)),
  boxEvents: (id: string) => http.get<List<BoxaEvent>>(paths.boxEvents(id)),
  snapshots: (id: string) => http.get<List<Snapshot>>(paths.boxSnapshots(id)),
  takeover: (id: string) => http.post<Box>(`/boxes/${id}/takeover`),
  release: (id: string) => http.post<Box>(`/boxes/${id}/release`),
  // v1.1: khôi phục snapshot / fork từ snapshot → Box
  restoreSnapshot: (id: string, snapshotId: string) => http.post<Box>(`/boxes/${id}/restore`, { snapshot_id: snapshotId }),
  forkSnapshot: (id: string, snapshotId: string) => http.post<Box>(`/boxes/${id}/fork`, { snapshot_id: snapshotId }),

  boxYaml: (id: string) => http.get<{ yaml: string }>(paths.boxYaml(id)),
  execBatch: (id: string, commands: string[], stopOnError = true) => http.post<ExecBatchResult>(`/boxes/${id}/exec-batch`, { commands, stop_on_error: stopOnError }),
  quote: (body: QuoteBody) => http.post<Quote>("/quote", body),

  getJob: (id: string, wait?: number) => http.get<Job>(`${paths.job(id)}${qs({ wait })}`),
  getLog: (id: string) => http.get<string>(paths.log(id)),
  cancelJob: (id: string) => http.post<Job>(`/jobs/${id}/cancel`),
  setEgress: (id: string, body: { allow: string[]; deny_by_default: boolean }) => http.put<Box>(`/boxes/${id}/egress`, body),

  // Agents
  listAgents: () => http.get<List<Agent>>(paths.agents),
  getAgent: (id: string) => http.get<Agent>(paths.agent(id)),
  createAgent: (body: Partial<Agent> & { name: string; kind: AgentKind }) => http.post<Agent>(paths.agents, body),
  updateAgent: (id: string, body: Partial<Agent>) => http.patch<Agent>(paths.agent(id), body),
  deleteAgent: (id: string) => http.del<{ ok: boolean }>(paths.agent(id)),
  snippet: (id: string, kind: AgentKind, transport?: Transport) => http.get<Snippet>(paths.snippet(id, kind, transport)),
  agentKinds: () => http.get<List<AgentKindInfo>>(paths.agentKinds),

  // Keys
  listKeys: () => http.get<List<ApiKey>>(paths.keys),
  createKey: (body: { name: string; agent_id: string | null; scopes: string[] }) => http.post<ApiKey>(paths.keys, body),
  revokeKey: (id: string) => http.del<ApiKey>(`/keys/${id}`),

  // Costs
  costs: (f?: CostFilters) => http.get<CostsResponse>(paths.costs(f)),
  suggestions: () => http.get<List<Suggestion>>(paths.suggestions),

  // Infra
  providers: () => http.get<List<Provider>>(paths.providers),
  setProviderToken: (id: string, token: string) => http.put<Provider>(`/providers/${id}/token`, { token }),
  hosts: () => http.get<List<Host>>(paths.hosts),
  createHost: (body: { provider: string; sku: string; region: string }) => http.post<Host>(paths.hosts, body),
  drainHost: (id: string) => http.post<Host>(`/hosts/${id}/drain`),
  deleteHost: (id: string) => http.del<{ ok: boolean }>(`/hosts/${id}`),
  offers: () => http.get<List<Offer>>(paths.offers),
  providerRegions: (id: string) => http.get<List<ProviderRegion>>(paths.providerRegions(id)),
  getStorage: () => http.get<StorageView>(paths.storage),
  saveStorage: (body: StorageInput) => http.put<StorageView>(paths.storage, body),
  testStorage: () => http.post<StorageTestResult>(`${paths.storage}/test`),
  getNotify: () => http.get<NotifyView>(paths.notify),
  saveNotify: (body: NotifyInput) => http.put<NotifyView>(paths.notify, body),
  testNotify: (channel: "telegram" | "email" | "all", kind: "test" | "daily_report" = "test") => http.post<NotifyTestResult>(`${paths.notify}/test`, { channel, kind }),

  // v1.3: provider manager
  providersHealth: () => http.get<List<ProviderHealth>>(paths.providersHealth),
  providerHealth: (id: string) => http.get<ProviderHealth>(paths.providerHealth(id)),
  validateProvider: (id: string, token: string) => http.post<ValidateTokenResult>(`/providers/${id}/validate`, { token }),
  checkProvider: (id: string) => http.post<ProviderHealth>(`/providers/${id}/check`),
  resetProvider: (id: string) => http.post<ProviderHealth>(`/providers/${id}/reset`),
  getProviderPrefs: (id: string) => http.get<ProviderPrefs>(paths.providerPrefs(id)),
  saveProviderPrefs: (id: string, body: Partial<ProviderPrefs>) => http.put<ProviderPrefs>(paths.providerPrefs(id), body),
  catalog: () => http.get<Catalog>(paths.catalog()),
  // v1.3: chọn box theo nhu cầu
  profiles: () => http.get<ProfilesResponse>(paths.profiles),
  recommend: (body: RecommendBody) => http.post<RecommendResult>("/recommend", body),
  // v1.4
  sandboxProviders: () => http.get<List<SandboxProvider>>(paths.sandboxProviders),
  lintAdapter: (yaml: string) => http.post<AdapterLintResult>("/sandbox/adapters/lint", { yaml }),
  // v1.3: tốc độ + agent
  rightsizeAll: () => http.get<List<RightSize>>(paths.rightsize),
  rightsizeBox: (id: string) => http.get<RightSize>(paths.boxRightsize(id)),
  getWarmPool: () => http.get<WarmView>(paths.warmPool),
  saveWarmPool: (body: { enabled: boolean; pools: WarmPoolSpec[] }) => http.put<WarmView>(paths.warmPool, body),
  speed: () => http.get<SpeedView>(paths.speed),
  cloneBox: (id: string, slug?: string) => http.post<Box>(`/boxes/${id}/clone`, { slug: slug ?? "" }),
  resumeBox: (id: string) => http.get<ResumeInfo>(paths.boxResume(id)),
  // v1.3: mesh
  mesh: () => http.get<MeshView>(paths.mesh),
  meshConfig: () => http.get<MeshConfigView>(paths.meshConfig),
  meshAcl: () => http.get<MeshACLView>(paths.meshAcl),
  meshKey: (body: { host_id?: string; box_id?: string; reusable: boolean; ttl_minutes: number }) => http.post<MeshKeyResult>("/mesh/keys", body),

  // Templates
  templates: () => http.get<List<Template>>(paths.templates),
  createTemplate: (body: { name: string; description: string; yaml: string }) => http.post<Template>(paths.templates, body),
  updateTemplate: (name: string, body: { description?: string; yaml: string }) =>
    http.put<Template>(`/templates/${encodeURIComponent(name)}`, body),
  deleteTemplate: (name: string) => http.del<{ ok: boolean }>(`/templates/${encodeURIComponent(name)}`),
  validateTemplate: (yaml: string) => http.post<ValidateResult>("/templates/validate", { yaml }),

  // Secrets
  secrets: () => http.get<List<Secret>>(paths.secrets),
  createSecret: (body: { name: string; value: string; agents: string[] }) => http.post<Secret>(paths.secrets, body),
  rotateSecret: (name: string, value: string) => http.post<Secret>(`/secrets/${encodeURIComponent(name)}/rotate`, { value }),
  deleteSecret: (name: string) => http.del<{ ok: boolean }>(`/secrets/${encodeURIComponent(name)}`),

  // Approvals
  approvals: () => http.get<List<Approval>>(paths.approvals),
  approve: (id: string) => http.post<Approval>(`/approvals/${id}/approve`),
  reject: (id: string) => http.post<Approval>(`/approvals/${id}/reject`),

  emergencyStop: () => http.post<{ ok: boolean; slept: number }>("/emergency-stop", { confirm: "DUNG" }),

  settings: () => http.get<Settings>(paths.settings),
  saveSettings: (s: Settings) => http.put<Settings>(paths.settings, s),

  // Admin
  adminSystem: () => http.get<AdminSystem>(paths.adminSystem),
  adminJobs: () => http.get<List<SchedJob>>(paths.adminJobs),
  adminAudit: (f?: { actor?: string; type?: string }) => http.get<List<BoxaEvent>>(paths.adminAudit(f)),
  adminQuotas: () => http.get<Quotas>(paths.adminQuotas),
  saveQuotas: (q: Quotas) => http.put<Quotas>(paths.adminQuotas, q),
  adminUsers: () => http.get<List<AdminUser>>(paths.adminUsers),
};

export function errMsg(e: unknown): string {
  if (e instanceof ApiError) return e.hint ? `${e.message} — ${e.hint}` : e.message;
  if (e instanceof Error) return e.message;
  return String(e);
}
