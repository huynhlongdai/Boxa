import "server-only";
import { cookies, headers } from "next/headers";
import { localeCookie, resolveLocale, type Locale } from "./config";

/** Locale của request hiện tại (dùng trong route handler). */
export async function requestLocale(): Promise<Locale> {
  const jar = await cookies();
  const h = await headers();
  return resolveLocale(localeCookie(jar), h.get("accept-language"));
}
