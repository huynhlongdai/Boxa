"use client";
// Trình sửa box.yaml: đánh số dòng, kiểm tra cú pháp qua POST /templates/validate (debounce).
import * as React from "react";
import { CheckCircle2, XCircle } from "lucide-react";
import { useTranslations } from "next-intl";
import { api } from "@/lib/api";
import type { ValidateResult } from "@/lib/types";
import { cn } from "@/lib/utils";

export function YamlEditor({ value, onChange, onValidated, readOnly, minRows = 16 }: { value: string; onChange?: (v: string) => void; onValidated?: (r: ValidateResult | null) => void; readOnly?: boolean; minRows?: number }) {
  const t = useTranslations("yaml");
  const [result, setResult] = React.useState<ValidateResult | null>(null);
  const [checking, setChecking] = React.useState(false);
  const gutter = React.useRef<HTMLDivElement>(null);
  const cb = React.useRef(onValidated);
  cb.current = onValidated;

  React.useEffect(() => {
    if (readOnly) return;
    setChecking(true);
    const t = setTimeout(async () => {
      try {
        const r = await api.validateTemplate(value);
        setResult(r);
        cb.current?.(r);
      } catch {
        setResult(null);
        cb.current?.(null);
      } finally {
        setChecking(false);
      }
    }, 400);
    return () => clearTimeout(t);
  }, [value, readOnly]);

  const lines = value.split("\n");
  const errLines = new Set(result?.errors.map((e) => e.line));
  const rows = Math.max(minRows, lines.length + 1);

  return (
    <div className="flex flex-col gap-2">
      <div className="flex overflow-hidden rounded-md border border-border bg-background focus-within:ring-2 focus-within:ring-ring">
        <div ref={gutter} className="select-none overflow-hidden border-r border-border bg-surface py-2 text-right font-mono text-[12.5px] leading-[1.6] text-muted-foreground" aria-hidden style={{ height: `${rows * 1.6 * 12.5 + 16}px` }}>
          {Array.from({ length: rows }).map((_, i) => (
            <div key={i} className={cn("px-2", errLines.has(i + 1) && "bg-st-error/15 text-st-error")}>{i + 1}</div>
          ))}
        </div>
        <textarea
          value={value}
          onChange={(e) => onChange?.(e.target.value)}
          onScroll={(e) => { if (gutter.current) gutter.current.scrollTop = e.currentTarget.scrollTop; }}
          readOnly={readOnly}
          spellCheck={false}
          rows={rows}
          className="w-full resize-none overflow-auto whitespace-pre bg-transparent px-3 py-2 font-mono text-[12.5px] leading-[1.6] outline-none"
          aria-label="box.yaml"
        />
      </div>
      {!readOnly && (
        <div className="min-h-5 text-[13px]">
          {checking ? (
            <span className="text-muted-foreground">{t("checking")}</span>
          ) : result?.ok ? (
            <span className="flex items-center gap-1.5 text-st-running"><CheckCircle2 className="size-4" /> {t("valid")}</span>
          ) : result ? (
            <ul className="flex flex-col gap-0.5 text-st-error">
              {result.errors.map((e, i) => (
                <li key={i} className="flex items-start gap-1.5"><XCircle className="mt-0.5 size-4 shrink-0" /> {t("lineError", { line: e.line, message: e.message })}</li>
              ))}
            </ul>
          ) : null}
        </div>
      )}
    </div>
  );
}
