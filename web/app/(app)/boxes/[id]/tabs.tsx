"use client";
import * as React from "react";
import { useRouter } from "next/navigation";
import { ChevronRight, Download, FileText, Folder, GitFork, History, Save, Sparkles, Terminal as TermIcon, Zap } from "lucide-react";
import { api, paths } from "@/lib/api";
import { useAction, useApi } from "@/lib/hooks";
import { boxToTemplateYaml, boxToYaml } from "@/lib/box-yaml";
import type { Box, BoxaEvent, FilesResponse, Job, List, Observe, Snapshot } from "@/lib/types";
import { bytes, cn, compact, dateTime, mb, ms, relTime, usd } from "@/lib/utils";
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

function Row({ k, children }: { k: string; children: React.ReactNode }) {
  return (
    <div className="flex items-start justify-between gap-4 border-b border-border/60 py-2 text-[13px] last:border-0">
      <span className="shrink-0 text-muted-foreground">{k}</span>
      <span className="min-w-0 text-right">{children}</span>
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

// ---------------- Tổng quan ----------------
export function OverviewTab({ box }: { box: Box }) {
  const { data: obs } = useApi<Observe>(box.status === "running" ? paths.boxObserve(box.id) : null, { refreshInterval: 10000 });
  const remaining = box.budget_usd ? Math.max(0, box.budget_usd - box.spent_usd) : null;
  return (
    <div className="grid gap-4 lg:grid-cols-3">
      <Card className="lg:col-span-2">
        <CardHeader><CardTitle>Thông số</CardTitle></CardHeader>
        <CardContent>
          <Row k="Spec"><Mono>{box.spec.cpu} vCPU · {mb(box.spec.ram_mb)} RAM · {box.spec.disk_gb} GB đĩa</Mono></Row>
          <Row k="Template"><Mono>{box.template}</Mono></Row>
          <Row k="Host"><Mono>{box.host_id ?? "—"}</Mono> <span className="text-muted-foreground">({box.provider}/{box.region})</span></Row>
          <Row k="URL">{box.url ? <a className="break-all text-primary hover:underline" href={box.url} target="_blank" rel="noreferrer"><Mono>{box.url}</Mono></a> : <span className="text-muted-foreground">chỉ có khi box chạy</span>}</Row>
          <Row k="Cổng"><Mono>{box.ports.length ? box.ports.join(", ") : "—"}</Mono></Row>
          <Row k="Secrets">{box.secrets.length ? <span className="flex flex-wrap justify-end gap-1">{box.secrets.map((s) => <Badge key={s} variant="outline" className="font-mono">{s}</Badge>)}</span> : "—"}</Row>
          <Row k="TTL / tự ngủ sau">{box.ttl_seconds ? `${Math.round(box.ttl_seconds / 3600)} giờ` : "không giới hạn"} · {box.idle_minutes ? `${box.idle_minutes} phút rảnh` : "không tự ngủ"}</Row>
          <Row k="Tạo lúc">{dateTime(box.created_at)} · hoạt động {relTime(box.last_active_at)}</Row>
          <div className="mt-3 rounded-md border border-primary/25 bg-primary/5 p-3">
            <p className="flex items-center gap-1.5 text-[12px] font-medium text-primary"><Sparkles className="size-3.5" /> Vì sao Scheduler chọn nơi này</p>
            <p className="mt-1 text-[13px]">{box.placement_reason || "—"}</p>
          </div>
        </CardContent>
      </Card>
      <div className="flex flex-col gap-4">
        <Card>
          <CardHeader><CardTitle>Chi phí</CardTitle></CardHeader>
          <CardContent className="flex flex-col gap-3">
            <div>
              <p className="text-xs text-muted-foreground">Đã tiêu / ngân sách box</p>
              <MoneyBudget spent={box.spent_usd} budget={box.budget_usd} className="text-lg" />
              <BudgetBar spent={box.spent_usd} budget={box.budget_usd} className="mt-2" />
            </div>
            <Row k="Chi phí theo giờ"><Mono>{usd(box.cost_per_hour)}/giờ</Mono></Row>
            <Row k="Ngân sách còn lại"><Mono>{remaining === null ? "∞" : usd(remaining)}</Mono></Row>
            {remaining !== null && box.cost_per_hour > 0 && <Row k="Đủ chạy thêm"><Mono>~{Math.floor(remaining / box.cost_per_hour)} giờ</Mono></Row>}
          </CardContent>
        </Card>
        <Card>
          <CardHeader><CardTitle>Tài nguyên</CardTitle></CardHeader>
          <CardContent className="flex flex-col gap-3 text-[13px]">
            {obs ? (
              <>
                {([["CPU", obs.cpu_pct], ["RAM", obs.ram_pct], ["Đĩa", obs.disk_pct]] as const).map(([k, v]) => (
                  <div key={k}>
                    <div className="mb-1 flex justify-between"><span className="text-muted-foreground">{k}</span><Mono>{v}%</Mono></div>
                    <Progress value={v} warnAt={75} dangerAt={92} />
                  </div>
                ))}
                {obs.processes.length > 0 && (
                  <div className="mt-1 border-t border-border pt-2">
                    {obs.processes.map((p) => (
                      <div key={p.pid} className="flex justify-between gap-2 py-0.5"><Mono className="truncate">{p.command}</Mono><Mono className="text-muted-foreground">{p.cpu_pct}%</Mono></div>
                    ))}
                  </div>
                )}
              </>
            ) : (
              <p className="text-muted-foreground">{box.status === "running" ? "Đang đọc số liệu…" : "Box không chạy."}</p>
            )}
          </CardContent>
        </Card>
      </div>
    </div>
  );
}

// ---------------- Timeline ----------------
export function TimelineTab({ box }: { box: Box }) {
  const { data: ev, error } = useApi<List<BoxaEvent>>(paths.boxEvents(box.id));
  const { data: jobs } = useApi<List<Job>>(paths.adminJobs);
  const [open, setOpen] = React.useState<string | null>(null);
  if (error) return <ErrorBox error={error} />;
  if (!ev) return <LoadingRows />;
  const myJobs = (jobs?.items ?? []).filter((j) => j.box_id === box.id);
  type Item = { ts: string; kind: "job"; job: Job } | { ts: string; kind: "event"; ev: BoxaEvent };
  const items: Item[] = [
    ...myJobs.map((j) => ({ ts: j.created_at, kind: "job" as const, job: j })),
    ...ev.items.filter((e) => !e.type.startsWith("exec")).map((e) => ({ ts: e.ts, kind: "event" as const, ev: e })),
  ].sort((a, b) => Date.parse(b.ts) - Date.parse(a.ts));
  const saved = myJobs.reduce((a, j) => a + (j.result?.tokens_saved ?? 0), 0);
  if (!items.length) return <Empty title="Chưa có hoạt động" />;
  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap gap-2 text-[13px]">
        <Badge variant="outline">{myJobs.length} lệnh</Badge>
        <Badge variant="red">{myJobs.filter((j) => (j.result?.exit_code ?? 0) !== 0).length} lỗi</Badge>
        <Badge variant="green"><Zap className="size-3" /> tiết kiệm {compact(saved)} token nhờ nén output</Badge>
      </div>
      <ol className="relative flex flex-col gap-2 border-l border-border pl-4">
        {items.map((it) =>
          it.kind === "job" ? (
            <li key={it.job.id} className="relative">
              <span className={cn("absolute -left-[21px] top-3 size-2.5 rounded-full border-2 border-background", (it.job.result?.exit_code ?? 0) === 0 ? "bg-st-running" : "bg-st-error")} />
              <button onClick={() => setOpen(open === it.job.id ? null : it.job.id)} className="w-full rounded-md border border-border bg-card p-3 text-left hover:border-border/100 hover:bg-surface-2/40">
                <div className="flex flex-wrap items-center gap-2">
                  <TermIcon className="size-3.5 text-muted-foreground" />
                  <Mono className="min-w-0 flex-1 truncate">{it.job.command}</Mono>
                  {it.job.result && (
                    <>
                      <Badge variant={it.job.result.exit_code === 0 ? "green" : "red"} className="font-mono">exit {it.job.result.exit_code}</Badge>
                      <span className="font-mono text-xs text-muted-foreground">{ms(it.job.result.duration_ms)}</span>
                      {it.job.result.tokens_saved > 0 && <span className="font-mono text-xs text-st-running">−{compact(it.job.result.tokens_saved)} token</span>}
                    </>
                  )}
                  {!it.job.result && <Badge variant="blue">{it.job.status}</Badge>}
                  <span className="text-xs text-muted-foreground">{dateTime(it.ts)}</span>
                </div>
                {open === it.job.id && it.job.result && (
                  <pre className="mt-2 max-h-64 overflow-auto rounded bg-background p-2 font-mono text-[12px] leading-relaxed">
                    {it.job.result.stdout_tail}
                    {it.job.result.stderr_tail && <span className="text-st-error">{`\n${it.job.result.stderr_tail}`}</span>}
                    {it.job.result.truncated && <span className="text-muted-foreground">{"\n… (đã rút gọn — xem đầy đủ ở tab Logs)"}</span>}
                  </pre>
                )}
              </button>
            </li>
          ) : (
            <li key={it.ev.id} className="relative py-1">
              <span className={cn("absolute -left-[20px] top-2.5 size-2 rounded-full", it.ev.type.includes("fail") ? "bg-st-error" : "bg-muted-foreground/60")} />
              <div className="flex flex-wrap items-baseline gap-x-2 text-[13px]">
                <span className="font-mono text-[11.5px] text-muted-foreground">{it.ev.type}</span>
                <span>{it.ev.message}</span>
                {it.ev.cost_usd !== null && <Mono className="text-muted-foreground">{usd(it.ev.cost_usd)}</Mono>}
                <span className="text-xs text-muted-foreground">· {it.ev.actor} · {dateTime(it.ts)}</span>
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
  const [root, setRoot] = React.useState<"/work" | "/memory">("/work");
  const [dir, setDir] = React.useState("/work");
  const [file, setFile] = React.useState<string | null>(null);
  const { data, error, isLoading } = useApi<FilesResponse>(paths.boxFiles(box.id, dir));
  const { data: fileData } = useApi<FilesResponse>(file ? paths.boxFiles(box.id, file) : null);
  const crumbs = dir.split("/").filter(Boolean);
  const go = (p: string) => { setDir(p); setFile(null); };

  return (
    <div className="grid gap-4 lg:grid-cols-5">
      <Card className="lg:col-span-2">
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
                        {e.type === "dir" ? <Folder className="size-4 text-st-provisioning" /> : <FileText className="size-4 text-muted-foreground" />}
                        <Mono className="flex-1 truncate">{e.name}{e.type === "dir" ? "/" : ""}</Mono>
                        <span className="text-[11px] text-muted-foreground">{bytes(e.size)}</span>
                      </button>
                    </li>
                  );
                })}
              </ul>
            ) : <p className="p-4 text-center text-[13px] text-muted-foreground">Thư mục trống</p>
          ) : null}
        </div>
      </Card>
      <div className="lg:col-span-3">
        {file && fileData && "content" in fileData ? (
          <div className="flex flex-col gap-2">
            <div className="flex items-center justify-between gap-2">
              <Mono className="truncate">{file}</Mono>
              <div className="flex items-center gap-2">
                <span className="text-xs text-muted-foreground">{fileData.total_lines} dòng</span>
                <Button size="xs" variant="secondary" onClick={() => download(file.split("/").pop() ?? "file.txt", fileData.content)}><Download /> Tải về</Button>
              </div>
            </div>
            <CodeBlock code={fileData.content} language={file.split(".").pop()} maxHeight={520} />
          </div>
        ) : (
          <Empty title="Chọn một file để xem" desc="/work là thư mục làm việc, /memory là bộ nhớ lâu dài của agent (giữ qua các lần ngủ)." />
        )}
      </div>
    </div>
  );
}

// ---------------- Logs ----------------
export function LogsTab({ box }: { box: Box }) {
  const { data: jobs, error } = useApi<List<Job>>(paths.adminJobs);
  const mine = (jobs?.items ?? []).filter((j) => j.box_id === box.id && j.result?.log_id);
  const [sel, setSel] = React.useState<string | null>(null);
  const logId = sel ?? mine[0]?.result?.log_id ?? null;
  const { data: text } = useApi<string>(logId ? paths.log(logId) : null);
  if (error) return <ErrorBox error={error} />;
  if (!jobs) return <LoadingRows />;
  if (!mine.length) return <Empty title="Chưa có log nào được lưu" />;
  return (
    <div className="grid gap-4 lg:grid-cols-5">
      <Card className="p-2 lg:col-span-2">
        <ul className="flex flex-col">
          {mine.map((j) => (
            <li key={j.id}>
              <button onClick={() => setSel(j.result!.log_id)} className={cn("flex w-full items-center gap-2 rounded px-2 py-2 text-left hover:bg-surface-2", logId === j.result!.log_id && "bg-surface-2")}>
                <span className={cn("size-2 shrink-0 rounded-full", j.result!.exit_code === 0 ? "bg-st-running" : "bg-st-error")} />
                <Mono className="min-w-0 flex-1 truncate">{j.command}</Mono>
                <span className="shrink-0 text-[11px] text-muted-foreground">{relTime(j.created_at)}</span>
              </button>
            </li>
          ))}
        </ul>
      </Card>
      <div className="lg:col-span-3">
        {logId && (
          <div className="flex flex-col gap-2">
            <div className="flex items-center justify-between">
              <Mono className="text-muted-foreground">{logId}</Mono>
              <Button size="xs" variant="secondary" disabled={typeof text !== "string"} onClick={() => download(`${logId}.log`, String(text))}><Download /> Tải log</Button>
            </div>
            <pre className="max-h-[520px] overflow-auto rounded-md border border-border bg-background p-3 font-mono text-[12.5px] leading-relaxed">{typeof text === "string" ? text : "Đang tải…"}</pre>
          </div>
        )}
      </div>
    </div>
  );
}

// ---------------- Snapshots ----------------
export function SnapshotsTab({ box }: { box: Box }) {
  const { data, error } = useApi<List<Snapshot>>(paths.boxSnapshots(box.id));
  const { run, pending } = useAction();
  const router = useRouter();
  if (error) return <ErrorBox error={error} />;
  if (!data) return <LoadingRows />;
  return (
    <div className="flex flex-col gap-3">
      <div className="flex justify-end">
        <Button size="sm" variant="secondary" disabled={!!pending || !["running", "sleeping"].includes(box.status)} onClick={() => run("snap", () => api.snapshotBox(box.id), { success: (s) => `Đã tạo ${s.id}`, revalidate: [paths.boxSnapshots(box.id)] })}>
          <History /> Tạo snapshot ngay
        </Button>
      </div>
      {!data.items.length ? <Empty title="Chưa có snapshot" desc="Reaper tự snapshot khi cho box ngủ." /> : (
        <Card>
          <Table>
            <THead><TR className="hover:bg-transparent"><TH>ID</TH><TH>Kích thước</TH><TH>Tạo lúc</TH><TH className="hidden md:table-cell">Vị trí</TH><TH className="text-right">Thao tác</TH></TR></THead>
            <TBody>
              {data.items.map((s) => (
                <TR key={s.id}>
                  <TD><Mono>{s.id}</Mono></TD>
                  <TD className="font-mono text-[12.5px]">{mb(s.size_mb)}</TD>
                  <TD className="whitespace-nowrap text-[13px]">{dateTime(s.created_at)}</TD>
                  <TD className="hidden max-w-[260px] truncate md:table-cell"><Mono className="text-muted-foreground">{s.location}</Mono></TD>
                  <TD>
                    <div className="flex justify-end gap-1">
                      <ConfirmButton size="xs" variant="outline" title={`Khôi phục ${box.slug} từ ${s.id}?`} desc="Trạng thái hiện tại của box sẽ bị thay bằng snapshot này. Nên tạo snapshot mới trước nếu cần giữ." confirmLabel="Khôi phục"
                        onConfirm={() => run("restore", () => api.restoreSnapshot(box.id, s.id), { success: "Đang khôi phục…", revalidate: [paths.box(box.id)] })}>
                        <History /> Khôi phục
                      </ConfirmButton>
                      <Button size="xs" variant="outline" disabled={!!pending} onClick={async () => {
                        const nb = await run("fork", () => api.forkSnapshot(box.id, s.id), { success: (b) => `Đã fork thành ${b.slug}` });
                        if (nb) router.push(`/boxes/${nb.id}`);
                      }}>
                        <GitFork /> Fork
                      </Button>
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
  const [name, setName] = React.useState(`${box.template}-${box.slug.split("-")[0]}`);
  const [desc, setDesc] = React.useState(`Lưu từ box ${box.slug}`);
  const [open, setOpen] = React.useState(false);
  const { run, pending } = useAction();
  const router = useRouter();
  return (
    <div className="flex flex-col gap-3">
      <div className="flex items-center justify-between gap-2">
        <p className="text-[13px] text-muted-foreground">box.yaml — chỉ đọc</p>
        <Dialog open={open} onOpenChange={setOpen}>
          <DialogTrigger asChild><Button size="sm" variant="secondary"><Save /> Lưu thành template</Button></DialogTrigger>
          <DialogContent>
            <DialogHeader>
              <DialogTitle>Lưu thành template</DialogTitle>
              <DialogDescription>Agent có thể tạo box giống hệt bằng <span className="font-mono">template: {name || "…"}</span>.</DialogDescription>
            </DialogHeader>
            <Field label="Tên template"><Input value={name} onChange={(e) => setName(e.target.value.toLowerCase().replace(/[^a-z0-9-]/g, "-"))} className="font-mono" /></Field>
            <Field label="Mô tả"><Input value={desc} onChange={(e) => setDesc(e.target.value)} /></Field>
            <DialogFooter>
              <Button variant="ghost" onClick={() => setOpen(false)}>Huỷ</Button>
              <Button disabled={!name || !!pending} onClick={async () => {
                const t = await run("tpl", () => api.createTemplate({ name, description: desc, yaml: boxToTemplateYaml(box, name) }), { success: `Đã lưu template ${name}`, revalidate: [paths.templates] });
                if (t) { setOpen(false); router.push(`/templates?t=${encodeURIComponent(t.name)}`); }
              }}>Lưu</Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      </div>
      <CodeBlock code={boxToYaml(box)} language="yaml" maxHeight={520} />
    </div>
  );
}
