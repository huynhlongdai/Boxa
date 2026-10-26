"use client";
import * as React from "react";
import Link from "next/link";
import { Plus } from "lucide-react";
import { paths } from "@/lib/api";
import { useApi } from "@/lib/hooks";
import type { Agent, List } from "@/lib/types";
import { relTime } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog";
import { Table, TBody, TD, TH, THead, TR } from "@/components/ui/table";
import { BudgetBar, MoneyBudget } from "@/components/boxa/budget";
import { AgentWizard } from "@/components/boxa/agent-wizard";
import { Empty, ErrorBox, LoadingRows, PageHeader } from "@/components/boxa/page";

export default function AgentsPage() {
  const { data, error, isLoading, mutate } = useApi<List<Agent>>(paths.agents);
  const [open, setOpen] = React.useState(false);
  const [key, setKey] = React.useState(0);
  return (
    <>
      <PageHeader
        title="Agents"
        desc="Mỗi agent có key, ngân sách và chính sách riêng."
        actions={
          <Dialog open={open} onOpenChange={(o) => { setOpen(o); if (!o) setKey((k) => k + 1); }}>
            <DialogTrigger asChild><Button size="sm"><Plus /> Tạo agent</Button></DialogTrigger>
            <DialogContent className="max-w-2xl">
              <DialogHeader>
                <DialogTitle>Tạo agent</DialogTitle>
                <DialogDescription>Ba bước, xong là có key và đoạn cấu hình để dán vào agent.</DialogDescription>
              </DialogHeader>
              <AgentWizard key={key} compact />
            </DialogContent>
          </Dialog>
        }
      />
      {error ? <ErrorBox error={error} retry={() => mutate()} /> : isLoading || !data ? <LoadingRows /> : !data.items.length ? (
        <Empty title="Chưa có agent nào" desc="Tạo agent đầu tiên để nhận API key." />
      ) : (
        <>
          <Card className="hidden md:block">
            <Table>
              <THead><TR className="hover:bg-transparent"><TH>Tên</TH><TH>Kind</TH><TH>Box đang có</TH><TH>Chi hôm nay / ngân sách</TH><TH>Chi tháng / ngân sách</TH><TH>Lần gọi cuối</TH></TR></THead>
              <TBody>
                {data.items.map((a) => (
                  <TR key={a.id}>
                    <TD><Link href={`/agents/${a.id}`} className="font-mono text-[13px] hover:text-primary">{a.name}</Link></TD>
                    <TD><Badge variant="outline" className="font-mono">{a.kind}</Badge></TD>
                    <TD className="font-mono text-[13px]">{a.box_count}</TD>
                    <TD className="min-w-[150px]"><MoneyBudget spent={a.spent_today_usd} budget={a.budget_day_usd} /><BudgetBar spent={a.spent_today_usd} budget={a.budget_day_usd} className="mt-1" /></TD>
                    <TD className="min-w-[150px]"><MoneyBudget spent={a.spent_month_usd} budget={a.budget_month_usd} /><BudgetBar spent={a.spent_month_usd} budget={a.budget_month_usd} className="mt-1" /></TD>
                    <TD className="text-[13px] text-muted-foreground">{relTime(a.last_call_at)}</TD>
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
                    <span className="font-mono text-sm">{a.name}</span>
                    <Badge variant="outline" className="font-mono">{a.kind}</Badge>
                  </div>
                  <div className="mt-3 flex items-center justify-between text-xs text-muted-foreground">
                    <span>{a.box_count} box · gọi {relTime(a.last_call_at)}</span>
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
