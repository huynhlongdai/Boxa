import "server-only";

export const COOKIE_NAME = "boxaz_key";
/** Cookie trước khi đổi tên Boxa → Boxaz; vẫn đọc để phiên cũ không bị đăng xuất. */
export const LEGACY_COOKIE_NAME = "boxa_key";

type Jar = { get(name: string): { value: string } | undefined };

/** Khoá API trong cookie phiên (tên mới, rồi tên cũ). */
export function sessionKey(jar: Jar): string | undefined {
  return jar.get(COOKIE_NAME)?.value || jar.get(LEGACY_COOKIE_NAME)?.value;
}

/** Base URL của Boxaz Core (phía server). Ưu tiên BOXAZ_API, sau đó NEXT_PUBLIC_BOXAZ_API. */
export function coreBase(): string {
  return (process.env.BOXAZ_API || process.env.NEXT_PUBLIC_BOXAZ_API || "http://localhost:8080/v1").replace(/\/+$/, "");
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
 *  - BOXAZ_MOCK=1 → luôn mock
 *  - BOXAZ_MOCK=0 → không bao giờ mock
 *  - không đặt + môi trường dev + Core không liên lạc được → mock
 */
export async function isMock(): Promise<boolean> {
  const v = process.env.BOXAZ_MOCK;
  if (v === "1" || v === "true") return true;
  if (v === "0" || v === "false") return false;
  if (process.env.NODE_ENV === "production") return false;
  return !(await coreReachable());
}
