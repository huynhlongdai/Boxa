"use client";
// Cập nhật trực tiếp qua SSE /stream (đi qua proxy), tự rơi về polling khi mất kết nối.
import * as React from "react";
import { SWRConfig, useSWRConfig } from "swr";
import { fetcher, PROXY_BASE } from "@/lib/api";
import type { Overview } from "@/lib/types";

interface Session {
  mock: boolean;
  authed: boolean;
  key_prefix: string | null;
  ws_base: string | null;
}
interface LiveCtx {
  connected: boolean;
  session: Session | null;
  lastEventAt: number | null;
}
const Ctx = React.createContext<LiveCtx>({ connected: false, session: null, lastEventAt: null });
export const useLive = () => React.useContext(Ctx);

function StreamBridge({ onState }: { onState: (c: boolean, t?: number) => void }) {
  const { mutate } = useSWRConfig();
  React.useEffect(() => {
    if (typeof window === "undefined" || !("EventSource" in window)) return;
    let es: EventSource | null = null;
    let retry: ReturnType<typeof setTimeout> | null = null;
    let closed = false;
    const connect = () => {
      es = new EventSource(`${PROXY_BASE}/stream`);
      es.onopen = () => onState(true);
      es.onerror = () => {
        onState(false);
        es?.close();
        if (!closed) retry = setTimeout(connect, 8000);
      };
      es.addEventListener("overview.updated", (e) => {
        try {
          const data = JSON.parse((e as MessageEvent).data) as Overview;
          mutate("/overview", data, { revalidate: false });
        } catch {
          /* noop */
        }
        onState(true, Date.now());
      });
      es.addEventListener("box.updated", (e) => {
        try {
          const data = JSON.parse((e as MessageEvent).data) as { id: string };
          mutate((k) => typeof k === "string" && (k.startsWith("/boxes?") || k === "/boxes" || k === `/boxes/${data.id}`));
        } catch {
          /* noop */
        }
        onState(true, Date.now());
      });
      es.addEventListener("event.created", (e) => {
        try {
          const data = JSON.parse((e as MessageEvent).data) as { box_id: string | null };
          if (data.box_id) mutate(`/boxes/${data.box_id}/events`);
          mutate((k) => typeof k === "string" && k.startsWith("/admin/audit"));
        } catch {
          /* noop */
        }
        onState(true, Date.now());
      });
      es.addEventListener("approval.created", () => {
        mutate("/approvals");
        mutate("/overview");
        onState(true, Date.now());
      });
    };
    connect();
    return () => {
      closed = true;
      if (retry) clearTimeout(retry);
      es?.close();
    };
  }, [mutate, onState]);
  return null;
}

export function LiveProvider({ children }: { children: React.ReactNode }) {
  const [connected, setConnected] = React.useState(false);
  const [lastEventAt, setLast] = React.useState<number | null>(null);
  const [session, setSession] = React.useState<Session | null>(null);
  React.useEffect(() => {
    fetch("/api/session", { cache: "no-store" })
      .then((r) => r.json())
      .then((s: Session) => setSession(s))
      .catch(() => setSession(null));
  }, []);
  const onState = React.useCallback((c: boolean, t?: number) => {
    setConnected(c);
    if (t) setLast(t);
  }, []);
  return (
    <SWRConfig
      value={{
        fetcher,
        // SSE sống → polling thưa; mất SSE → polling 10s
        refreshInterval: connected ? 60_000 : 10_000,
        revalidateOnFocus: true,
        dedupingInterval: 2000,
        shouldRetryOnError: (err: { status?: number }) => err?.status !== 401 && err?.status !== 404,
      }}
    >
      <Ctx.Provider value={{ connected, session, lastEventAt }}>
        <StreamBridge onState={onState} />
        {children}
      </Ctx.Provider>
    </SWRConfig>
  );
}
