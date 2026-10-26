"use client";
import * as React from "react";
import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { useTranslations } from "next-intl";
import { ChevronRight, Download, FileText, Folder, GitFork, History, Save, Sparkles, Terminal as TermIcon, Zap } from "lucide-react";
import { api, paths } from "@/lib/api";
import { startsWith, useAction, useApi } from "@/lib/hooks";
import { useFmt } from "@/lib/format";
import { boxToTemplateYaml, boxToYaml } from "@/lib/box-yaml";
import type { Box, BoxaEvent, FilesResponse, List, Observe, Snapshot } from "@/lib/types";
import { parseExecEvent } from "@/lib/exec-event";
import { cn } from "@/lib/utils";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Progress } from "@/components/ui/progress";
import { Field, Input } from "@/components/ui/input";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog";
import { Table, TBody, TD, TH, THead, TR } from "@/components/ui/table";
import { BudgetBar, MoneyBudget } from "@/components/boxa/budget";
import { CodeBlock } from "@/components/boxa/code-block";
import { ConfirmButton } from "@/components/boxa/confirm";
import { Empty, ErrorBox, LoadingRows } from "@/components/boxa/page";
import { Mono } from "@/components/boxa/status";
import { EgressCard } from "@/components/boxa/egress";
import { EstimateCard, ResumeCard } from "@/components/boxa/box-v13";
import { RightsizeCard } from "@/components/boxa/speed";

function Row({ k, children }: { k: string; children: React.ReactNode }) {
  return (
    <div className="flex items-start justify-between gap-4 border-b border-border/60 py-2 text-[13px] last:border-0">
      <span className="shrink-0 text-muted-foreground">{k}</span>
      <span className="min-w-0 break-words text-right">{children}</span>
    </div>
  );
}

function download(name: string, content: string, type = "text/plain;charset=utf-8") {
  const url = URL.createObjectURL(new Blob([content], { type }));
  const a = document.createElement("a");
  a.href = url;
  a.download = name;
  a.click();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}

const monoTag = (c: React.ReactNode) => <span className="font-mono">{c}</span>;

// ---------------- Tổng quan ----------------
export function OverviewTab({ box }: { box: Box }) {
  const t = useTranslations("boxDetail");
  const tc = useTranslations("common");
  const f = useFmt();
  const { data: obs } = useApi<Observe>(box.status === "running" ? paths.boxObserve(box.id) : null, { refreshInterval: 10000 });
  const remaining = box.budget_usd ? Math.max(0, box.budget_usd - box.spent_usd) : null;
  return (
    <div className="grid grid-cols-1 items-start gap-4 lg:grid-cols-3">
      <div className="flex min-w-0 flex-col gap-4 lg:col-span-2">
      <Card className="min-w-0">
        <CardHeader><CardTitle>{t("specs")}</CardTitle></CardHeader>
        <CardContent>
          <Row k={t("spec")}><Mono>{t("specValue", { cpu: box.spec.cpu, ram: f.mb(box.spec.ram_mb), disk: box.spec.disk_gb })}</Mono></Row>
          <Row k={t("template")}><Mono>{box.template}</Mono></Row>
          <Row k={t("host")}><Mono>{box.host_id ?? "—"}</Mono> <span className="text-muted-foreground">({box.provider}/{box.region})</span></Row>
          <Row k={t("url")}>{box.url ? <a className="break-all text-primary hover:underline" href={box.url} target="_blank" rel="noreferrer"><Mono>{box.url}</Mono></a> : <span className="text-muted-foreground">{t("urlOnlyRunning")}</span>}</Row>
          <Row k={t("ports")}><Mono>{box.ports.length ? box.ports.join(", ") : "—"}</Mono></Row>
          <Row k={t("secrets")}>{box.secrets.length ? <span className="flex flex-wrap justify-end gap-1">{box.secrets.map((s) => <Badge key={s} variant="outline" className="font-mono">{s}</Badge>)}</span> : "—"}</Row>
          <Row k={t("ttlIdle")}>{box.ttl_seconds ? f.seconds(box.ttl_seconds) : t("ttlNone")} · {box.idle_minutes ? t("idleValue", { time: f.unit(box.idle_minutes, "minute") }) : t("idleNone")}</Row>
          <Row k={tc("createdAt")}>{t("createdActive", { created: f.dateTime(box.created_at), active: f.relTime(box.last_active_at) })}</Row>
          {box.error && (
            <div className="mt-3 rounded-md border border-st-error/30 bg-st-error/5 p-3 text-[13px]">
              <p className="font-medium text-st-error">{box.retries ? t("errorRetries", { n: box.retries }) : t("error")}</p>
              <p className="mt-0.5 break-words font-mono text-[12.5px]">{box.error}</p>
            </div>
          )}
          <div className="mt-3 rounded-md border border-primary/25 bg-primary/5 p-3">
            <p className="flex items-center gap-1.5 text-[12px] font-medium text-primary"><Sparkles className="size-3.5" /> {t("whyHere")}</p>
            <p className="mt-1 text-[13px]">{box.placement_reason || "—"}</p>
          </div>
        </CardContent>
      </Card>
      <div className="grid grid-cols-1 items-start gap-4 md:grid-cols-2 [&>*]:min-w-0">
        <EstimateCard box={box} />
        <ResumeCard box={box} />
      </div>
      </div>
      <div className="flex min-w-0 flex-col gap-4">
        <RightsizeCard boxId={box.id} enabled={["running", "sleeping"].includes(box.status)} />
        <EgressCard box={box} />
        <Card>
          <CardHeader><CardTitle>{t("cost")}</CardTitle></CardHeader>
          <CardContent className="flex flex-col gap-3">
            <div>
              <p className="text-xs text-muted-foreground">{t("spentBox")}</p>
              <MoneyBudget spent={box.spent_usd} budget={box.budget_usd} className="text-lg" />
              <BudgetBar spent={box.spent_usd} budget={box.budget_usd} className="mt-2" />
            </div>
            <Row k={t("hourly")}><Mono>{f.usd(box.cost_per_hour)}</Mono></Row>
            <Row k={t("remaining")}><Mono>{remaining === null ? "∞" : f.usd(remaining)}</Mono></Row>
            {remaining !== null && box.cost_per_hour > 0 && <Row k={t("runway")}><Mono>{t("runwayValue", { time: f.seconds((remaining / box.cost_per_hour) * 3600) })}</Mono></Row>}
          </CardContent>
        </Card>
        <Card>
          <CardHeader><CardTitle>{t("inside")}</CardTitle></CardHeader>
          <CardContent className="flex flex-col gap-3 text-[13px]">
            {obs ? (
              <>
                {([[t("cpu"), obs.cpu_pct], [t("ram"), obs.ram_pct], [t("disk"), obs.disk_pct]] as const).filter(([, v]) => typeof v === "number").map(([k, v]) => (
                  <div key={k}>
                    <div className="mb-1 flex justify-between"><span className="text-muted-foreground">{k}</span><Mono>{v}%</Mono></div>
                    <Progress value={v as number} warnAt={75} dangerAt={92} />
                  </div>
                ))}
                <div>
                  <p className="mb-1 text-xs text-muted-foreground">{t("processes")}</p>
                  {obs.processes.length ? obs.processes.map((p) => (
                    <div key={p.pid} className="flex justify-between gap-2 py-0.5"><Mono className="truncate">{p.cmd}</Mono><Mono className="text-muted-foreground">{p.pid}</Mono></div>
                  )) : <p className="text-muted-foreground">—</p>}
                </div>
                <Row k={t("listening")}><Mono>{obs.ports?.length ? obs.ports.join(", ") : "—"}</Mono></Row>
                {obs.files_changed?.length > 0 && (
                  <div>
                    <p className="mb-1 text-xs text-muted-foreground">{t("changedSince", { time: f.dateTime(obs.since) })}</p>
                    {obs.files_changed.slice(0, 8).map((fc) => <Mono key={fc} className="block truncate">{fc}</Mono>)}
                  </div>
                )}
              </>
            ) : (
              <p className="text-muted-foreground">{box.status === "running" ? t("reading") : t("notRunning")}</p>
            )}
          </CardContent>
        </Card>
      </div>
    </div>
  );
}

// ---------------- Timeline ----------------
export function TimelineTab({ box }: { box: Box }) {
  const t = useTranslations("boxDetail.timeline");
  const f = useFmt();
  const { data: ev, error } = useApi<List<BoxaEvent>>(paths.boxEvents(box.id));
  if (error) return <ErrorBox error={error} />;
  if (!ev) return <LoadingRows />;
  const items = [...ev.items].sort((a, b) => Date.parse(b.ts) - Date.parse(a.ts)).map((e) => ({ e, x: parseExecEvent(e) }));
  const execs = items.filter((i) => i.x);
  const saved = execs.reduce((a, i) => a + (i.x?.tokens_saved ?? 0), 0);
  const hasTokens = execs.some((i) => i.x?.tokens_saved !== null);
  if (!items.length) return <Empty title={t("empty")} />;
  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap gap-2 text-[13px]">
        <Badge variant="outline">{t("commands", { n: execs.length })}</Badge>
        <Badge variant="red">{t("errors", { n: execs.filter((i) => i.x!.exit_code !== 0 || i.x!.timed_out).length })}</Badge>
        {hasTokens && <Badge variant="green" title={t("tokensTitle")}><Zap className="size-3" /> {t("savedTokens", { n: f.compact(saved) })}</Badge>}
      </div>
      <ol className="relative flex flex-col gap-2 border-l border-border pl-4">
        {items.map(({ e, x }) =>
          x ? (
            <li key={e.id} className="relative">
              <span className={cn("absolute -left-[21px] top-3 size-2.5 rounded-full border-2 border-background", x.exit_code === 0 && !x.timed_out ? "bg-st-running" : "bg-st-error")} />
              <div className="rounded-md border border-border bg-card p-3">
                <div className="flex flex-wrap items-center gap-2">
                  <TermIcon className="size-3.5 shrink-0 text-muted-foreground" />
                  <Mono className="min-w-0 flex-1 basis-60 truncate">{x.command}</Mono>
                  {x.timed_out && <Badge variant="yellow">TIMEOUT</Badge>}
                  <Badge variant={x.exit_code === 0 ? "green" : "red"} className="font-mono">exit {x.exit_code}</Badge>
                  <span className="font-mono text-xs text-muted-foreground">{f.ms(x.duration_ms)}</span>
                  {x.tokens_saved ? <span className="font-mono text-xs text-st-running" title={t("tokensTitle")}>{t("tokensShort", { n: f.compact(x.tokens_saved) })}</span> : null}
                  {x.log_id && (
                    <Link href={`/boxes/${box.id}?tab=logs&log=${encodeURIComponent(x.log_id)}`} scroll={false} className="font-mono text-xs text-primary hover:underline" title={t("openLog", { id: x.log_id })}>
                      {t("log")} · {x.log_id}
                    </Link>
                  )}
                  <span className="text-xs text-muted-foreground">{e.actor} · {f.dateTime(e.ts)}</span>
                </div>
              </div>
            </li>
          ) : (
            <li key={e.id} className="relative py-1">
              <span className={cn("absolute -left-[20px] top-2.5 size-2 rounded-full", /fail|down|error/.test(e.type) ? "bg-st-error" : "bg-muted-foreground/60")} />
              <div className="flex flex-wrap items-baseline gap-x-2 text-[13px]">
                <span className="font-mono text-[11.5px] text-muted-foreground">{e.type}</span>
                <span className="min-w-0 break-words">{e.message}</span>
                {e.cost_usd !== null && <Mono className="text-muted-foreground">{f.usd(e.cost_usd)}</Mono>}
                <span className="text-xs text-muted-foreground">· {e.actor} · {f.dateTime(e.ts)}</span>
              </div>
            </li>
          ),
        )}
      </ol>
    </div>
  );
}

// ---------------- Files ----------------
export function FilesTab({ box }: { box: Box }) {
  const t = useTranslations("boxDetail.files");
  const tc = useTranslations("common");
  const f = useFmt();
  const [root, setRoot] = React.useState<"/work" | "/memory">("/work");
  const [dir, setDir] = React.useState("/work");
  const [file, setFile] = React.useState<string | null>(null);
  const { data, error, isLoading } = useApi<FilesResponse>(paths.boxFiles(box.id, dir));
  const { data: fileData } = useApi<FilesResponse>(file ? paths.boxFiles(box.id, file) : null);
  const crumbs = dir.split("/").filter(Boolean);
  const go = (p: string) => { setDir(p); setFile(null); };

  return (
    <div className="grid gap-4 lg:grid-cols-5">
      <Card className="min-w-0 lg:col-span-2">
        <div className="flex gap-1 border-b border-border p-2">
          {(["/work", "/memory"] as const).map((r) => (
            <Button key={r} size="xs" variant={root === r ? "secondary" : "ghost"} className="font-mono" onClick={() => { setRoot(r); go(r); }}>{r}</Button>
          ))}
        </div>
        <div className="flex flex-wrap items-center gap-0.5 px-3 pt-2 font-mono text-[12px] text-muted-foreground">
          {crumbs.map((c, i) => {
            const p = "/" + crumbs.slice(0, i + 1).join("/");
            return (
              <React.Fragment key={p}>
                {i > 0 && <ChevronRight className="size-3" />}
                <button className="hover:text-foreground" onClick={() => go(p)}>{c}</button>
              </React.Fragment>
            );
          })}
        </div>
        <div className="p-2">
          {error ? <ErrorBox error={error} /> : isLoading || !data ? <LoadingRows rows={3} /> : "entries" in data ? (
            data.entries.length ? (
              <ul>
                {data.entries.map((e) => {
                  const p = `${dir}/${e.name}`;
                  return (
                    <li key={e.name}>
                      <button
                        onClick={() => (e.type === "dir" ? go(p) : setFile(p))}
                        className={cn("flex w-full items-center gap-2 rounded px-2 py-1.5 text-left text-[13px] hover:bg-surface-2", file === p && "bg-surface-2")}
                      >
                        {e.type === "dir" ? <Folder className="size-4 shrink-0 text-st-provisioning" /> : <FileText className="size-4 shrink-0 text-muted-foreground" />}
                        <Mono className="flex-1 truncate">{e.name}{e.type === "dir" ? "/" : ""}</Mono>
                        <span className="text-[11px] text-muted-foreground">{f.bytes(e.size)}</span>
                      </button>
                    </li>
                  );
                })}
              </ul>
            ) : <p className="p-4 text-center text-[13px] text-muted-foreground">{t("empty")}</p>
          ) : null}
        </div>
      </Card>
      <div className="min-w-0 lg:col-span-3">
        {file && fileData && "content" in fileData ? (
          <div className="flex flex-col gap-2">
            <div className="flex flex-wrap items-center justify-between gap-2">
              <Mono className="min-w-0 truncate">{file}</Mono>
              <div className="flex items-center gap-2">
                <span className="text-xs text-muted-foreground">{t("lines", { n: fileData.total_lines })}</span>
                <Button size="xs" variant="secondary" onClick={() => download(file.split("/").pop() ?? "file.txt", fileData.content)}><Download /> {tc("download")}</Button>
              </div>
            </div>
            <CodeBlock code={fileData.content} language={file.split(".").pop()} maxHeight={520} />
          </div>
        ) : (
          <Empty title={t("pick")} desc={t("pickDesc")} />
        )}
      </div>
    </div>
  );
}

// ---------------- Logs ----------------
export function LogsTab({ box }: { box: Box }) {
  const t = useTranslations("boxDetail.logs");
  const tc = useTranslations("common");
  const f = useFmt();
  const sp = useSearchParams();
  const router = useRouter();
  const { data: ev, error } = useApi<List<BoxaEvent>>(paths.boxEvents(box.id));
  const known = (ev?.items ?? []).map(parseExecEvent).filter((x): x is NonNullable<typeof x> => Boolean(x?.log_id));
  const urlLog = sp.get("log");
  const [input, setInput] = React.useState(urlLog ?? "");
  const [logId, setLogIdState] = React.useState<string | null>(urlLog);
  // Theo dõi ?log= (link từ Timeline) và giữ URL đồng bộ để chia sẻ được
  React.useEffect(() => {
    if (urlLog && urlLog !== logId) {
      setLogIdState(urlLog);
      setInput(urlLog);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [urlLog]);
  const setLogId = (id: string | null) => {
    setLogIdState(id);
    router.replace(`/boxes/${box.id}?tab=logs${id ? `&log=${encodeURIComponent(id)}` : ""}`, { scroll: false });
  };
  const { data: text, error: logErr } = useApi<string>(logId ? paths.log(logId) : null);
  if (error) return <ErrorBox error={error} />;
  if (!ev) return <LoadingRows />;
  const journal = [...ev.items].sort((a, b) => Date.parse(a.ts) - Date.parse(b.ts)).map((e) => `${e.ts}  ${e.type.padEnd(18)} ${e.actor}  ${e.message}`).join("\n");
  return (
    <div className="grid gap-4 lg:grid-cols-5">
      <div className="flex min-w-0 flex-col gap-3 lg:col-span-2">
        <Card className="p-3">
          <p className="mb-2 text-[13px] font-medium">{t("openTitle")}</p>
          <form className="flex gap-2" onSubmit={(e) => { e.preventDefault(); setLogId(input.trim() || null); }}>
            <Input value={input} onChange={(e) => setInput(e.target.value)} placeholder={t("logIdPh")} className="min-w-0 font-mono" />
            <Button type="submit" size="sm" variant="secondary" disabled={!input.trim()}>{tc("open")}</Button>
          </form>
          {known.length > 0 && (
            <ul className="mt-2 flex flex-col">
              {known.map((k) => (
                <li key={k.log_id}>
                  <button onClick={() => { setInput(k.log_id!); setLogId(k.log_id); }} className={cn("flex w-full items-center gap-2 rounded px-2 py-1.5 text-left hover:bg-surface-2", logId === k.log_id && "bg-surface-2")}>
                    <span className={cn("size-2 shrink-0 rounded-full", k.exit_code === 0 ? "bg-st-running" : "bg-st-error")} />
                    <Mono className="min-w-0 flex-1 truncate">{k.command}</Mono>
                    {k.tokens_saved ? <span className="shrink-0 font-mono text-[11px] text-st-running">{t("withTokens", { n: f.compact(k.tokens_saved) })}</span> : null}
                  </button>
                </li>
              ))}
            </ul>
          )}
        </Card>
        <p className="text-xs text-muted-foreground">{t.rich("hint", { mono: monoTag })}</p>
      </div>
      <div className="flex min-w-0 flex-col gap-2 lg:col-span-3">
        {logId ? (
          <>
            <div className="flex flex-wrap items-center justify-between gap-2">
              <Mono className="min-w-0 truncate text-muted-foreground">{logId}</Mono>
              <div className="flex gap-2">
                <Button size="xs" variant="ghost" onClick={() => setLogId(null)}>{t("journal")}</Button>
                <Button size="xs" variant="secondary" disabled={typeof text !== "string"} onClick={() => download(`${logId}.log`, String(text))}><Download /> {t("downloadLog")}</Button>
              </div>
            </div>
            <pre className="max-h-[520px] overflow-auto rounded-md border border-border bg-background p-3 font-mono text-[12.5px] leading-relaxed">{logErr ? t("openFailed", { error: logErr.message }) : typeof text === "string" ? text : tc("loading")}</pre>
          </>
        ) : (
          <>
            <div className="flex flex-wrap items-center justify-between gap-2">
              <span className="text-[13px] text-muted-foreground">{t("journalTitle", { n: ev.items.length })}</span>
              <Button size="xs" variant="secondary" onClick={() => download(`${box.slug}-events.log`, journal)}><Download /> {tc("download")}</Button>
            </div>
            <pre className="max-h-[520px] overflow-auto rounded-md border border-border bg-background p-3 font-mono text-[12px] leading-relaxed">{journal || t("noEvents")}</pre>
          </>
        )}
      </div>
    </div>
  );
}

// ---------------- Snapshots ----------------
export function SnapshotsTab({ box }: { box: Box }) {
  const t = useTranslations("boxDetail.snapshots");
  const tc = useTranslations("common");
  const f = useFmt();
  const { data, error } = useApi<List<Snapshot>>(paths.boxSnapshots(box.id));
  const { run, pending } = useAction();
  const router = useRouter();
  if (error) return <ErrorBox error={error} />;
  if (!data) return <LoadingRows />;
  return (
    <div className="flex flex-col gap-3">
      <div className="flex justify-end">
        <Button size="sm" variant="secondary" disabled={!!pending || !["running", "sleeping"].includes(box.status)} onClick={() => run("snap", () => api.snapshotBox(box.id), { success: (s) => t("created", { id: s.id }), revalidate: [paths.boxSnapshots(box.id)] })}>
          <History /> {t("createNow")}
        </Button>
      </div>
      {!data.items.length ? <Empty title={t("empty")} desc={t("emptyDesc")} /> : (
        <Card>
          <Table>
            <THead><TR className="hover:bg-transparent"><TH>{t("id")}</TH><TH>{t("size")}</TH><TH>{tc("createdAt")}</TH><TH>{t("storage")}</TH><TH className="hidden md:table-cell">{t("location")}</TH><TH className="text-right">{tc("actions")}</TH></TR></THead>
            <TBody>
              {data.items.map((s) => (
                <TR key={s.id}>
                  <TD><Mono>{s.id}</Mono></TD>
                  <TD className="whitespace-nowrap font-mono text-[12.5px]">{f.mb(s.size_mb)}</TD>
                  <TD className="whitespace-nowrap text-[13px]">{f.dateTime(s.created_at)}</TD>
                  <TD>
                    {s.storage === "s3" ? <Badge variant="green" title={s.remote_url}>{t("storageS3")}</Badge> : <Badge variant={s.upload_error ? "yellow" : "gray"} title={s.upload_error || t("storageLocalHint")}>{t("storageLocal")}</Badge>}
                  </TD>
                  <TD className="hidden max-w-[260px] truncate md:table-cell"><Mono className="text-muted-foreground">{s.remote_url || s.location}</Mono></TD>
                  <TD>
                    <div className="flex justify-end gap-1">
                      <ConfirmButton size="xs" variant="outline" disabled={!!pending} title={t("restoreTitle", { slug: box.slug, id: s.id })} desc={t("restoreDesc")} confirmLabel={t("restore")}
                        onConfirm={() => run("restore", () => api.restoreSnapshot(box.id, s.id), { success: (b) => t("restoring", { slug: b.slug }), revalidate: [paths.box(box.id), paths.boxEvents(box.id), startsWith("/boxes?"), "/boxes", "/overview"] })}>
                        <History /> {t("restore")}
                      </ConfirmButton>
                      <ConfirmButton size="xs" variant="outline" disabled={!!pending} title={t("forkTitle", { id: s.id })} desc={t("forkDesc")} confirmLabel={t("fork")}
                        onConfirm={async () => {
                          const nb = await run("fork", () => api.forkSnapshot(box.id, s.id), { success: (b) => t("forked", { slug: b.slug }), revalidate: [startsWith("/boxes"), "/overview"] });
                          if (nb) router.push(`/boxes/${nb.id}`);
                        }}>
                        <GitFork /> {t("fork")}
                      </ConfirmButton>
                    </div>
                  </TD>
                </TR>
              ))}
            </TBody>
          </Table>
        </Card>
      )}
    </div>
  );
}

// ---------------- Cấu hình ----------------
export function ConfigTab({ box }: { box: Box }) {
  const t = useTranslations("boxDetail.config");
  const tc = useTranslations("common");
  // v1.1: GET /boxes/:id/yaml → {yaml}; Core cũ không có thì dựng lại từ object Box
  const { data: remote, error: yamlErr, isLoading } = useApi<{ yaml: string }>(paths.boxYaml(box.id), { shouldRetryOnError: false, refreshInterval: 0 });
  const fromCore = Boolean(remote?.yaml);
  const yaml = remote?.yaml ?? boxToYaml(box);
  const [name, setName] = React.useState(`${box.template}-${box.slug.split("-")[0]}`);
  const [desc, setDesc] = React.useState(t("defaultDesc", { slug: box.slug }));
  const [open, setOpen] = React.useState(false);
  const { run, pending } = useAction();
  const router = useRouter();
  const templateYaml = fromCore
    ? /^slug:/m.test(yaml) ? yaml.replace(/^slug: .*$/m, `name: ${name}`) : `name: ${name}\n${yaml}`
    : boxToTemplateYaml(box, name);
  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <p className="text-[13px] text-muted-foreground">
          {t("readOnly")}
          {!isLoading && <Badge variant={fromCore ? "green" : "outline"} className="ml-2" title={yamlErr ? String((yamlErr as Error).message) : undefined}>{fromCore ? t("fromCore") : t("rebuilt")}</Badge>}
        </p>
        <Dialog open={open} onOpenChange={setOpen}>
          <DialogTrigger asChild><Button size="sm" variant="secondary"><Save /> {t("saveTemplate")}</Button></DialogTrigger>
          <DialogContent>
            <DialogHeader>
              <DialogTitle>{t("saveTemplate")}</DialogTitle>
              <DialogDescription>{t.rich("saveTemplateDesc", { name: name || "…", mono: monoTag })}</DialogDescription>
            </DialogHeader>
            <Field label={t("templateName")}><Input value={name} onChange={(e) => setName(e.target.value.toLowerCase().replace(/[^a-z0-9-]/g, "-"))} className="font-mono" /></Field>
            <Field label={tc("description")}><Input value={desc} onChange={(e) => setDesc(e.target.value)} /></Field>
            <DialogFooter>
              <Button variant="ghost" onClick={() => setOpen(false)}>{tc("cancel")}</Button>
              <Button disabled={!name || !!pending} onClick={async () => {
                const tp = await run("tpl", () => api.createTemplate({ name, description: desc, yaml: templateYaml }), { success: t("savedToast", { name }), revalidate: [paths.templates] });
                if (tp) { setOpen(false); router.push(`/templates?t=${encodeURIComponent(tp.name)}`); }
              }}>{tc("save")}</Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      </div>
      <CodeBlock code={yaml} language="yaml" maxHeight={520} />
    </div>
  );
}
