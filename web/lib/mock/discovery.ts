// Discovery cho agent (contract v1.1) ở MOCK MODE: OpenAPI 3.1, llms.txt, MCP server card, A2A agent card, tool schemas.
// Nội dung bằng tiếng Anh, trung lập ngôn ngữ (agent đọc); giao diện /developers mới là phần được dịch.
import type { ToolFormat } from "../types";

type JSONSchema = Record<string, unknown>;
interface ToolDef {
  name: string;
  title: string;
  description: string;
  method: string;
  path: string;
  input: JSONSchema;
}

const spec: JSONSchema = {
  type: "object",
  properties: { cpu: { type: "integer", minimum: 1, maximum: 16 }, ram_mb: { type: "integer", minimum: 256 }, disk_gb: { type: "integer", minimum: 5 } },
  required: ["cpu", "ram_mb", "disk_gb"],
};
const boxId = { box_id: { type: "string", description: "Box id (box_…) or slug" } };
const obj = (properties: Record<string, unknown>, required: string[] = []): JSONSchema => ({ type: "object", properties, required, additionalProperties: false });

export const TOOLS: ToolDef[] = [
  { name: "box_up", title: "Create a box", method: "POST", path: "/boxes", description: "Create (or reuse, with Idempotency-Key) a cloud box for the agent. Call box_quote first when cost matters. Returns the Box plus `next` hints.", input: obj({ template: { type: "string", default: "python" }, slug: { type: "string" }, lifecycle: { type: "string", enum: ["task", "session", "service"] }, spec, ttl_seconds: { type: "integer" }, idle_minutes: { type: "integer" }, budget_usd: { type: "number" }, ports: { type: "array", items: { type: "integer" } }, secrets: { type: "array", items: { type: "string" } }, setup: { type: "array", items: { type: "string" } }, yaml: { type: "string", description: "Full box.yaml instead of fields" } }) },
  { name: "box_quote", title: "Quote a box", method: "POST", path: "/quote", description: "Dry-run placement: provider, region, cost_per_hour, expected_total_usd, placement_reason and alternatives. Creates nothing.", input: obj({ template: { type: "string" }, lifecycle: { type: "string", enum: ["task", "session", "service"] }, spec, ttl_seconds: { type: "integer" } }, ["spec"]) },
  { name: "box_exec", title: "Run a command", method: "POST", path: "/boxes/{box_id}/exec", description: "Run a bash command in /work. Returns exit_code, the last tail_lines of stdout/stderr, truncated, log_id, files_changed, tokens_saved. Use async=true for long jobs and poll box_job. Sleeping boxes are woken automatically.", input: obj({ ...boxId, command: { type: "string" }, cwd: { type: "string" }, timeout_s: { type: "integer" }, async: { type: "boolean" }, tail_lines: { type: "integer", default: 60 } }, ["box_id", "command"]) },
  { name: "box_exec_batch", title: "Run several commands", method: "POST", path: "/boxes/{box_id}/exec-batch", description: "Run commands in order; stop_on_error stops at the first non-zero exit. One round trip instead of many.", input: obj({ ...boxId, commands: { type: "array", items: { type: "string" }, minItems: 1 }, stop_on_error: { type: "boolean", default: true } }, ["box_id", "commands"]) },
  { name: "box_job", title: "Get a job", method: "GET", path: "/jobs/{job_id}", description: "Status/result of an async exec. Pass wait (≤30 s) to long-poll instead of looping.", input: obj({ job_id: { type: "string" }, wait: { type: "integer", maximum: 30 } }, ["job_id"]) },
  { name: "box_read", title: "Read a file", method: "GET", path: "/boxes/{box_id}/files", description: "Read a file (optionally a line range) or list a directory.", input: obj({ ...boxId, path: { type: "string" }, from: { type: "integer" }, to: { type: "integer" } }, ["box_id", "path"]) },
  { name: "box_write", title: "Write a file", method: "PUT", path: "/boxes/{box_id}/files", description: "Write a whole file (UTF-8).", input: obj({ ...boxId, path: { type: "string" }, content: { type: "string" } }, ["box_id", "path", "content"]) },
  { name: "box_grep", title: "Search files", method: "POST", path: "/boxes/{box_id}/grep", description: "Search file contents; cheaper than reading whole files.", input: obj({ ...boxId, pattern: { type: "string" }, path: { type: "string", default: "/work" } }, ["box_id", "pattern"]) },
  { name: "box_observe", title: "Observe a box", method: "GET", path: "/boxes/{box_id}/observe", description: "Changed files, processes and listening ports since the last call.", input: obj({ ...boxId }, ["box_id"]) },
  { name: "box_snapshot", title: "Snapshot a box", method: "POST", path: "/boxes/{box_id}/snapshot", description: "Take a snapshot now.", input: obj({ ...boxId }, ["box_id"]) },
  { name: "box_restore", title: "Restore a snapshot", method: "POST", path: "/boxes/{box_id}/restore", description: "Replace the box state with a snapshot.", input: obj({ ...boxId, snapshot_id: { type: "string" } }, ["box_id", "snapshot_id"]) },
  { name: "box_fork", title: "Fork a snapshot", method: "POST", path: "/boxes/{box_id}/fork", description: "Create a new box from a snapshot; the original is untouched.", input: obj({ ...boxId, snapshot_id: { type: "string" } }, ["box_id", "snapshot_id"]) },
  { name: "box_sleep", title: "Sleep a box", method: "POST", path: "/boxes/{box_id}/sleep", description: "Snapshot and stop compute billing; wake on next exec.", input: obj({ ...boxId }, ["box_id"]) },
  { name: "box_down", title: "Delete a box", method: "DELETE", path: "/boxes/{box_id}", description: "Delete the box when the task is done.", input: obj({ ...boxId }, ["box_id"]) },
  { name: "box_list", title: "List boxes", method: "GET", path: "/boxes", description: "List this agent's boxes (filters: status, lifecycle).", input: obj({ status: { type: "string" }, lifecycle: { type: "string" } }) },
  { name: "box_logs", title: "Read a full log", method: "GET", path: "/logs/{log_id}", description: "Full output of a truncated exec, by log_id.", input: obj({ log_id: { type: "string" } }, ["log_id"]) },
  { name: "box_templates", title: "List templates", method: "GET", path: "/templates", description: "Templates usable as `template` in box_up.", input: obj({}) },
  { name: "box_cost", title: "Spend so far", method: "GET", path: "/costs", description: "Ledger and totals for this agent (today/month vs budget).", input: obj({ from: { type: "string", format: "date" }, to: { type: "string", format: "date" } }) },
];

export function toolSchemas(format: ToolFormat) {
  switch (format) {
    case "anthropic":
      return { format, tools: TOOLS.map((t) => ({ name: t.name, description: t.description, input_schema: t.input })) };
    case "gemini":
      return { format, tools: [{ functionDeclarations: TOOLS.map((t) => ({ name: t.name, description: t.description, parameters: t.input })) }] };
    case "mcp":
      return { format, tools: TOOLS.map((t) => ({ name: t.name, title: t.title, description: t.description, inputSchema: t.input })) };
    default:
      return { format: "openai", tools: TOOLS.map((t) => ({ type: "function", function: { name: t.name, description: t.description, parameters: t.input, strict: false } })) };
  }
}

const ERROR_SCHEMA = {
  type: "object",
  properties: {
    error: {
      type: "object",
      required: ["code", "message"],
      properties: {
        code: { type: "string", enum: ["BUDGET_EXCEEDED", "NO_CAPACITY", "PROVIDER_FAILED", "BOX_SLEEPING", "TIMEOUT", "POLICY_DENIED", "NOT_FOUND", "UNAUTHORIZED", "INVALID"] },
        message: { type: "string", description: "Localized via Accept-Language" },
        hint: { type: "string", description: "One sentence telling the agent what to do next" },
        retry_after_s: { type: "integer" },
        docs_url: { type: "string", format: "uri" },
      },
    },
  },
};

export function openapi(root: string) {
  const pathsObj: Record<string, Record<string, unknown>> = {};
  for (const t of TOOLS) {
    const p = t.path;
    const params = [...p.matchAll(/\{(\w+)\}/g)].map((m) => ({ name: m[1], in: "path", required: true, schema: { type: "string" } }));
    const props = (t.input.properties ?? {}) as Record<string, unknown>;
    const bodyProps = Object.fromEntries(Object.entries(props).filter(([k]) => !params.some((x) => x.name === k)));
    const isGet = t.method === "GET" || t.method === "DELETE";
    const op: Record<string, unknown> = {
      operationId: t.name,
      summary: t.title,
      description: t.description,
      parameters: [
        ...params,
        { name: "Accept-Language", in: "header", required: false, schema: { type: "string", enum: ["vi", "en", "zh", "ja"] } },
        ...(isGet ? Object.entries(bodyProps).map(([name, schema]) => ({ name, in: "query", required: false, schema })) : []),
      ],
      responses: {
        "200": { description: "OK" },
        default: { description: "Error", content: { "application/json": { schema: { $ref: "#/components/schemas/Error" } } } },
      },
      security: [{ bearer: [] }],
    };
    if (!isGet) op.requestBody = { required: true, content: { "application/json": { schema: { ...t.input, properties: bodyProps } } } };
    if (t.name === "box_up") (op.parameters as unknown[]).push({ name: "Idempotency-Key", in: "header", required: false, schema: { type: "string" } });
    pathsObj[p] = { ...(pathsObj[p] ?? {}), [t.method.toLowerCase()]: op };
  }
  pathsObj["/agent-kinds"] = { get: { operationId: "agent_kinds", summary: "Supported agent kinds", responses: { "200": { description: "OK" } } } };
  pathsObj["/tools"] = { get: { operationId: "tool_schemas", summary: "Tool schemas", security: [], parameters: [{ name: "format", in: "query", schema: { type: "string", enum: ["openai", "anthropic", "gemini", "mcp"] } }], responses: { "200": { description: "OK" } } } };
  return {
    openapi: "3.1.0",
    info: { title: "Boxa API", version: "1.1.0", description: "Personal cloud boxes for AI agents. Errors carry a stable `code`, a localized `message`/`hint`, and optional `retry_after_s` / `docs_url`." },
    servers: [{ url: `${root}/v1` }],
    components: { securitySchemes: { bearer: { type: "http", scheme: "bearer" } }, schemas: { Error: ERROR_SCHEMA } },
    paths: pathsObj,
  };
}

export function llmsTxt(root: string, full = false) {
  const lines = [
    "# Boxa",
    "",
    "> Personal cloud boxes for AI agents: create a box, run commands, read/write files, snapshot, sleep, pay per use under a budget.",
    "",
    "## Connect",
    `- MCP (streamable HTTP): ${root}/mcp  — header Authorization: Bearer $BOXA_API_KEY`,
    "- MCP (stdio): `box mcp` with BOXA_URL and BOXA_API_KEY in the environment",
    `- REST: ${root}/v1  — OpenAPI: ${root}/v1/openapi.json`,
    `- Tool schemas: ${root}/v1/tools?format=openai|anthropic|gemini|mcp`,
    `- MCP server card: ${root}/.well-known/mcp.json · A2A agent card: ${root}/.well-known/agent.json`,
    "",
    "## Rules of thumb",
    "- Call box_quote before box_up when cost matters; respect the `next` hints in responses.",
    "- Prefer box_exec_batch for several commands; use async + GET /v1/jobs/{id}?wait=30 for long ones.",
    "- Output is trimmed (tokens_saved); fetch the full log with box_logs when truncated=true.",
    "- On error read `code` (stable) and `hint`; honor `retry_after_s`. Send Accept-Language for localized messages.",
    "- box_down when done (task), or let the Reaper sleep idle boxes (session).",
    "",
    "## Tools",
    ...TOOLS.map((t) => `- ${t.name}: ${t.description}`),
  ];
  if (full) {
    lines.push("", "## Endpoints");
    for (const t of TOOLS) lines.push(`### ${t.method} /v1${t.path} (${t.name})`, "```json", JSON.stringify(t.input, null, 2), "```", "");
  }
  return lines.join("\n") + "\n";
}

export function mcpCard(root: string) {
  return {
    name: "boxa",
    title: "Boxa",
    version: "1.1.0",
    description: "Cloud boxes for AI agents (create, exec, files, snapshots, budgets).",
    transports: [
      { type: "streamable-http", url: `${root}/mcp`, auth: { type: "bearer", env: "BOXA_API_KEY" } },
      { type: "stdio", command: "box", args: ["mcp"], env: ["BOXA_URL", "BOXA_API_KEY"] },
    ],
    tools: TOOLS.map((t) => t.name),
    documentation: `${root}/llms.txt`,
  };
}

export function a2aCard(root: string) {
  return {
    protocolVersion: "0.3.0",
    name: "Boxa",
    description: "Provisions and operates sandboxed cloud boxes for other agents, within a budget.",
    url: `${root}/a2a`,
    version: "1.1.0",
    defaultInputModes: ["application/json", "text/plain"],
    defaultOutputModes: ["application/json", "text/plain"],
    capabilities: { streaming: true, pushNotifications: false },
    securitySchemes: { bearer: { type: "http", scheme: "bearer" } },
    security: [{ bearer: [] }],
    skills: [
      { id: "run-code", name: "Run code in a box", description: "Create a box, run commands, return output and files.", tags: ["exec", "sandbox"] },
      { id: "quote", name: "Quote compute", description: "Price and placement for a spec without creating anything.", tags: ["cost"] },
      { id: "snapshot", name: "Snapshot / fork", description: "Save, restore or fork box state.", tags: ["state"] },
    ],
  };
}

/** Bộ định tuyến discovery cho mock. Trả null nếu không phải endpoint discovery. */
export function discovery(segs: string[], q: URLSearchParams, apiBase: string): { status: number; body: unknown; contentType?: string } | null {
  const root = apiBase.replace(/\/v1\/?$/, "");
  const [a, b, c] = segs;
  if (a === "openapi.json") return { status: 200, body: openapi(root) };
  if (a === "tools") {
    const f = (q.get("format") ?? "openai") as ToolFormat;
    return { status: 200, body: toolSchemas(["openai", "anthropic", "gemini", "mcp"].includes(f) ? f : "openai") };
  }
  if (a === "_root") {
    if (b === "llms.txt") return { status: 200, body: llmsTxt(root), contentType: "text/plain; charset=utf-8" };
    if (b === "llms-full.txt") return { status: 200, body: llmsTxt(root, true), contentType: "text/plain; charset=utf-8" };
    if (b === ".well-known" && c === "mcp.json") return { status: 200, body: mcpCard(root) };
    if (b === ".well-known" && c === "agent.json") return { status: 200, body: a2aCard(root) };
  }
  return null;
}
