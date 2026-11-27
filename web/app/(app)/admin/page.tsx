"use client";
import * as React from "react";
import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { Activity, AlertTriangle, FilterX, Plus, Save } from "lucide-react";
import { useTranslations } from "next-intl";
import { api, paths } from "@/lib/api";
import { useAction, useApi } from "@/lib/hooks";
import type { AdminSystem, AdminUser, Agent, ApiKey, BoxmesEvent, Health, List, Quotas, SchedJob, Settings } from "@/lib/types";
import { cn } from "@/lib/utils";
import { useFmt } from "@/lib/format";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Field, Input, Select, Textarea } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { Table, TBody, TD, TH, THead, TR } from "@/components/ui/table";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog";
import { ConfirmButton } from "@/components/boxmes/confirm";
import { CopyButton } from "@/components/boxmes/code-block";
import { Dot, Mono } from "@/components/boxmes/status";
import { Empty, ErrorBox, LoadingRows, PageHeader } from "@/components/boxmes/page";

function SystemTab() {
  const { data, error } = useApi<AdminSystem>(paths.adminSystem, { refreshInterval: 15000 });
  const { data: health } = useApi<Health>(paths.health);
  const t = useTranslations("admin");
  const tc = useTranslations("common");
  const f = useFmt();
  if (error) return <ErrorBox error={error} />;
  if (!data) return <LoadingRows />;
  const tone = (s: string) => (s === "ok" ? "green" : s === "degraded" ? "yellow" : "red") as "green" | "yellow" | "red";
  return (
    <div className="flex flex-col gap-4">
      <div className="grid grid-cols-2 gap-3 md:grid-cols-4">
        <Card className="p-4"><p className="text-xs text-muted-foreground">{t("version")}</p><p className="mt-1 font-mono text-lg">{data.version}</p></Card>
        <Card className="p-4"><p className="text-xs text-muted-foreground">{t("uptime")}</p><p className="mt-1 font-mono text-lg">{f.seconds(data.uptime_s)}</p></Card>
        <Card className="p-4"><p className="text-xs text-muted-foreground">{t("health")}</p><p className="mt-1 text-lg">{health?.ok ? <span className="text-st-running">{t("ok")}</span> : <span className="text-st-error">{t("fail")}</span>}</p></Card>
        <Card className="p-4"><p className="text-xs text-muted-foreground">{t("dbBurst")}</p><p className="mt-1 break-words font-mono text-lg">{data.db ?? "—"} · {data.auto_burst ? t("on") : t("off")}</p></Card>
      </div>
      <Card>
        <Table>
          <THead><TR className="hover:bg-transparent"><TH>{t("component")}</TH><TH>{tc("status")}</TH><TH className="hidden md:table-cell">{t("detail")}</TH><TH className="text-right">{t("latency")}</TH></TR></THead>
          <TBody>
            {data.components.map((c) => (
              <TR key={c.name}>
                <TD><Mono>{c.name}</Mono></TD>
                <TD><Badge variant={tone(c.status)}><Dot tone={tone(c.status)} />{["ok", "degraded", "down"].includes(c.status) ? t(`compStatus.${c.status}`) : c.status}</Badge></TD>
                <TD className="hidden text-[13px] text-muted-foreground md:table-cell">{c.detail ?? ""}</TD>
                <TD className={cn("text-right font-mono text-[13px]", c.latency_ms > 500 && "text-st-warning")}>{c.latency_ms < 1 ? c.latency_ms.toFixed(2) : Math.round(c.latency_ms)} ms</TD>
              </TR>
            ))}
          </TBody>
        </Table>
      </Card>
    </div>
  );
}

function JobsTab() {
  const { data, error } = useApi<List<SchedJob>>(paths.adminJobs, { refreshInterval: 10000 });
  const t = useTranslations("admin");
  const tc = useTranslations("common");
  const f = useFmt();
  if (error) return <ErrorBox error={error} />;
  if (!data) return <LoadingRows />;
  if (!data.items.length) return <Empty title={t("noJobs")} />;
  return (
    <div className="flex flex-col gap-2">
      <p className="text-[13px] text-muted-foreground">{t("jobsDesc")}</p>
      <Card>
        <Table>
          <THead><TR className="hover:bg-transparent"><TH>{t("colJob")}</TH><TH>{t("colEvery")}</TH><TH>{t("colLastRun")}</TH><TH>{tc("status")}</TH><TH>{t("colDuration")}</TH><TH className="text-right">{t("colRuns")}</TH></TR></THead>
          <TBody>
            {data.items.map((j) => (
              <TR key={j.name}>
                <TD><Mono>{j.name}</Mono></TD>
                <TD className="whitespace-nowrap text-[13px]">{t("every", { time: f.seconds(j.interval_s) })}</TD>
                <TD className="whitespace-nowrap text-[13px] text-muted-foreground">{f.relTime(j.last_run_at)}</TD>
                <TD>
                  <Badge variant={j.last_status === "ok" ? "green" : j.last_status ? "red" : "gray"}>{j.last_status === "ok" ? t("jobOk") : j.last_status || t("neverRan")}</Badge>
                  {j.last_error && <p className="mt-1 max-w-[320px] text-xs text-st-error">{j.last_error}</p>}
                </TD>
                <TD className="whitespace-nowrap font-mono text-[13px]">{f.ms(j.duration_ms)}</TD>
                <TD className="text-right font-mono text-[13px]">{f.num(j.runs)}</TD>
              </TR>
            ))}
          </TBody>
        </Table>
      </Card>
    </div>
  );
}

function AuditTab() {
  const [actor, setActor] = React.useState("");
  const [type, setType] = React.useState("");
  const { data, error } = useApi<List<BoxmesEvent>>(paths.adminAudit({ actor: actor || undefined, type: type || undefined }));
  const t = useTranslations("admin");
  const tc = useTranslations("common");
  const { dateTime } = useFmt();
  return (
    <div className="flex flex-col gap-3">
      <div className="grid grid-cols-2 gap-2 sm:flex">
        <Input value={actor} onChange={(e) => setActor(e.target.value)} placeholder={t("actorPh")} aria-label={t("colActor")} className="font-mono sm:w-64" />
        <Select value={type} onChange={(e) => setType(e.target.value)} aria-label={t("colType")}>
          <option value="">{t("anyType")}</option>
          {["box", "exec", "agent", "key", "secret", "host", "provider", "approval", "budget", "settings", "emergency", "terminal"].map((t) => <option key={t} value={t}>{t}.*</option>)}
        </Select>
        {(actor || type) && <Button size="sm" variant="ghost" onClick={() => { setActor(""); setType(""); }}><FilterX /> {tc("clearFilters")}</Button>}
      </div>
      {error ? <ErrorBox error={error} /> : !data ? <LoadingRows /> : !data.items.length ? <Empty title={t("noEvents")} /> : (
        <Card>
          <Table>
            <THead><TR className="hover:bg-transparent"><TH>{tc("time")}</TH><TH>{t("colActor")}</TH><TH>{t("colType")}</TH><TH>{t("colMessage")}</TH></TR></THead>
            <TBody>
              {data.items.slice(0, 200).map((e) => (
                <TR key={e.id}>
                  <TD className="whitespace-nowrap text-[13px] text-muted-foreground">{dateTime(e.ts)}</TD>
                  <TD className="whitespace-nowrap"><Mono>{e.actor}</Mono></TD>
                  <TD><Badge variant={e.type.includes("fail") || e.type.startsWith("emergency") ? "red" : "outline"} className="font-mono">{e.type}</Badge></TD>
                  <TD className="min-w-[240px] text-[13px]">{e.message}</TD>
                </TR>
              ))}
            </TBody>
          </Table>
        </Card>
      )}
    </div>
  );
}

function KeysTab() {
  const { data, error } = useApi<List<ApiKey>>(paths.keys);
  const { data: agents } = useApi<List<Agent>>(paths.agents);
  const { run, pending } = useAction();
  const [open, setOpen] = React.useState(false);
  const [name, setName] = React.useState("");
  const [created, setCreated] = React.useState<ApiKey | null>(null);
  const t = useTranslations("admin");
  const tc = useTranslations("common");
  const ta = useTranslations("agentDetail");
  const { relTime } = useFmt();
  const agentName = (id: string | null) => (id ? agents?.items.find((a) => a.id === id)?.name ?? id : null);
  if (error) return <ErrorBox error={error} />;
  if (!data) return <LoadingRows />;
  return (
    <div className="flex flex-col gap-3">
      <div className="flex justify-end">
        <Dialog open={open} onOpenChange={(o) => { setOpen(o); if (!o) { setCreated(null); setName(""); } }}>
          <DialogTrigger asChild><Button size="sm"><Plus /> {t("createAdminKey")}</Button></DialogTrigger>
          <DialogContent>
            <DialogHeader><DialogTitle>{t("createAdminKey")}</DialogTitle><DialogDescription>{t("adminKeyDesc")}</DialogDescription></DialogHeader>
            {!created ? (
              <>
                <Field label={ta("keyName")}><Input value={name} onChange={(e) => setName(e.target.value)} placeholder={t("keyNamePh")} /></Field>
                <DialogFooter><Button disabled={!name || !!pending} onClick={async () => { const k = await run("k", () => api.createKey({ name, agent_id: null, scopes: ["admin"] }), { revalidate: [paths.keys] }); if (k) setCreated(k); }}>{tc("create")}</Button></DialogFooter>
              </>
            ) : (
              <div className="rounded-lg border border-st-warning/40 bg-st-warning/5 p-3">
                <p className="flex items-center gap-1.5 text-[13px] font-medium text-st-warning"><AlertTriangle className="size-4 shrink-0" /> {t("onceOnly")}</p>
                <div className="mt-2 flex items-center gap-2"><code className="min-w-0 flex-1 truncate rounded border border-border bg-background px-2 py-1.5 font-mono text-[13px]">{created.key}</code><CopyButton text={created.key ?? ""} label={tc("copyKey")} /></div>
              </div>
            )}
          </DialogContent>
        </Dialog>
      </div>
      <Card>
        <Table>
          <THead><TR className="hover:bg-transparent"><TH>{tc("name")}</TH><TH>{ta("prefix")}</TH><TH>{tc("agent")}</TH><TH>{ta("scopes")}</TH><TH>{ta("lastUsed")}</TH><TH className="text-right">{tc("status")}</TH></TR></THead>
          <TBody>
            {data.items.map((k) => (
              <TR key={k.id}>
                <TD className="text-[13px]">{k.name}</TD>
                <TD><Mono>{k.prefix}…</Mono></TD>
                <TD>{agentName(k.agent_id) ? <Link href={`/agents/${k.agent_id}`} className="font-mono text-[12.5px] hover:text-primary">{agentName(k.agent_id)}</Link> : <Badge variant="yellow">{t("adminBadge")}</Badge>}</TD>
                <TD><div className="flex flex-wrap gap-1">{k.scopes.map((s) => <Badge key={s} variant="outline" className="font-mono">{s}</Badge>)}</div></TD>
                <TD className="whitespace-nowrap text-[13px] text-muted-foreground">{relTime(k.last_used_at)}</TD>
                <TD className="text-right">
                  {k.revoked_at ? <Badge variant="gray">{t("revoked")}</Badge> : (
                    <ConfirmButton size="xs" variant="outline" destructive title={t("revokeTitle", { prefix: k.prefix })} desc={k.scopes.includes("admin") ? t("revokeAdminDesc") : t("revokeAgentDesc")} confirmLabel={ta("revoke")}
                      onConfirm={() => run("r", () => api.revokeKey(k.id), { success: t("revoked"), revalidate: [paths.keys] })}>{ta("revoke")}</ConfirmButton>
                  )}
                </TD>
              </TR>
            ))}
          </TBody>
        </Table>
      </Card>
    </div>
  );
}

/** Khoá quota đã biết → đơn vị ("@box"/"@request" = dịch theo admin.unit). */
const QUOTA_UNITS: Record<string, string> = {
  max_boxes_per_agent: "@box",
  max_concurrent_boxes: "@box",
  max_cpu_per_box: "vCPU",
  max_ram_mb_per_box: "MB",
  max_disk_gb_per_box: "GB",
  rate_limit_per_minute: "@request",
  max_spend_day_usd: "USD",
};

function QuotaForm({ initial }: { initial: Quotas }) {
  const [q, setQ] = React.useState(initial);
  const { run, pending } = useAction();
  const t = useTranslations("admin");
  const keys = Object.keys(initial).filter((k) => typeof initial[k] === "number");
  return (
    <Card>
      <CardHeader><CardTitle>{t("tabs.quotas")}</CardTitle><CardDescription>{t("quotasDesc")}</CardDescription></CardHeader>
      <CardContent className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
        {keys.map((k) => {
          const known = k in QUOTA_UNITS;
          const label = known ? t(`quota.${k}`) : k;
          const rawUnit = QUOTA_UNITS[k] ?? "";
          const unit = rawUnit.startsWith("@") ? t(`unit.${rawUnit.slice(1)}`) : rawUnit;
          return (
            <Field key={k} label={label} hint={<><span className="font-mono">{k}</span>{unit ? ` · ${unit}` : ""}</>}>
              <Input type="number" min={0} step={k.endsWith("_usd") ? 0.5 : 1} value={q[k]} onChange={(e) => setQ((x) => ({ ...x, [k]: Number(e.target.value) }))} className="font-mono" />
            </Field>
          );
        })}
        <div className="flex justify-end sm:col-span-2 lg:col-span-3">
          <Button disabled={JSON.stringify(q) === JSON.stringify(initial) || !!pending} onClick={() => run("q", () => api.saveQuotas(q), { success: t("savedQuotas"), revalidate: [paths.adminQuotas] })}><Save /> {t("saveQuotas")}</Button>
        </div>
      </CardContent>
    </Card>
  );
}

function QuotasTab() {
  const { data, error } = useApi<Quotas>(paths.adminQuotas, { refreshInterval: 0 });
  if (error) return <ErrorBox error={error} />;
  if (!data) return <LoadingRows />;
  return <QuotaForm key={JSON.stringify(data)} initial={data} />;
}

function FeaturesTab() {
  const { data: s, error } = useApi<Settings>(paths.settings);
  const { run, pending } = useAction();
  const [msg, setMsg] = React.useState<string | null>(null);
  const t = useTranslations("admin");
  if (error) return <ErrorBox error={error} />;
  if (!s) return <LoadingRows />;
  const message = msg ?? s.maintenance.message;
  const save = (next: Settings, success: string) => run("s", () => api.saveSettings(next), { success, revalidate: [paths.settings] });
  return (
    <div className="grid gap-4 lg:grid-cols-2">
      <Card>
        <CardHeader><CardTitle>{t("features")}</CardTitle></CardHeader>
        <CardContent className="flex flex-col gap-2">
          <label className="flex items-center justify-between gap-3 rounded-md border border-border px-3 py-2.5 text-[13px] opacity-80">
            <span className="min-w-0">{t("gpu")}<span className="block text-xs text-muted-foreground">{t("gpuHint")}</span></span>
            <span className="flex items-center gap-2"><Badge variant="outline" className="font-mono">P5</Badge><Switch checked={false} disabled aria-label="GPU (P5)" /></span>
          </label>
        </CardContent>
      </Card>
      <Card className={cn(s.maintenance.enabled && "border-st-warning/50")}>
        <CardHeader><CardTitle>{t("maintenance")}</CardTitle><CardDescription>{t("maintenanceDesc")}</CardDescription></CardHeader>
        <CardContent className="flex flex-col gap-3">
          <label className="flex items-center justify-between gap-3 rounded-md border border-border px-3 py-2.5 text-[13px]">
            <span className="font-medium">{s.maintenance.enabled ? t("underMaintenance") : t("normal")}</span>
            <Switch checked={s.maintenance.enabled} disabled={!!pending} onCheckedChange={(c) => save({ ...s, maintenance: { enabled: c, message } }, c ? t("maintenanceOn") : t("maintenanceOff"))} />
          </label>
          <Field label={t("bannerMessage")}><Textarea rows={2} value={message} onChange={(e) => setMsg(e.target.value)} /></Field>
          <div className="flex justify-end"><Button size="sm" variant="secondary" disabled={message === s.maintenance.message || !!pending} onClick={() => save({ ...s, maintenance: { ...s.maintenance, message } }, t("messageSaved"))}><Save /> {t("saveMessage")}</Button></div>
        </CardContent>
      </Card>
    </div>
  );
}

function UsersTab() {
  const { data, error } = useApi<List<AdminUser>>(paths.adminUsers);
  const t = useTranslations("admin");
  const { relTime } = useFmt();
  if (error) return <ErrorBox error={error} />;
  if (!data) return <LoadingRows />;
  const roleTone: Record<string, "yellow" | "blue" | "gray"> = { owner: "yellow", admin: "blue", member: "gray", viewer: "gray" };
  return (
    <div className="flex flex-col gap-3">
      <Card>
        <Table>
          <THead><TR className="hover:bg-transparent"><TH>{t("colUser")}</TH><TH>{t("colRole")}</TH><TH>{t("colWorkspaces")}</TH><TH>{t("colNote")}</TH></TR></THead>
          <TBody>
            {data.items.map((u) => (
              <TR key={u.id}>
                <TD><p className="text-[13px] font-medium">{u.name || u.email}</p>{u.name && <Mono className="text-muted-foreground">{u.email}</Mono>}</TD>
                <TD>{(() => { const role = u.platform_admin ? "admin" : (u.workspaces?.[0]?.role ?? "member"); return <Badge variant={roleTone[role] ?? "gray"}>{roleTone[role] ? t(`role.${role}`) : role}</Badge>; })()}</TD>
                <TD className="text-[13px]">{u.workspaces?.length ? u.workspaces.map((w) => `${w.name} (${w.role})`).join(", ") : "—"}</TD>
                <TD className="text-[13px] text-muted-foreground">{u.email_verified === false ? t("emailUnverified") : u.last_login_at ? t("lastLogin", { time: relTime(u.last_login_at) }) : "—"}</TD>
              </TR>
            ))}
          </TBody>
        </Table>
      </Card>
      <p className="text-xs text-muted-foreground">{t("usersNote")}</p>
    </div>
  );
}

const TABS = ["system", "jobs", "audit", "keys", "quotas", "features", "users"] as const;

function Inner() {
  const sp = useSearchParams();
  const router = useRouter();
  const tab = sp.get("s") ?? "system";
  const t = useTranslations("admin");
  return (
    <>
      <PageHeader title={t("title")} desc={t("desc")} actions={<Badge variant="outline"><Activity className="size-3" /> {t("scope")}</Badge>} />
      <Tabs value={tab} onValueChange={(v) => router.replace(`/admin?s=${v}`, { scroll: false })}>
        <TabsList>{TABS.map((v) => <TabsTrigger key={v} value={v}>{t(`tabs.${v}`)}</TabsTrigger>)}</TabsList>
        <TabsContent value="system"><SystemTab /></TabsContent>
        <TabsContent value="jobs"><JobsTab /></TabsContent>
        <TabsContent value="audit"><AuditTab /></TabsContent>
        <TabsContent value="keys"><KeysTab /></TabsContent>
        <TabsContent value="quotas"><QuotasTab /></TabsContent>
        <TabsContent value="features"><FeaturesTab /></TabsContent>
        <TabsContent value="users"><UsersTab /></TabsContent>
      </Tabs>
    </>
  );
}

export default function AdminPage() {
  return <React.Suspense fallback={<LoadingRows />}><Inner /></React.Suspense>;
}
