"use client";
import { useTranslations } from "next-intl";
import { AlertTriangle, Inbox } from "lucide-react";
import { Skeleton } from "@/components/ui/skeleton";
import { cn } from "@/lib/utils";

export function PageHeader({ title, desc, actions, className }: { title: React.ReactNode; desc?: React.ReactNode; actions?: React.ReactNode; className?: string }) {
  return (
    <div className={cn("mb-5 flex flex-col gap-3 sm:flex-row sm:items-end sm:justify-between", className)}>
      <div className="min-w-0">
        <h1 className="text-xl font-semibold tracking-tight sm:text-2xl">{title}</h1>
        {desc && <p className="mt-1 max-w-2xl text-sm text-muted-foreground">{desc}</p>}
      </div>
      {actions && <div className="flex flex-wrap items-center gap-2">{actions}</div>}
    </div>
  );
}

export function SectionTitle({ children, right, className }: { children: React.ReactNode; right?: React.ReactNode; className?: string }) {
  return (
    <div className={cn("mb-3 flex flex-wrap items-center justify-between gap-2", className)}>
      <h2 className="text-[15px] font-semibold">{children}</h2>
      {right}
    </div>
  );
}

export function Empty({ title, desc, action }: { title: string; desc?: string; action?: React.ReactNode }) {
  return (
    <div className="flex flex-col items-center justify-center gap-2 rounded-lg border border-dashed border-border px-6 py-10 text-center">
      <Inbox className="size-6 text-muted-foreground" />
      <p className="text-sm font-medium">{title}</p>
      {desc && <p className="max-w-sm text-[13px] text-muted-foreground">{desc}</p>}
      {action}
    </div>
  );
}

export function ErrorBox({ error, retry }: { error: unknown; retry?: () => void }) {
  const t = useTranslations("common");
  const msg = error instanceof Error ? error.message : String(error);
  const hint = (error as { hint?: string })?.hint;
  return (
    <div className="flex items-start gap-3 rounded-lg border border-st-error/30 bg-st-error/5 p-4 text-sm">
      <AlertTriangle className="mt-0.5 size-4 shrink-0 text-st-error" />
      <div className="min-w-0 flex-1">
        <p className="font-medium text-st-error">{t("loadError")}</p>
        <p className="mt-0.5 break-words text-muted-foreground">{msg}</p>
        {hint && <p className="mt-0.5 break-words text-[13px] text-muted-foreground/80">{hint}</p>}
      </div>
      {retry && (
        <button className="shrink-0 text-[13px] text-primary hover:underline" onClick={retry}>
          {t("reload")}
        </button>
      )}
    </div>
  );
}

export function LoadingRows({ rows = 4 }: { rows?: number }) {
  return (
    <div className="flex flex-col gap-2">
      {Array.from({ length: rows }).map((_, i) => (
        <Skeleton key={i} className="h-10 w-full" />
      ))}
    </div>
  );
}
