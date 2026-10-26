"use client";
import * as React from "react";
import { Bell, Globe, Languages, PiggyBank, Recycle, Save } from "lucide-react";
import { useTranslations } from "next-intl";
import { api, paths } from "@/lib/api";
import { useAction, useApi } from "@/lib/hooks";
import type { Overview, Settings } from "@/lib/types";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Field, Input, Select } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { Skeleton } from "@/components/ui/skeleton";
import { MoneyBudget, BudgetBar } from "@/components/boxa/budget";
import { ErrorBox, PageHeader } from "@/components/boxa/page";
import { cn } from "@/lib/utils";
import { LocaleGrid } from "@/components/locale-switcher";
import { LOCALES, LOCALE_NATIVE } from "@/i18n/config";

// Tên sự kiện theo Core (model.Settings.Notify.Events)
const EVENTS = ["budget", "approval", "box_failed", "host_down", "reaper", "emergency_stop"] as const;

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

      <Card>
        <CardHeader><CardTitle className="flex items-center gap-2"><Bell className="size-4" /> {t("notify")}</CardTitle><CardDescription>{t("notifyDesc")}</CardDescription></CardHeader>
        <CardContent className="grid gap-4 md:grid-cols-2">
          <Field label={t("telegram")}><Input value={s.notify.telegram_chat_id} onChange={(e) => upd("notify", { ...s.notify, telegram_chat_id: e.target.value })} className="font-mono" /></Field>
          <Field label={t("email")}><Input type="email" value={s.notify.email} onChange={(e) => upd("notify", { ...s.notify, email: e.target.value })} /></Field>
          <Field label={t("reportHour")} hint={t("reportHourHint")}>
            <Select value={s.notify.daily_report_hour} onChange={(e) => upd("notify", { ...s.notify, daily_report_hour: Number(e.target.value) })}>
              {Array.from({ length: 24 }).map((_, h) => <option key={h} value={h}>{String(h).padStart(2, "0")}:00</option>)}
            </Select>
          </Field>
          <div className="flex flex-col gap-2 md:col-span-2">
            <span className="text-[13px] font-medium">{t("events")}</span>
            <div className="grid gap-2 sm:grid-cols-2">
              {EVENTS.map((id) => (
                <label key={id} className="flex items-center justify-between gap-3 rounded-md border border-border px-3 py-2 text-[13px]">
                  <span className="min-w-0">{t(`event.${id}`)} <span className="block font-mono text-[11px] text-muted-foreground">{id}</span></span>
                  <Switch checked={s.notify.events.includes(id)} onCheckedChange={(c) => upd("notify", { ...s.notify, events: c ? [...s.notify.events, id] : s.notify.events.filter((x) => x !== id) })} />
                </label>
              ))}
            </div>
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader><CardTitle className="flex items-center gap-2"><Recycle className="size-4" /> {t("reaper")}</CardTitle><CardDescription>{t("reaperDesc")}</CardDescription></CardHeader>
        <CardContent className="grid gap-4 sm:grid-cols-3">
          <Field label={t("idle")}><Input type="number" min={1} value={s.reaper.idle_minutes} onChange={(e) => upd("reaper", { ...s.reaper, idle_minutes: Number(e.target.value) })} className="font-mono" /></Field>
          <Field label={t("ttl")} hint={t("ttlHint")}><Input type="number" min={1} value={Math.round(s.reaper.default_ttl_seconds / 3600)} onChange={(e) => upd("reaper", { ...s.reaper, default_ttl_seconds: Number(e.target.value) * 3600 })} className="font-mono" /></Field>
          <Field label={t("keepSnapshots")}><Input type="number" min={0} value={s.reaper.keep_snapshots} onChange={(e) => upd("reaper", { ...s.reaper, keep_snapshots: Number(e.target.value) })} className="font-mono" /></Field>
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
        </CardContent>
      </Card>

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
