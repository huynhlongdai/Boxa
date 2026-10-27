"use client";
// v1.8/v1.9: gateway domain (URL dễ đọc <name>.<domain>, <name>-<label>.<domain>; alias <key>-<port>) + chính sách preview của provider + trạng thái URL công khai.
import * as React from "react";
import { useTranslations } from "next-intl";
import { Globe2, RefreshCw } from "lucide-react";
import { paths } from "@/lib/api";
import { useApi } from "@/lib/hooks";
import type { GatewayInfo, GatewaySettings, ProviderPreviewMode } from "@/lib/types";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Field, Input, Select } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { Mono } from "@/components/boxaz/status";

export function GatewaySettingsCard({ value, onChange }: { value: GatewaySettings; onChange: (v: GatewaySettings) => void }) {
  const t = useTranslations("gateway");
  const { data: g, mutate, isValidating } = useApi<GatewayInfo>(paths.gateway(true), { refreshInterval: 0 });
  const domainEnv = g?.domain_source === "env";
  const previewEnv = g?.provider_preview_source === "env";
  const mode = g ? ({ host: t("modeHost"), path: t("modePath"), relative: t("modeRelative") } as const)[g.mode] : "";
  return (
    <Card id="gateway" data-testid="gateway-card">
      <CardHeader>
        <CardTitle className="flex flex-wrap items-center gap-2">
          <Globe2 className="size-4" /> {t("title")}
          {g && <Badge variant={g.mode === "host" ? "green" : "gray"}>{mode}</Badge>}
          {g && <Badge variant={g.private_previews ? "green" : "yellow"}>{g.private_previews ? t("privateOn") : t("privateOff")}</Badge>}
        </CardTitle>
        <CardDescription>{t("desc")}</CardDescription>
      </CardHeader>
      <CardContent className="grid gap-4 md:grid-cols-2">
        <Field label={t("domain")} hint={domainEnv ? `${t("fromEnv")}: BOXAZ_GATEWAY_DOMAIN` : t("domainHint")}>
          <Input value={domainEnv ? g?.domain ?? "" : value.domain} disabled={domainEnv} placeholder="example.com" className="font-mono"
            onChange={(e) => onChange({ ...value, domain: e.target.value.trim().toLowerCase() })} data-testid="gateway-domain" />
        </Field>
        <Field label={t("previewMode")} hint={previewEnv ? `${t("fromEnv")}: BOXAZ_PROVIDER_PREVIEW` : t("previewHint")}>
          <Select value={previewEnv ? g?.provider_preview ?? "auto" : value.provider_preview || "auto"} disabled={previewEnv}
            onChange={(e) => onChange({ ...value, provider_preview: e.target.value as ProviderPreviewMode })}>
            <option value="auto">{t("pmAuto")}</option>
            <option value="private">{t("pmPrivate")}</option>
            <option value="public">{t("pmPublic")}</option>
          </Select>
        </Field>
        {!g ? <Skeleton className="h-24 md:col-span-2" /> : (
          <div className="grid gap-2 text-[13px] md:col-span-2">
            <div className="flex flex-wrap items-center gap-2"><span className="w-40 shrink-0 text-muted-foreground">{t("pattern")}</span><Mono className="break-all" data-testid="gateway-pattern">{g.url_pattern}</Mono></div>
            {g.label_pattern && <div className="flex flex-wrap items-center gap-2"><span className="w-40 shrink-0 text-muted-foreground">{t("labelPattern")}</span><Mono className="break-all">{g.label_pattern}</Mono></div>}
            {g.alias_pattern && <div className="flex flex-wrap items-center gap-2"><span className="w-40 shrink-0 text-muted-foreground">{t("aliasPattern")}</span><Mono className="break-all text-muted-foreground">{g.alias_pattern}</Mono></div>}
            <p className="text-xs text-muted-foreground">{t("guessable")}</p>
            <div className="flex flex-wrap items-center gap-2">
              <span className="w-40 shrink-0 text-muted-foreground">{t("publicUrl")}</span>
              {g.public_url ? <Mono className="break-all">{g.public_url}</Mono> : <span className="text-muted-foreground">—</span>}
            </div>
            <div className="flex flex-wrap items-center gap-2">
              <span className="w-40 shrink-0 text-muted-foreground">{t("status")}</span>
              {g.check ? (g.check.ok
                ? <Badge variant="green" data-testid="gateway-status">{t("reachable", { ms: g.check.latency_ms })}</Badge>
                : <Badge variant="red" data-testid="gateway-status" title={g.check.error}>{t("unreachable")}{g.check.status ? ` (${g.check.status})` : ""}</Badge>)
                : <Badge variant="gray">{t("notChecked")}</Badge>}
              <Button size="xs" variant="ghost" onClick={() => mutate()} disabled={isValidating}><RefreshCw className={isValidating ? "animate-spin" : ""} /> {t("recheck")}</Button>
            </div>
            <div className="flex flex-wrap items-start gap-2">
              <span className="w-40 shrink-0 text-muted-foreground">{t("providers")}</span>
              <div className="flex min-w-0 flex-wrap gap-1.5">
                {g.providers.map((p) => (
                  <Badge key={p.provider} title={p.note} variant={!p.preview_host ? "gray" : p.private_preview && g.private_previews ? "green" : "yellow"}>
                    {p.provider}: {!p.preview_host ? t("provNone") : p.private_preview && g.private_previews ? t("provPrivate") : t("provPublic")}
                  </Badge>
                ))}
              </div>
            </div>
            <div className="flex flex-wrap items-start gap-2">
              <span className="w-40 shrink-0 text-muted-foreground">{t("reserved")}</span>
              <p className="min-w-0 flex-1 break-words font-mono text-xs text-muted-foreground">{g.reserved.join(", ")}</p>
            </div>
          </div>
        )}
      </CardContent>
    </Card>
  );
}
