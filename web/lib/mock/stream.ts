import { overview, store, tickSpend } from "./handler";

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
      controller.enqueue(enc.encode(`retry: 3000\n: boxa mock stream\n\n`));
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
