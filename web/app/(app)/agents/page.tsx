"use client";
import * as React from "react";
import Link from "next/link";
import { Plus } from "lucide-react";
import { useTranslations } from "next-intl";
import { paths } from "@/lib/api";
import { useApi } from "@/lib/hooks";
import type { Agent, List } from "@/lib/types";
import { useFmt } from "@/lib/format";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog";
import { Table, TBody, TD, TH, THead, TR } from "@/components/ui/table";
import { BudgetBar, MoneyBudget } from "@/components/boxaz/budget";
import { AgentWizard } from "@/components/boxaz/agent-wizard";
import { Empty, ErrorBox, LoadingRows, PageHeader } from "@/components/boxaz/page";

export default function AgentsPage() {
  const { data, error, isLoading, mutate } = useApi<List<Agent>>(paths.agents);
  const [open, setOpen] = React.useState(false);
  const [key, setKey] = React.useState(0);
  const t = useTranslations("agents");
  const tc = useTranslations("common");
  const { relTime } = useFmt();
  return (
    <>
      <PageHeader
        title={t("title")}
        desc={t("desc")}
        actions={
          <Dialog open={open} onOpenChange={(o) => { setOpen(o); if (!o) setKey((k) => k + 1); }}>
            <DialogTrigger asChild><Button size="sm"><Plus /> {t("create")}</Button></DialogTrigger>
            <DialogContent className="max-w-2xl">
              <DialogHeader>
                <DialogTitle>{t("create")}</DialogTitle>
                <DialogDescription>{t("createDesc")}</DialogDescription>
              </DialogHeader>
              <AgentWizard key={key} compact />
            </DialogContent>
          </Dialog>
        }
      />
      {error ? <ErrorBox error={error} retry={() => mutate()} /> : isLoading || !data ? <LoadingRows /> : !data.items.length ? (
        <Empty title={t("empty")} desc={t("emptyDesc")} />
      ) : (
        <>
          <Card className="hidden md:block">
            <Table>
              <THead><TR className="hover:bg-transparent"><TH>{tc("name")}</TH><TH>{t("colKind")}</TH><TH>{t("colBoxes")}</TH><TH>{t("colToday")}</TH><TH>{t("colMonth")}</TH><TH>{t("colLastCall")}</TH></TR></THead>
              <TBody>
                {data.items.map((a) => (
                  <TR key={a.id}>
                    <TD><Link href={`/agents/${a.id}`} className="font-mono text-[13px] hover:text-primary">{a.name}</Link></TD>
                    <TD><Badge variant="outline" className="font-mono">{a.kind}</Badge></TD>
                    <TD className="font-mono text-[13px]">{a.box_count}</TD>
                    <TD className="min-w-[150px]"><MoneyBudget spent={a.spent_today_usd} budget={a.budget_day_usd} /><BudgetBar spent={a.spent_today_usd} budget={a.budget_day_usd} className="mt-1" /></TD>
                    <TD className="min-w-[150px]"><MoneyBudget spent={a.spent_month_usd} budget={a.budget_month_usd} /><BudgetBar spent={a.spent_month_usd} budget={a.budget_month_usd} className="mt-1" /></TD>
                    <TD className="whitespace-nowrap text-[13px] text-muted-foreground">{relTime(a.last_call_at)}</TD>
                  </TR>
                ))}
              </TBody>
            </Table>
          </Card>
          <div className="flex flex-col gap-2.5 md:hidden">
            {data.items.map((a) => (
              <Link key={a.id} href={`/agents/${a.id}`}>
                <Card className="p-3.5">
                  <div className="flex items-center justify-between">
                    <span className="min-w-0 truncate font-mono text-sm">{a.name}</span>
                    <Badge variant="outline" className="font-mono">{a.kind}</Badge>
                  </div>
                  <div className="mt-3 flex items-center justify-between text-xs text-muted-foreground">
                    <span>{t("mobileLine", { boxes: tc("boxCount", { n: a.box_count }), last: relTime(a.last_call_at) })}</span>
                    <MoneyBudget spent={a.spent_today_usd} budget={a.budget_day_usd} />
                  </div>
                  <BudgetBar spent={a.spent_today_usd} budget={a.budget_day_usd} className="mt-2" />
                </Card>
              </Link>
            ))}
          </div>
        </>
      )}
    </>
  );
}
