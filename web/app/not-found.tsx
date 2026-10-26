import Link from "next/link";
import { getTranslations } from "next-intl/server";

export default async function NotFound() {
  const t = await getTranslations("notFound");
  return (
    <div className="grid min-h-dvh place-items-center px-4 text-center">
      <div>
        <p className="font-mono text-sm text-muted-foreground">404</p>
        <h1 className="mt-2 text-xl font-semibold">{t("title")}</h1>
        <Link href="/" className="mt-4 inline-block text-sm text-primary hover:underline">{t("home")}</Link>
      </div>
    </div>
  );
}
