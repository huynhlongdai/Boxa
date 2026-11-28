"use client";
// Đăng ký (spec §3.1): email · mật khẩu (thanh độ mạnh) · tên · tên workspace · mã mời (khi signup_mode=invite) · đồng ý điều khoản.
// ?invite=<mã mời> tự điền mã; ?workspace_invite=<token>&email= khi đến từ lời mời workspace (/invite/<token>):
// tài khoản vào thẳng workspace đó, không cần mã mời, không tạo workspace riêng.
import * as React from "react";
import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { Check, ListPlus, UserPlus } from "lucide-react";
import { useTranslations } from "next-intl";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Field, Input } from "@/components/ui/input";
import { AuthCardHeader, FormError, safeNext } from "@/components/auth-ui";
import { ApiError, authPost, http, paths } from "@/lib/api";
import type { AuthConfig } from "@/lib/types";
import { cn } from "@/lib/utils";

function strength(pw: string): number {
  let s = 0;
  if (pw.length >= 10) s++;
  if (pw.length >= 14) s++;
  if (/[a-z]/.test(pw) && /[A-Z]/.test(pw)) s++;
  if (/\d/.test(pw)) s++;
  if (/[^A-Za-z0-9]/.test(pw)) s++;
  return Math.min(4, s);
}

function suggestWorkspace(email: string) {
  const [local, domain = ""] = email.split("@");
  const d = domain.split(".")[0] ?? "";
  const generic = ["gmail", "yahoo", "outlook", "hotmail", "icloud", "proton", "protonmail", "live", "mail"];
  const base = d && !generic.includes(d.toLowerCase()) ? d : local;
  return (base ?? "").replace(/[^a-zA-Z0-9-]+/g, "-").replace(/^-+|-+$/g, "").toLowerCase().slice(0, 40);
}

function Waitlist({ initialEmail }: { initialEmail: string }) {
  const t = useTranslations("signup");
  const [email, setEmail] = React.useState(initialEmail);
  const [pos, setPos] = React.useState<number | null>(null);
  const [err, setErr] = React.useState<string | null>(null);
  const [busy, setBusy] = React.useState(false);
  return (
    <div className="flex flex-col gap-3">
      <p className="text-[13px] text-muted-foreground">{t("closedDesc")}</p>
      {pos !== null ? (
        <p className="flex items-center gap-2 rounded-md border border-st-running/30 bg-st-running/10 px-3 py-2 text-[13px] text-st-running"><Check className="size-4" /> {t("waitlistJoined", { position: pos })}</p>
      ) : (
        <form
          className="flex flex-col gap-3"
          onSubmit={async (e) => {
            e.preventDefault();
            setBusy(true);
            setErr(null);
            try {
              const r = await http.post<{ position?: number }>("/waitlist", { email });
              setPos(r.position ?? 0);
            } catch (x) {
              setErr(x instanceof ApiError ? [x.message, x.hint].filter(Boolean).join(" — ") : String(x));
            } finally {
              setBusy(false);
            }
          }}
        >
          <Field label={t("email")}><Input type="email" required value={email} onChange={(e) => setEmail(e.target.value)} /></Field>
          {err && <FormError>{err}</FormError>}
          <Button type="submit" disabled={busy || !email.trim()}><ListPlus /> {t("joinWaitlist")}</Button>
        </form>
      )}
    </div>
  );
}

export function SignupForm() {
  const t = useTranslations("signup");
  const router = useRouter();
  const sp = useSearchParams();
  const wsInvite = sp.get("workspace_invite") ?? "";
  const next = safeNext(sp.get("next"), wsInvite ? "/" : "/onboarding");
  const [cfg, setCfg] = React.useState<AuthConfig | null>(null);
  const [email, setEmail] = React.useState(sp.get("email") ?? "");
  const [password, setPassword] = React.useState("");
  const [name, setName] = React.useState("");
  const [workspace, setWorkspace] = React.useState("");
  const [wsTouched, setWsTouched] = React.useState(false);
  const [code, setCode] = React.useState(sp.get("invite") ?? "");
  const [terms, setTerms] = React.useState(false);
  const [err, setErr] = React.useState<ApiError | string | null>(null);
  const [busy, setBusy] = React.useState(false);

  React.useEffect(() => {
    http.get<AuthConfig>(paths.authConfig).then(setCfg).catch(() => setCfg({ signup_mode: "invite", first_user: false, min_password_len: 8, email_verification: true, email_configured: false }));
  }, []);

  // Khớp core (manager/tenancy.go minPasswordLen = 8): web không tự nâng mức tối thiểu.
  const minLen = cfg?.min_password_len || 8;
  const needCode = cfg?.signup_mode === "invite" && !cfg.first_user && !wsInvite;
  const closed = cfg?.signup_mode === "closed" && !cfg.first_user && !wsInvite;
  const st = strength(password);
  const wsName = wsTouched ? workspace : suggestWorkspace(email);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setErr(null);
    try {
      await authPost("/api/auth/signup", {
        email,
        password,
        name,
        ...(wsInvite ? { workspace_invite: wsInvite } : { workspace: wsName }),
        ...(needCode ? { invite_code: code } : {}),
      });
      router.push(next);
      router.refresh();
    } catch (x) {
      setErr(x instanceof ApiError ? x : t("failed"));
    } finally {
      setBusy(false);
    }
  };

  const codeError = err instanceof ApiError && needCode && /invite/i.test(`${err.message} ${err.hint ?? ""}`);
  const loginHref = `/login?${new URLSearchParams({ ...(email ? { email } : {}), ...(sp.get("next") ? { next: sp.get("next")! } : {}) })}`;

  return (
    <div className="grid gap-6 md:grid-cols-[minmax(0,1fr)_minmax(0,0.85fr)]">
      <Card className="min-w-0 break-words p-6">
        <AuthCardHeader title={wsInvite ? t("titleInvite") : t("title")} subtitle={cfg?.first_user ? t("firstUser") : t("subtitle")} />
        {!cfg ? (
          <div className="h-64 animate-pulse rounded-md bg-surface-2" />
        ) : closed ? (
          <Waitlist initialEmail={email} />
        ) : (
          <form onSubmit={submit} className="flex flex-col gap-4">
            <Field label={t("email")} hint={wsInvite ? t("inviteEmailHint") : undefined}>
              <Input type="email" autoComplete="email" required value={email} onChange={(e) => setEmail(e.target.value)} readOnly={Boolean(wsInvite && sp.get("email"))} />
            </Field>
            <Field label={t("password")} hint={t("passwordHint", { n: minLen })}>
              <Input type="password" autoComplete="new-password" required minLength={minLen} maxLength={72} value={password} onChange={(e) => setPassword(e.target.value)} />
            </Field>
            <div className="-mt-2 flex items-center gap-2" aria-hidden={!password}>
              <div className="grid flex-1 grid-cols-4 gap-1">
                {[0, 1, 2, 3].map((i) => (
                  <span key={i} className={cn("h-1 rounded-full", i < st ? (st <= 1 ? "bg-st-error" : st === 2 ? "bg-st-warning" : "bg-st-running") : "bg-surface-2")} />
                ))}
              </div>
              <span className="w-20 text-right text-[11.5px] text-muted-foreground">{password ? t(`strength.${st}` as "strength.0") : ""}</span>
            </div>
            <Field label={t("name")}>
              <Input autoComplete="name" value={name} onChange={(e) => setName(e.target.value)} maxLength={80} />
            </Field>
            {!wsInvite && (
              <Field label={t("workspace")} hint={t("workspaceHint")}>
                <Input value={wsName} onChange={(e) => { setWsTouched(true); setWorkspace(e.target.value); }} maxLength={60} />
              </Field>
            )}
            {needCode && (
              <Field label={t("inviteCode")} hint={t("inviteCodeHint")}>
                <Input value={code} onChange={(e) => setCode(e.target.value.trim())} required className="font-mono" autoComplete="off" />
              </Field>
            )}
            <label className="flex items-start gap-2 text-[13px]">
              <input type="checkbox" checked={terms} onChange={(e) => setTerms(e.target.checked)} className="mt-0.5 size-4 accent-primary" />
              <span>{t("terms")}</span>
            </label>
            {err && (
              <FormError>
                {typeof err === "string" ? err : [err.message, err.hint].filter(Boolean).join(" — ")}
                {codeError && <span className="mt-1 block">{t("codeWaitlist")}</span>}
              </FormError>
            )}
            <Button type="submit" disabled={busy || !email.trim() || password.length < minLen || !terms || (needCode && !code)}>
              <UserPlus /> {busy ? t("creating") : wsInvite ? t("submitInvite") : t("submit")}
            </Button>
            <p className="text-center text-[13px] text-muted-foreground">
              {t("haveAccount")} <Link href={loginHref} className="text-primary hover:underline">{t("loginLink")}</Link>
            </p>
          </form>
        )}
      </Card>
      <div className="hidden min-w-0 flex-col justify-center gap-4 md:flex">
        {(["b1", "b2", "b3"] as const).map((k) => (
          <div key={k} className="flex gap-3">
            <span className="mt-0.5 grid size-6 shrink-0 place-items-center rounded-full bg-primary/15 text-primary"><Check className="size-3.5" /></span>
            <div className="min-w-0">
              <p className="text-sm font-medium">{t(`benefits.${k}`)}</p>
              <p className="text-[13px] text-muted-foreground">{t(`benefits.${k}Desc`)}</p>
            </div>
          </div>
        ))}
        <p className="rounded-md border border-border bg-surface px-3 py-2 font-mono text-[12.5px] text-muted-foreground">{t("priceExample")}</p>
      </div>
    </div>
  );
}
