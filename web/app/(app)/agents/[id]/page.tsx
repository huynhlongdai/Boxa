"use client";
import * as React from "react";
import Link from "next/link";
import { useParams } from "next/navigation";
import { AlertTriangle, ArrowLeft, Cable, KeyRound, Plus, Save } from "lucide-react";
import { useTranslations } from "next-intl";
import { api, paths } from "@/lib/api";
import { useAction, useApi } from "@/lib/hooks";
import type { Agent, ApiKey, Box, List } from "@/lib/types";
import { useFmt } from "@/lib/format";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Field, Input, Textarea } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { Table, TBody, TD, TH, THead, TR } from "@/components/ui/table";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog";
import { BudgetBar, MoneyBudget } from "@/components/boxmes/budget";
import { CopyButton } from "@/components/boxmes/code-block";
import { ConfirmButton } from "@/components/boxmes/confirm";
import { ErrorBox, Empty } from "@/components/boxmes/page";
import { BoxStatusChip, Mono } from "@/components/boxmes/status";
import { SnippetViewer, useAgentKinds } from "@/components/boxmes/snippet-viewer";

function NewKeyDialog({ agent }: { agent: Agent }) {
  const [open, setOpen] = React.useState(false);
  const [name, setName] = React.useState("");
  const [created, setCreated] = React.useState<ApiKey | null>(null);
  const { run, pending } = useAction();
  const t = useTranslations("agentDetail");
  const tc = useTranslations("common");
  return (
    <Dialog open={open} onOpenChange={(o) => { setOpen(o); if (!o) { setCreated(null); setName(""); } }}>
      <DialogTrigger asChild><Button size="sm" variant="secondary"><Plus /> {t("newKey")}</Button></DialogTrigger>
      <DialogContent>
        <DialogHeader>
          <DialogTitle className="break-words">{t("newKeyTitle", { name: agent.name })}</DialogTitle>
          <DialogDescription>{t("newKeyDesc")}</DialogDescription>
        </DialogHeader>
        {!created ? (
          <>
            <Field label={t("keyName")}><Input value={name} onChange={(e) => setName(e.target.value)} placeholder={t("keyNamePh")} /></Field>
            <DialogFooter>
              <Button disabled={!name || !!pending} onClick={async () => {
                const k = await run("k", () => api.createKey({ name, agent_id: agent.id, scopes: ["boxes:write", "exec", "files", "snapshots"] }), { revalidate: [paths.keys] });
                if (k) setCreated(k);
              }}>{t("createKey")}</Button>
            </DialogFooter>
          </>
        ) : (
          <div className="rounded-lg border border-st-warning/40 bg-st-warning/5 p-3">
            <p className="flex items-center gap-1.5 text-[13px] font-medium text-st-warning"><AlertTriangle className="size-4 shrink-0" /> {t("onceCopy")}</p>
            <div className="mt-2 flex items-center gap-2">
              <code className="min-w-0 flex-1 truncate rounded border border-border bg-background px-2 py-1.5 font-mono text-[13px]">{created.key}</code>
              <CopyButton text={created.key ?? ""} label={tc("copyKey")} />
            </div>
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}

function PolicyForm({ agent }: { agent: Agent }) {
  const [day, setDay] = React.useState(String(agent.budget_day_usd));
  const [month, setMonth] = React.useState(String(agent.budget_month_usd));
  const [providers, setProviders] = React.useState(agent.policy.providers.join(", "));
  const [regions, setRegions] = React.useState(agent.policy.regions.join(", "));
  const [egress, setEgress] = React.useState((agent.policy.egress_allowlist ?? []).join("\n"));
  const { run, pending } = useAction();
  const t = useTranslations("agentDetail");
  const tc = useTranslations("common");
  const split = (s: string) => s.split(/[\s,]+/).filter(Boolean);
  return (
    <div className="grid gap-4 lg:grid-cols-2">
      <Card>
        <CardHeader><CardTitle>{t("budget")}</CardTitle></CardHeader>
        <CardContent className="flex flex-col gap-4">
          <div>
            <div className="mb-1 flex justify-between text-[13px]"><span className="text-muted-foreground">{t("todayShort")}</span><MoneyBudget spent={agent.spent_today_usd} budget={agent.budget_day_usd} /></div>
            <BudgetBar spent={agent.spent_today_usd} budget={agent.budget_day_usd} />
          </div>
          <div>
            <div className="mb-1 flex justify-between text-[13px]"><span className="text-muted-foreground">{t("monthShort")}</span><MoneyBudget spent={agent.spent_month_usd} budget={agent.budget_month_usd} /></div>
            <BudgetBar spent={agent.spent_month_usd} budget={agent.budget_month_usd} />
          </div>
          <div className="grid grid-cols-2 gap-3">
            <Field label={t("dayUsd")}><Input type="number" step="0.05" value={day} onChange={(e) => setDay(e.target.value)} className="font-mono" /></Field>
            <Field label={t("monthUsd")}><Input type="number" step="1" value={month} onChange={(e) => setMonth(e.target.value)} className="font-mono" /></Field>
          </div>
        </CardContent>
      </Card>
      <Card>
        <CardHeader><CardTitle>{t("policy")}</CardTitle></CardHeader>
        <CardContent className="flex flex-col gap-3">
          <Field label={t("providers")} hint={t("providersHint")}><Input value={providers} onChange={(e) => setProviders(e.target.value)} className="font-mono" /></Field>
          <Field label={t("regions")} hint={t("regionsHint")}><Input value={regions} onChange={(e) => setRegions(e.target.value)} className="font-mono" /></Field>
          <Field label={t("egress")} hint={t("egressHint")}><Textarea rows={3} value={egress} onChange={(e) => setEgress(e.target.value)} className="font-mono" /></Field>
        </CardContent>
      </Card>
      <div className="flex justify-end lg:col-span-2">
        <Button disabled={!!pending} onClick={() => run("save", () => api.updateAgent(agent.id, {
          budget_day_usd: Number(day),
          budget_month_usd: Number(month),
          policy: { providers: split(providers), regions: split(regions), egress_allowlist: egress.trim() ? split(egress) : null },
        }), { success: t("savedPolicy"), revalidate: [paths.agent(agent.id), paths.agents] })}>
          <Save /> {tc("saveChanges")}
        </Button>
      </div>
    </div>
  );
}

export default function AgentDetailPage() {
  const { id } = useParams<{ id: string }>();
  const { data: agent, error, mutate } = useApi<Agent>(paths.agent(id));
  const { data: keys } = useApi<List<ApiKey>>(paths.keys);
  const { data: boxes } = useApi<List<Box>>(paths.boxes({ agent: id }));
  const { run } = useAction();
  const t = useTranslations("agentDetail");
  const tc = useTranslations("common");
  const { dateTime, mb, relTime } = useFmt();
  const { kinds } = useAgentKinds();
  if (error) return <ErrorBox error={error} retry={() => mutate()} />;
  if (!agent) return <Skeleton className="h-64" />;
  const myKeys = (keys?.items ?? []).filter((k) => k.agent_id === agent.id);

  return (
    <>
      <Link href="/agents" className="mb-3 inline-flex items-center gap-1 text-[13px] text-muted-foreground hover:text-foreground"><ArrowLeft className="size-4" /> {t("back")}</Link>
      <div className="mb-5 flex flex-wrap items-center gap-2">
        <h1 className="break-all font-mono text-xl font-semibold">{agent.name}</h1>
        <Badge variant="outline" className="font-mono">{agent.kind}</Badge>
        <span className="text-[13px] text-muted-foreground">{t("meta", { created: relTime(agent.created_at), last: relTime(agent.last_call_at) })}</span>
      </div>

      <div className="mb-4 grid grid-cols-2 gap-3 lg:grid-cols-4">
        <Card className="min-w-0 p-4"><p className="text-xs text-muted-foreground">{t("boxes")}</p><p className="mt-1 text-2xl font-semibold">{agent.box_count}</p></Card>
        <Card className="min-w-0 p-4"><p className="text-xs text-muted-foreground">{t("memory")}</p><p className="mt-1 font-mono text-2xl font-semibold">{mb(agent.memory_mb)}</p></Card>
        <Card className="min-w-0 p-4"><p className="text-xs text-muted-foreground">{t("today")}</p><MoneyBudget spent={agent.spent_today_usd} budget={agent.budget_day_usd} className="mt-1 block text-lg" /></Card>
        <Card className="min-w-0 p-4"><p className="text-xs text-muted-foreground">{t("month")}</p><MoneyBudget spent={agent.spent_month_usd} budget={agent.budget_month_usd} className="mt-1 block text-lg" /></Card>
      </div>

      <PolicyForm key={agent.id + agent.budget_day_usd} agent={agent} />

      <Card className="mt-4">
        <CardHeader className="flex-row items-center justify-between space-y-0">
          <CardTitle className="flex items-center gap-2"><KeyRound className="size-4" /> {t("keys")}</CardTitle>
          <NewKeyDialog agent={agent} />
        </CardHeader>
        <CardContent>
          {!myKeys.length ? <Empty title={t("noKeys")} /> : (
            <Table>
              <THead><TR className="hover:bg-transparent"><TH>{tc("name")}</TH><TH>{t("prefix")}</TH><TH className="hidden sm:table-cell">{t("scopes")}</TH><TH>{t("lastUsed")}</TH><TH className="text-right">{tc("status")}</TH></TR></THead>
              <TBody>
                {myKeys.map((k) => (
                  <TR key={k.id}>
                    <TD className="text-[13px]">{k.name}</TD>
                    <TD><Mono>{k.prefix}…</Mono></TD>
                    <TD className="hidden sm:table-cell"><div className="flex flex-wrap gap-1">{k.scopes.map((s) => <Badge key={s} variant="outline" className="font-mono">{s}</Badge>)}</div></TD>
                    <TD className="text-[13px] text-muted-foreground">{relTime(k.last_used_at)}</TD>
                    <TD className="text-right">
                      {k.revoked_at ? <Badge variant="gray">{t("revokedAgo", { time: relTime(k.revoked_at) })}</Badge> : (
                        <ConfirmButton size="xs" variant="outline" destructive title={t("revokeTitle", { prefix: k.prefix })} desc={t("revokeDesc")} confirmLabel={t("revoke")}
                          onConfirm={() => run("rv", () => api.revokeKey(k.id), { success: t("revoked"), revalidate: [paths.keys] })}>
                          {t("revoke")}
                        </ConfirmButton>
                      )}
                    </TD>
                  </TR>
                ))}
              </TBody>
            </Table>
          )}
        </CardContent>
      </Card>

      <Card className="mt-4">
        <CardHeader>
          <CardTitle className="flex items-center gap-2"><Cable className="size-4" /> {t("connect")}</CardTitle>
          <p className="text-[13px] text-muted-foreground">{t("connectDesc")}</p>
        </CardHeader>
        <CardContent><SnippetViewer agentId={agent.id} agentName={agent.name} kind={agent.kind} kinds={kinds} /></CardContent>
      </Card>

      <Card className="mt-4">
        <CardHeader><CardTitle>{t("history")}</CardTitle></CardHeader>
        <CardContent>
          {!boxes?.items.length ? <Empty title={t("noBoxes")} /> : (
            <ul className="divide-y divide-border">
              {boxes.items.map((b) => (
                <li key={b.id} className="flex flex-wrap items-center gap-2 py-2.5">
                  <Link href={`/boxes/${b.id}`} className="font-mono text-[13px] hover:text-primary">{b.slug}</Link>
                  <BoxStatusChip status={b.status} />
                  <Mono className="text-muted-foreground">{b.provider}/{b.region}</Mono>
                  <span className="ml-auto flex items-center gap-3 text-xs text-muted-foreground">
                    <span>{dateTime(b.created_at)}</span>
                    <MoneyBudget spent={b.spent_usd} budget={b.budget_usd} />
                  </span>
                </li>
              ))}
            </ul>
          )}
        </CardContent>
      </Card>
    </>
  );
}
