"use client";
import { useTranslations } from "next-intl";
import { PageHeader } from "@/components/boxmes/page";
import { RecommendPanel } from "@/components/boxmes/recommend";

export default function RecommendPage() {
  const t = useTranslations("rec");
  return (
    <>
      <PageHeader title={t("title")} desc={t("desc")} />
      <RecommendPanel />
    </>
  );
}
