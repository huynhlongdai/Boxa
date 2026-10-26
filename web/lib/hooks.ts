"use client";
import * as React from "react";
import useSWR, { useSWRConfig, type SWRConfiguration } from "swr";
import { toast } from "sonner";
import { errMsg } from "./api";

/** SWR với key là đường dẫn API (null = tạm không tải). */
export function useApi<T>(path: string | null, opts?: SWRConfiguration<T>) {
  return useSWR<T>(path, opts);
}

/** Chạy một mutation, hiện toast và revalidate các key liên quan. */
export function useAction() {
  const { mutate } = useSWRConfig();
  const [pending, setPending] = React.useState<string | null>(null);
  const run = React.useCallback(
    async <R,>(id: string, fn: () => Promise<R>, opts?: { success?: string | ((r: R) => string); revalidate?: (string | ((k: string) => boolean))[] }) => {
      setPending(id);
      try {
        const r = await fn();
        if (opts?.success) toast.success(typeof opts.success === "function" ? opts.success(r) : opts.success);
        for (const k of opts?.revalidate ?? []) {
          if (typeof k === "string") await mutate(k);
          else await mutate((key) => typeof key === "string" && k(key));
        }
        return r;
      } catch (e) {
        toast.error(errMsg(e));
        return undefined;
      } finally {
        setPending(null);
      }
    },
    [mutate],
  );
  return { run, pending };
}

export const startsWith = (p: string) => (k: string) => k.startsWith(p);

export function useMediaQuery(q: string) {
  const [m, setM] = React.useState(false);
  React.useEffect(() => {
    const mq = window.matchMedia(q);
    const on = () => setM(mq.matches);
    on();
    mq.addEventListener("change", on);
    return () => mq.removeEventListener("change", on);
  }, [q]);
  return m;
}
