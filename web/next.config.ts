import type { NextConfig } from "next";
import createNextIntlPlugin from "next-intl/plugin";

const withNextIntl = createNextIntlPlugin("./i18n/request.ts");

// Compat (Boxa → Boxaz rename): legacy BOXA_* / NEXT_PUBLIC_BOXA_* env vars are
// copied onto the new BOXAZ_* names when those are unset (the new name wins).
// next.config runs at build (NEXT_PUBLIC_* are inlined) and at `next start`.
for (const [k, v] of Object.entries(process.env)) {
  const m = /^(NEXT_PUBLIC_)?BOXA_(.+)$/.exec(k);
  if (!m || v === undefined) continue;
  const nk = `${m[1] ?? ""}BOXAZ_${m[2]}`;
  if (process.env[nk] === undefined) {
    process.env[nk] = v;
    console.warn(`[boxaz] ${k} is deprecated; rename it to ${nk}`);
  }
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
