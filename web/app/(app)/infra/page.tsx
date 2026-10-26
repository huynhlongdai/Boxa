"use client";
import * as React from "react";
import { ArrowDownUp, CheckCircle2, Cpu, Plug, PlugZap, Plus, ServerCog, Trash2, TrendingDown } from "lucide-react";
import { useRouter, useSearchParams } from "next/navigation";
import { useTranslations } from "next-intl";
import { api, paths } from "@/lib/api";
import { useAction, useApi } from "@/lib/hooks";
import type { Host, List, Offer, Provider, ProviderRegion } from "@/lib/types";
import { useFmt } from "@/lib/format";
import { Card } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input, Select } from "@/components/ui/input";
import { Progress } from "@/components/ui/progress";
import { Table, TBody, TD, TH, THead, TR } from "@/components/ui/table";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog";
import { ConfirmButton } from "@/components/boxa/confirm";
import { ApprovalList } from "@/components/boxa/approvals";
import { ConfidenceBadge, HostStatusChip, Mono, ProviderChip } from "@/components/boxa/status";
import { ErrorBox, LoadingRows, PageHeader, SectionTitle } from "@/components/boxa/page";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { CatalogView, ConnectProviderWizard, ProviderHealthView } from "@/components/boxa/provider-manager";
import { SpeedSummary, WarmPoolSettings } from "@/components/boxa/speed";
import { MeshView } from "@/components/boxa/mesh";

const TABS = ["providers", "health", "catalog", "speed", "mesh"] as const;


const TOKEN_HINT: Record<string, string> = { contabo: "contabo", netcup: "netcup", digitalocean: "digitalocean", hetzner: "hetzner", vultr: "vultr" };

function Regions({ id }: { id: string }) {
  const [open, setOpen] = React.useState(false);
  const { data } = useApi<List<ProviderRegion>>(open ? paths.providerRegions(id) : null, { refreshInterval: 0 });
  const t = useTranslations("infra");
  return (
    <details className="mt-2 text-xs" onToggle={(e) => setOpen((e.target as HTMLDetailsElement).open)}>
      <summary className="cursor-pointer select-none text-muted-foreground hover:text-foreground">{t("regions")}{data ? ` (${data.items.length})` : ""}</summary>
      <div className="mt-1.5 flex flex-wrap gap-1">
        {!data ? <span className="text-muted-foreground">…</span> : data.items.length === 0 ? <span className="text-muted-foreground">—</span> : data.items.map((r) => (
          <Badge key={r.id} variant="outline" title={r.name} className="font-mono">{r.id}</Badge>
        ))}
      </div>
    </details>
  );
}

function ProviderCard({ p }: { p: Provider }) {
  const [token, setToken] = React.useState("");
  const [editing, setEditing] = React.useState(!p.connected && p.id !== "local");
  const { run, pending } = useAction();
  const t = useTranslations("infra");
  const tc = useTranslations("common");
  const { relTime } = useFmt();
  return (
    <Card className="flex flex-col p-4">
      <div className="flex items-start justify-between gap-2">
        <div>
          <p className="font-medium">{p.name}</p>
          <p className="mt-0.5 flex items-center gap-1.5 text-xs text-muted-foreground">
            <Badge variant="outline" className="font-mono">{p.tier}</Badge> {["T1", "T2", "T3"].includes(p.tier) ? t(`tier.${p.tier}`) : ""}
          </p>
        </div>
        <ProviderChip p={p} />
      </div>
      <div className="mt-2 flex flex-wrap gap-1.5">
        {p.chain_rank ? <Badge variant="blue" title={t("chainHint")}>{t("chainRank", { n: p.chain_rank })}</Badge> : null}
        {p.token_source === "env" && <Badge variant="outline" title={t("envTokenHint")}>{t("envToken")}</Badge>}
        {p.implemented === false && <Badge variant="yellow">{t("feedOnly")}</Badge>}
      </div>
      {p.error && <p className="mt-2 text-[12.5px] text-st-error">{p.error}</p>}
      <p className="mt-2 text-xs text-muted-foreground">{t("lastCheck", { time: relTime(p.last_check_at) })}</p>
      {p.id !== "local" && <Regions id={p.id} />}
      <div className="mt-auto pt-3">
        {p.id === "local" ? (
          <p className="text-xs text-muted-foreground">{t("localHint")}</p>
        ) : editing ? (
          <>
          {TOKEN_HINT[p.id] && <p className="mb-1.5 text-xs text-muted-foreground">{t(`tokenHint.${TOKEN_HINT[p.id]}`)}</p>}
          <form className="flex gap-2" onSubmit={async (e) => {
            e.preventDefault();
            const r = await run("tok", () => api.setProviderToken(p.id, token), { success: t("connectedToast", { name: p.name }), revalidate: [paths.providers] });
            if (r) { setToken(""); setEditing(false); }
          }}>
            <Input type="password" autoComplete="off" value={token} onChange={(e) => setToken(e.target.value)} placeholder={t("pasteToken")} className="min-w-0 font-mono" aria-label={t("tokenFor", { name: p.name })} />
            <Button type="submit" size="sm" disabled={!token || !!pending}>{pending ? tc("checking") : t("check")}</Button>
          </form>
          </>
        ) : (
          <Button size="sm" variant="outline" onClick={() => setEditing(true)}><Plug /> {t("changeToken")}</Button>
        )}
      </div>
    </Card>
  );
}

function Bar({ label, v }: { label: string; v: number }) {
  return (
    <div className="flex items-center gap-2 text-[12px]">
      <span className="w-12 shrink-0 truncate text-muted-foreground">{label}</span>
      <Progress value={v} warnAt={75} dangerAt={92} className="flex-1" />
      <span className="w-9 text-right font-mono">{v}%</span>
    </div>
  );
}

function AddHostDialog({ offers, providers }: { offers: Offer[]; providers: Provider[] }) {
  const connected = providers.filter((p) => p.connected).map((p) => p.id as string);
  const choices = offers.filter((o) => connected.includes(o.provider));
  const [sel, setSel] = React.useState(0);
  const [open, setOpen] = React.useState(false);
  const { run, pending } = useAction();
  const t = useTranslations("infra");
  const tc = useTranslations("common");
  const { usd, mb } = useFmt();
  const o = choices[sel];
  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild><Button size="sm"><Plus /> {t("addHost")}</Button></DialogTrigger>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("addHost")}</DialogTitle>
          <DialogDescription>{t("addHostDesc")}</DialogDescription>
        </DialogHeader>
        <Select value={sel} onChange={(e) => setSel(Number(e.target.value))}>
          {choices.map((c, i) => <option key={`${c.provider}-${c.sku}-${c.region}`} value={i}>{c.provider} · {c.sku} · {c.region} — {tc("perMonth", { price: usd(c.price_month, 2) })}</option>)}
        </Select>
        {o && <p className="text-[13px] text-muted-foreground"><Mono>{o.cpu} vCPU · {mb(o.ram_mb)} · {o.disk_gb} GB</Mono> · <ConfidenceBadge c={o.confidence} /></p>}
        <DialogFooter>
          <Button variant="ghost" onClick={() => setOpen(false)}>{tc("cancel")}</Button>
          <Button disabled={!o || !!pending} onClick={async () => {
            const h = await run("h", () => api.createHost({ provider: o.provider, sku: o.sku, region: o.region }), { success: t("creatingHost"), revalidate: [paths.hosts] });
            if (h) setOpen(false);
          }}>{t("createHost", { price: o ? usd(o.price_month, 2) : "" })}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function ProvidersTab() {
  const { data: providers, error: pe } = useApi<List<Provider>>(paths.providers);
  const { data: hosts, error: he, mutate: mh } = useApi<List<Host>>(paths.hosts, { refreshInterval: (d) => (d?.items.some((h) => h.status === "provisioning") ? 2500 : 15000) });
  const { data: offers } = useApi<List<Offer>>(paths.offers);
  const { run } = useAction();
  const t = useTranslations("infra");
  const tc = useTranslations("common");
  const { usd, mb, relTime, unit } = useFmt();
  const [sortAsc, setSortAsc] = React.useState(true);
  const sortedOffers = [...(offers?.items ?? [])].sort((a, b) => (sortAsc ? 1 : -1) * (a.price_hour / a.cpu - b.price_hour / b.cpu));
  const poolMonth = (hosts?.items ?? []).filter((h) => h.billing === "pool").reduce((a, h) => a + h.price_month, 0);
  const [wizard, setWizard] = React.useState(false);

  return (
    <>
      <SectionTitle>{t("expansion")}</SectionTitle>
      <div className="mb-6"><ApprovalList filter={(a) => a.type === "pool_expand" || a.type === "host_down"} emptyText={t("noExpansion")} /></div>

      <SectionTitle right={providers && <Button size="sm" data-open-wizard onClick={() => setWizard(true)}><PlugZap /> {t("connectWizard")}</Button>}>{t("providers")}</SectionTitle>
      {providers && <ConnectProviderWizard providers={providers.items} open={wizard} onOpenChange={setWizard} />}
      {pe ? <ErrorBox error={pe} /> : !providers ? <LoadingRows rows={2} /> : (
        <>
          <p className="mb-3 flex flex-wrap items-center gap-1.5 text-[13px] text-muted-foreground">
            <span>{t("chainTitle")}</span>
            <Badge variant="green">{t("chainPool")}</Badge>
            {[...providers.items].filter((p) => p.chain_rank).sort((a, b) => (a.chain_rank ?? 0) - (b.chain_rank ?? 0)).map((p) => (
              <React.Fragment key={p.id}>
                <span aria-hidden>→</span>
                <Badge variant={p.connected ? "default" : "outline"} className={p.connected ? "" : "opacity-70"}>{p.name}</Badge>
              </React.Fragment>
            ))}
          </p>
          <div className="mb-6 grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
            {providers.items.map((p) => <ProviderCard key={p.id} p={p} />)}
          </div>
        </>
      )}

      <SectionTitle right={providers && offers && <AddHostDialog offers={offers.items} providers={providers.items} />}>
        {t("hosts")} <span className="ml-1 text-[13px] font-normal text-muted-foreground">{t("poolMonth", { price: usd(poolMonth, 2) })}</span>
      </SectionTitle>
      {he ? <ErrorBox error={he} retry={() => mh()} /> : !hosts ? <LoadingRows /> : (
        <div className="mb-6 grid gap-3 md:grid-cols-2 xl:grid-cols-3">
          {hosts.items.map((h) => (
            <Card key={h.id} className="p-4">
              <div className="flex items-start justify-between gap-2">
                <div className="min-w-0">
                  <p className="flex items-center gap-1.5"><ServerCog className="size-4 text-muted-foreground" /><Mono className="truncate">{h.id}</Mono></p>
                  <p className="mt-0.5 text-xs text-muted-foreground"><Mono>{h.provider}/{h.region} · {h.sku} · {h.ip}</Mono></p>
                </div>
                <HostStatusChip status={h.status} />
              </div>
              <div className="mt-3 flex flex-col gap-1.5">
                <Bar label={t("cpu")} v={h.cpu_used_pct} />
                <Bar label={t("ram")} v={h.ram_used_pct} />
                <Bar label={t("disk")} v={h.disk_used_pct} />
              </div>
              <div className="mt-3 flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
                <Badge variant={h.billing === "pool" ? "green" : "yellow"}>{h.billing === "pool" ? t("billingPool") : t("billingBurst")}</Badge>
                <span><Cpu className="mr-0.5 inline size-3" /><Mono>{h.cpu} vCPU · {mb(h.ram_mb)} · {h.disk_gb} GB</Mono></span>
                <span>{tc("boxCount", { n: h.box_count })}</span>
                <span className="ml-auto font-mono text-foreground">{h.billing === "pool" ? tc("perMonth", { price: usd(h.price_month, 2) }) : tc("perHour", { price: usd(h.price_hour) })}</span>
              </div>
              <div className="mt-3 flex gap-2 border-t border-border pt-3">
                <ConfirmButton size="xs" variant="outline" disabled={h.status !== "ready"} title={t("drainTitle", { id: h.id })} desc={t("drainDesc")} confirmLabel={t("drain")}
                  onConfirm={() => run("d", () => api.drainHost(h.id), { success: t("draining"), revalidate: [paths.hosts] })}>
                  <ArrowDownUp /> {t("drain")}
                </ConfirmButton>
                <ConfirmButton size="xs" variant="outline" destructive title={t("deleteTitle", { id: h.id })} desc={t("deleteDesc")} confirmLabel={t("deleteHost")} typeToConfirm={h.id}
                  onConfirm={() => run("x", () => api.deleteHost(h.id), { success: t("hostDeleted"), revalidate: [paths.hosts] })}>
                  <Trash2 /> {tc("delete")}
                </ConfirmButton>
              </div>
            </Card>
          ))}
        </div>
      )}

      <SectionTitle right={<span className="text-xs text-muted-foreground">{t("source", { time: relTime(offers?.items[0]?.fetched_at) })}</span>}>{t("prices")}</SectionTitle>
      <Card>
        <Table>
          <THead>
            <TR className="hover:bg-transparent">
              <TH>{tc("provider")}</TH><TH>{t("colSku")}</TH><TH>{tc("region")}</TH><TH>{t("colConfig")}</TH>
              <TH><button className="inline-flex items-center gap-1 hover:text-foreground" onClick={() => setSortAsc((x) => !x)}>{t("colPerVcpu")} <ArrowDownUp className="size-3" /></button></TH>
              <TH>{t("colHour")}</TH><TH>{t("colMonth")}</TH><TH className="hidden lg:table-cell">{t("colMin")}</TH><TH>{t("colConfidence")}</TH>
            </TR>
          </THead>
          <TBody>
            {sortedOffers.map((o, i) => (
              <TR key={`${o.provider}-${o.sku}-${o.region}`}>
                <TD className="whitespace-nowrap"><Mono>{o.provider}</Mono>{i === 0 && sortAsc && <TrendingDown className="ml-1 inline size-3.5 text-st-running" />}</TD>
                <TD><Mono>{o.sku}</Mono></TD>
                <TD><Mono>{o.region}</Mono></TD>
                <TD className="whitespace-nowrap"><Mono className="text-muted-foreground">{o.cpu}c · {mb(o.ram_mb)} · {o.disk_gb}G</Mono></TD>
                <TD className="font-mono text-[13px] font-medium">{usd(o.price_hour / o.cpu, 5)}</TD>
                <TD className="font-mono text-[13px]">{usd(o.price_hour)}</TD>
                <TD className="font-mono text-[13px]">{usd(o.price_month, 2)}</TD>
                <TD className="hidden text-[13px] text-muted-foreground lg:table-cell">{o.min_billing_minutes >= 43200 ? t("oneMonth") : unit(o.min_billing_minutes, "minute")}</TD>
                <TD><ConfidenceBadge c={o.confidence} /></TD>
              </TR>
            ))}
          </TBody>
        </Table>
      </Card>
      <p className="mt-2 flex items-center gap-1.5 text-xs text-muted-foreground"><CheckCircle2 className="size-3.5 shrink-0" /> {t("unverifiedNote")}</p>
    </>
  );
}

function Inner() {
  const t = useTranslations("infra");
  const sp = useSearchParams();
  const router = useRouter();
  const raw = sp.get("s") ?? "providers";
  const tab = (TABS as readonly string[]).includes(raw) ? raw : "providers";
  return (
    <>
      <PageHeader title={t("title")} desc={t("desc")} />
      <Tabs value={tab} onValueChange={(v) => router.replace(v === "providers" ? "/infra" : `/infra?s=${v}`, { scroll: false })}>
        <TabsList>{TABS.map((v) => <TabsTrigger key={v} value={v}>{t(`tabs.${v}`)}</TabsTrigger>)}</TabsList>
        <TabsContent value="providers"><ProvidersTab /></TabsContent>
        <TabsContent value="health"><ProviderHealthView /></TabsContent>
        <TabsContent value="catalog"><CatalogView /></TabsContent>
        <TabsContent value="speed"><SpeedSummary /><WarmPoolSettings /></TabsContent>
        <TabsContent value="mesh"><MeshView /></TabsContent>
      </Tabs>
    </>
  );
}

export default function InfraPage() {
  return <React.Suspense fallback={<LoadingRows />}><Inner /></React.Suspense>;
}
