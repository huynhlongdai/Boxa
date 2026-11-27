// Danh sách kind dự phòng (khớp GET /agent-kinds của contract v1.1) — dùng khi Core cũ chưa có endpoint, và cho MOCK MODE.
import type { AgentKindCategory, AgentKindInfo, Transport } from "./types";

export const CATEGORIES: AgentKindCategory[] = ["coding", "framework", "workflow", "platform", "custom"];
export const TRANSPORTS: Transport[] = ["mcp-http", "mcp-stdio", "rest", "sdk-python", "sdk-ts", "cli", "function-calling"];

const k = (id: string, name: string, category: AgentKindCategory, transports: Transport[], doc_url: string | null = null): AgentKindInfo => ({ id, name, category, transports, doc_url });

export const FALLBACK_KINDS: AgentKindInfo[] = [
  // Đồng bộ với boxmes/core internal/agentkit/kinds.go (thứ tự hiển thị)
  k("claude-code", "Claude Code", "coding", ["mcp-http", "mcp-stdio", "cli"], "https://docs.anthropic.com/en/docs/claude-code/mcp"),
  k("codex-cli", "OpenAI Codex CLI", "coding", ["mcp-stdio", "mcp-http", "cli"], "https://github.com/openai/codex/blob/main/docs/config.md"),
  k("gemini-cli", "Gemini CLI", "coding", ["mcp-http", "mcp-stdio", "cli"], "https://github.com/google-gemini/gemini-cli/blob/main/docs/tools/mcp-server.md"),
  k("cursor", "Cursor", "coding", ["mcp-http", "mcp-stdio"], "https://docs.cursor.com/context/model-context-protocol"),
  k("windsurf", "Windsurf", "coding", ["mcp-http", "mcp-stdio"], "https://docs.windsurf.com/windsurf/cascade/mcp"),
  k("cline", "Cline", "coding", ["mcp-http", "mcp-stdio"], "https://docs.cline.bot/mcp/configuring-mcp-servers"),
  k("openai-agents", "OpenAI Agents SDK", "framework", ["mcp-http", "sdk-python", "function-calling"], "https://openai.github.io/openai-agents-python/mcp/"),
  k("langchain", "LangChain", "framework", ["mcp-http", "sdk-python", "function-calling"], "https://github.com/langchain-ai/langchain-mcp-adapters"),
  k("langgraph", "LangGraph", "framework", ["mcp-http", "sdk-python", "function-calling"], "https://langchain-ai.github.io/langgraph/agents/mcp/"),
  k("crewai", "CrewAI", "framework", ["mcp-http", "sdk-python"], "https://docs.crewai.com/en/mcp/overview"),
  k("autogen", "AutoGen", "framework", ["mcp-http", "sdk-python"], "https://microsoft.github.io/autogen/stable/reference/python/autogen_ext.tools.mcp.html"),
  k("vercel-ai-sdk", "Vercel AI SDK", "framework", ["mcp-http", "sdk-ts", "function-calling"], "https://ai-sdk.dev/docs/ai-sdk-core/mcp-tools"),
  k("mastra", "Mastra", "framework", ["mcp-http", "sdk-ts"], "https://mastra.ai/docs/tools-mcp/mcp-overview"),
  k("pydantic-ai", "Pydantic AI", "framework", ["mcp-http", "sdk-python"], "https://ai.pydantic.dev/mcp/client/"),
  k("llamaindex", "LlamaIndex", "framework", ["mcp-http", "sdk-python"], "https://docs.llamaindex.ai/en/stable/api_reference/tools/mcp/"),
  k("n8n", "n8n", "workflow", ["mcp-http", "rest"], "https://docs.n8n.io/integrations/builtin/cluster-nodes/sub-nodes/n8n-nodes-langchain.toolmcp/"),
  k("dify", "Dify", "workflow", ["mcp-http", "rest"], "https://docs.dify.ai/en/guides/tools/mcp"),
  k("flowise", "Flowise", "workflow", ["mcp-http", "rest"], "https://docs.flowiseai.com/integrations/langchain/tools/mcp"),
  k("make", "Make", "workflow", ["rest"], "https://apps.make.com/http"),
  k("zapier", "Zapier", "workflow", ["rest"], "https://help.zapier.com/hc/en-us/articles/8496326446989"),
  k("hark", "Hark", "platform", ["mcp-http"], null),
  k("custom", "Custom (REST / curl)", "custom", ["rest", "cli", "mcp-http", "mcp-stdio", "function-calling"], null),
];

export function kindInfo(id: string, list: AgentKindInfo[] = FALLBACK_KINDS): AgentKindInfo {
  return list.find((x) => x.id === id) ?? FALLBACK_KINDS.find((x) => x.id === id) ?? k(id, id, "custom", ["rest", "cli"]);
}
