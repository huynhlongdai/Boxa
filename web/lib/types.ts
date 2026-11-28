// Kiểu dữ liệu theo docs/api-contract-v1.md (snake_case, thời gian RFC3339 UTC, tiền USD).

export type ErrorCode =
  | "BUDGET_EXCEEDED"
  | "NO_CAPACITY"
  | "PROVIDER_FAILED"
  | "BOX_SLEEPING"
  | "TIMEOUT"
  | "POLICY_DENIED"
  | "NOT_FOUND"
  | "UNAUTHORIZED"
  | "INVALID"
  | "RATE_LIMITED"
  | "INTERNAL"
  | "QUOTA_EXCEEDED";

export const ERROR_CODES: ErrorCode[] = ["BUDGET_EXCEEDED", "NO_CAPACITY", "PROVIDER_FAILED", "BOX_SLEEPING", "TIMEOUT", "POLICY_DENIED", "NOT_FOUND", "UNAUTHORIZED", "INVALID", "RATE_LIMITED", "INTERNAL", "QUOTA_EXCEEDED"];

/** v1.1: message/hint đã được Core dịch theo Accept-Language; code không đổi. */
export interface ApiErrorBody {
  error: { code: ErrorCode; message: string; hint?: string; retry_after_s?: number; docs_url?: string; next?: string[]; details?: Record<string, unknown> };
}

export interface List<T> {
  items: T[];
  next_cursor: string | null;
}

export type BoxStatus =
  | "requested"
  | "placing"
  | "provisioning"
  | "running"
  | "sleeping"
  | "snapshotting"
  | "terminated"
  | "failed";
export type Lifecycle = "task" | "session" | "service";

export interface Spec {
  cpu: number;
  ram_mb: number;
  disk_gb: number;
}

export interface Box {
  id: string;
  slug: string;
  agent_id: string;
  agent_name: string;
  status: BoxStatus;
  lifecycle: Lifecycle;
  template: string;
  spec: Spec;
  provider: string;
  region: string;
  host_id: string | null;
  url: string | null;
  ttl_seconds: number | null;
  idle_minutes: number | null;
  budget_usd: number | null;
  spent_usd: number;
  cost_per_hour: number;
  placement_reason: string;
  ports: number[];
  secrets: string[];
  created_at: string;
  last_active_at: string | null;
  // Trường phụ Core có thể trả thêm
  error?: string;
  retries?: number;
  taken_over?: boolean;
  billing?: string;
  /** v1.2: allowlist egress hiệu lực (null/undefined = không giới hạn). */
  egress?: BoxEgress | null;
}

/** v1.2: chính sách egress của box và cách host thực thi nó. */
export interface BoxEgress {
  allow: string[];
  deny_by_default: boolean;
  source: "box" | "agent" | "settings";
  enforced: boolean;
  mode: "incus-acl" | "nftables" | "none" | string;
  warning?: string;
  rules: number;
  applied_at?: string | null;
}

export interface CreateBoxBody {
  agent_id?: string;
  slug?: string;
  template?: string;
  spec?: Spec;
  lifecycle?: Lifecycle;
  ttl_seconds?: number;
  idle_minutes?: number;
  budget_usd?: number;
  ports?: number[];
  secrets?: string[];
  setup?: string[];
  yaml?: string;
  egress?: { allow: string[]; deny_by_default?: boolean };
}

/** v1.1: 22 kind (GET /agent-kinds). Giữ kiểu string để không vỡ khi Core thêm kind mới. */
export type AgentKind = string;
export type AgentKindCategory = "coding" | "framework" | "workflow" | "platform" | "custom";
export type Transport = "mcp-stdio" | "mcp-http" | "rest" | "sdk-python" | "sdk-ts" | "cli" | "function-calling";

export interface AgentKindInfo {
  id: AgentKind;
  name: string;
  category: AgentKindCategory;
  transports: Transport[];
  doc_url: string | null;
}

export interface AgentPolicy {
  providers: string[];
  regions: string[];
  egress_allowlist: string[] | null;
}

export interface Agent {
  id: string;
  name: string;
  kind: AgentKind;
  budget_day_usd: number;
  budget_month_usd: number;
  spent_today_usd: number;
  spent_month_usd: number;
  policy: AgentPolicy;
  box_count: number;
  last_call_at: string | null;
  memory_mb: number;
  created_at: string;
}

export interface ApiKey {
  id: string;
  agent_id: string | null;
  name: string;
  prefix: string;
  scopes: string[];
  created_at: string;
  last_used_at: string | null;
  revoked_at: string | null;
  key?: string; // chỉ có trong response tạo key
}

export interface ExecResult {
  exit_code: number;
  duration_ms: number;
  stdout_tail: string;
  stderr_tail: string;
  truncated: boolean;
  log_id: string | null;
  files_changed: string[];
  tokens_saved: number;
}

export interface Job {
  id: string;
  box_id: string;
  command: string;
  status: "queued" | "running" | "done" | "failed" | "canceled";
  result: ExecResult | null;
  created_at: string;
  finished_at: string | null;
}

export interface BoxmesEvent {
  id: string;
  ts: string;
  actor: string;
  box_id: string | null;
  agent_id: string | null;
  type: string;
  message: string;
  cost_usd: number | null;
  /** v1.1: event box.exec có thể mang sẵn các trường này. */
  tokens_saved?: number | null;
  log_id?: string | null;
  data?: { exit_code?: number; duration_ms?: number; command?: string; timed_out?: boolean; tokens_saved?: number; log_id?: string | null } | null;
}

export interface Host {
  id: string;
  provider: string;
  region: string;
  sku: string;
  ip: string;
  status: "provisioning" | "ready" | "draining" | "down";
  cpu: number;
  ram_mb: number;
  disk_gb: number;
  cpu_used_pct: number;
  ram_used_pct: number;
  disk_used_pct: number;
  box_count: number;
  price_month: number;
  price_hour: number;
  /** v1.4: sandbox = host một-box trên Sandbox API (tính theo giây, xoá cùng box). */
  billing: "pool" | "burst" | "sandbox";
  /** v1.4: sandbox = boxd dial-in trong sandbox (T2), shim = exec-only shim ở gateway (T3). */
  driver?: string;
  provider_ref?: string;
  created_at: string;
}

/** T1 cố định + id provider sandbox (v1.4, do registry của Core quyết định). */
export type ProviderId = "hetzner" | "vultr" | "digitalocean" | "contabo" | "netcup" | "local" | (string & {});

export interface Provider {
  id: ProviderId;
  name: string;
  connected: boolean;
  tier: "T1" | "T2" | "T3";
  last_check_at: string | null;
  error: string | null;
  /** v1.2 */
  implemented?: boolean;
  /** Vị trí (1-based) trong chuỗi dự phòng burst của Scheduler; 0/undefined = không nằm trong chuỗi. */
  chain_rank?: number;
  /** env = token lấy từ biến môi trường, api = dán trên dashboard. */
  token_source?: "env" | "api";
  /** v1.4: vps | sandbox (điền từ registry, không lưu). */
  group?: "vps" | "sandbox" | string;
  /** v1.4: api | self | native | yaml */
  adapter?: string;
  /** v1.4: live-verified | docs-verified | sdk-source (provider sandbox). */
  verification?: string;
  docs?: string;
}

export interface ProviderRegion {
  id: string;
  name: string;
  available: boolean;
}

export interface Offer {
  provider: string;
  sku: string;
  region: string;
  cpu: number;
  ram_mb: number;
  disk_gb: number;
  price_hour: number;
  price_month: number;
  min_billing_minutes: number;
  source: string;
  fetched_at: string;
  confidence: "verified" | "unverified" | "stock_limited";
}

export interface LedgerEntry {
  id: string;
  ts: string;
  agent_id: string;
  box_id: string;
  provider: string;
  kind: "compute" | "storage" | "egress" | "snapshot";
  amount_usd: number;
  note: string;
}

export interface CostsResponse extends List<LedgerEntry> {
  summary: {
    total: number;
    by_provider: Record<string, number>;
    pool_usd: number;
    burst_usd: number;
  };
}

// GET /costs/suggestions → { items: Suggestion[] } (shape theo boxmes/core manager.Suggestion)
export interface Suggestion {
  kind: string;
  title: string;
  detail: string;
  saving_usd: number;
  box_id?: string;
  agent_id?: string;
}

export interface Template {
  name: string;
  description: string;
  yaml: string;
  uses: number;
  created_at: string;
}

export interface ValidateResult {
  ok: boolean;
  errors: { line: number; message: string }[];
}

export interface Secret {
  name: string;
  agents: string[];
  created_at: string;
  rotated_at: string | null;
}

export interface Snapshot {
  id: string;
  box_id: string;
  size_mb: number;
  created_at: string;
  location: string;
  /** v1.2: local = chỉ khôi phục trên host gốc; s3 = R2/B2, khôi phục/fork ở host bất kỳ. */
  storage?: "local" | "s3";
  remote_url?: string;
  upload_error?: string;
}

export interface Approval {
  id: string;
  type: "budget" | "service_stop" | "pool_expand" | "host_down";
  title: string;
  detail: string;
  amount_usd: number | null;
  created_at: string;
  status: "pending" | "approved" | "rejected";
}

export interface Health {
  ok: boolean;
  version: string;
  components: Record<"db" | "scheduler" | "reaper" | "price_feed" | "gateway", string>;
}

export interface Overview {
  spent_today: number;
  budget_day: number;
  spent_month: number;
  budget_month: number;
  boxes_running: number;
  boxes_sleeping: number;
  saved_vs_247_usd: number;
  cost_7d: { date: string; by_agent: Record<string, number> }[];
  top_tasks: { box_id: string; slug: string; agent_name: string; cost_usd: number }[];
  approvals_pending: number;
}

export type SmtpTls = "starttls" | "tls" | "none" | "";

export interface NotifySettings {
  telegram_chat_id: string;
  email: string;
  events: string[];
  daily_report_hour: number;
  /** v1.2 */
  timezone?: string;
  smtp?: { host: string; port: number; username: string; from: string; tls: SmtpTls };
}

export interface Settings {
  notify: NotifySettings;
  reaper: { idle_minutes: number; default_ttl_seconds: number; keep_snapshots: number; snapshot_retention_days?: number };
  base_domain: string;
  url_token_protect: boolean;
  /** Tắt fallback tunnel trong box (cloudflared quick tunnel) cho URL công khai. */
  disable_tunnel?: boolean;
  /** v1.2: box không khai báo egress thì chặn hết (chỉ còn allowlist của agent). */
  egress_deny_by_default?: boolean;
  budget_month_usd: number;
  maintenance: { enabled: boolean; message: string };
  features: { gpu: boolean };
  /** v1.1: ngôn ngữ mặc định cho thông báo (Telegram/email) và lỗi API. */
  locale?: string;
  /** v1.8: gateway domain (URL box <key>-<port>.<domain>) + chính sách preview của provider. Env BOXMES_GATEWAY_DOMAIN / BOXMES_PROVIDER_PREVIEW thắng. */
  gateway?: GatewaySettings;
}

export type ProviderPreviewMode = "auto" | "private" | "public";

export interface GatewaySettings {
  domain: string;
  provider_preview: ProviderPreviewMode | "";
}

/** v1.8: GET /v1/gateway[?check=1]. */
export interface GatewayInfo {
  mode: "host" | "path" | "relative";
  domain: string;
  domain_source: "env" | "settings" | "none";
  public_url: string;
  url_pattern: string;
  /** v1.9: URL các port khác theo nhãn, và alias cũ <key>-<port>. */
  label_pattern?: string;
  alias_pattern?: string;
  internet_facing: boolean;
  reserved: string[];
  provider_preview: ProviderPreviewMode;
  provider_preview_source: "env" | "settings" | "default";
  private_previews: boolean;
  providers: { provider: string; preview_host: boolean; private_preview: boolean; note: string }[];
  check?: { ok: boolean; status?: number; latency_ms: number; error?: string; checked_at: string };
}

export interface FileEntry {
  name: string;
  type: "file" | "dir";
  size: number;
}
export type FilesResponse = { entries: FileEntry[] } | { content: string; total_lines: number };

export interface Snippet {
  kind: AgentKind;
  language: string;
  snippet: string;
  transport?: Transport;
}

/** v1.1: POST /quote (dry-run, không tạo box). */
export interface QuoteOption {
  provider: string;
  region: string;
  sku?: string;
  cost_per_hour: number;
  expected_total_usd?: number;
  placement_reason?: string;
  reason?: string;
  billing?: "pool" | "burst";
}
export interface Quote {
  provider: string;
  region: string;
  sku?: string;
  billing?: "pool" | "burst";
  cost_per_hour: number;
  expected_total_usd: number;
  expected_hours?: number;
  placement_reason: string;
  alternatives: QuoteOption[];
}

export interface QuoteBody {
  agent_id?: string;
  template?: string;
  spec: Spec;
  lifecycle?: Lifecycle;
  ttl_seconds?: number;
  idle_minutes?: number;
}

export type ToolFormat = "openai" | "anthropic" | "gemini" | "mcp";

// ---- Các shape admin chưa được contract mô tả chi tiết (giả định, cần core xác nhận) ----
// (shape theo boxmes/core; contract v1 chỉ mô tả sơ lược)
export interface AdminSystem {
  version: string;
  uptime_s: number;
  db?: string;
  public_url?: string;
  auto_burst?: boolean;
  components: { name: string; status: string; latency_ms: number; detail?: string }[];
}

/** GET /admin/jobs: các job nền định kỳ của Core (reaper, ledger, price_feed…). */
export interface SchedJob {
  name: string;
  interval_s: number;
  last_run_at: string | null;
  last_status: string;
  last_error: string;
  duration_ms: number;
  runs: number;
}

/** GET/PUT /admin/quotas: map số; nhãn tiếng Việt cho các khoá đã biết. */
export type Quotas = Record<string, number>;

/** v2 tenancy: GET /admin/users = user + workspaces (role per workspace). */
export interface UserWorkspace {
  id: string;
  name: string;
  role: string; // owner | member
  max_boxes?: number;
  max_ttl_hours?: number;
}

export interface AdminUser {
  id: string;
  email: string;
  name?: string;
  email_verified?: boolean;
  platform_admin?: boolean;
  workspaces?: UserWorkspace[];
  created_at?: string;
  last_login_at?: string | null;
}

/** GET /boxes/:id/observe (proto.ObserveResult). cpu/ram/disk là tuỳ chọn. */
export interface Observe {
  files_changed: string[];
  processes: { pid: number; cmd: string }[];
  ports: number[];
  since: string;
  cpu_pct?: number;
  ram_pct?: number;
  disk_pct?: number;
}

export interface ExecBatchResult {
  results: (ExecResult & { command: string })[];
  stopped_at?: number | null;
  next?: string[];
}

export type StreamEventName = "box.updated" | "event.created" | "approval.created" | "overview.updated";

// ---- v1.2: thông báo, lưu trữ snapshot ----
export const NOTIFY_EVENTS = ["daily_report", "budget", "approval", "emergency_stop", "box_failed", "host_down"] as const;
export type NotifyEvent = (typeof NOTIFY_EVENTS)[number];

export interface Delivery {
  ts: string;
  kind: string;
  channel: "telegram" | "email";
  ok: boolean;
  error?: string;
  title: string;
}

/** GET /notify */
export interface NotifyView extends NotifySettings {
  has_telegram_token: boolean;
  has_smtp_password: boolean;
  telegram_configured: boolean;
  email_configured: boolean;
  events_available: string[];
  recent: Delivery[];
}

/** PUT /notify: secret chỉ ghi (để trống = giữ nguyên). */
export interface NotifyInput extends NotifySettings {
  telegram_bot_token?: string;
  smtp_password?: string;
  clear_telegram_token?: boolean;
  clear_smtp_password?: boolean;
}

export interface NotifyTestResult {
  results: Delivery[];
}

export type StorageProvider = "local" | "r2" | "b2" | "s3" | "minio";

/** GET /storage */
export interface StorageView {
  provider: StorageProvider;
  endpoint: string;
  region: string;
  bucket: string;
  prefix: string;
  access_key_id: string;
  configured: boolean;
  source: "env" | "settings" | "none";
  has_secret: boolean;
  keep_snapshots: number;
  snapshot_retention_days: number;
  fallback: string;
}

export interface StorageInput {
  provider: StorageProvider;
  endpoint?: string;
  region?: string;
  bucket?: string;
  prefix?: string;
  access_key_id?: string;
  secret_access_key?: string;
}

export interface StorageTestResult {
  ok: boolean;
  error?: string;
  latency_ms: number;
  bucket: string;
}

// ---------------- v1.3: provider manager, recommend, speed, mesh ----------------
// Shape bám theo boxmes/core/internal (manager/provmgr.go, recommend.go, agent13.go, warmpool.go, speed.go, mesh.go).

/** Requirements: "box cần gì" (POST /recommend, POST /boxes {requirements}). Bỏ trống = dùng mặc định của profile. */
export interface Requirements {
  cpu?: number;
  ram_mb?: number;
  disk_gb?: number;
  gpu?: number;
  arch?: "amd64" | "arm64" | "any" | string;
  region?: string;
  location?: string;
  max_latency_ms?: number;
  max_price_hour?: number;
  lifetime?: "ephemeral" | "persistent" | string;
  duration_hours?: number;
  isolation?: "any" | "container" | "vm" | "sandbox" | string;
  profile?: string;
  optimize?: "balanced" | "cost" | "speed" | "reliability" | "latency" | string;
  public_url?: boolean;
  untrusted?: boolean;
  /** v1.4: workload fork/clone box (chạy nhiều nhánh song song). */
  fork?: boolean;
}

/** Ước tính giá/ETA đi kèm mọi response tạo box. */
export interface BoxEstimate {
  kind: "warm" | "pool" | "vm" | "burst" | "clone" | string;
  cost_per_hour: number;
  expected_hours: number;
  expected_total_usd: number;
  eta_seconds: number;
  actual_seconds?: number;
  score?: number;
  warm_hit: boolean;
}

export interface Box {
  profile?: string;
  requirements?: Requirements | null;
  isolation?: string;
  estimate?: BoxEstimate | null;
  cloned_from?: string;
  name?: string;
  next?: string[];
}

export interface CreateBoxBody {
  profile?: string;
  requirements?: Requirements;
}

export interface ProviderPrefs {
  enabled: boolean;
  priority: number;
  max_hourly_usd: number;
  max_monthly_usd: number;
  max_servers: number;
  regions: string[];
  default_region: string;
  /** v1.4 (provider sandbox): auto | dial (T2) | shim (T3). */
  sandbox_mode?: SandboxMode | string;
  updated_at?: string;
}

export interface ValidateTokenResult {
  provider: string;
  ok: boolean;
  error?: string;
  error_class?: string;
  latency_ms: number;
  regions: ProviderRegion[];
  sizes: Offer[];
  size_count: number;
  hint?: string;
}

export interface BreakerState {
  state: "closed" | "open" | "half_open" | string;
  consecutive_failures: number;
  opened_at?: string | null;
  open_until?: string | null;
  opens: number;
  last_reason?: string;
}

export interface HealthStats {
  provider: string;
  region?: string;
  samples: number;
  api_latency_p50_ms: number;
  api_latency_p95_ms: number;
  api_success_rate: number;
  create_attempts: number;
  create_success_rate: number;
  provision_p50_s: number;
  provision_p95_s: number;
  error_classes: Record<string, number>;
  last_ok_at: string | null;
  last_error_at: string | null;
  last_error?: string;
  last_error_class?: string;
  breaker: BreakerState;
  rate_limit: { blocked_until?: string | null; hits: number; quota_hit_at?: string | null; remaining: number };
  status: "healthy" | "degraded" | "down" | "unknown";
  reliability: number;
}

export interface ScoreComponent {
  score: number;
  weight: number;
  value: string;
}
export type ScoreFactor = "cost" | "health" | "speed" | "latency" | "capacity" | "reliability";

export interface HealthSample {
  ts: string;
  kind: string;
  region?: string;
  ok: boolean;
  latency_ms: number;
  class: string;
  error?: string;
}

export interface ProviderHealth {
  provider: Provider;
  stats: HealthStats;
  regions: HealthStats[];
  prefs: ProviderPrefs;
  score: { score: number; region: string; sku: string; breakdown: Record<string, ScoreComponent>; reasons: string[] } | null;
  recent?: HealthSample[];
  usable: boolean;
  blocked?: string;
  hosts: number;
  offer_age_s: number;
}

export interface CatalogItem {
  provider: string;
  sku: string;
  region: string;
  arch: string;
  cpu: number;
  ram_mb: number;
  disk_gb: number;
  price_hour: number;
  price_month: number;
  min_billing_minutes: number;
  price_source: string;
  sources: string[];
  feed_price_hour?: number | null;
  fetched_at: string;
  age_s: number;
  stale: boolean;
  confidence: string;
  health: string;
  usable: boolean;
  blocked?: string;
  /** v1.4: T1 (server) | T2/T3 (sandbox API, tính theo giây). */
  tier?: "T1" | "T2" | "T3" | string;
  group?: "vps" | "sandbox" | string;
}
export interface CatalogSource {
  source: string;
  provider: string;
  count: number;
  fetched_at: string;
  age_s: number;
  stale: boolean;
}
export interface Catalog {
  items: CatalogItem[];
  sources: CatalogSource[];
  capabilities: Record<string, Record<string, unknown>>;
  generated_at: string;
  stale_after_s: number;
  cached: boolean;
  total: number;
}

export interface WorkloadProfile {
  id: string;
  name: string;
  description: string;
  template: string;
  lifecycle: Lifecycle;
  idle_minutes: number;
  ttl_seconds: number;
  defaults: Requirements;
  weights: Record<ScoreFactor, number>;
  egress_deny_by_default: boolean;
  egress_allow: string[] | null;
  warm: boolean;
  tips: string[] | null;
}
export interface KindProfile {
  kind: string;
  profile: string;
  spec: Requirements;
  why: string;
}
export interface ProfilesResponse {
  items: WorkloadProfile[];
  kinds: KindProfile[];
  optimize: string[];
  isolation: string[];
  lifetime: string[];
}

export interface RecommendOption {
  kind: "warm" | "pool" | "vm" | "burst" | "sandbox" | string;
  provider: string;
  region: string;
  host_id?: string;
  sku?: string;
  arch?: string;
  isolation: string;
  cpu: number;
  ram_mb: number;
  disk_gb: number;
  cost_per_hour: number;
  expected_total_usd: number;
  eta_seconds: number;
  reliability: number;
  health: string;
  headroom: number;
  latency_ms: number;
  min_billing_h: number;
  requires_approval: boolean;
  price_stale: boolean;
  blocked?: string;
  notes?: string[] | null;
  warm_id?: string;
  rank: number;
  score: number;
  breakdown: Record<string, ScoreComponent>;
  reasons: string[];
  /** v1.4 */
  tier?: "T1" | "T2" | "T3" | string;
  fork?: boolean;
  pause?: boolean;
  ports?: boolean;
  gpu?: boolean;
  max_lifetime_h?: number;
  boot_ms?: number;
  verification?: string;
  strong_isolation?: boolean;
}
export interface Rejection {
  kind: string;
  provider?: string;
  region?: string;
  host_id?: string;
  sku?: string;
  reason: string;
}
export interface RecommendBody {
  requirements: Requirements;
  profile?: string;
  template?: string;
  lifecycle?: Lifecycle;
  agent_id?: string;
  kind?: string;
  limit?: number;
}
export interface RecommendResult {
  requirements: Requirements;
  profile: WorkloadProfile;
  template: string;
  lifecycle: Lifecycle;
  expected_hours: number;
  options: RecommendOption[];
  rejected: Rejection[];
  pick: RecommendOption | null;
  auto_pick: string;
  explain: string;
  box_up: Record<string, unknown>;
  next?: string[];
}

export interface RightSize {
  box_id: string;
  slug: string;
  spec: Spec;
  samples: number;
  window_s: number;
  cpu_p95_pct: number;
  ram_p95_mb: number;
  ram_max_mb: number;
  idle_s: number;
  suggestion: "keep" | "downsize" | "upsize" | "sleep" | "stop" | "insufficient_data";
  suggested_spec?: Spec | null;
  savings_per_hour: number;
  savings_per_month: number;
  reasons: string[];
}

export interface WarmPoolSpec {
  template: string;
  profile?: string;
  spec: Spec;
  min: number;
  max: number;
  ttl_minutes: number;
}
export interface WarmStats {
  hits: number;
  misses: number;
  hit_rate: number;
  avg_claim_ms: number;
  avg_cold_ms: number;
  created: number;
  expired: number;
  last_refill?: string;
  refill_error?: string;
}
export interface WarmView {
  enabled: boolean;
  pools: WarmPoolSpec[];
  entries: { id: string; host_id: string; template: string; spec: Spec; status: "starting" | "ready"; created_at: string; ready_at?: string | null; start_ms: number }[];
  stats: WarmStats;
  ready: Record<string, number>;
}

export interface SpeedHost {
  host_id: string;
  provider: string;
  region: string;
  driver: string;
  arch: string;
  cow: string;
  vm: boolean;
  cache: boolean;
  mesh: boolean;
  load_pct: number;
  boxes: number;
  cold_eta_s: number;
  features: string[];
}
export interface SpeedView {
  placement: "binpack" | "spread" | string;
  warm: WarmStats;
  warm_enabled: boolean;
  warm_ready: number;
  avg_claim_ms: number;
  avg_cold_ms: number;
  speedup: number;
  hosts: SpeedHost[];
  cache_env: string[];
  exec_compact: boolean;
}

export interface ResumeInfo {
  box: Box;
  woke: boolean;
  idle_seconds: number;
  recent_execs: { ts: string; command: string; exit_code: number | null; log_id: string | null }[];
  running_jobs: Job[];
  last_snapshot: Snapshot | null;
  files_changed: string[];
  listening_ports: number[];
  next: string[];
}

export interface MeshNode {
  id: string;
  name: string;
  givenName: string;
  ipAddresses: string[];
  online: boolean;
  lastSeen: string;
  forcedTags: string[];
  validTags: string[];
  user: { name: string };
}
export interface MeshView {
  enabled: boolean;
  url?: string;
  user?: string;
  nodes: MeshNode[];
  online: number;
  hosts_joined: number;
  hosts: number;
  acl_rules: number;
  derp: string;
  error?: string;
  hint?: string;
}
export interface MeshConfigView {
  enabled: boolean;
  config: string;
  derp: string;
  acl: string;
  paths: Record<string, string>;
}
export interface MeshACLView {
  enabled: boolean;
  policy: unknown;
  notes: string[];
}
export interface MeshKeyResult {
  key: string;
  tags: string[];
  ephemeral: boolean;
  expires_at: string;
  login_server: string;
  command: string;
}

// ---------------- v1.4: Sandbox API providers (T2/T3) ----------------
// Shape bám theo boxmes/core/internal/sandbox/sandbox.go (Info) + manager/sandbox14.go (SandboxProviderView) + api/v14.go (lint).

export type SandboxMode = "auto" | "dial" | "shim";
export type Verification = "live-verified" | "docs-verified" | "sdk-source";

export interface SandboxCapabilities {
  background: boolean;
  pause: boolean;
  snapshot: boolean;
  fork: boolean;
  ports: boolean;
  pty: boolean;
  files: boolean;
  gpu: boolean;
  boot_ms: number;
  /** 0 = không giới hạn / không công bố. */
  max_lifetime_s: number;
  max_cpu: number;
  max_ram_mb: number;
  isolation: "microvm" | "gvisor" | "container" | "vm" | string;
}
export interface SandboxPricing {
  cpu_second: number;
  gb_second: number;
  disk_gb_second: number;
  flat_second: number;
  source: string;
  /** docs | estimated */
  confidence: string;
  note?: string;
}
/** fixed_price: gói giá cố định (Render) — price_hour USD/giờ, tính theo giây; không dùng đơn giá CPU/GB. */
export interface SandboxSize { sku: string; cpu: number; ram_mb: number; disk_gb: number; fixed_price?: boolean; price_hour?: number; note?: string }
export interface SandboxCredField { name: string; label: string; secret: boolean; optional: boolean; default?: string; hint?: string }
export interface SandboxInfo {
  id: string;
  name: string;
  tier: "T2" | "T3" | string;
  adapter: "native" | "yaml" | string;
  verification: Verification | string;
  verified_note?: string;
  docs: string;
  website: string;
  initials: string;
  color: string;
  capabilities: SandboxCapabilities;
  pricing: SandboxPricing;
  sizes: SandboxSize[];
  regions: { id: string; name: string }[];
  templates: string[];
  default_template: string;
  workdir: string;
  credentials: SandboxCredField[];
  env_vars: string[];
  notes: string[];
  from_usd_h: number;
  /** Không có API exec (Render): luôn T2 dial-in, boxd là lệnh khởi động của service, core cần BOXMES_PUBLIC_URL. */
  dial_only?: boolean;
  /** Provider có thể khởi động lại instance với filesystem mới; boxd vào lại bằng token của host. */
  ephemeral_restart?: boolean;
}
/** GET /v1/sandbox/providers */
export interface SandboxProvider extends SandboxInfo {
  /** dial (T2) | shim (T3) — chế độ hiệu lực sau khi áp sandbox_mode. */
  mode: "dial" | "shim" | string;
  connected: boolean;
  usable: boolean;
  blocked?: string;
  health: string;
  chain_rank?: number;
}
/** POST /v1/sandbox/adapters/lint */
export interface AdapterLintResult {
  ok: boolean;
  error?: string | null;
  info?: SandboxInfo | null;
}

// ---------------- v1.7: gateway URL theo box+port, URL token, app supervisor, capabilities ----------------
export type PortAccess = "public" | "token";

export interface Box {
  /** v1.7: gateway đánh thức box khi có request (undefined = bật). */
  wake_on_request?: boolean;
  /** v1.7: port → public | token (mặc định public). */
  port_access?: Record<string, PortAccess | string>;
  /** v1.7: app được supervisor chạy (null/undefined = không có). */
  app?: AppSpec | null;
  /** v1.9: tên gateway dễ đọc (https://<subdomain>.<domain>/) + nhãn port. */
  subdomain?: string;
  port_labels?: Record<string, string>;
}

export interface PortView {
  port: number;
  url: string;
  /** provider-preview | ingress | tunnel | gateway | none */
  kind: string;
  public: boolean;
  note?: string;
  caveats?: string[];
  gateway_url?: string;
  access: PortAccess | string;
  /** v1.8: host preview của provider cho port này: private (chỉ qua gateway) | public (ai biết sandbox id cũng vào được); không có = provider không có preview. */
  provider_preview?: "private" | "public";
  /** v1.9: nhãn chữ của port (web, api, db…) trong URL <name>-<label>.<domain>. */
  label?: string;
  /** v1.9: port chính, phục vụ ở tên box trần https://<name>.<domain>/. */
  primary?: boolean;
  /** v1.9: URL cũ <key>-<port> (vẫn chạy). */
  alias_url?: string;
}

export interface PortsResult {
  box_id: string;
  url: string;
  /** v1.9: tên gateway của box và port mà tên trần trỏ vào. */
  name?: string;
  primary_port?: number;
  ports: PortView[];
}

export interface URLToken {
  id: string;
  box_id: string;
  port: number;
  label?: string;
  prefix: string;
  /** Chỉ có trong response tạo token. */
  token?: string;
  /** Chỉ có trong response tạo token: gateway URL kèm ?token=. */
  url?: string;
  expires_at: string;
  created_at: string;
  revoked_at?: string | null;
  expired?: boolean;
}

export interface URLTokenList {
  box_id: string;
  port: number;
  access: PortAccess | string;
  gateway_url: string;
  items: URLToken[];
}

export interface AppReady {
  http?: string;
  tcp?: boolean;
}

export interface AppSpec {
  repo?: string;
  ref?: string;
  install?: string;
  start: string;
  port?: number;
  /** v1.9: nhãn URL của port app (<name>-<label>.<domain>). */
  label?: string;
  ready?: AppReady | null;
  restart?: "always" | "on-failure" | "never" | string;
  env?: Record<string, string>;
  workdir?: string;
  backoff_max_s?: number;
}

/** cloning | installing | starting | ready | backoff | exited | stopped | sleeping | none */
export type AppState = string;

export interface AppStatus {
  state: AppState;
  ready: boolean;
  pid?: number;
  restarts: number;
  last_exit?: number | null;
  started_at?: string | null;
  ready_at?: string | null;
  message?: string;
  supervisor?: "boxd" | "script" | string;
  spec_hash?: string;
}

export interface AppView {
  box_id: string;
  spec: AppSpec | null;
  status: AppStatus;
}

export interface AppLogsResult {
  data: string;
  offset: number;
  truncated?: boolean;
}

export interface CapabilityRow {
  provider: string;
  group: "vps" | "sandbox" | string;
  memory_pause: boolean;
  wake_on_http_native: boolean;
  wake_via_gateway: boolean;
  fork: boolean;
  inbound_http: boolean;
  max_runtime: number;
  /** v1.8: host preview của provider từ chối traffic không đi qua gateway (E2B / Novita traffic token). */
  private_preview?: boolean;
  note?: string;
}

// ---------------- v1.10: truy cập box (ticket một lần, terminal bền, TCP forward, SSH) ----------------
export type TicketPurpose = "terminal" | "tcp" | "ssh" | "ssh_cert" | "cdp" | "vnc" | "vnc_share";

export interface Box {
  /** v1.10: port thêm mà TCP forward / ssh -L được phép tới (ngoài port đã mở). */
  forward_ports?: number[];
}

/** POST/GET /boxes/{id}/tickets — `ticket` chỉ có trong phản hồi tạo (một lần). */
export interface AccessTicket {
  id: string;
  box_id: string;
  purpose: TicketPurpose;
  port?: number;
  session_id?: string;
  prefix: string;
  ticket?: string;
  url?: string;
  core_url?: string;
  command?: string;
  expires_at: string;
  created_at: string;
  redeemed_at?: string | null;
  revoked_at?: string | null;
  live?: number;
  actor?: string;
  key_id?: string;
}

/** GET /boxes/{id}/terminals — PTY bền (sống qua lúc rớt WebSocket trong detach TTL). */
export interface TerminalSession {
  id: string;
  box_id: string;
  name?: string;
  cols: number;
  rows: number;
  viewers: number;
  controlled: boolean;
  created_at: string;
  last_io: string;
  detached_at?: string | null;
  expires_at?: string | null;
  exited?: boolean;
  exit_code?: number | null;
  buffered_bytes: number;
  actor?: string;
}

export interface TerminalList {
  items: TerminalSession[];
  detach_ttl_s: number;
}

/** GET /boxes/{id}/access */
export interface AccessInfo {
  box_id: string;
  name?: string;
  host?: string;
  /** boxd | agent | agent-on-demand | none */
  link: string;
  live: Record<string, number>;
  tcp: { url: string; core_url: string; allowed_ports: number[]; auth: string };
  terminal: { url: string; core_url: string; detach_ttl_s: number; sessions: TerminalSession[] };
  ssh: {
    url: string;
    core_url: string;
    host: string;
    proxy_command: string;
    command: string;
    config: string;
    known_hosts: string;
    host_key_fingerprint: string;
    direct?: string;
    features: string;
  };
  commands: Record<string, string>;
  notes?: string[];
  tickets: string;
}

export interface SshKey {
  id: string;
  name: string;
  public_key: string;
  fingerprint: string;
  agent_id?: string;
  key_id?: string;
  created_at: string;
}

/** GET /ssh */
export interface SshInfo {
  known_hosts: string;
  host_key_fingerprint: string;
  host_key_alias: string;
  direct: string;
  proxy_command: string;
  domain: string;
}

// ---------------- v1.11: truy cập P1 (CDP browser, desktop VNC, share link, SSH cert, SFTP) ----------------
/** Mục cdp / desktop trong GET /boxes/{id}/access. */
export interface AccessAddon {
  url: string;
  core_url: string;
  page?: string;
  start: string;
  running: boolean;
  clients: number;
}

export interface AccessInfo {
  /** v1.11 */
  cdp?: AccessAddon;
  /** v1.11 */
  desktop?: AccessAddon;
}

/** GET /boxes/{id}/browser — không đánh thức box, không khởi động gì. */
export interface BrowserState {
  box_id: string;
  running: boolean;
  headless: boolean;
  source?: string;
  binary?: string;
  version?: Record<string, unknown>;
  started: boolean;
  start_ms?: number;
  clients: number;
  idle_stop_s: number;
  port: number;
  note?: string;
}

/** POST /boxes/{id}/browser — endpoint CDP có ticket (dùng lại được tới expires_at). */
export interface BrowserSession extends BrowserState {
  cdp_url: string;
  http_url?: string;
  core_url: string;
  ticket_id: string;
  expires_at: string;
  live_view?: string;
}

/** GET /boxes/{id}/desktop */
export interface DesktopState {
  box_id: string;
  running: boolean;
  display?: string;
  width?: number;
  height?: number;
  vnc_server?: string;
  wm?: string;
  started: boolean;
  start_ms?: number;
  viewers: number;
  shares: number;
  idle_stop_s: number;
  port: number;
  browser_on_desktop: boolean;
  note?: string;
}

/** POST /boxes/{id}/desktop — ticket viewer một lần (60 s để mở). */
export interface DesktopSession extends DesktopState {
  vnc_url: string;
  viewer_url: string;
  core_url: string;
  ticket_id: string;
  view_only: boolean;
  expires_at: string;
}

/** GET/POST /boxes/{id}/desktop/shares — `url` chỉ có lúc tạo. */
export interface DesktopShare {
  id: string;
  box_id: string;
  url?: string;
  prefix: string;
  view_only: boolean;
  expires_at: string;
  created_at: string;
  revoked_at?: string | null;
  viewers: number;
  uses: number;
  actor?: string;
}

/** POST /ssh/certs */
export interface SshCert {
  id: string;
  box_id: string;
  certificate: string;
  serial: number;
  principals: string[];
  valid_after: string;
  valid_before: string;
  fingerprint: string;
  host: string;
  user: string;
  proxy_command: string;
  known_hosts: string;
  command: string;
  direct_command?: string;
  config: string;
  user_ca: string;
}

// ---- v2.0: tenancy — workspace, vai trò, thành viên, lời mời, hạn mức, vị trí box ----
export const WORKSPACE_ROLES = ["owner", "admin", "developer", "billing", "viewer", "auditor"] as const;
export type WorkspaceRole = (typeof WORKSPACE_ROLES)[number];

/** Quyền trong workspace hiện tại (core/internal/manager/scope.go). */
export const PERMS = {
  view: "view",
  boxesWrite: "boxes:write",
  exec: "exec",
  agentsWrite: "agents:write",
  secretsRead: "secrets:read",
  secretsWrite: "secrets:write",
  secretsUse: "secrets:use",
  templatesWrite: "templates:write",
  billing: "billing:read",
  members: "members:write",
  audit: "audit:read",
  emergency: "emergency_stop",
  workspaceWrite: "workspace:write",
  approvals: "approvals:decide",
} as const;
export type Perm = (typeof PERMS)[keyof typeof PERMS];

export interface AuthUser {
  id: string;
  email: string;
  name?: string;
  email_verified: boolean;
  platform_admin: boolean;
  created_at: string;
}

export interface Workspace {
  id: string;
  name: string;
  owner_user_id?: string;
  plan?: string;
  status?: "active" | "suspended" | string;
  created_at?: string;
  /** Vai trò của tôi trong workspace này ("" với API key). */
  role: WorkspaceRole | "";
}

/** GET /v1/auth/me (với API key: dashboard tự dựng từ /workspaces/current). */
export interface Me {
  user: AuthUser | null;
  workspace: Workspace | null;
  workspaces: Workspace[];
  role: WorkspaceRole | "";
  permissions: string[];
  platform_admin: boolean;
}

/** GET /api/session (route Next, không bao giờ chứa token). */
export interface SessionInfo {
  mock: boolean;
  authed: boolean;
  /** session = đăng nhập email/mật khẩu (cookie giữ token bxs_), key = dán API key, null = chưa đăng nhập. */
  mode: "session" | "key" | "mock" | null;
  key_prefix: string | null;
  ws_base: string | null;
  me: Me | null;
}

export interface AuthConfig {
  signup_mode: "invite" | "open" | "closed";
  first_user: boolean;
  min_password_len: number;
  email_verification: boolean;
  email_configured: boolean;
}

export interface Quota {
  plan: string;
  max_boxes: number;
  max_cpu_per_box: number;
  max_ram_mb_per_box: number;
  max_ttl_hours: number;
  public_url: boolean;
  egress: "open" | "restricted" | string;
  max_members: number;
  max_agents: number;
  max_keys: number;
  max_secrets: number;
  max_boxes_per_minute: number;
}

export interface CurrentWorkspace extends Omit<Workspace, "role"> {
  role: WorkspaceRole | "";
  plan: string;
  quota: Quota;
  permissions: string[];
  member_count: number;
}

export interface WorkspaceUsage {
  workspace_id: string;
  plan: string;
  quota: Quota;
  used: {
    boxes: number;
    boxes_total: number;
    agents: number;
    keys: number;
    secrets: number;
    members: number;
    spent_today_usd: number;
    spent_month_usd: number;
  };
}

export interface PlanInfo {
  id: string;
  quota: Quota;
}

export interface Member {
  user_id: string;
  email: string;
  name?: string;
  role: WorkspaceRole;
  email_verified: boolean;
  joined_at: string;
}

export type InviteStatus = "pending" | "accepted" | "expired";

export interface WorkspaceInvite {
  id: string;
  workspace_id: string;
  email: string;
  role: WorkspaceRole;
  invited_by: string;
  expires_at: string;
  accepted_at: string | null;
  accepted_by?: string;
  created_at: string;
  status: InviteStatus;
}

/** POST /workspaces/current/invites: link chỉ hiện một lần. */
export interface WorkspaceInviteCreated extends WorkspaceInvite {
  invite_url: string;
  mailed: boolean;
}

/** GET /v1/workspace-invites/{token} (không cần đăng nhập). */
export interface InvitePreview {
  workspace: { id: string; name: string };
  email: string;
  role: WorkspaceRole;
  invited_by: string;
  expires_at: string;
  status: InviteStatus;
  account_exists: boolean;
}

export type LocationRegion = "eu" | "us" | "apac" | "sandbox" | "local" | "other";

export interface BoxLocation {
  region: LocationRegion | string;
  country?: string;
  city?: string;
  provider: string;
  /** "<provider>-<mã site>", vd hetzner-fsn — giá trị của ?location=. */
  site: string;
  host?: string;
}

export interface LocationRow extends BoxLocation {
  running: number;
  sleeping: number;
  total: number;
  cost_today_usd: number;
}

export interface RegionRow {
  region: string;
  running: number;
  sleeping: number;
  total: number;
  cost_today_usd: number;
}

/** GET /v1/boxes/locations. */
export interface LocationsView {
  items: LocationRow[];
  regions: RegionRow[];
  total: RegionRow;
}

export interface Box {
  /** v2.0: nơi box chạy (suy từ provider + vùng/host). */
  location?: BoxLocation;
  workspace_id?: string;
}

/** details của lỗi QUOTA_EXCEEDED (v2.0). */
export interface QuotaDetails {
  quota: string;
  limit: number | boolean;
  used?: number;
  plan?: string;
  upgrade_url?: string;
}

// ---------------- v2.2 Hạ tầng: tài khoản nhà cung cấp (M29) + M29-S sandbox nhiều khoá ----------------
// Bám docs/api-contract-v1.md "v2.2 additions" và "v2.2 M29-S". Chỉ admin nền tảng.
export const ACCOUNT_STRATEGIES = ["priority", "weighted", "round_robin", "least_used", "drain_first"] as const;
export type AccountStrategy = (typeof ACCOUNT_STRATEGIES)[number];
export const ACCOUNT_STATUSES = ["active", "cooling", "rate_limited", "quota_full", "low_balance", "out_of_credit", "auth_failed", "token_expiring", "disabled", "draining", "archived"] as const;
export type AccountStatus = (typeof ACCOUNT_STATUSES)[number];
export type AccountStateAction = "enable" | "disable" | "drain" | "undrain" | "reset";
export interface AccountLimit { limit: number; used: number; source?: "manual" | "learned" | "api"; at?: string }
export interface AccountStats {
  hosts_live: number; boxes_live: number; vcpu_live: number;
  spend_today: number; spend_month: number; spend_today_usd: number; spend_month_usd: number; burn_per_hour_usd: number;
  balance_effective?: number; balance_effective_usd?: number; runway_hours?: number; runway_days?: number; forecast_month_end: number;
  eligible: boolean; ineligible?: string; breaker: "closed" | "open" | "half_open"; has_credential: boolean;
  sandboxes_running?: number; group_running?: number; in_flight?: number; creates_last_sec?: number; creates_last_min?: number;
  /** v2.3: provider báo số dư qua API (được thăm dò), còn lại nhập tay / ước tính */
  balance_api?: boolean;
}
export type AccountThresholdKey = "runway_warn_days" | "runway_critical_days" | "limit_warn_pct" | "limit_critical_pct" | "expiry_warn_days" | "expiry_critical_days";
export interface ProviderAccount {
  id: string; provider: string; label: string; external_id?: string; owner_workspace_id?: string; tags: string[]; owner?: string; notes?: string; console_url?: string;
  is_default: boolean; credential_ref: string; credential_source: "api" | "env"; key_fingerprint?: string; billing_key_fingerprint?: string; scopes: string[];
  token_expires_at?: string; refresh_expires_at?: string; rotated_at?: string; rotate_every_days?: number;
  enabled: boolean; priority: number; weight: number; regions: string[]; pool?: string; drain: boolean; auto_drained: boolean;
  status: AccountStatus; status_reason?: string; status_since?: string; cooldown_until?: string; error_streak: number;
  last_error_class?: string; last_error?: string; last_ok_at?: string; last_check_at?: string;
  billing_model?: "prepaid" | "postpaid" | "contract" | "free_tier"; currency: string; fx_rate_usd?: number;
  balance?: number; balance_source?: "manual" | "api" | "estimated"; balance_at?: string;
  credit_total?: number; credit_remaining?: number; credit_expires_at?: string; credit_at?: string; pending_charges?: number; month_to_date?: number;
  spend_cap_day: number; spend_cap_month: number; min_balance: number; low_balance_warn: number;
  billing_cycle?: string; invoice_day?: number; next_invoice_at?: string; trial_ends_at?: string; contract_ends_at?: string;
  limits: Record<string, AccountLimit>; thresholds: Partial<Record<AccountThresholdKey, number>>;
  templates?: string[]; templates_missing?: string[]; templates_synced_at?: string; limit_group?: string; billing_group?: string;
  created_at: string; created_by?: string; updated_at: string; updated_by?: string; stats?: AccountStats; warnings?: string[];
  /** v2.3 (M29 pha B): đọc số dư qua API (Runpod). stale = lần đọc cuối lỗi, giữ số đọc tốt gần nhất. */
  spend_per_hour?: number; balance_polled_at?: string; balance_stale?: boolean; balance_error?: string; balance_error_at?: string;
}
/** v2.3: một snapshot số dư (một lần thăm dò). Tiền theo `currency`, trừ trường *_usd. */
export interface BalancePoint {
  account_id: string; provider: string; ts: string; source: "api" | "manual" | "estimated" | "none"; currency: string;
  balance?: number; balance_effective?: number; balance_usd?: number; spend_per_hour?: number; spend_limit_per_hour?: number;
  burn_per_hour_usd: number; runway_days?: number; spend_today_usd: number; spend_month_usd: number; stale: boolean; error?: string;
}
/** GET /v1/admin/providers/{p}/accounts/{a}/balance/history?days=N */
export interface BalanceHistory {
  provider: string; account_id: string; label: string; currency: string; days: number; interval_s: number;
  source: "api" | "manual" | "estimated" | "none"; balance_api: boolean; stale: boolean; balance_error?: string; balance_at?: string; polled_at?: string;
  points: BalancePoint[]; runway_trend_per_day?: number; spent?: number; top_ups?: number;
}
export interface ProviderBalancePoint { ts: string; balance_usd?: number; burn_per_hour_usd: number; runway_days?: number; spend_today_usd: number; accounts: number; stale_accounts: number }
/** GET /v1/admin/providers/{p}/balance/history?days=N */
export interface ProviderBalanceHistory {
  provider: string; days: number; bucket_s: number; accounts: { id: string; label: string; source: string; stale: boolean }[];
  points: ProviderBalancePoint[]; runway_trend_per_day?: number;
}
/** POST / PATCH (merge-patch; ngày RFC 3339 hoặc YYYY-MM-DD, "" xoá; limits giá trị 0 = bỏ). */
export interface ProviderAccountInput {
  label?: string; token?: string; billing_token?: string; skip_validate?: boolean; external_id?: string; tags?: string[]; owner?: string; notes?: string; console_url?: string; scopes?: string[];
  enabled?: boolean; priority?: number; weight?: number; regions?: string[]; pool?: string; drain?: boolean;
  billing_model?: string; currency?: string; fx_rate_usd?: number; balance?: number; clear_balance?: boolean; credit_total?: number; credit_remaining?: number;
  pending_charges?: number; month_to_date?: number; spend_cap_day?: number; spend_cap_month?: number; min_balance?: number; low_balance_warn?: number;
  billing_cycle?: string; invoice_day?: number; rotate_every_days?: number;
  token_expires_at?: string; refresh_expires_at?: string; credit_expires_at?: string; trial_ends_at?: string; contract_ends_at?: string; next_invoice_at?: string;
  limits?: Record<string, number>; thresholds?: Partial<Record<AccountThresholdKey, number>>;
  templates?: string[]; clear_templates_missing?: boolean; limit_group?: string; billing_group?: string;
}
export interface AccountTestResult { ok: boolean; error?: string; error_class?: string; latency_ms: number; key_fingerprint?: string; duplicates?: string[]; account?: ProviderAccount; balance_read?: boolean; balance_error?: string }
export interface ProviderStrategyView { provider: string; strategy: AccountStrategy | ""; effective_strategy: AccountStrategy; sticky_minutes: number; failover_across_accounts: boolean; strategies: AccountStrategy[] }
export type InfraAlertLevel = "info" | "warn" | "critical";
export interface InfraAlert {
  id: string; type: string; level: InfraAlertLevel; status: "open" | "acked" | "closed"; provider?: string; account_id?: string; host_id?: string;
  message: string; data?: { args?: unknown[] }; count: number; opened_at: string; updated_at: string; closed_at?: string; acked_at?: string; acked_by?: string;
  notified_at?: string; notified_level?: string;
}
export interface AdminProviderRow {
  id: string; name: string; tier: string; group: "vps" | "sandbox" | string; connected: boolean; implemented: boolean; health: string; strategy: AccountStrategy;
  accounts_total: number; accounts_healthy: number; accounts_degraded: number; hosts_live: number; boxes_live: number;
  balance_usd?: number; balance_by_currency?: Record<string, number>; spend_today_usd: number; spend_month_usd: number; forecast_month_usd: number;
  runway_days?: number; max_limit_pct: number; open_alerts: number;
}
export interface InfraSummary {
  balance_usd: number; balance_by_currency: Record<string, number>; spend_today_usd: number; spend_month_usd: number; forecast_month_usd: number;
  shortest_runway?: { provider: string; account_id: string; label: string; days: number };
  accounts_healthy: number; accounts_degraded: number; accounts_broken: number; open_alerts: number;
}
export interface AdminProvidersView { summary: InfraSummary; items: AdminProviderRow[] }
export interface AdminProviderDetail extends AdminProviderRow {
  prefs: Record<string, unknown>; strategy_config: ProviderStrategyView; accounts: ProviderAccount[]; alerts: InfraAlert[]; capabilities?: Record<string, unknown>;
}
export type TemplateAvailability = "available" | "missing" | "not_listed" | "unknown";
export interface SandboxAccountRow {
  id: string; label: string; status: AccountStatus; eligible: boolean; ineligible?: string; limit_group?: string; billing_group?: string;
  running: number; group_running: number; in_flight: number; concurrent_limit?: number; concurrent_source?: "manual" | "learned" | "api"; concurrency_pct?: number;
  create_per_sec?: number; create_per_min?: number; creates_last_sec: number; creates_last_min: number; cooldown_until?: string;
  templates: Record<string, TemplateAvailability>; templates_synced_at?: string; scopes: string[]; scope_problems?: string[];
  balance_effective?: number; credit_expires_at?: string;
}
export interface SandboxAccountsView { provider: string; strategy: AccountStrategy; templates: string[]; accounts: SandboxAccountRow[] }
export interface TemplateSyncResult { templates: string[]; added: string[]; removed: string[]; account: ProviderAccount }
