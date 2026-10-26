"use client";
import * as React from "react";
import { Check, Copy } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

export async function copyText(text: string, okMsg = "Đã copy") {
  try {
    await navigator.clipboard.writeText(text);
    toast.success(okMsg);
    return true;
  } catch {
    toast.error("Trình duyệt chặn clipboard — hãy bôi đen và copy tay");
    return false;
  }
}

export function CopyButton({ text, label = "Copy", className }: { text: string; label?: string; className?: string }) {
  const [done, setDone] = React.useState(false);
  return (
    <Button
      size="xs"
      variant="secondary"
      className={className}
      onClick={async () => {
        if (await copyText(text)) {
          setDone(true);
          setTimeout(() => setDone(false), 1500);
        }
      }}
    >
      {done ? <Check /> : <Copy />}
      {done ? "Đã copy" : label}
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
