// Đoạn cấu hình sinh sẵn theo (kind × transport) — dùng cho MOCK MODE và làm dự phòng khi Core chưa trả biến thể transport.
// Nội dung snippet viết bằng tiếng Anh (code/comment trung lập ngôn ngữ), tham chiếu biến môi trường BOXAZ_API_KEY như Core.
import type { AgentKind, Snippet, Transport } from "./types";
import { kindInfo } from "./agent-kinds";

export const KEY_PLACEHOLDER = "<BOXAZ_API_KEY>";
/** Tên gói SDK (giả định, cần khớp với Core khi phát hành SDK). */
export const SDK_PY = "boxaz";
export const SDK_TS = "@boxaz/sdk";

function rootOf(apiBase: string) {
  return apiBase.replace(/\/v1\/?$/, "").replace(/\/+$/, "");
}

const mcpJson = (url: string, extra: Record<string, unknown> = {}) =>
  JSON.stringify({ mcpServers: { boxaz: { ...extra, url, headers: { Authorization: "Bearer ${BOXAZ_API_KEY}" } } } }, null, 2);

const stdioJson = (root: string) =>
  JSON.stringify({ mcpServers: { boxaz: { command: "box", args: ["mcp"], env: { BOXAZ_URL: root, BOXAZ_API_KEY: "${BOXAZ_API_KEY}" } } } }, null, 2);

function mcpHttp(kind: AgentKind, name: string, root: string): Snippet {
  const url = `${root}/mcp`;
  switch (kind) {
    case "claude-code":
      return { kind, transport: "mcp-http", language: "bash", snippet: `# Claude Code — remote MCP over HTTP\nclaude mcp add --transport http boxaz ${url} \\\n  --header "Authorization: Bearer $BOXAZ_API_KEY"\n\n# or commit .mcp.json to the repo:\n${mcpJson(url, { type: "http" })}\n` };
    case "cursor":
      return { kind, transport: "mcp-http", language: "json", snippet: `// ~/.cursor/mcp.json (or .cursor/mcp.json in the project)\n${mcpJson(url)}\n` };
    case "windsurf":
      return { kind, transport: "mcp-http", language: "json", snippet: `// ~/.codeium/windsurf/mcp_config.json\n${JSON.stringify({ mcpServers: { boxaz: { serverUrl: url, headers: { Authorization: "Bearer ${BOXAZ_API_KEY}" } } } }, null, 2)}\n` };
    case "cline":
      return { kind, transport: "mcp-http", language: "json", snippet: `// Cline → MCP Servers → Configure → cline_mcp_settings.json\n${JSON.stringify({ mcpServers: { boxaz: { type: "streamableHttp", url, headers: { Authorization: "Bearer ${BOXAZ_API_KEY}" } } } }, null, 2)}\n` };
    case "gemini-cli":
      return { kind, transport: "mcp-http", language: "json", snippet: `// ~/.gemini/settings.json\n${JSON.stringify({ mcpServers: { boxaz: { httpUrl: url, headers: { Authorization: "Bearer $BOXAZ_API_KEY" } } } }, null, 2)}\n` };
    case "openai-agents":
      return {
        kind, transport: "mcp-http", language: "python",
        snippet: `# pip install openai-agents
import asyncio, os
from agents import Agent, Runner
from agents.mcp import MCPServerStreamableHttp

boxaz = MCPServerStreamableHttp(
    name="boxaz",
    params={"url": "${url}", "headers": {"Authorization": f"Bearer {os.environ['BOXAZ_API_KEY']}"}},
)

async def main():
    async with boxaz:
        agent = Agent(name="${name}", instructions="Use box_up to create a box, box_exec to run commands, box_down when done.", mcp_servers=[boxaz])
        result = await Runner.run(agent, "Create a python box and run pytest -q")
        print(result.final_output)

asyncio.run(main())
`,
      };
    case "vercel-ai-sdk":
    case "mastra":
      return {
        kind, transport: "mcp-http", language: "ts",
        snippet: `// pnpm add ai @ai-sdk/openai @modelcontextprotocol/sdk
import { experimental_createMCPClient as createMCPClient, generateText } from "ai";
import { StreamableHTTPClientTransport } from "@modelcontextprotocol/sdk/client/streamableHttp.js";
import { openai } from "@ai-sdk/openai";

const boxaz = await createMCPClient({
  transport: new StreamableHTTPClientTransport(new URL("${url}"), {
    requestInit: { headers: { Authorization: \`Bearer \${process.env.BOXAZ_API_KEY}\` } },
  }),
});
const { text } = await generateText({ model: openai("gpt-4.1"), tools: await boxaz.tools(), maxSteps: 8, prompt: "Create a python box and run pytest -q" });
console.log(text);
await boxaz.close();
`,
      };
    case "langchain":
    case "langgraph":
      return {
        kind, transport: "mcp-http", language: "python",
        snippet: `# pip install langchain-mcp-adapters langgraph
import os
from langchain_mcp_adapters.client import MultiServerMCPClient

client = MultiServerMCPClient({
    "boxaz": {
        "transport": "streamable_http",
        "url": "${url}",
        "headers": {"Authorization": f"Bearer {os.environ['BOXAZ_API_KEY']}"},
    }
})
tools = await client.get_tools()  # → pass to create_react_agent(model, tools)
`,
      };
    case "crewai":
      return {
        kind, transport: "mcp-http", language: "python",
        snippet: `# pip install 'crewai-tools[mcp]'
import os
from crewai import Agent
from crewai_tools import MCPServerAdapter

params = {"url": "${url}", "transport": "streamable-http", "headers": {"Authorization": f"Bearer {os.environ['BOXAZ_API_KEY']}"}}
with MCPServerAdapter(params) as tools:
    agent = Agent(role="${name}", goal="Run code in Boxaz boxes", backstory="Cloud runner", tools=tools)
`,
      };
    case "pydantic-ai":
      return {
        kind, transport: "mcp-http", language: "python",
        snippet: `# pip install pydantic-ai
import os
from pydantic_ai import Agent
from pydantic_ai.mcp import MCPServerStreamableHTTP

boxaz = MCPServerStreamableHTTP("${url}", headers={"Authorization": f"Bearer {os.environ['BOXAZ_API_KEY']}"})
agent = Agent("openai:gpt-4.1", toolsets=[boxaz])
`,
      };
    case "n8n":
      return {
        kind, transport: "mcp-http", language: "json",
        snippet: JSON.stringify({ node: "MCP Client Tool", endpoint: url, transport: "httpStreamable", authentication: "headerAuth", header: { name: "Authorization", value: "Bearer {{$env.BOXAZ_API_KEY}}" } }, null, 2),
      };
    case "hark":
      return { kind, transport: "mcp-http", language: "yaml", snippet: `# Hark → Settings → MCP servers → Add\nname: boxaz\ntransport: http\nurl: ${url}\nheaders:\n  Authorization: "Bearer \${BOXAZ_API_KEY}"\n` };
    default:
      return { kind, transport: "mcp-http", language: "json", snippet: `// Any MCP client (streamable HTTP)\n${mcpJson(url, { type: "http" })}\n` };
  }
}

function mcpStdio(kind: AgentKind, root: string): Snippet {
  if (kind === "codex-cli")
    return { kind, transport: "mcp-stdio", language: "toml", snippet: `# ~/.codex/config.toml\n[mcp_servers.boxaz]\ncommand = "box"\nargs = ["mcp"]\nenv = { BOXAZ_URL = "${root}", BOXAZ_API_KEY = "<BOXAZ_API_KEY>" }\n` };
  if (kind === "claude-code")
    return { kind, transport: "mcp-stdio", language: "bash", snippet: `# Local stdio via the box CLI\nBOXAZ_URL=${root} BOXAZ_API_KEY=$BOXAZ_API_KEY claude mcp add boxaz -- box mcp\n` };
  return { kind, transport: "mcp-stdio", language: "json", snippet: `// stdio: the box CLI speaks MCP on stdin/stdout\n${stdioJson(root)}\n` };
}

function rest(kind: AgentKind, root: string): Snippet {
  if (kind === "n8n")
    return {
      kind, transport: "rest", language: "json",
      snippet: JSON.stringify({ nodes: [{ name: "Boxaz · exec", type: "n8n-nodes-base.httpRequest", typeVersion: 4, parameters: { method: "POST", url: `${root}/v1/boxes/{{$json.box_id}}/exec`, sendHeaders: true, headerParameters: { parameters: [{ name: "Authorization", value: "Bearer {{$env.BOXAZ_API_KEY}}" }] }, sendBody: true, contentType: "json", jsonBody: '={ "command": "{{$json.command}}", "tail_lines": 50 }' } }] }, null, 2),
    };
  return {
    kind, transport: "rest", language: "bash",
    snippet: `export BOXAZ_URL=${root}
# export BOXAZ_API_KEY=…  (shown once when the key is created)

# 1. Price check first (dry-run, no box created)
curl -s -X POST $BOXAZ_URL/v1/quote -H "Authorization: Bearer $BOXAZ_API_KEY" -H "Content-Type: application/json" \\
  -d '{"template":"python","lifecycle":"task","spec":{"cpu":2,"ram_mb":2048,"disk_gb":20}}'

# 2. Create a box (idempotent)
curl -s -X POST $BOXAZ_URL/v1/boxes -H "Authorization: Bearer $BOXAZ_API_KEY" \\
  -H "Idempotency-Key: $(uuidgen)" -H "Content-Type: application/json" \\
  -d '{"template":"python","lifecycle":"task","spec":{"cpu":2,"ram_mb":2048,"disk_gb":20}}'

# 3. Run commands
curl -s -X POST $BOXAZ_URL/v1/boxes/<box_id>/exec-batch -H "Authorization: Bearer $BOXAZ_API_KEY" \\
  -H "Content-Type: application/json" -d '{"commands":["python -V","pytest -q"],"stop_on_error":true}'
`,
  };
}

function sdkPython(kind: AgentKind, name: string, root: string): Snippet {
  return {
    kind, transport: "sdk-python", language: "python",
    snippet: `# pip install ${SDK_PY}
import os
from boxaz import Boxaz

boxaz = Boxaz(base_url="${root}", api_key=os.environ["BOXAZ_API_KEY"])  # agent: ${name}
quote = boxaz.quote(template="python", spec={"cpu": 2, "ram_mb": 2048, "disk_gb": 20})
print(quote.provider, quote.cost_per_hour)

box = boxaz.boxes.create(template="python", lifecycle="task", spec={"cpu": 2, "ram_mb": 2048, "disk_gb": 20})
res = box.exec("pytest -q", tail_lines=60)
print(res.exit_code, res.stdout_tail, res.tokens_saved)
box.down()
`,
  };
}

function sdkTs(kind: AgentKind, name: string, root: string): Snippet {
  return {
    kind, transport: "sdk-ts", language: "ts",
    snippet: `// pnpm add ${SDK_TS}
import { Boxaz } from "${SDK_TS}";

const boxaz = new Boxaz({ baseUrl: "${root}", apiKey: process.env.BOXAZ_API_KEY! }); // agent: ${name}
const quote = await boxaz.quote({ template: "node", spec: { cpu: 2, ram_mb: 2048, disk_gb: 20 } });
console.log(quote.provider, quote.cost_per_hour);

const box = await boxaz.boxes.create({ template: "node", lifecycle: "task", spec: { cpu: 2, ram_mb: 2048, disk_gb: 20 } });
const res = await box.exec("pnpm test", { tailLines: 60 });
console.log(res.exit_code, res.stdout_tail, res.tokens_saved);
await box.down();
`,
  };
}

function cli(kind: AgentKind, root: string): Snippet {
  return {
    kind, transport: "cli", language: "bash",
    snippet: `# box CLI (single binary)
export BOXAZ_URL=${root}
export BOXAZ_API_KEY=<BOXAZ_API_KEY>

box quote --template python --cpu 2 --ram 2048
box up --template python --lifecycle task --cpu 2 --ram 2048   # → prints box id
box exec <box_id> -- pytest -q
box log <log_id>          # full output when truncated
box down <box_id>
`,
  };
}

function functionCalling(kind: AgentKind, root: string): Snippet {
  return {
    kind, transport: "function-calling", language: "python",
    snippet: `# Tool schemas: GET ${root}/v1/tools?format=openai|anthropic|gemini|mcp
import os, requests, json
from openai import OpenAI

H = {"Authorization": f"Bearer {os.environ['BOXAZ_API_KEY']}"}
tools = requests.get("${root}/v1/tools", params={"format": "openai"}, headers=H).json()["tools"]
client = OpenAI()
msg = client.chat.completions.create(model="gpt-4.1", tools=tools, messages=[{"role": "user", "content": "Run pytest in a new python box"}]).choices[0].message
for call in msg.tool_calls or []:
    args = json.loads(call.function.arguments)
    # Execute each call against its REST endpoint (operationId = tool name in ${root}/v1/openapi.json),
    # then send the JSON result back as a {"role": "tool", "tool_call_id": call.id} message.
    print(call.function.name, args)
`,
  };
}

/** Tạo snippet cho (kind, transport). Không truyền transport → transport chính của kind. Transport nào cũng có bản dùng chung. */
export function makeSnippet(kind: AgentKind, agentName: string, apiBase: string, transport?: Transport | string | null): Snippet {
  const root = rootOf(apiBase);
  const info = kindInfo(kind);
  const valid = ["mcp-http", "mcp-stdio", "rest", "sdk-python", "sdk-ts", "cli", "function-calling"];
  const t = (transport && valid.includes(transport) ? transport : info.transports[0]) as Transport;
  switch (t) {
    case "mcp-http":
      return mcpHttp(kind, agentName, root);
    case "mcp-stdio":
      return mcpStdio(kind, root);
    case "sdk-python":
      return sdkPython(kind, agentName, root);
    case "sdk-ts":
      return sdkTs(kind, agentName, root);
    case "cli":
      return cli(kind, root);
    case "function-calling":
      return functionCalling(kind, root);
    default:
      return rest(kind, root);
  }
}
