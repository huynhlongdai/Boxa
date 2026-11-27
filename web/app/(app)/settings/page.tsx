"use client";
import * as React from "react";
import { Skeleton } from "@/components/ui/skeleton";
import { SettingsTabs } from "./settings-tabs";

export default function SettingsPage() {
  return (
    <React.Suspense fallback={<Skeleton className="h-96" />}>
      <SettingsTabs />
    </React.Suspense>
  );
}
