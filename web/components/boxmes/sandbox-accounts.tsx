"use client";
// v2.2 M29-S: tab Sandbox của provider sandbox (E2B, Daytona, Novita…): tài khoản theo từng khoá, đồng hồ concurrency
// (group_running + in_flight / concurrent_limit), create-rate, limit_group / billing_group, credit khuyến mãi + runway,
// và ma trận template (available · missing · not_listed · unknown) với nút "Đồng bộ" theo từng tài khoản.
// Bám docs/boxmes-infra-accounts-spec.md §13.6 và docs/api-contract-v1.md "v2.2 M29-S". Chỉ admin nền tảng.
import * as React from "react";
import Link from "next/link";
import { useTranslations } from "next-intl";
import { AlertTriangle, Check, CircleHelp, Eraser, Minus, RefreshCw, Wallet, X } from "lucide-react";
import { api, paths } from "@/lib/api";
import { startsWith, useAction, useApi } from "@/lib/hooks";
import { useFmt } from "@/lib/format";
import type { AdminProviderDetail, AdminProvidersView, SandboxAccountRow, SandboxAccountsView, TemplateAvailability } from "@/lib/types";
import { Card } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Select } from "@/components/ui/input";
import { Progress } from "@/components/ui/progress";
import { Table, TBody, TD, TH, THead, TR } from "@/components/ui/table";
import { Empty, ErrorBox, LoadingRows, SectionTitle } from "@/components/boxmes/page";
import { Mono } from "@/components/boxmes/status";
import { AccountDrawer, AccountStatusChip, useMoney } from "@/components/boxmes/provider-accounts";
import { cn } from "@/lib/utils";

const TEMPLATE_API = new Set(["e2b", "daytona", "novita"]);

export function SandboxAccountsPanel({ provider }: { provider: string }) {
  const t = useTranslations("pa");
  const { data, error, mutate } = useApi<SandboxAccountsView>(paths.providerSandbox(provider), { refreshInterval: 10000 });
  const { data: detail } = useApi<AdminProviderDetail>(paths.adminProvider(provider), { refreshInterval: 20000 });
  const [openId, setOpenId] = React.useState<string | null>(null);
  if (error) return <ErrorBox error={error} retry={() => mutate()} />;
  if (!data) return <LoadingRows rows={3} />;
  const open = detail?.accounts.find((a) => a.id === openId) ?? null;
  const templateApi = detail?.capabilities ? !!detail.capabilities.template_api : TEMPLATE_API.has(provider);
  return (
    <div data-sandbox-accounts={provider}>
      <p className="mb-3 max-w-3xl text-[13px] text-muted-foreground">{t("sbx.desc")}</p>
      <SectionTitle right={<span className="text-xs text-muted-foreground">{t("sbx.strategy", { s: t(`strategy.${data.strategy}`) })}</span>}>{t("sbx.accountsTitle")}</SectionTitle>
      {data.accounts.length === 0 ? <Empty title={t("noAccounts")} desc={t("noAccountsDesc")} /> : (
        <Card className="relative mb-6 overflow-x-auto">
          <Table>
            <THead>
              <TR className="hover:bg-transparent">
                <TH>{t("col.label")}</TH><TH>{t("col.status")}</TH><TH>{t("sbx.groups")}</TH><TH>{t("sbx.slots")}</TH><TH className="hidden md:table-cell">{t("sbx.createRate")}</TH>
                <TH>{t("sbx.credit")}</TH><TH className="hidden lg:table-cell">{t("sbx.scopes")}</TH>
              </TR>
            </THead>
            <TBody>{data.accounts.map((r) => <SandboxRow key={r.id} r={r} onOpen={() => setOpenId(r.id)} />)}</TBody>
          </Table>
        </Card>
      )}
      <TemplateMatrix provider={provider} view={data} templateApi={templateApi} />
      <AccountDrawer provider={provider} account={open} sandbox onClose={() => setOpenId(null)} />
    </div>
  );
}

function SandboxRow({ r, onOpen }: { r: SandboxAccountRow; onOpen: () => void }) {
  const t = useTranslations("pa");
  const money = useMoney();
  const { relTime } = useFmt();
  const used = r.group_running + r.in_flight;
  const pct = r.concurrent_limit ? (used / r.concurrent_limit) * 100 : 0;
  const creditDays = r.credit_expires_at ? (Date.parse(r.credit_expires_at) - Date.now()) / 86_400_000 : null;
  return (
    <TR className="cursor-pointer" onClick={onOpen} data-sandbox-account={r.id}>
      <TD className="min-w-[9rem]">
        <button type="button" className="text-left font-medium hover:underline" onClick={(e) => { e.stopPropagation(); onOpen(); }}>{r.label}</button>
        <p className="text-xs text-muted-foreground"><Mono>{r.id}</Mono></p>
        {!r.eligible && r.ineligible && <p className="text-xs text-st-warning">{r.ineligible}</p>}
      </TD>
      <TD><AccountStatusChip status={r.status} cooldownUntil={r.cooldown_until} /></TD>
      <TD>
        <span className="flex flex-wrap gap-1">
          {r.limit_group ? <Badge variant="blue" title={t("limitGroupHint")}>{t("limitGroupTag", { g: r.limit_group })}</Badge> : null}
          {r.billing_group ? <Badge variant="outline" title={t("billingGroupHint")}><Wallet className="size-3" />{r.billing_group}</Badge> : null}
          {!r.limit_group && !r.billing_group && <span className="text-muted-foreground">—</span>}
        </span>
      </TD>
      <TD className="w-40">
        {r.concurrent_limit ? (
          <span className="flex flex-col gap-1" data-concurrency={Math.round(pct)}>
            <Progress value={pct} warnAt={80} dangerAt={95} />
            <span className="text-[11.5px] text-muted-foreground">
              <span className="font-mono text-foreground">{used}/{r.concurrent_limit}</span>
              {r.in_flight > 0 && ` · ${t("sbx.inFlight", { n: r.in_flight })}`}
              {r.limit_group && r.group_running !== r.running && ` · ${t("sbx.ownRunning", { n: r.running })}`}
              {r.concurrent_source === "learned" && <Badge variant="yellow" className="ml-1" title={t("sbx.learnedHint")}>{t("learned")}</Badge>}
            </span>
          </span>
        ) : <span className="text-[12px] text-muted-foreground">{t("sbx.runningNoLimit", { n: used })}</span>}
      </TD>
      <TD className="hidden whitespace-nowrap font-mono text-[12px] md:table-cell">
        {r.create_per_sec ? <span className={cn(r.creates_last_sec >= r.create_per_sec && "text-st-warning")}>{r.creates_last_sec}/{r.create_per_sec} {t("sbx.perSec")}</span> : null}
        {r.create_per_sec && r.create_per_min ? <br /> : null}
        {r.create_per_min ? <span className={cn(r.creates_last_min >= r.create_per_min && "text-st-warning")}>{r.creates_last_min}/{r.create_per_min} {t("sbx.perMin")}</span> : null}
        {!r.create_per_sec && !r.create_per_min && <span className="text-muted-foreground">—</span>}
      </TD>
      <TD className="whitespace-nowrap">
        <span className="flex flex-col">
          <span className="font-mono text-[13px]">{money(r.balance_effective)}</span>
          {r.credit_expires_at && <span title={r.credit_expires_at} className={cn("text-[11.5px]", creditDays !== null && creditDays <= 3 ? "text-st-error" : creditDays !== null && creditDays <= 14 ? "text-st-warning" : "text-muted-foreground")}>{t("sbx.creditExpires", { time: relTime(r.credit_expires_at) })}</span>}
        </span>
      </TD>
      <TD className="hidden lg:table-cell">
        {r.scope_problems?.length ? r.scope_problems.map((p) => <p key={p} className="flex items-start gap-1 text-[12px] text-st-error"><AlertTriangle className="mt-0.5 size-3 shrink-0" />{p}</p>)
          : r.scopes.length ? <span className="text-[12px] text-muted-foreground">{r.scopes.join(", ")}</span> : <span className="text-muted-foreground">—</span>}
      </TD>
    </TR>
  );
}

const CELL: Record<TemplateAvailability, { icon: React.ReactNode; cls: string }> = {
  available: { icon: <Check className="size-3.5" />, cls: "text-st-running" },
  missing: { icon: <X className="size-3.5" />, cls: "text-st-error" },
  not_listed: { icon: <Minus className="size-3.5" />, cls: "text-muted-foreground" },
  unknown: { icon: <CircleHelp className="size-3.5" />, cls: "text-st-warning" },
};

function TemplateMatrix({ provider, view, templateApi }: { provider: string; view: SandboxAccountsView; templateApi: boolean }) {
  const t = useTranslations("pa");
  const { relTime } = useFmt();
  const { run, pending } = useAction();
  const rv = [startsWith(paths.adminProvider(provider)), paths.adminProviders];
  const sync = (id: string) => run(`sync:${id}`, () => api.syncAccountTemplates(provider, id), {
    success: (r) => t("sbx.synced", { added: r.added.length, removed: r.removed.length }), revalidate: rv,
  });
  const syncAll = async () => { for (const a of view.accounts) await sync(a.id); };
  const forget = (id: string) => run(`forget:${id}`, () => api.updateProviderAccount(provider, id, { clear_templates_missing: true }), { success: t("sbx.forgotten"), revalidate: rv });
  return (
    <div data-template-matrix>
      <SectionTitle right={templateApi && view.accounts.length > 0 ? (
        <Button size="sm" variant="outline" disabled={!!pending} onClick={syncAll} data-sync-all><RefreshCw className={cn(pending?.startsWith("sync") && "animate-spin")} /> {t("sbx.syncAll")}</Button>
      ) : null}>{t("sbx.matrixTitle")}</SectionTitle>
      <p className="mb-2 flex flex-wrap gap-3 text-xs text-muted-foreground">
        {(Object.keys(CELL) as TemplateAvailability[]).map((k) => <span key={k} className="inline-flex items-center gap-1"><span className={CELL[k].cls}>{CELL[k].icon}</span>{t(`tpl.${k}`)}</span>)}
      </p>
      {!templateApi && <p className="mb-2 text-[13px] text-muted-foreground">{t("sbx.noTemplateApi")}</p>}
      {view.templates.length === 0 || view.accounts.length === 0 ? <Empty title={t("sbx.noTemplates")} /> : (
        <Card className="relative overflow-x-auto">
          <Table>
            <THead>
              <TR className="hover:bg-transparent">
                <TH>{t("sbx.template")}</TH>
                {view.accounts.map((a) => (
                  <TH key={a.id} className="min-w-[8rem] align-top">
                    <span className="block font-medium text-foreground">{a.label}</span>
                    <span className="block text-[11px] font-normal">{a.templates_synced_at ? t("sbx.syncedAt", { time: relTime(a.templates_synced_at) }) : t("sbx.neverSynced")}</span>
                    <span className="mt-1 flex gap-1">
                      {templateApi && <Button size="xs" variant="ghost" className="h-6 px-1.5" disabled={!!pending} onClick={() => sync(a.id)} data-sync-account={a.id}><RefreshCw className="size-3" /> {t("sbx.sync")}</Button>}
                      {Object.values(a.templates).includes("missing") && <Button size="xs" variant="ghost" className="h-6 px-1.5" disabled={!!pending} onClick={() => forget(a.id)} title={t("sbx.forgetHint")}><Eraser className="size-3" /> {t("sbx.forget")}</Button>}
                    </span>
                  </TH>
                ))}
                <TH />
              </TR>
            </THead>
            <TBody>
              {view.templates.map((tp) => {
                const avail = view.accounts.filter((a) => a.templates[tp] === "available").length;
                return (
                  <TR key={tp} data-template={tp}>
                    <TD><Mono>{tp}</Mono></TD>
                    {view.accounts.map((a) => {
                      const v = a.templates[tp] ?? "unknown";
                      return (
                        <TD key={a.id} title={t(`tplHint.${v}`)}>
                          <span className={cn("inline-flex items-center gap-1 text-[12px]", CELL[v].cls)}>{CELL[v].icon}<span className="sr-only sm:not-sr-only">{t(`tpl.${v}`)}</span></span>
                        </TD>
                      );
                    })}
                    <TD>{avail === 1 && view.accounts.length > 1 ? <Badge variant="yellow" title={t("sbx.noBackupHint")}>{t("sbx.noBackup")}</Badge> : avail === 0 ? <Badge variant="red">{t("sbx.nowhere")}</Badge> : null}</TD>
                  </TR>
                );
              })}
            </TBody>
          </Table>
        </Card>
      )}
    </div>
  );
}

/** Tab "Sandbox API" của trang Hạ tầng: chọn provider sandbox rồi xem tài khoản + ma trận template. */
export function SandboxAccountsSection() {
  const t = useTranslations("pa");
  const { data } = useApi<AdminProvidersView>(paths.adminProviders, { refreshInterval: 0 });
  const sbx = (data?.items ?? []).filter((r) => r.group === "sandbox" && r.accounts_total > 0);
  const [sel, setSel] = React.useState<string>("");
  const cur = sel || sbx[0]?.id || "";
  if (!data) return null;
  if (!sbx.length) return null;
  return (
    <div className="mb-8" data-sandbox-accounts-section>
      <SectionTitle right={
        <div className="flex items-center gap-2">
          <Select value={cur} onChange={(e) => setSel(e.target.value)} aria-label={t("sbx.pickProvider")} className="h-8 w-auto text-[13px]">
            {sbx.map((r) => <option key={r.id} value={r.id}>{r.name} ({r.accounts_total})</option>)}
          </Select>
          <Link href={`/infra/providers/${encodeURIComponent(cur)}?tab=sandbox`} className="text-[13px] text-primary hover:underline">{t("sbx.openDetail")}</Link>
        </div>
      }>{t("sbx.sectionTitle")}</SectionTitle>
      {cur && <SandboxAccountsPanel key={cur} provider={cur} />}
    </div>
  );
}
