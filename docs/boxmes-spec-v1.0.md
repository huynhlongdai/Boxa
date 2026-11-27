---
title: "Boxmes — Spec chi tiết v1.0"
subtitle: "Nền tảng box đa nhà cung cấp cho AI agent, nhiều khách hàng (SaaS)"
date: "2026-11-26"
---

# Boxmes spec v1.0 — đặc tả chi tiết ứng dụng

Ngày: 2026-11-26. Thay thế v0.4 (2026-10-27). v0.4 vẫn đúng về kiến trúc kết nối provider (mục 1–9) và Growth/Invite (mục 10); v1.0 gom toàn bộ những gì **đã xây** (core v1.12 + tenancy), bổ sung các **module còn thiếu** để mở cho nhiều khách hàng và thu tiền, và chốt lộ trình tới bản ra mắt.

Nguồn nghiên cứu đối thủ (có URL cho từng số liệu): `docs/research/competitors-2026-11.md`.

Đặc tả giao diện (UI) v2 đi kèm: [`docs/boxmes-ui-spec-v2.md`](boxmes-ui-spec-v2.md).

Ký hiệu trạng thái module: **Có** = đã code + test · **Một phần** = có nhưng thiếu phần quan trọng · **Mới** = chưa có. Ưu tiên: **P0** = bắt buộc trước khi nhận tiền người lạ · **P1** = cần trong 3–6 tháng đầu để cạnh tranh · **P2** = sau / doanh nghiệp.

---

## 0. Tóm tắt điều hành

- **Boxmes là gì:** một API / MCP / CLI duy nhất để AI agent xin "box" (máy Linux cách ly) ở **bất kỳ đâu** — VPS thô (Hetzner, Vultr, Contabo, netcup, DigitalOcean, máy nhà) hoặc sandbox API (E2B, Daytona, Novita, Prized, Blaxel, Vercel, Render…) — và tự chọn chỗ **rẻ nhất đạt yêu cầu**, kèm ngân sách, duyệt, ngủ/thức, snapshot, URL công khai, terminal/SSH/VNC.
- **Hiện trạng (HEAD `af2dc05`):** ~56k dòng Go, ~18k dòng TypeScript dashboard, SDK Python + TS, 18+ tool MCP, 6 provider VPS + 12 provider sandbox, 4 ngôn ngữ (vi/en/zh/ja). Đã chạy thật với E2B, Novita, Prized, Hetzner/Vultr (đọc). Vừa thêm lớp tài khoản/phiên/mã mời/danh sách chờ.
- **Khoảng trống để thành SaaS:** chưa cô lập dữ liệu theo workspace, chưa có vai trò thành viên, chưa có thanh toán/ví credit, chưa có gói & hạn mức theo khách, chưa có chống lạm dụng, chưa có webhook/audit cho khách, dashboard chưa có màn đăng ký/đăng nhập.
- **Lợi thế khác biệt (giữ và đẩy mạnh):** (1) VPS thô + Incus là một loại provider → giá box 2 vCPU/4 GB chỉ ~€0.01/giờ so với $0.07–0.24/giờ của sandbox thương mại; (2) quản trị agent: key riêng, ngân sách, chính sách, duyệt "Cần anh"; (3) A2A + MCP + llms.txt; (4) price feed sống; (5) truy cập đầy đủ (terminal, SSH cert, SFTP, CDP, VNC, link chia sẻ) qua một API.
- **Thời điểm:** Runloop đóng (13/11/2026) và Ona/Gitpod đóng cloud (31/10/2026) → có một làn sóng người dùng cần chỗ mới; nên có **API tương thích E2B** và công cụ nhập sớm.
- **Thanh toán:** Stripe không nhận doanh nghiệp đăng ký tại Việt Nam → đề xuất **Polar** (merchant of record, chi trả về VN, có meter + credit), Paddle dự phòng; tự giữ sổ cái và số dư credit trong Boxmes để chặn chi theo thời gian thực.

---

## 1. Định vị và khách hàng

### 1.1 Định vị
"Hạ tầng box cho agent, rẻ nhất có thể, có phanh." Boxmes không cạnh tranh làm sandbox nhanh nhất; Boxmes là **lớp điều phối trung lập** đặt trên các sandbox và VPS, cộng lớp quản trị chi phí/quyền cho agent.

| Nhóm đối thủ | Ví dụ | Boxmes khác ở đâu |
|---|---|---|
| Sandbox đơn nhà | E2B, Daytona, Modal, Blaxel, Vercel, Cloudflare, Morph, Fly Sprites | Không khoá vào một nơi; dùng được VPS giá rẻ; ngân sách/duyệt theo agent |
| Router đa provider | ComputeSDK (~39 provider), Crabbox, Open Sandbox Router | Boxmes có data plane riêng (`boxd`) nên hành vi giống nhau ở mọi nơi, có VPS + Incus, có quản trị và truy cập đầy đủ |
| Quản trị agent | Sandscale, Coder Agent Firewall | Boxmes vừa quản trị vừa cấp hạ tầng |
| Dev environment | Coder, Daytona, (Ona đã đóng) | Boxmes hướng agent trước, người sau |
| Hyperscaler | AWS/Azure/GCP agent sandbox (2026) | Trung lập, rẻ, đơn giản, đa provider |

### 1.2 Khách hàng mục tiêu (persona)
1. **Dev cá nhân / indie hacker** chạy Claude Code, Codex, Cursor agent: cần box rẻ, URL công khai, ngủ khi không dùng. Gói Free/Pro.
2. **Startup làm sản phẩm AI agent** (coding agent, data agent, browser agent): cần API ổn định, nhiều box song song, webhook, hạn mức, hoá đơn. Gói Pro/Team.
3. **Agency / đội automation** (n8n, Dify, Make): nhiều khách con, mỗi khách một workspace, ngân sách riêng. Gói Team.
4. **Công ty có tài khoản cloud riêng**: muốn Boxmes điều phối trên tài khoản Hetzner/AWS của mình (BYOC), trả phí nền tảng. Gói BYOC/Enterprise.
5. **Người vận hành nền tảng (anh)**: quản trị toàn hệ thống, pool VPS, giá, mã mời, lạm dụng.

### 1.3 Use case chính
- Agent viết code chạy test trong box tạm (task, TTL ngắn, fork để thử nhiều hướng).
- Agent deploy web app demo → URL công khai, ngủ khi rảnh, thức khi có request.
- Agent duyệt web (CDP) hoặc dùng máy tính (VNC, computer-use).
- Chạy dịch vụ 24/7 rẻ trên pool VPS.
- Người dùng mở terminal/SSH/VS Code vào box của agent để xem/sửa.
- Đánh giá (eval) song song hàng trăm box ngắn.

---

## 2. Kiến trúc tổng thể (v1.0)

```
 Người dùng (dashboard web) · Agent (MCP / REST / A2A / CLI / SDK / API tương thích E2B)
            │ phiên bxs_  · API key bx_ (theo workspace) · ticket truy cập
            ▼
 ┌──────────────────────────── boxmes-core ─────────────────────────────┐
 │ Edge: auth (session/key/ticket), tenant resolver, rate limit,       │
 │       idempotency, quota check, i18n lỗi, OpenAPI/llms.txt          │
 │ Identity & Tenancy: users, workspaces, members/RBAC, invites        │
 │ Box Manager · Scheduler/Placement · Reaper · Warm pools             │
 │ Governance: budgets, policies, approvals, audit                     │
 │ Billing: meter → ledger → wallet/credit → Polar (MoR)               │
 │ Access gateway: URL box, wake-on-request, terminal/SSH/SFTP/CDP/VNC │
 │ Integrations: webhooks, notify, OTel export, Git, E2B-compat        │
 │ Trust & Safety: tier gating, abuse signals, kill switch             │
 │ Store: SQLite (1 máy) | Postgres (SaaS)                             │
 └───────────▲───────────────────────────────────────┬─────────────────┘
             │ boxd dial-out (WSS, multiplex RPC)     │ Provider adapters (BPI)
 ┌───────────┴───────────┐                ┌──────────▼───────────────────────┐
 │ boxd: Incus/local/    │                │ T1 VPS: Hetzner, Vultr, DO,      │
 │ sandbox; exec, file,  │                │   Contabo, netcup, local         │
 │ PTY, egress, metrics, │                │ T2/T3 sandbox: E2B, Daytona,     │
 │ app supervisor        │                │   Novita, Prized, Blaxel, Vercel,│
 └───────────────────────┘                │   Render, boxd.sh, Agent37, …    │
                                          └──────────────────────────────────┘
```

Nguyên tắc giữ từ v0.3/0.4: boxd là data plane chung; boxd gọi ra (không mở cổng); adapter mỏng/khai báo YAML; năng lực và giá là dữ liệu (battleships.dev + self-test).

Nguyên tắc mới ở v1.0:
1. **Mọi tài nguyên thuộc đúng một workspace.** Mọi truy vấn đi qua `tenant scope`; không có đường nào đọc chéo trừ admin nền tảng (và có audit).
2. **Tiền đi trước tài nguyên.** Không tạo/wake box khi ví workspace không đủ số dư tối thiểu (trừ BYOC và gói trả sau được duyệt).
3. **Mặc định an toàn cho tài khoản mới.** Egress hạn chế, không URL công khai, box nhỏ, TTL ngắn cho tới khi xác minh (thẻ/nạp tiền).
4. **Mọi hành động quan trọng có sự kiện.** Một luồng event duy nhất cấp cho audit, webhook, thông báo, OTel, analytics.

---

## 3. Mô hình dữ liệu cốt lõi

| Thực thể | Trường chính | Ghi chú |
|---|---|---|
| User | id, email, name, password_hash, email_verified, platform_admin, created_at, last_login_at, mfa | **Có** (trừ MFA) |
| Workspace | id, name, slug, plan, status (active/suspended/closed), max_boxes, max_ttl_hours, region_policy, created_by | **Có** cơ bản; thêm plan/status/region |
| Membership | workspace_id, user_id, role | **Có** (owner/member) → mở rộng RBAC |
| Session | id, user_id, workspace_id, token_hash, ip, ua, expires_at | **Có** |
| APIKey | id, workspace_id, agent_id?, scopes, expires_at, last_used_at, created_by | **Một phần**: thêm hạn dùng, tạo bởi user |
| Agent | id, workspace_id, name, kind, budget_day/month, policy | **Có** (UNIQUE theo workspace) |
| Box | id, workspace_id, agent_id, spec, lifecycle, state, host, provider, cost | **Có**, cần bắt buộc lọc workspace |
| Host | id, provider, owner (platform/workspace for BYOC), pool, region | Thêm `owner_workspace_id` cho BYOC |
| Snapshot / Volume | workspace_id, size, storage, retention | Volume **Mới** |
| Secret | workspace_id, name, value_enc, scope | **Có** |
| LedgerEntry | workspace_id, box_id, kind, qty, unit_cost, price, at | Thêm cột `price` (giá bán) cạnh `cost` (giá vốn) |
| Wallet | workspace_id, balance_credits, auto_topup, threshold, hard_limit | **Mới** |
| Invoice / Payment | workspace_id, provider_ref (Polar), amount, status | **Mới** |
| Plan | id, quota json, price, features | **Mới** |
| Invite / Waitlist / Referral | như v0.4 §10 | Invite + waitlist **Có**; referral **Mới** |
| Webhook | workspace_id, url, events, secret, status | **Mới** |
| AuditEvent | workspace_id, actor, ip, action, target, result, hash_prev | Event có sẵn; thêm workspace + chuỗi hash |

---

## 4. Danh mục module

Mỗi module: mục đích · trạng thái · chức năng · API chính · UI · ưu tiên phần còn thiếu.

### M1. Danh tính & đăng nhập — **Một phần** (P0)
- Đã có: đăng ký (người đầu = admin nền tảng), chế độ `invite|open|closed`, đăng nhập email/mật khẩu (bcrypt, chặn 10 lần sai/15 phút), phiên `bxs_` 30 ngày, đăng xuất 1/tất cả, đổi mật khẩu, xác thực email, quên/đặt lại mật khẩu, đổi workspace.
- Thiếu:
  - **P0** màn web: đăng ký, đăng nhập, quên/đặt lại, xác thực email, nhập mã mời; dashboard dùng phiên thay vì dán admin key (giữ "đăng nhập bằng key" cho người vận hành tự host).
  - **P0** cookie phiên httpOnly + SameSite=Lax + CSRF cho các route thay đổi khi đi bằng cookie.
  - **P1** đăng nhập Google/GitHub (OAuth), magic link email.
  - **P1** MFA TOTP + mã khôi phục; bắt buộc cho admin nền tảng.
  - **P1** quản lý phiên (danh sách thiết bị, thu hồi từng phiên).
  - **P2** SSO SAML/OIDC + SCIM (Enterprise).
- API thêm: `GET/DELETE /v1/auth/sessions[/{id}]`, `POST /v1/auth/mfa/{setup,verify,disable}`, `GET /v1/auth/oauth/{provider}/start|callback`.

### M2. Workspace, thành viên, RBAC — **Một phần** (P0)
- Vai trò đề xuất: **Owner** (mọi quyền, xoá workspace, thanh toán) · **Admin** (mọi quyền trừ xoá workspace/chuyển owner) · **Developer** (box, agent, key của mình, secret dùng) · **Billing** (ví, hoá đơn, gói) · **Viewer** (chỉ đọc, không đọc log exec chứa dữ liệu nhạy cảm) · **Auditor** (đọc audit).
- Chức năng: tạo workspace, mời thành viên qua email (token 7 ngày), đổi vai trò, rời/xoá thành viên, chuyển owner, đổi tên/slug, tạm khoá (suspended), đóng workspace (giữ dữ liệu 30 ngày rồi xoá).
- **P0 cô lập dữ liệu:** mọi handler boxes/agents/keys/secrets/templates/snapshots/costs/approvals/events lọc theo `principal.WorkspaceID`; key agent bị khoá trong workspace của nó; admin nền tảng xem chéo có ghi audit. Test e2e "hai workspace không thấy nhau" cho từng route (sinh tự động từ OpenAPI ops).
- **P0 hạn mức workspace:** `max_boxes`, `max_ttl_hours`, vCPU/RAM đồng thời, số box tạo/phút — kiểm tra ở `CreateBox`, `Wake`, `Fork`, `Clone`.
- API: `POST /v1/workspaces`, `GET/PATCH/DELETE /v1/workspaces/{id}`, `GET/POST /v1/workspaces/{id}/members`, `PATCH/DELETE …/members/{user}`, `POST …/invitations`, `POST /v1/invitations/{token}/accept`, `POST …/transfer`.
- UI: Cài đặt › Workspace (tên, slug, vùng), Thành viên (bảng: người, vai trò, lần đăng nhập gần nhất — như mockup v1.4 tab "Người dùng"), bộ chọn workspace trên top bar.

### M3. API key & danh tính agent — **Một phần** (P0)
- Đã có: key admin/agent, scope, SHA-256, rate limit, ngân sách theo agent.
- Thiếu: **P0** key thuộc workspace, có hạn dùng, tạo bởi user nào, `last_used_at/ip`; scope chi tiết (`box:read`, `box:write`, `exec`, `file`, `secret:use`, `billing:read`, `admin`); key "chỉ đọc" cho dashboard bên thứ ba. **P1** xoay vòng key (2 key song song), cảnh báo key không dùng 90 ngày. **P2** OIDC workload identity (GitHub Actions → key ngắn hạn).

### M4. Vòng đời box — **Có**
- `requested → placing → provisioning → running ⇄ sleeping → snapshotting → terminated | failed`; lifecycle `task|session|service`; TTL, idle sleep, reaper, retry loại host lỗi, fork/clone/restore, emergency stop.
- Bổ sung: **P1** `pause_on_budget` theo workspace; **P1** nhãn (labels) + metadata tự do, lọc theo nhãn; **P1** tạo hàng loạt `POST /v1/boxes:batch` (eval); **P2** lịch (cron) chạy lệnh trong box hoặc bật/tắt service theo giờ.

### M5. Xếp chỗ (scheduler) & giá — **Có**, cần nâng (P1)
- Đã có: lọc capability, giá theo price feed + engine, pool chi phí biên ≈ 0, burst, chuỗi provider dự phòng, `placement_reason`, `/v1/quote`, `/v1/recommend`.
- Bổ sung:
  - **P1 giá theo mức dùng dự kiến**: sandbox tính theo CPU thực dùng (Vercel, Modal) rẻ hơn khi agent chủ yếu nhàn rỗi → thêm hồ sơ tải `cpu_util_expected` vào báo giá.
  - **P1 vùng & lưu trú dữ liệu**: `region: eu|us|apac|<mã>`, chính sách workspace "chỉ EU".
  - **P1 cờ năng lực mở rộng**: `memory_snapshot`, `fork_live`, `gpu`, `nested_docker`, `windows`, `macos` (từ battleships + self-test); Incus stateful snapshot (CRIU) — cần kiểm chứng trên host.
  - **P1 điểm tin cậy provider** từ dữ liệu provider health (tỷ lệ lỗi tạo, thời gian boot p50/p95) đưa vào điểm.
  - **P2 di chuyển box** giữa provider khi rẻ hơn (snapshot → restore).

### M6. Adapter provider — **Có**
- T1: Hetzner, Vultr (live), DigitalOcean, Contabo, netcup, local. T2/T3: E2B, Novita, Prized (live), Daytona, boxd.sh, Runloop (**đã đóng 13/11/2026 → gỡ khỏi chuỗi mặc định, đánh dấu deprecated**), Blaxel (đã thuộc Baseten), Upstash Box, Agent 37, boat, Vercel, Render.
- Bổ sung: **P1** OVHcloud, Scaleway, Linode/Akamai, AWS EC2 (spot), Fly Machines (REST công khai), Northflank; **P1** self-test tự động hằng tuần cho provider đã kết nối; **P2** Modal (qua sidecar SDK), Cloudflare Containers (qua Worker mẫu do Boxmes cung cấp), AWS/Azure/GCP agent sandbox.

### M7. Data plane (exec, file, job, log) — **Có**
- exec/exec-batch/async job/long-poll, output shaper (tail, `tokens_saved`, `log_id`), file đọc/ghi/grep, observe, PTY, AGENTS.md theo ngôn ngữ.
- Bổ sung: **P1 code interpreter** — ngữ cảnh Python/Node giữ trạng thái (kernel Jupyter trong box), trả output phong phú (bảng, ảnh biểu đồ PNG, JSON) — `POST /v1/boxes/{id}/code {language, code, context_id?}`; **P1 theo dõi file** (watch) qua SSE; **P1 upload/download lớn** qua URL presign (tránh đi qua core).

### M8. Truy cập box — **Có**
- Terminal bền (ticket một lần), forward port TCP, SSH (ticket + chứng chỉ ngắn hạn), SFTP, trình duyệt CDP, desktop VNC (noVNC), link chia sẻ.
- Bổ sung: **P1 computer-use API** trên VNC: `screenshot`, `click`, `type`, `key`, `scroll`, `drag` + adapter cho tool computer-use của Anthropic/OpenAI; **P1 VS Code/Cursor Remote** (hướng dẫn + `box ssh-config`); **P2 hồ sơ trình duyệt bền**, ghi lại phiên (replay), live view + giao cho người (human hand-off), proxy/captcha (theo Browserbase/Steel).

### M9. App & URL công khai — **Có**
- App supervisor (clone, install, start, restart, ready check), URL cố định theo box+port `https://<name>.<domain>`, wake-on-request, URL token, chặn URL preview provider, chuỗi dự phòng preview → ingress → tunnel.
- Bổ sung: **P0 gating**: URL công khai chỉ mở cho workspace đã xác minh (chống phishing/lạm dụng); **P1 tên miền riêng** (CNAME + TLS tự động); **P1 giới hạn băng thông** và tính phí egress theo GB; **P2 deploy từ Git push** (webhook).

### M10. Snapshot, volume, lưu trữ — **Một phần** (P1)
- Đã có: snapshot local / S3 (R2/B2/MinIO) presign, retention, restore/fork, snapshot T3 trên core.
- Bổ sung: **P1 Volume** độc lập với box (tạo, gắn vào nhiều box, snapshot, sao lưu S3), giới hạn theo gói; **P1 hạn lưu trữ** theo workspace và tính phí GB-tháng; **P2** chia sẻ snapshot làm template công khai.

### M11. Template & warm pool — **Một phần** (P1)
- Đã có: `box.yaml`, template dựng sẵn, YAML editor trên web.
- Bổ sung: **P1 warm pool** — giữ N box đã boot cho mỗi template/vùng để che thời gian khởi động VPS + Incus (mục tiêu < 1 s cho template phổ biến); **P1 build template từ Dockerfile** (image Incus/OCI) + cache; **P2 marketplace template** (cộng đồng, đánh giá).

### M12. Mạng — **Một phần**
- Đã có: egress allowlist theo box/chính sách, mesh (cơ bản), ingress/gateway.
- Bổ sung: **P1 egress proxy giữ bí mật** — secret được tiêm ở proxy (header/Bearer) khi box gọi API đích đã khai báo, giá trị không bao giờ vào box (như E2B, Vercel, Fly Connectors); **P1 mặc định egress hạn chế** cho gói Free; **P2 IP tĩnh ra ngoài**, mạng riêng giữa box cùng workspace (WireGuard/headscale).

### M13. Secrets — **Có**
- AES-256-GCM, tiêm theo exec, chỉ trả tên.
- Bổ sung: **P0** scope theo workspace + phân quyền (ai được dùng secret nào); **P1** tích hợp egress proxy (M12); **P2** KMS ngoài (AWS KMS/Vault), quét lộ secret trong output.

### M14. Quản trị agent (ngân sách, chính sách, duyệt) — **Có**
- Ngân sách ngày/tháng theo agent và toàn hệ thống, ngưỡng 80/100%, mục "Cần anh" (pool_expand, budget, host_down, service_stop), policy (provider cho phép, egress).
- Bổ sung: **P0 ngân sách cấp workspace** (trần cứng tháng, cảnh báo 50/80/100%); **P1 duyệt qua Telegram/email/webhook** (nút Duyệt/Từ chối); **P2 DLP** quét prompt/tool call MCP, audit có chuỗi hash kiểm chứng được (khác biệt hoá như Sandscale).

### M15. Thanh toán & ví credit — **Mới** (P0)
- Mô hình: **trả trước bằng credit** (1 credit = $0.01) + **tự nạp** khi số dư dưới ngưỡng; gói tháng cộng credit hằng tháng. Lý do: VPS là chi phí thật của anh; trả trước giới hạn rủi ro nợ xấu; giống Daytona/Steel/Morph/Blaxel.
- Luồng: meter (mỗi phút, từ ledger) → `price` theo bảng giá bán → trừ ví → ngưỡng: 20% cảnh báo, 0 → box `session/task` ngủ, `service` vào duyệt (ân hạn 24 giờ cấu hình được), chặn tạo box mới (`PAYMENT_REQUIRED`).
- Cổng: **Polar** (merchant of record: thu thuế, hoá đơn, chi trả về VN; có meter/credit) — mặc định; **Paddle** dự phòng; **Stripe** khi có pháp nhân Mỹ (Stripe Atlas). Thiết kế `PaymentProvider` interface: `CreateCheckout(pack)`, `CreateSubscription(plan)`, `Webhook(event)`, `Portal(customer)`.
- Gói credit: 5/20/50/100/500 USD; tự nạp: số tiền + ngưỡng; hoá đơn PDF từ MoR; lịch sử giao dịch; mã khuyến mãi / credit tặng (khởi nghiệp, referral) có hạn dùng.
- API: `GET /v1/billing` (số dư, gói, tự nạp, dự báo hết tiền), `POST /v1/billing/checkout {pack|plan}`, `PUT /v1/billing/auto-topup`, `GET /v1/billing/transactions`, `GET /v1/billing/invoices`, `POST /v1/billing/portal`, `POST /v1/billing/webhooks/polar` (không auth, ký HMAC), admin `POST /v1/admin/workspaces/{id}/credits {amount, reason}`.
- UI: Cài đặt › Thanh toán (số dư lớn, nút nạp, tự nạp, bảng giao dịch, hoá đơn, gói hiện tại, dự báo "còn ~N ngày với mức dùng hiện tại").
- Kế toán nội bộ: ledger giữ **giá vốn** (provider) và **giá bán**; báo cáo biên lợi nhuận theo provider/gói cho admin.

### M16. Gói, hạn mức, rate limit — **Mới** (P0)
- Bảng `plans` cấu hình được (không hard-code); hạn mức kiểm tra ở một chỗ (`quota.Check(ws, action, amount)`), lỗi `QUOTA_EXCEEDED` kèm `limit`, `used`, `upgrade_url`.
- Hạn mức: box đồng thời, vCPU/RAM đồng thời, TTL tối đa, thời gian chạy liên tục tối đa, box tạo/phút, số agent, số key, số secret, dung lượng snapshot/volume, URL công khai (bật/tắt), egress (hạn chế/mở), provider cho phép (pool / sandbox / BYOC), GPU.
- Rate limit API theo workspace × nhóm endpoint, `429` + `Retry-After` (đã có cơ chế bucket theo key — chuyển sang theo workspace).
- **Bậc theo nạp tiền** (như Daytona/Blaxel): tổng nạp ≥ $10 → mở URL công khai + egress mở; ≥ $50 → nâng hạn mức đồng thời…

### M17. Growth: mã mời, danh sách chờ, giới thiệu — **Một phần** (P1)
- Đã có: mã mời (max_uses, hạn, gửi mail), danh sách chờ (vị trí), mời N người đầu danh sách.
- Bổ sung theo v0.4 §10: **P1** kênh/chiến dịch cho mã, mã giới thiệu cá nhân + `referred_by`, thưởng (+credit khi người được mời nạp lần đầu), `GET /v1/waitlist/me`, `GET /v1/referrals/me`, funnel (phát → redeem → tạo box đầu → giữ 7 ngày → trả tiền), UTM; **P1** trang công khai waitlist/referral (mobile-first, chia sẻ X/Facebook/Discord/Zalo); **P2** bot Discord, webhook social, tự hạ hạn mức trial khi đạt mục tiêu đăng ký.
- Chính sách trial v0.4 (5 box trial toàn hệ thống, TTL ≤ 12 h) được thay bằng **credit tặng** + hạn mức gói Free (M16) — đơn giản hơn và đo bằng tiền.

### M18. Chống lạm dụng (trust & safety) — **Mới** (P0)
Quan trọng vì VPS thô + URL công khai dễ bị dùng để đào coin, quét cổng, phishing → nhà cung cấp (Hetzner/Contabo) khoá tài khoản.
- **P0**: xác thực email bắt buộc trước khi tạo box; chặn email dùng một lần; giới hạn đăng ký theo IP; tài khoản chưa nạp tiền: egress allowlist (pypi, npm, github, apt…), không URL công khai, box ≤ 1 vCPU/2 GB, TTL ≤ 2 h.
- **P0**: phát hiện đào coin (CPU 100% kéo dài + kết nối pool đã biết), quét cổng (nhiều SYN ra ngoài), gửi mail (cổng 25 chặn mặc định); hành động: ngủ box + khoá workspace + tạo mục duyệt cho admin.
- **P0** nút "khoá workspace" (kill switch) + hàng chờ báo cáo lạm dụng (abuse@) cho admin.
- **P1** điểm rủi ro đăng ký (IP/ASN, quốc gia, tốc độ), xác minh bằng thẻ (micro-charge qua MoR) cho tính năng rủi ro.

### M19. Thông báo & webhook — **Một phần** (P0)
- Đã có: Telegram, email SMTP (báo cáo ngày, ngân sách, duyệt, box lỗi, host down).
- **P0 webhook cho khách**: đăng ký URL + chọn sự kiện (`box.created|running|sleeping|terminated|failed`, `job.finished`, `budget.threshold`, `billing.low_balance`, `approval.created`), ký `X-Boxmes-Signature: t=…,v1=HMAC-SHA256`, thử lại lũy thừa 24 h, lịch sử gửi + gửi lại thủ công, tự tắt sau nhiều lần lỗi.
- **P1** Slack, Discord; thông báo theo người dùng (mỗi thành viên chọn kênh).
- **P1 email giao dịch**: chào mừng, xác thực, đặt lại mật khẩu, mời thành viên, sắp hết tiền, hoá đơn — mẫu đa ngôn ngữ; dùng SMTP/Resend/SES.

### M20. Audit & tuân thủ — **Một phần** (P0/P1)
- Đã có: bảng event, `GET /v1/admin/audit` (toàn hệ thống).
- **P0** audit theo workspace cho khách: ai (user/key/agent), IP, hành động, đối tượng, kết quả; lọc + xuất CSV/JSON; giữ 90 ngày (Team: 1 năm).
- **P1** dữ liệu & quyền riêng tư: xuất toàn bộ dữ liệu workspace, xoá tài khoản (GDPR), chính sách lưu trữ log/snapshot, DPA, điều khoản dịch vụ + chính sách sử dụng chấp nhận được (AUP), trang trạng thái (status page) + SLA cho Team.
- **P2** chuỗi hash audit kiểm chứng được, SOC 2 Type I khi có khách doanh nghiệp.

### M21. Quan sát (observability) — **Một phần** (P1)
- Đã có: metrics host/box mỗi 10 s, provider health, jobs.
- **P1** API metrics theo box (CPU/RAM/disk/net, 24 h) + biểu đồ trên dashboard; **P1** xuất OTLP (metrics, log exec, trace cho mỗi lệnh agent) tới Grafana/Datadog/Honeycomb của khách; **P1** log sink (HTTP/S3); **P1** Prometheus `/metrics` cho người vận hành.

### M22. Tích hợp agent — **Có**, mở rộng (P1)
- Đã có: MCP HTTP + stdio (18 tool, resources, prompts), A2A, CLI `box`, SDK Python/TS, 22 loại agent + snippet, llms.txt, OpenAPI 3.1.
- **P1 API tương thích E2B** (`/e2b/...`): agent dùng SDK E2B chỉ đổi base URL/key; ánh xạ sandbox → box (template, commands, files, pause/resume). Mục tiêu: hút người dùng Runloop/Ona/E2B đang tìm chỗ rẻ hơn. **P1** công cụ nhập template/snapshot từ E2B/Daytona.
- **P1** SDK Go; MCP OAuth (để Claude/ChatGPT kết nối bằng đăng nhập thay vì dán key).

### M23. Git — **Mới** (P1)
- GitHub App / GitLab OAuth gắn vào workspace; clone repo private bằng token ngắn hạn (qua egress proxy, không lưu trong box); tạo nhánh, commit, push, mở PR; `POST /v1/boxes/{id}/git/{clone,commit,push,pr}`; tool MCP `box_git`.

### M24. GPU / Windows / macOS — **Mới** (P2)
- Định tuyến khi `requirements.gpu|os` được yêu cầu: Daytona, Modal, Northflank, RunPod, Vast (GPU), Daytona Windows/macOS; Boxmes giữ API giống hệt.

### M25. Bảng điều khiển web — **Có**, cần bổ sung (P0)
- Đã có (Next.js, vi/en/zh/ja): Tổng quan, Boxes (+ chi tiết: terminal, tệp, cổng/app, snapshot, màn hình), Agents, Chi phí, Hạ tầng (VPS, Sandbox API, sức khoẻ, bảng giá, tốc độ, mesh, adapter), Recommend, Templates, Secrets, Developers (8 tab tích hợp), Cài đặt, Admin (hệ thống, job, audit, API keys, quota, tính năng, người dùng), Onboarding.
- **P0 mới**: Đăng ký / Đăng nhập / Quên mật khẩu / Xác thực email / Nhập mã mời; bộ chọn workspace; Thành viên; Thanh toán; API keys theo workspace; Webhooks; Audit của workspace.
- **P0 tách quyền giao diện**: khách thấy "khu workspace"; mục Hạ tầng (pool VPS, provider của nền tảng), Admin chỉ hiện cho admin nền tảng; khách BYOC thấy mục "Provider của tôi".
- **P1** trang công khai: landing, bảng giá, docs, waitlist/referral, status.

### M26. Quản trị nền tảng (ops console) — **Một phần** (P0)
- Đã có: hệ thống, job, audit, key, quota, tính năng, người dùng (v2), mã mời, danh sách chờ (API).
- **P0**: danh sách workspace (gói, số dư, chi/thu tháng, trạng thái), khoá/mở workspace, tặng credit, xem như (impersonate, có audit), hàng chờ lạm dụng; UI cho mã mời + danh sách chờ; **P1** báo cáo doanh thu/biên lợi nhuận theo ngày, theo provider, theo gói; cohort đăng ký → trả tiền.

### M27. BYOC (mang tài khoản cloud riêng) — **Mới** (P1)
- Workspace kết nối token Hetzner/Vultr/E2B… của chính họ; host/box chạy trên tài khoản đó, Boxmes chỉ thu **phí nền tảng** (đề xuất $0.01/vCPU-giờ hoặc 10% chi phí compute, tối thiểu $X/tháng). Tách `Host.owner_workspace_id`; scheduler chỉ dùng host của workspace đó (hoặc pool nền tảng nếu cho phép).

### M28. Tài liệu, i18n, hỗ trợ — **Có**, mở rộng (P1)
- Đã có: README, API contract, llms.txt, catalog lỗi dịch 4 ngôn ngữ.
- **P1** trang docs công khai (quickstart theo từng agent, cookbook: deploy app, eval song song, browser agent, computer use), changelog, kênh hỗ trợ (Discord + email), mẫu email đa ngôn ngữ.

---

## 5. Gói & giá đề xuất (cần anh chốt)

Giá vốn tham khảo (list price 26/11/2026, nguồn trong file nghiên cứu): box 2 vCPU/4 GB/giờ — Hetzner CX23 ~€0.0096 · Northflank $0.067 · Fly $0.092 · Morph $0.10 · E2B/Daytona/Blaxel $0.166 · Cloudflare $0.18 · Modal $0.238. Free tier đối thủ: E2B $100 một lần, Daytona $200, Blaxel tới $200, Modal $30/tháng (cần thẻ), Fly $5/tháng (cần thẻ).

| Gói | Giá | Bao gồm | Hạn mức chính |
|---|---|---|---|
| **Free** | $0 | $5 credit tặng một lần (≈ 200 giờ box pool 2c/4g) | 1 box đồng thời, ≤ 1 vCPU/2 GB, TTL ≤ 2 h, egress hạn chế, không URL công khai, chỉ pool nền tảng |
| **Pro** | $20/tháng | $20 credit/tháng | 10 box đồng thời, ≤ 8 vCPU/16 GB/box, TTL ≤ 24 h, service 24/7, URL công khai, sandbox API, webhook |
| **Team** | $99/tháng (5 thành viên, +$10/người) | $100 credit/tháng | 50 box đồng thời, RBAC đủ, audit 1 năm, SSO (sau), hỗ trợ ưu tiên |
| **BYOC** | $49/tháng + $0.01/vCPU-giờ | — | Không giới hạn theo gói, chạy trên tài khoản cloud của khách |
| **Enterprise** | liên hệ | — | SSO/SCIM, DPA, SLA, vùng riêng |

Bảng giá credit (đề xuất ban đầu, chỉnh theo số liệu):
- **Box pool VPS** (lợi thế giá): $0.006/vCPU-giờ + $0.003/GB RAM-giờ → 2c/4g ≈ **$0.024/giờ** (rẻ hơn ~3–10 lần sandbox thương mại, biên ~2× giá vốn Hetzner khi pool lấp > 50%). Box ngủ: chỉ tính đĩa.
- **Box sandbox API** (E2B, Daytona…): giá vốn provider × 1.15.
- **Lưu trữ** snapshot/volume: $0.08/GB-tháng. **Egress**: 100 GB/tháng miễn phí (Pro), sau đó $0.05/GB.
- **Tính theo giây**, làm tròn lên phút ở hoá đơn.

---

## 6. Yêu cầu phi chức năng

| Mục | Mục tiêu |
|---|---|
| Khả dụng control plane | 99.5% (Pro), 99.9% (Team, có SLA) |
| API p95 (không tính exec) | < 150 ms |
| Tạo box từ warm pool | < 1 s; pool lạnh VPS < 30 s; sandbox theo provider |
| Wake-on-request | < 3 s với box pool đang ngủ |
| Đo đếm | sai số ledger so với hoá đơn provider < 5%/tháng |
| Cô lập | container Incus không đặc quyền, giới hạn cpu/mem/pids/disk; tài khoản Free không dùng chung host với Team (pool riêng) |
| Bảo mật | mật khẩu bcrypt, token chỉ lưu hash, secrets AES-256-GCM, TLS mọi nơi, CSRF cho cookie, MFA cho admin, quét phụ thuộc (govulncheck, pnpm audit) trong CI |
| Lưu dữ liệu | event/log exec 30 ngày (Free) / 90 ngày (Pro) / 1 năm (Team); snapshot theo retention; xoá workspace sau 30 ngày đóng |
| Mở rộng | core chạy nhiều bản sau load balancer khi dùng Postgres (khoá phân tán cho reaper/ledger), hub boxd có sticky theo host |
| Sao lưu | Postgres PITR hằng ngày; thử khôi phục hằng tháng |
| i18n | mọi lỗi/hint/email/UI có vi/en/zh/ja (test catalog bắt buộc) |

---

## 7. Lộ trình

Mốc tính từ 2026-11-26; mỗi mốc kết thúc bằng commit `Boxmes v2.x …` + test e2e + docs (theo quy ước repo).

| Giai đoạn | Thời lượng | Nội dung | Kết quả |
|---|---|---|---|
| **v2.0 Tenancy hoàn chỉnh** | 2 tuần | M2 cô lập dữ liệu mọi route + test chéo workspace; hạn mức workspace; M3 key theo workspace; M2 thành viên + vai trò; M1 màn web đăng ký/đăng nhập + cookie phiên + CSRF; dashboard tách quyền | Nhiều người dùng thật trên một core, không thấy dữ liệu nhau |
| **v2.1 Thanh toán** | 2 tuần | M15 ví credit + Polar + tự nạp + hoá đơn; M16 gói/hạn mức/rate limit; giá bán trong ledger; ngân sách workspace | Thu được tiền, chặn chi khi hết credit |
| **v2.2 An toàn & khách hàng** | 1–2 tuần | M18 chống lạm dụng P0; M19 webhook + email giao dịch; M20 audit workspace + xuất; M26 ops console P0; điều khoản/AUP/privacy | **Mở beta công khai (invite)** |
| **v2.3 Cạnh tranh** | 3–4 tuần | Warm pool; egress proxy giữ bí mật; volume; API tương thích E2B; code interpreter; computer-use; Git; OTel; vùng | Ngang tầm E2B/Daytona về tính năng chính, rẻ hơn |
| **v2.4 Growth** | 2 tuần | Referral + funnel + trang waitlist; landing/pricing/docs/status; BYOC; Slack/Discord | Mở đăng ký rộng |
| **v3** | sau | SSO/SCIM, GPU/Windows/macOS, marketplace template, browser nâng cao, DLP, SOC 2 | Doanh nghiệp |

Danh sách kiểm tra trước khi nhận tiền người lạ: cô lập workspace có test · hạn mức + rate limit · ví credit + chặn khi hết · xác thực email + gating tính năng rủi ro · phát hiện đào coin/quét cổng + kill switch · audit · điều khoản + AUP + privacy · sao lưu DB · status page · tài khoản provider có giới hạn chi (Hetzner project limit).

---

## 8. Rủi ro

- **Lạm dụng làm khoá tài khoản provider** (Hetzner/Contabo cấm đào coin, quét) → M18, pool Free tách riêng, nhiều tài khoản provider.
- **Pháp lý thanh toán** (VN không dùng được Stripe) → Polar/Paddle; cân nhắc pháp nhân Singapore/Mỹ khi doanh thu tăng.
- **Phụ thuộc provider sandbox đóng cửa** (Runloop, Ona) → chính là lợi thế của lớp trung lập; theo dõi qua provider health, tự bỏ provider chết khỏi chuỗi.
- **Cạnh tranh giá từ hyperscaler** → giữ lợi thế VPS giá rẻ + quản trị agent.
- **Chi phí pool rảnh** (VPS trả theo tháng) → warm pool co giãn theo tải, burst theo giờ, báo cáo lấp đầy.
- **Một người vận hành** → tự động hoá ops (reaper, alert, sao lưu), ưu tiên tính năng giảm hỗ trợ (docs, lỗi tự giải thích có `next`).

---

## 9. Quyết định đã chốt (2026-11-27, anh duyệt "theo đề xuất")

1. **Cổng thanh toán:** Polar (merchant of record) làm chính, Paddle dự phòng. Pháp nhân: dùng tài khoản Polar cá nhân/hộ kinh doanh trước, tách pháp nhân khi doanh thu ổn định.
2. **Bảng giá & credit:** áp dụng nguyên bảng mục 5. Free **không cần thẻ**: $5 credit một lần, 1 box ≤1 vCPU/2 GB, TTL ≤2 giờ, egress hạn chế, không URL công khai; mở URL công khai khi nạp ≥ $10 (bậc xác minh M18).
3. **Thứ tự:** v2.0 tenancy trước, rồi v2.1 billing, v2.2 an toàn + beta; API tương thích E2B ở v2.3.
4. **Tên miền:** giữ `boxs.tech` cho gateway; tạm dùng `app.boxs.tech` (dashboard) và `docs.boxs.tech` cho tới khi mua domain thương hiệu Boxmes (khi đó chỉ đổi `BOXMES_DASHBOARD_URL`/`BOXMES_PUBLIC_URL`).
5. **Dữ liệu:** EU (Hetzner) mặc định; trường `region` theo workspace có sẵn trong mô hình để mở chọn vùng sau.
6. **BYOC:** để sau khi có khách trả tiền (v2.4), không mở trong beta.

---

## Phụ lục A. API mới dự kiến (tóm tắt)

| Nhóm | Endpoint |
|---|---|
| Auth (có) | `/v1/auth/{config,signup,login,logout,me,password,verify-email,password/forgot,password/reset,workspace}`, `/v1/waitlist` |
| Auth (mới) | `/v1/auth/sessions`, `/v1/auth/mfa/*`, `/v1/auth/oauth/{provider}/*` |
| Workspace | `/v1/workspaces[/{id}]`, `/members`, `/invitations`, `/transfer` |
| Billing | `/v1/billing`, `/checkout`, `/auto-topup`, `/transactions`, `/invoices`, `/portal`, `/webhooks/polar` |
| Plans/quota | `GET /v1/plans`, `GET /v1/usage` (đã dùng / hạn mức) |
| Webhooks | `/v1/webhooks[/{id}]`, `/deliveries`, `/{id}/test` |
| Audit | `GET /v1/audit?actor=&action=&from=&to=`, `GET /v1/audit.csv` |
| Volumes | `/v1/volumes[/{id}]`, `POST /v1/boxes/{id}/volumes` |
| Warm pool | `/v1/templates/{id}/pool` |
| Code / computer-use | `POST /v1/boxes/{id}/code`, `POST /v1/boxes/{id}/computer/{screenshot,click,type,key,scroll}` |
| Git | `POST /v1/boxes/{id}/git/{clone,commit,push,pr}`, `/v1/integrations/github` |
| Metrics | `GET /v1/boxes/{id}/metrics`, `PUT /v1/observability` (OTLP) |
| E2B-compat | `/e2b/sandboxes…` (ánh xạ API E2B) |
| Admin | `/v1/admin/workspaces[/{id}]` (khoá, credit, impersonate), `/v1/admin/abuse`, `/v1/admin/revenue` |

## Phụ lục B. Biến môi trường mới dự kiến

`BOXMES_SIGNUP_MODE`, `BOXMES_SESSION_TTL` (có) · `BOXMES_DASHBOARD_URL` (có) · `BOXMES_POLAR_TOKEN`, `BOXMES_POLAR_WEBHOOK_SECRET`, `BOXMES_BILLING_MODE=off|prepaid` · `BOXMES_FREE_CREDIT_USD` · `BOXMES_ABUSE_*` (ngưỡng) · `BOXMES_OAUTH_{GITHUB,GOOGLE}_{ID,SECRET}` · `BOXMES_MAIL_PROVIDER=smtp|resend|ses`.

Chế độ tự host một người (như hiện nay) vẫn được giữ: `BOXMES_BILLING_MODE=off`, `BOXMES_SIGNUP_MODE=closed` → hành vi giống v1.12.
