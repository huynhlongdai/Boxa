import { cookies, headers } from "next/headers";
import { getRequestConfig } from "next-intl/server";
import { LEGACY_LOCALE_COOKIE, LOCALE_COOKIE, TIME_ZONE, resolveLocale } from "./config";

export default getRequestConfig(async () => {
  const jar = await cookies();
  const h = await headers();
  const locale = resolveLocale((jar.get(LOCALE_COOKIE)?.value ?? jar.get(LEGACY_LOCALE_COOKIE)?.value), h.get("accept-language"));
  return {
    locale,
    timeZone: TIME_ZONE,
    messages: (await import(`../messages/${locale}.json`)).default,
    onError(error) {
      // Thiếu key: ghi log (smoke script bắt chuỗi MISSING_MESSAGE), không làm sập trang.
      console.error(`[i18n] ${error.code}: ${error.message}`);
    },
    getMessageFallback({ namespace, key }) {
      return [namespace, key].filter(Boolean).join(".");
    },
  };
});
