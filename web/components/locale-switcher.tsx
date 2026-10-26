"use client";
// Bộ chuyển ngôn ngữ: ghi cookie `boxa_locale` rồi render lại phía server (không đổi URL).
import * as React from "react";
import { useRouter } from "next/navigation";
import { useLocale, useTranslations } from "next-intl";
import { Check, Languages } from "lucide-react";
import { LOCALES, LOCALE_COOKIE, LOCALE_NATIVE, type Locale } from "@/i18n/config";
import { cn } from "@/lib/utils";

export function setLocaleCookie(l: Locale) {
  document.cookie = `${LOCALE_COOKIE}=${l}; path=/; max-age=${60 * 60 * 24 * 365}; samesite=lax`;
}

export function useSwitchLocale() {
  const router = useRouter();
  const [pending, start] = React.useTransition();
  const change = React.useCallback(
    (l: Locale) => {
      setLocaleCookie(l);
      start(() => router.refresh());
    },
    [router],
  );
  return { change, pending };
}

/** Dạng select gọn (sidebar footer). */
export function LocaleSelect({ className, compact }: { className?: string; compact?: boolean }) {
  const locale = useLocale() as Locale;
  const t = useTranslations("locale");
  const { change, pending } = useSwitchLocale();
  return (
    <label className={cn("relative flex items-center gap-1.5 text-[12px] text-muted-foreground", className)} title={t("label")}>
      <Languages className={cn("size-4 shrink-0", pending && "animate-pulse")} />
      <select
        aria-label={t("label")}
        value={locale}
        onChange={(e) => change(e.target.value as Locale)}
        className={cn(
          "cursor-pointer appearance-none bg-transparent text-[12px] text-muted-foreground outline-none hover:text-foreground",
          compact ? "absolute inset-0 opacity-0" : "pr-1",
        )}
      >
        {LOCALES.map((l) => (
          <option key={l} value={l} className="bg-popover text-foreground">
            {LOCALE_NATIVE[l]}
          </option>
        ))}
      </select>
    </label>
  );
}

/** Dạng lưới nút (sheet "Thêm" trên mobile, trang Cài đặt). */
export function LocaleGrid({ className, onChanged }: { className?: string; onChanged?: () => void }) {
  const locale = useLocale() as Locale;
  const { change } = useSwitchLocale();
  return (
    <div className={cn("grid grid-cols-2 gap-2 sm:grid-cols-4", className)} role="radiogroup">
      {LOCALES.map((l) => (
        <button
          key={l}
          type="button"
          role="radio"
          aria-checked={locale === l}
          lang={l}
          onClick={() => {
            change(l);
            onChanged?.();
          }}
          className={cn(
            "flex items-center justify-center gap-1.5 rounded-lg border px-3 py-2 text-[13px] transition-colors",
            locale === l ? "border-primary/50 bg-primary/10 text-primary" : "border-border bg-background hover:bg-surface-2/60",
          )}
        >
          {locale === l && <Check className="size-3.5" />}
          {LOCALE_NATIVE[l]}
        </button>
      ))}
    </div>
  );
}
