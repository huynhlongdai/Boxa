"use client";
// v1.10 Terminal: xterm.js qua WebSocket tới Core, xác thực bằng ticket một lần (bxa_…, 60 s).
//  - Ticket lấy qua proxy Next (POST /api/boxmes/boxes/{id}/tickets) — API key chỉ nằm ở server, không bao giờ trong URL / JS.
//  - WS: <core>/v1/boxes/{id}/terminal?ticket=…&proto=2&mode=control|view[&session=…]&cols&rows
//    proto=2: output là frame nhị phân, input gửi nhị phân; frame text là JSON điều khiển
//    (session / viewers / control / exit / ping; client gửi resize / control / ping).
//  - Terminal bền: chọn phiên (list, gắn lại có replay, phiên mới, kill); tự kết nối lại với ticket mới, gắn lại đúng phiên.
//  - Origin của dashboard phải nằm trong allowlist của Core (BOXMES_DASHBOARD_URL hoặc BOXMES_ACCESS_ORIGINS).
//  - "Chiếm quyền" (takeover) vẫn là tạm dừng exec của agent; quyền gõ trong phiên là chế độ Điều khiển / Chỉ xem.
// MOCK MODE: route Next không upgrade WebSocket → shell mô phỏng (lệnh qua /exec, /files); ticket + danh sách phiên là mock thật.
import * as React from "react";
import type { Terminal as XTerm } from "@xterm/xterm";
import type { FitAddon } from "@xterm/addon-fit";
import { Eye, Hand, Keyboard, Plug, PlugZap, Plus, Skull, Undo2 } from "lucide-react";
import { toast } from "sonner";
import { useTranslations } from "next-intl";
import { api, errMsg, paths } from "@/lib/api";
import type { AccessTicket, Box, ExecResult, TerminalList, TerminalSession } from "@/lib/types";
import { useApi } from "@/lib/hooks";
import { useFmt } from "@/lib/format";
import { useLive } from "@/components/live";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Select } from "@/components/ui/input";
import { Dot } from "./status";
import { ConfirmButton } from "./confirm";
import { toastError } from "@/lib/error-toast";

type ConnState = "connecting" | "open" | "reconnecting" | "closed" | "ended" | "mock";
type Mode = "control" | "view";
const NEW = "__new__";
const MAX_RETRIES = 8;

/** URL WebSocket terminal: NEXT_PUBLIC_BOXMES_WS (nếu đặt) → core_url của ticket → suy ra từ NEXT_PUBLIC_BOXMES_API. */
export function terminalWsUrl(t: AccessTicket, wsBase: string | null, q: Record<string, string | number | undefined>) {
  let base: string;
  if (wsBase) base = `${wsBase.replace(/\/+$/, "")}/boxes/${t.box_id}/terminal`;
  else if (t.core_url && /^wss?:\/\//.test(t.core_url)) base = t.core_url;
  else base = `${(process.env.NEXT_PUBLIC_BOXMES_API || "http://localhost:8080/v1").replace(/^http/, "ws").replace(/\/+$/, "")}/boxes/${t.box_id}/terminal`;
  const u = new URL(base, typeof window !== "undefined" ? window.location.href : "http://localhost");
  if (u.protocol === "http:") u.protocol = "ws:";
  if (u.protocol === "https:") u.protocol = "wss:";
  u.searchParams.delete("token");
  for (const [k, v] of Object.entries(q)) if (v !== undefined && v !== "") u.searchParams.set(k, String(v));
  return u.toString();
}

const gray = (s: string) => `\x1b[90m${s}\x1b[0m`;
const yellow = (s: string) => `\x1b[33m${s}\x1b[0m`;

export function BoxTerminal({ box }: { box: Box }) {
  const { session } = useLive();
  const t = useTranslations("terminal");
  const f = useFmt();
  const tRef = React.useRef(t);
  tRef.current = t;
  const hostRef = React.useRef<HTMLDivElement>(null);
  const termRef = React.useRef<XTerm | null>(null);
  const wsRef = React.useRef<WebSocket | null>(null);
  const sidRef = React.useRef<string>("");
  const modeRef = React.useRef<Mode>("control");
  const hasCtlRef = React.useRef(false);
  const retryRef = React.useRef<{ n: number; timer: ReturnType<typeof setTimeout> | null }>({ n: 0, timer: null });
  const connectRef = React.useRef<(sid: string) => void>(() => {});
  const statusRef = React.useRef(box.status);
  statusRef.current = box.status;

  const [takeover, setTakeover] = React.useState(Boolean(box.taken_over));
  const [conn, setConn] = React.useState<ConnState>("connecting");
  const [busy, setBusy] = React.useState(false);
  const [sid, setSid] = React.useState("");
  const [mode, setMode] = React.useState<Mode>("control");
  const [hasCtl, setHasCtl] = React.useState(false);
  const [viewers, setViewers] = React.useState(0);
  const mock = session?.mock ?? false;
  const connectable = box.status === "running" || box.status === "sleeping";
  const sleeping = box.status === "sleeping";

  const { data: terms, mutate: reloadTerms } = useApi<TerminalList>(connectable ? paths.boxTerminals(box.id) : null, { refreshInterval: 10000 });
  const sessions: TerminalSession[] = terms?.items ?? [];

  React.useEffect(() => {
    modeRef.current = mode;
  }, [mode]);
  React.useEffect(() => {
    hasCtlRef.current = hasCtl;
  }, [hasCtl]);

  React.useEffect(() => {
    if (!session || !hostRef.current || !connectable) return;
    let disposed = false;
    let fit: FitAddon | null = null;
    let ro: ResizeObserver | null = null;
    let feed: ReturnType<typeof setInterval> | null = null;
    const retry = retryRef.current;
    const enc = new TextEncoder();

    (async () => {
      const [{ Terminal }, { FitAddon }] = await Promise.all([import("@xterm/xterm"), import("@xterm/addon-fit")]);
      if (disposed || !hostRef.current) return;
      const term = new Terminal({
        fontFamily: '"JetBrains Mono", ui-monospace, Menlo, monospace',
        fontSize: 13,
        cursorBlink: true,
        convertEol: session.mock,
        scrollback: 5000,
        theme: { background: "#0a0b0d", foreground: "#e8eaee", cursor: "#4f8ff7", selectionBackground: "#4f8ff744", green: "#22c55e", red: "#f1555b", yellow: "#f5a524", blue: "#4f8ff7" },
      });
      fit = new FitAddon();
      term.loadAddon(fit);
      term.open(hostRef.current);
      fit.fit();
      termRef.current = term;
      const sendResize = () => {
        const ws = wsRef.current;
        if (ws?.readyState === WebSocket.OPEN && modeRef.current === "control") ws.send(JSON.stringify({ type: "resize", cols: term.cols, rows: term.rows }));
      };
      ro = new ResizeObserver(() => {
        fit?.fit();
        sendResize();
      });
      ro.observe(hostRef.current);

      const tt = () => tRef.current;

      // ---------------- MOCK ----------------
      if (session.mock) {
        const user = tt()("promptUser");
        const prompt = () => term.write(`\r\n\x1b[32m${user}@${box.slug}\x1b[0m:\x1b[34m/work\x1b[0m$ `);
        connectRef.current = async (want: string) => {
          term.reset();
          setConn("connecting");
          try {
            const tk = await api.createTicket(box.id, want ? { purpose: "terminal", session_id: want } : { purpose: "terminal" });
            if (disposed) return;
            sidRef.current = tk.session_id ?? "";
            setSid(sidRef.current);
            setConn("mock");
            setHasCtl(modeRef.current === "control");
            setViewers(1);
            term.writeln(gray(tt()("mockHeader", { id: box.id, where: `${box.provider}/${box.region}` })));
            term.writeln(gray(tt()("mockTicket", { prefix: tk.prefix })));
            term.writeln(gray(want ? tt()("reattached", { id: sidRef.current, bytes: f.bytes(4096) }) : tt()("newSessionLine", { id: sidRef.current, ttl: f.seconds(terms?.detach_ttl_s ?? 600) })));
            if (statusRef.current === "sleeping") term.writeln(yellow(tt()("wakeLine")));
            term.writeln(gray(tt()("mockWatching", { agent: box.agent_name })));
            if (modeRef.current === "control") prompt();
            reloadTerms();
          } catch (e) {
            setConn("closed");
            term.writeln(yellow(tt()("ticketFailed", { msg: errMsg(e) })));
          }
        };
        const lines = [
          `\x1b[36m[agent]\x1b[0m $ ls /work`,
          `README.md  out  requirements.txt`,
          `\x1b[36m[agent]\x1b[0m $ git status --short`,
          ` M crawl.py`,
          `\x1b[36m[agent]\x1b[0m $ python -m pytest -q`,
          `\x1b[32m12 passed\x1b[0m in 3.41s`,
        ];
        let i = 0;
        feed = setInterval(() => {
          if (modeRef.current === "control" || statusRef.current !== "running") return;
          term.writeln(lines[i % lines.length]);
          i++;
        }, 2200);
        let line = "";
        term.onData(async (d) => {
          if (modeRef.current !== "control") return;
          if (d === "\r") {
            const cmd = line.trim();
            line = "";
            if (!cmd) return prompt();
            term.write("\r\n");
            if (cmd === "clear") { term.clear(); return prompt(); }
            if (cmd === "help") { term.write(tt()("mockHelp")); return prompt(); }
            if (cmd === "pwd") { term.write("/work"); return prompt(); }
            if (cmd === "whoami") { term.write(tt()("whoami")); return prompt(); }
            try {
              const [bin, arg] = cmd.split(/\s+/, 2);
              if (bin === "ls" || bin === "cat") {
                const p = arg ? (arg.startsWith("/") ? arg : `/work/${arg}`) : "/work";
                const r = await api.files(box.id, p);
                if ("entries" in r) term.write(r.entries.map((e) => (e.type === "dir" ? `\x1b[34m${e.name}/\x1b[0m` : e.name)).join("  "));
                else term.write(r.content.replace(/\n/g, "\r\n"));
              } else {
                const r = (await api.exec(box.id, { command: cmd })) as ExecResult;
                term.write((r.stdout_tail || "").replace(/\n/g, "\r\n"));
                if (r.stderr_tail) term.write(`\r\n\x1b[31m${r.stderr_tail}\x1b[0m`);
              }
            } catch (e) {
              term.write(`\x1b[31m${errMsg(e)}\x1b[0m`);
            }
            return prompt();
          }
          if (d === "\u007f") {
            if (line.length) { line = line.slice(0, -1); term.write("\b \b"); }
            return;
          }
          if (d >= " " || d === "\t") { line += d; term.write(d); }
        });
        connectRef.current("");
        return;
      }

      // ---------------- Thật: ticket → WebSocket proto=2 ----------------
      const scheduleRetry = (why: string) => {
        if (disposed) return;
        if (retry.n >= MAX_RETRIES) {
          setConn("closed");
          term.writeln(yellow(tt()("giveUp")));
          return;
        }
        const delay = Math.min(10_000, 1000 * 2 ** retry.n);
        retry.n += 1;
        setConn("reconnecting");
        term.writeln(yellow(tt()("reconnecting", { s: Math.round(delay / 1000), n: retry.n, why })));
        retry.timer = setTimeout(() => connectRef.current(sidRef.current), delay);
      };

      connectRef.current = async (want: string) => {
        if (retry.timer) clearTimeout(retry.timer);
        retry.timer = null;
        const old = wsRef.current;
        if (old) {
          old.onclose = null;
          old.close();
          wsRef.current = null;
        }
        if (retry.n === 0) setConn("connecting");
        if (statusRef.current === "sleeping") term.writeln(yellow(tt()("wakeLine")));
        let tk: AccessTicket;
        try {
          tk = await api.createTicket(box.id, want ? { purpose: "terminal", session_id: want } : { purpose: "terminal" });
        } catch (e) {
          if (disposed) return;
          term.writeln(yellow(tt()("ticketFailed", { msg: errMsg(e) })));
          // phiên đã hết → mở phiên mới ở lần thử sau
          if (want && /session|ended|not found/i.test(errMsg(e))) sidRef.current = "";
          return scheduleRetry("ticket");
        }
        if (disposed || !tk.ticket) return;
        let opened = false;
        let ended = false;
        const ws = new WebSocket(
          terminalWsUrl(tk, session.ws_base, { ticket: tk.ticket, proto: 2, mode: modeRef.current, session: want || undefined, cols: term.cols, rows: term.rows }),
        );
        ws.binaryType = "arraybuffer";
        wsRef.current = ws;
        ws.onopen = () => {
          opened = true;
          retry.n = 0;
          setConn("open");
        };
        ws.onmessage = (ev) => {
          if (ev.data instanceof ArrayBuffer) {
            term.write(new Uint8Array(ev.data));
            return;
          }
          if (typeof ev.data !== "string") return;
          let m: { type?: string; id?: string; replay?: number; detach_ttl_s?: number; control?: boolean; n?: number; code?: number; reason?: string; created?: boolean };
          try {
            m = JSON.parse(ev.data);
          } catch {
            term.write(ev.data);
            return;
          }
          switch (m.type) {
            case "session": {
              const reattach = !m.created;
              if (reattach && (m.replay ?? 0) > 0) term.reset();
              sidRef.current = m.id ?? "";
              setSid(sidRef.current);
              setHasCtl(Boolean(m.control));
              term.writeln(gray(reattach ? tt()("reattached", { id: m.id ?? "", bytes: f.bytes(m.replay ?? 0) }) : tt()("newSessionLine", { id: m.id ?? "", ttl: f.seconds(m.detach_ttl_s ?? 600) })));
              if (m.control) sendResize();
              reloadTerms();
              break;
            }
            case "viewers":
              setViewers(m.n ?? 0);
              setHasCtl(Boolean(m.control));
              break;
            case "control":
              if (m.control === false && hasCtlRef.current) term.writeln(yellow(tt()("controlLost")));
              setHasCtl(Boolean(m.control));
              break;
            case "exit":
              ended = true;
              setConn("ended");
              term.writeln(yellow(m.code !== undefined ? tt()("endedCode", { code: m.code }) : tt()("ended", { reason: m.reason || "—" })));
              sidRef.current = "";
              reloadTerms();
              break;
            case "ping":
              if (ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify({ type: "pong" }));
              break;
          }
        };
        ws.onclose = () => {
          if (wsRef.current === ws) wsRef.current = null;
          if (disposed || ended) return;
          if (!opened && retry.n === 0) term.writeln(`${yellow(tt()("wsFailed"))}\r\n${gray(tt()("wsFailedHint"))}`);
          else if (opened) term.writeln(`\r\n${yellow(tt()("lost"))}`);
          scheduleRetry(opened ? "close" : "open");
        };
      };
      term.onData((d) => {
        const ws = wsRef.current;
        if (modeRef.current === "control" && ws?.readyState === WebSocket.OPEN) ws.send(enc.encode(d));
      });
      connectRef.current("");
    })();

    return () => {
      disposed = true;
      if (feed) clearInterval(feed);
      if (retry.timer) clearTimeout(retry.timer);
      retry.n = 0;
      ro?.disconnect();
      const ws = wsRef.current;
      if (ws) {
        ws.onclose = null;
        ws.close();
      }
      wsRef.current = null;
      termRef.current?.dispose();
      termRef.current = null;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [session, box.id, box.slug, box.agent_name, box.provider, box.region, connectable]);

  const pick = (v: string) => {
    retryRef.current.n = 0;
    termRef.current?.reset();
    connectRef.current(v === NEW ? "" : v);
  };
  const reconnect = () => {
    retryRef.current.n = 0;
    connectRef.current(sidRef.current);
  };
  const switchMode = (m: Mode) => {
    setMode(m);
    modeRef.current = m;
    const ws = wsRef.current;
    if (ws?.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify({ type: "control", control: m === "control" }));
      if (m === "control" && termRef.current) ws.send(JSON.stringify({ type: "resize", cols: termRef.current.cols, rows: termRef.current.rows }));
    }
    if (mock) setHasCtl(m === "control");
    termRef.current?.writeln(gray(m === "control" ? t("modeControlLine") : t("modeViewLine")));
    if (m === "control") termRef.current?.focus();
  };
  const kill = async () => {
    if (!sid) return;
    try {
      await api.killTerminal(box.id, sid);
      toast.success(t("killed", { id: sid }));
      if (mock) {
        termRef.current?.writeln(yellow(t("ended", { reason: "killed" })));
        setConn("ended");
        sidRef.current = "";
        setSid("");
      }
      reloadTerms();
    } catch (e) {
      toastError(e);
    }
  };

  const doTakeover = async () => {
    setBusy(true);
    try {
      await api.takeover(box.id);
      setTakeover(true);
      toast.success(t("takeoverToast"));
      termRef.current?.writeln(yellow(t("tookOver")));
    } catch (e) {
      toastError(e);
    } finally {
      setBusy(false);
    }
  };
  const doRelease = async () => {
    setBusy(true);
    try {
      await api.release(box.id);
      setTakeover(false);
      toast.success(t("releaseToast"));
      termRef.current?.writeln(yellow(t("released")));
    } catch (e) {
      toastError(e);
    } finally {
      setBusy(false);
    }
  };

  const connBadge =
    conn === "open" ? <Badge variant="green"><Dot tone="green" pulse />{t("stateOpen")}</Badge>
    : conn === "mock" ? <Badge variant="yellow"><Dot tone="yellow" />{t("stateMock")}</Badge>
    : conn === "connecting" ? <Badge variant="blue"><Dot tone="blue" pulse />{t("stateConnecting")}</Badge>
    : conn === "reconnecting" ? <Badge variant="yellow"><Dot tone="yellow" pulse />{t("stateReconnecting")}</Badge>
    : conn === "ended" ? <Badge variant="outline">{t("stateEnded")}</Badge>
    : <Badge variant="red"><Dot tone="red" />{t("stateClosed")}</Badge>;

  const known = sid && !sessions.some((s) => s.id === sid) ? [{ id: sid } as TerminalSession, ...sessions] : sessions;
  const label = (s: TerminalSession) => {
    const parts = [s.name ? `${s.name} · ${s.id}` : s.id];
    if (s.viewers !== undefined) parts.push(t("viewersN", { n: s.viewers }));
    if (s.expires_at) parts.push(t("detachedLeft", { time: f.relTime(s.expires_at) }));
    return parts.join(" · ");
  };

  return (
    <div className="flex flex-col gap-3" data-testid="terminal-v110">
      <div className="flex flex-wrap items-center gap-2">
        {connBadge}
        <Badge variant={hasCtl && mode === "control" ? "yellow" : "outline"} data-testid="terminal-mode-badge">
          {mode === "control" ? (hasCtl ? t("youControl") : t("controlPending")) : t("watchOnly")}
        </Badge>
        {viewers > 1 && <Badge variant="outline">{t("viewersN", { n: viewers })}</Badge>}
        {takeover && <Badge variant="yellow">{t("agentPaused")}</Badge>}
        <div className="ml-auto flex flex-wrap gap-2">
          {(conn === "closed" || conn === "ended") && (
            <Button size="sm" variant="outline" onClick={reconnect}><Plug /> {t("reconnect")}</Button>
          )}
          {takeover ? (
            <Button size="sm" variant="secondary" disabled={busy} onClick={doRelease}><Undo2 /> {t("release")}</Button>
          ) : (
            <Button size="sm" variant="secondary" disabled={busy || box.status !== "running"} onClick={doTakeover} title={t("takeoverHint")}><Hand /> {t("takeover")}</Button>
          )}
        </div>
      </div>

      {connectable && (
        <div className="flex flex-wrap items-center gap-2 rounded-md border border-border px-3 py-2 text-[13px]" data-testid="terminal-sessions">
          <span className="text-muted-foreground">{t("session")}</span>
          <Select value={sid || NEW} onChange={(e) => pick(e.target.value)} className="h-8 w-auto min-w-[220px] max-w-full font-mono text-[12.5px]" data-testid="terminal-session-picker">
            {known.map((s) => <option key={s.id} value={s.id}>{label(s)}</option>)}
            <option value={NEW}>{t("newSession")}</option>
          </Select>
          <Button size="sm" variant="outline" onClick={() => pick(NEW)} data-testid="terminal-new-session"><Plus /> {t("newSession")}</Button>
          <div className="inline-flex overflow-hidden rounded-md border border-border" role="group" aria-label={t("modeLabel")}>
            <Button size="sm" variant={mode === "control" ? "default" : "ghost"} className="rounded-none" onClick={() => switchMode("control")} data-testid="terminal-mode-control"><Keyboard /> {t("modeControl")}</Button>
            <Button size="sm" variant={mode === "view" ? "default" : "ghost"} className="rounded-none" onClick={() => switchMode("view")} data-testid="terminal-mode-view"><Eye /> {t("modeView")}</Button>
          </div>
          <ConfirmButton
            size="sm" variant="ghost" className="ml-auto text-st-error" disabled={!sid} destructive data-testid="terminal-kill"
            title={t("killTitle", { id: sid || "—" })} desc={t("killDesc")} confirmLabel={t("kill")} onConfirm={kill}
          >
            <Skull /> {t("kill")}
          </ConfirmButton>
        </div>
      )}

      {sleeping && (
        <p className="flex items-center gap-2 rounded-md border border-st-warning/30 bg-st-warning/5 px-3 py-2 text-[13px]" data-testid="terminal-wake-notice">
          <PlugZap className="size-4 shrink-0" /> {t("wakeNotice")}
        </p>
      )}
      {mock && connectable && <p className="text-xs text-muted-foreground" data-testid="terminal-mock-notice">{t("mockNotice")}</p>}
      {!connectable && (
        <p className="flex items-center gap-2 text-[13px] text-muted-foreground"><PlugZap className="size-4 shrink-0" /> {t("notRunning")}</p>
      )}
      {connectable && <div className="h-[420px] overflow-hidden rounded-md border border-border bg-background md:h-[520px]" ref={hostRef} />}
      {connectable && <p className="text-xs text-muted-foreground">{t("footer", { ttl: f.seconds(terms?.detach_ttl_s ?? 600) })}</p>}
    </div>
  );
}
