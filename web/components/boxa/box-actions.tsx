"use client";
import { Camera, Moon, Sun, Trash2 } from "lucide-react";
import { api } from "@/lib/api";
import { startsWith, useAction } from "@/lib/hooks";
import type { Box } from "@/lib/types";
import { Button } from "@/components/ui/button";
import { ConfirmButton } from "./confirm";
import { cn } from "@/lib/utils";

/** Nút nhanh: wake / sleep / snapshot / xoá. */
export function BoxActions({ box, labels, className, onDeleted }: { box: Box; labels?: boolean; className?: string; onDeleted?: () => void }) {
  const { run, pending } = useAction();
  const reval = [startsWith("/boxes"), "/overview"];
  const canWake = ["sleeping", "failed"].includes(box.status);
  const canSleep = box.status === "running";
  const size = labels ? "sm" : "icon-sm";
  return (
    <div className={cn("flex items-center gap-1", className)}>
      {canWake && (
        <Button size={size} variant="ghost" title="Đánh thức" aria-label="Đánh thức" disabled={!!pending}
          onClick={() => run("wake", () => api.wakeBox(box.id), { success: `Đang đánh thức ${box.slug}`, revalidate: reval })}>
          <Sun className="text-st-warning" />{labels && "Đánh thức"}
        </Button>
      )}
      {canSleep && (
        <Button size={size} variant="ghost" title="Cho ngủ" aria-label="Cho ngủ" disabled={!!pending}
          onClick={() => run("sleep", () => api.sleepBox(box.id), { success: `Đã cho ${box.slug} ngủ`, revalidate: reval })}>
          <Moon />{labels && "Cho ngủ"}
        </Button>
      )}
      <Button size={size} variant="ghost" title="Snapshot" aria-label="Snapshot" disabled={!!pending || !["running", "sleeping"].includes(box.status)}
        onClick={() => run("snap", () => api.snapshotBox(box.id), { success: (s) => `Đã tạo snapshot ${s.id}`, revalidate: [`/boxes/${box.id}/snapshots`] })}>
        <Camera />{labels && "Snapshot"}
      </Button>
      <ConfirmButton
        size={size}
        variant="ghost"
        title={`Xoá box ${box.slug}?`}
        desc="Box và đĩa sẽ bị xoá vĩnh viễn. Snapshot đã lưu vẫn giữ theo chính sách Reaper."
        confirmLabel="Xoá box"
        destructive
        typeToConfirm={box.slug}
        aria-label="Xoá"
        onConfirm={async () => {
          const r = await run("del", () => api.deleteBox(box.id), { success: `Đã xoá ${box.slug}`, revalidate: reval });
          if (r) onDeleted?.();
        }}
      >
        <Trash2 className="text-st-error" />{labels && "Xoá"}
      </ConfirmButton>
    </div>
  );
}
