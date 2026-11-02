import type { NextConfig } from "next";

// The browser always talks to the API same-origin under /api. In development
// Next.js proxies it to the Go API; in production the reverse proxy (Caddy)
// routes /api/* to the API directly and this rewrite is unused.
const apiOrigin = process.env.OSRIVA_API_ORIGIN ?? "http://127.0.0.1:8080";

const securityHeaders = [
  { key: "X-Content-Type-Options", value: "nosniff" },
  { key: "X-Frame-Options", value: "DENY" },
  { key: "Referrer-Policy", value: "same-origin" },
  { key: "Permissions-Policy", value: "camera=(), microphone=(), geolocation=()" },
];

const config: NextConfig = {
  output: "standalone",
  poweredByHeader: false,
  reactStrictMode: true,
  async rewrites() {
    return [{ source: "/api/:path*", destination: `${apiOrigin}/api/:path*` }];
  },
  async headers() {
    return [{ source: "/:path*", headers: securityHeaders }];
  },
};

export default config;
