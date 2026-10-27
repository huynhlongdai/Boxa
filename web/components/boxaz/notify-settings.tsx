"use client";
// v1.2: báo cáo & cảnh báo qua Telegram bot và email (SMTP). Secret chỉ ghi, lưu trong kho secret mã hoá của Core.
import * as React from "react";
import { useTranslations } from "next-intl";
import { Bell, CheckCircle2, Mail, Save, Send, XCircle } from "lucide-react";
import { api, paths } from "@/lib/api";
import { useAction, useApi } from "@/lib/hooks";
import { useFmt } from "@/lib/format";
import { NOTIFY_EVENTS, type NotifyInput, type NotifyView, type SmtpTls } from "@/lib/types";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Field, Input, Select } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { Skeleton } from "@/components/ui/skeleton";
import { ErrorBox } from "@/components/boxaz/page";

const TIMEZONES = ["Asia/Ho_Chi_Minh", "Asia/Singapore", "Asia/Shanghai", "Asia/Tokyo", "Europe/Berlin", "America/New_York", "UTC"];

function NotifyForm({ initial }: { initial: NotifyView }) {
  const t = useTranslations("notify");
  const tc = useTranslations("common");
  const f = useFmt();
  const { run, pending } = useAction();
  const smtp0 = initial.smtp ?? { host: "", port: 587, username: "", from: "", tls: "starttls" as SmtpTls };
  const [v, setV] = React.useState<NotifyInput>({
    telegram_chat_id: initial.telegram_chat_id, email: initial.email, events: initial.events, daily_report_hour: initial.daily_report_hour,
    timezone: initial.timezone || "Asia/Ho_Chi_Minh", smtp: { ...smtp0, tls: smtp0.tls || "starttls" },
  });
  const [token, setToken] = React.useState("");
  const [pass, setPass] = React.useState("");
  const smtp = v.smtp!;
  const setSmtp = (p: Partial<typeof smtp>) => setV((x) => ({ ...x, smtp: { ...x.smtp!, ...p } }));
  const save = (extra: Partial<NotifyInput> = {}) =>
    run("save", () => api.saveNotify({ ...v, ...extra, ...(token ? { telegram_bot_token: token } : {}), ...(pass ? { smtp_password: pass } : {}) }), { success: t("saved"), revalidate: [paths.notify, paths.settings] });
  const test = (channel: "telegram" | "email" | "all", kind: "test" | "daily_report" = "test") =>
    run(`test-${channel}-${kind}`, async () => {
      const r = await api.testNotify(channel, kind);
      const failed = r.results.filter((x) => !x.ok);
      if (failed.length) throw new Error(failed.map((x) => `${x.channel}: ${x.error}`).join(" · "));
      return r;
    }, { success: t("testSent"), revalidate: [paths.notify] });

  return (
    <Card id="notify">
      <CardHeader>
        <CardTitle className="flex items-center gap-2"><Bell className="size-4" /> {t("title")}</CardTitle>
        <CardDescription>{t("desc")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-5">
        <section className="grid gap-4 md:grid-cols-2">
          <h4 className="flex items-center gap-2 text-[13px] font-semibold md:col-span-2">
            <Send className="size-3.5" /> Telegram
            {initial.telegram_configured ? <Badge variant="green">{t("ready")}</Badge> : <Badge variant="gray">{t("notSet")}</Badge>}
          </h4>
          <Field label={t("botToken")} hint={initial.has_telegram_token ? t("secretStored") : t("botTokenHint")}>
            <Input type="password" autoComplete="off" value={token} onChange={(e) => setToken(e.target.value)} placeholder={initial.has_telegram_token ? "••••••••" : "123456:ABC…"} className="font-mono" />
          </Field>
          <Field label={t("chatId")} hint={t("chatIdHint")}>
            <Input value={v.telegram_chat_id} onChange={(e) => setV({ ...v, telegram_chat_id: e.target.value })} className="font-mono" />
          </Field>
        </section>

        <section className="grid gap-4 md:grid-cols-2">
          <h4 className="flex items-center gap-2 text-[13px] font-semibold md:col-span-2">
            <Mail className="size-3.5" /> {t("emailTitle")}
            {initial.email_configured ? <Badge variant="green">{t("ready")}</Badge> : <Badge variant="gray">{t("notSet")}</Badge>}
          </h4>
          <Field label={t("recipients")} hint={t("recipientsHint")} className="md:col-span-2">
            <Input value={v.email} onChange={(e) => setV({ ...v, email: e.target.value })} placeholder="anh@example.com, ops@example.com" />
          </Field>
          <Field label={t("smtpHost")}><Input value={smtp.host} onChange={(e) => setSmtp({ host: e.target.value.trim() })} placeholder="smtp.gmail.com" className="font-mono" /></Field>
          <div className="grid grid-cols-2 gap-3">
            <Field label={t("smtpPort")}><Input type="number" min={1} max={65535} value={smtp.port || ""} onChange={(e) => setSmtp({ port: Number(e.target.value) })} placeholder="587" className="font-mono" /></Field>
            <Field label={t("smtpTls")}>
              <Select value={smtp.tls || "starttls"} onChange={(e) => setSmtp({ tls: e.target.value as SmtpTls })}>
                <option value="starttls">STARTTLS</option>
                <option value="tls">TLS</option>
                <option value="none">{t("tlsNone")}</option>
              </Select>
            </Field>
          </div>
          <Field label={t("smtpUser")}><Input autoComplete="off" value={smtp.username} onChange={(e) => setSmtp({ username: e.target.value })} /></Field>
          <Field label={t("smtpPass")} hint={initial.has_smtp_password ? t("secretStored") : undefined}>
            <Input type="password" autoComplete="new-password" value={pass} onChange={(e) => setPass(e.target.value)} placeholder={initial.has_smtp_password ? "••••••••" : ""} />
          </Field>
          <Field label={t("smtpFrom")} className="md:col-span-2"><Input value={smtp.from} onChange={(e) => setSmtp({ from: e.target.value })} placeholder="Boxaz <boxaz@example.com>" /></Field>
        </section>

        <section className="grid gap-4 md:grid-cols-2">
          <Field label={t("reportHour")}>
            <Select value={v.daily_report_hour} onChange={(e) => setV({ ...v, daily_report_hour: Number(e.target.value) })}>
              {Array.from({ length: 24 }).map((_, h) => <option key={h} value={h}>{String(h).padStart(2, "0")}:00</option>)}
            </Select>
          </Field>
          <Field label={t("timezone")}>
            <Select value={v.timezone} onChange={(e) => setV({ ...v, timezone: e.target.value })}>
              {[...new Set([v.timezone ?? "", ...TIMEZONES])].filter(Boolean).map((z) => <option key={z} value={z}>{z}</option>)}
            </Select>
          </Field>
          <div className="flex flex-col gap-2 md:col-span-2">
            <span className="text-[13px] font-medium">{t("events")}</span>
            <div className="grid gap-2 sm:grid-cols-2">
              {NOTIFY_EVENTS.map((id) => (
                <label key={id} className="flex items-center justify-between gap-3 rounded-md border border-border px-3 py-2 text-[13px]">
                  <span className="min-w-0">{t(`event.${id}`)} <span className="block font-mono text-[11px] text-muted-foreground">{id}</span></span>
                  <Switch checked={v.events.includes(id)} onCheckedChange={(c) => setV({ ...v, events: c ? [...v.events, id] : v.events.filter((x) => x !== id) })} />
                </label>
              ))}
            </div>
          </div>
        </section>

        <div className="flex flex-wrap gap-2">
          <Button size="sm" disabled={!!pending} onClick={() => save()}><Save /> {t("save")}</Button>
          <Button size="sm" variant="outline" disabled={!!pending || !initial.telegram_configured} onClick={() => test("telegram")}><Send /> {t("testTelegram")}</Button>
          <Button size="sm" variant="outline" disabled={!!pending || !initial.email_configured} onClick={() => test("email")}><Mail /> {t("testEmail")}</Button>
          <Button size="sm" variant="ghost" disabled={!!pending || (!initial.telegram_configured && !initial.email_configured)} onClick={() => test("all", "daily_report")}>{t("sendReport")}</Button>
          {initial.has_telegram_token && <Button size="sm" variant="ghost" disabled={!!pending} onClick={() => save({ clear_telegram_token: true })}>{t("clearToken")}</Button>}
        </div>
        <p className="text-xs text-muted-foreground">{t("saveFirst")}</p>

        {initial.recent.length > 0 && (
          <div>
            <p className="mb-1.5 text-[13px] font-medium">{t("recent")}</p>
            <ul className="flex flex-col divide-y divide-border rounded-md border border-border">
              {initial.recent.slice(0, 6).map((d, i) => (
                <li key={`${d.ts}-${d.channel}-${i}`} className="flex items-start gap-2 px-3 py-2 text-[12.5px]">
                  {d.ok ? <CheckCircle2 className="mt-0.5 size-3.5 shrink-0 text-st-running" /> : <XCircle className="mt-0.5 size-3.5 shrink-0 text-st-error" />}
                  <span className="min-w-0 flex-1">
                    <span className="font-medium">{d.title}</span> <span className="font-mono text-muted-foreground">· {d.channel} · {d.kind}</span>
                    {d.error && <span className="block break-words text-st-error">{d.error}</span>}
                  </span>
                  <span className="shrink-0 text-muted-foreground">{f.relTime(d.ts)}</span>
                </li>
              ))}
            </ul>
          </div>
        )}
        <span className="sr-only">{tc("saved")}</span>
      </CardContent>
    </Card>
  );
}

export function NotifySettingsCard() {
  const { data, error, mutate } = useApi<NotifyView>(paths.notify, { refreshInterval: 0 });
  if (error) return <ErrorBox error={error} retry={() => mutate()} />;
  if (!data) return <Skeleton className="h-80" />;
  return <NotifyForm key={JSON.stringify({ ...data, recent: undefined })} initial={data} />;
}
