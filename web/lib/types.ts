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
  | "INVALID";

export interface ApiErrorBody {
  error: { code: ErrorCode; message: string; hint?: string };
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
}

export type AgentKind = "claude-code" | "openai-agents" | "n8n" | "hark" | "custom";

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
  status: "queued" | "running" | "done" | "failed";
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

// Không có trong contract chi tiết; giả định: { items: Suggestion[] }
export interface Suggestion {
  id: string;
  title: string;
  detail: string;
  saving_month_usd: number;
  box_id?: string | null;
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

export interface Settings {
  notify: {
    telegram_chat_id: string;
    email: string;
    events: string[];
    daily_report_hour: number;
  };
  reaper: { idle_minutes: number; default_ttl_seconds: number; keep_snapshots: number };
  base_domain: string;
  url_token_protect: boolean;
  budget_month_usd: number;
  maintenance: { enabled: boolean; message: string };
  features: { gpu: boolean };
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
}

// ---- Các shape admin chưa được contract mô tả chi tiết (giả định, cần core xác nhận) ----
export interface AdminSystem {
  version: string;
  uptime_s: number;
  components: { name: string; status: "ok" | "degraded" | "down"; latency_ms: number }[];
}

export interface Quotas {
  max_boxes_per_agent: number;
  max_concurrent_boxes: number;
  max_box_cpu: number;
  max_box_ram_mb: number;
  max_spend_day_usd: number;
  exec_rate_per_min: number;
}

export interface AdminUser {
  id: string;
  email: string;
  name: string;
  role: "owner" | "admin" | "viewer";
  created_at: string;
  last_login_at: string | null;
}

// Giả định: GET /boxes/:id/observe
export interface Observe {
  cpu_pct: number;
  ram_pct: number;
  disk_pct: number;
  processes: { pid: number; command: string; cpu_pct: number }[];
}

export type StreamEventName = "box.updated" | "event.created" | "approval.created" | "overview.updated";
