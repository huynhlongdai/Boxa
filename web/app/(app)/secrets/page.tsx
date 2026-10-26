"use client";
import * as React from "react";
import { EyeOff, Plus, RefreshCw, Trash2 } from "lucide-react";
import { api, paths } from "@/lib/api";
import { useAction, useApi } from "@/lib/hooks";
import type { Agent, List, Secret } from "@/lib/types";
import { dateTime, relTime } from "@/lib/utils";
import { Card } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Field, Input, Textarea } from "@/components/ui/input";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog";
import { Table, TBody, TD, TH, THead, TR } from "@/components/ui/table";
import { ConfirmButton } from "@/components/boxa/confirm";
import { Empty, ErrorBox, LoadingRows, PageHeader } from "@/components/boxa/page";
import { Mono } from "@/components/boxa/status";

function AddSecret({ agents }: { agents: Agent[] }) {
  const [open, setOpen] = React.useState(false);
  const [name, setName] = React.useState("");
  const [value, setValue] = React.useState("");
  const [sel, setSel] = React.useState<string[]>([]);
  const { run, pending } = useAction();
  const reset = () => { setName(""); setValue(""); setSel([]); };
  return (
    <Dialog open={open} onOpenChange={(o) => { setOpen(o); if (!o) reset(); }}>
      <DialogTrigger asChild><Button size="sm"><Plus /> Thêm secret</Button></DialogTrigger>
      <DialogContent>
        <DialogHeader><DialogTitle>Thêm secret</DialogTitle><DialogDescription>Giá trị được mã hoá và không bao giờ hiện lại trên giao diện.</DialogDescription></DialogHeader>
        <Field label="Tên" hint="A-Z, 0-9, dấu gạch dưới"><Input value={name} onChange={(e) => setName(e.target.value.toUpperCase().replace(/[^A-Z0-9_]/g, "_"))} placeholder="SHOPEE_COOKIE" className="font-mono" /></Field>
        <Field label="Giá trị"><Textarea value={value} onChange={(e) => setValue(e.target.value)} className="font-mono" rows={3} autoComplete="off" spellCheck={false} /></Field>
        <div className="flex flex-col gap-1.5">
          <span className="text-[13px] font-medium">Agent được dùng</span>
          <div className="flex flex-wrap gap-1.5">
            {agents.map((a) => (
              <button key={a.id} type="button" onClick={() => setSel((x) => (x.includes(a.id) ? x.filter((y) => y !== a.id) : [...x, a.id]))}
                className={`rounded-full border px-2.5 py-1 font-mono text-[12px] ${sel.includes(a.id) ? "border-primary/60 bg-primary/15 text-primary" : "border-border text-muted-foreground"}`}>{a.name}</button>
            ))}
          </div>
        </div>
        <DialogFooter>
          <Button variant="ghost" onClick={() => setOpen(false)}>Huỷ</Button>
          <Button disabled={!name || !value || !!pending} onClick={async () => {
            const r = await run("c", () => api.createSecret({ name, value, agents: sel }), { success: `Đã lưu ${name}`, revalidate: [paths.secrets] });
            if (r) { setOpen(false); reset(); }
          }}>Lưu</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function RotateSecret({ s }: { s: Secret }) {
  const [open, setOpen] = React.useState(false);
  const [value, setValue] = React.useState("");
  const { run, pending } = useAction();
  return (
    <Dialog open={open} onOpenChange={(o) => { setOpen(o); if (!o) setValue(""); }}>
      <DialogTrigger asChild><Button size="xs" variant="outline"><RefreshCw /> Xoay vòng</Button></DialogTrigger>
      <DialogContent>
        <DialogHeader><DialogTitle>Xoay vòng <span className="font-mono">{s.name}</span></DialogTitle><DialogDescription>Box mới nhận giá trị mới ngay; box đang chạy nhận khi được đánh thức lại.</DialogDescription></DialogHeader>
        <Field label="Giá trị mới"><Textarea value={value} onChange={(e) => setValue(e.target.value)} className="font-mono" rows={3} autoComplete="off" spellCheck={false} /></Field>
        <DialogFooter>
          <Button variant="ghost" onClick={() => setOpen(false)}>Huỷ</Button>
          <Button disabled={!value || !!pending} onClick={async () => { const r = await run("r", () => api.rotateSecret(s.name, value), { success: "Đã xoay vòng", revalidate: [paths.secrets] }); if (r) setOpen(false); }}>Lưu giá trị mới</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

export default function SecretsPage() {
  const { data, error, mutate } = useApi<List<Secret>>(paths.secrets);
  const { data: agents } = useApi<List<Agent>>(paths.agents);
  const { run } = useAction();
  const name = (id: string) => agents?.items.find((a) => a.id === id)?.name ?? id;
  return (
    <>
      <PageHeader title="Secrets" desc="Chỉ hiện tên. Giá trị không bao giờ rời khỏi Core." actions={<AddSecret agents={agents?.items ?? []} />} />
      <p className="mb-4 flex items-center gap-1.5 text-[13px] text-muted-foreground"><EyeOff className="size-4" /> Secret được gắn vào box qua biến môi trường khi agent khai báo <span className="font-mono">secrets: [TÊN]</span>.</p>
      {error ? <ErrorBox error={error} retry={() => mutate()} /> : !data ? <LoadingRows /> : !data.items.length ? <Empty title="Chưa có secret" /> : (
        <Card>
          <Table>
            <THead><TR className="hover:bg-transparent"><TH>Tên</TH><TH>Agent được dùng</TH><TH className="hidden md:table-cell">Tạo</TH><TH>Xoay vòng</TH><TH className="text-right">Thao tác</TH></TR></THead>
            <TBody>
              {data.items.map((s) => (
                <TR key={s.name}>
                  <TD><Mono>{s.name}</Mono><div className="font-mono text-[11px] text-muted-foreground">••••••••</div></TD>
                  <TD><div className="flex flex-wrap gap-1">{s.agents.length ? s.agents.map((a) => <Badge key={a} variant="outline" className="font-mono">{name(a)}</Badge>) : <span className="text-xs text-muted-foreground">chưa gán</span>}</div></TD>
                  <TD className="hidden whitespace-nowrap text-[13px] text-muted-foreground md:table-cell">{dateTime(s.created_at)}</TD>
                  <TD className="whitespace-nowrap text-[13px] text-muted-foreground">{s.rotated_at ? relTime(s.rotated_at) : "chưa"}</TD>
                  <TD>
                    <div className="flex justify-end gap-1">
                      <RotateSecret s={s} />
                      <ConfirmButton size="xs" variant="outline" destructive title={`Xoá ${s.name}?`} desc="Box mới sẽ không còn biến này. Box đang chạy giữ giá trị cũ tới khi ngủ." confirmLabel="Xoá" typeToConfirm={s.name}
                        onConfirm={() => run("d", () => api.deleteSecret(s.name), { success: "Đã xoá", revalidate: [paths.secrets] })}>
                        <Trash2 />
                      </ConfirmButton>
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
