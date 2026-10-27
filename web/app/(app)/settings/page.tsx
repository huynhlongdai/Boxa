"use client";
import * as React from "react";
import { Globe, Languages, Network, PiggyBank, Recycle, Save } from "lucide-react";
import { useTranslations } from "next-intl";
import { api, paths } from "@/lib/api";
import { useAction, useApi } from "@/lib/hooks";
import type { Overview, Settings } from "@/lib/types";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Field, Input, Select } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { Skeleton } from "@/components/ui/skeleton";
import { MoneyBudget, BudgetBar } from "@/components/boxaz/budget";
import { ErrorBox, PageHeader } from "@/components/boxaz/page";
import { cn } from "@/lib/utils";
import { LocaleGrid } from "@/components/locale-switcher";
import { LOCALES, LOCALE_NATIVE } from "@/i18n/config";
import { NotifySettingsCard } from "@/components/boxaz/notify-settings";
import { StorageSettingsCard } from "@/components/boxaz/storage-settings";
import { GatewaySettingsCard } from "@/components/boxaz/gateway-settings";
import { SshKeysCard } from "@/components/boxaz/access";

function Form({ initial }: { initial: Settings }) {
  const [s, setS] = React.useState<Settings>(initial);
  const { run, pending } = useAction();
  const t = useTranslations("settings");
  const tl = useTranslations("locale");
  const tc = useTranslations("common");
  const { data: ov } = useApi<Overview>(paths.overview);
  const upd = <K extends keyof Settings>(k: K, v: Settings[K]) => setS((x) => ({ ...x, [k]: v }));
  const dirty = JSON.stringify(s) !== JSON.stringify(initial);
  const exampleUrl = `shopee-crawl-01-49-12-88-17.${s.base_domain || "sslip.io"}`;

  return (
    <div className="flex flex-col gap-4">
      <Card id="language">
        <CardHeader><CardTitle className="flex items-center gap-2"><Languages className="size-4" /> {t("language")}</CardTitle></CardHeader>
        <CardContent className="grid gap-5 md:grid-cols-2">
          <div className="flex flex-col gap-1.5">
            <span className="text-[13px] font-medium">{tl("dashboard")}</span>
            <LocaleGrid className="sm:grid-cols-2" />
            <p className="text-xs text-muted-foreground">{tl("dashboardHint")}</p>
          </div>
          <Field label={tl("notify")} hint={tl("notifyHint")}>
            <Select value={s.locale ?? "vi"} onChange={(e) => upd("locale", e.target.value)}>
              {LOCALES.map((l) => <option key={l} value={l}>{LOCALE_NATIVE[l]}</option>)}
            </Select>
          </Field>
        </CardContent>
      </Card>

      <NotifySettingsCard />

      <StorageSettingsCard />

      <Card>
        <CardHeader><CardTitle className="flex items-center gap-2"><Recycle className="size-4" /> {t("reaper")}</CardTitle><CardDescription>{t("reaperDesc")}</CardDescription></CardHeader>
        <CardContent className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
          <Field label={t("idle")}><Input type="number" min={1} value={s.reaper.idle_minutes} onChange={(e) => upd("reaper", { ...s.reaper, idle_minutes: Number(e.target.value) })} className="font-mono" /></Field>
          <Field label={t("ttl")} hint={t("ttlHint")}><Input type="number" min={1} value={Math.round(s.reaper.default_ttl_seconds / 3600)} onChange={(e) => upd("reaper", { ...s.reaper, default_ttl_seconds: Number(e.target.value) * 3600 })} className="font-mono" /></Field>
          <Field label={t("keepSnapshots")}><Input type="number" min={0} value={s.reaper.keep_snapshots} onChange={(e) => upd("reaper", { ...s.reaper, keep_snapshots: Number(e.target.value) })} className="font-mono" /></Field>
          <Field label={t("retentionDays")} hint={t("retentionDaysHint")}><Input type="number" min={0} value={s.reaper.snapshot_retention_days ?? 0} onChange={(e) => upd("reaper", { ...s.reaper, snapshot_retention_days: Number(e.target.value) })} className="font-mono" /></Field>
        </CardContent>
      </Card>

      <Card id="network">
        <CardHeader><CardTitle className="flex items-center gap-2"><Network className="size-4" /> {t("network")}</CardTitle><CardDescription>{t("networkDesc")}</CardDescription></CardHeader>
        <CardContent>
          <label className="flex items-center justify-between gap-3 rounded-md border border-border px-3 py-2.5 text-[13px]">
            <span className="min-w-0">{t("egressDeny")}<span className="block text-xs text-muted-foreground">{t("egressDenyHint")}</span></span>
            <Switch checked={!!s.egress_deny_by_default} onCheckedChange={(c) => upd("egress_deny_by_default", c)} />
          </label>
        </CardContent>
      </Card>

      <Card>
        <CardHeader><CardTitle className="flex items-center gap-2"><Globe className="size-4" /> {t("domain")}</CardTitle><CardDescription>{t("domainDesc")}</CardDescription></CardHeader>
        <CardContent className="grid gap-4 md:grid-cols-2">
          <Field label="BASE_DOMAIN" hint={t.rich("domainHint", { url: exampleUrl, mono: (c) => <span className="break-all font-mono">{c}</span> })}>
            <Input value={s.base_domain} onChange={(e) => upd("base_domain", e.target.value.trim())} className="font-mono" />
          </Field>
          <label className="flex items-center justify-between gap-3 self-start rounded-md border border-border px-3 py-2.5 text-[13px] md:mt-6">
            <span className="min-w-0">{t("urlToken")}<span className="block text-xs text-muted-foreground">{t("urlTokenHint")}</span></span>
            <Switch checked={s.url_token_protect} onCheckedChange={(c) => upd("url_token_protect", c)} />
          </label>
          <label className="flex items-center justify-between gap-3 self-start rounded-md border border-border px-3 py-2.5 text-[13px] md:col-start-2">
            <span className="min-w-0">{t("tunnel")}<span className="block text-xs text-muted-foreground">{t("tunnelHint")}</span></span>
            <Switch checked={!s.disable_tunnel} onCheckedChange={(c) => upd("disable_tunnel", !c)} />
          </label>
        </CardContent>
      </Card>

      <SshKeysCard />

      <GatewaySettingsCard value={s.gateway ?? { domain: "", provider_preview: "" }} onChange={(v) => upd("gateway", v)} />

      <Card>
        <CardHeader><CardTitle className="flex items-center gap-2"><PiggyBank className="size-4" /> {t("budget")}</CardTitle></CardHeader>
        <CardContent className="grid gap-4 md:grid-cols-2">
          <Field label={t("budgetUsd")} hint={t("budgetHint")}><Input type="number" min={0} value={s.budget_month_usd} onChange={(e) => upd("budget_month_usd", Number(e.target.value))} className="font-mono" /></Field>
          {ov && (
            <div className="md:mt-6">
              <p className="mb-1 text-[13px] text-muted-foreground">{t("spentMonth")} <MoneyBudget spent={ov.spent_month} budget={s.budget_month_usd} /></p>
              <BudgetBar spent={ov.spent_month} budget={s.budget_month_usd} />
            </div>
          )}
        </CardContent>
      </Card>

      <div className="sticky bottom-20 z-10 flex justify-end md:bottom-4">
        <Button disabled={!dirty || !!pending} className={cn("shadow-lg", !dirty && "hidden md:inline-flex")} onClick={() => run("save", () => api.saveSettings(s), { success: t("savedToast"), revalidate: [paths.settings, paths.overview] })}>
          <Save /> {dirty ? t("save") : tc("saved")}
        </Button>
      </div>
    </div>
  );
}

export default function SettingsPage() {
  const { data, error, mutate } = useApi<Settings>(paths.settings, { refreshInterval: 0 });
  const t = useTranslations("settings");
  return (
    <>
      <PageHeader title={t("title")} desc={t("desc")} />
      {error ? <ErrorBox error={error} retry={() => mutate()} /> : !data ? <Skeleton className="h-96" /> : <Form key={JSON.stringify(data)} initial={data} />}
    </>
  );
}
