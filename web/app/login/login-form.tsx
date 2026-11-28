"use client";
// Đăng nhập (spec §3.2): email + mật khẩu (phiên Core trong cookie HttpOnly); "Đăng nhập bằng API key" thu gọn cho tự host.
import * as React from "react";
import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { ChevronDown, KeyRound, Lock, LogIn, Mail } from "lucide-react";
import { AuthCardHeader, FormError, safeNext } from "@/components/auth-ui";
import { useTranslations } from "next-intl";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Field, Input } from "@/components/ui/input";
import { ApiError, authPost } from "@/lib/api";
import { cn } from "@/lib/utils";

export function LoginForm() {
  const router = useRouter();
  const sp = useSearchParams();
  const t = useTranslations("login");
  const next = safeNext(sp.get("next"));
  const [email, setEmail] = React.useState(sp.get("email") ?? "");
  const [password, setPassword] = React.useState("");
  const [key, setKey] = React.useState("");
  const [keyMode, setKeyMode] = React.useState(false);
  const [err, setErr] = React.useState<string | null>(null);
  const [busy, setBusy] = React.useState(false);
  const [mock, setMock] = React.useState(false);

  React.useEffect(() => {
    fetch("/api/session").then((r) => r.json()).then((s: { mock: boolean }) => setMock(s.mock)).catch(() => {});
  }, []);

  const describe = (e: unknown) => {
    if (e instanceof ApiError) {
      if (e.code === "RATE_LIMITED") return t("rateLimited", { minutes: Math.max(1, Math.ceil((e.retryAfter ?? 60) / 60)) });
      return [e.message, e.hint].filter(Boolean).join(" — ");
    }
    return t("failed");
  };

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setErr(null);
    try {
      await authPost("/api/auth/login", keyMode ? { key } : { email, password });
      router.push(next);
      router.refresh();
    } catch (x) {
      setErr(describe(x));
    } finally {
      setBusy(false);
    }
  };

  const signupHref = `/signup${next !== "/" ? `?next=${encodeURIComponent(next)}` : ""}`;

  return (
    <Card className="w-full min-w-0 max-w-sm break-words p-6">
      <AuthCardHeader title={t("title")} subtitle={keyMode ? t("keySubtitle") : t("subtitle")} />
      <form onSubmit={submit} className="flex flex-col gap-4">
        {!keyMode ? (
          <>
            <Field label={t("emailLabel")}>
              <div className="relative">
                <Mail className="absolute left-2.5 top-2.5 size-4 text-muted-foreground" />
                <Input type="email" autoComplete="email" required value={email} onChange={(e) => setEmail(e.target.value)} className="pl-8" placeholder="you@example.com" />
              </div>
            </Field>
            <Field label={t("passwordLabel")} hint={<Link href={`/forgot-password${email.trim() ? `?email=${encodeURIComponent(email.trim())}` : ""}`} className="text-primary hover:underline" data-forgot-link>{t("forgot")}</Link>}>
              <Input type="password" autoComplete="current-password" required value={password} onChange={(e) => setPassword(e.target.value)} />
            </Field>
          </>
        ) : (
          <Field label={t("keyLabel")} hint={t.rich("keyHint", { mono: (c) => <span className="font-mono">{c}</span> })}>
            <div className="relative">
              <KeyRound className="absolute left-2.5 top-2.5 size-4 text-muted-foreground" />
              <Input type="password" autoComplete="off" value={key} onChange={(e) => setKey(e.target.value)} placeholder="bx_live_…" className="pl-8 font-mono" />
            </div>
          </Field>
        )}
        {err && <FormError>{err}</FormError>}
        <Button type="submit" disabled={busy || (keyMode ? !key.trim() : !email.trim() || !password)}>
          {keyMode ? <Lock /> : <LogIn />} {busy ? t("checking") : t("submit")}
        </Button>
        {!keyMode && (
          <p className="text-center text-[13px] text-muted-foreground">
            {t("noAccount")} <Link href={signupHref} className="text-primary hover:underline">{t("signupLink")}</Link>
          </p>
        )}
        <button
          type="button"
          onClick={() => { setKeyMode((k) => !k); setErr(null); }}
          className="flex items-center justify-center gap-1 text-[12.5px] text-muted-foreground hover:text-foreground"
          aria-expanded={keyMode}
        >
          {keyMode ? t("useEmail") : t("useKey")} <ChevronDown className={cn("size-3.5 transition-transform", keyMode && "rotate-180")} />
        </button>
        {mock && (
          <Button type="button" variant="ghost" onClick={() => router.push("/")}>
            {t("mock")}
          </Button>
        )}
      </form>
    </Card>
  );
}
