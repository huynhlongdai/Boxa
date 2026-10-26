"use client";
// Terminal xterm.js qua WebSocket GET /boxes/:id/terminal (text frames, PTY).
// Mặc định chỉ xem; "Chiếm quyền" gọi POST /takeover rồi cho anh gõ; "Trả quyền" gọi POST /release.
// MOCK MODE: mô phỏng shell trong trình duyệt, lệnh chạy qua /exec và /files của mock.
import * as React from "react";
import type { Terminal as XTerm } from "@xterm/xterm";
import type { FitAddon } from "@xterm/addon-fit";
import { Hand, Plug, PlugZap, Undo2 } from "lucide-react";
import { toast } from "sonner";
import { api, errMsg } from "@/lib/api";
import type { Box, ExecResult } from "@/lib/types";
import { useLive } from "@/components/live";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Dot } from "./status";

type ConnState = "connecting" | "open" | "closed" | "mock";

function wsUrl(boxId: string, wsBase: string | null) {
  const base = wsBase || (process.env.NEXT_PUBLIC_BOXA_API || "http://localhost:8080/v1").replace(/^http/, "ws");
  return `${base.replace(/\/+$/, "")}/boxes/${boxId}/terminal`;
}

export function BoxTerminal({ box }: { box: Box }) {
  const { session } = useLive();
  const hostRef = React.useRef<HTMLDivElement>(null);
  const termRef = React.useRef<XTerm | null>(null);
  const wsRef = React.useRef<WebSocket | null>(null);
  const controlRef = React.useRef(false);
  const [control, setControl] = React.useState(false);
  const [conn, setConn] = React.useState<ConnState>("connecting");
  const [busy, setBusy] = React.useState(false);
  const mock = session?.mock ?? false;

  React.useEffect(() => {
    controlRef.current = control;
  }, [control]);

  React.useEffect(() => {
    if (!session || !hostRef.current) return;
    let disposed = false;
    let fit: FitAddon | null = null;
    let ro: ResizeObserver | null = null;
    let feed: ReturnType<typeof setInterval> | null = null;

    (async () => {
      const [{ Terminal }, { FitAddon }] = await Promise.all([import("@xterm/xterm"), import("@xterm/addon-fit")]);
      if (disposed || !hostRef.current) return;
      const term = new Terminal({
        fontFamily: '"JetBrains Mono", ui-monospace, Menlo, monospace',
        fontSize: 13,
        cursorBlink: true,
        convertEol: true,
        theme: { background: "#0a0b0d", foreground: "#e8eaee", cursor: "#4f8ff7", selectionBackground: "#4f8ff744", green: "#22c55e", red: "#f1555b", yellow: "#f5a524", blue: "#4f8ff7" },
      });
      fit = new FitAddon();
      term.loadAddon(fit);
      term.open(hostRef.current);
      fit.fit();
      termRef.current = term;
      ro = new ResizeObserver(() => fit?.fit());
      ro.observe(hostRef.current);

      const prompt = () => term.write(`\r\n\x1b[32manh@${box.slug}\x1b[0m:\x1b[34m/work\x1b[0m$ `);

      if (session.mock) {
        setConn("mock");
        term.writeln(`\x1b[90m# Boxa terminal (MOCK) · ${box.id} · ${box.provider}/${box.region}\x1b[0m`);
        term.writeln(`\x1b[90m# Đang xem agent ${box.agent_name}. Bấm “Chiếm quyền” để gõ lệnh.\x1b[0m`);
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
          if (controlRef.current || box.status !== "running") return;
          term.writeln(lines[i % lines.length]);
          i++;
        }, 2200);
        let line = "";
        term.onData(async (d) => {
          if (!controlRef.current) return;
          if (d === "\r") {
            const cmd = line.trim();
            line = "";
            if (!cmd) return prompt();
            term.write("\r\n");
            if (cmd === "clear") { term.clear(); return prompt(); }
            if (cmd === "help") { term.write("Lệnh mock: ls [path], cat <file>, pwd, whoami, clear, mọi lệnh khác gửi qua /exec"); return prompt(); }
            if (cmd === "pwd") { term.write("/work"); return prompt(); }
            if (cmd === "whoami") { term.write("anh (đang chiếm quyền)"); return prompt(); }
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
        return;
      }

      // Thật: WebSocket tới Core. Cookie httpOnly boxa_key được trình duyệt gửi kèm khi Core cùng site.
      const connect = () => {
        setConn("connecting");
        const ws = new WebSocket(wsUrl(box.id, session.ws_base));
        wsRef.current = ws;
        ws.onopen = () => {
          setConn("open");
          term.writeln(`\x1b[90m# Đã kết nối PTY ${box.id}\x1b[0m`);
        };
        ws.onmessage = (ev) => term.write(typeof ev.data === "string" ? ev.data : "");
        ws.onclose = () => {
          setConn("closed");
          if (!disposed) term.writeln("\r\n\x1b[33m# Mất kết nối terminal\x1b[0m");
        };
        ws.onerror = () => setConn("closed");
      };
      term.onData((d) => {
        if (controlRef.current && wsRef.current?.readyState === WebSocket.OPEN) wsRef.current.send(d);
      });
      connect();
    })();

    return () => {
      disposed = true;
      if (feed) clearInterval(feed);
      ro?.disconnect();
      wsRef.current?.close();
      termRef.current?.dispose();
      termRef.current = null;
    };
  }, [session, box.id, box.slug, box.agent_name, box.provider, box.region, box.status]);

  const takeover = async () => {
    setBusy(true);
    try {
      await api.takeover(box.id);
      setControl(true);
      toast.success("Anh đang cầm terminal. Agent tạm dừng exec.");
      termRef.current?.focus();
      if (mock) termRef.current?.write(`\r\n\x1b[33m# Anh đã chiếm quyền\x1b[0m\r\n\x1b[32manh@${box.slug}\x1b[0m:\x1b[34m/work\x1b[0m$ `);
    } catch (e) {
      toast.error(errMsg(e));
    } finally {
      setBusy(false);
    }
  };
  const release = async () => {
    setBusy(true);
    try {
      await api.release(box.id);
      setControl(false);
      toast.success("Đã trả quyền cho agent");
      termRef.current?.writeln("\r\n\x1b[33m# Đã trả quyền cho agent\x1b[0m");
    } catch (e) {
      toast.error(errMsg(e));
    } finally {
      setBusy(false);
    }
  };

  const connBadge =
    conn === "open" ? <Badge variant="green"><Dot tone="green" pulse />Đã kết nối</Badge>
    : conn === "mock" ? <Badge variant="yellow"><Dot tone="yellow" />Mô phỏng</Badge>
    : conn === "connecting" ? <Badge variant="blue"><Dot tone="blue" pulse />Đang kết nối</Badge>
    : <Badge variant="red"><Dot tone="red" />Mất kết nối</Badge>;

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-2">
        {connBadge}
        <Badge variant={control ? "yellow" : "outline"}>{control ? "Anh đang điều khiển" : "Chỉ xem — agent điều khiển"}</Badge>
        <div className="ml-auto flex gap-2">
          {conn === "closed" && (
            <Button size="sm" variant="outline" onClick={() => window.location.reload()}><Plug /> Kết nối lại</Button>
          )}
          {control ? (
            <Button size="sm" variant="secondary" disabled={busy} onClick={release}><Undo2 /> Trả quyền</Button>
          ) : (
            <Button size="sm" disabled={busy || box.status !== "running"} onClick={takeover}><Hand /> Chiếm quyền</Button>
          )}
        </div>
      </div>
      {box.status !== "running" && (
        <p className="flex items-center gap-2 text-[13px] text-muted-foreground"><PlugZap className="size-4" /> Box không ở trạng thái chạy — đánh thức box để dùng terminal.</p>
      )}
      <div className="h-[420px] overflow-hidden rounded-md border border-border bg-background md:h-[520px]" ref={hostRef} />
    </div>
  );
}
