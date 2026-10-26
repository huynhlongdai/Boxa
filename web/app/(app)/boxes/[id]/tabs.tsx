"use client";
import * as React from "react";
import { useRouter, useSearchParams } from "next/navigation";
import { ChevronRight, Download, FileText, Folder, GitFork, History, Save, Sparkles, Terminal as TermIcon, Zap } from "lucide-react";
import { api, paths } from "@/lib/api";
import { useAction, useApi } from "@/lib/hooks";
import { boxToTemplateYaml, boxToYaml } from "@/lib/box-yaml";
import type { Box, BoxaEvent, FilesResponse, List, Observe, Snapshot } from "@/lib/types";
import { parseExecEvent } from "@/lib/exec-event";
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
          {box.error && (
            <div className="mt-3 rounded-md border border-st-error/30 bg-st-error/5 p-3 text-[13px]">
              <p className="font-medium text-st-error">Lỗi{box.retries ? ` (đã thử ${box.retries} lần)` : ""}</p>
              <p className="mt-0.5 font-mono text-[12.5px]">{box.error}</p>
            </div>
          )}
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
          <CardHeader><CardTitle>Đang chạy trong box</CardTitle></CardHeader>
          <CardContent className="flex flex-col gap-3 text-[13px]">
            {obs ? (
              <>
                {([["CPU", obs.cpu_pct], ["RAM", obs.ram_pct], ["Đĩa", obs.disk_pct]] as const).filter(([, v]) => typeof v === "number").map(([k, v]) => (
                  <div key={k}>
                    <div className="mb-1 flex justify-between"><span className="text-muted-foreground">{k}</span><Mono>{v}%</Mono></div>
                    <Progress value={v as number} warnAt={75} dangerAt={92} />
                  </div>
                ))}
                <div>
                  <p className="mb-1 text-xs text-muted-foreground">Tiến trình</p>
                  {obs.processes.length ? obs.processes.map((p) => (
                    <div key={p.pid} className="flex justify-between gap-2 py-0.5"><Mono className="truncate">{p.cmd}</Mono><Mono className="text-muted-foreground">{p.pid}</Mono></div>
                  )) : <p className="text-muted-foreground">—</p>}
                </div>
                <Row k="Cổng đang nghe"><Mono>{obs.ports?.length ? obs.ports.join(", ") : "—"}</Mono></Row>
                {obs.files_changed?.length > 0 && (
                  <div>
                    <p className="mb-1 text-xs text-muted-foreground">File đổi từ {dateTime(obs.since)}</p>
                    {obs.files_changed.slice(0, 8).map((f) => <Mono key={f} className="block truncate">{f}</Mono>)}
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
  if (error) return <ErrorBox error={error} />;
  if (!ev) return <LoadingRows />;
  const items = [...ev.items].sort((a, b) => Date.parse(b.ts) - Date.parse(a.ts)).map((e) => ({ e, x: parseExecEvent(e) }));
  const execs = items.filter((i) => i.x);
  const saved = execs.reduce((a, i) => a + (i.x?.tokens_saved ?? 0), 0);
  const hasTokens = execs.some((i) => i.x?.tokens_saved !== null);
  if (!items.length) return <Empty title="Chưa có hoạt động" />;
  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap gap-2 text-[13px]">
        <Badge variant="outline">{execs.length} lệnh</Badge>
        <Badge variant="red">{execs.filter((i) => i.x!.exit_code !== 0 || i.x!.timed_out).length} lỗi</Badge>
        {hasTokens && <Badge variant="green"><Zap className="size-3" /> tiết kiệm {compact(saved)} token nhờ nén output</Badge>}
      </div>
      <ol className="relative flex flex-col gap-2 border-l border-border pl-4">
        {items.map(({ e, x }) =>
          x ? (
            <li key={e.id} className="relative">
              <span className={cn("absolute -left-[21px] top-3 size-2.5 rounded-full border-2 border-background", x.exit_code === 0 && !x.timed_out ? "bg-st-running" : "bg-st-error")} />
              <div className="rounded-md border border-border bg-card p-3">
                <div className="flex flex-wrap items-center gap-2">
                  <TermIcon className="size-3.5 text-muted-foreground" />
                  <Mono className="min-w-0 flex-1 basis-60 truncate" >{x.command}</Mono>
                  {x.timed_out && <Badge variant="yellow">TIMEOUT</Badge>}
                  <Badge variant={x.exit_code === 0 ? "green" : "red"} className="font-mono">exit {x.exit_code}</Badge>
                  <span className="font-mono text-xs text-muted-foreground">{ms(x.duration_ms)}</span>
                  {x.tokens_saved ? <span className="font-mono text-xs text-st-running">−{compact(x.tokens_saved)} token</span> : null}
                  {x.log_id && <a href={`?tab=logs&log=${x.log_id}`} className="font-mono text-xs text-primary hover:underline">log</a>}
                  <span className="text-xs text-muted-foreground">{e.actor} · {dateTime(e.ts)}</span>
                </div>
              </div>
            </li>
          ) : (
            <li key={e.id} className="relative py-1">
              <span className={cn("absolute -left-[20px] top-2.5 size-2 rounded-full", /fail|down|error/.test(e.type) ? "bg-st-error" : "bg-muted-foreground/60")} />
              <div className="flex flex-wrap items-baseline gap-x-2 text-[13px]">
                <span className="font-mono text-[11.5px] text-muted-foreground">{e.type}</span>
                <span>{e.message}</span>
                {e.cost_usd !== null && <Mono className="text-muted-foreground">{usd(e.cost_usd)}</Mono>}
                <span className="text-xs text-muted-foreground">· {e.actor} · {dateTime(e.ts)}</span>
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
  const sp = useSearchParams();
  const { data: ev, error } = useApi<List<BoxaEvent>>(paths.boxEvents(box.id));
  const known = (ev?.items ?? []).map(parseExecEvent).filter((x): x is NonNullable<typeof x> => Boolean(x?.log_id));
  const [input, setInput] = React.useState(sp.get("log") ?? "");
  const [logId, setLogId] = React.useState<string | null>(sp.get("log"));
  const { data: text, error: logErr } = useApi<string>(logId ? paths.log(logId) : null);
  if (error) return <ErrorBox error={error} />;
  if (!ev) return <LoadingRows />;
  const journal = [...ev.items].sort((a, b) => Date.parse(a.ts) - Date.parse(b.ts)).map((e) => `${e.ts}  ${e.type.padEnd(18)} ${e.actor}  ${e.message}`).join("\n");
  return (
    <div className="grid gap-4 lg:grid-cols-5">
      <div className="flex flex-col gap-3 lg:col-span-2">
        <Card className="p-3">
          <p className="mb-2 text-[13px] font-medium">Mở log đầy đủ của một lệnh</p>
          <form className="flex gap-2" onSubmit={(e) => { e.preventDefault(); setLogId(input.trim() || null); }}>
            <Input value={input} onChange={(e) => setInput(e.target.value)} placeholder="log_id (trong ExecResult)" className="font-mono" />
            <Button type="submit" size="sm" variant="secondary" disabled={!input.trim()}>Mở</Button>
          </form>
          {known.length > 0 && (
            <ul className="mt-2 flex flex-col">
              {known.map((k) => (
                <li key={k.log_id}>
                  <button onClick={() => { setInput(k.log_id!); setLogId(k.log_id); }} className={cn("flex w-full items-center gap-2 rounded px-2 py-1.5 text-left hover:bg-surface-2", logId === k.log_id && "bg-surface-2")}>
                    <span className={cn("size-2 shrink-0 rounded-full", k.exit_code === 0 ? "bg-st-running" : "bg-st-error")} />
                    <Mono className="min-w-0 flex-1 truncate">{k.command}</Mono>
                  </button>
                </li>
              ))}
            </ul>
          )}
        </Card>
        <p className="text-xs text-muted-foreground">Log đầy đủ được lưu theo <span className="font-mono">log_id</span> mà agent nhận trong kết quả exec (khi output bị rút gọn).</p>
      </div>
      <div className="flex flex-col gap-2 lg:col-span-3">
        {logId ? (
          <>
            <div className="flex items-center justify-between">
              <Mono className="text-muted-foreground">{logId}</Mono>
              <div className="flex gap-2">
                <Button size="xs" variant="ghost" onClick={() => setLogId(null)}>Nhật ký box</Button>
                <Button size="xs" variant="secondary" disabled={typeof text !== "string"} onClick={() => download(`${logId}.log`, String(text))}><Download /> Tải log</Button>
              </div>
            </div>
            <pre className="max-h-[520px] overflow-auto rounded-md border border-border bg-background p-3 font-mono text-[12.5px] leading-relaxed">{logErr ? `Không mở được log: ${logErr.message}` : typeof text === "string" ? text : "Đang tải…"}</pre>
          </>
        ) : (
          <>
            <div className="flex items-center justify-between">
              <span className="text-[13px] text-muted-foreground">Nhật ký sự kiện của box ({ev.items.length} dòng)</span>
              <Button size="xs" variant="secondary" onClick={() => download(`${box.slug}-events.log`, journal)}><Download /> Tải về</Button>
            </div>
            <pre className="max-h-[520px] overflow-auto rounded-md border border-border bg-background p-3 font-mono text-[12px] leading-relaxed">{journal || "Chưa có sự kiện"}</pre>
          </>
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
