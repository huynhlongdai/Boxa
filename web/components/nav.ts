import { Bell, Bot, Boxes, Cable, CreditCard, Database, FileCode2, KeyRound, LayoutDashboard, Server, Settings, ShieldCheck, Wallet, Wand2, type LucideIcon } from "lucide-react";
import { PERMS, type Perm } from "@/lib/types";

/** Khoá dịch trong namespace "nav". */
export type NavKey =
  | "overview"
  | "approvals"
  | "boxes"
  | "recommend"
  | "agents"
  | "costs"
  | "billing"
  | "infra"
  | "templates"
  | "volumes"
  | "secrets"
  | "developers"
  | "settings"
  | "admin";

/** Khoá dịch nhóm sidebar (namespace "nav.groups"); null = không tiêu đề. */
export type NavGroupKey = "ops" | "resources" | "money" | "integrations" | "platform";

export interface NavItem {
  href: string;
  key: NavKey;
  icon: LucideIcon;
  mobileTab?: boolean;
  /** v2.0: chỉ hiện khi có quyền này trong workspace hiện tại. */
  perm?: Perm;
  /** v2.0: chỉ quản trị nền tảng. */
  platform?: boolean;
  /** Chưa có (P1): hiện mờ, không bấm được. */
  soon?: boolean;
}

export interface NavGroup {
  key: NavGroupKey | null;
  items: NavItem[];
  platform?: boolean;
}

// docs/boxmes-ui-spec-v2.md §1: Tổng quan · Cần anh — Vận hành — Tài nguyên — Tiền — Tích hợp — Cài đặt — Nền tảng (PA).
export const NAV_GROUPS: NavGroup[] = [
  {
    key: null,
    items: [
      { href: "/", key: "overview", icon: LayoutDashboard, mobileTab: true },
      { href: "/approvals", key: "approvals", icon: Bell, mobileTab: true },
    ],
  },
  {
    key: "ops",
    items: [
      { href: "/boxes", key: "boxes", icon: Boxes, mobileTab: true },
      { href: "/agents", key: "agents", icon: Bot },
      { href: "/templates", key: "templates", icon: FileCode2 },
      { href: "/recommend", key: "recommend", icon: Wand2 },
    ],
  },
  {
    key: "resources",
    items: [
      { href: "/volumes", key: "volumes", icon: Database, soon: true },
      { href: "/secrets", key: "secrets", icon: KeyRound, perm: PERMS.secretsRead },
    ],
  },
  {
    key: "money",
    items: [
      { href: "/costs", key: "costs", icon: Wallet, mobileTab: true },
      { href: "/settings/billing", key: "billing", icon: CreditCard, perm: PERMS.billing },
    ],
  },
  { key: "integrations", items: [{ href: "/developers", key: "developers", icon: Cable }] },
  { key: null, items: [{ href: "/settings", key: "settings", icon: Settings }] },
  {
    key: "platform",
    platform: true,
    items: [
      { href: "/infra", key: "infra", icon: Server, platform: true },
      { href: "/admin", key: "admin", icon: ShieldCheck, platform: true },
    ],
  },
];

/** Danh sách phẳng (bảng lệnh ⌘K, tab "Thêm" trên mobile). */
export const NAV: NavItem[] = NAV_GROUPS.flatMap((g) => g.items);

/** Mục nav người dùng được thấy (theo quyền + quản trị nền tảng). */
export function visibleNav(item: NavItem, can: (p: Perm) => boolean, platformAdmin: boolean) {
  if (item.platform && !platformAdmin) return false;
  if (item.perm && !can(item.perm)) return false;
  return true;
}

export function isActive(pathname: string, href: string) {
  if (href === "/settings") return pathname === "/settings" || (pathname.startsWith("/settings/") && !pathname.startsWith("/settings/billing"));
  return href === "/" ? pathname === "/" : pathname === href || pathname.startsWith(`${href}/`);
}
