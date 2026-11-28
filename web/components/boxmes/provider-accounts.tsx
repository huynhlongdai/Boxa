"use client";
// v2.2 Hạ tầng — tài khoản nhà cung cấp (M29): KPI, bảng provider, bảng tài khoản, ngăn kéo chi tiết,
// luồng thêm khoá 3 bước, sửa ưu tiên/chiến lược/hạn mức, bật/tắt/drain, xoá có xác nhận, danh sách cảnh báo.
// Bám docs/boxmes-infra-accounts-spec.md §8 và docs/api-contract-v1.md "v2.2 additions". Chỉ admin nền tảng.
import * as React from "react";
import Link from "next/link";
import { useTranslations } from "next-intl";
import {
  AlertTriangle, ArrowDownUp, BellOff, CheckCircle2, ChevronRight, CircleSlash, KeyRound, Pause, Play, Plus, RefreshCw, RotateCcw, ShieldCheck, Trash2, Wallet,
} from "lucide-react";
import { api, paths } from "@/lib/api";
import { startsWith, useAction, useApi } from "@/lib/hooks";
import { useFmt } from "@/lib/format";
import { toastError } from "@/lib/error-toast";
import {
  ACCOUNT_STRATEGIES, type AccountStatus, type AccountStrategy, type AccountTestResult, type AdminProviderDetail, type AdminProviderRow, type AdminProvidersView,
  type BoxmesEvent, type InfraAlert, type List, type ProviderAccount, type ProviderAccountInput,
} from "@/lib/types";
import { Card } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Field, Input, Select, Textarea } from "@/components/ui/input";
import { Progress } from "@/components/ui/progress";
import { Switch } from "@/components/ui/switch";
import { Table, TBody, TD, TH, THead, TR } from "@/components/ui/table";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, SheetContent } from "@/components/ui/dialog";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Empty, ErrorBox, LoadingRows, SectionTitle } from "@/components/boxmes/page";
import { Mono } from "@/components/boxmes/status";
import { ProviderLogo } from "@/components/boxmes/sandbox";
import { cn } from "@/lib/utils";

// ---------------- tiền: USD + tiền gốc ----------------
/** Định dạng tiền theo mã tiền tệ (EUR, GBP…) và locale hiện tại. */
export function useMoney() {
  const { tag, usd } = useFmt();
  return React.useCallback(
    (n: number | null | undefined, currency = "USD") => {
      if (n === null || n === undefined || Number.isNaN(n)) return "—";
      if (currency === "USD") return usd(n, 2);
      try {
        return new Intl.NumberFormat(tag, { style: "currency", currency, currencyDisplay: "narrowSymbol", minimumFractionDigits: 2, maximumFractionDigits: 2 }).format(n);
      } catch {
        return `${n.toFixed(2)} ${currency}`;
      }
    },
    [tag, usd],
  );
}

/** USD (để tổng hợp) kèm tiền gốc bên cạnh (quyết định 2026-11-27 §12.5). */
export function Money({ usd, amount, currency, className }: { usd?: number | null; amount?: number | null; currency?: string; className?: string }) {
  const money = useMoney();
  const showOrig = currency && currency !== "USD" && amount !== undefined && amount !== null;
  return (
    <span className={cn("whitespace-nowrap font-mono text-[13px]", className)}>
      {money(usd ?? (currency === "USD" || !currency ? amount : undefined), "USD")}
      {showOrig && <span className="ml-1 text-[11.5px] text-muted-foreground">({money(amount, currency)})</span>}
    </span>
  );
}

// ---------------- trạng thái ----------------
const STATUS_TONE: Record<AccountStatus, "green" | "yellow" | "red" | "gray"> = {
  active: "green", cooling: "yellow", rate_limited: "yellow", quota_full: "yellow", low_balance: "yellow", token_expiring: "yellow", draining: "yellow",
  out_of_credit: "red", auth_failed: "red", disabled: "gray", archived: "gray",
};

function useCountdown(iso?: string) {
  const [, tick] = React.useState(0);
  React.useEffect(() => {
    if (!iso || Date.parse(iso) <= Date.now()) return;
    const id = setInterval(() => tick((x) => x + 1), 1000);
    return () => clearInterval(id);
  }, [iso]);
  if (!iso) return null;
  const s = Math.round((Date.parse(iso) - Date.now()) / 1000);
  return s > 0 ? s : null;
}

export function AccountStatusChip({ status, reason, cooldownUntil }: { status: AccountStatus; reason?: string; cooldownUntil?: string }) {
  const t = useTranslations("pa");
  const { seconds } = useFmt();
  const left = useCountdown(cooldownUntil);
  return (
    <span className="inline-flex flex-wrap items-center gap-1">
      <Badge variant={STATUS_TONE[status] ?? "gray"} title={reason}>{t(`status.${status}`)}</Badge>
      {left !== null && <span className="font-mono text-[11.5px] text-st-warning" title={t("cooldownHint")}>{t("cooldown", { time: seconds(left) })}</span>}
    </span>
  );
}

function Runway({ days }: { days?: number | null }) {
  const t = useTranslations("pa");
  if (days === undefined || days === null) return <span className="text-muted-foreground">—</span>;
  const tone = days < 2 ? "text-st-error" : days < 7 ? "text-st-warning" : "text-foreground";
  return <span className={cn("whitespace-nowrap font-mono text-[13px]", tone)}>{t("runwayDays", { n: Math.round(days * 10) / 10 })}</span>;
}

function Breaker({ state }: { state: "closed" | "open" | "half_open" }) {
  const t = useTranslations("pa");
  return <Badge variant={state === "closed" ? "outline" : state === "open" ? "red" : "yellow"}>{t(`breaker.${state}`)}</Badge>;
}

function StrategyBadge({ s }: { s: AccountStrategy }) {
  const t = useTranslations("pa");
  return <Badge variant="outline" title={t(`strategyDesc.${s}`)}>{t(`strategy.${s}`)}</Badge>;
}

/** Ngày hết hạn: đỏ ≤ 3 ngày, vàng ≤ 14 ngày (ngưỡng mặc định §12.4). */
function Expiry({ iso }: { iso?: string }) {
  const { relTime, dateTime } = useFmt();
  if (!iso) return <span className="text-muted-foreground">—</span>;
  const d = (Date.parse(iso) - Date.now()) / 86_400_000;
  return <span title={dateTime(iso)} className={cn("whitespace-nowrap text-[13px]", d <= 3 ? "text-st-error" : d <= 14 ? "text-st-warning" : "text-muted-foreground")}>{relTime(iso)}</span>;
}

// ---------------- KPI + bảng provider (tab Tài khoản) ----------------
function Kpi({ label, children, hint }: { label: string; children: React.ReactNode; hint?: React.ReactNode }) {
  return (
    <Card className="min-w-0 p-3.5">
      <p className="text-xs text-muted-foreground">{label}</p>
      <div className="mt-1 text-lg font-semibold leading-tight">{children}</div>
      {hint && <div className="mt-0.5 text-xs text-muted-foreground">{hint}</div>}
    </Card>
  );
}

export function AccountsOverview() {
  const t = useTranslations("pa");
  const { data, error, mutate } = useApi<AdminProvidersView>(paths.adminProviders, { refreshInterval: 30000 });
  const money = useMoney();
  const [group, setGroup] = React.useState<"all" | "vps" | "sandbox">("all");
  const [onlyAlerts, setOnlyAlerts] = React.useState(false);
  if (error) return <ErrorBox error={error} retry={() => mutate()} />;
  if (!data) return <LoadingRows rows={4} />;
  const s = data.summary;
  const rows = data.items.filter((r) => (group === "all" || r.group === group) && (!onlyAlerts || r.open_alerts > 0));
  return (
    <div data-accounts-overview>
      <div className="mb-5 grid grid-cols-2 gap-3 md:grid-cols-3 xl:grid-cols-6">
        <Kpi label={t("kpi.balance")} hint={Object.entries(s.balance_by_currency ?? {}).map(([c, v]) => money(v, c)).join(" · ") || undefined}>
          <span className="font-mono">{money(s.balance_usd)}</span>
        </Kpi>
        <Kpi label={t("kpi.spendToday")}><span className="font-mono">{money(s.spend_today_usd)}</span></Kpi>
        <Kpi label={t("kpi.spendMonth")} hint={t("kpi.forecast", { amount: money(s.forecast_month_usd) })}><span className="font-mono">{money(s.spend_month_usd)}</span></Kpi>
        <Kpi label={t("kpi.shortestRunway")} hint={s.shortest_runway ? <Link className="hover:underline" href={`/infra/providers/${encodeURIComponent(s.shortest_runway.provider)}`}>{s.shortest_runway.label}</Link> : undefined}>
          <Runway days={s.shortest_runway?.days} />
        </Kpi>
        <Kpi label={t("kpi.accounts")}>
          <span className="flex flex-wrap items-center gap-1.5 text-[13px] font-normal">
            <Badge variant="green">{t("kpi.healthy", { n: s.accounts_healthy })}</Badge>
            <Badge variant="yellow">{t("kpi.degraded", { n: s.accounts_degraded })}</Badge>
            <Badge variant="red">{t("kpi.broken", { n: s.accounts_broken })}</Badge>
          </span>
        </Kpi>
        <Kpi label={t("kpi.alerts")}><span className={cn("font-mono", s.open_alerts ? "text-st-warning" : "")}>{s.open_alerts}</span></Kpi>
      </div>

      <SectionTitle right={
        <div className="flex flex-wrap items-center gap-2 text-[13px]">
          <Select value={group} onChange={(e) => setGroup(e.target.value as typeof group)} aria-label={t("filterGroup")} className="h-8 w-auto">
            <option value="all">{t("groupAll")}</option>
            <option value="vps">{t("groupVps")}</option>
            <option value="sandbox">{t("groupSandbox")}</option>
          </Select>
          <label className="inline-flex items-center gap-1.5"><Switch checked={onlyAlerts} onCheckedChange={setOnlyAlerts} aria-label={t("onlyAlerts")} /> {t("onlyAlerts")}</label>
        </div>
      }>{t("providersTitle")}</SectionTitle>
      <Card className="relative mb-6 overflow-x-auto">
        <Table>
          <THead>
            <TR className="hover:bg-transparent">
              <TH>{t("col.provider")}</TH><TH>{t("col.accounts")}</TH><TH>{t("col.balance")}</TH><TH>{t("col.spendMonth")}</TH><TH>{t("col.runway")}</TH>
              <TH className="hidden lg:table-cell">{t("col.live")}</TH><TH className="hidden md:table-cell">{t("col.maxLimit")}</TH><TH>{t("col.strategy")}</TH><TH>{t("col.alerts")}</TH><TH />
            </TR>
          </THead>
          <TBody>
            {rows.length === 0 ? (
              <TR><TD colSpan={10} className="py-6 text-center text-[13px] text-muted-foreground">{t("noProviders")}</TD></TR>
            ) : rows.map((r) => <ProviderRow key={r.id} r={r} />)}
          </TBody>
        </Table>
      </Card>

      <InfraAlertsList />
    </div>
  );
}

function ProviderRow({ r }: { r: AdminProviderRow }) {
  const t = useTranslations("pa");
  const money = useMoney();
  const href = `/infra/providers/${encodeURIComponent(r.id)}`;
  return (
    <TR data-provider-row={r.id}>
      <TD className="whitespace-nowrap">
        <Link href={href} className="inline-flex items-center gap-2 hover:underline">
          <ProviderLogo id={r.id} className="size-6 rounded text-[9px]" />
          <span className="font-medium">{r.name}</span>
          <Badge variant="outline">{r.group === "sandbox" ? t("groupSandbox") : t("groupVps")}</Badge>
        </Link>
      </TD>
      <TD className="whitespace-nowrap text-[13px]">
        {r.accounts_total === 0 ? <span className="text-muted-foreground">{t("noAccountsShort")}</span> : (
          <span className={r.accounts_degraded ? "text-st-warning" : ""}>{t("healthyOf", { ok: r.accounts_healthy, total: r.accounts_total })}</span>
        )}
      </TD>
      <TD>
        <span className="flex flex-col">
          <span className="font-mono text-[13px]">{money(r.balance_usd)}</span>
          {Object.entries(r.balance_by_currency ?? {}).map(([c, v]) => <span key={c} className="font-mono text-[11.5px] text-muted-foreground">{money(v, c)}</span>)}
        </span>
      </TD>
      <TD className="font-mono text-[13px]">{money(r.spend_month_usd)}</TD>
      <TD><Runway days={r.runway_days} /></TD>
      <TD className="hidden text-[13px] lg:table-cell">{r.hosts_live}</TD>
      <TD className="hidden w-28 md:table-cell">
        {r.max_limit_pct > 0 ? <span className="flex items-center gap-1.5"><Progress value={r.max_limit_pct} warnAt={80} dangerAt={95} className="w-14" /><span className="font-mono text-[12px]">{r.max_limit_pct}%</span></span> : <span className="text-muted-foreground">—</span>}
      </TD>
      <TD><StrategyBadge s={r.strategy} /></TD>
      <TD>{r.open_alerts ? <Badge variant="yellow"><AlertTriangle className="size-3" />{r.open_alerts}</Badge> : <span className="text-muted-foreground">—</span>}</TD>
      <TD><Link href={href} aria-label={t("openProvider", { name: r.name })} className="text-muted-foreground hover:text-foreground"><ChevronRight className="size-4" /></Link></TD>
    </TR>
  );
}

// ---------------- cảnh báo ----------------
const ALERT_TONE = { critical: "red", warn: "yellow", info: "blue" } as const;
const ALERT_TYPES = ["low_balance", "out_of_credit", "auth_failed", "token_expiring", "credit_expiring", "trial_ending", "contract_ending", "rotation_due", "limit_near", "spend_cap_near", "failover", "single_account", "orphan_host", "balance_stale"];

export function InfraAlertsList({ provider }: { provider?: string }) {
  const t = useTranslations("pa");
  const { relTime } = useFmt();
  const [status, setStatus] = React.useState<"" | "open" | "acked" | "closed" | "all">("");
  const { data, error, mutate } = useApi<List<InfraAlert>>(paths.infraAlerts(status || undefined), { refreshInterval: 30000 });
  const { run, pending } = useAction();
  const items = (data?.items ?? []).filter((a) => !provider || a.provider === provider);
  return (
    <div data-infra-alerts>
      <SectionTitle right={
        <Select value={status} onChange={(e) => setStatus(e.target.value as typeof status)} aria-label={t("alerts.filter")} className="h-8 w-auto text-[13px]">
          <option value="">{t("alerts.openAcked")}</option>
          <option value="open">{t("alerts.open")}</option>
          <option value="acked">{t("alerts.acked")}</option>
          <option value="closed">{t("alerts.closed")}</option>
          <option value="all">{t("alerts.all")}</option>
        </Select>
      }>{t("alerts.title")}</SectionTitle>
      {error ? <ErrorBox error={error} retry={() => mutate()} /> : !data ? <LoadingRows rows={2} /> : items.length === 0 ? (
        <Empty title={t("alerts.empty")} />
      ) : (
        <Card className="divide-y divide-border/60">
          {items.map((a) => (
            <div key={a.id} className="flex flex-col gap-2 px-4 py-3 sm:flex-row sm:items-center" data-alert={a.type}>
              <div className="flex min-w-0 flex-1 items-start gap-2.5">
                <Badge variant={ALERT_TONE[a.level]} className="mt-0.5">{t(`alerts.level.${a.level}`)}</Badge>
                <div className="min-w-0">
                  <p className="text-[13.5px] font-medium">{ALERT_TYPES.includes(a.type) ? t(`alerts.type.${a.type}`) : a.type}</p>
                  <p className="break-words text-[13px] text-muted-foreground">{a.message}</p>
                  <p className="mt-0.5 flex flex-wrap gap-x-2 text-xs text-muted-foreground">
                    {a.provider && <Link className="hover:underline" href={`/infra/providers/${encodeURIComponent(a.provider)}`}>{a.provider}</Link>}
                    {a.account_id && <Mono>{a.account_id}</Mono>}
                    <span>{t("alerts.opened", { time: relTime(a.opened_at) })}</span>
                    {a.count > 1 && <span>×{a.count}</span>}
                    {a.status === "acked" && <span>{t("alerts.ackedBy", { who: a.acked_by ?? "—", time: relTime(a.acked_at) })}</span>}
                  </p>
                </div>
              </div>
              {a.status === "open" && (
                <Button size="xs" variant="outline" disabled={!!pending} onClick={() => run(a.id, () => api.ackInfraAlert(a.id), { success: t("alerts.ackedToast"), revalidate: [startsWith("/admin/infra/alerts"), startsWith("/admin/providers")] })}>
                  <BellOff /> {t("alerts.ack")}
                </Button>
              )}
            </div>
          ))}
        </Card>
      )}
    </div>
  );
}

// ---------------- chiến lược ----------------
export function StrategyEditor({ provider, cfg, sandbox }: { provider: string; cfg: AdminProviderDetail["strategy_config"]; sandbox: boolean }) {
  const t = useTranslations("pa");
  const tc = useTranslations("common");
  const [strategy, setStrategy] = React.useState<string>(cfg.strategy);
  const [sticky, setSticky] = React.useState(String(cfg.sticky_minutes ?? 0));
  const [failover, setFailover] = React.useState(cfg.failover_across_accounts);
  const { run, pending } = useAction();
  React.useEffect(() => { setStrategy(cfg.strategy); setSticky(String(cfg.sticky_minutes ?? 0)); setFailover(cfg.failover_across_accounts); }, [cfg]);
  const effective = (strategy || (sandbox ? "drain_first" : "priority")) as AccountStrategy;
  const dirty = strategy !== cfg.strategy || Number(sticky) !== cfg.sticky_minutes || failover !== cfg.failover_across_accounts;
  const stickyN = Number(sticky);
  const stickyOk = Number.isInteger(stickyN) && stickyN >= 0 && stickyN <= 1440;
  return (
    <Card className="p-4" data-strategy-editor>
      <p className="mb-3 text-[13.5px] font-semibold">{t("strategyTitle")}</p>
      <div className="grid gap-3 sm:grid-cols-[minmax(0,1fr)_10rem]">
        <Field label={t("strategyLabel")} hint={t(`strategyDesc.${effective}`)}>
          <Select value={strategy} onChange={(e) => setStrategy(e.target.value)} data-strategy-select>
            <option value="">{t("strategyDefault", { s: t(`strategy.${sandbox ? "drain_first" : "priority"}`) })}</option>
            {ACCOUNT_STRATEGIES.map((s) => <option key={s} value={s}>{t(`strategy.${s}`)}</option>)}
          </Select>
        </Field>
        <Field label={t("sticky")} hint={t("stickyHint")}>
          <Input type="number" min={0} max={1440} value={sticky} onChange={(e) => setSticky(e.target.value)} />
        </Field>
      </div>
      <label className="mt-3 flex items-center gap-2 text-[13px]">
        <Switch checked={failover} onCheckedChange={setFailover} aria-label={t("failoverAcross")} /> {t("failoverAcross")}
      </label>
      <div className="mt-3 flex justify-end">
        <Button size="sm" disabled={!dirty || !stickyOk || !!pending} onClick={() => run("strategy", () => api.setProviderStrategy(provider, { strategy, sticky_minutes: stickyN, failover_across_accounts: failover }), {
          success: tc("saved"), revalidate: [startsWith(paths.adminProvider(provider)), paths.adminProviders],
        })}>{tc("save")}</Button>
      </div>
    </Card>
  );
}

// ---------------- chi tiết provider: bảng tài khoản ----------------
export function ProviderAccountsPanel({ provider }: { provider: string }) {
  const t = useTranslations("pa");
  const money = useMoney();
  const { data, error, mutate } = useApi<AdminProviderDetail>(paths.adminProvider(provider), { refreshInterval: 20000 });
  const [openId, setOpenId] = React.useState<string | null>(null);
  const [adding, setAdding] = React.useState(false);
  const [showArchived, setShowArchived] = React.useState(false);
  const { data: withArchived } = useApi<List<ProviderAccount>>(showArchived ? paths.providerAccounts(provider, true) : null, { refreshInterval: 20000 });
  if (error) return <ErrorBox error={error} retry={() => mutate()} />;
  if (!data) return <LoadingRows rows={4} />;
  const accounts = showArchived && withArchived ? withArchived.items : data.accounts;
  const open = accounts.find((a) => a.id === openId) ?? null;
  return (
    <div data-provider-accounts={provider}>
      <div className="mb-5 grid gap-3 lg:grid-cols-[minmax(0,1fr)_22rem]">
        <div className="grid grid-cols-2 content-start gap-3 md:grid-cols-4">
          <Kpi label={t("col.accounts")}>{t("healthyOf", { ok: data.accounts_healthy, total: data.accounts_total })}</Kpi>
          <Kpi label={t("col.balance")} hint={Object.entries(data.balance_by_currency ?? {}).map(([c, v]) => money(v, c)).join(" · ") || undefined}>
            <Money usd={data.balance_usd} />
          </Kpi>
          <Kpi label={t("col.spendMonth")}><Money usd={data.spend_month_usd} /></Kpi>
          <Kpi label={t("col.runway")}><Runway days={data.runway_days} /></Kpi>
          {data.capabilities && (
            <Card className="col-span-2 p-3.5 text-[12.5px] text-muted-foreground md:col-span-4">
              <span className="mr-2 font-medium text-foreground">{t("caps.title")}</span>
              <CapBadge on={!!data.capabilities.balance_api} label={t("caps.balanceApi")} />
              <CapBadge on={!!data.capabilities.limits_api} label={t("caps.limitsApi")} />
              {data.group === "sandbox" && <CapBadge on={!!data.capabilities.template_api} label={t("caps.templateApi")} />}
              {!data.capabilities.balance_api && <span className="ml-1">{t("caps.manualHint")}</span>}
            </Card>
          )}
        </div>
        <StrategyEditor provider={provider} cfg={data.strategy_config} sandbox={data.group === "sandbox"} />
      </div>

      <SectionTitle right={
        <div className="flex flex-wrap items-center gap-3">
          <label className="inline-flex items-center gap-1.5 text-[13px]"><Switch checked={showArchived} onCheckedChange={setShowArchived} aria-label={t("showArchived")} /> {t("showArchived")}</label>
          <Button size="sm" onClick={() => setAdding(true)} data-add-account><Plus /> {t("addAccount")}</Button>
        </div>
      }>{t("accountsTitle")}</SectionTitle>
      {accounts.length === 0 ? (
        <Empty title={t("noAccounts")} desc={t("noAccountsDesc")} action={<Button size="sm" onClick={() => setAdding(true)}><Plus /> {t("addAccount")}</Button>} />
      ) : (
        <Card className="relative mb-6 overflow-x-auto">
          <Table>
            <THead>
              <TR className="hover:bg-transparent">
                <TH>{t("col.label")}</TH><TH>{t("col.status")}</TH><TH>{t("col.priority")}</TH><TH>{t("col.balance")}</TH>
                <TH className="hidden md:table-cell">{t("col.spendCap")}</TH><TH>{t("col.runway")}</TH><TH className="hidden lg:table-cell">{t("col.limits")}</TH>
                <TH className="hidden lg:table-cell">{t("col.expiry")}</TH><TH className="hidden md:table-cell">{t("col.health")}</TH><TH className="hidden sm:table-cell">{t("col.hosts")}</TH>
              </TR>
            </THead>
            <TBody>
              {accounts.map((a) => <AccountRow key={a.id} a={a} strategy={data.strategy_config.effective_strategy} onOpen={() => setOpenId(a.id)} />)}
            </TBody>
          </Table>
        </Card>
      )}

      <InfraAlertsList provider={provider} />
      <AddAccountDialog provider={provider} providerName={data.name} sandbox={data.group === "sandbox"} open={adding} onOpenChange={setAdding} onCreated={(id) => setOpenId(id)} />
      <AccountDrawer provider={provider} account={open} sandbox={data.group === "sandbox"} onClose={() => setOpenId(null)} />
    </div>
  );
}

function CapBadge({ on, label }: { on: boolean; label: string }) {
  return <Badge variant={on ? "green" : "outline"} className="mr-1">{on ? <CheckCircle2 className="size-3" /> : <CircleSlash className="size-3" />}{label}</Badge>;
}

function topLimit(a: ProviderAccount) {
  let best: { name: string; pct: number; used: number; limit: number; source?: string } | null = null;
  for (const [name, l] of Object.entries(a.limits ?? {})) {
    if (!l.limit || name.startsWith("create_per")) continue;
    const pct = Math.round((l.used / l.limit) * 100);
    if (!best || pct > best.pct) best = { name, pct, used: l.used, limit: l.limit, source: l.source };
  }
  return best;
}

function AccountRow({ a, strategy, onOpen }: { a: ProviderAccount; strategy: AccountStrategy; onOpen: () => void }) {
  const t = useTranslations("pa");
  const money = useMoney();
  const s = a.stats;
  const lim = topLimit(a);
  const soonest = [a.token_expires_at, a.credit_expires_at, a.trial_ends_at, a.contract_ends_at].filter(Boolean).sort()[0];
  const cap = a.spend_cap_month || a.spend_cap_day;
  const capUsed = a.spend_cap_month ? s?.spend_month ?? 0 : s?.spend_today ?? 0;
  return (
    <TR className="cursor-pointer" onClick={onOpen} data-account-row={a.id}>
      <TD className="min-w-[10rem]">
        <button type="button" className="text-left font-medium hover:underline" onClick={(e) => { e.stopPropagation(); onOpen(); }}>{a.label}</button>
        <p className="flex flex-wrap items-center gap-1 text-xs text-muted-foreground">
          <Mono>{a.id}</Mono>
          {a.is_default && <Badge variant="outline">{t("default")}</Badge>}
          {a.credential_source === "env" && <Badge variant="outline" title={t("envHint")}>env</Badge>}
          {a.limit_group && <Badge variant="blue" title={t("limitGroupHint")}>{t("limitGroupTag", { g: a.limit_group })}</Badge>}
          {a.billing_group && <Badge variant="outline" title={t("billingGroupHint")}><Wallet className="size-3" />{a.billing_group}</Badge>}
        </p>
      </TD>
      <TD><AccountStatusChip status={a.status} reason={a.status_reason ?? s?.ineligible} cooldownUntil={a.cooldown_until} /></TD>
      <TD className="whitespace-nowrap font-mono text-[13px]">{strategy === "weighted" ? t("weightN", { n: a.weight }) : `#${a.priority}`}</TD>
      <TD>
        <span className="flex flex-col">
          <Money usd={s?.balance_effective_usd} amount={s?.balance_effective} currency={a.currency} />
          {a.balance_source && <span className="text-[11px] text-muted-foreground">{t(`balanceSource.${a.balance_source}`)}</span>}
          {a.balance === undefined && a.credit_remaining !== undefined && <span className="text-[11px] text-muted-foreground">{t("creditLabel")}</span>}
        </span>
      </TD>
      <TD className="hidden w-32 md:table-cell">
        {cap ? (
          <span className="flex flex-col gap-1">
            <Progress value={(capUsed / cap) * 100} warnAt={80} dangerAt={95} />
            <span className="font-mono text-[11.5px] text-muted-foreground">{money(capUsed, a.currency)} / {money(cap, a.currency)}</span>
          </span>
        ) : <span className="font-mono text-[12px] text-muted-foreground">{money(s?.spend_month, a.currency)}</span>}
      </TD>
      <TD><Runway days={s?.runway_days} /></TD>
      <TD className="hidden w-32 lg:table-cell">
        {lim ? (
          <span className="flex flex-col gap-1" title={`${lim.name}: ${lim.used}/${lim.limit}`}>
            <Progress value={lim.pct} warnAt={80} dangerAt={95} />
            <span className="text-[11.5px] text-muted-foreground"><Mono>{lim.name}</Mono> {lim.used}/{lim.limit}{lim.source === "learned" ? ` · ${t("learned")}` : ""}</span>
          </span>
        ) : <span className="text-muted-foreground">—</span>}
      </TD>
      <TD className="hidden lg:table-cell"><Expiry iso={soonest} /></TD>
      <TD className="hidden md:table-cell">{s ? <Breaker state={s.breaker} /> : "—"}</TD>
      <TD className="hidden font-mono text-[13px] sm:table-cell">{s?.hosts_live ?? 0}</TD>
    </TR>
  );
}

// ---------------- thêm tài khoản: 3 bước ----------------
export function AddAccountDialog({ provider, providerName, sandbox, open, onOpenChange, onCreated }: {
  provider: string; providerName: string; sandbox: boolean; open: boolean; onOpenChange: (o: boolean) => void; onCreated?: (id: string) => void;
}) {
  const t = useTranslations("pa");
  const tc = useTranslations("common");
  const [step, setStep] = React.useState(1);
  const [label, setLabel] = React.useState("");
  const [externalId, setExternalId] = React.useState("");
  const [priority, setPriority] = React.useState("");
  const [token, setToken] = React.useState("");
  const [billingToken, setBillingToken] = React.useState("");
  const [currency, setCurrency] = React.useState("USD");
  const [limitGroup, setLimitGroup] = React.useState("");
  const [billingGroup, setBillingGroup] = React.useState("");
  const [test, setTest] = React.useState<AccountTestResult | null>(null);
  const [busy, setBusy] = React.useState(false);
  const { run, pending } = useAction();
  const reset = () => { setStep(1); setLabel(""); setExternalId(""); setPriority(""); setToken(""); setBillingToken(""); setCurrency("USD"); setLimitGroup(""); setBillingGroup(""); setTest(null); };
  const close = (o: boolean) => { onOpenChange(o); if (!o) reset(); };
  const validate = async () => {
    setBusy(true);
    try {
      setTest(await api.validateProviderAccount(provider, token, externalId.trim() || undefined));
    } catch (e) {
      toastError(e);
    } finally {
      setBusy(false);
    }
  };
  const save = async (skipValidate: boolean) => {
    const body: ProviderAccountInput & { label: string; token: string } = { label: label.trim(), token, currency };
    if (externalId.trim()) body.external_id = externalId.trim();
    if (priority) body.priority = Number(priority);
    if (billingToken) body.billing_token = billingToken;
    if (limitGroup.trim()) body.limit_group = limitGroup.trim();
    if (billingGroup.trim()) body.billing_group = billingGroup.trim();
    if (skipValidate) body.skip_validate = true;
    const r = await run("create", () => api.createProviderAccount(provider, body), {
      success: (a) => t("createdToast", { label: a.label }), revalidate: [startsWith(paths.adminProvider(provider)), paths.adminProviders],
    });
    if (r) {
      close(false);
      onCreated?.(r.id);
      for (const w of r.warnings ?? []) toastError(new Error(w));
    }
  };
  const groupOk = (g: string) => g.length <= 64 && !/[\s|/]/.test(g);
  const step1Ok = label.trim().length > 0 && (!priority || (Number(priority) >= 1 && Number(priority) <= 99)) && groupOk(limitGroup.trim()) && groupOk(billingGroup.trim());
  return (
    <Dialog open={open} onOpenChange={close}>
      <DialogContent className="max-w-xl" data-add-account-dialog>
        <DialogHeader>
          <DialogTitle>{t("add.title", { name: providerName })}</DialogTitle>
          <DialogDescription>{t("add.step", { n: step, total: 3 })} — {t(`add.stepName${step}`)}</DialogDescription>
        </DialogHeader>
        <ol className="flex gap-1.5" aria-hidden>
          {[1, 2, 3].map((n) => <li key={n} className={cn("h-1 flex-1 rounded-full", n <= step ? "bg-primary" : "bg-surface-2")} />)}
        </ol>

        {step === 1 && (
          <div className="grid gap-3">
            <p className="text-[13px] text-muted-foreground">{t("add.guide")}</p>
            <Field label={t("f.label")} hint={t("add.labelHint")}>
              <Input value={label} onChange={(e) => setLabel(e.target.value)} placeholder={t("add.labelPlaceholder", { name: providerName })} autoFocus maxLength={80} />
            </Field>
            <div className="grid gap-3 sm:grid-cols-2">
              <Field label={t("f.externalId")} hint={t("add.externalIdHint")}><Input value={externalId} onChange={(e) => setExternalId(e.target.value)} className="font-mono" /></Field>
              <Field label={t("f.priority")} hint={t("add.priorityHint")}><Input type="number" min={1} max={99} value={priority} onChange={(e) => setPriority(e.target.value)} /></Field>
              <Field label={t("f.currency")}>
                <Select value={currency} onChange={(e) => setCurrency(e.target.value)}>
                  {["USD", "EUR", "GBP"].map((c) => <option key={c} value={c}>{c}</option>)}
                </Select>
              </Field>
              {sandbox && <Field label={t("f.limitGroup")} hint={t("limitGroupHint")}><Input value={limitGroup} onChange={(e) => setLimitGroup(e.target.value)} className="font-mono" /></Field>}
              {sandbox && <Field label={t("f.billingGroup")} hint={t("billingGroupHint")}><Input value={billingGroup} onChange={(e) => setBillingGroup(e.target.value)} className="font-mono" /></Field>}
            </div>
          </div>
        )}

        {step === 2 && (
          <div className="grid gap-3">
            <p className="text-[13px] text-muted-foreground">{t("add.keyGuide")}</p>
            <Field label={t("f.token")} hint={t("add.tokenHint")}>
              <Input type="password" autoComplete="off" value={token} onChange={(e) => { setToken(e.target.value); setTest(null); }} className="font-mono" autoFocus data-account-token />
            </Field>
            <Field label={t("f.billingToken")} hint={t("add.billingTokenHint")}>
              <Input type="password" autoComplete="off" value={billingToken} onChange={(e) => setBillingToken(e.target.value)} className="font-mono" />
            </Field>
          </div>
        )}

        {step === 3 && (
          <div className="grid gap-3" data-add-account-test>
            <div className="flex flex-wrap items-center gap-2">
              <Button size="sm" variant="outline" disabled={busy || !token} onClick={validate}><ShieldCheck /> {busy ? tc("checking") : t("add.test")}</Button>
              <span className="text-[13px] text-muted-foreground">{t("add.testHint")}</span>
            </div>
            {test && (
              <div className={cn("rounded-md border p-3 text-[13px]", test.ok ? "border-st-running/30 bg-st-running/5" : "border-st-error/30 bg-st-error/5")}>
                <p className={cn("flex items-center gap-1.5 font-medium", test.ok ? "text-st-running" : "text-st-error")}>
                  {test.ok ? <CheckCircle2 className="size-4" /> : <AlertTriangle className="size-4" />}
                  {test.ok ? t("add.testOk", { ms: test.latency_ms }) : t("add.testFail", { cls: test.error_class ?? "?" })}
                </p>
                {test.error && <p className="mt-1 break-words text-muted-foreground">{test.error}</p>}
                {test.key_fingerprint && <p className="mt-1 text-muted-foreground">{t("f.fingerprint")}: <Mono>{test.key_fingerprint}</Mono></p>}
                {!!test.duplicates?.length && <p className="mt-1 text-st-warning">{t("add.duplicates", { ids: test.duplicates.join(", ") })}</p>}
              </div>
            )}
            <dl className="grid grid-cols-[8rem_minmax(0,1fr)] gap-x-3 gap-y-1 text-[13px]">
              <dt className="text-muted-foreground">{t("f.label")}</dt><dd className="break-words">{label}</dd>
              {externalId && <><dt className="text-muted-foreground">{t("f.externalId")}</dt><dd><Mono>{externalId}</Mono></dd></>}
              <dt className="text-muted-foreground">{t("f.currency")}</dt><dd>{currency}</dd>
              <dt className="text-muted-foreground">{t("f.billingToken")}</dt><dd>{billingToken ? t("add.billingSet") : "—"}</dd>
            </dl>
          </div>
        )}

        <DialogFooter>
          {step > 1 ? <Button variant="ghost" onClick={() => setStep(step - 1)}>{tc("back")}</Button> : <Button variant="ghost" onClick={() => close(false)}>{tc("cancel")}</Button>}
          {step < 3 ? (
            <Button disabled={step === 1 ? !step1Ok : !token} onClick={() => setStep(step + 1)}>{tc("next")}</Button>
          ) : (
            <>
              {test && !test.ok && <Button variant="outline" disabled={!!pending} onClick={() => save(true)}>{t("add.saveAnyway")}</Button>}
              <Button disabled={!test?.ok || !!pending} onClick={() => save(false)} data-add-account-save>{pending ? tc("creating") : t("add.save")}</Button>
            </>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// ---------------- ngăn kéo tài khoản ----------------
export function AccountDrawer({ provider, account, sandbox, onClose }: { provider: string; account: ProviderAccount | null; sandbox: boolean; onClose: () => void }) {
  const t = useTranslations("pa");
  const [tab, setTab] = React.useState("overview");
  React.useEffect(() => { if (account) setTab("overview"); }, [account?.id]); // eslint-disable-line react-hooks/exhaustive-deps
  return (
    <Dialog open={!!account} onOpenChange={(o) => { if (!o) onClose(); }}>
      <SheetContent side="right" className="max-w-xl" aria-describedby={undefined} data-account-drawer>
        {account && (
          <>
            <DialogTitle className="pr-8 text-base">{account.label}</DialogTitle>
            <p className="mb-2 flex flex-wrap items-center gap-1.5 text-xs text-muted-foreground">
              <Mono>{account.id}</Mono>
              <AccountStatusChip status={account.status} reason={account.status_reason} cooldownUntil={account.cooldown_until} />
            </p>
            {account.status_reason && <p className="mb-2 text-[13px] text-muted-foreground">{account.status_reason}</p>}
            {account.stats?.ineligible && <p className="mb-2 text-[13px] text-st-warning">{t("ineligible", { why: account.stats.ineligible })}</p>}
            <AccountActions provider={provider} a={account} onDeleted={onClose} />
            <Tabs value={tab} onValueChange={setTab} className="mt-3">
              <TabsList>
                <TabsTrigger value="overview">{t("tab.overview")}</TabsTrigger>
                <TabsTrigger value="edit">{t("tab.edit")}</TabsTrigger>
                <TabsTrigger value="key">{t("tab.key")}</TabsTrigger>
                <TabsTrigger value="timeline">{t("tab.timeline")}</TabsTrigger>
              </TabsList>
              <TabsContent value="overview"><AccountOverview a={account} /></TabsContent>
              <TabsContent value="edit"><AccountEditForm provider={provider} a={account} sandbox={sandbox} /></TabsContent>
              <TabsContent value="key"><RotateKey provider={provider} a={account} /></TabsContent>
              <TabsContent value="timeline"><AccountTimeline provider={provider} id={account.id} /></TabsContent>
            </Tabs>
          </>
        )}
      </SheetContent>
    </Dialog>
  );
}

function AccountActions({ provider, a, onDeleted }: { provider: string; a: ProviderAccount; onDeleted: () => void }) {
  const t = useTranslations("pa");
  const { run, pending } = useAction();
  const rv = [startsWith(paths.adminProvider(provider)), paths.adminProviders];
  const state = (action: "enable" | "disable" | "drain" | "undrain" | "reset") => run(action, () => api.setProviderAccountState(provider, a.id, action), { success: t(`toast.${action}`), revalidate: rv });
  const archived = a.status === "archived";
  return (
    <div className="flex flex-wrap gap-2" data-account-actions>
      <Button size="xs" variant="outline" disabled={!!pending || archived} onClick={() => run("test", () => api.testProviderAccount(provider, a.id), {
        success: (r) => (r.ok ? t("toast.testOk", { ms: r.latency_ms }) : t("toast.testFail", { error: r.error ?? r.error_class ?? "?" })), revalidate: rv,
      })}><RefreshCw /> {t("action.test")}</Button>
      {a.enabled ? (
        <Button size="xs" variant="outline" disabled={!!pending || archived} onClick={() => state("disable")}><Pause /> {t("action.disable")}</Button>
      ) : (
        <Button size="xs" variant="outline" disabled={!!pending} onClick={() => (archived ? run("restore", () => api.updateProviderAccount(provider, a.id, { enabled: true }), { success: t("toast.enable"), revalidate: rv }) : state("enable"))}><Play /> {t("action.enable")}</Button>
      )}
      {a.drain ? (
        <Button size="xs" variant="outline" disabled={!!pending || archived} onClick={() => state("undrain")}><ArrowDownUp /> {t("action.undrain")}</Button>
      ) : (
        <Button size="xs" variant="outline" disabled={!!pending || archived} onClick={() => state("drain")} title={t("drainHint")}><ArrowDownUp /> {t("action.drain")}</Button>
      )}
      <Button size="xs" variant="outline" disabled={!!pending || archived} onClick={() => state("reset")} title={t("resetHint")}><RotateCcw /> {t("action.reset")}</Button>
      {!archived && <DeleteAccountButton provider={provider} a={a} onDeleted={onDeleted} />}
    </div>
  );
}

function DeleteAccountButton({ provider, a, onDeleted }: { provider: string; a: ProviderAccount; onDeleted: () => void }) {
  const t = useTranslations("pa");
  const tc = useTranslations("common");
  const [open, setOpen] = React.useState(false);
  const [typed, setTyped] = React.useState("");
  const [reason, setReason] = React.useState("");
  const [purge, setPurge] = React.useState(false);
  const { run, pending } = useAction();
  const hosts = a.stats?.hosts_live ?? 0;
  const ok = typed.trim() === a.label && (hosts === 0 || reason.trim().length > 0);
  return (
    <>
      <Button size="xs" variant="outline" className="text-st-error" onClick={() => setOpen(true)} data-delete-account><Trash2 /> {tc("delete")}</Button>
      <Dialog open={open} onOpenChange={(o) => { setOpen(o); if (!o) { setTyped(""); setReason(""); setPurge(false); } }}>
        <DialogContent className="max-w-md">
          <DialogHeader>
            <DialogTitle className="break-words">{t("delete.title", { label: a.label })}</DialogTitle>
            <DialogDescription>{hosts > 0 ? t("delete.descHosts", { n: hosts }) : t("delete.desc")}</DialogDescription>
          </DialogHeader>
          {a.credential_source === "env" && <p className="text-[13px] text-st-warning">{t("delete.env")}</p>}
          {hosts > 0 && <Field label={t("delete.reason")} hint={t("delete.reasonHint")}><Input value={reason} onChange={(e) => setReason(e.target.value)} /></Field>}
          {hosts === 0 && (
            <label className="flex items-start gap-2 text-[13px]">
              <Switch checked={purge} onCheckedChange={setPurge} aria-label={t("delete.purge")} />
              <span>{t("delete.purge")}<span className="block text-xs text-muted-foreground">{t("delete.purgeHint")}</span></span>
            </label>
          )}
          <Field label={t("delete.typeLabel", { label: a.label })}><Input value={typed} onChange={(e) => setTyped(e.target.value)} autoFocus /></Field>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setOpen(false)}>{tc("cancel")}</Button>
            <Button variant="destructive" disabled={!ok || !!pending} onClick={async () => {
              const r = await run("del", () => api.deleteProviderAccount(provider, a.id, { force: hosts > 0, reason: reason.trim() || undefined, purge: hosts === 0 && purge }), {
                success: purge ? t("toast.purged") : t("toast.archived"), revalidate: [startsWith(paths.adminProvider(provider)), paths.adminProviders],
              });
              if (r !== undefined) { setOpen(false); onDeleted(); }
            }}>{purge ? t("delete.purgeConfirm") : t("delete.confirm")}</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}

function Row({ k, children }: { k: string; children: React.ReactNode }) {
  return <><dt className="text-muted-foreground">{k}</dt><dd className="min-w-0 break-words">{children}</dd></>;
}

function AccountOverview({ a }: { a: ProviderAccount }) {
  const t = useTranslations("pa");
  const money = useMoney();
  const { relTime, dateTime } = useFmt();
  const s = a.stats;
  return (
    <div className="grid gap-4 pt-2">
      <dl className="grid grid-cols-[9.5rem_minmax(0,1fr)] gap-x-3 gap-y-1.5 text-[13px]">
        <Row k={t("f.balance")}>
          <Money usd={s?.balance_effective_usd} amount={s?.balance_effective} currency={a.currency} />
          {a.balance_source && <span className="ml-1 text-xs text-muted-foreground">{t(`balanceSource.${a.balance_source}`)}{a.balance_at ? ` · ${relTime(a.balance_at)}` : ""}</span>}
        </Row>
        {a.credit_remaining !== undefined && <Row k={t("f.creditRemaining")}>{money(a.credit_remaining, a.currency)}{a.credit_total ? ` / ${money(a.credit_total, a.currency)}` : ""}{a.credit_at ? <span className="ml-1 text-xs text-muted-foreground">{relTime(a.credit_at)}</span> : null}</Row>}
        <Row k={t("f.spendToday")}><Money usd={s?.spend_today_usd} amount={s?.spend_today} currency={a.currency} /></Row>
        <Row k={t("f.spendMonth")}><Money usd={s?.spend_month_usd} amount={s?.spend_month} currency={a.currency} /></Row>
        <Row k={t("f.burn")}>{s ? `${money(s.burn_per_hour_usd)}/h` : "—"}</Row>
        <Row k={t("f.forecast")}>{money(s?.forecast_month_end)}</Row>
        <Row k={t("col.runway")}><Runway days={s?.runway_days} /></Row>
        <Row k={t("f.minBalance")}>{money(a.min_balance, a.currency)}</Row>
        <Row k={t("f.spendCaps")}>{a.spend_cap_day ? money(a.spend_cap_day, a.currency) : "—"} / {a.spend_cap_month ? money(a.spend_cap_month, a.currency) : "—"}</Row>
        <Row k={t("f.currency")}>{a.currency}{a.fx_rate_usd ? ` · 1 ${a.currency} = ${money(a.fx_rate_usd)}` : ""}</Row>
        <Row k={t("f.billingModel")}>{a.billing_model ?? "—"}</Row>
      </dl>
      <dl className="grid grid-cols-[9.5rem_minmax(0,1fr)] gap-x-3 gap-y-1.5 border-t border-border pt-3 text-[13px]">
        <Row k={t("f.priority")}>#{a.priority} · {t("weightN", { n: a.weight })}</Row>
        <Row k={t("f.regions")}>{a.regions.length ? a.regions.join(", ") : t("allRegions")}</Row>
        {a.pool && <Row k={t("f.pool")}>{a.pool}</Row>}
        <Row k={t("f.externalId")}>{a.external_id ? <Mono>{a.external_id}</Mono> : "—"}</Row>
        {a.limit_group && <Row k={t("f.limitGroup")}><Mono>{a.limit_group}</Mono></Row>}
        {a.billing_group && <Row k={t("f.billingGroup")}><Mono>{a.billing_group}</Mono></Row>}
        <Row k={t("f.fingerprint")}>{a.key_fingerprint ? <Mono>{a.key_fingerprint}</Mono> : "—"}{a.billing_key_fingerprint ? <span className="ml-2 text-xs text-muted-foreground">{t("f.billingToken")}: <Mono>{a.billing_key_fingerprint}</Mono></span> : null}</Row>
        <Row k={t("f.credential")}><Mono>{a.credential_ref}</Mono> ({a.credential_source})</Row>
        {a.scopes.length > 0 && <Row k={t("f.scopes")}>{a.scopes.map((x) => <Badge key={x} variant="outline" className="mr-1 font-mono">{x}</Badge>)}</Row>}
        <Row k={t("f.tokenExpires")}><Expiry iso={a.token_expires_at} /></Row>
        <Row k={t("f.creditExpires")}><Expiry iso={a.credit_expires_at} /></Row>
        {a.trial_ends_at && <Row k={t("f.trialEnds")}><Expiry iso={a.trial_ends_at} /></Row>}
        {a.contract_ends_at && <Row k={t("f.contractEnds")}><Expiry iso={a.contract_ends_at} /></Row>}
        <Row k={t("f.rotatedAt")}>{a.rotated_at ? dateTime(a.rotated_at) : "—"}</Row>
        <Row k={t("col.health")}>{s ? <><Breaker state={s.breaker} /> <span className="ml-1 text-xs text-muted-foreground">{t("lastCheck", { time: relTime(a.last_check_at) })}</span></> : "—"}</Row>
        {a.last_error && <Row k={t("f.lastError")}><span className="text-st-error">{a.last_error_class ? `[${a.last_error_class}] ` : ""}{a.last_error}</span></Row>}
        <Row k={t("col.hosts")}>{s ? `${s.hosts_live} · ${t("boxesN", { n: s.boxes_live })}` : "—"}</Row>
      </dl>
      {Object.keys(a.limits ?? {}).length > 0 && (
        <div className="border-t border-border pt-3">
          <p className="mb-2 text-[13px] font-medium">{t("col.limits")}</p>
          <div className="grid gap-2">
            {Object.entries(a.limits).map(([name, l]) => (
              <div key={name} className="flex items-center gap-2 text-[12.5px]">
                <Mono className="w-40 shrink-0 truncate">{name}</Mono>
                {name.startsWith("create_per") ? <span className="font-mono">{l.limit}</span> : <><Progress value={l.limit ? (l.used / l.limit) * 100 : 0} warnAt={80} dangerAt={95} className="flex-1" /><span className="w-16 text-right font-mono">{l.used}/{l.limit}</span></>}
                <Badge variant={l.source === "learned" ? "yellow" : "outline"}>{l.source ? t(`limitSource.${l.source}`) : "—"}</Badge>
              </div>
            ))}
          </div>
        </div>
      )}
      {a.notes && <p className="whitespace-pre-wrap border-t border-border pt-3 text-[13px] text-muted-foreground">{a.notes}</p>}
    </div>
  );
}

/** Ô số tuỳ chọn: "" = không đổi. */
const num = (v: string) => (v.trim() === "" ? undefined : Number(v));
const dateOnly = (iso?: string) => (iso ? iso.slice(0, 10) : "");

function AccountEditForm({ provider, a, sandbox }: { provider: string; a: ProviderAccount; sandbox: boolean }) {
  const t = useTranslations("pa");
  const tc = useTranslations("common");
  const init = React.useCallback(() => ({
    label: a.label, priority: String(a.priority), weight: String(a.weight), regions: a.regions.join(", "), pool: a.pool ?? "", external_id: a.external_id ?? "",
    currency: a.currency, fx_rate_usd: a.fx_rate_usd ? String(a.fx_rate_usd) : "", balance: "", credit_remaining: "", min_balance: String(a.min_balance ?? 0),
    low_balance_warn: String(a.low_balance_warn ?? 0), spend_cap_day: String(a.spend_cap_day ?? 0), spend_cap_month: String(a.spend_cap_month ?? 0),
    token_expires_at: dateOnly(a.token_expires_at), credit_expires_at: dateOnly(a.credit_expires_at), trial_ends_at: dateOnly(a.trial_ends_at), contract_ends_at: dateOnly(a.contract_ends_at),
    servers: String(a.limits.servers?.limit ?? ""), concurrent_sandboxes: String(a.limits.concurrent_sandboxes?.limit ?? ""),
    create_per_sec: String(a.limits.create_per_sec?.limit ?? ""), create_per_min: String(a.limits.create_per_min?.limit ?? ""),
    limit_group: a.limit_group ?? "", billing_group: a.billing_group ?? "", templates: (a.templates ?? []).join(", "), scopes: a.scopes.join(", "),
    billing_token: "", notes: a.notes ?? "", console_url: a.console_url ?? "",
  }), [a]);
  const [f, setF] = React.useState(init);
  React.useEffect(() => setF(init()), [init]);
  const set = (k: keyof ReturnType<typeof init>) => (e: React.ChangeEvent<HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement>) => setF((x) => ({ ...x, [k]: e.target.value }));
  const { run, pending } = useAction();
  const list = (s: string) => s.split(",").map((x) => x.trim()).filter(Boolean);
  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    const patch: ProviderAccountInput = {};
    const o = init();
    if (f.label.trim() !== o.label) patch.label = f.label.trim();
    if (f.priority !== o.priority) patch.priority = Number(f.priority);
    if (f.weight !== o.weight) patch.weight = Number(f.weight);
    if (f.regions !== o.regions) patch.regions = list(f.regions);
    if (f.pool !== o.pool) patch.pool = f.pool.trim();
    if (f.external_id !== o.external_id) patch.external_id = f.external_id.trim();
    if (f.currency !== o.currency) patch.currency = f.currency;
    if (f.fx_rate_usd !== o.fx_rate_usd) patch.fx_rate_usd = num(f.fx_rate_usd) ?? 0;
    if (f.balance.trim()) patch.balance = Number(f.balance);
    if (f.credit_remaining.trim()) patch.credit_remaining = Number(f.credit_remaining);
    for (const k of ["min_balance", "low_balance_warn", "spend_cap_day", "spend_cap_month"] as const) if (f[k] !== o[k]) patch[k] = num(f[k]) ?? 0;
    for (const k of ["token_expires_at", "credit_expires_at", "trial_ends_at", "contract_ends_at"] as const) if (f[k] !== o[k]) patch[k] = f[k];
    const limits: Record<string, number> = {};
    for (const k of ["servers", "concurrent_sandboxes", "create_per_sec", "create_per_min"] as const) if (f[k] !== o[k]) limits[k] = num(f[k]) ?? 0;
    if (Object.keys(limits).length) patch.limits = limits;
    if (f.limit_group !== o.limit_group) patch.limit_group = f.limit_group.trim();
    if (f.billing_group !== o.billing_group) patch.billing_group = f.billing_group.trim();
    if (f.templates !== o.templates) patch.templates = list(f.templates);
    if (f.scopes !== o.scopes) patch.scopes = list(f.scopes);
    if (f.billing_token) patch.billing_token = f.billing_token;
    if (f.notes !== o.notes) patch.notes = f.notes;
    if (f.console_url !== o.console_url) patch.console_url = f.console_url.trim();
    if (!Object.keys(patch).length) return;
    const r = await run("patch", () => api.updateProviderAccount(provider, a.id, patch), {
      success: tc("saved"), revalidate: [startsWith(paths.adminProvider(provider)), paths.adminProviders],
    });
    for (const w of r?.warnings ?? []) toastError(new Error(w));
  };
  const n = (k: keyof ReturnType<typeof init>, label: string, hint?: string, extra?: React.InputHTMLAttributes<HTMLInputElement>) => (
    <Field label={label} hint={hint}><Input type="number" step="any" value={f[k]} onChange={set(k)} {...extra} /></Field>
  );
  const d = (k: keyof ReturnType<typeof init>, label: string) => <Field label={label}><Input type="date" value={f[k]} onChange={set(k)} /></Field>;
  const env = a.credential_source === "env";
  return (
    <form onSubmit={submit} className="grid gap-4 pt-2" data-account-edit>
      <fieldset className="grid gap-3 sm:grid-cols-2">
        <legend className="mb-2 text-[13px] font-semibold">{t("edit.scheduling")}</legend>
        <Field label={t("f.label")} className="sm:col-span-2"><Input value={f.label} onChange={set("label")} maxLength={80} required /></Field>
        {n("priority", t("f.priority"), t("add.priorityHint"), { min: 1, max: 99, step: 1 })}
        {n("weight", t("f.weight"), t("weightHint"), { min: 1, max: 1000, step: 1 })}
        <Field label={t("f.regions")} hint={t("edit.regionsHint")}><Input value={f.regions} onChange={set("regions")} className="font-mono" /></Field>
        <Field label={t("f.pool")}><Select value={f.pool} onChange={set("pool")}><option value="">—</option>{["free", "pro", "team", "burst"].map((p) => <option key={p} value={p}>{p}</option>)}</Select></Field>
        <Field label={t("f.externalId")} hint={t("add.externalIdHint")} className="sm:col-span-2"><Input value={f.external_id} onChange={set("external_id")} className="font-mono" /></Field>
      </fieldset>
      <fieldset className="grid gap-3 border-t border-border pt-3 sm:grid-cols-2">
        <legend className="mb-2 pt-3 text-[13px] font-semibold">{t("edit.money")}</legend>
        <Field label={t("f.currency")}><Select value={f.currency} onChange={set("currency")}>{["USD", "EUR", "GBP"].map((c) => <option key={c} value={c}>{c}</option>)}</Select></Field>
        {n("fx_rate_usd", t("f.fxRate"), t("edit.fxHint"), { min: 0 })}
        {n("balance", t("f.balanceManual"), t("edit.balanceHint"), { placeholder: a.balance !== undefined ? String(a.balance) : "" })}
        {n("credit_remaining", t("f.creditRemaining"), t("edit.creditHint"), { placeholder: a.credit_remaining !== undefined ? String(a.credit_remaining) : "" })}
        {n("min_balance", t("f.minBalance"), sandbox ? t("edit.minBalanceSandbox") : t("edit.minBalanceHint"), { min: 0 })}
        {n("low_balance_warn", t("f.lowBalanceWarn"), undefined, { min: 0 })}
        {n("spend_cap_day", t("f.spendCapDay"), t("edit.capHint"), { min: 0 })}
        {n("spend_cap_month", t("f.spendCapMonth"), t("edit.capHint"), { min: 0 })}
        {d("credit_expires_at", t("f.creditExpires"))}
        {d("token_expires_at", t("f.tokenExpires"))}
        {d("trial_ends_at", t("f.trialEnds"))}
        {d("contract_ends_at", t("f.contractEnds"))}
      </fieldset>
      <fieldset className="grid gap-3 border-t border-border pt-3 sm:grid-cols-2">
        <legend className="mb-2 pt-3 text-[13px] font-semibold">{t("edit.limits")}</legend>
        <p className="text-xs text-muted-foreground sm:col-span-2">{t("edit.limitsHint")}</p>
        {sandbox ? (
          <>
            {n("concurrent_sandboxes", "concurrent_sandboxes", undefined, { min: 0, step: 1 })}
            {n("create_per_sec", "create_per_sec", undefined, { min: 0 })}
            {n("create_per_min", "create_per_min", undefined, { min: 0 })}
          </>
        ) : n("servers", "servers", undefined, { min: 0, step: 1 })}
        {sandbox && (
          <>
            <Field label={t("f.limitGroup")} hint={t("limitGroupHint")}><Input value={f.limit_group} onChange={set("limit_group")} className="font-mono" /></Field>
            <Field label={t("f.billingGroup")} hint={t("billingGroupHint")}><Input value={f.billing_group} onChange={set("billing_group")} className="font-mono" /></Field>
            <Field label={t("f.templates")} hint={t("edit.templatesHint")} className="sm:col-span-2"><Input value={f.templates} onChange={set("templates")} className="font-mono" /></Field>
            <Field label={t("f.scopes")} hint={t("edit.scopesHint")} className="sm:col-span-2"><Input value={f.scopes} onChange={set("scopes")} className="font-mono" /></Field>
          </>
        )}
      </fieldset>
      <fieldset className="grid gap-3 border-t border-border pt-3">
        <legend className="mb-2 pt-3 text-[13px] font-semibold">{t("edit.other")}</legend>
        {!env && <Field label={t("f.billingToken")} hint={a.billing_key_fingerprint ? t("edit.billingTokenSet", { fp: a.billing_key_fingerprint }) : t("add.billingTokenHint")}>
          <Input type="password" autoComplete="off" value={f.billing_token} onChange={set("billing_token")} className="font-mono" />
        </Field>}
        <Field label={t("f.consoleUrl")}><Input type="url" value={f.console_url} onChange={set("console_url")} /></Field>
        <Field label={t("f.notes")}><Textarea rows={3} value={f.notes} onChange={set("notes")} /></Field>
      </fieldset>
      <div className="flex justify-end gap-2">
        <Button type="button" variant="ghost" onClick={() => setF(init())}>{t("edit.revert")}</Button>
        <Button type="submit" disabled={!!pending}>{pending ? tc("processing") : tc("saveChanges")}</Button>
      </div>
    </form>
  );
}

function RotateKey({ provider, a }: { provider: string; a: ProviderAccount }) {
  const t = useTranslations("pa");
  const [token, setToken] = React.useState("");
  const [ext, setExt] = React.useState(a.external_id ?? "");
  const { run, pending } = useAction();
  if (a.credential_source === "env") return <p className="pt-3 text-[13px] text-muted-foreground">{t("rotate.env", { ref: a.credential_ref })}</p>;
  return (
    <form className="grid gap-3 pt-3" onSubmit={async (e) => {
      e.preventDefault();
      const r = await run("rotate", () => api.rotateProviderAccount(provider, a.id, token, ext.trim() || undefined), {
        success: t("rotate.done"), revalidate: [startsWith(paths.adminProvider(provider)), paths.adminProviders],
      });
      if (r) { setToken(""); for (const w of r.warnings ?? []) toastError(new Error(w)); }
    }}>
      <p className="text-[13px] text-muted-foreground">{t("rotate.desc")}</p>
      <Field label={t("f.token")}><Input type="password" autoComplete="off" value={token} onChange={(e) => setToken(e.target.value)} className="font-mono" /></Field>
      <Field label={t("f.externalId")} hint={a.external_id ? t("rotate.extRequired", { id: a.external_id }) : t("rotate.extUnknown")}>
        <Input value={ext} onChange={(e) => setExt(e.target.value)} className="font-mono" />
      </Field>
      <div className="flex justify-end"><Button type="submit" disabled={!token || !!pending}><KeyRound /> {t("rotate.submit")}</Button></div>
    </form>
  );
}

function AccountTimeline({ provider, id }: { provider: string; id: string }) {
  const t = useTranslations("pa");
  const { dateTime } = useFmt();
  const { data, error } = useApi<List<BoxmesEvent>>(paths.providerAccountTimeline(provider, id), { refreshInterval: 30000 });
  if (error) return <div className="pt-3"><ErrorBox error={error} /></div>;
  if (!data) return <div className="pt-3"><LoadingRows rows={3} /></div>;
  if (!data.items.length) return <p className="pt-3 text-[13px] text-muted-foreground">{t("timelineEmpty")}</p>;
  return (
    <ol className="grid gap-2 pt-3">
      {data.items.map((e) => (
        <li key={e.id} className="flex gap-3 text-[12.5px]">
          <span className="w-24 shrink-0 font-mono text-muted-foreground">{dateTime(e.ts)}</span>
          <span className="min-w-0"><Mono className="text-foreground">{e.type.replace(/^provider_account\./, "")}</Mono> <span className="break-words text-muted-foreground">{e.message}</span></span>
        </li>
      ))}
    </ol>
  );
}
