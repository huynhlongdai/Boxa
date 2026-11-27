# Boxmes core — backend (Go)

Boxmes là hạ tầng cloud cá nhân: cấp **box** (sandbox Linux) cho AI agent trên VPS rẻ của nhiều nhà cung cấp, chọn chỗ rẻ nhất đạt yêu cầu, tự ngủ/dọn dẹp và giữ ngân sách. Thư mục này chứa toàn bộ backend: `boxmes-core` (control plane), `boxd` (host agent), `box` (CLI + MCP stdio). Dashboard nằm ở `../web` và gọi đúng hợp đồng `docs/api-contract-v1.md`.

## 1. Kiến trúc

```
 Agent (Claude Code, OpenAI Agents, n8n, Hark…)
   │  MCP (HTTP /mcp hoặc stdio `box mcp`) · REST /v1 · CLI `box`
   ▼
 ┌──────────────────────── boxmes-core ────────────────────────┐
 │ Gateway: API key + scope, idempotency, rate limit, SSE,   │
 │          WebSocket terminal, MCP, ingress box URL          │
 │ Box Manager (state machine) · Scheduler v1 · Reaper 30s    │
 │ Budget & Ledger (mỗi phút) · Approvals · Price feed 6h     │
 │ Secrets (AES-256-GCM) · Templates/box.yaml · Audit events  │
 │ Store: SQLite (mặc định) | Postgres (DATABASE_URL)         │
 └───────────────▲────────────────────────────┬──────────────┘
                 │ WebSocket dial-OUT (wss)    │ Provider adapters (BPI)
                 │ exec/file/PTY/metrics/http  │ hetzner · vultr · digitalocean · contabo · netcup · local
 ┌───────────────┴──────────┐                 ▼
 │ boxd trên mỗi host       │     battleships.dev (giá + capability mở)
 │ driver incus | local     │
 │ output shaper, logs, PTY │
 └──────────────────────────┘
```

Điểm chính theo spec v0.3:

- **boxd gọi ra (dial-out)** tới core qua WebSocket (`/v1/hosts/connect`), host không cần mở cổng. Một kết nối multiplex RPC (exec, file, grep, observe, snapshot, PTY, HTTP ingress) + metrics mỗi 10 giây.
- **Bootstrap token**: host mới (cloud-init) nhận một token dùng một lần. Lần kết nối đầu, core đổi nó lấy host token riêng (lưu hash) và boxd ghi vào `host.json`. Riêng máy tự đăng ký (install.sh/dev) thì dùng `BOXMES_BOOTSTRAP_TOKEN` chung.
- **Provider chỉ lo vòng đời host** (create/destroy/status, sleep/wake/snapshot nếu có). Mọi thao tác trong box đều đi qua boxd, nên hành vi ở mọi provider giống nhau.

### Vòng đời box

`requested → placing → provisioning → running ⇄ sleeping → (snapshotting) → terminated`; lỗi thì `failed` sau tối đa 2 lần thử lại (mỗi lần thử loại host vừa lỗi). Mỗi chuyển trạng thái đều ghi event, phát SSE `box.updated` và lưu `placement_reason`.

- `task`: hết TTL thì terminate.
- `session`: rảnh quá `idle_minutes` thì sleep; gọi exec/file/terminal sẽ tự wake.
- `service`: chạy liên tục. Khi vượt ngân sách thì tạo mục "Cần anh" (`service_stop`) chứ không tự dừng.

### Scheduler v1 (M4)

- Ứng viên gồm host pool đang kết nối, còn RAM/CPU (CPU overcommit 4x, dùng được 90% RAM, disk thin 2x), và các offer từ price feed hoặc API provider đã kết nối.
- Host pool có chi phí biên ≈ 0 khi tải sau khi đặt box dưới 80%; vượt 80% thì bị phạt theo mức quá tải.
- Burst: `price_hour × max(giờ_dự_kiến, min_billing) + setup`, có áp trần giá tháng. Giờ dự kiến: task = TTL, session = 4h, service = 730h.
- Chọn chi phí thấp nhất; nếu hoà thì ưu tiên pool. Lý do chọn và các phương án thay thế được ghi vào `placement_reason`.
- **Không tự thuê máy** trừ khi đặt `BOXMES_AUTO_BURST=true`. Mặc định, khi hết chỗ core trả lỗi `NO_CAPACITY` và tạo mục duyệt `pool_expand` kèm offer rẻ nhất.
- Chi phí ghi vào ledger: box pool = giá giờ của host × cpu_box/cpu_host; box burst = giá host chia đều cho số box trên host đó.

### Reaper (30 giây) & ngân sách

- Reaper:
  - session rảnh → sleep;
  - task quá TTL → terminate;
  - box mồ côi (agent không gọi > 24h, trừ service) → snapshot rồi terminate;
  - host burst trống > 10 phút → destroy tại provider;
  - host mất tunnel > 2 phút → `down` và tạo mục duyệt `host_down`.
- Ledger: mỗi phút ghi một dòng `compute` cho từng box tính tiền. Box pool chỉ tính khi đang chạy; box burst tính cả khi ngủ.
- Ngưỡng 80% ngân sách ngày/tháng (theo agent và toàn hệ thống) → tạo mục duyệt `budget`.
- Ngưỡng 100%: session → sleep; task → snapshot rồi sleep; service → tạo mục duyệt. Box mới và lệnh wake bị chặn với lỗi `BUDGET_EXCEEDED`. Duyệt mục `budget` = cho phép vượt trong kỳ đó.
- Overview tính: chi hôm nay/tháng, số box chạy/ngủ, `saved_vs_247_usd`, chi phí 7 ngày theo agent, top 5 box tốn nhất. Có thêm `GET /costs.csv` và gợi ý tiết kiệm `GET /costs/suggestions`.

### Output shaper (M7)

`exec` chỉ trả `stdout_tail`/`stderr_tail` (mặc định 60 dòng, tối đa 16KB mỗi luồng), cờ `truncated`, `files_changed` (file trong /work, /memory đổi trong lúc chạy) và `tokens_saved` ≈ (byte bị cắt)/4. Output lớn hơn 8KB hoặc bị cắt thì log đầy đủ được lưu trên host và trả `log_id` (đọc qua `GET /v1/logs/:id`). Lệnh dài dùng `async: true` → `job_id`, rồi gọi `GET /v1/jobs/:id?wait=30`.

## 2. Cấu trúc mã

```
cmd/boxmes-core   server (env → manager + api)
cmd/boxd        host agent (incus | local)
cmd/box         CLI + `box mcp` (stdio)
internal/api        REST/SSE/WS terminal/MCP HTTP/ingress (chi)
internal/manager    box lifecycle, scheduler, reaper, ledger/budget, approvals, providers, secrets, templates
internal/store      Store interface + SQL dùng chung SQLite/Postgres, migrations embed
internal/hostlink   tunnel core⇄boxd (Peer RPC + Hub)
internal/boxd       agent, driver incus/local, output shaper, metrics
internal/provider   BPI + hetzner, vultr, local, stub; cloud-init template
internal/pricefeed  battleships.dev (index + providers/<id>.json)
internal/mcp        MCP server (JSON-RPC 2.0): 18 tool (tools.go = nguồn duy nhất cho /v1/tools), resources, prompts; dùng chung HTTP và stdio
internal/i18n       catalog vi/en/zh/ja (embed JSON) cho lỗi, AGENTS.md, snippet, llms.txt, prompt
internal/agentkit   22 loại agent (GET /agent-kinds) + bộ sinh snippet
internal/boxyaml    parse/validate box.yaml (lỗi có số dòng), template dựng sẵn
deploy/             install.sh, systemd, Caddyfile, cloud-init, docker-compose
scripts/e2e.sh      e2e với binary thật
```

## 3. Chạy local (không cần Docker/Incus/Postgres)

Yêu cầu Go 1.27+ (module khai `go 1.27.3`).

```bash
go build -o bin/ ./cmd/...

# Terminal 1 — core (SQLite ở ./data, admin key cố định cho dev)
BOXMES_DATA_DIR=./data BOXMES_ADMIN_KEY=bx_admin_dev BOXMES_BOOTSTRAP_TOKEN=dev-boot ./bin/boxmes-core

# Terminal 2 — boxd với driver local (mỗi box là 1 thư mục, KHÔNG cô lập — chỉ để dev)
BOXD_CORE_URL=http://127.0.0.1:8080 BOXD_TOKEN=dev-boot BOXD_DRIVER=local BOXD_STATE_DIR=./boxd-state ./bin/boxd

# Terminal 3 — tạo agent + key rồi dùng CLI
export BOXMES_URL=http://127.0.0.1:8080
curl -s -XPOST $BOXMES_URL/v1/agents -H "Authorization: Bearer bx_admin_dev" -d '{"name":"coder","kind":"claude-code","budget_day_usd":2}'
export BOXMES_API_KEY=$(curl -s -XPOST $BOXMES_URL/v1/keys -H "Authorization: Bearer bx_admin_dev" -d '{"agent_id":"coder"}' | jq -r .key)
./bin/box up --template python --lifecycle session --slug demo
./bin/box exec demo -- 'echo hi && ls'
echo "print(1)" | ./bin/box write demo /work/a.py
./bin/box read demo /work/a.py
./bin/box exec demo --async -- 'sleep 5; echo xong'      # → job_id
./bin/box sleep demo && ./bin/box wake demo && ./bin/box snapshot demo
./bin/box cost && ./bin/box down demo
```

Nếu không đặt `BOXMES_ADMIN_KEY`, lần chạy đầu core tự sinh admin key và **in ra stdout đúng một lần**.

Kết nối agent qua MCP:

```bash
# Claude Code — HTTP
claude mcp add --transport http boxmes http://127.0.0.1:8080/mcp --header "Authorization: Bearer $BOXMES_API_KEY"
# hoặc stdio
BOXMES_URL=http://127.0.0.1:8080 BOXMES_API_KEY=... claude mcp add boxmes -- box mcp
```

`GET /v1/agents/:id/snippet?kind=<kind>&lang=<vi|en|zh|ja>` sinh sẵn đoạn cấu hình cho 22 loại agent (xem `GET /v1/agent-kinds` và mục 8).

### Kiểm thử

```bash
go vet ./... && go test ./...      # unit + e2e in-process (core + boxd local + WS terminal + MCP + ingress)
./scripts/e2e.sh                   # e2e với binary thật + CLI (cần curl, jq, python3)
make dist                          # binary tĩnh linux amd64/arm64 vào dist/
```

## 4. Deploy lên 1 host Hetzner (core + boxd + Incus cùng máy)

1. Tạo server Hetzner Cloud chạy Ubuntu 24.04 (CX22/CPX21 là đủ khởi đầu). Firewall chỉ mở 22, 80, 443.
2. Trên máy dev: `make dist`, rồi `scp -r dist deploy root@<IP>:/root/boxmes/`.
3. Trên server: `cd /root/boxmes && HOST_PRICE_MONTH=4.59 ACME_EMAIL=anh@email bash deploy/install.sh`. Nếu đã có domain thì thêm `BASE_DOMAIN=example.com`.
   - Script cài `incus` (`incus admin init --minimal`), `caddy`, copy binary, sinh `BOXMES_MASTER_KEY`, admin key và bootstrap token vào `/etc/boxmes/core.env` và `/etc/boxd.env` (quyền 600), rồi bật `boxmes-core`, `boxd`, `caddy`.
   - Core nghe ở `127.0.0.1:8080`. Caddy cấp HTTPS cho `core-<ip>.sslip.io` (hoặc `core.<BASE_DOMAIN>`) và cấp cert theo yêu cầu cho URL box (`<slug>-<ip>.sslip.io`, hoặc `<slug>.box.<BASE_DOMAIN>`). Mỗi lần cấp cert, Caddy hỏi core qua `/v1/internal/tls-ask`.
   - boxd trên cùng máy nối tới `http://127.0.0.1:8080` và tự đăng ký thành host pool, giá lấy từ `HOST_PRICE_MONTH`.
4. Mở dashboard hoặc API bằng admin key được in ra. Vào **Hạ tầng → Provider → Hetzner**, dán API token (Read & Write); core kiểm tra token rồi mã hoá AES-GCM khi lưu.
5. Thêm host khi cần: `POST /v1/hosts {"provider":"hetzner","sku":"cx22","region":"fsn1"}`. Core tạo server với cloud-init (`deploy/cloud-init.yaml.tmpl`): cài incus, tải `boxd` từ `https://<core>/dl/boxd-linux-<arch>`, rồi dial-out về core bằng bootstrap token dùng một lần. Lệnh này **tốn tiền thật**.

Backup: thư mục `/var/lib/boxmes` (SQLite + master key). Nếu dùng Postgres, đặt `DATABASE_URL` và backup DB hằng ngày.

## 5. Biến môi trường

### boxmes-core

| Biến | Mặc định | Ý nghĩa |
|---|---|---|
| `BOXMES_ADDR` | `:8080` | địa chỉ lắng nghe (production: `127.0.0.1:8080` sau Caddy) |
| `BOXMES_DATA_DIR` | `./data` | SQLite + `master.key` |
| `DATABASE_URL` | `<data>/boxmes.db` | `postgres://…` để dùng Postgres (pgx) |
| `BOXMES_MASTER_KEY` | tự sinh vào `master.key` | khoá mã hoá secrets và token provider |
| `BOXMES_ADMIN_KEY` | — | admin key cố định; không đặt thì tự sinh và in một lần |
| `BOXMES_ADMIN_KEY_FILE` | — | ghi admin key tự sinh ra file này |
| `BOXMES_GATEWAY_DOMAIN` | — | v1.8: URL box host-based `https://<key>-<port>.<domain>/` (cần DNS wildcard + TLS trỏ về core, xem `deploy/cloudflare-tunnel`); thắng `settings.gateway.domain` |
| `BOXMES_PROVIDER_PREVIEW` | `auto` | v1.8: `auto` (private khi gateway truy cập được từ Internet) / `private` / `public` — sandbox E2B/Novita có từ chối truy cập thẳng URL preview hay không |
| `BOXMES_GATEWAY_RESERVED` | — | v1.8: thêm subdomain dành riêng (phân cách bằng dấu phẩy), không bao giờ trỏ vào box |
| `BOXMES_PUBLIC_URL` | — | URL https của core để boxd/cloud-init gọi về (bắt buộc khi tạo host qua provider) |
| `BASE_DOMAIN` | `sslip.io` | domain cho URL box |
| `BOXMES_BOOTSTRAP_TOKEN` | — | token chung cho boxd tự đăng ký (install.sh/dev) |
| `BOXMES_AUTO_BURST` | `false` | cho scheduler tự thuê host theo giờ |
| `BOXMES_LOCAL_PRICE_MONTH` | `0` | giá/tháng của host tự đăng ký (phân bổ chi phí) |
| `BOXMES_LEDGER_INTERVAL` | `1m` | chu kỳ ghi ledger |
| `BOXMES_REAPER_INTERVAL` | `30s` | chu kỳ reaper |
| `BOXMES_PRICE_FEED` | on | `off` để tắt |
| `BOXMES_PRICE_FEED_URL` | `https://battleships.dev` | nguồn giá |
| `BOXMES_PRICE_FEED_PROVIDERS` | `hetzner-cloud,vultr,contabo,netcup,digitalocean` | allowlist |
| `BOXMES_PRICE_FEED_INTERVAL` | `6h` | chu kỳ cập nhật giá |
| `BOXMES_DIST_DIR` | — | thư mục phục vụ `/dl/boxd-linux-<arch>` |
| `BOXMES_DISABLE_TUNNEL` | `false` | `1` = tắt bước cuối của chuỗi URL công khai (cloudflared quick tunnel trong sandbox); tương đương `settings.disable_tunnel` |
| `BOXMES_INGRESS_ASSUME_PUBLIC` | `false` | `1` = coi `BOXMES_PUBLIC_URL` là truy cập được từ Internet dù tên/IP trông như nội bộ (vd. sau NAT có port-forward) |
| `BOXMES_BOXD_URL` | `<public>/dl/boxd` | URL tải boxd cho cloud-init |
| `BOXMES_SETUP_COST_USD` | `0.002` | chi phí khởi tạo cộng vào báo giá burst |
| `BOXMES_ORPHAN_AFTER` | `24h` | ngưỡng box mồ côi |
| `BOXMES_BURST_EMPTY_AFTER` | `10m` | host burst trống thì huỷ |
| `BOXMES_HOST_DOWN_AFTER` | `2m` | mất tunnel thì đánh dấu down |
| `BOXMES_CREATE_WAIT` | `120s` | `POST /boxes` chờ tối đa (dùng `?wait=false` để trả ngay) |
| `BOXMES_CORS_ORIGINS` | `*` | CORS cho dashboard |
| `HETZNER_TOKEN` · `VULTR_API_KEY` · `DIGITALOCEAN_TOKEN` · `NETCUP_REFRESH_TOKEN` | — | v1.2: kết nối provider lúc khởi động (token mã hoá vào DB, `token_source=env`) |
| `CONTABO_CLIENT_ID` · `CONTABO_CLIENT_SECRET` · `CONTABO_API_USER` · `CONTABO_API_PASSWORD` | — | v1.2: thông tin OAuth Contabo (đủ 4 biến mới kết nối) |
| `BOXMES_PROVIDER_CHAIN` | `hetzner,vultr,digitalocean,contabo,netcup` | v1.2: thứ tự dự phòng burst của scheduler |
| `BOXMES_S3_PROVIDER` · `BOXMES_S3_ENDPOINT` · `BOXMES_S3_REGION` · `BOXMES_S3_BUCKET` · `BOXMES_S3_PREFIX` · `BOXMES_S3_ACCESS_KEY_ID` · `BOXMES_S3_SECRET_ACCESS_KEY` | — (no prefix; keys under `snapshots/`) | v1.2: lưu snapshot lên R2/B2/S3; đặt bằng env thì dashboard chỉ xem được |

### boxd

| Biến | Mặc định | Ý nghĩa |
|---|---|---|
| `BOXD_CORE_URL` | `http://localhost:8080` | URL core (http(s) hoặc ws(s)) |
| `BOXD_TOKEN` | — | bootstrap token (dùng một lần, hoặc token chung) |
| `BOXD_DRIVER` | `incus` nếu có, không thì `local` | `incus` hoặc `local` |
| `BOXD_STATE_DIR` | `./boxd-state` | `host.json` (host token), logs |
| `BOXD_ROOT` | `<state>/root` | thư mục box của driver local |
| `BOXD_PROVIDER` / `BOXD_REGION` / `BOXD_IP` | `local` / — / — | khai báo host; IP dùng cho URL sslip.io |
| `BOXD_MEMORY_ROOT` | `/var/lib/boxd/memory` | volume `/memory` theo agent (incus) |
| `BOXD_SNAPSHOT_EXPORT_DIR` | — | nếu đặt thì snapshot incus được export thêm ra tar.gz |
| `BOXD_LOCAL_ULIMIT_MEM` | — | `1` để áp `ulimit -v` theo RAM (driver local) |
| `BOXD_EGRESS_MODE` | `acl` | v1.2 (incus): `acl` = Incus network ACL trên NIC của box, `nft` = bảng nftables `inet boxmes_<box>` trên host |

### box (CLI)

`BOXMES_URL` (mặc định `http://localhost:8080`), `BOXMES_API_KEY`.

## 6. Bảo mật

- API key lưu dạng SHA-256. Key admin có scope `admin`; key agent có `box:create, box:exec, box:file, snapshot, secret:use` và chỉ thấy box của agent mình.
- Secrets và token provider được mã hoá AES-256-GCM bằng `BOXMES_MASTER_KEY`. API chỉ trả tên secret. Giá trị được tiêm làm biến môi trường **ở mỗi lần exec**, không ghi vào cấu hình container.
- Container Incus chạy không đặc quyền (`security.privileged=false`), có giới hạn `limits.cpu`/`limits.memory`/`root size`/`limits.processes`.
- URL box mặc định được bảo vệ bằng token (`?boxmes_token=` → cookie).
- `POST /emergency-stop {"confirm":"DUNG"}` cho mọi box đang chạy đi ngủ.

## 7. Trạng thái (v0.1.0)

**Đã làm và đã chạy test:** REST theo hợp đồng v1, SSE, WebSocket terminal (PTY), MCP HTTP + stdio (12 tool), API key/scope/idempotency/rate limit, state machine + retry, scheduler v1, reaper, ledger/budget/approvals/overview/CSV/gợi ý, price feed battleships (đã chạy với dữ liệu thật: 727 offer, 5 provider), secrets, templates + box.yaml, settings/quotas/admin/audit, boxd dial-out + driver local, ingress box URL qua tunnel, kiểm tra token Hetzner/Vultr (gọi API thật, token sai → 401).

**Đã viết nhưng chưa kiểm chứng được ở sandbox:**
- driver `incus` (đã test argv với runner giả);
- tạo/xoá server Hetzner/Vultr (đã test với server HTTP giả);
- cloud-init, install.sh, Caddy on-demand TLS;
- Postgres (SQL portable, chưa chạy với Postgres thật).

**Stub / chưa làm:**
- headscale mesh, GPU;
- `memory_mb` của agent chưa đo;
- ingress chỉ hỗ trợ HTTP có buffer, không hỗ trợ WebSocket/streaming.

## 8. v1.1 — hỗ trợ agent tối đa & đa ngôn ngữ

Theo `docs/api-contract-v1.md` mục "v1.1 additions".

**Đa ngôn ngữ (vi, en, zh, ja).** Mọi request có thể gửi `Accept-Language` (hoặc `?lang=`); mặc định API là `en`. `code` lỗi không bao giờ đổi, `message` + `hint` được dịch. Catalog nằm ở `internal/i18n/catalog/<locale>.json` (embed): `msg`/`hint` dùng chính chuỗi format tiếng Anh làm khoá (gettext), nên code vẫn viết `model.E(code, "box %s offline", id)`; `text` là các chuỗi có khoá (snippet, llms.txt, prompt MCP, AGENTS.md). Test `TestCatalogComplete` quét toàn bộ mã nguồn và fail nếu có thông báo lỗi chưa dịch hoặc sai verb `%`. Settings có `locale` (mặc định `vi`, dùng làm ngôn ngữ dự phòng cho AGENTS.md); agent có `locale` riêng (`PATCH /agents/:id {"locale":"ja"}`). CLI/stdio MCP đọc `BOXMES_LANG` (hoặc `LANG`).

**Lỗi cho agent.** Envelope thêm `retry_after_s` (NO_CAPACITY 60, PROVIDER_FAILED 30, RATE_LIMITED theo bucket, INTERNAL 5; kèm header `Retry-After`) và `docs_url` → `GET /v1/errors#CODE` (catalog lỗi đã dịch, không cần auth).

**Khám phá (không cần auth).** `GET /v1/openapi.json` (OpenAPI 3.1; schema sinh bằng reflection từ struct Go, test `TestOpenAPICoversRoutes` đi qua mọi route chi), `GET /llms.txt`, `GET /llms-full.txt` (định dạng llmstxt.org, phần giới thiệu dịch theo ngôn ngữ), `GET /.well-known/mcp.json` (server card), `GET /.well-known/agent.json` (A2A agent card 0.3), `GET /v1/tools?format=openai|anthropic|gemini|mcp`, `GET /v1/agent-kinds`.

**Endpoint mới.**
- `POST /v1/quote` — chạy thử placement + giá cho một spec (cùng body với tạo box hoặc `{yaml}`), không tạo gì, không tạo approval: `{available, provider, region, cost_per_hour, expected_hours, expected_total_usd, placement_reason, alternatives[], within_budget}`.
- `POST /v1/boxes/:id/exec-batch` `{commands:[string|{command,cwd,timeout_s}], stop_on_error (mặc định true), cwd, timeout_s, tail_lines}` → `{ok, results[], completed, stopped_at, tokens_saved}` (tối đa 50 lệnh).
- `GET /v1/jobs/:id?wait=30` — long-poll (tối đa 60s).
- `POST /v1/boxes/:id/restore` `{snapshot_id?}` — đưa /work + /memory về snapshot (mặc định bản mới nhất; snapshot phải nằm trên cùng host). `POST /v1/boxes/:id/fork` `{snapshot_id?, slug?}` → box mới từ snapshot (nếu box chưa có snapshot thì tự chụp). Driver local: untar; incus: `incus snapshot restore` / `incus copy src/snap dst` (dự phòng `incus import` từ tarball export).
- `GET /v1/boxes/:id/yaml` → `{yaml, source: request|generated}` (lưu yaml gốc lúc tạo; box tạo bằng JSON thì sinh yaml tương đương).
- Phản hồi `box_up`/`exec`/`fork`/`quote`/`exec-batch` có `next: ["box_exec(box_id=…, command=…)", …]`. Event `box.exec` có `data: {exit_code, duration_ms, tokens_saved, log_id, truncated}` (migration `002_event_data.sql`).

**MCP.** 18 tool: thêm `box_quote`, `box_exec_batch`, `box_fork`, `box_restore`, `box_logs` (theo log_id có tail/grep, hoặc liệt kê exec gần đây của box), `box_templates`; tool có annotations (readOnly/destructive/idempotent). Resources: `boxmes://box/{id}`, `boxmes://box/{id}/file/{path}`, `boxmes://box/{id}/logs`, `boxmes://log/{log_id}` (+ `resources/templates/list`). Prompts: `debug-failing-command`, `start-project`, `cost-review` (dịch theo Accept-Language).

**AGENTS.md** trong mỗi box (`/AGENTS.md`) được viết theo ngôn ngữ của agent: danh tính, ngân sách, quy tắc vòng đời, /work, /memory, danh sách tool, cách gọi Boxmes (MCP/REST/discovery), mẹo tiết kiệm token. Template: `internal/i18n/agentsmd/<locale>.md.tmpl`.

**22 loại agent + snippet.** claude-code, codex-cli, gemini-cli, cursor, windsurf, cline (MCP config đúng định dạng từng công cụ), openai-agents, langchain, langgraph, crewai, autogen, pydantic-ai, llamaindex (Python), vercel-ai-sdk, mastra (TypeScript), n8n, dify, flowise, make, zapier (JSON node/HTTP), hark, custom (curl). Snippet đọc key từ `BOXMES_API_KEY`; phản hồi thêm `file` (đường dẫn file cấu hình) và `notes` (đã dịch, ghi rõ chỗ định dạng có thể khác theo phiên bản).

**CLI mới:** `box quote …`, `box batch <box> -- cmd1 ;; cmd2`, `box restore <box> [snap]`, `box fork <box> [snap] [--slug]`, `box yaml <box>`.

**SDK:** `../sdk/python` (gói `boxmes`) và `../sdk/typescript` (`@boxmes/sdk`) — xem README từng gói.

## 9. v1.2 — thêm provider, snapshot lên R2/B2, thông báo, egress, A2A

Theo `docs/api-contract-v1.md` mục "v1.2 additions". Mọi thứ dưới đây đã có test với server giả (httptest) — **chưa gọi API thật** của DigitalOcean/Contabo/netcup/R2/B2/Telegram/SMTP.

**Provider mới (`internal/provider`).** Cùng interface với Hetzner/Vultr: `Create/Delete/Status/ValidateToken` + `Regions` + `List` (server do Boxmes tạo, theo tag/nickname), giá lấy từ price feed.
- **DigitalOcean**: `/v2/droplets` (tag trung tính `managed`, `mgr-host-<id>`), cloud-init qua `user_data`, size `s-*`, region `sgp1/fra1/…`.
- **Contabo**: OAuth2 password grant (token cache tới khi hết hạn), `POST /v1/compute/instances` với `x-request-id`, tìm image Ubuntu theo tên; token có dạng `client_id:client_secret:api_user:api_password` hoặc JSON. Lưu ý: Contabo tính tiền theo tháng nên chỉ hợp làm pool.
- **netcup (SCP REST)**: không có API đặt mua server — Boxmes *nhận* server dự phòng đã đặt sẵn có nickname `mgr-spare-*` (tên cũ `boxa-spare-*` vẫn được nhận), cài lại image kèm cloud-init và đổi nickname thành `mgr-<host>`; xoá = trả về `mgr-spare-*` (không huỷ hợp đồng). Token = refresh token SCP (đổi lấy access token qua Keycloak).
- **Chuỗi dự phòng**: pool hiện có → burst theo `BOXMES_PROVIDER_CHAIN` (mặc định Hetzner → Vultr → DigitalOcean → Contabo → netcup), chỉ dùng provider đã kết nối và nằm trong policy của agent. Tạo host lỗi thì loại provider đó (`provider:<id>`) và thử provider kế tiếp. `GET /v1/providers` trả thêm `implemented`, `chain_rank`, `token_source`; `GET /v1/providers/:id/regions`, `GET /v1/providers/:id/servers`.

**Snapshot lên object storage (`internal/objstore`, `manager/storage.go`, `boxd/objsync.go`).** Client S3 tự viết (SigV4 + presigned URL, không thêm dependency) cho R2/B2/S3/MinIO. Core ký URL `PUT` lúc snapshot và `GET` lúc restore/fork rồi gửi cho boxd; boxd stream tar.gz thẳng lên/xuống bucket, **secret không bao giờ rời core**. Snapshot có `storage: local|s3`, `remote_url`, `upload_error`. Có bản trên bucket thì restore/fork chạy được ở **host bất kỳ** (trước đây phải cùng host). Retention: `reaper.keep_snapshots` (theo box) + `reaper.snapshot_retention_days`; reaper xoá cả object trên bucket. Không cấu hình (hoặc upload lỗi) → giữ hành vi cũ, snapshot nằm tại host. Cấu hình: env `BOXMES_S3_*` hoặc `PUT /v1/storage {provider, endpoint, region, bucket, prefix, access_key_id, secret_access_key}` (secret lưu trong kho secret mã hoá với tên dành riêng `BOXMES_SYS_*`, ẩn khỏi `GET /secrets`); `POST /v1/storage/test` ghi/đọc/xoá một object thử. Test chỉ dùng S3 giả chạy trong process (`objstore.NewFake`, có kiểm chữ ký).

**Thông báo (`internal/notify`, `manager/notify.go`).** Telegram `sendMessage` (HTML) và email SMTP (STARTTLS / TLS / none, PLAIN auth). Sự kiện: `daily_report` (giờ + múi giờ trong settings), `budget` (80% và 100% ngân sách ngày của agent, tổng tháng), `approval` (mục "Cần anh" mới), `emergency_stop`, `box_failed`, `host_down`. Nội dung dịch theo `settings.locale`. Bot token và mật khẩu SMTP lưu trong kho secret mã hoá (`BOXMES_SYS_TELEGRAM_BOT_TOKEN`, `BOXMES_SYS_SMTP_PASSWORD`), API chỉ trả `has_*`. `GET/PUT /v1/notify`, `POST /v1/notify/test {channel: telegram|email|all, kind: test|daily_report}`; 30 lần gửi gần nhất nằm trong `recent`.

**Egress allowlist (`internal/egress`, `boxd/egress.go`).** Khai báo theo box: `egress: {allow: [...], deny_by_default}` (JSON, box.yaml dạng map hoặc list, tham số `egress` của tool MCP `box_up`), sửa sau bằng `PUT /v1/boxes/:id/egress`. Mục hợp lệ: tên miền, `*.wildcard`, IPv4/IPv6, CIDR, kèm `:port` tuỳ chọn. Thứ tự áp dụng: box → `policy.egress_allowlist` của agent (là trần: box không được xin ngoài danh sách agent → `POLICY_DENIED`) → `settings.egress_deny_by_default`. boxd (incus) phân giải tên miền thành IP và áp **Incus network ACL** (mặc định) hoặc bảng **nftables** riêng cho box, luôn cho phép DNS tới resolver; rule được sinh thuần (có unit test). Driver local **không thực thi** — box trả `egress.enforced=false` kèm `warning` rõ ràng và chỉ xuất `BOXMES_EGRESS_ALLOW` cho tool tự tuân thủ. Box hiển thị `egress {allow, deny_by_default, source, enforced, mode, rules, warning, applied_at}`.

**A2A JSON-RPC (`internal/api/a2a.go`).** `POST /a2a` (cùng Bearer key): `message/send` (= `tasks/send`), `message/stream` (= `tasks/sendSubscribe`, SSE), `tasks/get`, `tasks/cancel`, `tasks/resubscribe`. Text part = lệnh + `metadata.box_id` (hoặc data part `{box_id, command, cwd?, timeout_s?}`) → job exec async, **task id = job id**; data part `{tool, arguments}` gọi bất kỳ tool nào trong 18 tool (chạy đồng bộ). Trạng thái job → A2A: `queued→submitted`, `running→working`, `done→completed`, `failed→failed`, `canceled→canceled`. `tasks/cancel` = `POST /v1/jobs/:id/cancel` (kill lệnh trên host). Agent card (`/.well-known/agent.json`) giờ có `url: <base>/a2a`, `preferredTransport: JSONRPC`, `capabilities.streaming: true`, skill `exec` + 18 tool.

**Kiểm thử v1.2.** `go test ./...` (thêm `internal/{objstore,notify,egress}`, `provider/more_providers_test.go`, `boxd/egress_test.go`, `e2e/v12_test.go`: snapshot qua S3 giả giữa 2 host + retention, egress, Telegram giả, A2A send/get/cancel/stream, chuỗi provider) và `scripts/e2e.sh` (thêm mục v1.2).



## 10. v1.4 — provider Sandbox API (T2 / T3)

Ngoài VPS/VM thô (T1), Boxmes thuê được sandbox của các dịch vụ "Sandbox API". Mỗi box chạy trong **một sandbox riêng**, tính tiền **theo giây**, và sandbox bị xoá khi box kết thúc. Hợp với task ngắn, code không tin cậy, hoặc việc cần fork nhanh.

**Hai cách chạy (`sandbox_mode`, theo provider hoặc `BOXMES_SANDBOX_MODE`).**
- **T2 – dial-in:** core chạy `boxd --dial` trong sandbox bằng bootstrap token dùng một lần, boxd gọi ngược về core qua `BOXMES_PUBLIC_URL` và đăng ký như một host bình thường (đủ terminal, snapshot, egress). Cần sandbox giữ được tiến trình nền và core có URL công khai.
- **T3 – shim:** core trả lời các RPC của boxd bằng exec/file API của provider (`internal/sandbox/shim.go`). Không cần URL công khai, nhưng không có terminal hay luật egress. Snapshot T3 là tarball `/work`+`/memory` do core giữ (`internal/sandbox/shimsnap.go`), xem mục 11.
- `auto` (mặc định) chọn T2 khi provider là T2, giữ được tiến trình nền và `BOXMES_PUBLIC_URL` đã đặt, ngược lại thì T3.

**Adapter.** Có hai loại adapter, đều nằm trong `internal/sandbox`. Loại *native* viết bằng Go. Loại *YAML* là adapter khai báo: route HTTP, cách xác thực, đường dẫn JSON, ánh xạ trạng thái. Adapter YAML dựng sẵn nằm trong `builtin/*.yaml`; muốn thêm thì bỏ file vào `BOXMES_SANDBOX_ADAPTERS_DIR`. Trước khi nạp, kiểm tra file bằng `POST /v1/sandbox/adapters/lint` (admin key) hoặc màn hình **Hạ tầng → Sandbox API → Adapter YAML tuỳ chỉnh**.

### Bảng provider

**T1 – VPS / VM** (boxd + Incus, một host chạy nhiều box, tính theo giờ hoặc tháng):

| Provider | Kết nối | Mức xác minh | Ghi chú |
|---|---|---|---|
| Hetzner Cloud (`hetzner`) | `HETZNER_TOKEN` | **live**: kiểm tra token gọi API thật (token sai → 401) | giá live qua API + battleships.dev |
| Vultr (`vultr`) | `VULTR_API_KEY` | **live**: kiểm tra token gọi API thật | giá live qua API + battleships.dev |
| DigitalOcean (`digitalocean`) | `DIGITALOCEAN_TOKEN` | docs (test bằng server giả) | |
| Contabo (`contabo`) | `CONTABO_CLIENT_ID` · `CONTABO_CLIENT_SECRET` · `CONTABO_API_USER` · `CONTABO_API_PASSWORD` | docs (test bằng server giả) | trả theo tháng |
| netcup (`netcup`) | `NETCUP_REFRESH_TOKEN` | docs (test bằng server giả) | trả theo tháng |
| Máy cục bộ (`local`) | boxd + bootstrap token | — | tự đăng ký, không cần token |

**T2 / T3 – Sandbox API** (một sandbox cho mỗi box, tính theo giây; giá "từ" là size rẻ nhất quy ra giờ):

| Provider | Tier | Adapter | Mức xác minh | Isolation | Boot | Capability | Sống tối đa | Giá từ | Biến môi trường |
|---|---|---|---|---|---|---|---|---|---|
| boxd.sh (`boxdsh`) | T2 | native | docs | microvm | 300 ms | fork, pause, cổng | không giới hạn | $0.0614/giờ | `BOXDSH_API_KEY`, `BOXDSH_ORG` |
| Prized Sandboxes (`prized`) | T2 | native | **live** (đã chạy thật toàn bộ vòng đời, free tier) | microvm | 4000 ms | fork, pause | 7 ngày | $0.0660/giờ | `PRIZED_API_KEY` |
| Daytona (`daytona`) | T2 | native | docs | container | 1000 ms | fork, pause, cổng, GPU | không giới hạn | $0.0669/giờ (ước tính) | `DAYTONA_API_KEY`, `DAYTONA_ORGANIZATION_ID` |
| Novita Sandbox (`novita`) | T2 | native | **live** (tạo/exec/file/xoá chạy thật ở us-phx-1; pause và luồng boxmes-core chưa chạy vì hết số dư) | microvm | 600 ms | pause, cổng | 4 giờ | $0.0763/giờ | `NOVITA_API_KEY`, `NOVITA_TEMPLATES` |
| E2B (`e2b`) | T2 | native | **live** (đã chạy thật toàn bộ vòng đời + box up/exec/down qua boxmes-core, gói Hobby) | microvm | 400 ms | fork, pause, cổng | 24 giờ | $0.1089/giờ | `E2B_API_KEY`, `E2B_TEMPLATES` |
| Runloop (`runloop`) | T2 | yaml | docs | microvm | 2000 ms | pause, cổng | không giới hạn | $0.0792/giờ | `RUNLOOP_API_KEY` |
| Blaxel (`blaxel`) | T2 | yaml | docs | microvm | 25 ms | pause, cổng | không giới hạn | $0.0828/giờ | `BL_API_KEY`, `BL_WORKSPACE` |
| Upstash Box (`upstash-box`) | T3 | yaml | mã nguồn SDK | container | 1500 ms | pause, cổng | không giới hạn | $0.0828/giờ (ước tính) | `UPSTASH_BOX_API_KEY` |
| Agent 37 (`agent37`) | T2 | yaml | docs | gvisor | 5000 ms | fork, pause, cổng | không giới hạn | $0.0832/giờ (ước tính) | `AGENT37_API_KEY` |
| boat (`boat`) | T2 | yaml | docs | vm | 3000 ms | fork, pause, cổng | không giới hạn | $0.1656/giờ (ước tính) | `BOAT_API_KEY` |
| Vercel Sandbox (`vercel`) | T2 | yaml | docs | microvm | 1500 ms | fork, cổng | 5 giờ | $0.1706/giờ (ước tính) | `VERCEL_TOKEN`, `VERCEL_PROJECT_ID`, `VERCEL_TEAM_ID` |
| Render (`render`) | T2 (chỉ dial) | native | docs | container | ~90 s (kéo image + deploy) | cổng (onrender.com, 1 cổng) | không giới hạn | $0 (free) / $0.0096/giờ (0.5c-512mb) | `RENDER_API_KEY`, `RENDER_OWNER_ID`, `RENDER_AGENT_IMAGE`, `RENDER_DISK_GB` |

Mức xác minh: **live** = đã gọi API thật bằng key thật (chỉ các lệnh đọc, không tốn tiền); **docs** = route và trường lấy từ tài liệu/OpenAPI chính thức, chạy với server giả; **mã nguồn SDK** = provider không công bố REST, adapter đọc từ SDK chính thức. "Ước tính" nghĩa là provider không công bố đơn giá theo giây, nên Boxmes dùng giá ước tính. Daytona và Upstash Box chạy container chung kernel, nên bị loại khi `untrusted=true`.

**Chưa tích hợp:**

| Provider | Lý do |
|---|---|
| Modal | Chỉ có SDK Python/JS chạy trên gRPC riêng của Modal, không có REST công khai để core gọi. |
| Cloudflare Sandbox | Chạy trong Worker của chính người dùng (Durable Object + SDK), không có control plane REST dạng dịch vụ. |
| Koyeb | Sandbox còn ở giai đoạn public preview, tạo qua SDK/CLI, exec qua WebSocket stream; chưa có REST ổn định. |
| zipbox | Bản này chưa xác minh API và đơn giá. Có thể thêm bằng adapter YAML tuỳ chỉnh. |

### API v1.4
- `GET /v1/sandbox/providers` → `{items:[{id, name, tier, adapter, verification, verified_note, docs, initials, color, capabilities:{background, pause, snapshot, fork, ports, pty, files, gpu, boot_ms, max_lifetime_s, max_cpu, max_ram_mb, isolation}, pricing:{cpu_second, gb_second, disk_gb_second, flat_second, source, confidence}, sizes, regions, credentials, env_vars, mode, connected, usable, blocked?, health, from_usd_h, chain_rank?}]}`.
- `POST /v1/sandbox/adapters/lint {yaml}` (admin) → `{ok, error?, info?}`.
- `GET /v1/providers` có thêm `group: vps|sandbox`, `adapter: api|self|native|yaml`, `verification`, `docs`. `GET /v1/catalog` có thêm `tier: T1|T2|T3` và `group`; offer sandbox có `min_billing_minutes=0`.
- `PUT /v1/providers/:id/prefs` nhận thêm `sandbox_mode: auto|dial|shim`. Credential nhiều trường gửi dạng JSON trong `token`, ví dụ `{"token":"…","project_id":"…"}`.
- `POST /v1/recommend` và `requirements` nhận thêm `fork`; `isolation` có thêm giá trị `sandbox`. Phương án sandbox có `kind=sandbox` cùng `tier, fork, pause, ports, gpu, max_lifetime_h, boot_ms, verification, strong_isolation`. Lý do bị loại do capability: thiếu fork, không pause được (box lâu dài), quá thời gian sống tối đa, không có URL công khai, hoặc container chung kernel với code không tin cậy (luật này áp dụng cả khi `isolation=sandbox`).
- Host sandbox có `billing=sandbox` và `driver=sandbox` (T2) hoặc `shim` (T3). Host bị huỷ cùng box của nó.

### Biến môi trường v1.4

| Biến | Mặc định | Ý nghĩa |
|---|---|---|
| `BOXMES_SANDBOX_ADAPTERS_DIR` | — | thư mục chứa adapter YAML bổ sung (`*.yaml`) |
| `BOXMES_SANDBOX_MODE` | `auto` | `auto` / `dial` / `shim` cho mọi provider sandbox (prefs của từng provider được ưu tiên hơn) |
| `BOXMES_AUTO_SANDBOX` | tắt | cho `box_up` tự thuê sandbox mà không cần duyệt (tính theo giây, xoá cùng box) |
| `BOXMES_PROVIDER_CHAIN` | chuỗi T1 rồi tới chuỗi sandbox | thứ tự dự phòng (mặc định sandbox: e2b, daytona, novita, prized, boxdsh, runloop, blaxel, boat, agent37, vercel, upstash-box, render) |
| `E2B_API_KEY`, `DAYTONA_API_KEY`, `PRIZED_API_KEY`, … | — | kết nối provider sandbox lúc khởi động (xem cột "Biến môi trường") |

**Kiểm thử v1.4.** `go test ./...` gồm `sandbox/native_test.go` (httptest cho từng adapter native), `yaml_shim_test.go` (engine YAML + shim), `selector/sandbox_test.go`, `e2e/v14_test.go` (failover sang shim T3, boxd dial-in T2, dọn dẹp). Test live cho Prized là tuỳ chọn: chỉ đọc `BOXMES_LIVE_PRIZED=1 PRIZED_API_KEY=…`; tạo sandbox thật (vài giây, free tier) `BOXMES_LIVE_PRIZED_CREATE=1 PRIZED_API_KEY=… go test ./internal/sandbox -run PrizedLive`; end to end qua boxmes-core `PRIZED_API_KEY=… scripts/live-prized.sh` (shim T3; thêm `PRIZED_MODE=dial BOXMES_PUBLIC_URL=https://… BOXMES_DIST_DIR=…` cho T2, ví dụ qua cloudflared quick tunnel). Script luôn xoá sandbox nhãn `managed=1` khi kết thúc. E2B / Novita tương tự: `BOXMES_LIVE_E2B_CREATE=1 E2B_API_KEY=… go test ./internal/sandbox -run E2BLive` (hoặc `BOXMES_LIVE_NOVITA_CREATE=1 NOVITA_API_KEY=… … -run NovitaLive`), end to end `E2B_API_KEY=… scripts/live-e2b.sh` / `NOVITA_API_KEY=… scripts/live-novita.sh`. Danh sách sandbox của E2B trễ vài giây sau khi tạo/xoá, nên script kiểm tra bằng `GET /sandboxes/{id}`. Mọi thứ gửi tới provider (tên, nhãn/metadata `managed`/`mgr-host`, User-Agent) đều trung tính, không chứa tên sản phẩm. `scripts/e2e.sh` có mục v1.4: một adapter YAML trỏ tới `scripts/fake-sandbox.py` (sandbox REST chỉ exec, chạy trên localhost). Mục này kiểm tra lint, kết nối, recommend, tạo box trên host sandbox (shim T3), exec, và sandbox bị xoá khi box kết thúc.

## 11. Deploy app trên sandbox (URL công khai, sleep, snapshot, clone)

Agent dùng box sandbox để chạy web app thật: `box up` → `git clone` + cài đặt → chạy server ở `0.0.0.0:<port>` → lấy URL công khai từ Boxmes.

- **URL công khai của port.** `GET /v1/boxes/:id/ports` liệt kê từng port `{port, url, kind, public, note, caveats}`; `POST /v1/boxes/:id/ports {port}` mở thêm port cho box đang chạy. CLI `box url <box> [port]`, MCP tool `box_url {box_id, port?}`. `url` của box là URL của port đầu tiên trong `ports`. Boxmes đi theo chuỗi dự phòng, dừng ở bước đầu tiên dùng được:
  1. `provider-preview` — host preview của provider: E2B `https://<port>-<sandboxID>.e2b.app`, Novita `https://<port>-<sandboxID>.<domain>` (vd. `us-phx-1.sandbox.novita.ai`, không cần traffic token), Daytona theo API preview. Khi box ngủ URL trả 502 cho tới khi wake.
  2. `ingress` — core phục vụ port ở `<BOXMES_PUBLIC_URL>/x/<key>-<port>/` (`key` là HMAC theo box, không đoán được) và chuyển từng request qua ingress Proxy sẵn có. Box T2 đã có boxd dial-in nên không cần gì thêm; box T3 (shim) được core thả một agent nhỏ (`boxd ingress`, tải từ `<public>/dl/boxd-linux-<arch>`, cần `BOXMES_DIST_DIR`) gọi ra `/v1/ingress/connect`. Điều kiện: `BOXMES_PUBLIC_URL` đã đặt và trông như địa chỉ Internet (không phải localhost/`.local`/IP nội bộ; ép bằng `BOXMES_INGRESS_ASSUME_PUBLIC=1`) **và** agent kết nối được từ trong sandbox trong 25 s. Hạn chế: URL có tiền tố đường dẫn nên app phải dùng link tương đối; chỉ HTTP, đệm qua core (không WebSocket/streaming, body tối đa 32 MB); request đầu tiên vào box đang ngủ sẽ đánh thức box.
  3. `tunnel` — Cloudflare quick tunnel chạy trong sandbox (cloudflared tải khi cần, miễn phí, không cần tài khoản) → `https://<random>.trycloudflare.com/`. Hạn chế: không cam kết uptime; URL đổi nếu tunnel phải khởi động lại (vd. wake làm mất tiến trình); request đầu có thể chậm vài giây. Tắt bằng `settings.disable_tunnel` hoặc `BOXMES_DISABLE_TUNNEL=1`.
  4. `none` — không có URL; `note` giải thích, `caveats` ghi lý do từng bước bị bỏ qua.
  Sau khi wake, core dựng lại exposure (khởi động lại agent / kiểm tra lại tunnel, URL mới nếu phải chạy lại); `box down` gỡ hết. Mọi thứ gửi vào sandbox dùng tên trung tính (thư mục `.boxd`, `boxd ingress`, `cloudflared`).
- **Sleep / wake.** `box sleep` = pause của provider, `box wake` = resume (shim T3 và host T2). Idle sleep của reaper (`idle_minutes`) dùng cùng đường này. Đo live: tiến trình app và file còn nguyên sau khi wake, URL phục vụ lại ngay.
- **Snapshot T3.** Shim tar `work memory AGENTS.md` trong sandbox, kéo về core (`$BOXMES_DATA_DIR/snapshots/<snap>.tar.gz`, `location=local://…`, tối đa 64 MB; lớn hơn cần object storage, khi đó sandbox tự upload lên URL presign). Restore đẩy tarball lên và giải nén thay `/work` + `/memory` (giữ AGENTS.md của box). Snapshot của shim restore được vào box sandbox khác; `box fork` từ snapshot T3 tạo một sandbox mới cùng provider. Retention xoá cả file tarball ở core.
- **Clone box sandbox.** Provider có fork gốc (E2B, Prized): `POST /boxes/:id/clone` gọi fork của provider (bộ nhớ + đĩa, tiến trình đang chạy cũng được nhân bản), rồi đăng ký sandbox con thành host T3 mới. Provider không có fork (Novita): snapshot → sandbox mới → restore (file được nhân bản, tiến trình phải chạy lại). Box T2 dial-in chưa clone được (trả gợi ý dùng `sandbox_mode=shim`).
- **Sửa lỗi.** Host đã xoá không còn bị "sống lại": `destroyHost` ghi trạng thái `deleted` trước khi đóng tunnel, `HostDisconnected` bỏ qua host đã xoá, và boxd của host đã xoá không đăng ký lại được (trước đây boxd kết nối lại ngay sau khi bị đóng có thể ghi đè host thành `ready`).

Kết quả live 2026-10-27 (`scripts/live-deploy.sh`, repo `heroku/node-js-sample`, Express trên cổng 8000):

| | E2B | Novita | Prized |
|---|---|---|---|
| box up | 0.44 s | 0.69 s | 4.2 s |
| clone + `npm install` | 3.0 s | 3.0 s | 3.7 s |
| URL công khai → 200 "Hello World!" | có, 0.14 s | có, 0.2 s | không có preview → tunnel / ingress (bảng dưới) |
| sleep (pause) / wake (resume) | 0.19 s / 0.17 s | 0.77 s / 0.21 s | 0.34 s / 0.30 s |
| tiến trình + file sau wake | còn | còn | file còn |
| idle sleep (`idle_minutes=1`) | 65 s | 65 s | — |
| snapshot / restore (0.7 MB) | 0.26 s / 0.14 s | 0.78 s / 0.44 s | 0.32 s / 0.79 s |
| fork snapshot → sandbox mới | 0.56 s | 1.2 s | — |
| clone | 1.8 s, fork gốc, app chạy ngay | 1.6 s, qua snapshot, chạy lại app | 1.2 s, fork gốc |

Chạy: `E2B_API_KEY=… scripts/live-deploy.sh`, `SANDBOX_PROVIDER=novita NOVITA_API_KEY=… scripts/live-deploy.sh` (`SKIP_FORK=1`, `SKIP_IDLE=1` để ít sandbox / nhanh hơn). Script tạo tối đa 2 sandbox cùng lúc, có watchdog xoá mọi sandbox `managed=1` sau 280 s, và kiểm tra provider không còn sandbox nào. Test không cần mạng: `e2e/deploy_test.go` (provider giả có/không fork: URL preview, ports, sleep→pause, snapshot/restore, fork, clone, dọn dẹp) và `TestShimT3` (snapshot/restore/fork của shim).

Chạy lại sau khi đổi tên (2026-10-27, `live-deploy.sh`): E2B box up 0.44 s, npm install 5.0 s, URL → 200 0.17 s, sleep/wake 0.16/0.19 s, idle sleep 63 s, snapshot/restore 0.31/0.17 s, fork 0.44 s, clone 1.7 s (tiến trình còn); Novita box up 0.58 s, install 4.8 s, URL → 200 0.20 s, sleep/wake 0.59/0.18 s, idle sleep 64 s, snapshot/restore 0.60/0.45 s, fork 1.3 s, clone 1.6 s (chạy lại app). Không lỗi.

**URL công khai trên Prized (không có preview)** — `scripts/live-publicurl.sh` (2026-10-27, `heroku/node-js-sample`, cổng 8000):

| | `MODE=tunnel` (không đặt `BOXMES_PUBLIC_URL`) | `MODE=ingress` (core lộ ra qua cloudflared quick tunnel làm `BOXMES_PUBLIC_URL`) |
|---|---|---|
| kind / URL | `tunnel`, `https://<random>.trycloudflare.com/` | `ingress`, `<public>/x/<key>-8000/` |
| box up / clone + `npm install` | 4.3 s / 3.6 s | 4.1 s / 3.4 s |
| `GET /ports` lần đầu (dựng exposure) | 7.9 s (tải cloudflared + chờ URL) | 0.53 s (tải agent + kết nối) |
| curl từ ngoài → 200 "Hello World!" | 0.11 s (sau đó 0.08 s) | 0.23 s (sau đó 0.11 s) |
| URL khi box ngủ | 502 | 200 — request đầu đánh thức box |
| sleep / wake | 0.26 s / 0.29 s | 0.31 s / 0.19 s |
| sau wake | tiến trình còn, `GET /ports` 7.2 s, URL giữ nguyên, 200 sau 0.08 s | tiến trình còn, `GET /ports` 9 ms, URL giữ nguyên, 200 sau 0.10 s |
| box down → URL chết | 0.05 s (tunnel còn trả lời vài giây khi Prized đang xoá, rồi 530) | 0.1 s → 404 ngay |

Chạy: `PRIZED_API_KEY=… scripts/live-publicurl.sh` (tunnel) hoặc `MODE=ingress PRIZED_API_KEY=… scripts/live-publicurl.sh` (cần `cloudflared` trong PATH; script tự build `boxd-linux-amd64/arm64` vào `BOXMES_DIST_DIR`). Một sandbox, watchdog 280 s. Test không cần mạng: `e2e/publicurl_test.go` (agent boxd thật tải từ `/dl`, cloudflared giả).


## 12. Đổi tên Boxa → Boxaz

Sản phẩm đổi tên thành **Boxaz**: binary `boxaz-core` (trước là `boxa-core`), biến môi trường `BOXAZ_*`, systemd `boxaz-core.service`, module Go `boxaz/core`, SDK `boxaz` (Python) / `Boxaz` (TypeScript). Lớp tương thích giữ cấu hình cũ chạy được:

- `BOXA_*` vẫn được đọc khi `BOXAZ_*` cùng tên chưa đặt (tên mới luôn thắng); core in một dòng cảnh báo deprecated lúc khởi động. Dashboard (`next.config.ts`) và SDK làm tương tự với `BOXA_*` / `NEXT_PUBLIC_BOXA_*`.
- Dữ liệu cũ: `<data>/boxa.db` được dùng nếu chưa có `boxaz.db`; `/var/lib/boxa/memory`, container `boxa-*`, file `.boxa.json` của driver local, secret hệ thống `BOXA_SYS_*`, cookie `boxa_key` / `boxa_locale`, tham số ingress `boxa_token` / cookie `boxa_t` vẫn được nhận.
- SDK: `import boxa` (Python) và `Boxa`, `BoxaError`… (TypeScript) là alias deprecated.
- Mọi thứ provider nhìn thấy (tên `mgr-*`, nhãn `managed`/`mgr-host`, User-Agent `sandbox-client/1`) vẫn trung tính, không chứa tên sản phẩm.

## 13. v1.7 — app được giám sát, URL Boxmes đánh thức box, URL token

Mục tiêu: agent deploy web app một lần rồi quên — app tự chạy lại, URL không đổi, box ngủ khi rảnh và thức khi có người truy cập.

- **App supervisor.** `PUT /v1/boxes/:id/app {repo?, ref?, install?, start, port?, ready?: {http|tcp}, restart?: always|on-failure|never, env?, workdir?, backoff_max_s?}` khai báo app; Boxmes clone repo (nếu có), chạy `install` một lần cho mỗi spec, chạy `start` (nhận `$PORT`), restart khi crash với backoff (trần mặc định 30 s), chạy lại sau wake / restore / clone và chỉ cho traffic qua gateway khi app sẵn sàng (HTTP status < 500 ở `ready.http`, mặc định TCP trên `port`). Box T1/T2 dùng supervisor trong boxd; box T3 (exec-only) dùng script `setsid` trong sandbox (`status.supervisor = boxd | script`). `GET /app` → `{spec, status: {state, ready, pid, restarts, last_exit, started_at, ready_at, message, supervisor}}`, `POST /app/restart`, `DELETE /app`, `GET /app/logs?offset=&max=` (ring buffer + file; `?follow=1` stream SSE event `log {data, offset, truncated}`). CLI `box app set|status|restart|logs [-f]|rm`, MCP `box_app_set`, `box_app_status`, `box_app_logs`.
- **Gateway URL theo box + port.** Mỗi port đã mở có `gateway_url` cố định: `<BOXMES_PUBLIC_URL>/x/<key>-<port>/`, hoặc `https://<key>-<port>.<BOXMES_GATEWAY_DOMAIN>/` khi đặt `BOXMES_GATEWAY_DOMAIN` (không còn tiền tố đường dẫn). Gateway chuyển thẳng WebSocket, SSE và body chunked (không đệm) qua link stream của box hoặc host preview của provider (chỉ HTTP/1.1).
- **Wake on request.** Request vào box đang ngủ đánh thức box, giữ request tối đa `settings.wake.hold_s` (mặc định 10 s, hoặc `BOXMES_WAKE_HOLD`), chờ app sẵn sàng rồi trả lời; hết thời gian thì trả trang "waking" (trình duyệt) hoặc 503 + `Retry-After` (`settings.wake.retry_after_s`, mặc định 5). HEAD, bot, link preview và health probe (`/healthz`, `/robots.txt`…) không đánh thức box trừ khi `settings.wake.wake_on_probes=true`. Tắt theo box: `PATCH /v1/boxes/:id {wake_on_request: false}`.
- **URL token.** `POST /v1/boxes/:id/ports/:port/tokens {ttl_s?, label?, private?}` cấp token `bxt_…` chỉ cho box + port đó (mặc định 1 ngày, 60 s … 30 ngày), trả giá trị một lần kèm `url` (`gateway_url?token=…`) và chuyển port sang `access=token`. Token gửi qua `?token=` (đổi sang cookie, 302), `Authorization: Bearer` hoặc `X-Boxmes-Token`; `?token=` của chính app (không có tiền tố `bxt_`) được giữ nguyên. `GET …/tokens` liệt kê (không có giá trị), `DELETE …/tokens/:token_id` thu hồi ngay, `PUT /v1/boxes/:id/ports/:port {access: public|token}`. CLI `box url <box> <port> --token [--ttl 1h]`, MCP `box_url_token`. Không bao giờ chia sẻ credential của provider.
  - **Lưu ý:** token chỉ bảo vệ URL Boxmes. Port ở chế độ token không còn được Boxmes trả URL preview của provider, nhưng trên provider có host preview riêng (E2B, Novita, Daytona) URL preview đó **vẫn truy cập được tại provider** với bất kỳ ai biết sandbox id (`https://<port>-<sandboxID>.e2b.app`…). Đừng dựa vào token để giữ bí mật trên các provider này; với dữ liệu nhạy cảm hãy để app tự xác thực hoặc dùng provider không có inbound HTTP (Prized, đi qua ingress của Boxmes).
- **Capability lifecycle.** `GET /v1/capabilities` → mỗi provider `{provider, group, memory_pause, wake_on_http_native, wake_via_gateway, fork, inbound_http, max_runtime, note}` (E2B / Novita / Prized theo tài liệu và đo live, sandbox khác lấy từ capability của adapter, host VPS: sleep dừng container nên app chạy lại khi wake).
- **Dashboard.** Tab **Port & app** ở trang box: URL Boxmes từng port (copy / mở), bật tắt token, cấp token (thời hạn + nhãn, hiện giá trị một lần), danh sách + thu hồi token, công tắc wake on request, badge capability của provider, trạng thái app (spec, state, restarts, ready) với nút restart và log theo dõi trực tiếp (SSE). Có dữ liệu giả lập khi `BOXMES_MOCK=1`; chuỗi đủ 4 ngôn ngữ.
- **Sửa lỗi trong đợt live.** Transport tới host preview chỉ dùng HTTP/1.1 (ALPN h2 làm hỏng WebSocket upgrade), bỏ các header CDN đầu vào trước khi chuyển tiếp, và fork gốc không còn mang theo ingress agent / tunnel của box nguồn.

Kết quả live 2026-10-27 (`scripts/live-app.sh`, repo `heroku/node-js-sample`, `node index.js` trên cổng 8000, core lộ ra Internet qua cloudflared quick tunnel làm `BOXMES_PUBLIC_URL`):

| | Prized | E2B |
|---|---|---|
| sleep → request vào URL Boxmes → 200 (wake on request) | 426 ms | 432 ms |
| kill app → supervisor restart → ready | 1.7 s | 1.6 s |
| restore snapshot → app ready | 2.0 s | 1.5 s |
| clone → app của bản clone ready | 1.7 s | 1.7 s |
| URL token: không token / Bearer / `?token=` / sau thu hồi | 401 / 200 / 302 / 401 | 401 / 200 / 302 / 401 |

Chạy: `PROVIDERS="prized e2b" PRIZED_API_KEY=… E2B_API_KEY=… scripts/live-app.sh` (cần `cloudflared`). Mỗi provider tối đa 2 sandbox cùng lúc (box + clone), watchdog xoá mọi sandbox `managed=1` sau 280 s, cuối cùng kiểm tra provider không còn sandbox nào. Test không cần mạng: `e2e/v17_test.go` (gateway, wake, token, app), `boxd/supervisor_test.go`, `sandbox/appscript_test.go`; `scripts/e2e.sh` chạy cả luồng v1.7 với core + boxd thật.

## 14. v1.8 — gateway trên domain thật (boxs.tech) + chặn URL preview của provider

Mục tiêu: URL app ổn định, đẹp, có TLS trên domain riêng, và token URL thật sự bảo vệ được app (không còn đường vòng qua URL preview của provider).

- **Gateway trên domain riêng.** `BOXMES_GATEWAY_DOMAIN=boxs.tech` + `BOXMES_PUBLIC_URL=https://gw.boxs.tech`: API/dashboard ở `gw.boxs.tech`, mỗi port của box ở `https://<key>-<port>.boxs.tech/` (key = HMAC 20 hex theo box). Universal SSL miễn phí của Cloudflare chỉ phủ wildcard một cấp nên URL dùng một nhãn `<key>-<port>`. Domain cũng đặt được trong dashboard (`settings.gateway.domain`, env thắng).
- **Subdomain dành riêng.** Core chỉ chuyển vào box nhãn đúng dạng `^[0-9a-f]{20}-[0-9]{1,5}$`; host của `BOXMES_PUBLIC_URL` và domain trần là core; danh sách dành riêng (`hark`, `mcp`, `gw`, `www`, `api`, `admin`, `app`, `mail`, `status`, `docs`… + `BOXMES_GATEWAY_RESERVED`) và mọi nhãn khác trả 404. Trước đây `gw.<domain>` bị hiểu nhầm là box. Bản ghi DNS cụ thể (vd. `hark.boxs.tech`, `mcp.boxs.tech` của tunnel khác) luôn thắng wildcard nên không bị ảnh hưởng.
- **Cloudflare Tunnel.** `deploy/cloudflare-tunnel/`: `setup.sh` (tạo tunnel remotely-managed qua API, ingress `gw.` + `*.` → core, CNAME proxied, ghi token; idempotent, không đụng tunnel/bản ghi khác), `run.sh` (cloudflared, token qua env), `up.sh` / `down.sh` (một lệnh bật/tắt core + cloudflared). Hướng dẫn chi tiết trong `deploy/cloudflare-tunnel/README.md`.
- **Đóng lỗ hổng URL preview.** `BOXMES_PROVIDER_PREVIEW=auto` (mặc định): khi gateway truy cập được từ Internet, sandbox mới của E2B và Novita được tạo private (`network.allowPublicTraffic=false`, Novita kèm `secure: true`). Host preview của provider trả 403 nếu thiếu traffic token; chỉ gateway gửi kèm `E2B-Traffic-Access-Token` lên upstream (header của client bị bỏ). Token lấy từ response create/fork, mất sau khi core khởi động lại thì lấy lại qua `POST /sandboxes/{id}/connect`. Port của sandbox private có `kind: gateway`, `url = gateway_url`, `provider_preview: private`; port token không còn caveat "preview vẫn công khai". Fork gốc (clone) giữ chế độ private. Prized không có host preview (đi qua ingress agent → core, URL cũng là `<key>-<port>.<domain>`). Daytona/adapter khác: preview vẫn công khai, port báo `provider_preview: public` và port token có caveat. `public` tắt cơ chế này.
- **API.** `GET /v1/gateway[?check=1]` → `{mode: host|path|relative, domain, domain_source, public_url, url_pattern, internet_facing, reserved, provider_preview, provider_preview_source, private_previews, providers:[{provider, preview_host, private_preview, note}], check?:{ok, status, latency_ms, error?}}`. `PortView.provider_preview`, `Host.private_traffic`, capability `private_preview`; `settings.gateway {domain, provider_preview}` (domain sai / mode sai → 400).
- **Dashboard.** Cài đặt → thẻ *Gateway domain*: kiểu URL, mẫu URL, URL công khai + trạng thái (tự kiểm tra, độ trễ), domain (khoá khi đặt bằng env), chính sách preview, badge theo provider, danh sách subdomain dành riêng. Tab Port & app hiện "preview private" thay cho cảnh báo khi port `provider_preview=private`. Đủ 4 ngôn ngữ; mock `BOXMES_MOCK=1`.

Kết quả live 2026-10-27 (`scripts/live-domain.sh`, `heroku/node-js-sample` qua app spec, cổng 8000, curl từ ngoài qua DNS-over-HTTPS 1.1.1.1, TLS Let's Encrypt `*.boxs.tech` hợp lệ; tunnel `boxaz-gw` id `5b1d9faa-acb1-4014-bda2-40903ecedb81`):

| | E2B | Novita (us-phx-1) | Prized |
|---|---|---|---|
| box up / app ready | 0.51 s / 5.6 s | 0.71 s / 3.5 s | 4.5 s / 5.2 s |
| `https://<key>-8000.boxs.tech/` → 200 lần đầu / ấm | 275 ms / 115 ms | 326 ms / 155 ms | 183 ms / 109 ms |
| sleep → request → 200 (wake on request) | 378 ms | 489 ms | 531 ms |
| URL token: không / Bearer / `?token=` / sau thu hồi | 401 / 200 / 302 / 401 | 401 / 200 / 302 / 401 | 401 / 200 / 302 / 401 |
| URL preview trực tiếp của provider (chạy / ngủ / port token) | 403 / 403 (không đánh thức) / 403 | 403 / 403 / 403 | không có |
| SSE qua domain | 5 event trải 2.0 s (không đệm) | — | — |
| clone (fork gốc) | 200 sau 1.0 s, preview của clone 403 | — | — |
| box down → URL | 404 | 404 | 404 |
| core khởi động lại → URL | 200 sau 0.54 s (traffic token lấy lại qua connect) | — | — |

Tổng cộng 6 sandbox (E2B 3, Novita 1, Prized 2), không quá 2 cùng lúc; cuối cùng E2B, Novita (cả 2 region) và Prized đều `[]`. Lần đầu Novita trả 400 "cannot create a sandbox without public access unless you enable secure envd access" → thêm `secure: true`.

Chạy lại: `ENV_FILE=…/gateway.env deploy/cloudflare-tunnel/up.sh`, rồi `ENV_FILE=…/gateway.env PROVIDERS="e2b novita prized" scripts/live-domain.sh`. Test không cần mạng: `e2e/v18_test.go` (provider giả có host preview đòi traffic token: URL theo domain, header lên upstream, header giả của client bị bỏ, host dành riêng/khác → 404, settings, chế độ public), `sandbox/TestE2BPrivateTraffic` (body create, token, lấy lại qua connect cho E2B và Novita).


## 15. v1.9 — URL dễ đọc theo tên box (không còn số trong URL)

Mục tiêu: URL app nhớ được, gõ được, không lộ key hay số port — `https://demo-app.boxs.tech/` thay cho `https://21e664405e704d2d1786-8000.boxs.tech/`.

- **Dạng URL (chọn `<tên-box>-<nhãn>`).** Port chính: `https://<tên-box>.<domain>/`. Port khác: `https://<tên-box>-<nhãn>.<domain>/` (vd. `demo-app-api.boxs.tech`, `demo-app-db.boxs.tech`). Chọn `<tên>-<nhãn>` (không phải `<nhãn>-<tên>`) để mọi URL của một box đứng cạnh nhau và bắt đầu bằng tên box. Vẫn là **một nhãn DNS** nên wildcard `*.boxs.tech` + Universal SSL miễn phí vẫn đủ. Không có domain gateway: `<BOXMES_PUBLIC_URL>/x/<tên-box>/` và `/x/<tên-box>-<nhãn>/`.
- **Port chính** = port của app spec (nếu đã mở), không thì port đầu tiên được mở.
- **Tên box.** Lấy theo thứ tự: `subdomain` khi tạo → `name` (box_ensure) → `slug` được yêu cầu → tên template; slugify về DNS-safe (`a-z0-9-`, bỏ dấu tiếng Việt: "Tiệm Phở" → `tiem-pho`, tối đa 40 ký tự để còn chỗ cho `-<nhãn>`, phải có ít nhất một chữ cái, không được trùng dạng cũ `<key>-<port>`). Duy nhất trên cả gateway (gồm cả các host `<tên>-<nhãn>` của box khác). Tên tự suy ra mà bị trùng/bị dành riêng → thêm **một từ** (không dùng số): `demo-app` → `demo-app-blue`, `gw` → `gw-blue`; bản clone/fork cũng vậy. Tên đặt tường minh mà trùng → 400 `INVALID` kèm `next` gợi ý tên trống (`box url <id> --name demo-app-blue`). Tên dành riêng (`hark`, `mcp`, `gw`, `www`, `api`, `admin`, … + `BOXMES_GATEWAY_RESERVED`) luôn bị từ chối. Box `down` thì tên được giải phóng.
- **Nhãn port.** Chữ cái đầu, `a-z0-9-`, tối đa 20 ký tự, không bao giờ là số. Mặc định theo port quen thuộc: 80/3000/4200/5000/5173/8000/8080 → `web` (port web thứ hai → `app`, `site`, `ui`), 3001/4000/8001 → `api`, 8081/9000 → `admin`, 5432 → `db`, 3306 → `mysql`, 6379 → `redis`, 27017 → `mongo`, 8888 → `jupyter`, 7860 → `gradio`, 8501 → `streamlit`, 11434 → `ollama`, 9229 → `debug`, 9090 → `metrics`…; port lạ → từ NATO (`alpha`, `bravo`, …). Đặt nhãn: `POST /ports {port, label}`, `PUT /ports/{port} {label}`, `app.label`.
- **Đổi tên.** `PATCH /v1/boxes/{id} {subdomain}`, CLI `box url <box> --name demo-app` (nhãn port: `box url <box> 8001 --as files`, hoặc `--label` khi không kèm `--token`), MCP `box_url {name, label}`, dashboard (tab Port & app: ô *Tên* + nút đổi nhãn ✎). Tên cũ ngừng trả lời ngay; URL token, chế độ token, wake-on-request, preview private không đổi.
- **Tương thích.** Địa chỉ cũ `<key>-<port>` (host và `/x/…`) vẫn chạy như alias, được trả trong `alias_url`; API/CLI/MCP/web hiện URL theo tên trước. Box tạo trước v1.9 được đặt tên khi core khởi động.
- **Lưu ý bảo mật.** URL theo tên **đoán được** (khác key HMAC cũ). App riêng tư hãy bật chế độ token (`box url <box> <port> --token`) hoặc để app tự xác thực.
- **API.** `PortView` thêm `label`, `primary`, `alias_url` (`url`/`gateway_url` theo tên); `PortsResult` thêm `name`, `primary_port`; `Box` thêm `subdomain`, `port_labels`; `GET /v1/gateway` thêm `label_pattern`, `alias_pattern` (`url_pattern` = `https://<name>.<domain>/`). Lỗi mới có bản dịch vi/zh/ja.

Kết quả live 2026-10-27 (`scripts/live-names.sh`, E2B, `heroku/node-js-sample` cổng 8000, curl từ ngoài qua DoH 1.1.1.1, tunnel `boxaz-gw`): box up 0.42 s, app ready 6.2 s; `https://demo-app.boxs.tech/` → 200 lần đầu 290 ms / ấm 143 ms (TLS hợp lệ); alias `https://<key>-8000.boxs.tech/` → 200; port thứ hai `8001` nhãn `files` → `https://demo-app-files.boxs.tech/` → 200; sleep → request → 200 trong 577 ms; box down → 404. 1 sandbox, cuối cùng E2B `[]`.

Test không cần mạng: `e2e/v19_test.go` (tên, nhãn, trùng → hậu tố từ, tên tường minh trùng/dành riêng/sai → 400 + `next`, đổi tên, tên chiếm host nhãn của box khác, giải phóng khi down, alias), `model/names_test.go` (slugify tiếng Việt, luật tên/nhãn); `scripts/e2e.sh` (URL `/x/e2e-app/`, `box url --name`, alias, tên dành riêng); web `scripts/smoke.sh` (mock).


## 16. v1.10 — truy cập box: terminal bền, forward port, SSH (ticket một lần)

Mục tiêu: con người vào được box ngoài HTTP — terminal trên dashboard, nối Postgres/Redis trong box về `localhost`, `ssh` / VS Code Remote-SSH — mà **API key không bao giờ nằm trong URL**, và terminal không mất khi rớt mạng.

- **Ticket một lần.** `POST /v1/boxes/{id}/tickets {purpose: terminal|tcp|ssh, port?, session_id?, ttl_s?, allow?}` → `bxa_<box>_<40 hex>`; đổi được **một lần**, trong **60 giây** (`ttl_s` 10…86400), gắn với một box + một mục đích (+ port với `tcp`, + phiên với `terminal` nếu truyền `session_id`). Core chỉ lưu tiền tố + hash; giá trị trả về đúng một lần (`Cache-Control: no-store`). Kết nối đã mở bị ràng buộc với ticket: `DELETE /v1/boxes/{id}/tickets/{tid}` (hoặc `all`) ngắt ngay các phiên đó. `GET /v1/boxes/{id}/tickets` liệt kê (không có giá trị, có `live`).
- **WebSocket chỉ nhận ticket hoặc header.** `/v1/boxes/{id}/{terminal,tcp,ssh}` và `wss://<tên>.<domain>/.boxmes/{terminal,tcp,ssh}` nhận `?ticket=bxa_…` hoặc `Authorization: Bearer <ticket|API key>`. `?token=<API key>` và cookie dashboard bị từ chối (401 "API keys are not accepted in URLs").
- **Origin allowlist.** Trình duyệt phải đến từ origin được phép: `BOXMES_DASHBOARD_URL` (origin của web dashboard), `BOXMES_PUBLIC_URL`, `BOXMES_ACCESS_ORIGINS` (phẩy), CORS origin tường minh. Client không gửi `Origin` (CLI) luôn được; origin loopback được khi core cũng chạy trên loopback (dev). Sai → 403 `POLICY_DENIED` trước khi upgrade.
- **Terminal bền.** `wss://<core>/v1/boxes/{id}/terminal?ticket=…&proto=2&mode=control|view[&session=<id>][&cols&rows]`. Phiên (`term_…`) sống qua lúc rớt mạng trong `BOXMES_TERMINAL_DETACH_TTL` (mặc định 10 phút), gắn lại sẽ phát lại tối đa `BOXMES_TERMINAL_BUFFER` (256 KiB) output cuối; một người điều khiển (input + resize), nhiều người xem; mở terminal đánh thức box đang ngủ và giữ box thức; box ngủ thì phiên đóng. `proto=2`: output là frame nhị phân, input gửi nhị phân; frame text là JSON điều khiển — server gửi `session {id, created, control, replay, cols, rows, viewers, detach_ttl_s}`, `viewers {n, control}`, `control {control:false}` (bị người khác lấy quyền), `exit {code?, reason?}`, `ping`; client gửi `resize {cols, rows}`, `control {control}`, `ping`. Không có `proto=2` thì giữ giao thức text cũ. `GET /v1/boxes/{id}/terminals` → `{items:[{id, name, cols, rows, viewers, controlled, created_at, last_io, detached_at?, expires_at?, exited?, exit_code?, buffered_bytes}], detach_ttl_s}`; `DELETE /v1/boxes/{id}/terminals/{sid|all}`.
- **Forward TCP.** `box forward <box> <port-trong-box> [port-local] [--bind 127.0.0.1]`: mỗi kết nối local lấy một ticket `tcp` rồi mở `wss://<tên>.<domain>/.boxmes/tcp?port=N` (frame nhị phân = byte TCP, ping 25 s, tự thử lại). Port được phép = port đã mở + SSH nội bộ + danh sách forward của box: `POST /v1/boxes/{id}/forward-ports {port}`, thay cả danh sách bằng `PATCH /v1/boxes/{id} {forward_ports:[…]}` (tối đa 32); `box forward` tự thêm (`allow=true`).
- **SSH không cần sshd trong box.** Gateway SSH nằm trong core (`x/crypto/ssh` qua WebSocket, tuỳ chọn listener trực tiếp `BOXMES_SSH_ADDR`): shell, exec, pty, env, window-change, signal, `ssh -L` (VS Code / Cursor Remote-SSH), sftp khi box có `sftp-server`; không có `-R` / agent / X11. `box ssh <box> [-- lệnh]` (OpenSSH qua `ProxyCommand box ssh-proxy %h`, không có `ssh` thì dùng client dựng sẵn với `-L` / `-N`), `box ssh-config [--write]` (khối `Host` cho `~/.ssh/config`, host key alias `boxmes-ssh`, `~/.boxmes/known_hosts`), `box ssh-key add <file.pub> | ls | rm <id>` (`GET/POST/DELETE /v1/ssh/keys`), `GET /v1/ssh` (known_hosts, fingerprint, direct). SSH trực tiếp: `ssh -p 2222 $(box ticket <box> ssh)@<host>` (ticket làm username) hoặc key đã đăng ký.
- **Tóm tắt theo box.** `GET /v1/boxes/{id}/access` → `{box_id, name, host, link: boxd|agent|agent-on-demand|none, live, tcp {url, core_url, allowed_ports}, terminal {url, core_url, detach_ttl_s, sessions}, ssh {host, command, config, known_hosts, host_key_fingerprint, direct?, features}, commands {forward, ssh, ssh_exec, ssh_config, ssh_proxy, ssh_direct?}, notes?}`. `box access <box>` in ra như vậy; `box ticket`, `box terminals <box> [--kill <sid>|all]`.
- **MCP / SDK.** 30 tool: thêm `box_ticket`, `box_forward`, `box_ssh_info`, `box_terminal_sessions`. SDK Python (sync/async) + TypeScript: `access()`, `ticket()`, `tickets()`, `revoke_ticket()`, `allow_forward_port()`, `terminals()`, `kill_terminal()`, `ssh_info()`, SSH keys. `llms.txt` có mục "Box access" (4 ngôn ngữ), A2A card `x-boxmes.access` + `access_detail` theo `Accept-Language`, MCP `instructions` có cả 4 ngôn ngữ.
- **Dashboard.** Tab **Terminal**: lấy ticket qua proxy Next (`POST /api/boxmes/boxes/{id}/tickets`, API key chỉ ở server) rồi mở WebSocket `?ticket=…&proto=2`; chọn phiên (liệt kê, gắn lại có replay, phiên mới, đóng phiên), chế độ Điều khiển / Chỉ xem, tự kết nối lại bằng ticket mới (backoff 1→10 s, gắn lại đúng phiên), thông báo đánh thức box đang ngủ; "Chiếm quyền" vẫn là tạm dừng exec của agent. Tab **Truy cập**: lệnh copy được (`box forward <tên> <port>`, `box ssh <tên>`, `ssh -L`, OpenSSH, SSH trực tiếp), khối ssh-config + host key, quản lý port forward, danh sách ticket + **thu hồi tất cả**. Cài đặt → **SSH keys**. Mock `BOXMES_MOCK=1` có đủ route mới (WebSocket không đi qua proxy mock nên shell được mô phỏng, có ghi chú); 4 ngôn ngữ.
- **Cấu hình khi deploy dashboard.** Đặt `BOXMES_DASHBOARD_URL=https://<domain-dashboard>` ở core (compose đã có biến), và để `BOXMES_PUBLIC_URL` là URL core mà trình duyệt tới được (hoặc `NEXT_PUBLIC_BOXMES_WS` ở web) — terminal mở WebSocket thẳng tới core vì route Next không proxy được WebSocket.

**Bảo mật.**
- API key không còn xuất hiện trong URL (log proxy, lịch sử trình duyệt, header `Referer`); ticket rò rỉ chỉ dùng được một lần, trong 60 giây, cho một box + một mục đích.
- Ticket được hash khi lưu, có thể thu hồi; thu hồi ngắt phiên đang mở. Kết nối bằng API key (header) không bị thu hồi theo ticket — thu hồi key để cắt.
- Origin allowlist chặn trang lạ mở WebSocket bằng ticket đánh cắp trong trình duyệt nạn nhân; cookie dashboard không còn được nhận trên WebSocket (không còn rủi ro cross-site WebSocket hijacking).
- Forward chỉ tới port trong allowlist của box; SSH không có `-R`, agent hay X11 forwarding. Đối chiếu fingerprint host key (`GET /v1/ssh`, `box ssh-config` tự ghi `~/.boxmes/known_hosts`) trước khi tin lần đầu.
- Box đang bị người chiếm quyền (takeover): key không phải admin không lấy được ticket.

Kết quả live 2026-10-27 (`scripts/live-access.sh`, E2B / Prized, qua `boxs.tech`):

| | E2B | Prized |
|---|---|---|
| terminal: ticket → output đầu tiên | 1.83 s | 0.81 s |
| terminal: gắn lại phiên (replay) | 278 ms | 261 ms |
| `box forward` → `curl` qua localhost (http.server :9000) | 318 ms | 225 ms |
| `box ssh <box> -- uname -a` | 608 ms | 522 ms |
| `ssh -L` forward | 683 ms | 612 ms |

Chạy lại: `ENV_FILE=…/gateway.env PROVIDERS="e2b prized" scripts/live-access.sh`. Test không cần mạng: `e2e/v110_test.go` (ticket bắt buộc / dùng lại / hết hạn / sai mục đích, origin, `?token=` bị từ chối, terminal gắn lại + replay + viewer/controller, forward echo, ssh exec + mã thoát + `-L`), `scripts/e2e.sh` (bước v1.10, OpenSSH nếu có), web `scripts/smoke.sh` (mock: ticket, terminals, access, forward-ports, SSH keys, chuỗi 4 ngôn ngữ, bundle).

## 17. v1.11 — truy cập P1: SFTP, chứng chỉ SSH, trình duyệt (CDP), desktop (VNC), link chia sẻ

Mục tiêu: ngoài terminal / forward / SSH của v1.10, con người và agent **chép file**, **SSH không cần API key**, **điều khiển Chromium trong box** (Playwright, puppeteer, browser-use) và **xem / điều khiển màn hình** của box — vẫn không API key nào trong URL, mọi thứ đi qua WSS của gateway (không mở port 22 / 9222 / 5900).

**Lệnh.**

| Việc | Lệnh |
|---|---|
| Chép file (luôn chạy được) | `box sftp <box> put ./a.txt /tmp/a.txt` · `get /tmp/a.txt ./a.txt` · `ls [dir]` · `rm <path>` · `mkdir <dir>` |
| SFTP / scp của OpenSSH | `sftp -o ProxyCommand='box ssh-proxy %h' -o HostKeyAlias=boxmes-ssh boxmes@<tên>.<domain>` · `scp -o ProxyCommand='box ssh-proxy %h' -o HostKeyAlias=boxmes-ssh ./a.txt boxmes@<tên>.<domain>:/tmp/` |
| Chứng chỉ SSH ngắn hạn | `box ssh-cert <box> [--key ~/.ssh/id_ed25519.pub] [--ttl 12h] [--out file] [--json]` |
| ssh-config dùng chứng chỉ (VS Code / Cursor) | `box ssh-config --cert --ttl 12h --write [box…]` |
| SSH bằng chứng chỉ, không API key | `ssh -o CertificateFile=~/.ssh/id_ed25519-cert.pub -o ProxyCommand='box ssh-proxy --anon %h' -o HostKeyAlias=boxmes-ssh <tên>@<tên>.<domain>` |
| Trình duyệt (CDP) | `box browser <box> [--ttl 1h] [--json]` · `--status` · `--stop` |
| Desktop (VNC) | `box desktop <box>` (mở trang noVNC) · `--share [--ttl 1h] [--interactive]` · `--shares` · `--revoke-shares` · `--status` · `--stop` |

**URL và API.**
- **SFTP.** Subsystem `sftp` trên gateway SSH của core: box có `sftp-server` thì dùng nó; không có (`BOXMES_SFTP=core` ép luôn) thì core tự phục vụ SFTP (`pkg/sftp` request server) bằng các lệnh shell của box — nên chạy được cả trên sandbox chỉ có exec. `box sftp` dùng client Go dựng sẵn, không cần `sftp` cài trên máy.
- **Chứng chỉ SSH.** `POST /v1/ssh/certs {box, public_key, ttl_s? (60…43200, mặc định 3600)}` → `{id, box_id, certificate, serial, principals: [box id, tên, slug], valid_after, valid_before, fingerprint, host, user, proxy_command: "box ssh-proxy --anon %h", known_hosts, command, direct_command?, config, user_ca}`. Core giữ một SSH user CA ed25519 (sealed trong DB, `GET /v1/ssh` trả `user_ca`, `cert_max_ttl_s`). Chứng chỉ được nhận trên WebSocket `…/.boxmes/ssh` **không ticket** (`box ssh-proxy --anon`) và trên listener trực tiếp `BOXMES_SSH_ADDR`; mỗi chứng chỉ là một bản ghi ticket `ssh_cert` → `DELETE /v1/boxes/{id}/tickets/{id}` (hoặc `all`) thu hồi và ngắt phiên đang mở.
- **Trình duyệt.** `POST /v1/boxes/{id}/browser {ttl_s? (mặc định 3600, ≤ 86400), headless?}` → `{running, headless, source: found|installed|existing, binary, version, started, start_ms, clients, idle_stop_s, port: 9222, cdp_url, http_url, core_url, ticket_id, expires_at, live_view?}`. `cdp_url = wss://<tên>.<domain>/.boxmes/cdp?ticket=…` (ticket `cdp` **dùng lại được** tới `expires_at`), `http_url` cho `connectOverCDP("https://…")` (core viết lại `/json/version` để `webSocketDebuggerUrl` trỏ về gateway). Không có domain gateway: `…/v1/boxes/{id}/cdp?ticket=…`. Core tìm Chromium / Chrome / Playwright có sẵn hoặc cài (apt trên Debian, `npx playwright`, Chrome for Testing x86_64; `BOXMES_BROWSER_INSTALL=off` tắt), chạy trên `127.0.0.1:9222` với profile bền `~/.local/share/bx-browser`; headless trừ khi desktop đang chạy (khi đó hiện trên desktop). Box không có user namespace → `--no-sandbox`; RAM < 1,5 GB → cờ tiết kiệm bộ nhớ (< 900 MB thêm `--js-flags=--jitless --no-zygote`); không root / không sudo → tải thư viện Chromium bằng `apt-get download` + `dpkg-deb -x` vào thư mục người dùng (ưu tiên headless shell). `GET` chỉ báo trạng thái (không đánh thức box), `DELETE` tắt; tự tắt sau `BOXMES_BROWSER_IDLE` (10 phút) không có client.
- **Desktop.** `POST /v1/boxes/{id}/desktop {view_only?, width?, height?, ttl_s? (mặc định 60)}` → `{running, display, width, height, vnc_server, wm, started, start_ms, viewers, shares, idle_stop_s, port: 5900, browser_on_desktop, vnc_url, viewer_url, core_url, ticket_id, view_only, expires_at}`. Core cài Xvfb + x11vnc + openbox khi thiếu (apt/apk/dnf, cần root hoặc sudo), VNC chỉ nghe `127.0.0.1:5900`. `viewer_url = https://<tên>.<domain>/.boxmes/desktop?ticket=…[&view_only=1]` là trang noVNC vendored do core phục vụ (`/.boxmes/novnc/…`, CSP chặt, `frame-ancestors` = origin dashboard), `vnc_url = wss://…/.boxmes/vnc?ticket=…` (RFB qua frame nhị phân). Ticket `vnc` dùng **một lần** trong 60 giây. `view_only`: core lọc bỏ phím / chuột / clipboard của người xem ngay trong luồng RFB (không tin client). Tự tắt sau `BOXMES_DESKTOP_IDLE` (15 phút) không người xem.
- **Link chia sẻ.** `POST /v1/boxes/{id}/desktop/shares {ttl_s (60…86400, mặc định 3600), view_only (mặc định true)}` → `{id: "shr_…", url: "https://<tên>.<domain>/.boxmes/desktop?share=bxs_…", prefix, view_only, expires_at, …}` (URL chỉ trả một lần). Dùng lại được tới hạn, nhúng iframe ở bất kỳ đâu (`frame-ancestors *`). `GET …/desktop/shares` → `{items: [{id, prefix, view_only, expires_at, created_at, revoked_at?, viewers, uses}]}`; `DELETE …/desktop/shares/{id|all}` → `{revoked, viewers_closed}`: người đang xem bị ngắt ngay, link trả **410**.
- **Tóm tắt.** `GET /v1/boxes/{id}/access` thêm `cdp {url, core_url, start, running, clients}`, `desktop {url, core_url, page, start, running, clients}` và lệnh `ssh_cert`, `sftp`, `scp`, `browser`, `desktop`, `desktop_share`.
- **MCP / SDK.** 32 tool: thêm `box_browser` (start / status / stop, trả `cdp_url`) và `box_desktop` (start / share / shares / revoke / stop); `box_ssh_info` ký chứng chỉ khi có `public_key`. SDK Python (sync/async) + TypeScript: `ssh_cert`, `browser`, `cdp_url`, `connect_playwright`, `browser_status`, `stop_browser`, `desktop`, `desktop_status`, `stop_desktop`, `share_desktop`, `desktop_shares`, `revoke_desktop_shares` (TS camelCase). `llms.txt` có mục "Box access P1" (vi/en/zh/ja), A2A `x-boxmes.access` + `access_detail`.
- **Dashboard.** Tab **Màn hình**: lấy ticket viewer mới qua proxy Next (`POST /api/boxmes/boxes/{id}/desktop`, API key chỉ ở server) rồi nhúng trang noVNC của core bằng iframe; bật / tắt desktop, chế độ **Điều khiển / Chỉ xem**, kết nối lại (ticket mới), toàn màn hình, mở tab riêng; thẻ **Link chia sẻ** (tạo với TTL 15 phút / 1 / 4 / 24 giờ, chỉ xem hoặc cho điều khiển, copy URL một lần, danh sách, thu hồi từng link / tất cả). Tab **Trình duyệt**: trạng thái, bật / lấy URL CDP (TTL, luôn headless), copy URL ws / http / core, snippet Playwright (Node / Python), puppeteer, SDK, curl, CLI; tắt. Tab **Truy cập** thêm thẻ **Chứng chỉ SSH ngắn hạn** (dán key hoặc chọn key đã lưu, 1 / 4 / 12 giờ) và **SFTP / scp**. Mock `BOXMES_MOCK=1` có đủ route mới (không có VNC / CDP thật: màn hình mô phỏng + ghi chú); 4 ngôn ngữ. Iframe cần `BOXMES_DASHBOARD_URL` đúng origin dashboard để `frame-ancestors` cho phép.

**Bảo mật.**
- Không API key trong URL: CDP / VNC / trang desktop chỉ nhận ticket (`cdp` dùng lại tới hạn, `vnc` một lần 60 s) hoặc header; link chia sẻ là token `bxs_` riêng, chỉ lưu tiền tố + hash, thu hồi được, mặc định chỉ xem.
- Origin của control plane được siết: domain gateway trần không phải core, `Set-Cookie` có `Domain` từ box bị bỏ, origin là host của box không bao giờ mở được socket truy cập (một app trong box không thể dùng trình duyệt người xem để mở terminal / CDP của box khác).
- Chặn metadata cloud: egress của box chặn `169.254.169.254` (và metadata GCP / Azure / Alibaba…) theo mặc định (`BOXMES_EGRESS_ALLOW_METADATA=1` để mở); Chromium chạy với `--host-resolver-rules` map các host metadata về NOTFOUND và core viết lại `Page.navigate` / `Target.createTarget` tới địa chỉ metadata.
- Chứng chỉ SSH: ≤ 12 giờ, principals chỉ là box đó, extension chỉ `permit-pty` + `permit-port-forwarding`, thu hồi như ticket; private key của CA được lưu dạng sealed trong DB.
- View-only được thực thi ở core (lọc message RFB client → server), không phụ thuộc noVNC.

**Kết quả live** (2026-10-27/28, `scripts/live-access-p1.sh`, qua `boxs.tech` + Cloudflare Tunnel):

| | E2B | Prized |
|---|---|---|
| `box sftp put` / `get` 1 MB | 2.9 s / 0.84 s | 2.7 s / 0.99 s |
| OpenSSH `sftp` / `scp` | ~0.9 s | ~0.9 s |
| SSH bằng chứng chỉ (`--anon`, không API key) | 335 ms | 320 ms |
| Trình duyệt: lần đầu (gồm cài) / warm | 17.4 s / 215–464 ms | chưa chạy (thiếu thư viện, không root) |
| CDP connect / `Browser.getVersion` | 222 ms / 35 ms | — |
| Desktop: lần đầu / warm | 6.6 s / 324 ms | không khả thi (cần root để cài Xvfb) |
| VNC handshake / framebuffer update đầu | 316–452 ms / 83–101 ms | — |
| Thu hồi link chia sẻ → 410 | 470 ms | — |

Retest 2026-10-28 (bản sửa): E2B 512 MB với cờ tiết kiệm bộ nhớ — Chromium 156 cài trong 18.4 s, `Browser.getVersion` 59 ms, navigate example.com → title "Example Domain" 408 ms, metadata bị chặn (`ERR_NAME_NOT_RESOLVED`), warm 256 ms. Prized (Ubuntu 24.04, không root / sudo, image không có apt lists): tải `.deb` thư viện vào chỗ của user giờ chạy được (index apt riêng, không cần root) nhưng Chromium vẫn thiếu thư viện → trình duyệt **chưa chạy được trên Prized**; desktop cũng không (Xvfb / x11vnc cần root để cài). Cách tiếp theo: tải đệ quy dependency (`apt-cache depends --recurse`) hoặc dùng image có sẵn chromium / xvfb.

Chạy lại: `ENV_FILE=…/gateway.env PROVIDERS="e2b prized" scripts/live-access-p1.sh`. Test không cần mạng: `internal/e2e/v111_test.go` (sftp qua core, cert trên WS + listener trực tiếp + thu hồi, CDP `/json` rewrite + `Browser.getVersion` + viết lại metadata + thu hồi, RFB view-only + shared flag, share link 200 → 410 + ngắt viewer), `scripts/e2e.sh` (bước v1.11, `scripts/accessprobe.py`), web `scripts/smoke.sh` (mock: browser, desktop, shares, ssh certs, access, chuỗi 4 ngôn ngữ, bundle).

## 18. v1.12 — provider Render (chỉ dial-in)

Render không có API exec/file cho service, nên **mỗi box là một web service Render** chạy image có sẵn (mặc định `docker.io/library/buildpack-deps:bookworm-scm`: bash, curl, git, ssh client). Lệnh khởi động của service tải boxd từ core (`<BOXMES_PUBLIC_URL>/dl/boxd-linux-<arch>`, cần `BOXMES_DIST_DIR`) rồi chạy `boxd --dial` ở foreground. Mọi thứ trong box (exec, file, terminal, forward TCP, SSH, snapshot tarball) đều đi qua boxd như host T2.

**Vì sao T2 "chỉ dial".** Không có API exec nên không làm được shim T3; boxd tự gọi ra core như mọi host. Hệ quả: core **bắt buộc** có `BOXMES_PUBLIC_URL` truy cập được từ Internet. Thiếu nó, provider hiện `blocked: needs BOXMES_PUBLIC_URL` và `/recommend` không chọn Render. `sandbox_mode=shim` bị bỏ qua (`mode` luôn là `dial`).

**Cách chạy trong service.**
- Biến môi trường gửi lên Render (tên trung tính): `MGR_BOOT` (lệnh bootstrap, chứa token bootstrap một host), `BOXD_LISTEN_PORT=${PORT}`, `BOXD_HEALTH_PATH=/.well-known/mgr-health`, `BOXD_PREVIEW_PORT=8000`, và với gói free `BOXD_KEEPALIVE_URL=${RENDER_EXTERNAL_URL}/.well-known/mgr-health`. Docker command: `/bin/sh -c eval "$MGR_BOOT"` (theo tài liệu Render, phần sau `-c` là một đối số; **chưa kiểm chứng live**). Tên service `mgr-<host>`, User-Agent `sandbox-client/1`; không có chữ Boxa/Boxmes.
- boxd giữ cổng `$PORT` (Render chỉ coi deploy là live khi có cổng mở): trả `ok` ở health path, các request khác (cả WebSocket) chuyển tới cổng 8000 trong box. Vì vậy `https://<service>.onrender.com/` là URL preview công khai của cổng 8000 (`kind: provider-preview`, `provider_preview: public` — ai biết URL cũng vào được, token Boxmes chỉ bảo vệ URL gateway). Cổng khác đi qua gateway Boxmes. Tắt hẳn bằng trường credential `preview_port=0`.
- Gói free tự ngủ sau 15 phút không có traffic **vào**; WebSocket gọi ra của boxd không tính. boxd gọi URL công khai của chính nó mỗi 5 phút (`keep_awake=1` mặc định) để service không bị spin down khi box còn sống. Việc này tiêu giờ free (750 giờ/tháng mỗi workspace, ~1 box chạy 24/7).
- Render có thể khởi động lại instance với filesystem mới (spin down, bảo trì, gói free "có thể restart bất kỳ lúc nào"). Token bootstrap của host Render vẫn hợp lệ cho đúng host đó (`ephemeral_restart`), nên boxd vào lại được; box mất file sẽ bị đánh `failed` ("box missing on host after reconnect"). Gói trả phí có thể gắn disk (`disk_gb`, $0.25/GB-tháng, mount ở `/srv/w`) để giữ file qua restart.

**Capability.** `pause/snapshot/fork/files: false`, `ports: true`, isolation `container` (bị loại khi `untrusted=true`). Sleep = boxd dừng tiến trình của box, service vẫn chạy (gói trả phí vẫn tính tiền); box `persistent` vẫn được chọn vì service không giới hạn thời gian sống. Snapshot là tarball boxd (lên S3/R2 nếu có object storage). Không có fork/clone gốc. Log, custom domain, one-off job, SSH của Render chưa dùng (SSH/job cần gói trả phí; terminal/SSH của Boxmes đã đi qua boxd).

**Giá** (render.com/pricing, tính theo giây, giá tháng ÷ 730):

| SKU | CPU / RAM | Giá |
|---|---|---|
| `free` | 0.1 / 512 MB | $0 — 750 giờ/tháng/workspace, spin down sau 15 phút, khởi động lạnh ~1 phút, mất file khi spin down |
| `0.5c-512mb` | 0.5 / 512 MB | $7/tháng ≈ $0.0096/giờ |
| `1c-2g` | 1 / 2 GB | $25/tháng ≈ $0.0342/giờ |
| `2c-4g` | 2 / 4 GB | $85/tháng ≈ $0.1164/giờ |
| `2c-8g`, `4c-8g`, `4c-16g`, `8c-16g`, `8c-32g` | | $135 / $175 / $225 / $300 / $450 mỗi tháng |

Vùng: oregon (mặc định), ohio, virginia, frankfurt, singapore. Kích thước ổ tạm (ephemeral) Render không công bố; catalog giả định 20 GB (ước tính). Create → box dùng được: kéo image + deploy, ước tính 1–3 phút (chưa đo live).

**Kết nối.** `RENDER_API_KEY` (Account settings → API keys), tuỳ chọn `RENDER_OWNER_ID` (mặc định workspace đầu tiên của key, lấy qua `GET /owners`), `RENDER_AGENT_IMAGE` (image công khai có `/bin/sh` + curl/wget), `RENDER_DISK_GB`. Credential JSON thêm `preview_port`, `keep_awake`. Gói trả phí cần thẻ trong workspace (API trả 402 → gợi ý thêm phương thức thanh toán); gói free theo tài liệu không cần thẻ.

**Kiểm thử.** `sandbox/render_test.go` (httptest API Render: owners, tạo service với body đầy đủ, trạng thái deploy, suspend/resume, list chỉ lấy `mgr-*`, xoá, 402; lệnh khởi động chạy thật bằng `/bin/sh -c eval "$MGR_BOOT"` với boxd giả), `boxd/portholder_test.go` (health, 502 khi chưa có app, forward, keepalive), `e2e/render_test.go` (driver Render thật + API giả chạy boxd trong tiến trình: box up, exec, URL onrender.com, restart filesystem mới → vào lại bằng cùng token và box bị `failed`, box down xoá service, không lộ tên sản phẩm), `scripts/e2e.sh` mục render (dial-only, blocked khi thiếu `BOXMES_PUBLIC_URL`, giá gói, kiểm tra key qua `GET /owners`). Chưa chạy live (chưa có API key).


## 19. Hosting core + web trên Render (gói free)

Core (`mgr-gw`) và dashboard (`mgr-web`) chạy trên Render, workspace "Caca's workspace", vùng **singapore**, gói **free** ($0). Tên thấy được phía Render đều trung tính (`mgr-*`).

| Service | Nguồn | URL |
|---|---|---|
| `mgr-gw` (core) | Docker, context `core/`, `core/deploy/render/Dockerfile`, health `/v1/health` | https://mgr-gw.onrender.com · qua tunnel: https://gw.boxs.tech, `https://<box>.boxs.tech` |
| `mgr-web` (Next.js) | Docker, context `web/`, `web/Dockerfile`, health `/` | https://mgr-web-x2v1.onrender.com |

**Core.** `start.sh` tạo thư mục dữ liệu (`BOXMES_DATA_DIR`), chạy `cloudflared tunnel run` (tự khởi động lại) khi có `TUNNEL_TOKEN`, rồi chạy core bằng user `app` (core không thấy `TUNNEL_TOKEN`). Named tunnel `boxaz-gw` trỏ `gw.boxs.tech` và `*.boxs.tech` về `http://localhost:8091`; TLS wildcard vẫn ở Cloudflare. Chỉ một nơi được chạy connector của tunnel này: khi core chạy trên Render thì gateway trong workspace phải tắt.

Biến môi trường chính (giá trị bí mật chỉ đặt trong Render, không commit): `BOXMES_PUBLIC_URL=https://gw.boxs.tech`, `BOXMES_GATEWAY_DOMAIN`, `BOXMES_DATA_DIR=/tmp/data`, `BOXMES_ADMIN_KEY`, `BOXMES_MASTER_KEY`, `BOXMES_DASHBOARD_URL` và `BOXMES_ACCESS_ORIGINS` = URL của `mgr-web` (cho terminal/SSH WebSocket), `TUNNEL_TOKEN`, `KEEPALIVE_S=600`, và key thật của provider (`E2B_API_KEY`, `NOVITA_API_KEY`, `PRIZED_API_KEY`, …).

**Web.** `BOXMES_API` và `NEXT_PUBLIC_BOXMES_API` = `https://gw.boxs.tech/v1` (Render truyền biến môi trường thành build arg của Docker nên `NEXT_PUBLIC_*` được nhúng lúc build), `BOXMES_MOCK=0`. `pnpm-workspace.yaml` liệt kê rõ `allowBuilds` (cả các gói bị từ chối) vì pnpm 11+ báo `ERR_PNPM_IGNORED_BUILDS` khi `pnpm install --frozen-lockfile`.

**Giới hạn của gói free.**
- **Không có ổ đĩa.** Dữ liệu nằm ở `/tmp/data` (SQLite, agent, key, box, secret, sổ chi phí). **Mỗi lần deploy hoặc restart (kể cả spin down, Render bảo trì) là mất hết trạng thái**: chỉ còn admin key và master key vì chúng nằm trong biến môi trường. Box đang chạy ở provider sẽ không còn được core quản lý (phải xoá tay ở provider) và key agent phải tạo lại.
- Spin down sau 15 phút không có request **vào** service; traffic qua tunnel không tính. `start.sh` tự gọi `$RENDER_EXTERNAL_URL/v1/health` mỗi `KEEPALIVE_S` giây (600 = 10 phút; `0` để tắt). Gói free có 750 giờ/tháng cho mỗi workspace, chia cho mọi service free (2 service chạy liên tục ≈ 1460 giờ, nên `mgr-web` sẽ ngủ khi không dùng, khởi động lạnh ~1 phút).
- 512 MB RAM, 0.1 CPU.

**Nâng cấp lên trạng thái bền.** Thêm thẻ ở dashboard.render.com/billing, chuyển `mgr-gw` sang gói `starter` (hoặc lớn hơn), gắn disk (vd `mgr-data`, mount `/data`, 1 GB), đặt `BOXMES_DATA_DIR=/data` và `KEEPALIVE_S=0`, rồi deploy lại. `start.sh` tự `chown` disk cho user `app`.

**Thời gian đo (2026-11-19).** `mgr-gw`: tạo service → live 146 s (build Docker lần đầu), `/v1/health` 200 trong 0.25 s; deploy lại sau khi thêm `TUNNEL_TOKEN` → live 31 s, `https://gw.boxs.tech/v1/health` 200 trong 0.62 s (tunnel healthy, 4 kết nối sin*). `mgr-web`: build + deploy → live 159 s, `/login` 200 0.29 s, đăng nhập bằng admin key và `/api/boxmes/{health,overview,boxes,providers}` qua `gw.boxs.tech` đều 200.

## 20. v2 tenancy — tài khoản, phiên đăng nhập, mã mời, danh sách chờ

Lớp store (bảng `users`, `workspaces`, `memberships`, `sessions`, `invite_codes`, `waitlist`, `password_tokens`; cột `workspace_id` trên agents/boxes/api_keys/secrets, dữ liệu cũ dồn vào workspace `ws_default`) đã có từ commit trước. Bước này thêm lớp xác thực:

- **Đăng ký** `POST /v1/auth/signup {email, password, name?, invite_code?, workspace?}`. Tài khoản **đầu tiên** không cần mã, thành quản trị nền tảng (`platform_admin`) và làm owner `ws_default`. Người sau được một workspace riêng (5 box, TTL ≤ 12 h) với vai trò owner.
  - `BOXMES_SIGNUP_MODE=invite` (mặc định): cần mã mời; `open`: ai cũng đăng ký được; `closed`: chỉ người đầu tiên, còn lại `POLICY_DENIED` + `next: POST /v1/waitlist`.
  - Mã mời dùng nguyên tử (`uses < max_uses`, chưa hết hạn, mỗi user một lần); email được xoá khỏi danh sách chờ khi đăng ký.
- **Đăng nhập** `POST /v1/auth/login` → token phiên `bxs_…` (hash SHA-256 trong DB, mặc định 30 ngày, `BOXMES_SESSION_TTL`). Mật khẩu bcrypt, 8–72 byte; sai 10 lần / 15 phút cho một email → `RATE_LIMITED`. Token dùng như API key: `Authorization: Bearer`, `?token=` hoặc cookie `boxmes_key` của dashboard.
- `POST /v1/auth/logout[?all=1]`, `GET|PATCH /v1/auth/me`, `POST /v1/auth/password` (đổi mật khẩu, đăng xuất các phiên khác), `POST /v1/auth/workspace {workspace_id}` (đổi workspace hiện tại), `GET /v1/workspaces`.
- **Email**: `POST /v1/auth/verify-email {token}`, `POST /v1/auth/verify-email/resend`, `POST /v1/auth/password/forgot {email}` (luôn 202), `POST /v1/auth/password/reset {token, password}` (đăng xuất mọi phiên, coi email là đã xác thực). Link dùng `BOXMES_DASHBOARD_URL` (hoặc `BOXMES_PUBLIC_URL`): `/verify-email?token=`, `/reset-password?token=`, `/signup?invite=`. Gửi qua SMTP trong `settings.notify`; chưa cấu hình SMTP thì nội dung thư được ghi vào log để người vận hành tự copy link.
- **Danh sách chờ** `POST /v1/waitlist {email, note?}` → vị trí.
- **Admin nền tảng** (admin key hoặc phiên của `platform_admin`): `GET /v1/admin/users` (kèm workspace + vai trò — thay cho dạng v1 `{admin_keys, note}`), `PATCH|DELETE /v1/admin/users/{id}` (không bỏ được admin cuối cùng), `GET|POST /v1/admin/invites`, `DELETE /v1/admin/invites/{id|code}`, `GET /v1/admin/waitlist`, `POST /v1/admin/waitlist/invite {emails[] | count, expires_in_hours?}` (gửi mã dùng 1 lần, mặc định hạn 14 ngày), `DELETE /v1/admin/waitlist/{email}`.
- **Giới hạn tạm thời**: phiên của thành viên không phải admin nền tảng chỉ gọi được `/v1/auth/*` và `/v1/workspaces` (kể cả `/mcp`, `/a2a` đều bị chặn với `UNAUTHORIZED`) cho tới khi boxes/agents/keys/secrets được lọc theo workspace — bước kế tiếp.
- Test: `internal/e2e/tenancy_test.go` (luồng đầy đủ + 3 chế độ đăng ký).

Còn lại của v2: lọc mọi handler theo `workspace_id` + áp `max_boxes`/`max_ttl_hours` của workspace; mời thành viên vào workspace (owner/member); trang đăng ký/đăng nhập/quên mật khẩu/danh sách chờ trên web và chuyển dashboard từ "dán admin key" sang phiên; trang admin quản lý mã mời và danh sách chờ.

## 21. Đổi tên Boxaz → Boxmes

Sản phẩm đổi tên lần hai thành **Boxmes**: binary `boxmes-core` (trước là `boxaz-core`, ban đầu `boxa-core`), biến môi trường `BOXMES_*`, systemd `boxmes-core.service`, module Go `boxmes/core`, SDK `boxmes` (Python) / `Boxmes`, `@boxmes/sdk` (TypeScript), cookie dashboard `boxmes_key` / `boxmes_locale`, thư mục CLI `~/.boxmes`. API (đường dẫn, dạng JSON) không đổi. Lớp tương thích giữ cấu hình của cả hai đời tên cũ chạy được:

- **Biến môi trường**: `core/internal/envcompat` đọc theo thứ tự `BOXMES_*` → `BOXAZ_*` → `BOXA_*` (tên mới nhất luôn thắng). Lúc khởi động `boxmes-core`, `boxd`, `box` in một dòng cảnh báo: `deprecated env vars honoured, rename them to BOXMES_*: …` cho biến cũ được dùng và `legacy env vars ignored because a newer name is set: …` cho biến cũ bị tên mới hơn che. Dashboard (`next.config.ts`, cả `NEXT_PUBLIC_BOXAZ_*` / `NEXT_PUBLIC_BOXA_*`) và hai SDK theo cùng thứ tự.
- **Dữ liệu cũ**: `<data>/boxaz.db` rồi `boxa.db` được dùng nếu chưa có `boxmes.db`; container `boxaz-*` / `boxa-*`, file `.boxaz.json` / `.boxa.json` của driver local, secret hệ thống `BOXAZ_SYS_*` / `BOXA_SYS_*`, thư mục `~/.boxaz` của CLI vẫn được nhận.
- **Web / gateway**: cookie `boxaz_key` / `boxa_key` và `boxaz_locale` / `boxa_locale` vẫn được đọc (đăng xuất xoá cả cookie cũ); tham số ingress `boxaz_token` / `boxa_token`, cookie `boxaz_t` / `boxa_t` / `boxaz_ut`, header `X-Boxaz-Token` / `X-Boxaz-No-Wake`, đường dẫn `/.boxaz/…` trên host của box vẫn được chấp nhận.
- **MCP**: URI `boxaz://…` vẫn đọc được; trả lời dùng `boxmes://…`.
- **SDK**: `import boxaz` / `import boxa` (Python, kèm `DeprecationWarning`) và `Boxaz`, `BoxazError`, `Boxa`, `BoxaError`… (TypeScript) là alias deprecated của tên mới.
- Mọi thứ provider nhìn thấy (`mgr-*`, nhãn `managed`/`mgr-host`, User-Agent `sandbox-client/1`) vẫn trung tính như §12.

**Đổi tên KHÔNG có lớp tương thích** (hai đầu phải cùng phiên bản hoặc phải áp lại cấu hình):

- Giao thức wire core ↔ boxd: `boxmes-wire/1` — nâng `boxmes-core` và `boxd` cùng lúc.
- Mesh: tag `tag:boxmes-host` / `tag:boxmes-core`, user headscale `boxmes`, DERP và MagicDNS `boxmes.mesh` — host cũ cần cấp lại pre-auth key / áp lại ACL mesh.
- Egress: ACL Incus `boxmes-eg-*` và bảng nftables `inet boxmes_eg_*` — rule cũ (`boxaz-eg-*` / `boxaz_eg_*`) không được dọn hay nhận ra; áp lại policy egress cho box đang chạy (và xoá bảng cũ bằng tay nếu còn).
- SSH: user `boxmes` (`boxmes@<box>`) và host key alias `boxmes-ssh` — chạy lại `box ssh-config --write` và cập nhật `known_hosts` (`~/.boxmes/known_hosts`).

Không đổi: domain `boxs.tech` và repo `huynhlongdai/Boxa`.

## 22. v2.0 tenancy — cô lập workspace, gói & hạn mức, vai trò, thành viên, key workspace, vị trí box

Commit `213c624` hoàn tất phần "còn lại của v2" ở §20: mọi handler đều lọc theo `workspace_id`, nên phiên của thành viên thường gọi được **toàn bộ** API (kể cả `/mcp`, `/a2a`) với quyền theo vai trò — giới hạn tạm thời ở §20 không còn. Test: `internal/e2e/v200_tenancy_test.go` (cô lập, vai trò + lời mời, hạn mức, vị trí, đăng ký tranh chấp).

**Mô hình.**

- Mỗi agent, box, key, secret, template, approval, sự kiện và dòng ledger thuộc đúng một workspace. Dữ liệu trước v2.0 nằm trong `ws_default` (workspace của người vận hành, gói `unlimited`).
- **Người gọi nền tảng** (admin key, phiên của user `platform_admin`) thấy mọi workspace; `?workspace=<id>` thu hẹp phần đọc, còn bản ghi mới rơi vào workspace đó (không có thì workspace hiện tại của phiên, cuối cùng là `ws_default`).
- **Người gọi workspace** (phiên của thành viên, key workspace) chỉ thấy workspace của mình; `?workspace=` bị bỏ qua. Quyền theo vai trò (phiên) hoặc scope (key).
- **Agent key** chỉ thấy bản ghi của agent mình trong workspace của agent đó (như trước v2.0).
- Một user làm owner tối đa 10 workspace. `POST /v1/workspaces` không đổi workspace hiện tại của phiên; đổi bằng `POST /v1/auth/workspace`.

**Vai trò** (vai trò `member` cũ được đọc là `developer`):

| Quyền | owner | admin | developer | billing | viewer | auditor |
|---|:-:|:-:|:-:|:-:|:-:|:-:|
| `view` — xem box, agent, chi phí, sự kiện, approval, template | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| `boxes:write` — tạo/xoá/ngủ/đánh thức/snapshot/fork box, port, app | ✓ | ✓ | ✓ | | | |
| `exec` — exec, job/log, file, terminal, ssh, trình duyệt, desktop | ✓ | ✓ | ✓ | | | |
| `agents:write` — agent và key (developer: chỉ cái mình tạo) | ✓ | ✓ | ✓ | | | |
| `secrets:read` (tên) / `secrets:use` (gắn vào box) | ✓ | ✓ | ✓ | | đọc | đọc |
| `secrets:write` — tạo/xoay/xoá secret | ✓ | ✓ | | | | |
| `templates:write` | ✓ | ✓ | ✓ | | | |
| `billing:read` — gói, usage | ✓ | | | ✓ | | |
| `members:write` — thành viên, lời mời | ✓ | ✓ | | | | |
| `audit:read` — nhật ký workspace | ✓ | ✓ | | | | ✓ |
| `emergency_stop`, `workspace:write` (đổi tên), `approvals:decide` | ✓ | ✓ | | | | |

Chỉ owner mới cấp / đổi / gỡ vai trò owner; không hạ cấp hay gỡ được owner cuối cùng. Thành viên tự rời workspace bằng `DELETE …/members/{user_id của mình}`.

**Gói & hạn mức** (`model.Plans`; 0 = không giới hạn):

| Hạn mức | free | pro | team |
|---|---|---|---|
| Box chạy đồng thời (`max_boxes`, không tính box đang ngủ) | 1 | 10 | 50 |
| vCPU / box | 1 | 8 | 16 |
| RAM / box | 2 GB | 16 GB | 32 GB |
| TTL tối đa | 2 h | 24 h | 168 h |
| URL công khai (`public_url`) | không | có | có |
| Egress | restricted | open | open |
| Thành viên (tính cả lời mời đang chờ) | 1 | 3 | không giới hạn |
| Agent (không tính agent ngầm `operator`) | 3 | 25 | không giới hạn |
| Key đang hiệu lực (key workspace + agent) | 5 | 50 | không giới hạn |
| Secret | 10 | 100 | không giới hạn |
| Box tạo mới / phút | 5 | 30 | 120 |

- Gói `unlimited` (không giới hạn gì) dành cho `ws_default`. Workspace mới nhận gói mặc định của instance `BOXMES_DEFAULT_PLAN` (`free` nếu không đặt / sai); workspace có `plan` rỗng cũng theo giá trị này.
- Chỉ admin nền tảng đổi `plan`, `quota_overrides` (ghi đè từng hạn mức) và `status` (`active` | `suspended`) qua `PATCH /v1/admin/workspaces/{id}`. Workspace `suspended` chỉ còn đọc + endpoint tài khoản (ghi → 403 `POLICY_DENIED`).
- Kiểm tra lúc tạo / fork / clone / restore box (CPU, RAM, TTL, số box chạy, tốc độ tạo), lúc đánh thức (lại chiếm một slot), lúc thêm agent / key / secret / lời mời. Tạo box không xin TTL thì TTL bị đặt bằng mức trần của gói.
- `public_url: false`: mọi port mới mặc định truy cập bằng URL token; đặt `access: public` → `QUOTA_EXCEEDED`.
- `egress: restricted`: box không có chính sách egress được gán allowlist của gói (registry gói, git host, mirror OS — `model.EgressRestrictedAllow`, nguồn `plan`, chặn mặc định); allowlist tự đặt phải nằm trong danh sách đó, không thì `QUOTA_EXCEEDED` với `details.host`.
- Vượt hạn mức → **403 `QUOTA_EXCEEDED`**, `details: {quota, limit, used?, plan, upgrade_url}` (`upgrade_url` = `<BOXMES_DASHBOARD_URL>/settings/billing`), `next: ["GET /v1/workspaces/current/usage", "GET /v1/plans"]`; vượt tốc độ tạo box có thêm `retry_after_s: 60`.

**Endpoint** (đều dưới `/v1`):

- Không cần xác thực: `GET /plans` (danh sách gói + hạn mức), `GET /workspace-invites/{token}` (xem trước lời mời: workspace, email, vai trò, người mời, hạn, `status`, `account_exists`).
- Phiên: `GET /auth/me` (thêm `role`, `permissions`, `platform_admin`), `POST /auth/workspace {workspace_id}`, `GET /workspaces`, `POST /workspaces {name}` → 201, `POST /workspace-invites/{token}/accept` (đúng email được mời; workspace đó thành workspace hiện tại). Người chưa có tài khoản đăng ký bằng `POST /auth/signup {…, workspace_invite: "bxi_…"}` (không cần mã mời, không tạo workspace riêng).
- Workspace hiện tại: `GET|PATCH /workspaces/current` (đổi tên; `plan`/`quota_overrides`/`status` chỉ admin nền tảng), `GET /workspaces/current/usage`, `GET /workspaces/current/audit?actor=&type=&box=&agent=&limit=`, `GET /workspaces/current/members`, `PATCH|DELETE /workspaces/current/members/{user_id}`, `GET|POST /workspaces/current/invites`, `DELETE /workspaces/current/invites/{id}`. Lời mời sống 7 ngày, token `bxi_…` chỉ trả một lần trong `invite_url` (`<dashboard>/invite/<token>`), mời lại cùng email thay link cũ.
- Key workspace: `POST /keys {name, scopes?, expires_in_days? | expires_at?}` (không có `agent_id`) → key `bx_ws_…`. Scope: `boxes:read`, `boxes:write`, `exec`, `secrets:read`, `secrets:write`, `billing:read`, `admin` (mọi quyền trong workspace); mặc định `boxes:read` + `boxes:write` + `exec`. Key không bao giờ có nhiều quyền hơn người tạo. Key hết hạn → 401.
- Vị trí box: `Box.location {region, country?, city?, provider, site, host?}` suy từ placement (region `eu | us | apac | sandbox | local | other`, site `<provider>-<mã>`, vd `hetzner-fsn`). `GET /boxes?region=&location=&provider=` lọc theo đó; `GET /boxes/locations` → `{items[] (theo site: running, sleeping, total, cost_today_usd), regions[], total}` cho bản đồ dashboard.
- Admin nền tảng: `GET /admin/workspaces` (kèm `plan`, `members`, `boxes`, `spent_month_usd`), `PATCH /admin/workspaces/{id}`.

**Ngữ nghĩa lỗi.**

- Bản ghi của workspace khác → **404 `NOT_FOUND`** (không bao giờ 403, để không lộ sự tồn tại); đổi sang workspace mình không là thành viên cũng 404.
- Phiên có vai trò không đủ quyền → **403 `POLICY_DENIED`** (hint: nhờ owner/admin đổi vai trò); chưa thuộc workspace nào → 403 kèm gợi ý `POST /v1/workspaces`.
- Key workspace thiếu scope → **401 `UNAUTHORIZED`** (`key lacks scope "…"`); thiếu/sai/hết hạn key → 401. Route `/v1/admin/*` với người gọi không phải nền tảng → 401.
- Vượt hạn mức gói → 403 `QUOTA_EXCEEDED` (trên).

**Hạn chế đã biết.**

- Bộ đếm "box tạo mới / phút" nằm trong bộ nhớ của tiến trình core: khởi động lại thì về 0, chạy nhiều core thì mỗi core đếm riêng.
- Egress của gói chỉ được kiểm khi tạo box và khi `PUT /boxes/{id}/egress`; hạ gói xuống `free` (hoặc ghi đè `egress: restricted`) không áp lại cho box đang chạy.
- Chưa chuyển được agent / box giữa các workspace.
- Giá gói và thanh toán chưa có (v2.1); `GET /v1/plans` chỉ trả hạn mức.

SDK: Python `me()`, `switch_workspace()`, `list_workspaces()`, `create_workspace()`, `get_workspace()`, `rename_workspace()`, `workspace_usage()`, `workspace_audit()`, `plans()`, `list_members()`, `update_member_role()`, `remove_member()`, `create_invite()`, `list_invites()`, `revoke_invite()`, `get_invite()`, `accept_invite()`, `create_key(…, scopes, expires_in_days=…)`, `list_keys()`, `revoke_key()`, `list_boxes(region=, location=)`, `box_locations()`, `admin_workspaces()`, `admin_update_workspace()`, `BoxmesError.details`; TypeScript cùng bộ, tên camelCase.
