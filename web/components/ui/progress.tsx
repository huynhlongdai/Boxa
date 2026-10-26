import { cn } from "@/lib/utils";

/** Thanh tiến độ; tự đổi màu vàng ≥ warnAt, đỏ ≥ dangerAt. */
export function Progress({ value, className, warnAt = 80, dangerAt = 100, tone }: { value: number; className?: string; warnAt?: number; dangerAt?: number; tone?: "auto" | "blue" }) {
  const v = Math.max(0, Math.min(100, value));
  const color = tone === "blue" ? "bg-st-provisioning" : value >= dangerAt ? "bg-st-error" : value >= warnAt ? "bg-st-warning" : "bg-st-running";
  return (
    <div className={cn("h-1.5 w-full overflow-hidden rounded-full bg-surface-2", className)} role="progressbar" aria-valuenow={Math.round(value)} aria-valuemin={0} aria-valuemax={100}>
      <div className={cn("h-full rounded-full transition-all", color)} style={{ width: `${v}%` }} />
    </div>
  );
}
