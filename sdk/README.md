# Boxa SDKs

- [`python/`](python/README.md) — package `boxa` (sync `Boxa` + async `AsyncBoxa`, typed, httpx or stdlib, `as_openai_tools()`).
- [`typescript/`](typescript/README.md) — package `@boxa/sdk` (fetch-based, typed, zero deps, `asOpenAITools()`).

Both read `BOXA_URL`, `BOXA_API_KEY`, `BOXA_LANG`, send `Accept-Language` for localized errors, retry
retryable codes with `retry_after_s`, and execute tool calls through `/mcp` so behaviour matches the MCP server.
