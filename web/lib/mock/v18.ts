// MOCK MODE v1.8/v1.9: gateway trên domain thật (URL dễ đọc <name>.<domain>, <name>-<label>.<domain>; alias <key>-<port>) + preview private của provider.
// Bám theo core/internal/manager/gatewaydomain.go (GET /v1/gateway).
import type { GatewayInfo, ProviderPreviewMode } from "../types";
import type { MockState } from "./seed";

type Res = { status: number; body: unknown };

export const MOCK_GATEWAY_DOMAIN = "boxmes.dev";
const RESERVED = ["admin", "api", "app", "assets", "auth", "autoconfig", "autodiscover", "blog", "cdn", "console", "core", "dashboard", "dl", "docs", "ftp", "git", "grafana", "gw", "hark", "help", "imap", "login", "mail", "mcp", "metrics", "mx", "ns", "ns1", "ns2", "pop", "pop3", "portal", "smtp", "sso", "static", "status", "support", "webmail", "ws", "www"];

const PROVIDERS: GatewayInfo["providers"] = [
  { provider: "daytona", preview_host: true, private_preview: false, note: "preview host is public at the provider: anyone with the sandbox id can reach a port directly, even when its Boxmes URL needs a token" },
  { provider: "e2b", preview_host: true, private_preview: true, note: "preview host refuses requests without the sandbox traffic token when the sandbox is created private; the gateway adds it upstream" },
  { provider: "novita", preview_host: true, private_preview: true, note: "preview host refuses requests without the sandbox traffic token when the sandbox is created private; the gateway adds it upstream" },
  { provider: "prized", preview_host: false, private_preview: false, note: "no provider preview host: traffic reaches the box only through the Boxmes gateway (boxd / ingress agent)" },
];

export function gatewayInfo(s: MockState, check: boolean): GatewayInfo {
  const g = s.settings.gateway ?? { domain: "", provider_preview: "" };
  const domain = g.domain || MOCK_GATEWAY_DOMAIN; // mock: như đặt BOXMES_GATEWAY_DOMAIN
  const mode = (g.provider_preview || "auto") as ProviderPreviewMode;
  return {
    mode: "host", domain, domain_source: g.domain ? "settings" : "env", public_url: `https://gw.${domain}`,
    url_pattern: `https://<name>.${domain}/`, label_pattern: `https://<name>-<label>.${domain}/`, alias_pattern: `https://<key>-<port>.${domain}/`, internet_facing: true, reserved: RESERVED,
    provider_preview: mode, provider_preview_source: g.provider_preview ? "settings" : "default", private_previews: mode !== "public",
    providers: PROVIDERS,
    ...(check ? { check: { ok: true, status: 200, latency_ms: 70 + Math.round(Math.random() * 40), checked_at: new Date().toISOString() } } : {}),
  };
}

export function handleV18(s: MockState, M: string, segs: string[], q: URLSearchParams): Res | null {
  if (segs[0] === "gateway" && !segs[1] && M === "GET") return { status: 200, body: gatewayInfo(s, q.get("check") === "1") };
  return null;
}
