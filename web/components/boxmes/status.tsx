"use client";
import { useTranslations } from "next-intl";
import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";
import type { BoxStatus, Host, Lifecycle, Offer, Provider } from "@/lib/types";

type Tone = "green" | "gray" | "blue" | "yellow" | "red" | "outline" | "default";

const BOX: Record<BoxStatus, Tone> = {
  requested: "blue",
  placing: "blue",
  provisioning: "blue",
  running: "green",
  sleeping: "gray",
  snapshotting: "blue",
  terminated: "outline",
  failed: "red",
};
export const BOX_STATUSES = Object.keys(BOX) as BoxStatus[];

const DOT: Record<Tone, string> = {
  green: "bg-st-running",
  gray: "bg-st-sleeping",
  blue: "bg-st-provisioning",
  yellow: "bg-st-warning",
  red: "bg-st-error",
  outline: "bg-muted-foreground",
  default: "bg-muted-foreground",
};

export function Dot({ tone, pulse }: { tone: Tone; pulse?: boolean }) {
  return (
    <span className="relative inline-flex size-1.5 shrink-0">
      {pulse && <span className={cn("absolute inline-flex h-full w-full animate-ping rounded-full opacity-60", DOT[tone])} />}
      <span className={cn("relative inline-flex size-1.5 rounded-full", DOT[tone])} />
    </span>
  );
}

/** Nhãn trạng thái box theo ngôn ngữ hiện tại. */
export function useBoxStatusLabel() {
  const t = useTranslations("status.box");
  return (s: BoxStatus) => (BOX[s] ? t(s) : s);
}

export function BoxStatusChip({ status, className }: { status: BoxStatus; className?: string }) {
  const label = useBoxStatusLabel();
  const tone = BOX[status] ?? "default";
  return (
    <Badge variant={tone} className={cn("whitespace-nowrap", className)}>
      <Dot tone={tone} pulse={["provisioning", "placing", "requested", "snapshotting"].includes(status)} />
      {label(status)}
    </Badge>
  );
}

const HOST: Record<Host["status"], Tone> = { provisioning: "blue", ready: "green", draining: "yellow", down: "red" };
export function HostStatusChip({ status }: { status: Host["status"] }) {
  const t = useTranslations("status.host");
  const tone = HOST[status] ?? "default";
  return (
    <Badge variant={tone} className="whitespace-nowrap">
      <Dot tone={tone} pulse={status === "provisioning"} />
      {HOST[status] ? t(status) : status}
    </Badge>
  );
}

export function ProviderChip({ p }: { p: Provider }) {
  const t = useTranslations("status.provider");
  if (p.error) return <Badge variant="red" className="whitespace-nowrap"><Dot tone="red" />{t("error")}</Badge>;
  if (p.connected) return <Badge variant="green" className="whitespace-nowrap"><Dot tone="green" />{t("connected")}</Badge>;
  return <Badge variant="gray" className="whitespace-nowrap"><Dot tone="gray" />{t("disconnected")}</Badge>;
}

const CONF: Record<Offer["confidence"], Tone> = { verified: "green", unverified: "yellow", stock_limited: "blue" };
export function ConfidenceBadge({ c }: { c: Offer["confidence"] }) {
  const t = useTranslations("status.confidence");
  if (!CONF[c]) return <Badge variant="outline">{c}</Badge>;
  return <Badge variant={CONF[c]} title={t(`${c}Hint`)} className="whitespace-nowrap">{t(c)}</Badge>;
}

export function LifecycleBadge({ lc }: { lc: Lifecycle }) {
  return <Badge variant="outline" className="font-mono">{lc}</Badge>;
}

export function Mono({ children, className }: { children: React.ReactNode; className?: string }) {
  return <span className={cn("font-mono text-[12.5px]", className)}>{children}</span>;
}
