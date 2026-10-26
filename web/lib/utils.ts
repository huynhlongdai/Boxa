import { clsx, type ClassValue } from "clsx";
import { twMerge } from "tailwind-merge";

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}

export function pct(part: number, whole: number): number {
  if (!whole) return 0;
  return Math.max(0, Math.min(999, (part / whole) * 100));
}

// Định dạng theo ngôn ngữ (tiền, ngày, thời lượng…) nằm ở lib/format.ts (hook useFmt).

export const AGENT_COLORS = ["#4f8ff7", "#22c55e", "#f5a524", "#c084fc", "#f472b6", "#2dd4bf", "#fb923c"];
