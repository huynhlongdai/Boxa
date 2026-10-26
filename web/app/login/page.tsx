import { Suspense } from "react";
import type { Metadata } from "next";
import { getTranslations } from "next-intl/server";
import { LocaleSelect } from "@/components/locale-switcher";
import { LoginForm } from "./login-form";

export async function generateMetadata(): Promise<Metadata> {
  const t = await getTranslations("meta");
  return { title: t("login") };
}

export default function LoginPage() {
  return (
    <div className="grid min-h-dvh place-items-center px-4">
      <div className="absolute right-4 top-4"><LocaleSelect /></div>
      <div className="w-full min-w-0 max-w-sm">
        <Suspense>
          <LoginForm />
        </Suspense>
      </div>
    </div>
  );
}
