"use client";
// v1.4: màn hình "Adapter YAML tuỳ chỉnh" — dán adapter khai báo, kiểm tra qua POST /v1/sandbox/adapters/lint.
import Link from "next/link";
import { ArrowLeft } from "lucide-react";
import { useTranslations } from "next-intl";
import { PageHeader } from "@/components/boxaz/page";
import { AdapterLinter } from "@/components/boxaz/sandbox";
import { SAMPLE_ADAPTER } from "@/lib/adapter-sample";

export default function AdaptersPage() {
  const t = useTranslations("sbx");
  return (
    <>
      <Link href="/infra?s=sandbox" className="mb-3 inline-flex items-center gap-1 text-[13px] text-muted-foreground hover:text-foreground">
        <ArrowLeft className="size-4" /> {t("adapter.back")}
      </Link>
      <PageHeader title={t("adapter.title")} desc={t("adapter.desc")} />
      <AdapterLinter sample={SAMPLE_ADAPTER} />
    </>
  );
}
