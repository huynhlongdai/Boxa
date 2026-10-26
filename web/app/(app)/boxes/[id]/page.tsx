"use client";
import * as React from "react";
import Link from "next/link";
import { useParams, useRouter, useSearchParams } from "next/navigation";
import { ArrowLeft, ExternalLink } from "lucide-react";
import { useTranslations } from "next-intl";
import { paths } from "@/lib/api";
import { useApi } from "@/lib/hooks";
import type { Box } from "@/lib/types";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Skeleton } from "@/components/ui/skeleton";
import { BoxStatusChip, LifecycleBadge, Mono } from "@/components/boxa/status";
import { BoxActions } from "@/components/boxa/box-actions";
import { CloneButton } from "@/components/boxa/box-v13";
import { BoxPlacementBadges } from "@/components/boxa/sandbox";
import { ErrorBox } from "@/components/boxa/page";
import { BoxTerminal } from "@/components/boxa/terminal";
import { OverviewTab, TimelineTab, FilesTab, LogsTab, SnapshotsTab, ConfigTab } from "./tabs";

const TABS = ["overview", "terminal", "timeline", "files", "logs", "snapshots", "config"] as const;

function Inner() {
  const { id } = useParams<{ id: string }>();
  const sp = useSearchParams();
  const router = useRouter();
  const t = useTranslations("boxDetail");
  const tab = sp.get("tab") ?? "overview";
  const { data: box, error, mutate } = useApi<Box>(paths.box(id), { refreshInterval: (b) => (b && ["provisioning", "placing", "requested"].includes(b.status) ? 2000 : 15000) });

  if (error) return <ErrorBox error={error} retry={() => mutate()} />;
  if (!box) return <Skeleton className="h-64" />;

  return (
    <>
      <Link href="/boxes" className="mb-3 inline-flex items-center gap-1 text-[13px] text-muted-foreground hover:text-foreground">
        <ArrowLeft className="size-4" /> {t("back")}
      </Link>
      <div className="mb-4 flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <h1 className="break-all font-mono text-xl font-semibold">{box.slug}</h1>
            <BoxStatusChip status={box.status} />
            <LifecycleBadge lc={box.lifecycle} />
          </div>
          <p className="mt-1 break-words text-[13px] text-muted-foreground">
            <Mono>{box.id}</Mono> · {box.agent_name} · <Mono>{box.provider}/{box.region}</Mono>
            {box.url && (
              <>
                {" · "}
                <a href={box.url} target="_blank" rel="noreferrer" className="inline-flex items-center gap-1 text-primary hover:underline">
                  <Mono>{box.url.replace(/^https?:\/\//, "")}</Mono> <ExternalLink className="size-3" />
                </a>
              </>
            )}
          </p>
          <div className="mt-1.5"><BoxPlacementBadges box={box} /></div>
        </div>
        <div className="flex flex-wrap items-center gap-1">
          <CloneButton box={box} />
          <BoxActions box={box} labels className="flex-wrap" onDeleted={() => router.push("/boxes")} />
        </div>
      </div>

      <Tabs value={tab} onValueChange={(v) => router.replace(`/boxes/${id}?tab=${v}`, { scroll: false })}>
        <TabsList>
          {TABS.map((v) => (
            <TabsTrigger key={v} value={v}>{t(`tabs.${v}`)}</TabsTrigger>
          ))}
        </TabsList>
        <TabsContent value="overview"><OverviewTab box={box} /></TabsContent>
        <TabsContent value="terminal"><BoxTerminal box={box} /></TabsContent>
        <TabsContent value="timeline"><TimelineTab box={box} /></TabsContent>
        <TabsContent value="files"><FilesTab box={box} /></TabsContent>
        <TabsContent value="logs"><LogsTab box={box} /></TabsContent>
        <TabsContent value="snapshots"><SnapshotsTab box={box} /></TabsContent>
        <TabsContent value="config"><ConfigTab box={box} /></TabsContent>
      </Tabs>
    </>
  );
}

export default function BoxDetailPage() {
  return (
    <React.Suspense fallback={<Skeleton className="h-64" />}>
      <Inner />
    </React.Suspense>
  );
}
