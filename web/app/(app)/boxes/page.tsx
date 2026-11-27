"use client";
import * as React from "react";
import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { FilterX, MapPin } from "lucide-react";
import { useTranslations } from "next-intl";
import { paths, type BoxFilters } from "@/lib/api";
import { useApi } from "@/lib/hooks";
import { PERMS, type Agent, type Box, type List, type LocationsView } from "@/lib/types";
import { Can } from "@/lib/session";
import { regionRank, useLocationFmt } from "@/lib/location";
import { cn } from "@/lib/utils";
import { useFmt } from "@/lib/format";
import { Card } from "@/components/ui/card";
import { Select } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { Table, TBody, TD, TH, THead, TR } from "@/components/ui/table";
import { BoxStatusChip, BOX_STATUSES, LifecycleBadge, Mono, useBoxStatusLabel } from "@/components/boxmes/status";
import { MoneyBudget, BudgetBar } from "@/components/boxmes/budget";
import { BoxActions } from "@/components/boxmes/box-actions";
import { CreateBoxDialog } from "@/components/boxmes/create-box";
import { Empty, ErrorBox, LoadingRows, PageHeader } from "@/components/boxmes/page";

const PROVIDERS = ["hetzner", "vultr", "contabo", "digitalocean", "netcup", "local"];
const GROUPS = ["none", "location", "agent", "status"] as const;
type GroupBy = (typeof GROUPS)[number];

function Chip({ active, onClick, children, title }: { active: boolean; onClick: () => void; children: React.ReactNode; title?: string }) {
  return (
    <button
      type="button"
      onClick={onClick}
      title={title}
      aria-pressed={active}
      className={cn(
        "inline-flex h-7 shrink-0 items-center gap-1.5 rounded-full border px-2.5 text-[12.5px] transition-colors",
        active ? "border-primary/60 bg-primary/15 text-primary" : "border-border text-muted-foreground hover:text-foreground",
      )}
    >
      {children}
    </button>
  );
}

interface Group {
  key: string;
  title: React.ReactNode;
  boxes: Box[];
}

function BoxesInner() {
  const router = useRouter();
  const sp = useSearchParams();
  const t = useTranslations("boxes");
  const tc = useTranslations("common");
  const statusLabel = useBoxStatusLabel();
  const { since, usd } = useFmt();
  const lf = useLocationFmt();
  const f: BoxFilters = {
    status: sp.get("status") ?? "",
    agent: sp.get("agent") ?? "",
    provider: sp.get("provider") ?? "",
    lifecycle: sp.get("lifecycle") ?? "",
    region: sp.get("region") ?? "",
    location: sp.get("location") ?? "",
  };
  const groupBy: GroupBy = GROUPS.includes(sp.get("group") as GroupBy) ? (sp.get("group") as GroupBy) : "none";
  const setMany = (kv: Record<string, string>) => {
    const n = new URLSearchParams(sp.toString());
    for (const [k, v] of Object.entries(kv)) {
      if (v) n.set(k, v);
      else n.delete(k);
    }
    router.replace(`/boxes${n.toString() ? `?${n}` : ""}`);
  };
  const set = (k: keyof BoxFilters | "group", v: string) => setMany({ [k]: v });
  const { data, error, isLoading, mutate } = useApi<List<Box>>(paths.boxes(f));
  const { data: agents } = useApi<List<Agent>>(paths.agents);
  const { data: locs } = useApi<LocationsView>(paths.boxLocations, { refreshInterval: 60_000 });
  const any = Object.values(f).some(Boolean);
  const regions = [...(locs?.regions ?? [])].sort((a, b) => regionRank(a.region) - regionRank(b.region));
  const siteRegion = locs?.items.find((l) => l.site === f.location)?.region ?? "";
  const activeRegion = f.region || siteRegion;
  const sites = (locs?.items ?? []).filter((l) => l.region === activeRegion);

  const groups: Group[] = (() => {
    const items = data?.items ?? [];
    if (groupBy === "none") return [{ key: "all", title: null, boxes: items }];
    const m = new Map<string, Group>();
    for (const b of items) {
      let key = "";
      let title: React.ReactNode = "";
      if (groupBy === "location") {
        key = b.location?.site ?? "_unplaced";
      } else if (groupBy === "agent") {
        key = b.agent_id || "_none";
        title = b.agent_name || b.agent_id || "—";
      } else {
        key = b.status;
        title = statusLabel(b.status);
      }
      const g = m.get(key) ?? { key, title, boxes: [] };
      g.boxes.push(b);
      m.set(key, g);
    }
    const out = [...m.values()];
    if (groupBy === "location") {
      for (const g of out) {
        const loc = g.boxes[0]?.location;
        const row = locs?.items.find((l) => l.site === g.key);
        g.title = loc ? (
          <span className="flex min-w-0 flex-wrap items-baseline gap-x-1.5">
            <span className="font-semibold">{lf.region(loc.region)}</span>
            <span className="text-muted-foreground">—</span>
            <span className="min-w-0 truncate">{lf.place(loc)}</span>
            <span className="text-muted-foreground">· {t("groupCount", { n: g.boxes.length })}{row ? ` · ${t("costToday", { cost: usd(row.cost_today_usd) })}` : ""}</span>
          </span>
        ) : t("unplaced");
      }
      out.sort((a, b) => {
        const la = a.boxes[0]?.location, lb = b.boxes[0]?.location;
        return regionRank(la?.region ?? "zz") - regionRank(lb?.region ?? "zz") || b.boxes.length - a.boxes.length || a.key.localeCompare(b.key);
      });
    } else out.sort((a, b) => String(a.title).localeCompare(String(b.title)));
    return out;
  })();

  return (
    <>
      <PageHeader title={t("title")} desc={t("desc")} actions={<Can perm={PERMS.boxesWrite}><CreateBoxDialog /></Can>} />
      {regions.length > 0 && (
        <div className="mb-3 flex flex-col gap-2" data-location-chips>
          <div className="no-scrollbar -mx-1 flex items-center gap-1.5 overflow-x-auto px-1">
            <MapPin className="size-4 shrink-0 text-muted-foreground" aria-label={t("location")} />
            <Chip active={!f.region && !f.location} onClick={() => setMany({ region: "", location: "" })}>{t("allLocations")} <span className="font-mono text-[11px]">{locs?.total.total ?? 0}</span></Chip>
            {regions.map((r) => (
              <Chip key={r.region} active={activeRegion === r.region && !f.location} onClick={() => setMany({ region: r.region, location: "" })}>
                {lf.region(r.region)} <span className="font-mono text-[11px]">{r.total}</span>
              </Chip>
            ))}
          </div>
          {activeRegion && sites.length > 0 && (
            <div className="no-scrollbar -mx-1 flex items-center gap-1.5 overflow-x-auto px-1 pl-6">
              {sites.map((l) => (
                <Chip key={l.site} active={f.location === l.site} title={lf.place(l)} onClick={() => setMany({ location: f.location === l.site ? "" : l.site, region: f.location === l.site ? activeRegion : "" })}>
                  {lf.short(l)} <span className="font-mono text-[11px]">{l.total}</span>
                </Chip>
              ))}
            </div>
          )}
        </div>
      )}
      <div className="mb-4 grid grid-cols-2 gap-2 sm:flex sm:flex-wrap">
        <Select value={f.status} onChange={(e) => set("status", e.target.value)} aria-label={t("filterStatus")}>
          <option value="">{t("anyStatus")}</option>
          {BOX_STATUSES.filter((k) => k !== "terminated").map((k) => <option key={k} value={k}>{statusLabel(k)}</option>)}
        </Select>
        <Select value={f.agent} onChange={(e) => set("agent", e.target.value)} aria-label={t("filterAgent")}>
          <option value="">{tc("anyAgent")}</option>
          {agents?.items.map((a) => <option key={a.id} value={a.id}>{a.name}</option>)}
        </Select>
        <Select value={f.provider} onChange={(e) => set("provider", e.target.value)} aria-label={t("filterProvider")}>
          <option value="">{tc("anyProvider")}</option>
          {PROVIDERS.map((p) => <option key={p} value={p}>{p}</option>)}
        </Select>
        <Select value={f.lifecycle} onChange={(e) => set("lifecycle", e.target.value)} aria-label={t("filterLifecycle")}>
          <option value="">{t("anyLifecycle")}</option>
          <option value="task">task</option>
          <option value="session">session</option>
          <option value="service">service</option>
        </Select>
        <Select value={groupBy} onChange={(e) => set("group", e.target.value === "none" ? "" : e.target.value)} aria-label={t("groupBy")}>
          {GROUPS.map((g) => <option key={g} value={g}>{t(`group.${g}` as "group.none")}</option>)}
        </Select>
        {any && <Button variant="ghost" size="sm" className="col-span-2 sm:col-span-1" onClick={() => router.replace(groupBy === "none" ? "/boxes" : `/boxes?group=${groupBy}`)}><FilterX /> {tc("clearFilters")}</Button>}
      </div>

      {error ? <ErrorBox error={error} retry={() => mutate()} /> : isLoading || !data ? <LoadingRows rows={6} /> : !data.items.length ? (
        <Empty title={t("empty")} desc={t("emptyDesc")} />
      ) : (
        <>
          {/* Desktop/tablet: bảng */}
          <Card className="hidden md:block">
            <Table>
              <THead>
                <TR className="hover:bg-transparent">
                  <TH>{t("colSlug")}</TH><TH>{tc("agent")}</TH><TH>{tc("status")}</TH><TH>{t("colWhere")}</TH><TH>{t("colLifecycle")}</TH><TH>{t("colUptime")}</TH><TH>{tc("spentOfBudget")}</TH><TH className="text-right">{tc("actions")}</TH>
                </TR>
              </THead>
              <TBody>
                {groups.map((g) => (
                  <React.Fragment key={g.key}>
                  {g.title !== null && (
                    <TR className="bg-surface-2/40 hover:bg-surface-2/40" data-group={g.key}>
                      <TD colSpan={8} className="py-2 text-[13px]">{g.title}</TD>
                    </TR>
                  )}
                  {g.boxes.map((b) => (
                  <TR key={b.id}>
                    <TD>
                      <Link href={`/boxes/${b.id}`} className="font-mono text-[13px] hover:text-primary">{b.slug}</Link>
                      <div className="font-mono text-[11px] text-muted-foreground">{b.id}</div>
                    </TD>
                    <TD className="text-[13px]">{b.agent_name}</TD>
                    <TD><BoxStatusChip status={b.status} /></TD>
                    <TD>
                      <Mono>{b.provider}</Mono> <span className="text-muted-foreground">/</span> <Mono className="text-muted-foreground">{b.region}</Mono>
                      {b.location && <div className="text-[11.5px] text-muted-foreground">{lf.region(b.location.region)} · {lf.short(b.location)}</div>}
                    </TD>
                    <TD><LifecycleBadge lc={b.lifecycle} /></TD>
                    <TD className="whitespace-nowrap text-[13px] text-muted-foreground">{b.status === "running" ? since(b.created_at) : "—"}</TD>
                    <TD className="min-w-[140px]">
                      <MoneyBudget spent={b.spent_usd} budget={b.budget_usd} />
                      <BudgetBar spent={b.spent_usd} budget={b.budget_usd} className="mt-1" />
                    </TD>
                    <TD><Can perm={PERMS.boxesWrite}><BoxActions box={b} className="justify-end" /></Can></TD>
                  </TR>
                  ))}
                  </React.Fragment>
                ))}
              </TBody>
            </Table>
          </Card>
          {/* Mobile: thẻ */}
          <div className="flex flex-col gap-2.5 md:hidden">
            {groups.map((g) => (
              <React.Fragment key={g.key}>
              {g.title !== null && <div className="mt-1 px-1 text-[12.5px]">{g.title}</div>}
              {g.boxes.map((b) => (
              <Card key={b.id} className="p-3.5">
                <div className="flex items-start justify-between gap-2">
                  <Link href={`/boxes/${b.id}`} className="min-w-0">
                    <p className="truncate font-mono text-sm">{b.slug}</p>
                    <p className="text-xs text-muted-foreground">{b.agent_name} · <span className="font-mono">{b.provider}/{b.region}</span>{b.location ? ` · ${lf.short(b.location)}` : ""}</p>
                  </Link>
                  <BoxStatusChip status={b.status} />
                </div>
                <div className="mt-3 flex items-center gap-2">
                  <LifecycleBadge lc={b.lifecycle} />
                  <span className="text-xs text-muted-foreground">{b.status === "running" ? since(b.created_at) : ""}</span>
                  <span className="ml-auto"><MoneyBudget spent={b.spent_usd} budget={b.budget_usd} /></span>
                </div>
                <BudgetBar spent={b.spent_usd} budget={b.budget_usd} className="mt-2" />
                <Can perm={PERMS.boxesWrite}><BoxActions box={b} className="-mb-1 mt-2 justify-end border-t border-border pt-2" /></Can>
              </Card>
              ))}
              </React.Fragment>
            ))}
          </div>
        </>
      )}
    </>
  );
}

export default function BoxesPage() {
  return (
    <React.Suspense fallback={<LoadingRows rows={6} />}>
      <BoxesInner />
    </React.Suspense>
  );
}
