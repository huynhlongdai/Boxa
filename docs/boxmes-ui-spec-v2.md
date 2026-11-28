---
title: "Boxmes — Thiết kế UI v2 (theo spec v1.0)"
date: "2026-11-26"
---

# Boxmes — Thiết kế UI v2

Ngày: 2026-11-26. Đi kèm `boxmes-spec-v1.0.md` (module M1–M28). Tài liệu này đặc tả **từng màn hình**: mục đích, bố cục, thành phần, hành động, trạng thái (rỗng / đang tải / lỗi), quyền theo vai trò, và API gọi tới. Mockup bấm thử được dựng song song bằng dữ liệu mẫu.

Ký hiệu: **[Có]** màn đã có trong `web/` (v1.12) · **[Sửa]** có nhưng cần đổi cho v2 · **[Mới]** chưa có. Quyền: O=Owner, A=Admin, D=Developer, B=Billing, V=Viewer, Au=Auditor, PA=admin nền tảng.

---

## 0. Nguyên tắc thiết kế

1. **Agent trước, người sau, nhưng người cầm phanh.** Mọi màn đều trả lời được: đang tốn bao nhiêu, cái gì đang chạy, cái gì cần anh duyệt.
2. **Tiền luôn hiển thị.** Số dư credit + chi hôm nay nằm trên top bar ở mọi màn.
3. **Một thao tác chính mỗi màn** (nút chính góc phải tiêu đề). Thao tác nguy hiểm (xoá, dừng khẩn cấp, khoá workspace) luôn có hộp xác nhận gõ tên.
4. **Lỗi tự giải thích.** Mọi lỗi API hiển thị `message` + `hint` đã dịch + nút theo `next` (vd "Nạp thêm credit", "Nâng gói").
5. **Bàn phím:** ⌘K bảng lệnh (nhảy màn, tạo box, tìm box/agent theo tên), `g b` Boxes, `g a` Agents, `?` phím tắt.
6. **4 ngôn ngữ** vi/en/zh/ja, mặc định theo trình duyệt; số tiền USD, giờ theo múi giờ người dùng (cài đặt hồ sơ).
7. **Responsive:** ≥1280 sidebar đầy đủ; 768–1279 sidebar thu thành icon; <768 thanh tab dưới (Tổng quan, Boxes, Cần anh, Thanh toán, Thêm).
8. **Sáng/tối** theo hệ thống, chuyển tay trong hồ sơ.

### Design tokens (giữ của web hiện tại, chỉ đổi thương hiệu)
- Chữ: Inter (UI), JetBrains Mono (mã, ID, lệnh). Cỡ 12/14/16/20/28.
- Màu trạng thái box: chạy = xanh lá, ngủ = xám xanh, đang tạo = xanh dương nhấp nháy, lỗi = đỏ, snapshot = tím, đã xoá = xám.
- Màu tiền: dưới 80% = trung tính, 80–99% = cam, ≥100% = đỏ.
- Bo góc 8 px, khoảng cách lưới 4 px, thẻ có viền 1 px thay vì bóng.

---

## 1. Kiến trúc thông tin

```
Công khai (chưa đăng nhập)
  / (landing) · /pricing · /docs · /status · /waitlist · /r/<mã giới thiệu>
  /login · /signup[?invite=] · /forgot-password · /reset-password?token= · /verify-email?token=
  /invite/<token> (nhận lời mời workspace)

Ứng dụng (đã đăng nhập) — vỏ: top bar + sidebar
  Khu workspace
    Tổng quan · Boxes (+ chi tiết box) · Agents (+ chi tiết agent) · Cần anh
    Templates · Volumes · Secrets · Chi phí · Developers (API keys, Webhooks, Tích hợp, Git)
    Cài đặt workspace (Chung, Thành viên, Thanh toán, Gói & hạn mức, Audit, Thông báo, Provider của tôi[BYOC], Nguy hiểm)
  Cá nhân
    Hồ sơ · Bảo mật (mật khẩu, MFA, phiên) · Thông báo cá nhân · Giới thiệu bạn bè
  Nền tảng (chỉ PA)
    Hạ tầng (provider, host, pool, bảng giá, sức khoẻ, mesh, adapter)
    Admin (Workspace, Người dùng, Mã mời, Danh sách chờ, Lạm dụng, Doanh thu, Hệ thống, Job, Audit hệ thống, Tính năng)
```

Sidebar (nhóm, thu gọn được): Tổng quan · Cần anh (badge) — **Vận hành:** Boxes · Agents · Templates — **Tài nguyên:** Volumes · Secrets — **Tiền:** Chi phí · Thanh toán (lối tắt tới Cài đặt → Thanh toán; O/B) — **Tích hợp:** Developers — Cài đặt — **Nền tảng** (chỉ PA): Hạ tầng · Admin. Cuối sidebar: thẻ gói ("Pro · 3/10 box") + nút "Mời bạn, nhận $10". Mobile: tab dưới Tổng quan · Boxes · Cần anh · Chi phí · Thêm (sheet theo cùng nhóm).

---

## 2. Vỏ ứng dụng (top bar)

| Vị trí | Thành phần | Hành vi |
|---|---|---|
| Trái | Logo Boxmes · **Bộ chọn workspace** (tên + gói; danh sách workspace của tôi, "+ Tạo workspace") | Đổi workspace = `POST /v1/auth/workspace`, tải lại dữ liệu |
| Giữa | Ô tìm ⌘K | Bảng lệnh |
| Phải | **Số dư** "$42.18" (đỏ khi < ngưỡng, bấm → Thanh toán) · **Chi hôm nay** "$1.84" · **Cần anh** (pill cam có số) · Chuông thông báo · Ngôn ngữ · Avatar (Hồ sơ, Bảo mật, Đăng xuất) | |
| Banner toàn cục | Email chưa xác thực ("Xác thực email để tạo box — Gửi lại") · Số dư thấp ("Còn ~2 ngày — Nạp ngay") · Workspace bị khoá (đỏ, không đóng được) · Đang xem như người khác (PA impersonate, nút "Thoát") | Ưu tiên: khoá > impersonate > số dư > email |

---

## 3. Màn công khai & xác thực (M1, M17) — [Mới]

### 3.1 Đăng ký `/signup`
- Bố cục 2 cột: trái là form; phải là 3 lợi ích + giá box ví dụ ("2 vCPU/4 GB từ $0.024/giờ").
- Trường: Email · Mật khẩu (thanh độ mạnh, ≥ 10 ký tự) · Tên (tuỳ chọn) · Tên workspace (gợi ý từ email) · **Mã mời** (hiện khi `signup_mode=invite`; tự điền từ `?invite=`) · ô đồng ý Điều khoản/AUP.
- Nút phụ: "Tiếp tục với GitHub" / "Google" (P1).
- Lỗi: mã sai/hết lượt → hint + nút "Tham gia danh sách chờ"; chế độ `closed` → thay form bằng thẻ danh sách chờ.
- Thành công → `/onboarding` (bước 1: xác thực email).
- API: `GET /v1/auth/config`, `POST /v1/auth/signup`.

### 3.2 Đăng nhập `/login`
- Email · Mật khẩu · "Quên mật khẩu?" · "Đăng nhập bằng API key" (thu gọn, cho tự host).
- Bị khoá tạm (RATE_LIMITED) → thông báo "Thử lại sau N phút".
- MFA (P1): bước 2 nhập mã 6 số / mã khôi phục.

### 3.3 Quên / đặt lại mật khẩu, xác thực email
- Quên: nhập email → luôn báo "Nếu email tồn tại, đã gửi link" (không lộ tài khoản).
- Đặt lại: mật khẩu mới ×2 → "Đã đổi, mọi phiên khác đã đăng xuất" → đăng nhập.
- Xác thực: trạng thái tự chạy khi mở link: đang xác thực → thành công (nút "Vào dashboard") / link hết hạn (nút "Gửi lại").

### 3.4 Danh sách chờ & giới thiệu `/waitlist`, `/r/<mã>`
- Mobile-first. Ô email → thẻ kết quả: "Bạn ở vị trí #128", mã giới thiệu + nút copy, chia sẻ X/Facebook/Discord/Zalo, thanh "Mời thêm 3 người để lên top 50".
- Ô "Đã có mã mời?" → sang `/signup?invite=`.
- API: `POST /v1/waitlist`, `GET /v1/waitlist/me`.

### 3.5 Nhận lời mời workspace `/invite/<token>`
- Thẻ: "Lan mời anh vào workspace **kecon-team** với vai trò Developer". Đã đăng nhập → nút "Tham gia"; chưa → đăng nhập/đăng ký rồi tự quay lại.

---

## 4. Onboarding — [Sửa]
Checklist 5 bước bên phải màn Tổng quan khi workspace mới (ẩn được):
1. Xác thực email ✓
2. Nạp credit hoặc dùng $5 tặng (mở URL công khai khi đã nạp)
3. Tạo agent đầu tiên → chọn loại (22 loại) → copy cấu hình MCP
4. Agent tạo box đầu tiên (đợi realtime, có nút "Tạo box thử")
5. Mời thành viên / đặt ngân sách
Tiến độ "3/5" và phần thưởng "+$2 khi hoàn tất".

---

## 5. Tổng quan (M14, M15) — [Sửa]
- Tiêu đề: "Tổng quan" · phụ đề "kecon-team · 15 box · 4 agent" · nút nguy hiểm "■ Dừng khẩn cấp" (O/A).
- **Hàng KPI (5 thẻ):** Số dư credit (kèm "còn ~18 ngày") · Chi hôm nay / ngân sách ngày (thanh) · Chi tháng / trần tháng · Box chạy/ngủ · Tiết kiệm so với 24/7.
- **Cần anh** (tối đa 5, "Xem tất cả"): mỗi mục có tiêu đề, chi tiết, nút Duyệt/Từ chối hoặc hành động gợi ý.
- **Chi phí 7 ngày** (cột chồng theo agent) + **Top 5 task tốn nhất hôm nay**.
- **Hoạt động gần đây** (luồng event realtime SSE: box tạo/ngủ/lỗi, ai làm).
- **Hạn mức gói** (thanh nhỏ: box đồng thời 3/10, vCPU 6/32, snapshot 2.1/10 GB).
- Rỗng: workspace mới → checklist onboarding chiếm cột phải.

---

## 6. Boxes (M4, M5, M7–M10) — [Sửa]

### 6.1 Danh sách
- Bộ lọc tab: Tất cả · Đang chạy · Đang ngủ · Lỗi; lọc thêm: agent, provider, vòng đời, nhãn; tìm theo tên. Chuyển dạng **thẻ / bảng**.
- Cột bảng: Tên (+ URL nếu có) · Agent · Trạng thái · Nơi chạy (provider · vùng · T1/T2/T3) · Vòng đời · Uptime · Chi phí / ngân sách · Hành động nhanh (Ngủ/Đánh thức, Snapshot, Terminal, ⋯).
- Chọn nhiều → thanh hàng loạt: Ngủ, Snapshot, Xoá.
- Nút chính "+ Tạo box".
- **Phân loại theo vị trí:** mỗi box có `location` (vùng EU/US/APAC/Sandbox API · quốc gia · thành phố · provider/host). Chip "Vị trí" có đếm số, kết hợp được với tab trạng thái; "Nhóm theo" Không nhóm / Vị trí / Agent / Trạng thái, nhóm Vị trí hiện mục thu gọn "EU — Đức · Falkenstein (Hetzner FSN) · 3 box · $0.61 hôm nay". Sidebar: Boxes xổ ra EU › Falkenstein, Helsinki; APAC › Singapore, Tokyo; US; Sandbox API (kèm số box), mở `#/boxes?loc=`. Tổng quan có thẻ "Box theo vị trí". API: `GET /v1/boxes?region=&provider=&location=`, `GET /v1/boxes/locations`.

### 6.2 Tạo box (ngăn kéo phải, 3 bước)
1. **Nguồn:** Template (lưới thẻ) · box.yaml (editor có kiểm lỗi theo dòng) · Fork từ snapshot.
2. **Cấu hình:** cpu/ram/disk (thanh trượt, giới hạn theo gói), vòng đời task/session/service, TTL, idle phút, vùng (Tự động / EU / US / APAC), yêu cầu (fork, GPU, cách ly mạnh), egress (Hạn chế / Mở — khoá khi chưa xác minh, tooltip lý do), volume gắn kèm, nhãn.
3. **Báo giá & nơi đặt:** gọi `POST /v1/quote` mỗi khi đổi cấu hình: thẻ phương án chọn (provider, giá/giờ, tổng dự kiến, lý do) + 2 phương án thay thế; cảnh báo vượt ngân sách/hạn mức. Nút "Tạo box".

### 6.3 Chi tiết box `/boxes/[id]`
Đầu trang: tên (sửa được) · trạng thái · nơi chạy · URL chính (copy) · nút Ngủ/Đánh thức · Snapshot · Fork · ⋯ (Clone, Xoá, Xem YAML).
Tab:
| Tab | Nội dung |
|---|---|
| Tổng quan | spec, agent, vòng đời, TTL còn lại (đồng hồ), chi phí đến giờ, `placement_reason`, nhãn |
| Terminal | xterm.js, nhiều phiên, nút "Mở SSH" (lệnh `box ssh` copy), tải lên/xuống |
| Tệp | cây thư mục /work, /memory; xem/sửa file nhỏ, tải lên kéo-thả, tải xuống |
| Lệnh & job | lịch sử exec (lệnh, exit, thời lượng, tokens_saved, log), chạy lệnh mới, job async |
| Code (P1) | notebook đơn giản: ô Python/Node, output bảng/ảnh, ngữ cảnh giữ trạng thái |
| Cổng & app | bảng port (URL Boxmes, truy cập công khai/token/riêng, nhãn), tạo URL token, cấu hình app supervisor (repo, install, start, restart), tên miền riêng (P1) |
| Màn hình | noVNC nhúng + bảng **computer-use** (P1: nhật ký hành động screenshot/click/type) |
| Trình duyệt | CDP: URL kết nối, xem trực tiếp (P2 replay) |
| Snapshot | danh sách (thời điểm, dung lượng, nơi lưu), Restore, Fork, Xoá, chính sách giữ |
| Volumes | volume đang gắn, gắn/thả |
| Mạng | egress allowlist (thêm/xoá mục, kiểm tra cú pháp), bí mật qua proxy (P1: chọn secret → domain đích) |
| Metrics | CPU/RAM/disk/net 1h/24h (P1) |
| Git (P1) | repo đã clone, nhánh, thay đổi, Commit, Push, Mở PR |
| Sự kiện | timeline trạng thái + audit của box |
Viewer: tab Terminal/Tệp chỉ đọc hoặc ẩn; Lệnh ẩn output.

---

## 7. Agents (M3, M14) — [Sửa]
- Danh sách thẻ: tên, loại (icon), template mặc định, trạng thái, số box, chi hôm nay / ngân sách ngày, lần gọi cuối.
- Tạo agent (ngăn kéo): tên · loại (lưới 22 loại có tìm) · ngân sách ngày/tháng · chính sách (provider cho phép, egress mặc định, cần duyệt khi > $X) → màn **Kết nối**: snippet theo loại (MCP JSON / Python / TS / n8n…), key hiện **một lần** + nút copy, kiểm tra kết nối realtime ("Đang chờ agent gọi lần đầu…" → ✓).
- Chi tiết agent: tab Tổng quan (chi phí 30 ngày), Box, Key (xoay vòng, thu hồi), Chính sách, Hoạt động.

## 8. Cần anh (M14) — [Mới, tách trang]
- Hộp thư duyệt: lọc loại (ngân sách, mở rộng pool, service dừng, host down, lạm dụng [PA]); mỗi mục: mô tả, số liệu, ai/agent nào, hạn tự quyết; Duyệt / Từ chối / Ghi chú. Tab "Đã xử lý". Duyệt hàng loạt.

## 9. Templates & warm pool (M11) — [Sửa]
- Lưới template (dựng sẵn + của workspace): tên, ảnh nền/ngôn ngữ, cpu/ram, lần dùng. Editor box.yaml 2 cột (YAML | xem trước cấu hình + báo giá).
- **Warm pool (P1):** mỗi template bật "Giữ sẵn N box ở vùng X", hiển thị box sẵn sàng / đang dùng, chi phí giữ chỗ/ngày.

## 10. Volumes (M10) — [Mới, P1]
- Bảng: tên, dung lượng dùng/tối đa, vùng, gắn vào box nào, sao lưu cuối. Tạo volume (tên, cỡ, vùng). Chi tiết: duyệt file, snapshot volume, lịch sao lưu.

## 11. Secrets (M13) — [Sửa]
- Bảng: tên, phạm vi (workspace / agent cụ thể), dùng qua (env / proxy → domain), cập nhật lúc, ai tạo. Không bao giờ hiện giá trị; "Thay giá trị" mở form mới. Quyền: D dùng, A/O sửa.

## 12. Chi phí (M14, M15) — [Sửa]
- Bộ chọn khoảng 7/30/90 ngày, nhóm theo agent / box / provider / nhãn.
- Biểu đồ chi theo ngày; thẻ theo provider; gợi ý tiết kiệm (Áp dụng); gợi ý cỡ box; sổ chi tiết + Xuất CSV.
- Ngân sách workspace: trần tháng, cảnh báo 50/80/100%, hành động khi chạm trần (ngủ box / chỉ cảnh báo).

## 13. Developers (M3, M19, M21, M22, M23) — [Sửa]
Tab:
| Tab | Nội dung |
|---|---|
| API keys | bảng: tên, scope, agent, tạo bởi, dùng lần cuối (IP), hết hạn; tạo key (chọn scope bằng checkbox nhóm, hạn dùng), hiện một lần; thu hồi |
| Webhooks [Mới] | endpoint: URL, sự kiện, trạng thái; chi tiết: secret ký (hiện/ẩn, xoay), lịch sử gửi (mã HTTP, thời gian, body), Gửi lại, "Gửi thử" |
| Tích hợp | MCP (URL + nút thêm vào Claude/Cursor), A2A, CLI cài đặt, SDK Python/TS, **API tương thích E2B** (P1: base URL + hướng dẫn đổi 2 dòng) |
| Quan sát (P1) | đích OTLP (endpoint, header), log sink, nút kiểm tra |
| Git (P1) | kết nối GitHub App / GitLab, repo được phép |
| Tài liệu | link OpenAPI, llms.txt, ví dụ curl |

## 14. Cài đặt workspace (M2, M15, M16, M18–M20, M27) — [Mới phần lớn]
| Tab | Nội dung | Quyền |
|---|---|---|
| Chung | tên, slug, vùng dữ liệu mặc định, múi giờ báo cáo | O/A |
| Thành viên | bảng: người, email, vai trò (dropdown), lần đăng nhập cuối, MFA; nút "Mời thành viên" (email + vai trò, nhiều email); lời mời đang chờ (gửi lại, huỷ) | O/A sửa; khác xem |
| **Thanh toán** | thẻ số dư lớn + dự báo; nút gói nạp $5/$20/$50/$100/$500 (→ checkout Polar); tự nạp (bật, ngưỡng, số tiền); lịch sử giao dịch (nạp, trừ theo ngày, tặng, hoàn); hoá đơn (PDF); cổng quản lý thanh toán | O/B |
| Gói & hạn mức | thẻ gói hiện tại + so sánh gói (Free/Pro/Team/BYOC), nút Nâng cấp; bảng hạn mức đã dùng/tối đa; bậc xác minh ("Nạp $10 để mở URL công khai") | O/B xem đổi; khác xem |
| Audit | bảng: thời điểm, ai (user/key/agent), IP, hành động, đối tượng, kết quả; lọc; Xuất CSV/JSON | O/A/Au |
| Thông báo | kênh workspace: Telegram, email, Slack/Discord (P1); sự kiện nào gửi đâu; báo cáo ngày lúc mấy giờ | O/A |
| Provider của tôi (BYOC, P1) | kết nối token cloud riêng (Hetzner, Vultr, E2B…), host của tôi, phí nền tảng | O/A |
| Nguy hiểm | chuyển owner, xuất dữ liệu, đóng workspace (gõ tên xác nhận) | O |

## 15. Cá nhân — [Mới]
- **Hồ sơ:** tên, email (đổi = xác thực lại), ngôn ngữ, múi giờ, giao diện sáng/tối.
- **Bảo mật:** đổi mật khẩu; MFA (QR + mã khôi phục, P1); bảng phiên (thiết bị, IP, vị trí ước lượng, lần cuối, "Thu hồi"; "Đăng xuất mọi nơi").
- **Giới thiệu bạn bè:** link + mã, số người đã đăng ký / đã nạp, credit đã nhận, nút chia sẻ.

## 16. Hạ tầng (chỉ PA; M5, M6, M11) — [Có, chuyển vào khu Nền tảng]
Giữ các tab hiện có: VPS · Sandbox API · Sức khoẻ · Bảng giá · Tốc độ · Mesh · Adapter. Bổ sung: **Pool** (lấp đầy theo pool Free/Pro/Team, đề xuất thêm/bớt host), **Warm pool toàn hệ thống**, **Provider deprecated** (Runloop đánh dấu đã đóng), cột độ tin cậy p50/p95 boot và tỷ lệ lỗi.

**Hạ tầng v2 — Tài khoản nhà cung cấp (M29) [Mới, v2.2]** — chi tiết ở `docs/boxmes-infra-accounts-spec.md` §8:
- Tab **Tài khoản** (mặc định): dải KPI (tổng số dư khả dụng, chi hôm nay/tháng, dự báo cuối tháng, runway ngắn nhất "còn ~X ngày", account khoẻ/suy giảm/hỏng, cảnh báo mở) + bảng provider (số account, số dư, chi tháng, runway, host/box, % hạn mức cao nhất, sức khoẻ, chiến lược, badge cảnh báo).
- **Chi tiết provider** `/infra/providers/[id]`: năng lực (API số dư/hạn mức, rate limit, mô hình trả tiền), bảng tài khoản (trạng thái + lý do + cooldown, ưu tiên kéo thả/trọng số, số dư kèm nguồn API/ước tính/nhập tay, chi so với trần, runway, thanh hạn mức, hạn token/credit, host; hành động Kiểm tra · Drain · Tắt · Rotate · Sửa), chiến lược chọn, biểu đồ số dư/chi + dự báo.
- **Ngăn kéo tài khoản**: Tổng quan (nhập tay khi không có API) · Hạn mức · Tiền (snapshots, ledger, đối soát) · Host/Box · Dòng thời gian · Cảnh báo (ngưỡng riêng).
- **Luồng thêm/rotate khoá**: hướng dẫn quyền tối thiểu → dán khoá → validate (org/project, scopes, số dư, hạn mức, cảnh báo trùng tài khoản thật) → cấu hình → lưu; rotate bắt buộc cùng `external_id`.
- Tab cũ: Sức khoẻ thêm lọc/breaker theo account; VPS/Sandbox thêm cột "Tài khoản".
- **Trạng thái web (2026-11-28)**: đã có tab Tài khoản (mặc định `/infra`), `/infra/providers/[id]` (tài khoản, chiến lược 5 lựa chọn, thêm khoá 3 bước, ngăn kéo Tổng quan/Sửa/Khoá/Dòng thời gian, xoá có xác nhận, cảnh báo), tab Sandbox (M29-S: slot, nhóm, credit, ma trận template + Đồng bộ). Chưa có: biểu đồ số dư/chi, kéo thả ưu tiên, cột "Tài khoản" ở tab cũ, ngưỡng riêng theo tài khoản.

## 17. Admin nền tảng (M26, M18, M17) — [Sửa]
| Tab | Nội dung |
|---|---|
| Workspace [Mới] | bảng: tên, owner, gói, số dư, chi/thu tháng, box chạy, trạng thái, điểm rủi ro; hành động: xem như (impersonate, ghi audit), tặng credit, khoá/mở, đổi gói |
| Người dùng | bảng v2: email, xác thực, PA, workspace, tạo lúc, đăng nhập cuối; sửa PA, xoá |
| Mã mời [Mới UI] | tạo lô (số lượng, kênh, chiến dịch, max_uses, hạn, credit tặng), bảng mã (copy, xuất CSV, tạm dừng), funnel theo kênh |
| Danh sách chờ [Mới UI] | bảng: email, vị trí, nguồn, người giới thiệu, ngày; "Mời N người đầu", mời chọn lọc, xoá |
| Lạm dụng [Mới] | hàng chờ cảnh báo: loại (đào coin, quét cổng, phishing, báo cáo ngoài), workspace, box, bằng chứng (CPU, kết nối), hành động: ngủ box, khoá workspace, bỏ qua |
| Doanh thu [Mới] | doanh thu nạp, credit tiêu thụ, giá vốn provider, biên lợi nhuận theo ngày/provider/gói; cohort đăng ký → trả tiền |
| Hệ thống / Job / Audit / Tính năng / Quota | giữ như hiện có |

---

## 18. Trạng thái chung

| Trạng thái | Thiết kế |
|---|---|
| Đang tải | skeleton theo hình dạng nội dung, không spinner toàn trang |
| Rỗng | minh hoạ nhỏ + 1 câu + nút hành động ("Chưa có box. Kết nối agent hoặc tạo box thử") |
| Lỗi API | thẻ lỗi: message + hint (dịch) + nút theo `next` + mã lỗi nhỏ (copy) |
| Hết credit (`PAYMENT_REQUIRED`) | hộp thoại: số dư, gói nạp nhanh, link Thanh toán |
| Vượt hạn mức (`QUOTA_EXCEEDED`) | hộp thoại: đã dùng/tối đa, nút Nâng gói |
| Không đủ quyền | ẩn nút; nếu vào thẳng URL → trang "Anh cần vai trò Admin, hỏi Owner: <tên>" |
| Realtime | SSE `box.updated`, `approval.created`, `billing.balance` cập nhật không reload; chấm xanh "Trực tiếp" trên top bar |

## 19. Ma trận quyền (rút gọn)

| Hành động | O | A | D | B | V | Au | PA |
|---|---|---|---|---|---|---|---|
| Xem box/agent/chi phí | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| Tạo/xoá box, exec, terminal | ✓ | ✓ | ✓ | — | — | — | ✓ |
| Quản lý agent & key | ✓ | ✓ | của mình | — | — | — | ✓ |
| Secrets (sửa) | ✓ | ✓ | — | — | — | — | — |
| Thanh toán, gói | ✓ | — | — | ✓ | — | — | ✓ |
| Thành viên | ✓ | ✓ | — | — | — | — | ✓ |
| Audit | ✓ | ✓ | — | — | — | ✓ | ✓ |
| Dừng khẩn cấp | ✓ | ✓ | — | — | — | — | ✓ |
| Hạ tầng, Admin | — | — | — | — | — | — | ✓ |

## 20. Thứ tự hiện thực (khớp lộ trình spec)
- **v2.0:** §3.1–3.3, §2 bộ chọn workspace, §14 Chung + Thành viên, §15 Hồ sơ + Bảo mật (phiên), §13 API keys theo workspace, ẩn Hạ tầng/Admin với người không phải PA.
- **v2.1:** §14 Thanh toán + Gói & hạn mức, số dư top bar, hộp thoại PAYMENT_REQUIRED/QUOTA_EXCEEDED, §12 ngân sách workspace.
- **v2.2:** §13 Webhooks, §14 Audit + Thông báo, §17 Workspace + Lạm dụng + Mã mời + Danh sách chờ, §8 Cần anh.
- **v2.3:** §6.3 tab Code, Màn hình computer-use, Git, Metrics, Mạng proxy; §9 warm pool; §10 Volumes; §13 E2B-compat, Quan sát.
- **v2.4:** §3.4 waitlist/referral, landing/pricing/status, §15 Giới thiệu bạn bè, §14 BYOC, §17 Doanh thu.
