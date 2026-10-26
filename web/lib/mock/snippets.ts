import type { AgentKind, Snippet } from "../types";

export const KEY_PLACEHOLDER = "<BOXA_API_KEY>";

/** Đoạn cấu hình sinh sẵn theo kind. Chuỗi KEY_PLACEHOLDER được UI thay bằng key thật (chỉ hiện 1 lần). */
export function makeSnippet(kind: AgentKind, agentName: string, apiBase: string): Snippet {
  const mcpUrl = apiBase.replace(/\/v1\/?$/, "") + "/mcp";
  switch (kind) {
    case "claude-code":
      return {
        kind,
        language: "json",
        snippet: JSON.stringify(
          {
            mcpServers: {
              boxa: {
                type: "http",
                url: mcpUrl,
                headers: { Authorization: `Bearer ${KEY_PLACEHOLDER}` },
              },
            },
          },
          null,
          2,
        ),
      };
    case "openai-agents":
      return {
        kind,
        language: "python",
        snippet: `# pip install openai-agents
from agents import Agent, Runner
from agents.mcp import MCPServerStreamableHttp

boxa = MCPServerStreamableHttp(
    name="boxa",
    params={
        "url": "${mcpUrl}",
        "headers": {"Authorization": "Bearer ${KEY_PLACEHOLDER}"},
    },
)

async def main():
    async with boxa:
        agent = Agent(
            name="${agentName}",
            instructions="Dùng box_up để tạo box, box_exec để chạy lệnh, box_down khi xong.",
            mcp_servers=[boxa],
        )
        result = await Runner.run(agent, "Tạo box python và chạy pytest -q")
        print(result.final_output)
`,
      };
    case "n8n":
      return {
        kind,
        language: "json",
        snippet: JSON.stringify(
          {
            nodes: [
              {
                name: "Boxa · chạy lệnh",
                type: "n8n-nodes-base.httpRequest",
                typeVersion: 4,
                parameters: {
                  method: "POST",
                  url: `${apiBase}/boxes/{{$json.box_id}}/exec`,
                  sendHeaders: true,
                  headerParameters: { parameters: [{ name: "Authorization", value: `Bearer ${KEY_PLACEHOLDER}` }] },
                  sendBody: true,
                  contentType: "json",
                  jsonBody: '={ "command": "{{$json.command}}", "tail_lines": 50 }',
                },
              },
            ],
          },
          null,
          2,
        ),
      };
    case "hark":
      return {
        kind,
        language: "yaml",
        snippet: `# Hark → Cài đặt → MCP servers → Thêm
name: boxa
transport: http
url: ${mcpUrl}
headers:
  Authorization: "Bearer ${KEY_PLACEHOLDER}"
tools: [box_up, box_exec, box_read, box_write, box_snapshot, box_sleep, box_down, box_cost]
`,
      };
    default:
      return {
        kind,
        language: "bash",
        snippet: `export BOXA_API=${apiBase}
export BOXA_KEY=${KEY_PLACEHOLDER}

# Tạo box
curl -s -X POST $BOXA_API/boxes \\
  -H "Authorization: Bearer $BOXA_KEY" \\
  -H "Idempotency-Key: $(uuidgen)" \\
  -H "Content-Type: application/json" \\
  -d '{"template":"python","lifecycle":"task","spec":{"cpu":2,"ram_mb":2048,"disk_gb":20}}'

# Chạy lệnh
curl -s -X POST $BOXA_API/boxes/<box_id>/exec \\
  -H "Authorization: Bearer $BOXA_KEY" \\
  -H "Content-Type: application/json" \\
  -d '{"command":"python -V","tail_lines":20}'
`,
      };
  }
}
