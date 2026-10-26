import { Bot, Boxes, FileCode2, KeyRound, LayoutDashboard, Server, Settings, ShieldCheck, Wallet, type LucideIcon } from "lucide-react";

export interface NavItem {
  href: string;
  label: string;
  icon: LucideIcon;
  mobileTab?: boolean;
}

export const NAV: NavItem[] = [
  { href: "/", label: "Tổng quan", icon: LayoutDashboard, mobileTab: true },
  { href: "/boxes", label: "Boxes", icon: Boxes, mobileTab: true },
  { href: "/agents", label: "Agents", icon: Bot, mobileTab: true },
  { href: "/costs", label: "Chi phí", icon: Wallet, mobileTab: true },
  { href: "/infra", label: "Hạ tầng", icon: Server },
  { href: "/templates", label: "Templates", icon: FileCode2 },
  { href: "/secrets", label: "Secrets", icon: KeyRound },
  { href: "/settings", label: "Cài đặt", icon: Settings },
  { href: "/admin", label: "Quản trị", icon: ShieldCheck },
];

export function isActive(pathname: string, href: string) {
  return href === "/" ? pathname === "/" : pathname === href || pathname.startsWith(`${href}/`);
}
