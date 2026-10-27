// Package agentkit knows every agent kind Boxaz supports (GET /agent-kinds) and
// generates a ready-to-paste connection snippet for each one
// (GET /agents/:id/snippet?kind=&lang=). All snippets read the key from the
// BOXAZ_API_KEY environment variable where the target tool allows it.
package agentkit

// Kind is one supported agent/framework/platform.
type Kind struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Category   string   `json:"category"` // coding|framework|workflow|platform|custom
	Transports []string `json:"transports"`
	DocURL     *string  `json:"doc_url"`
	Language   string   `json:"language"` // snippet language
}

func u(s string) *string { return &s }

// Kinds is the contract list (docs/api-contract-v1.md v1.1), in display order.
var Kinds = []Kind{
	{"claude-code", "Claude Code", "coding", []string{"mcp-http", "mcp-stdio", "cli"}, u("https://docs.anthropic.com/en/docs/claude-code/mcp"), "bash"},
	{"codex-cli", "OpenAI Codex CLI", "coding", []string{"mcp-stdio", "mcp-http", "cli"}, u("https://github.com/openai/codex/blob/main/docs/config.md"), "toml"},
	{"gemini-cli", "Gemini CLI", "coding", []string{"mcp-http", "mcp-stdio", "cli"}, u("https://github.com/google-gemini/gemini-cli/blob/main/docs/tools/mcp-server.md"), "json"},
	{"cursor", "Cursor", "coding", []string{"mcp-http", "mcp-stdio"}, u("https://docs.cursor.com/context/model-context-protocol"), "json"},
	{"windsurf", "Windsurf", "coding", []string{"mcp-http", "mcp-stdio"}, u("https://docs.windsurf.com/windsurf/cascade/mcp"), "json"},
	{"cline", "Cline", "coding", []string{"mcp-http", "mcp-stdio"}, u("https://docs.cline.bot/mcp/configuring-mcp-servers"), "json"},
	{"openai-agents", "OpenAI Agents SDK", "framework", []string{"mcp-http", "sdk-python", "function-calling"}, u("https://openai.github.io/openai-agents-python/mcp/"), "python"},
	{"langchain", "LangChain", "framework", []string{"mcp-http", "sdk-python", "function-calling"}, u("https://github.com/langchain-ai/langchain-mcp-adapters"), "python"},
	{"langgraph", "LangGraph", "framework", []string{"mcp-http", "sdk-python", "function-calling"}, u("https://langchain-ai.github.io/langgraph/agents/mcp/"), "python"},
	{"crewai", "CrewAI", "framework", []string{"mcp-http", "sdk-python"}, u("https://docs.crewai.com/en/mcp/overview"), "python"},
	{"autogen", "AutoGen", "framework", []string{"mcp-http", "sdk-python"}, u("https://microsoft.github.io/autogen/stable/reference/python/autogen_ext.tools.mcp.html"), "python"},
	{"vercel-ai-sdk", "Vercel AI SDK", "framework", []string{"mcp-http", "sdk-ts", "function-calling"}, u("https://ai-sdk.dev/docs/ai-sdk-core/mcp-tools"), "typescript"},
	{"mastra", "Mastra", "framework", []string{"mcp-http", "sdk-ts"}, u("https://mastra.ai/docs/tools-mcp/mcp-overview"), "typescript"},
	{"pydantic-ai", "Pydantic AI", "framework", []string{"mcp-http", "sdk-python"}, u("https://ai.pydantic.dev/mcp/client/"), "python"},
	{"llamaindex", "LlamaIndex", "framework", []string{"mcp-http", "sdk-python"}, u("https://docs.llamaindex.ai/en/stable/api_reference/tools/mcp/"), "python"},
	{"n8n", "n8n", "workflow", []string{"mcp-http", "rest"}, u("https://docs.n8n.io/integrations/builtin/cluster-nodes/sub-nodes/n8n-nodes-langchain.toolmcp/"), "json"},
	{"dify", "Dify", "workflow", []string{"mcp-http", "rest"}, u("https://docs.dify.ai/en/guides/tools/mcp"), "json"},
	{"flowise", "Flowise", "workflow", []string{"mcp-http", "rest"}, u("https://docs.flowiseai.com/integrations/langchain/tools/mcp"), "json"},
	{"make", "Make", "workflow", []string{"rest"}, u("https://apps.make.com/http"), "json"},
	{"zapier", "Zapier", "workflow", []string{"rest"}, u("https://help.zapier.com/hc/en-us/articles/8496326446989"), "json"},
	{"hark", "Hark", "platform", []string{"mcp-http"}, nil, "json"},
	{"custom", "Custom (REST / curl)", "custom", []string{"rest", "cli", "mcp-http", "mcp-stdio", "function-calling"}, nil, "bash"},
}

// Get returns a kind by id.
func Get(id string) (Kind, bool) {
	for _, k := range Kinds {
		if k.ID == id {
			return k, true
		}
	}
	return Kind{}, false
}

// IDs returns all kind ids.
func IDs() []string {
	out := make([]string, len(Kinds))
	for i, k := range Kinds {
		out[i] = k.ID
	}
	return out
}
