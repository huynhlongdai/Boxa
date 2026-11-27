"use client";
// v1.3: điểm 6 yếu tố có giải thích (cost, health, speed, latency, capacity, reliability) — dùng chung cho
// điểm provider (Infra › Sức khỏe) và phương án gợi ý (Gợi ý box).
import { useTranslations } from "next-intl";
import type { ScoreComponent } from "@/lib/types";
import { cn } from "@/lib/utils";
import { Mono } from "./status";

export const FACTORS = ["cost", "health", "speed", "latency", "capacity", "reliability"] as const;

export function scoreTone(s: number) {
  return s >= 75 ? "text-st-running" : s >= 50 ? "text-st-warning" : "text-st-error";
}

export function ScorePill({ score, className }: { score: number; className?: string }) {
  return <span className={cn("font-mono text-lg font-semibold tabular-nums", scoreTone(score), className)}>{score.toFixed(1)}</span>;
}

/** Bảng phân rã điểm: mỗi yếu tố = điểm 0..100 × trọng số, kèm giá trị đầu vào dễ đọc. */
export function ScoreBreakdown({ breakdown, className }: { breakdown: Record<string, ScoreComponent>; className?: string }) {
  const t = useTranslations("score");
  return (
    <div className={cn("flex flex-col gap-1.5", className)} data-score-breakdown>
      {FACTORS.filter((f) => breakdown[f]).map((f) => {
        const c = breakdown[f];
        return (
          <div key={f} className="grid grid-cols-[minmax(0,6.5rem)_1fr_auto] items-center gap-2 text-[12px]">
            <span className="truncate text-muted-foreground" title={t(`help.${f}`)}>{t(`f.${f}`)}</span>
            <div className="relative h-1.5 min-w-0 overflow-hidden rounded-full bg-surface-2">
              <div className={cn("absolute inset-y-0 left-0 rounded-full", c.score >= 75 ? "bg-st-running" : c.score >= 50 ? "bg-st-warning" : "bg-st-error")} style={{ width: `${Math.max(2, Math.min(100, c.score))}%` }} />
            </div>
            <span className="whitespace-nowrap text-right font-mono tabular-nums">
              {Math.round(c.score)} <span className="text-muted-foreground">× {Math.round(c.weight * 100)}%</span>
            </span>
            <span className="col-span-3 -mt-1 truncate pl-0 text-[11px] text-muted-foreground sm:col-start-2 sm:col-span-2"><Mono>{c.value}</Mono></span>
          </div>
        );
      })}
    </div>
  );
}
