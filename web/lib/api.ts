// API client có kiểu, bám theo docs/api-contract-v1.md.
// Trình duyệt luôn gọi qua proxy Next (/api/boxa/...), proxy gắn API key từ cookie httpOnly,
// nên key không bao giờ nằm trong JS phía client.
import type {
  AdminSystem,
  AdminUser,
  Agent,
  AgentKind,
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
  Overview,
  Provider,
  Quotas,
  SchedJob,
  Secret,
  Settings,
  Snapshot,
  Snippet,
  Suggestion,
  Template,
  ValidateResult,
} from "./types";

export const PROXY_BASE = "/api/boxa";

export class ApiError extends Error {
  code: string;
  hint?: string;
  status: number;
  constructor(status: number, body: Partial<ApiErrorBody> | null, fallback: string) {
    super(body?.error?.message ?? fallback);
    this.status = status;
    this.code = body?.error?.code ?? "INVALID";
    this.hint = body?.error?.hint;
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
  job: (id: string) => `/jobs/${id}`,
  log: (id: string) => `/logs/${id}`,
  agents: "/agents",
  agent: (id: string) => `/agents/${id}`,
  snippet: (id: string, kind: AgentKind) => `/agents/${id}/snippet${qs({ kind })}`,
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
  adminSystem: "/admin/system",
  adminJobs: "/admin/jobs",
  adminAudit: (f?: { actor?: string; type?: string }) => `/admin/audit${qs(f)}`,
  adminQuotas: "/admin/quotas",
  adminUsers: "/admin/users",
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
  // Ngoài contract v1 (cần core bổ sung): khôi phục snapshot / fork từ snapshot
  restoreSnapshot: (id: string, snapshotId: string) => http.post<Box>(`/boxes/${id}/restore`, { snapshot_id: snapshotId }),
  forkSnapshot: (id: string, snapshotId: string) => http.post<Box>(`/boxes/${id}/fork`, { snapshot_id: snapshotId }),

  getJob: (id: string) => http.get<Job>(paths.job(id)),
  getLog: (id: string) => http.get<string>(paths.log(id)),

  // Agents
  listAgents: () => http.get<List<Agent>>(paths.agents),
  getAgent: (id: string) => http.get<Agent>(paths.agent(id)),
  createAgent: (body: Partial<Agent> & { name: string; kind: AgentKind }) => http.post<Agent>(paths.agents, body),
  updateAgent: (id: string, body: Partial<Agent>) => http.patch<Agent>(paths.agent(id), body),
  deleteAgent: (id: string) => http.del<{ ok: boolean }>(paths.agent(id)),
  snippet: (id: string, kind: AgentKind) => http.get<Snippet>(paths.snippet(id, kind)),

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
