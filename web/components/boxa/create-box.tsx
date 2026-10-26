"use client";
import * as React from "react";
import { useRouter } from "next/navigation";
import { useTranslations } from "next-intl";
import { Calculator, Plus, Sparkles } from "lucide-react";
import { toast } from "sonner";
import { api, errMsg, paths } from "@/lib/api";
import { startsWith, useAction, useApi } from "@/lib/hooks";
import { useFmt } from "@/lib/format";
import type { Agent, Lifecycle, List, Quote, QuoteBody, Template } from "@/lib/types";
import { cn } from "@/lib/utils";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog";
import { Field, Input, Select } from "@/components/ui/input";
import { Mono } from "./status";

/** Kết quả POST /quote: giá + lý do đặt chỗ + phương án khác. */
export function QuoteView({ quote, stale }: { quote: Quote; stale?: boolean }) {
  const t = useTranslations("createBox");
  const { usd, num } = useFmt();
  const billing = (b?: string) => (b === "burst" ? t("billingBurst") : b === "pool" ? t("billingPool") : null);
  return (
    <div className={cn("flex flex-col gap-3 rounded-lg border border-primary/30 bg-primary/5 p-3.5 text-[13px]", stale && "opacity-60")}>
      <div className="flex flex-wrap items-center justify-between gap-2">
        <p className="flex items-center gap-1.5 font-medium text-primary"><Calculator className="size-4" /> {t("quoteTitle")}</p>
        {billing(quote.billing) && <Badge variant={quote.billing === "pool" ? "green" : "yellow"}>{billing(quote.billing)}</Badge>}
      </div>
      <div className="grid grid-cols-1 gap-2 sm:grid-cols-3">
        <div className="min-w-0">
          <p className="text-xs text-muted-foreground">{t("placement")}</p>
          <Mono className="break-all">{quote.provider}/{quote.region}{quote.sku ? ` · ${quote.sku}` : ""}</Mono>
        </div>
        <div>
          <p className="text-xs text-muted-foreground">{t("perHour")}</p>
          <Mono>{usd(quote.cost_per_hour)}</Mono>
        </div>
        <div>
          <p className="text-xs text-muted-foreground">{t("expectedTotal")}</p>
          <Mono>{usd(quote.expected_total_usd)}</Mono>
          {quote.expected_hours ? <span className="ml-1 text-xs text-muted-foreground">{t("expectedHours", { hours: num(quote.expected_hours, 1) })}</span> : null}
        </div>
      </div>
      <div>
        <p className="flex items-center gap-1 text-xs text-muted-foreground"><Sparkles className="size-3" /> {t("why")}</p>
        <p className="mt-0.5">{quote.placement_reason || "—"}</p>
      </div>
      <div>
        <p className="mb-1 text-xs text-muted-foreground">{t("alternatives")}</p>
        {quote.alternatives?.length ? (
          <ul className="flex flex-col divide-y divide-border/60 rounded-md border border-border bg-background">
            {quote.alternatives.map((a, i) => (
              <li key={`${a.provider}-${a.region}-${a.sku ?? i}`} className="flex flex-wrap items-baseline gap-x-3 gap-y-0.5 px-2.5 py-1.5">
                <Mono className="min-w-0 break-all">{a.provider}/{a.region}{a.sku ? ` · ${a.sku}` : ""}</Mono>
                <Mono className="text-muted-foreground">{usd(a.cost_per_hour)}</Mono>
                {a.expected_total_usd !== undefined && <Mono className="text-muted-foreground">Σ {usd(a.expected_total_usd)}</Mono>}
                {(a.placement_reason || a.reason) && <span className="w-full text-xs text-muted-foreground">{a.placement_reason || a.reason}</span>}
              </li>
            ))}
          </ul>
        ) : (
          <p className="text-xs text-muted-foreground">{t("noAlternatives")}</p>
        )}
      </div>
      {stale && <p className="text-xs text-st-warning">{t("quoteStale")}</p>}
    </div>
  );
}

export function CreateBoxDialog() {
  const t = useTranslations("createBox");
  const tc = useTranslations("common");
  const [open, setOpen] = React.useState(false);
  const { data: agents } = useApi<List<Agent>>(open ? paths.agents : null);
  const { data: templates } = useApi<List<Template>>(open ? paths.templates : null);
  const [agentId, setAgent] = React.useState("");
  const [template, setTemplate] = React.useState("python");
  const [lifecycle, setLc] = React.useState<Lifecycle>("task");
  const [slug, setSlug] = React.useState("");
  const [cpu, setCpu] = React.useState(2);
  const [ram, setRam] = React.useState(2048);
  const [quote, setQuote] = React.useState<{ q: Quote; sig: string } | null>(null);
  const [quoting, setQuoting] = React.useState(false);
  const { run, pending } = useAction();
  const router = useRouter();
  const idem = React.useMemo(() => (open ? crypto.randomUUID() : ""), [open]);
  const body: QuoteBody = { agent_id: agentId || undefined, template, lifecycle, spec: { cpu, ram_mb: ram, disk_gb: 20 } };
  const sig = JSON.stringify(body);

  const getQuote = async () => {
    setQuoting(true);
    try {
      setQuote({ q: await api.quote(body), sig });
    } catch (e) {
      toast.error(errMsg(e));
    } finally {
      setQuoting(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={(o) => { setOpen(o); if (!o) setQuote(null); }}>
      <DialogTrigger asChild>
        <Button size="sm"><Plus /> {t("trigger")}</Button>
      </DialogTrigger>
      <DialogContent className="max-w-xl">
        <DialogHeader>
          <DialogTitle>{t("title")}</DialogTitle>
          <DialogDescription>{t("desc")}</DialogDescription>
        </DialogHeader>
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label={tc("agent")}>
            <Select value={agentId} onChange={(e) => setAgent(e.target.value)}>
              <option value="">{tc("chooseAgent")}</option>
              {agents?.items.map((a) => <option key={a.id} value={a.id}>{a.name}</option>)}
            </Select>
          </Field>
          <Field label={t("template")}>
            <Select value={template} onChange={(e) => setTemplate(e.target.value)}>
              {(templates?.items ?? [{ name: "python" } as Template]).map((tp) => <option key={tp.name} value={tp.name}>{tp.name}</option>)}
            </Select>
          </Field>
          <Field label={t("lifecycle")}>
            <Select value={lifecycle} onChange={(e) => setLc(e.target.value as Lifecycle)}>
              <option value="task">{t("lcTask")}</option>
              <option value="session">{t("lcSession")}</option>
              <option value="service">{t("lcService")}</option>
            </Select>
          </Field>
          <Field label={t("slug")}>
            <Input value={slug} onChange={(e) => setSlug(e.target.value.toLowerCase().replace(/[^a-z0-9-]/g, "-"))} placeholder={t("slugPh")} className="font-mono" />
          </Field>
          <Field label={t("vcpu")}>
            <Select value={cpu} onChange={(e) => setCpu(Number(e.target.value))}>{[1, 2, 4, 8].map((n) => <option key={n} value={n}>{n}</option>)}</Select>
          </Field>
          <Field label={t("ram")}>
            <Select value={ram} onChange={(e) => setRam(Number(e.target.value))}>{[1024, 2048, 4096, 8192].map((n) => <option key={n} value={n}>{n / 1024} GB</option>)}</Select>
          </Field>
        </div>
        {quote ? <QuoteView quote={quote.q} stale={quote.sig !== sig} /> : !agentId ? <p className="text-xs text-muted-foreground">{t("pickAgentFirst")}</p> : null}
        <DialogFooter className="gap-2 sm:justify-between">
          <Button variant="outline" disabled={quoting} onClick={getQuote}>
            <Calculator /> {quoting ? t("quoting") : t("quote")}
          </Button>
          <div className="flex flex-col-reverse gap-2 sm:flex-row">
            <Button variant="ghost" onClick={() => setOpen(false)}>{tc("cancel")}</Button>
            <Button
              disabled={!agentId || !!pending}
              onClick={async () => {
                const b = await run("create", () => api.createBox({ ...body, agent_id: agentId, slug: slug || undefined }, idem), { success: (x) => t("creatingToast", { slug: x.slug }), revalidate: [startsWith("/boxes"), "/overview"] });
                if (b) { setOpen(false); router.push(`/boxes/${b.id}`); }
              }}
            >
              {pending ? tc("creating") : t("trigger")}
            </Button>
          </div>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
