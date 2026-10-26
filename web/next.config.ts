import type { NextConfig } from "next";
import createNextIntlPlugin from "next-intl/plugin";

const withNextIntl = createNextIntlPlugin("./i18n/request.ts");

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
