"use client";
import * as React from "react";
import { useRouter, useSearchParams } from "next/navigation";
import { FlaskConical, Plus, Save, Trash2 } from "lucide-react";
import { api, paths } from "@/lib/api";
import { useAction, useApi } from "@/lib/hooks";
import type { List, Template, ValidateResult } from "@/lib/types";
import { cn, relTime } from "@/lib/utils";
import { Card } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Field, Input } from "@/components/ui/input";
import { ConfirmButton } from "@/components/boxa/confirm";
import { YamlEditor } from "@/components/boxa/yaml-editor";
import { ErrorBox, LoadingRows, PageHeader } from "@/components/boxa/page";

const NEW_YAML = `name: moi
image: ubuntu:24.04
spec:
  cpu: 2
  ram_mb: 2048
  disk_gb: 20
lifecycle: task
idle_minutes: 15
setup:
  - echo "xin chào"
`;

function Inner() {
  const sp = useSearchParams();
  const router = useRouter();
  const { data, error, mutate } = useApi<List<Template>>(paths.templates);
  const [sel, setSel] = React.useState<string | null>(sp.get("t"));
  const [isNew, setIsNew] = React.useState(false);
  const [yaml, setYaml] = React.useState("");
  const [name, setName] = React.useState("");
  const [desc, setDesc] = React.useState("");
  const [valid, setValid] = React.useState<ValidateResult | null>(null);
  const { run, pending } = useAction();

  const current = data?.items.find((t) => t.name === sel) ?? null;
  React.useEffect(() => {
    if (!data) return;
    if (!sel && !isNew && data.items[0]) setSel(data.items[0].name);
  }, [data, sel, isNew]);
  React.useEffect(() => {
    if (current && !isNew) {
      setYaml(current.yaml);
      setName(current.name);
      setDesc(current.description);
    }
  }, [current, isNew]);

  const dirty = isNew || (current && (yaml !== current.yaml || desc !== current.description));

  return (
    <>
      <PageHeader title="Templates" desc="Mẫu box.yaml dùng lại. Agent gọi box_up với template: <tên>." actions={
        <Button size="sm" onClick={() => { setIsNew(true); setSel(null); setYaml(NEW_YAML); setName(""); setDesc(""); }}><Plus /> Template mới</Button>
      } />
      {error ? <ErrorBox error={error} retry={() => mutate()} /> : !data ? <LoadingRows /> : (
        <div className="grid gap-4 lg:grid-cols-[300px_1fr]">
          <Card className="h-fit p-1.5">
            <ul className="flex flex-col">
              {data.items.map((t) => (
                <li key={t.name}>
                  <button onClick={() => { setIsNew(false); setSel(t.name); router.replace(`/templates?t=${encodeURIComponent(t.name)}`, { scroll: false }); }}
                    className={cn("flex w-full flex-col gap-0.5 rounded-md px-3 py-2.5 text-left hover:bg-surface-2", sel === t.name && !isNew && "bg-surface-2")}>
                    <span className="flex items-center justify-between gap-2"><span className="font-mono text-[13px]">{t.name}</span><Badge variant="outline">{t.uses} lần dùng</Badge></span>
                    <span className="line-clamp-2 text-xs text-muted-foreground">{t.description}</span>
                  </button>
                </li>
              ))}
            </ul>
          </Card>
          <div className="flex min-w-0 flex-col gap-3">
            <div className="grid gap-3 sm:grid-cols-2">
              <Field label="Tên"><Input value={name} disabled={!isNew} onChange={(e) => setName(e.target.value.toLowerCase().replace(/[^a-z0-9-]/g, "-"))} className="font-mono" /></Field>
              <Field label="Mô tả"><Input value={desc} onChange={(e) => setDesc(e.target.value)} /></Field>
            </div>
            <YamlEditor value={yaml} onChange={setYaml} onValidated={setValid} />
            <div className="flex flex-wrap items-center gap-2">
              <Button disabled={!dirty || !valid?.ok || !name || !!pending} onClick={async () => {
                if (isNew) {
                  const t = await run("c", () => api.createTemplate({ name, description: desc, yaml }), { success: `Đã tạo ${name}`, revalidate: [paths.templates] });
                  if (t) { setIsNew(false); setSel(t.name); }
                } else await run("u", () => api.updateTemplate(name, { description: desc, yaml }), { success: "Đã lưu", revalidate: [paths.templates] });
              }}><Save /> Lưu</Button>
              <Button variant="secondary" disabled={!valid?.ok || !!pending} onClick={async () => {
                const b = await run("try", () => api.createBox({ yaml }, crypto.randomUUID()), { success: (x) => `Đang dựng thử ${x.slug}` });
                if (b) router.push(`/boxes/${b.id}`);
              }}><FlaskConical /> Dựng thử</Button>
              {current && !isNew && (
                <ConfirmButton variant="ghost" className="ml-auto text-st-error" destructive title={`Xoá template ${current.name}?`} desc="Box đang chạy không bị ảnh hưởng; agent sẽ không tạo box mới từ template này được nữa." confirmLabel="Xoá"
                  onConfirm={async () => { const r = await run("d", () => api.deleteTemplate(current.name), { success: "Đã xoá", revalidate: [paths.templates] }); if (r) setSel(null); }}>
                  <Trash2 /> Xoá
                </ConfirmButton>
              )}
            </div>
            {current && !isNew && <p className="text-xs text-muted-foreground">Tạo {relTime(current.created_at)}</p>}
          </div>
        </div>
      )}
    </>
  );
}

export default function TemplatesPage() {
  return <React.Suspense fallback={<LoadingRows />}><Inner /></React.Suspense>;
}
