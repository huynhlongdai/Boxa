"use client";
// Định dạng theo ngôn ngữ đang chọn: tiền USD, số, ngày giờ, thời gian tương đối, thời lượng, dung lượng.
// Dùng Intl với thẻ BCP 47 của locale (vi-VN, en-US, zh-CN, ja-JP), giờ hiển thị theo Asia/Ho_Chi_Minh.
import * as React from "react";
import { useLocale, useTranslations } from "next-intl";
import { INTL_TAG, TIME_ZONE, isLocale, type Locale } from "@/i18n/config";

export interface Fmt {
  locale: Locale;
  tag: string;
  /** Tiền USD: |n| < 1 giữ 4 chữ số thập phân, còn lại 2 (hoặc `digits`). */
  usd: (n: number | null | undefined, digits?: number) => string;
  /** Số thường (nhóm hàng nghìn theo locale). */
  num: (n: number, maxDigits?: number) => string;
  /** Số rút gọn: 12K, 1,2 N, 1.2万… */
  compact: (n: number) => string;
  /** Phần trăm từ 0–100. */
  pct: (n: number) => string;
  /** Ngày + giờ ngắn (dd/mm hh:mm) theo giờ Việt Nam. */
  dateTime: (iso: string | null | undefined) => string;
  /** Ngày ngắn cho trục biểu đồ (yyyy-mm-dd → 26/10). */
  shortDate: (isoDate: string) => string;
  /** "3 phút trước", "in 2 hours"… */
  relTime: (iso: string | null | undefined) => string;
  /** Thời lượng từ một thời điểm tới giờ. */
  since: (iso: string | null | undefined) => string;
  /** Thời lượng từ số giây. */
  seconds: (s: number) => string;
  /** Mili giây: 120 ms, 1.4 s, 3 phút 2 giây. */
  ms: (n: number) => string;
  bytes: (n: number) => string;
  mb: (n: number) => string;
  /** Đơn vị đơn lẻ (minute/hour/day/second) dạng ngắn theo locale. */
  unit: (n: number, unit: "second" | "minute" | "hour" | "day", display?: "long" | "short" | "narrow") => string;
}

const cache = new Map<string, Intl.NumberFormat>();
function nf(tag: string, opts: Intl.NumberFormatOptions) {
  const k = tag + JSON.stringify(opts);
  let f = cache.get(k);
  if (!f) {
    f = new Intl.NumberFormat(tag, opts);
    cache.set(k, f);
  }
  return f;
}

export function makeFmt(locale: Locale, words: { never: string; justNow: string }): Fmt {
  const tag = INTL_TAG[locale];
  const rtf = new Intl.RelativeTimeFormat(tag, { numeric: "auto" });
  const unit: Fmt["unit"] = (n, u, display = "short") => nf(tag, { style: "unit", unit: u, unitDisplay: display, maximumFractionDigits: 1 }).format(n);
  const join = (a: string, b: string) => (locale === "zh" || locale === "ja" ? `${a}${b}` : `${a} ${b}`);
  const seconds = (s: number) => {
    s = Math.max(0, s);
    if (s < 60) return unit(Math.round(s), "second");
    if (s < 3600) return unit(Math.round(s / 60), "minute");
    if (s < 86400) {
      const h = Math.floor(s / 3600);
      const m = Math.round((s % 3600) / 60);
      return m ? join(unit(h, "hour"), unit(m, "minute")) : unit(h, "hour");
    }
    const d = Math.floor(s / 86400);
    const h = Math.floor((s % 86400) / 3600);
    return h ? join(unit(d, "day"), unit(h, "hour")) : unit(d, "day");
  };
  return {
    locale,
    tag,
    usd: (n, digits) => {
      if (n === null || n === undefined || Number.isNaN(n)) return "—";
      const d = digits ?? (Math.abs(n) < 1 ? 4 : 2);
      return nf(tag, { style: "currency", currency: "USD", currencyDisplay: "narrowSymbol", minimumFractionDigits: d, maximumFractionDigits: d }).format(n);
    },
    num: (n, maxDigits = 2) => nf(tag, { maximumFractionDigits: maxDigits }).format(n),
    compact: (n) => nf(tag, { notation: "compact", maximumFractionDigits: 1 }).format(n),
    pct: (n) => nf(tag, { style: "percent", maximumFractionDigits: 0 }).format(n / 100),
    dateTime: (iso) => {
      if (!iso) return "—";
      return new Date(iso).toLocaleString(tag, { timeZone: TIME_ZONE, hour: "2-digit", minute: "2-digit", day: "2-digit", month: "2-digit" });
    },
    shortDate: (isoDate) => {
      const d = new Date(isoDate.length === 10 ? `${isoDate}T00:00:00Z` : isoDate);
      return d.toLocaleDateString(tag, { day: "2-digit", month: "2-digit", timeZone: "UTC" });
    },
    relTime: (iso) => {
      if (!iso) return words.never;
      const diff = (Date.parse(iso) - Date.now()) / 1000;
      const abs = Math.abs(diff);
      if (abs < 45) return words.justNow;
      if (abs < 3600) return rtf.format(Math.round(diff / 60), "minute");
      if (abs < 86400) return rtf.format(Math.round(diff / 3600), "hour");
      return rtf.format(Math.round(diff / 86400), "day");
    },
    since: (iso) => (iso ? seconds((Date.now() - Date.parse(iso)) / 1000) : "—"),
    seconds,
    ms: (n) => {
      if (n < 1000) return `${nf(tag, { maximumFractionDigits: 0 }).format(n)} ms`;
      if (n < 60_000) return `${nf(tag, { maximumFractionDigits: 1 }).format(n / 1000)} s`;
      return join(unit(Math.floor(n / 60_000), "minute"), unit(Math.round((n % 60_000) / 1000), "second"));
    },
    bytes: (n) => {
      if (n < 1024) return `${nf(tag, {}).format(n)} B`;
      if (n < 1024 * 1024) return `${nf(tag, { maximumFractionDigits: 1 }).format(n / 1024)} KB`;
      return `${nf(tag, { maximumFractionDigits: 1 }).format(n / 1024 / 1024)} MB`;
    },
    mb: (n) => (n >= 1024 ? `${nf(tag, { maximumFractionDigits: n % 1024 ? 1 : 0 }).format(n / 1024)} GB` : `${nf(tag, {}).format(n)} MB`),
    unit,
  };
}

/** Hook: bộ định dạng theo locale hiện tại. */
export function useFmt(): Fmt {
  const raw = useLocale();
  const t = useTranslations("fmt");
  const locale: Locale = isLocale(raw) ? raw : "vi";
  const never = t("never");
  const justNow = t("justNow");
  return React.useMemo(() => makeFmt(locale, { never, justNow }), [locale, never, justNow]);
}
