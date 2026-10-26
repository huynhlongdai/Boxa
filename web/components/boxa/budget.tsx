"use client";
import { Card } from "@/components/ui/card";
import { Progress } from "@/components/ui/progress";
import { cn, pct } from "@/lib/utils";
import { useFmt } from "@/lib/format";

/** Tiền luôn đi kèm ngân sách: "$0.68 / $0.80". */
export function MoneyBudget({ spent, budget, className }: { spent: number; budget: number | null | undefined; className?: string }) {
  const { usd } = useFmt();
  const p = budget ? pct(spent, budget) : 0;
  return (
    <span className={cn("tabular font-mono text-[12.5px]", className)}>
      <span className={cn("whitespace-nowrap", p >= 100 ? "text-st-error" : p >= 80 ? "text-st-warning" : "text-foreground")}>{usd(spent)}</span>
      <span className="text-muted-foreground"> / <span className="whitespace-nowrap">{budget ? usd(budget) : "∞"}</span></span>
    </span>
  );
}

export function BudgetBar({ spent, budget, className }: { spent: number; budget: number | null | undefined; className?: string }) {
  if (!budget) return null;
  return <Progress value={pct(spent, budget)} className={className} />;
}

export function KpiCard({
  label,
  value,
  sub,
  spent,
  budget,
  icon,
  tone,
}: {
  label: string;
  value: React.ReactNode;
  sub?: React.ReactNode;
  spent?: number;
  budget?: number;
  icon?: React.ReactNode;
  tone?: "green";
}) {
  const p = spent !== undefined && budget ? pct(spent, budget) : null;
  return (
    <Card className="flex min-w-0 flex-col gap-2 p-4">
      <div className="flex items-start justify-between gap-2 text-[13px] text-muted-foreground">
        <span className="min-w-0">{label}</span>
        <span className="shrink-0 text-muted-foreground/70 [&_svg]:size-4">{icon}</span>
      </div>
      <div className={cn("tabular min-w-0 break-words text-xl font-semibold tracking-tight sm:text-2xl", tone === "green" && "text-st-running")}>{value}</div>
      {p !== null && <Progress value={p} />}
      {sub && <div className="text-xs text-muted-foreground">{sub}</div>}
    </Card>
  );
}
