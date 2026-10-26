import { Card } from "@/components/ui/card";
import { Progress } from "@/components/ui/progress";
import { cn, pct, usd } from "@/lib/utils";

/** Tiền luôn đi kèm ngân sách: "$0.68 / $0.80". */
export function MoneyBudget({ spent, budget, className }: { spent: number; budget: number | null | undefined; className?: string }) {
  const p = budget ? pct(spent, budget) : 0;
  return (
    <span className={cn("tabular font-mono text-[12.5px]", className)}>
      <span className={cn(p >= 100 ? "text-st-error" : p >= 80 ? "text-st-warning" : "text-foreground")}>{usd(spent)}</span>
      <span className="text-muted-foreground"> / {budget ? usd(budget) : "∞"}</span>
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
    <Card className="flex flex-col gap-2 p-4">
      <div className="flex items-center justify-between text-[13px] text-muted-foreground">
        <span>{label}</span>
        <span className="text-muted-foreground/70 [&_svg]:size-4">{icon}</span>
      </div>
      <div className={cn("tabular text-2xl font-semibold tracking-tight", tone === "green" && "text-st-running")}>{value}</div>
      {p !== null && <Progress value={p} />}
      {sub && <div className="text-xs text-muted-foreground">{sub}</div>}
    </Card>
  );
}
