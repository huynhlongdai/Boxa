import { clsx, type ClassValue } from "clsx";
import { twMerge } from "tailwind-merge";

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}

/** Tiền USD: số nhỏ giữ 4 chữ số, số lớn 2 chữ số. */
export function usd(n: number | null | undefined, digits?: number): string {
  if (n === null || n === undefined || Number.isNaN(n)) return "—";
  const d = digits ?? (Math.abs(n) < 1 ? 4 : 2);
  return `$${n.toFixed(d)}`;
}

export function pct(part: number, whole: number): number {
  if (!whole) return 0;
  return Math.max(0, Math.min(999, (part / whole) * 100));
}

const rtf = new Intl.RelativeTimeFormat("vi", { numeric: "auto" });

export function relTime(isoStr: string | null | undefined): string {
  if (!isoStr) return "chưa có";
  const diff = (Date.parse(isoStr) - Date.now()) / 1000;
  const abs = Math.abs(diff);
  if (abs < 45) return "vừa xong";
  if (abs < 3600) return rtf.format(Math.round(diff / 60), "minute");
  if (abs < 86400) return rtf.format(Math.round(diff / 3600), "hour");
  return rtf.format(Math.round(diff / 86400), "day");
}

/** Giờ địa phương Việt Nam. */
export function dateTime(isoStr: string | null | undefined): string {
  if (!isoStr) return "—";
  return new Date(isoStr).toLocaleString("vi-VN", {
    timeZone: "Asia/Ho_Chi_Minh",
    hour: "2-digit",
    minute: "2-digit",
    day: "2-digit",
    month: "2-digit",
  });
}

export function shortDate(isoDate: string): string {
  const d = new Date(isoDate.length === 10 ? `${isoDate}T00:00:00Z` : isoDate);
  return d.toLocaleDateString("vi-VN", { day: "2-digit", month: "2-digit", timeZone: "UTC" });
}

export function duration(sinceIso: string | null | undefined): string {
  if (!sinceIso) return "—";
  const s = Math.max(0, (Date.now() - Date.parse(sinceIso)) / 1000);
  if (s < 3600) return `${Math.round(s / 60)} phút`;
  if (s < 86400) return `${Math.floor(s / 3600)} giờ ${Math.round((s % 3600) / 60)} phút`;
  return `${Math.floor(s / 86400)} ngày ${Math.floor((s % 86400) / 3600)} giờ`;
}

export function ms(n: number): string {
  if (n < 1000) return `${n} ms`;
  if (n < 60_000) return `${(n / 1000).toFixed(1)} s`;
  return `${Math.floor(n / 60_000)} phút ${Math.round((n % 60_000) / 1000)} s`;
}

export function bytes(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / 1024 / 1024).toFixed(1)} MB`;
}

export function mb(n: number): string {
  return n >= 1024 ? `${(n / 1024).toFixed(n % 1024 ? 1 : 0)} GB` : `${n} MB`;
}

export function compact(n: number): string {
  return new Intl.NumberFormat("vi-VN", { notation: "compact", maximumFractionDigits: 1 }).format(n);
}

export const AGENT_COLORS = ["#4f8ff7", "#22c55e", "#f5a524", "#c084fc", "#f472b6", "#2dd4bf", "#fb923c"];
