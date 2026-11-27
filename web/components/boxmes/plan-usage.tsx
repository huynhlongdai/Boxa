"use client";
// v2.0 Cài đặt › Gói & hạn mức (spec §14): gói hiện tại, đã dùng / tối đa từng hạn mức, so sánh gói.
// Đổi gói: người vận hành nền tảng (PATCH /v1/admin/workspaces/{id}); thanh toán tự phục vụ có ở v2.1.
import { useTranslations } from "next-intl";
import { Gauge, Layers } from "lucide-react";
import { paths } from "@/lib/api";
import { useApi } from "@/lib/hooks";
import { useFmt } from "@/lib/format";
import { useQuotaLabel } from "@/lib/error-toast";
import type { List, PlanInfo, Quota, WorkspaceUsage } from "@/lib/types";
import { cn } from "@/lib/utils";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Table, TBody, TD, TH, THead, TR } from "@/components/ui/table";
import { ErrorBox, LoadingRows } from "./page";
import { usePlanLabel } from "./workspace";

type CountKey = "boxes" | "agents" | "keys" | "secrets" | "members";
const COUNTED: { used: CountKey; quota: keyof Quota }[] = [
  { used: "boxes", quota: "max_boxes" },
  { used: "agents", quota: "max_agents" },
  { used: "keys", quota: "max_keys" },
  { used: "secrets", quota: "max_secrets" },
  { used: "members", quota: "max_members" },
];
const LIMITS: (keyof Quota)[] = ["max_cpu_per_box", "max_ram_mb_per_box", "max_ttl_hours", "max_boxes_per_minute", "public_url", "egress"];

function useQuotaValue() {
  const t = useTranslations("quota");
  return (k: keyof Quota, v: Quota[keyof Quota]) => {
    if (typeof v === "boolean") return v ? t("yes") : t("no");
    if (k === "egress") return v === "open" ? t("egressOpen") : t("egressRestricted");
    if (v === 0) return t("unlimited");
    if (k === "max_ram_mb_per_box") return t("gb", { n: Math.round((Number(v) / 1024) * 10) / 10 });
    if (k === "max_cpu_per_box") return t("vcpu", { n: Number(v) });
    if (k === "max_ttl_hours") return t("hours", { n: Number(v) });
    return String(v);
  };
}

function Bar({ used, max }: { used: number; max: number }) {
  const p = max > 0 ? Math.min(100, (used / max) * 100) : 0;
  return (
    <span className="block h-1.5 w-full overflow-hidden rounded-full bg-surface-2">
      <span className={cn("block h-full rounded-full", p >= 100 ? "bg-st-error" : p >= 80 ? "bg-st-warning" : "bg-primary")} style={{ width: `${max > 0 ? p : 0}%` }} />
    </span>
  );
}

export function PlanUsageTab() {
  const t = useTranslations("quota");
  const label = useQuotaLabel();
  const value = useQuotaValue();
  const planLabel = usePlanLabel();
  const { usd } = useFmt();
  const { data: u, error, mutate } = useApi<WorkspaceUsage>(paths.workspaceUsage);
  const { data: plans } = useApi<List<PlanInfo>>(paths.plans, { refreshInterval: 0 });
  if (error) return <ErrorBox error={error} retry={() => mutate()} />;
  if (!u) return <LoadingRows rows={6} />;
  const shown = (plans?.items ?? []).filter((p) => p.id !== "unlimited" || p.id === u.plan);
  return (
    <div className="flex flex-col gap-4">
      <Card>
        <CardHeader>
          <CardTitle className="flex flex-wrap items-center gap-2"><Gauge className="size-4" /> {t("title")} <Badge variant="blue" data-plan={u.plan}>{planLabel(u.plan)}</Badge></CardTitle>
          <CardDescription>{t("desc")}</CardDescription>
        </CardHeader>
        <CardContent className="flex flex-col gap-5">
          <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-5">
            {COUNTED.map((c) => {
              const max = Number(u.quota[c.quota]);
              const used = u.used[c.used];
              const p = max > 0 ? (used / max) * 100 : 0;
              return (
                <div key={c.used} className="flex flex-col gap-1.5 rounded-md border border-border p-3" data-quota-row={c.quota}>
                  <span className="text-[12.5px] text-muted-foreground">{label(c.quota)}</span>
                  <span className={cn("font-mono text-lg", p >= 100 ? "text-st-error" : p >= 80 ? "text-st-warning" : "")}>
                    {used}
                    <span className="text-[13px] text-muted-foreground"> / {max > 0 ? max : "∞"}</span>
                  </span>
                  <Bar used={used} max={max} />
                </div>
              );
            })}
          </div>
          <div className="grid gap-x-6 gap-y-2 sm:grid-cols-2 lg:grid-cols-3">
            {LIMITS.map((k) => (
              <div key={k} className="flex items-center justify-between gap-3 border-b border-border/60 py-1.5 text-[13px]">
                <span className="text-muted-foreground">{label(k)}</span>
                <span className="font-mono">{value(k, u.quota[k])}</span>
              </div>
            ))}
            <div className="flex items-center justify-between gap-3 border-b border-border/60 py-1.5 text-[13px]">
              <span className="text-muted-foreground">{t("spentToday")}</span>
              <span className="font-mono">{usd(u.used.spent_today_usd)}</span>
            </div>
            <div className="flex items-center justify-between gap-3 border-b border-border/60 py-1.5 text-[13px]">
              <span className="text-muted-foreground">{t("spentMonth")}</span>
              <span className="font-mono">{usd(u.used.spent_month_usd)}</span>
            </div>
          </div>
          <p className="text-[12.5px] text-muted-foreground">{t("sleepingNote", { total: u.used.boxes_total, running: u.used.boxes })}</p>
        </CardContent>
      </Card>

      {shown.length > 0 && (
        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2"><Layers className="size-4" /> {t("compare")}</CardTitle>
            <CardDescription>{t("compareDesc")}</CardDescription>
          </CardHeader>
          <CardContent className="px-0 sm:px-0">
            <Table>
              <THead>
                <TR className="hover:bg-transparent">
                  <TH>{t("limit")}</TH>
                  {shown.map((p) => (
                    <TH key={p.id} className={cn(p.id === u.plan && "text-primary")}>{planLabel(p.id)}{p.id === u.plan ? ` · ${t("current")}` : ""}</TH>
                  ))}
                </TR>
              </THead>
              <TBody>
                {[...COUNTED.map((c) => c.quota), ...LIMITS].map((k) => (
                  <TR key={k}>
                    <TD className="text-[13px] text-muted-foreground">{label(k)}</TD>
                    {shown.map((p) => (
                      <TD key={p.id} className={cn("font-mono text-[12.5px]", p.id === u.plan && "bg-primary/5")}>{value(k, p.quota[k])}</TD>
                    ))}
                  </TR>
                ))}
              </TBody>
            </Table>
          </CardContent>
        </Card>
      )}
    </div>
  );
}
