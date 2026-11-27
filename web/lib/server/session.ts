import "server-only";
// v2.0: ai đang đăng nhập và được làm gì trong workspace hiện tại — dựng phía server, không bao giờ trả token.
//  - phiên (bxs_…): GET /v1/auth/me của Core
//  - API key (chế độ admin key cũ hoặc key workspace): không có user; quyền lấy từ GET /v1/workspaces/current,
//    "quản trị nền tảng" = gọi được GET /v1/admin/system.
import type { CurrentWorkspace, Me, Workspace } from "@/lib/types";
import { PERMS } from "@/lib/types";
import { coreBase, sessionMode } from "./config";

const ALL_PERMS = Object.values(PERMS) as string[];

async function coreGet(path: string, token: string, lang: string): Promise<{ status: number; body: unknown }> {
  const res = await fetch(`${coreBase()}${path}`, { headers: { Authorization: `Bearer ${token}`, "Accept-Language": lang }, cache: "no-store" });
  let body: unknown = null;
  try {
    body = await res.json();
  } catch {
    body = null;
  }
  return { status: res.status, body };
}

export type MeResult = { ok: true; me: Me } | { ok: false; status: number; body: unknown };

/** Đọc Me cho token trong cookie. status 401 = phiên/key không còn hợp lệ. */
export async function loadMe(token: string, lang: string): Promise<MeResult> {
  if (sessionMode(token) === "session") {
    const r = await coreGet("/auth/me", token, lang);
    if (r.status !== 200) return { ok: false, status: r.status, body: r.body };
    const me = r.body as Me;
    return { ok: true, me: { ...me, workspaces: me.workspaces ?? [], permissions: me.permissions ?? [] } };
  }
  const [sys, cur] = await Promise.all([coreGet("/admin/system", token, lang), coreGet("/workspaces/current", token, lang)]);
  if (sys.status === 401 && cur.status === 401) return { ok: false, status: 401, body: cur.body };
  const platform = sys.status === 200;
  const ws = cur.status === 200 ? (cur.body as CurrentWorkspace) : null;
  const workspace: Workspace | null = ws ? { id: ws.id, name: ws.name, plan: ws.plan, status: ws.status, created_at: ws.created_at, owner_user_id: ws.owner_user_id, role: ws.role || "" } : null;
  return {
    ok: true,
    me: {
      user: null,
      workspace,
      workspaces: workspace ? [workspace] : [],
      role: ws?.role || "",
      permissions: platform ? ALL_PERMS : ws?.permissions ?? [],
      platform_admin: platform,
    },
  };
}
