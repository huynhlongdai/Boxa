"use client";
import * as React from "react";
import Link from "next/link";
import { Download, FilterX, Lightbulb } from "lucide-react";
import { useTranslations } from "next-intl";
import { paths, type CostFilters } from "@/lib/api";
import { useApi } from "@/lib/hooks";
import type { Agent, Box, CostsResponse, List, Overview, Suggestion } from "@/lib/types";
import { AGENT_COLORS } from "@/lib/utils";
import { useFmt } from "@/lib/format";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input, Select } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { Table, TBody, TD, TH, THead, TR } from "@/components/ui/table";
import { DonutChart, HBarChart, StackedCostChart } from "@/components/boxmes/charts";
import { Empty, ErrorBox, LoadingRows, PageHeader } from "@/components/boxmes/page";
import { MoneyBudget, BudgetBar } from "@/components/boxmes/budget";
import { Mono } from "@/components/boxmes/status";
import { RightsizeList } from "@/components/boxmes/speed";


export default function CostsPage() {
  const t = useTranslations("costs");
  const tc = useTranslations("common");
  const { usd, dateTime } = useFmt();
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
        title={t("title")}
        desc={t("desc")}
        actions={<Button size="sm" variant="secondary" asChild><a href={paths.costsCsv(f)} download><Download /> {t("exportCsv")}</a></Button>}
      />

      <div className="mb-4 grid gap-3 md:grid-cols-3">
        <Card className="p-4">
          <p className="text-xs text-muted-foreground">{t("totalFiltered")}</p>
          <p className="mt-1 font-mono text-2xl font-semibold">{data ? usd(data.summary.total, 4) : "…"}</p>
          {ov && <p className="mt-1 text-xs text-muted-foreground">{t("thisMonth")} <MoneyBudget spent={ov.spent_month} budget={ov.budget_month} /></p>}
          {ov && <BudgetBar spent={ov.spent_month} budget={ov.budget_month} className="mt-2" />}
        </Card>
        <Card className="p-4">
          <p className="text-xs text-muted-foreground">{t("pool")}</p>
          <p className="mt-1 font-mono text-2xl font-semibold text-st-running">{data ? usd(data.summary.pool_usd) : "…"}</p>
          <p className="mt-1 text-xs text-muted-foreground">{t("poolDesc")}</p>
        </Card>
        <Card className="p-4">
          <p className="text-xs text-muted-foreground">{t("burst")}</p>
          <p className="mt-1 font-mono text-2xl font-semibold text-st-warning">{data ? usd(data.summary.burst_usd) : "…"}</p>
          <p className="mt-1 text-xs text-muted-foreground">{t("burstDesc")}</p>
        </Card>
      </div>

      <div className="mb-4 grid gap-4 lg:grid-cols-3">
        <Card className="lg:col-span-2">
          <CardHeader><CardTitle>{t("byAgent7d")}</CardTitle></CardHeader>
          <CardContent>{ov ? <StackedCostChart data={ov.cost_7d} /> : <Skeleton className="h-[240px]" />}</CardContent>
        </Card>
        <Card>
          <CardHeader><CardTitle>{t("poolVsBurst")}</CardTitle></CardHeader>
          <CardContent>
            {data ? <DonutChart data={[{ name: t("poolLegend"), value: data.summary.pool_usd, color: "#22c55e" }, { name: t("burstLegend"), value: data.summary.burst_usd, color: "#f5a524" }]} /> : <Skeleton className="h-[200px]" />}
          </CardContent>
        </Card>
      </div>

      <div className="mb-4 grid gap-4 lg:grid-cols-3">
        <Card>
          <CardHeader><CardTitle>{t("byProvider")}</CardTitle></CardHeader>
          <CardContent>
            {data ? <HBarChart data={Object.entries(data.summary.by_provider).sort((a, b) => b[1] - a[1]).map(([name, value]) => ({ name, value }))} /> : <Skeleton className="h-[200px]" />}
          </CardContent>
        </Card>
        <Card className="lg:col-span-2">
          <CardHeader><CardTitle className="flex items-center gap-2"><Lightbulb className="size-4 text-st-warning" /> {t("suggestions")}</CardTitle></CardHeader>
          <CardContent className="flex flex-col gap-2.5">
            {!sugg ? <LoadingRows rows={3} /> : !sugg.items.length ? <p className="text-sm text-muted-foreground">{t("noSuggestions")}</p> : sugg.items.map((s, i) => (
              <div key={`${s.kind}-${i}`} className="flex items-start gap-3 rounded-lg border border-border bg-background p-3">
                <div className="min-w-0 flex-1">
                  <p className="text-sm font-medium">{s.title}</p>
                  <p className="mt-0.5 text-[13px] text-muted-foreground">{s.detail}</p>
                  {s.box_id && <Link href={`/boxes/${s.box_id}`} className="mt-1 inline-block text-[12.5px] text-primary hover:underline">{t("openBox")}</Link>}
                </div>
                <Badge variant="green" className="shrink-0 font-mono" title={t("estSaving")}>−{usd(s.saving_usd, 2)}</Badge>
              </div>
            ))}
          </CardContent>
        </Card>
      </div>

      <RightsizeList />

      <Card>
        <CardHeader className="gap-3">
          <CardTitle>{t("ledger")}</CardTitle>
          <div className="grid grid-cols-2 gap-2 md:flex md:flex-wrap">
            <Select value={f.agent ?? ""} onChange={(e) => set("agent", e.target.value)} aria-label={tc("agent")}>
              <option value="">{tc("anyAgent")}</option>
              {agents?.items.map((a) => <option key={a.id} value={a.id}>{a.name}</option>)}
            </Select>
            <Select value={f.task ?? ""} onChange={(e) => set("task", e.target.value)} aria-label={t("taskBox")}>
              <option value="">{t("anyTask")}</option>
              {boxes?.items.map((b) => <option key={b.id} value={b.id}>{b.slug}</option>)}
            </Select>
            <Select value={f.provider ?? ""} onChange={(e) => set("provider", e.target.value)} aria-label={tc("provider")}>
              <option value="">{tc("anyProvider")}</option>
              {["hetzner", "vultr", "contabo", "digitalocean", "netcup"].map((p) => <option key={p} value={p}>{p}</option>)}
            </Select>
            <Input type="date" value={f.from ?? ""} onChange={(e) => set("from", e.target.value)} aria-label={t("from")} className="md:w-40" />
            <Input type="date" value={f.to ?? ""} onChange={(e) => set("to", e.target.value)} aria-label={t("to")} className="md:w-40" />
            {any && <Button size="sm" variant="ghost" onClick={() => setF({})}><FilterX /> {tc("clearFilters")}</Button>}
          </div>
        </CardHeader>
        <CardContent className="px-0">
          {error ? <div className="px-4"><ErrorBox error={error} retry={() => mutate()} /></div> : isLoading || !data ? <div className="px-4"><LoadingRows /></div> : !data.items.length ? <div className="px-4"><Empty title={t("empty")} /></div> : (
            <>
              <Table>
                <THead><TR className="hover:bg-transparent"><TH>{tc("time")}</TH><TH>{tc("agent")}</TH><TH>{t("colBox")}</TH><TH>{tc("provider")}</TH><TH>{t("colKind")}</TH><TH className="hidden lg:table-cell">{t("colNote")}</TH><TH className="text-right">{t("colAmount")}</TH></TR></THead>
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
                      <TD><Badge variant="outline" className="whitespace-nowrap">{["compute", "storage", "egress", "snapshot"].includes(l.kind) ? t(`kind.${l.kind}`) : l.kind}</Badge></TD>
                      <TD className="hidden max-w-[260px] truncate text-[13px] text-muted-foreground lg:table-cell">{l.note}</TD>
                      <TD className="text-right font-mono text-[13px]">{usd(l.amount_usd, 4)}</TD>
                    </TR>
                  ))}
                </TBody>
              </Table>
              {data.items.length > limit && (
                <div className="flex justify-center pt-3"><Button size="sm" variant="ghost" onClick={() => setLimit((n) => n + 50)}>{tc("showMore", { n: data.items.length - limit })}</Button></div>
              )}
            </>
          )}
        </CardContent>
      </Card>
    </>
  );
}
