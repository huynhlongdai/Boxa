import { NextRequest, NextResponse } from "next/server";
import { CSRF_COOKIE, csrfCookieOptions, newCsrfToken, sessionKey, sessionMode, isMock } from "@/lib/server/config";
import { loadMe } from "@/lib/server/session";
import { requestLocale } from "@/i18n/server-locale";
import { handleMock } from "@/lib/mock/handler";
import type { Me, SessionInfo } from "@/lib/types";

export const dynamic = "force-dynamic";

/**
 * Thông tin phiên cho client: mock hay không, đã đăng nhập chưa, kiểu phiên, ai đang đăng nhập + quyền trong
 * workspace hiện tại (v2.0), URL WebSocket terminal. KHÔNG BAO GIỜ trả token phiên hay API key
 * (chỉ tiền tố của API key ở chế độ admin key để người vận hành nhận ra key nào).
 * Đồng thời cấp cookie double-submit boxmes_csrf nếu trình duyệt chưa có.
 */
export async function GET(req: NextRequest) {
  const key = sessionKey(req.cookies);
  const mock = await isMock();
  const locale = await requestLocale();
  const out: SessionInfo = { mock, authed: false, mode: null, key_prefix: null, ws_base: process.env.NEXT_PUBLIC_BOXMES_WS || null, me: null };
  let status = 200;
  if (mock) {
    const r = handleMock("GET", ["auth", "me"], new URLSearchParams(), {}, "", locale);
    out.authed = true;
    out.mode = "mock";
    out.me = r.body as Me;
  } else if (key) {
    out.mode = sessionMode(key);
    if (out.mode === "key") out.key_prefix = `${key.slice(0, 12)}…`;
    try {
      const r = await loadMe(key, locale);
      if (r.ok) {
        out.authed = true;
        out.me = r.me;
      } else if (r.status !== 401) {
        // Core trả lỗi khác (vd 5xx): vẫn coi là đã đăng nhập để không đá người dùng ra ngoài vì Core chập chờn.
        out.authed = true;
      }
    } catch {
      out.authed = true; // Core không liên lạc được: giữ phiên, trang sẽ hiện lỗi kết nối
      status = 200;
    }
  }
  const res = NextResponse.json(out, { status, headers: { "Cache-Control": "no-store" } });
  if (!req.cookies.get(CSRF_COOKIE)?.value) res.cookies.set(CSRF_COOKIE, newCsrfToken(), csrfCookieOptions(req));
  return res;
}
