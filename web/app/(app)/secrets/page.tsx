"use client";
import * as React from "react";
import { EyeOff, Plus, RefreshCw, Trash2 } from "lucide-react";
import { useTranslations } from "next-intl";
import { api, paths } from "@/lib/api";
import { useAction, useApi } from "@/lib/hooks";
import type { Agent, List, Secret } from "@/lib/types";
import { useFmt } from "@/lib/format";
import { Card } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Field, Input, Textarea } from "@/components/ui/input";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog";
import { Table, TBody, TD, TH, THead, TR } from "@/components/ui/table";
import { ConfirmButton } from "@/components/boxmes/confirm";
import { Empty, ErrorBox, LoadingRows, PageHeader } from "@/components/boxmes/page";
import { Mono } from "@/components/boxmes/status";
import { Can } from "@/lib/session";
import { PERMS } from "@/lib/types";

function AddSecret({ agents }: { agents: Agent[] }) {
  const [open, setOpen] = React.useState(false);
  const [name, setName] = React.useState("");
  const [value, setValue] = React.useState("");
  const [sel, setSel] = React.useState<string[]>([]);
  const { run, pending } = useAction();
  const t = useTranslations("secrets");
  const tc = useTranslations("common");
  const reset = () => { setName(""); setValue(""); setSel([]); };
  return (
    <Dialog open={open} onOpenChange={(o) => { setOpen(o); if (!o) reset(); }}>
      <DialogTrigger asChild><Button size="sm"><Plus /> {t("add")}</Button></DialogTrigger>
      <DialogContent>
        <DialogHeader><DialogTitle>{t("add")}</DialogTitle><DialogDescription>{t("addDesc")}</DialogDescription></DialogHeader>
        <Field label={tc("name")} hint={t("nameHint")}><Input value={name} onChange={(e) => setName(e.target.value.toUpperCase().replace(/[^A-Z0-9_]/g, "_"))} placeholder="SHOPEE_COOKIE" className="font-mono" /></Field>
        <Field label={t("value")}><Textarea value={value} onChange={(e) => setValue(e.target.value)} className="font-mono" rows={3} autoComplete="off" spellCheck={false} /></Field>
        <div className="flex flex-col gap-1.5">
          <span className="text-[13px] font-medium">{t("agents")}</span>
          <div className="flex flex-wrap gap-1.5">
            {agents.map((a) => (
              <button key={a.id} type="button" onClick={() => setSel((x) => (x.includes(a.id) ? x.filter((y) => y !== a.id) : [...x, a.id]))}
                className={`rounded-full border px-2.5 py-1 font-mono text-[12px] ${sel.includes(a.id) ? "border-primary/60 bg-primary/15 text-primary" : "border-border text-muted-foreground"}`}>{a.name}</button>
            ))}
          </div>
        </div>
        <DialogFooter>
          <Button variant="ghost" onClick={() => setOpen(false)}>{tc("cancel")}</Button>
          <Button disabled={!name || !value || !!pending} onClick={async () => {
            const r = await run("c", () => api.createSecret({ name, value, agents: sel }), { success: t("savedToast", { name }), revalidate: [paths.secrets] });
            if (r) { setOpen(false); reset(); }
          }}>{tc("save")}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function RotateSecret({ s }: { s: Secret }) {
  const [open, setOpen] = React.useState(false);
  const [value, setValue] = React.useState("");
  const { run, pending } = useAction();
  const t = useTranslations("secrets");
  const tc = useTranslations("common");
  return (
    <Dialog open={open} onOpenChange={(o) => { setOpen(o); if (!o) setValue(""); }}>
      <DialogTrigger asChild><Button size="xs" variant="outline"><RefreshCw /> {t("rotate")}</Button></DialogTrigger>
      <DialogContent>
        <DialogHeader><DialogTitle className="break-all">{t.rich("rotateTitle", { name: s.name, mono: (c) => <span className="font-mono">{c}</span> })}</DialogTitle><DialogDescription>{t("rotateDesc")}</DialogDescription></DialogHeader>
        <Field label={t("newValue")}><Textarea value={value} onChange={(e) => setValue(e.target.value)} className="font-mono" rows={3} autoComplete="off" spellCheck={false} /></Field>
        <DialogFooter>
          <Button variant="ghost" onClick={() => setOpen(false)}>{tc("cancel")}</Button>
          <Button disabled={!value || !!pending} onClick={async () => { const r = await run("r", () => api.rotateSecret(s.name, value), { success: t("rotated"), revalidate: [paths.secrets] }); if (r) setOpen(false); }}>{t("saveNew")}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

export default function SecretsPage() {
  const { data, error, mutate } = useApi<List<Secret>>(paths.secrets);
  const { data: agents } = useApi<List<Agent>>(paths.agents);
  const { run } = useAction();
  const t = useTranslations("secrets");
  const tc = useTranslations("common");
  const { dateTime, relTime } = useFmt();
  const name = (id: string) => agents?.items.find((a) => a.id === id)?.name ?? id;
  return (
    <>
      <PageHeader title={t("title")} desc={t("desc")} actions={<Can perm={PERMS.secretsWrite}><AddSecret agents={agents?.items ?? []} /></Can>} />
      <p className="mb-4 flex items-center gap-1.5 text-[13px] text-muted-foreground"><EyeOff className="size-4 shrink-0" /> <span>{t.rich("hint", { mono: (c) => <span className="font-mono">{c}</span> })}</span></p>
      {error ? <ErrorBox error={error} retry={() => mutate()} /> : !data ? <LoadingRows /> : !data.items.length ? <Empty title={t("empty")} /> : (
        <Card>
          <Table>
            <THead><TR className="hover:bg-transparent"><TH>{tc("name")}</TH><TH>{t("agents")}</TH><TH className="hidden md:table-cell">{tc("createdAt")}</TH><TH>{t("colRotated")}</TH><TH className="text-right">{tc("actions")}</TH></TR></THead>
            <TBody>
              {data.items.map((s) => (
                <TR key={s.name}>
                  <TD><Mono>{s.name}</Mono><div className="font-mono text-[11px] text-muted-foreground">••••••••</div></TD>
                  <TD><div className="flex flex-wrap gap-1">{s.agents.length ? s.agents.map((a) => <Badge key={a} variant="outline" className="font-mono">{name(a)}</Badge>) : <span className="text-xs text-muted-foreground">{t("unassigned")}</span>}</div></TD>
                  <TD className="hidden whitespace-nowrap text-[13px] text-muted-foreground md:table-cell">{dateTime(s.created_at)}</TD>
                  <TD className="whitespace-nowrap text-[13px] text-muted-foreground">{s.rotated_at ? relTime(s.rotated_at) : t("never")}</TD>
                  <TD>
                    <div className="flex justify-end gap-1">
                      <Can perm={PERMS.secretsWrite}>
                      <RotateSecret s={s} />
                      <ConfirmButton size="xs" variant="outline" destructive title={t("deleteTitle", { name: s.name })} desc={t("deleteDesc")} confirmLabel={tc("delete")} typeToConfirm={s.name} aria-label={tc("delete")}
                        onConfirm={() => run("d", () => api.deleteSecret(s.name), { success: tc("deleted"), revalidate: [paths.secrets] })}>
                        <Trash2 />
                      </ConfirmButton>
                      </Can>
                    </div>
                  </TD>
                </TR>
              ))}
            </TBody>
          </Table>
        </Card>
      )}
    </>
  );
}
