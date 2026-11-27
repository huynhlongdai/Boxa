"use client";
import { useTranslations } from "next-intl";
import { ApprovalList } from "@/components/boxmes/approvals";
import { PageHeader } from "@/components/boxmes/page";

/** "Cần anh" (spec §8): việc chờ người duyệt — ngân sách, dừng service, mở rộng pool, host chết. */
export default function ApprovalsPage() {
  const t = useTranslations("approvals");
  return (
    <>
      <PageHeader title={t("pageTitle")} desc={t("pageDesc")} />
      <ApprovalList />
    </>
  );
}
