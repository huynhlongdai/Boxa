"use client";
import * as React from "react";
import { ArrowDownUp, CheckCircle2, Cpu, Plug, Plus, ServerCog, Trash2, TrendingDown } from "lucide-react";
import { api, paths } from "@/lib/api";
import { useAction, useApi } from "@/lib/hooks";
import type { Host, List, Offer, Provider } from "@/lib/types";
import { mb, relTime, usd } from "@/lib/utils";
import { Card } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input, Select } from "@/components/ui/input";
import { Progress } from "@/components/ui/progress";
import { Table, TBody, TD, TH, THead, TR } from "@/components/ui/table";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog";
import { ConfirmButton } from "@/components/boxa/confirm";
import { ApprovalList } from "@/components/boxa/approvals";
import { ConfidenceBadge, HostStatusChip, Mono, ProviderChip } from "@/components/boxa/status";
import { ErrorBox, LoadingRows, PageHeader, SectionTitle } from "@/components/boxa/page";

const TIER_DESC = { T1: "VPS/VM thô · boxd + Incus", T2: "Sandbox API · boxd dial-out", T3: "Chỉ exec API · boxd-shim" } as const;

function ProviderCard({ p }: { p: Provider }) {
  const [token, setToken] = React.useState("");
  const [editing, setEditing] = React.useState(!p.connected);
  const { run, pending } = useAction();
  return (
    <Card className="flex flex-col p-4">
      <div className="flex items-start justify-between gap-2">
        <div>
          <p className="font-medium">{p.name}</p>
          <p className="mt-0.5 flex items-center gap-1.5 text-xs text-muted-foreground">
            <Badge variant="outline" className="font-mono">{p.tier}</Badge> {TIER_DESC[p.tier]}
          </p>
        </div>
        <ProviderChip p={p} />
      </div>
      {p.error && <p className="mt-2 text-[12.5px] text-st-error">{p.error}</p>}
      <p className="mt-2 text-xs text-muted-foreground">Kiểm tra lần cuối: {relTime(p.last_check_at)}</p>
      <div className="mt-auto pt-3">
        {editing ? (
          <form className="flex gap-2" onSubmit={async (e) => {
            e.preventDefault();
            const r = await run("tok", () => api.setProviderToken(p.id, token), { success: `Đã kết nối ${p.name}`, revalidate: [paths.providers] });
            if (r) { setToken(""); setEditing(false); }
          }}>
            <Input type="password" autoComplete="off" value={token} onChange={(e) => setToken(e.target.value)} placeholder="Dán API token" className="font-mono" aria-label={`API token ${p.name}`} />
            <Button type="submit" size="sm" disabled={!token || !!pending}>{pending ? "Đang kiểm tra…" : "Kiểm tra"}</Button>
          </form>
        ) : (
          <Button size="sm" variant="outline" onClick={() => setEditing(true)}><Plug /> Đổi token</Button>
        )}
      </div>
    </Card>
  );
}

function Bar({ label, v }: { label: string; v: number }) {
  return (
    <div className="flex items-center gap-2 text-[12px]">
      <span className="w-9 text-muted-foreground">{label}</span>
      <Progress value={v} warnAt={75} dangerAt={92} className="flex-1" />
      <span className="w-9 text-right font-mono">{v}%</span>
    </div>
  );
}

function AddHostDialog({ offers, providers }: { offers: Offer[]; providers: Provider[] }) {
  const connected = providers.filter((p) => p.connected).map((p) => p.id as string);
  const choices = offers.filter((o) => connected.includes(o.provider));
  const [sel, setSel] = React.useState(0);
  const [open, setOpen] = React.useState(false);
  const { run, pending } = useAction();
  const o = choices[sel];
  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild><Button size="sm"><Plus /> Thêm host pool</Button></DialogTrigger>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Thêm host pool</DialogTitle>
          <DialogDescription>Host tính tiền theo tháng, Scheduler sẽ xếp box vào khi còn chỗ.</DialogDescription>
        </DialogHeader>
        <Select value={sel} onChange={(e) => setSel(Number(e.target.value))}>
          {choices.map((c, i) => <option key={`${c.provider}-${c.sku}-${c.region}`} value={i}>{c.provider} · {c.sku} · {c.region} — {usd(c.price_month, 2)}/tháng</option>)}
        </Select>
        {o && <p className="text-[13px] text-muted-foreground"><Mono>{o.cpu} vCPU · {mb(o.ram_mb)} · {o.disk_gb} GB</Mono> · <ConfidenceBadge c={o.confidence} /></p>}
        <DialogFooter>
          <Button variant="ghost" onClick={() => setOpen(false)}>Huỷ</Button>
          <Button disabled={!o || !!pending} onClick={async () => {
            const h = await run("h", () => api.createHost({ provider: o.provider, sku: o.sku, region: o.region }), { success: "Đang tạo host…", revalidate: [paths.hosts] });
            if (h) setOpen(false);
          }}>Tạo host ({o ? usd(o.price_month, 2) : ""}/tháng)</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

export default function InfraPage() {
  const { data: providers, error: pe } = useApi<List<Provider>>(paths.providers);
  const { data: hosts, error: he, mutate: mh } = useApi<List<Host>>(paths.hosts, { refreshInterval: (d) => (d?.items.some((h) => h.status === "provisioning") ? 2500 : 15000) });
  const { data: offers } = useApi<List<Offer>>(paths.offers);
  const { run } = useAction();
  const [sortAsc, setSortAsc] = React.useState(true);
  const sortedOffers = [...(offers?.items ?? [])].sort((a, b) => (sortAsc ? 1 : -1) * (a.price_hour / a.cpu - b.price_hour / b.cpu));
  const poolMonth = (hosts?.items ?? []).filter((h) => h.billing === "pool").reduce((a, h) => a + h.price_month, 0);

  return (
    <>
      <PageHeader title="Hạ tầng" desc="Providers, hosts trong pool và bảng giá thị trường." />

      <SectionTitle>Đề xuất mở rộng pool</SectionTitle>
      <div className="mb-6"><ApprovalList filter={(a) => a.type === "pool_expand" || a.type === "host_down"} emptyText="Chưa có đề xuất mở rộng pool." /></div>

      <SectionTitle>Providers</SectionTitle>
      {pe ? <ErrorBox error={pe} /> : !providers ? <LoadingRows rows={2} /> : (
        <div className="mb-6 grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
          {providers.items.map((p) => <ProviderCard key={p.id} p={p} />)}
        </div>
      )}

      <SectionTitle right={providers && offers && <AddHostDialog offers={offers.items} providers={providers.items} />}>
        Hosts <span className="ml-1 text-[13px] font-normal text-muted-foreground">· pool {usd(poolMonth, 2)}/tháng</span>
      </SectionTitle>
      {he ? <ErrorBox error={he} retry={() => mh()} /> : !hosts ? <LoadingRows /> : (
        <div className="mb-6 grid gap-3 md:grid-cols-2 xl:grid-cols-3">
          {hosts.items.map((h) => (
            <Card key={h.id} className="p-4">
              <div className="flex items-start justify-between gap-2">
                <div className="min-w-0">
                  <p className="flex items-center gap-1.5"><ServerCog className="size-4 text-muted-foreground" /><Mono className="truncate">{h.id}</Mono></p>
                  <p className="mt-0.5 text-xs text-muted-foreground"><Mono>{h.provider}/{h.region} · {h.sku} · {h.ip}</Mono></p>
                </div>
                <HostStatusChip status={h.status} />
              </div>
              <div className="mt-3 flex flex-col gap-1.5">
                <Bar label="CPU" v={h.cpu_used_pct} />
                <Bar label="RAM" v={h.ram_used_pct} />
                <Bar label="Đĩa" v={h.disk_used_pct} />
              </div>
              <div className="mt-3 flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
                <Badge variant={h.billing === "pool" ? "green" : "yellow"}>{h.billing === "pool" ? "pool · tháng" : "burst · giờ"}</Badge>
                <span><Cpu className="mr-0.5 inline size-3" /><Mono>{h.cpu} vCPU · {mb(h.ram_mb)} · {h.disk_gb} GB</Mono></span>
                <span>{h.box_count} box</span>
                <span className="ml-auto font-mono text-foreground">{h.billing === "pool" ? `${usd(h.price_month, 2)}/tháng` : `${usd(h.price_hour)}/giờ`}</span>
              </div>
              <div className="mt-3 flex gap-2 border-t border-border pt-3">
                <ConfirmButton size="xs" variant="outline" disabled={h.status !== "ready"} title={`Drain ${h.id}?`} desc="Host ngừng nhận box mới; box đang chạy sẽ được chuyển dần sang host khác khi ngủ/thức." confirmLabel="Drain"
                  onConfirm={() => run("d", () => api.drainHost(h.id), { success: "Đang drain host", revalidate: [paths.hosts] })}>
                  <ArrowDownUp /> Drain
                </ConfirmButton>
                <ConfirmButton size="xs" variant="outline" destructive title={`Xoá host ${h.id}?`} desc="Huỷ máy ở nhà cung cấp. Chỉ xoá được khi không còn box đang chạy." confirmLabel="Xoá host" typeToConfirm={h.id}
                  onConfirm={() => run("x", () => api.deleteHost(h.id), { success: "Đã xoá host", revalidate: [paths.hosts] })}>
                  <Trash2 /> Xoá
                </ConfirmButton>
              </div>
            </Card>
          ))}
        </div>
      )}

      <SectionTitle right={<span className="text-xs text-muted-foreground">Nguồn: battleships.dev · cập nhật {relTime(offers?.items[0]?.fetched_at)}</span>}>Bảng giá</SectionTitle>
      <Card>
        <Table>
          <THead>
            <TR className="hover:bg-transparent">
              <TH>Provider</TH><TH>SKU</TH><TH>Vùng</TH><TH>Cấu hình</TH>
              <TH><button className="inline-flex items-center gap-1 hover:text-foreground" onClick={() => setSortAsc((x) => !x)}>$/vCPU-giờ <ArrowDownUp className="size-3" /></button></TH>
              <TH>$/giờ</TH><TH>$/tháng</TH><TH className="hidden lg:table-cell">Tính tối thiểu</TH><TH>Độ tin cậy</TH>
            </TR>
          </THead>
          <TBody>
            {sortedOffers.map((o, i) => (
              <TR key={`${o.provider}-${o.sku}-${o.region}`}>
                <TD className="whitespace-nowrap"><Mono>{o.provider}</Mono>{i === 0 && sortAsc && <TrendingDown className="ml-1 inline size-3.5 text-st-running" />}</TD>
                <TD><Mono>{o.sku}</Mono></TD>
                <TD><Mono>{o.region}</Mono></TD>
                <TD className="whitespace-nowrap"><Mono className="text-muted-foreground">{o.cpu}c · {mb(o.ram_mb)} · {o.disk_gb}G</Mono></TD>
                <TD className="font-mono text-[13px] font-medium">{usd(o.price_hour / o.cpu, 5)}</TD>
                <TD className="font-mono text-[13px]">{usd(o.price_hour)}</TD>
                <TD className="font-mono text-[13px]">{usd(o.price_month, 2)}</TD>
                <TD className="hidden text-[13px] text-muted-foreground lg:table-cell">{o.min_billing_minutes >= 43200 ? "1 tháng" : `${o.min_billing_minutes} phút`}</TD>
                <TD><ConfidenceBadge c={o.confidence} /></TD>
              </TR>
            ))}
          </TBody>
        </Table>
      </Card>
      <p className="mt-2 flex items-center gap-1.5 text-xs text-muted-foreground"><CheckCircle2 className="size-3.5" /> Offer “Chưa kiểm chứng” luôn được self-test trước khi Scheduler dùng thật.</p>
    </>
  );
}
