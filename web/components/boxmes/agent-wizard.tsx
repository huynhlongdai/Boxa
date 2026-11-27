"use client";
// Tạo agent 3 bước: tên + kind (22 loại từ GET /agent-kinds, nhóm theo category, có ô tìm) → ngân sách + chính sách
// → API key (hiện 1 lần) + đoạn cấu hình theo transport.
import * as React from "react";
import { useTranslations } from "next-intl";
import { AlertTriangle, ArrowLeft, ArrowRight, Check, Search } from "lucide-react";
import { useSWRConfig } from "swr";
import { api, paths } from "@/lib/api";
import { CATEGORIES } from "@/lib/agent-kinds";
import type { Agent, AgentKind, ApiKey } from "@/lib/types";
import { cn } from "@/lib/utils";
import { strip } from "@/lib/text";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Field, Input, Textarea } from "@/components/ui/input";
import { CodeBlock, CopyButton } from "./code-block";
import { SnippetViewer, useAgentKinds } from "./snippet-viewer";
import { toastError } from "@/lib/error-toast";

const PROVIDERS = ["hetzner", "vultr", "contabo", "digitalocean", "netcup"];
const REGIONS = ["fsn1", "nbg1", "hel1", "sgp", "eu-de", "sin"];

function Chip({ on, children, onClick }: { on: boolean; children: React.ReactNode; onClick: () => void }) {
  return (
    <button type="button" onClick={onClick} className={cn("rounded-full border px-3 py-1 font-mono text-[12.5px] transition-colors", on ? "border-primary/60 bg-primary/15 text-primary" : "border-border text-muted-foreground hover:text-foreground")}>
      {on && <Check className="-ml-0.5 mr-1 inline size-3" />}
      {children}
    </button>
  );
}

function Steps({ step }: { step: number }) {
  const t = useTranslations("wizard.steps");
  const labels = [t("name"), t("policy"), t("key")];
  return (
    <ol className="mb-5 flex items-center gap-2 text-[12.5px]">
      {labels.map((l, i) => (
        <li key={l} className="flex min-w-0 flex-1 items-center gap-2">
          <span className={cn("grid size-6 shrink-0 place-items-center rounded-full border text-[11px] font-semibold", i < step ? "border-st-running bg-st-running text-black" : i === step ? "border-primary text-primary" : "border-border text-muted-foreground")}>
            {i < step ? <Check className="size-3.5" /> : i + 1}
          </span>
          <span className={cn("hidden truncate sm:inline", i === step ? "text-foreground" : "text-muted-foreground")}>{l}</span>
          {i < 2 && <span className="h-px min-w-3 flex-1 bg-border" />}
        </li>
      ))}
    </ol>
  );
}

export interface WizardResult {
  agent: Agent;
  key: ApiKey;
}

function KindPicker({ value, onChange, compact }: { value: AgentKind; onChange: (k: AgentKind) => void; compact?: boolean }) {
  const t = useTranslations("kinds");
  const { kinds, fromCore } = useAgentKinds();
  const [q, setQ] = React.useState("");
  const sq = strip(q);
  const match = (k: (typeof kinds)[number]) => !sq || strip(`${k.id} ${k.name} ${k.category} ${k.transports.join(" ")}`).includes(sq);
  const shown = kinds.filter(match);
  return (
    <div className="flex flex-col gap-2">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="relative min-w-0 flex-1 sm:max-w-xs">
          <Search className="absolute left-2.5 top-2.5 size-4 text-muted-foreground" />
          <Input value={q} onChange={(e) => setQ(e.target.value)} placeholder={t("search")} className="pl-8" aria-label={t("search")} />
        </div>
        <span className="text-[11.5px] text-muted-foreground" title={fromCore ? t("fromCore") : t("fallback")}>
          {t("count", { n: kinds.length })} · {fromCore ? "/agent-kinds" : t("fallback")}
        </span>
      </div>
      <div className={cn("flex max-h-[46dvh] flex-col gap-3 overflow-y-auto pr-1", compact && "max-h-[40dvh]")}>
        {!shown.length && <p className="py-4 text-center text-[13px] text-muted-foreground">{t("noMatch", { q })}</p>}
        {CATEGORIES.map((cat) => {
          const list = shown.filter((k) => k.category === cat);
          if (!list.length) return null;
          return (
            <section key={cat}>
              <p className="mb-1.5 text-[12px] font-medium text-muted-foreground">
                {t(`category.${cat}`)} <span className="font-normal opacity-80">· {t(`categoryDesc.${cat}`)}</span>
              </p>
              <div className={cn("grid gap-2", compact ? "sm:grid-cols-2" : "sm:grid-cols-2 lg:grid-cols-3")}>
                {list.map((k) => (
                  <button key={k.id} type="button" onClick={() => onChange(k.id)} aria-pressed={value === k.id}
                    className={cn("flex min-w-0 flex-col items-start rounded-lg border p-2.5 text-left transition-colors", value === k.id ? "border-primary/60 bg-primary/10" : "border-border hover:bg-surface-2/50")}>
                    <span className="flex w-full items-center justify-between gap-2">
                      <span className="truncate text-sm font-medium">{k.name}</span>
                      {value === k.id && <Check className="size-4 shrink-0 text-primary" />}
                    </span>
                    <span className="font-mono text-[11px] text-muted-foreground">{k.id}</span>
                    <span className="mt-1.5 flex flex-wrap gap-1">
                      {k.transports.map((tr) => <Badge key={tr} variant="outline" className="px-1.5 py-0 text-[10px]">{t(`transport.${tr}`)}</Badge>)}
                    </span>
                  </button>
                ))}
              </div>
            </section>
          );
        })}
        {/* kind mới mà Core thêm nhưng category lạ */}
        {shown.filter((k) => !CATEGORIES.includes(k.category)).map((k) => (
          <button key={k.id} type="button" onClick={() => onChange(k.id)} className={cn("rounded-lg border p-2.5 text-left", value === k.id ? "border-primary/60 bg-primary/10" : "border-border")}>
            <span className="text-sm font-medium">{k.name}</span> <span className="font-mono text-[11px] text-muted-foreground">{k.id}</span>
          </button>
        ))}
      </div>
    </div>
  );
}

export function AgentWizard({ onDone, defaultName = "", compact }: { onDone?: (r: WizardResult) => void; defaultName?: string; compact?: boolean }) {
  const t = useTranslations("wizard");
  const tc = useTranslations("common");
  const { kinds } = useAgentKinds();
  const [step, setStep] = React.useState(0);
  const [name, setName] = React.useState(defaultName);
  const [kind, setKind] = React.useState<AgentKind>("claude-code");
  const [day, setDay] = React.useState("0.50");
  const [month, setMonth] = React.useState("10");
  const [providers, setProviders] = React.useState<string[]>(["hetzner", "vultr"]);
  const [regions, setRegions] = React.useState<string[]>([]);
  const [egress, setEgress] = React.useState("");
  const [busy, setBusy] = React.useState(false);
  const [result, setResult] = React.useState<WizardResult | null>(null);
  const { mutate } = useSWRConfig();

  const nameOk = /^[a-z0-9][a-z0-9-]{1,38}$/.test(name);

  const create = async () => {
    setBusy(true);
    try {
      const agent = await api.createAgent({
        name,
        kind,
        budget_day_usd: Number(day),
        budget_month_usd: Number(month),
        policy: { providers, regions, egress_allowlist: egress.trim() ? egress.split(/[\s,]+/).filter(Boolean) : null },
      });
      const key = await api.createKey({ name: `${name}-default`, agent_id: agent.id, scopes: ["boxes:write", "exec", "files", "snapshots"] });
      setResult({ agent, key });
      setStep(2);
      mutate(paths.agents);
      mutate(paths.keys);
      onDone?.({ agent, key });
    } catch (e) {
      toastError(e);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="min-w-0">
      <Steps step={step} />
      {step === 0 && (
        <div className="flex flex-col gap-4">
          <Field label={t("name")} hint={t("nameHint")}>
            <Input value={name} autoFocus onChange={(e) => setName(e.target.value.toLowerCase().replace(/\s+/g, "-"))} placeholder={t("namePh")} className="font-mono" />
          </Field>
          <div className="flex flex-col gap-1.5">
            <span className="text-[13px] font-medium">{t("kind")}</span>
            <KindPicker value={kind} onChange={setKind} compact={compact} />
          </div>
          <div className="flex justify-end">
            <Button disabled={!nameOk} onClick={() => setStep(1)}>{tc("next")} <ArrowRight /></Button>
          </div>
        </div>
      )}
      {step === 1 && (
        <div className="flex flex-col gap-4">
          <div className="grid gap-3 sm:grid-cols-2">
            <Field label={t("dayBudget")} hint={t("dayBudgetHint")}>
              <Input type="number" step="0.05" min="0" value={day} onChange={(e) => setDay(e.target.value)} className="font-mono" />
            </Field>
            <Field label={t("monthBudget")}>
              <Input type="number" step="1" min="0" value={month} onChange={(e) => setMonth(e.target.value)} className="font-mono" />
            </Field>
          </div>
          <div className="flex flex-col gap-1.5">
            <span className="text-[13px] font-medium">{t("providers")}</span>
            <div className="flex flex-wrap gap-1.5">
              {PROVIDERS.map((p) => <Chip key={p} on={providers.includes(p)} onClick={() => setProviders((x) => (x.includes(p) ? x.filter((y) => y !== p) : [...x, p]))}>{p}</Chip>)}
            </div>
          </div>
          <div className="flex flex-col gap-1.5">
            <span className="text-[13px] font-medium">{t("regions")} <span className="font-normal text-muted-foreground">{t("regionsAny")}</span></span>
            <div className="flex flex-wrap gap-1.5">
              {REGIONS.map((r) => <Chip key={r} on={regions.includes(r)} onClick={() => setRegions((x) => (x.includes(r) ? x.filter((y) => y !== r) : [...x, r]))}>{r}</Chip>)}
            </div>
          </div>
          <Field label={t("egress")} hint={t("egressHint")}>
            <Textarea value={egress} onChange={(e) => setEgress(e.target.value)} placeholder={"*.shopee.vn\napi.openai.com"} className="font-mono" rows={3} />
          </Field>
          <div className="flex flex-wrap justify-between gap-2">
            <Button variant="ghost" onClick={() => setStep(0)}><ArrowLeft /> {tc("back")}</Button>
            <Button disabled={busy || !providers.length || !(Number(day) >= 0) || !(Number(month) >= 0)} onClick={create}>{busy ? tc("creating") : t("create")} <ArrowRight /></Button>
          </div>
        </div>
      )}
      {step === 2 && result && (
        <div className="flex min-w-0 flex-col gap-4">
          <div className="rounded-lg border border-st-warning/40 bg-st-warning/5 p-3">
            <p className="flex items-center gap-1.5 text-[13px] font-medium text-st-warning"><AlertTriangle className="size-4 shrink-0" /> {t("keyOnce")}</p>
            <p className="mt-1 text-[12.5px] text-muted-foreground">{t("keyOnceDesc")}</p>
            <div className="mt-2 flex items-center gap-2">
              <code className="min-w-0 flex-1 truncate rounded border border-border bg-background px-2 py-1.5 font-mono text-[13px]">{result.key.key ?? `${result.key.prefix}…`}</code>
              <CopyButton text={result.key.key ?? ""} label={tc("copyKey")} />
            </div>
          </div>
          <div className="flex flex-col gap-2">
            <span className="text-[13px] font-medium">{t("envStep")}</span>
            <CodeBlock code={`export BOXMES_API_KEY=${result.key.key ?? "<key>"}\nexport BOXMES_URL=${(process.env.NEXT_PUBLIC_BOXMES_API || "http://localhost:8080/v1").replace(/\/v1\/?$/, "")}`} language="bash" maxHeight={120} />
          </div>
          <div className="flex min-w-0 flex-col gap-2">
            <span className="text-[13px] font-medium">{t("snippetStep")}</span>
            <SnippetViewer agentId={result.agent.id} agentName={result.agent.name} kind={result.agent.kind || kind} apiKey={result.key.key} kinds={kinds} />
          </div>
        </div>
      )}
    </div>
  );
}
