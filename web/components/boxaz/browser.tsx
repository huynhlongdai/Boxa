"use client";
// v1.11 tab "Trình duyệt": Chromium trong box qua CDP. GET /boxes/{id}/browser (không đánh thức box),
// POST lấy endpoint CDP có ticket (dùng lại được tới hết hạn, URL chỉ trả một lần), DELETE tắt browser.
// Snippet Playwright (Node / Python), puppeteer, SDK, curl /json/version và CLI; khi chưa lấy URL thì snippet
// dùng SDK / CLI để tự lấy (API key không bao giờ nằm trong URL).
import * as React from "react";
import { useTranslations } from "next-intl";
import { Globe, Code2, Link2, Play, Square } from "lucide-react";
import { api, paths } from "@/lib/api";
import { useAction, useApi } from "@/lib/hooks";
import { useFmt } from "@/lib/format";
import type { Box, BrowserSession, BrowserState } from "@/lib/types";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Field, Select } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { CodeBlock, CopyButton } from "@/components/boxaz/code-block";
import { ConfirmButton } from "@/components/boxaz/confirm";
import { ErrorBox, LoadingRows } from "@/components/boxaz/page";
import { Dot, Mono } from "@/components/boxaz/status";

const TTLS = [900, 3600, 4 * 3600, 86400] as const;
const LANGS = ["playwright", "playwrightPy", "puppeteer", "sdkPy", "sdkTs", "curl", "cli"] as const;
type Lang = (typeof LANGS)[number];

export function cdpSnippet(lang: Lang, name: string, url: string | null, httpUrl: string | null): { code: string; language: string } {
  const u = url ?? "<cdp_url>";
  switch (lang) {
    case "playwright":
      return { language: "node · playwright", code: `import { chromium } from "playwright";\n\nconst browser = await chromium.connectOverCDP(${JSON.stringify(u)});\nconst ctx = browser.contexts()[0] ?? (await browser.newContext());\nconst page = ctx.pages()[0] ?? (await ctx.newPage());\nawait page.goto("https://example.com");\nconsole.log(await page.title());\nawait browser.close(); // disconnects; the browser in the box keeps running` };
    case "playwrightPy":
      return { language: "python · playwright", code: `from playwright.sync_api import sync_playwright\n\nwith sync_playwright() as p:\n    browser = p.chromium.connect_over_cdp(${JSON.stringify(u)})\n    ctx = browser.contexts[0] if browser.contexts else browser.new_context()\n    page = ctx.pages[0] if ctx.pages else ctx.new_page()\n    page.goto("https://example.com")\n    print(page.title())` };
    case "puppeteer":
      return { language: "node · puppeteer", code: `import puppeteer from "puppeteer-core";\n\nconst browser = await puppeteer.connect({ browserWSEndpoint: ${JSON.stringify(u)} });\nconst page = await browser.newPage();\nawait page.goto("https://example.com");\nconsole.log(await page.title());\nbrowser.disconnect();` };
    case "sdkPy":
      return { language: "python · boxaz", code: `from boxaz import Boxaz\nfrom playwright.sync_api import sync_playwright\n\nbx = Boxaz()  # BOXAZ_API_KEY\nwith sync_playwright() as p:\n    browser = bx.connect_playwright(p, ${JSON.stringify(name)})\n    page = browser.contexts[0].new_page()\n    page.goto("https://example.com")\n    print(page.title())` };
    case "sdkTs":
      return { language: "node · @boxaz/sdk", code: `import { Boxaz } from "@boxaz/sdk";\nimport { chromium } from "playwright";\n\nconst bx = new Boxaz(); // BOXAZ_API_KEY\nconst browser = await chromium.connectOverCDP(await bx.cdpUrl(${JSON.stringify(name)}));\nconst page = await browser.contexts()[0].newPage();\nawait page.goto("https://example.com");` };
    case "curl":
      return { language: "shell", code: `curl -s ${JSON.stringify((httpUrl ?? "<http_url>").replace("/.boxaz/cdp?", "/.boxaz/cdp/json/version?"))}` };
    case "cli":
      return { language: "shell", code: `box browser ${name}            # CDP URL (1 h)\nbox browser ${name} --ttl 4h --json\nbox browser ${name} --status\nbox browser ${name} --stop` };
  }
}

function StatusCard({ box, state, onSession }: { box: Box; state: BrowserState; onSession: (s: BrowserSession | null) => void }) {
  const t = useTranslations("browser");
  const { run, pending } = useAction();
  const [ttl, setTtl] = React.useState<number>(3600);
  const [headless, setHeadless] = React.useState(false);
  const revalidate = [paths.boxBrowser(box.id), paths.boxAccess(box.id), paths.boxTickets(box.id), paths.boxDesktop(box.id)];
  const ver = (state.version?.Browser as string | undefined) ?? "";
  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2"><Dot tone={state.running ? "green" : "gray"} pulse={state.clients > 0} /> {state.running ? t("running") : t("stopped")}</CardTitle>
        <CardDescription>{t("statusDesc", { idle: Math.round(state.idle_stop_s / 60) })}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3 text-[13px]" data-testid="browser-status">
        <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1.5">
          <dt className="text-muted-foreground">{t("version")}</dt><dd><Mono>{ver || "—"}</Mono></dd>
          <dt className="text-muted-foreground">{t("mode")}</dt><dd>{state.running ? (state.headless ? t("headless") : t("onDesktop")) : "—"}</dd>
          <dt className="text-muted-foreground">{t("binary")}</dt><dd><Mono className="break-all text-[12px]">{state.binary || "—"}</Mono></dd>
          <dt className="text-muted-foreground">{t("clients")}</dt><dd>{state.clients}</dd>
          <dt className="text-muted-foreground">{t("port")}</dt><dd><Mono>127.0.0.1:{state.port}</Mono></dd>
        </dl>
        {state.note && <p className="text-xs text-muted-foreground">{state.note}</p>}
        <div className="flex flex-wrap items-end gap-3">
          <Field label={t("ttl")} className="w-32">
            <Select value={String(ttl)} onChange={(e) => setTtl(Number(e.target.value))} data-testid="browser-ttl">
              {TTLS.map((s) => <option key={s} value={s}>{s >= 3600 ? t("hours", { n: s / 3600 }) : t("minutes", { n: s / 60 })}</option>)}
            </Select>
          </Field>
          <label className="flex h-9 items-center gap-2"><Switch checked={headless} onCheckedChange={setHeadless} /> {t("forceHeadless")}</label>
        </div>
        <div className="flex flex-wrap gap-2">
          <Button size="sm" disabled={!!pending} data-testid="browser-start"
            onClick={() => run("browser-open", () => api.openBrowser(box.id, { ttl_s: ttl, ...(headless ? { headless: true } : {}) }), {
              success: (r) => (r.started && r.start_ms ? t("startedIn", { ms: r.start_ms }) : t("urlReady")), revalidate,
            }).then((r) => r && onSession(r))}>
            <Play /> {state.running ? t("getUrl") : t("startGetUrl")}
          </Button>
          {state.running && (
            <ConfirmButton size="sm" variant="outline" destructive title={t("stopTitle")} desc={t("stopDesc")} confirmLabel={t("stop")} data-testid="browser-stop"
              onConfirm={() => run("browser-stop", () => api.stopBrowser(box.id), { success: t("stoppedToast"), revalidate }).then(() => onSession(null))}>
              <Square /> {t("stop")}
            </ConfirmButton>
          )}
        </div>
        <p className="text-xs text-muted-foreground">{t("firstStart")}</p>
      </CardContent>
    </Card>
  );
}

function EndpointCard({ sess }: { sess: BrowserSession }) {
  const t = useTranslations("browser");
  const f = useFmt();
  return (
    <Card className="border-primary/30" data-testid="browser-endpoint">
      <CardHeader>
        <CardTitle className="flex items-center gap-2"><Link2 className="size-4 text-primary" /> {t("endpoint")}</CardTitle>
        <CardDescription>{t("endpointDesc", { when: f.relTime(sess.expires_at) })}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-2.5">
        {[["cdpUrl", sess.cdp_url], ["httpUrl", sess.http_url ?? ""], ["coreUrl", sess.core_url]].filter(([, v]) => v).map(([k, v]) => (
          <div key={k} className="flex flex-col gap-1">
            <span className="text-xs text-muted-foreground">{t(k as "cdpUrl" | "httpUrl" | "coreUrl")}</span>
            <div className="flex items-center gap-2 rounded-md border border-border bg-background px-2.5 py-1.5">
              <code className="min-w-0 flex-1 overflow-x-auto whitespace-nowrap font-mono text-[12px]">{v}</code>
              <CopyButton text={v} />
            </div>
          </div>
        ))}
        <div className="flex flex-wrap gap-1.5 text-xs">
          <Badge variant="outline">{t("ticket", { id: sess.ticket_id })}</Badge>
          {sess.live_view && <Badge variant="blue">{t("liveView")}</Badge>}
        </div>
        <p className="text-xs text-muted-foreground">{t("endpointHint")}</p>
      </CardContent>
    </Card>
  );
}

function SnippetsCard({ box, sess }: { box: Box; sess: BrowserSession | null }) {
  const t = useTranslations("browser");
  const [lang, setLang] = React.useState<Lang>("playwright");
  const name = box.subdomain || box.slug;
  const s = cdpSnippet(lang, name, sess?.cdp_url ?? null, sess?.http_url ?? null);
  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2"><Code2 className="size-4 text-primary" /> {t("snippets")}</CardTitle>
        <CardDescription>{sess ? t("snippetsDescUrl") : t("snippetsDesc")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3" data-testid="browser-snippets">
        <div className="flex flex-wrap gap-1.5">
          {LANGS.map((l) => (
            <Button key={l} size="xs" variant={l === lang ? "default" : "outline"} onClick={() => setLang(l)} data-testid={`browser-snippet-${l}`}>{t(`lang.${l}`)}</Button>
          ))}
        </div>
        <CodeBlock code={s.code} language={s.language} maxHeight={300} />
      </CardContent>
    </Card>
  );
}

export function BrowserTab({ box }: { box: Box }) {
  const t = useTranslations("browser");
  const { data, error, mutate } = useApi<BrowserState>(paths.boxBrowser(box.id), { refreshInterval: 15000 });
  const [sess, setSess] = React.useState<BrowserSession | null>(null);
  if (error) return <ErrorBox error={error} retry={() => mutate()} />;
  if (!data) return <LoadingRows />;
  return (
    <div className="flex flex-col gap-4" data-testid="browser-panel">
      <p className="flex items-center gap-2 text-[13px] text-muted-foreground"><Globe className="size-4" /> {t("intro")}</p>
      <div className="grid grid-cols-1 items-start gap-4 lg:grid-cols-2 [&>*]:min-w-0">
        <div className="flex min-w-0 flex-col gap-4">
          <StatusCard box={box} state={data} onSession={setSess} />
          {sess && <EndpointCard sess={sess} />}
        </div>
        <SnippetsCard box={box} sess={sess} />
      </div>
    </div>
  );
}
