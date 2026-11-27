# Boxmes — Hạ tầng v2: Tài khoản nhà cung cấp (provider accounts)

> Phiên bản: 0.1 (đề xuất) · Ngày: 2026-11-27 · Phạm vi: khu **Nền tảng → Hạ tầng** (chỉ platform-admin, PA).
> Liên quan: spec v1.0 §M6, §M5, §M26, §M27 (`docs/boxmes-spec-v1.0.md`), UI v2 §16 (`docs/boxmes-ui-spec-v2.md`).
> Tài liệu này **chỉ là thiết kế**; chưa có code core tương ứng.

---

## 0. Tóm tắt

Hôm nay mỗi provider trong core chỉ có **đúng một token** (`model.Provider.TokenEnc`), một bộ `ProviderPrefs` và một circuit breaker. Admin không thấy số dư, hạn token, hạn credit, chu kỳ hoá đơn, hạn mức tài khoản (server/vCPU/concurrency) và không có cơ chế dự phòng khi một tài khoản bị khoá, hết tiền hay chạm hạn mức.

Đề xuất: tách **provider (loại dịch vụ, adapter)** khỏi **provider account (một tài khoản/khoá API cụ thể)**. Mỗi provider có `accounts[]`, mỗi account có credential trong vault, ưu tiên/trọng số, trạng thái, hạn mức, số dư, các mốc thời gian, chi tiêu ngày/tháng so với trần, và lịch sử. Scheduler chọn **account** (không chỉ provider), failover theo loại lỗi, có cooldown/breaker theo account. Một vòng thăm dò (poller) đọc số dư/hạn mức qua API khi provider hỗ trợ, hoặc ước tính từ số nhập tay + ledger. Cảnh báo (số dư thấp, sắp hết hạn credit/token, gần hạn mức, failover, hoá đơn sắp đến) đi qua kênh notify sẵn có. Mọi thay đổi ghi audit.

Ưu tiên: **Pha A (v2.2, chặn beta)** — mô hình nhiều account + `host.account_id` + nhập tay + cảnh báo cơ bản + failover; **Pha B (v2.3)** — đọc số dư/hạn mức qua API, dự báo "còn X ngày"; **Pha C (v2.4)** — dùng lại cho BYOC (account thuộc workspace).

---

## 1. Hiện trạng trong core (đã đọc code, commit `9ae77d8`)

| Thành phần | Nơi | Hiện trạng |
|---|---|---|
| Danh sách provider | `core/internal/provider/provider.go` `Default()`; sandbox ở `core/internal/sandbox/*.go`, YAML ở `sandbox/builtin/*.yaml` | T1: local, hetzner, vultr, digitalocean, contabo, netcup. T2/T3: e2b, novita, prized, daytona, boxdsh, render + YAML agent37, blaxel, boat, runloop (deprecated), upstash-box, vercel. Kế hoạch (spec M6): OVH, Scaleway, Linode/Akamai, AWS EC2 spot, Fly Machines, Northflank, Modal, Cloudflare Containers. |
| Mô hình provider | `model.Provider` (`model/model.go:290`) | `ID, Name, Connected, Tier, LastCheckAt, Error, Implemented, Capabilities, TokenEnc, TokenSource(env/api), ChainRank, Group, Adapter, Verification, Docs, Lifecycle`. **Một token duy nhất.** Lưu dạng doc JSON trong bảng `providers(id, created_at, doc)`. |
| Credential | `manager/providers.go` `SetProviderToken`, `providerToken`; `manager/providers_v12.go` `ConfigureProvidersFromEnv`; `provider.EnvTokens` | Token mã hoá AES-GCM bằng `BOXMES_MASTER_KEY` (`secret.Box.Seal`). Nguồn: `PUT /providers/{id}/token` hoặc env (`HETZNER_TOKEN`, `VULTR_API_KEY`, `DIGITALOCEAN_TOKEN`, `CONTABO_*` 4 biến, `NETCUP_REFRESH_TOKEN`, `E2B_API_KEY`, `NOVITA_API_KEY`, `DAYTONA_API_KEY`+`DAYTONA_ORGANIZATION_ID`, `RENDER_API_KEY`+`RENDER_OWNER_ID`, `PRIZED_API_KEY`, `BOXDSH_API_KEY`…). **Env luôn thắng** và ghi đè token dashboard. System secrets `BOXMES_SYS_*` chỉ dùng cho Telegram/SMTP/storage, không cho provider. |
| Tuỳ chọn | `model.ProviderPrefs` (`model/v13.go:53`), KV `provider_prefs:<id>` | `Enabled, Priority(0..99), MaxHourlyUSD, MaxMonthlyUSD, MaxServers, Regions, DefaultRegion, SandboxMode`. `capsAllow` tính trần từ giá niêm yết các host đang sống, **không phải chi tiêu thật**. |
| Sức khoẻ | `provhealth` + `manager/provmgr.go` | Đo latency API, tỷ lệ tạo lỗi, thời gian provision p50/p95, phân loại lỗi (`auth, rate_limit, quota, capacity, timeout, network, server, invalid`), breaker **theo provider** (cooldown 60s → 15m), tôn trọng rate-limit. Probe = `Validate(token)` định kỳ. KV `provider_health`. |
| Host | `model.Host` | Có `Provider, ProviderRef, PriceMonth/Hour, Billing` — **không có account**. Xoá/Status/List host luôn gọi `providerToken(ctx, h.Provider)` (`hosts.go:367`, `sandbox14.go:79`, `sandboxdeploy.go:52`, `gatewaydomain.go:182`) ⇒ nếu đổi token sang tài khoản khác, Boxmes **mất quyền xoá host cũ** (rò tiền). |
| Chi phí | `model.LedgerEntry` | Có `Provider`, không có account. Ngân sách theo agent/global (`budget.go`), không theo tài khoản provider. |
| API | `api/server.go:135-138`, `api/v13.go:28-34` | `GET /providers`, `PUT /providers/{id}/token`, `GET /providers/{id}/regions|servers`, `GET /providers/health`, `GET /providers/{id}/health`, `POST /providers/{id}/validate|check|reset`, `GET|PUT /providers/{id}/prefs`. |
| UI | `web/app/(app)/infra/*`, UI spec §16 | Tab VPS · Sandbox API · Sức khoẻ · Bảng giá · Tốc độ · Mesh · Adapter. Không có màn tài khoản. |

### 1.1 Lỗ hổng (gaps)

1. **Một token/provider** → không dự phòng; tài khoản bị khoá (lạm dụng, hết tiền) là sập cả provider. Rủi ro này đã được nêu ở spec v1.0 §8 ("nhiều tài khoản provider") nhưng chưa có mô hình.
2. **Host không gắn account** → không thể xoá/đọc trạng thái đúng tài khoản; đổi/rotate token nguy hiểm.
3. **Không có số dư/credit/chi tiêu thật** theo tài khoản; không dự báo cạn tiền.
4. **Không có mốc thời gian**: hạn token (DO/Fly/Daytona/Vultr cho đặt hạn), hạn refresh token netcup (30 ngày không dùng là chết), hạn credit khuyến mãi (E2B $100 một lần, Linode promo, Fly starter credit 7 ngày), ngày chốt hoá đơn, hạn hợp đồng (Contabo 1/3/6/12 tháng).
5. **Không biết hạn mức tài khoản** (DO `droplet_limit`, Hetzner limits, E2B concurrency 10/100, Daytona tier vCPU, Runpod `spendLimit`/h) → scheduler chỉ biết khi tạo hỏng.
6. **Breaker theo provider** trộn lẫn lỗi của từng tài khoản (401 của tài khoản A làm "đỏ" cả provider) và ngược lại (capacity hết ở vùng là lỗi của provider, không phải của tài khoản).
7. **Env token ghi đè dashboard**, không thể khai báo nhiều khoá qua env.
8. **Không có cảnh báo** về tiền/hạn; không audit việc xem/đổi khoá ngoài event `provider.connected/disconnected`.
9. **Không có trần chi tiêu thật** theo tài khoản (ngày/tháng), chỉ có trần tính từ giá niêm yết.
10. **UI** không có nơi xem "tài khoản nào đang gánh bao nhiêu, còn bao nhiêu tiền, còn bao lâu".

---

## 2. Nghiên cứu: năng lực từng provider

Ghi chú: "API số dư" = có endpoint đọc số dư/credit/chi tiêu bằng chính khoá mà Boxmes dùng (hoặc khoá phụ chỉ-đọc). "Nhập tay" = admin nhập, Boxmes ước tính tiếp từ ledger. Ngày đọc nguồn: 2026-11-27. Những ô ghi "chưa xác minh" là không tìm thấy tài liệu công khai trong lần nghiên cứu này.

### 2.1 Bảng năng lực

| Provider | API số dư / chi tiêu | Endpoint | API hạn mức | Rate limit | Token / hạn | Mô hình trả tiền | Ghi chú & nguồn |
|---|---|---|---|---|---|---|---|
| **Hetzner Cloud** | **Không** (chỉ bảng giá `GET /v1/pricing`) | — | Không có endpoint limits; lỗi `resource_limit_exceeded` khi chạm | 3600 req/giờ **mỗi project**, chung cho mọi token của project; header `RateLimit-Limit/Remaining/Reset` | Token theo project, không hạn | Postpaid, hoá đơn tháng; cost alert theo project chỉ qua email | Ước tính chi phí = kiểm kê × bảng giá. Mặc định 20 project/tài khoản. [docs.hetzner.cloud](https://docs.hetzner.cloud/reference/cloud) · [Hetzner FAQ](https://docs.hetzner.com/cloud/general/faq) · [CloudTally](https://cloudtally.eu/blog/how-to-track-hetzner-cloud-costs) |
| **Vultr** | **Có** | `GET /v2/account` → `balance` (âm = có credit), `pending_charges`, `last_payment_date/amount`, `acls` | Không thấy endpoint limits | ~30 req/s/IP, 429 + `Retry-After` | API key; có mục "API Key Expiration" trong docs (chưa đọc chi tiết); hỗ trợ nhiều key, sub-account | Prepaid credit + postpaid | `acls` cho biết key có quyền `billing`. [vultr.com/api](https://www.vultr.com/api/) · [rate limit](https://docs.vultr.com/support/platform/api/what-rate-limits-apply-to-the-vultr-api) |
| **DigitalOcean** | **Có** | `GET /v2/customers/my/balance` → `account_balance`, `month_to_date_usage`, `month_to_date_balance`; `/v2/customers/my/invoices` (+ `preview` hằng ngày); prepayment status | **Có**: `GET /v2/account` → `droplet_limit`, `volume_limit`, `reserved_ip_limit` | 5000 req/giờ + 250 req/phút **mỗi token** | PAT có hạn 30/60/90 ngày/1 năm/không hạn; custom scopes (`account:read`, `billing:read`…), scope không sửa được | Postpaid, hoá đơn ngày 1 hằng tháng; có prepayment | [Billing API](https://docs.digitalocean.com/reference/api/reference/billing) · [Account](https://docs.digitalocean.com/reference/api/reference/account) · [rate limit](https://docs.digitalocean.com/reference/api/reference/public-apis) · [scopes](https://docs.digitalocean.com/reference/api/scopes) |
| **Contabo** | **Không** thấy endpoint số dư/hoá đơn trong OpenAPI | — | Không | Có 429, con số không công bố | OAuth2 password grant (client id/secret + API user/password) → access token ngắn hạn | **Hợp đồng trả trước 1/3/6/12 tháng** theo từng VPS | Cần theo dõi **ngày hết hạn hợp đồng từng VPS** (nhập tay hoặc từ trường instance nếu có). [api.contabo.com](https://api.contabo.com/) · [user mgmt](https://docs.contabo.com/docs/account-management/user-management) |
| **netcup** | **Không** (SCP REST chỉ quản lý server; hoá đơn ở CCP) | — | Không | Chưa công bố | Device-flow OIDC → **refresh token offline sống mãi nếu dùng ít nhất 1 lần/30 ngày**; access token 300 s | Hợp đồng tháng/năm | Boxmes phải "chạm" refresh token định kỳ (≤ 7 ngày) và cảnh báo nếu hỏng. [netcup REST](https://www.netcup.com/en/helpcenter/documentation/servercontrolpanel/api) · [Authentication.md](https://github.com/digilolnet/go-netcup-scp/blob/master/Authentication.md) |
| **E2B** | **Không** có API số dư công khai (Usage page trong console) | — | Hạn mức theo gói: Hobby 10 sandbox đồng thời, 1 tạo/s, 1 giờ chạy liên tục; Pro 100–1.100, 5 tạo/s, 24 giờ | 10–20 req/s mỗi endpoint, chung cả project; 429 + `Retry-After` | API key | Usage-based, $100 credit một lần (Hobby); **hết credit không có thẻ → khoá mọi project**; tính tiền đầu tháng; spending limit trong console | Hobby concurrency giảm 20→10 ngày 24/11/2026. [docs.e2b.dev/billing](https://docs.e2b.dev/billing) · [UsagePricing](https://www.usagepricing.com/blueprint/activity/e2b-2026-11-24-hobby-concurrency-halved) |
| **Novita** | **Có** | `GET https://api.novita.ai/openapi/v1/billing/balance/detail` → `availableBalance`, `cashBalance`, `creditLimit`, `pendingCharges`, `outstandingInvoices` (đơn vị 1/10000 USD); + hoá đơn tháng/usage | Chưa xác minh | Billing trả 429 `LIST_BILL_TOO_FAST` khi gọi dồn | API key, không thấy scope | Prepaid + hạn mức nợ | [Novita balance](https://docs.novita.ai/api-reference/basic-get-user-balance) |
| **Daytona** | **Có** (có điều kiện) | `GET /orgs/{org_id}/billing/balances` (scope `read:billing`) — tài liệu mới yêu cầu user JWT; tổng = free + paid credit, "spent this month", "amount due" | **Có**: limits tổ chức qua SDK/CLI (`read:limits`), trả `usage/limit` (vd. concurrent sandboxes) | Theo project | API key có thể đặt hạn (`--expires`), có permissions | Pay-as-you-go, ví credit; tier theo số tiền nạp (Tier 2 cần $25, Tier 3 $500, Tier 4 $2000/30 ngày) | Tài liệu Daytona đang chuyển phiên bản (daytona.io → daytona.com) — cần kiểm lại khi làm adapter. [Billing](https://www.daytona.com/docs/billing) · [Billing API](https://www.daytona.com/docs/api/daytona/billing) · [Limits tiers](https://www.daytona.io/docs/limits) · [API keys](https://www.daytona.com/docs/api-keys) |
| **Render** | Không thấy API billing công khai | — | Sandboxes mặc định 1.000 đồng thời (Pro/Scale) | Chưa xác minh | API key | Gói workspace + usage | Nhập tay. [render.com/pricing](https://render.com/pricing) |
| **Vercel (Sandbox)** | **Có** (chi tiêu, không phải số dư) | `GET /v1/billing/charges` (FOCUS v1.3, JSONL, độ phân giải ngày) | Concurrency 10 (Hobby) / 10.000 (Pro) | Chưa xác minh | Token | Pro $20 credit/tháng rồi usage; Spend Management (ngưỡng, webhook, pause) | [billing charges](https://vercel.com/changelog/access-billing-usage-cost-data-api) · [Sandbox pricing](https://vercel.com/docs/sandbox/pricing) · [Spend Mgmt](https://vercel.com/docs/spend-management) |
| **Runpod** (kế hoạch GPU) | **Có** | GraphQL `myself { clientBalance currentSpendPerHr spendLimit }`; API v2 `GET` account limits | **Có** (spend limit $/giờ, mặc định $80/h) | Chưa xác minh | API key | Prepaid credit | GraphQL sẽ ngừng ~đầu 2027 theo ghi chú cộng đồng — cần theo dõi. [runpodctl](https://github.com/runpod/runpodctl/blob/main/internal/api/gpu.go) · [billing](https://docs.runpod.io/accounts-billing/billing) · [account limits v2](https://docs.runpod.io/api-reference-v2/account/get-account-limits) |
| **Linode/Akamai** (kế hoạch) | **Có** | `GET /v4/account` → `balance`, `balance_uninvoiced`, `active_promotions[].credit_remaining/expire_dt` | Chưa xác minh | Chưa xác minh | PAT có hạn + scope `account:read_only` | Postpaid | Có **ngày hết hạn promo** → nguồn tốt cho cảnh báo credit expiry. [get-account](https://techdocs.akamai.com/linode-api/reference/get-account) |
| **Scaleway** (kế hoạch) | **Có** (tiêu thụ) | `GET /billing/v2beta1/consumptions?organization_id=` | Chưa xác minh | Chưa xác minh | API key IAM (có thể đặt hạn) | Postpaid | [Billing API](https://www.scaleway.com/en/developers/api/billing) |
| **OVHcloud** (kế hoạch) | **Có** | `GET /me/debtAccount`, `/me/billing/report/consumption` | Chưa xác minh | Chưa xác minh | OAuth2/app key | Postpaid | [OVH Profile API](https://developers.eu.ovhcloud.com/reference/account/profile.md) |
| **Fly.io** (kế hoạch) | Không thấy API số dư (hoá đơn qua Stripe portal) | — | Giới hạn máy cho org mới (theo cộng đồng ~50, nâng qua support) | Machines API 1 req/s/hành động/máy (burst 3), Get 5 req/s | Token có hạn, **mặc định 20 năm**, nên đặt `-x` | Hoá đơn tháng theo org; tài khoản mới $1 credit tối đa 7 ngày | [tokens](https://fly.io/docs/security/tokens) · [rate limits](https://fly.io/docs/machines/api/working-with-machines-api) · [billing](https://fly.io/docs/about/billing) |
| **Modal** (kế hoạch) | **Có** (báo cáo chi phí, gói Team/Enterprise) | SDK `Workspace.billing.report(start, end, resolution)` / `modal billing report` | Chưa xác minh | Chưa xác minh | Token id/secret | Usage + credit tháng | Báo cáo trước credit; dữ liệu trễ vài phút. [modal billing](https://modal.com/docs/guide/billing.md) |
| **Prized, boxd.sh, Agent 37, boat, Upstash Box, Blaxel** | Chưa xác minh | — | Chưa xác minh | Chưa xác minh | API key | — | Mặc định "nhập tay" cho tới khi adapter khai báo năng lực. |
| **Runloop** | — | — | — | — | — | — | Đã đóng 13/11/2026 (spec v1.0 M6) — chỉ hiển thị lịch sử. |

### 2.2 Bài học từ các công cụ trưởng thành

| Công cụ | Mẫu đáng mượn | Nguồn |
|---|---|---|
| **LiteLLM Router** | Nhiều "deployment" cùng một nhóm; chiến lược `simple-shuffle` theo trọng số, `least-busy`, `usage-based`; `order` (ưu tiên 1→2→3); **cooldown theo deployment** (không theo cả nhóm) khi vượt `allowed_fails` trong cửa sổ; 429 → cooldown ngay; retry policy theo loại lỗi; honor `Retry-After` (tối đa 1 phút) rồi backoff mũ có jitter. | [routing](https://docs.litellm.ai/docs/routing) · [reliability](https://docs.litellm.ai/docs/router/learn/reliability) · [load balancing](https://docs.litellm.ai/docs/proxy/load_balancing) |
| **OpenRouter** | Mỗi key có `limit`, `limit_remaining`, `limit_reset` (daily/weekly/monthly), `usage_daily/weekly/monthly`; tách **402 = hết credit**, **403 = chạm budget**, **429 = rate limit**. | [limits](https://openrouter.ai/docs/api_reference/limits) · [key](https://openrouter.ai/docs/api/api-reference/api-keys/get-current-key) |
| **SkyPilot** | Auto-failover qua vùng rồi qua cloud; **blocklist tạm** vị trí lỗi cho request hiện tại; kiểm quota trước (~1 s) thay vì thử tạo hỏng (~30 s); phân biệt lỗi config/auth (không retry) với capacity/quota (thử chỗ khác). | [auto-failover](https://docs.skypilot.co/en/latest/examples/auto-failover.html) · [issue #10119](https://github.com/skypilot-org/skypilot/issues/10119) |
| **Vantage / CloudZero** | Gộp nhiều tài khoản/provider một màn; budget theo kỳ với ngưỡng % (CloudZero 10%→100%, chỉ báo ngưỡng cao nhất vừa vượt); forecast cuối kỳ; anomaly detection chi phí ngày. | [Vantage budgets](https://docs.vantage.sh/budgets) · [CloudZero notifications](https://docs.cloudzero.com/docs/notifications) |
| **Vercel Spend Management** | Ngưỡng chi → thông báo / webhook / tự dừng; lưu ý kiểm tra **trễ vài phút** nên đặt trần thấp hơn mức tối đa chịu được. | [spend management](https://vercel.com/docs/spend-management) |
| **Terraform/Pulumi multi-account** | Provider alias/nhiều cấu hình cho cùng loại provider; tài nguyên luôn nhớ provider-config đã tạo ra nó → tương đương `host.account_id` bắt buộc. | (mẫu chung) |

Kết luận áp dụng cho Boxmes:
- **Capacity/hết hàng vùng là lỗi của provider-region**, không đổi tài khoản cùng provider (vô ích) — chuyển vùng/provider. **Auth/402/hết credit/quota** là lỗi của **tài khoản** — chuyển tài khoản cùng provider trước.
- Breaker cần **hai tầng**: account và provider. Provider "đỏ" khi ≥2 account cùng lỗi `server/timeout/network` hoặc chỉ có một account.
- Số dư chỉ là **tín hiệu**, không phải hàng rào cứng tuyệt đối (trễ cập nhật). Dùng thêm **trần chi tiêu nội bộ** tính từ ledger.

---

## 3. Mô hình dữ liệu

### 3.1 Bảng mới `provider_accounts` (migration `005_provider_accounts.sql`)

Theo mẫu store hiện tại (cột khoá + `doc` JSON):

```sql
CREATE TABLE IF NOT EXISTS provider_accounts (
  id TEXT PRIMARY KEY,            -- pa_<rand>
  provider TEXT NOT NULL,         -- hetzner, vultr, e2b...
  owner_workspace_id TEXT,        -- NULL = tài khoản nền tảng; có giá trị = BYOC (Pha C)
  created_at TEXT NOT NULL,
  doc TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS provider_accounts_provider ON provider_accounts(provider);

CREATE TABLE IF NOT EXISTS provider_account_snapshots (  -- chuỗi số dư/chi tiêu để vẽ và dự báo
  account_id TEXT NOT NULL, ts TEXT NOT NULL, doc TEXT NOT NULL,
  PRIMARY KEY (account_id, ts)
);
```

`model.ProviderAccount` (JSON trong `doc`):

| Nhóm | Trường | Ý nghĩa |
|---|---|---|
| Định danh | `id`, `provider`, `label` ("Hetzner EU #2"), `external_id` (project id / org id / team id), `owner_workspace_id`, `tags[]`, `owner` (người chịu trách nhiệm), `notes`, `console_url` | |
| Credential | `credential_ref` (`vault:<secret_id>` hoặc `env:HETZNER_TOKEN__EU2`), `credential_source` (`api`/`env`/`vault`), `key_fingerprint` (sha256 8 ký tự + 4 ký tự cuối), `scopes[]` (đọc được: DO, Vultr acls, Daytona), `token_expires_at`, `refresh_expires_at` (netcup: last_used + 30 ngày), `rotated_at`, `rotate_every_days` | Không bao giờ trả token ra API; chỉ fingerprint. |
| Điều phối | `enabled`, `priority` (1 = trước), `weight` (cho chiến lược weighted), `regions[]` (cho phép; rỗng = theo prefs provider), `pool` (free/pro/team/burst — gắn pool spec M18), `drain` (không nhận host mới, giữ host cũ) | |
| Trạng thái | `status`: `active · cooling · rate_limited · quota_full · low_balance · out_of_credit · auth_failed · token_expiring · disabled · draining · archived`, `status_reason`, `status_since`, `cooldown_until`, `error_streak`, `last_error_class`, `last_error`, `last_ok_at`, `last_check_at` | Trạng thái tính lại sau mỗi check/tạo. |
| Tiền | `billing_model` (`prepaid`/`postpaid`/`contract`/`free_tier`), `currency` (USD/EUR), `balance` (dương = còn tiền), `balance_source` (`api`/`manual`/`estimated`), `balance_at`, `credit_total`, `credit_remaining`, `credit_expires_at`, `pending_charges`, `month_to_date`, `spend_today` / `spend_month` (Boxmes ledger), `spend_cap_day`, `spend_cap_month`, `min_balance` (sàn: dưới mức này không tạo host mới), `low_balance_warn` | `spend_*` luôn có (từ ledger) kể cả provider không có API. |
| Kỳ hoá đơn | `billing_cycle` (`monthly`/`hourly-prepaid`/`contract`), `invoice_day` (vd. 1), `next_invoice_at`, `trial_ends_at`, `contract_ends_at` (Contabo; có thể theo từng host — xem §3.2) | |
| Hạn mức | `limits`: `{servers, vcpu, ram_gb, concurrent_sandboxes, create_per_sec, spend_per_hour, projects}` mỗi mục `{limit, used, source: api/manual/learned, at}`; `rate_limit`: `{limit, remaining, reset_at}` từ header | "learned" = suy ra khi gặp `resource_limit_exceeded`. |
| Thống kê | `hosts_live`, `boxes_live`, `burn_per_hour` (EWMA), `runway_hours`, `forecast_month_end` | Tính, không lưu cứng (cache). |
| Audit | `created_by`, `updated_at`, `updated_by` | |

### 3.2 Thay đổi mô hình cũ

- `model.Host`: thêm `AccountID string` + `ContractEndsAt *time.Time` (Contabo/netcup).
- `model.LedgerEntry`: thêm `AccountID string` (để có chi tiêu thật theo tài khoản).
- `model.Box` (sandbox T2 không có host riêng): đã có host ảo; dùng `Host.AccountID`.
- `model.Provider`: giữ, thêm trường tổng hợp chỉ-đọc `Accounts{total, active, degraded}`, `Balance{sum, currency}`, `RunwayHours` (min). `TokenEnc/TokenSource` thành **deprecated**, chỉ đọc khi migrate.
- `ProviderPrefs`: giữ (cấp provider). Thêm `Strategy` (`priority`|`weighted`|`round_robin`|`least_used`|`drain_first`), `StickyMinutes`, `FailoverAcrossAccounts bool` (mặc định true).
- `provhealth`: khoá breaker thành `provider` **và** `provider/account_id`.

### 3.3 Credential & env nhiều khoá

- Dashboard: token mới lưu vào **vault hiện có** (bảng secrets mã hoá, tên dành riêng `BOXMES_SYS_PROVIDER_<ACCOUNT_ID>`), account chỉ giữ `credential_ref`. Lý do: tái dùng mã hoá + chặn đọc qua `/secrets` (tiền tố `BOXMES_SYS_` đã bị chặn).
- Env: giữ biến cũ (= account "env-default") và thêm dạng có nhãn `HETZNER_TOKEN__EU2=...`, `E2B_API_KEY__BACKUP=...` (hai gạch dưới + nhãn). Với credential nhiều phần (Contabo): `CONTABO_CLIENT_ID__B`, … cùng nhãn. Env account có `credential_source=env`, **không sửa/xoá được từ UI** (chỉ tắt/drain), và **không còn ghi đè** account dashboard.
- Khoá chỉ-đọc billing tách riêng (tuỳ chọn): `billing_credential_ref` cho Vultr (`billing.Read`), DO (`billing:read`), OpenRouter-style management key, Daytona JWT. Khoá tạo server không cần quyền billing.

---

## 4. Chọn tài khoản & failover

### 4.1 Lọc đủ điều kiện (theo thứ tự, mỗi bước ghi lý do vào `placement_reason`)

1. `enabled && !drain && status ∉ {auth_failed, disabled, archived, out_of_credit}`.
2. `cooldown_until < now` và breaker account không mở; breaker provider không mở.
3. Vùng: `region ∈ account.regions ∩ prefs.regions` (rỗng = mọi vùng).
4. Hạn mức: `limits.servers.used + 1 ≤ limit` (tương tự vCPU, concurrent sandboxes); nếu `source=learned` thì coi là trần mềm.
5. Tiền: `balance_effective - giá_dự_kiến(TTL hoặc 24h) ≥ min_balance` (prepaid); `spend_today + giá_giờ ≤ spend_cap_day`, `spend_month + … ≤ spend_cap_month`; `forecast_month_end ≤ spend_cap_month` (postpaid).
6. Pool: khớp pool của workspace (Free/Pro/Team) nếu account có `pool`.

`balance_effective` = số dư API mới nhất − chi phí ledger kể từ `balance_at` (đỡ trễ), hoặc số nhập tay − ledger kể từ lúc nhập.

### 4.2 Xếp hạng trong cùng provider

- `priority` (mặc định): account priority nhỏ nhất; hoà → ít host hơn.
- `weighted`: random theo `weight` (giống LiteLLM simple-shuffle).
- `round_robin`: con trỏ xoay vòng trong KV.
- `least_used`: tỷ lệ dùng hạn mức thấp nhất (`used/limit`).
- `drain_first`: ưu tiên account sắp hết credit/sắp hết hạn credit (dùng hết credit khuyến mãi trước khi mất).
- **Sticky**: cùng workspace/agent trong `StickyMinutes` dùng lại account cũ (giảm phân mảnh hoá đơn, giữ snapshot cùng tài khoản).

Scheduler hiện chọn **offer (provider, region, sku)**; bước mới chèn **sau** khi đã chọn offer: `pickAccount(provider, region, price)`. Nếu không account nào đủ điều kiện → offer bị loại với lý do "no eligible account: …" và scheduler sang offer kế (provider/vùng khác) như chuỗi hiện có.

### 4.3 Ma trận failover theo lớp lỗi (`provhealth.Classify` + bổ sung)

| Lớp lỗi (ví dụ) | Áp lên | Hành động |
|---|---|---|
| `auth` (401/403, token bị thu hồi/hết hạn) | account | `status=auth_failed`, không retry account này, thử account kế cùng provider ngay; cảnh báo **critical**. |
| `payment` (402, "insufficient balance", "payment required", E2B workspace blocked) — **lớp mới** | account | `status=out_of_credit`, thử account kế; cảnh báo critical. |
| `quota` (`resource_limit_exceeded`, "droplet limit") | account | ghi `limits.*.limit = used` (learned), `status=quota_full` tới khi số host giảm hoặc 1 giờ; thử account kế. |
| `rate_limit` (429) | account (vì limit theo token/project) | `cooldown_until = now + Retry-After` (hoặc reset header; tối thiểu 10 s, tối đa 15 phút); thử account kế nếu có, nếu không thì chờ/đổi provider. |
| `capacity` (hết hàng vùng/SKU) | **provider×region** | không đổi account; blocklist (provider, region, sku) 10 phút cho mọi account; sang vùng/provider khác. |
| `server`/`timeout`/`network` | account, leo thang provider | `error_streak++`; ≥3 trong 5 phút → breaker account mở (60 s → 15 m, nhân đôi); ≥2 account cùng provider mở trong 5 phút → breaker provider mở. |
| `invalid` (sai sku/region/tham số) | yêu cầu | không failover account (lỗi của Boxmes/catalog); báo `provider.create_failed`. |

Giới hạn: tối đa 3 account/provider và 6 lần thử tổng cho một yêu cầu tạo (tránh bão gọi API). Mỗi lần chuyển account phát event `provider_account.failover {from, to, class}`.

### 4.4 Thao tác gắn account

Mọi thao tác trên host/box đã tồn tại (`Delete`, `Status`, `List`, `Wake`, snapshot, gateway domain) dùng `credentialFor(host.AccountID)`. Account bị `auth_failed` vẫn được thử khi xoá (có thể khoá chỉ hết hạn quyền tạo); nếu xoá hỏng → cảnh báo "host mồ côi, cần xử lý tay" kèm `console_url`.

---

## 5. Thăm dò (polling) & ước tính

| Việc | Chu kỳ mặc định | Điều kiện / ghi chú |
|---|---|---|
| Probe sức khoẻ (Validate) | 5 phút / account | Gộp với đọc số dư khi cùng endpoint (Vultr `/v2/account`, DO `/v2/account`). Bỏ qua khi `rate_limited`. Hetzner: chung 3600 req/giờ/project → probe ≤ 12/giờ là an toàn. |
| Số dư / chi tiêu (API) | 15 phút (prepaid) · 60 phút (postpaid) | Vultr, DO, Novita, Daytona, Runpod, Linode, Scaleway, OVH, Vercel charges (ngày), Modal (giờ). Novita: không dồn gọi (429 `LIST_BILL_TOO_FAST`). |
| Hạn mức (API) | 6 giờ + ngay sau lỗi `quota` | DO `droplet_limit`, Daytona limits, Runpod limits. |
| Ước tính (không API) | mỗi lần ledger ghi | `estimated = manual_balance − Σ ledger(account) từ manual_at`; Hetzner: kiểm kê × `/v1/pricing`. Hiển thị nhãn "ước tính". |
| Làm tươi refresh token (netcup) | 7 ngày | Đổi refresh → access; lỗi → `auth_failed` + critical. |
| Hạn token / credit / hợp đồng / hoá đơn | 1 lần/ngày (08:00 giờ admin) | Sinh cảnh báo theo §6. |
| Đối soát | đầu tháng | So `month_to_date` provider với ledger Boxmes; chênh > 10% → cảnh báo "lệch chi phí" (giá vốn sai, host mồ côi). |

Năng lực mỗi adapter khai báo qua interface tuỳ chọn (giống `Regioner`/`Lister`):

```go
type AccountInfo struct { Balance, PendingCharges, MonthToDate *float64; Currency string
    CreditRemaining *float64; CreditExpiresAt, TokenExpiresAt *time.Time
    Limits map[string]Limit; Scopes []string; ExternalID string }
type AccountReader interface { AccountInfo(ctx context.Context, token string) (AccountInfo, error) }
```

Thiếu interface ⇒ UI hiện ô "Nhập tay" cho các trường tiền/hạn.

### 5.1 Dự báo "còn X ngày"

- `burn_per_hour` = max( EWMA 24h của ledger(account)/giờ, Σ `price_hour` host đang sống của account ) — lấy max để không lạc quan khi vừa tạo host.
- Prepaid: `runway_hours = (balance_effective − min_balance) / burn_per_hour`; hiển thị "còn ~X ngày (tới ~dd/mm)"; nếu `credit_expires_at` sớm hơn → hiện "credit hết hạn trước, dự kiến bỏ phí $Y".
- Postpaid: `forecast_month_end = month_to_date + burn_per_hour × giờ_còn_lại_tháng`; so với `spend_cap_month`.
- Cấp provider: runway = runway của **tổng** các account đủ điều kiện (vì failover), và cảnh báo riêng khi account đang gánh tải chính sắp cạn.

---

## 6. Cảnh báo

Đi qua `notify` hiện có (Telegram/SMTP) + event (`events`) + mục "Cần anh" của PA. Dedup theo `(account, loại, ngưỡng)`; chỉ báo ngưỡng cao nhất vừa vượt (kiểu CloudZero); nhắc lại tối đa 1 lần/24 h nếu chưa xử lý; tự đóng khi điều kiện hết.

| Loại | Ngưỡng mặc định | Mức |
|---|---|---|
| `low_balance` | runway < 7 ngày (warn) · < 2 ngày (critical) · số dư < `low_balance_warn` | warn/critical |
| `out_of_credit` / `payment_failed` | lỗi 402/khoá workspace | critical |
| `credit_expiring` | `credit_expires_at` trong 14 / 3 ngày và còn > $1 | info/warn |
| `trial_ending` | `trial_ends_at` trong 7 / 1 ngày | warn |
| `token_expiring` | `token_expires_at` trong 14 / 3 ngày; netcup không làm tươi được | warn/critical |
| `rotation_due` | `rotated_at + rotate_every_days` đã qua | info |
| `limit_near` | `used/limit` ≥ 80% (warn) / 95% (critical) bất kỳ hạn mức | warn/critical |
| `spend_cap_near` | `spend_today`/`spend_month` ≥ 80% / 100% trần; forecast cuối tháng > trần | warn/critical |
| `failover` | có failover trong 15 phút (gom nhóm), hoặc provider chỉ còn 1 account khả dụng | info/warn |
| `auth_failed` | 401/403 | critical |
| `invoice_due` | `next_invoice_at` trong 3 ngày (postpaid, kèm forecast) | info |
| `contract_ending` | host Contabo/netcup hết hợp đồng trong 14 / 3 ngày | warn |
| `cost_mismatch` | đối soát lệch > 10% | warn |
| `orphan_host` | xoá host thất bại vì credential | critical |
| `balance_stale` | số dư API cũ > 3 chu kỳ, hoặc nhập tay > 30 ngày | info |

Tự động (cấu hình được, mặc định bật): `out_of_credit`/`auth_failed` → loại account khỏi chọn; `runway < 2 ngày` → `drain` (không nhận host mới); `spend_cap_month` chạm → loại account. **Không** tự xoá host đang chạy (quyết định thuộc admin; có nút "Di chuyển box" sau khi M5 P2 có).

---

## 7. Audit

Event mới (đi `Emit`/`EmitData`, `actor` = PA hoặc `system`), **không bao giờ** chứa token; chỉ `key_fingerprint`:

`provider_account.created · updated · enabled · disabled · drained · archived · key_rotated · key_revealed_denied · validated · check_failed · status_changed{from,to,reason} · failover{from,to,class,request} · balance_updated{source,old,new} · limit_learned · alert_opened/closed · orphan_host`.

Trang audit cho phép lọc theo `account_id`. Xuất CSV theo spec M20.

---

## 8. Màn hình admin (khu Nền tảng → Hạ tầng)

### 8.1 Tab mới **Tài khoản** (mặc định khi vào Hạ tầng)

- **Dải KPI**: tổng số dư khả dụng (quy USD, hiện EUR riêng), chi hôm nay / tháng này (ledger), dự báo cuối tháng, runway ngắn nhất ("Vultr #1 còn ~3 ngày"), số account khoẻ / suy giảm / hỏng, số cảnh báo mở.
- **Bảng provider** (một dòng/provider, mở rộng được): logo · tên · số account (khoẻ/tổng) · tổng số dư · chi tháng · runway · host/box đang chạy · % hạn mức cao nhất · sức khoẻ (p95 boot, tỷ lệ lỗi) · chiến lược · badge cảnh báo. Lọc: nhóm (VPS/Sandbox), trạng thái, "có cảnh báo".

### 8.2 Chi tiết provider `/infra/providers/[id]`

- Đầu trang: năng lực (API số dư ✓/✗, API hạn mức, rate limit đã biết, mô hình trả tiền) lấy từ bảng §2.1 + liên kết docs.
- **Bảng tài khoản**: nhãn · trạng thái (chip màu + lý do + cooldown đếm ngược) · ưu tiên/trọng số (kéo thả để đổi ưu tiên) · số dư (nguồn: API/ước tính/nhập tay, "cập nhật 12 phút trước") · chi hôm nay/tháng so với trần (thanh) · runway · hạn mức (thanh mini servers/vCPU/concurrency) · token hết hạn · credit hết hạn · host · hành động (Kiểm tra ngay, Drain, Tắt, Rotate key, Sửa).
- Cấu hình cấp provider (prefs cũ + chiến lược + sticky).
- Biểu đồ số dư/chi theo ngày (stack theo account) + đường dự báo.

### 8.3 Chi tiết tài khoản (ngăn kéo phải)

Tab: **Tổng quan** (các trường §3.1, nhập tay được khi không có API, có ghi "theo nhập tay ngày …") · **Hạn mức** · **Tiền** (snapshots, ledger theo account, đối soát) · **Host/Box** (danh sách gắn account) · **Dòng thời gian** (status_changed, failover, rotate, cảnh báo — từ audit) · **Cảnh báo** (ngưỡng riêng ghi đè mặc định).

### 8.4 Luồng thêm / rotate khoá

1. Chọn provider → hướng dẫn tạo khoá (quyền tối thiểu cần cho tạo server; quyền billing chỉ-đọc nếu muốn số dư; đặt hạn khuyến nghị 90 ngày trừ Hetzner).
2. Dán khoá (ô password) → `validate`: gọi Validate + AccountInfo; hiện email/org/project, scopes, số dư, hạn mức, **cảnh báo trùng** nếu `external_id` trùng một account khác (cùng tài khoản thật ⇒ không phải dự phòng thật).
3. Đặt nhãn, ưu tiên, vùng, pool, trần chi, ngưỡng, hạn credit/trial/hoá đơn (nếu không có API).
4. Lưu → account `active`.

**Rotate**: dán khoá mới → validate → kiểm `external_id` **trùng** account cũ (bắt buộc, để host cũ vẫn xoá được) → đổi `credential_ref` nguyên tử → `rotated_at` → giữ khoá cũ 24 h làm dự phòng đọc (tuỳ chọn) rồi xoá khỏi vault → nhắc thu hồi khoá cũ ở console provider. Khoá khác tài khoản ⇒ phải tạo account mới, không rotate.

### 8.5 Thay đổi các tab cũ

- **Sức khoẻ**: thêm cột/lọc theo account; breaker hai tầng; nút reset theo account.
- **VPS / Sandbox API**: cột "Tài khoản" trên host.
- **Tổng quan nền tảng / Admin → Doanh thu**: giá vốn theo provider **và** account.

---

## 9. API (admin, `ScopeAdmin` + PA)

Đặt dưới `/v1/admin/...` cho khu nền tảng (giữ tương thích các route `/providers/*` cũ):

| Method | Path | Mô tả |
|---|---|---|
| GET | `/v1/admin/providers` | Danh sách provider + tổng hợp account (KPI) |
| GET | `/v1/admin/providers/{p}` | Chi tiết provider (năng lực, prefs, chiến lược, tổng hợp) |
| GET | `/v1/admin/providers/{p}/accounts` | Danh sách account (không có token) |
| POST | `/v1/admin/providers/{p}/accounts/validate` | Kiểm khoá trước khi lưu, trả AccountInfo + cảnh báo trùng |
| POST | `/v1/admin/providers/{p}/accounts` | Tạo account `{label, token, priority, weight, regions, pool, caps, thresholds, billing…}` |
| GET | `/v1/admin/providers/{p}/accounts/{a}` | Chi tiết account |
| PATCH | `/v1/admin/providers/{p}/accounts/{a}` | Sửa trường (merge-patch), kể cả nhập tay số dư/hạn |
| POST | `/v1/admin/providers/{p}/accounts/{a}/rotate` | `{token}` → rotate (kiểm external_id) |
| POST | `/v1/admin/providers/{p}/accounts/{a}/check` | Probe + đọc số dư/hạn mức ngay |
| POST | `/v1/admin/providers/{p}/accounts/{a}/{enable|disable|drain|reset}` | Đổi trạng thái / reset breaker-cooldown |
| DELETE | `/v1/admin/providers/{p}/accounts/{a}` | Archive (chặn khi còn host sống, trừ `?force=1` + lý do) |
| GET | `/v1/admin/providers/{p}/accounts/{a}/snapshots?from=&to=` | Chuỗi số dư/chi |
| GET | `/v1/admin/providers/{p}/accounts/{a}/timeline` | Sự kiện audit của account |
| PUT | `/v1/admin/providers/{p}/strategy` | `{strategy, sticky_minutes, failover_across_accounts}` |
| GET | `/v1/admin/infra/alerts?status=open` · POST `/v1/admin/infra/alerts/{id}/ack` | Cảnh báo hạ tầng |
| GET | `/v1/admin/infra/forecast` | Runway & dự báo cuối tháng theo provider/account |

Tương thích: `PUT /providers/{id}/token` = upsert account `default` của provider (giữ CLI/UI cũ chạy). `GET /providers` thêm `accounts_summary`.

---

## 10. Kế hoạch migrate

1. **Migration 005**: tạo bảng; với mỗi `providers.doc` có `TokenEnc` → tạo account `pa_<provider>_default` (`label="Mặc định"`, `credential_source` = `TokenSource`, chuyển ciphertext sang vault, `priority=1`). Env token cũ → account `env-default`.
2. Gán `Host.AccountID` = account default của provider cho mọi host chưa xoá; `LedgerEntry.AccountID` cho bản ghi tháng hiện tại (bản ghi cũ để trống = "không rõ").
3. `providerToken(ctx, id)` → `credentialFor(ctx, accountID)`; giữ wrapper cũ trả account default (để code cũ chưa sửa vẫn chạy) và log deprecation.
4. `provhealth` snapshot cũ → gán cho account default + provider.
5. Song song 1 phiên bản: đọc cả `TokenEnc` (fallback) — xoá ở phiên bản sau.
6. Test e2e: 2 account giả lập (fake provider) — 401 ở A → tạo trên B; 429 → cooldown; capacity → không đổi account; xoá host gắn A dùng khoá A sau khi B là mặc định; rotate cùng external_id; rotate khác external_id bị từ chối; không có token nào trong response/event/log.

---

## 11. Lộ trình đề xuất

| Pha | Phiên bản | Nội dung | Lý do |
|---|---|---|---|
| **A — nền** | **v2.2** (cùng "An toàn & beta") | Bảng `provider_accounts`, `host.account_id`, `ledger.account_id`, env có nhãn, chọn account theo `priority`/`weighted`, ma trận failover §4.3 (thêm lớp `payment`), breaker 2 tầng, nhập tay số dư/hạn/hoá đơn, `spend_cap_*` theo ledger, cảnh báo: auth_failed, out_of_credit, low_balance (ước tính), token/credit/trial/contract expiring, failover, limit_near (learned); audit; UI tab Tài khoản + chi tiết provider + luồng thêm/rotate | Checklist trước beta đã yêu cầu "tài khoản provider có giới hạn chi" và rủi ro "lạm dụng làm khoá tài khoản provider" (spec v1.0 §7–§8). Không có `host.account_id` thì không thể thêm tài khoản thứ hai an toàn. |
| **B — đo thật** | v2.3 | `AccountReader` cho Vultr, DO, Novita, Daytona (+ Runpod/Linode khi có adapter); hạn mức qua API (DO, Daytona); làm tươi netcup; snapshots + biểu đồ; dự báo runway/cuối tháng; đối soát tháng; chiến lược `least_used`, `drain_first`, sticky; cảnh báo invoice_due, cost_mismatch, rotation_due | Cần dữ liệu thật để tối ưu giá vốn khi bắt đầu có doanh thu. |
| **C — BYOC** | v2.4 | `owner_workspace_id` ≠ NULL: workspace tự thêm account (M27); scheduler chỉ dùng account của workspace hoặc pool nền tảng nếu cho phép; cảnh báo gửi owner workspace | Tái dùng toàn bộ mô hình, không làm lại. |

---

## 12. Câu hỏi mở cho anh (owner)

1. **Nhiều tài khoản cùng provider có hợp lệ với ToS không?** Hetzner/Contabo có thể coi nhiều tài khoản cùng chủ là lách hạn mức/lạm dụng. Đề xuất: ưu tiên nhiều **project** (Hetzner) / sub-account (Vultr) / team (DO) trong cùng tài khoản thật cho hạn mức, và chỉ dùng tài khoản thật khác (pháp nhân/thẻ khác) cho dự phòng khi bị khoá — anh xác nhận chính sách?
2. **Chiến lược mặc định**: `priority` (dồn vào account chính, account phụ để dự phòng) hay `weighted` (rải đều để không account nào chạm hạn mức/abuse)? Em đề xuất `priority` cho VPS, `drain_first` cho sandbox có credit khuyến mãi.
3. **Tự động khi runway < 2 ngày**: chỉ `drain` (đề xuất) hay cho phép tự nạp tiền (cần lưu thẻ/thao tác ở console — hiện không có API nạp cho đa số provider)?
4. **Ngưỡng mặc định** ở §6 (7/2 ngày, 80/95%, 14/3 ngày) có ổn không? Kênh: Telegram cho critical, email cho tổng hợp ngày?
5. **Tiền tệ**: hiển thị quy đổi USD (dùng tỷ giá nào, cập nhật bao lâu) hay giữ EUR riêng cho Hetzner/netcup/Contabo?
6. **Lưu khoá billing chỉ-đọc riêng** có cần không, hay chấp nhận dùng chung khoá tạo server (đơn giản hơn nhưng quyền rộng hơn)?
7. **Đặt Pha A vào v2.2** (làm v2.2 dài thêm ~1 tuần) hay tách thành mốc riêng "v2.1.5 Hạ tầng"?
8. **Provider kế hoạch** nào cần adapter `AccountReader` sớm nhất (Runpod/Linode/Scaleway/OVH/Fly/Modal)?

---

## Phụ lục — Biến môi trường mới

`<PROVIDER_ENV>__<NHÃN>` (vd. `HETZNER_TOKEN__EU2`, `CONTABO_CLIENT_ID__B` …) · `BOXMES_ACCOUNT_POLL_BALANCE=15m` · `BOXMES_ACCOUNT_POLL_PROBE=5m` · `BOXMES_ACCOUNT_ALERT_RUNWAY_DAYS=7,2` · `BOXMES_ACCOUNT_AUTODRAIN=1` · `BOXMES_FX_SOURCE` (nếu quy đổi tiền tệ).
