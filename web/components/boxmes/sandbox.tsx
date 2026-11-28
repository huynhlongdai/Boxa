"use client";
// v1.4 Sandbox API providers (T2 dial-in / T3 shim): logo chữ tắt, huy hiệu tier / mức xác minh / capability,
// chọn sandbox_mode, danh sách provider sandbox và màn hình lint adapter YAML tuỳ chỉnh.
import * as React from "react";
import Link from "next/link";
import { useTranslations } from "next-intl";
import { BookOpen, CheckCircle2, ClipboardPaste, Cpu, ExternalLink, FileCode2, GitFork, Globe, Infinity as InfinityIcon, Pause, PlugZap, ShieldCheck, Timer, XCircle, Zap } from "lucide-react";
import { api, paths } from "@/lib/api";
import { useApi } from "@/lib/hooks";
import { useFmt } from "@/lib/format";
import { toastError } from "@/lib/error-toast";
import type { AdapterLintResult, Box, Host, List, Provider, SandboxCapabilities, SandboxInfo, SandboxProvider } from "@/lib/types";
import { cn } from "@/lib/utils";
import { Card } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { ErrorBox, LoadingRows, SectionTitle } from "./page";
import { Mono } from "./status";

// ---------------- Logo ----------------
const T1_LOGO: Record<string, { initials: string; color: string }> = {
  hetzner: { initials: "HZ", color: "#d50c2d" },
  vultr: { initials: "VU", color: "#007bfc" },
  digitalocean: { initials: "DO", color: "#0069ff" },
  contabo: { initials: "CB", color: "#1d4ed8" },
  netcup: { initials: "NC", color: "#056473" },
  runpod: { initials: "RP", color: "#673ab7" },
  local: { initials: "LO", color: "#475569" },
};

export function ProviderLogo({ id, info, className }: { id: string; info?: Pick<SandboxInfo, "initials" | "color"> | null; className?: string }) {
  // Provider sandbox: lấy chữ tắt + màu từ adapter (SWR gộp chung một request /sandbox/providers)
  const { data } = useApi<List<SandboxProvider>>(!info && !T1_LOGO[id] ? paths.sandboxProviders : null, { refreshInterval: 0 });
  const l = info ?? T1_LOGO[id] ?? data?.items.find((p) => p.id === id) ?? { initials: id.slice(0, 2).toUpperCase(), color: "#64748b" };
  return (
    <span aria-hidden className={cn("grid size-8 shrink-0 place-items-center rounded-md font-mono text-[11px] font-semibold text-white ring-1 ring-inset ring-white/15", className)} style={{ backgroundColor: l.color }}>
      {l.initials.slice(0, 3)}
    </span>
  );
}

// ---------------- Badges ----------------
export function TierBadge({ tier, className }: { tier?: string; className?: string }) {
  const t = useTranslations("sbx");
  const k = (["T1", "T2", "T3"].includes(tier ?? "") ? tier : "T1") as "T1" | "T2" | "T3";
  return <Badge variant={k === "T1" ? "default" : k === "T2" ? "blue" : "gray"} title={t(`tierHelp.${k}`)} className={className}><span className="font-mono">{k}</span> {t(`tier.${k}`)}</Badge>;
}

const VER: Record<string, { k: "live" | "docs" | "sdk"; v: "green" | "blue" | "yellow" }> = {
  "live-verified": { k: "live", v: "green" },
  "docs-verified": { k: "docs", v: "blue" },
  "sdk-source": { k: "sdk", v: "yellow" },
};
export function VerificationBadge({ v, note }: { v?: string; note?: string }) {
  const t = useTranslations("sbx");
  const m = VER[v ?? ""];
  if (!m) return null;
  return <Badge variant={m.v} title={note ? `${t(`verHelp.${m.k}`)} — ${note}` : t(`verHelp.${m.k}`)}><ShieldCheck className="size-3" />{t(`ver.${m.k}`)}</Badge>;
}
export function EstimatedPriceBadge({ confidence }: { confidence?: string }) {
  const t = useTranslations("sbx");
  if (!confidence || confidence === "docs" || confidence === "verified") return null;
  return <Badge variant="yellow" title={t("estPriceHelp")}>{t("estPrice")}</Badge>;
}

function Cap({ on, icon, label, title }: { on: boolean; icon: React.ReactNode; label: string; title?: string }) {
  const t = useTranslations("sbx");
  return (
    <Badge variant={on ? "outline" : "outline"} className={cn(on ? "text-foreground" : "opacity-55 line-through decoration-muted-foreground/70")} title={on ? title ?? label : t("cap.unsupported", { name: label })}>
      {icon}{label}
    </Badge>
  );
}

/** Huy hiệu capability: boot, fork, pause, thời gian sống tối đa, GPU, cổng công khai (+ isolation). */
export function CapabilityBadges({ caps, className, isolation = true }: { caps: Pick<SandboxCapabilities, "boot_ms" | "fork" | "pause" | "max_lifetime_s" | "gpu" | "ports"> & { isolation?: string }; className?: string; isolation?: boolean }) {
  const t = useTranslations("sbx");
  const { ms, seconds } = useFmt();
  return (
    <div className={cn("flex flex-wrap gap-1", className)} data-caps>
      {caps.boot_ms > 0 && <Badge variant="outline" className="text-foreground" title={t("cap.bootHelp")}><Zap className="size-3" />{t("cap.boot", { v: ms(caps.boot_ms) })}</Badge>}
      <Cap on={caps.fork} icon={<GitFork className="size-3" />} label={t("cap.fork")} />
      <Cap on={caps.pause} icon={<Pause className="size-3" />} label={t("cap.pause")} />
      <Badge variant="outline" className="text-foreground" title={t("cap.lifeHelp")}>
        {caps.max_lifetime_s > 0 ? <><Timer className="size-3" />{t("cap.life", { v: seconds(caps.max_lifetime_s) })}</> : <><InfinityIcon className="size-3" />{t("cap.lifeInf")}</>}
      </Badge>
      <Cap on={caps.gpu} icon={<Cpu className="size-3" />} label={t("cap.gpu")} />
      <Cap on={caps.ports} icon={<Globe className="size-3" />} label={t("cap.ports")} />
      {isolation && caps.isolation && <Badge variant={["microvm", "vm", "gvisor"].includes(caps.isolation) ? "green" : "yellow"} title={t(["microvm", "vm", "gvisor"].includes(caps.isolation) ? "cap.strong" : "cap.shared")}><span className="font-mono">{caps.isolation}</span></Badge>}
    </div>
  );
}

/** Giá theo giây: $/giờ ÷ 3600. */
export function usePerSecond() {
  const { usd } = useFmt();
  return React.useCallback((perHour: number) => `${usd(perHour / 3600, 6)}/s`, [usd]);
}

// ---------------- sandbox_mode ----------------
export function SandboxModeField({ value, onChange, info }: { value: string; onChange: (v: string) => void; info: Pick<SandboxInfo, "tier" | "capabilities" | "dial_only"> }) {
  const t = useTranslations("sbx");
  const canDial = info.tier === "T2" && info.capabilities.background;
  const dialOnly = !!info.dial_only; // no exec API (Render): a T3 shim cannot work
  const v = ["auto", "dial", "shim"].includes(value) ? value : "auto";
  return (
    <div className="flex flex-col gap-1.5" data-sandbox-mode>
      <p className="text-[13px] font-medium">{t("mode.label")}</p>
      <div className="grid gap-1.5 sm:grid-cols-3" role="radiogroup" aria-label={t("mode.label")}>
        {(["auto", "dial", "shim"] as const).map((m) => {
          const disabled = (m === "dial" && !canDial) || (m === "shim" && dialOnly);
          return (
            <button key={m} type="button" role="radio" aria-checked={v === m} disabled={disabled} onClick={() => onChange(m)}
              className={cn("min-w-0 rounded-md border p-2 text-left text-[12.5px] transition-colors disabled:cursor-not-allowed disabled:opacity-50", v === m ? "border-primary bg-primary/5" : "border-border hover:bg-surface-2")}>
              <span className="block font-medium">{t(`mode.${m}`)}</span>
              <span className="mt-0.5 block text-[11.5px] text-muted-foreground">{t(`modeHelp.${m}`)}</span>
            </button>
          );
        })}
      </div>
      {!canDial && <p className="text-xs text-muted-foreground">{t("mode.noDial")}</p>}
      {dialOnly && <p className="text-xs text-muted-foreground" data-dial-only>{t("mode.dialOnly")}</p>}
    </div>
  );
}

export function ModeBadge({ mode }: { mode: string }) {
  const t = useTranslations("sbx");
  const m = mode === "dial" ? "dial" : "shim";
  return <Badge variant={m === "dial" ? "blue" : "gray"} title={t(`modeHelp.${m}`)}>{t(`mode.${m}`)}</Badge>;
}

// ---------------- Provider sandbox list ----------------
function SandboxCard({ p, onConnect }: { p: SandboxProvider; onConnect: (id: string) => void }) {
  const t = useTranslations("sbx");
  const { usd } = useFmt();
  const perSec = usePerSecond();
  const [more, setMore] = React.useState(false);
  return (
    <Card className="flex min-w-0 flex-col p-4" data-sandbox-provider={p.id}>
      <div className="flex items-start gap-3">
        <ProviderLogo id={p.id} info={p} />
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center justify-between gap-x-2 gap-y-1">
            <p className="truncate font-medium">{p.name}</p>
            {p.connected ? <Badge variant="green">{t("connected")}</Badge> : <Badge variant="outline">{t("notConnected")}</Badge>}
          </div>
          <div className="mt-1 flex flex-wrap gap-1">
            <TierBadge tier={p.mode === "shim" ? "T3" : p.tier} />
            <ModeBadge mode={p.mode} />
            <VerificationBadge v={p.verification} note={p.verified_note} />
            <EstimatedPriceBadge confidence={p.pricing.confidence} />
            <Badge variant="outline" title={t("adapterHelp")}>{p.adapter === "yaml" ? "YAML" : t("native")}</Badge>
            {p.dial_only && <Badge variant="blue" title={t("dialOnlyHelp")} data-dial-only-badge>{t("dialOnly")}</Badge>}
          </div>
        </div>
      </div>
      <CapabilityBadges caps={p.capabilities} className="mt-3" />
      <div className="mt-3 grid grid-cols-2 gap-2 text-[12.5px]">
        <div className="min-w-0"><p className="text-[11px] text-muted-foreground">{t("from")}</p><p className="font-mono">{p.from_usd_h === 0 && p.sizes.some((z) => z.fixed_price) ? t("freePlan") : `${usd(p.from_usd_h)}/h`}</p></div>
        <div className="min-w-0"><p className="text-[11px] text-muted-foreground">{t("perSecond")}</p><p className="truncate font-mono" title={perSec(p.from_usd_h)}>{perSec(p.from_usd_h)}</p></div>
      </div>
      {p.blocked && (p.connected || p.dial_only) && <p className="mt-2 break-words text-[12px] text-st-warning">{p.blocked}</p>}
      {(p.notes.length > 0 || p.verified_note) && (
        <div className="mt-2">
          <button type="button" className="text-[12px] text-primary hover:underline" aria-expanded={more} onClick={() => setMore((x) => !x)}>{more ? t("less") : t("more")}</button>
          {more && (
            <div className="mt-1 flex flex-col gap-1 text-[12px] text-muted-foreground">
              {p.notes.map((n) => <p key={n} className="break-words">{n}</p>)}
              {p.verified_note && <p className="break-words"><span className="font-medium text-foreground">{t("verifiedFrom")}</span> {p.verified_note}</p>}
              {p.env_vars.length > 0 && <p className="break-words">{t("envVars")} <Mono>{p.env_vars.join(", ")}</Mono></p>}
            </div>
          )}
        </div>
      )}
      <div className="mt-auto pt-3"><div className="flex flex-wrap items-center gap-2 border-t border-border pt-3">
        <Button size="xs" variant={p.connected ? "outline" : "default"} onClick={() => onConnect(p.id)} data-connect><PlugZap /> {p.connected ? t("settings") : t("connect")}</Button>
        <a href={p.docs} target="_blank" rel="noreferrer" className="ml-auto inline-flex items-center gap-1 text-[12.5px] text-primary hover:underline"><BookOpen className="size-3.5" />{t("docs")}<ExternalLink className="size-3" /></a>
      </div></div>
    </Card>
  );
}

const SKIPPED = ["modal", "cloudflare", "koyeb", "zipbox"] as const;

export function SandboxProvidersView({ onConnect }: { onConnect: (id: string) => void }) {
  const t = useTranslations("sbx");
  const { data, error, mutate } = useApi<List<SandboxProvider>>(paths.sandboxProviders, { refreshInterval: 30000 });
  if (error) return <ErrorBox error={error} retry={() => mutate()} />;
  if (!data) return <LoadingRows rows={3} />;
  const conn = data.items.filter((p) => p.connected).length;
  return (
    <>
      <SectionTitle right={<Link href="/infra/adapters" className="inline-flex items-center gap-1 text-[13px] text-primary hover:underline" data-open-adapters><FileCode2 className="size-4" />{t("customAdapter")}</Link>}>
        {t("title")} <span className="ml-1 text-[13px] font-normal text-muted-foreground">{t("count", { n: conn, total: data.items.length })}</span>
      </SectionTitle>
      <p className="mb-3 max-w-3xl text-[13px] text-muted-foreground">{t("desc")}</p>
      <div className="mb-6 grid gap-3 md:grid-cols-2 xl:grid-cols-3">{data.items.map((p) => <SandboxCard key={p.id} p={p} onConnect={onConnect} />)}</div>
      <SectionTitle>{t("skipped.title")}</SectionTitle>
      <Card className="mb-6 divide-y divide-border/60">
        {SKIPPED.map((k) => (
          <div key={k} className="flex flex-col gap-0.5 px-4 py-2.5 text-[13px] sm:flex-row sm:items-baseline sm:gap-3">
            <span className="w-32 shrink-0 font-medium">{t(`skipped.${k}.name`)}</span>
            <span className="min-w-0 text-muted-foreground">{t(`skipped.${k}.why`)}</span>
          </div>
        ))}
      </Card>
    </>
  );
}

// ---------------- Box: tier / provider ----------------
export function BoxPlacementBadges({ box }: { box: Box }) {
  const t = useTranslations("sbx");
  const { data: hosts } = useApi<List<Host>>(box.host_id ? paths.hosts : null, { refreshInterval: 0 });
  const { data: sbx } = useApi<List<SandboxProvider>>(paths.sandboxProviders, { refreshInterval: 0 });
  const { data: provs } = useApi<List<Provider>>(paths.providers, { refreshInterval: 0 });
  const host = hosts?.items.find((h) => h.id === box.host_id);
  const info = sbx?.items.find((p) => p.id === box.provider);
  const tier = host?.billing === "sandbox" ? (host.driver === "shim" ? "T3" : "T2") : info ? (info.mode === "shim" ? "T3" : info.tier) : "T1";
  const name = info?.name ?? provs?.items.find((p) => p.id === box.provider)?.name ?? box.provider;
  return (
    <span className="inline-flex flex-wrap items-center gap-1.5" data-box-tier={tier}>
      <ProviderLogo id={box.provider} info={info} className="size-5 rounded text-[8.5px]" />
      <span className="text-[13px]">{name}</span>
      <TierBadge tier={tier} />
      {tier !== "T1" && <Badge variant="outline" title={t("perSecondHelp")}>{t("billedPerSecond")}</Badge>}
    </span>
  );
}

// ---------------- Custom YAML adapter ----------------
export function AdapterLinter({ sample }: { sample: string }) {
  const t = useTranslations("sbx");
  const { usd } = useFmt();
  const [yaml, setYaml] = React.useState("");
  const [res, setRes] = React.useState<AdapterLintResult | null>(null);
  const [busy, setBusy] = React.useState(false);
  const lint = async () => {
    setBusy(true);
    try {
      setRes(await api.lintAdapter(yaml));
    } catch (e) {
      toastError(e);
    } finally {
      setBusy(false);
    }
  };
  const info = res?.ok ? res.info : null;
  return (
    <div className="grid min-w-0 gap-4 lg:grid-cols-[minmax(0,1.3fr)_minmax(0,1fr)]" data-adapter-linter>
      <div className="flex min-w-0 flex-col gap-2">
        <label htmlFor="adapter-yaml" className="text-[13px] font-medium">{t("adapter.yaml")}</label>
        <textarea id="adapter-yaml" wrap="off" value={yaml} onChange={(e) => { setYaml(e.target.value); setRes(null); }} spellCheck={false} placeholder={t("adapter.placeholder")}
          className="min-h-[22rem] w-full resize-y rounded-md border border-border bg-surface-2/40 p-3 font-mono text-[12.5px] leading-5 outline-none focus-visible:ring-2 focus-visible:ring-primary/50" />
        <div className="flex flex-wrap gap-2">
          <Button disabled={!yaml.trim() || busy} onClick={lint} data-lint><CheckCircle2 /> {busy ? t("adapter.linting") : t("adapter.lint")}</Button>
          <Button variant="outline" onClick={() => { setYaml(sample); setRes(null); }} data-sample><ClipboardPaste /> {t("adapter.sample")}</Button>
        </div>
      </div>
      <div className="flex min-w-0 flex-col gap-3">
        {!res ? (
          <Card className="p-4 text-[13px] text-muted-foreground">
            <p className="font-medium text-foreground">{t("adapter.howTitle")}</p>
            <ol className="mt-2 list-decimal space-y-1 pl-4">
              <li>{t("adapter.how1")}</li>
              <li>{t.rich("adapter.how2", { mono: (c) => <Mono className="break-all">{c}</Mono> })}</li>
              <li>{t("adapter.how3")}</li>
            </ol>
            <p className="mt-2 text-xs">{t("adapter.required")} <Mono className="break-all">id, name, base_url, validate, create, get, delete, exec</Mono></p>
          </Card>
        ) : !res.ok ? (
          <Card className="border-st-error/40 p-4 text-[13px]" role="alert" data-lint-result="error">
            <p className="flex items-center gap-1.5 font-medium text-st-error"><XCircle className="size-4" /> {t("adapter.invalid")}</p>
            <p className="mt-1 break-words font-mono text-[12.5px] text-muted-foreground">{res.error}</p>
          </Card>
        ) : info && (
          <Card className="p-4 text-[13px]" data-lint-result="ok">
            <p className="flex items-center gap-1.5 font-medium text-st-running"><CheckCircle2 className="size-4" /> {t("adapter.valid")}</p>
            <div className="mt-3 flex items-start gap-3">
              <ProviderLogo id={info.id} info={info} />
              <div className="min-w-0">
                <p className="font-medium">{info.name} <Mono className="text-muted-foreground">({info.id})</Mono></p>
                <div className="mt-1 flex flex-wrap gap-1"><TierBadge tier={info.tier} /><VerificationBadge v={info.verification} /><EstimatedPriceBadge confidence={info.pricing.confidence} /></div>
              </div>
            </div>
            <CapabilityBadges caps={info.capabilities} className="mt-3" />
            <dl className="mt-3 grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1 text-[12.5px]">
              <dt className="text-muted-foreground">{t("adapter.sizes")}</dt><dd className="break-words font-mono">{info.sizes.map((z) => `${z.sku} (${z.cpu}c/${z.ram_mb}M)`).join(", ") || "—"}</dd>
              <dt className="text-muted-foreground">{t("adapter.regions")}</dt><dd className="break-words font-mono">{info.regions.map((r) => r.id).join(", ") || "—"}</dd>
              <dt className="text-muted-foreground">{t("from")}</dt><dd className="font-mono">{info.from_usd_h ? `${usd(info.from_usd_h)}/h` : "—"}</dd>
              <dt className="text-muted-foreground">{t("adapter.creds")}</dt><dd className="break-words font-mono">{info.credentials.map((c) => c.name + (c.optional ? "?" : "")).join(", ")}</dd>
            </dl>
          </Card>
        )}
      </div>
    </div>
  );
}
