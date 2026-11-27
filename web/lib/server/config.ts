import "server-only";

export const COOKIE_NAME = "boxmes_key";
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
