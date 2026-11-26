// Dữ liệu mẫu (MOCK MODE) khớp contract v1. Bối cảnh: agent của anh chạy trên Hetzner/Vultr/Contabo.
import type {
  AdminUser,
  Agent,
  ApiKey,
  Approval,
  Box,
  BoxazEvent,
  Host,
  Job,
  LedgerEntry,
  Offer,
  Provider,
  Quotas,
  Secret,
  Settings,
  Snapshot,
  Template,
  Delivery,
  StorageView,
} from "../types";
import { sandboxProviderRows } from "./v14";

export const MIN = 60_000;
export const HOUR = 60 * MIN;
export const DAY = 24 * HOUR;
export const iso = (t: number) => new Date(t).toISOString();
export const ago = (ms: number) => iso(Date.now() - ms);
export const r4 = (n: number) => Math.round(n * 10000) / 10000;

// PRNG cố định để dữ liệu ổn định giữa các lần chạy
function rng(seed: number) {
  let s = seed >>> 0;
  return () => {
    s = (s * 1664525 + 1013904223) >>> 0;
    return s / 4294967296;
  };
}

export interface MockState {
  agents: Agent[];
  boxes: (Box & { _ready_at?: number; _taken_over?: boolean })[];
  hosts: (Host & { _ready_at?: number })[];
  providers: Provider[];
  offers: Offer[];
  ledger: LedgerEntry[];
  keys: ApiKey[];
  templates: Template[];
  secrets: Secret[];
  snapshots: Snapshot[];
  approvals: Approval[];
  events: BoxazEvent[];
  jobs: Job[];
  logs: Record<string, string>;
  files: Record<string, Record<string, string>>; // box_id -> path -> content
  settings: Settings;
  /** v1.2: secret thông báo (chỉ biết có/không), lịch sử gửi, cấu hình lưu trữ snapshot. */
  notify_secrets: { telegram: boolean; smtp: boolean };
  deliveries: Delivery[];
  storage: StorageView;
  quotas: Quotas;
  users: AdminUser[];
  first_call_at: Record<string, number>; // agent_id -> thời điểm giả lập "lệnh đầu tiên"
  seq: number;
  started_at: number;
}

const TEMPLATES: Template[] = [
  {
    name: "base",
    description: "Ubuntu 24.04 tối giản, có git, curl, jq",
    uses: 41,
    created_at: ago(40 * DAY),
    yaml: `name: base
image: ubuntu:24.04
spec:
  cpu: 1
  ram_mb: 1024
  disk_gb: 10
lifecycle: task
idle_minutes: 15
setup:
  - apt-get install -y git curl jq
`,
  },
  {
    name: "python",
    description: "Python 3.12 + uv + pip cache dùng chung",
    uses: 63,
    created_at: ago(38 * DAY),
    yaml: `name: python
image: ubuntu:24.04
spec:
  cpu: 2
  ram_mb: 2048
  disk_gb: 20
lifecycle: task
idle_minutes: 15
setup:
  - curl -LsSf https://astral.sh/uv/install.sh | sh
  - uv python install 3.12
`,
  },
  {
    name: "node",
    description: "Node 22 + pnpm, cache store dùng chung giữa các box",
    uses: 37,
    created_at: ago(38 * DAY),
    yaml: `name: node
image: ubuntu:24.04
spec:
  cpu: 2
  ram_mb: 2048
  disk_gb: 20
lifecycle: session
idle_minutes: 30
ports: [3000]
setup:
  - curl -fsSL https://deb.nodesource.com/setup_22.x | bash -
  - apt-get install -y nodejs && corepack enable
`,
  },
  {
    name: "python-browser",
    description: "Python + Playwright Chromium cho crawl và tự động hoá web",
    uses: 22,
    created_at: ago(30 * DAY),
    yaml: `name: python-browser
image: ubuntu:24.04
spec:
  cpu: 2
  ram_mb: 4096
  disk_gb: 25
lifecycle: task
idle_minutes: 10
egress_allowlist:
  - "*.shopee.vn"
  - "*.susercontent.com"
setup:
  - uv pip install playwright
  - playwright install --with-deps chromium
`,
  },
];

export function makeSeed(): MockState {
  const now = Date.now();
  const rand = rng(20261026);

  const agents: Agent[] = [
    {
      id: "agt_shopee",
      name: "shopee-crawler",
      kind: "custom",
      budget_day_usd: 0.8,
      budget_month_usd: 15,
      spent_today_usd: 0,
      spent_month_usd: 0,
      policy: { providers: ["hetzner", "vultr"], regions: ["fsn1", "hel1", "sgp"], egress_allowlist: ["*.shopee.vn", "*.susercontent.com"] },
      box_count: 0,
      last_call_at: ago(2 * MIN),
      memory_mb: 184,
      created_at: ago(34 * DAY),
    },
    {
      id: "agt_kecon",
      name: "kecon-writer",
      kind: "claude-code",
      budget_day_usd: 0.6,
      budget_month_usd: 12,
      spent_today_usd: 0,
      spent_month_usd: 0,
      policy: { providers: ["hetzner"], regions: ["fsn1", "hel1"], egress_allowlist: null },
      box_count: 0,
      last_call_at: ago(6 * MIN),
      memory_mb: 512,
      created_at: ago(29 * DAY),
    },
    {
      id: "agt_replingo",
      name: "replingo-bot",
      kind: "openai-agents",
      budget_day_usd: 0.5,
      budget_month_usd: 10,
      spent_today_usd: 0,
      spent_month_usd: 0,
      policy: { providers: ["contabo", "vultr"], regions: ["eu-de", "sgp"], egress_allowlist: ["api.openai.com", "*.googleapis.com"] },
      box_count: 0,
      last_call_at: ago(14 * MIN),
      memory_mb: 96,
      created_at: ago(21 * DAY),
    },
    {
      id: "agt_n8n",
      name: "n8n-flows",
      kind: "n8n",
      budget_day_usd: 0.3,
      budget_month_usd: 6,
      spent_today_usd: 0,
      spent_month_usd: 0,
      policy: { providers: ["contabo"], regions: ["eu-de"], egress_allowlist: null },
      box_count: 0,
      last_call_at: ago(41 * MIN),
      memory_mb: 48,
      created_at: ago(18 * DAY),
    },
    {
      id: "agt_hark",
      name: "hark-main",
      kind: "hark",
      budget_day_usd: 1.0,
      budget_month_usd: 20,
      spent_today_usd: 0,
      spent_month_usd: 0,
      policy: { providers: ["hetzner", "vultr", "contabo", "e2b", "prized", "upstash-box"], regions: ["fsn1", "hel1", "sgp", "eu-de", "global", "us-east-1"], egress_allowlist: null },
      box_count: 0,
      last_call_at: ago(1 * MIN),
      memory_mb: 1240,
      created_at: ago(40 * DAY),
    },
  ];
  const agentName = (id: string) => agents.find((a) => a.id === id)!.name;

  const hosts: Host[] = [
    { id: "hst_fsn1_01", provider: "hetzner", region: "fsn1", sku: "cx42", ip: "49.12.88.17", status: "ready", cpu: 8, ram_mb: 16384, disk_gb: 160, cpu_used_pct: 46, ram_used_pct: 58, disk_used_pct: 37, box_count: 3, price_month: 19.4, price_hour: 0.0311, billing: "pool", created_at: ago(39 * DAY) },
    { id: "hst_hel1_01", provider: "hetzner", region: "hel1", sku: "cpx41", ip: "65.21.140.203", status: "ready", cpu: 8, ram_mb: 16384, disk_gb: 240, cpu_used_pct: 71, ram_used_pct: 64, disk_used_pct: 52, box_count: 2, price_month: 27.6, price_hour: 0.0442, billing: "pool", created_at: ago(25 * DAY) },
    { id: "hst_ctb_eu_01", provider: "contabo", region: "eu-de", sku: "cloud-vps-20", ip: "167.86.94.51", status: "ready", cpu: 6, ram_mb: 12288, disk_gb: 200, cpu_used_pct: 23, ram_used_pct: 41, disk_used_pct: 18, box_count: 2, price_month: 8.5, price_hour: 0.0118, billing: "pool", created_at: ago(20 * DAY) },
    { id: "hst_vlt_sgp_01", provider: "vultr", region: "sgp", sku: "vc2-4c-8gb", ip: "139.180.141.66", status: "ready", cpu: 4, ram_mb: 8192, disk_gb: 160, cpu_used_pct: 12, ram_used_pct: 22, disk_used_pct: 9, box_count: 3, price_month: 40, price_hour: 0.06, billing: "burst", created_at: ago(5 * HOUR) },
    // v1.4: host một-box trên Sandbox API (tính theo giây, xoá cùng box): E2B dial-in (T2), Upstash Box shim (T3)
    { id: "hst_sbx_e2b_01", provider: "e2b", region: "global", sku: "base", ip: "", status: "ready", cpu: 2, ram_mb: 512, disk_gb: 10, cpu_used_pct: 31, ram_used_pct: 44, disk_used_pct: 12, box_count: 1, price_month: 79.497, price_hour: 0.1089, billing: "sandbox", driver: "sandbox", provider_ref: "i7k2m9x0q4", created_at: ago(40 * MIN) },
    { id: "hst_sbx_ups_01", provider: "upstash-box", region: "us-east-1", sku: "small", ip: "", status: "ready", cpu: 1, ram_mb: 2048, disk_gb: 10, cpu_used_pct: 8, ram_used_pct: 15, disk_used_pct: 5, box_count: 1, price_month: 60.444, price_hour: 0.0828, billing: "sandbox", driver: "shim", provider_ref: "box_9QeL2v", created_at: ago(18 * MIN) },
  ];

  const mk = (b: Partial<Box> & Pick<Box, "id" | "slug" | "agent_id" | "status" | "lifecycle" | "template" | "provider" | "region" | "host_id">): Box => {
    const ip = hosts.find((h) => h.id === b.host_id)?.ip.replace(/\./g, "-") ?? "0-0-0-0";
    return {
      agent_name: agentName(b.agent_id),
      spec: { cpu: 2, ram_mb: 2048, disk_gb: 20 },
      url: b.status === "running" ? `https://${b.slug}-${ip}.sslip.io` : null,
      ttl_seconds: b.lifecycle === "task" ? 4 * 3600 : null,
      idle_minutes: b.lifecycle === "service" ? null : 15,
      budget_usd: 2,
      spent_usd: 0,
      cost_per_hour: 0.01,
      placement_reason: "",
      ports: [],
      secrets: [],
      created_at: ago(3 * DAY),
      last_active_at: ago(5 * MIN),
      ...b,
    } as Box;
  };

  const boxes: MockState["boxes"] = [
    mk({ id: "box_7f3a91", slug: "shopee-crawl-01", agent_id: "agt_shopee", status: "running", lifecycle: "task", template: "python-browser", provider: "hetzner", region: "fsn1", host_id: "hst_fsn1_01", spec: { cpu: 2, ram_mb: 4096, disk_gb: 25 }, cost_per_hour: 0.0078, budget_usd: 1.5, placement_reason: "Pool Hetzner fsn1 còn trống 4 vCPU → chi phí biên ≈ $0; agent cho phép fsn1; egress allowlist khớp.", ports: [], secrets: ["SHOPEE_COOKIE"], created_at: ago(5 * HOUR), last_active_at: ago(1 * MIN) }),
    mk({ id: "box_2c81e4", slug: "shopee-crawl-02", agent_id: "agt_shopee", status: "sleeping", lifecycle: "task", template: "python-browser", provider: "vultr", region: "sgp", host_id: "hst_vlt_sgp_01", spec: { cpu: 2, ram_mb: 4096, disk_gb: 25 }, cost_per_hour: 0.03, budget_usd: 1.0, placement_reason: "Pool châu Âu đầy lúc 06:10 → burst Vultr sgp theo giờ (rẻ hơn E2B cho thời lượng dự kiến 3 giờ, gần sàn TMĐT VN).", secrets: ["SHOPEE_COOKIE"], created_at: ago(9 * HOUR), last_active_at: ago(2 * HOUR) }),
    mk({ id: "box_a9d054", slug: "kecon-story-gen", agent_id: "agt_kecon", status: "running", lifecycle: "session", template: "node", provider: "hetzner", region: "hel1", host_id: "hst_hel1_01", spec: { cpu: 4, ram_mb: 8192, disk_gb: 40 }, cost_per_hour: 0.0221, budget_usd: 4, placement_reason: "Session dài → T1 pool; hel1 có RAM trống 8 GB; Claude Code agent chỉ cho phép Hetzner.", ports: [3000], secrets: ["ANTHROPIC_API_KEY", "ELEVENLABS_API_KEY"], created_at: ago(2 * DAY), last_active_at: ago(3 * MIN) }),
    mk({ id: "box_51be07", slug: "kecon-tts", agent_id: "agt_kecon", status: "sleeping", lifecycle: "session", template: "python", provider: "hetzner", region: "fsn1", host_id: "hst_fsn1_01", cost_per_hour: 0.0039, budget_usd: 2, placement_reason: "Pool fsn1; đã ngủ sau 15 phút rảnh (Reaper), snapshot lưu tại R2.", secrets: ["ELEVENLABS_API_KEY"], created_at: ago(4 * DAY), last_active_at: ago(7 * HOUR) }),
    mk({ id: "box_c4410f", slug: "replingo-api", agent_id: "agt_replingo", status: "running", lifecycle: "service", template: "node", provider: "contabo", region: "eu-de", host_id: "hst_ctb_eu_01", spec: { cpu: 2, ram_mb: 4096, disk_gb: 30 }, cost_per_hour: 0.0029, budget_usd: 8, placement_reason: "Service 24/7 → pool tháng rẻ nhất (Contabo $8.5/tháng cho 6 vCPU).", ports: [8080], secrets: ["OPENAI_API_KEY"], created_at: ago(19 * DAY), last_active_at: ago(30 * 1000) }),
    mk({ id: "box_e07b22", slug: "replingo-eval", agent_id: "agt_replingo", status: "provisioning", lifecycle: "task", template: "python", provider: "vultr", region: "sgp", host_id: "hst_vlt_sgp_01", cost_per_hour: 0.015, budget_usd: 0.5, placement_reason: "Contabo eu-de chậm boot (>60s) → task ngắn đặt ở burst Vultr sgp đang có sẵn.", secrets: ["OPENAI_API_KEY"], created_at: ago(1 * MIN), last_active_at: null }),
    mk({ id: "box_5e2b70", slug: "replingo-fuzz", agent_id: "agt_replingo", status: "running", lifecycle: "task", template: "python", provider: "e2b", region: "global", host_id: "hst_sbx_e2b_01", spec: { cpu: 2, ram_mb: 512, disk_gb: 10 }, cost_per_hour: 0.1089, budget_usd: 0.5, placement_reason: "Code không tin cậy (fuzz input người dùng) → sandbox E2B microVM, tính theo giây; boxd dial-in (T2), xoá sandbox khi box kết thúc.", secrets: [], created_at: ago(40 * MIN), last_active_at: ago(2 * MIN) }),
    mk({ id: "box_u5b3c1", slug: "hark-sbx-scratch", agent_id: "agt_hark", status: "running", lifecycle: "task", template: "base", provider: "upstash-box", region: "us-east-1", host_id: "hst_sbx_ups_01", spec: { cpu: 1, ram_mb: 2048, disk_gb: 10 }, cost_per_hour: 0.0828, budget_usd: 0.3, placement_reason: "Task ngắn, pool đầy → Upstash Box qua shim chỉ exec (T3): không terminal, không snapshot.", secrets: [], created_at: ago(18 * MIN), last_active_at: ago(5 * MIN) }),
    mk({ id: "box_9a6d13", slug: "n8n-main", agent_id: "agt_n8n", status: "running", lifecycle: "service", template: "base", provider: "contabo", region: "eu-de", host_id: "hst_ctb_eu_01", spec: { cpu: 1, ram_mb: 2048, disk_gb: 20 }, cost_per_hour: 0.0015, budget_usd: 4, placement_reason: "Service → pool Contabo; cùng host với replingo-api để tiết kiệm.", ports: [5678], secrets: ["TELEGRAM_BOT_TOKEN"], created_at: ago(17 * DAY), last_active_at: ago(4 * MIN) }),
    mk({ id: "box_3f0c88", slug: "hark-research", agent_id: "agt_hark", status: "running", lifecycle: "session", template: "python-browser", provider: "hetzner", region: "hel1", host_id: "hst_hel1_01", spec: { cpu: 2, ram_mb: 4096, disk_gb: 25 }, cost_per_hour: 0.011, budget_usd: 3, placement_reason: "Session → pool hel1; cần browser, Hetzner hỗ trợ Chromium headless.", secrets: ["OPENAI_API_KEY"], created_at: ago(6 * HOUR), last_active_at: ago(40 * 1000) }),
    mk({ id: "box_b18f6e", slug: "hark-scratch", agent_id: "agt_hark", status: "failed", lifecycle: "task", template: "base", provider: "vultr", region: "sgp", host_id: "hst_vlt_sgp_01", spec: { cpu: 1, ram_mb: 1024, disk_gb: 10 }, cost_per_hour: 0.0075, budget_usd: 0.3, placement_reason: "Task ngắn → burst Vultr sgp.", created_at: ago(3 * HOUR), last_active_at: ago(2 * HOUR) }),
  ];
  boxes.find((b) => b.id === "box_e07b22")!._ready_at = now + 12_000;

  // Ledger 7 ngày: mỗi box có compute theo ngày + vài khoản storage/egress/snapshot
  const ledger: LedgerEntry[] = [];
  let lid = 1;
  const startOfToday = new Date(now);
  startOfToday.setUTCHours(0, 0, 0, 0);
  const dailyBase: Record<string, number> = {
    box_7f3a91: 0.19, box_2c81e4: 0.21, box_a9d054: 0.26, box_51be07: 0.05, box_c4410f: 0.07,
    box_9a6d13: 0.036, box_3f0c88: 0.18, box_b18f6e: 0.02,
  };
  for (let d = 6; d >= 0; d--) {
    const dayStart = startOfToday.getTime() - d * DAY;
    for (const b of boxes) {
      const base = dailyBase[b.id];
      if (!base) continue;
      if (b.id === "box_b18f6e" && d > 0) continue;
      if (b.id === "box_2c81e4" && d > 1) continue;
      const hoursIntoDay = d === 0 ? Math.max(1, (now - dayStart) / HOUR) : 24;
      const factor = d === 0 ? hoursIntoDay / 24 + 0.15 : 0.7 + rand() * 0.6;
      const amt = r4(base * factor * (b.agent_id === "agt_shopee" && d === 0 ? 1.9 : 1));
      const ts = d === 0 ? now - rand() * Math.min(hoursIntoDay, 6) * HOUR : dayStart + (8 + rand() * 12) * HOUR;
      ledger.push({ id: `led_${String(lid++).padStart(5, "0")}`, ts: iso(ts), agent_id: b.agent_id, box_id: b.id, provider: b.provider, kind: "compute", amount_usd: amt, note: `${b.slug} · ${b.provider === "vultr" ? "burst theo giờ" : "phần pool phân bổ"}` });
      if (rand() < 0.35) {
        ledger.push({ id: `led_${String(lid++).padStart(5, "0")}`, ts: iso(ts + 20 * MIN), agent_id: b.agent_id, box_id: b.id, provider: b.provider, kind: rand() < 0.5 ? "egress" : "storage", amount_usd: r4(0.002 + rand() * 0.012), note: rand() < 0.5 ? "egress ra Internet" : "volume /memory" });
      }
      if (rand() < 0.15) {
        ledger.push({ id: `led_${String(lid++).padStart(5, "0")}`, ts: iso(ts + 40 * MIN), agent_id: b.agent_id, box_id: b.id, provider: b.provider, kind: "snapshot", amount_usd: r4(0.001 + rand() * 0.004), note: "snapshot lưu R2" });
      }
    }
  }

  const providers: Provider[] = [
    { id: "hetzner", name: "Hetzner Cloud", connected: true, tier: "T1", last_check_at: ago(12 * MIN), error: null, implemented: true, chain_rank: 1, token_source: "env", group: "vps", adapter: "api" },
    { id: "vultr", name: "Vultr", connected: true, tier: "T1", last_check_at: ago(12 * MIN), error: null, implemented: true, chain_rank: 2, token_source: "api", group: "vps", adapter: "api" },
    { id: "digitalocean", name: "DigitalOcean", connected: false, tier: "T1", last_check_at: null, error: null, implemented: true, chain_rank: 3, group: "vps", adapter: "api" },
    { id: "contabo", name: "Contabo", connected: true, tier: "T1", last_check_at: ago(13 * MIN), error: null, implemented: true, chain_rank: 4, token_source: "api", group: "vps", adapter: "api" },
    { id: "netcup", name: "netcup", connected: false, tier: "T1", last_check_at: ago(2 * DAY), error: "Token hết hạn (401 từ SCP API)", implemented: true, chain_rank: 5, group: "vps", adapter: "api" },
    { id: "local", name: "Máy cục bộ (Incus)", connected: false, tier: "T1", last_check_at: null, error: null, implemented: true, group: "vps", adapter: "self" },
    ...sandboxProviderRows(),
  ];

  const fetched = ago(3 * HOUR);
  const offer = (provider: string, sku: string, region: string, cpu: number, ram_gb: number, disk_gb: number, price_month: number, price_hour: number, min_billing_minutes: number, confidence: Offer["confidence"]): Offer => ({
    provider, sku, region, cpu, ram_mb: ram_gb * 1024, disk_gb, price_hour, price_month, min_billing_minutes,
    source: `battleships.dev/data/providers/${provider}.json`, fetched_at: fetched, confidence,
  });
  const offers: Offer[] = [
    offer("contabo", "cloud-vps-20", "eu-de", 6, 12, 200, 8.5, 0.0118, 43200, "verified"),
    offer("netcup", "rs-1000-g11", "nbg", 4, 8, 256, 9.2, 0.0128, 43200, "unverified"),
    offer("hetzner", "cax31", "fsn1", 8, 16, 160, 15.9, 0.0255, 60, "verified"),
    offer("hetzner", "cx42", "fsn1", 8, 16, 160, 19.4, 0.0311, 60, "verified"),
    offer("hetzner", "cx42", "hel1", 8, 16, 160, 19.4, 0.0311, 60, "stock_limited"),
    offer("hetzner", "cpx41", "hel1", 8, 16, 240, 27.6, 0.0442, 60, "verified"),
    offer("hetzner", "cx22", "fsn1", 2, 4, 40, 4.6, 0.0074, 60, "verified"),
    offer("vultr", "vc2-4c-8gb", "sgp", 4, 8, 160, 40, 0.06, 60, "verified"),
    offer("vultr", "vhf-2c-4gb", "sgp", 2, 4, 128, 24, 0.036, 60, "verified"),
    offer("digitalocean", "s-4vcpu-8gb", "sgp1", 4, 8, 160, 48, 0.0714, 60, "unverified"),
    offer("digitalocean", "s-2vcpu-4gb", "sgp1", 2, 4, 80, 24, 0.0357, 60, "unverified"),
    offer("contabo", "cloud-vps-10", "sin", 4, 8, 75, 6.9, 0.0096, 43200, "stock_limited"),
    offer("netcup", "vps-1000-g11", "vie", 4, 8, 256, 7.4, 0.0101, 43200, "unverified"),
    offer("digitalocean", "s-1vcpu-1gb", "fra1", 1, 1, 25, 6, 0.0089, 1, "verified"),
  ];

  const keys: ApiKey[] = [
    { id: "key_admin01", agent_id: null, name: "admin (dashboard)", prefix: "bx_live_ad9F", scopes: ["admin"], created_at: ago(40 * DAY), last_used_at: ago(1 * MIN), revoked_at: null },
    { id: "key_shp01", agent_id: "agt_shopee", name: "crawler-prod", prefix: "bx_live_s7Qe", scopes: ["boxes:write", "exec"], created_at: ago(34 * DAY), last_used_at: ago(2 * MIN), revoked_at: null },
    { id: "key_shp00", agent_id: "agt_shopee", name: "crawler-cu", prefix: "bx_live_k2Lm", scopes: ["boxes:write", "exec"], created_at: ago(34 * DAY), last_used_at: ago(20 * DAY), revoked_at: ago(19 * DAY) },
    { id: "key_kec01", agent_id: "agt_kecon", name: "claude-code laptop", prefix: "bx_live_c9Xa", scopes: ["boxes:write", "exec", "files"], created_at: ago(29 * DAY), last_used_at: ago(6 * MIN), revoked_at: null },
    { id: "key_rep01", agent_id: "agt_replingo", name: "replingo-sdk", prefix: "bx_live_r3Tt", scopes: ["boxes:write", "exec"], created_at: ago(21 * DAY), last_used_at: ago(14 * MIN), revoked_at: null },
    { id: "key_n8n01", agent_id: "agt_n8n", name: "n8n http node", prefix: "bx_live_n8Wd", scopes: ["exec"], created_at: ago(18 * DAY), last_used_at: ago(41 * MIN), revoked_at: null },
    { id: "key_hrk01", agent_id: "agt_hark", name: "hark-main", prefix: "bx_live_h4Rk", scopes: ["boxes:write", "exec", "files", "snapshots"], created_at: ago(40 * DAY), last_used_at: ago(1 * MIN), revoked_at: null },
  ];

  const secrets: Secret[] = [
    { name: "SHOPEE_COOKIE", agents: ["agt_shopee"], created_at: ago(30 * DAY), rotated_at: ago(3 * DAY) },
    { name: "ANTHROPIC_API_KEY", agents: ["agt_kecon"], created_at: ago(29 * DAY), rotated_at: null },
    { name: "ELEVENLABS_API_KEY", agents: ["agt_kecon"], created_at: ago(20 * DAY), rotated_at: null },
    { name: "OPENAI_API_KEY", agents: ["agt_replingo", "agt_hark"], created_at: ago(40 * DAY), rotated_at: ago(12 * DAY) },
    { name: "TELEGRAM_BOT_TOKEN", agents: ["agt_n8n", "agt_hark"], created_at: ago(17 * DAY), rotated_at: null },
  ];

  const snapshots: Snapshot[] = [
    { id: "snp_kt01", box_id: "box_51be07", size_mb: 812, created_at: ago(7 * HOUR), location: "local:///var/lib/boxd/snapshots/snp_kt01.tar.gz", storage: "s3", remote_url: "s3://boxaz-snapshots/boxaz/snapshots/box_51be07/snp_kt01.tar.gz" },
    { id: "snp_kt00", box_id: "box_51be07", size_mb: 790, created_at: ago(1 * DAY + 3 * HOUR), location: "local:///var/lib/boxd/snapshots/snp_kt00.tar.gz", storage: "local", upload_error: "upload HTTP 403: SignatureDoesNotMatch" },
    { id: "snp_sc02", box_id: "box_2c81e4", size_mb: 1430, created_at: ago(2 * HOUR), location: "r2://boxaz-snapshots/box_2c81e4/snp_sc02" },
    { id: "snp_kg01", box_id: "box_a9d054", size_mb: 2210, created_at: ago(20 * HOUR), location: "r2://boxaz-snapshots/box_a9d054/snp_kg01" },
    { id: "snp_ra01", box_id: "box_c4410f", size_mb: 640, created_at: ago(1 * DAY), location: "r2://boxaz-snapshots/box_c4410f/snp_ra01" },
    { id: "snp_hr01", box_id: "box_3f0c88", size_mb: 1120, created_at: ago(3 * HOUR), location: "r2://boxaz-snapshots/box_3f0c88/snp_hr01" },
  ];

  const approvals: Approval[] = [
    { id: "apr_001", type: "budget", title: "shopee-crawler đã dùng 85% ngân sách ngày", detail: "Đợt crawl ngành Điện thoại & Phụ kiện còn ~40 trang. Cho thêm $0.50 hôm nay để chạy xong?", amount_usd: 0.5, created_at: ago(9 * MIN), status: "pending" },
    { id: "apr_002", type: "pool_expand", title: "Đề xuất thêm host pool Hetzner CX42 (fsn1)", detail: "Burst Vultr sgp đã chạy 31 giờ tuần này ($1.86). Thêm 1 host tháng $19.40 giúp tiết kiệm dự kiến ~$14/tháng khi tải giữ như hiện tại.", amount_usd: 19.4, created_at: ago(55 * MIN), status: "pending" },
    { id: "apr_003", type: "service_stop", title: "replingo-api sắp bị dừng", detail: "Service đã dùng 92% ngân sách tháng của replingo-bot. Reaper sẽ cho ngủ sau 6 giờ nữa nếu không duyệt thêm.", amount_usd: 2, created_at: ago(2 * HOUR), status: "pending" },
    { id: "apr_000", type: "host_down", title: "Host hst_vlt_sgp_00 mất kết nối", detail: "Đã tự chuyển 2 box sang hst_vlt_sgp_01.", amount_usd: null, created_at: ago(2 * DAY), status: "approved" },
  ];

  // Jobs + logs
  const jobs: Job[] = [];
  const logs: Record<string, string> = {};
  let jid = 1;
  const addJob = (box_id: string, command: string, exit: number, dur: number, minsAgo: number, tokens: number, stdout: string, stderr = "", files: string[] = []) => {
    const id = `job_${String(jid++).padStart(4, "0")}`;
    const log_id = `log_${id.slice(4)}`;
    const created = now - minsAgo * MIN;
    jobs.push({
      id, box_id, command, status: exit === 0 ? "done" : "failed",
      result: { exit_code: exit, duration_ms: dur, stdout_tail: stdout, stderr_tail: stderr, truncated: tokens > 0, log_id, files_changed: files, tokens_saved: tokens },
      created_at: iso(created), finished_at: iso(created + dur),
    });
    logs[log_id] = `$ ${command}\n${stdout}\n${stderr ? `[stderr]\n${stderr}\n` : ""}[exit ${exit}] ${(dur / 1000).toFixed(1)}s\n`;
  };
  addJob("box_7f3a91", "uv pip install -r requirements.txt", 0, 8400, 290, 1830, "Resolved 23 packages in 412ms\nInstalled 23 packages in 1.9s", "", ["/work/.venv"]);
  addJob("box_7f3a91", "python crawl.py --cat dien-thoai-phu-kien --pages 1-60", 0, 1_820_000, 240, 48210, "[crawl] trang 1/60 · 48 sản phẩm\n...\n[crawl] trang 60/60 · 47 sản phẩm\nĐã lưu 2.874 sản phẩm → /work/out/dien-thoai.jsonl", "", ["/work/out/dien-thoai.jsonl"]);
  addJob("box_7f3a91", "python crawl.py --cat thoi-trang-nu --pages 1-80", 0, 2_410_000, 95, 61320, "[crawl] trang 80/80 · 50 sản phẩm\nĐã lưu 3.910 sản phẩm → /work/out/thoi-trang-nu.jsonl", "", ["/work/out/thoi-trang-nu.jsonl"]);
  addJob("box_7f3a91", "python dedupe.py out/*.jsonl", 0, 12_300, 20, 2210, "Gộp 6.784 dòng → 6.512 sản phẩm duy nhất (bỏ 272 trùng)", "", ["/work/out/merged.jsonl"]);
  addJob("box_7f3a91", "python crawl.py --cat nha-cua-doi-song --pages 1-40", 1, 64_000, 3, 5120, "[crawl] trang 1/40 · 48 sản phẩm\n[crawl] trang 2/40 · 0 sản phẩm", "playwright._impl._errors.TimeoutError: Timeout 30000ms exceeded.\n  waiting for selector \".shopee-search-item-result__item\"\nGợi ý: cookie có thể đã hết hạn — xoay vòng SHOPEE_COOKIE.");
  addJob("box_a9d054", "pnpm install --frozen-lockfile", 0, 21_300, 600, 3400, "Packages: +812\nDone in 21.3s", "", ["/work/node_modules"]);
  addJob("box_a9d054", "pnpm test -- story-engine", 0, 34_100, 52, 9100, "✓ 48 tests passed (story-engine)\n✓ snapshot: truyen-co-tich/tam-cam.md", "");
  addJob("box_a9d054", "node scripts/gen-story.mjs --theme 'chú mèo đi học' --age 5", 0, 41_800, 8, 1200, "Đã tạo /work/stories/chu-meo-di-hoc.md (612 từ, 5 cảnh)", "", ["/work/stories/chu-meo-di-hoc.md"]);
  addJob("box_c4410f", "pm2 reload replingo-api", 0, 2100, 300, 0, "[PM2] Reloading process replingo-api\n[PM2] ✓ done");
  addJob("box_c4410f", "curl -s localhost:8080/health", 0, 90, 4, 0, '{"ok":true,"uptime_s":18234}');
  addJob("box_9a6d13", "docker compose pull && docker compose up -d", 0, 47_000, 1440, 6200, "n8n Pulled\nContainer n8n Started");
  addJob("box_3f0c88", "python research.py --topic 'giá VPS châu Á 2026'", 0, 312_000, 35, 22800, "Đã đọc 18 nguồn, ghi /memory/notes/vps-asia.md", "", ["/memory/notes/vps-asia.md"]);
  addJob("box_b18f6e", "apt-get install -y build-essential", 100, 61_000, 130, 3900, "Reading package lists...", "E: Could not get lock /var/lib/dpkg/lock-frontend. It is held by process 812 (unattended-upgr)\nGợi ý: đợi unattended-upgrades xong hoặc tạo lại box.");

  const events: BoxazEvent[] = [];
  let eid = 1;
  const ev = (minsAgo: number, actor: string, box_id: string | null, agent_id: string | null, type: string, message: string, cost_usd: number | null = null) => {
    events.push({ id: `evt_${String(eid++).padStart(5, "0")}`, ts: iso(now - minsAgo * MIN), actor, box_id, agent_id, type, message, cost_usd });
  };
  ev(300, "agent:shopee-crawler", "box_7f3a91", "agt_shopee", "box.created", "Tạo box shopee-crawl-01 (python-browser, 2 vCPU/4 GB) tại hetzner/fsn1");
  ev(298, "scheduler", "box_7f3a91", "agt_shopee", "box.placed", "Đặt vào hst_fsn1_01: pool còn trống, chi phí biên ≈ $0");
  ev(296, "system", "box_7f3a91", "agt_shopee", "box.running", "Box sẵn sàng sau 41s");
  ev(128, "system", "box_b18f6e", "agt_hark", "box.failed", "Box chuyển sang failed sau 3 lần thử");
  ev(120, "reaper", "box_2c81e4", "agt_shopee", "box.sleeping", "Ngủ sau 15 phút rảnh, snapshot snp_sc02 (1.4 GB)", 0.0021);
  ev(55, "scheduler", null, null, "approval.created", "Đề xuất thêm host pool Hetzner CX42 (fsn1)");
  ev(9, "budget", null, "agt_shopee", "budget.warning", "shopee-crawler đạt 85% ngân sách ngày");
  ev(4, "user:anh", null, null, "settings.updated", "Đổi giờ gửi báo cáo ngày sang 21:00");
  ev(1, "agent:replingo-bot", "box_e07b22", "agt_replingo", "box.created", "Tạo box replingo-eval (python) — đang dựng tại vultr/sgp");

  // Sự kiện box.exec theo đúng format của Core: "exit=<code> <ms>ms: <command>" (+ phần mở rộng tokens_saved/log)
  for (const j of jobs) {
    const r = j.result!;
    events.push({
      id: `evt_${String(eid++).padStart(5, "0")}`,
      ts: j.finished_at ?? j.created_at,
      actor: `agent:${boxes.find((b) => b.id === j.box_id)?.agent_name ?? "?"}`,
      box_id: j.box_id,
      agent_id: boxes.find((b) => b.id === j.box_id)?.agent_id ?? null,
      type: "box.exec",
      message: `exit=${r.exit_code} ${r.duration_ms}ms: ${j.command} tokens_saved=${r.tokens_saved} log=${r.log_id}`,
      cost_usd: null,
    });
  }

  // Files theo box
  const files: Record<string, Record<string, string>> = {};
  for (const b of boxes) {
    const f: Record<string, string> = {
      "/memory/notes.md": `# Ghi nhớ của ${b.agent_name}\n\n- Box ${b.slug} dùng template ${b.template}.\n- Nhớ nén output trước khi trả về để tiết kiệm token.\n`,
      "/memory/state.json": JSON.stringify({ last_task: b.slug, checkpoints: 3, updated_at: iso(now - 10 * MIN) }, null, 2),
      "/work/README.md": `# ${b.slug}\n\nThư mục làm việc của agent ${b.agent_name}.\n`,
    };
    if (b.agent_id === "agt_shopee") {
      f["/work/crawl.py"] = `import asyncio, json, sys\nfrom playwright.async_api import async_playwright\n\nCATS = sys.argv[2] if len(sys.argv) > 2 else "dien-thoai-phu-kien"\n\nasync def main():\n    async with async_playwright() as p:\n        b = await p.chromium.launch()\n        page = await b.new_page()\n        await page.goto(f"https://shopee.vn/{CATS}")\n        # ...\n\nasyncio.run(main())\n`;
      f["/work/requirements.txt"] = "playwright==1.48.0\norjson==3.10.7\n";
      f["/work/out/merged.jsonl"] = Array.from({ length: 6 }, (_, i) => JSON.stringify({ id: 2200000 + i, name: ["Ốp lưng iPhone 15", "Cáp sạc nhanh 65W", "Tai nghe bluetooth", "Kính cường lực", "Sạc dự phòng 20000mAh", "Giá đỡ điện thoại"][i], price_vnd: [39000, 89000, 249000, 25000, 359000, 59000][i], sold: [12000, 8400, 3100, 22000, 5600, 1900][i] })).join("\n") + "\n";
      f["/memory/selectors.json"] = JSON.stringify({ item: ".shopee-search-item-result__item", price: "[data-sqe=price]" }, null, 2);
    }
    if (b.agent_id === "agt_kecon") {
      f["/work/stories/chu-meo-di-hoc.md"] = "# Chú mèo đi học\n\nSáng hôm ấy, Mướp dậy thật sớm. Hôm nay là ngày đầu tiên chú đến lớp...\n";
      f["/work/package.json"] = JSON.stringify({ name: "kecon-story-engine", private: true, scripts: { test: "vitest" } }, null, 2);
    }
    if (b.agent_id === "agt_replingo") {
      f["/work/server.js"] = "import express from 'express';\nconst app = express();\napp.get('/health', (_, res) => res.json({ ok: true }));\napp.listen(8080);\n";
    }
    if (b.agent_id === "agt_hark") {
      f["/memory/notes/vps-asia.md"] = "# Giá VPS châu Á 2026\n\n- Vultr sgp vc2-4c-8gb: $0.06/giờ\n- Contabo sin cloud-vps-10: $6.9/tháng (hay hết hàng)\n";
    }
    files[b.id] = f;
  }

  const settings: Settings = {
    notify: {
      telegram_chat_id: "-1002233445566", email: "itroot2015@gmail.com", events: ["daily_report", "budget", "approval", "box_failed", "host_down"], daily_report_hour: 21,
      timezone: "Asia/Ho_Chi_Minh", smtp: { host: "smtp.gmail.com", port: 587, username: "itroot2015@gmail.com", from: "Boxaz <itroot2015@gmail.com>", tls: "starttls" },
    },
    reaper: { idle_minutes: 15, default_ttl_seconds: 14400, keep_snapshots: 3, snapshot_retention_days: 30 },
    egress_deny_by_default: false,
    base_domain: "sslip.io",
    url_token_protect: true,
    disable_tunnel: false,
    gateway: { domain: "", provider_preview: "" },
    budget_month_usd: 60,
    maintenance: { enabled: false, message: "Boxaz đang bảo trì, box vẫn chạy bình thường nhưng không tạo được box mới." },
    features: { gpu: false },
  };

  const quotas: Quotas = { max_boxes_per_agent: 5, max_cpu_per_box: 8, max_ram_mb_per_box: 16384, max_disk_gb_per_box: 100, rate_limit_per_minute: 600 };

  const users: AdminUser[] = [
    { id: "usr_owner", email: "owner@boxaz.dev", name: "Owner", email_verified: true, platform_admin: true, created_at: ago(0), workspaces: [{ id: "ws_default", name: "default", role: "owner", max_boxes: 5, max_ttl_hours: 12 }] },
    { id: "usr_bob", email: "bob@example.com", email_verified: false, platform_admin: false, created_at: ago(0), workspaces: [{ id: "ws_bob", name: "Bob Labs", role: "owner", max_boxes: 5, max_ttl_hours: 12 }] },
  ];

  return {
    agents, boxes, hosts, providers, offers, ledger, keys, templates: TEMPLATES.map((t) => ({ ...t })), secrets, snapshots,
    approvals, events, jobs, logs, files, settings, quotas, users, first_call_at: {}, seq: 1000, started_at: now,
    notify_secrets: { telegram: true, smtp: false },
    deliveries: [
      { ts: ago(21 * HOUR), kind: "daily_report", channel: "telegram", ok: true, title: "Báo cáo chi phí ngày" },
      { ts: ago(9 * MIN), kind: "budget", channel: "telegram", ok: true, title: "shopee-crawler đã dùng 85% ngân sách ngày" },
      { ts: ago(9 * MIN), kind: "budget", channel: "email", ok: false, error: "email not configured (smtp host, from, recipients)", title: "shopee-crawler đã dùng 85% ngân sách ngày" },
    ],
    storage: {
      provider: "r2", endpoint: "https://8c1f2a.r2.cloudflarestorage.com", region: "auto", bucket: "my-snapshots", prefix: "team-a",
      access_key_id: "b1c5f0e3a2d94e7f", configured: true, source: "settings", has_secret: true, keep_snapshots: 3, snapshot_retention_days: 30,
      fallback: "local: snapshots stay on their host (tar.gz / incus snapshot); restore and fork only on that host",
    },
  };
}
