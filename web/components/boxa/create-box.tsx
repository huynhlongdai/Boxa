"use client";
import * as React from "react";
import { useRouter } from "next/navigation";
import { Plus } from "lucide-react";
import { api, paths } from "@/lib/api";
import { startsWith, useAction, useApi } from "@/lib/hooks";
import type { Agent, Lifecycle, List, Template } from "@/lib/types";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog";
import { Field, Input, Select } from "@/components/ui/input";

export function CreateBoxDialog() {
  const [open, setOpen] = React.useState(false);
  const { data: agents } = useApi<List<Agent>>(open ? paths.agents : null);
  const { data: templates } = useApi<List<Template>>(open ? paths.templates : null);
  const [agentId, setAgent] = React.useState("");
  const [template, setTemplate] = React.useState("python");
  const [lifecycle, setLc] = React.useState<Lifecycle>("task");
  const [slug, setSlug] = React.useState("");
  const [cpu, setCpu] = React.useState(2);
  const [ram, setRam] = React.useState(2048);
  const { run, pending } = useAction();
  const router = useRouter();
  const idem = React.useMemo(() => (open ? crypto.randomUUID() : ""), [open]);

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button size="sm"><Plus /> Tạo box</Button>
      </DialogTrigger>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Tạo box thủ công</DialogTitle>
          <DialogDescription>Thường agent tự tạo box. Dùng khi anh cần thử nhanh hoặc cứu hộ.</DialogDescription>
        </DialogHeader>
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label="Agent">
            <Select value={agentId} onChange={(e) => setAgent(e.target.value)}>
              <option value="">— chọn agent —</option>
              {agents?.items.map((a) => <option key={a.id} value={a.id}>{a.name}</option>)}
            </Select>
          </Field>
          <Field label="Template">
            <Select value={template} onChange={(e) => setTemplate(e.target.value)}>
              {(templates?.items ?? [{ name: "python" } as Template]).map((t) => <option key={t.name} value={t.name}>{t.name}</option>)}
            </Select>
          </Field>
          <Field label="Lifecycle">
            <Select value={lifecycle} onChange={(e) => setLc(e.target.value as Lifecycle)}>
              <option value="task">task — xong là xoá</option>
              <option value="session">session — ngủ khi rảnh</option>
              <option value="service">service — chạy 24/7</option>
            </Select>
          </Field>
          <Field label="Slug (tuỳ chọn)">
            <Input value={slug} onChange={(e) => setSlug(e.target.value.toLowerCase().replace(/[^a-z0-9-]/g, "-"))} placeholder="vd: thu-crawler" className="font-mono" />
          </Field>
          <Field label="vCPU">
            <Select value={cpu} onChange={(e) => setCpu(Number(e.target.value))}>{[1, 2, 4, 8].map((n) => <option key={n} value={n}>{n}</option>)}</Select>
          </Field>
          <Field label="RAM">
            <Select value={ram} onChange={(e) => setRam(Number(e.target.value))}>{[1024, 2048, 4096, 8192].map((n) => <option key={n} value={n}>{n / 1024} GB</option>)}</Select>
          </Field>
        </div>
        <DialogFooter>
          <Button variant="ghost" onClick={() => setOpen(false)}>Huỷ</Button>
          <Button
            disabled={!agentId || !!pending}
            onClick={async () => {
              const b = await run("create", () => api.createBox({ agent_id: agentId, template, lifecycle, slug: slug || undefined, spec: { cpu, ram_mb: ram, disk_gb: 20 } }, idem), { success: (x) => `Đang tạo ${x.slug}`, revalidate: [startsWith("/boxes"), "/overview"] });
              if (b) { setOpen(false); router.push(`/boxes/${b.id}`); }
            }}
          >
            {pending ? "Đang tạo…" : "Tạo box"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
