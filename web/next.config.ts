import type { NextConfig } from "next";
import createNextIntlPlugin from "next-intl/plugin";

const withNextIntl = createNextIntlPlugin("./i18n/request.ts");

// Compat (Boxa → Boxaz → Boxmes renames): legacy BOXAZ_* / BOXA_* (and their
// NEXT_PUBLIC_ twins) env vars are copied onto the new BOXMES_* names when those
// are unset. Priority: BOXMES_* → BOXAZ_* → BOXA_* (the newest name wins).
// next.config runs at build (NEXT_PUBLIC_* are inlined) and at `next start`.
const LEGACY_RANK: Record<string, number> = { BOXAZ: 0, BOXA: 1 };
const legacyEnv = new Map<string, { key: string; value: string; rank: number }>();
for (const [k, v] of Object.entries(process.env)) {
  const m = /^(NEXT_PUBLIC_)?(BOXAZ|BOXA)_(.+)$/.exec(k);
  if (!m || v === undefined) continue;
  const nk = `${m[1] ?? ""}BOXMES_${m[3]}`;
  if (process.env[nk] !== undefined) continue; // the new name wins
  const rank = LEGACY_RANK[m[2]];
  const cur = legacyEnv.get(nk);
  if (!cur || rank < cur.rank) legacyEnv.set(nk, { key: k, value: v, rank });
}
for (const [nk, { key, value }] of legacyEnv) {
  process.env[nk] = value;
  console.warn(`[boxmes] ${key} is deprecated; rename it to ${nk}`);
}

const nextConfig: NextConfig = {
  output: "standalone",
  reactStrictMode: true,
  poweredByHeader: false,
  experimental: {
    // Giữ bộ nhớ build thấp (máy build nhỏ / CI 2–4 GB RAM): một worker, tối ưu bộ nhớ webpack
    webpackMemoryOptimizations: true,
    cpus: 1,
    workerThreads: false,
  },
};

export default withNextIntl(nextConfig);
