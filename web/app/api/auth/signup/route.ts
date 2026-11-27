// Đăng ký (v2.0): POST /v1/auth/signup của Core {email, password, name?, invite_code?, workspace?, workspace_invite?}.
// Thành công → token phiên vào cookie HttpOnly; trình duyệt nhận {ok, user, workspace, role}.
import { NextRequest } from "next/server";
import { isMock } from "@/lib/server/config";
import { coreUnreachable, corePost, csrfReject, jsonRes, publicAuthBody, setSessionCookies } from "@/lib/server/auth";
import { requestLocale } from "@/i18n/server-locale";

export const dynamic = "force-dynamic";

const FIELDS = ["email", "password", "name", "invite_code", "workspace", "workspace_invite"] as const;

export async function POST(req: NextRequest) {
  const locale = await requestLocale();
  const bad = await csrfReject(req, locale);
  if (bad) return bad;
  let raw: Record<string, unknown> = {};
  try {
    raw = (await req.json()) as Record<string, unknown>;
  } catch {
    /* noop */
  }
  const body: Record<string, string> = {};
  for (const f of FIELDS) {
    const v = raw[f];
    if (typeof v === "string" && v.trim()) body[f] = f === "password" ? v : v.trim();
  }
  if (await isMock()) return jsonRes(201, { ok: true, mock: true });
  let r;
  try {
    r = await corePost("/auth/signup", body, req, locale);
  } catch {
    return coreUnreachable(locale);
  }
  if (r.status !== 201 || typeof r.body?.token !== "string") return jsonRes(r.status === 201 ? 502 : r.status, r.body);
  const res = jsonRes(201, publicAuthBody(r.body));
  setSessionCookies(res, req, r.body.token as string, r.body.expires_at as string | undefined);
  return res;
}
