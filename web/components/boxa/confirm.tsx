"use client";
import * as React from "react";
import { Button, type ButtonProps } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";

/** Nút có hộp xác nhận. typeToConfirm: bắt gõ đúng chuỗi mới cho bấm. */
export function ConfirmButton({
  title,
  desc,
  confirmLabel = "Xác nhận",
  onConfirm,
  destructive,
  typeToConfirm,
  children,
  ...btn
}: ButtonProps & {
  title: string;
  desc?: React.ReactNode;
  confirmLabel?: string;
  onConfirm: () => Promise<unknown> | void;
  destructive?: boolean;
  typeToConfirm?: string;
}) {
  const [open, setOpen] = React.useState(false);
  const [busy, setBusy] = React.useState(false);
  const [typed, setTyped] = React.useState("");
  return (
    <Dialog open={open} onOpenChange={(o) => { setOpen(o); if (!o) setTyped(""); }}>
      <DialogTrigger asChild>
        <Button {...btn}>{children}</Button>
      </DialogTrigger>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          {desc && <DialogDescription asChild><div>{desc}</div></DialogDescription>}
        </DialogHeader>
        {typeToConfirm && (
          <div className="flex flex-col gap-1.5">
            <p className="text-[13px] text-muted-foreground">
              Gõ <span className="font-mono text-foreground">{typeToConfirm}</span> để xác nhận
            </p>
            <Input value={typed} onChange={(e) => setTyped(e.target.value)} className="font-mono" autoFocus />
          </div>
        )}
        <DialogFooter>
          <Button variant="ghost" onClick={() => setOpen(false)}>Huỷ</Button>
          <Button
            variant={destructive ? "destructive" : "default"}
            disabled={busy || (typeToConfirm ? typed.trim() !== typeToConfirm : false)}
            onClick={async () => {
              setBusy(true);
              try {
                await onConfirm();
                setOpen(false);
                setTyped("");
              } finally {
                setBusy(false);
              }
            }}
          >
            {busy ? "Đang xử lý…" : confirmLabel}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
