"use client";
// Tạo agent 3 bước: tên + kind → ngân sách + chính sách → API key (hiện 1 lần) + đoạn cấu hình sinh sẵn.
import * as React from "react";
import { AlertTriangle, ArrowLeft, ArrowRight, Check } from "lucide-react";
import { useSWRConfig } from "swr";
import { toast } from "sonner";
import { api, errMsg, paths } from "@/lib/api";
import type { Agent, AgentKind, ApiKey, Snippet } from "@/lib/types";
import { KEY_PLACEHOLDER } from "@/lib/mock/snippets";
import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { Field, Input, Textarea } from "@/components/ui/input";
import { CodeBlock, CopyButton } from "./code-block";

export const KINDS: { id: AgentKind; label: string; desc: string }[] = [
  { id: "claude-code", label: "Claude Code", desc: "MCP server qua HTTP — dán vào .mcp.json" },
  { id: "openai-agents", label: "OpenAI Agents SDK", desc: "Python SDK, MCPServerStreamableHttp" },
  { id: "n8n", label: "n8n", desc: "Node HTTP Request gọi REST" },
  { id: "hark", label: "Hark", desc: "Thêm MCP server trong Hark" },
  { id: "custom", label: "Tuỳ chỉnh", desc: "REST/cURL, CLI hoặc SDK bất kỳ" },
];
const PROVIDERS = ["hetzner", "vultr", "contabo", "digitalocean", "netcup"];
const REGIONS = ["fsn1", "nbg1", "hel1", "sgp", "eu-de", "sin"];

function Chip({ on, children, onClick }: { on: boolean; children: React.ReactNode; onClick: () => void }) {
  return (
    <button type="button" onClick={onClick} className={cn("rounded-full border px-3 py-1 font-mono text-[12.5px] transition-colors", on ? "border-primary/60 bg-primary/15 text-primary" : "border-border text-muted-foreground hover:text-foreground")}>
      {on && <Check className="-ml-0.5 mr-1 inline size-3" />}
      {children}
    </button>
  );
}

function Steps({ step }: { step: number }) {
  const labels = ["Tên & loại", "Ngân sách & chính sách", "Key & cấu hình"];
  return (
    <ol className="mb-5 flex items-center gap-2 text-[12.5px]">
      {labels.map((l, i) => (
        <li key={l} className="flex flex-1 items-center gap-2">
          <span className={cn("grid size-6 shrink-0 place-items-center rounded-full border text-[11px] font-semibold", i < step ? "border-st-running bg-st-running text-black" : i === step ? "border-primary text-primary" : "border-border text-muted-foreground")}>
            {i < step ? <Check className="size-3.5" /> : i + 1}
          </span>
          <span className={cn("hidden sm:inline", i === step ? "text-foreground" : "text-muted-foreground")}>{l}</span>
          {i < 2 && <span className="h-px flex-1 bg-border" />}
        </li>
      ))}
    </ol>
  );
}

export interface WizardResult {
  agent: Agent;
  key: ApiKey;
}

export function AgentWizard({ onDone, defaultName = "", compact }: { onDone?: (r: WizardResult) => void; defaultName?: string; compact?: boolean }) {
  const [step, setStep] = React.useState(0);
  const [name, setName] = React.useState(defaultName);
  const [kind, setKind] = React.useState<AgentKind>("claude-code");
  const [day, setDay] = React.useState("0.50");
  const [month, setMonth] = React.useState("10");
  const [providers, setProviders] = React.useState<string[]>(["hetzner", "vultr"]);
  const [regions, setRegions] = React.useState<string[]>([]);
  const [egress, setEgress] = React.useState("");
  const [busy, setBusy] = React.useState(false);
  const [result, setResult] = React.useState<WizardResult | null>(null);
  const [snippetKind, setSnippetKind] = React.useState<AgentKind>("claude-code");
  const [snippet, setSnippet] = React.useState<Snippet | null>(null);
  const { mutate } = useSWRConfig();

  const nameOk = /^[a-z0-9][a-z0-9-]{1,38}$/.test(name);

  React.useEffect(() => {
    if (!result) return;
    api.snippet(result.agent.id, snippetKind).then(setSnippet).catch((e) => toast.error(errMsg(e)));
  }, [result, snippetKind]);

  const create = async () => {
    setBusy(true);
    try {
      const agent = await api.createAgent({
        name,
        kind,
        budget_day_usd: Number(day),
        budget_month_usd: Number(month),
        policy: { providers, regions, egress_allowlist: egress.trim() ? egress.split(/[\s,]+/).filter(Boolean) : null },
      });
      const key = await api.createKey({ name: `${name}-default`, agent_id: agent.id, scopes: ["boxes:write", "exec", "files", "snapshots"] });
      setResult({ agent, key });
      setSnippetKind(kind);
      setStep(2);
      mutate(paths.agents);
      mutate(paths.keys);
      onDone?.({ agent, key });
    } catch (e) {
      toast.error(errMsg(e));
    } finally {
      setBusy(false);
    }
  };

  const filled = snippet && result?.key.key ? snippet.snippet.split(KEY_PLACEHOLDER).join(result.key.key) : snippet?.snippet ?? "";

  return (
    <div>
      <Steps step={step} />
      {step === 0 && (
        <div className="flex flex-col gap-4">
          <Field label="Tên agent" hint="Chữ thường, số, dấu gạch ngang. Ví dụ: shopee-crawler">
            <Input value={name} autoFocus onChange={(e) => setName(e.target.value.toLowerCase().replace(/\s+/g, "-"))} placeholder="vd: kecon-writer" className="font-mono" />
          </Field>
          <div className="flex flex-col gap-1.5">
            <span className="text-[13px] font-medium">Loại agent (kind)</span>
            <div className={cn("grid gap-2", compact ? "sm:grid-cols-2" : "sm:grid-cols-2 lg:grid-cols-3")}>
              {KINDS.map((k) => (
                <button key={k.id} type="button" onClick={() => setKind(k.id)} className={cn("rounded-lg border p-3 text-left transition-colors", kind === k.id ? "border-primary/60 bg-primary/10" : "border-border hover:bg-surface-2/50")}>
                  <p className="text-sm font-medium">{k.label}</p>
                  <p className="font-mono text-[11px] text-muted-foreground">{k.id}</p>
                  <p className="mt-1 text-[12px] text-muted-foreground">{k.desc}</p>
                </button>
              ))}
            </div>
          </div>
          <div className="flex justify-end">
            <Button disabled={!nameOk} onClick={() => setStep(1)}>Tiếp <ArrowRight /></Button>
          </div>
        </div>
      )}
      {step === 1 && (
        <div className="flex flex-col gap-4">
          <div className="grid gap-3 sm:grid-cols-2">
            <Field label="Ngân sách ngày (USD)" hint="Vượt 80% sẽ vào hộp “Cần anh”">
              <Input type="number" step="0.05" min="0" value={day} onChange={(e) => setDay(e.target.value)} className="font-mono" />
            </Field>
            <Field label="Ngân sách tháng (USD)">
              <Input type="number" step="1" min="0" value={month} onChange={(e) => setMonth(e.target.value)} className="font-mono" />
            </Field>
          </div>
          <div className="flex flex-col gap-1.5">
            <span className="text-[13px] font-medium">Provider được phép</span>
            <div className="flex flex-wrap gap-1.5">
              {PROVIDERS.map((p) => <Chip key={p} on={providers.includes(p)} onClick={() => setProviders((x) => (x.includes(p) ? x.filter((y) => y !== p) : [...x, p]))}>{p}</Chip>)}
            </div>
          </div>
          <div className="flex flex-col gap-1.5">
            <span className="text-[13px] font-medium">Vùng được phép <span className="font-normal text-muted-foreground">(bỏ trống = mọi vùng)</span></span>
            <div className="flex flex-wrap gap-1.5">
              {REGIONS.map((r) => <Chip key={r} on={regions.includes(r)} onClick={() => setRegions((x) => (x.includes(r) ? x.filter((y) => y !== r) : [...x, r]))}>{r}</Chip>)}
            </div>
          </div>
          <Field label="Egress allowlist" hint="Mỗi dòng một domain, hỗ trợ *. Bỏ trống = cho phép mọi đích.">
            <Textarea value={egress} onChange={(e) => setEgress(e.target.value)} placeholder={"*.shopee.vn\napi.openai.com"} className="font-mono" rows={3} />
          </Field>
          <div className="flex justify-between">
            <Button variant="ghost" onClick={() => setStep(0)}><ArrowLeft /> Quay lại</Button>
            <Button disabled={busy || !providers.length || !(Number(day) >= 0) || !(Number(month) >= 0)} onClick={create}>{busy ? "Đang tạo…" : "Tạo agent & key"} <ArrowRight /></Button>
          </div>
        </div>
      )}
      {step === 2 && result && (
        <div className="flex flex-col gap-4">
          <div className="rounded-lg border border-st-warning/40 bg-st-warning/5 p-3">
            <p className="flex items-center gap-1.5 text-[13px] font-medium text-st-warning"><AlertTriangle className="size-4" /> API key chỉ hiện một lần</p>
            <p className="mt-1 text-[12.5px] text-muted-foreground">Copy và cất ngay. Mất key thì thu hồi và tạo key mới ở trang agent.</p>
            <div className="mt-2 flex items-center gap-2">
              <code className="min-w-0 flex-1 truncate rounded border border-border bg-background px-2 py-1.5 font-mono text-[13px]">{result.key.key ?? `${result.key.prefix}…`}</code>
              <CopyButton text={result.key.key ?? ""} label="Copy key" />
            </div>
          </div>
          <div className="flex flex-col gap-2">
            <span className="text-[13px] font-medium">1. Đặt biến môi trường trên máy chạy agent</span>
            <CodeBlock code={`export BOXA_API_KEY=${result.key.key ?? "<key>"}\nexport BOXA_URL=${(process.env.NEXT_PUBLIC_BOXA_API || "http://localhost:8080/v1").replace(/\/v1\/?$/, "")}`} language="bash" maxHeight={120} />
          </div>
          <div className="flex flex-col gap-2">
            <span className="text-[13px] font-medium">2. Dán đoạn cấu hình sinh sẵn</span>
            <div className="no-scrollbar flex gap-1 overflow-x-auto">
              {KINDS.map((k) => (
                <Button key={k.id} size="xs" variant={snippetKind === k.id ? "secondary" : "ghost"} onClick={() => setSnippetKind(k.id)}>{k.label}</Button>
              ))}
            </div>
            {snippet ? <CodeBlock code={filled} language={snippet.language} /> : <div className="h-40 animate-pulse rounded-md bg-surface-2" />}
          </div>
        </div>
      )}
    </div>
  );
}
