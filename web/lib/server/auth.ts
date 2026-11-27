import "server-only";
// v2.0: các route /api/auth/* gọi Core rồi giữ token phiên trong cookie HttpOnly; trình duyệt chỉ nhận {ok, user, workspace}.
import { NextRequest, NextResponse } from "next/server";
import { getTranslations } from "next-intl/server";
import { COOKIE_NAME, CSRF_COOKIE, LEGACY_COOKIE_NAMES, coreBase, csrfCookieOptions, newCsrfToken, sessionCookieOptions } from "./config";
import { csrfCheck } from "./csrf";
import type { Locale } from "@/i18n/config";

export const jsonRes = (status: number, body: unknown) => NextResponse.json(body, { status, headers: { "Cache-Control": "no-store" } });

/** 403 nếu request đổi trạng thái không qua được lớp chống CSRF (xem lib/server/csrf.ts). */
export async function csrfReject(req: NextRequest, locale: Locale): Promise<NextResponse | null> {
  const why = csrfCheck(req);
  if (!why) return null;
  const t = await getTranslations({ locale, namespace: "apiErrors" });
  return jsonRes(403, { error: { code: "POLICY_DENIED", message: t(why === "origin" ? "csrfOrigin" : "csrfToken"), hint: t("csrfHint") } });
}

/** Gọi Core (JSON) từ phía server. Trả status + body đã parse. */
export async function corePost(path: string, body: unknown, req: NextRequest, locale: Locale, token?: string) {
  const headers: Record<string, string> = { "Content-Type": "application/json", "Accept-Language": locale };
  if (token) headers.Authorization = `Bearer ${token}`;
  const ua = req.headers.get("user-agent");
  if (ua) headers["User-Agent"] = ua;
  const xff = req.headers.get("x-forwarded-for");
  if (xff) headers["X-Forwarded-For"] = xff;
  const res = await fetch(`${coreBase()}${path}`, { method: "POST", headers, body: JSON.stringify(body ?? {}), cache: "no-store" });
  let data: unknown = null;
  try {
    data = await res.json();
  } catch {
    data = null;
  }
  return { status: res.status, body: data as Record<string, unknown> | null };
}

export async function coreUnreachable(locale: Locale) {
  const t = await getTranslations({ locale, namespace: "apiErrors" });
  return jsonRes(502, { error: { code: "PROVIDER_FAILED", message: t("coreUnreachable"), hint: t("coreUnreachableHint", { url: coreBase() }) } });
}

/** Đặt cookie phiên (token Core) + cookie CSRF mới (xoay sau mỗi lần đăng nhập). */
export function setSessionCookies(res: NextResponse, req: NextRequest, token: string, expiresAt?: string) {
  let maxAge = 60 * 60 * 24 * 30;
  if (expiresAt) {
    const s = Math.floor((Date.parse(expiresAt) - Date.now()) / 1000);
    if (Number.isFinite(s) && s > 60) maxAge = s;
  }
  res.cookies.set(COOKIE_NAME, token, sessionCookieOptions(req, maxAge));
  for (const name of LEGACY_COOKIE_NAMES) res.cookies.set(name, "", { httpOnly: true, path: "/", maxAge: 0 });
  res.cookies.set(CSRF_COOKIE, newCsrfToken(), csrfCookieOptions(req));
}

export function clearSessionCookies(res: NextResponse) {
  res.cookies.set(COOKIE_NAME, "", { httpOnly: true, path: "/", maxAge: 0 });
  // Xoá cả cookie cũ (boxaz_key, boxa_key) để phiên trước đổi tên không "sống lại".
  for (const name of LEGACY_COOKIE_NAMES) res.cookies.set(name, "", { httpOnly: true, path: "/", maxAge: 0 });
}

/** Bỏ token khỏi trả lời đăng nhập/đăng ký của Core trước khi đưa cho trình duyệt. */
export function publicAuthBody(body: Record<string, unknown> | null) {
  const { token: _token, ...rest } = body ?? {};
  void _token;
  return { ok: true, ...rest };
}
