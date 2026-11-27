"use client";
import Link from "next/link";
import { useTranslations } from "next-intl";
import { Box as BoxIcon, CalendarDays, PiggyBank, Wallet } from "lucide-react";
import { paths } from "@/lib/api";
import { useApi } from "@/lib/hooks";
import type { Overview } from "@/lib/types";
import { pct } from "@/lib/utils";
import { useFmt } from "@/lib/format";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { KpiCard } from "@/components/boxmes/budget";
import { ApprovalList } from "@/components/boxmes/approvals";
import { StackedCostChart } from "@/components/boxmes/charts";
import { EmergencyButton } from "@/components/boxmes/emergency";
import { ErrorBox, PageHeader } from "@/components/boxmes/page";
import { Badge } from "@/components/ui/badge";

export default function OverviewPage() {
  const { data, error, mutate } = useApi<Overview>(paths.overview);
  const t = useTranslations("overview");
  const { usd, pct: fpct } = useFmt();

  return (
    <>
      <PageHeader
        title={t("title")}
        desc={t("desc")}
        actions={<EmergencyButton />}
      />
      {error && <ErrorBox error={error} retry={() => mutate()} />}

      <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
        {data ? (
          <>
            <KpiCard
              label={t("spentToday")}
              icon={<Wallet />}
              value={<span>{usd(data.spent_today)} <span className="whitespace-nowrap text-sm font-normal text-muted-foreground">/ {usd(data.budget_day)}</span></span>}
              spent={data.spent_today}
              budget={data.budget_day}
              sub={t("ofDayBudget", { pct: fpct(pct(data.spent_today, data.budget_day)) })}
            />
            <KpiCard
              label={t("spentMonth")}
              icon={<CalendarDays />}
              value={<span>{usd(data.spent_month, 2)} <span className="whitespace-nowrap text-sm font-normal text-muted-foreground">/ {usd(data.budget_month, 0)}</span></span>}
              spent={data.spent_month}
              budget={data.budget_month}
              sub={t("ofMonthBudget", { pct: fpct(pct(data.spent_month, data.budget_month)) })}
            />
            <KpiCard
              label={t("runningSleeping")}
              icon={<BoxIcon />}
              value={<span><span className="text-st-running">{data.boxes_running}</span> <span className="text-muted-foreground">/ {data.boxes_sleeping}</span></span>}
              sub={<Link href="/boxes" className="hover:text-foreground">{t("seeBoxes")}</Link>}
            />
            <KpiCard
              label={t("saved")}
              icon={<PiggyBank />}
              tone="green"
              value={usd(data.saved_vs_247_usd, 2)}
              sub={t("savedDesc")}
            />
          </>
        ) : (
          Array.from({ length: 4 }).map((_, i) => <Skeleton key={i} className="h-[118px]" />)
        )}
      </div>

      <div className="mt-4 grid gap-4 lg:grid-cols-5">
        <Card className="lg:col-span-3">
          <CardHeader className="flex-row items-center justify-between space-y-0">
            <CardTitle className="flex items-center gap-2">
              {t("needsYou")}
              {data && data.approvals_pending > 0 && <Badge variant="yellow">{data.approvals_pending}</Badge>}
            </CardTitle>
          </CardHeader>
          <CardContent>
            <ApprovalList />
          </CardContent>
        </Card>

        <Card className="lg:col-span-2">
          <CardHeader className="flex-row items-center justify-between space-y-0">
            <CardTitle>{t("topTasks")}</CardTitle>
          </CardHeader>
          <CardContent>
            {data ? (
              data.top_tasks.length ? (
                <ol className="flex flex-col divide-y divide-border">
                  {data.top_tasks.map((task, i) => {
                    const max = data.top_tasks[0].cost_usd || 1;
                    return (
                      <li key={task.box_id} className="py-2.5">
                        <Link href={`/boxes/${task.box_id}`} className="group flex items-center gap-3">
                          <span className="w-4 text-xs text-muted-foreground">{i + 1}</span>
                          <div className="min-w-0 flex-1">
                            <p className="truncate font-mono text-[13px] group-hover:text-primary">{task.slug}</p>
                            <p className="text-xs text-muted-foreground">{task.agent_name}</p>
                            <div className="mt-1.5 h-1 rounded-full bg-surface-2">
                              <div className="h-1 rounded-full bg-primary/70" style={{ width: `${(task.cost_usd / max) * 100}%` }} />
                            </div>
                          </div>
                          <span className="font-mono text-[13px]">{usd(task.cost_usd)}</span>
                        </Link>
                      </li>
                    );
                  })}
                </ol>
              ) : (
                <p className="py-6 text-center text-sm text-muted-foreground">{t("noCostToday")}</p>
              )
            ) : (
              <Skeleton className="h-48" />
            )}
          </CardContent>
        </Card>
      </div>

      <Card className="mt-4">
        <CardHeader className="flex-row items-center justify-between space-y-0">
          <CardTitle>{t("cost7d")}</CardTitle>
          <Link href="/costs" className="shrink-0 text-[13px] text-primary hover:underline">{t("details")}</Link>
        </CardHeader>
        <CardContent>{data ? <StackedCostChart data={data.cost_7d} height={260} /> : <Skeleton className="h-[260px]" />}</CardContent>
      </Card>

    </>
  );
}
