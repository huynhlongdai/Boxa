import { Suspense } from "react";
import type { Metadata } from "next";
import { getTranslations } from "next-intl/server";
import { AuthPage } from "@/components/auth-ui";
import { ForgotPasswordForm } from "@/components/auth-flows";

export async function generateMetadata(): Promise<Metadata> {
  const t = await getTranslations("meta");
  return { title: t("forgotPassword") };
}

export default function Page() {
  return (
    <AuthPage>
      <Suspense>
        <ForgotPasswordForm />
      </Suspense>
    </AuthPage>
  );
}
