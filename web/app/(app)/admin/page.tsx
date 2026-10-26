"use client";
import * as React from "react";
import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { Activity, AlertTriangle, FilterX, Plus, Save } from "lucide-react";
import { api, paths } from "@/lib/api";
import { useAction, useApi } from "@/lib/hooks";
import type { AdminSystem, AdminUser, Agent, ApiKey, BoxaEvent, Box, Health, Job, List, Quotas, Settings } from "@/lib/types";
import { cn, dateTime, ms, relTime } from "@/lib/utils";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Field, Input, Select, Textarea } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { Table, TBody, TD, TH, THead, TR } from "@/components/ui/table";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog";
import { ConfirmButton } from "@/components/boxa/confirm";
import { CopyButton } from "@/components/boxa/code-block";
import { Dot, Mono } from "@/components/boxa/status";
import { Empty, ErrorBox, LoadingRows, PageHeader } from "@/components/boxa/page";

function SystemTab() {
  const { data, error } = useApi<AdminSystem>(paths.adminSystem, { refreshInterval: 15000 });
  const { data: health } = useApi<Health>(paths.health);
  if (error) return <ErrorBox error={error} />;
  if (!data) return <LoadingRows />;
  const tone = (s: string) => (s === "ok" ? "green" : s === "degraded" ? "yellow" : "red") as "green" | "yellow" | "red";
  return (
    <div className="flex flex-col gap-4">
      <div className="grid grid-cols-2 gap-3 md:grid-cols-4">
        <Card className="p-4"><p className="text-xs text-muted-foreground">Phiên bản</p><p className="mt-1 font-mono text-lg">{data.version}</p></Card>
        <Card className="p-4"><p className="text-xs text-muted-foreground">Uptime</p><p className="mt-1 font-mono text-lg">{Math.floor(data.uptime_s / 86400)}n {Math.floor((data.uptime_s % 86400) / 3600)}g</p></Card>
        <Card className="p-4"><p className="text-xs text-muted-foreground">/health</p><p className="mt-1 text-lg">{health?.ok ? <span className="text-st-running">OK</span> : <span className="text-st-error">Lỗi</span>}</p></Card>
        <Card className="p-4"><p className="text-xs text-muted-foreground">Thành phần lỗi</p><p className="mt-1 text-lg">{data.components.filter((c) => c.status !== "ok").length}</p></Card>
      </div>
      <Card>
        <Table>
          <THead><TR className="hover:bg-transparent"><TH>Thành phần</TH><TH>Trạng thái</TH><TH className="text-right">Độ trễ</TH></TR></THead>
          <TBody>
            {data.components.map((c) => (
              <TR key={c.name}>
                <TD><Mono>{c.name}</Mono></TD>
                <TD><Badge variant={tone(c.status)}><Dot tone={tone(c.status)} />{c.status === "ok" ? "Ổn định" : c.status === "degraded" ? "Chậm" : "Ngừng"}</Badge></TD>
                <TD className={cn("text-right font-mono text-[13px]", c.latency_ms > 500 && "text-st-warning")}>{c.latency_ms} ms</TD>
              </TR>
            ))}
          </TBody>
        </Table>
      </Card>
    </div>
  );
}

function JobsTab() {
  const { data, error } = useApi<List<Job>>(paths.adminJobs);
  const { data: boxes } = useApi<List<Box>>(paths.boxes());
  const slug = (id: string) => boxes?.items.find((b) => b.id === id)?.slug ?? id;
  if (error) return <ErrorBox error={error} />;
  if (!data) return <LoadingRows />;
  const st = { queued: "gray", running: "blue", done: "green", failed: "red" } as const;
  const label = { queued: "Chờ", running: "Đang chạy", done: "Xong", failed: "Lỗi" } as const;
  return (
    <Card>
      <Table>
        <THead><TR className="hover:bg-transparent"><TH>Job</TH><TH>Box</TH><TH>Lệnh</TH><TH>Trạng thái</TH><TH>Exit</TH><TH>Thời lượng</TH><TH>Tạo</TH></TR></THead>
        <TBody>
          {data.items.map((j) => (
            <TR key={j.id}>
              <TD><Mono>{j.id}</Mono></TD>
              <TD><Link href={`/boxes/${j.box_id}?tab=timeline`} className="hover:text-primary"><Mono>{slug(j.box_id)}</Mono></Link></TD>
              <TD className="max-w-[320px] truncate"><Mono>{j.command}</Mono></TD>
              <TD><Badge variant={st[j.status]}>{label[j.status]}</Badge></TD>
              <TD className={cn("font-mono text-[13px]", (j.result?.exit_code ?? 0) !== 0 && "text-st-error")}>{j.result?.exit_code ?? "—"}</TD>
              <TD className="whitespace-nowrap font-mono text-[13px]">{j.result ? ms(j.result.duration_ms) : "—"}</TD>
              <TD className="whitespace-nowrap text-[13px] text-muted-foreground">{relTime(j.created_at)}</TD>
            </TR>
          ))}
        </TBody>
      </Table>
    </Card>
  );
}

function AuditTab() {
  const [actor, setActor] = React.useState("");
  const [type, setType] = React.useState("");
  const { data, error } = useApi<List<BoxaEvent>>(paths.adminAudit({ actor: actor || undefined, type: type || undefined }));
  return (
    <div className="flex flex-col gap-3">
      <div className="grid grid-cols-2 gap-2 sm:flex">
        <Input value={actor} onChange={(e) => setActor(e.target.value)} placeholder="Actor (vd: user:anh, reaper)" className="font-mono sm:w-64" />
        <Select value={type} onChange={(e) => setType(e.target.value)}>
          <option value="">Mọi loại</option>
          {["box", "exec", "agent", "key", "secret", "host", "provider", "approval", "budget", "settings", "emergency", "terminal"].map((t) => <option key={t} value={t}>{t}.*</option>)}
        </Select>
        {(actor || type) && <Button size="sm" variant="ghost" onClick={() => { setActor(""); setType(""); }}><FilterX /> Bỏ lọc</Button>}
      </div>
      {error ? <ErrorBox error={error} /> : !data ? <LoadingRows /> : !data.items.length ? <Empty title="Không có sự kiện" /> : (
        <Card>
          <Table>
            <THead><TR className="hover:bg-transparent"><TH>Thời gian</TH><TH>Actor</TH><TH>Loại</TH><TH>Nội dung</TH></TR></THead>
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
  const agentName = (id: string | null) => (id ? agents?.items.find((a) => a.id === id)?.name ?? id : null);
  if (error) return <ErrorBox error={error} />;
  if (!data) return <LoadingRows />;
  return (
    <div className="flex flex-col gap-3">
      <div className="flex justify-end">
        <Dialog open={open} onOpenChange={(o) => { setOpen(o); if (!o) { setCreated(null); setName(""); } }}>
          <DialogTrigger asChild><Button size="sm"><Plus /> Tạo key admin</Button></DialogTrigger>
          <DialogContent>
            <DialogHeader><DialogTitle>Tạo key admin</DialogTitle><DialogDescription>Key admin thấy và điều khiển mọi box. Key cho agent tạo ở trang agent.</DialogDescription></DialogHeader>
            {!created ? (
              <>
                <Field label="Tên key"><Input value={name} onChange={(e) => setName(e.target.value)} placeholder="vd: telegram-bot" /></Field>
                <DialogFooter><Button disabled={!name || !!pending} onClick={async () => { const k = await run("k", () => api.createKey({ name, agent_id: null, scopes: ["admin"] }), { revalidate: [paths.keys] }); if (k) setCreated(k); }}>Tạo</Button></DialogFooter>
              </>
            ) : (
              <div className="rounded-lg border border-st-warning/40 bg-st-warning/5 p-3">
                <p className="flex items-center gap-1.5 text-[13px] font-medium text-st-warning"><AlertTriangle className="size-4" /> Chỉ hiện một lần</p>
                <div className="mt-2 flex items-center gap-2"><code className="min-w-0 flex-1 truncate rounded border border-border bg-background px-2 py-1.5 font-mono text-[13px]">{created.key}</code><CopyButton text={created.key ?? ""} label="Copy key" /></div>
              </div>
            )}
          </DialogContent>
        </Dialog>
      </div>
      <Card>
        <Table>
          <THead><TR className="hover:bg-transparent"><TH>Tên</TH><TH>Prefix</TH><TH>Agent</TH><TH>Scopes</TH><TH>Dùng lần cuối</TH><TH className="text-right">Trạng thái</TH></TR></THead>
          <TBody>
            {data.items.map((k) => (
              <TR key={k.id}>
                <TD className="text-[13px]">{k.name}</TD>
                <TD><Mono>{k.prefix}…</Mono></TD>
                <TD>{agentName(k.agent_id) ? <Link href={`/agents/${k.agent_id}`} className="font-mono text-[12.5px] hover:text-primary">{agentName(k.agent_id)}</Link> : <Badge variant="yellow">admin</Badge>}</TD>
                <TD><div className="flex flex-wrap gap-1">{k.scopes.map((s) => <Badge key={s} variant="outline" className="font-mono">{s}</Badge>)}</div></TD>
                <TD className="whitespace-nowrap text-[13px] text-muted-foreground">{relTime(k.last_used_at)}</TD>
                <TD className="text-right">
                  {k.revoked_at ? <Badge variant="gray">Đã thu hồi</Badge> : (
                    <ConfirmButton size="xs" variant="outline" destructive title={`Thu hồi ${k.prefix}…?`} desc={k.scopes.includes("admin") ? "Đây là key admin. Nếu dashboard đang dùng key này anh sẽ bị đăng xuất." : "Agent dùng key này sẽ mất quyền ngay."} confirmLabel="Thu hồi"
                      onConfirm={() => run("r", () => api.revokeKey(k.id), { success: "Đã thu hồi", revalidate: [paths.keys] })}>Thu hồi</ConfirmButton>
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

const QUOTA_FIELDS: [keyof Quotas, string, string][] = [
  ["max_boxes_per_agent", "Box tối đa / agent", "box"],
  ["max_concurrent_boxes", "Box chạy đồng thời (toàn hệ thống)", "box"],
  ["max_box_cpu", "vCPU tối đa / box", "vCPU"],
  ["max_box_ram_mb", "RAM tối đa / box", "MB"],
  ["max_spend_day_usd", "Trần chi tiêu / ngày", "USD"],
  ["exec_rate_per_min", "Giới hạn exec / phút / agent", "lệnh"],
];

function QuotaForm({ initial }: { initial: Quotas }) {
  const [q, setQ] = React.useState(initial);
  const { run, pending } = useAction();
  return (
    <Card>
      <CardHeader><CardTitle>Quota & giới hạn</CardTitle><CardDescription>Chặn cứng ở Core — agent nhận lỗi POLICY_DENIED khi vượt.</CardDescription></CardHeader>
      <CardContent className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
        {QUOTA_FIELDS.map(([k, label, unit]) => (
          <Field key={k} label={label} hint={unit}>
            <Input type="number" min={0} step={k === "max_spend_day_usd" ? 0.5 : 1} value={q[k]} onChange={(e) => setQ((x) => ({ ...x, [k]: Number(e.target.value) }))} className="font-mono" />
          </Field>
        ))}
        <div className="flex justify-end sm:col-span-2 lg:col-span-3">
          <Button disabled={JSON.stringify(q) === JSON.stringify(initial) || !!pending} onClick={() => run("q", () => api.saveQuotas(q), { success: "Đã lưu quota", revalidate: [paths.adminQuotas] })}><Save /> Lưu quota</Button>
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
  if (error) return <ErrorBox error={error} />;
  if (!s) return <LoadingRows />;
  const message = msg ?? s.maintenance.message;
  const save = (next: Settings, success: string) => run("s", () => api.saveSettings(next), { success, revalidate: [paths.settings] });
  return (
    <div className="grid gap-4 lg:grid-cols-2">
      <Card>
        <CardHeader><CardTitle>Tính năng</CardTitle></CardHeader>
        <CardContent className="flex flex-col gap-2">
          <label className="flex items-center justify-between gap-3 rounded-md border border-border px-3 py-2.5 text-[13px] opacity-80">
            <span>GPU (RunPod, Vast — attachable)<span className="block text-xs text-muted-foreground">Dời khỏi v1, dự kiến ở giai đoạn P5</span></span>
            <span className="flex items-center gap-2"><Badge variant="outline" className="font-mono">P5</Badge><Switch checked={false} disabled aria-label="GPU (P5)" /></span>
          </label>
        </CardContent>
      </Card>
      <Card className={cn(s.maintenance.enabled && "border-st-warning/50")}>
        <CardHeader><CardTitle>Chế độ bảo trì</CardTitle><CardDescription>Bật lên: hiện banner trên mọi trang, Core từ chối tạo box mới (box đang chạy vẫn chạy).</CardDescription></CardHeader>
        <CardContent className="flex flex-col gap-3">
          <label className="flex items-center justify-between gap-3 rounded-md border border-border px-3 py-2.5 text-[13px]">
            <span className="font-medium">{s.maintenance.enabled ? "Đang bảo trì" : "Hoạt động bình thường"}</span>
            <Switch checked={s.maintenance.enabled} disabled={!!pending} onCheckedChange={(c) => save({ ...s, maintenance: { enabled: c, message } }, c ? "Đã bật chế độ bảo trì" : "Đã tắt chế độ bảo trì")} />
          </label>
          <Field label="Thông điệp banner"><Textarea rows={2} value={message} onChange={(e) => setMsg(e.target.value)} /></Field>
          <div className="flex justify-end"><Button size="sm" variant="secondary" disabled={message === s.maintenance.message || !!pending} onClick={() => save({ ...s, maintenance: { ...s.maintenance, message } }, "Đã lưu thông điệp")}><Save /> Lưu thông điệp</Button></div>
        </CardContent>
      </Card>
    </div>
  );
}

function UsersTab() {
  const { data, error } = useApi<List<AdminUser>>(paths.adminUsers);
  if (error) return <ErrorBox error={error} />;
  if (!data) return <LoadingRows />;
  const role = { owner: ["Chủ sở hữu", "yellow"], admin: ["Admin", "blue"], viewer: ["Chỉ xem", "gray"] } as const;
  return (
    <Card>
      <Table>
        <THead><TR className="hover:bg-transparent"><TH>Tên</TH><TH>Email</TH><TH>Vai trò</TH><TH>Đăng nhập cuối</TH><TH className="hidden md:table-cell">Tạo</TH></TR></THead>
        <TBody>
          {data.items.map((u) => (
            <TR key={u.id}>
              <TD className="text-[13px] font-medium">{u.name}</TD>
              <TD><Mono>{u.email}</Mono></TD>
              <TD><Badge variant={role[u.role][1]}>{role[u.role][0]}</Badge></TD>
              <TD className="text-[13px] text-muted-foreground">{relTime(u.last_login_at)}</TD>
              <TD className="hidden text-[13px] text-muted-foreground md:table-cell">{dateTime(u.created_at)}</TD>
            </TR>
          ))}
        </TBody>
      </Table>
    </Card>
  );
}

const TABS = [
  ["system", "Hệ thống"],
  ["jobs", "Hàng đợi job"],
  ["audit", "Audit log"],
  ["keys", "API keys"],
  ["quotas", "Quota & giới hạn"],
  ["features", "Tính năng / bảo trì"],
  ["users", "Người dùng"],
] as const;

function Inner() {
  const sp = useSearchParams();
  const router = useRouter();
  const tab = sp.get("s") ?? "system";
  return (
    <>
      <PageHeader title="Quản trị" desc="Sức khoẻ hệ thống, job, audit, quota và bảo trì." actions={<Badge variant="outline"><Activity className="size-3" /> scope admin</Badge>} />
      <Tabs value={tab} onValueChange={(v) => router.replace(`/admin?s=${v}`, { scroll: false })}>
        <TabsList>{TABS.map(([v, l]) => <TabsTrigger key={v} value={v}>{l}</TabsTrigger>)}</TabsList>
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
