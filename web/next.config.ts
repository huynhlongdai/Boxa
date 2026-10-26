import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  output: "standalone",
  reactStrictMode: true,
  poweredByHeader: false,
  experimental: {
    // Giữ bộ nhớ build thấp (máy build nhỏ / CI 2–4 GB RAM)
    webpackMemoryOptimizations: true,
    cpus: 1,
  },
};

export default nextConfig;
