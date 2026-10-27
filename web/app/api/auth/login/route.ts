import { NextRequest, NextResponse } from "next/server";
import { getTranslations } from "next-intl/server";
import { COOKIE_NAME, coreBase, isMock } from "@/lib/server/config";
import { requestLocale } from "@/i18n/server-locale";

export const dynamic = "force-dynamic";

export async function POST(req: NextRequest) {
  const locale = await requestLocale();
  const t = await getTranslations({ locale, namespace: "apiErrors" });
  let key = "";
  try {
    const body = (await req.json()) as { key?: string };
    key = (body.key ?? "").trim();
  } catch {
    /* noop */
  }
  if (!key) {
    return NextResponse.json({ error: { code: "INVALID", message: t("missingKey"), hint: t("missingKeyHint") } }, { status: 400 });
  }
  if (!(await isMock())) {
    // Kiểm tra key với Core trước khi lưu cookie
    try {
      const res = await fetch(`${coreBase()}/overview`, { headers: { Authorization: `Bearer ${key}`, "Accept-Language": locale }, cache: "no-store" });
      if (res.status === 401 || res.status === 403) {
        return NextResponse.json({ error: { code: "UNAUTHORIZED", message: t("invalidKey"), hint: t("invalidKeyHint") } }, { status: 401 });
      }
    } catch {
      return NextResponse.json({ error: { code: "PROVIDER_FAILED", message: t("coreUnreachable"), hint: t("coreUnreachableHint", { url: coreBase() }) } }, { status: 502 });
    }
  }
  const res = NextResponse.json({ ok: true });
  res.cookies.set(COOKIE_NAME, key, {
    httpOnly: true,
    sameSite: "lax",
    secure: process.env.NODE_ENV === "production" && process.env.BOXAZ_INSECURE_COOKIE !== "1",
    path: "/",
    maxAge: 60 * 60 * 24 * 30,
  });
  return res;
}
