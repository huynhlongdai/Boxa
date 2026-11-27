// Cấu hình ngôn ngữ dùng chung (client + server). Không phụ thuộc next/headers.
export const LOCALES = ["vi", "en", "zh", "ja"] as const;
export type Locale = (typeof LOCALES)[number];
export const DEFAULT_LOCALE: Locale = "vi";
export const LOCALE_COOKIE = "boxmes_locale";
/**
 * Tên cookie trước các lần đổi tên (Boxaz → Boxmes, rồi Boxa → Boxaz), mới nhất trước;
 * vẫn đọc để không mất lựa chọn ngôn ngữ cũ.
 */
export const LEGACY_LOCALE_COOKIES = ["boxaz_locale", "boxa_locale"] as const;

/** Đọc cookie ngôn ngữ: boxmes_locale → boxaz_locale → boxa_locale. */
export function localeCookie(jar: { get(name: string): { value: string } | undefined }): string | undefined {
  for (const name of [LOCALE_COOKIE, ...LEGACY_LOCALE_COOKIES]) {
    const v = jar.get(name)?.value;
    if (v) return v;
  }
  return undefined;
}
export const TIME_ZONE = "Asia/Ho_Chi_Minh";

/** Thẻ BCP 47 đầy đủ cho Intl (số, tiền, ngày). */
export const INTL_TAG: Record<Locale, string> = { vi: "vi-VN", en: "en-US", zh: "zh-CN", ja: "ja-JP" };

/** Tên ngôn ngữ viết bằng chính ngôn ngữ đó (cho bộ chuyển). */
export const LOCALE_NATIVE: Record<Locale, string> = { vi: "Tiếng Việt", en: "English", zh: "简体中文", ja: "日本語" }; // i18n-ignore

export function isLocale(v: unknown): v is Locale {
  return typeof v === "string" && (LOCALES as readonly string[]).includes(v);
}

/** Chuẩn hoá một thẻ ngôn ngữ bất kỳ (vd "zh-Hans-CN", "en_GB") về Locale hỗ trợ, hoặc null. */
export function normalizeLocale(tag: string | null | undefined): Locale | null {
  if (!tag) return null;
  const base = tag.trim().toLowerCase().replace("_", "-").split("-")[0];
  return isLocale(base) ? base : null;
}

/** Chọn locale từ header Accept-Language theo trọng số q. */
export function pickFromAcceptLanguage(header: string | null | undefined): Locale | null {
  if (!header) return null;
  const parts = header
    .split(",")
    .map((p, i) => {
      const [tag, ...params] = p.trim().split(";");
      const q = params.map((x) => x.trim()).find((x) => x.startsWith("q="));
      return { tag, q: q ? Number(q.slice(2)) || 0 : 1, i };
    })
    .filter((p) => p.tag && p.q > 0)
    .sort((a, b) => b.q - a.q || a.i - b.i);
  for (const p of parts) {
    const l = normalizeLocale(p.tag);
    if (l) return l;
  }
  return null;
}

/** Cookie `boxmes_locale` (hoặc tên cũ) > Accept-Language > mặc định (vi). */
export function resolveLocale(cookie: string | null | undefined, acceptLanguage: string | null | undefined): Locale {
  return normalizeLocale(cookie) ?? pickFromAcceptLanguage(acceptLanguage) ?? DEFAULT_LOCALE;
}
