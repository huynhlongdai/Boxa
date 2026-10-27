# Boxaz SDKs

- [`python/`](python/README.md) — package `boxaz` (sync `Boxaz` + async `AsyncBoxaz`, typed, httpx or stdlib, `as_openai_tools()`).
- [`typescript/`](typescript/README.md) — package `@boxaz/sdk` (fetch-based, typed, zero deps, `asOpenAITools()`).

Both read `BOXAZ_URL`, `BOXAZ_API_KEY`, `BOXAZ_LANG`, send `Accept-Language` for localized errors, retry
retryable codes with `retry_after_s`, and execute tool calls through `/mcp` so behaviour matches the MCP server.
