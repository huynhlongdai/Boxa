import { overview, store, tickSpend } from "./handler";
import { appLogsFrom } from "./v17";

/** SSE giả lập cho MOCK MODE: đẩy overview.updated / box.updated / event.created định kỳ. */
export function mockStream(signal: AbortSignal): ReadableStream<Uint8Array> {
  const enc = new TextEncoder();
  let timer: ReturnType<typeof setInterval> | null = null;
  let n = 0;
  return new ReadableStream({
    start(controller) {
      const send = (event: string, data: unknown) => {
        try {
          controller.enqueue(enc.encode(`event: ${event}\ndata: ${JSON.stringify(data)}\n\n`));
        } catch {
          /* stream đã đóng */
        }
      };
      controller.enqueue(enc.encode(`retry: 3000\n: boxmes mock stream\n\n`));
      send("overview.updated", overview(store()));
      timer = setInterval(() => {
        const s = store();
        n += 1;
        const running = tickSpend(s, 60); // tua nhanh: mỗi 5s = 1 phút chi phí
        send("overview.updated", overview(s));
        if (running.length) {
          const b = running[n % running.length];
          b.last_active_at = new Date().toISOString();
          send("box.updated", { id: b.id, slug: b.slug, status: b.status, last_active_at: b.last_active_at });
        }
        if (n % 4 === 0 && running.length) {
          const b = running[(n / 4) % running.length];
          const cmds = ["ls -la /work", "python -m pytest -q", "git status --short", "du -sh /work/out", "curl -s localhost:8080/health"];
          const evt = {
            id: `evt_live_${Date.now().toString(36)}`,
            ts: new Date().toISOString(),
            actor: `agent:${b.agent_name}`,
            box_id: b.id,
            agent_id: b.agent_id,
            type: "box.exec",
            message: `exit=0 ${200 + (n % 7) * 130}ms: ${cmds[n % cmds.length]}`,
            cost_usd: null,
            tokens_saved: (n % 3) * 140,
            log_id: null,
            data: { exit_code: 0, duration_ms: 200 + (n % 7) * 130, command: cmds[n % cmds.length], tokens_saved: (n % 3) * 140 },
          };
          s.events.push(evt);
          send("event.created", evt);
        }
      }, 5000);
      signal.addEventListener("abort", () => {
        if (timer) clearInterval(timer);
        try {
          controller.close();
        } catch {
          /* noop */
        }
      });
    },
    cancel() {
      if (timer) clearInterval(timer);
    },
  });
}

/** v1.7: SSE log app (GET /boxes/{id}/app/logs?follow=1) — event `log` {data, offset, truncated}, giống core/internal/api/v17.go. */
export function mockAppLogStream(boxId: string, signal: AbortSignal): ReadableStream<Uint8Array> | null {
  const first = appLogsFrom(store(), boxId, -1);
  if (!first) return null;
  const enc = new TextEncoder();
  let timer: ReturnType<typeof setInterval> | null = null;
  let next = first.offset;
  let n = 0;
  return new ReadableStream({
    start(controller) {
      const send = (raw: string) => {
        try {
          controller.enqueue(enc.encode(raw));
        } catch {
          /* stream đã đóng */
        }
      };
      send(`event: log\ndata: ${JSON.stringify(first)}\n\n`);
      timer = setInterval(() => {
        n += 1;
        const r = appLogsFrom(store(), boxId, next, n % 2 === 0);
        if (!r) return;
        if (r.data || r.truncated) send(`event: log\ndata: ${JSON.stringify(r)}\n\n`);
        else if (n % 15 === 0) send(": ping\n\n");
        next = r.offset;
      }, 1000);
      signal.addEventListener("abort", () => {
        if (timer) clearInterval(timer);
        try {
          controller.close();
        } catch {
          /* noop */
        }
      });
    },
    cancel() {
      if (timer) clearInterval(timer);
    },
  });
}
