import "server-only";
import { cookies } from "next/headers";
import { redirect } from "next/navigation";
import { COOKIE_NAME, isMock } from "./config";

/** Chặn trang khi chưa có API key (trừ MOCK MODE). */
export async function requireSession() {
  if (await isMock()) return;
  const jar = await cookies();
  if (!jar.get(COOKIE_NAME)?.value) redirect("/login");
}
