import { Bot, Boxes, Cable, FileCode2, KeyRound, LayoutDashboard, Server, Settings, ShieldCheck, Wallet, Wand2, type LucideIcon } from "lucide-react";

/** Khoá dịch trong namespace "nav". */
export type NavKey = "overview" | "boxes" | "recommend" | "agents" | "costs" | "infra" | "templates" | "secrets" | "developers" | "settings" | "admin";

export interface NavItem {
  href: string;
  key: NavKey;
  icon: LucideIcon;
  mobileTab?: boolean;
}

export const NAV: NavItem[] = [
  { href: "/", key: "overview", icon: LayoutDashboard, mobileTab: true },
  { href: "/boxes", key: "boxes", icon: Boxes, mobileTab: true },
  { href: "/agents", key: "agents", icon: Bot, mobileTab: true },
  { href: "/costs", key: "costs", icon: Wallet, mobileTab: true },
  { href: "/recommend", key: "recommend", icon: Wand2 },
  { href: "/infra", key: "infra", icon: Server },
  { href: "/templates", key: "templates", icon: FileCode2 },
  { href: "/secrets", key: "secrets", icon: KeyRound },
  { href: "/developers", key: "developers", icon: Cable },
  { href: "/settings", key: "settings", icon: Settings },
  { href: "/admin", key: "admin", icon: ShieldCheck },
];

export function isActive(pathname: string, href: string) {
  return href === "/" ? pathname === "/" : pathname === href || pathname.startsWith(`${href}/`);
}
