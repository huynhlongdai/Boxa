"use client";
// v2.0: phiên phía client — ai đang đăng nhập, workspace hiện tại, quyền (permissions[]) để ẩn/hiện nav và nút.
// Nguồn: route Next /api/session (không bao giờ chứa token). Server vẫn là nơi quyết định quyền;
// ẩn nút chỉ để người dùng không bấm vào thứ chắc chắn bị 403.
import * as React from "react";
import useSWR, { useSWRConfig } from "swr";
import { api } from "./api";
import type { Me, Perm, SessionInfo } from "./types";

export const SESSION_KEY = "__session";

async function fetchSession(): Promise<SessionInfo> {
  const r = await fetch("/api/session", { cache: "no-store", credentials: "same-origin" });
  return (await r.json()) as SessionInfo;
}

export function useSession() {
  const { data, mutate, isLoading } = useSWR<SessionInfo>(SESSION_KEY, fetchSession, { refreshInterval: 120_000, revalidateOnFocus: true, dedupingInterval: 3000 });
  const me: Me | null = data?.me ?? null;
  const can = React.useCallback(
    (perm: Perm | string) => {
      if (!data) return false;
      if (data.mock) return true;
      // Core không trả lời được /auth/me: không ẩn gì cả, để server tự từ chối nếu thiếu quyền.
      if (data.authed && !me) return true;
      return Boolean(me?.platform_admin || me?.permissions.includes(perm));
    },
    [data, me],
  );
  return {
    session: data ?? null,
    me,
    loading: isLoading,
    platformAdmin: Boolean(data?.mock || me?.platform_admin || (data?.authed && !me && data.mode === "key")),
    can,
    refresh: () => mutate(),
  };
}

/** Đổi workspace hiện tại rồi tải lại mọi dữ liệu (mọi khoá SWR). */
export function useSwitchWorkspace() {
  const { mutate } = useSWRConfig();
  return React.useCallback(
    async (id: string) => {
      const me = await api.switchWorkspace(id);
      await mutate(SESSION_KEY);
      await mutate((k) => typeof k === "string" && k !== SESSION_KEY, undefined, { revalidate: true });
      return me;
    },
    [mutate],
  );
}

/** Hiện children khi có quyền perm (hoặc fallback). */
export function Can({ perm, children, fallback = null }: { perm: Perm; children: React.ReactNode; fallback?: React.ReactNode }) {
  const { can } = useSession();
  return React.createElement(React.Fragment, null, can(perm) ? children : fallback);
}
