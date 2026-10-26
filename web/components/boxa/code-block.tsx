"use client";
import * as React from "react";
import { Check, Copy } from "lucide-react";
import { toast } from "sonner";
import { useTranslations } from "next-intl";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

export async function copyText(text: string, okMsg: string, failMsg: string) {
  try {
    await navigator.clipboard.writeText(text);
    toast.success(okMsg);
    return true;
  } catch {
    toast.error(failMsg);
    return false;
  }
}

/** copyText với thông báo theo ngôn ngữ hiện tại. */
export function useCopy() {
  const t = useTranslations("common");
  return (text: string) => copyText(text, t("copied"), t("copyFailed"));
}

export function CopyButton({ text, label, className }: { text: string; label?: string; className?: string }) {
  const [done, setDone] = React.useState(false);
  const t = useTranslations("common");
  const copy = useCopy();
  return (
    <Button
      size="xs"
      variant="secondary"
      className={className}
      onClick={async () => {
        if (await copy(text)) {
          setDone(true);
          setTimeout(() => setDone(false), 1500);
        }
      }}
    >
      {done ? <Check /> : <Copy />}
      {done ? t("copied") : label ?? t("copy")}
    </Button>
  );
}

export function CodeBlock({ code, language, className, maxHeight = 360 }: { code: string; language?: string; className?: string; maxHeight?: number }) {
  return (
    <div className={cn("relative overflow-hidden rounded-md border border-border bg-background", className)}>
      <div className="flex items-center justify-between border-b border-border px-3 py-1.5">
        <span className="font-mono text-[11px] uppercase tracking-wide text-muted-foreground">{language ?? "text"}</span>
        <CopyButton text={code} />
      </div>
      <pre className="overflow-auto p-3 font-mono text-[12.5px] leading-relaxed text-foreground/90" style={{ maxHeight }}>
        <code>{code}</code>
      </pre>
    </div>
  );
}
