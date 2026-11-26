# Boxaz spec v0.4 — Lớp kết nối mọi box + Growth/Invite

> Bản mới nhất: `boxaz-spec-v1.0.md` (2026-11-26).

Ngày: 2026-10-27 (v0.4 thêm mục 10 Growth/Invite; mục 1–9 giữ nguyên từ v0.3, ngày 2026-10-26). Kế thừa v0.2 (kiến trúc Gateway/Core/Host Agent/Adapter, 15 module). v0.3 đổi mục tiêu: Boxaz trở thành **lớp điều phối trung lập** nối được mọi loại box (VPS, sandbox API, PaaS, GPU, browser, macOS) với một API duy nhất cho agent, chọn nơi rẻ nhất đạt yêu cầu.

## 1. Bức tranh thị trường (battleships.dev, 369 provider, dữ liệu 2026-10)
- agent-sandbox 129 · hyperscaler 44 · PaaS 42 · GPU 41 · browser 37 · dev-env 33 · macOS 26 · Windows 13.
- Cách ly: VM 135 · container 75 · Firecracker 49 · gVisor 14 · dedicated 23 · khác.
- Mỗi provider có file JSON `features_resolved` (~60 cờ năng lực: snapshot, fork, pause_resume, ssh, docker_inside, browser, gpu, egress_allowlist…) + bảng giá + trích dẫn nguồn. Có `engine.js` tính giá theo workload.

=> Không ai tự viết adapter cho 369 nơi. Cần: **chuẩn hoá năng lực + chuẩn hoá đường điều khiển + nguồn giá mở**.

## 2. Nguyên lý cốt lõi
1. **Một "data plane" đồng nhất: `boxd` chạy trong mọi box có thể.** Provider chỉ lo *tạo/xoá/ngủ/snapshot*; mọi `exec`, file, log, PTY, output shaping, đo token đều qua `boxd` → hành vi giống hệt nhau ở mọi provider.
2. **`boxd` gọi ra ngoài (outbound), không cần mở cổng.** Kết nối ngược về Gateway qua QUIC/WebSocket (TLS, mTLS bằng token bootstrap một lần). Không phụ thuộc IP public, NAT, firewall, UDP → chạy được cả trong sandbox chặn inbound.
3. **Adapter mỏng, khai báo.** Chỉ cần ánh xạ ~6 động từ vòng đời; phần còn lại là `boxd`.
4. **Năng lực là dữ liệu, không phải code.** Scheduler đọc capability vector (lấy từ battleships, ghi đè bằng kết quả tự kiểm thử).
5. **Giá là dữ liệu mở.** Price feed = battleships JSON + `engine.js`, cập nhật hằng ngày; Boxaz đối chiếu với hoá đơn thật để hiệu chỉnh.

## 3. Ba tầng adapter (Box Provider Interface — BPI)

```
interface Provider {
  capabilities() -> CapVector            // từ battleships + self-test
  quote(spec, lifecycle, hours) -> Price // engine.js
  create(spec, bootstrap) -> Handle      // bootstrap = lệnh cài boxd + token
  destroy(h); sleep(h)?; wake(h)?        // ? = tuỳ năng lực
  snapshot(h)?; restore(snap)?; fork(h)?
  status(h) -> state
}
```

| Tầng | Ví dụ | Cách tạo | Data plane | Ghi chú |
|---|---|---|---|---|
| **T1 VPS/VM thô** | Hetzner, Vultr, Contabo, netcup, DO, OCI, AWS/GCP/Azure | OpenTofu provider hoặc API gốc; cloud-init cài Incus + boxd | boxd host → nhiều box Incus bên trong (bin-packing) | Rẻ nhất cho 24/7 và pool. Hàng trăm cloud có sẵn OpenTofu provider → phủ diện rộng gần như miễn phí |
| **T2 Sandbox API** | E2B, Daytona, Modal, Agent 37, zipbox, boat.dev, Upstash Box, Koyeb, Cloudflare Sandbox, Vercel Sandbox, Blaxel, Runloop | SDK/REST của provider; lệnh khởi động chạy `boxd --dial` | boxd trong sandbox, dial-out | Tốt cho task ngắn, boot nhanh, fork/snapshot bộ nhớ |
| **T3 Chỉ có exec API** | sandbox không cho chạy tiến trình nền | adapter dịch exec/file sang API native | "boxd-shim" phía Gateway giả lập | Hạ cấp: không PTY, output shaping làm ở Gateway |

Mở rộng đặc thù: browser cloud (Browserbase, Steel, Browser Use) và GPU (RunPod, Vast) là **tài nguyên gắn kèm** (attachable) cho box, không thay box.

### Adapter khai báo (cho T2/T3 dạng REST đơn giản)
```yaml
provider: zipbox
auth: { header: Authorization, value: "Bearer ${secret:ZIPBOX_KEY}" }
create:  { POST: /v1/sandboxes, body: { image: "{{image}}", cpu: "{{cpu}}", start_cmd: "{{bootstrap}}" }, id: $.id }
destroy: { DELETE: /v1/sandboxes/{{id}} }
status:  { GET: /v1/sandboxes/{{id}}, map: { running: $.state=="running" } }
```
→ thêm provider mới = 1 file YAML + secret, không build lại Core. Provider phức tạp thì viết plugin Go (go-plugin/gRPC) hoặc WASM.

## 4. Mạng nối các box
- **Control:** boxd ⇄ Gateway qua QUIC (fallback WebSocket/443). Một kết nối mang exec, file, PTY, metrics (multiplex).
- **Box ⇄ box (khi agent cần cụm):** overlay WireGuard qua headscale; box không có UDP thì đi relay DERP. Tên nội bộ `<slug>.boxaz.internal`.
- **Ingress ra Internet:** Gateway/Caddy reverse-tunnel tới boxd → URL `<slug>-<ip>.sslip.io` dù box nằm sau NAT ở provider nào.

## 5. Scheduler đa thị trường
Đầu vào: spec (cpu/ram/disk/gpu/browser), lifecycle (task/session/service), yêu cầu (cap flags), vùng, ngân sách, chính sách agent.
1. **Lọc** provider có đủ cap flags + được agent cho phép + còn quota.
2. **Định giá** bằng `engine.js` theo đúng cách tính của từng nơi (per-second, phí tối thiểu, idle, plan); pool đang có chỗ trống → chi phí biên ≈ 0.
3. **Chấm điểm** = giá dự kiến + phạt độ trễ boot + phạt độ tin cậy (lịch sử lỗi của Boxaz) + phạt vùng xa.
4. **Quy tắc mặc định:**
   - `service`/24/7 → T1 pool (Contabo/netcup/Hetzner).
   - `session` → T1 pool, ngủ bằng Incus stop/snapshot.
   - `task` ngắn, cần boot < 2s hoặc fork → T2 rẻ nhất.
   - Pool đầy → burst T1 theo giờ (Vultr/Hetzner) hoặc T2, tuỳ cái rẻ hơn cho thời lượng dự kiến.
5. **Di chuyển**: box session có thể snapshot → khôi phục ở provider khác khi rẻ hơn (chỉ T1↔T1 và nơi có custom image).

## 6. Price & capability feed
- Job hằng ngày: tải `https://battleships.dev/data/index.json` → các `/data/providers/<id>.json`; lưu bảng `offers` + `capabilities` (Postgres), giữ lịch sử.
- Chạy `engine.js` qua sidecar Node (hoặc goja trong Go) để báo giá.
- **Self-test** khi kết nối provider mới: tạo box nhỏ, đo boot time, thử snapshot/exec/egress → ghi đè cờ "unverified".
- **Ledger thật**: đối chiếu chi phí dự kiến vs hoá đơn provider → hệ số hiệu chỉnh từng provider.
- Đóng góp ngược: phát hiện giá sai → mở issue/PR cho battleships (data mở).

## 7. Stack bổ sung so với v0.2
- quic-go (tunnel boxd), headscale + DERP (mesh), OpenTofu (T1 đa cloud), go-plugin/wazero (adapter plugin), Node sidecar/goja (engine.js), Postgres bảng offers/capabilities.

## 8. Lộ trình cập nhật
- **P1 (tuần 1–2):** BPI + boxd dial-out + T1 Hetzner/Incus. (thay cho kết nối SSH trực tiếp)
- **P2 (tuần 3):** reaper, sleep/wake, snapshot R2, ledger; price feed battleships + engine.js.
- **P3 (tuần 4):** T1 Vultr/Contabo/netcup qua OpenTofu; T2 hai provider đầu (E2B hoặc Agent 37 + một nơi rẻ như zipbox); scheduler chấm điểm.
- **P4:** adapter YAML khai báo + self-test; headscale mesh; dashboard Hạ tầng hiển thị toàn thị trường.
- **P5:** GPU/browser attachable, di chuyển box giữa provider.

## 9. Rủi ro
- Điều khoản provider (một số cấm chạy agent nền/tunnel) → cờ chính sách mỗi provider.
- Dữ liệu battleships có mục "unverified" → luôn self-test trước khi dùng thật.
- Tài khoản/KYC từng provider vẫn do anh tạo; Boxaz chỉ giữ token trong Secrets.

## 10. Module Growth / Invite (mới ở v0.4)

Mục đích kép: mã mời vừa là **cổng dùng thử** (kiểm soát chi phí box free), vừa là **công cụ marketing/referral** để lan truyền sản phẩm. Mọi mã đều đo được nguồn (kênh, người giới thiệu, chiến dịch).

### 10.1 Khái niệm
- **Invite code**: chuỗi ngắn dễ gõ (vd `BOXAZ-7F2K-Q9`), có `channel`, `campaign`, `referrer_id` (nếu là mã giới thiệu), `max_uses`, `expires_at`, `trial_policy` (số box, giờ tối đa), trạng thái `active/paused/exhausted/expired`.
- **Referral code**: mã cá nhân mỗi user (và mỗi người trong waitlist) được cấp, dạng `boxaz.sslip.io/r/<handle>`; ai dùng mã → ghi `referrer_id` để tính thưởng và xếp hạng.
- **Waitlist entry**: email, nguồn, `referral_code` riêng, `referred_by`, điểm ưu tiên, vị trí hàng đợi, trạng thái `waiting/invited/redeemed`.
- **Trial grant**: quyền dùng thử gắn với tài khoản sau khi redeem: số box đồng thời, thời gian sống mỗi box, tổng giờ, ngày hết hạn.

### 10.2 Kênh phát mã
| Kênh | Cách nhận | Ghi nhận |
|---|---|---|
| Inbox / reply mạng xã hội | User nhắn tin hoặc reply tên dự án ("Boxaz") dưới bài post → bot/admin trả mã (bán tự động: webhook hoặc admin bấm "Phát mã" trong hàng chờ yêu cầu) | `channel=social:<x/fb/threads>`, `campaign=<post id>` |
| Email đăng ký | Điền email ở trang đăng ký/waitlist → mã gửi vào inbox khi tới lượt (xác thực email luôn) | `channel=email` |
| Discord | Lệnh `/boxaz invite` trong server Discord hoặc role sự kiện → bot DM mã (giới hạn 1 mã/tài khoản Discord) | `channel=discord`, `campaign=<event>` |
| Waitlist + referral | Đăng ký waitlist nhận mã giới thiệu riêng; mỗi người đăng ký qua mã đó đẩy người giới thiệu lên hàng trên | `channel=waitlist`, `referrer_id` |
| Admin thủ công | Admin tạo lô mã cho đối tác, KOL, cộng đồng | `channel=manual`, `campaign` tự đặt |

Chống lạm dụng: 1 trial/email đã xác thực, giới hạn theo IP/thiết bị, chặn email tạm (danh sách domain disposable), rate-limit redeem, mã social/Discord dùng 1 lần.

### 10.3 Marketing & attribution
- Mỗi mã/lô có kênh + chiến dịch → dashboard đếm: phát ra, redeem, kích hoạt (tạo box đầu tiên), giữ chân 7 ngày, chuyển trả phí.
- Referral: người giới thiệu thấy số người đã mời; thưởng mặc định: +1 bậc ưu tiên waitlist mỗi lượt đăng ký, +2 giờ trial mỗi lượt redeem thành công (cấu hình được).
- Leaderboard giới thiệu (tuỳ chọn, ẩn email).
- UTM từ link giới thiệu được lưu cùng waitlist entry.

### 10.4 Chính sách dùng thử (capacity)
- **Giai đoạn mở đầu**: mở **5 box trial đồng thời trên toàn hệ thống** (pool trial riêng, ưu tiên host pool rẻ/warm pool; không cho dùng GPU, không cho provider burst trả theo giờ trừ khi admin bật).
- Thời gian sống mỗi box trial: mặc định 2 giờ, **admin chỉnh được tối đa 12 giờ**; hết giờ → snapshot (nếu bật) rồi xoá; box ngủ khi rảnh 5 phút.
- Khi pool trial đầy → người redeem vào hàng chờ trial, nhận thông báo (email/Telegram/Discord) khi có slot.
- **Khi số đăng ký ban đầu đạt mục tiêu** (`initial_signup_target`, cấu hình): hệ thống tự hạ còn **1 box trial** (admin được báo trước và xác nhận), rồi tinh chỉnh tiếp theo số liệu chi phí/chuyển đổi.
- Mọi tham số là cấu hình runtime: `trial.max_boxes`, `trial.box_ttl_hours (≤12)`, `trial.total_hours_per_user`, `trial.initial_signup_target`, `trial.after_target_max_boxes=1`, `trial.allowed_providers`, `trial.egress=allowlist`.
- Egress trial mặc định allowlist (pypi, npm, github…) để tránh lạm dụng (spam, mining); giới hạn CPU/RAM nhỏ (1 vCPU / 2 GB).

### 10.5 API (REST, thêm vào contract)
- `POST /v1/invites/redeem` `{code, email}` → `{trial_grant, queue_position?}`; lỗi có `next` (vd `join_waitlist`).
- `POST /v1/waitlist` `{email, referral_code?, utm?}` → `{position, referral_code, share_url}`.
- `GET /v1/waitlist/me` → vị trí, số người đã mời, thưởng.
- `GET /v1/referrals/me` → mã, link chia sẻ, danh sách lượt (ẩn email), thưởng.
- `POST /v1/invite-requests` (webhook social/Discord hoặc form) `{channel, handle, message, campaign}` → tạo yêu cầu chờ phát mã.
- Admin: `POST /v1/admin/invites` (tạo lô: số lượng, kênh, chiến dịch, max_uses, hạn, trial_policy), `GET /v1/admin/invites?channel=&campaign=`, `PATCH /v1/admin/invites/{id}` (pause/hết hạn), `GET /v1/admin/invites/stats` (funnel theo kênh/chiến dịch/referrer), `GET|PUT /v1/admin/trial-policy`, `GET /v1/admin/waitlist` + `POST /v1/admin/waitlist/invite` (mời N người đầu hàng), `GET /v1/admin/invite-requests` + `POST .../{id}/approve`.
- MCP/CLI: `box invite redeem <code>`, `box trial status` (thời gian còn lại, slot).

### 10.6 UI/UX
- **Admin › Mã mời** (khu Admin): tab *Lô mã* (tạo lô, copy/xuất CSV, trạng thái, lượt dùng), tab *Yêu cầu* (hàng chờ từ social/Discord/email, nút "Phát mã"/"Từ chối"), tab *Hiệu quả* (funnel theo kênh: phát → redeem → tạo box → giữ 7 ngày → trả phí; bảng top referrer), tab *Chính sách trial* (số box đồng thời 5, TTL slider 1–12 giờ, mục tiêu đăng ký, ô xác nhận khi tự hạ về 1 box, provider cho phép).
- **Trang công khai Waitlist/Referral** (mobile-first): nhập email → hiện vị trí hàng đợi, mã giới thiệu + nút copy/chia sẻ (X, Facebook, Discord, Zalo), thanh tiến độ "mời thêm 3 người để lên top 50"; ô "Đã có mã mời? Nhập để dùng thử".
- **Màn redeem trong app**: nhập mã → thấy quyền trial (vd "2 box · tối đa 12 giờ/box"), đồng hồ thời gian còn lại ở top bar, banner khi sắp hết giờ, CTA nâng cấp.
- **Tổng quan (admin)**: thẻ "Trial: 3/5 slot đang dùng · 42/100 đăng ký mục tiêu".

### 10.7 Lộ trình
- P1: mã mời admin tạo + redeem + trial policy (5 box, TTL ≤12h) + waitlist cơ bản.
- P2: referral + attribution dashboard + email tự động.
- P3: bot Discord, webhook social inbox/reply, tự hạ về 1 box khi đạt mục tiêu.
