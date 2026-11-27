"use client";
// Nhận lời mời workspace (spec §3.5): "Lan mời anh vào workspace kecon-team với vai trò Developer".
// Đã đăng nhập đúng email → "Tham gia" (POST /v1/workspace-invites/{token}/accept, Core chuyển phiên sang workspace đó);
// chưa → đăng nhập (có tài khoản) hoặc đăng ký với workspace_invite rồi tự quay lại.
import * as React from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { useTranslations } from "next-intl";
import { Building2, CircleAlert, LogIn, LogOut, UserPlus, Users } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { AuthCardHeader, FormError } from "@/components/auth-ui";
import { useRoleLabel, useLogout } from "@/components/boxmes/workspace";
import { ApiError, api } from "@/lib/api";
import { useSession } from "@/lib/session";
import { useFmt } from "@/lib/format";
import type { InvitePreview } from "@/lib/types";

export function InviteCard({ token }: { token: string }) {
  const t = useTranslations("invite");
  const router = useRouter();
  const roleLabel = useRoleLabel();
  const { dateTime } = useFmt();
  const { session, me, refresh } = useSession();
  const logout = useLogout();
  const [inv, setInv] = React.useState<InvitePreview | null>(null);
  const [loadErr, setLoadErr] = React.useState<ApiError | null>(null);
  const [err, setErr] = React.useState<string | null>(null);
  const [busy, setBusy] = React.useState(false);

  React.useEffect(() => {
    api.invitePreview(token).then(setInv).catch((e) => setLoadErr(e instanceof ApiError ? e : new ApiError(0, null, String(e))));
  }, [token]);

  const here = `/invite/${encodeURIComponent(token)}`;
  const accept = async () => {
    setBusy(true);
    setErr(null);
    try {
      await api.acceptInvite(token);
      await refresh();
      router.push("/");
      router.refresh();
    } catch (e) {
      setErr(e instanceof ApiError ? [e.message, e.hint].filter(Boolean).join(" — ") : String(e));
    } finally {
      setBusy(false);
    }
  };

  if (loadErr) {
    return (
      <Card className="p-6">
        <AuthCardHeader title={t("invalidTitle")} />
        <FormError>{[loadErr.message, loadErr.hint].filter(Boolean).join(" — ")}</FormError>
        <Button asChild variant="ghost" className="mt-4 w-full"><Link href="/login">{t("toLogin")}</Link></Button>
      </Card>
    );
  }
  if (!inv || !session) return <Card className="h-72 animate-pulse p-6" />;

  const userEmail = me?.user?.email ?? "";
  const sameUser = Boolean(userEmail) && userEmail.toLowerCase() === inv.email.toLowerCase();
  const signedInOther = Boolean(userEmail) && !sameUser;
  const alreadyMember = sameUser && Boolean(me?.workspaces.some((w) => w.id === inv.workspace.id));
  const qs = new URLSearchParams({ email: inv.email, next: here });
  const signupQs = new URLSearchParams({ workspace_invite: token, email: inv.email, next: "/" });

  return (
    <Card className="min-w-0 break-words p-6">
      <AuthCardHeader title={t("title")} />
      <div className="flex flex-col gap-3">
        <div className="flex items-start gap-3 rounded-lg border border-border bg-background p-3.5">
          <span className="grid size-9 shrink-0 place-items-center rounded-md bg-surface-2"><Building2 className="size-4" /></span>
          <div className="min-w-0">
            <p className="text-[13.5px] leading-snug">
              {t.rich("headline", { who: inv.invited_by || "?", ws: inv.workspace.name, role: roleLabel(inv.role), b: (c) => <b className="font-semibold">{c}</b> })}
            </p>
            <p className="mt-1 text-[12px] text-muted-foreground">{t("forEmail", { email: inv.email })}</p>
            <div className="mt-2 flex flex-wrap gap-1.5">
              <Badge variant={inv.status === "pending" ? "blue" : inv.status === "accepted" ? "green" : "gray"}>{t(`status.${inv.status}` as "status.pending")}</Badge>
              {inv.status === "pending" && <span className="text-[12px] text-muted-foreground">{t("expires", { time: dateTime(inv.expires_at) })}</span>}
            </div>
          </div>
        </div>

        {inv.status !== "pending" ? (
          <>
            <p className="flex items-start gap-2 text-[13px] text-muted-foreground"><CircleAlert className="mt-0.5 size-4 shrink-0" /> {inv.status === "expired" ? t("expiredDesc") : t("acceptedDesc")}</p>
            <Button asChild variant="secondary"><Link href={me?.user ? "/" : `/login?${qs}`}>{me?.user ? t("toDashboard") : t("toLogin")}</Link></Button>
          </>
        ) : alreadyMember ? (
          <>
            <p className="text-[13px] text-muted-foreground">{t("alreadyMember")}</p>
            <Button asChild><Link href="/">{t("toDashboard")}</Link></Button>
          </>
        ) : sameUser ? (
          <Button onClick={accept} disabled={busy}><Users /> {busy ? t("joining") : t("join")}</Button>
        ) : signedInOther ? (
          <>
            <p className="text-[13px] text-st-warning">{t("otherEmail", { current: userEmail, email: inv.email })}</p>
            <Button variant="secondary" onClick={async () => { await logout(false); }}><LogOut /> {t("switchAccount")}</Button>
          </>
        ) : session.mode === "key" ? (
          <>
            <p className="text-[13px] text-muted-foreground">{t("keyMode")}</p>
            <Button variant="secondary" onClick={() => logout(false)}><LogOut /> {t("switchAccount")}</Button>
          </>
        ) : session.mock ? (
          <Button onClick={accept} disabled={busy}><Users /> {busy ? t("joining") : t("join")}</Button>
        ) : inv.account_exists ? (
          <Button asChild><Link href={`/login?${qs}`}><LogIn /> {t("loginToJoin")}</Link></Button>
        ) : (
          <>
            <Button asChild><Link href={`/signup?${signupQs}`}><UserPlus /> {t("signupToJoin")}</Link></Button>
            <p className="text-center text-[12.5px] text-muted-foreground">
              {t("haveAccount")} <Link href={`/login?${qs}`} className="text-primary hover:underline">{t("loginLink")}</Link>
            </p>
          </>
        )}
        {err && <FormError>{err}</FormError>}
      </div>
    </Card>
  );
}
