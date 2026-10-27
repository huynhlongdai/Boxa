# Boxaz Web

Giao diện quản trị của **Boxaz**, hạ tầng cloud cá nhân cho AI agent. Anh dùng nó để giám sát, duyệt và cứu hộ; còn tạo box, chạy lệnh là việc của agent.

- Next.js 15 (App Router) + TypeScript + Tailwind + các component kiểu shadcn/ui (viết sẵn trong `components/ui`)
- Recharts (biểu đồ), xterm.js (terminal), cmdk (⌘K), SWR (dữ liệu), lucide-react (icon)
- Tiếng Việt toàn bộ, dark mode mặc định, responsive ở 390 / 768 / 1366
- Code bám theo `docs/api-contract-v1.md` (`lib/types.ts`, `lib/api.ts`)

## Chạy nhanh (MOCK MODE, không cần Core)

```bash
cd boxaz/web
pnpm install
BOXAZ_MOCK=1 pnpm dev        # http://localhost:3000
```

Ở MOCK MODE, proxy trả dữ liệu giả lập đúng contract (agent `shopee-crawler`, `kecon-writer`, `replingo-bot`, `n8n-flows`, `hark-main`; box trên Hetzner fsn1/hel1, Vultr sgp, Contabo). Các thao tác ghi (wake/sleep/snapshot, tạo agent, duyệt, dừng khẩn cấp…) sửa trạng thái trong bộ nhớ, khởi động lại server là về như cũ. SSE `/stream` cũng được giả lập (chi phí tăng dần, sự kiện mới).

Khi chạy `pnpm dev` mà **không đặt** `BOXAZ_MOCK` và Core không trả lời ở `BOXAZ_API`, app tự chuyển sang mock.

## Chạy với Boxaz Core thật

```bash
BOXAZ_API=http://localhost:8080/v1 BOXAZ_MOCK=0 pnpm dev
```

Mở `/login`, dán API key có scope `admin`. Key được lưu trong cookie **httpOnly** `boxaz_key`; trình duyệt chỉ gọi `/api/boxaz/*`, route handler `app/api/boxaz/[...path]/route.ts` gắn `Authorization: Bearer <key>` rồi chuyển tiếp tới Core, nên key không bao giờ có trong JavaScript phía client.

## Biến môi trường

| Biến | Mặc định | Ý nghĩa |
|---|---|---|
| `BOXAZ_API` | `http://localhost:8080/v1` | URL Core cho proxy phía server (đọc lúc chạy) |
| `NEXT_PUBLIC_BOXAZ_API` | `http://localhost:8080/v1` | Dùng lúc build: hiển thị trong snippet cấu hình, suy ra URL WebSocket terminal. Proxy cũng dùng nếu thiếu `BOXAZ_API` |
| `NEXT_PUBLIC_BOXAZ_WS` | (suy từ `NEXT_PUBLIC_BOXAZ_API`, `http→ws`) | URL WebSocket riêng cho terminal, vd `wss://boxaz.example.com/v1` |
| `BOXAZ_MOCK` | (trống) | `1` luôn mock · `0` không bao giờ · trống = tự mock khi dev mà Core không chạy |
| `BOXAZ_INSECURE_COOKIE` | (trống) | `1` cho phép cookie không `Secure` khi chạy production qua http nội bộ |

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
docker build -t boxaz-web --build-arg NEXT_PUBLIC_BOXAZ_API=https://boxaz.example.com/v1 .
docker run -p 3000:3000 -e BOXAZ_API=http://boxaz-core:8080/v1 -e BOXAZ_MOCK=0 boxaz-web
# demo không cần Core:
docker run -p 3000:3000 -e BOXAZ_MOCK=1 boxaz-web
```

Ảnh dùng `output: "standalone"` (`node server.js`). Khi chạy standalone ngoài Docker, nhớ copy `.next/static` vào `.next/standalone/.next/static` và `public/` vào `.next/standalone/public` (script `smoke.sh` đã làm việc này).

## Màn hình (routes)

| Route | Nội dung |
|---|---|
| `/` Tổng quan | 4 thẻ chỉ số kèm thanh ngân sách; hộp **Cần anh** (Duyệt / Từ chối); chi phí 7 ngày xếp chồng theo agent; top 5 task tốn nhất hôm nay; nút đỏ **Dừng khẩn cấp** (2 bước, gõ `DUNG`) |
| `/boxes` | Bảng lọc theo trạng thái / agent / provider / lifecycle (lọc lưu trên URL), thẻ trên mobile, nút nhanh wake / sleep / snapshot / xoá; tạo box thủ công (có `Idempotency-Key`) |
| `/boxes/[id]` | Tab Tổng quan (spec, URL, lý do Scheduler chọn, **egress allowlist** — đang thực thi bằng incus ACL/nftables hay chỉ cảnh báo trên driver local, sửa danh sách —, chi phí giờ, ngân sách còn lại, CPU/RAM/đĩa) · Terminal (xterm + WebSocket, **Chiếm quyền / Trả quyền**) · Timeline (lệnh, exit code, thời lượng, token tiết kiệm) · Files (`/work`, `/memory`, xem + tải về) · Logs · Snapshots (khôi phục / fork, nơi lưu: R2/B2/S3 hay tại host, lỗi upload) · Cấu hình (box.yaml chỉ đọc + **Lưu thành template**) |
| `/agents` | Danh sách + **wizard 3 bước**: tên + kind → ngân sách + chính sách (provider, vùng, egress) → API key hiện một lần + đoạn cấu hình sinh sẵn theo kind (Claude Code MCP JSON, OpenAI Agents Python, n8n HTTP node, Hark, cURL) có nút Copy |
| `/agents/[id]` | Keys (tạo / thu hồi), ngân sách, chính sách, dung lượng /memory, lịch sử box |
| `/costs` | Ledger lọc theo agent / task / provider / ngày, xuất CSV (`/costs.csv`), biểu đồ theo provider, pool vs burst, 7 ngày; thẻ gợi ý tiết kiệm |
| `/infra` | Chuỗi dự phòng (pool → Hetzner → Vultr → DigitalOcean → Contabo → netcup), thẻ provider (nhập + kiểm tra token có gợi ý định dạng riêng cho Contabo/netcup, nhãn “Dự phòng #n”, token từ env, danh sách vùng), host với thanh CPU/RAM/đĩa + drain / xoá, thêm host pool, bảng giá sắp theo $/vCPU-giờ với nhãn độ tin cậy, đề xuất mở rộng pool |
| `/templates` | Danh sách + trình sửa YAML (đánh số dòng, kiểm tra qua `/templates/validate`), Lưu, **Dựng thử** |
| `/secrets` | Chỉ tên, agent được dùng, ngày tạo / xoay vòng; thêm, xoay vòng, xoá (không bao giờ hiện giá trị) |
| `/settings` | Ngôn ngữ; **Thông báo** (Telegram bot token + chat ID, SMTP, người nhận, sự kiện, giờ + múi giờ báo cáo, nút **Gửi thử** Telegram / email / báo cáo ngày, lịch sử gửi; secret chỉ ghi); **Lưu trữ snapshot** (R2/B2/S3/MinIO, kiểm tra kết nối); Reaper (giữ N snapshot, xoá sau N ngày); Mạng (chặn egress mặc định); `BASE_DOMAIN`, bảo vệ URL bằng token, ngân sách tháng |
| `/developers` | Discovery (OpenAPI, llms.txt, MCP card, A2A card), schema tool, quick start, SDK/CLI, **A2A JSON-RPC** (message/send, message/stream, tasks/get, tasks/cancel), bảng mã lỗi |
| `/admin` | Hệ thống · Hàng đợi job · Audit log · API keys · Quota & giới hạn · Tính năng / bảo trì (GPU khoá, nhãn P5; bật bảo trì hiện banner toàn cục) · Người dùng |
| `/onboarding` | Token Hetzner → tạo host pool đầu tiên (có tiến trình) → tạo agent → copy cấu hình → chờ “lệnh đầu tiên” → thành công |
| `/login` | Nhập API key admin → cookie httpOnly |

**⌘K / Ctrl+K** (nút ⌘ trên thanh trên ở mobile): điều hướng, `wake <slug>`, `sleep <slug>`, `kill all` (mở hộp dừng khẩn cấp), `cost hôm nay`. Gõ không dấu cũng được.

**Cập nhật trực tiếp**: `EventSource('/api/boxaz/stream')` cập nhật cache SWR theo `overview.updated`, `box.updated`, `event.created`, `approval.created`. Mất SSE thì SWR tự polling 10 giây (có SSE: 60 giây). Chỉ báo “Trực tiếp / Polling 10s” ở góc sidebar.

## Cấu trúc

```
app/(app)/…            các trang có sidebar (layout kiểm tra phiên)
app/onboarding, login  trang không có sidebar
app/api/boxaz/[...path] proxy → Core (hoặc mock)
app/api/auth/*         đăng nhập / đăng xuất (cookie httpOnly)
app/api/session        cho client biết mock hay không (không trả key)
components/ui          primitive kiểu shadcn (button, card, dialog/sheet, tabs, switch, table…)
components/boxaz        component nghiệp vụ (chip trạng thái, thẻ chỉ số + ngân sách, hộp duyệt, biểu đồ, terminal, YAML editor, code block có Copy, wizard agent…)
lib/types.ts, api.ts   kiểu + client theo contract
lib/mock/              seed + handler + SSE giả lập
```

## Đối chiếu với boxaz/core (đã kiểm tra trực tiếp ngày 2026-10-26)

Đã chạy web (BOXAZ_MOCK=0) qua proxy tới bản Core đang chạy thử: đăng nhập bằng key admin và 19 endpoint GET (`health`, `overview`, `boxes`, `agents`, `keys`, `costs`, `costs/suggestions`, `providers`, `hosts`, `offers`, `templates`, `secrets`, `approvals`, `settings`, `admin/*`) đều trả 200. Những chỗ contract v1 mô tả sơ lược, web và mock đã theo shape thật của Core:

- `GET /admin/jobs` = job nền định kỳ `{name, interval_s, last_run_at, last_status, last_error, duration_ms, runs}` (không phải exec Job).
- `GET /admin/quotas` = map số (`max_boxes_per_agent`, `max_cpu_per_box`, `max_ram_mb_per_box`, `max_disk_gb_per_box`, `rate_limit_per_minute`); form tự hiện mọi khoá, có nhãn tiếng Việt cho khoá đã biết.
- `GET /admin/users` = `{id, name, role, admin_keys, note}`; `GET /admin/system` có `detail` cho từng thành phần, `db`, `auto_burst`.
- `GET /costs/suggestions` = `{kind, title, detail, saving_usd, box_id?, agent_id?}`; `GET /boxes/:id/observe` = `{files_changed, processes:[{pid,cmd}], ports, since}`.
- Timeline đọc event `box.exec` có message dạng `exit=<code> <ms>ms: <lệnh>` (tiền tố `TIMEOUT ` nếu quá giờ).
- Snippet của Core tham chiếu biến môi trường `BOXAZ_API_KEY`; wizard hiện thêm khối `export BOXAZ_API_KEY=…` với key vừa tạo.
- Tên sự kiện thông báo (v1.2): `daily_report`, `budget`, `approval`, `emergency_stop`, `box_failed`, `host_down`. Cấu hình qua `GET/PUT /notify`, gửi thử `POST /notify/test`.
- v1.2: `GET/PUT /storage` + `POST /storage/test`, `PUT /boxes/:id/egress`, `POST /jobs/:id/cancel`, `GET /providers/:id/regions`; Provider có `implemented`, `chain_rank`, `token_source`; Snapshot có `storage`, `remote_url`, `upload_error`; Box có `egress`.

## Điểm cần Core bổ sung

1. **Xác thực WebSocket terminal**: Core hiện nhận key qua header hoặc `?token=`. Web không nhúng key vào JS nên **không** dùng `?token=`; nó mở thẳng `NEXT_PUBLIC_BOXAZ_WS` (hoặc suy từ `NEXT_PUBLIC_BOXAZ_API`) và dựa vào cookie httpOnly `boxaz_key` mà trình duyệt tự gửi khi cùng site. Cần thêm vào `bearer()` của Core: đọc cookie `boxaz_key` nếu không có header. Chưa có thì terminal (chế độ thật) báo không kết nối được; MOCK MODE vẫn mô phỏng đầy đủ. Route handler của Next không proxy được WebSocket.
2. ~~Khôi phục / fork snapshot~~: Core đã có từ v1.1; từ v1.2 snapshot trên R2/B2 khôi phục/fork được ở host bất kỳ.
3. **Token tiết kiệm & log theo lệnh**: event `box.exec` chưa có `tokens_saved` và `log_id`. Web đọc được đuôi tuỳ chọn ` tokens_saved=<n> log=<id>` nếu Core thêm vào message (mock đã làm vậy); không có thì tab Logs hiện nhật ký sự kiện của box và ô mở log theo `log_id`.
4. **box.yaml gốc**: tab Cấu hình dựng lại YAML từ object Box vì contract chưa có endpoint trả YAML gốc.
