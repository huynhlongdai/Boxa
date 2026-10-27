# Gateway Boxaz trên domain riêng qua Cloudflare Tunnel

Đưa gateway lên domain thật: API/dashboard ở `https://gw.<domain>`, mỗi box ở `https://<tên-box>.<domain>/` (port chính) và `https://<tên-box>-<nhãn>.<domain>/` (port khác, nhãn chữ như `api`, `db`), host-based, không có tiền tố đường dẫn; địa chỉ cũ `<key>-<port>` vẫn chạy (v1.9). Không cần mở port hay có IP công khai: `cloudflared` gọi ra Cloudflare, Cloudflare lo TLS.

## Yêu cầu

- Zone trên Cloudflare (gói Free được). **Universal SSL miễn phí chỉ phủ wildcard một cấp** (`*.example.com`), vì vậy URL box là một nhãn `<tên-box>[-<nhãn>].example.com` — không dùng `*.box.example.com` (cần Advanced Certificate trả phí).
- API token: *Account → Cloudflare Tunnel: Edit* và *Zone → DNS: Edit* (zone đó). Đặt `CF_API_TOKEN` (bỏ trống nếu có proxy tự chèn header Authorization).
- `cloudflared` (bản đúng kiến trúc máy: amd64 / arm64), `curl`, `jq`.

## 1. Tạo tunnel + ingress + DNS (một lần, chạy lại vô hại)

```bash
CF_ACCOUNT_ID=<account id> CF_ZONE_ID=<zone id> GATEWAY_DOMAIN=example.com \
CORE_URL=http://localhost:8091 TOKEN_FILE=/etc/boxaz/tunnel-token \
CF_API_TOKEN=… core/deploy/cloudflare-tunnel/setup.sh
```

Script (idempotent, dò theo tên):

- tạo tunnel `TUNNEL_NAME` (mặc định `boxaz-gw`) kiểu *remotely-managed* (`config_src=cloudflare`: ingress nằm trong API, máy chạy không cần file config);
- đặt ingress: `gw.<domain>` và `*.<domain>` → `CORE_URL`, còn lại `http_status:404`;
- tạo CNAME proxied `gw.<domain>` và `*.<domain>` → `<tunnel-id>.cfargotunnel.com`. Bản ghi cụ thể (vd. `api.`, `www.`, `mcp.`) luôn được ưu tiên hơn wildcard nên các dịch vụ khác trên zone không bị ảnh hưởng. Script không đụng tunnel/bản ghi khác và từ chối trỏ lại bản ghi đang trỏ nơi khác trừ khi `FORCE=1`;
- ghi token chạy tunnel vào `TOKEN_FILE` (quyền 600).

## 2. Cấu hình core

```bash
BOXAZ_PUBLIC_URL=https://gw.example.com
BOXAZ_GATEWAY_DOMAIN=example.com
BOXAZ_ADDR=127.0.0.1:8091           # khớp CORE_URL ở bước 1
# tuỳ chọn: BOXAZ_PROVIDER_PREVIEW=auto|private|public (mặc định auto)
# tuỳ chọn: BOXAZ_GATEWAY_RESERVED=foo,bar (thêm subdomain dành riêng)
```

Gateway domain cũng đặt được ở dashboard (Cài đặt → Gateway domain, `settings.gateway.domain`); env luôn thắng. `GET /v1/gateway?check=1` cho biết kiểu URL, mẫu URL, subdomain dành riêng, chính sách preview và kết quả tự kiểm tra `BOXAZ_PUBLIC_URL/v1/health`.

Định tuyến theo host ở core:

| Host | Đi đâu |
|---|---|
| `gw.<domain>` (host của `BOXAZ_PUBLIC_URL`) hoặc `<domain>` | API + dashboard của core |
| `<20 hex>-<port>.<domain>` | box tương ứng (gateway: token, wake on request, WebSocket/SSE) |
| subdomain dành riêng (`hark`, `mcp`, `gw`, `www`, `api`, `admin`, `mail`, …) | 404, không bao giờ vào box |
| mọi subdomain khác | 404 |

Key của box là HMAC 20 ký tự hex nên không thể trùng tên dành riêng (tên dành riêng có chữ ngoài a–f và không có đuôi `-<số>`; test kiểm tra điều này).

## 3. Chạy

Một lệnh (core + cloudflared, chạy nền bằng `setsid nohup`, pid/log cạnh file env):

```bash
ENV_FILE=/etc/boxaz/gateway.env core/deploy/cloudflare-tunnel/up.sh     # khởi động / khởi động lại
ENV_FILE=/etc/boxaz/gateway.env core/deploy/cloudflare-tunnel/down.sh   # dừng (tunnel + DNS vẫn giữ)
```

`gateway.env` là file `KEY=VALUE` gồm biến của core ở bước 2 cộng `TOKEN_FILE` (và tuỳ chọn `CLOUDFLARED`, `BOXAZ_CORE_BIN`, key provider). Chỉ chạy cloudflared: `TOKEN_FILE=… run.sh` (foreground) hoặc `DETACH=1 run.sh`. Token được truyền qua biến môi trường `TUNNEL_TOKEN`, không nằm trên dòng lệnh. Với systemd: một unit `ExecStart=/usr/bin/cloudflared tunnel --no-autoupdate run` + `EnvironmentFile` chứa `TUNNEL_TOKEN=…`.

Chuyển gateway sang máy khác: copy `TOKEN_FILE` + file env, chạy `up.sh` ở máy mới, dừng máy cũ. Tunnel và DNS giữ nguyên (nhiều máy cùng chạy một token = nhiều connector, Cloudflare chia tải).

## Preview của provider (lỗ hổng URL preview)

Token URL của Boxaz chỉ bảo vệ URL gateway. Từ v1.8, khi gateway truy cập được từ Internet (`BOXAZ_PROVIDER_PREVIEW=auto`, mặc định) core tạo sandbox **private** ở provider hỗ trợ, để không ai vào thẳng URL preview của provider được:

| Provider | Cách chặn | Kết quả đo live 2026-10-27 |
|---|---|---|
| E2B | `network.allowPublicTraffic=false` lúc tạo (chỉ đặt được khi tạo); gateway gửi `E2B-Traffic-Access-Token` lên upstream. Token lấy từ response create / fork; core khởi động lại thì lấy lại qua `POST /sandboxes/{id}/connect`. | preview trực tiếp → 403 (cả khi box ngủ, không đánh thức box); qua gateway → 200; fork (clone) giữ chế độ private |
| Novita | Như E2B (API tương thích) + bắt buộc `secure: true` (envd có access token). | preview trực tiếp → 403; qua gateway → 200 |
| Prized | Không có host preview: traffic chỉ vào qua ingress agent trong box → core. | không có URL nào để vào thẳng |
| Daytona và adapter YAML khác | Chưa hỗ trợ: preview vẫn công khai; port token có caveat `provider_preview=public`. | — |

Port của box báo `provider_preview: private|public`. `BOXAZ_PROVIDER_PREVIEW=public` tắt cơ chế này (sandbox tạo công khai như trước, URL port là URL preview của provider).
