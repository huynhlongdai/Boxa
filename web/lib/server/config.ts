import "server-only";

export const COOKIE_NAME = "boxa_key";

/** Base URL của Boxa Core (phía server). Ưu tiên BOXA_API, sau đó NEXT_PUBLIC_BOXA_API. */
export function coreBase(): string {
  return (process.env.BOXA_API || process.env.NEXT_PUBLIC_BOXA_API || "http://localhost:8080/v1").replace(/\/+$/, "");
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
 *  - BOXA_MOCK=1 → luôn mock
 *  - BOXA_MOCK=0 → không bao giờ mock
 *  - không đặt + môi trường dev + Core không liên lạc được → mock
 */
export async function isMock(): Promise<boolean> {
  const v = process.env.BOXA_MOCK;
  if (v === "1" || v === "true") return true;
  if (v === "0" || v === "false") return false;
  if (process.env.NODE_ENV === "production") return false;
  return !(await coreReachable());
}
