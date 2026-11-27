"use client";
// v2.0: mảnh giao diện dùng chung cho /login, /signup, /invite/[token].
import * as React from "react";
import { Box as BoxIcon } from "lucide-react";
import { LocaleSelect } from "@/components/locale-switcher";

/** Chỉ cho quay lại đường dẫn nội bộ (chặn open redirect kiểu //evil.com). */
export function safeNext(v: string | null, fallback = "/") {
  return v && v.startsWith("/") && !v.startsWith("//") && !v.startsWith("/\\") ? v : fallback;
}

export function AuthCardHeader({ title, subtitle }: { title: string; subtitle?: string }) {
  return (
    <div className="mb-5 flex items-center gap-2">
      <span className="grid size-9 shrink-0 place-items-center rounded-md bg-primary/15 text-primary"><BoxIcon className="size-5" /></span>
      <div className="min-w-0">
        <h1 className="text-lg font-semibold">{title}</h1>
        {subtitle && <p className="text-[13px] text-muted-foreground">{subtitle}</p>}
      </div>
    </div>
  );
}

export function FormError({ children }: { children: React.ReactNode }) {
  return <p role="alert" className="break-words rounded-md border border-st-error/30 bg-st-error/10 px-3 py-2 text-[13px] text-st-error">{children}</p>;
}

/** Khung trang công khai: chọn ngôn ngữ góc phải, nội dung giữa màn. */
export function AuthPage({ children, wide }: { children: React.ReactNode; wide?: boolean }) {
  return (
    <div className="grid min-h-dvh place-items-center px-4 py-10">
      <div className="absolute right-4 top-4"><LocaleSelect /></div>
      <div className={wide ? "w-full min-w-0 max-w-4xl" : "w-full min-w-0 max-w-sm"}>{children}</div>
    </div>
  );
}
