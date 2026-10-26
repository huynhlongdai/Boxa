"use client";
// v1.3 trên trang box: nút clone (copy-on-write), thẻ ước tính (giá/ETA lúc tạo so với thực tế), tóm tắt resume.
import * as React from "react";
import { useRouter } from "next/navigation";
import { useTranslations } from "next-intl";
import { Copy, History, Timer } from "lucide-react";
import { api, paths } from "@/lib/api";
import { startsWith, useAction, useApi } from "@/lib/hooks";
import { useFmt } from "@/lib/format";
import type { Box, ResumeInfo } from "@/lib/types";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog";
import { KindBadge, useProfileText } from "./recommend";
import { Mono } from "./status";

export function CloneButton({ box }: { box: Box }) {
  const t = useTranslations("clone");
  const tc = useTranslations("common");
  const te = useTranslations("estimate");
  const { usd, num } = useFmt();
  const [open, setOpen] = React.useState(false);
  const [slug, setSlug] = React.useState("");
  const { run, pending } = useAction();
  const router = useRouter();
  const can = ["running", "sleeping"].includes(box.status);
  return (
    <Dialog open={open} onOpenChange={(o) => { setOpen(o); if (o) setSlug(`${box.slug}-clone`); }}>
      <DialogTrigger asChild>
        <Button size="sm" variant="ghost" disabled={!can} title={can ? t("hint") : t("onlyLive")}><Copy />{t("button")}</Button>
      </DialogTrigger>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>{t("title", { slug: box.slug })}</DialogTitle>
          <DialogDescription>{t("desc")}</DialogDescription>
        </DialogHeader>
        <Input aria-label={t("slug")} value={slug} onChange={(e) => setSlug(e.target.value.toLowerCase().replace(/[^a-z0-9-]/g, "-"))} className="font-mono" />
        <DialogFooter>
          <Button variant="ghost" onClick={() => setOpen(false)}>{tc("cancel")}</Button>
          <Button disabled={!!pending || !slug} onClick={async () => {
            const b = await run("clone", () => api.cloneBox(box.id, slug), {
              success: (x) => (x.estimate ? `${t("done", { slug: x.slug })} · ${te("toast", { eta: num(x.estimate.eta_seconds, 1), hour: usd(x.estimate.cost_per_hour), total: usd(x.estimate.expected_total_usd) })}` : t("done", { slug: x.slug })),
              revalidate: [startsWith("/boxes"), "/overview"],
            });
            if (b) { setOpen(false); router.push(`/boxes/${b.id}`); }
          }}><Copy /> {pending ? tc("processing") : t("confirm")}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

export function EstimateCard({ box }: { box: Box }) {
  const t = useTranslations("estimate");
  const { usd, num } = useFmt();
  const ptext = useProfileText();
  const e = box.estimate;
  if (!e && !box.profile && !box.cloned_from) return null;
  return (
    <Card data-estimate>
      <CardHeader><CardTitle className="flex items-center gap-2"><Timer className="size-4 text-primary" /> {t("title")}</CardTitle></CardHeader>
      <CardContent className="flex flex-col gap-2 text-[13px]">
        {e && (
          <>
            <div className="flex flex-wrap items-center gap-1.5">
              <KindBadge kind={e.kind} />
              {e.warm_hit && <Badge variant="green">{t("warmHit")}</Badge>}
              {typeof e.score === "number" && <Badge variant="outline">{t("score", { v: num(e.score, 1) })}</Badge>}
            </div>
            <div className="grid grid-cols-2 gap-2">
              <div><p className="text-[11px] text-muted-foreground">{t("eta")}</p><p className="font-mono">~{num(e.eta_seconds, 1)} s</p></div>
              <div><p className="text-[11px] text-muted-foreground">{t("actual")}</p><p className="font-mono">{e.actual_seconds ? `${num(e.actual_seconds, 1)} s` : t("measuring")}</p></div>
              <div><p className="text-[11px] text-muted-foreground">{t("perHour")}</p><p className="font-mono">{usd(e.cost_per_hour)}</p></div>
              <div><p className="text-[11px] text-muted-foreground">{t("expected", { hours: num(e.expected_hours, 1) })}</p><p className="font-mono">{usd(e.expected_total_usd)}</p></div>
            </div>
          </>
        )}
        {box.profile && <p className="text-muted-foreground">{t("profile")} <span className="text-foreground">{ptext(box.profile).name}</span></p>}
        {box.cloned_from && <p className="text-muted-foreground">{t("clonedFrom")} <Mono>{box.cloned_from}</Mono></p>}
      </CardContent>
    </Card>
  );
}

function ResumeBody({ r }: { r: ResumeInfo }) {
  const t = useTranslations("resume");
  const { relTime, seconds } = useFmt();
  return (
    <div className="flex flex-col gap-2 text-[13px]">
      <p className="text-muted-foreground">{r.woke ? t("woke") : t("idle", { time: seconds(r.idle_seconds) })}</p>
      {r.recent_execs.length > 0 && (
        <div>
          <p className="mb-0.5 text-[11.5px] text-muted-foreground">{t("recent")}</p>
          <ul className="flex flex-col gap-0.5">
            {r.recent_execs.slice(0, 4).map((x, i) => (
              <li key={i} className="flex min-w-0 items-center gap-2">
                <Badge variant={x.exit_code === 0 ? "green" : x.exit_code == null ? "gray" : "red"} className="font-mono">{x.exit_code ?? "…"}</Badge>
                <Mono className="min-w-0 flex-1 truncate">{x.command}</Mono>
                <span className="shrink-0 text-[11px] text-muted-foreground">{relTime(x.ts)}</span>
              </li>
            ))}
          </ul>
        </div>
      )}
      <p className="text-muted-foreground">{t("jobs", { n: r.running_jobs.length })} · {t("ports")} <Mono>{r.listening_ports.length ? r.listening_ports.join(", ") : "—"}</Mono></p>
      {r.last_snapshot && <p className="text-muted-foreground">{t("snapshot")} <Mono>{r.last_snapshot.id}</Mono> · {relTime(r.last_snapshot.created_at)}</p>}
      {r.files_changed.length > 0 && <p className="min-w-0 truncate text-muted-foreground">{t("files")} <Mono>{r.files_changed.slice(0, 4).join(", ")}</Mono></p>}
      {r.next.length > 0 && (
        <div>
          <p className="mb-0.5 text-[11.5px] text-muted-foreground">{t("next")}</p>
          {r.next.slice(0, 3).map((n) => <Mono key={n} className="block break-all text-[12px]">{n}</Mono>)}
        </div>
      )}
    </div>
  );
}

/** Tóm tắt resume. GET /resume đánh thức box đang ngủ, nên với box ngủ chỉ gọi khi người dùng bấm. */
export function ResumeCard({ box }: { box: Box }) {
  const t = useTranslations("resume");
  const live = box.status === "running";
  const { data } = useApi<ResumeInfo>(live ? paths.boxResume(box.id) : null, { refreshInterval: 30000 });
  const [manual, setManual] = React.useState<ResumeInfo | null>(null);
  const { run, pending } = useAction();
  if (!live && box.status !== "sleeping") return null;
  const r = manual ?? data;
  return (
    <Card data-resume>
      <CardHeader><CardTitle className="flex items-center gap-2"><History className="size-4 text-primary" /> {t("title")}</CardTitle></CardHeader>
      <CardContent>
        {r ? <ResumeBody r={r} /> : live ? <p className="text-[13px] text-muted-foreground">…</p> : (
          <div className="flex flex-col items-start gap-2 text-[13px]">
            <p className="text-muted-foreground">{t("sleeping")}</p>
            <Button size="sm" variant="outline" disabled={!!pending} onClick={async () => {
              const x = await run("res", () => api.resumeBox(box.id), { success: t("resumed"), revalidate: [startsWith("/boxes"), "/overview"] });
              if (x) setManual(x);
            }}><History /> {t("wakeResume")}</Button>
          </div>
        )}
      </CardContent>
    </Card>
  );
}
