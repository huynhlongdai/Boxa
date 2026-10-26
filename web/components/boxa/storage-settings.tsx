"use client";
// v1.2: nơi lưu snapshot — R2 / B2 / S3 qua S3 API; không cấu hình thì snapshot nằm tại host.
import * as React from "react";
import { useTranslations } from "next-intl";
import { Database, FlaskConical, Save } from "lucide-react";
import { api, paths } from "@/lib/api";
import { useAction, useApi } from "@/lib/hooks";
import type { StorageInput, StorageProvider, StorageView } from "@/lib/types";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Field, Input, Select } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { ErrorBox } from "@/components/boxa/page";

const PLACEHOLDER: Record<StorageProvider, string> = {
  local: "",
  r2: "https://<account-id>.r2.cloudflarestorage.com",
  b2: "https://s3.us-west-004.backblazeb2.com",
  s3: "https://s3.eu-central-1.amazonaws.com",
  minio: "http://minio.internal:9000",
};

function StorageForm({ initial }: { initial: StorageView }) {
  const t = useTranslations("storage");
  const { run, pending } = useAction();
  const [v, setV] = React.useState<StorageInput>({
    provider: initial.provider || "local", endpoint: initial.endpoint, region: initial.region, bucket: initial.bucket, prefix: initial.prefix, access_key_id: initial.access_key_id,
  });
  const [secret, setSecret] = React.useState("");
  const fromEnv = initial.source === "env";
  const remote = v.provider !== "local";
  return (
    <Card id="storage">
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <Database className="size-4" /> {t("title")}
          {initial.configured ? <Badge variant="green">{t("active", { bucket: initial.bucket })}</Badge> : <Badge variant="gray">{t("localOnly")}</Badge>}
          {fromEnv && <Badge variant="outline">{t("fromEnv")}</Badge>}
        </CardTitle>
        <CardDescription>{t("desc")}</CardDescription>
      </CardHeader>
      <CardContent className="grid gap-4 md:grid-cols-2">
        <Field label={t("provider")}>
          <Select value={v.provider} disabled={fromEnv} onChange={(e) => setV({ ...v, provider: e.target.value as StorageProvider })}>
            <option value="local">{t("pLocal")}</option>
            <option value="r2">Cloudflare R2</option>
            <option value="b2">Backblaze B2 (S3 API)</option>
            <option value="s3">AWS S3</option>
            <option value="minio">MinIO / S3-compatible</option>
          </Select>
        </Field>
        {remote ? (
          <>
            <Field label={t("endpoint")}><Input disabled={fromEnv} value={v.endpoint} onChange={(e) => setV({ ...v, endpoint: e.target.value.trim() })} placeholder={PLACEHOLDER[v.provider]} className="font-mono" /></Field>
            <Field label={t("bucket")}><Input disabled={fromEnv} value={v.bucket} onChange={(e) => setV({ ...v, bucket: e.target.value.trim() })} placeholder="boxa-snapshots" className="font-mono" /></Field>
            <Field label={t("region")} hint={t("regionHint")}><Input disabled={fromEnv} value={v.region} onChange={(e) => setV({ ...v, region: e.target.value.trim() })} placeholder={v.provider === "r2" ? "auto" : "us-west-004"} className="font-mono" /></Field>
            <Field label={t("prefix")}><Input disabled={fromEnv} value={v.prefix} onChange={(e) => setV({ ...v, prefix: e.target.value.trim() })} placeholder="boxa" className="font-mono" /></Field>
            <Field label={t("accessKey")}><Input disabled={fromEnv} autoComplete="off" value={v.access_key_id} onChange={(e) => setV({ ...v, access_key_id: e.target.value.trim() })} className="font-mono" /></Field>
            <Field label={t("secretKey")} hint={initial.has_secret ? t("secretStored") : undefined} className="md:col-span-2">
              <Input disabled={fromEnv} type="password" autoComplete="new-password" value={secret} onChange={(e) => setSecret(e.target.value)} placeholder={initial.has_secret ? "••••••••" : ""} className="font-mono" />
            </Field>
          </>
        ) : (
          <p className="self-end text-[13px] text-muted-foreground">{t("localDesc")}</p>
        )}
        <p className="text-xs text-muted-foreground md:col-span-2">{t("retentionNote", { keep: initial.keep_snapshots, days: initial.snapshot_retention_days || "∞" })}</p>
        <div className="flex flex-wrap gap-2 md:col-span-2">
          <Button size="sm" disabled={!!pending || fromEnv} onClick={() => run("save", () => api.saveStorage({ ...v, ...(secret ? { secret_access_key: secret } : {}) }), { success: t("saved"), revalidate: [paths.storage] })}><Save /> {t("save")}</Button>
          <Button size="sm" variant="outline" disabled={!!pending || !initial.configured} onClick={() => run("test", async () => {
            const r = await api.testStorage();
            if (!r.ok) throw new Error(r.error || "storage test failed");
            return r;
          }, { success: (r) => t("testOk", { ms: r.latency_ms }) })}><FlaskConical /> {t("test")}</Button>
        </div>
      </CardContent>
    </Card>
  );
}

export function StorageSettingsCard() {
  const { data, error, mutate } = useApi<StorageView>(paths.storage, { refreshInterval: 0 });
  if (error) return <ErrorBox error={error} retry={() => mutate()} />;
  if (!data) return <Skeleton className="h-64" />;
  return <StorageForm key={JSON.stringify(data)} initial={data} />;
}
