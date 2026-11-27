import { Suspense } from "react";
import type { Metadata } from "next";
import { getTranslations } from "next-intl/server";
import { AuthPage } from "@/components/auth-ui";
import { SignupForm } from "./signup-form";

export async function generateMetadata(): Promise<Metadata> {
  const t = await getTranslations("meta");
  return { title: t("signup") };
}

export default function SignupPage() {
  return (
    <AuthPage wide>
      <Suspense>
        <SignupForm />
      </Suspense>
    </AuthPage>
  );
}
