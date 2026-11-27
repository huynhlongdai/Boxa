"use client";
// Menu thả xuống gọn nhẹ (không thêm dependency): nút + bảng nổi, đóng khi bấm ra ngoài / Esc / chọn mục.
import * as React from "react";
import { cn } from "@/lib/utils";

interface MenuCtx {
  close: () => void;
}
const Ctx = React.createContext<MenuCtx>({ close: () => {} });

export function Menu({
  trigger,
  children,
  align = "start",
  className,
  label,
}: {
  trigger: (p: { open: boolean; toggle: () => void }) => React.ReactNode;
  children: React.ReactNode;
  align?: "start" | "end";
  className?: string;
  label?: string;
}) {
  const [open, setOpen] = React.useState(false);
  const ref = React.useRef<HTMLDivElement>(null);
  React.useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent | TouchEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false);
    };
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && setOpen(false);
    document.addEventListener("mousedown", onDown);
    document.addEventListener("touchstart", onDown);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", onDown);
      document.removeEventListener("touchstart", onDown);
      document.removeEventListener("keydown", onKey);
    };
  }, [open]);
  const close = React.useCallback(() => setOpen(false), []);
  return (
    <div ref={ref} className="relative min-w-0">
      {trigger({ open, toggle: () => setOpen((o) => !o) })}
      {open && (
        <div
          role="menu"
          aria-label={label}
          className={cn(
            "absolute top-full z-50 mt-1.5 max-h-[70vh] w-72 max-w-[calc(100vw-2rem)] overflow-y-auto rounded-lg border border-border bg-surface p-1 shadow-xl",
            align === "end" ? "right-0" : "left-0",
            className,
          )}
        >
          <Ctx.Provider value={{ close }}>{children}</Ctx.Provider>
        </div>
      )}
    </div>
  );
}

export function MenuItem({ className, onSelect, children, disabled, active }: { className?: string; onSelect?: () => void; children: React.ReactNode; disabled?: boolean; active?: boolean }) {
  const { close } = React.useContext(Ctx);
  return (
    <button
      type="button"
      role="menuitem"
      disabled={disabled}
      onClick={() => {
        close();
        onSelect?.();
      }}
      className={cn(
        "flex w-full min-w-0 items-center gap-2 rounded-md px-2.5 py-2 text-left text-[13px] transition-colors hover:bg-surface-2 disabled:cursor-not-allowed disabled:opacity-50 [&_svg]:size-4 [&_svg]:shrink-0",
        active && "bg-surface-2",
        className,
      )}
    >
      {children}
    </button>
  );
}

export function MenuLabel({ children, className }: { children: React.ReactNode; className?: string }) {
  return <div className={cn("px-2.5 pb-1 pt-2 text-[11px] font-medium uppercase tracking-wide text-muted-foreground", className)}>{children}</div>;
}

export function MenuSeparator() {
  return <div className="my-1 h-px bg-border" />;
}
