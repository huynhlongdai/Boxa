"use client";
// v2.0 (bổ sung 2026-11-28): quên mật khẩu, đặt lại mật khẩu, xác thực email — các trang công khai.
// Core: POST /v1/auth/password/forgot {email} (202, luôn cùng câu trả lời), POST /v1/auth/password/reset {token, password}
// (đăng xuất mọi phiên), POST /v1/auth/verify-email {token} → {ok, email}. Link trong email: /reset-password?token=…, /verify-email?token=…
// Proxy cho phép các route này không cần đăng nhập (app/api/boxmes isPublic).
import * as React from "react";
import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { CheckCircle2, KeyRound, Mail, MailCheck } from "lucide-react";
import { useTranslations } from "next-intl";
import { AuthCardHeader, FormError } from "@/components/auth-ui";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Field, Input } from "@/components/ui/input";
import { ApiError, http, paths } from "@/lib/api";
import type { AuthConfig } from "@/lib/types";

function describe(e: unknown, fallback: string) {
  if (e instanceof ApiError) return [e.message, e.hint].filter(Boolean).join(" — ");
  return fallback;
}

export function ForgotPasswordForm() {
  const t = useTranslations("authFlow");
  const sp = useSearchParams();
  const [email, setEmail] = React.useState(sp.get("email") ?? "");
  const [sent, setSent] = React.useState(false);
  const [err, setErr] = React.useState<string | null>(null);
  const [busy, setBusy] = React.useState(false);
  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setErr(null);
    try {
      await http.post("/auth/password/forgot", { email: email.trim() });
      setSent(true);
    } catch (x) {
      setErr(describe(x, t("failed")));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Card className="w-full min-w-0 max-w-sm break-words p-6" data-forgot-password>
      <AuthCardHeader title={t("forgotTitle")} subtitle={t("forgotSubtitle")} />
      {sent ? (
        <div className="flex flex-col gap-4">
          <p className="flex items-start gap-2 text-[13.5px]"><MailCheck className="mt-0.5 size-4 shrink-0 text-st-running" />{t("forgotSent", { email: email.trim() })}</p>
          <Button variant="outline" asChild><Link href={`/login?email=${encodeURIComponent(email.trim())}`}>{t("backToLogin")}</Link></Button>
        </div>
      ) : (
        <form onSubmit={submit} className="flex flex-col gap-4">
          <Field label={t("email")}>
            <div className="relative">
              <Mail className="absolute left-2.5 top-2.5 size-4 text-muted-foreground" />
              <Input type="email" autoComplete="email" required value={email} onChange={(e) => setEmail(e.target.value)} className="pl-8" placeholder="you@example.com" autoFocus />
            </div>
          </Field>
          {err && <FormError>{err}</FormError>}
          <Button type="submit" disabled={busy || !email.trim()}>{busy ? t("sending") : t("sendLink")}</Button>
          <p className="text-center text-[13px] text-muted-foreground"><Link href="/login" className="text-primary hover:underline">{t("backToLogin")}</Link></p>
        </form>
      )}
    </Card>
  );
}

export function ResetPasswordForm() {
  const t = useTranslations("authFlow");
  const sp = useSearchParams();
  const router = useRouter();
  const token = sp.get("token") ?? "";
  const [cfg, setCfg] = React.useState<AuthConfig | null>(null);
  const [password, setPassword] = React.useState("");
  const [confirm, setConfirm] = React.useState("");
  const [done, setDone] = React.useState(false);
  const [err, setErr] = React.useState<string | null>(null);
  const [busy, setBusy] = React.useState(false);
  React.useEffect(() => {
    http.get<AuthConfig>(paths.authConfig).then(setCfg).catch(() => setCfg(null));
  }, []);
  const minLen = cfg?.min_password_len || 8;
  const bytes = new TextEncoder().encode(password).length;
  const mismatch = confirm.length > 0 && confirm !== password;
  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setErr(null);
    try {
      await http.post("/auth/password/reset", { token, password });
      setDone(true);
      setTimeout(() => router.push("/login"), 2500);
    } catch (x) {
      setErr(describe(x, t("failed")));
    } finally {
      setBusy(false);
    }
  };
  if (!token) {
    return (
      <Card className="w-full min-w-0 max-w-sm break-words p-6">
        <AuthCardHeader title={t("resetTitle")} />
        <FormError>{t("noToken")}</FormError>
        <Button className="mt-4 w-full" variant="outline" asChild><Link href="/forgot-password">{t("requestNew")}</Link></Button>
      </Card>
    );
  }
  return (
    <Card className="w-full min-w-0 max-w-sm break-words p-6" data-reset-password>
      <AuthCardHeader title={t("resetTitle")} subtitle={t("resetSubtitle")} />
      {done ? (
        <div className="flex flex-col gap-4">
          <p className="flex items-start gap-2 text-[13.5px]"><CheckCircle2 className="mt-0.5 size-4 shrink-0 text-st-running" />{t("resetDone")}</p>
          <Button asChild><Link href="/login">{t("backToLogin")}</Link></Button>
        </div>
      ) : (
        <form onSubmit={submit} className="flex flex-col gap-4">
          <Field label={t("newPassword")} hint={t("passwordHint", { n: minLen })}>
            <Input type="password" autoComplete="new-password" required minLength={minLen} value={password} onChange={(e) => setPassword(e.target.value)} autoFocus />
          </Field>
          <Field label={t("confirmPassword")}>
            <Input type="password" autoComplete="new-password" required value={confirm} onChange={(e) => setConfirm(e.target.value)} />
          </Field>
          {mismatch && <FormError>{t("mismatch")}</FormError>}
          {bytes > 72 && <FormError>{t("tooLong")}</FormError>}
          {err && <FormError>{err}</FormError>}
          <Button type="submit" disabled={busy || password.length < minLen || bytes > 72 || confirm !== password}><KeyRound /> {busy ? t("saving") : t("setPassword")}</Button>
          <p className="text-center text-[13px] text-muted-foreground"><Link href="/forgot-password" className="text-primary hover:underline">{t("requestNew")}</Link></p>
        </form>
      )}
    </Card>
  );
}

export function VerifyEmailView() {
  const t = useTranslations("authFlow");
  const sp = useSearchParams();
  const token = sp.get("token") ?? "";
  const [state, setState] = React.useState<{ status: "idle" | "busy" | "ok" | "error"; email?: string; error?: string }>({ status: token ? "busy" : "error", error: token ? undefined : t("noToken") });
  const started = React.useRef(false);
  React.useEffect(() => {
    if (!token || started.current) return;
    started.current = true;
    http.post<{ ok: boolean; email?: string }>("/auth/verify-email", { token })
      .then((r) => setState({ status: "ok", email: r.email }))
      .catch((x) => setState({ status: "error", error: describe(x, t("failed")) }));
  }, [token, t]);
  return (
    <Card className="w-full min-w-0 max-w-sm break-words p-6" data-verify-email>
      <AuthCardHeader title={t("verifyTitle")} />
      {state.status === "busy" && <p className="text-[13.5px] text-muted-foreground">{t("verifying")}</p>}
      {state.status === "ok" && (
        <div className="flex flex-col gap-4">
          <p className="flex items-start gap-2 text-[13.5px]"><CheckCircle2 className="mt-0.5 size-4 shrink-0 text-st-running" />{state.email ? t("verifiedEmail", { email: state.email }) : t("verified")}</p>
          <Button asChild><Link href="/">{t("toDashboard")}</Link></Button>
        </div>
      )}
      {state.status === "error" && (
        <div className="flex flex-col gap-4">
          <FormError>{state.error}</FormError>
          <p className="text-[13px] text-muted-foreground">{t("verifyResendHint")}</p>
          <Button variant="outline" asChild><Link href="/">{t("toDashboard")}</Link></Button>
        </div>
      )}
    </Card>
  );
}
