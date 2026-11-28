"use client";
// v2.2 (M29 + M29-S): chi tiết provider /infra/providers/[id] — tài khoản, chiến lược, cảnh báo; provider sandbox thêm tab Sandbox
// (slot concurrency theo khoá, limit_group/billing_group, credit, ma trận template + đồng bộ). Chỉ admin nền tảng (layout infra).
import * as React from "react";
import Link from "next/link";
import { useParams, useRouter, useSearchParams } from "next/navigation";
import { ArrowLeft } from "lucide-react";
import { useTranslations } from "next-intl";
import { paths } from "@/lib/api";
import { useApi } from "@/lib/hooks";
import type { AdminProviderDetail } from "@/lib/types";
import { Badge } from "@/components/ui/badge";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { ErrorBox, LoadingRows, PageHeader } from "@/components/boxmes/page";
import { ProviderLogo } from "@/components/boxmes/sandbox";
import { InfraAlertsList, ProviderAccountsPanel } from "@/components/boxmes/provider-accounts";
import { SandboxAccountsPanel } from "@/components/boxmes/sandbox-accounts";

function Inner() {
  const t = useTranslations("pa");
  const { id } = useParams<{ id: string }>();
  const provider = decodeURIComponent(id);
  const sp = useSearchParams();
  const router = useRouter();
  const { data, error, mutate } = useApi<AdminProviderDetail>(paths.adminProvider(provider), { refreshInterval: 20000 });
  const sandbox = data?.group === "sandbox";
  const tabs = sandbox ? ["accounts", "sandbox", "alerts"] : ["accounts", "alerts"];
  const raw = sp.get("tab") ?? "accounts";
  const tab = tabs.includes(raw) ? raw : "accounts";
  return (
    <>
      <Link href="/infra" className="mb-3 inline-flex items-center gap-1 text-[13px] text-muted-foreground hover:text-foreground">
        <ArrowLeft className="size-4" /> {t("back")}
      </Link>
      {error ? <ErrorBox error={error} retry={() => mutate()} /> : !data ? <LoadingRows rows={4} /> : (
        <>
          <PageHeader
            title={<span className="inline-flex items-center gap-2.5"><ProviderLogo id={data.id} />{data.name}</span>}
            desc={t("detailDesc")}
            actions={<><Badge variant="outline" className="font-mono">{data.tier}</Badge><Badge variant="outline">{sandbox ? t("groupSandbox") : t("groupVps")}</Badge></>}
          />
          <Tabs value={tab} onValueChange={(v) => router.replace(v === "accounts" ? `/infra/providers/${encodeURIComponent(provider)}` : `/infra/providers/${encodeURIComponent(provider)}?tab=${v}`, { scroll: false })}>
            <TabsList>
              {tabs.map((v) => <TabsTrigger key={v} value={v}>{t(`detailTab.${v}`)}{v === "alerts" && data.open_alerts ? <Badge variant="yellow" className="ml-1">{data.open_alerts}</Badge> : null}</TabsTrigger>)}
            </TabsList>
            <TabsContent value="accounts"><ProviderAccountsPanel provider={provider} /></TabsContent>
            {sandbox && <TabsContent value="sandbox"><SandboxAccountsPanel provider={provider} /></TabsContent>}
            <TabsContent value="alerts"><InfraAlertsList provider={provider} /></TabsContent>
          </Tabs>
        </>
      )}
    </>
  );
}

export default function ProviderDetailPage() {
  return <React.Suspense fallback={<LoadingRows />}><Inner /></React.Suspense>;
}
