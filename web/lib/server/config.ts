import "server-only";

export const COOKIE_NAME = "boxmes_key";
/**
 * v2.0: cookie double-submit chống CSRF. KHÔNG httpOnly (JS phía client đọc để gửi lại trong header
 * X-Boxmes-CSRF), SameSite=Strict, không chứa bí mật nào ngoài một số ngẫu nhiên gắn với trình duyệt.
 */
export const CSRF_COOKIE = "boxmes_csrf";
export const CSRF_HEADER = "x-boxmes-csrf";
/** Tiền tố token phiên đăng nhập (email + mật khẩu) của Core; còn lại là API key (chế độ admin key). */
export const SESSION_PREFIX = "bxs_";
/**
 * Cookie trước các lần đổi tên (Boxaz → Boxmes, rồi Boxa → Boxaz), mới nhất trước;
 * vẫn đọc để phiên cũ không bị đăng xuất, và bị xoá khi đăng xuất.
 */
export const LEGACY_COOKIE_NAMES = ["boxaz_key", "boxa_key"] as const;

type Jar = { get(name: string): { value: string } | undefined };

/** Khoá API trong cookie phiên: boxmes_key → boxaz_key → boxa_key. */
export function sessionKey(jar: Jar): string | undefined {
  for (const name of [COOKIE_NAME, ...LEGACY_COOKIE_NAMES]) {
    const v = jar.get(name)?.value;
    if (v) return v;
  }
  return undefined;
}

/** Base URL của Boxmes Core (phía server). Ưu tiên BOXMES_API, sau đó NEXT_PUBLIC_BOXMES_API. */
export function coreBase(): string {
  return (process.env.BOXMES_API || process.env.NEXT_PUBLIC_BOXMES_API || "http://localhost:8080/v1").replace(/\/+$/, "");
}

let cache: { at: number; reachable: boolean } | null = null;

async function coreReachable(): Promise<boolean> {
  if (cache && Date.now() - cache.at < 30_000) return cache.reachable;
  let reachable = false;
  try {
    const ctl = new AbortController();
    const t = setTimeout(() => ctl.abort(), 800);
    const res = await fetch(`${coreBase()}/health`, { signal: ctl.signal, cache: "no-store" });
    clearTimeout(t);
    reachable = res.ok || res.status < 500;
  } catch {
    reachable = false;
  }
  cache = { at: Date.now(), reachable };
  return reachable;
}

/**
 * MOCK MODE:
 *  - BOXMES_MOCK=1 → luôn mock
 *  - BOXMES_MOCK=0 → không bao giờ mock
 *  - không đặt + môi trường dev + Core không liên lạc được → mock
 */
export async function isMock(): Promise<boolean> {
  const v = process.env.BOXMES_MOCK;
  if (v === "1" || v === "true") return true;
  if (v === "0" || v === "false") return false;
  if (process.env.NODE_ENV === "production") return false;
  return !(await coreReachable());
}

/** Phiên là token đăng nhập (bxs_…) hay API key dán tay. */
export function sessionMode(key: string | undefined): "session" | "key" | null {
  if (!key) return null;
  return key.startsWith(SESSION_PREFIX) ? "session" : "key";
}

type ReqLike = { headers: { get(name: string): string | null }; nextUrl?: { protocol: string } };

/**
 * Cờ Secure của cookie phiên: luôn bật, trừ khi BOXMES_INSECURE_COOKIE=1 hoặc đang chạy dev qua http
 * trên một host không phải localhost (trình duyệt vẫn nhận cookie Secure trên http://localhost).
 */
export function secureCookie(req?: ReqLike): boolean {
  if (process.env.BOXMES_INSECURE_COOKIE === "1") return false;
  if (process.env.NODE_ENV === "production" || !req) return true;
  const proto = req.headers.get("x-forwarded-proto") ?? req.nextUrl?.protocol?.replace(/:$/, "") ?? "http";
  if (proto === "https") return true;
  const host = (req.headers.get("host") ?? "").replace(/:\d+$/, "");
  return host === "localhost" || host === "127.0.0.1" || host === "[::1]";
}

/** Tuỳ chọn cookie phiên: HttpOnly, SameSite=Lax, Secure (xem secureCookie), path /. */
export function sessionCookieOptions(req?: ReqLike, maxAge = 60 * 60 * 24 * 30) {
  return { httpOnly: true, sameSite: "lax" as const, secure: secureCookie(req), path: "/", maxAge };
}

/** Tuỳ chọn cookie CSRF: đọc được từ JS, SameSite=Strict. */
export function csrfCookieOptions(req?: ReqLike) {
  return { httpOnly: false, sameSite: "strict" as const, secure: secureCookie(req), path: "/", maxAge: 60 * 60 * 24 * 30 };
}

/** Số ngẫu nhiên 128 bit cho cookie CSRF. */
export function newCsrfToken(): string {
  const b = new Uint8Array(16);
  crypto.getRandomValues(b);
  return Array.from(b, (x) => x.toString(16).padStart(2, "0")).join("");
}
