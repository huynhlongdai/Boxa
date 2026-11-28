# Boxmes Web

Giao diện quản trị của **Boxmes**, hạ tầng cloud cá nhân cho AI agent. Anh dùng nó để giám sát, duyệt và cứu hộ; còn tạo box, chạy lệnh là việc của agent.

- Next.js 15 (App Router) + TypeScript + Tailwind + các component kiểu shadcn/ui (viết sẵn trong `components/ui`)
- Recharts (biểu đồ), xterm.js (terminal), cmdk (⌘K), SWR (dữ liệu), lucide-react (icon)
- Tiếng Việt toàn bộ, dark mode mặc định, responsive ở 390 / 768 / 1366
- Code bám theo `docs/api-contract-v1.md` (`lib/types.ts`, `lib/api.ts`)

## Chạy nhanh (MOCK MODE, không cần Core)

```bash
cd boxmes/web
pnpm install
BOXMES_MOCK=1 pnpm dev        # http://localhost:3000
```

Ở MOCK MODE, proxy trả dữ liệu giả lập đúng contract (agent `shopee-crawler`, `kecon-writer`, `replingo-bot`, `n8n-flows`, `hark-main`; box trên Hetzner fsn1/hel1, Vultr sgp, Contabo). Các thao tác ghi (wake/sleep/snapshot, tạo agent, duyệt, dừng khẩn cấp…) sửa trạng thái trong bộ nhớ, khởi động lại server là về như cũ. SSE `/stream` cũng được giả lập (chi phí tăng dần, sự kiện mới).

Khi chạy `pnpm dev` mà **không đặt** `BOXMES_MOCK` và Core không trả lời ở `BOXMES_API`, app tự chuyển sang mock.

## Chạy với Boxmes Core thật

```bash
BOXMES_API=http://localhost:8080/v1 BOXMES_MOCK=0 pnpm dev
```

Mở `/signup` (tài khoản đầu tiên thành quản trị nền tảng + owner `ws_default`) hoặc `/login` (email + mật khẩu). Chế độ cũ vẫn còn: ở `/login` bấm **Đăng nhập bằng API key** và dán admin key / key workspace. Cả hai đều lưu vào cookie `boxmes_key`; trình duyệt chỉ gọi `/api/boxmes/*`, route handler `app/api/boxmes/[...path]/route.ts` gắn `Authorization: Bearer <token>` rồi chuyển tiếp tới Core, nên token/key không bao giờ có trong JavaScript phía client.

### v2.0: phiên, workspace, CSRF

- **Phiên**: `POST /api/auth/login {email, password}` / `POST /api/auth/signup {email, password, name?, workspace?, invite_code?, workspace_invite?}` gọi Core (`/v1/auth/login|signup`), cất token `bxs_…` vào cookie `boxmes_key` **HttpOnly; Secure; SameSite=Lax; Path=/** (hạn theo `expires_at` của Core) và chỉ trả `{ok, user, workspace, role}`. Proxy từ chối `/api/boxmes/auth/login|signup` (404) để token không bao giờ đi qua JS. `POST /api/auth/logout[?all=1]` huỷ phiên ở Core rồi xoá cookie. `Secure` luôn bật, trừ `BOXMES_INSECURE_COOKIE=1` hoặc `next dev` qua http trên host không phải localhost.
- **`GET /api/session`**: `{mock, authed, mode: session|key|mock, key_prefix (chỉ chế độ key), ws_base, me}` — `me` là `/v1/auth/me` (user, workspace + role, workspaces[], permissions[], platform_admin). Với API key dashboard tự dựng `me` từ `/v1/workspaces/current` (quyền theo scope) và `/v1/admin/system` (200 = quản trị nền tảng). Không bao giờ chứa token.
- **Chống CSRF** (`lib/server/csrf.ts`, áp cho proxy và `/api/auth/*`), ba lớp:
  1. Cookie phiên `SameSite=Lax`: trình duyệt không gửi nó theo POST/PUT/PATCH/DELETE từ site khác.
  2. Request đổi trạng thái có `Origin` (thiếu thì `Referer`) phải trùng `Host` / `X-Forwarded-Host` của dashboard hoặc nằm trong `BOXMES_DASHBOARD_ORIGINS` → sai là 403 `POLICY_DENIED`, kể cả `/api/auth/login` (chặn login CSRF).
  3. Double-submit: khi request mang cookie phiên, header `X-Boxmes-CSRF` phải bằng cookie `boxmes_csrf` (không HttpOnly, `SameSite=Strict`, 128 bit ngẫu nhiên, cấp ở `/api/session`, xoay mỗi lần đăng nhập). Có cookie phiên mà thiếu cả Origin lẫn Referer vẫn phải qua lớp này. `lib/api.ts` tự gắn header; request không cookie phiên (curl, smoke MOCK) chỉ bị lớp 2 xét.
- **Workspace**: bộ chọn ở top bar (`POST /v1/auth/workspace`, tải lại mọi khoá SWR, nối lại SSE), "+ Tạo workspace" (`POST /v1/workspaces` rồi chuyển sang). Key chỉ thuộc một workspace nên không đổi được.
- **Quyền**: nav và nút ẩn theo `permissions[]` (`lib/session.ts`: `useSession().can(perm)`, `<Can perm>`); khu **Nền tảng** (Hạ tầng, Admin) chỉ hiện với `platform_admin`, vào thẳng URL thì gặp trang "cần vai trò…". Core vẫn là nơi quyết định (404 khác workspace, 403 `POLICY_DENIED`, 401 thiếu scope); 401 chỉ đẩy về `/login` khi `/api/session` xác nhận phiên đã hết.
- **Hạn mức**: `QUOTA_EXCEEDED` hiện toast riêng (hạn mức nào, đã dùng/tối đa từ `error.details`, gói, `hint`, `next`, nút **Xem gói & hạn mức** → `/settings/billing`, cũng là `upgrade_url` Core trả về).
- Smoke với Core thật: `BASE=http://localhost:3199 ADMIN_KEY=<admin key> bash scripts/smoke-v20.sh` (Next chạy `BOXMES_MOCK=0 BOXMES_API=<core>/v1`, Core mới chưa có user): đăng ký → cookie → CSRF → workspace → mời → xem lời mời → đăng ký bằng `workspace_invite` → accept → đổi workspace → thành viên / owner cuối → QUOTA_EXCEEDED → vị trí box → đăng xuất → chế độ admin key.

## Biến môi trường

| Biến | Mặc định | Ý nghĩa |
|---|---|---|
| `BOXMES_API` | `http://localhost:8080/v1` | URL Core cho proxy phía server (đọc lúc chạy) |
| `NEXT_PUBLIC_BOXMES_API` | `http://localhost:8080/v1` | Dùng lúc build: hiển thị trong snippet cấu hình, suy ra URL WebSocket terminal. Proxy cũng dùng nếu thiếu `BOXMES_API` |
| `NEXT_PUBLIC_BOXMES_WS` | (`core_url` của ticket, rồi suy từ `NEXT_PUBLIC_BOXMES_API`, `http→ws`) | URL WebSocket riêng cho terminal, vd `wss://boxmes.example.com/v1` |

**Phía Core (v1.10)**: đặt `BOXMES_DASHBOARD_URL=https://<domain dashboard>` (hoặc thêm vào `BOXMES_ACCESS_ORIGINS`, cách nhau dấu phẩy) để Core nhận WebSocket terminal từ origin của dashboard; thiếu thì Core trả 403 `POLICY_DENIED` trước khi upgrade. Dev trên localhost được chấp nhận khi Core cũng chạy trên loopback.
| `BOXMES_MOCK` | (trống) | `1` luôn mock · `0` không bao giờ · trống = tự mock khi dev mà Core không chạy |
| `BOXMES_INSECURE_COOKIE` | (trống) | `1` cho phép cookie không `Secure` khi chạy production qua http nội bộ |
| `BOXMES_DASHBOARD_ORIGINS` | (trống; rơi về `BOXMES_DASHBOARD_URL`) | v2.0: origin được phép gửi request đổi trạng thái ngoài chính `Host` của dashboard (cách nhau dấu phẩy), khi reverse proxy đổi Host |

Xem `.env.example`.

## Lệnh

```bash
pnpm dev         # dev server :3000
pnpm build       # build production (output: standalone)
pnpm start       # next start (xem ghi chú standalone bên dưới)
pnpm lint        # ESLint (next/core-web-vitals + typescript)
pnpm typecheck   # tsc --noEmit
pnpm smoke       # sau khi build: chạy server mock trên :3199, curl mọi trang + API, kiểm SSE
pnpm i18n:check  # key/placeholder đủ ở vi/en/zh/ja, không còn chuỗi tiếng Việt cứng
```

Kiểm tra tràn layout + tiêu đề mồ côi (vi/en) ở 390/1366/1600 px, cả 4 ngôn ngữ (cần Chromium + `playwright-core`):

```bash
BASE=http://localhost:3199 CHROMIUM_PATH=… node scripts/overflow-check.mjs   # VIEWPORTS=390x844,1366x900,1600x900 mặc định
```

Chụp màn hình (tuỳ chọn, cần Chromium + `playwright-core`):

```bash
CHROMIUM_PATH=/usr/bin/chromium BASE=http://localhost:3199 OUT=./docs/screenshots node scripts/screenshots.mjs
```

## Docker

```bash
docker build -t boxmes-web --build-arg NEXT_PUBLIC_BOXMES_API=https://boxmes.example.com/v1 .
docker run -p 3000:3000 -e BOXMES_API=http://boxmes-core:8080/v1 -e BOXMES_MOCK=0 boxmes-web
# demo không cần Core:
docker run -p 3000:3000 -e BOXMES_MOCK=1 boxmes-web
```

Ảnh dùng `output: "standalone"` (`node server.js`). Khi chạy standalone ngoài Docker, nhớ copy `.next/static` vào `.next/standalone/.next/static` và `public/` vào `.next/standalone/public` (script `smoke.sh` đã làm việc này).

## Màn hình (routes)

| Route | Nội dung |
|---|---|
| `/` Tổng quan | 4 thẻ chỉ số kèm thanh ngân sách; hộp **Cần anh** (Duyệt / Từ chối); chi phí 7 ngày xếp chồng theo agent; top 5 task tốn nhất hôm nay; nút đỏ **Dừng khẩn cấp** (2 bước, gõ `DUNG`) |
| `/boxes` | Bảng lọc theo trạng thái / agent / provider / lifecycle (lọc lưu trên URL), thẻ trên mobile, nút nhanh wake / sleep / snapshot / xoá; tạo box thủ công (có `Idempotency-Key`). v2.0: chip **Vị trí** theo vùng › site có đếm (`GET /v1/boxes/locations`, lọc `?region=` / `?location=`), **Nhóm theo** Không nhóm / Vị trí / Agent / Trạng thái (nhóm Vị trí: "EU — Đức · Falkenstein (Hetzner FSN) · 3 box · $0.61 hôm nay"); sidebar Boxes xổ ra EU › Falkenstein, Helsinki… |
| `/approvals` | v2.0 **Cần anh**: yêu cầu chờ duyệt (nút Duyệt/Từ chối chỉ với `approvals:decide`) |
| `/boxes/[id]` | Tab Tổng quan (spec, URL, lý do Scheduler chọn, **egress allowlist** — đang thực thi bằng incus ACL/nftables hay chỉ cảnh báo trên driver local, sửa danh sách —, chi phí giờ, ngân sách còn lại, CPU/RAM/đĩa) · **Port & app** (v1.7: URL Boxmes từng port, bật tắt chỉ-token, cấp / liệt kê / thu hồi URL token, wake on request, badge capability của provider, trạng thái app + restart + log SSE; cảnh báo URL preview của provider vẫn mở với ai biết sandbox id) · Terminal (xterm + WebSocket, **Chiếm quyền / Trả quyền**) · Timeline (lệnh, exit code, thời lượng, token tiết kiệm) · Files (`/work`, `/memory`, xem + tải về) · Logs · Snapshots (khôi phục / fork, nơi lưu: R2/B2/S3 hay tại host, lỗi upload) · Cấu hình (box.yaml chỉ đọc + **Lưu thành template**) |
| `/agents` | Danh sách + **wizard 3 bước**: tên + kind → ngân sách + chính sách (provider, vùng, egress) → API key hiện một lần + đoạn cấu hình sinh sẵn theo kind (Claude Code MCP JSON, OpenAI Agents Python, n8n HTTP node, Hark, cURL) có nút Copy |
| `/agents/[id]` | Keys (tạo / thu hồi), ngân sách, chính sách, dung lượng /memory, lịch sử box |
| `/costs` | Ledger lọc theo agent / task / provider / ngày, xuất CSV (`/costs.csv`), biểu đồ theo provider, pool vs burst, 7 ngày; thẻ gợi ý tiết kiệm |
| `/infra` | v2.2: tab mặc định **Tài khoản** — dải KPI (số dư khả dụng quy USD + tiền gốc EUR, chi hôm nay / tháng, dự báo cuối tháng, runway ngắn nhất, tài khoản khoẻ / suy giảm / hỏng, cảnh báo mở), bảng provider (lọc VPS / Sandbox / "chỉ có cảnh báo") và danh sách cảnh báo hạ tầng (lọc trạng thái, **Đã biết** = ack). Tab **Provider & host** (`?s=providers`): Chuỗi dự phòng (pool → Hetzner → Vultr → DigitalOcean → Contabo → netcup), thẻ provider (nhập + kiểm tra token có gợi ý định dạng riêng cho Contabo/netcup, nhãn “Dự phòng #n”, token từ env, danh sách vùng), host với thanh CPU/RAM/đĩa + drain / xoá, thêm host pool, bảng giá sắp theo $/vCPU-giờ với nhãn độ tin cậy, đề xuất mở rộng pool |
| `/infra/providers/[id]` | v2.2 (M29 + M29-S, chỉ quản trị nền tảng): KPI provider, năng lực (API số dư / hạn mức / template), **chiến lược chọn tài khoản** (5 chiến lược `priority` · `weighted` · `round_robin` · `least_used` · `drain_first`, sticky phút, failover giữa tài khoản), bảng tài khoản (nhãn, trạng thái + lý do + cooldown đếm ngược, ưu tiên / trọng số, số dư USD kèm tiền gốc + nguồn, chi so với trần, runway, thanh hạn mức "học được", hết hạn gần nhất, breaker, host; bật "gồm đã lưu trữ"), **thêm khoá 3 bước** (provider + nhãn + external id / ưu tiên / tiền tệ / nhóm → khoá + khoá billing chỉ-đọc tuỳ chọn → kiểm tra (`validate`, báo trùng tài khoản thật) & lưu), **ngăn kéo tài khoản** (Tổng quan · Sửa: ưu tiên, trọng số, vùng, pool, số dư / credit nhập tay, tỷ giá, sàn, trần chi, hạn token / credit / trial / hợp đồng, hạn mức, `limit_group` / `billing_group`, template, scopes · Khoá: rotate bắt buộc cùng `external_id` · Dòng thời gian), hành động Kiểm tra ngay / Bật / Tắt / Drain / Bỏ drain / Reset / Xoá (lưu trữ; còn host → bắt lý do; purge khi không còn host). Provider sandbox có tab **Sandbox** (`?tab=sandbox`): tài khoản theo khoá — slot `group_running + in_flight / concurrent_limit` (nhãn "học được"), tốc độ tạo, nhóm, credit + hạn credit, scope thiếu — và **ma trận template** (có / thiếu / không liệt kê / chưa rõ, cảnh báo "không có dự phòng"), nút **Đồng bộ** theo tài khoản + **Đồng bộ tất cả**, **Quên** template thiếu. Tab Sandbox API của `/infra` cũng có khối "Tài khoản sandbox" chọn theo provider |
| `/templates` | Danh sách + trình sửa YAML (đánh số dòng, kiểm tra qua `/templates/validate`), Lưu, **Dựng thử** |
| `/secrets` | Chỉ tên, agent được dùng, ngày tạo / xoay vòng; thêm, xoay vòng, xoá (không bao giờ hiện giá trị) |
| `/settings` | v2.0 tab **Chung** (tên workspace, gói, vai trò, ngôn ngữ) · **Thành viên** (đổi vai trò, xoá, rời workspace, quy tắc owner cuối; mời nhiều email + vai trò, link hiện một lần, đang chờ / đã nhận / hết hạn, gửi lại, thu hồi) · **Gói & hạn mức** (`/settings/billing`: đã dùng/tối đa, so sánh gói) · **Hệ thống** (chỉ quản trị nền tảng): Ngôn ngữ; **Thông báo** (Telegram bot token + chat ID, SMTP, người nhận, sự kiện, giờ + múi giờ báo cáo, nút **Gửi thử** Telegram / email / báo cáo ngày, lịch sử gửi; secret chỉ ghi); **Lưu trữ snapshot** (R2/B2/S3/MinIO, kiểm tra kết nối); Reaper (giữ N snapshot, xoá sau N ngày); Mạng (chặn egress mặc định); `BASE_DOMAIN`, bảo vệ URL bằng token, ngân sách tháng |
| `/developers` | Discovery (OpenAPI, llms.txt, MCP card, A2A card), schema tool, quick start, SDK/CLI, **A2A JSON-RPC** (message/send, message/stream, tasks/get, tasks/cancel), bảng mã lỗi |
| `/admin` | Hệ thống · Hàng đợi job · Audit log · API keys · Quota & giới hạn · Tính năng / bảo trì (GPU khoá, nhãn P5; bật bảo trì hiện banner toàn cục) · Người dùng |
| `/onboarding` | Token Hetzner → tạo host pool đầu tiên (có tiến trình) → tạo agent → copy cấu hình → chờ “lệnh đầu tiên” → thành công |
| `/login` | v2.0: email + mật khẩu (RATE_LIMITED → "thử lại sau N phút"); "Đăng nhập bằng API key" thu gọn cho tự host |
| `/signup` | v2.0: email, mật khẩu (thanh độ mạnh, ≥ `min_password_len` của Core = 8 ký tự), tên, tên workspace (gợi ý từ email), mã mời khi `signup_mode=invite` (tự điền `?invite=`), đồng ý điều khoản; `closed` → thẻ danh sách chờ; `?workspace_invite=` khi đến từ lời mời |
| `/forgot-password` · `/reset-password?token=` · `/verify-email?token=` | v2.2: quên mật khẩu (`POST /v1/auth/password/forgot`, luôn cùng câu trả lời), đặt mật khẩu mới (≥ 8 ký tự, ≤ 72 byte, nhập lại; Core đăng xuất mọi phiên → về `/login`), xác thực email từ link trong thư (`POST /v1/auth/verify-email`). Link "Quên mật khẩu?" ở `/login` |
| `/invite/[token]` | v2.0: "X mời anh vào workspace Y với vai trò Z" (không cần đăng nhập để xem); đúng email → **Tham gia**; khác email → đổi tài khoản; chưa đăng nhập → Đăng nhập / Đăng ký rồi quay lại |

**⌘K / Ctrl+K** (nút ⌘ trên thanh trên ở mobile): điều hướng, `wake <slug>`, `sleep <slug>`, `kill all` (mở hộp dừng khẩn cấp), `cost hôm nay`. Gõ không dấu cũng được.

**Cập nhật trực tiếp**: `EventSource('/api/boxmes/stream')` cập nhật cache SWR theo `overview.updated`, `box.updated`, `event.created`, `approval.created`. Mất SSE thì SWR tự polling 10 giây (có SSE: 60 giây). Chỉ báo “Trực tiếp / Polling 10s” ở góc sidebar.

## Cấu trúc

```
app/(app)/…            các trang có sidebar (layout kiểm tra phiên)
app/onboarding, login  trang không có sidebar
app/api/boxmes/[...path] proxy → Core (hoặc mock)
app/api/auth/*         đăng nhập / đăng xuất (cookie httpOnly)
app/api/session        cho client biết mock hay không (không trả key)
components/ui          primitive kiểu shadcn (button, card, dialog/sheet, tabs, switch, table…)
components/boxmes        component nghiệp vụ (chip trạng thái, thẻ chỉ số + ngân sách, hộp duyệt, biểu đồ, terminal, YAML editor, code block có Copy, wizard agent…)
lib/types.ts, api.ts   kiểu + client theo contract
lib/mock/              seed + handler + SSE giả lập
```

## Đối chiếu với boxmes/core (đã kiểm tra trực tiếp ngày 2026-10-26)

Đã chạy web (BOXMES_MOCK=0) qua proxy tới bản Core đang chạy thử: đăng nhập bằng key admin và 19 endpoint GET (`health`, `overview`, `boxes`, `agents`, `keys`, `costs`, `costs/suggestions`, `providers`, `hosts`, `offers`, `templates`, `secrets`, `approvals`, `settings`, `admin/*`) đều trả 200. Những chỗ contract v1 mô tả sơ lược, web và mock đã theo shape thật của Core:

- `GET /admin/jobs` = job nền định kỳ `{name, interval_s, last_run_at, last_status, last_error, duration_ms, runs}` (không phải exec Job).
- `GET /admin/quotas` = map số (`max_boxes_per_agent`, `max_cpu_per_box`, `max_ram_mb_per_box`, `max_disk_gb_per_box`, `rate_limit_per_minute`); form tự hiện mọi khoá, có nhãn tiếng Việt cho khoá đã biết.
- `GET /admin/users` = `{id, email, name?, email_verified, platform_admin, workspaces:[{id,name,role}]}` (v2); `GET /admin/system` có `detail` cho từng thành phần, `db`, `auto_burst`.
- `GET /costs/suggestions` = `{kind, title, detail, saving_usd, box_id?, agent_id?}`; `GET /boxes/:id/observe` = `{files_changed, processes:[{pid,cmd}], ports, since}`.
- Timeline đọc event `box.exec` có message dạng `exit=<code> <ms>ms: <lệnh>` (tiền tố `TIMEOUT ` nếu quá giờ).
- Snippet của Core tham chiếu biến môi trường `BOXMES_API_KEY`; wizard hiện thêm khối `export BOXMES_API_KEY=…` với key vừa tạo.
- Tên sự kiện thông báo (v1.2): `daily_report`, `budget`, `approval`, `emergency_stop`, `box_failed`, `host_down`. Cấu hình qua `GET/PUT /notify`, gửi thử `POST /notify/test`.
- v1.2: `GET/PUT /storage` + `POST /storage/test`, `PUT /boxes/:id/egress`, `POST /jobs/:id/cancel`, `GET /providers/:id/regions`; Provider có `implemented`, `chain_rank`, `token_source`; Snapshot có `storage`, `remote_url`, `upload_error`; Box có `egress`.

## Điểm cần Core bổ sung

1. ~~Xác thực WebSocket terminal~~: xong ở v1.10. Tab Terminal lấy ticket một lần (`POST /api/boxmes/boxes/{id}/tickets {purpose:"terminal"}`, proxy gắn key phía server) rồi mở `wss://<core>/v1/boxes/{id}/terminal?ticket=bxa_…&proto=2&mode=control|view[&session=…]`. Core không còn nhận cookie hay `?token=` trên WebSocket. Phiên terminal bền: chọn / gắn lại (replay) / mở mới / đóng, tự kết nối lại bằng ticket mới. Tab **Truy cập**: lệnh `box forward` / `box ssh`, ssh-config, port forward, ticket + thu hồi tất cả; Settings có **SSH keys**.
2. ~~Khôi phục / fork snapshot~~: Core đã có từ v1.1; từ v1.2 snapshot trên R2/B2 khôi phục/fork được ở host bất kỳ.
3. **Token tiết kiệm & log theo lệnh**: event `box.exec` chưa có `tokens_saved` và `log_id`. Web đọc được đuôi tuỳ chọn ` tokens_saved=<n> log=<id>` nếu Core thêm vào message (mock đã làm vậy); không có thì tab Logs hiện nhật ký sự kiện của box và ô mở log theo `log_id`.
4. **box.yaml gốc**: tab Cấu hình dựng lại YAML từ object Box vì contract chưa có endpoint trả YAML gốc.
