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
  | "INTERNAL";

export const ERROR_CODES: ErrorCode[] = ["BUDGET_EXCEEDED", "NO_CAPACITY", "PROVIDER_FAILED", "BOX_SLEEPING", "TIMEOUT", "POLICY_DENIED", "NOT_FOUND", "UNAUTHORIZED", "INVALID", "RATE_LIMITED", "INTERNAL"];

/** v1.1: message/hint đã được Core dịch theo Accept-Language; code không đổi. */
export interface ApiErrorBody {
  error: { code: ErrorCode; message: string; hint?: string; retry_after_s?: number; docs_url?: string };
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

export interface BoxaEvent {
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
  billing: "pool" | "burst";
  created_at: string;
}

export type ProviderId = "hetzner" | "vultr" | "digitalocean" | "contabo" | "netcup" | "local";

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

// GET /costs/suggestions → { items: Suggestion[] } (shape theo boxa/core manager.Suggestion)
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
  /** v1.2: box không khai báo egress thì chặn hết (chỉ còn allowlist của agent). */
  egress_deny_by_default?: boolean;
  budget_month_usd: number;
  maintenance: { enabled: boolean; message: string };
  features: { gpu: boolean };
  /** v1.1: ngôn ngữ mặc định cho thông báo (Telegram/email) và lỗi API. */
  locale?: string;
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
// (shape theo boxa/core; contract v1 chỉ mô tả sơ lược)
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

export interface AdminUser {
  id: string;
  name: string;
  role: string;
  email?: string;
  admin_keys?: number;
  note?: string;
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
