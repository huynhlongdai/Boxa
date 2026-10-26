"use client";
import { Camera, Moon, Sun, Trash2 } from "lucide-react";
import { useTranslations } from "next-intl";
import { api } from "@/lib/api";
import { startsWith, useAction } from "@/lib/hooks";
import type { Box } from "@/lib/types";
import { Button } from "@/components/ui/button";
import { ConfirmButton } from "./confirm";
import { cn } from "@/lib/utils";

/** Nút nhanh: wake / sleep / snapshot / xoá. */
export function BoxActions({ box, labels, className, onDeleted }: { box: Box; labels?: boolean; className?: string; onDeleted?: () => void }) {
  const { run, pending } = useAction();
  const t = useTranslations("boxActions");
  const reval = [startsWith("/boxes"), "/overview"];
  const canWake = ["sleeping", "failed"].includes(box.status);
  const canSleep = box.status === "running";
  const size = labels ? "sm" : "icon-sm";
  return (
    <div className={cn("flex items-center gap-1", className)}>
      {canWake && (
        <Button size={size} variant="ghost" title={t("wake")} aria-label={t("wake")} disabled={!!pending}
          onClick={() => run("wake", () => api.wakeBox(box.id), { success: t("waking", { slug: box.slug }), revalidate: reval })}>
          <Sun className="text-st-warning" />{labels && t("wake")}
        </Button>
      )}
      {canSleep && (
        <Button size={size} variant="ghost" title={t("sleep")} aria-label={t("sleep")} disabled={!!pending}
          onClick={() => run("sleep", () => api.sleepBox(box.id), { success: t("slept", { slug: box.slug }), revalidate: reval })}>
          <Moon />{labels && t("sleep")}
        </Button>
      )}
      <Button size={size} variant="ghost" title={t("snapshot")} aria-label={t("snapshot")} disabled={!!pending || !["running", "sleeping"].includes(box.status)}
        onClick={() => run("snap", () => api.snapshotBox(box.id), { success: (s) => t("snapshotDone", { id: s.id }), revalidate: [`/boxes/${box.id}/snapshots`] })}>
        <Camera />{labels && t("snapshot")}
      </Button>
      <ConfirmButton
        size={size}
        variant="ghost"
        title={t("deleteTitle", { slug: box.slug })}
        desc={t("deleteDesc")}
        confirmLabel={t("deleteConfirm")}
        destructive
        typeToConfirm={box.slug}
        aria-label={t("delete")}
        onConfirm={async () => {
          const r = await run("del", () => api.deleteBox(box.id), { success: t("deletedToast", { slug: box.slug }), revalidate: reval });
          if (r) onDeleted?.();
        }}
      >
        <Trash2 className="text-st-error" />{labels && t("delete")}
      </ConfirmButton>
    </div>
  );
}
