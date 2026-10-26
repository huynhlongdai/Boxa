import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";
import type { BoxStatus, Host, Lifecycle, Offer, Provider } from "@/lib/types";

type Tone = "green" | "gray" | "blue" | "yellow" | "red" | "outline" | "default";

const BOX: Record<BoxStatus, [string, Tone]> = {
  requested: ["Đã yêu cầu", "blue"],
  placing: ["Đang xếp chỗ", "blue"],
  provisioning: ["Đang tạo", "blue"],
  running: ["Đang chạy", "green"],
  sleeping: ["Đang ngủ", "gray"],
  snapshotting: ["Đang snapshot", "blue"],
  terminated: ["Đã xoá", "outline"],
  failed: ["Lỗi", "red"],
};

const DOT: Record<Tone, string> = {
  green: "bg-st-running",
  gray: "bg-st-sleeping",
  blue: "bg-st-provisioning",
  yellow: "bg-st-warning",
  red: "bg-st-error",
  outline: "bg-muted-foreground",
  default: "bg-muted-foreground",
};

export function Dot({ tone, pulse }: { tone: Tone; pulse?: boolean }) {
  return (
    <span className="relative inline-flex size-1.5">
      {pulse && <span className={cn("absolute inline-flex h-full w-full animate-ping rounded-full opacity-60", DOT[tone])} />}
      <span className={cn("relative inline-flex size-1.5 rounded-full", DOT[tone])} />
    </span>
  );
}

export function BoxStatusChip({ status, className }: { status: BoxStatus; className?: string }) {
  const [label, tone] = BOX[status] ?? [status, "default"];
  return (
    <Badge variant={tone} className={className}>
      <Dot tone={tone} pulse={["provisioning", "placing", "requested", "snapshotting"].includes(status)} />
      {label}
    </Badge>
  );
}

const HOST: Record<Host["status"], [string, Tone]> = {
  provisioning: ["Đang tạo", "blue"],
  ready: ["Sẵn sàng", "green"],
  draining: ["Đang drain", "yellow"],
  down: ["Mất kết nối", "red"],
};
export function HostStatusChip({ status }: { status: Host["status"] }) {
  const [label, tone] = HOST[status];
  return (
    <Badge variant={tone}>
      <Dot tone={tone} pulse={status === "provisioning"} />
      {label}
    </Badge>
  );
}

export function ProviderChip({ p }: { p: Provider }) {
  if (p.error) return <Badge variant="red"><Dot tone="red" />Lỗi</Badge>;
  if (p.connected) return <Badge variant="green"><Dot tone="green" />Đã kết nối</Badge>;
  return <Badge variant="gray"><Dot tone="gray" />Chưa kết nối</Badge>;
}

const CONF: Record<Offer["confidence"], [string, Tone, string]> = {
  verified: ["Đã kiểm chứng", "green", "Boxa đã tự kiểm thử offer này"],
  unverified: ["Chưa kiểm chứng", "yellow", "Lấy từ battleships, chưa self-test"],
  stock_limited: ["Hạn chế hàng", "blue", "Nhà cung cấp hay hết hàng ở vùng này"],
};
export function ConfidenceBadge({ c }: { c: Offer["confidence"] }) {
  const [label, tone, title] = CONF[c];
  return <Badge variant={tone} title={title}>{label}</Badge>;
}

const LC: Record<Lifecycle, string> = { task: "task", session: "session", service: "service" };
export function LifecycleBadge({ lc }: { lc: Lifecycle }) {
  return <Badge variant="outline" className="font-mono">{LC[lc]}</Badge>;
}

export function Mono({ children, className }: { children: React.ReactNode; className?: string }) {
  return <span className={cn("font-mono text-[12.5px]", className)}>{children}</span>;
}

export const BOX_STATUS_LABEL = Object.fromEntries(Object.entries(BOX).map(([k, v]) => [k, v[0]])) as Record<BoxStatus, string>;
