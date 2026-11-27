"use client";
// v2.0: bộ chọn workspace (top bar), tạo workspace, menu người dùng, banner workspace, thẻ gói ở sidebar,
// trang "không đủ quyền".
import * as React from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { useTranslations } from "next-intl";
import { Building2, Check, ChevronsUpDown, Lock, LogOut, MailWarning, Plus, Settings, ShieldAlert, UserRound } from "lucide-react";
import { toast } from "sonner";
import { api, authPost, http, paths } from "@/lib/api";
import { useApi } from "@/lib/hooks";
import { toastError } from "@/lib/error-toast";
import { useSession, useSwitchWorkspace } from "@/lib/session";
import type { Perm, WorkspaceUsage } from "@/lib/types";
import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Field, Input } from "@/components/ui/input";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Menu, MenuItem, MenuLabel, MenuSeparator } from "@/components/ui/menu";

export function useRoleLabel() {
  const t = useTranslations("roles");
  return (role: string) => (role && ["owner", "admin", "developer", "billing", "viewer", "auditor"].includes(role) ? t(role as "owner") : t("key"));
}

export function usePlanLabel() {
  const t = useTranslations("plans");
  return (plan?: string) => (plan && ["free", "pro", "team", "unlimited"].includes(plan) ? t(plan as "free") : plan || t("free"));
}

export function CreateWorkspaceDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (o: boolean) => void }) {
  const t = useTranslations("workspace");
  const tc = useTranslations("common");
  const router = useRouter();
  const switchTo = useSwitchWorkspace();
  const [name, setName] = React.useState("");
  const [busy, setBusy] = React.useState(false);
  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!name.trim()) return;
    setBusy(true);
    try {
      const ws = await api.createWorkspace(name.trim());
      await switchTo(ws.id);
      toast.success(t("created", { name: ws.name }));
      setName("");
      onOpenChange(false);
      router.push("/");
    } catch (err) {
      toastError(err);
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-md">
        <form onSubmit={submit} className="flex flex-col gap-4">
          <DialogHeader>
            <DialogTitle>{t("createTitle")}</DialogTitle>
            <DialogDescription>{t("createDesc")}</DialogDescription>
          </DialogHeader>
          <Field label={t("name")}>
            <Input value={name} onChange={(e) => setName(e.target.value)} maxLength={60} autoFocus placeholder={t("namePlaceholder")} />
          </Field>
          <DialogFooter>
            <Button type="button" variant="ghost" onClick={() => onOpenChange(false)}>{tc("cancel")}</Button>
            <Button type="submit" disabled={busy || !name.trim()}><Plus /> {busy ? tc("processing") : t("create")}</Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

/** Top bar: tên workspace + gói; danh sách workspace của tôi (đổi = POST /v1/auth/workspace) + "Tạo workspace". */
export function WorkspaceSwitcher({ className }: { className?: string }) {
  const t = useTranslations("workspace");
  const roleLabel = useRoleLabel();
  const planLabel = usePlanLabel();
  const router = useRouter();
  const { me, session } = useSession();
  const switchTo = useSwitchWorkspace();
  const [creating, setCreating] = React.useState(false);
  const [busy, setBusy] = React.useState<string | null>(null);
  if (!session) return <div className={cn("h-9 w-40 animate-pulse rounded-md bg-surface-2", className)} />;
  const cur = me?.workspace ?? null;
  const canSwitch = session.mode !== "key" && Boolean(me?.user || session.mock);

  const pick = async (id: string) => {
    if (id === cur?.id) return;
    setBusy(id);
    try {
      const m = await switchTo(id);
      toast.success(t("switched", { name: m.workspace?.name ?? id }));
      router.refresh();
    } catch (e) {
      toastError(e);
    } finally {
      setBusy(null);
    }
  };

  return (
    <>
      <Menu
        label={t("switcher")}
        className="w-80"
        trigger={({ toggle, open }) => (
          <button
            type="button"
            onClick={toggle}
            aria-expanded={open}
            aria-label={t("switcher")}
            className={cn("flex h-9 min-w-0 max-w-[15rem] items-center gap-2 rounded-md border border-border bg-background px-2.5 text-left text-[13px] hover:bg-surface-2", className)}
          >
            <Building2 className="size-4 shrink-0 text-muted-foreground" />
            <span className="min-w-0 truncate font-medium">{cur?.name ?? t("none")}</span>
            {cur && <Badge variant="outline" className="hidden shrink-0 px-1.5 py-0 text-[10.5px] sm:inline-flex">{planLabel(cur.plan)}</Badge>}
            <ChevronsUpDown className="ml-auto size-3.5 shrink-0 text-muted-foreground" />
          </button>
        )}
      >
        <MenuLabel>{t("mine")}</MenuLabel>
        {(me?.workspaces ?? []).map((w) => (
          <MenuItem key={w.id} onSelect={() => pick(w.id)} disabled={!canSwitch || busy !== null} active={w.id === cur?.id}>
            <span className="min-w-0 flex-1">
              <span className="block truncate font-medium">{w.name}</span>
              <span className="block truncate text-[11.5px] text-muted-foreground">{roleLabel(w.role)} · {planLabel(w.plan)}{w.status === "suspended" ? ` · ${t("suspended")}` : ""}</span>
            </span>
            {w.id === cur?.id ? <Check className="text-primary" /> : busy === w.id ? <span className="text-[11px] text-muted-foreground">…</span> : null}
          </MenuItem>
        ))}
        {!me?.workspaces?.length && <p className="px-2.5 py-2 text-[12.5px] text-muted-foreground">{t("noneYet")}</p>}
        {session.mode === "key" && <p className="px-2.5 py-2 text-[12px] text-muted-foreground">{t("keyMode")}</p>}
        {canSwitch && (
          <>
            <MenuSeparator />
            <MenuItem onSelect={() => setCreating(true)}><Plus /> {t("create")}</MenuItem>
          </>
        )}
      </Menu>
      <CreateWorkspaceDialog open={creating} onOpenChange={setCreating} />
    </>
  );
}

export function useLogout() {
  return React.useCallback(async (all = false) => {
    try {
      await authPost(`/api/auth/logout${all ? "?all=1" : ""}`);
    } catch {
      /* vẫn rời trang */
    }
    window.location.href = "/login";
  }, []);
}

/** Top bar phải: người đang đăng nhập, vai trò, Cài đặt, Đăng xuất (thiết bị này / mọi nơi). */
export function UserMenu() {
  const t = useTranslations("workspace");
  const tn = useTranslations("nav");
  const roleLabel = useRoleLabel();
  const { me, session } = useSession();
  const logout = useLogout();
  if (!session) return null;
  const label = me?.user?.name || me?.user?.email || (session.mode === "key" ? session.key_prefix ?? "API key" : session.mock ? "MOCK" : "?");
  return (
    <Menu
      align="end"
      label={t("account")}
      trigger={({ toggle, open }) => (
        <button type="button" onClick={toggle} aria-expanded={open} aria-label={t("account")} className="grid size-9 shrink-0 place-items-center rounded-full border border-border bg-surface-2 text-[13px] font-semibold uppercase hover:bg-surface">
          {me?.user ? (me.user.name || me.user.email).slice(0, 1) : <UserRound className="size-4" />}
        </button>
      )}
    >
      <div className="px-2.5 py-2">
        <p className="truncate text-[13px] font-medium">{label}</p>
        {me?.user && me.user.name && <p className="truncate text-[12px] text-muted-foreground">{me.user.email}</p>}
        <div className="mt-1.5 flex flex-wrap gap-1">
          {me?.workspace && <Badge variant="outline">{roleLabel(me.role)}</Badge>}
          {me?.platform_admin && <Badge variant="blue">{t("platformAdmin")}</Badge>}
          {session.mode === "key" && <Badge variant="yellow">{t("keySession")}</Badge>}
        </div>
      </div>
      <MenuSeparator />
      <MenuItem onSelect={() => (window.location.href = "/settings")}><Settings /> {tn("settings")}</MenuItem>
      <MenuItem onSelect={() => logout(false)}><LogOut /> {tn("logout")}</MenuItem>
      {session.mode === "session" && <MenuItem onSelect={() => logout(true)}><LogOut /> {t("logoutAll")}</MenuItem>}
    </Menu>
  );
}

/** Banner toàn cục: workspace bị khoá (đỏ) > chưa thuộc workspace nào > email chưa xác thực. */
export function WorkspaceBanners() {
  const t = useTranslations("workspace");
  const { me, session } = useSession();
  const [creating, setCreating] = React.useState(false);
  const [sent, setSent] = React.useState(false);
  if (!session || session.mock || !me) return null;
  if (me.workspace?.status === "suspended") {
    return (
      <div className="flex items-center gap-2 border-b border-st-error/30 bg-st-error/10 px-4 py-2 text-[13px] text-st-error">
        <Lock className="size-4 shrink-0" /> <span>{t("suspendedBanner", { name: me.workspace.name })}</span>
      </div>
    );
  }
  if (me.user && !me.workspace) {
    return (
      <div className="flex flex-wrap items-center gap-2 border-b border-st-warning/30 bg-st-warning/10 px-4 py-2 text-[13px] text-st-warning">
        <Building2 className="size-4 shrink-0" /> <span className="min-w-0 flex-1">{t("noWorkspaceBanner")}</span>
        <Button size="xs" variant="outline" onClick={() => setCreating(true)}><Plus /> {t("create")}</Button>
        <CreateWorkspaceDialog open={creating} onOpenChange={setCreating} />
      </div>
    );
  }
  if (me.user && !me.user.email_verified) {
    return (
      <div className="flex flex-wrap items-center gap-2 border-b border-border bg-surface px-4 py-2 text-[13px] text-muted-foreground">
        <MailWarning className="size-4 shrink-0 text-st-warning" /> <span className="min-w-0 flex-1">{t("verifyBanner")}</span>
        <Button
          size="xs"
          variant="ghost"
          disabled={sent}
          onClick={async () => {
            try {
              await http.post("/auth/verify-email/resend");
              setSent(true);
              toast.success(t("verifySent"));
            } catch (e) {
              toastError(e);
            }
          }}
        >
          {sent ? t("verifySent") : t("verifyResend")}
        </Button>
      </div>
    );
  }
  return null;
}

/** Cuối sidebar: "Pro · 3/10 box" → Cài đặt › Gói. */
export function PlanCard() {
  const t = useTranslations("workspace");
  const planLabel = usePlanLabel();
  const { me, session } = useSession();
  const { data } = useApi<WorkspaceUsage>(session && (me?.workspace || session.mock) ? paths.workspaceUsage : null, { refreshInterval: 60_000 });
  if (!data) return null;
  const max = data.quota.max_boxes;
  const p = max ? (data.used.boxes / max) * 100 : 0;
  return (
    <Link href="/settings/billing" className="hidden rounded-md border border-border bg-background px-2.5 py-2 text-[12.5px] hover:bg-surface-2 lg:block">
      <span className="flex items-center justify-between gap-2">
        <span className="font-medium">{planLabel(data.plan)}</span>
        <span className={cn("font-mono", p >= 100 ? "text-st-error" : p >= 80 ? "text-st-warning" : "text-muted-foreground")}>
          {max ? t("boxesOf", { used: data.used.boxes, max }) : t("boxesUnlimited", { used: data.used.boxes })}
        </span>
      </span>
      {max > 0 && (
        <span className="mt-1.5 block h-1 overflow-hidden rounded-full bg-surface-2">
          <span className={cn("block h-full rounded-full", p >= 100 ? "bg-st-error" : p >= 80 ? "bg-st-warning" : "bg-primary")} style={{ width: `${Math.min(100, p)}%` }} />
        </span>
      )}
    </Link>
  );
}

/** Trang/khối khi thiếu quyền (spec §18): "Anh cần vai trò …". */
export function Forbidden({ need, platform }: { need?: string; platform?: boolean }) {
  const t = useTranslations("workspace");
  const roleLabel = useRoleLabel();
  const { me } = useSession();
  return (
    <div className="flex flex-col items-center justify-center gap-2 rounded-lg border border-dashed border-border px-6 py-14 text-center">
      <ShieldAlert className="size-7 text-st-warning" />
      <p className="text-sm font-medium">{platform ? t("forbiddenPlatform") : t("forbiddenRole", { role: roleLabel(need ?? "admin") })}</p>
      <p className="max-w-sm text-[13px] text-muted-foreground">{me?.workspace ? t("forbiddenYour", { role: roleLabel(me.role), name: me.workspace.name }) : t("forbiddenAsk")}</p>
    </div>
  );
}

/** Bọc nội dung trang: chưa đủ quyền → Forbidden; đang tải phiên → không hiện gì. */
export function RequirePerm({ perm, platform, need, children }: { perm?: Perm; platform?: boolean; need?: string; children: React.ReactNode }) {
  const { session, can, platformAdmin } = useSession();
  if (!session) return null;
  if (platform && !platformAdmin) return <Forbidden platform />;
  if (perm && !can(perm)) return <Forbidden need={need} />;
  return <>{children}</>;
}
