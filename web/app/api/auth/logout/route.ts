import { NextResponse } from "next/server";
import { COOKIE_NAME, LEGACY_COOKIE_NAMES } from "@/lib/server/config";

export async function POST() {
  const res = NextResponse.json({ ok: true });
  res.cookies.set(COOKIE_NAME, "", { httpOnly: true, path: "/", maxAge: 0 });
  // Xoá cả cookie cũ (boxaz_key, boxa_key) để phiên trước đổi tên không "sống lại".
  for (const name of LEGACY_COOKIE_NAMES) res.cookies.set(name, "", { httpOnly: true, path: "/", maxAge: 0 });
  return res;
}
