// Proxy: trình duyệt → /api/boxa/<path> → Boxa Core (<BOXA_API>/<path>), gắn Bearer key từ cookie httpOnly.
// Ở MOCK MODE, trả dữ liệu giả lập từ lib/mock theo đúng contract.
import { cookies } from "next/headers";
import { NextRequest } from "next/server";
import { COOKIE_NAME, coreBase, isMock } from "@/lib/server/config";
import { handleMock } from "@/lib/mock/handler";
import { mockStream } from "@/lib/mock/stream";

export const dynamic = "force-dynamic";
export const runtime = "nodejs";

type Ctx = { params: Promise<{ path: string[] }> };

const json = (status: number, body: unknown, extra?: Record<string, string>) =>
  new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json; charset=utf-8", ...extra } });

async function handle(req: NextRequest, ctx: Ctx) {
  const { path } = await ctx.params;
  const segs = path ?? [];
  const search = req.nextUrl.searchParams;
  const jar = await cookies();
  const key = jar.get(COOKIE_NAME)?.value;
  const mock = await isMock();

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
    const r = handleMock(req.method, segs, search, body, apiBase);
    if (typeof r.body === "string") {
      return new Response(r.body, { status: r.status, headers: { "Content-Type": r.contentType ?? "text/plain; charset=utf-8", "X-Boxa-Mock": "1", ...r.headers } });
    }
    return json(r.status, r.body, { "X-Boxa-Mock": "1", ...r.headers });
  }

  if (!key && segs[0] !== "health") {
    return json(401, { error: { code: "UNAUTHORIZED", message: "Chưa đăng nhập", hint: "Mở /login và nhập API key admin." } });
  }

  const target = `${coreBase()}/${segs.map(encodeURIComponent).join("/")}${search.toString() ? `?${search}` : ""}`;
  const headers = new Headers();
  for (const h of ["content-type", "accept", "idempotency-key", "last-event-id"]) {
    const v = req.headers.get(h);
    if (v) headers.set(h, v);
  }
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
    for (const h of ["content-type", "content-disposition", "cache-control"]) {
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
      error: { code: "PROVIDER_FAILED", message: "Không kết nối được Boxa Core", hint: `Kiểm tra BOXA_API (${coreBase()}) hoặc bật BOXA_MOCK=1. ${(e as Error).message}` },
    });
  }
}

export const GET = handle;
export const POST = handle;
export const PUT = handle;
export const PATCH = handle;
export const DELETE = handle;
