"use client";
import * as React from "react";
import { useRouter, useSearchParams } from "next/navigation";
import { FlaskConical, Plus, Save, Trash2 } from "lucide-react";
import { useTranslations } from "next-intl";
import { api, paths } from "@/lib/api";
import { useAction, useApi } from "@/lib/hooks";
import type { List, Template, ValidateResult } from "@/lib/types";
import { cn } from "@/lib/utils";
import { useFmt } from "@/lib/format";
import { Card } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Field, Input } from "@/components/ui/input";
import { ConfirmButton } from "@/components/boxmes/confirm";
import { YamlEditor } from "@/components/boxmes/yaml-editor";
import { ErrorBox, LoadingRows, PageHeader } from "@/components/boxmes/page";

const NEW_YAML = `name: moi
image: ubuntu:24.04
spec:
  cpu: 2
  ram_mb: 2048
  disk_gb: 20
lifecycle: task
idle_minutes: 15
setup:
  - echo "hello"
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
  const t = useTranslations("templates");
  const tc = useTranslations("common");
  const { relTime } = useFmt();

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
      <PageHeader title={t("title")} desc={t("desc")} actions={
        <Button size="sm" onClick={() => { setIsNew(true); setSel(null); setYaml(NEW_YAML); setName(""); setDesc(""); }}><Plus /> {t("new")}</Button>
      } />
      {error ? <ErrorBox error={error} retry={() => mutate()} /> : !data ? <LoadingRows /> : (
        <div className="grid gap-4 lg:grid-cols-[300px_1fr]">
          <Card className="h-fit p-1.5">
            <ul className="flex flex-col">
              {data.items.map((tp) => (
                <li key={tp.name}>
                  <button onClick={() => { setIsNew(false); setSel(tp.name); router.replace(`/templates?t=${encodeURIComponent(tp.name)}`, { scroll: false }); }}
                    className={cn("flex w-full flex-col gap-0.5 rounded-md px-3 py-2.5 text-left hover:bg-surface-2", sel === tp.name && !isNew && "bg-surface-2")}>
                    <span className="flex items-center justify-between gap-2"><span className="min-w-0 truncate font-mono text-[13px]">{tp.name}</span><Badge variant="outline" className="shrink-0 whitespace-nowrap">{t("uses", { n: tp.uses })}</Badge></span>
                    <span className="line-clamp-2 text-xs text-muted-foreground">{tp.description}</span>
                  </button>
                </li>
              ))}
            </ul>
          </Card>
          <div className="flex min-w-0 flex-col gap-3">
            <div className="grid gap-3 sm:grid-cols-2">
              <Field label={tc("name")}><Input value={name} disabled={!isNew} onChange={(e) => setName(e.target.value.toLowerCase().replace(/[^a-z0-9-]/g, "-"))} className="font-mono" /></Field>
              <Field label={tc("description")}><Input value={desc} onChange={(e) => setDesc(e.target.value)} /></Field>
            </div>
            <YamlEditor value={yaml} onChange={setYaml} onValidated={setValid} />
            <div className="flex flex-wrap items-center gap-2">
              <Button disabled={!dirty || !valid?.ok || !name || !!pending} onClick={async () => {
                if (isNew) {
                  const tp = await run("c", () => api.createTemplate({ name, description: desc, yaml }), { success: t("createdToast", { name }), revalidate: [paths.templates] });
                  if (tp) { setIsNew(false); setSel(tp.name); }
                } else await run("u", () => api.updateTemplate(name, { description: desc, yaml }), { success: tc("saved"), revalidate: [paths.templates] });
              }}><Save /> {tc("save")}</Button>
              <Button variant="secondary" disabled={!valid?.ok || !!pending} onClick={async () => {
                const b = await run("try", () => api.createBox({ yaml }, crypto.randomUUID()), { success: (x) => t("tryToast", { slug: x.slug }) });
                if (b) router.push(`/boxes/${b.id}`);
              }}><FlaskConical /> {t("tryBuild")}</Button>
              {current && !isNew && (
                <ConfirmButton variant="ghost" className="ml-auto text-st-error" destructive title={t("deleteTitle", { name: current.name })} desc={t("deleteDesc")} confirmLabel={tc("delete")}
                  onConfirm={async () => { const r = await run("d", () => api.deleteTemplate(current.name), { success: tc("deleted"), revalidate: [paths.templates] }); if (r) setSel(null); }}>
                  <Trash2 /> {tc("delete")}
                </ConfirmButton>
              )}
            </div>
            {current && !isNew && <p className="text-xs text-muted-foreground">{t("createdAgo", { time: relTime(current.created_at) })}</p>}
          </div>
        </div>
      )}
    </>
  );
}

export default function TemplatesPage() {
  return <React.Suspense fallback={<LoadingRows />}><Inner /></React.Suspense>;
}
