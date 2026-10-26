import { Providers } from "@/components/providers";
import { requireSession } from "@/lib/server/guard";

export const dynamic = "force-dynamic";
export const metadata = { title: "Khởi động lần đầu" };

export default async function OnboardingLayout({ children }: { children: React.ReactNode }) {
  await requireSession();
  return <Providers>{children}</Providers>;
}
