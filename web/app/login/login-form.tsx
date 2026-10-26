"use client";
import * as React from "react";
import { useRouter, useSearchParams } from "next/navigation";
import { Box as BoxIcon, KeyRound, Lock } from "lucide-react";
import { useTranslations } from "next-intl";
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
  const t = useTranslations("login");

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
      setErr([j?.error?.message, j?.error?.hint].filter(Boolean).join(" — ") || t("failed"));
    }
  };

  return (
    <Card className="w-full min-w-0 max-w-sm break-words p-6">
      <div className="mb-5 flex items-center gap-2">
        <span className="grid size-9 place-items-center rounded-md bg-primary/15 text-primary"><BoxIcon className="size-5" /></span>
        <div>
          <h1 className="text-lg font-semibold">{t("title")}</h1>
          <p className="text-[13px] text-muted-foreground">{t("subtitle")}</p>
        </div>
      </div>
      <form onSubmit={submit} className="flex flex-col gap-4">
        <Field label={t("keyLabel")} hint={t.rich("keyHint", { mono: (c) => <span className="font-mono">{c}</span> })}>
          <div className="relative">
            <KeyRound className="absolute left-2.5 top-2.5 size-4 text-muted-foreground" />
            <Input type="password" autoComplete="off" value={key} onChange={(e) => setKey(e.target.value)} placeholder="bx_live_…" className="pl-8 font-mono" />
          </div>
        </Field>
        {err && <p className="rounded-md border border-st-error/30 bg-st-error/10 px-3 py-2 text-[13px] text-st-error">{err}</p>}
        <Button type="submit" disabled={busy || !key.trim()}>
          <Lock /> {busy ? t("checking") : t("submit")}
        </Button>
        {mock && (
          <Button type="button" variant="ghost" onClick={() => router.push("/")}>
            {t("mock")}
          </Button>
        )}
      </form>
    </Card>
  );
}
