"use client";
// v1.3: trạng thái mesh headscale/WireGuard — bật/tắt (kèm gợi ý cấu hình), node, xem trước ACL/config, tạo pre-auth key.
import * as React from "react";
import { useTranslations } from "next-intl";
import { KeyRound, Network, Wifi, WifiOff } from "lucide-react";
import { api, paths } from "@/lib/api";
import { useAction, useApi } from "@/lib/hooks";
import { useFmt } from "@/lib/format";
import type { Box, Host, List, MeshACLView, MeshConfigView, MeshKeyResult, MeshView } from "@/lib/types";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Field, Input, Select } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { Table, TBody, TD, TH, THead, TR } from "@/components/ui/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog";
import { CodeBlock, CopyButton } from "./code-block";
import { ErrorBox, LoadingRows, SectionTitle } from "./page";
import { Mono } from "./status";

function KeyDialog({ disabled }: { disabled: boolean }) {
  const t = useTranslations("mesh");
  const tc = useTranslations("common");
  const { relTime } = useFmt();
  const [open, setOpen] = React.useState(false);
  const [target, setTarget] = React.useState("host:");
  const [reusable, setReusable] = React.useState(false);
  const [ttl, setTtl] = React.useState("60");
  const [res, setRes] = React.useState<MeshKeyResult | null>(null);
  const { data: hosts } = useApi<List<Host>>(open ? paths.hosts : null);
  const { data: boxes } = useApi<List<Box>>(open ? paths.boxes({ status: "running" }) : null);
  const { run, pending } = useAction();
  return (
    <Dialog open={open} onOpenChange={(o) => { setOpen(o); if (!o) setRes(null); }}>
      <DialogTrigger asChild><Button size="sm" data-mesh-key-open disabled={disabled}><KeyRound /> {t("createKey")}</Button></DialogTrigger>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>{t("keyTitle")}</DialogTitle>
          <DialogDescription>{t("keyDesc")}</DialogDescription>
        </DialogHeader>
        {res ? (
          <div className="flex min-w-0 flex-col gap-3 text-[13px]" data-mesh-key>
            <p className="text-st-running">{t("keyCreated", { time: relTime(res.expires_at) })}</p>
            <div className="flex min-w-0 items-center gap-2 rounded-md border border-border bg-surface-2/50 p-2">
              <Mono className="min-w-0 flex-1 break-all">{res.key}</Mono>
              <CopyButton text={res.key} />
            </div>
            <div className="flex flex-wrap gap-1">{res.tags.map((x) => <Badge key={x} variant="outline" className="font-mono">{x}</Badge>)}{res.ephemeral && <Badge variant="yellow">{t("ephemeral")}</Badge>}</div>
            <p className="text-xs text-muted-foreground">{t("runOnNode")}</p>
            <CodeBlock code={res.command} language="bash" />
            <p className="text-xs text-st-warning">{t("keyOnce")}</p>
          </div>
        ) : (
          <div className="flex flex-col gap-3">
            <Field label={t("target")}>
              <Select value={target} onChange={(e) => setTarget(e.target.value)}>
                <option value="host:">{t("newHost")}</option>
                <optgroup label={t("hosts")}>{hosts?.items.map((h) => <option key={h.id} value={`host:${h.id}`}>{h.id} · {h.provider}/{h.region}</option>)}</optgroup>
                <optgroup label={t("boxes")}>{boxes?.items.map((b) => <option key={b.id} value={`box:${b.id}`}>{b.slug}</option>)}</optgroup>
              </Select>
            </Field>
            <div className="grid gap-3 sm:grid-cols-2">
              <Field label={t("ttl")} hint={t("ttlHint")}><Input type="number" min={1} max={10080} value={ttl} onChange={(e) => setTtl(e.target.value)} /></Field>
              <label className="flex items-center justify-between gap-3 self-end rounded-md border border-border px-3 py-2 text-[13px]">
                {t("reusable")}<Switch checked={reusable} onCheckedChange={setReusable} aria-label={t("reusable")} />
              </label>
            </div>
          </div>
        )}
        <DialogFooter>
          <Button variant="ghost" onClick={() => setOpen(false)}>{tc("close")}</Button>
          {!res && (
            <Button disabled={!!pending} onClick={async () => {
              const [kind, id] = target.split(":");
              const r = await run("k", () => api.meshKey({ ...(kind === "box" ? { box_id: id } : id ? { host_id: id } : {}), reusable, ttl_minutes: Number(ttl) || 60 }), { revalidate: [paths.mesh] });
              if (r) setRes(r);
            }}><KeyRound /> {pending ? tc("processing") : t("create")}</Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

export function MeshView() {
  const t = useTranslations("mesh");
  const { relTime } = useFmt();
  const { data, error, mutate } = useApi<MeshView>(paths.mesh, { refreshInterval: 30000 });
  const { data: cfg } = useApi<MeshConfigView>(paths.meshConfig, { refreshInterval: 0 });
  const { data: acl } = useApi<MeshACLView>(paths.meshAcl, { refreshInterval: 0 });
  if (error) return <ErrorBox error={error} retry={() => mutate()} />;
  if (!data) return <LoadingRows rows={3} />;
  return (
    <>
      <SectionTitle right={<KeyDialog disabled={!data.enabled} />}>
        <span className="inline-flex items-center gap-1.5"><Network className="size-4 text-muted-foreground" /> {t("title")}</span>
      </SectionTitle>
      <p className="mb-3 max-w-3xl text-[13px] text-muted-foreground">{t("desc")}</p>
      {!data.enabled ? (
        <Card className="mb-6 border-dashed p-4 text-[13px]" data-mesh-disabled>
          <p className="flex items-center gap-1.5 font-medium"><WifiOff className="size-4 text-muted-foreground" /> {t("disabled")}</p>
          <p className="mt-1 text-muted-foreground">{data.hint || t("disabledHint")}</p>
          <CodeBlock className="mt-3" language="bash" code={"BOXAZ_MESH_URL=https://mesh.example.com\nBOXAZ_MESH_API_KEY=…   # headscale apikeys create\nBOXAZ_MESH_USER=boxaz"} />
        </Card>
      ) : (
        <div className="mb-4 grid grid-cols-2 gap-3 md:grid-cols-4">
          {([[t("server"), <Mono key="u" className="break-all">{data.url}</Mono>], [t("online"), `${data.online}/${data.nodes.length}`], [t("hostsJoined"), `${data.hosts_joined}/${data.hosts}`], [t("aclRules"), String(data.acl_rules)]] as const).map(([k, v]) => (
            <Card key={k} className="min-w-0 p-3">
              <p className="text-xs text-muted-foreground">{k}</p>
              <div className="mt-0.5 font-mono text-[14px]">{v}</div>
            </Card>
          ))}
        </div>
      )}
      {data.error && <p className="mb-3 text-[13px] text-st-error">{data.error}</p>}
      {data.enabled && (
        <Card className="mb-4">
          <Table>
            <THead><TR className="hover:bg-transparent"><TH>{t("node")}</TH><TH className="hidden sm:table-cell">{t("ips")}</TH><TH>{t("tags")}</TH><TH>{t("lastSeen")}</TH></TR></THead>
            <TBody>
              {data.nodes.map((n) => (
                <TR key={n.id}>
                  <TD className="whitespace-nowrap">{n.online ? <Wifi className="mr-1 inline size-3.5 text-st-running" /> : <WifiOff className="mr-1 inline size-3.5 text-muted-foreground" />}<Mono>{n.givenName || n.name}</Mono></TD>
                  <TD className="hidden sm:table-cell"><Mono className="text-muted-foreground">{n.ipAddresses[0]}</Mono></TD>
                  <TD><span className="flex flex-wrap gap-1">{n.forcedTags.map((x) => <Badge key={x} variant="outline" className="font-mono">{x}</Badge>)}</span></TD>
                  <TD className="whitespace-nowrap text-[12.5px]">{n.online ? t("now") : relTime(n.lastSeen)}</TD>
                </TR>
              ))}
            </TBody>
          </Table>
        </Card>
      )}
      <Card className="mb-6 min-w-0">
        <CardHeader><CardTitle>{t("preview")}</CardTitle></CardHeader>
        <CardContent className="min-w-0">
          {acl && acl.notes.length > 0 && <ul className="mb-3 list-disc pl-4 text-[12.5px] text-st-warning">{acl.notes.map((n) => <li key={n}>{n}</li>)}</ul>}
          {cfg ? (
            <Tabs defaultValue="acl">
              <TabsList>
                <TabsTrigger value="acl">acl.json</TabsTrigger>
                <TabsTrigger value="config">config.yaml</TabsTrigger>
                <TabsTrigger value="derp">derp.yaml</TabsTrigger>
              </TabsList>
              <TabsContent value="acl"><CodeBlock code={cfg.acl} language="json" maxHeight={300} /><p className="mt-1 text-xs text-muted-foreground"><Mono>{cfg.paths.acl}</Mono></p></TabsContent>
              <TabsContent value="config"><CodeBlock code={cfg.config} language="yaml" maxHeight={300} /><p className="mt-1 text-xs text-muted-foreground"><Mono>{cfg.paths.config}</Mono></p></TabsContent>
              <TabsContent value="derp"><CodeBlock code={cfg.derp} language="yaml" maxHeight={300} /><p className="mt-1 text-xs text-muted-foreground"><Mono>{cfg.paths.derp}</Mono></p></TabsContent>
            </Tabs>
          ) : <LoadingRows rows={2} />}
        </CardContent>
      </Card>
    </>
  );
}
