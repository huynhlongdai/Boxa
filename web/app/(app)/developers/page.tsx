"use client";
// "Cho agent": mọi thứ agent cần để tự kết nối Boxa (contract v1.1 — discovery, tool schemas, quick start, SDK, mã lỗi).
import * as React from "react";
import { useTranslations } from "next-intl";
import { Bot, Braces, Download, ExternalLink, Eye, EyeOff, FileText, KeyRound, Network, Plug, Terminal, TriangleAlert } from "lucide-react";
import { paths, PROXY_BASE } from "@/lib/api";
import { useApi } from "@/lib/hooks";
import { ERROR_CODES, type ToolFormat } from "@/lib/types";
import { SDK_PY, SDK_TS } from "@/lib/snippets";
import { cn } from "@/lib/utils";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, TBody, TD, TH, THead, TR } from "@/components/ui/table";
import { CodeBlock, CopyButton } from "@/components/boxa/code-block";
import { ErrorBox, LoadingRows, PageHeader } from "@/components/boxa/page";

const API = (process.env.NEXT_PUBLIC_BOXA_API || "http://localhost:8080/v1").replace(/\/+$/, "");
const ROOT = API.replace(/\/v1$/, "");
const FORMATS: ToolFormat[] = ["openai", "anthropic", "gemini", "mcp"];
const HTTP: Record<string, number> = { BUDGET_EXCEEDED: 402, NO_CAPACITY: 503, PROVIDER_FAILED: 503, BOX_SLEEPING: 409, TIMEOUT: 504, POLICY_DENIED: 403, NOT_FOUND: 404, UNAUTHORIZED: 401, INVALID: 400, RATE_LIMITED: 429, INTERNAL: 500 };
const mono = (c: React.ReactNode) => <code className="rounded bg-surface-2 px-1 font-mono text-[12px]">{c}</code>;

type ItemKey = "openapi" | "llms" | "llmsFull" | "mcp" | "a2a" | "mcpEndpoint";
interface Item {
  key: ItemKey;
  icon: React.ReactNode;
  publicUrl: string;
  proxy: string | null;
  language: string;
}

const ITEMS: Item[] = [
  { key: "openapi", icon: <Braces />, publicUrl: `${API}/openapi.json`, proxy: paths.openapi, language: "json" },
  { key: "llms", icon: <FileText />, publicUrl: `${ROOT}/llms.txt`, proxy: paths.llmsTxt, language: "markdown" },
  { key: "llmsFull", icon: <FileText />, publicUrl: `${ROOT}/llms-full.txt`, proxy: paths.llmsFullTxt, language: "markdown" },
  { key: "mcp", icon: <Plug />, publicUrl: `${ROOT}/.well-known/mcp.json`, proxy: paths.mcpCard, language: "json" },
  { key: "a2a", icon: <Network />, publicUrl: `${ROOT}/.well-known/agent.json`, proxy: paths.a2aCard, language: "json" },
  { key: "mcpEndpoint", icon: <Bot />, publicUrl: `${ROOT}/mcp`, proxy: null, language: "text" },
];

/** Tải văn bản thô qua proxy (không ném lỗi: trả status để hiện "chưa có"). */
function useRaw(path: string | null) {
  const [state, setState] = React.useState<{ text: string | null; status: number | null; loading: boolean }>({ text: null, status: null, loading: false });
  React.useEffect(() => {
    if (!path) return;
    let off = false;
    setState({ text: null, status: null, loading: true });
    fetch(`${PROXY_BASE}${path}`, { cache: "no-store" })
      .then(async (r) => {
        const body = await r.text();
        let text = body;
        if ((r.headers.get("content-type") ?? "").includes("json")) {
          try {
            text = JSON.stringify(JSON.parse(body), null, 2);
          } catch {
            /* giữ nguyên */
          }
        }
        if (!off) setState({ text: r.ok ? text : null, status: r.status, loading: false });
      })
      .catch(() => !off && setState({ text: null, status: 0, loading: false }));
    return () => {
      off = true;
    };
  }, [path]);
  return state;
}

function download(name: string, content: string, type: string) {
  const url = URL.createObjectURL(new Blob([content], { type }));
  const a = document.createElement("a");
  a.href = url;
  a.download = name;
  a.click();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}

function DiscoveryItem({ item }: { item: Item }) {
  const t = useTranslations("developers");
  const [open, setOpen] = React.useState(false);
  const raw = useRaw(open ? item.proxy : null);
  return (
    <Card className="flex min-w-0 flex-col p-4">
      <div className="flex items-start gap-3">
        <span className="mt-0.5 grid size-8 shrink-0 place-items-center rounded-md bg-primary/10 text-primary [&_svg]:size-4">{item.icon}</span>
        <div className="min-w-0 flex-1">
          <p className="flex flex-wrap items-center gap-2 font-medium">
            {t(`items.${item.key}`)}
            <Badge variant={item.key === "mcpEndpoint" ? "yellow" : "green"} className="text-[10.5px]">
              {item.key === "mcpEndpoint" ? <><KeyRound className="size-3" /> {t("needsKey")}</> : t("noAuth")}
            </Badge>
          </p>
          <p className="mt-0.5 text-[12.5px] text-muted-foreground">{t(`items.${item.key}Desc`)}</p>
        </div>
      </div>
      <code className="mt-3 block min-w-0 truncate rounded border border-border bg-background px-2 py-1.5 font-mono text-[12px]" title={item.publicUrl}>{item.publicUrl}</code>
      <div className="mt-2 flex flex-wrap gap-1.5">
        <CopyButton text={item.publicUrl} label={t("copyUrl")} />
        {item.proxy && (
          <Button size="xs" variant="ghost" onClick={() => setOpen((o) => !o)} aria-expanded={open}>
            {open ? <EyeOff /> : <Eye />} {open ? t("hidePreview") : t("preview")}
          </Button>
        )}
        {item.proxy && (
          <Button size="xs" variant="ghost" asChild>
            <a href={`${PROXY_BASE}${item.proxy}`} target="_blank" rel="noreferrer"><ExternalLink /> {t("openNew")}</a>
          </Button>
        )}
      </div>
      {open && (
        <div className="mt-3 min-w-0">
          {raw.loading ? <LoadingRows rows={2} /> : raw.text !== null ? (
            <CodeBlock code={raw.text} language={item.language} maxHeight={320} />
          ) : (
            <p className="flex items-center gap-1.5 text-[12.5px] text-st-warning"><TriangleAlert className="size-4 shrink-0" /> {t("unavailable", { status: raw.status ?? "—" })}</p>
          )}
        </div>
      )}
    </Card>
  );
}

function ToolSchemas() {
  const t = useTranslations("developers");
  const [format, setFormat] = React.useState<ToolFormat>("openai");
  const { data, error, isLoading } = useApi<{ format: string; tools: unknown[] }>(paths.tools(format), { refreshInterval: 0, revalidateOnFocus: false, shouldRetryOnError: false });
  const tools = data?.tools ?? [];
  // gemini gói tool trong functionDeclarations
  const flat = (format === "gemini" ? (tools[0] as { functionDeclarations?: unknown[] })?.functionDeclarations ?? [] : tools) as Record<string, unknown>[];
  const names = flat.map((x) => String(x.name ?? (x.function as { name?: string })?.name ?? "?"));
  const json = data ? JSON.stringify(data, null, 2) : "";
  return (
    <Card>
      <CardHeader className="gap-3">
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div className="min-w-0">
            <CardTitle className="flex items-center gap-2"><Braces className="size-4" /> {t("tools")}</CardTitle>
            <CardDescription className="mt-1">{t("toolsDesc")}</CardDescription>
          </div>
          <div className="flex items-center gap-1 rounded-md border border-border p-0.5" role="radiogroup" aria-label={t("format")}>
            {FORMATS.map((f) => (
              <Button key={f} size="xs" role="radio" aria-checked={format === f} variant={format === f ? "secondary" : "ghost"} className="font-mono" onClick={() => setFormat(f)}>{f}</Button>
            ))}
          </div>
        </div>
      </CardHeader>
      <CardContent className="flex min-w-0 flex-col gap-3">
        {error ? <ErrorBox error={error} /> : isLoading || !data ? <LoadingRows rows={3} /> : (
          <>
            <div className="flex flex-wrap items-center gap-1.5">
              <Badge variant="outline">{t("toolCount", { n: names.length })}</Badge>
              {names.map((n) => <Badge key={n} variant="outline" className="font-mono text-[11px]">{n}</Badge>)}
            </div>
            <div className="flex flex-wrap gap-2">
              <code className="min-w-0 flex-1 truncate rounded border border-border bg-background px-2 py-1.5 font-mono text-[12px]">GET {API}/tools?format={format}</code>
              <CopyButton text={`${API}/tools?format=${format}`} label={t("copyUrl")} />
              <Button size="xs" variant="secondary" onClick={() => download(`boxa-tools-${format}.json`, json, "application/json")}><Download /> {t("downloadJson")}</Button>
            </div>
            <CodeBlock code={json} language="json" maxHeight={380} />
          </>
        )}
      </CardContent>
    </Card>
  );
}

const QUICKSTART = `export BOXA_URL=${ROOT}
export BOXA_API_KEY=bx_live_…
H="Authorization: Bearer $BOXA_API_KEY"

# 1. Quote (dry-run, nothing is created)
curl -s -X POST $BOXA_URL/v1/quote -H "$H" -H "Content-Type: application/json" -H "Accept-Language: en" \\
  -d '{"template":"python","lifecycle":"task","spec":{"cpu":2,"ram_mb":2048,"disk_gb":20}}'

# 2. Create a box (safe to retry with the same Idempotency-Key)
curl -s -X POST $BOXA_URL/v1/boxes -H "$H" -H "Idempotency-Key: $(uuidgen)" -H "Content-Type: application/json" \\
  -d '{"template":"python","lifecycle":"task","spec":{"cpu":2,"ram_mb":2048,"disk_gb":20}}'

# 3. Run several commands in one call
curl -s -X POST $BOXA_URL/v1/boxes/BOX_ID/exec-batch -H "$H" -H "Content-Type: application/json" \\
  -d '{"commands":["python -V","pip install -q pytest","pytest -q"],"stop_on_error":true}'

# 4. Long job: async + long-poll
curl -s -X POST $BOXA_URL/v1/boxes/BOX_ID/exec -H "$H" -H "Content-Type: application/json" \\
  -d '{"command":"python train.py","async":true}'
curl -s "$BOXA_URL/v1/jobs/JOB_ID?wait=30" -H "$H"

# 5. Full output when truncated=true
curl -s $BOXA_URL/v1/logs/LOG_ID -H "$H"

# 6. Done
curl -s -X DELETE $BOXA_URL/v1/boxes/BOX_ID -H "$H"
`;

const SDK_LINES = `# Python
pip install ${SDK_PY}

# TypeScript / JavaScript
pnpm add ${SDK_TS}        # or: npm i ${SDK_TS}

# Environment for every SDK, the CLI and MCP stdio
export BOXA_URL=${ROOT}
export BOXA_API_KEY=bx_live_…`;

const CLI_LINES = `# box CLI — build from the boxa/core repo
cd boxa/core && go install ./cmd/box
box mcp            # MCP over stdio for local agents
box up --template python && box exec BOX_ID -- python -V`;

const ERROR_ENVELOPE = `{
  "error": {
    "code": "NO_CAPACITY",
    "message": "No capacity for 8 vCPU within shopee-crawler's policy",
    "hint": "Lower the spec or allow more providers; retry in 60 seconds.",
    "retry_after_s": 60,
    "docs_url": "https://boxa.dev/docs/errors#no_capacity"
  }
}`;

export default function DevelopersPage() {
  const t = useTranslations("developers");
  return (
    <>
      <PageHeader title={t("title")} desc={t("desc")} />

      <Card className="mb-4 p-4">
        <p className="text-xs text-muted-foreground">{t("baseUrl")}</p>
        <div className="mt-1 flex flex-wrap items-center gap-2">
          <code className="min-w-0 flex-1 truncate font-mono text-[13px]">{API}</code>
          <CopyButton text={API} label={t("copyUrl")} />
        </div>
      </Card>

      <section className="mb-6">
        <h2 className="mb-1 text-[15px] font-semibold">{t("discovery")}</h2>
        <p className="mb-3 max-w-3xl text-[13px] text-muted-foreground">{t("discoveryDesc")}</p>
        <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">
          {ITEMS.map((it) => <DiscoveryItem key={it.key} item={it} />)}
        </div>
      </section>

      <div className="mb-6"><ToolSchemas /></div>

      <div className="mb-6 grid gap-4 lg:grid-cols-5">
        <Card className="min-w-0 lg:col-span-3">
          <CardHeader>
            <CardTitle className="flex items-center gap-2"><Terminal className="size-4" /> {t("quickstart")}</CardTitle>
            <CardDescription>{t("quickstartDesc")}</CardDescription>
          </CardHeader>
          <CardContent><CodeBlock code={QUICKSTART} language="bash" maxHeight={460} /></CardContent>
        </Card>
        <Card className="min-w-0 lg:col-span-2">
          <CardHeader>
            <CardTitle className="flex items-center gap-2"><Download className="size-4" /> {t("sdk")}</CardTitle>
            <CardDescription>{t("sdkDesc")}</CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-3">
            <CodeBlock code={SDK_LINES} language="bash" maxHeight={220} />
            <p className="text-xs text-muted-foreground">{t("sdkNote")}</p>
            <p className="text-[13px] font-medium">{t("cliFromSource")}</p>
            <CodeBlock code={CLI_LINES} language="bash" maxHeight={160} />
          </CardContent>
        </Card>
      </div>

      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2"><TriangleAlert className="size-4" /> {t("errors")}</CardTitle>
          <CardDescription>{t.rich("errorsDesc", { mono })}</CardDescription>
        </CardHeader>
        <CardContent className="flex min-w-0 flex-col gap-4 px-0 sm:px-0">
          <Table>
            <THead>
              <TR className="hover:bg-transparent"><TH>{t("colCode")}</TH><TH>{t("colHttp")}</TH><TH>{t("colMeaning")}</TH><TH>{t("colHint")}</TH></TR>
            </THead>
            <TBody>
              {ERROR_CODES.map((c) => (
                <TR key={c}>
                  <TD className="whitespace-nowrap align-top"><code className={cn("font-mono text-[12px]", HTTP[c] >= 500 ? "text-st-error" : HTTP[c] === 402 || HTTP[c] === 429 ? "text-st-warning" : "text-foreground")}>{c}</code></TD>
                  <TD className="align-top font-mono text-[12.5px] text-muted-foreground">{HTTP[c]}</TD>
                  <TD className="min-w-[180px] align-top text-[13px]">{t(`errs.${c}`)}</TD>
                  <TD className="min-w-[220px] align-top text-[13px] text-muted-foreground">{t(`errs.${c}Hint`)}</TD>
                </TR>
              ))}
            </TBody>
          </Table>
          <div className="px-4">
            <p className="mb-2 text-[13px] font-medium">{t("envelope")}</p>
            <CodeBlock code={ERROR_ENVELOPE} language="json" maxHeight={220} />
          </div>
        </CardContent>
      </Card>
    </>
  );
}
