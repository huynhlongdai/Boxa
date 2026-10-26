"use client";
// Đoạn cấu hình theo transport: MCP (HTTP | stdio), REST, SDK Python, SDK TS, CLI, function-calling.
// Lấy từ GET /agents/:id/snippet?kind=&lang=<transport>; Core cũ không phân biệt transport → sinh tại chỗ (lib/snippets).
import * as React from "react";
import { useTranslations } from "next-intl";
import { api, paths } from "@/lib/api";
import { useApi } from "@/lib/hooks";
import { FALLBACK_KINDS, kindInfo } from "@/lib/agent-kinds";
import { KEY_PLACEHOLDER, makeSnippet } from "@/lib/snippets";
import type { AgentKind, AgentKindInfo, List, Snippet, Transport } from "@/lib/types";
import { cn } from "@/lib/utils";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Select } from "@/components/ui/input";
import { CodeBlock } from "./code-block";
import { toastError } from "@/lib/error-toast";

const PUBLIC_API = process.env.NEXT_PUBLIC_BOXA_API || "http://localhost:8080/v1";

/** Danh sách kind: GET /agent-kinds, rơi về danh sách dự phòng khi Core chưa có. */
export function useAgentKinds(): { kinds: AgentKindInfo[]; fromCore: boolean; loading: boolean } {
  const { data, error, isLoading } = useApi<List<AgentKindInfo>>(paths.agentKinds, { shouldRetryOnError: false, revalidateOnFocus: false, refreshInterval: 0 });
  if (data?.items?.length) return { kinds: data.items, fromCore: true, loading: false };
  return { kinds: FALLBACK_KINDS, fromCore: false, loading: isLoading && !error };
}

const TABS = ["mcp", "rest", "sdk-python", "sdk-ts", "cli", "function-calling"] as const;
type Tab = (typeof TABS)[number];

export function SnippetViewer({ agentId, agentName, kind: initialKind, apiKey, kinds }: { agentId: string; agentName: string; kind: AgentKind; apiKey?: string; kinds: AgentKindInfo[] }) {
  const t = useTranslations("kinds");
  const tw = useTranslations("wizard");
  const [kind, setKind] = React.useState<AgentKind>(initialKind);
  const info = kindInfo(kind, kinds);
  const firstTab: Tab = info.transports[0]?.startsWith("mcp") ? "mcp" : ((info.transports[0] as Tab) ?? "rest");
  const [tab, setTab] = React.useState<Tab>(firstTab);
  const [mcpVariant, setMcpVariant] = React.useState<"mcp-http" | "mcp-stdio">(info.transports.includes("mcp-http") || !info.transports.includes("mcp-stdio") ? "mcp-http" : "mcp-stdio");
  const transport: Transport = tab === "mcp" ? mcpVariant : tab;
  const native = info.transports.includes(transport);
  const [snippet, setSnippet] = React.useState<Snippet | null>(null);
  const primary = info.transports[0];

  React.useEffect(() => {
    let off = false;
    setSnippet(null);
    api
      .snippet(agentId, kind, transport)
      .then((s) => {
        if (off) return;
        // Core chưa hỗ trợ biến thể transport (không trả `transport`) → dùng bản sinh tại chỗ cho tab khác tab chính
        if (!s.transport && transport !== primary) setSnippet(makeSnippet(kind, agentName, PUBLIC_API, transport));
        else setSnippet(s);
      })
      .catch((e) => {
        if (off) return;
        toastError(e);
        setSnippet(makeSnippet(kind, agentName, PUBLIC_API, transport));
      });
    return () => {
      off = true;
    };
  }, [agentId, agentName, kind, transport, primary]);

  const code = snippet ? (apiKey ? snippet.snippet.split(KEY_PLACEHOLDER).join(apiKey) : snippet.snippet) : "";

  return (
    <div className="flex flex-col gap-2">
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-[12.5px] text-muted-foreground">{tw("snippetFor")}</span>
        <Select value={kind} onChange={(e) => { const k = e.target.value; setKind(k); const ki = kindInfo(k, kinds); setTab(ki.transports[0]?.startsWith("mcp") ? "mcp" : ((ki.transports[0] as Tab) ?? "rest")); }} className="h-8 w-auto min-w-0 max-w-full text-[13px]" aria-label={tw("snippetFor")}>
          {kinds.map((k) => <option key={k.id} value={k.id}>{k.name}</option>)}
        </Select>
        {info.doc_url && <a href={info.doc_url} target="_blank" rel="noreferrer" className="text-[12.5px] text-primary hover:underline">{t("docs")} ↗</a>}
      </div>
      <div className="no-scrollbar -mx-1 flex gap-1 overflow-x-auto px-1" role="tablist">
        {TABS.map((tb) => {
          const supported = tb === "mcp" ? info.transports.some((x) => x.startsWith("mcp")) : info.transports.includes(tb);
          return (
            <Button key={tb} role="tab" aria-selected={tab === tb} size="xs" variant={tab === tb ? "secondary" : "ghost"} className={cn("shrink-0", !supported && "text-muted-foreground")} onClick={() => setTab(tb)}>
              {t(`transport.${tb}`)}
              {supported && <span className="size-1.5 rounded-full bg-st-running" aria-hidden />}
            </Button>
          );
        })}
      </div>
      {tab === "mcp" && (
        <div className="flex items-center gap-1 text-[12px]">
          <span className="mr-1 text-muted-foreground">{t("mcpVariant")}</span>
          {(["mcp-http", "mcp-stdio"] as const).map((v) => (
            <Button key={v} size="xs" variant={mcpVariant === v ? "outline" : "ghost"} className="h-6 font-mono text-[11.5px]" onClick={() => setMcpVariant(v)}>{v}</Button>
          ))}
        </div>
      )}
      <div className="flex flex-wrap items-center gap-2">
        <Badge variant={native ? "green" : "outline"}>{native ? t("supported") : t("generic")}</Badge>
        {!native && <span className="text-[12px] text-muted-foreground">{t("genericHint", { kind: info.name })}</span>}
      </div>
      {snippet ? <CodeBlock code={code} language={snippet.language} /> : <div className="h-40 animate-pulse rounded-md bg-surface-2" />}
    </div>
  );
}
