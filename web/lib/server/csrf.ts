import "server-only";
// v2.0 chống CSRF cho mọi request đổi trạng thái đi qua Next (proxy /api/boxmes/* và /api/auth/*).
//
// Ba lớp, lớp nào cũng đủ chặn kiểu tấn công thường gặp; gộp lại để một lớp hỏng không mở toang:
//  1. Cookie phiên SameSite=Lax: trình duyệt không gửi cookie phiên theo POST/PUT/PATCH/DELETE từ site khác.
//  2. Kiểm Origin (rơi về Referer): request đổi trạng thái có Origin/Referer phải trùng Host (hoặc X-Forwarded-Host)
//     của chính dashboard, hoặc nằm trong BOXMES_DASHBOARD_ORIGINS. Origin khác → 403, dù có cookie hay không
//     (chặn luôn "login CSRF": ép nạn nhân đăng nhập vào tài khoản của kẻ tấn công).
//  3. Double-submit: khi request mang cookie phiên, header X-Boxmes-CSRF phải bằng cookie boxmes_csrf
//     (SameSite=Strict, chỉ JS cùng origin đọc được). Request có cookie phiên mà thiếu cả Origin lẫn Referer
//     cũng phải qua lớp này, nên công cụ cũ / proxy bóc Origin không thành lỗ hổng.
// Request không mang cookie phiên (curl, smoke test MOCK) không có "quyền xung quanh" để lợi dụng, nên chỉ
// bị lớp 2 chặn khi Origin sai.
import type { NextRequest } from "next/server";
import { CSRF_COOKIE, CSRF_HEADER, sessionKey } from "./config";

const SAFE = new Set(["GET", "HEAD", "OPTIONS"]);

function hostOf(u: string | null): string | null {
  if (!u || u === "null") return u === "null" ? "null" : null;
  try {
    return new URL(u).host.toLowerCase();
  } catch {
    return "invalid";
  }
}

function allowedHosts(req: NextRequest): Set<string> {
  const out = new Set<string>();
  for (const h of [req.headers.get("host"), req.headers.get("x-forwarded-host")]) {
    for (const part of (h ?? "").split(",")) {
      const v = part.trim().toLowerCase();
      if (v) out.add(v);
    }
  }
  for (const o of (process.env.BOXMES_DASHBOARD_ORIGINS ?? process.env.BOXMES_DASHBOARD_URL ?? "").split(",")) {
    const h = hostOf(o.trim());
    if (h && h !== "invalid" && h !== "null") out.add(h);
  }
  return out;
}

function timingSafeEqual(a: string, b: string): boolean {
  if (a.length !== b.length) return false;
  let r = 0;
  for (let i = 0; i < a.length; i++) r |= a.charCodeAt(i) ^ b.charCodeAt(i);
  return r === 0;
}

export type CsrfFailure = "origin" | "token";

/** Trả lý do từ chối, hoặc null nếu request an toàn để chuyển tiếp. */
export function csrfCheck(req: NextRequest): CsrfFailure | null {
  if (SAFE.has(req.method.toUpperCase())) return null;
  const origin = hostOf(req.headers.get("origin"));
  const source = origin ?? hostOf(req.headers.get("referer"));
  if (source !== null && !allowedHosts(req).has(source)) return "origin";
  const hasSession = Boolean(sessionKey(req.cookies));
  if (hasSession) {
    const cookie = req.cookies.get(CSRF_COOKIE)?.value ?? "";
    const header = req.headers.get(CSRF_HEADER) ?? "";
    if (!cookie || !header || !timingSafeEqual(cookie, header)) return "token";
  }
  return null;
}
