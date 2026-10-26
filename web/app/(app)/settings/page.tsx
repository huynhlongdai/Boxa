"use client";
import * as React from "react";
import { Bell, Globe, PiggyBank, Recycle, Save } from "lucide-react";
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

// Tên sự kiện theo Core (model.Settings.Notify.Events)
const EVENTS: [string, string][] = [
  ["budget", "Agent vượt 80% ngân sách"],
  ["approval", "Có yêu cầu cần anh duyệt"],
  ["box_failed", "Box lỗi"],
  ["host_down", "Host mất kết nối"],
  ["reaper", "Box bị Reaper cho ngủ / xoá"],
  ["emergency_stop", "Dừng khẩn cấp"],
];

function Form({ initial }: { initial: Settings }) {
  const [s, setS] = React.useState<Settings>(initial);
  const { run, pending } = useAction();
  const { data: ov } = useApi<Overview>(paths.overview);
  const upd = <K extends keyof Settings>(k: K, v: Settings[K]) => setS((x) => ({ ...x, [k]: v }));
  const dirty = JSON.stringify(s) !== JSON.stringify(initial);
  const exampleUrl = `shopee-crawl-01-49-12-88-17.${s.base_domain || "sslip.io"}`;

  return (
    <div className="flex flex-col gap-4">
      <Card>
        <CardHeader><CardTitle className="flex items-center gap-2"><Bell className="size-4" /> Thông báo</CardTitle><CardDescription>Telegram bot có nút Duyệt / Từ chối ngay trong tin nhắn.</CardDescription></CardHeader>
        <CardContent className="grid gap-4 md:grid-cols-2">
          <Field label="Telegram chat ID"><Input value={s.notify.telegram_chat_id} onChange={(e) => upd("notify", { ...s.notify, telegram_chat_id: e.target.value })} className="font-mono" /></Field>
          <Field label="Email"><Input type="email" value={s.notify.email} onChange={(e) => upd("notify", { ...s.notify, email: e.target.value })} /></Field>
          <Field label="Giờ gửi báo cáo ngày" hint="Giờ Việt Nam (UTC+7)">
            <Select value={s.notify.daily_report_hour} onChange={(e) => upd("notify", { ...s.notify, daily_report_hour: Number(e.target.value) })}>
              {Array.from({ length: 24 }).map((_, h) => <option key={h} value={h}>{String(h).padStart(2, "0")}:00</option>)}
            </Select>
          </Field>
          <div className="flex flex-col gap-2 md:col-span-2">
            <span className="text-[13px] font-medium">Loại sự kiện gửi thông báo</span>
            <div className="grid gap-2 sm:grid-cols-2">
              {EVENTS.map(([id, label]) => (
                <label key={id} className="flex items-center justify-between gap-3 rounded-md border border-border px-3 py-2 text-[13px]">
                  <span>{label} <span className="block font-mono text-[11px] text-muted-foreground">{id}</span></span>
                  <Switch checked={s.notify.events.includes(id)} onCheckedChange={(c) => upd("notify", { ...s.notify, events: c ? [...s.notify.events, id] : s.notify.events.filter((x) => x !== id) })} />
                </label>
              ))}
            </div>
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader><CardTitle className="flex items-center gap-2"><Recycle className="size-4" /> Mặc định Reaper</CardTitle><CardDescription>Áp dụng khi agent không tự khai báo.</CardDescription></CardHeader>
        <CardContent className="grid gap-4 sm:grid-cols-3">
          <Field label="Ngủ sau (phút rảnh)"><Input type="number" min={1} value={s.reaper.idle_minutes} onChange={(e) => upd("reaper", { ...s.reaper, idle_minutes: Number(e.target.value) })} className="font-mono" /></Field>
          <Field label="TTL mặc định (giờ)" hint="Cho lifecycle task"><Input type="number" min={1} value={Math.round(s.reaper.default_ttl_seconds / 3600)} onChange={(e) => upd("reaper", { ...s.reaper, default_ttl_seconds: Number(e.target.value) * 3600 })} className="font-mono" /></Field>
          <Field label="Giữ số snapshot / box"><Input type="number" min={0} value={s.reaper.keep_snapshots} onChange={(e) => upd("reaper", { ...s.reaper, keep_snapshots: Number(e.target.value) })} className="font-mono" /></Field>
        </CardContent>
      </Card>

      <Card>
        <CardHeader><CardTitle className="flex items-center gap-2"><Globe className="size-4" /> Domain</CardTitle><CardDescription>v1 dùng sslip.io miễn phí + HTTPS. Mua domain riêng thì chỉ cần đổi biến này.</CardDescription></CardHeader>
        <CardContent className="grid gap-4 md:grid-cols-2">
          <Field label="BASE_DOMAIN" hint={<>URL box sẽ có dạng <span className="font-mono">{exampleUrl}</span></>}>
            <Input value={s.base_domain} onChange={(e) => upd("base_domain", e.target.value.trim())} className="font-mono" />
          </Field>
          <label className="flex items-center justify-between gap-3 self-start rounded-md border border-border px-3 py-2.5 text-[13px] md:mt-6">
            <span>Bảo vệ URL box bằng token<span className="block text-xs text-muted-foreground">Thêm ?t=… vào URL, người lạ không mở được</span></span>
            <Switch checked={s.url_token_protect} onCheckedChange={(c) => upd("url_token_protect", c)} />
          </label>
        </CardContent>
      </Card>

      <Card>
        <CardHeader><CardTitle className="flex items-center gap-2"><PiggyBank className="size-4" /> Ngân sách tổng tháng</CardTitle></CardHeader>
        <CardContent className="grid gap-4 md:grid-cols-2">
          <Field label="USD / tháng" hint="Ngân sách ngày = tháng / 30"><Input type="number" min={0} value={s.budget_month_usd} onChange={(e) => upd("budget_month_usd", Number(e.target.value))} className="font-mono" /></Field>
          {ov && (
            <div className="md:mt-6">
              <p className="mb-1 text-[13px] text-muted-foreground">Đã chi tháng này <MoneyBudget spent={ov.spent_month} budget={s.budget_month_usd} /></p>
              <BudgetBar spent={ov.spent_month} budget={s.budget_month_usd} />
            </div>
          )}
        </CardContent>
      </Card>

      <div className="sticky bottom-20 z-10 flex justify-end md:bottom-4">
        <Button disabled={!dirty || !!pending} className={cn("shadow-lg", !dirty && "hidden md:inline-flex")} onClick={() => run("save", () => api.saveSettings(s), { success: "Đã lưu cài đặt", revalidate: [paths.settings, paths.overview] })}>
          <Save /> {dirty ? "Lưu cài đặt" : "Đã lưu"}
        </Button>
      </div>
    </div>
  );
}

export default function SettingsPage() {
  const { data, error, mutate } = useApi<Settings>(paths.settings, { refreshInterval: 0 });
  return (
    <>
      <PageHeader title="Cài đặt" desc="Thông báo, Reaper, domain và ngân sách." />
      {error ? <ErrorBox error={error} retry={() => mutate()} /> : !data ? <Skeleton className="h-96" /> : <Form key={JSON.stringify(data)} initial={data} />}
    </>
  );
}
