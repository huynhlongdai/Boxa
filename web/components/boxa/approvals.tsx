"use client";
import { AlertTriangle, Check, HardDrive, PauseCircle, Server, X } from "lucide-react";
import { api, paths } from "@/lib/api";
import { useAction, useApi } from "@/lib/hooks";
import type { Approval, List } from "@/lib/types";
import { cn, relTime, usd } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Empty, ErrorBox, LoadingRows } from "./page";

const ICON: Record<Approval["type"], React.ReactNode> = {
  budget: <AlertTriangle className="text-st-warning" />,
  service_stop: <PauseCircle className="text-st-error" />,
  pool_expand: <Server className="text-st-provisioning" />,
  host_down: <HardDrive className="text-st-error" />,
};
const TYPE_LABEL: Record<Approval["type"], string> = {
  budget: "Ngân sách",
  service_stop: "Service sắp dừng",
  pool_expand: "Mở rộng pool",
  host_down: "Host lỗi",
};

export function ApprovalItem({ a }: { a: Approval }) {
  const { run, pending } = useAction();
  const reval = [paths.approvals, paths.overview, paths.hosts, paths.agents];
  return (
    <div className="flex flex-col gap-3 rounded-lg border border-border bg-background p-3.5 sm:flex-row sm:items-start">
      <div className="flex flex-1 gap-3">
        <span className="mt-0.5 [&_svg]:size-[18px]">{ICON[a.type]}</span>
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2">
            <p className="font-medium leading-snug">{a.title}</p>
          </div>
          <p className="mt-1 text-[13px] text-muted-foreground">{a.detail}</p>
          <div className="mt-2 flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
            <Badge variant="outline">{TYPE_LABEL[a.type]}</Badge>
            {a.amount_usd !== null && <span className="font-mono text-foreground">{usd(a.amount_usd)}</span>}
            <span>· {relTime(a.created_at)}</span>
          </div>
        </div>
      </div>
      {a.status === "pending" ? (
        <div className="flex gap-2 sm:flex-col">
          <Button size="sm" variant="success" className="flex-1" disabled={!!pending} onClick={() => run("a", () => api.approve(a.id), { success: "Đã duyệt", revalidate: reval })}>
            <Check /> Duyệt
          </Button>
          <Button size="sm" variant="outline" className="flex-1" disabled={!!pending} onClick={() => run("r", () => api.reject(a.id), { success: "Đã từ chối", revalidate: reval })}>
            <X /> Từ chối
          </Button>
        </div>
      ) : (
        <Badge variant={a.status === "approved" ? "green" : "gray"} className={cn("self-start")}>{a.status === "approved" ? "Đã duyệt" : "Đã từ chối"}</Badge>
      )}
    </div>
  );
}

export function ApprovalList({ filter, emptyText = "Không có gì cần anh lúc này." }: { filter?: (a: Approval) => boolean; emptyText?: string }) {
  const { data, error, isLoading, mutate } = useApi<List<Approval>>(paths.approvals);
  if (error) return <ErrorBox error={error} retry={() => mutate()} />;
  if (isLoading || !data) return <LoadingRows rows={2} />;
  const items = data.items.filter((a) => a.status === "pending").filter(filter ?? (() => true));
  if (!items.length) return <Empty title={emptyText} desc="Khi agent vượt 80% ngân sách, service sắp bị dừng hay cần mua thêm host, yêu cầu sẽ hiện ở đây." />;
  return (
    <div className="flex flex-col gap-2.5">
      {items.map((a) => (
        <ApprovalItem key={a.id} a={a} />
      ))}
    </div>
  );
}
