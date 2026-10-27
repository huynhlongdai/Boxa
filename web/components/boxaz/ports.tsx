"use client";
// v1.7: tab Ports của box — Boxaz gateway URL theo port, quyền truy cập (public / token), URL token (tạo / liệt kê / thu hồi),
// wake_on_request, trạng thái app do supervisor chạy (restart + log SSE) và capability lifecycle của provider.
import * as React from "react";
import { useTranslations } from "next-intl";
import { useSWRConfig } from "swr";
import { AlertTriangle, ExternalLink, KeyRound, Plus, RefreshCw, RotateCcw, ScrollText, ShieldCheck, Zap } from "lucide-react";
import { api, paths } from "@/lib/api";
import { useAction, useApi } from "@/lib/hooks";
import { useFmt } from "@/lib/format";
import type { AppLogsResult, AppView, Box, CapabilityRow, List, PortsResult, PortView, URLToken, URLTokenList } from "@/lib/types";
import { cn } from "@/lib/utils";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Field, Input, Select } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog";
import { Table, TBody, TD, TH, THead, TR } from "@/components/ui/table";
import { CopyButton } from "@/components/boxaz/code-block";
import { ConfirmButton } from "@/components/boxaz/confirm";
import { Empty, ErrorBox, LoadingRows } from "@/components/boxaz/page";
import { Mono } from "@/components/boxaz/status";

const KINDS = ["provider-preview", "ingress", "tunnel", "gateway", "none"] as const;
const STATES = ["cloning", "installing", "starting", "ready", "backoff", "exited", "stopped", "sleeping", "none"] as const;
const TTLS = [3600, 86400, 604800, 2592000] as const;
const CAPS = ["memory_pause", "wake_on_http_native", "wake_via_gateway", "fork", "inbound_http"] as const;
const MAX_LOG_CHARS = 200_000;

function Row({ k, children }: { k: string; children: React.ReactNode }) {
  return (
    <div className="flex items-start justify-between gap-4 border-b border-border/60 py-1.5 text-[13px] last:border-0">
      <span className="shrink-0 text-muted-foreground">{k}</span>
      <span className="min-w-0 break-words text-right">{children}</span>
    </div>
  );
}

function stateVariant(s: string): "green" | "blue" | "yellow" | "red" | "gray" {
  if (s === "ready") return "green";
  if (["cloning", "installing", "starting"].includes(s)) return "blue";
  if (s === "backoff") return "yellow";
  if (s === "exited") return "red";
  return "gray";
}

// ---------------- Capability ----------------
export function CapabilityBadges({ provider }: { provider: string }) {
  const t = useTranslations("ports.caps");
  const f = useFmt();
  const { data } = useApi<List<CapabilityRow>>(paths.capabilities, { revalidateOnFocus: false });
  const row = data?.items.find((c) => c.provider === provider);
  if (!data) return <div className="h-5" />;
  if (!row) return <p className="text-xs text-muted-foreground">{t("unknown")}</p>;
  return (
    <div className="flex flex-col gap-1.5" data-testid="capabilities">
      <div className="flex flex-wrap gap-1">
        {CAPS.map((c) => (
          <Badge key={c} variant={row[c] ? "green" : "outline"} title={t(`${c}Hint`)} className={cn(!row[c] && "line-through decoration-muted-foreground/50")}>
            {t(c)}
          </Badge>
        ))}
        <Badge variant="outline" title={t("maxRuntimeHint")}>{row.max_runtime ? t("maxRuntime", { time: f.seconds(row.max_runtime) }) : t("noMaxRuntime")}</Badge>
      </div>
      {row.note && <p className="text-xs text-muted-foreground">{row.note}</p>}
    </div>
  );
}

// ---------------- Wake on request ----------------
function WakeCard({ box }: { box: Box }) {
  const t = useTranslations("ports");
  const { run, pending } = useAction();
  const on = box.wake_on_request !== false;
  return (
    <Card>
      <CardHeader><CardTitle className="flex items-center gap-2"><Zap className="size-4 text-primary" /> {t("lifecycle")}</CardTitle></CardHeader>
      <CardContent className="flex flex-col gap-3">
        <label className="flex items-center justify-between gap-3 rounded-md border border-border px-3 py-2 text-[13px]">
          <span className="min-w-0">{t("wake")}<span className="block text-xs text-muted-foreground">{t("wakeHint")}</span></span>
          <Switch
            checked={on}
            disabled={!!pending}
            aria-label={t("wake")}
            data-testid="wake-toggle"
            onCheckedChange={(v) => run("wake", () => api.patchBox(box.id, { wake_on_request: v }), { success: v ? t("wakeOn") : t("wakeOff"), revalidate: [paths.box(box.id)] })}
          />
        </label>
        <div>
          <p className="mb-1.5 text-xs text-muted-foreground">{t("capsTitle", { provider: box.provider })}</p>
          <CapabilityBadges provider={box.provider} />
        </div>
      </CardContent>
    </Card>
  );
}

// ---------------- Token ----------------
function IssueTokenDialog({ box, port }: { box: Box; port: number }) {
  const t = useTranslations("ports.token");
  const tc = useTranslations("common");
  const f = useFmt();
  const [open, setOpen] = React.useState(false);
  const [ttl, setTtl] = React.useState<number>(86400);
  const [label, setLabel] = React.useState("");
  const [made, setMade] = React.useState<URLToken | null>(null);
  const { run, pending } = useAction();
  return (
    <Dialog open={open} onOpenChange={(o) => { setOpen(o); if (!o) { setMade(null); setLabel(""); } }}>
      <DialogTrigger asChild>
        <Button size="xs" variant="outline" disabled={!["running", "sleeping"].includes(box.status)} data-testid={`issue-token-${port}`}><KeyRound /> {t("issue")}</Button>
      </DialogTrigger>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("title", { port })}</DialogTitle>
          <DialogDescription>{t("desc")}</DialogDescription>
        </DialogHeader>
        {made ? (
          <div className="flex flex-col gap-3 text-[13px]">
            <p className="rounded-md border border-st-warning/30 bg-st-warning/5 p-2 text-st-warning">{t("once")}</p>
            <Field label={t("shareUrl")}>
              <div className="flex items-center gap-2">
                <Input readOnly value={made.url ?? ""} className="font-mono text-[12px]" onFocus={(e) => e.currentTarget.select()} />
                <CopyButton text={made.url ?? ""} />
              </div>
            </Field>
            <Field label={t("value")}>
              <div className="flex items-center gap-2">
                <Input readOnly value={made.token ?? ""} className="font-mono text-[12px]" onFocus={(e) => e.currentTarget.select()} />
                <CopyButton text={made.token ?? ""} />
              </div>
            </Field>
            <p className="text-xs text-muted-foreground">{t("expires", { time: f.dateTime(made.expires_at) })}</p>
          </div>
        ) : (
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <Field label={t("ttl")}>
              <Select value={String(ttl)} onChange={(e) => setTtl(Number(e.target.value))} aria-label={t("ttl")}>
                {TTLS.map((s) => <option key={s} value={s}>{f.seconds(s)}</option>)}
              </Select>
            </Field>
            <Field label={t("label")}>
              <Input value={label} maxLength={80} onChange={(e) => setLabel(e.target.value)} placeholder={t("labelPh")} />
            </Field>
            <p className="text-xs text-muted-foreground sm:col-span-2">{t("switchesAccess")}</p>
          </div>
        )}
        <DialogFooter>
          {made ? (
            <Button onClick={() => setOpen(false)}>{tc("close")}</Button>
          ) : (
            <>
              <Button variant="ghost" onClick={() => setOpen(false)}>{tc("cancel")}</Button>
              <Button disabled={!!pending} onClick={async () => {
                const r = await run("tok", () => api.createPortToken(box.id, port, { ttl_s: ttl, label: label.trim() || undefined }), {
                  revalidate: [paths.boxPorts(box.id), paths.portTokens(box.id, port), paths.box(box.id), paths.boxEvents(box.id)],
                });
                if (r) setMade(r);
              }}>{t("create")}</Button>
            </>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function TokenList({ box, port }: { box: Box; port: number }) {
  const t = useTranslations("ports.token");
  const f = useFmt();
  const { run } = useAction();
  const { data, error } = useApi<URLTokenList>(paths.portTokens(box.id, port));
  if (error) return <ErrorBox error={error} />;
  if (!data) return <LoadingRows rows={2} />;
  if (!data.items.length) return <p className="py-2 text-[13px] text-muted-foreground">{t("none")}</p>;
  return (
    <div className="overflow-x-auto">
      <Table data-testid={`tokens-${port}`}>
        <THead>
          <TR><TH>{t("label")}</TH><TH>{t("prefix")}</TH><TH>{t("created")}</TH><TH>{t("expiresCol")}</TH><TH>{t("state")}</TH><TH /></TR>
        </THead>
        <TBody>
          {data.items.map((k) => {
            const state = k.revoked_at ? "revoked" : k.expired || Date.parse(k.expires_at) <= Date.now() ? "expired" : "active";
            return (
              <TR key={k.id}>
                <TD className="max-w-40 truncate">{k.label || "—"}</TD>
                <TD><Mono>{k.prefix}…</Mono></TD>
                <TD className="whitespace-nowrap text-muted-foreground">{f.relTime(k.created_at)}</TD>
                <TD className="whitespace-nowrap text-muted-foreground">{f.dateTime(k.expires_at)}</TD>
                <TD><Badge variant={state === "active" ? "green" : state === "expired" ? "gray" : "red"}>{t(`st.${state}`)}</Badge></TD>
                <TD className="text-right">
                  {state === "active" && (
                    <ConfirmButton size="xs" variant="ghost" destructive title={t("revokeTitle", { prefix: k.prefix })} desc={t("revokeDesc")} confirmLabel={t("revoke")}
                      onConfirm={() => run("revoke", () => api.revokePortToken(box.id, port, k.id), { success: t("revoked"), revalidate: [paths.portTokens(box.id, port), paths.boxEvents(box.id)] })}>
                      {t("revoke")}
                    </ConfirmButton>
                  )}
                </TD>
              </TR>
            );
          })}
        </TBody>
      </Table>
    </div>
  );
}

// ---------------- Port ----------------
function PortRow({ box, pv, inbound }: { box: Box; pv: PortView; inbound: boolean }) {
  const t = useTranslations("ports");
  const { run, pending } = useAction();
  const [showTokens, setShowTokens] = React.useState(pv.access === "token");
  const kind = (KINDS as readonly string[]).includes(pv.kind) ? pv.kind : "none";
  const tokenMode = pv.access === "token";
  const gw = pv.gateway_url || (tokenMode ? pv.url : "");
  const providerUrl = pv.kind !== "gateway" && pv.url && pv.url !== gw ? pv.url : "";
  return (
    <div className="rounded-md border border-border p-3" data-testid={`port-${pv.port}`}>
      <div className="flex flex-wrap items-center gap-2">
        <Mono className="text-[15px] font-semibold">:{pv.port}</Mono>
        <Badge variant="outline" title={t(`kindHint.${kind}`)}>{t(`kind.${kind}`)}</Badge>
        <Badge variant={tokenMode ? "yellow" : "green"}>{tokenMode ? t("access.token") : t("access.public")}</Badge>
        <span className="flex-1" />
        <label className="flex items-center gap-2 text-[13px]">
          <span className="text-muted-foreground">{t("requireToken")}</span>
          <Switch checked={tokenMode} disabled={!!pending} aria-label={t("requireToken")} data-testid={`access-${pv.port}`}
            onCheckedChange={(v) => run("acc", () => api.setPortAccess(box.id, pv.port, v ? "token" : "public"), {
              success: v ? t("nowToken", { port: pv.port }) : t("nowPublic", { port: pv.port }),
              revalidate: [paths.boxPorts(box.id), paths.portTokens(box.id, pv.port), paths.box(box.id)],
            })} />
        </label>
      </div>
      <div className="mt-2 flex flex-col gap-1.5 text-[13px]">
        <div className="flex flex-wrap items-center gap-2">
          <span className="w-28 shrink-0 text-muted-foreground">{t("boxazUrl")}</span>
          {gw ? (
            <>
              <a href={gw} target="_blank" rel="noreferrer" className="min-w-0 break-all text-primary hover:underline"><Mono>{gw}</Mono></a>
              <CopyButton text={gw} />
            </>
          ) : <span className="text-muted-foreground">—</span>}
        </div>
        {providerUrl && (
          <div className="flex flex-wrap items-center gap-2">
            <span className="w-28 shrink-0 text-muted-foreground">{t("providerUrl")}</span>
            <a href={providerUrl} target="_blank" rel="noreferrer" className="inline-flex min-w-0 items-center gap-1 break-all text-muted-foreground hover:text-foreground"><Mono>{providerUrl}</Mono> <ExternalLink className="size-3 shrink-0" /></a>
          </div>
        )}
        {tokenMode && inbound && pv.provider_preview !== "private" && (
          <p className="flex gap-1.5 rounded-md border border-st-warning/30 bg-st-warning/5 p-2 text-[12.5px] text-st-warning" data-testid="preview-caveat">
            <AlertTriangle className="mt-0.5 size-3.5 shrink-0" /> {t("previewCaveat")}
          </p>
        )}
        {pv.provider_preview === "private" && (
          <p className="flex gap-1.5 rounded-md border border-st-running/30 bg-st-running/5 p-2 text-[12.5px] text-st-running" data-testid="preview-private">
            <ShieldCheck className="mt-0.5 size-3.5 shrink-0" /> {t("previewPrivate")}
          </p>
        )}
        {pv.caveats?.length ? (
          <ul className="list-disc pl-5 text-xs text-muted-foreground">{pv.caveats.map((c) => <li key={c}>{c}</li>)}</ul>
        ) : null}
      </div>
      <div className="mt-2 flex flex-wrap items-center gap-2">
        <IssueTokenDialog box={box} port={pv.port} />
        <Button size="xs" variant="ghost" onClick={() => setShowTokens((x) => !x)} aria-expanded={showTokens}>{showTokens ? t("hideTokens") : t("showTokens")}</Button>
      </div>
      {showTokens && <div className="mt-2"><TokenList box={box} port={pv.port} /></div>}
    </div>
  );
}

function ExposeForm({ box }: { box: Box }) {
  const t = useTranslations("ports");
  const [port, setPort] = React.useState("");
  const { run, pending } = useAction();
  const n = Number(port);
  const valid = Number.isInteger(n) && n >= 1 && n <= 65535;
  return (
    <form className="flex items-center gap-2" onSubmit={async (e) => {
      e.preventDefault();
      if (!valid) return;
      const r = await run("expose", () => api.exposePort(box.id, n), { success: t("exposed", { port: n }), revalidate: [paths.boxPorts(box.id), paths.box(box.id)] });
      if (r) setPort("");
    }}>
      <Input value={port} onChange={(e) => setPort(e.target.value.replace(/\D/g, "").slice(0, 5))} placeholder="8000" inputMode="numeric" className="h-8 w-24 font-mono" aria-label={t("exposePort")} />
      <Button size="sm" variant="outline" type="submit" disabled={!valid || !!pending || box.status !== "running"}><Plus /> {t("expose")}</Button>
    </form>
  );
}

function PortsCard({ box }: { box: Box }) {
  const t = useTranslations("ports");
  const { data, error, mutate } = useApi<PortsResult>(paths.boxPorts(box.id), { refreshInterval: 30000 });
  const { data: caps } = useApi<List<CapabilityRow>>(paths.capabilities, { revalidateOnFocus: false });
  const inbound = !!caps?.items.find((c) => c.provider === box.provider)?.inbound_http;
  return (
    <Card>
      <CardHeader className="flex flex-row flex-wrap items-center justify-between gap-2 space-y-0">
        <CardTitle>{t("title")}</CardTitle>
        <ExposeForm box={box} />
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        <p className="text-xs text-muted-foreground">{t("intro")}</p>
        {error ? <ErrorBox error={error} retry={() => mutate()} /> : !data ? <LoadingRows rows={2} /> : !data.ports.length ? (
          <Empty title={t("empty")} desc={t("emptyDesc")} />
        ) : data.ports.map((pv) => <PortRow key={pv.port} box={box} pv={pv} inbound={inbound} />)}
      </CardContent>
    </Card>
  );
}

// ---------------- App ----------------
function AppLogs({ box }: { box: Box }) {
  const t = useTranslations("ports.app");
  const [text, setText] = React.useState("");
  const [follow, setFollow] = React.useState(false);
  const [live, setLive] = React.useState(false);
  const [truncated, setTruncated] = React.useState(false);
  const pre = React.useRef<HTMLPreElement>(null);
  const add = React.useCallback((r: AppLogsResult, reset = false) => {
    if (r.truncated) setTruncated(true);
    setText((cur) => {
      const next = (reset ? "" : cur) + r.data;
      return next.length > MAX_LOG_CHARS ? next.slice(next.length - MAX_LOG_CHARS) : next;
    });
  }, []);
  const load = React.useCallback(async () => {
    try {
      add(await api.appLogs(box.id), true);
    } catch {
      /* lỗi đã hiện ở card trạng thái */
    }
  }, [box.id, add]);

  React.useEffect(() => {
    if (!follow) {
      void load();
      return;
    }
    let first = true;
    const es = new EventSource(paths.boxAppLogsStream(box.id));
    es.addEventListener("open", () => setLive(true));
    es.addEventListener("log", (e) => {
      try {
        add(JSON.parse((e as MessageEvent).data) as AppLogsResult, first);
        first = false;
      } catch {
        /* bỏ qua khung hỏng */
      }
    });
    es.addEventListener("error", () => setLive(false));
    return () => {
      es.close();
      setLive(false);
    };
  }, [follow, box.id, add, load]);

  React.useEffect(() => {
    const el = pre.current;
    if (el && follow) el.scrollTop = el.scrollHeight;
  }, [text, follow]);

  return (
    <div className="flex flex-col gap-2" data-testid="app-logs">
      <div className="flex flex-wrap items-center gap-2">
        <span className="flex items-center gap-1.5 text-[13px] font-medium"><ScrollText className="size-4 text-muted-foreground" /> {t("logs")}</span>
        {follow && <Badge variant={live ? "green" : "gray"}>{live ? t("live") : t("connecting")}</Badge>}
        {truncated && <Badge variant="outline" title={t("truncatedHint")}>{t("truncated")}</Badge>}
        <span className="flex-1" />
        <label className="flex items-center gap-2 text-[13px]">
          <span className="text-muted-foreground">{t("follow")}</span>
          <Switch checked={follow} onCheckedChange={setFollow} aria-label={t("follow")} data-testid="logs-follow" />
        </label>
        {!follow && <Button size="xs" variant="ghost" onClick={() => void load()}><RefreshCw /> {t("refresh")}</Button>}
        <CopyButton text={text} />
      </div>
      <pre ref={pre} className="max-h-80 min-h-32 overflow-auto rounded-md border border-border bg-background p-3 font-mono text-[12px] leading-relaxed text-foreground/90">
        {text || <span className="text-muted-foreground">{t("noLogs")}</span>}
      </pre>
    </div>
  );
}

function AppCard({ box }: { box: Box }) {
  const t = useTranslations("ports.app");
  const f = useFmt();
  const { mutate } = useSWRConfig();
  const { run, pending } = useAction();
  const { data, error } = useApi<AppView>(paths.boxApp(box.id), { refreshInterval: (v) => (v?.status && ["cloning", "installing", "starting", "backoff"].includes(v.status.state) ? 2000 : 10000) });
  const st = data?.status;
  const state = st && (STATES as readonly string[]).includes(st.state) ? st.state : "none";
  const ready = data?.spec?.ready;
  return (
    <Card>
      <CardHeader className="flex flex-row flex-wrap items-center justify-between gap-2 space-y-0">
        <CardTitle className="flex items-center gap-2">
          {t("title")}
          {data?.spec && <Badge variant={stateVariant(state)} data-testid="app-state">{t(`state.${state}`)}</Badge>}
          {data?.spec && st?.ready && <Badge variant="green">{t("ready")}</Badge>}
        </CardTitle>
        {data?.spec && (
          <ConfirmButton size="xs" variant="outline" title={t("restartTitle")} desc={t("restartDesc")} confirmLabel={t("restart")} disabled={!!pending || box.status !== "running"} data-testid="app-restart"
            onConfirm={async () => {
              const r = await run("restart", () => api.restartApp(box.id), { success: t("restarted"), revalidate: [paths.boxEvents(box.id)] });
              if (r) await mutate(paths.boxApp(box.id), r, { revalidate: false });
            }}>
            <RotateCcw /> {t("restart")}
          </ConfirmButton>
        )}
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        {error ? <ErrorBox error={error} /> : !data ? <LoadingRows rows={3} /> : !data.spec ? (
          <Empty title={t("none")} desc={t("noneDesc")} />
        ) : (
          <>
            <div className="grid grid-cols-1 gap-x-6 md:grid-cols-2">
              <div>
                <Row k={t("start")}><Mono>{data.spec.start}</Mono></Row>
                {data.spec.repo && <Row k={t("repo")}><Mono>{data.spec.repo}{data.spec.ref ? `@${data.spec.ref}` : ""}</Mono></Row>}
                {data.spec.install && <Row k={t("install")}><Mono>{data.spec.install}</Mono></Row>}
                <Row k={t("port")}><Mono>{data.spec.port ?? "—"}</Mono></Row>
                <Row k={t("readyCheck")}><Mono>{ready?.http ? `HTTP ${ready.http}` : ready?.tcp || data.spec.port ? t("tcp") : t("started")}</Mono></Row>
                <Row k={t("restartPolicy")}><Mono>{data.spec.restart ?? "always"}</Mono></Row>
                <Row k={t("workdir")}><Mono>{data.spec.workdir ?? "/work"}</Mono></Row>
                {data.spec.env && Object.keys(data.spec.env).length > 0 && (
                  <Row k={t("env")}><span className="flex flex-wrap justify-end gap-1">{Object.keys(data.spec.env).map((k) => <Badge key={k} variant="outline" className="font-mono">{k}</Badge>)}</span></Row>
                )}
              </div>
              <div>
                <Row k={t("restarts")}><Mono>{st?.restarts ?? 0}</Mono></Row>
                <Row k={t("lastExit")}><Mono>{st?.last_exit ?? "—"}</Mono></Row>
                <Row k={t("pid")}><Mono>{st?.pid ?? "—"}</Mono></Row>
                <Row k={t("startedAt")}>{st?.started_at ? f.relTime(st.started_at) : "—"}</Row>
                <Row k={t("readyAt")}>{st?.ready_at ? f.relTime(st.ready_at) : "—"}</Row>
                <Row k={t("supervisor")}><Mono>{st?.supervisor ?? "—"}</Mono></Row>
                {st?.message && <Row k={t("message")}><span className="font-mono text-[12.5px]">{st.message}</span></Row>}
              </div>
            </div>
            <AppLogs box={box} />
          </>
        )}
      </CardContent>
    </Card>
  );
}

export function PortsTab({ box }: { box: Box }) {
  return (
    <div className="grid grid-cols-1 items-start gap-4 lg:grid-cols-3">
      <div className="flex min-w-0 flex-col gap-4 lg:col-span-2">
        <PortsCard box={box} />
        <AppCard box={box} />
      </div>
      <div className="flex min-w-0 flex-col gap-4">
        <WakeCard box={box} />
      </div>
    </div>
  );
}
