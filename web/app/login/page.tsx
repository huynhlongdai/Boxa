import { Suspense } from "react";
import type { Metadata } from "next";
import { getTranslations } from "next-intl/server";
import { AuthPage } from "@/components/auth-ui";
import { LoginForm } from "./login-form";

export async function generateMetadata(): Promise<Metadata> {
  const t = await getTranslations("meta");
  return { title: t("login") };
}

export default function LoginPage() {
  return (
    <AuthPage>
      <Suspense>
        <LoginForm />
      </Suspense>
    </AuthPage>
  );
}
