import type { BoxaEvent } from "./types";

export interface ExecInfo {
  exit_code: number;
  duration_ms: number;
  command: string;
  timed_out: boolean;
  tokens_saved: number | null;
  log_id: string | null;
}

/**
 * Core ghi mỗi lệnh exec thành event `box.exec` với message
 * "exit=<code> <ms>ms: <command>" (có thể có tiền tố "TIMEOUT ").
 * Hỗ trợ thêm phần đuôi tuỳ chọn " tokens_saved=<n> log=<id>" nếu Core bổ sung.
 */
export function parseExecEvent(e: BoxaEvent): ExecInfo | null {
  if (e.type !== "box.exec" && e.type !== "exec") return null;
  const m = e.message.match(/^(TIMEOUT )?exit=(-?\d+) (\d+)ms: ([\s\S]*)$/);
  if (!m) return null;
  let cmd = m[4];
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
  return { timed_out: Boolean(m[1]), exit_code: Number(m[2]), duration_ms: Number(m[3]), command: cmd, tokens_saved: tokens, log_id: log && log !== "null" ? log : null };
}
