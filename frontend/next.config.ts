import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  output: "export",
  trailingSlash: true,
  poweredByHeader: false,
  ...(process.env.NODE_ENV === "development" ? {
    async rewrites() {
      const backend = process.env.API_PROXY_TARGET || "http://127.0.0.1:8080";
      return [
      { source: "/api/:path*", destination: `${backend}/api/:path*` },
      { source: "/messages", destination: `${backend}/messages` },
      ];
    },
  } : {}),
};

export default nextConfig;
