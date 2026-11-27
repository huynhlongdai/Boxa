import { NextRequest } from "next/server";
import { sessionKey, sessionMode, isMock } from "@/lib/server/config";
import { clearSessionCookies, corePost, csrfReject, jsonRes } from "@/lib/server/auth";
import { requestLocale } from "@/i18n/server-locale";

export const dynamic = "force-dynamic";

/** Đăng xuất: huỷ phiên ở Core (nếu là phiên bxs_; ?all=1 = mọi thiết bị) rồi xoá cookie. API key thì chỉ xoá cookie. */
export async function POST(req: NextRequest) {
  const locale = await requestLocale();
  const bad = await csrfReject(req, locale);
  if (bad) return bad;
  const key = sessionKey(req.cookies);
  if (key && sessionMode(key) === "session" && !(await isMock())) {
    const all = req.nextUrl.searchParams.get("all") === "1";
    try {
      await corePost(`/auth/logout${all ? "?all=1" : ""}`, {}, req, locale, key);
    } catch {
      /* Core không trả lời: vẫn xoá cookie phía trình duyệt */
    }
  }
  const res = jsonRes(200, { ok: true });
  clearSessionCookies(res);
  return res;
}
