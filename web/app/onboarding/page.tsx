"use client";
// Khởi động lần đầu: token Hetzner → host pool đầu tiên (có tiến trình) → agent đầu tiên → copy cấu hình → chờ lệnh đầu tiên.
import * as React from "react";
import Link from "next/link";
import { ArrowRight, Box as BoxIcon, Check, CheckCircle2, ExternalLink, KeyRound, Loader2, PartyPopper, Server } from "lucide-react";
import { toast } from "sonner";
import { useTranslations } from "next-intl";
import { api, errMsg, paths } from "@/lib/api";
import { useApi } from "@/lib/hooks";
import type { Agent, Host, List, Offer, Provider } from "@/lib/types";
import { cn } from "@/lib/utils";
import { useFmt } from "@/lib/format";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Field, Input } from "@/components/ui/input";
import { AgentWizard, type WizardResult } from "@/components/boxmes/agent-wizard";
import { ConfidenceBadge } from "@/components/boxmes/status";
import { LocaleSelect } from "@/components/locale-switcher";
import { toastError } from "@/lib/error-toast";

const STEPS = ["token", "host", "agent", "call"] as const;
const mono = (c: React.ReactNode) => <span className="font-mono">{c}</span>;

function Stepper({ step }: { step: number }) {
  const t = useTranslations("onboarding.steps");
  return (
    <ol className="mb-6 grid grid-cols-4 gap-2">
      {STEPS.map((s, i) => (
        <li key={s} className="flex flex-col gap-1.5">
          <span className={cn("h-1 rounded-full", i < step ? "bg-st-running" : i === step ? "bg-primary" : "bg-surface-2")} />
          <span className={cn("text-[11.5px] leading-tight sm:text-xs", i === step ? "text-foreground" : "text-muted-foreground")}>{t(s)}</span>
        </li>
      ))}
    </ol>
  );
}

function TokenStep({ onNext }: { onNext: () => void }) {
  const { data: providers } = useApi<List<Provider>>(paths.providers);
  const hz = providers?.items.find((p) => p.id === "hetzner");
  const [token, setToken] = React.useState("");
  const [busy, setBusy] = React.useState(false);
  const [err, setErr] = React.useState<string | null>(null);
  const t = useTranslations("onboarding");
  const tc = useTranslations("common");
  return (
    <div className="flex flex-col gap-4">
      <div>
        <h2 className="text-lg font-semibold">{t("tokenTitle")}</h2>
        <p className="mt-1 text-sm text-muted-foreground">{t.rich("tokenDesc", { b: (c) => <b>{c}</b> })}</p>
      </div>
      <a href="https://console.hetzner.cloud/projects" target="_blank" rel="noreferrer" className="inline-flex w-fit items-center gap-1 text-[13px] text-primary hover:underline">{t("openConsole")} <ExternalLink className="size-3.5" /></a>
      <Field label={t("tokenLabel")} hint={t("tokenHint")}>
        <div className="relative">
          <KeyRound className="absolute left-2.5 top-2.5 size-4 text-muted-foreground" />
          <Input type="password" autoComplete="off" value={token} onChange={(e) => setToken(e.target.value)} placeholder={t("tokenPh")} className="pl-8 font-mono" />
        </div>
      </Field>
      {err && <p className="rounded-md border border-st-error/30 bg-st-error/10 px-3 py-2 text-[13px] text-st-error">{err}</p>}
      <div className="flex flex-wrap items-center justify-between gap-2">
        {hz?.connected ? <Button variant="ghost" onClick={onNext}><Check className="text-st-running" /> {t("alreadyConnected")}</Button> : <span />}
        <Button disabled={!token || busy} onClick={async () => {
          setBusy(true); setErr(null);
          try {
            await api.setProviderToken("hetzner", token);
            toast.success(t("tokenOk"));
            onNext();
          } catch (e) { setErr(errMsg(e)); } finally { setBusy(false); }
        }}>{busy ? <><Loader2 className="animate-spin" /> {tc("checking")}</> : <>{t("checkContinue")} <ArrowRight /></>}</Button>
      </div>
    </div>
  );
}

const HOST_PHASES = ["api", "cloudInit", "gateway", "selfTest"] as const;

function HostStep({ onNext }: { onNext: () => void }) {
  const { data: offers } = useApi<List<Offer>>(paths.offers);
  const choices = (offers?.items ?? []).filter((o) => o.provider === "hetzner").sort((a, b) => a.price_month - b.price_month);
  const [sel, setSel] = React.useState<string | null>(null);
  const [hostId, setHostId] = React.useState<string | null>(null);
  const [startAt, setStartAt] = React.useState(0);
  const [now, setNow] = React.useState(0);
  const t = useTranslations("onboarding");
  const tc = useTranslations("common");
  const { usd, mb } = useFmt();
  const { data: hosts } = useApi<List<Host>>(hostId ? paths.hosts : null, { refreshInterval: 2000 });
  const host = hosts?.items.find((h) => h.id === hostId);
  const ready = host?.status === "ready";
  const chosen = choices.find((o) => `${o.sku}|${o.region}` === sel) ?? choices.find((o) => o.sku === "cx22") ?? choices[0];

  React.useEffect(() => {
    if (!hostId || ready) return;
    const t = setInterval(() => setNow(Date.now()), 500);
    return () => clearInterval(t);
  }, [hostId, ready]);

  const phase = ready ? HOST_PHASES.length : Math.min(HOST_PHASES.length - 1, Math.floor((now - startAt) / 2500));

  if (hostId) {
    return (
      <div className="flex flex-col gap-4">
        <div>
          <h2 className="text-lg font-semibold">{ready ? t("hostReady") : t("hostBuilding")}</h2>
          <p className="mt-1 break-all font-mono text-[13px] text-muted-foreground">{host?.id ?? hostId} · {chosen?.sku} · {chosen?.region}{host?.ip ? ` · ${host.ip}` : ""}</p>
        </div>
        <ol className="flex flex-col gap-2">
          {HOST_PHASES.map((p, i) => (
            <li key={p} className="flex items-center gap-2.5 text-sm">
              {i < phase ? <CheckCircle2 className="size-4 text-st-running" /> : i === phase && !ready ? <Loader2 className="size-4 animate-spin text-st-provisioning" /> : <span className="size-4 rounded-full border border-border" />}
              <span className={cn(i <= phase ? "text-foreground" : "text-muted-foreground")}>{t(`phases.${p}`)}</span>
            </li>
          ))}
        </ol>
        <div className="h-1.5 overflow-hidden rounded-full bg-surface-2">
          <div className="h-full rounded-full bg-st-provisioning transition-all duration-500" style={{ width: `${ready ? 100 : Math.min(95, ((phase + 0.5) / HOST_PHASES.length) * 100)}%` }} />
        </div>
        <div className="flex justify-end"><Button disabled={!ready} onClick={onNext}>{tc("continue")} <ArrowRight /></Button></div>
      </div>
    );
  }

  return (
    <div className="flex flex-col gap-4">
      <div>
        <h2 className="text-lg font-semibold">{t("hostTitle")}</h2>
        <p className="mt-1 text-sm text-muted-foreground">{t("hostDesc")}</p>
      </div>
      <div className="grid gap-2 sm:grid-cols-2">
        {choices.map((o) => {
          const id = `${o.sku}|${o.region}`;
          const on = chosen && `${chosen.sku}|${chosen.region}` === id;
          return (
            <button key={id} onClick={() => setSel(id)} className={cn("rounded-lg border p-3 text-left transition-colors", on ? "border-primary/60 bg-primary/10" : "border-border hover:bg-surface-2/50")}>
              <div className="flex items-center justify-between gap-2">
                <span className="font-mono text-sm">{o.sku} · {o.region}</span>
                <ConfidenceBadge c={o.confidence} />
              </div>
              <p className="mt-1 font-mono text-xs text-muted-foreground">{t("hostSpec", { cpu: o.cpu, ram: mb(o.ram_mb), disk: o.disk_gb })}</p>
              <p className="mt-1 font-mono text-sm">{tc("perMonth", { price: usd(o.price_month, 2) })}</p>
            </button>
          );
        })}
      </div>
      <div className="flex justify-between gap-2">
        <Button variant="ghost" onClick={onNext}>{t("skipHost")}</Button>
        <Button disabled={!chosen} onClick={async () => {
          try {
            const h = await api.createHost({ provider: "hetzner", sku: chosen!.sku, region: chosen!.region });
            setStartAt(Date.now()); setNow(Date.now()); setHostId(h.id);
          } catch (e) { toastError(e); }
        }}><Server /> {t("createHost")} {chosen ? `(${tc("perMonth", { price: usd(chosen.price_month, 2) })})` : ""}</Button>
      </div>
    </div>
  );
}

function WaitStep({ agent }: { agent: Agent }) {
  const { data } = useApi<Agent>(paths.agent(agent.id), { refreshInterval: (a) => (a?.last_call_at ? 0 : 2000) });
  const done = Boolean(data?.last_call_at);
  const t = useTranslations("onboarding");
  return done ? (
    <div className="flex flex-col items-center gap-3 py-6 text-center">
      <span className="grid size-14 place-items-center rounded-full bg-st-running/15 text-st-running"><PartyPopper className="size-7" /></span>
      <h2 className="text-xl font-semibold">{t.rich("success", { name: agent.name, mono })}</h2>
      <p className="max-w-md text-sm text-muted-foreground">{t("successDesc")}</p>
      <div className="mt-2 flex flex-wrap justify-center gap-2">
        <Button asChild><Link href="/">{t("toOverview")}</Link></Button>
        <Button asChild variant="secondary"><Link href={`/agents/${agent.id}`}>{t("viewAgent")}</Link></Button>
      </div>
    </div>
  ) : (
    <div className="flex flex-col items-center gap-3 py-8 text-center">
      <Loader2 className="size-8 animate-spin text-primary" />
      <h2 className="text-lg font-semibold">{t("waiting")}</h2>
      <p className="max-w-md text-sm text-muted-foreground">{t.rich("waitingDesc", { name: agent.name, mono })}</p>
    </div>
  );
}

export default function OnboardingPage() {
  const [step, setStep] = React.useState(0);
  const [res, setRes] = React.useState<WizardResult | null>(null);
  const t = useTranslations("onboarding");
  return (
    <div className="mx-auto flex min-h-dvh w-full max-w-2xl flex-col px-4 py-8">
      <div className="mb-6 flex items-center justify-between">
        <Link href="/" className="flex items-center gap-2 font-semibold"><span className="grid size-7 place-items-center rounded-md bg-primary/15 text-primary"><BoxIcon className="size-4" /></span>Boxmes</Link>
        <div className="flex items-center gap-4"><LocaleSelect /><Link href="/" className="text-[13px] text-muted-foreground hover:text-foreground">{t("later")}</Link></div>
      </div>
      <h1 className="mb-1 text-2xl font-semibold tracking-tight">{t("title")}</h1>
      <p className="mb-6 text-sm text-muted-foreground">{t("desc")}</p>
      <Stepper step={step} />
      <Card className="p-5">
        {step === 0 && <TokenStep onNext={() => setStep(1)} />}
        {step === 1 && <HostStep onNext={() => setStep(2)} />}
        {step === 2 && (
          <div className="flex flex-col gap-4">
            <div>
              <h2 className="text-lg font-semibold">{t("agentTitle")}</h2>
              <p className="mt-1 text-sm text-muted-foreground">{t("agentDesc")}</p>
            </div>
            <AgentWizard compact onDone={setRes} />
            {res && <div className="flex justify-end"><Button onClick={() => setStep(3)}>{t("pasted")} <ArrowRight /></Button></div>}
          </div>
        )}
        {step === 3 && res && <WaitStep agent={res.agent} />}
      </Card>
    </div>
  );
}
