# Boxaz — competitor & feature-gap research (agent sandboxes / dev boxes)

Researched 26 Nov 2026 (Asia/Bangkok). Every claim carries a source URL. Prices are list prices in USD unless marked, read from the URL shown (vendor pages fetched on 26 Nov 2026 where marked **[fetched]**; others from dated third-party reviews and marked as such). Legend in tables: ✓ = documented, ◐ = partial / beta / enterprise-only, ✗ = documented absent, **?** = not verified in this pass (treat as unknown, not as "no").

---

## 0. Market events that change the picture (late 2026)

| Event | Date | Source |
|---|---|---|
| **Runloop shut down** — joined OpenAI; transition support ended 13 Nov 2026; migration guides point to Tensorlake, Modal, Freestyle | Nov 2026 | https://docs.runloop.ai/docs/transitions/overview · https://www.linkedin.com/feed/update/urn:li:activity:7528212053472268288 |
| **Ona (ex-Gitpod) Cloud wound down 31 Oct 2026**; Ona joined OpenAI (Codex team); only Enterprise VPC runners remain | Oct 2026 | https://agentsandboxes.org/sandboxes/ona-gitpod · https://druce.ai/governance/wiki/vendors/ona |
| **Blaxel acquired by Baseten** (platform continues) | 10 Sep 2026 | https://agentsandboxes.org/sandboxes/blaxel/ |
| **Daytona 2.0** launched (Firecracker microVMs, Starter/Team plans; Team $250/mo) alongside original platform; Daytona core went closed-source Jun 2026 | Nov 2026 | https://www.agent37.com/blog/daytona-pricing · https://bex.co/blog/2026/09/24/open-sandbox-router-one-sandbox-api-e2b-modal-kubernetes |
| CodeSandbox SDK → **Together Code Sandbox** (allowlist access via Together sales) | 2025–26 | https://agentsandboxes.org/sandboxes/codesandbox/ |
| Hyperscalers entered: **AWS Lambda MicroVMs** (Jun 2026), **Azure Container Apps Sandboxes** (preview Jun 2026), **Google Agent Substrate** (1.0, donated to CNCF, GA on GKE Nov 2026) + Agent Compute Engine (preview) | 2026 | https://aws.amazon.com/about-aws/whats-new/2026/06/aws-lambda-microvms · https://techcommunity.microsoft.com/blog/appsonazureblog/introducing-azure-container-apps-sandboxes-secure-infrastructure-for-agentic-wor/4524131 · https://opensource.googleblog.com/2026/11/sandboxing-untrusted-ai-agents-at-scale.html · https://cloud.google.com/blog/topics/ai-infrastructure/introducing-agent-compute-engine-and-expanded-agent-governance?hl=en |
| **Docker Cloud Sandboxes** (microVM, `sbx move` laptop↔cloud) | 24 Sep 2026 | https://createos.sh/blogs/ai-agent-sandbox-platforms |
| **Billing vendors consolidated**: Stripe bought Metronome (Jan 2026), Adyen bought Orb (Jul 2026), Kong bought OpenMeter (Sep 2025); **Lemon Squeezy closing** (no new stores from 16 Nov 2026, stops 31 Mar 2027) | 2025–26 | https://paywithlocus.com/guides/usage-based-billing-software **[fetched]** · https://saasyland.com/blog/polar-vs-stripe-vs-lemon-squeezy |
| **Hetzner raised cloud prices twice** (1 Apr and 15 Jun 2026; CX/CAX +31–38%, CPX/CCX +113–175%); CX/CAX often sold out | 2026 | https://docs.hetzner.com/general/infrastructure-and-availability/price-adjustment · https://hostingsift.com/blog/is-hetzner-still-cheapest-vps-2026 |

**Direct competitors to Boxaz's "neutral layer" positioning** (important — not just sandbox vendors):
- **ComputeSDK** — "one router for sandboxes and Actions", TS SDK + REST + CLI + MCP across ~39 providers, managed compute without vendor keys or BYO keys, fallback order, public weekly benchmarks; Devboxes "coming soon". https://docs.computesdk.com/ · https://www.computesdk.com/llms.txt · https://www.computesdk.com/discover
- **Crabbox** — orchestrator over 60+ providers incl. Hetzner, AWS, Daytona, E2B, Modal, K8s, plain SSH hosts. https://roboticforce.io/lab/e2b-vs-crabbox
- **Open Sandbox Router** (capability negotiation, create-time failover, durable sandbox→provider binding) https://bex.co/blog/2026/09/24/open-sandbox-router-one-sandbox-api-e2b-modal-kubernetes ; Cased `sandboxes` lib https://cased.com/blog/2025-10-05-sandboxes ; VibeKit https://blog.logrocket.com/comparing-ai-agent-sandbox-platforms-e2b-modal-daytona-and-more/
- **Sandscale** (newcomer, Nov 2026) — sells *governance* on every plan: Vault, DLP on prompts/MCP tool calls, human approvals, hash-chained audit log; SSO/roles/audit export on Team ($48/member/mo). Overlaps Boxaz's agent policies/approvals. https://shipyardhq.dev/products/sandscale · https://dev.to/sandscale/sandscale-vs-e2b-how-two-firecracker-sandbox-platforms-differ-1hgi

Boxaz differentiators none of them fully cover (keep leaning on these): raw-VPS + Incus as a provider class (real price arbitrage, see §3), per-agent keys/budgets/policies/approvals, A2A, cost ledger + live price feed, VNC/CDP/SFTP/SSH-cert all in one API, i18n dashboard (vi/en/zh/ja).

---

## 1. Feature matrix (features × platforms)

### 1a. Core sandbox platforms

| Feature | E2B | Daytona | Modal | Morph | Fly (Machines / Sprites) | Cloudflare Sandbox | Vercel Sandbox | Blaxel | Northflank | Together/CodeSandbox | Runloop † |
|---|---|---|---|---|---|---|---|---|---|---|---|
| Isolation | Firecracker µVM [1] | container default; Linux/Win VMs; GPU containers [6] | gVisor or VM runtime [11] | VM (Infinibranch) [15] | Firecracker µVM [19][20] | Firecracker µVM per container [23] | Firecracker µVM [27] | µVM [31] | Kata/Cloud Hypervisor; gVisor for GPU [34] | Firecracker µVM [38] | µVM on bare-metal hypervisor [42] |
| Pause/resume **with memory** | ✓ [2] | ✓ on VM sandboxes ("hot snapshots") [7] | ◐ memory snapshots alpha, 7-day TTL [12] | ✓ <250 ms [15] | Machines ✓ suspend [21]; Sprites ✗ (fs checkpoints) [19] | ✗ (fs snapshots beta) [24] | ✗ (fs snapshot on stop) [27] | ✓ standby ~25 ms [31] | ✗ (pause = scale to zero) [34] | ✓ hibernation [38] | ◐ suspend/resume (Pro) [42] |
| Fork / branch | ✓ fork from snapshot [1] | ✓ VM fork [6] | ✓ via snapshots [12] | ✓ branch N copies [16] | Sprites checkpoint/restore [19] | ◐ restore snapshot into new sandbox [24] | ◐ snapshots [27] | ? | ✗ no fork [34] | ✓ live fork (≤5) [39] | ✓ disk snapshot fan-out [43] |
| Persistent volumes | ◐ beta [1] | ✓ shared volumes, per-tenant subpath [8] | ✓ volumes + bucket mounts [11] | ✓ S3-compatible volumes [17] | ✓ Fly Volumes; Sprites 100 GB durable disk [20] | ✓ R2/S3 mount, directory backup [25] | ✓ Drives [28] | ✓ volumes $0.12/GB-mo, Agent Drive [32] | ✓ volumes [34] | ✓ persistent files [38] | ✓ |
| Code interpreter (stateful contexts) | ✓ Code Interpreter SDK [3] | ◐ code execution API [9] | ? | ? | ? | ◐ persistent contexts (moved to "patterns" in 1.0) [26] | ? | ? | ? | ✓ (stated use case) [38] | ? |
| Desktop / computer-use API | ✓ Desktop SDK (XFCE, VNC, click/type) [4] | ✓ Linux/Windows/macOS computer use [9] | ? | ✓ built-in remote desktop [15] | ? | ✗ | ✗ [29] | ? | ? | ? | ✓ browser & computer use [42] |
| GPU | ✗ [1] | ✓ up to 8 GPUs, B300→4090 [10] | ✓ [11] | ? | ? | ? | ✗ | ✗ (Custom only) [32] | ✓ L4→B200 [35] | ✗ [39] | ✗ |
| Git API (clone/commit/push/PR) | ? | ✓ [9] | ? | ✓ Git auth in devboxes [17] | ? | ◐ (git helper being moved out) [26] | ? | ? | ✓ (CI/CD platform) [35] | ✓ [38] | ✓ repo connections [42] |
| Warm pools | ? | ✓ [7] | ◐ via directory snapshots [12] | ? | Machines min-running [21] | ? | ? | n/a (standby) | ? | ? | ? |
| BYOC / VPC / self-host | ✓ Enterprise BYOC; E2B Embed self-host [5] | ✓ custom regions + self-hosted runners [10] | ✗ hosted only [11] | ? | ✗ | ✗ | ✗ | ✓ "bring your own metal" [32] | ✓ self-serve BYOC [35] | ✗ [39] | ◐ VPC (Enterprise) |
| SOC 2 | ? | ✓ Type II (Jul 2026) [10] | ✓ all plans [13] | ? | ✓ Type II, BAA pre-signed [22] | ? | ? | ✓ (badges) [33] | ✓ Type II, HIPAA, ISO 27001 [35] | ? | ✓ Type II [42] |
| Audit logs | ✗ not found in docs [5] | ✓ (API/CLI/dashboard) [9] | ◐ Enterprise [13] | ? | ? | ? | ? | ? | ◐ Enterprise [35] | ? | ✓ (MCP Hub audit) [42] |
| SSO SAML/OIDC (+SCIM) | ? | ◐ Enterprise SAML/OIDC + directory sync [9] | ◐ Enterprise Okta SSO [13] | ? | ? | (Cloudflare acct) | (Vercel acct) | ◐ SAML via $1,600/mo add-on [32] | ◐ Enterprise SAML/SCIM [35] | ? | ? |
| RBAC | ? | ✓ granular roles (Viewer/Developer/…/Auditor) [9] | ◐ Team plan [13] | ? | ? | (Cloudflare acct) | ✓ Vercel "Identity & Access" [28] | ? | ✓ PAYG [35] | ? | ? |
| Lifecycle webhooks | ✓ signed [5] | ✓ sandboxes/snapshots/volumes [9] | ? | ? | ? | ◐ status hooks in Worker [25] | ✓ [28] | ? | ✓ notifications → webhooks [35] | ✓ webhook-driven lifecycle [39] | ? |
| Metrics / OTel export | ✓ metrics API; OTel export (Enterprise) [5] | ? | ✓ real-time metrics/logs [13] | ? | ✓ monitoring [22] | ✓ Workers Logs [25] | ✓ Observability [28] | ✓ "full observability suite" [32] | ✓ log sinks (PAYG) [35] | ? | ✓ |
| MCP | ✓ in-sandbox MCP gateway (200+ tools) [4] | ✓ MCP server [9] | ? | ? | ✓ Fly MCP (mcp.fly.io) [19] | ✓ Cloudflare MCP [25] | ✓ Vercel MCP [28] | ✓ MCP hosting [32] | ? | ? | ✓ MCP Hub [42] |
| Egress control / credential brokering | ✓ allow/deny lists + secret-injecting egress proxy [1][5] | ✓ CIDR/domain allowlists, tier-based [9] | ✓ CIDR + domain (beta) [11] | ? | ✓ Connectors (creds never enter) [19] | ✓ outbound handler injects creds [24] | ✓ firewall + credential brokering [27] | ✓ proxy secret injection, static IPs [32] | ✓ FQDN/IP policies, secrets at egress [34] | ? | ✓ credential gateway [42] |
| Regions | US/EU/APAC [5] | us, eu + custom [10] | selectable at 1.15–1.75× [13] | ? | 18 regions [22] | global edge | 19 regions [28] | 4 [33] | multi-cloud [35] | ? | US |
| Free tier | $100 one-time [1] | $200 one-time, no card [10] | $30/mo (card required) [13] | $0 plan, buy MCUs [18] | $5/mo (card required) [22] | needs $5 Workers Paid [23] | Hobby: 5 CPU-h/mo etc. [28] | up to $200 credits [32] | $20/mo resources [35] | 40 VM-h/mo (Build) [40] | — |
| Pricing model | plan + per-second usage [1] | prepaid wallet, per-second [10] | plan + per-second, invoiced; budgets [13] | MCU credits [18] | per-second, monthly invoice [22] | active-CPU per 10 ms [23] | active CPU + provisioned mem [28] | active GB-RAM·s, top-up tiers [32] | per-second, card at cycle end [35] | VM credits, per minute [40] | — |

† Runloop is shut down (see §0) — kept because its feature set (blueprints, repo connections, credential gateway, MCP Hub, benchmarks) shows what customers valued.

### 1b. Dev-environment & browser platforms

| Feature | Coder | Ona (ex-Gitpod) | Browserbase | Steel |
|---|---|---|---|---|
| Model | self-hosted CDE (Terraform templates); Community free, Premium per user [44] | Enterprise VPC runners only (Ona Cloud closed 31 Oct 2026) [46] | managed cloud browsers [48] | managed or self-hosted (Apache-2.0) browsers [50] |
| Notable features | AI Gateway, Agent Firewall (AI Governance), Coder Agents GA (self-hosted, air-gapped), MCP server [45] | devcontainer.json environments, guardrails/audit, warm pools, SSO [46][47] | Contexts (persisted auth/cookies), recordings/replay, auto-captcha, Verified identity, proxies, webhooks, Model Gateway [48][49] | Profiles, credentials, live view, human hand-off, recordings, HAR, agent traces, captcha, proxies, dedicated IPs [50] |
| Enterprise | RBAC, audit, multi-org (Premium) [45] | SSO/OIDC, audit trails [47] | SSO, HIPAA BAA, DPA on Scale [49] | SSO, SLA on Enterprise; BAA on Scale [50] |
| Free tier | Community free [44] | n/a | 1 browser-hr, 3 concurrent, 15-min sessions [49] | $30 one-time credits (90 days) [50] |
| Billing | annual per user (quote) [44] | OCU credits (earlier) [47] | plan + overage ($0.12/$0.10 per browser-hr) [49] | prepaid credits, auto top-up, refuse at zero; $10 paid deposit to unlock proxies/captcha [50] |

### 1c. Notable 2026 newcomers (one line each)

| Platform | Distinctive | Source |
|---|---|---|
| Tensorlake | µVM, memory checkpoints, clone running sandbox, warm pools, hosted Git + versioned FS, BYOC priced as 30%→10% of compute; prepaid credits with **hard stop at zero** | https://agentsandboxes.org/sandboxes/tensorlake · https://tensorlake.ai/blog/introducing-tensorlake-byoc · https://www.anchorterminal.com/tools/tensorlake-sandboxes |
| Freestyle | full KVM VMs (nested virt, Docker, eBPF), live fork with memory, default-deny network, VPC/WireGuard; Free plan hard stop | https://www.freestyle.sh/docs/vms/pricing-and-limits · https://agentsandboxes.org/sandboxes/freestyle/ |
| Upstash Box | active-CPU billing, memory free; headless browser, git→PR, cron inside box, keep-alive flat rate | https://upstash.com/box · https://upstash.com/pricing/box |
| Sandscale | governance-first (DLP, approvals, hash-chained audit, Vault on every plan); "awake vCPU-hour" billing | https://shipyardhq.dev/products/sandscale |
| AWS Lambda MicroVMs | Firecracker, suspend/resume ≤8 h, HTTPS endpoint per VM | https://aws.amazon.com/blogs/compute/running-self-hosted-ai-agent-sandboxes-with-aws-lambda-microvms |
| ComputeSDK | multi-provider router + public benchmarks (direct competitor) | https://docs.computesdk.com/ |

---

## 2. Prioritized modules/features Boxaz lacks (25)

Priority for a **multi-tenant SaaS launch**: P0 = needed before taking money from strangers; P1 = needed to compete in first 3–6 months; P2 = later / enterprise.

### P0 — launch blockers
1. **Billing & payments module (wallet + metering export)** — turn the existing cost ledger into a per-workspace prepaid wallet: credits, one-time + auto top-up, invoices, tax via a merchant of record, hard stop or grace at zero. Competitors: Daytona wallet + auto top-up [10b], Steel prepaid credits refuse at zero [50], Tensorlake hard stop [n1], Polar/Orb-style credits. See §4 — **Stripe is not available to Vietnam-registered businesses**, which shapes the choice.
2. **Plans, tiers & quotas engine** — per-plan caps on concurrent boxes, vCPU/RAM pool, max continuous runtime, creation rate, secrets count; tiers unlocked by verification/top-up. Competitors: E2B plan table [5], Daytona Tier 1–4 by top-up [10c], Blaxel Tier 0–9 by top-up [32], Vercel concurrency/vCPU-rate quotas [28].
3. **API rate limiting** — per workspace × endpoint limits with `429 + Retry-After`, SDK auto-retry. Competitor: E2B [5], Vercel control-plane quotas [28].
4. **Spend limits, budgets & alerts at workspace level** — hard monthly cap + threshold notifications (Boxaz has per-agent budgets; need tenant-level). Competitors: E2B Limits page [5], Modal workspace budgets/spend limits [13], Vercel Spend Management [28].
5. **Abuse / trust & safety for free tier** — card or paid-deposit verification before risky features (public URLs, open egress, large boxes), mining/scan detection, default-restricted egress on low tiers, kill switch per workspace. Competitors: Daytona restricts network on Tiers 1–2 [9]; Steel requires $10 paid credit before proxies/captcha [50]; Fly Free requires card [22]. Critical for Boxaz because public URLs + raw VPS IPs get you abuse reports and provider bans (Hetzner/Contabo).
6. **Tenant-facing audit log (+ export)** — who/which API key/agent did what to which box, IP, outcome; filter, export CSV/JSON; ideally hash-chained. Competitors: Daytona audit fields & CLI [9], Sandscale hash-chained [n4], Modal/Northflank Enterprise [13][35].
7. **Workspace RBAC + scoped API keys** — roles (Owner, Admin, Developer, Viewer, Billing, Auditor) and per-key scopes/expiry, on top of the new invites. Competitors: Daytona role assignments [9], Modal RBAC (Team) [13], Northflank RBAC [35].
8. **Signed lifecycle webhooks** — box created/started/paused/snapshotted/killed/cost-threshold events, HMAC-signed with retries; complements Telegram/email notifications. Competitors: E2B [5], Daytona [9], Vercel [28].

### P1 — competitive parity
9. **Warm pools per template** — keep N pre-booted boxes per template/region to hide VPS + Incus cold start; bill idle pool to the tenant or absorb. Competitors: Daytona warm pools [7], Tensorlake pools [n1], Ona warm pools [47], Steel reserve pools (Enterprise) [50].
10. **Credential-brokering egress proxy** — secrets injected at the network edge so they never enter the box (extend current egress allowlist + secrets). Competitors: E2B egress proxy [n5], Vercel credential brokering [27], Blaxel proxy [32], Fly Connectors [19], Cloudflare outbound handlers [24], Runloop gateway [42].
11. **Provider-agnostic volumes** — volume objects independent of box lifetime, mount into many boxes, per-tenant subpath, snapshot/backup to S3/R2. Competitors: Daytona volumes [8], Modal volumes [11], Vercel Drives [28], Blaxel volumes/Agent Drive [32].
12. **Memory-snapshot capability flag + normalized pause semantics** — expose `capabilities.memory_snapshot`, `fork`, `gpu` per provider in the price/placement engine so "cheapest box meeting requirements" can require them (Incus supports stateful snapshots via CRIU — verify per host). Competitors: E2B, Morph, Blaxel, Freestyle, Modal (alpha) [2][15][31][12].
13. **Stateful code-interpreter API** — Jupyter-style contexts with rich outputs (charts, dataframes) over exec. Competitors: E2B Code Interpreter SDK [3], Cloudflare persistent contexts [26].
14. **Computer-use action API on top of VNC** — screenshot/click/type/scroll/key endpoints + OpenAI/Anthropic computer-use adapters. Competitors: E2B Desktop [4], Daytona Computer Use [9], Morph desktop [15].
15. **Git module** — clone with short-lived credentials, branch/commit/push, open PR (GitHub/GitLab app). Competitors: Daytona git ops [9], Upstash Box git→PR [n3], Runloop repo connections [42], Tensorlake hosted Git [n1].
16. **Observability export** — per-box CPU/RAM/disk metrics API, OTel (OTLP) export of metrics/logs/traces, log sinks. Competitors: E2B metrics + OTel [5], Northflank log sinks [35], Modal real-time metrics [13].
17. **Regions & data-residency constraints in placement** — `region: eu|us|apac`, "EU-only providers" policy per workspace. Competitors: E2B US/EU/APAC [5], Daytona us/eu + custom [10], Vercel 19 regions [28]. (Natural fit for Boxaz's multi-provider routing.)
18. **BYO provider account (BYOK/BYOC) with platform fee** — tenant plugs in its own Hetzner/Vultr/E2B/Modal keys; Boxaz bills only an orchestration fee. Benchmarks: Northflank BYOC $0.02083/node vCPU-h [35], Tensorlake BYOC 30%→10% of compute [n1], ComputeSDK "bring your own keys" [n6].
19. **Compatibility shims (E2B-compatible API / migration importers)** — accept E2B SDK calls & templates so users switch with a base-URL change; import Runloop/Ona refugees. Competitors: Blaxel migration skill [32], E2BGateway [n7]; Runloop/Ona shutdowns create a migration wave now.
20. **Data lifecycle & compliance basics** — DPA, workspace data export/delete (GDPR), retention settings for logs/snapshots/audit, status page + SLA. Competitors: Fly DPA/BAA [22], Browserbase retention by plan [49], Vercel snapshot retention [28].

### P2 — later / enterprise
21. **SSO (SAML/OIDC) + SCIM** — Enterprise tier. Competitors: Daytona [9], Modal [13], Northflank [35], Browserbase Scale [49], Sandscale Team [n4].
22. **Browser module upgrade** — persistent browser profiles/contexts, session recording/replay, live view + human hand-off, captcha/proxy add-ons on the CDP feature. Competitors: Browserbase [48][49], Steel [50].
23. **Agent governance extras** — DLP scanning of prompts/MCP tool calls and hash-chained, verifiable audit to differentiate Boxaz's existing policies/approvals. Competitor: Sandscale [n4]; Coder Agent Firewall [45].
24. **GPU / Windows / macOS box classes via providers** — route to Modal/Daytona/Northflank GPU or Daytona Windows/macOS when requirements ask. Competitors: Daytona [10], Modal [13], Northflank [35].
25. **Public provider benchmarks & reliability board + startup credits program** — publish Boxaz's provider-health data weekly (marketing + trust), and a credits/promo-code program. Competitors: ComputeSDK weekly benchmarks [n6]; Daytona up to $50k startup credits [10], Fly up to $15k [22], Modal startup/academic credits [13].

(Also seen, lower value for Boxaz now: cron inside boxes — Upstash Box [n3], Blaxel cron jobs [32]; hosted MCP servers — Blaxel [32]; agent benchmark/eval harness — Runloop [42].)

---

## 3. Pricing & free-tier benchmarks

### 3a. Rates and free tiers

| Platform | Compute rate | Plans / fees | Free tier | Source |
|---|---|---|---|---|
| E2B | $0.000014/vCPU-s ($0.0504/h), $0.0000045/GiB-s ($0.0162/h); disk included | Hobby $0; Pro $150/mo (24 h sessions, 100–1,100 concurrent); Enterprise custom | $100 one-time; 10 concurrent, 1 h sessions | https://docs.e2b.dev/billing **[fetched]** · https://docs.e2b.dev/faq/calculate-sandbox-price |
| Daytona | $0.0504/vCPU-h, $0.0162/GiB-h, $0.000108/GiB-h disk after 5 GiB; Windows $0.0858/vCPU-h; GPUs $0.57–$8.99/h | pay-as-you-go wallet; Enterprise custom; (2.0: Team $250/mo) | $200 compute, no card | https://www.daytona.io/pricing **[fetched]** · https://www.agent37.com/blog/daytona-pricing |
| Modal Sandboxes | $0.00003942/core-s ($0.1419/core-h, 1 core = 2 vCPU), $0.00000667/GiB-s ($0.0240/h); billed max(request, usage) | Starter $0; Team $250/mo (+$100 credits); Enterprise | $30/mo credits (payment method required) | https://modal.com/pricing **[fetched]** |
| Vercel Sandbox | $0.128/active-CPU-h, $0.0212/GB-h provisioned mem, $0.60/1M creations, snapshots $0.08/GB-mo | Pro: usage against $20/mo credit | Hobby: 5 active CPU-h, 420 GB-h, 5,000 creations/mo, 45-min sessions, 10 concurrent | https://vercel.com/docs/sandbox/pricing **[fetched]** |
| Cloudflare Containers/Sandbox | $0.000020/vCPU-s active, $0.0000025/GiB-s, $0.00000007/GB-s disk; egress $0.025/GB NA/EU after 1 TB | requires $5/mo Workers Paid (includes 375 vCPU-min, 25 GiB-h, 200 GB-h) | none beyond Workers Paid allotment | https://developers.cloudflare.com/containers/platform/pricing **[fetched]** |
| Blaxel | $0.0000115/GB-RAM-s active (CPU included); snapshots $0.20/GB-mo; volumes $0.12/GB-mo | PAYG; support add-ons $800–$1,600/mo + % usage; HIPAA $250/mo | up to $200 credits; Tier 0 = 10 sandboxes | https://blaxel.ai/pricing **[fetched]** |
| Fly Sprites | $0.0385/CPU-h (actual use), $0.021875/GB-h (actual), hot storage $0.50/GB-mo, cold $0.02/GB-mo | PAYG | Free plan $5 usage/mo, card required | https://fly.io/pricing **[fetched]** |
| Fly Machines | shared-cpu-1x 256 MB $2.19/mo … performance-2x 4 GB $0.0917/h; stopped $0.15/GB rootfs | PAYG | same $5/mo | https://fly.io/pricing **[fetched]** |
| Northflank | $0.01667/vCPU-h, $0.00833/GB-h (e.g. 2 vCPU/4 GB $0.06667/h); BYOC $0.02083/node vCPU-h | PAYG; Enterprise custom | Sandbox plan: up to $20/mo resources, no card | https://northflank.com/pricing **[fetched]** |
| Morph Cloud | 1 MCU = $0.05 = 1 vCPU-h + 4 GB RAM-h + 16 GB disk-h | Developer $0 (buy MCUs); Team $40/mo (1,000 MCU); Scale $250/mo (7,500 MCU) | none (buy credits) | https://cloud.morph.so/web/pricing **[fetched]** |
| Together/CodeSandbox SDK | $0.01486/credit; Nano (2 cores/4 GB) 10 credits/h = $0.1486/h; billed per minute | Pro $12/workspace; Scale $170/workspace | Build: 40 VM-h/mo, 10 concurrent VMs | https://codesandbox.io/docs/learn/credit-usage/credits · https://programcode.net/reviews/codesandbox |
| Freestyle | $0.04032/vCPU-h, $0.0129/GiB-h | Hobby $50/mo min, Pro $500/mo min | Free: 200 vCPU-h + 400 GiB-h/mo, hard stop | https://www.freestyle.sh/docs/vms/pricing-and-limits |
| Tensorlake | $0.07/core-h active (20% floor), $0.015/GB-h; Pro $0.042 / $0.009 | credit packs $5–$20; Pro $250 | Free: 1 sandbox, 2 h sessions | https://www.anchorterminal.com/tools/tensorlake-sandboxes |
| Upstash Box | $0.10/active CPU-h (2 vCPU/4 GB); keep-alive $0.0111/h | PAYG / Fixed | 10 boxes, 5 CPU-h/mo | https://upstash.com/pricing/box |
| Browserbase | $0.12 (Dev) / $0.10 (Startup) per browser-h overage; proxy $12/$10 per GB | Dev $20, Startup $99, Scale custom | 1 browser-h, 3 concurrent, 15-min sessions | https://docs.browserbase.com/account/billing/plans **[fetched]** |
| Steel | $0.10 / $0.08 per browser-h; proxy $10 / $6 per GB; captcha $3 / $1 per 1k | Launch $0, Scale $250/mo (+$100 credits) | $30 one-time (90 days) | https://docs.steel.dev/overview/pricinglimits **[fetched]** |

### 3b. Normalized: one 2 vCPU / 4 GiB box, 1 running hour (my arithmetic from the rates above)

| Platform | $/hour | Note |
|---|---|---|
| Hetzner CX23 (raw VPS, 2 vCPU/4 GB) | **€0.0096** (≈ $0.011) | hourly with monthly cap €5.49 + €0.50 IPv4; often sold out — https://docs.hetzner.com/general/infrastructure-and-availability/price-adjustment |
| Contabo Cloud VPS 4 (4 vCPU/8 GB) | €4.40/mo on 24-mo term (monthly, not hourly) | https://www.vpnsmith.com/en/blog/vps-prices-after-2026-price-increases |
| Northflank | 0.0667 | |
| Fly Machines performance-2x | 0.0917 | |
| Morph (2 MCU) | 0.1000 | |
| Vercel at 10% CPU | 0.1104 | at 100% CPU: 0.3408 |
| Freestyle | 0.1322 | |
| CodeSandbox Nano | 0.1486 | |
| Fly Sprites (fully busy) | 0.1645 | billed on actual use, idle ≈ storage only |
| E2B / Daytona / Blaxel | 0.1656 | Blaxel only while active |
| Cloudflare (CPU fully active) | 0.1800 | + $5/mo plan |
| Tensorlake (busy) | 0.2000 | |
| Modal (full request) | 0.2380 | billed on max(request, usage) — idle cheaper |

**Takeaway for Boxaz:** raw VPS + Incus is ~15× cheaper per hour than the per-second sandbox APIs for always-on or long boxes, but sandbox APIs win for short bursts and idle-heavy agents (active-CPU billing at Vercel/Fly Sprites/Blaxel/Modal). The placement engine should price **expected duty cycle**, not just list $/h — and the dashboard can show "you saved X vs E2B" as a marketing hook. Hetzner's 2026 hikes and stock-outs mean provider diversity (Contabo, Netcup, OVH, Vultr) is itself a feature.

### 3c. Free-tier patterns seen
- **One-time signup credit, no card**: E2B $100, Daytona $200, Blaxel ≤$200, Steel $30 (90-day expiry), Runloop had $50.
- **Recurring monthly allowance**: Modal $30/mo (card required), Fly $5/mo (card required), Vercel Hobby quotas, Northflank $20/mo, Upstash 5 CPU-h, Freestyle 200 vCPU-h, CodeSandbox 40 VM-h.
- **Gates that grow with payment**: concurrency/session length by plan (E2B, Vercel) or by cumulative top-up (Daytona tiers, Blaxel tiers); feature unlock after paid deposit (Steel $10).
- Suggested Boxaz launch shape (judgment call): one-time ~$10–25 credit with **no card but restricted** (small boxes, 1 h max, egress allowlist only, no public URLs), full features after card/first top-up; tiers by cumulative top-up like Daytona/Blaxel.

---

## 4. Billing / payment approach in the space

**What competitors do**
- *Plan fee + per-second usage, invoiced in arrears*: E2B (charged at start of month for previous month) [5], Modal (pay-as-you-go invoices, no prepaid needed) [13], Vercel (usage against $20 credit) [28], Fly (monthly) [22], Northflank (card charged at cycle end, payments via Stripe) [35].
- *Prepaid wallet / credits with top-ups*: Daytona (wallet, one-time + automatic top-up with threshold/target, disabled after failed payment) [10b]; Steel (prepaid, refuses new sessions at zero, auto top-up) [50]; Tensorlake (packs, hard stop) [n1]; Morph (MCU credits) [18]; Blaxel (tiers by top-ups) [32]; CodeSandbox (VM credits) [40].
- Daytona is also purchasable through **Stripe Projects** (agent-driven provisioning with Shared Payment Tokens) — https://www.daytona.io/dotfiles/daytona-stripe-projects

**Billing engines (prices from https://paywithlocus.com/guides/usage-based-billing-software [fetched], checked by them 10–23 Nov 2026)**

| Tool | Status 2026 | Price | Fit |
|---|---|---|---|
| Stripe Billing (Meters API, billing credits) | Stripe | 0.7% of billing volume; 100M meter events/mo included | simplest if you have a Stripe account |
| Metronome | owned by Stripe since 14 Jan 2026 | 0.8% + $0.04/1k events | high-volume, commits/credits |
| Orb | owned by Adyen since 1 Jul 2026 | $6/credit (1 credit = $1k billed or 200k events) | complex usage, simulations |
| Lago | independent, AGPLv3 self-host | free self-hosted | data on own infra; tax/dunning are add-ons |
| OpenMeter | Kong (Konnect Metering & Billing); OSS Apache-2.0 | 0.4% + $20/M events | metering only OSS |
| Stigg | independent | free to 5M events/mo | entitlements & real-time credit budgets |

**Merchant-of-record options (relevant because Boxaz's owner is in Vietnam):**
- **Stripe does not accept Vietnam-registered businesses** (not on stripe.com/global; workaround = US entity via Stripe Atlas). https://stripe.com/global · https://cdn.finfan.io/en/blogs/how-vietnamese-saas-businesses-receive-foreign-currency-cmw3e8gts001n13uwupr8pci1 · https://getpaidatlas.com/guides/get-paid-from-vietnam
- **Polar** (MoR, open source) — pays out to Vietnam via Stripe Connect Express; has **event ingestion, meters, metered prices, prepaid credits, customer usage portal, cost insights** — i.e. a near drop-in for Boxaz's usage model. Fees: 5% + 50¢ (or $20/mo + 3.8% + 40¢), +1.5% non-US cards. https://polar.sh/docs/merchant-of-record/supported-countries · https://polar.sh/docs/features/usage-based-billing/introduction · https://polar.sh/docs/features/usage-based-billing/credits · https://migratefromlemonsqueezy.com/lemon-squeezy-alternative-vietnam
- **Paddle** — accepts Vietnam sellers (not on blocklist), 5% + 50¢; usage-based billing is **early-access beta** (usage-events/meters API). https://developer.paddle.com/api-reference/usage-events/ · https://getpaidatlas.com/guides/get-paid-from-vietnam
- Dodo Payments, Creem also accept Vietnam sellers; **avoid Lemon Squeezy** (closing). https://okaytoship.com/learn/payment-providers-by-seller-country

**Recommendation for Boxaz (judgment, not a sourced fact):**
1. Keep the **source of truth in-house**: Boxaz's cost ledger already records per-box cost; add an immutable `usage_events` table (workspace, agent, box, provider, metric, qty, unit_cost, sell_price, idempotency key) and a per-workspace **credit balance ledger** (grants, top-ups, debits, expiries). Enforce quotas/hard-stop from this ledger in real time — no external engine does that fast enough for box kill decisions.
2. Use a **MoR for collection & tax**: Polar first (Vietnam payouts + native meters/credits + open source), Paddle as fallback; or form a US/SG entity later and move to Stripe Billing credits + Meters (and Metronome if contracts get complex).
3. Sell **credit packs + auto top-up** (Daytona/Steel pattern) rather than post-paid invoicing at launch — limits fraud exposure on raw VPS spend you prepay to Hetzner/Contabo.
4. Price = provider cost × margin with a floor per box-hour, shown transparently next to "same box on E2B/Daytona" for the marketing angle.

---

## Source key (numbered refs used in tables)
[1] https://agentsandboxes.org/sandboxes/e2b (and https://e2b.dev/pricing) · [2] https://docs.e2b.dev/sandbox/persistence · [3] https://dbotmaker.io/review/e2b · [4] https://docs.e2b.dev/use-cases/computer-use ; https://docs.e2b.dev/mcp-gateway · [5] https://docs.e2b.dev/billing **[fetched]** ; https://docs.e2b.dev/sandbox/lifecycle-events-webhooks ; https://docs.e2b.dev/sandbox/otel-telemetry-export ; https://e2b.dev/ ; https://dev.to/sandscale/sandscale-vs-e2b-how-two-firecracker-sandbox-platforms-differ-1hgi (no audit log found)
[6] https://agentsandboxes.org/sandboxes/daytona · [7] https://www.daytona.io/docs/snapshots/ · [8] https://www.daytona.io/docs/en/volumes/ · [9] https://www.daytona.io/docs/en/audit-logs ; https://www.daytona.io/docs/en/organizations/ ; https://www.daytona.io/docs/sso ; https://www.daytona.io/dotfiles/webhooks ; https://www.daytona.io/docs/git-operations ; https://chiri.ai/chiri-atlas/daytona ; https://tygartmedia.com/daytona-ai-devtools-profile/ · [10] https://www.daytona.io/pricing **[fetched]** ; https://www.daytona.io/docs/en/regions/ ; https://www.daytona.io/docs/en/regions-and-runners/ ; https://chiri.ai/chiri-atlas/daytona (SOC 2) · [10b] https://www.daytona.io/docs/en/v2/billing/ · [10c] https://www.daytona.io/docs/en/limits/
[11] https://agentsandboxes.org/sandboxes/modal/ ; https://sandboxreview.com/sandboxes/modal · [12] https://modal.com/docs/guide/sandbox-snapshots ; https://modal.com/blog/directory-snapshots-resumable-project-state-for-sandboxes · [13] https://modal.com/pricing **[fetched]**
[15] https://cloud.morph.so/docs/developers · [16] https://cloud.morph.so/docs/documentation/instances/branch · [17] https://cloud.morph.so/web/product/devboxes ; https://agenticindex.io/vendors/morph-cloud · [18] https://cloud.morph.so/web/pricing **[fetched]**
[19] https://fly.io/sprites ; https://agentsandboxes.org/sandboxes/fly-sprites ; https://fly.io/blog/sprites-mcp ; https://www.thundercompute.com/blog/e2b-alternatives-for-ai-agents · [20] https://docs.fly.io/volumes/overview · [21] https://fly.io/docs/reference/suspend-resume · [22] https://fly.io/pricing **[fetched]**
[23] https://developers.cloudflare.com/containers/platform/pricing **[fetched]** ; https://agentsandboxes.org/sandboxes/cloudflare-sandbox · [24] https://developers.cloudflare.com/changelog/post/2026-09-30-sandbox-sdk-1-0/ · [25] https://github.com/cloudflare/sandbox-sdk · [26] https://blog.cloudflare.com/sandbox-ga/ ; https://developers.cloudflare.com/changelog/post/2026-06-09-deprecating-sandbox-sdk-features/
[27] https://agentsandboxes.org/sandboxes/vercel-sandbox/ · [28] https://vercel.com/docs/sandbox/pricing **[fetched]** (docs nav lists Lifecycle Webhooks, Drives, Identity & Access, Observability, Vercel MCP, Spend Management) · [29] https://dbotmaker.io/review/vercel-sandbox
[31] https://blaxel.ai/platform/sandboxes.html · [32] https://blaxel.ai/pricing **[fetched]** · [33] https://www.simular.ai/alternatives/e2b-alternatives-for-ai-agents ; https://wheredoirunthis.com/products/blaxel
[34] https://agentsandboxes.org/sandboxes/northflank/ ; https://upstash.com/blog/ai-agent-sandbox-providers-compared-2026 · [35] https://northflank.com/pricing **[fetched]**
[38] https://codesandbox.io/docs/sdk/core-concepts ; https://www.npmjs.com/package/%40codesandbox/sdk · [39] https://codesandbox.io/docs/sdk/create ; https://codesandbox.io/docs/sdk/manage-sandboxes ; https://www.thundercompute.com/blog/e2b-alternatives-for-ai-agents · [40] https://codesandbox.io/docs/learn/credit-usage/credits ; https://codesandbox.io/docs/sdk/pricing
[42] https://discoverai.tools/articles/runloop-ai-coding-agent-devbox-review-2026 ; https://runloop.ai/ ; https://www.anchorterminal.com/tools/runloop · [43] https://docs.runloop.ai/docs/devboxes/snapshots
[44] https://coder.com/pricing · [45] https://infragap.com/cde-market-guide ; https://coder.com/blog/coder-agents-ga · [46] https://agentsandboxes.org/sandboxes/ona-gitpod · [47] https://aisotools.com/blog/gitpod-review-2026
[48] https://docs.browserbase.com/platform/browser/core-features/contexts ; https://devshot.co/reviews/browserbase-review · [49] https://docs.browserbase.com/account/billing/plans **[fetched]** · [50] https://docs.steel.dev/overview/pricinglimits **[fetched]**
[n1] https://agentsandboxes.org/sandboxes/tensorlake ; https://tensorlake.ai/blog/introducing-tensorlake-byoc ; https://www.usecarly.com/blog/tensorlake/ · [n3] https://upstash.com/box · [n4] https://shipyardhq.dev/products/sandscale ; https://dev.to/sandscale/what-an-audit-log-for-ai-coding-agents-should-record-1dj9 · [n5] https://e2b.dev/ (egress proxy diagram) · [n6] https://docs.computesdk.com/ ; https://www.computesdk.com/llms.txt · [n7] https://dev.to/dongjiang/why-i-built-e2bgateway-solving-ai-agent-sandbox-vendor-lock-in-51k6

## Gaps / caveats
- Cells marked **?** were not verified this pass (notably E2B SOC2/SSO/RBAC, Vercel/Cloudflare compliance, Modal webhooks, Morph enterprise features). Don't read them as "absent".
- Several numbers come from dated third-party reviews (Tensorlake, Freestyle docs, CodeSandbox plans, Upstash) rather than a vendor page fetched today.
- Modal Starter concurrency: the fetched pricing page says 250 containers; some Nov 2026 reviews say 100 — page wins.
- E2B Hobby concurrency: fetched docs say 10; some reviews say 20 — docs win.
- Incus stateful (memory) snapshots via CRIU are my suggestion for item 12; not researched here.
