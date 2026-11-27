// Đăng nhập dashboard. Hai chế độ:
//  - {email, password}: POST /v1/auth/login của Core → token phiên bxs_… vào cookie HttpOnly (v2.0, mặc định)
//  - {key}: dán API key (admin key / key workspace) như trước — kiểm với Core rồi lưu vào cùng cookie
// Trình duyệt không bao giờ nhận token.
import { NextRequest } from "next/server";
import { getTranslations } from "next-intl/server";
import { coreBase, isMock } from "@/lib/server/config";
import { coreUnreachable, corePost, csrfReject, jsonRes, publicAuthBody, setSessionCookies } from "@/lib/server/auth";
import { requestLocale } from "@/i18n/server-locale";

export const dynamic = "force-dynamic";

export async function POST(req: NextRequest) {
  const locale = await requestLocale();
  const bad = await csrfReject(req, locale);
  if (bad) return bad;
  const t = await getTranslations({ locale, namespace: "apiErrors" });
  let body: { key?: string; email?: string; password?: string } = {};
  try {
    body = (await req.json()) as typeof body;
  } catch {
    /* noop */
  }
  const mock = await isMock();

  if (body.email !== undefined || body.password !== undefined) {
    const email = (body.email ?? "").trim();
    if (!email || !body.password) {
      return jsonRes(400, { error: { code: "INVALID", message: t("missingCredentials"), hint: t("missingCredentialsHint") } });
    }
    if (mock) return jsonRes(200, { ok: true, mock: true });
    let r;
    try {
      r = await corePost("/auth/login", { email, password: body.password }, req, locale);
    } catch {
      return coreUnreachable(locale);
    }
    if (r.status !== 200 || typeof r.body?.token !== "string") return jsonRes(r.status === 200 ? 502 : r.status, r.body);
    const res = jsonRes(200, publicAuthBody(r.body));
    setSessionCookies(res, req, r.body.token as string, r.body.expires_at as string | undefined);
    return res;
  }

  const key = (body.key ?? "").trim();
  if (!key) {
    return jsonRes(400, { error: { code: "INVALID", message: t("missingKey"), hint: t("missingKeyHint") } });
  }
  if (!mock) {
    // Kiểm tra key với Core trước khi lưu cookie
    try {
      const res = await fetch(`${coreBase()}/overview`, { headers: { Authorization: `Bearer ${key}`, "Accept-Language": locale }, cache: "no-store" });
      if (res.status === 401 || res.status === 403) {
        return jsonRes(401, { error: { code: "UNAUTHORIZED", message: t("invalidKey"), hint: t("invalidKeyHint") } });
      }
    } catch {
      return coreUnreachable(locale);
    }
  }
  const res = jsonRes(200, { ok: true });
  setSessionCookies(res, req, key);
  return res;
}
