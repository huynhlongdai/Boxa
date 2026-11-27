"use client";
// Dừng khẩn cấp: xác nhận 2 bước (bước 1 cảnh báo, bước 2 gõ DUNG) → POST /emergency-stop {confirm:"DUNG"}
import * as React from "react";
import { OctagonX } from "lucide-react";
import { useTranslations } from "next-intl";
import { toast } from "sonner";
import { useSWRConfig } from "swr";
import { api } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/utils";
import { toastError } from "@/lib/error-toast";

const Ctx = React.createContext<{ open: () => void }>({ open: () => {} });
export const useEmergency = () => React.useContext(Ctx);

export function EmergencyProvider({ children }: { children: React.ReactNode }) {
  const [open, setOpen] = React.useState(false);
  const [step, setStep] = React.useState<1 | 2>(1);
  const [typed, setTyped] = React.useState("");
  const [busy, setBusy] = React.useState(false);
  const { mutate } = useSWRConfig();
  const t = useTranslations("emergency");
  const tc = useTranslations("common");
  const close = () => {
    setOpen(false);
    setTimeout(() => { setStep(1); setTyped(""); }, 200);
  };
  const doStop = async () => {
    setBusy(true);
    try {
      const r = await api.emergencyStop();
      toast.success(t("done", { n: r.slept }));
      await mutate(() => true);
      close();
    } catch (e) {
      toastError(e);
    } finally {
      setBusy(false);
    }
  };
  return (
    <Ctx.Provider value={{ open: () => setOpen(true) }}>
      {children}
      <Dialog open={open} onOpenChange={(o) => (o ? setOpen(true) : close())}>
        <DialogContent className="max-w-md border-st-error/40">
          <DialogHeader>
            <DialogTitle className="flex items-center gap-2 text-st-error">
              <OctagonX className="size-5" /> {t("title")}
            </DialogTitle>
            <DialogDescription>
              {step === 1 ? t("step1") : t("step2", { word: "DUNG" })}
            </DialogDescription>
          </DialogHeader>
          {step === 2 && (
            <Input
              autoFocus
              value={typed}
              onChange={(e) => setTyped(e.target.value)}
              placeholder="DUNG"
              className="font-mono text-base tracking-widest"
              onKeyDown={(e) => { if (e.key === "Enter" && typed.trim() === "DUNG") doStop(); }}
            />
          )}
          <DialogFooter>
            <Button variant="ghost" onClick={close}>{tc("cancel")}</Button>
            {step === 1 ? (
              <Button variant="destructive" onClick={() => setStep(2)}>{tc("continue")}</Button>
            ) : (
              <Button variant="destructive" disabled={typed.trim() !== "DUNG" || busy} onClick={doStop}>
                {busy ? t("stopping") : t("sleepAll")}
              </Button>
            )}
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </Ctx.Provider>
  );
}

export function EmergencyButton({ className, compact }: { className?: string; compact?: boolean }) {
  const { open } = useEmergency();
  const t = useTranslations("emergency");
  return (
    <Button variant="destructive" className={cn("font-semibold", className)} onClick={open} size={compact ? "sm" : "default"}>
      <OctagonX /> {t("title")}
    </Button>
  );
}
