"use client";
// v1.3 tốc độ: tóm tắt /speed (warm pool, CoW, cache, bin-packing), chỉnh warm pool (min/max/TTL theo pool),
// gợi ý right-sizing cho cả đội box và cho từng box.
import * as React from "react";
import Link from "next/link";
import { useTranslations } from "next-intl";
import { Flame, Gauge, Plus, Scale, Trash2 } from "lucide-react";
import { api, paths } from "@/lib/api";
import { useAction, useApi } from "@/lib/hooks";
import { useFmt } from "@/lib/format";
import type { List, ProfilesResponse, RightSize, SpeedView, WarmPoolSpec, WarmView } from "@/lib/types";
import { cn } from "@/lib/utils";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input, Select } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { Table, TBody, TD, TH, THead, TR } from "@/components/ui/table";
import { Empty, ErrorBox, LoadingRows, SectionTitle } from "./page";
import { Mono } from "./status";

function Stat({ k, v, sub }: { k: string; v: React.ReactNode; sub?: React.ReactNode }) {
  return (
    <Card className="min-w-0 p-3">
      <p className="truncate text-xs text-muted-foreground">{k}</p>
      <p className="mt-0.5 font-mono text-lg tabular-nums">{v}</p>
      {sub && <p className="truncate text-[11.5px] text-muted-foreground">{sub}</p>}
    </Card>
  );
}

export function SpeedSummary() {
  const t = useTranslations("speed");
  const { num, ms } = useFmt();
  const { data, error, mutate } = useApi<SpeedView>(paths.speed, { refreshInterval: 30000 });
  if (error) return <ErrorBox error={error} retry={() => mutate()} />;
  if (!data) return <LoadingRows rows={2} />;
  return (
    <>
      <SectionTitle right={<Badge variant="outline">{t(data.placement === "spread" ? "spread" : "binpack")}</Badge>}>
        <span className="inline-flex items-center gap-1.5"><Gauge className="size-4 text-muted-foreground" /> {t("title")}</span>
      </SectionTitle>
      <div className="mb-4 grid grid-cols-2 gap-3 lg:grid-cols-4" data-speed-summary>
        <Stat k={t("warmReady")} v={data.warm_enabled ? data.warm_ready : t("off")} sub={t("hitRate", { pct: Math.round(data.warm.hit_rate * 100), hits: data.warm.hits, misses: data.warm.misses })} />
        <Stat k={t("claim")} v={ms(data.avg_claim_ms)} sub={t("cold", { v: ms(data.avg_cold_ms) })} />
        <Stat k={t("speedup")} v={`×${num(data.speedup, 1)}`} sub={t("speedupHint")} />
        <Stat k={t("compact")} v={data.exec_compact ? t("on") : t("off")} sub={t("compactHint")} />
      </div>
      <Card className="mb-4">
        <Table>
          <THead><TR className="hover:bg-transparent"><TH>{t("host")}</TH><TH>{t("cow")}</TH><TH>{t("features")}</TH><TH>{t("load")}</TH><TH className="hidden sm:table-cell">{t("boxes")}</TH><TH>{t("coldEta")}</TH></TR></THead>
          <TBody>
            {data.hosts.map((h) => (
              <TR key={h.host_id}>
                <TD className="whitespace-nowrap"><Mono>{h.host_id}</Mono><span className="ml-1 text-xs text-muted-foreground">{h.provider}/{h.region}</span></TD>
                <TD><Badge variant={h.cow === "copy" ? "yellow" : "green"} className="font-mono">{h.cow}</Badge></TD>
                <TD><span className="flex flex-wrap gap-1">{h.vm && <Badge variant="outline">VM</Badge>}{h.cache && <Badge variant="outline">{t("cache")}</Badge>}{h.mesh && <Badge variant="outline">mesh</Badge>}<Badge variant="outline" className="font-mono">{h.arch}</Badge></span></TD>
                <TD className="font-mono text-[13px]">{Math.round(h.load_pct)}%</TD>
                <TD className="hidden font-mono text-[13px] sm:table-cell">{h.boxes}</TD>
                <TD className="whitespace-nowrap font-mono text-[13px]">~{num(h.cold_eta_s, 1)} s</TD>
              </TR>
            ))}
          </TBody>
        </Table>
      </Card>
      {data.cache_env.length > 0 && (
        <details className="mb-6 text-[13px]">
          <summary className="cursor-pointer select-none text-muted-foreground hover:text-foreground">{t("cacheEnv", { n: data.cache_env.length })}</summary>
          <ul className="mt-1.5 flex flex-col gap-0.5">{data.cache_env.map((e) => <li key={e}><Mono className="break-all text-[12px]">{e}</Mono></li>)}</ul>
        </details>
      )}
    </>
  );
}

type Row = { template: string; profile: string; cpu: string; ram_mb: string; disk_gb: string; min: string; max: string; ttl: string };
const toRow = (p: WarmPoolSpec): Row => ({ template: p.template, profile: p.profile ?? "", cpu: String(p.spec.cpu), ram_mb: String(p.spec.ram_mb), disk_gb: String(p.spec.disk_gb), min: String(p.min), max: String(p.max), ttl: String(p.ttl_minutes) });
const fromRow = (r: Row): WarmPoolSpec => ({ template: r.template.trim(), profile: r.profile || undefined, spec: { cpu: Number(r.cpu) || 1, ram_mb: Number(r.ram_mb) || 1024, disk_gb: Number(r.disk_gb) || 10 }, min: Number(r.min) || 0, max: Number(r.max) || 0, ttl_minutes: Number(r.ttl) || 30 });

export function WarmPoolSettings() {
  const t = useTranslations("speed");
  const tc = useTranslations("common");
  const { relTime } = useFmt();
  const { data, error, mutate } = useApi<WarmView>(paths.warmPool, { refreshInterval: 30000 });
  const { data: profiles } = useApi<ProfilesResponse>(paths.profiles, { refreshInterval: 0 });
  const [enabled, setEnabled] = React.useState(true);
  const [rows, setRows] = React.useState<Row[]>([]);
  const [dirty, setDirty] = React.useState(false);
  const { run, pending } = useAction();
  React.useEffect(() => {
    if (data && !dirty) { setEnabled(data.enabled); setRows(data.pools.map(toRow)); }
  }, [data, dirty]);
  if (error) return <ErrorBox error={error} retry={() => mutate()} />;
  if (!data) return <LoadingRows rows={2} />;
  const upd = (i: number, k: keyof Row, v: string) => { setDirty(true); setRows((rs) => rs.map((r, j) => (j === i ? { ...r, [k]: v } : r))); };
  const invalid = rows.some((r) => !r.template.trim() || Number(r.min) < 0 || Number(r.max) < Number(r.min) || Number(r.max) > 10 || Number(r.ttl) < 5 || Number(r.ttl) > 1440);
  const num = (i: number, k: keyof Row, label: string, extra?: React.InputHTMLAttributes<HTMLInputElement>) => (
    <Input type="number" inputMode="numeric" aria-label={label} value={rows[i][k]} onChange={(e) => upd(i, k, e.target.value)} className="h-8 w-[4.5rem] px-2 font-mono text-[13px]" {...extra} />
  );
  return (
    <>
      <SectionTitle right={<label className="flex items-center gap-2 text-[13px]">{t("warmEnabled")}<Switch checked={enabled} onCheckedChange={(v) => { setDirty(true); setEnabled(v); }} aria-label={t("warmEnabled")} /></label>}>
        <span className="inline-flex items-center gap-1.5"><Flame className="size-4 text-st-warning" /> {t("warmTitle")}</span>
      </SectionTitle>
      <p className="mb-3 max-w-3xl text-[13px] text-muted-foreground">{t("warmDesc")}</p>
      <Card className={cn("mb-3", !enabled && "opacity-70")} data-warm-pool>
        <Table>
          <THead>
            <TR className="hover:bg-transparent">
              <TH>{t("template")}</TH><TH>{t("profile")}</TH><TH>{t("spec")}</TH><TH>{t("min")}</TH><TH>{t("max")}</TH><TH>{t("ttlMin")}</TH><TH>{t("ready")}</TH><TH aria-label={tc("actions")} />
            </TR>
          </THead>
          <TBody>
            {rows.map((r, i) => (
              <TR key={i}>
                <TD><Input aria-label={t("template")} value={r.template} onChange={(e) => upd(i, "template", e.target.value)} className="h-8 w-32 px-2 font-mono text-[13px]" /></TD>
                <TD>
                  <Select aria-label={t("profile")} value={r.profile} onChange={(e) => upd(i, "profile", e.target.value)} className="h-8 w-52 text-[13px]">
                    <option value="">—</option>
                    {profiles?.items.map((p) => <option key={p.id} value={p.id}>{p.id}</option>)}
                  </Select>
                </TD>
                <TD className="whitespace-nowrap"><span className="flex items-center gap-1">{num(i, "cpu", "vCPU", { min: 1 })}<span className="text-xs text-muted-foreground">c</span>{num(i, "ram_mb", "RAM MB", { min: 256, step: 256, className: "h-8 w-[5.5rem] px-2 font-mono text-[13px]" })}<span className="text-xs text-muted-foreground">MB</span></span></TD>
                <TD>{num(i, "min", t("min"), { min: 0, max: 10 })}</TD>
                <TD>{num(i, "max", t("max"), { min: 0, max: 10 })}</TD>
                <TD>{num(i, "ttl", t("ttlMin"), { min: 5, max: 1440 })}</TD>
                <TD className="font-mono text-[13px]">{data.ready[r.template] ?? 0}</TD>
                <TD><Button size="icon-sm" variant="ghost" aria-label={tc("delete")} onClick={() => { setDirty(true); setRows((rs) => rs.filter((_, j) => j !== i)); }}><Trash2 /></Button></TD>
              </TR>
            ))}
          </TBody>
        </Table>
      </Card>
      {invalid && <p className="mb-2 text-xs text-st-error">{t("invalid")}</p>}
      <div className="mb-3 flex flex-wrap items-center gap-2">
        <Button size="sm" variant="outline" onClick={() => { setDirty(true); setRows((rs) => [...rs, { template: "base", profile: "general", cpu: "1", ram_mb: "1024", disk_gb: "10", min: "1", max: "2", ttl: "30" }]); }}><Plus /> {t("addPool")}</Button>
        <Button size="sm" disabled={!dirty || invalid || !!pending} onClick={async () => {
          const r = await run("w", () => api.saveWarmPool({ enabled, pools: rows.map(fromRow) }), { success: t("saved"), revalidate: [paths.warmPool, paths.speed] });
          if (r) setDirty(false);
        }}>{pending ? tc("processing") : tc("saveChanges")}</Button>
        {dirty && <Button size="sm" variant="ghost" onClick={() => setDirty(false)}>{tc("cancel")}</Button>}
      </div>
      <p className="mb-6 text-xs text-muted-foreground">
        {t("warmStats", { created: data.stats.created, expired: data.stats.expired, entries: data.entries.length })}
        {data.stats.last_refill ? ` · ${t("lastRefill", { time: relTime(data.stats.last_refill) })}` : ""}
        {data.stats.refill_error ? <span className="text-st-error"> · {data.stats.refill_error}</span> : null}
      </p>
    </>
  );
}

// ---------------- Right-sizing ----------------
const SUG_VARIANT: Record<RightSize["suggestion"], "green" | "yellow" | "red" | "blue" | "gray"> = { keep: "green", downsize: "blue", upsize: "yellow", sleep: "yellow", stop: "red", insufficient_data: "gray" };

export function SuggestionBadge({ s }: { s: RightSize["suggestion"] }) {
  const t = useTranslations("speed");
  return <Badge variant={SUG_VARIANT[s]}>{t(`sug.${s}`)}</Badge>;
}

function SpecChange({ r }: { r: RightSize }) {
  const { mb } = useFmt();
  if (!r.suggested_spec) return <Mono className="text-muted-foreground">{r.spec.cpu}c · {mb(r.spec.ram_mb)}</Mono>;
  return <Mono>{r.spec.cpu}c · {mb(r.spec.ram_mb)} <span className="text-muted-foreground">→</span> {r.suggested_spec.cpu}c · {mb(r.suggested_spec.ram_mb)}</Mono>;
}

export function RightsizeList() {
  const t = useTranslations("speed");
  const { usd } = useFmt();
  const { data, error, mutate } = useApi<List<RightSize>>(paths.rightsize, { refreshInterval: 60000 });
  if (error) return <ErrorBox error={error} retry={() => mutate()} />;
  if (!data) return <LoadingRows rows={3} />;
  const rows = [...data.items].sort((a, b) => b.savings_per_month - a.savings_per_month);
  const total = rows.reduce((a, r) => a + r.savings_per_month, 0);
  return (
    <Card className="mb-6 min-w-0" data-rightsize-fleet>
      <CardHeader className="flex flex-row flex-wrap items-center justify-between gap-2 space-y-0">
        <CardTitle className="flex items-center gap-2"><Scale className="size-4 text-primary" /> {t("rsTitle")}</CardTitle>
        <span className="text-[13px] text-muted-foreground">{t("rsTotal", { usd: usd(total, 2) })}</span>
      </CardHeader>
      <CardContent className="p-0">
        {rows.length === 0 ? <div className="p-4"><Empty title={t("rsEmpty")} /></div> : (
          <Table>
            <THead><TR className="hover:bg-transparent"><TH>{t("box")}</TH><TH>{t("suggestion")}</TH><TH>{t("spec")}</TH><TH className="hidden md:table-cell">{t("usage")}</TH><TH>{t("saving")}</TH><TH className="hidden lg:table-cell">{t("why")}</TH></TR></THead>
            <TBody>
              {rows.map((r) => (
                <TR key={r.box_id}>
                  <TD className="whitespace-nowrap"><Link href={`/boxes/${r.box_id}`} className="hover:underline"><Mono>{r.slug}</Mono></Link></TD>
                  <TD><SuggestionBadge s={r.suggestion} /></TD>
                  <TD className="whitespace-nowrap"><SpecChange r={r} /></TD>
                  <TD className="hidden whitespace-nowrap md:table-cell"><Mono className="text-muted-foreground">CPU {Math.round(r.cpu_p95_pct)}% · RAM {Math.round(r.ram_p95_mb)} MB</Mono></TD>
                  <TD className="whitespace-nowrap font-mono text-[13px]">{r.savings_per_month > 0 ? t("perMonth", { usd: usd(r.savings_per_month, 2) }) : "—"}</TD>
                  <TD className="hidden max-w-[22rem] text-[12.5px] text-muted-foreground lg:table-cell"><span className="line-clamp-2">{r.reasons.join("; ")}</span></TD>
                </TR>
              ))}
            </TBody>
          </Table>
        )}
      </CardContent>
    </Card>
  );
}

export function RightsizeCard({ boxId, enabled }: { boxId: string; enabled: boolean }) {
  const t = useTranslations("speed");
  const { usd, seconds } = useFmt();
  const { data } = useApi<RightSize>(enabled ? paths.boxRightsize(boxId) : null, { refreshInterval: 60000 });
  if (!enabled) return null;
  return (
    <Card data-rightsize-box>
      <CardHeader><CardTitle className="flex items-center gap-2"><Scale className="size-4 text-primary" /> {t("rsBoxTitle")}</CardTitle></CardHeader>
      <CardContent className="flex flex-col gap-2 text-[13px]">
        {!data ? <LoadingRows rows={1} /> : (
          <>
            <div className="flex flex-wrap items-center justify-between gap-2"><SuggestionBadge s={data.suggestion} /><SpecChange r={data} /></div>
            {data.savings_per_month > 0 && <p className="font-mono">{t("perMonth", { usd: usd(data.savings_per_month, 2) })} <span className="text-muted-foreground">({t("perHour", { usd: usd(data.savings_per_hour) })})</span></p>}
            {data.reasons.map((r) => <p key={r} className="text-muted-foreground">{r}</p>)}
            <p className="text-[11.5px] text-muted-foreground">{t("window", { n: data.samples, time: seconds(data.window_s) })}</p>
          </>
        )}
      </CardContent>
    </Card>
  );
}
