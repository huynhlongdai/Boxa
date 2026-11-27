"use client";
import * as React from "react";
import { Skeleton } from "@/components/ui/skeleton";
import { SettingsTabs } from "../settings-tabs";

/** /settings/billing = Cài đặt › Gói & hạn mức (đích của upgrade_url trong lỗi QUOTA_EXCEEDED và mục "Thanh toán" ở sidebar). */
export default function BillingPage() {
  return (
    <React.Suspense fallback={<Skeleton className="h-96" />}>
      <SettingsTabs fixedTab="plan" />
    </React.Suspense>
  );
}
