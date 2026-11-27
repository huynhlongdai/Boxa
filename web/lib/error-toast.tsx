"use client";
// v1.3: toast lỗi hiện hint + error.next (các lệnh gợi ý tiếp theo từ Core) để người dùng/agent biết làm gì tiếp.
import { toast } from "sonner";
import { useTranslations } from "next-intl";
import { ApiError } from "./api";

function ErrorDetails({ hint, next, code }: { hint?: string; next: string[]; code?: string }) {
  const t = useTranslations("apiErrors");
  return (
    <div className="flex min-w-0 flex-col gap-1.5">
      {hint && <p className="break-words">{hint}</p>}
      {next.length > 0 && (
        <div data-error-next>
          <p className="text-[11.5px] font-medium opacity-80">{t("tryNext")}</p>
          <ul className="mt-0.5 flex flex-col gap-0.5">
            {next.slice(0, 3).map((n) => (
              <li key={n} className="break-all font-mono text-[11.5px] leading-4">{n}</li>
            ))}
          </ul>
        </div>
      )}
      {code && <p className="font-mono text-[10.5px] opacity-60">{code}</p>}
    </div>
  );
}

export const QUOTA_KEYS = ["max_boxes", "max_cpu_per_box", "max_ram_mb_per_box", "max_ttl_hours", "public_url", "egress", "max_members", "max_agents", "max_keys", "max_secrets", "max_boxes_per_minute"] as const;

export function useQuotaLabel() {
  const t = useTranslations("quota");
  return (k: string) => ((QUOTA_KEYS as readonly string[]).includes(k) ? t(`names.${k}` as "names.max_boxes") : k);
}

function QuotaTitle() {
  const t = useTranslations("quota");
  return <>{t("exceededTitle")}</>;
}

/** v2.0: QUOTA_EXCEEDED thân thiện — hạn mức nào, đã dùng/tối đa, gói hiện tại, gợi ý tiếp theo + nút xem gói. */
function QuotaDetails({ e }: { e: ApiError }) {
  const t = useTranslations("quota");
  const ta = useTranslations("apiErrors");
  const label = useQuotaLabel();
  const tp = useTranslations("plans");
  const planName = (p: string) => (["free", "pro", "team", "unlimited"].includes(p) ? tp(p as "free") : p);
  const d = (e.details ?? {}) as { quota?: string; limit?: number | boolean; used?: number; plan?: string; upgrade_url?: string };
  const q = d.quota ?? "";
  const limit = typeof d.limit === "boolean" ? (d.limit ? t("yes") : t("no")) : d.limit;
  return (
    <div className="flex min-w-0 flex-col gap-1.5" data-quota={q}>
      <p className="break-words font-medium">
        {d.used !== undefined && limit !== undefined
          ? t("exceededUsed", { what: label(q), used: d.used, limit: String(limit) })
          : t("exceededLimit", { what: label(q), limit: String(limit ?? "?") })}
        {d.plan ? ` · ${t("onPlan", { plan: planName(d.plan) })}` : ""}
      </p>
      {e.hint && <p className="break-words">{e.hint}</p>}
      {e.next.length > 0 && (
        <div data-error-next>
          <p className="text-[11.5px] font-medium opacity-80">{ta("tryNext")}</p>
          <ul className="mt-0.5 flex flex-col gap-0.5">
            {e.next.slice(0, 3).map((n) => (
              <li key={n} className="break-all font-mono text-[11.5px] leading-4">{n}</li>
            ))}
          </ul>
        </div>
      )}
      <a href="/settings/billing" className="mt-0.5 self-start rounded border border-current/30 px-2 py-0.5 text-[12px] font-medium underline-offset-2 hover:underline">
        {t("seePlan")}
      </a>
    </div>
  );
}

/** Hiện toast lỗi; ApiError có hint/next thì hiện thêm phần gợi ý. QUOTA_EXCEEDED có toast riêng. */
export function toastError(e: unknown) {
  if (e instanceof ApiError && e.code === "QUOTA_EXCEEDED") {
    toast.error(<QuotaTitle />, { description: <QuotaDetails e={e} />, duration: 15000 });
    return;
  }
  if (e instanceof ApiError) {
    const hasMore = !!e.hint || e.next.length > 0;
    toast.error(e.message, hasMore ? { description: <ErrorDetails hint={e.hint} next={e.next} code={e.code} />, duration: e.next.length ? 12000 : 6000 } : undefined);
    return;
  }
  toast.error(e instanceof Error ? e.message : String(e));
}
