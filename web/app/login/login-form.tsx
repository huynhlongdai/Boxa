"use client";
import * as React from "react";
import { useRouter, useSearchParams } from "next/navigation";
import { Box as BoxIcon, KeyRound, Lock } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Field, Input } from "@/components/ui/input";

export function LoginForm() {
  const router = useRouter();
  const sp = useSearchParams();
  const [key, setKey] = React.useState("");
  const [err, setErr] = React.useState<string | null>(null);
  const [busy, setBusy] = React.useState(false);
  const [mock, setMock] = React.useState(false);

  React.useEffect(() => {
    fetch("/api/session").then((r) => r.json()).then((s: { mock: boolean }) => setMock(s.mock)).catch(() => {});
  }, []);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setErr(null);
    const res = await fetch("/api/auth/login", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ key }) });
    setBusy(false);
    if (res.ok) {
      const next = sp.get("next");
      router.push(next && next.startsWith("/") ? next : "/");
      router.refresh();
    } else {
      const j = (await res.json().catch(() => null)) as { error?: { message?: string; hint?: string } } | null;
      setErr([j?.error?.message, j?.error?.hint].filter(Boolean).join(" — ") || "Đăng nhập thất bại");
    }
  };

  return (
    <Card className="w-full max-w-sm p-6">
      <div className="mb-5 flex items-center gap-2">
        <span className="grid size-9 place-items-center rounded-md bg-primary/15 text-primary"><BoxIcon className="size-5" /></span>
        <div>
          <h1 className="text-lg font-semibold">Đăng nhập Boxa</h1>
          <p className="text-[13px] text-muted-foreground">Dùng API key có scope admin</p>
        </div>
      </div>
      <form onSubmit={submit} className="flex flex-col gap-4">
        <Field label="API key admin" hint={<>Tạo bằng <span className="font-mono">box keys create --admin</span>. Key được lưu trong cookie httpOnly, không nằm trong JavaScript.</>}>
          <div className="relative">
            <KeyRound className="absolute left-2.5 top-2.5 size-4 text-muted-foreground" />
            <Input type="password" autoComplete="off" value={key} onChange={(e) => setKey(e.target.value)} placeholder="bx_live_…" className="pl-8 font-mono" />
          </div>
        </Field>
        {err && <p className="rounded-md border border-st-error/30 bg-st-error/10 px-3 py-2 text-[13px] text-st-error">{err}</p>}
        <Button type="submit" disabled={busy || !key.trim()}>
          <Lock /> {busy ? "Đang kiểm tra…" : "Đăng nhập"}
        </Button>
        {mock && (
          <Button type="button" variant="ghost" onClick={() => router.push("/")}>
            Đang ở MOCK MODE — vào luôn không cần key
          </Button>
        )}
      </form>
    </Card>
  );
}
