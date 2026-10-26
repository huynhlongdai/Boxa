"use client";
import * as React from "react";
import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { FilterX } from "lucide-react";
import { useTranslations } from "next-intl";
import { paths, type BoxFilters } from "@/lib/api";
import { useApi } from "@/lib/hooks";
import type { Agent, Box, List } from "@/lib/types";
import { useFmt } from "@/lib/format";
import { Card } from "@/components/ui/card";
import { Select } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { Table, TBody, TD, TH, THead, TR } from "@/components/ui/table";
import { BoxStatusChip, BOX_STATUSES, LifecycleBadge, Mono, useBoxStatusLabel } from "@/components/boxa/status";
import { MoneyBudget, BudgetBar } from "@/components/boxa/budget";
import { BoxActions } from "@/components/boxa/box-actions";
import { CreateBoxDialog } from "@/components/boxa/create-box";
import { Empty, ErrorBox, LoadingRows, PageHeader } from "@/components/boxa/page";

const PROVIDERS = ["hetzner", "vultr", "contabo", "digitalocean", "netcup", "local"];

function BoxesInner() {
  const router = useRouter();
  const sp = useSearchParams();
  const t = useTranslations("boxes");
  const tc = useTranslations("common");
  const statusLabel = useBoxStatusLabel();
  const { since } = useFmt();
  const f: BoxFilters = { status: sp.get("status") ?? "", agent: sp.get("agent") ?? "", provider: sp.get("provider") ?? "", lifecycle: sp.get("lifecycle") ?? "" };
  const set = (k: keyof BoxFilters, v: string) => {
    const n = new URLSearchParams(sp.toString());
    if (v) n.set(k, v);
    else n.delete(k);
    router.replace(`/boxes${n.toString() ? `?${n}` : ""}`);
  };
  const { data, error, isLoading, mutate } = useApi<List<Box>>(paths.boxes(f));
  const { data: agents } = useApi<List<Agent>>(paths.agents);
  const any = Object.values(f).some(Boolean);

  return (
    <>
      <PageHeader title={t("title")} desc={t("desc")} actions={<CreateBoxDialog />} />
      <div className="mb-4 grid grid-cols-2 gap-2 sm:flex sm:flex-wrap">
        <Select value={f.status} onChange={(e) => set("status", e.target.value)} aria-label={t("filterStatus")}>
          <option value="">{t("anyStatus")}</option>
          {BOX_STATUSES.filter((k) => k !== "terminated").map((k) => <option key={k} value={k}>{statusLabel(k)}</option>)}
        </Select>
        <Select value={f.agent} onChange={(e) => set("agent", e.target.value)} aria-label={t("filterAgent")}>
          <option value="">{tc("anyAgent")}</option>
          {agents?.items.map((a) => <option key={a.id} value={a.id}>{a.name}</option>)}
        </Select>
        <Select value={f.provider} onChange={(e) => set("provider", e.target.value)} aria-label={t("filterProvider")}>
          <option value="">{tc("anyProvider")}</option>
          {PROVIDERS.map((p) => <option key={p} value={p}>{p}</option>)}
        </Select>
        <Select value={f.lifecycle} onChange={(e) => set("lifecycle", e.target.value)} aria-label={t("filterLifecycle")}>
          <option value="">{t("anyLifecycle")}</option>
          <option value="task">task</option>
          <option value="session">session</option>
          <option value="service">service</option>
        </Select>
        {any && <Button variant="ghost" size="sm" className="col-span-2 sm:col-span-1" onClick={() => router.replace("/boxes")}><FilterX /> {tc("clearFilters")}</Button>}
      </div>

      {error ? <ErrorBox error={error} retry={() => mutate()} /> : isLoading || !data ? <LoadingRows rows={6} /> : !data.items.length ? (
        <Empty title={t("empty")} desc={t("emptyDesc")} />
      ) : (
        <>
          {/* Desktop/tablet: bảng */}
          <Card className="hidden md:block">
            <Table>
              <THead>
                <TR className="hover:bg-transparent">
                  <TH>{t("colSlug")}</TH><TH>{tc("agent")}</TH><TH>{tc("status")}</TH><TH>{t("colWhere")}</TH><TH>{t("colLifecycle")}</TH><TH>{t("colUptime")}</TH><TH>{tc("spentOfBudget")}</TH><TH className="text-right">{tc("actions")}</TH>
                </TR>
              </THead>
              <TBody>
                {data.items.map((b) => (
                  <TR key={b.id}>
                    <TD>
                      <Link href={`/boxes/${b.id}`} className="font-mono text-[13px] hover:text-primary">{b.slug}</Link>
                      <div className="font-mono text-[11px] text-muted-foreground">{b.id}</div>
                    </TD>
                    <TD className="text-[13px]">{b.agent_name}</TD>
                    <TD><BoxStatusChip status={b.status} /></TD>
                    <TD><Mono>{b.provider}</Mono> <span className="text-muted-foreground">/</span> <Mono className="text-muted-foreground">{b.region}</Mono></TD>
                    <TD><LifecycleBadge lc={b.lifecycle} /></TD>
                    <TD className="whitespace-nowrap text-[13px] text-muted-foreground">{b.status === "running" ? since(b.created_at) : "—"}</TD>
                    <TD className="min-w-[140px]">
                      <MoneyBudget spent={b.spent_usd} budget={b.budget_usd} />
                      <BudgetBar spent={b.spent_usd} budget={b.budget_usd} className="mt-1" />
                    </TD>
                    <TD><BoxActions box={b} className="justify-end" /></TD>
                  </TR>
                ))}
              </TBody>
            </Table>
          </Card>
          {/* Mobile: thẻ */}
          <div className="flex flex-col gap-2.5 md:hidden">
            {data.items.map((b) => (
              <Card key={b.id} className="p-3.5">
                <div className="flex items-start justify-between gap-2">
                  <Link href={`/boxes/${b.id}`} className="min-w-0">
                    <p className="truncate font-mono text-sm">{b.slug}</p>
                    <p className="text-xs text-muted-foreground">{b.agent_name} · <span className="font-mono">{b.provider}/{b.region}</span></p>
                  </Link>
                  <BoxStatusChip status={b.status} />
                </div>
                <div className="mt-3 flex items-center gap-2">
                  <LifecycleBadge lc={b.lifecycle} />
                  <span className="text-xs text-muted-foreground">{b.status === "running" ? since(b.created_at) : ""}</span>
                  <span className="ml-auto"><MoneyBudget spent={b.spent_usd} budget={b.budget_usd} /></span>
                </div>
                <BudgetBar spent={b.spent_usd} budget={b.budget_usd} className="mt-2" />
                <BoxActions box={b} className="-mb-1 mt-2 justify-end border-t border-border pt-2" />
              </Card>
            ))}
          </div>
        </>
      )}
    </>
  );
}

export default function BoxesPage() {
  return (
    <React.Suspense fallback={<LoadingRows rows={6} />}>
      <BoxesInner />
    </React.Suspense>
  );
}
