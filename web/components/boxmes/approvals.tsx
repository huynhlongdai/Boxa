"use client";
import { AlertTriangle, Check, HardDrive, PauseCircle, Server, X } from "lucide-react";
import { useTranslations } from "next-intl";
import { api, paths } from "@/lib/api";
import { useAction, useApi } from "@/lib/hooks";
import type { Approval, List } from "@/lib/types";
import { cn } from "@/lib/utils";
import { useFmt } from "@/lib/format";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Empty, ErrorBox, LoadingRows } from "./page";
import { useSession } from "@/lib/session";
import { PERMS } from "@/lib/types";

const ICON: Record<Approval["type"], React.ReactNode> = {
  budget: <AlertTriangle className="text-st-warning" />,
  service_stop: <PauseCircle className="text-st-error" />,
  pool_expand: <Server className="text-st-provisioning" />,
  host_down: <HardDrive className="text-st-error" />,
};

export function ApprovalItem({ a }: { a: Approval }) {
  const { run, pending } = useAction();
  const t = useTranslations("approvals");
  const { usd, relTime } = useFmt();
  const reval = [paths.approvals, paths.overview, paths.hosts, paths.agents];
  const { can } = useSession();
  return (
    <div className="flex flex-col gap-3 rounded-lg border border-border bg-background p-3.5 sm:flex-row sm:items-start">
      <div className="flex flex-1 gap-3">
        <span className="mt-0.5 [&_svg]:size-[18px]">{ICON[a.type] ?? ICON.budget}</span>
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2">
            <p className="font-medium leading-snug">{a.title}</p>
          </div>
          <p className="mt-1 text-[13px] text-muted-foreground">{a.detail}</p>
          <div className="mt-2 flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
            <Badge variant="outline">{ICON[a.type] ? t(`type.${a.type}`) : a.type}</Badge>
            {a.amount_usd !== null && <span className="font-mono text-foreground">{usd(a.amount_usd)}</span>}
            <span>· {relTime(a.created_at)}</span>
          </div>
        </div>
      </div>
      {a.status === "pending" && can(PERMS.approvals) ? (
        <div className="flex gap-2 sm:flex-col">
          <Button size="sm" variant="success" className="flex-1" disabled={!!pending} onClick={() => run("a", () => api.approve(a.id), { success: t("approved"), revalidate: reval })}>
            <Check /> {t("approve")}
          </Button>
          <Button size="sm" variant="outline" className="flex-1" disabled={!!pending} onClick={() => run("r", () => api.reject(a.id), { success: t("rejected"), revalidate: reval })}>
            <X /> {t("reject")}
          </Button>
        </div>
      ) : a.status === "pending" ? null : (
        <Badge variant={a.status === "approved" ? "green" : "gray"} className={cn("self-start")}>{a.status === "approved" ? t("approved") : t("rejected")}</Badge>
      )}
    </div>
  );
}

export function ApprovalList({ filter, emptyText }: { filter?: (a: Approval) => boolean; emptyText?: string }) {
  const { data, error, isLoading, mutate } = useApi<List<Approval>>(paths.approvals);
  const t = useTranslations("approvals");
  if (error) return <ErrorBox error={error} retry={() => mutate()} />;
  if (isLoading || !data) return <LoadingRows rows={2} />;
  const items = data.items.filter((a) => a.status === "pending").filter(filter ?? (() => true));
  if (!items.length) return <Empty title={emptyText ?? t("empty")} desc={t("emptyDesc")} />;
  return (
    <div className="flex flex-col gap-2.5">
      {items.map((a) => (
        <ApprovalItem key={a.id} a={a} />
      ))}
    </div>
  );
}
