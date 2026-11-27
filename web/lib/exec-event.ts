import type { BoxmesEvent } from "./types";

export interface ExecInfo {
  exit_code: number;
  duration_ms: number;
  command: string;
  timed_out: boolean;
  tokens_saved: number | null;
  log_id: string | null;
}

type EvData = { exit_code?: number; duration_ms?: number; command?: string; timed_out?: boolean; tokens_saved?: number; log_id?: string | null };

/**
 * Core ghi mỗi lệnh exec thành event `box.exec` với message
 * "exit=<code> <ms>ms: <command>" (có thể có tiền tố "TIMEOUT ").
 * v1.1: chi tiết có cấu trúc nằm ở `data` ({exit_code, tokens_saved, log_id…}) hoặc trường phẳng `tokens_saved` / `log_id`;
 * vẫn đọc được đuôi " tokens_saved=<n> log=<id>" trong message (bản Core cũ / mock).
 */
export function parseExecEvent(e: BoxmesEvent & { data?: EvData | null }): ExecInfo | null {
  if (e.type !== "box.exec" && e.type !== "exec") return null;
  const d: EvData = e.data ?? {};
  const m = e.message.match(/^(TIMEOUT )?exit=(-?\d+) (\d+)ms: ([\s\S]*)$/);
  if (!m && d.exit_code === undefined) return null;
  let cmd = m ? m[4] : d.command ?? "";
  let tokens: number | null = null;
  let log: string | null = null;
  const lm = cmd.match(/\s+log=(\S+)\s*$/);
  if (lm) {
    log = lm[1];
    cmd = cmd.slice(0, lm.index);
  }
  const tm = cmd.match(/\s+tokens_saved=(\d+)\s*$/);
  if (tm) {
    tokens = Number(tm[1]);
    cmd = cmd.slice(0, tm.index);
  }
  const tok = d.tokens_saved ?? e.tokens_saved ?? tokens;
  const lid = d.log_id ?? e.log_id ?? log;
  return {
    timed_out: d.timed_out ?? Boolean(m?.[1]),
    exit_code: d.exit_code ?? Number(m?.[2] ?? 0),
    duration_ms: d.duration_ms ?? Number(m?.[3] ?? 0),
    command: d.command ?? cmd,
    tokens_saved: typeof tok === "number" ? tok : null,
    log_id: lid && lid !== "null" ? lid : null,
  };
}
