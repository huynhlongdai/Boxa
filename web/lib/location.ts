"use client";
// v2.0: nhãn vị trí box (vùng · quốc gia · thành phố · provider/site) theo ngôn ngữ đang dùng.
import { useLocale, useTranslations } from "next-intl";
import type { BoxLocation } from "./types";

export const REGION_ORDER = ["eu", "us", "apac", "sandbox", "local", "other"];

export function regionRank(r: string) {
  const i = REGION_ORDER.indexOf(r);
  return i < 0 ? REGION_ORDER.length : i;
}

const PROVIDER_NAME: Record<string, string> = {
  hetzner: "Hetzner",
  vultr: "Vultr",
  digitalocean: "DigitalOcean",
  contabo: "Contabo",
  netcup: "netcup",
  local: "Local",
  e2b: "E2B",
  daytona: "Daytona",
  modal: "Modal",
};

export function providerName(p: string) {
  return PROVIDER_NAME[p] ?? (p ? p.charAt(0).toUpperCase() + p.slice(1) : "?");
}

/** "hetzner-fsn" → "FSN" */
export function siteCode(loc: Pick<BoxLocation, "site" | "provider">) {
  const pre = `${loc.provider}-`;
  return (loc.site.startsWith(pre) ? loc.site.slice(pre.length) : loc.site).toUpperCase();
}

export function useLocationFmt() {
  const t = useTranslations("location");
  const locale = useLocale();
  let dn: Intl.DisplayNames | null = null;
  try {
    dn = new Intl.DisplayNames([locale], { type: "region" });
  } catch {
    dn = null;
  }
  const region = (r: string) => (REGION_ORDER.includes(r) ? t(`regions.${r}` as "regions.eu") : r.toUpperCase());
  const country = (c?: string) => (c ? (dn?.of(c.toUpperCase()) ?? c) : "");
  /** "Đức · Falkenstein (Hetzner FSN)" */
  const place = (loc: BoxLocation) => {
    const where = [country(loc.country), loc.city].filter(Boolean).join(" · ");
    const prov = `${providerName(loc.provider)}${siteCode(loc) && siteCode(loc) !== loc.provider.toUpperCase() ? ` ${siteCode(loc)}` : ""}`;
    return where ? `${where} (${prov})` : prov;
  };
  /** Nhãn ngắn cho chip: "Falkenstein" / "Singapore" / "E2B". */
  const short = (loc: BoxLocation) => loc.city || country(loc.country) || `${providerName(loc.provider)} ${siteCode(loc)}`.trim();
  return { region, country, place, short };
}
