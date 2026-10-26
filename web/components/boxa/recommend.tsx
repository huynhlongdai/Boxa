"use client";
// v1.3 chọn box theo nhu cầu: form yêu cầu + chọn profile (7 workload profile + 22 mặc định theo loại agent)
// → POST /recommend → phương án xếp hạng (điểm 6 yếu tố, ETA, giá, lý do), phương án bị loại, nút "Tạo với phương án này".
import * as React from "react";
import { useRouter } from "next/navigation";
import { useTranslations } from "next-intl";
import { Ban, ChevronRight, Clock, Rocket, Sparkles, Wand2 } from "lucide-react";
import { toast } from "sonner";
import { api, paths } from "@/lib/api";
import { startsWith, useAction, useApi } from "@/lib/hooks";
import { useFmt } from "@/lib/format";
import { toastError } from "@/lib/error-toast";
import type { Agent, Box, List, ProfilesResponse, RecommendOption, RecommendResult, Requirements } from "@/lib/types";
import { cn } from "@/lib/utils";
import { Card } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Field, Input, Select } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { LoadingRows } from "./page";
import { Mono } from "./status";
import { ScoreBreakdown, ScorePill } from "./score";

export const PROFILE_IDS = ["general", "coding-agent", "browser-automation", "build", "data", "long-running-service", "sandbox-untrusted"] as const;
const KIND_COLORS: Record<string, "green" | "blue" | "yellow" | "gray" | "default"> = { warm: "green", pool: "blue", vm: "default", burst: "yellow", sandbox: "gray" };

export function KindBadge({ kind }: { kind: string }) {
  const t = useTranslations("rec");
  const k = ["warm", "pool", "vm", "burst", "sandbox", "clone"].includes(kind) ? kind : "pool";
  return <Badge variant={KIND_COLORS[k] ?? "default"}>{t(`kind.${k as "warm"}`)}</Badge>;
}

/** Tên/mô tả profile theo ngôn ngữ (Core trả tiếng Anh; profile lạ thì dùng chuỗi của Core). */
export function useProfileText() {
  const t = useTranslations("rec");
  return React.useCallback((id: string, fallback?: { name?: string; description?: string }) => {
    const known = (PROFILE_IDS as readonly string[]).includes(id);
    return { name: known ? t(`profiles.${id as "general"}.name`) : fallback?.name ?? id, desc: known ? t(`profiles.${id as "general"}.desc`) : fallback?.description ?? "" };
  }, [t]);
}

type Draft = { cpu: string; ram_mb: string; disk_gb: string; arch: string; region: string; location: string; max_latency_ms: string; max_price_hour: string; lifetime: string; duration_hours: string; isolation: string; optimize: string; public_url: boolean; untrusted: boolean };
const EMPTY: Draft = { cpu: "", ram_mb: "", disk_gb: "", arch: "", region: "", location: "", max_latency_ms: "", max_price_hour: "", lifetime: "", duration_hours: "", isolation: "", optimize: "balanced", public_url: false, untrusted: false };

function toReq(d: Draft): Requirements {
  const n = (s: string) => (s.trim() === "" ? undefined : Number(s));
  const r: Requirements = { cpu: n(d.cpu), ram_mb: n(d.ram_mb), disk_gb: n(d.disk_gb), arch: d.arch || undefined, region: d.region.trim() || undefined, location: d.location.trim() || undefined, max_latency_ms: n(d.max_latency_ms), max_price_hour: n(d.max_price_hour), lifetime: d.lifetime || undefined, duration_hours: n(d.duration_hours), isolation: d.isolation || undefined, optimize: d.optimize || undefined, public_url: d.public_url || undefined, untrusted: d.untrusted || undefined };
  return Object.fromEntries(Object.entries(r).filter(([, v]) => v !== undefined && !(typeof v === "number" && Number.isNaN(v)))) as Requirements;
}

function OptionCard({ o, best, onCreate, canCreate, creating }: { o: RecommendOption; best: boolean; onCreate: () => void; canCreate: boolean; creating: boolean }) {
  const t = useTranslations("rec");
  const { usd, mb, num } = useFmt();
  const [why, setWhy] = React.useState(best);
  return (
    <Card className={cn("min-w-0 p-3.5", best && "border-primary/50")} data-rec-option={o.rank}>
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div className="min-w-0">
          <p className="flex flex-wrap items-center gap-1.5 text-[13px]">
            <span className="font-mono text-muted-foreground">#{o.rank}</span>
            <KindBadge kind={o.kind} />
            {best && <Badge variant="blue"><Sparkles className="size-3" />{t("best")}</Badge>}
            {o.requires_approval && <Badge variant="yellow">{t("needsApproval")}</Badge>}
            {o.price_stale && <Badge variant="outline">{t("priceStale")}</Badge>}
          </p>
          <p className="mt-1 break-all font-mono text-[13px]">{o.provider}/{o.region}{o.sku ? ` · ${o.sku}` : ""}{o.host_id ? ` · ${o.host_id}` : ""}</p>
          <p className="text-xs text-muted-foreground"><Mono>{o.cpu}c · {mb(o.ram_mb)} · {o.disk_gb}G · {o.isolation}{o.arch ? ` · ${o.arch}` : ""}</Mono></p>
        </div>
        <div className="text-right">
          <ScorePill score={o.score} />
          <p className="text-[11px] text-muted-foreground">{t("score")}</p>
        </div>
      </div>
      <div className="mt-2.5 grid grid-cols-3 gap-2 text-[12.5px]">
        <div className="min-w-0"><p className="text-[11px] text-muted-foreground">{t("eta")}</p><p className="font-mono"><Clock className="mr-0.5 inline size-3" />~{num(o.eta_seconds, 1)} s</p></div>
        <div className="min-w-0"><p className="text-[11px] text-muted-foreground">{t("perHour")}</p><p className="font-mono">{usd(o.cost_per_hour)}</p></div>
        <div className="min-w-0"><p className="text-[11px] text-muted-foreground">{t("total")}</p><p className="font-mono">{usd(o.expected_total_usd)}</p></div>
      </div>
      {o.reasons.length > 0 && <ul className="mt-2 list-disc pl-4 text-[12.5px] text-muted-foreground">{o.reasons.map((r) => <li key={r} className="break-words">{r}</li>)}</ul>}
      <div className="mt-2.5 flex flex-wrap items-center justify-between gap-2 border-t border-border pt-2.5">
        <button type="button" className="inline-flex items-center gap-1 text-[12.5px] text-primary hover:underline" onClick={() => setWhy((x) => !x)} aria-expanded={why}>
          <ChevronRight className={cn("size-3.5 transition-transform", why && "rotate-90")} /> {t("whyScore")}
        </button>
        <Button size="xs" variant={best ? "default" : "outline"} disabled={!canCreate || creating} onClick={onCreate} title={canCreate ? undefined : t("pickAgent")}><Rocket /> {t("createWith")}</Button>
      </div>
      {why && <ScoreBreakdown breakdown={o.breakdown} className="mt-2" />}
    </Card>
  );
}

export function RecommendResults({ res, agentId, onCreated }: { res: RecommendResult; agentId?: string; onCreated?: (b: Box) => void }) {
  const t = useTranslations("rec");
  const tb = useTranslations("estimate");
  const { usd, num } = useFmt();
  const { run, pending } = useAction();
  const router = useRouter();
  const create = async (o: RecommendOption) => {
    if (!agentId) return;
    const requirements: Requirements = { ...res.requirements, region: o.region, ...(o.isolation === "vm" ? { isolation: "vm" } : {}) };
    const b = await run(`c${o.rank}`, () => api.createBox({ agent_id: agentId, profile: res.profile.id, template: res.template, lifecycle: res.lifecycle, requirements }), { revalidate: [startsWith("/boxes"), "/overview"] });
    if (b) {
      const e = b.estimate;
      toast.success(t("created", { slug: b.slug }), e ? { description: tb("toast", { eta: num(e.eta_seconds, 1), hour: usd(e.cost_per_hour), total: usd(e.expected_total_usd) }) } : undefined);
      if (onCreated) onCreated(b);
      else router.push(`/boxes/${b.id}`);
    }
  };
  return (
    <div className="flex min-w-0 flex-col gap-3" data-rec-results>
      <div className="rounded-lg border border-primary/30 bg-primary/5 p-3 text-[13px]">
        <p className="flex items-center gap-1.5 font-medium text-primary"><Wand2 className="size-4" /> {res.auto_pick}</p>
        <p className="mt-0.5 text-muted-foreground">{res.explain}</p>
        <p className="mt-1 text-xs text-muted-foreground">{t("effective", { hours: num(res.expected_hours, 1) })} <Mono>{res.template} · {res.lifecycle} · {res.requirements.cpu ?? "?"}c / {res.requirements.ram_mb ?? "?"} MB / {res.requirements.disk_gb ?? "?"} GB</Mono></p>
      </div>
      {res.options.length === 0 ? <p className="text-[13px] text-muted-foreground">{t("noOptions")}</p> : (
        <div className="grid gap-3 lg:grid-cols-2">
          {res.options.map((o) => <OptionCard key={`${o.rank}-${o.kind}-${o.provider}-${o.region}-${o.sku ?? o.host_id}`} o={o} best={o.rank === res.pick?.rank} canCreate={!!agentId} creating={!!pending} onCreate={() => create(o)} />)}
        </div>
      )}
      {!agentId && res.options.length > 0 && <p className="text-xs text-st-warning">{t("pickAgent")}</p>}
      {res.rejected.length > 0 && (
        <details className="rounded-lg border border-border p-3 text-[13px]" data-rec-rejected>
          <summary className="cursor-pointer select-none"><Ban className="mr-1 inline size-3.5 text-muted-foreground" />{t("rejected", { n: res.rejected.length })}</summary>
          <ul className="mt-2 flex flex-col divide-y divide-border/60">
            {res.rejected.map((r, i) => (
              <li key={i} className="flex flex-wrap items-baseline gap-x-2 gap-y-0.5 py-1.5">
                <KindBadge kind={r.kind} />
                <Mono className="break-all">{[r.provider, r.region].filter(Boolean).join("/")}{r.sku ? ` · ${r.sku}` : ""}{r.host_id ? ` · ${r.host_id}` : ""}</Mono>
                <span className="w-full text-xs text-muted-foreground sm:w-auto">{r.reason}</span>
              </li>
            ))}
          </ul>
        </details>
      )}
      {!!res.next?.length && <p className="text-xs text-muted-foreground">{t("agentsCall")} <Mono className="break-all">{res.next[0]}</Mono></p>}
    </div>
  );
}

export function RecommendPanel({ agentId: fixedAgent, compact, onCreated }: { agentId?: string; compact?: boolean; onCreated?: (b: Box) => void }) {
  const t = useTranslations("rec");
  const tc = useTranslations("common");
  const ptext = useProfileText();
  const { mb } = useFmt();
  const { data: profiles } = useApi<ProfilesResponse>(paths.profiles, { refreshInterval: 0 });
  const { data: agents } = useApi<List<Agent>>(fixedAgent === undefined ? paths.agents : null);
  const [agent, setAgent] = React.useState("");
  const [profile, setProfile] = React.useState("general");
  const [kind, setKind] = React.useState("");
  const [d, setD] = React.useState<Draft>(EMPTY);
  const [res, setRes] = React.useState<RecommendResult | null>(null);
  const [loading, setLoading] = React.useState(false);
  const agentId = fixedAgent ?? (agent || undefined);
  const prof = profiles?.items.find((p) => p.id === profile);
  const def = prof?.defaults ?? {};
  const set = <K extends keyof Draft>(k: K, v: Draft[K]) => setD((x) => ({ ...x, [k]: v }));

  const pickKind = (k: string) => {
    setKind(k);
    const kp = profiles?.kinds.find((x) => x.kind === k);
    if (kp) {
      setProfile(kp.profile);
      setD((x) => ({ ...x, cpu: String(kp.spec.cpu ?? ""), ram_mb: String(kp.spec.ram_mb ?? ""), disk_gb: String(kp.spec.disk_gb ?? "") }));
    }
  };
  const submit = async (e?: React.FormEvent) => {
    e?.preventDefault();
    setLoading(true);
    try {
      setRes(await api.recommend({ requirements: toReq(d), profile, agent_id: agentId, kind: kind || undefined, limit: compact ? 4 : 6 }));
    } catch (err) {
      toastError(err);
    } finally {
      setLoading(false);
    }
  };

  if (!profiles) return <LoadingRows rows={3} />;
  return (
    <div className="flex min-w-0 flex-col gap-4" data-recommend>
      <form onSubmit={submit} className="flex min-w-0 flex-col gap-4">
        <div>
          <p className="mb-1.5 text-[13px] font-medium">{t("profile")}</p>
          <div className={cn("grid gap-2", compact ? "grid-cols-1 sm:grid-cols-2" : "sm:grid-cols-2 xl:grid-cols-4")} role="radiogroup" aria-label={t("profile")}>
            {profiles.items.map((p) => {
              const tx = ptext(p.id, p);
              const on = p.id === profile;
              return (
                <button type="button" role="radio" aria-checked={on} key={p.id} onClick={() => { setProfile(p.id); setKind(""); }}
                  className={cn("min-w-0 rounded-lg border p-2.5 text-left transition-colors", on ? "border-primary bg-primary/5" : "border-border hover:bg-surface-2")}>
                  <span className="flex items-center justify-between gap-2">
                    <span className="truncate text-[13px] font-medium">{tx.name}</span>
                    <Mono className="shrink-0 text-[11px] text-muted-foreground">{p.defaults.cpu}c·{mb(p.defaults.ram_mb ?? 0)}</Mono>
                  </span>
                  {!compact && <span className="mt-0.5 line-clamp-2 block text-xs text-muted-foreground">{tx.desc}</span>}
                </button>
              );
            })}
          </div>
          {prof?.tips?.[0] && !compact && <p className="mt-1.5 text-xs text-muted-foreground">{t("tip")} {prof.tips[0]}</p>}
        </div>
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
          {fixedAgent === undefined && (
            <Field label={tc("agent")}>
              <Select value={agent} onChange={(e) => setAgent(e.target.value)}>
                <option value="">{tc("chooseAgent")}</option>
                {agents?.items.map((a) => <option key={a.id} value={a.id}>{a.name} · {a.kind}</option>)}
              </Select>
            </Field>
          )}
          <Field label={t("agentKind")} hint={kind ? t("kindHint", { profile: ptext(profiles.kinds.find((k) => k.kind === kind)?.profile ?? "general").name }) : undefined}>
            <Select value={kind} onChange={(e) => pickKind(e.target.value)}>
              <option value="">{t("kindNone")}</option>
              {profiles.kinds.map((k) => <option key={k.kind} value={k.kind}>{k.kind} → {k.profile}</option>)}
            </Select>
          </Field>
          <Field label={t("optimize")}>
            <Select value={d.optimize} onChange={(e) => set("optimize", e.target.value)}>
              {profiles.optimize.map((o) => <option key={o} value={o}>{t(`opt.${o as "balanced"}`)}</option>)}
            </Select>
          </Field>
          <Field label={t("isolation")}>
            <Select value={d.isolation} onChange={(e) => set("isolation", e.target.value)}>
              <option value="">{t("fromProfile", { v: def.isolation ?? "any" })}</option>
              {profiles.isolation.map((o) => <option key={o} value={o}>{o}</option>)}
            </Select>
          </Field>
          <Field label="vCPU"><Input type="number" min={1} max={64} step={1} inputMode="numeric" placeholder={String(def.cpu ?? "")} value={d.cpu} onChange={(e) => set("cpu", e.target.value)} /></Field>
          <Field label={t("ramMb")}><Input type="number" min={256} step={256} inputMode="numeric" placeholder={String(def.ram_mb ?? "")} value={d.ram_mb} onChange={(e) => set("ram_mb", e.target.value)} /></Field>
          <Field label={t("diskGb")}><Input type="number" min={5} inputMode="numeric" placeholder={String(def.disk_gb ?? "")} value={d.disk_gb} onChange={(e) => set("disk_gb", e.target.value)} /></Field>
          <Field label={t("lifetime")}>
            <Select value={d.lifetime} onChange={(e) => set("lifetime", e.target.value)}>
              <option value="">{t("fromProfile", { v: def.lifetime ?? "—" })}</option>
              {profiles.lifetime.map((o) => <option key={o} value={o}>{t(`life.${o as "ephemeral"}`)}</option>)}
            </Select>
          </Field>
        </div>
        <details className="rounded-lg border border-border p-3" open={!compact}>
          <summary className="cursor-pointer select-none text-[13px] font-medium">{t("more")}</summary>
          <div className="mt-3 grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
            <Field label={t("durationH")}><Input type="number" min={0} step="0.25" inputMode="decimal" placeholder={String(def.duration_hours ?? "")} value={d.duration_hours} onChange={(e) => set("duration_hours", e.target.value)} /></Field>
            <Field label={t("maxPrice")}><Input type="number" min={0} step="0.001" inputMode="decimal" placeholder="0.05" value={d.max_price_hour} onChange={(e) => set("max_price_hour", e.target.value)} /></Field>
            <Field label={t("maxLatency")}><Input type="number" min={0} inputMode="numeric" placeholder="150" value={d.max_latency_ms} onChange={(e) => set("max_latency_ms", e.target.value)} /></Field>
            <Field label={t("arch")}>
              <Select value={d.arch} onChange={(e) => set("arch", e.target.value)}><option value="">any</option><option value="amd64">amd64</option><option value="arm64">arm64</option></Select>
            </Field>
            <Field label={t("region")} hint={t("regionHint")}><Input value={d.region} onChange={(e) => set("region", e.target.value)} placeholder="fsn1" className="font-mono" /></Field>
            <Field label={t("location")} hint={t("locationHint")}><Input value={d.location} onChange={(e) => set("location", e.target.value)} placeholder="VN" className="font-mono" /></Field>
            <label className="flex items-center justify-between gap-3 self-end rounded-md border border-border px-3 py-2 text-[13px]">{t("publicUrl")}<Switch checked={d.public_url} onCheckedChange={(v) => set("public_url", v)} aria-label={t("publicUrl")} /></label>
            <label className="flex items-center justify-between gap-3 self-end rounded-md border border-border px-3 py-2 text-[13px]">{t("untrusted")}<Switch checked={d.untrusted} onCheckedChange={(v) => set("untrusted", v)} aria-label={t("untrusted")} /></label>
          </div>
        </details>
        <div className="flex flex-wrap gap-2">
          <Button type="submit" disabled={loading}><Wand2 /> {loading ? t("ranking") : t("submit")}</Button>
          <Button type="button" variant="ghost" onClick={() => { setD(EMPTY); setKind(""); setRes(null); }}>{t("reset")}</Button>
        </div>
      </form>
      {res && <RecommendResults res={res} agentId={agentId} onCreated={onCreated} />}
    </div>
  );
}
