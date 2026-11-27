"use client";
// v1.2: allowlist egress của box — xem cách host thực thi (incus ACL / nftables) và sửa danh sách.
import * as React from "react";
import { useTranslations } from "next-intl";
import { Pencil, ShieldAlert, ShieldCheck, ShieldOff } from "lucide-react";
import { api, paths } from "@/lib/api";
import { useAction } from "@/lib/hooks";
import { useFmt } from "@/lib/format";
import type { Box } from "@/lib/types";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog";

function EgressEditor({ box }: { box: Box }) {
  const t = useTranslations("egress");
  const tc = useTranslations("common");
  const eg = box.egress;
  const [open, setOpen] = React.useState(false);
  const [text, setText] = React.useState((eg?.source === "box" ? eg.allow : []).join("\n"));
  const [deny, setDeny] = React.useState(eg?.source === "box" ? eg.deny_by_default : false);
  const { run, pending } = useAction();
  const entries = text.split(/[\s,]+/).map((x) => x.trim()).filter(Boolean);
  const editable = ["running", "sleeping"].includes(box.status);
  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button size="xs" variant="outline" disabled={!editable}><Pencil /> {t("edit")}</Button>
      </DialogTrigger>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t.rich("editTitle", { slug: box.slug, g: (c) => <span className="inline-block">{c}</span> })}</DialogTitle>
          <DialogDescription>{t("editDesc")}</DialogDescription>
        </DialogHeader>
        <Textarea rows={6} value={text} onChange={(e) => setText(e.target.value)} placeholder={"pypi.org\n*.github.com\napi.openai.com:443\n10.0.0.0/8"} className="font-mono text-[12.5px]" aria-label={t("entries")} />
        <label className="flex items-center justify-between gap-3 rounded-md border border-border px-3 py-2 text-[13px]">
          <span className="min-w-0">{t("denyDefault")}<span className="block text-xs text-muted-foreground">{t("denyDefaultHint")}</span></span>
          <Switch checked={deny || entries.length > 0} disabled={entries.length > 0} onCheckedChange={setDeny} />
        </label>
        <p className="text-xs text-muted-foreground">{t("emptyMeans")}</p>
        <DialogFooter>
          <Button variant="ghost" onClick={() => setOpen(false)}>{tc("cancel")}</Button>
          <Button disabled={!!pending} onClick={async () => {
            const r = await run("egress", () => api.setEgress(box.id, { allow: entries, deny_by_default: deny }), { success: t("saved"), revalidate: [paths.box(box.id), paths.boxEvents(box.id)] });
            if (r) setOpen(false);
          }}>{t("apply")}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

export function EgressCard({ box }: { box: Box }) {
  const t = useTranslations("egress");
  const f = useFmt();
  const eg = box.egress;
  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between gap-2 space-y-0">
        <CardTitle className="flex items-center gap-2">
          {!eg ? <ShieldOff className="size-4 text-muted-foreground" /> : eg.enforced ? <ShieldCheck className="size-4 text-st-running" /> : <ShieldAlert className="size-4 text-st-warning" />}
          {t("title")}
        </CardTitle>
        <EgressEditor key={JSON.stringify(eg)} box={box} />
      </CardHeader>
      <CardContent className="flex flex-col gap-2 text-[13px]">
        {!eg ? (
          <p className="text-muted-foreground">{t("unrestricted")}</p>
        ) : (
          <>
            <div className="flex flex-wrap items-center gap-1.5">
              {eg.enforced ? <Badge variant="green">{t("enforced", { mode: eg.mode })}</Badge> : <Badge variant="yellow">{t("notEnforced")}</Badge>}
              <Badge variant="outline">{t(`source.${eg.source}`)}</Badge>
              {eg.deny_by_default && <Badge variant="outline">{t("denyOthers")}</Badge>}
            </div>
            {eg.allow.length ? (
              <div className="flex flex-wrap gap-1">{eg.allow.map((a) => <Badge key={a} variant="default" className="font-mono">{a}</Badge>)}</div>
            ) : (
              <p className="text-st-warning">{t("offline")}</p>
            )}
            {eg.warning && <p className="rounded-md border border-st-warning/30 bg-st-warning/5 p-2 text-[12.5px] text-st-warning">{eg.warning}</p>}
            {eg.applied_at && <p className="text-xs text-muted-foreground">{t("applied", { time: f.relTime(eg.applied_at), rules: eg.rules })}</p>}
          </>
        )}
      </CardContent>
    </Card>
  );
}
