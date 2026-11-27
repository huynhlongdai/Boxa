import { cookies } from "next/headers";
import { NextResponse } from "next/server";
import { sessionKey, isMock } from "@/lib/server/config";

export const dynamic = "force-dynamic";

/** Thông tin phiên cho client: có đang mock không, đã đăng nhập chưa, URL WebSocket terminal. Không trả key. */
export async function GET() {
  const jar = await cookies();
  const key = sessionKey(jar);
  const mock = await isMock();
  return NextResponse.json({
    mock,
    authed: mock || Boolean(key),
    key_prefix: key ? `${key.slice(0, 12)}…` : null,
    ws_base: process.env.NEXT_PUBLIC_BOXMES_WS || null,
  });
}
