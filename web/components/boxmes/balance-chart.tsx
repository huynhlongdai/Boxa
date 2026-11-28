"use client";
// v2.3 (M29 pha B): biểu đồ số dư / chi theo thời gian của tài khoản provider (ngăn kéo → Tổng quan) và xu hướng
// runway cấp provider (/infra/providers/[id]). Dữ liệu: GET …/accounts/{a}/balance/history và …/providers/{p}/balance/history
// (một snapshot mỗi lần thăm dò, mặc định 15 phút). Badge "cũ" khi lần đọc số dư qua API gần nhất bị lỗi (fail-soft).
import * as React from "react";
import { Area, CartesianGrid, ComposedChart, Line, LineChart, ResponsiveContainer, Tooltip, XAxis, YAxis } from "recharts";
import { useTranslations } from "next-intl";
import { TrendingDown, TrendingUp } from "lucide-react";
import { paths } from "@/lib/api";
import { useApi } from "@/lib/hooks";
import { useFmt } from "@/lib/format";
import { TIME_ZONE } from "@/i18n/config";
import type { BalanceHistory, BalancePoint, ProviderAccount, ProviderBalanceHistory } from "@/lib/types";
import { AGENT_COLORS, cn } from "@/lib/utils";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { ErrorBox, LoadingRows } from "@/components/boxmes/page";

const axis = { stroke: "#5b6472", fontSize: 11, tickLine: false, axisLine: false } as const;
const BAL = AGENT_COLORS[0];
const BURN = "#a78bfa"; // tím: tách khỏi vàng của đoạn "cũ"
const STALE = "#f59e0b";
const WINDOWS = [1, 7, 30, 90] as const;
const MAX_POINTS = 240;

/** Giữ điểm cuối mỗi bucket để biểu đồ ≤ MAX_POINTS điểm (90 ngày × 96 lần/ngày = 8 640 snapshot). */
function downsample<T extends { ts: string }>(pts: T[], max = MAX_POINTS): T[] {
  if (pts.length <= max) return pts;
  const t0 = Date.parse(pts[0].ts);
  const span = Date.parse(pts[pts.length - 1].ts) - t0 || 1;
  const out: T[] = [];
  let bucket = -1;
  for (const p of pts) {
    const b = Math.floor(((Date.parse(p.ts) - t0) / span) * (max - 1));
    if (b !== bucket) { out.push(p); bucket = b; } else out[out.length - 1] = p;
  }
  return out;
}

/** Badge "cũ" khi lần đọc số dư qua API gần nhất lỗi (số dư hiện là số đọc tốt gần nhất). */
export function BalanceStaleBadge({ a, className }: { a: Pick<ProviderAccount, "balance_stale" | "balance_error" | "balance_at">; className?: string }) {
  const t = useTranslations("pa.bal");
  const { relTime } = useFmt();
  if (!a.balance_stale) return null;
  return (
    <Badge variant="yellow" className={className} title={t("staleHint", { error: a.balance_error ?? "?", time: relTime(a.balance_at) })} data-balance-stale>
      {t("stale")}
    </Badge>
  );
}

function Trend({ v }: { v?: number }) {
  const t = useTranslations("pa.bal");
  if (v === undefined || v === null) return <span className="text-muted-foreground">—</span>;
  const down = v < 0;
  const Icon = down ? TrendingDown : TrendingUp;
  return (
    <span className={cn("inline-flex items-center gap-1 font-mono text-[12.5px]", down ? "text-st-warning" : "text-st-running")}>
      <Icon className="size-3.5" />{t("trendPerDay", { n: Math.round(v * 100) / 100 })}
    </span>
  );
}

function WindowPicker({ value, onChange }: { value: number; onChange: (d: number) => void }) {
  const t = useTranslations("pa.bal");
  return (
    <div className="inline-flex gap-1" role="group" aria-label={t("window")}>
      {WINDOWS.map((d) => (
        <Button key={d} size="xs" variant={d === value ? "default" : "ghost"} aria-pressed={d === value} onClick={() => onChange(d)}>{t("days", { n: d })}</Button>
      ))}
    </div>
  );
}

type Row = { ts: string; balance?: number; stale_balance?: number; burn: number; runway?: number; stale: boolean; error?: string };

function toRows(points: BalancePoint[]): Row[] {
  const rows = downsample(points).map((p) => {
    const v = p.balance_effective ?? p.balance;
    return { ts: p.ts, balance: p.stale ? undefined : v, stale_balance: p.stale ? v : undefined, burn: p.burn_per_hour_usd, runway: p.runway_days, stale: p.stale, error: p.error };
  });
  // nối đoạn "cũ" (nét đứt) vào điểm tốt ngay trước nó
  for (let i = 1; i < rows.length; i++) if (rows[i].stale && !rows[i - 1].stale) rows[i - 1].stale_balance = rows[i - 1].balance;
  return rows;
}

function ChartTip({ active, payload, currency }: { active?: boolean; payload?: { payload: Row }[]; currency: string }) {
  const t = useTranslations("pa.bal");
  const { dateTime, usd } = useFmt();
  const money = useCurrency();
  if (!active || !payload?.length) return null;
  const r = payload[0].payload;
  const bal = r.balance ?? r.stale_balance;
  return (
    <div className="rounded-md border border-border bg-popover px-3 py-2 text-xs shadow-xl">
      <p className="mb-1 font-medium">{dateTime(r.ts)}</p>
      <p className="flex justify-between gap-4"><span>{t("balance")}</span><span className="font-mono">{money(bal, currency)}</span></p>
      <p className="flex justify-between gap-4"><span>{t("burn")}</span><span className="font-mono">{usd(r.burn)}/h</span></p>
      {r.runway !== undefined && <p className="flex justify-between gap-4"><span>{t("runway")}</span><span className="font-mono">{t("daysN", { n: r.runway })}</span></p>}
      {r.stale && <p className="mt-1 max-w-[16rem] break-words text-st-warning">{t("stalePoint", { error: r.error ?? "?" })}</p>}
    </div>
  );
}

function useCurrency() {
  const { tag, usd } = useFmt();
  return React.useCallback((n: number | null | undefined, currency = "USD") => {
    if (n === null || n === undefined || Number.isNaN(n)) return "—";
    if (currency === "USD") return usd(n, 2);
    try {
      return new Intl.NumberFormat(tag, { style: "currency", currency, currencyDisplay: "narrowSymbol", maximumFractionDigits: 2 }).format(n);
    } catch {
      return `${n.toFixed(2)} ${currency}`;
    }
  }, [tag, usd]);
}

/** Biểu đồ số dư (vùng, theo tiền gốc) + burn/giờ (đường, USD) của một tài khoản; nét đứt vàng = số dư giữ lại khi đọc lỗi. */
export function AccountBalanceChart({ provider, account }: { provider: string; account: ProviderAccount }) {
  const t = useTranslations("pa.bal");
  const { shortDate, dateTime, relTime, usd, tag } = useFmt();
  const money = useCurrency();
  const [days, setDays] = React.useState(7);
  const { data, error, mutate } = useApi<BalanceHistory>(paths.accountBalanceHistory(provider, account.id, days), { refreshInterval: 60000 });
  const rows = React.useMemo(() => (data ? toRows(data.points) : []), [data]);
  const fmtTick = (v: string) => (days <= 1 ? new Date(v).toLocaleTimeString(tag, { hour: "2-digit", minute: "2-digit", timeZone: TIME_ZONE }) : shortDate(v));
  return (
    <div className="border-t border-border pt-3" data-balance-chart={account.id}>
      <div className="mb-2 flex flex-wrap items-center justify-between gap-2">
        <p className="flex flex-wrap items-center gap-1.5 text-[13px] font-medium">
          {t("title")}
          {data && <Badge variant="outline">{data.balance_api ? t("sourceApi") : t(`source.${data.source}`)}</Badge>}
          {data?.stale && <BalanceStaleBadge a={{ balance_stale: true, balance_error: data.balance_error, balance_at: data.balance_at }} />}
        </p>
        <WindowPicker value={days} onChange={setDays} />
      </div>
      {error ? <ErrorBox error={error} retry={() => mutate()} /> : !data ? <LoadingRows rows={2} /> : rows.length === 0 ? (
        <p className="rounded-md border border-dashed border-border px-3 py-6 text-center text-[13px] text-muted-foreground">
          {data.balance_api ? t("emptyApi", { min: Math.round(data.interval_s / 60) }) : t("emptyManual")}
        </p>
      ) : (
        <>
          <ResponsiveContainer width="100%" height={180}>
            <ComposedChart data={rows} margin={{ top: 6, right: 0, left: -6, bottom: 0 }}>
              <CartesianGrid stroke="#262b33" vertical={false} />
              <XAxis dataKey="ts" tickFormatter={fmtTick} minTickGap={28} {...axis} />
              <YAxis yAxisId="bal" tickFormatter={(v: number) => money(v, data.currency)} width={64} {...axis} />
              <YAxis yAxisId="burn" orientation="right" tickFormatter={(v: number) => usd(v, 2)} width={44} {...axis} />
              <Tooltip content={<ChartTip currency={data.currency} />} />
              <Area yAxisId="bal" type="monotone" dataKey="balance" name={t("balance")} stroke={BAL} fill={BAL} fillOpacity={0.15} strokeWidth={1.6} connectNulls={false} isAnimationActive={false} />
              <Line yAxisId="bal" type="monotone" dataKey="stale_balance" name={t("stale")} stroke={STALE} strokeDasharray="4 3" dot={false} strokeWidth={1.6} isAnimationActive={false} />
              <Line yAxisId="burn" type="stepAfter" dataKey="burn" name={t("burn")} stroke={BURN} dot={false} strokeWidth={1.2} strokeOpacity={0.8} isAnimationActive={false} />
            </ComposedChart>
          </ResponsiveContainer>
          <dl className="mt-2 grid grid-cols-2 gap-x-3 gap-y-1 text-[12.5px] sm:grid-cols-4">
            <div><dt className="text-muted-foreground">{t("spent")}</dt><dd className="font-mono">{money(data.spent, data.currency)}</dd></div>
            <div><dt className="text-muted-foreground">{t("topUps")}</dt><dd className="font-mono">{data.top_ups ? money(data.top_ups, data.currency) : "—"}</dd></div>
            <div><dt className="text-muted-foreground">{t("runwayTrend")}</dt><dd><Trend v={data.runway_trend_per_day} /></dd></div>
            <div><dt className="text-muted-foreground">{t("polled")}</dt><dd title={dateTime(data.polled_at)}>{data.balance_api ? relTime(data.polled_at) : "—"}</dd></div>
          </dl>
          {data.stale && data.balance_error && <p className="mt-2 break-words text-[12.5px] text-st-warning">{t("staleLine", { error: data.balance_error, time: relTime(data.balance_at) })}</p>}
          {data.balance_api && <p className="mt-1 text-[11.5px] text-muted-foreground">{t("apiNote", { min: Math.round(data.interval_s / 60) })}</p>}
        </>
      )}
    </div>
  );
}

/** Sparkline runway (hoặc số dư khi chưa có burn) của cả provider, 7 ngày, kèm xu hướng/ngày và số tài khoản đọc lỗi. */
export function ProviderRunwayTrend({ provider }: { provider: string }) {
  const t = useTranslations("pa.bal");
  const { usd } = useFmt();
  const { data } = useApi<ProviderBalanceHistory>(paths.providerBalanceHistory(provider, 7), { refreshInterval: 120000 });
  if (!data || data.points.length === 0) return null;
  const useRunway = data.points.some((p) => p.runway_days !== undefined);
  const rows = data.points.map((p) => ({ ts: p.ts, v: useRunway ? p.runway_days : p.balance_usd }));
  const last = data.points[data.points.length - 1];
  const stale = data.accounts.filter((a) => a.stale).length;
  return (
    <Card className="col-span-2 flex flex-col gap-3 p-3.5 sm:flex-row sm:items-center md:col-span-4" data-runway-trend={provider}>
      <div className="min-w-[10rem]">
        <p className="text-xs text-muted-foreground">{useRunway ? t("runwayTrend7d") : t("balanceTrend7d")}</p>
        <p className="mt-0.5 font-mono text-lg font-semibold leading-tight">
          {useRunway ? (last.runway_days !== undefined ? t("daysN", { n: last.runway_days }) : "—") : usd(last.balance_usd, 2)}
        </p>
        <div className="mt-0.5 flex flex-wrap items-center gap-1.5 text-xs">
          {useRunway && <Trend v={data.runway_trend_per_day} />}
          {stale > 0 && <Badge variant="yellow" title={t("staleAccountsHint")}>{t("staleAccounts", { n: stale })}</Badge>}
        </div>
      </div>
      <div className="h-14 min-w-0 flex-1" aria-label={useRunway ? t("runwayTrend7d") : t("balanceTrend7d")} role="img">
        <ResponsiveContainer width="100%" height="100%">
          <LineChart data={rows} margin={{ top: 4, right: 2, left: 2, bottom: 2 }}>
            <YAxis hide domain={["auto", "auto"]} />
            <Line type="monotone" dataKey="v" stroke={useRunway && last.runway_days !== undefined && last.runway_days < 7 ? STALE : BAL} dot={false} strokeWidth={1.6} isAnimationActive={false} />
          </LineChart>
        </ResponsiveContainer>
      </div>
      <p className="text-[11.5px] text-muted-foreground sm:max-w-[13rem]">{t("burnNow", { v: usd(last.burn_per_hour_usd, 2) })} · {t("accountsN", { n: last.accounts })}</p>
    </Card>
  );
}
