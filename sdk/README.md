# Boxmes SDKs

- [`python/`](python/README.md) — package `boxmes` (sync `Boxmes` + async `AsyncBoxmes`, typed, httpx or stdlib, `as_openai_tools()`).
- [`typescript/`](typescript/README.md) — package `@boxmes/sdk` (fetch-based, typed, zero deps, `asOpenAITools()`).

Both read `BOXMES_URL`, `BOXMES_API_KEY`, `BOXMES_LANG`, send `Accept-Language` for localized errors, retry
retryable codes with `retry_after_s`, and execute tool calls through `/mcp` so behaviour matches the MCP server.
