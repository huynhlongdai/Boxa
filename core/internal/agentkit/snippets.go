package agentkit

import (
	"strings"

	"boxmes/core/internal/i18n"
)

// Snippet is the response of GET /agents/:id/snippet. kind/language/snippet
// are the contract shape; file and notes are extra guidance (localized).
type Snippet struct {
	Kind     string   `json:"kind"`
	Language string   `json:"language"`
	Snippet  string   `json:"snippet"`
	File     string   `json:"file,omitempty"`
	Notes    []string `json:"notes"`
	Locale   string   `json:"locale"`
}

// Build renders the snippet for kind. base is the public core URL (no
// trailing slash), agentName the Boxmes agent name, loc the comment language.
// Unknown kinds fall back to "custom".
func Build(kind, base, agentName, loc string) Snippet {
	if _, ok := Get(kind); !ok {
		kind = "custom"
	}
	if !i18n.Supported(loc) {
		loc = i18n.Default
	}
	k, _ := Get(kind)
	t := func(key string, a ...any) string { return i18n.T(loc, key, a...) }
	tpl, file, notes := templateFor(kind)
	r := strings.NewReplacer(
		"{{BASE}}", base,
		"{{MCP}}", base+"/mcp",
		"{{NAME}}", agentName,
		"{{C_ENV}}", t("snip.env"),
		"{{C_HTTP}}", t("snip.http"),
		"{{C_STDIO}}", t("snip.stdio"),
		"{{C_RUN}}", t("snip.run"),
		"{{C_REST}}", t("snip.rest"),
		"{{C_UNCERTAIN}}", t("snip.uncertain"),
		"{{C_TOOLS}}", t("snip.tools"),
		"{{TASK}}", t("snip.task"),
		"{{INSTR}}", t("snip.instructions"),
	)
	s := Snippet{Kind: kind, Language: k.Language, Snippet: r.Replace(tpl), File: file, Locale: loc, Notes: []string{}}
	if file != "" {
		s.Notes = append(s.Notes, t("snip.file", file))
	}
	for _, n := range notes {
		s.Notes = append(s.Notes, r.Replace(t(n)))
	}
	return s
}

// templateFor returns the snippet template, its target file and note keys.
func templateFor(kind string) (tpl, file string, notes []string) {
	switch kind {
	case "claude-code":
		return `# {{C_ENV}}
export BOXMES_API_KEY=bx_agent_...

# {{C_HTTP}}
claude mcp add --transport http boxmes {{MCP}} --header "Authorization: Bearer $BOXMES_API_KEY"

# {{C_STDIO}}
claude mcp add boxmes --env BOXMES_URL={{BASE}} --env BOXMES_API_KEY=$BOXMES_API_KEY -- box mcp
`, "", []string{"snip.n.claude"}

	case "codex-cli":
		// Codex reads ~/.codex/config.toml. stdio is the long-standing format;
		// streamable HTTP (url + bearer_token_env_var) exists in newer releases.
		return `# {{C_STDIO}}
[mcp_servers.boxmes]
command = "box"
args = ["mcp"]
env = { BOXMES_URL = "{{BASE}}", BOXMES_API_KEY = "<BOXMES_API_KEY>" }
# env_vars = ["BOXMES_API_KEY"]   # forward the key from your shell instead ({{C_UNCERTAIN}})

# {{C_HTTP}}
# {{C_UNCERTAIN}}
# [mcp_servers.boxmes]
# url = "{{MCP}}"
# bearer_token_env_var = "BOXMES_API_KEY"
`, "~/.codex/config.toml", []string{"snip.n.paste", "snip.n.codex"}

	case "gemini-cli":
		return `{
  "mcpServers": {
    "boxmes": {
      "httpUrl": "{{MCP}}",
      "headers": { "Authorization": "Bearer $BOXMES_API_KEY" },
      "timeout": 600000,
      "trust": false
    }
  }
}
`, "~/.gemini/settings.json", []string{"snip.n.envexpand", "snip.n.stdio_alt"}

	case "cursor":
		return `{
  "mcpServers": {
    "boxmes": {
      "url": "{{MCP}}",
      "headers": { "Authorization": "Bearer ${env:BOXMES_API_KEY}" }
    }
  }
}
`, ".cursor/mcp.json", []string{"snip.n.envexpand", "snip.n.stdio_alt"}

	case "windsurf":
		return `{
  "mcpServers": {
    "boxmes": {
      "serverUrl": "{{MCP}}",
      "headers": { "Authorization": "Bearer ${env:BOXMES_API_KEY}" }
    }
  }
}
`, "~/.codeium/windsurf/mcp_config.json", []string{"snip.n.envexpand", "snip.n.uncertain", "snip.n.stdio_alt"}

	case "cline":
		return `{
  "mcpServers": {
    "boxmes": {
      "command": "box",
      "args": ["mcp"],
      "env": { "BOXMES_URL": "{{BASE}}", "BOXMES_API_KEY": "<BOXMES_API_KEY>" },
      "disabled": false,
      "autoApprove": ["box_list", "box_read", "box_observe", "box_cost", "box_quote", "box_templates"]
    }
  }
}
`, "cline_mcp_settings.json", []string{"snip.n.paste", "snip.n.cline_http"}

	case "openai-agents":
		return `# pip install openai-agents
# {{C_ENV}}
import asyncio, os
from agents import Agent, Runner
from agents.mcp import MCPServerStreamableHttp


async def main() -> None:
    # {{C_HTTP}}
    async with MCPServerStreamableHttp(
        name="boxmes",
        params={
            "url": "{{MCP}}",
            "headers": {"Authorization": f"Bearer {os.environ['BOXMES_API_KEY']}"},
            "timeout": 60,
        },
        client_session_timeout_seconds=600,
        cache_tools_list=True,
    ) as boxmes:
        agent = Agent(name="{{NAME}}", instructions="{{INSTR}}", mcp_servers=[boxmes])
        # {{C_RUN}}
        result = await Runner.run(agent, "{{TASK}}")
        print(result.final_output)


asyncio.run(main())
`, "", nil

	case "langchain":
		return `# pip install langchain langchain-mcp-adapters langchain-openai
# {{C_ENV}}
import asyncio, os
from langchain_mcp_adapters.client import MultiServerMCPClient
from langchain.agents import create_agent


async def main() -> None:
    # {{C_HTTP}}
    client = MultiServerMCPClient({
        "boxmes": {
            "transport": "streamable_http",
            "url": "{{MCP}}",
            "headers": {"Authorization": f"Bearer {os.environ['BOXMES_API_KEY']}"},
        }
    })
    tools = await client.get_tools()
    agent = create_agent("openai:gpt-4.1", tools, system_prompt="{{INSTR}}")
    # {{C_RUN}}
    out = await agent.ainvoke({"messages": [{"role": "user", "content": "{{TASK}}"}]})
    print(out["messages"][-1].content)


asyncio.run(main())
`, "", []string{"snip.n.langchain"}

	case "langgraph":
		return `# pip install langgraph langchain-mcp-adapters langchain-openai
# {{C_ENV}}
import asyncio, os
from langchain_mcp_adapters.client import MultiServerMCPClient
from langgraph.prebuilt import create_react_agent


async def main() -> None:
    # {{C_HTTP}}
    client = MultiServerMCPClient({
        "boxmes": {
            "transport": "streamable_http",
            "url": "{{MCP}}",
            "headers": {"Authorization": f"Bearer {os.environ['BOXMES_API_KEY']}"},
        }
    })
    tools = await client.get_tools()
    graph = create_react_agent("openai:gpt-4.1", tools, prompt="{{INSTR}}")
    # {{C_RUN}}
    out = await graph.ainvoke({"messages": [{"role": "user", "content": "{{TASK}}"}]})
    print(out["messages"][-1].content)


asyncio.run(main())
`, "", nil

	case "crewai":
		return `# pip install crewai "crewai-tools[mcp]"
# {{C_ENV}}
# {{C_UNCERTAIN}}
import os
from crewai import Agent, Crew, Task
from crewai_tools import MCPServerAdapter

server_params = {
    "url": "{{MCP}}",
    "transport": "streamable-http",
    "headers": {"Authorization": f"Bearer {os.environ['BOXMES_API_KEY']}"},
}

# {{C_HTTP}}
with MCPServerAdapter(server_params) as boxmes_tools:
    coder = Agent(
        role="{{NAME}}",
        goal="{{INSTR}}",
        backstory="Runs code in disposable Boxmes sandboxes.",
        tools=boxmes_tools,
    )
    task = Task(description="{{TASK}}", expected_output="The command output.", agent=coder)
    # {{C_RUN}}
    print(Crew(agents=[coder], tasks=[task]).kickoff())
`, "", []string{"snip.n.crewai"}

	case "autogen":
		return `# pip install "autogen-agentchat" "autogen-ext[openai,mcp]"
# {{C_ENV}}
import asyncio, os
from autogen_agentchat.agents import AssistantAgent
from autogen_ext.models.openai import OpenAIChatCompletionClient
from autogen_ext.tools.mcp import StreamableHttpServerParams, mcp_server_tools


async def main() -> None:
    # {{C_HTTP}}
    params = StreamableHttpServerParams(
        url="{{MCP}}",
        headers={"Authorization": f"Bearer {os.environ['BOXMES_API_KEY']}"},
        timeout=60,
    )
    tools = await mcp_server_tools(params)
    agent = AssistantAgent(
        "{{NAME}}",
        model_client=OpenAIChatCompletionClient(model="gpt-4.1"),
        tools=tools,
        system_message="{{INSTR}}",
        reflect_on_tool_use=True,
    )
    # {{C_RUN}}
    result = await agent.run(task="{{TASK}}")
    print(result.messages[-1].content)


asyncio.run(main())
`, "", nil

	case "pydantic-ai":
		return `# pip install pydantic-ai
# {{C_ENV}}
import asyncio, os
from pydantic_ai import Agent
from pydantic_ai.mcp import MCPServerStreamableHTTP

# {{C_HTTP}}
boxmes = MCPServerStreamableHTTP(
    "{{MCP}}",
    headers={"Authorization": f"Bearer {os.environ['BOXMES_API_KEY']}"},
)
agent = Agent("openai:gpt-4.1", toolsets=[boxmes], instructions="{{INSTR}}")


async def main() -> None:
    async with agent:
        # {{C_RUN}}
        result = await agent.run("{{TASK}}")
        print(result.output)


asyncio.run(main())
`, "", []string{"snip.n.pydantic"}

	case "llamaindex":
		return `# pip install llama-index llama-index-tools-mcp llama-index-llms-openai
# {{C_ENV}}
# {{C_UNCERTAIN}}
import asyncio, os
from llama_index.core.agent.workflow import FunctionAgent
from llama_index.llms.openai import OpenAI
from llama_index.tools.mcp import BasicMCPClient, McpToolSpec


async def main() -> None:
    # {{C_HTTP}}
    client = BasicMCPClient(
        "{{MCP}}",
        headers={"Authorization": f"Bearer {os.environ['BOXMES_API_KEY']}"},
    )
    tools = await McpToolSpec(client=client).to_tool_list_async()
    agent = FunctionAgent(tools=tools, llm=OpenAI(model="gpt-4.1"), system_prompt="{{INSTR}}")
    # {{C_RUN}}
    print(await agent.run("{{TASK}}"))


asyncio.run(main())
`, "", nil

	case "vercel-ai-sdk":
		return `// npm i ai @ai-sdk/openai @modelcontextprotocol/sdk
// {{C_ENV}}
import { experimental_createMCPClient as createMCPClient, generateText, stepCountIs } from "ai";
import { openai } from "@ai-sdk/openai";
import { StreamableHTTPClientTransport } from "@modelcontextprotocol/sdk/client/streamableHttp.js";

// {{C_HTTP}}
const boxmes = await createMCPClient({
  transport: new StreamableHTTPClientTransport(new URL("{{MCP}}"), {
    requestInit: { headers: { Authorization: ` + "`Bearer ${process.env.BOXMES_API_KEY}`" + ` } },
  }),
});

try {
  // {{C_RUN}}
  const { text } = await generateText({
    model: openai("gpt-4.1"),
    tools: await boxmes.tools(),
    stopWhen: stepCountIs(10),
    system: "{{INSTR}}",
    prompt: "{{TASK}}",
  });
  console.log(text);
} finally {
  await boxmes.close();
}
`, "", []string{"snip.n.aisdk"}

	case "mastra":
		return `// npm i @mastra/core @mastra/mcp @ai-sdk/openai
// {{C_ENV}}
import { Agent } from "@mastra/core/agent";
import { MCPClient } from "@mastra/mcp";
import { openai } from "@ai-sdk/openai";

// {{C_HTTP}}
const mcp = new MCPClient({
  servers: {
    boxmes: {
      url: new URL("{{MCP}}"),
      requestInit: { headers: { Authorization: ` + "`Bearer ${process.env.BOXMES_API_KEY}`" + ` } },
    },
  },
});

export const agent = new Agent({
  name: "{{NAME}}",
  instructions: "{{INSTR}}",
  model: openai("gpt-4.1"),
  tools: await mcp.getTools(),
});

// {{C_RUN}}
const res = await agent.generate("{{TASK}}");
console.log(res.text);
`, "", nil

	case "n8n":
		return `{
  "nodes": [
    {
      "name": "Boxmes MCP",
      "type": "@n8n/n8n-nodes-langchain.mcpClientTool",
      "typeVersion": 1.1,
      "position": [0, 0],
      "parameters": {
        "endpointUrl": "{{MCP}}",
        "serverTransport": "httpStreamable",
        "authentication": "bearerAuth",
        "include": "all"
      },
      "credentials": { "httpBearerAuth": { "name": "BOXMES_API_KEY" } }
    }
  ],
  "connections": {}
}
`, "", []string{"snip.n.n8n", "snip.n.uncertain"}

	case "dify":
		return `{
  "mcp_server": {
    "name": "Boxmes",
    "server_identifier": "boxmes",
    "server_url": "{{MCP}}",
    "headers": { "Authorization": "Bearer <BOXMES_API_KEY>" }
  },
  "custom_tool_openapi": {
    "schema_url": "{{BASE}}/v1/openapi.json",
    "auth": { "type": "api_key", "header": "Authorization", "value": "Bearer <BOXMES_API_KEY>" }
  }
}
`, "", []string{"snip.n.dify", "snip.n.paste"}

	case "flowise":
		return `{
  "url": "{{MCP}}",
  "headers": { "Authorization": "Bearer {{$vars.BOXMES_API_KEY}}" }
}
`, "", []string{"snip.n.flowise", "snip.n.uncertain"}

	case "make":
		return `{
  "module": "http:ActionSendData",
  "url": "{{BASE}}/v1/boxes/{{box_id}}/exec",
  "method": "post",
  "headers": [
    { "name": "Authorization", "value": "Bearer {{BOXMES_API_KEY}}" },
    { "name": "Accept-Language", "value": "en" }
  ],
  "bodyType": "raw",
  "contentType": "application/json",
  "data": "{\"command\": \"echo hello from Make\", \"tail_lines\": 40}",
  "parseResponse": true
}
`, "", []string{"snip.n.make", "snip.n.rest_flow"}

	case "zapier":
		return `{
  "app": "Webhooks by Zapier",
  "action": "Custom Request",
  "method": "POST",
  "url": "{{BASE}}/v1/boxes/<box_id>/exec",
  "headers": {
    "Authorization": "Bearer <BOXMES_API_KEY>",
    "Content-Type": "application/json"
  },
  "data": "{\"command\": \"echo hello from Zapier\", \"tail_lines\": 40}"
}
`, "", []string{"snip.n.paste", "snip.n.rest_flow"}

	case "hark":
		return `{
  "mcpServers": {
    "boxmes": {
      "url": "{{MCP}}",
      "headers": { "Authorization": "Bearer ${BOXMES_API_KEY}" }
    }
  }
}
`, "", []string{"snip.n.hark"}
	}
	// custom
	return `# {{C_ENV}}
export BOXMES_URL={{BASE}} BOXMES_API_KEY=bx_agent_...
H="Authorization: Bearer $BOXMES_API_KEY"

# {{C_REST}}
BOX=$(curl -s -X POST $BOXMES_URL/v1/boxes -H "$H" -H "Content-Type: application/json" \
  -d '{"template":"python","lifecycle":"task","spec":{"cpu":1,"ram_mb":1024,"disk_gb":10}}' | jq -r .id)
curl -s -X POST $BOXMES_URL/v1/boxes/$BOX/exec -H "$H" -d '{"command":"python3 -c \"print(42)\""}'
curl -s -X DELETE $BOXMES_URL/v1/boxes/$BOX -H "$H"

# {{C_TOOLS}}
curl -s "$BOXMES_URL/v1/tools?format=openai"
`, "", []string{"snip.n.discovery"}
}
