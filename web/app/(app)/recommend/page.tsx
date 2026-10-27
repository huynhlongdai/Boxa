"use client";
import { useTranslations } from "next-intl";
import { PageHeader } from "@/components/boxaz/page";
import { RecommendPanel } from "@/components/boxaz/recommend";

export default function RecommendPage() {
  const t = useTranslations("rec");
  return (
    <>
      <PageHeader title={t("title")} desc={t("desc")} />
      <RecommendPanel />
    </>
  );
}
