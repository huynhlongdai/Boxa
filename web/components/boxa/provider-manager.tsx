"use client";
// v1.3 smart provider manager: wizard kết nối provider (chọn → dán token → kiểm tra → region/size → ưu tiên & trần chi phí → lưu),
// bảng sức khỏe (latency p50/p95, tỉ lệ thành công, thời gian dựng, cầu dao) + điểm có giải thích, và catalog giá gộp.
import * as React from "react";
import { useTranslations } from "next-intl";
import { Activity, CheckCircle2, ChevronLeft, ChevronRight, Plug, RefreshCw, RotateCcw, Settings2, ShieldAlert, XCircle, Zap } from "lucide-react";
import { api, paths } from "@/lib/api";
import { startsWith, useAction, useApi } from "@/lib/hooks";
import { useFmt } from "@/lib/format";
import type { Catalog, List, Provider, ProviderHealth, ProviderPrefs, ProviderRegion, ValidateTokenResult } from "@/lib/types";
import { cn } from "@/lib/utils";
import { Card } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Field, Input, Select } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { Table, TBody, TD, TH, THead, TR } from "@/components/ui/table";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Empty, ErrorBox, LoadingRows, SectionTitle } from "./page";
import { Mono } from "./status";
import { ScoreBreakdown, ScorePill } from "./score";

const STATUS_VARIANT = { healthy: "green", degraded: "yellow", down: "red", unknown: "gray" } as const;
const BREAKER_VARIANT: Record<string, "green" | "yellow" | "red" | "gray"> = { closed: "green", half_open: "yellow", open: "red" };
const pct = (n: number) => `${Math.round(n * 1000) / 10}%`;

export function HealthBadge({ status }: { status: string }) {
  const t = useTranslations("pm");
  const v = STATUS_VARIANT[status as keyof typeof STATUS_VARIANT] ?? "gray";
  return <Badge variant={v}>{t(`status.${(status in STATUS_VARIANT ? status : "unknown") as keyof typeof STATUS_VARIANT}`)}</Badge>;
}
function BreakerBadge({ state }: { state: string }) {
  const t = useTranslations("pm");
  const k = ["closed", "half_open", "open"].includes(state) ? state : "closed";
  return <Badge variant={BREAKER_VARIANT[k]} title={t(`breakerHelp.${k}`)}><Zap className="size-3" />{t(`breaker.${k}`)}</Badge>;
}

// ---------------- Prefs form (dùng trong wizard và dialog chỉnh) ----------------
type PrefsDraft = { enabled: boolean; priority: string; max_hourly_usd: string; max_monthly_usd: string; max_servers: string; regions: string[]; default_region: string };
const toDraft = (p?: Partial<ProviderPrefs>): PrefsDraft => ({
  enabled: p?.enabled ?? true,
  priority: String(p?.priority ?? 0),
  max_hourly_usd: String(p?.max_hourly_usd ?? 0),
  max_monthly_usd: String(p?.max_monthly_usd ?? 0),
  max_servers: String(p?.max_servers ?? 0),
  regions: p?.regions ?? [],
  default_region: p?.default_region ?? "",
});
const fromDraft = (d: PrefsDraft): Partial<ProviderPrefs> => ({
  enabled: d.enabled,
  priority: Math.max(0, Math.round(Number(d.priority) || 0)),
  max_hourly_usd: Math.max(0, Number(d.max_hourly_usd) || 0),
  max_monthly_usd: Math.max(0, Number(d.max_monthly_usd) || 0),
  max_servers: Math.max(0, Math.round(Number(d.max_servers) || 0)),
  regions: d.regions,
  default_region: d.default_region,
});

function PrefsForm({ draft, set, regions }: { draft: PrefsDraft; set: (d: PrefsDraft) => void; regions: ProviderRegion[] }) {
  const t = useTranslations("pm");
  const toggle = (id: string) => set({ ...draft, regions: draft.regions.includes(id) ? draft.regions.filter((r) => r !== id) : [...draft.regions, id], default_region: draft.default_region === id && draft.regions.includes(id) ? "" : draft.default_region });
  return (
    <div className="flex flex-col gap-3">
      <label className="flex items-center justify-between gap-3 rounded-md border border-border px-3 py-2 text-[13px]">
        <span>{t("prefs.enabled")}<span className="block text-xs text-muted-foreground">{t("prefs.enabledHint")}</span></span>
        <Switch checked={draft.enabled} onCheckedChange={(v) => set({ ...draft, enabled: v })} aria-label={t("prefs.enabled")} />
      </label>
      <div className="grid gap-3 sm:grid-cols-2">
        <Field label={t("prefs.priority")} hint={t("prefs.priorityHint")}><Input type="number" min={0} inputMode="numeric" value={draft.priority} onChange={(e) => set({ ...draft, priority: e.target.value })} /></Field>
        <Field label={t("prefs.maxServers")} hint={t("prefs.zeroNoLimit")}><Input type="number" min={0} inputMode="numeric" value={draft.max_servers} onChange={(e) => set({ ...draft, max_servers: e.target.value })} /></Field>
        <Field label={t("prefs.maxHourly")} hint={t("prefs.zeroNoLimit")}><Input type="number" min={0} step="0.001" inputMode="decimal" value={draft.max_hourly_usd} onChange={(e) => set({ ...draft, max_hourly_usd: e.target.value })} /></Field>
        <Field label={t("prefs.maxMonthly")} hint={t("prefs.zeroNoLimit")}><Input type="number" min={0} step="1" inputMode="decimal" value={draft.max_monthly_usd} onChange={(e) => set({ ...draft, max_monthly_usd: e.target.value })} /></Field>
      </div>
      <div>
        <p className="mb-1 text-[13px] font-medium">{t("prefs.regions")}</p>
        <p className="mb-1.5 text-xs text-muted-foreground">{t("prefs.regionsHint")}</p>
        {regions.length === 0 ? <p className="text-xs text-muted-foreground">—</p> : (
          <div className="flex flex-wrap gap-1.5" role="group" aria-label={t("prefs.regions")}>
            {regions.map((r) => {
              const on = draft.regions.includes(r.id);
              return (
                <button type="button" key={r.id} title={r.name} aria-pressed={on} onClick={() => toggle(r.id)}
                  className={cn("rounded-full border px-2 py-0.5 font-mono text-[11.5px]", on ? "border-primary/50 bg-primary/15 text-primary" : "border-border text-muted-foreground hover:text-foreground")}>
                  {r.id}
                </button>
              );
            })}
          </div>
        )}
      </div>
      <Field label={t("prefs.defaultRegion")}>
        <Select value={draft.default_region} onChange={(e) => set({ ...draft, default_region: e.target.value })}>
          <option value="">{t("prefs.auto")}</option>
          {(draft.regions.length ? regions.filter((r) => draft.regions.includes(r.id)) : regions).map((r) => <option key={r.id} value={r.id}>{r.id} · {r.name}</option>)}
        </Select>
      </Field>
    </div>
  );
}

// ---------------- Wizard ----------------
const STEPS = ["choose", "token", "review", "prefs"] as const;

export function ConnectProviderWizard({ providers, open, onOpenChange, initial }: { providers: Provider[]; open: boolean; onOpenChange: (o: boolean) => void; initial?: string }) {
  const t = useTranslations("pm");
  const tc = useTranslations("common");
  const { usd, mb } = useFmt();
  const choices = providers.filter((p) => p.id !== "local");
  const [step, setStep] = React.useState(0);
  const [pid, setPid] = React.useState(initial ?? "");
  const [token, setToken] = React.useState("");
  const [val, setVal] = React.useState<ValidateTokenResult | null>(null);
  const [validating, setValidating] = React.useState(false);
  const [draft, setDraft] = React.useState<PrefsDraft>(toDraft());
  const { run, pending } = useAction();
  const p = choices.find((x) => x.id === pid);

  React.useEffect(() => {
    if (open) { setStep(initial ? 1 : 0); setPid(initial ?? ""); setToken(""); setVal(null); setDraft(toDraft()); }
  }, [open, initial]);

  const validate = async () => {
    if (!p) return;
    setValidating(true);
    const r = await run("val", () => api.validateProvider(p.id, token));
    setValidating(false);
    if (r) {
      setVal(r);
      if (r.ok) {
        const cur = await api.getProviderPrefs(p.id).catch(() => null);
        setDraft(toDraft({ ...cur, regions: cur?.regions?.length ? cur.regions : [] }));
        setStep(2);
      }
    }
  };
  const save = async () => {
    if (!p) return;
    const ok = await run("save", async () => {
      if (token) await api.setProviderToken(p.id, token);
      return api.saveProviderPrefs(p.id, fromDraft(draft));
    }, { success: t("wizard.saved", { name: p.name }), revalidate: [paths.providers, startsWith("/providers"), startsWith("/catalog")] });
    if (ok) onOpenChange(false);
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-2xl" data-connect-wizard>
        <DialogHeader>
          <DialogTitle>{t("wizard.title")}</DialogTitle>
          <DialogDescription>{t("wizard.desc")}</DialogDescription>
        </DialogHeader>
        <ol className="flex flex-wrap items-center gap-x-2 gap-y-1 text-[12px]" aria-label={t("wizard.steps")}>
          {STEPS.map((s, i) => (
            <li key={s} className={cn("flex items-center gap-1.5", i === step ? "font-medium text-foreground" : "text-muted-foreground")}>
              <span className={cn("grid size-5 place-items-center rounded-full border text-[11px]", i < step ? "border-st-running/50 bg-st-running/15 text-st-running" : i === step ? "border-primary text-primary" : "border-border")}>{i + 1}</span>
              {t(`wizard.step.${s}`)}
              {i < STEPS.length - 1 && <ChevronRight className="size-3 text-muted-foreground" aria-hidden />}
            </li>
          ))}
        </ol>

        {step === 0 && (
          <div className="grid gap-2 sm:grid-cols-2">
            {choices.map((c) => (
              <button key={c.id} type="button" onClick={() => setPid(c.id)} aria-pressed={pid === c.id}
                className={cn("flex items-start justify-between gap-2 rounded-lg border p-3 text-left text-[13px] transition-colors", pid === c.id ? "border-primary bg-primary/5" : "border-border hover:bg-surface-2")}>
                <span className="min-w-0">
                  <span className="block font-medium">{c.name}</span>
                  <span className="mt-0.5 block text-xs text-muted-foreground">{c.implemented === false ? t("wizard.feedOnly") : t("wizard.apiReady")}</span>
                </span>
                {c.connected ? <Badge variant="green">{t("wizard.connected")}</Badge> : <Badge variant="outline">{t("wizard.notConnected")}</Badge>}
              </button>
            ))}
          </div>
        )}

        {step === 1 && p && (
          <div className="flex flex-col gap-3">
            <p className="text-[13px] text-muted-foreground">{t("wizard.tokenFor", { name: p.name })}</p>
            {["hetzner", "vultr", "digitalocean", "contabo", "netcup"].includes(p.id) && <p className="text-xs text-muted-foreground">{t(`wizard.where.${p.id as "hetzner"}`)}</p>}
            <Input type="password" autoComplete="off" value={token} onChange={(e) => { setToken(e.target.value); setVal(null); }} placeholder={p.connected ? t("wizard.keepToken") : t("wizard.paste")} className="font-mono" aria-label={t("wizard.tokenFor", { name: p.name })} />
            {val && !val.ok && (
              <div className="flex items-start gap-2 rounded-md border border-st-error/30 bg-st-error/5 p-3 text-[13px]" role="alert">
                <XCircle className="mt-0.5 size-4 shrink-0 text-st-error" />
                <div className="min-w-0">
                  <p className="font-medium text-st-error">{val.error}</p>
                  {val.hint && <p className="mt-0.5 text-muted-foreground">{val.hint}</p>}
                  {val.error_class && <p className="mt-0.5 font-mono text-[11.5px] text-muted-foreground">{t("wizard.errClass", { cls: val.error_class, ms: Math.round(val.latency_ms) })}</p>}
                </div>
              </div>
            )}
          </div>
        )}

        {step === 2 && p && val?.ok && (
          <div className="flex flex-col gap-3 text-[13px]">
            <p className="flex items-center gap-1.5 text-st-running"><CheckCircle2 className="size-4" /> {t("wizard.valid", { ms: Math.round(val.latency_ms) })}</p>
            <div>
              <p className="mb-1 text-xs text-muted-foreground">{t("wizard.regions", { n: val.regions.length })}</p>
              <div className="flex flex-wrap gap-1">{val.regions.map((r) => <Badge key={r.id} variant="outline" className="font-mono" title={r.name}>{r.id}</Badge>)}</div>
            </div>
            <div>
              <p className="mb-1 text-xs text-muted-foreground">{t("wizard.sizes", { n: val.sizes.length, total: val.size_count })}</p>
              <div className="max-h-56 overflow-auto rounded-md border border-border">
                <Table>
                  <THead><TR className="hover:bg-transparent"><TH>{t("cat.sku")}</TH><TH>{tc("region")}</TH><TH>{t("cat.config")}</TH><TH>{t("cat.hour")}</TH></TR></THead>
                  <TBody>
                    {val.sizes.slice(0, 20).map((o) => (
                      <TR key={`${o.sku}-${o.region}`}>
                        <TD><Mono>{o.sku}</Mono></TD><TD><Mono>{o.region}</Mono></TD>
                        <TD className="whitespace-nowrap"><Mono className="text-muted-foreground">{o.cpu}c · {mb(o.ram_mb)} · {o.disk_gb}G</Mono></TD>
                        <TD className="font-mono text-[13px]">{usd(o.price_hour)}</TD>
                      </TR>
                    ))}
                  </TBody>
                </Table>
              </div>
            </div>
          </div>
        )}

        {step === 3 && p && <PrefsForm draft={draft} set={setDraft} regions={val?.regions ?? []} />}

        <DialogFooter className="gap-2 sm:justify-between">
          <Button variant="ghost" disabled={step === 0 || (step === 1 && !!initial)} onClick={() => setStep((s) => Math.max(0, s - 1))}><ChevronLeft /> {t("wizard.back")}</Button>
          {step === 0 && <Button disabled={!p} onClick={() => setStep(1)}>{t("wizard.next")} <ChevronRight /></Button>}
          {step === 1 && <Button disabled={(!token && !p?.connected) || validating} onClick={validate}><Plug /> {validating ? tc("checking") : t("wizard.validate")}</Button>}
          {step === 2 && <Button onClick={() => setStep(3)}>{t("wizard.next")} <ChevronRight /></Button>}
          {step === 3 && <Button disabled={!!pending} onClick={save}><CheckCircle2 /> {pending ? tc("processing") : t("wizard.save")}</Button>}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function PrefsDialog({ h, open, onOpenChange }: { h: ProviderHealth; open: boolean; onOpenChange: (o: boolean) => void }) {
  const t = useTranslations("pm");
  const tc = useTranslations("common");
  const [draft, setDraft] = React.useState(toDraft(h.prefs));
  const { data: regions } = useApi<List<ProviderRegion>>(open ? paths.providerRegions(h.provider.id) : null, { refreshInterval: 0 });
  const { run, pending } = useAction();
  React.useEffect(() => { if (open) setDraft(toDraft(h.prefs)); }, [open, h.prefs]);
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-xl">
        <DialogHeader>
          <DialogTitle>{t("prefs.title", { name: h.provider.name })}</DialogTitle>
          <DialogDescription>{t("prefs.desc")}</DialogDescription>
        </DialogHeader>
        <PrefsForm draft={draft} set={setDraft} regions={regions?.items ?? h.prefs.regions.map((id) => ({ id, name: id, available: true }))} />
        <DialogFooter>
          <Button variant="ghost" onClick={() => onOpenChange(false)}>{tc("cancel")}</Button>
          <Button disabled={!!pending} onClick={async () => {
            const r = await run("p", () => api.saveProviderPrefs(h.provider.id, fromDraft(draft)), { success: t("prefs.saved"), revalidate: [startsWith("/providers"), startsWith("/catalog")] });
            if (r) onOpenChange(false);
          }}>{pending ? tc("processing") : tc("save")}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// ---------------- Health ----------------
function Metric({ k, v, sub, warn }: { k: string; v: React.ReactNode; sub?: React.ReactNode; warn?: boolean }) {
  return (
    <div className="min-w-0">
      <p className="truncate text-[11.5px] text-muted-foreground">{k}</p>
      <p className={cn("font-mono text-[13px] tabular-nums", warn && "text-st-warning")}>{v}</p>
      {sub && <p className="truncate text-[11px] text-muted-foreground">{sub}</p>}
    </div>
  );
}

function HealthCard({ h }: { h: ProviderHealth }) {
  const t = useTranslations("pm");
  const { relTime, num } = useFmt();
  const { run, pending } = useAction();
  const [prefsOpen, setPrefsOpen] = React.useState(false);
  const [why, setWhy] = React.useState(false);
  const s = h.stats;
  const br = s.breaker;
  const reval = [startsWith("/providers"), startsWith("/catalog")];
  const ms = (n: number) => (n ? `${num(n, 0)} ms` : "—");
  const sec = (n: number) => (n ? `${num(n, 0)} s` : "—");
  return (
    <Card className="flex min-w-0 flex-col p-4" data-provider-health={h.provider.id}>
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div className="min-w-0">
          <p className="font-medium">{h.provider.name}</p>
          <p className="mt-0.5 text-xs text-muted-foreground">{t("health.samples", { n: s.samples })} · {t("health.hosts", { n: h.hosts })}{h.prefs.priority ? ` · ${t("health.priority", { n: h.prefs.priority })}` : ""}</p>
        </div>
        <div className="flex flex-wrap items-center gap-1.5">
          <HealthBadge status={s.status} />
          <BreakerBadge state={br.state} />
        </div>
      </div>
      {h.blocked && <p className="mt-2 flex items-start gap-1.5 text-[12.5px] text-st-warning"><ShieldAlert className="mt-0.5 size-3.5 shrink-0" /> <span className="min-w-0 break-words">{h.blocked}</span></p>}

      <div className="mt-3 grid grid-cols-2 gap-x-3 gap-y-2.5 sm:grid-cols-3">
        <Metric k={t("health.apiLatency")} v={`${ms(s.api_latency_p50_ms)}`} sub={t("health.p95", { v: ms(s.api_latency_p95_ms) })} warn={s.api_latency_p95_ms > 1000} />
        <Metric k={t("health.apiSuccess")} v={s.samples ? pct(s.api_success_rate) : "—"} warn={s.samples > 0 && s.api_success_rate < 0.95} />
        <Metric k={t("health.createSuccess")} v={s.create_attempts ? pct(s.create_success_rate) : "—"} sub={t("health.attempts", { n: s.create_attempts })} warn={s.create_attempts > 0 && s.create_success_rate < 0.9} />
        <Metric k={t("health.provision")} v={sec(s.provision_p50_s)} sub={t("health.p95", { v: sec(s.provision_p95_s) })} />
        <Metric k={t("health.lastOk")} v={relTime(s.last_ok_at)} />
        <Metric k={t("health.rateLimit")} v={s.rate_limit.remaining >= 0 ? t("health.remaining", { n: s.rate_limit.remaining }) : "—"} sub={s.rate_limit.hits ? t("health.hits", { n: s.rate_limit.hits }) : undefined} />
      </div>

      {(br.state !== "closed" || s.last_error) && (
        <div className="mt-3 rounded-md border border-border bg-surface-2/40 p-2.5 text-[12.5px]">
          {br.state !== "closed" && (
            <p>{t("health.breakerInfo", { n: br.consecutive_failures, reason: br.last_reason || "—" })}{br.open_until && br.state === "open" ? ` · ${t("health.until", { time: relTime(br.open_until) })}` : ""}</p>
          )}
          {s.last_error && <p className="mt-0.5 break-words text-muted-foreground">{t("health.lastError", { time: relTime(s.last_error_at) })}: <Mono>{s.last_error}</Mono>{s.last_error_class ? <Badge variant="outline" className="ml-1">{s.last_error_class}</Badge> : null}</p>}
          {Object.keys(s.error_classes).length > 0 && (
            <div className="mt-1.5 flex flex-wrap gap-1">{Object.entries(s.error_classes).map(([k, n]) => <Badge key={k} variant="outline" className="font-mono">{k} × {n}</Badge>)}</div>
          )}
        </div>
      )}

      {h.score ? (
        <div className="mt-3 rounded-md border border-primary/25 bg-primary/5 p-2.5">
          <button type="button" className="flex w-full items-center justify-between gap-2 text-left" onClick={() => setWhy((x) => !x)} aria-expanded={why}>
            <span className="min-w-0 text-[12.5px]">
              <span className="font-medium text-primary">{t("health.score")}</span>
              <span className="ml-1.5 text-muted-foreground"><Mono>{h.score.sku} · {h.score.region}</Mono></span>
            </span>
            <span className="flex items-center gap-1.5"><ScorePill score={h.score.score} className="text-base" /><ChevronRight className={cn("size-4 text-muted-foreground transition-transform", why && "rotate-90")} /></span>
          </button>
          {why && (
            <div className="mt-2 flex flex-col gap-2">
              <p className="text-[11.5px] text-muted-foreground">{t("health.scoreHint")}</p>
              <ScoreBreakdown breakdown={h.score.breakdown} />
              {h.score.reasons.length > 0 && <ul className="list-disc pl-4 text-[12px] text-muted-foreground">{h.score.reasons.map((r) => <li key={r}>{r}</li>)}</ul>}
            </div>
          )}
        </div>
      ) : (
        <p className="mt-3 text-xs text-muted-foreground">{t("health.noScore")}</p>
      )}

      <div className="mt-auto pt-3"><div className="flex flex-wrap gap-2 border-t border-border pt-3">
        <Button size="xs" variant="outline" disabled={!!pending} onClick={() => run("chk", () => api.checkProvider(h.provider.id), { success: t("health.checked", { name: h.provider.name }), revalidate: reval })}>
          <RefreshCw className={cn(pending === "chk" && "animate-spin")} /> {t("health.check")}
        </Button>
        <Button size="xs" variant="outline" disabled={!!pending || br.state === "closed"} title={t("health.resetHint")} onClick={() => run("rst", () => api.resetProvider(h.provider.id), { success: t("health.resetDone", { name: h.provider.name }), revalidate: reval })}>
          <RotateCcw /> {t("health.reset")}
        </Button>
        <Button size="xs" variant="ghost" onClick={() => setPrefsOpen(true)}><Settings2 /> {t("health.prefs")}</Button>
      </div></div>
      <PrefsDialog h={h} open={prefsOpen} onOpenChange={setPrefsOpen} />
    </Card>
  );
}

export function ProviderHealthView() {
  const t = useTranslations("pm");
  const { data, error, mutate } = useApi<List<ProviderHealth>>(paths.providersHealth, { refreshInterval: 30000 });
  if (error) return <ErrorBox error={error} retry={() => mutate()} />;
  if (!data) return <LoadingRows rows={3} />;
  const rows = [...data.items].sort((a, b) => (a.prefs.priority || 99) - (b.prefs.priority || 99));
  const counts = rows.reduce<Record<string, number>>((acc, r) => ((acc[r.stats.status] = (acc[r.stats.status] ?? 0) + 1), acc), {});
  return (
    <>
      <SectionTitle right={<span className="flex flex-wrap gap-1.5">{(["healthy", "degraded", "down", "unknown"] as const).filter((k) => counts[k]).map((k) => <Badge key={k} variant={STATUS_VARIANT[k]}>{t(`status.${k}`)} · {counts[k]}</Badge>)}</span>}>
        <span className="inline-flex items-center gap-1.5"><Activity className="size-4 text-muted-foreground" /> {t("health.title")}</span>
      </SectionTitle>
      <p className="mb-3 max-w-3xl text-[13px] text-muted-foreground">{t("health.desc")}</p>
      <div className="mb-6 grid gap-3 md:grid-cols-2 xl:grid-cols-3">{rows.map((h) => <HealthCard key={h.provider.id} h={h} />)}</div>
    </>
  );
}

// ---------------- Catalog ----------------
export function CatalogView() {
  const t = useTranslations("pm");
  const tc = useTranslations("common");
  const { usd, mb, relTime, seconds } = useFmt();
  const { data, error, mutate } = useApi<Catalog>(paths.catalog(), { refreshInterval: 60000 });
  const [prov, setProv] = React.useState("");
  const [arch, setArch] = React.useState("");
  const [minCpu, setMinCpu] = React.useState(0);
  const [usable, setUsable] = React.useState(false);
  if (error) return <ErrorBox error={error} retry={() => mutate()} />;
  if (!data) return <LoadingRows rows={4} />;
  const provs = [...new Set(data.items.map((i) => i.provider))].sort();
  const items = data.items.filter((i) => (!prov || i.provider === prov) && (!arch || i.arch === arch) && i.cpu >= minCpu && (!usable || i.usable));
  const staleN = data.sources.filter((s) => s.stale).length;
  return (
    <>
      <SectionTitle right={<span className="text-xs text-muted-foreground">{t("cat.generated", { time: relTime(data.generated_at), stale: seconds(data.stale_after_s) })}</span>}>{t("cat.title")}</SectionTitle>
      <p className="mb-3 max-w-3xl text-[13px] text-muted-foreground">{t("cat.desc")}</p>
      <div className="mb-3 flex flex-wrap gap-1.5" data-catalog-sources>
        {data.sources.map((s) => (
          <Badge key={`${s.provider}-${s.source}`} variant={s.stale ? "yellow" : "outline"} title={t("cat.sourceTitle", { n: s.count, time: relTime(s.fetched_at) })}>
            <Mono>{s.provider}</Mono> · {s.source.endsWith("-api") ? t("cat.live") : t("cat.feed")} · {relTime(s.fetched_at)}{s.stale ? ` · ${t("cat.stale")}` : ""}
          </Badge>
        ))}
        {staleN > 0 && <span className="text-xs text-st-warning">{t("cat.staleNote", { n: staleN })}</span>}
      </div>
      <div className="mb-3 grid grid-cols-2 gap-2 sm:flex sm:flex-wrap sm:items-center">
        <Select value={prov} onChange={(e) => setProv(e.target.value)} aria-label={tc("provider")} className="sm:w-40"><option value="">{t("cat.allProviders")}</option>{provs.map((p) => <option key={p} value={p}>{p}</option>)}</Select>
        <Select value={arch} onChange={(e) => setArch(e.target.value)} aria-label={t("cat.arch")} className="sm:w-32"><option value="">{t("cat.anyArch")}</option><option value="amd64">amd64</option><option value="arm64">arm64</option></Select>
        <Select value={minCpu} onChange={(e) => setMinCpu(Number(e.target.value))} aria-label={t("cat.minCpu")} className="sm:w-36"><option value={0}>{t("cat.anyCpu")}</option>{[2, 4, 8].map((n) => <option key={n} value={n}>≥ {n} vCPU</option>)}</Select>
        <label className="col-span-2 flex items-center gap-2 text-[13px] text-muted-foreground"><Switch checked={usable} onCheckedChange={setUsable} aria-label={t("cat.usableOnly")} /> {t("cat.usableOnly")}</label>
      </div>
      {items.length === 0 ? <Empty title={t("cat.empty")} /> : (
        <Card>
          <Table>
            <THead>
              <TR className="hover:bg-transparent">
                <TH>{tc("provider")}</TH><TH>{t("cat.sku")}</TH><TH>{tc("region")}</TH><TH>{t("cat.config")}</TH><TH>{t("cat.hour")}</TH>
                <TH className="hidden md:table-cell">{t("cat.month")}</TH><TH>{t("cat.source")}</TH><TH className="hidden lg:table-cell">{t("cat.age")}</TH><TH>{t("cat.state")}</TH>
              </TR>
            </THead>
            <TBody>
              {items.map((i) => (
                <TR key={`${i.provider}-${i.sku}-${i.region}`} className={cn(!i.usable && "opacity-70")}>
                  <TD className="whitespace-nowrap"><Mono>{i.provider}</Mono></TD>
                  <TD className="whitespace-nowrap"><Mono>{i.sku}</Mono></TD>
                  <TD className="whitespace-nowrap"><Mono>{i.region}</Mono></TD>
                  <TD className="whitespace-nowrap"><Mono className="text-muted-foreground">{i.cpu}c · {mb(i.ram_mb)} · {i.disk_gb}G · {i.arch}</Mono></TD>
                  <TD className="whitespace-nowrap font-mono text-[13px]">
                    {usd(i.price_hour)}
                    {i.feed_price_hour != null && <span className="ml-1 text-[11px] text-muted-foreground line-through" title={t("cat.feedPrice")}>{usd(i.feed_price_hour)}</span>}
                  </TD>
                  <TD className="hidden font-mono text-[13px] md:table-cell">{usd(i.price_month, 2)}</TD>
                  <TD className="whitespace-nowrap"><Badge variant={i.price_source.endsWith("-api") ? "blue" : "outline"}>{i.price_source.endsWith("-api") ? t("cat.live") : t("cat.feed")}</Badge></TD>
                  <TD className="hidden whitespace-nowrap text-[12.5px] lg:table-cell">{relTime(i.fetched_at)}{i.stale && <Badge variant="yellow" className="ml-1">{t("cat.stale")}</Badge>}</TD>
                  <TD className="max-w-[16rem]">
                    {i.usable ? <HealthBadge status={i.health} /> : <span className="block truncate text-[12px] text-st-warning" title={i.blocked ?? t("cat.notConnected")}>{i.blocked ?? t("cat.notConnected")}</span>}
                  </TD>
                </TR>
              ))}
            </TBody>
          </Table>
        </Card>
      )}
      <p className="mt-2 text-xs text-muted-foreground">{t("cat.count", { n: items.length, total: data.total })}</p>
    </>
  );
}
