"use client";
import * as React from "react";
import Link from "next/link";
import { Download, FilterX, Lightbulb } from "lucide-react";
import { paths, type CostFilters } from "@/lib/api";
import { useApi } from "@/lib/hooks";
import type { Agent, Box, CostsResponse, List, Overview, Suggestion } from "@/lib/types";
import { AGENT_COLORS, dateTime, usd } from "@/lib/utils";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input, Select } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { Table, TBody, TD, TH, THead, TR } from "@/components/ui/table";
import { DonutChart, HBarChart, StackedCostChart } from "@/components/boxa/charts";
import { Empty, ErrorBox, LoadingRows, PageHeader } from "@/components/boxa/page";
import { MoneyBudget, BudgetBar } from "@/components/boxa/budget";
import { Mono } from "@/components/boxa/status";

const KIND_LABEL = { compute: "compute", storage: "lưu trữ", egress: "egress", snapshot: "snapshot" } as const;

export default function CostsPage() {
  const [f, setF] = React.useState<CostFilters>({});
  const set = (k: keyof CostFilters, v: string) => setF((x) => ({ ...x, [k]: v || undefined }));
  const { data, error, isLoading, mutate } = useApi<CostsResponse>(paths.costs(f));
  const { data: agents } = useApi<List<Agent>>(paths.agents);
  const { data: boxes } = useApi<List<Box>>(paths.boxes());
  const { data: ov } = useApi<Overview>(paths.overview);
  const { data: sugg } = useApi<List<Suggestion>>(paths.suggestions);
  const agentName = (id: string) => agents?.items.find((a) => a.id === id)?.name ?? id;
  const boxSlug = (id: string) => boxes?.items.find((b) => b.id === id)?.slug ?? id;
  const [limit, setLimit] = React.useState(50);
  const any = Object.values(f).some(Boolean);

  return (
    <>
      <PageHeader
        title="Chi phí"
        desc="Ledger từng khoản theo agent, task, provider và ngày."
        actions={<Button size="sm" variant="secondary" asChild><a href={paths.costsCsv(f)} download><Download /> Xuất CSV</a></Button>}
      />

      <div className="mb-4 grid gap-3 md:grid-cols-3">
        <Card className="p-4">
          <p className="text-xs text-muted-foreground">Tổng theo bộ lọc</p>
          <p className="mt-1 font-mono text-2xl font-semibold">{data ? usd(data.summary.total, 4) : "…"}</p>
          {ov && <p className="mt-1 text-xs text-muted-foreground">Tháng này <MoneyBudget spent={ov.spent_month} budget={ov.budget_month} /></p>}
          {ov && <BudgetBar spent={ov.spent_month} budget={ov.budget_month} className="mt-2" />}
        </Card>
        <Card className="p-4">
          <p className="text-xs text-muted-foreground">Pool (tháng)</p>
          <p className="mt-1 font-mono text-2xl font-semibold text-st-running">{data ? usd(data.summary.pool_usd) : "…"}</p>
          <p className="mt-1 text-xs text-muted-foreground">Phần chi phí host tháng phân bổ cho box</p>
        </Card>
        <Card className="p-4">
          <p className="text-xs text-muted-foreground">Burst (theo giờ)</p>
          <p className="mt-1 font-mono text-2xl font-semibold text-st-warning">{data ? usd(data.summary.burst_usd) : "…"}</p>
          <p className="mt-1 text-xs text-muted-foreground">Host thuê theo giờ khi pool đầy</p>
        </Card>
      </div>

      <div className="mb-4 grid gap-4 lg:grid-cols-3">
        <Card className="lg:col-span-2">
          <CardHeader><CardTitle>7 ngày theo agent</CardTitle></CardHeader>
          <CardContent>{ov ? <StackedCostChart data={ov.cost_7d} /> : <Skeleton className="h-[240px]" />}</CardContent>
        </Card>
        <Card>
          <CardHeader><CardTitle>Pool vs burst</CardTitle></CardHeader>
          <CardContent>
            {data ? <DonutChart data={[{ name: "Pool (tháng)", value: data.summary.pool_usd, color: "#22c55e" }, { name: "Burst (giờ)", value: data.summary.burst_usd, color: "#f5a524" }]} /> : <Skeleton className="h-[200px]" />}
          </CardContent>
        </Card>
      </div>

      <div className="mb-4 grid gap-4 lg:grid-cols-3">
        <Card>
          <CardHeader><CardTitle>Theo provider</CardTitle></CardHeader>
          <CardContent>
            {data ? <HBarChart data={Object.entries(data.summary.by_provider).sort((a, b) => b[1] - a[1]).map(([name, value]) => ({ name, value }))} /> : <Skeleton className="h-[200px]" />}
          </CardContent>
        </Card>
        <Card className="lg:col-span-2">
          <CardHeader><CardTitle className="flex items-center gap-2"><Lightbulb className="size-4 text-st-warning" /> Gợi ý tiết kiệm</CardTitle></CardHeader>
          <CardContent className="flex flex-col gap-2.5">
            {!sugg ? <LoadingRows rows={3} /> : !sugg.items.length ? <p className="text-sm text-muted-foreground">Chưa có gợi ý — mọi thứ đang tối ưu.</p> : sugg.items.map((s) => (
              <div key={s.id} className="flex items-start gap-3 rounded-lg border border-border bg-background p-3">
                <div className="min-w-0 flex-1">
                  <p className="text-sm font-medium">{s.title}</p>
                  <p className="mt-0.5 text-[13px] text-muted-foreground">{s.detail}</p>
                  {s.box_id && <Link href={`/boxes/${s.box_id}`} className="mt-1 inline-block text-[12.5px] text-primary hover:underline">Mở box →</Link>}
                </div>
                <Badge variant="green" className="shrink-0 font-mono">−{usd(s.saving_month_usd, 2)}/tháng</Badge>
              </div>
            ))}
          </CardContent>
        </Card>
      </div>

      <Card>
        <CardHeader className="gap-3">
          <CardTitle>Ledger</CardTitle>
          <div className="grid grid-cols-2 gap-2 md:flex md:flex-wrap">
            <Select value={f.agent ?? ""} onChange={(e) => set("agent", e.target.value)} aria-label="Agent">
              <option value="">Mọi agent</option>
              {agents?.items.map((a) => <option key={a.id} value={a.id}>{a.name}</option>)}
            </Select>
            <Select value={f.task ?? ""} onChange={(e) => set("task", e.target.value)} aria-label="Task / box">
              <option value="">Mọi task (box)</option>
              {boxes?.items.map((b) => <option key={b.id} value={b.id}>{b.slug}</option>)}
            </Select>
            <Select value={f.provider ?? ""} onChange={(e) => set("provider", e.target.value)} aria-label="Provider">
              <option value="">Mọi provider</option>
              {["hetzner", "vultr", "contabo", "digitalocean", "netcup"].map((p) => <option key={p} value={p}>{p}</option>)}
            </Select>
            <Input type="date" value={f.from ?? ""} onChange={(e) => set("from", e.target.value)} aria-label="Từ ngày" className="md:w-40" />
            <Input type="date" value={f.to ?? ""} onChange={(e) => set("to", e.target.value)} aria-label="Đến ngày" className="md:w-40" />
            {any && <Button size="sm" variant="ghost" onClick={() => setF({})}><FilterX /> Bỏ lọc</Button>}
          </div>
        </CardHeader>
        <CardContent className="px-0">
          {error ? <div className="px-4"><ErrorBox error={error} retry={() => mutate()} /></div> : isLoading || !data ? <div className="px-4"><LoadingRows /></div> : !data.items.length ? <div className="px-4"><Empty title="Không có khoản chi nào khớp bộ lọc" /></div> : (
            <>
              <Table>
                <THead><TR className="hover:bg-transparent"><TH>Thời gian</TH><TH>Agent</TH><TH>Box</TH><TH>Provider</TH><TH>Loại</TH><TH className="hidden lg:table-cell">Ghi chú</TH><TH className="text-right">Số tiền</TH></TR></THead>
                <TBody>
                  {data.items.slice(0, limit).map((l) => (
                    <TR key={l.id}>
                      <TD className="whitespace-nowrap text-[13px] text-muted-foreground">{dateTime(l.ts)}</TD>
                      <TD className="whitespace-nowrap text-[13px]">
                        <span className="mr-1.5 inline-block size-2 rounded-sm" style={{ background: AGENT_COLORS[[...(agents?.items ?? [])].map((a) => a.name).sort().indexOf(agentName(l.agent_id)) % AGENT_COLORS.length] }} />
                        {agentName(l.agent_id)}
                      </TD>
                      <TD><Link href={`/boxes/${l.box_id}`} className="hover:text-primary"><Mono>{boxSlug(l.box_id)}</Mono></Link></TD>
                      <TD><Mono>{l.provider}</Mono></TD>
                      <TD><Badge variant="outline">{KIND_LABEL[l.kind]}</Badge></TD>
                      <TD className="hidden max-w-[260px] truncate text-[13px] text-muted-foreground lg:table-cell">{l.note}</TD>
                      <TD className="text-right font-mono text-[13px]">{usd(l.amount_usd, 4)}</TD>
                    </TR>
                  ))}
                </TBody>
              </Table>
              {data.items.length > limit && (
                <div className="flex justify-center pt-3"><Button size="sm" variant="ghost" onClick={() => setLimit((n) => n + 50)}>Xem thêm ({data.items.length - limit})</Button></div>
              )}
            </>
          )}
        </CardContent>
      </Card>
    </>
  );
}
