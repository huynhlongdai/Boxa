# Boxaz core — backend (Go)

Boxaz là hạ tầng cloud cá nhân: cấp **box** (sandbox Linux) cho AI agent trên VPS rẻ của nhiều nhà cung cấp, chọn chỗ rẻ nhất đạt yêu cầu, tự ngủ/dọn dẹp và giữ ngân sách. Thư mục này chứa toàn bộ backend: `boxaz-core` (control plane), `boxd` (host agent), `box` (CLI + MCP stdio). Dashboard nằm ở `../web` và gọi đúng hợp đồng `docs/api-contract-v1.md`.

## 1. Kiến trúc

```
 Agent (Claude Code, OpenAI Agents, n8n, Hark…)
   │  MCP (HTTP /mcp hoặc stdio `box mcp`) · REST /v1 · CLI `box`
   ▼
 ┌──────────────────────── boxaz-core ────────────────────────┐
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
- **Bootstrap token**: host mới (cloud-init) nhận một token dùng một lần. Lần kết nối đầu, core đổi nó lấy host token riêng (lưu hash) và boxd ghi vào `host.json`. Riêng máy tự đăng ký (install.sh/dev) thì dùng `BOXAZ_BOOTSTRAP_TOKEN` chung.
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
- **Không tự thuê máy** trừ khi đặt `BOXAZ_AUTO_BURST=true`. Mặc định, khi hết chỗ core trả lỗi `NO_CAPACITY` và tạo mục duyệt `pool_expand` kèm offer rẻ nhất.
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
cmd/boxaz-core   server (env → manager + api)
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
BOXAZ_DATA_DIR=./data BOXAZ_ADMIN_KEY=bx_admin_dev BOXAZ_BOOTSTRAP_TOKEN=dev-boot ./bin/boxaz-core

# Terminal 2 — boxd với driver local (mỗi box là 1 thư mục, KHÔNG cô lập — chỉ để dev)
BOXD_CORE_URL=http://127.0.0.1:8080 BOXD_TOKEN=dev-boot BOXD_DRIVER=local BOXD_STATE_DIR=./boxd-state ./bin/boxd

# Terminal 3 — tạo agent + key rồi dùng CLI
export BOXAZ_URL=http://127.0.0.1:8080
curl -s -XPOST $BOXAZ_URL/v1/agents -H "Authorization: Bearer bx_admin_dev" -d '{"name":"coder","kind":"claude-code","budget_day_usd":2}'
export BOXAZ_API_KEY=$(curl -s -XPOST $BOXAZ_URL/v1/keys -H "Authorization: Bearer bx_admin_dev" -d '{"agent_id":"coder"}' | jq -r .key)
./bin/box up --template python --lifecycle session --slug demo
./bin/box exec demo -- 'echo hi && ls'
echo "print(1)" | ./bin/box write demo /work/a.py
./bin/box read demo /work/a.py
./bin/box exec demo --async -- 'sleep 5; echo xong'      # → job_id
./bin/box sleep demo && ./bin/box wake demo && ./bin/box snapshot demo
./bin/box cost && ./bin/box down demo
```

Nếu không đặt `BOXAZ_ADMIN_KEY`, lần chạy đầu core tự sinh admin key và **in ra stdout đúng một lần**.

Kết nối agent qua MCP:

```bash
# Claude Code — HTTP
claude mcp add --transport http boxaz http://127.0.0.1:8080/mcp --header "Authorization: Bearer $BOXAZ_API_KEY"
# hoặc stdio
BOXAZ_URL=http://127.0.0.1:8080 BOXAZ_API_KEY=... claude mcp add boxaz -- box mcp
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
2. Trên máy dev: `make dist`, rồi `scp -r dist deploy root@<IP>:/root/boxaz/`.
3. Trên server: `cd /root/boxaz && HOST_PRICE_MONTH=4.59 ACME_EMAIL=anh@email bash deploy/install.sh`. Nếu đã có domain thì thêm `BASE_DOMAIN=example.com`.
   - Script cài `incus` (`incus admin init --minimal`), `caddy`, copy binary, sinh `BOXAZ_MASTER_KEY`, admin key và bootstrap token vào `/etc/boxaz/core.env` và `/etc/boxd.env` (quyền 600), rồi bật `boxaz-core`, `boxd`, `caddy`.
   - Core nghe ở `127.0.0.1:8080`. Caddy cấp HTTPS cho `core-<ip>.sslip.io` (hoặc `core.<BASE_DOMAIN>`) và cấp cert theo yêu cầu cho URL box (`<slug>-<ip>.sslip.io`, hoặc `<slug>.box.<BASE_DOMAIN>`). Mỗi lần cấp cert, Caddy hỏi core qua `/v1/internal/tls-ask`.
   - boxd trên cùng máy nối tới `http://127.0.0.1:8080` và tự đăng ký thành host pool, giá lấy từ `HOST_PRICE_MONTH`.
4. Mở dashboard hoặc API bằng admin key được in ra. Vào **Hạ tầng → Provider → Hetzner**, dán API token (Read & Write); core kiểm tra token rồi mã hoá AES-GCM khi lưu.
5. Thêm host khi cần: `POST /v1/hosts {"provider":"hetzner","sku":"cx22","region":"fsn1"}`. Core tạo server với cloud-init (`deploy/cloud-init.yaml.tmpl`): cài incus, tải `boxd` từ `https://<core>/dl/boxd-linux-<arch>`, rồi dial-out về core bằng bootstrap token dùng một lần. Lệnh này **tốn tiền thật**.

Backup: thư mục `/var/lib/boxaz` (SQLite + master key). Nếu dùng Postgres, đặt `DATABASE_URL` và backup DB hằng ngày.

## 5. Biến môi trường

### boxaz-core

| Biến | Mặc định | Ý nghĩa |
|---|---|---|
| `BOXAZ_ADDR` | `:8080` | địa chỉ lắng nghe (production: `127.0.0.1:8080` sau Caddy) |
| `BOXAZ_DATA_DIR` | `./data` | SQLite + `master.key` |
| `DATABASE_URL` | `<data>/boxaz.db` | `postgres://…` để dùng Postgres (pgx) |
| `BOXAZ_MASTER_KEY` | tự sinh vào `master.key` | khoá mã hoá secrets và token provider |
| `BOXAZ_ADMIN_KEY` | — | admin key cố định; không đặt thì tự sinh và in một lần |
| `BOXAZ_ADMIN_KEY_FILE` | — | ghi admin key tự sinh ra file này |
| `BOXAZ_PUBLIC_URL` | — | URL https của core để boxd/cloud-init gọi về (bắt buộc khi tạo host qua provider) |
| `BASE_DOMAIN` | `sslip.io` | domain cho URL box |
| `BOXAZ_BOOTSTRAP_TOKEN` | — | token chung cho boxd tự đăng ký (install.sh/dev) |
| `BOXAZ_AUTO_BURST` | `false` | cho scheduler tự thuê host theo giờ |
| `BOXAZ_LOCAL_PRICE_MONTH` | `0` | giá/tháng của host tự đăng ký (phân bổ chi phí) |
| `BOXAZ_LEDGER_INTERVAL` | `1m` | chu kỳ ghi ledger |
| `BOXAZ_REAPER_INTERVAL` | `30s` | chu kỳ reaper |
| `BOXAZ_PRICE_FEED` | on | `off` để tắt |
| `BOXAZ_PRICE_FEED_URL` | `https://battleships.dev` | nguồn giá |
| `BOXAZ_PRICE_FEED_PROVIDERS` | `hetzner-cloud,vultr,contabo,netcup,digitalocean` | allowlist |
| `BOXAZ_PRICE_FEED_INTERVAL` | `6h` | chu kỳ cập nhật giá |
| `BOXAZ_DIST_DIR` | — | thư mục phục vụ `/dl/boxd-linux-<arch>` |
| `BOXAZ_DISABLE_TUNNEL` | `false` | `1` = tắt bước cuối của chuỗi URL công khai (cloudflared quick tunnel trong sandbox); tương đương `settings.disable_tunnel` |
| `BOXAZ_INGRESS_ASSUME_PUBLIC` | `false` | `1` = coi `BOXAZ_PUBLIC_URL` là truy cập được từ Internet dù tên/IP trông như nội bộ (vd. sau NAT có port-forward) |
| `BOXAZ_BOXD_URL` | `<public>/dl/boxd` | URL tải boxd cho cloud-init |
| `BOXAZ_SETUP_COST_USD` | `0.002` | chi phí khởi tạo cộng vào báo giá burst |
| `BOXAZ_ORPHAN_AFTER` | `24h` | ngưỡng box mồ côi |
| `BOXAZ_BURST_EMPTY_AFTER` | `10m` | host burst trống thì huỷ |
| `BOXAZ_HOST_DOWN_AFTER` | `2m` | mất tunnel thì đánh dấu down |
| `BOXAZ_CREATE_WAIT` | `120s` | `POST /boxes` chờ tối đa (dùng `?wait=false` để trả ngay) |
| `BOXAZ_CORS_ORIGINS` | `*` | CORS cho dashboard |
| `HETZNER_TOKEN` · `VULTR_API_KEY` · `DIGITALOCEAN_TOKEN` · `NETCUP_REFRESH_TOKEN` | — | v1.2: kết nối provider lúc khởi động (token mã hoá vào DB, `token_source=env`) |
| `CONTABO_CLIENT_ID` · `CONTABO_CLIENT_SECRET` · `CONTABO_API_USER` · `CONTABO_API_PASSWORD` | — | v1.2: thông tin OAuth Contabo (đủ 4 biến mới kết nối) |
| `BOXAZ_PROVIDER_CHAIN` | `hetzner,vultr,digitalocean,contabo,netcup` | v1.2: thứ tự dự phòng burst của scheduler |
| `BOXAZ_S3_PROVIDER` · `BOXAZ_S3_ENDPOINT` · `BOXAZ_S3_REGION` · `BOXAZ_S3_BUCKET` · `BOXAZ_S3_PREFIX` · `BOXAZ_S3_ACCESS_KEY_ID` · `BOXAZ_S3_SECRET_ACCESS_KEY` | — (no prefix; keys under `snapshots/`) | v1.2: lưu snapshot lên R2/B2/S3; đặt bằng env thì dashboard chỉ xem được |

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
| `BOXD_EGRESS_MODE` | `acl` | v1.2 (incus): `acl` = Incus network ACL trên NIC của box, `nft` = bảng nftables `inet boxaz_<box>` trên host |

### box (CLI)

`BOXAZ_URL` (mặc định `http://localhost:8080`), `BOXAZ_API_KEY`.

## 6. Bảo mật

- API key lưu dạng SHA-256. Key admin có scope `admin`; key agent có `box:create, box:exec, box:file, snapshot, secret:use` và chỉ thấy box của agent mình.
- Secrets và token provider được mã hoá AES-256-GCM bằng `BOXAZ_MASTER_KEY`. API chỉ trả tên secret. Giá trị được tiêm làm biến môi trường **ở mỗi lần exec**, không ghi vào cấu hình container.
- Container Incus chạy không đặc quyền (`security.privileged=false`), có giới hạn `limits.cpu`/`limits.memory`/`root size`/`limits.processes`.
- URL box mặc định được bảo vệ bằng token (`?boxaz_token=` → cookie).
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

**Đa ngôn ngữ (vi, en, zh, ja).** Mọi request có thể gửi `Accept-Language` (hoặc `?lang=`); mặc định API là `en`. `code` lỗi không bao giờ đổi, `message` + `hint` được dịch. Catalog nằm ở `internal/i18n/catalog/<locale>.json` (embed): `msg`/`hint` dùng chính chuỗi format tiếng Anh làm khoá (gettext), nên code vẫn viết `model.E(code, "box %s offline", id)`; `text` là các chuỗi có khoá (snippet, llms.txt, prompt MCP, AGENTS.md). Test `TestCatalogComplete` quét toàn bộ mã nguồn và fail nếu có thông báo lỗi chưa dịch hoặc sai verb `%`. Settings có `locale` (mặc định `vi`, dùng làm ngôn ngữ dự phòng cho AGENTS.md); agent có `locale` riêng (`PATCH /agents/:id {"locale":"ja"}`). CLI/stdio MCP đọc `BOXAZ_LANG` (hoặc `LANG`).

**Lỗi cho agent.** Envelope thêm `retry_after_s` (NO_CAPACITY 60, PROVIDER_FAILED 30, RATE_LIMITED theo bucket, INTERNAL 5; kèm header `Retry-After`) và `docs_url` → `GET /v1/errors#CODE` (catalog lỗi đã dịch, không cần auth).

**Khám phá (không cần auth).** `GET /v1/openapi.json` (OpenAPI 3.1; schema sinh bằng reflection từ struct Go, test `TestOpenAPICoversRoutes` đi qua mọi route chi), `GET /llms.txt`, `GET /llms-full.txt` (định dạng llmstxt.org, phần giới thiệu dịch theo ngôn ngữ), `GET /.well-known/mcp.json` (server card), `GET /.well-known/agent.json` (A2A agent card 0.3), `GET /v1/tools?format=openai|anthropic|gemini|mcp`, `GET /v1/agent-kinds`.

**Endpoint mới.**
- `POST /v1/quote` — chạy thử placement + giá cho một spec (cùng body với tạo box hoặc `{yaml}`), không tạo gì, không tạo approval: `{available, provider, region, cost_per_hour, expected_hours, expected_total_usd, placement_reason, alternatives[], within_budget}`.
- `POST /v1/boxes/:id/exec-batch` `{commands:[string|{command,cwd,timeout_s}], stop_on_error (mặc định true), cwd, timeout_s, tail_lines}` → `{ok, results[], completed, stopped_at, tokens_saved}` (tối đa 50 lệnh).
- `GET /v1/jobs/:id?wait=30` — long-poll (tối đa 60s).
- `POST /v1/boxes/:id/restore` `{snapshot_id?}` — đưa /work + /memory về snapshot (mặc định bản mới nhất; snapshot phải nằm trên cùng host). `POST /v1/boxes/:id/fork` `{snapshot_id?, slug?}` → box mới từ snapshot (nếu box chưa có snapshot thì tự chụp). Driver local: untar; incus: `incus snapshot restore` / `incus copy src/snap dst` (dự phòng `incus import` từ tarball export).
- `GET /v1/boxes/:id/yaml` → `{yaml, source: request|generated}` (lưu yaml gốc lúc tạo; box tạo bằng JSON thì sinh yaml tương đương).
- Phản hồi `box_up`/`exec`/`fork`/`quote`/`exec-batch` có `next: ["box_exec(box_id=…, command=…)", …]`. Event `box.exec` có `data: {exit_code, duration_ms, tokens_saved, log_id, truncated}` (migration `002_event_data.sql`).

**MCP.** 18 tool: thêm `box_quote`, `box_exec_batch`, `box_fork`, `box_restore`, `box_logs` (theo log_id có tail/grep, hoặc liệt kê exec gần đây của box), `box_templates`; tool có annotations (readOnly/destructive/idempotent). Resources: `boxaz://box/{id}`, `boxaz://box/{id}/file/{path}`, `boxaz://box/{id}/logs`, `boxaz://log/{log_id}` (+ `resources/templates/list`). Prompts: `debug-failing-command`, `start-project`, `cost-review` (dịch theo Accept-Language).

**AGENTS.md** trong mỗi box (`/AGENTS.md`) được viết theo ngôn ngữ của agent: danh tính, ngân sách, quy tắc vòng đời, /work, /memory, danh sách tool, cách gọi Boxaz (MCP/REST/discovery), mẹo tiết kiệm token. Template: `internal/i18n/agentsmd/<locale>.md.tmpl`.

**22 loại agent + snippet.** claude-code, codex-cli, gemini-cli, cursor, windsurf, cline (MCP config đúng định dạng từng công cụ), openai-agents, langchain, langgraph, crewai, autogen, pydantic-ai, llamaindex (Python), vercel-ai-sdk, mastra (TypeScript), n8n, dify, flowise, make, zapier (JSON node/HTTP), hark, custom (curl). Snippet đọc key từ `BOXAZ_API_KEY`; phản hồi thêm `file` (đường dẫn file cấu hình) và `notes` (đã dịch, ghi rõ chỗ định dạng có thể khác theo phiên bản).

**CLI mới:** `box quote …`, `box batch <box> -- cmd1 ;; cmd2`, `box restore <box> [snap]`, `box fork <box> [snap] [--slug]`, `box yaml <box>`.

**SDK:** `../sdk/python` (gói `boxaz`) và `../sdk/typescript` (`@boxaz/sdk`) — xem README từng gói.

## 9. v1.2 — thêm provider, snapshot lên R2/B2, thông báo, egress, A2A

Theo `docs/api-contract-v1.md` mục "v1.2 additions". Mọi thứ dưới đây đã có test với server giả (httptest) — **chưa gọi API thật** của DigitalOcean/Contabo/netcup/R2/B2/Telegram/SMTP.

**Provider mới (`internal/provider`).** Cùng interface với Hetzner/Vultr: `Create/Delete/Status/ValidateToken` + `Regions` + `List` (server do Boxaz tạo, theo tag/nickname), giá lấy từ price feed.
- **DigitalOcean**: `/v2/droplets` (tag trung tính `managed`, `mgr-host-<id>`), cloud-init qua `user_data`, size `s-*`, region `sgp1/fra1/…`.
- **Contabo**: OAuth2 password grant (token cache tới khi hết hạn), `POST /v1/compute/instances` với `x-request-id`, tìm image Ubuntu theo tên; token có dạng `client_id:client_secret:api_user:api_password` hoặc JSON. Lưu ý: Contabo tính tiền theo tháng nên chỉ hợp làm pool.
- **netcup (SCP REST)**: không có API đặt mua server — Boxaz *nhận* server dự phòng đã đặt sẵn có nickname `mgr-spare-*` (tên cũ `boxa-spare-*` vẫn được nhận), cài lại image kèm cloud-init và đổi nickname thành `mgr-<host>`; xoá = trả về `mgr-spare-*` (không huỷ hợp đồng). Token = refresh token SCP (đổi lấy access token qua Keycloak).
- **Chuỗi dự phòng**: pool hiện có → burst theo `BOXAZ_PROVIDER_CHAIN` (mặc định Hetzner → Vultr → DigitalOcean → Contabo → netcup), chỉ dùng provider đã kết nối và nằm trong policy của agent. Tạo host lỗi thì loại provider đó (`provider:<id>`) và thử provider kế tiếp. `GET /v1/providers` trả thêm `implemented`, `chain_rank`, `token_source`; `GET /v1/providers/:id/regions`, `GET /v1/providers/:id/servers`.

**Snapshot lên object storage (`internal/objstore`, `manager/storage.go`, `boxd/objsync.go`).** Client S3 tự viết (SigV4 + presigned URL, không thêm dependency) cho R2/B2/S3/MinIO. Core ký URL `PUT` lúc snapshot và `GET` lúc restore/fork rồi gửi cho boxd; boxd stream tar.gz thẳng lên/xuống bucket, **secret không bao giờ rời core**. Snapshot có `storage: local|s3`, `remote_url`, `upload_error`. Có bản trên bucket thì restore/fork chạy được ở **host bất kỳ** (trước đây phải cùng host). Retention: `reaper.keep_snapshots` (theo box) + `reaper.snapshot_retention_days`; reaper xoá cả object trên bucket. Không cấu hình (hoặc upload lỗi) → giữ hành vi cũ, snapshot nằm tại host. Cấu hình: env `BOXAZ_S3_*` hoặc `PUT /v1/storage {provider, endpoint, region, bucket, prefix, access_key_id, secret_access_key}` (secret lưu trong kho secret mã hoá với tên dành riêng `BOXAZ_SYS_*`, ẩn khỏi `GET /secrets`); `POST /v1/storage/test` ghi/đọc/xoá một object thử. Test chỉ dùng S3 giả chạy trong process (`objstore.NewFake`, có kiểm chữ ký).

**Thông báo (`internal/notify`, `manager/notify.go`).** Telegram `sendMessage` (HTML) và email SMTP (STARTTLS / TLS / none, PLAIN auth). Sự kiện: `daily_report` (giờ + múi giờ trong settings), `budget` (80% và 100% ngân sách ngày của agent, tổng tháng), `approval` (mục "Cần anh" mới), `emergency_stop`, `box_failed`, `host_down`. Nội dung dịch theo `settings.locale`. Bot token và mật khẩu SMTP lưu trong kho secret mã hoá (`BOXAZ_SYS_TELEGRAM_BOT_TOKEN`, `BOXAZ_SYS_SMTP_PASSWORD`), API chỉ trả `has_*`. `GET/PUT /v1/notify`, `POST /v1/notify/test {channel: telegram|email|all, kind: test|daily_report}`; 30 lần gửi gần nhất nằm trong `recent`.

**Egress allowlist (`internal/egress`, `boxd/egress.go`).** Khai báo theo box: `egress: {allow: [...], deny_by_default}` (JSON, box.yaml dạng map hoặc list, tham số `egress` của tool MCP `box_up`), sửa sau bằng `PUT /v1/boxes/:id/egress`. Mục hợp lệ: tên miền, `*.wildcard`, IPv4/IPv6, CIDR, kèm `:port` tuỳ chọn. Thứ tự áp dụng: box → `policy.egress_allowlist` của agent (là trần: box không được xin ngoài danh sách agent → `POLICY_DENIED`) → `settings.egress_deny_by_default`. boxd (incus) phân giải tên miền thành IP và áp **Incus network ACL** (mặc định) hoặc bảng **nftables** riêng cho box, luôn cho phép DNS tới resolver; rule được sinh thuần (có unit test). Driver local **không thực thi** — box trả `egress.enforced=false` kèm `warning` rõ ràng và chỉ xuất `BOXAZ_EGRESS_ALLOW` cho tool tự tuân thủ. Box hiển thị `egress {allow, deny_by_default, source, enforced, mode, rules, warning, applied_at}`.

**A2A JSON-RPC (`internal/api/a2a.go`).** `POST /a2a` (cùng Bearer key): `message/send` (= `tasks/send`), `message/stream` (= `tasks/sendSubscribe`, SSE), `tasks/get`, `tasks/cancel`, `tasks/resubscribe`. Text part = lệnh + `metadata.box_id` (hoặc data part `{box_id, command, cwd?, timeout_s?}`) → job exec async, **task id = job id**; data part `{tool, arguments}` gọi bất kỳ tool nào trong 18 tool (chạy đồng bộ). Trạng thái job → A2A: `queued→submitted`, `running→working`, `done→completed`, `failed→failed`, `canceled→canceled`. `tasks/cancel` = `POST /v1/jobs/:id/cancel` (kill lệnh trên host). Agent card (`/.well-known/agent.json`) giờ có `url: <base>/a2a`, `preferredTransport: JSONRPC`, `capabilities.streaming: true`, skill `exec` + 18 tool.

**Kiểm thử v1.2.** `go test ./...` (thêm `internal/{objstore,notify,egress}`, `provider/more_providers_test.go`, `boxd/egress_test.go`, `e2e/v12_test.go`: snapshot qua S3 giả giữa 2 host + retention, egress, Telegram giả, A2A send/get/cancel/stream, chuỗi provider) và `scripts/e2e.sh` (thêm mục v1.2).



## 10. v1.4 — provider Sandbox API (T2 / T3)

Ngoài VPS/VM thô (T1), Boxaz thuê được sandbox của các dịch vụ "Sandbox API". Mỗi box chạy trong **một sandbox riêng**, tính tiền **theo giây**, và sandbox bị xoá khi box kết thúc. Hợp với task ngắn, code không tin cậy, hoặc việc cần fork nhanh.

**Hai cách chạy (`sandbox_mode`, theo provider hoặc `BOXAZ_SANDBOX_MODE`).**
- **T2 – dial-in:** core chạy `boxd --dial` trong sandbox bằng bootstrap token dùng một lần, boxd gọi ngược về core qua `BOXAZ_PUBLIC_URL` và đăng ký như một host bình thường (đủ terminal, snapshot, egress). Cần sandbox giữ được tiến trình nền và core có URL công khai.
- **T3 – shim:** core trả lời các RPC của boxd bằng exec/file API của provider (`internal/sandbox/shim.go`). Không cần URL công khai, nhưng không có terminal hay luật egress. Snapshot T3 là tarball `/work`+`/memory` do core giữ (`internal/sandbox/shimsnap.go`), xem mục 11.
- `auto` (mặc định) chọn T2 khi provider là T2, giữ được tiến trình nền và `BOXAZ_PUBLIC_URL` đã đặt, ngược lại thì T3.

**Adapter.** Có hai loại adapter, đều nằm trong `internal/sandbox`. Loại *native* viết bằng Go. Loại *YAML* là adapter khai báo: route HTTP, cách xác thực, đường dẫn JSON, ánh xạ trạng thái. Adapter YAML dựng sẵn nằm trong `builtin/*.yaml`; muốn thêm thì bỏ file vào `BOXAZ_SANDBOX_ADAPTERS_DIR`. Trước khi nạp, kiểm tra file bằng `POST /v1/sandbox/adapters/lint` (admin key) hoặc màn hình **Hạ tầng → Sandbox API → Adapter YAML tuỳ chỉnh**.

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
| Novita Sandbox (`novita`) | T2 | native | **live** (tạo/exec/file/xoá chạy thật ở us-phx-1; pause và luồng boxaz-core chưa chạy vì hết số dư) | microvm | 600 ms | pause, cổng | 4 giờ | $0.0763/giờ | `NOVITA_API_KEY`, `NOVITA_TEMPLATES` |
| E2B (`e2b`) | T2 | native | **live** (đã chạy thật toàn bộ vòng đời + box up/exec/down qua boxaz-core, gói Hobby) | microvm | 400 ms | fork, pause, cổng | 24 giờ | $0.1089/giờ | `E2B_API_KEY`, `E2B_TEMPLATES` |
| Runloop (`runloop`) | T2 | yaml | docs | microvm | 2000 ms | pause, cổng | không giới hạn | $0.0792/giờ | `RUNLOOP_API_KEY` |
| Blaxel (`blaxel`) | T2 | yaml | docs | microvm | 25 ms | pause, cổng | không giới hạn | $0.0828/giờ | `BL_API_KEY`, `BL_WORKSPACE` |
| Upstash Box (`upstash-box`) | T3 | yaml | mã nguồn SDK | container | 1500 ms | pause, cổng | không giới hạn | $0.0828/giờ (ước tính) | `UPSTASH_BOX_API_KEY` |
| Agent 37 (`agent37`) | T2 | yaml | docs | gvisor | 5000 ms | fork, pause, cổng | không giới hạn | $0.0832/giờ (ước tính) | `AGENT37_API_KEY` |
| boat (`boat`) | T2 | yaml | docs | vm | 3000 ms | fork, pause, cổng | không giới hạn | $0.1656/giờ (ước tính) | `BOAT_API_KEY` |
| Vercel Sandbox (`vercel`) | T2 | yaml | docs | microvm | 1500 ms | fork, cổng | 5 giờ | $0.1706/giờ (ước tính) | `VERCEL_TOKEN`, `VERCEL_PROJECT_ID`, `VERCEL_TEAM_ID` |

Mức xác minh: **live** = đã gọi API thật bằng key thật (chỉ các lệnh đọc, không tốn tiền); **docs** = route và trường lấy từ tài liệu/OpenAPI chính thức, chạy với server giả; **mã nguồn SDK** = provider không công bố REST, adapter đọc từ SDK chính thức. "Ước tính" nghĩa là provider không công bố đơn giá theo giây, nên Boxaz dùng giá ước tính. Daytona và Upstash Box chạy container chung kernel, nên bị loại khi `untrusted=true`.

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
| `BOXAZ_SANDBOX_ADAPTERS_DIR` | — | thư mục chứa adapter YAML bổ sung (`*.yaml`) |
| `BOXAZ_SANDBOX_MODE` | `auto` | `auto` / `dial` / `shim` cho mọi provider sandbox (prefs của từng provider được ưu tiên hơn) |
| `BOXAZ_AUTO_SANDBOX` | tắt | cho `box_up` tự thuê sandbox mà không cần duyệt (tính theo giây, xoá cùng box) |
| `BOXAZ_PROVIDER_CHAIN` | chuỗi T1 rồi tới chuỗi sandbox | thứ tự dự phòng (mặc định sandbox: e2b, daytona, novita, prized, boxdsh, runloop, blaxel, boat, agent37, vercel, upstash-box) |
| `E2B_API_KEY`, `DAYTONA_API_KEY`, `PRIZED_API_KEY`, … | — | kết nối provider sandbox lúc khởi động (xem cột "Biến môi trường") |

**Kiểm thử v1.4.** `go test ./...` gồm `sandbox/native_test.go` (httptest cho từng adapter native), `yaml_shim_test.go` (engine YAML + shim), `selector/sandbox_test.go`, `e2e/v14_test.go` (failover sang shim T3, boxd dial-in T2, dọn dẹp). Test live cho Prized là tuỳ chọn: chỉ đọc `BOXAZ_LIVE_PRIZED=1 PRIZED_API_KEY=…`; tạo sandbox thật (vài giây, free tier) `BOXAZ_LIVE_PRIZED_CREATE=1 PRIZED_API_KEY=… go test ./internal/sandbox -run PrizedLive`; end to end qua boxaz-core `PRIZED_API_KEY=… scripts/live-prized.sh` (shim T3; thêm `PRIZED_MODE=dial BOXAZ_PUBLIC_URL=https://… BOXAZ_DIST_DIR=…` cho T2, ví dụ qua cloudflared quick tunnel). Script luôn xoá sandbox nhãn `managed=1` khi kết thúc. E2B / Novita tương tự: `BOXAZ_LIVE_E2B_CREATE=1 E2B_API_KEY=… go test ./internal/sandbox -run E2BLive` (hoặc `BOXAZ_LIVE_NOVITA_CREATE=1 NOVITA_API_KEY=… … -run NovitaLive`), end to end `E2B_API_KEY=… scripts/live-e2b.sh` / `NOVITA_API_KEY=… scripts/live-novita.sh`. Danh sách sandbox của E2B trễ vài giây sau khi tạo/xoá, nên script kiểm tra bằng `GET /sandboxes/{id}`. Mọi thứ gửi tới provider (tên, nhãn/metadata `managed`/`mgr-host`, User-Agent) đều trung tính, không chứa tên sản phẩm. `scripts/e2e.sh` có mục v1.4: một adapter YAML trỏ tới `scripts/fake-sandbox.py` (sandbox REST chỉ exec, chạy trên localhost). Mục này kiểm tra lint, kết nối, recommend, tạo box trên host sandbox (shim T3), exec, và sandbox bị xoá khi box kết thúc.

## 11. Deploy app trên sandbox (URL công khai, sleep, snapshot, clone)

Agent dùng box sandbox để chạy web app thật: `box up` → `git clone` + cài đặt → chạy server ở `0.0.0.0:<port>` → lấy URL công khai từ Boxaz.

- **URL công khai của port.** `GET /v1/boxes/:id/ports` liệt kê từng port `{port, url, kind, public, note, caveats}`; `POST /v1/boxes/:id/ports {port}` mở thêm port cho box đang chạy. CLI `box url <box> [port]`, MCP tool `box_url {box_id, port?}`. `url` của box là URL của port đầu tiên trong `ports`. Boxaz đi theo chuỗi dự phòng, dừng ở bước đầu tiên dùng được:
  1. `provider-preview` — host preview của provider: E2B `https://<port>-<sandboxID>.e2b.app`, Novita `https://<port>-<sandboxID>.<domain>` (vd. `us-phx-1.sandbox.novita.ai`, không cần traffic token), Daytona theo API preview. Khi box ngủ URL trả 502 cho tới khi wake.
  2. `ingress` — core phục vụ port ở `<BOXAZ_PUBLIC_URL>/x/<key>-<port>/` (`key` là HMAC theo box, không đoán được) và chuyển từng request qua ingress Proxy sẵn có. Box T2 đã có boxd dial-in nên không cần gì thêm; box T3 (shim) được core thả một agent nhỏ (`boxd ingress`, tải từ `<public>/dl/boxd-linux-<arch>`, cần `BOXAZ_DIST_DIR`) gọi ra `/v1/ingress/connect`. Điều kiện: `BOXAZ_PUBLIC_URL` đã đặt và trông như địa chỉ Internet (không phải localhost/`.local`/IP nội bộ; ép bằng `BOXAZ_INGRESS_ASSUME_PUBLIC=1`) **và** agent kết nối được từ trong sandbox trong 25 s. Hạn chế: URL có tiền tố đường dẫn nên app phải dùng link tương đối; chỉ HTTP, đệm qua core (không WebSocket/streaming, body tối đa 32 MB); request đầu tiên vào box đang ngủ sẽ đánh thức box.
  3. `tunnel` — Cloudflare quick tunnel chạy trong sandbox (cloudflared tải khi cần, miễn phí, không cần tài khoản) → `https://<random>.trycloudflare.com/`. Hạn chế: không cam kết uptime; URL đổi nếu tunnel phải khởi động lại (vd. wake làm mất tiến trình); request đầu có thể chậm vài giây. Tắt bằng `settings.disable_tunnel` hoặc `BOXAZ_DISABLE_TUNNEL=1`.
  4. `none` — không có URL; `note` giải thích, `caveats` ghi lý do từng bước bị bỏ qua.
  Sau khi wake, core dựng lại exposure (khởi động lại agent / kiểm tra lại tunnel, URL mới nếu phải chạy lại); `box down` gỡ hết. Mọi thứ gửi vào sandbox dùng tên trung tính (thư mục `.boxd`, `boxd ingress`, `cloudflared`).
- **Sleep / wake.** `box sleep` = pause của provider, `box wake` = resume (shim T3 và host T2). Idle sleep của reaper (`idle_minutes`) dùng cùng đường này. Đo live: tiến trình app và file còn nguyên sau khi wake, URL phục vụ lại ngay.
- **Snapshot T3.** Shim tar `work memory AGENTS.md` trong sandbox, kéo về core (`$BOXAZ_DATA_DIR/snapshots/<snap>.tar.gz`, `location=local://…`, tối đa 64 MB; lớn hơn cần object storage, khi đó sandbox tự upload lên URL presign). Restore đẩy tarball lên và giải nén thay `/work` + `/memory` (giữ AGENTS.md của box). Snapshot của shim restore được vào box sandbox khác; `box fork` từ snapshot T3 tạo một sandbox mới cùng provider. Retention xoá cả file tarball ở core.
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

| | `MODE=tunnel` (không đặt `BOXAZ_PUBLIC_URL`) | `MODE=ingress` (core lộ ra qua cloudflared quick tunnel làm `BOXAZ_PUBLIC_URL`) |
|---|---|---|
| kind / URL | `tunnel`, `https://<random>.trycloudflare.com/` | `ingress`, `<public>/x/<key>-8000/` |
| box up / clone + `npm install` | 4.3 s / 3.6 s | 4.1 s / 3.4 s |
| `GET /ports` lần đầu (dựng exposure) | 7.9 s (tải cloudflared + chờ URL) | 0.53 s (tải agent + kết nối) |
| curl từ ngoài → 200 "Hello World!" | 0.11 s (sau đó 0.08 s) | 0.23 s (sau đó 0.11 s) |
| URL khi box ngủ | 502 | 200 — request đầu đánh thức box |
| sleep / wake | 0.26 s / 0.29 s | 0.31 s / 0.19 s |
| sau wake | tiến trình còn, `GET /ports` 7.2 s, URL giữ nguyên, 200 sau 0.08 s | tiến trình còn, `GET /ports` 9 ms, URL giữ nguyên, 200 sau 0.10 s |
| box down → URL chết | 0.05 s (tunnel còn trả lời vài giây khi Prized đang xoá, rồi 530) | 0.1 s → 404 ngay |

Chạy: `PRIZED_API_KEY=… scripts/live-publicurl.sh` (tunnel) hoặc `MODE=ingress PRIZED_API_KEY=… scripts/live-publicurl.sh` (cần `cloudflared` trong PATH; script tự build `boxd-linux-amd64/arm64` vào `BOXAZ_DIST_DIR`). Một sandbox, watchdog 280 s. Test không cần mạng: `e2e/publicurl_test.go` (agent boxd thật tải từ `/dl`, cloudflared giả).


## 12. Đổi tên Boxa → Boxaz

Sản phẩm đổi tên thành **Boxaz**: binary `boxaz-core` (trước là `boxa-core`), biến môi trường `BOXAZ_*`, systemd `boxaz-core.service`, module Go `boxaz/core`, SDK `boxaz` (Python) / `Boxaz` (TypeScript). Lớp tương thích giữ cấu hình cũ chạy được:

- `BOXA_*` vẫn được đọc khi `BOXAZ_*` cùng tên chưa đặt (tên mới luôn thắng); core in một dòng cảnh báo deprecated lúc khởi động. Dashboard (`next.config.ts`) và SDK làm tương tự với `BOXA_*` / `NEXT_PUBLIC_BOXA_*`.
- Dữ liệu cũ: `<data>/boxa.db` được dùng nếu chưa có `boxaz.db`; `/var/lib/boxa/memory`, container `boxa-*`, file `.boxa.json` của driver local, secret hệ thống `BOXA_SYS_*`, cookie `boxa_key` / `boxa_locale`, tham số ingress `boxa_token` / cookie `boxa_t` vẫn được nhận.
- SDK: `import boxa` (Python) và `Boxa`, `BoxaError`… (TypeScript) là alias deprecated.
- Mọi thứ provider nhìn thấy (tên `mgr-*`, nhãn `managed`/`mgr-host`, User-Agent `sandbox-client/1`) vẫn trung tính, không chứa tên sản phẩm.

## 13. v1.7 — app được giám sát, URL Boxaz đánh thức box, URL token

Mục tiêu: agent deploy web app một lần rồi quên — app tự chạy lại, URL không đổi, box ngủ khi rảnh và thức khi có người truy cập.

- **App supervisor.** `PUT /v1/boxes/:id/app {repo?, ref?, install?, start, port?, ready?: {http|tcp}, restart?: always|on-failure|never, env?, workdir?, backoff_max_s?}` khai báo app; Boxaz clone repo (nếu có), chạy `install` một lần cho mỗi spec, chạy `start` (nhận `$PORT`), restart khi crash với backoff (trần mặc định 30 s), chạy lại sau wake / restore / clone và chỉ cho traffic qua gateway khi app sẵn sàng (HTTP status < 500 ở `ready.http`, mặc định TCP trên `port`). Box T1/T2 dùng supervisor trong boxd; box T3 (exec-only) dùng script `setsid` trong sandbox (`status.supervisor = boxd | script`). `GET /app` → `{spec, status: {state, ready, pid, restarts, last_exit, started_at, ready_at, message, supervisor}}`, `POST /app/restart`, `DELETE /app`, `GET /app/logs?offset=&max=` (ring buffer + file; `?follow=1` stream SSE event `log {data, offset, truncated}`). CLI `box app set|status|restart|logs [-f]|rm`, MCP `box_app_set`, `box_app_status`, `box_app_logs`.
- **Gateway URL theo box + port.** Mỗi port đã mở có `gateway_url` cố định: `<BOXAZ_PUBLIC_URL>/x/<key>-<port>/`, hoặc `https://<key>-<port>.<BOXAZ_GATEWAY_DOMAIN>/` khi đặt `BOXAZ_GATEWAY_DOMAIN` (không còn tiền tố đường dẫn). Gateway chuyển thẳng WebSocket, SSE và body chunked (không đệm) qua link stream của box hoặc host preview của provider (chỉ HTTP/1.1).
- **Wake on request.** Request vào box đang ngủ đánh thức box, giữ request tối đa `settings.wake.hold_s` (mặc định 10 s, hoặc `BOXAZ_WAKE_HOLD`), chờ app sẵn sàng rồi trả lời; hết thời gian thì trả trang "waking" (trình duyệt) hoặc 503 + `Retry-After` (`settings.wake.retry_after_s`, mặc định 5). HEAD, bot, link preview và health probe (`/healthz`, `/robots.txt`…) không đánh thức box trừ khi `settings.wake.wake_on_probes=true`. Tắt theo box: `PATCH /v1/boxes/:id {wake_on_request: false}`.
- **URL token.** `POST /v1/boxes/:id/ports/:port/tokens {ttl_s?, label?, private?}` cấp token `bxt_…` chỉ cho box + port đó (mặc định 1 ngày, 60 s … 30 ngày), trả giá trị một lần kèm `url` (`gateway_url?token=…`) và chuyển port sang `access=token`. Token gửi qua `?token=` (đổi sang cookie, 302), `Authorization: Bearer` hoặc `X-Boxaz-Token`; `?token=` của chính app (không có tiền tố `bxt_`) được giữ nguyên. `GET …/tokens` liệt kê (không có giá trị), `DELETE …/tokens/:token_id` thu hồi ngay, `PUT /v1/boxes/:id/ports/:port {access: public|token}`. CLI `box url <box> <port> --token [--ttl 1h]`, MCP `box_url_token`. Không bao giờ chia sẻ credential của provider.
  - **Lưu ý:** token chỉ bảo vệ URL Boxaz. Port ở chế độ token không còn được Boxaz trả URL preview của provider, nhưng trên provider có host preview riêng (E2B, Novita, Daytona) URL preview đó **vẫn truy cập được tại provider** với bất kỳ ai biết sandbox id (`https://<port>-<sandboxID>.e2b.app`…). Đừng dựa vào token để giữ bí mật trên các provider này; với dữ liệu nhạy cảm hãy để app tự xác thực hoặc dùng provider không có inbound HTTP (Prized, đi qua ingress của Boxaz).
- **Capability lifecycle.** `GET /v1/capabilities` → mỗi provider `{provider, group, memory_pause, wake_on_http_native, wake_via_gateway, fork, inbound_http, max_runtime, note}` (E2B / Novita / Prized theo tài liệu và đo live, sandbox khác lấy từ capability của adapter, host VPS: sleep dừng container nên app chạy lại khi wake).
- **Dashboard.** Tab **Port & app** ở trang box: URL Boxaz từng port (copy / mở), bật tắt token, cấp token (thời hạn + nhãn, hiện giá trị một lần), danh sách + thu hồi token, công tắc wake on request, badge capability của provider, trạng thái app (spec, state, restarts, ready) với nút restart và log theo dõi trực tiếp (SSE). Có dữ liệu giả lập khi `BOXAZ_MOCK=1`; chuỗi đủ 4 ngôn ngữ.
- **Sửa lỗi trong đợt live.** Transport tới host preview chỉ dùng HTTP/1.1 (ALPN h2 làm hỏng WebSocket upgrade), bỏ các header CDN đầu vào trước khi chuyển tiếp, và fork gốc không còn mang theo ingress agent / tunnel của box nguồn.

Kết quả live 2026-10-27 (`scripts/live-app.sh`, repo `heroku/node-js-sample`, `node index.js` trên cổng 8000, core lộ ra Internet qua cloudflared quick tunnel làm `BOXAZ_PUBLIC_URL`):

| | Prized | E2B |
|---|---|---|
| sleep → request vào URL Boxaz → 200 (wake on request) | 426 ms | 432 ms |
| kill app → supervisor restart → ready | 1.7 s | 1.6 s |
| restore snapshot → app ready | 2.0 s | 1.5 s |
| clone → app của bản clone ready | 1.7 s | 1.7 s |
| URL token: không token / Bearer / `?token=` / sau thu hồi | 401 / 200 / 302 / 401 | 401 / 200 / 302 / 401 |

Chạy: `PROVIDERS="prized e2b" PRIZED_API_KEY=… E2B_API_KEY=… scripts/live-app.sh` (cần `cloudflared`). Mỗi provider tối đa 2 sandbox cùng lúc (box + clone), watchdog xoá mọi sandbox `managed=1` sau 280 s, cuối cùng kiểm tra provider không còn sandbox nào. Test không cần mạng: `e2e/v17_test.go` (gateway, wake, token, app), `boxd/supervisor_test.go`, `sandbox/appscript_test.go`; `scripts/e2e.sh` chạy cả luồng v1.7 với core + boxd thật.
