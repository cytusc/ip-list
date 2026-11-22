import type { NextConfig } from "next";
import { PHASE_DEVELOPMENT_SERVER } from "next/constants";

export default function config(phase: string): NextConfig {
  const base: NextConfig = {
    devIndicators: false,
    output: "export",
    trailingSlash: true,
    images: { unoptimized: true },
  };
  // `next dev` keeps its own server only for hot reload; the browser still talks
  // to the Rust gateway, which owns /api and the static export in production.
  if (phase === PHASE_DEVELOPMENT_SERVER) {
    return {
      ...base,
      async rewrites() {
        return [{ source: "/api/:path*", destination: "http://127.0.0.1:8088/api/:path*" }];
      },
    };
  }
  return base;
}
