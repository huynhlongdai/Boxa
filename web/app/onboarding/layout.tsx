import type { Metadata } from "next";
import { getTranslations } from "next-intl/server";
import { Providers } from "@/components/providers";
import { requireSession } from "@/lib/server/guard";

export const dynamic = "force-dynamic";

export async function generateMetadata(): Promise<Metadata> {
  const t = await getTranslations("meta");
  return { title: t("onboarding") };
}

export default async function OnboardingLayout({ children }: { children: React.ReactNode }) {
  await requireSession();
  return <Providers>{children}</Providers>;
}
