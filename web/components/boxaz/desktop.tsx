"use client";
// v1.11 tab "Màn hình": desktop của box qua VNC. Trang noVNC của core (/.boxaz/desktop?ticket=…) được nhúng bằng
// iframe; ticket viewer một lần (60 s) lấy qua proxy Next (POST /boxes/{id}/desktop) nên API key luôn ở phía server.
// Bật / tắt desktop, chế độ điều khiển hoặc chỉ xem, kết nối lại bằng ticket mới, mở tab riêng, toàn màn hình.
// Thẻ share link: tạo (TTL, chỉ xem / điều khiển), copy URL (chỉ hiện một lần), danh sách, thu hồi từng link / tất cả.
// MOCK MODE: không có WebSocket RFB thật → màn hình mô phỏng + ghi chú; trạng thái, ticket, share link là mock thật.
import * as React from "react";
import { useTranslations } from "next-intl";
import { Eye, ExternalLink, Maximize2, Monitor, MousePointer2, Play, Plus, RefreshCw, Share2, ShieldOff, Square } from "lucide-react";
import { api, paths } from "@/lib/api";
import { useAction, useApi } from "@/lib/hooks";
import { useFmt } from "@/lib/format";
import type { Box, DesktopSession, DesktopShare, DesktopState, List } from "@/lib/types";
import { useLive } from "@/components/live";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Field, Select } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { Table, TBody, TD, TH, THead, TR } from "@/components/ui/table";
import { CopyButton } from "@/components/boxaz/code-block";
import { ConfirmButton } from "@/components/boxaz/confirm";
import { Empty, ErrorBox, LoadingRows } from "@/components/boxaz/page";
import { Dot, Mono } from "@/components/boxaz/status";

const SHARE_TTLS = [900, 3600, 4 * 3600, 86400] as const;

function fmtTtl(s: number, t: ReturnType<typeof useTranslations>) {
  return s >= 3600 ? t("hours", { n: s / 3600 }) : t("minutes", { n: s / 60 });
}

type ShareState = "active" | "expired" | "revoked";
function shareState(x: DesktopShare): ShareState {
  if (x.revoked_at) return "revoked";
  return Date.parse(x.expires_at) > Date.now() ? "active" : "expired";
}

// ---------------- Màn hình mô phỏng (mock) ----------------
function MockScreen({ box, viewOnly, w, h }: { box: Box; viewOnly: boolean; w: number; h: number }) {
  const t = useTranslations("desktop");
  return (
    <div className="relative flex h-full w-full flex-col bg-gradient-to-br from-slate-800 via-slate-900 to-slate-950 text-slate-200" data-testid="desktop-mock">
      <div className="flex h-7 items-center gap-2 bg-black/40 px-3 font-mono text-[11px] text-slate-400">
        <span>openbox</span><span>·</span><span>{w}×{h}</span><span className="ml-auto">{box.slug}</span>
      </div>
      <div className="m-auto w-[min(520px,85%)] overflow-hidden rounded-md border border-white/10 bg-slate-950/80 shadow-2xl">
        <div className="flex items-center gap-1.5 border-b border-white/10 px-3 py-1.5 text-[11px] text-slate-400">
          <span className="size-2.5 rounded-full bg-red-400/70" /><span className="size-2.5 rounded-full bg-yellow-400/70" /><span className="size-2.5 rounded-full bg-green-400/70" />
          <span className="ml-2 font-mono">chromium — example.com</span>
        </div>
        <div className="p-6 text-center">
          <p className="text-lg font-semibold">Example Domain</p>
          <p className="mt-2 text-xs text-slate-400">{t("mockScreen")}</p>
        </div>
      </div>
      {viewOnly && <span className="absolute bottom-2 right-3 rounded bg-black/50 px-2 py-0.5 text-[11px]">{t("viewOnly")}</span>}
    </div>
  );
}

// ---------------- Viewer ----------------
function Viewer({ box, state }: { box: Box; state: DesktopState }) {
  const t = useTranslations("desktop");
  const { session } = useLive();
  const mock = session?.mock ?? false;
  const { run, pending } = useAction();
  const [viewOnly, setViewOnly] = React.useState(false);
  const [sess, setSess] = React.useState<DesktopSession | null>(null);
  const [nonce, setNonce] = React.useState(0);
  const frameRef = React.useRef<HTMLDivElement>(null);
  const revalidate = [paths.boxDesktop(box.id), paths.boxAccess(box.id), paths.boxTickets(box.id), paths.boxBrowser(box.id)];

  const connect = React.useCallback(async (vo: boolean) => {
    const r = await run("desktop-open", () => api.openDesktop(box.id, { view_only: vo }), { revalidate });
    if (r) { setSess(r); setNonce((n) => n + 1); }
    return r;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [box.id, run]);

  const openTab = async () => {
    const w = window.open("about:blank", "_blank");
    const r = await run("desktop-tab", () => api.openDesktop(box.id, { view_only: viewOnly }), { revalidate });
    if (!w) return;
    if (r && !mock) w.location.href = r.viewer_url;
    else w.close();
  };

  const fullscreen = () => frameRef.current?.requestFullscreen?.().catch(() => undefined);
  const w = sess?.width || state.width || 1280;
  const h = sess?.height || state.height || 800;

  return (
    <Card>
      <CardHeader className="flex flex-row flex-wrap items-start justify-between gap-2">
        <div className="min-w-0">
          <CardTitle className="flex items-center gap-2"><Monitor className="size-4 text-primary" /> {t("screen")}</CardTitle>
          <CardDescription>{t("screenDesc")}</CardDescription>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <label className="flex items-center gap-2 text-[13px]">
            <Switch checked={viewOnly} onCheckedChange={(v) => { setViewOnly(v); if (sess) connect(v); }} data-testid="desktop-view-only" />
            {viewOnly ? <><Eye className="size-3.5" /> {t("viewOnly")}</> : <><MousePointer2 className="size-3.5" /> {t("interactive")}</>}
          </label>
          {sess ? (
            <>
              <Button size="sm" variant="outline" disabled={!!pending} onClick={() => connect(viewOnly)} data-testid="desktop-reconnect"><RefreshCw /> {t("reconnect")}</Button>
              <Button size="sm" variant="ghost" onClick={fullscreen} aria-label={t("fullscreen")}><Maximize2 /></Button>
              <Button size="sm" variant="ghost" disabled={!!pending} onClick={openTab} aria-label={t("openTab")}><ExternalLink /></Button>
            </>
          ) : (
            <Button size="sm" disabled={!!pending} onClick={() => connect(viewOnly)} data-testid="desktop-connect">
              <Play /> {state.running ? t("connect") : t("startConnect")}
            </Button>
          )}
        </div>
      </CardHeader>
      <CardContent className="flex flex-col gap-2">
        <div ref={frameRef} className="relative w-full overflow-hidden rounded-md border border-border bg-black" style={{ aspectRatio: `${w} / ${h}` }} data-testid="desktop-frame">
          {!sess ? (
            <div className="flex h-full flex-col items-center justify-center gap-2 p-6 text-center text-[13px] text-slate-300">
              <Monitor className="size-8 text-slate-500" />
              <p>{state.running ? t("idleRunning") : t("idleStopped")}</p>
              <p className="text-xs text-slate-500">{t("firstStart")}</p>
            </div>
          ) : mock ? (
            <MockScreen box={box} viewOnly={sess.view_only} w={w} h={h} />
          ) : (
            <iframe
              key={nonce}
              src={sess.viewer_url}
              title={t("screen")}
              className="absolute inset-0 h-full w-full border-0"
              allow="clipboard-read; clipboard-write; fullscreen"
              referrerPolicy="no-referrer"
            />
          )}
        </div>
        {sess && (
          <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
            <Badge variant={sess.view_only ? "gray" : "blue"}>{sess.view_only ? t("viewOnly") : t("interactive")}</Badge>
            {sess.started && sess.start_ms ? <span>{t("startedIn", { ms: sess.start_ms })}</span> : null}
            <span>{t("ticketNote")}</span>
          </div>
        )}
        {mock && <p className="text-xs text-muted-foreground" data-testid="desktop-mock-notice">{t("mockNotice")}</p>}
      </CardContent>
    </Card>
  );
}

// ---------------- Trạng thái desktop ----------------
function StatusCard({ box, state }: { box: Box; state: DesktopState }) {
  const t = useTranslations("desktop");
  const { run, pending } = useAction();
  const revalidate = [paths.boxDesktop(box.id), paths.boxAccess(box.id), paths.boxBrowser(box.id), paths.boxDesktopShares(box.id)];
  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <Dot tone={state.running ? "green" : "gray"} pulse={state.running && state.viewers > 0} /> {state.running ? t("running") : t("stopped")}
        </CardTitle>
        <CardDescription>{t("statusDesc", { idle: Math.round(state.idle_stop_s / 60) })}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3 text-[13px]" data-testid="desktop-status">
        <dl className="grid grid-cols-2 gap-x-3 gap-y-1.5">
          <dt className="text-muted-foreground">{t("resolution")}</dt><dd><Mono>{state.width && state.height ? `${state.width}×${state.height}` : "—"}</Mono></dd>
          <dt className="text-muted-foreground">{t("display")}</dt><dd><Mono>{state.display || "—"}</Mono></dd>
          <dt className="text-muted-foreground">{t("server")}</dt><dd><Mono>{[state.vnc_server, state.wm].filter(Boolean).join(" · ") || "—"}</Mono></dd>
          <dt className="text-muted-foreground">{t("viewers")}</dt><dd>{state.viewers}</dd>
          <dt className="text-muted-foreground">{t("activeShares")}</dt><dd>{state.shares}</dd>
          <dt className="text-muted-foreground">{t("browserOnDesktop")}</dt><dd>{state.browser_on_desktop ? t("yes") : t("no")}</dd>
        </dl>
        {state.note && <p className="text-xs text-muted-foreground">{state.note}</p>}
        <div className="flex flex-wrap gap-2">
          {state.running ? (
            <ConfirmButton
              size="sm" variant="outline" destructive title={t("stopTitle")} desc={t("stopDesc")} confirmLabel={t("stop")} data-testid="desktop-stop"
              onConfirm={() => run("desktop-stop", () => api.stopDesktop(box.id), { success: t("stoppedToast"), revalidate })}
            >
              <Square /> {t("stop")}
            </ConfirmButton>
          ) : (
            <Button size="sm" variant="outline" disabled={!!pending} data-testid="desktop-start"
              onClick={() => run("desktop-start", () => api.openDesktop(box.id, { view_only: true }), { success: (r) => (r.start_ms ? t("startedIn", { ms: r.start_ms }) : t("running")), revalidate })}>
              <Play /> {t("start")}
            </Button>
          )}
        </div>
        <p className="text-xs text-muted-foreground">{t("cli")}: <Mono>box desktop {box.subdomain || box.slug}</Mono></p>
      </CardContent>
    </Card>
  );
}

// ---------------- Share link ----------------
function SharesCard({ box }: { box: Box }) {
  const t = useTranslations("desktop");
  const f = useFmt();
  const { run, pending } = useAction();
  const { data, error, mutate } = useApi<List<DesktopShare>>(paths.boxDesktopShares(box.id), { refreshInterval: 15000 });
  const [ttl, setTtl] = React.useState<number>(3600);
  const [viewOnly, setViewOnly] = React.useState(true);
  const [created, setCreated] = React.useState<DesktopShare | null>(null);
  const revalidate = [paths.boxDesktopShares(box.id), paths.boxDesktop(box.id), paths.boxTickets(box.id)];
  const items = [...(data?.items ?? [])].sort((a, b) => Date.parse(b.created_at) - Date.parse(a.created_at));
  const anyActive = items.some((x) => shareState(x) === "active");
  return (
    <Card data-testid="desktop-shares">
      <CardHeader className="flex flex-row flex-wrap items-start justify-between gap-2">
        <div className="min-w-0">
          <CardTitle className="flex items-center gap-2"><Share2 className="size-4 text-primary" /> {t("shares")}</CardTitle>
          <CardDescription>{t("sharesDesc")}</CardDescription>
        </div>
        <ConfirmButton
          size="sm" variant="outline" destructive disabled={!anyActive} title={t("revokeAllTitle")} desc={t("revokeAllDesc")} confirmLabel={t("revokeAll")} data-testid="desktop-shares-revoke-all"
          onConfirm={() => run("share-revoke-all", () => api.revokeDesktopShare(box.id, "all"), { success: (r) => t("revokedN", { n: r.revoked, viewers: r.viewers_closed }), revalidate }).then(() => setCreated(null))}
        >
          <ShieldOff /> {t("revokeAll")}
        </ConfirmButton>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        <form
          className="flex flex-wrap items-end gap-3"
          onSubmit={(e) => {
            e.preventDefault();
            run("share-create", () => api.createDesktopShare(box.id, { ttl_s: ttl, view_only: viewOnly }), { success: t("created"), revalidate }).then((r) => r && setCreated(r));
          }}
        >
          <Field label={t("ttl")} className="w-36">
            <Select value={String(ttl)} onChange={(e) => setTtl(Number(e.target.value))} data-testid="desktop-share-ttl">
              {SHARE_TTLS.map((s) => <option key={s} value={s}>{fmtTtl(s, t)}</option>)}
            </Select>
          </Field>
          <label className="flex h-9 items-center gap-2 text-[13px]">
            <Switch checked={!viewOnly} onCheckedChange={(v) => setViewOnly(!v)} data-testid="desktop-share-interactive" /> {t("allowControl")}
          </label>
          <Button type="submit" size="sm" disabled={!!pending} data-testid="desktop-share-create"><Plus /> {t("createShare")}</Button>
        </form>
        {created?.url && (
          <div className="flex flex-col gap-1 rounded-md border border-primary/30 bg-primary/5 p-2.5" data-testid="desktop-share-url">
            <span className="text-xs text-muted-foreground">{t("copyOnce")}</span>
            <div className="flex items-center gap-2">
              <code className="min-w-0 flex-1 overflow-x-auto whitespace-nowrap font-mono text-[12px]">{created.url}</code>
              <CopyButton text={created.url} />
            </div>
            {!created.view_only && <p className="text-xs text-st-warning">{t("interactiveWarn")}</p>}
          </div>
        )}
        {error ? <ErrorBox error={error} retry={() => mutate()} /> : !data ? <LoadingRows rows={2} /> : !items.length ? <Empty title={t("noShares")} /> : (
          <div className="overflow-x-auto">
            <Table>
              <THead><TR><TH>{t("colLink")}</TH><TH>{t("colMode")}</TH><TH>{t("colState")}</TH><TH>{t("colExpires")}</TH><TH>{t("colUse")}</TH><TH /></TR></THead>
              <TBody>
                {items.slice(0, 30).map((x) => {
                  const s = shareState(x);
                  return (
                    <TR key={x.id}>
                      <TD><Mono className="text-[12px]">{x.prefix}…</Mono></TD>
                      <TD><Badge variant={x.view_only ? "gray" : "yellow"}>{x.view_only ? t("viewOnly") : t("interactive")}</Badge></TD>
                      <TD><Badge variant={s === "active" ? "green" : s === "revoked" ? "red" : "outline"}>{t(`shareState.${s}`)}</Badge></TD>
                      <TD className="whitespace-nowrap text-muted-foreground">{f.relTime(x.revoked_at || x.expires_at)}</TD>
                      <TD className="whitespace-nowrap text-muted-foreground">{t("uses", { uses: x.uses, viewers: x.viewers })}</TD>
                      <TD className="text-right">
                        {s === "active" && (
                          <Button size="xs" variant="ghost" disabled={!!pending} data-testid="desktop-share-revoke"
                            onClick={() => run(`share-rv-${x.id}`, () => api.revokeDesktopShare(box.id, x.id), { success: t("revoked"), revalidate }).then(() => created?.id === x.id && setCreated(null))}>
                            {t("revoke")}
                          </Button>
                        )}
                      </TD>
                    </TR>
                  );
                })}
              </TBody>
            </Table>
          </div>
        )}
        <p className="text-xs text-muted-foreground">{t("sharesHint")}</p>
      </CardContent>
    </Card>
  );
}

export function DesktopTab({ box }: { box: Box }) {
  const { data, error, mutate } = useApi<DesktopState>(paths.boxDesktop(box.id), { refreshInterval: 15000 });
  if (error) return <ErrorBox error={error} retry={() => mutate()} />;
  if (!data) return <LoadingRows />;
  return (
    <div className="grid grid-cols-1 items-start gap-4 xl:grid-cols-[minmax(0,1fr)_340px] [&>*]:min-w-0" data-testid="desktop-panel">
      <div className="flex min-w-0 flex-col gap-4">
        <Viewer box={box} state={data} />
        <SharesCard box={box} />
      </div>
      <StatusCard box={box} state={data} />
    </div>
  );
}
