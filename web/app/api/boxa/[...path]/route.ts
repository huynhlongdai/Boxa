// Proxy: trình duyệt → /api/boxa/<path> → Boxa Core (<BOXA_API>/<path>), gắn Bearer key từ cookie httpOnly.
// Tiền tố đặc biệt "_root/": /api/boxa/_root/llms.txt → <gốc Core>/llms.txt (bỏ /v1) cho các endpoint discovery v1.1.
// Accept-Language: lấy theo cookie boxa_locale (ngôn ngữ dashboard) để lỗi từ Core trả về đúng ngôn ngữ; không có cookie thì
// chuyển nguyên header của trình duyệt.
// Ở MOCK MODE, trả dữ liệu giả lập từ lib/mock theo đúng contract.
import { cookies } from "next/headers";
import { NextRequest } from "next/server";
import { getTranslations } from "next-intl/server";
import { COOKIE_NAME, coreBase, isMock } from "@/lib/server/config";
import { LOCALE_COOKIE, normalizeLocale, resolveLocale } from "@/i18n/config";
import { handleMock } from "@/lib/mock/handler";
import { mockStream } from "@/lib/mock/stream";

export const dynamic = "force-dynamic";
export const runtime = "nodejs";

type Ctx = { params: Promise<{ path: string[] }> };

const json = (status: number, body: unknown, extra?: Record<string, string>) =>
  new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json; charset=utf-8", ...extra } });

/** Các endpoint không cần đăng nhập (contract v1.1: discovery + health). */
function isPublic(segs: string[]) {
  return segs[0] === "health" || segs[0] === "_root" || segs[0] === "openapi.json" || segs[0] === "tools" || segs[0] === "agent-kinds";
}

async function handle(req: NextRequest, ctx: Ctx) {
  const { path } = await ctx.params;
  const segs = path ?? [];
  const search = req.nextUrl.searchParams;
  const jar = await cookies();
  const key = jar.get(COOKIE_NAME)?.value;
  const mock = await isMock();
  const cookieLocale = normalizeLocale(jar.get(LOCALE_COOKIE)?.value);
  const browserLang = req.headers.get("accept-language");
  const locale = resolveLocale(cookieLocale, browserLang);
  const acceptLanguage = cookieLocale ? `${cookieLocale}, en;q=0.5` : browserLang || locale;

  if (mock) {
    if (segs[0] === "stream") {
      return new Response(mockStream(req.signal), {
        headers: { "Content-Type": "text/event-stream; charset=utf-8", "Cache-Control": "no-cache, no-transform", Connection: "keep-alive", "X-Boxa-Mock": "1" },
      });
    }
    let body: Record<string, unknown> = {};
    if (!["GET", "HEAD"].includes(req.method)) {
      try {
        body = (await req.json()) as Record<string, unknown>;
      } catch {
        body = {};
      }
    }
    const apiBase = process.env.NEXT_PUBLIC_BOXA_API || "http://localhost:8080/v1";
    const r = handleMock(req.method, segs, search, body, apiBase, locale);
    const extra = { "X-Boxa-Mock": "1", "Content-Language": locale, Vary: "Accept-Language, Cookie", ...r.headers };
    if (typeof r.body === "string") {
      return new Response(r.body, { status: r.status, headers: { "Content-Type": r.contentType ?? "text/plain; charset=utf-8", ...extra } });
    }
    return json(r.status, r.body, extra);
  }

  const t = await getTranslations({ locale, namespace: "apiErrors" });
  if (!key && !isPublic(segs)) {
    return json(401, { error: { code: "UNAUTHORIZED", message: t("notLoggedIn"), hint: t("notLoggedInHint") } });
  }

  const root = segs[0] === "_root";
  const base = root ? coreBase().replace(/\/v1$/, "") : coreBase();
  const rest = root ? segs.slice(1) : segs;
  const target = `${base}/${rest.map(encodeURIComponent).join("/")}${search.toString() ? `?${search}` : ""}`;
  const headers = new Headers();
  for (const h of ["content-type", "accept", "idempotency-key", "last-event-id"]) {
    const v = req.headers.get(h);
    if (v) headers.set(h, v);
  }
  headers.set("Accept-Language", acceptLanguage);
  if (key) headers.set("Authorization", `Bearer ${key}`);

  try {
    const upstream = await fetch(target, {
      method: req.method,
      headers,
      body: ["GET", "HEAD"].includes(req.method) ? undefined : await req.arrayBuffer(),
      cache: "no-store",
      signal: req.signal,
      redirect: "manual",
    });
    const out = new Headers();
    for (const h of ["content-type", "content-disposition", "cache-control", "content-language", "retry-after"]) {
      const v = upstream.headers.get(h);
      if (v) out.set(h, v);
    }
    if (segs[0] === "stream") {
      out.set("Cache-Control", "no-cache, no-transform");
      out.set("Connection", "keep-alive");
    }
    return new Response(upstream.body, { status: upstream.status, headers: out });
  } catch (e) {
    return json(502, {
      error: { code: "PROVIDER_FAILED", message: t("coreUnreachable"), hint: `${t("coreUnreachableHint", { url: coreBase() })} ${(e as Error).message}` },
    });
  }
}

export const GET = handle;
export const POST = handle;
export const PUT = handle;
export const PATCH = handle;
export const DELETE = handle;
