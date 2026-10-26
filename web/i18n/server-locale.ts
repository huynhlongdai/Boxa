import "server-only";
import { cookies, headers } from "next/headers";
import { LOCALE_COOKIE, resolveLocale, type Locale } from "./config";

/** Locale của request hiện tại (dùng trong route handler). */
export async function requestLocale(): Promise<Locale> {
  const jar = await cookies();
  const h = await headers();
  return resolveLocale(jar.get(LOCALE_COOKIE)?.value, h.get("accept-language"));
}
