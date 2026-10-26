"use client";
import * as React from "react";
import Link from "next/link";
import { usePathname, useRouter } from "next/navigation";
import * as DialogPrimitive from "@radix-ui/react-dialog";
import { Box as BoxIcon, Command as CmdIcon, LogOut, MoreHorizontal, Rocket, Search, Wrench } from "lucide-react";
import { NAV, isActive } from "./nav";
import { usePalette } from "./command-palette";
import { useLive } from "./live";
import { Dot } from "./boxa/status";
import { SheetContent } from "./ui/dialog";
import { useApi } from "@/lib/hooks";
import { paths } from "@/lib/api";
import type { Overview, Settings } from "@/lib/types";
import { cn, usd } from "@/lib/utils";

function Logo({ compact }: { compact?: boolean }) {
  return (
    <Link href="/" className="flex items-center gap-2 font-semibold tracking-tight">
      <span className="grid size-7 place-items-center rounded-md bg-primary/15 text-primary">
        <BoxIcon className="size-4" />
      </span>
      {!compact && <span className="text-[15px]">Boxa</span>}
    </Link>
  );
}

function LiveIndicator({ compact }: { compact?: boolean }) {
  const { connected, session } = useLive();
  return (
    <div className="flex items-center gap-2 text-[11.5px] text-muted-foreground" title={connected ? "Đang nhận cập nhật trực tiếp (SSE)" : "Mất SSE — đang làm mới mỗi 10 giây"}>
      <Dot tone={connected ? "green" : "yellow"} pulse={connected} />
      {!compact && <span>{connected ? "Trực tiếp" : "Polling 10s"}</span>}
      {session?.mock && <span className="rounded border border-st-warning/40 px-1 font-mono text-[10px] text-st-warning">MOCK</span>}
    </div>
  );
}

function MaintenanceBanner() {
  const { data } = useApi<Settings>(paths.settings);
  if (!data?.maintenance.enabled) return null;
  return (
    <div className="flex items-center gap-2 border-b border-st-warning/30 bg-st-warning/10 px-4 py-2 text-[13px] text-st-warning">
      <Wrench className="size-4 shrink-0" />
      <span className="font-medium">Đang bảo trì:</span>
      <span className="text-st-warning/90">{data.maintenance.message}</span>
    </div>
  );
}

function SpendPill() {
  const { data } = useApi<Overview>(paths.overview);
  if (!data) return null;
  const p = data.budget_day ? (data.spent_today / data.budget_day) * 100 : 0;
  return (
    <Link href="/costs" className="hidden items-center gap-1.5 rounded-md border border-border px-2 py-1 font-mono text-[12px] sm:flex">
      <span className={cn(p >= 100 ? "text-st-error" : p >= 80 ? "text-st-warning" : "text-foreground")}>{usd(data.spent_today)}</span>
      <span className="text-muted-foreground">/ {usd(data.budget_day)} hôm nay</span>
    </Link>
  );
}

export function Shell({ children }: { children: React.ReactNode }) {
  const pathname = usePathname();
  const router = useRouter();
  const palette = usePalette();
  const [moreOpen, setMoreOpen] = React.useState(false);
  const { data: ov } = useApi<Overview>(paths.overview);
  const pending = ov?.approvals_pending ?? 0;
  const moreItems = NAV.filter((n) => !n.mobileTab);
  const moreActive = moreItems.some((n) => isActive(pathname, n.href));

  const logout = async () => {
    await fetch("/api/auth/logout", { method: "POST" });
    router.push("/login");
  };

  return (
    <div className="flex min-h-dvh">
      {/* Sidebar: rail icon ở 768–1023px, đầy đủ từ 1024px */}
      <aside className="sticky top-0 hidden h-dvh w-16 shrink-0 flex-col border-r border-border bg-surface md:flex lg:w-60">
        <div className="flex h-14 items-center justify-center px-4 lg:justify-start">
          <span className="lg:hidden"><Logo compact /></span>
          <span className="hidden lg:block"><Logo /></span>
        </div>
        <div className="px-2 lg:px-3">
          <button
            onClick={palette.open}
            className="flex h-9 w-full items-center justify-center gap-2 rounded-md border border-border bg-background px-2.5 text-[13px] text-muted-foreground hover:text-foreground lg:justify-start"
            aria-label="Mở bảng lệnh"
          >
            <Search className="size-4" />
            <span className="hidden lg:inline">Tìm hoặc gõ lệnh…</span>
            <kbd className="ml-auto hidden rounded border border-border px-1 font-mono text-[10px] lg:inline">⌘K</kbd>
          </button>
        </div>
        <nav className="mt-3 flex flex-1 flex-col gap-0.5 overflow-y-auto px-2 lg:px-3">
          {NAV.map((n) => {
            const active = isActive(pathname, n.href);
            return (
              <Link
                key={n.href}
                href={n.href}
                title={n.label}
                className={cn(
                  "relative flex h-9 items-center justify-center gap-2.5 rounded-md px-2.5 text-sm transition-colors lg:justify-start",
                  active ? "bg-surface-2 text-foreground" : "text-muted-foreground hover:bg-surface-2/60 hover:text-foreground",
                )}
              >
                <n.icon className="size-[18px] shrink-0" />
                <span className="hidden lg:inline">{n.label}</span>
                {n.href === "/" && pending > 0 && (
                  <span className="absolute right-1 top-1 grid min-w-4 place-items-center rounded-full bg-st-warning px-1 text-[10px] font-semibold text-black lg:static lg:ml-auto">
                    {pending}
                  </span>
                )}
              </Link>
            );
          })}
        </nav>
        <div className="flex flex-col gap-2 border-t border-border p-3">
          <Link href="/onboarding" className="hidden items-center gap-2 text-[13px] text-muted-foreground hover:text-foreground lg:flex">
            <Rocket className="size-4" /> Khởi động lần đầu
          </Link>
          <div className="flex items-center justify-between">
            <LiveIndicator />
            <button onClick={logout} className="hidden text-muted-foreground hover:text-foreground lg:block" title="Đăng xuất" aria-label="Đăng xuất">
              <LogOut className="size-4" />
            </button>
          </div>
        </div>
      </aside>

      <div className="flex min-w-0 flex-1 flex-col">
        <MaintenanceBanner />
        {/* Top bar */}
        <header className="sticky top-0 z-30 flex h-14 items-center gap-3 border-b border-border bg-background/85 px-4 backdrop-blur md:px-6">
          <span className="md:hidden"><Logo /></span>
          <div className="ml-auto flex items-center gap-2">
            <SpendPill />
            <span className="md:hidden"><LiveIndicator compact /></span>
            <button
              onClick={palette.open}
              className="grid size-9 place-items-center rounded-md border border-border text-muted-foreground hover:text-foreground md:hidden"
              aria-label="Mở bảng lệnh"
            >
              <CmdIcon className="size-4" />
            </button>
          </div>
        </header>
        <main className="mx-auto w-full max-w-[1400px] flex-1 px-4 pb-28 pt-5 md:px-6 md:pb-10">{children}</main>
      </div>

      {/* Mobile bottom tab bar */}
      <nav className="fixed inset-x-0 bottom-0 z-40 grid grid-cols-5 border-t border-border bg-surface/95 pb-[env(safe-area-inset-bottom)] backdrop-blur md:hidden">
        {NAV.filter((n) => n.mobileTab).map((n) => {
          const active = isActive(pathname, n.href);
          return (
            <Link key={n.href} href={n.href} className={cn("relative flex h-14 flex-col items-center justify-center gap-0.5 text-[10.5px]", active ? "text-primary" : "text-muted-foreground")}>
              <n.icon className="size-5" />
              {n.label}
              {n.href === "/" && pending > 0 && (
                <span className="absolute right-[22%] top-1.5 grid min-w-4 place-items-center rounded-full bg-st-warning px-1 text-[10px] font-semibold text-black">{pending}</span>
              )}
            </Link>
          );
        })}
        <DialogPrimitive.Root open={moreOpen} onOpenChange={setMoreOpen}>
          <DialogPrimitive.Trigger className={cn("flex h-14 flex-col items-center justify-center gap-0.5 text-[10.5px]", moreActive ? "text-primary" : "text-muted-foreground")}>
            <MoreHorizontal className="size-5" />
            Thêm
          </DialogPrimitive.Trigger>
          <SheetContent side="bottom">
            <DialogPrimitive.Title className="mb-3 text-sm font-semibold">Thêm</DialogPrimitive.Title>
            <div className="grid grid-cols-3 gap-2">
              {[...moreItems, { href: "/onboarding", label: "Lần đầu", icon: Rocket }].map((n) => (
                <Link
                  key={n.href}
                  href={n.href}
                  onClick={() => setMoreOpen(false)}
                  className={cn(
                    "flex flex-col items-center gap-1.5 rounded-lg border border-border p-3 text-[12.5px]",
                    isActive(pathname, n.href) ? "border-primary/50 bg-primary/10 text-primary" : "bg-background",
                  )}
                >
                  <n.icon className="size-5" />
                  {n.label}
                </Link>
              ))}
              <button onClick={() => { setMoreOpen(false); palette.open(); }} className="flex flex-col items-center gap-1.5 rounded-lg border border-border bg-background p-3 text-[12.5px]">
                <CmdIcon className="size-5" /> Bảng lệnh
              </button>
              <button onClick={logout} className="flex flex-col items-center gap-1.5 rounded-lg border border-border bg-background p-3 text-[12.5px] text-muted-foreground">
                <LogOut className="size-5" /> Đăng xuất
              </button>
            </div>
          </SheetContent>
        </DialogPrimitive.Root>
      </nav>
    </div>
  );
}
