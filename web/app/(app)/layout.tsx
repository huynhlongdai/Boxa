import { Providers } from "@/components/providers";
import { Shell } from "@/components/shell";
import { requireSession } from "@/lib/server/guard";

export const dynamic = "force-dynamic";

export default async function AppLayout({ children }: { children: React.ReactNode }) {
  await requireSession();
  return (
    <Providers>
      <Shell>{children}</Shell>
    </Providers>
  );
}
