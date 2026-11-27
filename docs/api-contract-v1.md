# Boxmes API contract v1 (shared by core + web) — 2026-10-28 (v1.11)

Base: `http://localhost:8080/v1`. Auth: `Authorization: Bearer <key>`. Admin key (scope `admin`) sees everything; agent keys see only own boxes. JSON, snake_case. Times RFC3339 UTC. Money as number USD (float, 4 decimals).
Errors: HTTP 4xx/5xx with `{ "error": { "code": "BUDGET_EXCEEDED|NO_CAPACITY|PROVIDER_FAILED|BOX_SLEEPING|TIMEOUT|POLICY_DENIED|NOT_FOUND|UNAUTHORIZED|INVALID", "message": "...", "hint": "one sentence for the agent" } }`.
Lists: `{ "items": [...], "next_cursor": null }`. Create box accepts header `Idempotency-Key`.

## Objects
Box: `{id, slug, agent_id, agent_name, status: requested|placing|provisioning|running|sleeping|snapshotting|terminated|failed, lifecycle: task|session|service, template, spec:{cpu, ram_mb, disk_gb}, provider, region, host_id, url, ttl_seconds, idle_minutes, budget_usd, spent_usd, cost_per_hour, placement_reason, ports:[int], secrets:[name], created_at, last_active_at}`
Agent: `{id, name, kind: claude-code|openai-agents|n8n|hark|custom, budget_day_usd, budget_month_usd, spent_today_usd, spent_month_usd, policy:{providers:[..], regions:[..], egress_allowlist:[..]|null}, box_count, last_call_at, memory_mb, created_at}`
ApiKey: `{id, agent_id|null, name, prefix, scopes:[..], created_at, last_used_at, revoked_at}` (create returns `key` once).
ExecResult: `{exit_code, duration_ms, stdout_tail, stderr_tail, truncated, log_id|null, files_changed:[path], tokens_saved}`; async returns `{job_id}`.
Job: `{id, box_id, command, status: queued|running|done|failed, result: ExecResult|null, created_at, finished_at}`
Event: `{id, ts, actor, box_id|null, agent_id|null, type, message, cost_usd|null}`
Host: `{id, provider, region, sku, ip, status: provisioning|ready|draining|down, cpu, ram_mb, disk_gb, cpu_used_pct, ram_used_pct, disk_used_pct, box_count, price_month, price_hour, billing: pool|burst, created_at}`
Provider: `{id: hetzner|vultr|digitalocean|contabo|netcup|local, name, connected, tier: T1|T2|T3, last_check_at, error|null}`
Offer: `{provider, sku, region, cpu, ram_mb, disk_gb, price_hour, price_month, min_billing_minutes, source, fetched_at, confidence: verified|unverified|stock_limited}`
LedgerEntry: `{id, ts, agent_id, box_id, provider, kind: compute|storage|egress|snapshot, amount_usd, note}`
Template: `{name, description, yaml, uses, created_at}`
Secret: `{name, agents:[agent_id], created_at, rotated_at}` (value never returned)
Snapshot: `{id, box_id, size_mb, created_at, location}`
Approval ("Cần anh"): `{id, type: budget|service_stop|pool_expand|host_down, title, detail, amount_usd|null, created_at, status: pending|approved|rejected}`

## Endpoints
- `GET /health` (no auth) → `{ok, version, components:{db, scheduler, reaper, price_feed, gateway}}`
- `GET /overview` → `{spent_today, budget_day, spent_month, budget_month, boxes_running, boxes_sleeping, saved_vs_247_usd, cost_7d:[{date, by_agent:{name:usd}}], top_tasks:[{box_id, slug, agent_name, cost_usd}], approvals_pending:int}`
- Boxes: `POST /boxes` (body: `{agent_id?, slug?, template, spec, lifecycle, ttl_seconds?, idle_minutes?, budget_usd?, ports?, secrets?, setup?:[cmd]}` or `{yaml}`), `GET /boxes?status=&agent=&provider=&lifecycle=`, `GET /boxes/:id`, `DELETE /boxes/:id`, `POST /boxes/:id/{sleep|wake|snapshot}`, `POST /boxes/:id/exec` `{command, cwd?, timeout_s?, async?, tail_lines?}`, `GET /boxes/:id/files?path=&from=&to=` (dir → `{entries:[{name,type,size}]}`, file → `{content, total_lines}`), `PUT /boxes/:id/files` `{path, content}`, `POST /boxes/:id/grep` `{pattern, path}`, `GET /boxes/:id/observe`, `GET /boxes/:id/events`, `GET /boxes/:id/snapshots`, `POST /boxes/:id/takeover` / `release`.
- `GET /boxes/:id/terminal` WebSocket (text frames in/out, PTY). 
- `GET /jobs/:id`, `GET /logs/:id` (text).
- Agents: `GET/POST /agents`, `GET/PATCH/DELETE /agents/:id`, `GET /agents/:id/snippet?kind=` → `{kind, language, snippet}`.
- Keys: `GET/POST /keys`, `DELETE /keys/:id` (revoke).
- Costs: `GET /costs?agent=&provider=&from=&to=` → ledger list + `summary:{total, by_provider:{}, pool_usd, burst_usd}`; `GET /costs.csv`; `GET /costs/suggestions`.
- Infra: `GET /providers`, `PUT /providers/:id/token` `{token}` (validates), `GET /hosts`, `POST /hosts` `{provider, sku, region}`, `POST /hosts/:id/drain`, `DELETE /hosts/:id`, `GET /offers`.
- `GET/POST /templates`, `PUT/DELETE /templates/:name`, `POST /templates/validate` `{yaml}` → `{ok, errors:[{line,message}]}`.
- `GET/POST /secrets` `{name, value, agents}`, `POST /secrets/:name/rotate` `{value}`, `DELETE /secrets/:name`.
- `GET /approvals`, `POST /approvals/:id/{approve|reject}`.
- `POST /emergency-stop` `{confirm:"DUNG"}` → sleeps all boxes.
- `GET/PUT /settings` `{notify:{telegram_chat_id, email, events:[..], daily_report_hour}, reaper:{idle_minutes, default_ttl_seconds, keep_snapshots}, base_domain, url_token_protect, budget_month_usd, maintenance:{enabled, message}, features:{gpu:false}, disable_tunnel:false}` (`disable_tunnel` since v1.6)
- Admin: `GET /admin/system` (components with status/latency), `GET /admin/jobs`, `GET /admin/audit?actor=&type=` (Events), `GET/PUT /admin/quotas`, `GET /admin/users`.
- Live: `GET /stream` SSE, events `box.updated`, `event.created`, `approval.created`, `overview.updated` (data = JSON object).

## MCP tools (HTTP streamable at `/mcp`, stdio via `box mcp`)
box_up, box_exec, box_job, box_read, box_write, box_grep, box_observe, box_snapshot, box_sleep, box_down, box_list, box_cost — thin wrappers of REST above.

## v1.1 additions (2026-10-26) — multilingual + max agent support
- i18n: every request may send `Accept-Language` (vi, en, zh, ja; default en for API, vi for dashboard). Error `message` and `hint` are localized; `code` never changes. Settings gain `locale`.
- Agent kinds expanded; `GET /agent-kinds` → `{items:[{id, name, category: coding|framework|workflow|platform|custom, transports:[mcp-stdio|mcp-http|rest|sdk-python|sdk-ts|cli|function-calling], doc_url|null}]}`. Ids: claude-code, codex-cli, gemini-cli, cursor, windsurf, cline, openai-agents, langchain, langgraph, crewai, autogen, vercel-ai-sdk, mastra, pydantic-ai, llamaindex, n8n, dify, flowise, make, zapier, hark, custom. `GET /agents/:id/snippet?kind=&lang=` unchanged shape.
- Discovery (no auth): `GET /v1/openapi.json` (OpenAPI 3.1, full), `GET /llms.txt`, `GET /llms-full.txt`, `GET /.well-known/mcp.json` (server card), `GET /.well-known/agent.json` (A2A agent card), `GET /v1/tools?format=openai|anthropic|gemini|mcp` (tool schemas for the 12+ tools).
- New tools/endpoints: `POST /quote` (dry-run placement + price for a spec, no box created) → `{provider, region, cost_per_hour, expected_total_usd, placement_reason, alternatives:[...]}`; `POST /boxes/:id/exec-batch` `{commands:[...], stop_on_error}`; `GET /jobs/:id?wait=30` long-poll; `POST /boxes/:id/restore` and `/fork` `{snapshot_id}` → Box; `GET /boxes/:id/yaml` → `{yaml}`. MCP tools add box_quote, box_exec_batch, box_fork, box_restore, box_logs, box_templates. Exec events include `tokens_saved` and `log_id`.
- Error envelope adds optional `retry_after_s` and `docs_url`. Every successful box_up/exec response may include `next: [ "suggested next call" ]` (short hints for agents).
EOF

## v1.2 additions (2026-10-26) — providers, snapshot storage, notifications, egress, A2A
- Providers: adapters for `digitalocean`, `contabo`, `netcup` (same lifecycle as hetzner/vultr). Provider gains `implemented:bool, chain_rank:int (1-based scheduler fallback order, 0 = not in chain), token_source: env|api`. `GET /providers/:id/regions` → `{items:[{id, name, available}]}`; `GET /providers/:id/servers` → `{items:[{id, name, ip, status, region, sku, tags}]}` (needs a connected token). Tokens: DO = personal access token; Contabo = `client_id:client_secret:api_user:api_password` or JSON with those keys; netcup = SCP refresh token (Boxmes claims pre-ordered servers nicknamed `mgr-spare-*`; legacy `boxa-spare-*` still claimed). Env at startup: `HETZNER_TOKEN, VULTR_API_KEY, DIGITALOCEAN_TOKEN, CONTABO_CLIENT_ID/_SECRET/API_USER/API_PASSWORD, NETCUP_REFRESH_TOKEN`; chain via `BOXMES_PROVIDER_CHAIN` (default `hetzner,vultr,digitalocean,contabo,netcup`). Scheduler: existing pool → first connected provider in the chain (within agent policy); a failed create excludes that provider and tries the next.
- Snapshot storage (S3 API: Cloudflare R2, Backblaze B2, AWS S3, MinIO). Snapshot gains `storage: local|s3, remote_url?, upload_error?`. Core presigns PUT/GET URLs; boxd streams the tar.gz. With `storage=s3` restore/fork work on any host. Retention: `settings.reaper.keep_snapshots` + `settings.reaper.snapshot_retention_days` (0 = off); remote objects are deleted too. `GET /storage` → `{provider: local|r2|b2|s3|minio, endpoint, region, bucket, prefix, access_key_id, configured, source: env|settings|none, has_secret, keep_snapshots, snapshot_retention_days, fallback}`; `PUT /storage` `{provider, endpoint?, region?, bucket?, prefix?, access_key_id?, secret_access_key?}` (secret write-only; `provider:"local"` turns it off; 400 `INVALID` when env-configured); `POST /storage/test` → `{ok, error?, latency_ms, bucket}`.
- Notifications: `settings.notify` gains `timezone` (IANA) and `smtp:{host, port, username, from, tls: starttls|tls|none}`; events: `daily_report, budget, approval, emergency_stop, box_failed, host_down`. `GET /notify` → settings.notify + `{has_telegram_token, has_smtp_password, telegram_configured, email_configured, events_available:[..], recent:[{ts, kind, channel: telegram|email, ok, error?, title}]}`; `PUT /notify` = settings.notify + write-only `telegram_bot_token?, smtp_password?, clear_telegram_token?, clear_smtp_password?` (stored in the encrypted secrets store as reserved `BOXMES_SYS_*`, hidden from `/secrets`); `POST /notify/test` `{channel: telegram|email|all, kind: test|daily_report}` → `{results:[Delivery]}`.
- Egress: `POST /boxes` (and box.yaml, MCP `box_up`) accept `egress:{allow:[domain|*.domain|ip|cidr[:port]], deny_by_default?}` (box.yaml also accepts a plain list). `PUT /boxes/:id/egress` `{allow, deny_by_default}` → Box (empty + false = fall back). Precedence: box → agent `policy.egress_allowlist` (also a ceiling: entries outside it → 403 `POLICY_DENIED`) → `settings.egress_deny_by_default`. Box gains `egress:{allow, deny_by_default, source: box|agent|settings, enforced, mode: incus-acl|nftables|none, rules, warning?, applied_at}|null`. Enforced on incus hosts (Incus network ACL or nftables, DNS to the resolver always allowed); the local driver reports `enforced:false` + `warning`.
- Jobs: status adds `canceled`; `POST /jobs/:id/cancel` → Job.
- A2A JSON-RPC 2.0 at `POST /a2a` (same Bearer auth, protocol 0.3): `message/send` (alias `tasks/send`), `message/stream` (alias `tasks/sendSubscribe`, SSE of `Task` → `status-update` / `artifact-update` → final `status-update`), `tasks/get`, `tasks/cancel`, `tasks/resubscribe` `{id}`. A text part (command) + `metadata.box_id`, or a data part `{box_id, command, cwd?, timeout_s?}`, starts an async exec job — task id = job id; a data part `{tool, arguments}` calls any MCP tool synchronously. States: queued→submitted, running→working, done→completed, failed→failed, canceled→canceled. Errors: -32601 method not found, -32602 invalid params, -32001 task not found, -32002 not cancelable. Agent card: `url=<base>/a2a`, `preferredTransport: JSONRPC`, `capabilities.streaming: true`, skills = `exec` + every tool.

## v1.3 additions (2026-10-26) — smart providers, box selection, speed, mesh
- Provider health: `GET /v1/providers/health`, `GET /v1/providers/{id}/health`, `POST /v1/providers/{id}/validate` (checks a token without saving; lists regions/sizes), `POST /v1/providers/{id}/check`, `POST /v1/providers/{id}/reset`, `GET|PUT /v1/providers/{id}/prefs` (enabled, priority, max $/h, max $/mo, max servers, allowed regions, sandbox_mode). Circuit breaker: 3 consecutive failures or ≥50% failure rate; backoff 60 s → 15 min; Retry-After honoured; auth errors never trip.
- Merged catalog: `GET /v1/catalog` (Battleships feed + provider APIs; live price wins; source, staleness, arch, tier).
- Selection: `GET /v1/profiles` (7 workload profiles + 22 agent-kind defaults), `POST /v1/recommend` (requirements → ranked options with 6-factor score, ETA, price, reasons, rejected options). `box up` accepts `profile` / `requirements` (also in box.yaml).
- Right-sizing: `GET /v1/boxes/{id}/rightsize`, `GET /v1/rightsize`.
- Speed: `GET|PUT /v1/warm-pool`, `POST /v1/boxes/{id}/clone` (CoW), `GET /v1/speed`; placement `BOXMES_PLACEMENT=binpack|spread`; exec session mode + output compaction counted in `tokens_saved`.
- Agent support: `POST /v1/boxes/ensure` (idempotent get-or-create), `GET /v1/boxes/{id}/resume`; create responses carry `estimate` (kind, cost, ETA, actual seconds, warm hit); errors carry `next` (suggested tool calls). MCP adds `box_ensure`, `box_recommend`, `box_clone`.
- Mesh (headscale): `GET /v1/mesh`, `/v1/mesh/config`, `/v1/mesh/acl`, `POST /v1/mesh/keys`; disabled → hint (keys → 400).

## v1.4 additions (2026-10-27) — sandbox API providers (T2/T3)
- Tiers: T1 VPS, T2 sandbox that runs `boxd --dial` inside, T3 exec-only via gateway-side shim. One sandbox hosts one box and is destroyed with it.
- Native adapters: Prized (live-verified), E2B (live-verified, Hobby), Novita (E2B-compatible; live-verified create/exec/files/delete), Daytona, boxd.sh. YAML adapters: Runloop, Blaxel, Vercel Sandbox, boat, Agent 37, Upstash Box (T3). Skipped: Modal (gRPC only), Cloudflare Sandbox (needs a user-deployed Worker), Koyeb (WebSocket exec), zipbox (not verified this release).
- `GET /v1/sandbox/providers` (tier, adapter, verification, capabilities, per-second prices, regions, docs), `POST /v1/sandbox/adapters/lint` (validate a custom YAML adapter). `PUT /v1/providers/{id}/token` returns `group`, `adapter`, `verification`, `docs`.
- `POST /v1/recommend` accepts `untrusted` and `fork`; container-based sandboxes are rejected for untrusted code. Catalog rows carry `tier` and per-second price.
- Env: `BOXMES_SANDBOX_ADAPTERS_DIR`, `BOXMES_SANDBOX_MODE=auto|dial|shim`, `BOXMES_AUTO_SANDBOX`, `BOXMES_PROVIDER_CHAIN` (T1 chain then sandbox chain), `BOXMES_PUBLIC_URL` must be internet-reachable for T2 dial mode.

## v1.5 additions (2026-10-27) — deploying apps on sandbox boxes
- `GET /v1/boxes/{id}/ports` → `{box_id, url, ports:[{port, url, kind: provider-preview|ingress|tunnel|none, public, note?, caveats?:[string]}]}`; `POST /v1/boxes/{id}/ports {port}` exposes a port of a live box (same answer, the new port first). The box `url` is the first port's URL. Sandbox boxes answer with the provider's preview host (E2B `https://<port>-<id>.e2b.app`, Novita `https://<port>-<id>.<domain>`); providers without previews (Prized) go through the public-URL fallback chain below.
- MCP adds `box_url {box_id, port?}` (22 tools); CLI `box url <box> [port]`, `box clone <box> [--slug]`.
- T3 (shim) boxes now support `box_snapshot` / `box_restore` / `box_fork`: the snapshot is a tar.gz of `/work`, `/memory`, `AGENTS.md` kept by core (`location=local://…`, ≤64 MB unless object storage is configured, then uploaded from the sandbox); a T3 snapshot restores into any T3 box and forks onto a fresh sandbox of the same provider.
- `POST /v1/boxes/{id}/clone` on a sandbox box: provider-native fork when the provider has one (E2B, Prized; memory and running processes included), else snapshot → new sandbox → restore (Novita). `placement_reason` names the method.
- `box_sleep` on a sandbox box pauses it at the provider; wake resumes it (also for reaper idle sleep).

## Naming note — Boxaz → Boxmes (2026-11)
- Renamed Boxaz → Boxmes: env `BOXMES_*` (lookup `BOXMES_*` → `BOXAZ_*` → `BOXA_*`, legacy names logged as deprecated), binary `boxmes-core`, reserved secrets `BOXMES_SYS_*` (legacy `BOXAZ_SYS_*` / `BOXA_SYS_*` read), cookies `boxmes_key` / `boxmes_locale` (legacy `boxaz_*` / `boxa_*` read, cleared on logout), ingress token query `boxmes_token` (legacy `boxaz_token` / `boxa_token`), header `X-Boxmes-Token` (legacy `X-Boxaz-Token`), MCP URI scheme `boxmes://` (legacy `boxaz://` read), SDKs `boxmes` / `@boxmes/sdk` (deprecated aliases `boxaz`, `boxa`, `Boxaz*`, `Boxa*`). API paths and shapes are unchanged.
- Renamed without compat: wire protocol `boxmes-wire/1` (core and boxd must match), mesh tags / headscale user / DERP / MagicDNS `boxmes.mesh`, egress names `boxmes-eg-*` / `boxmes_eg_*`, SSH user `boxmes` and host key alias `boxmes-ssh`. Hosts from before the rename need egress rules, mesh ACL and `known_hosts` re-applied. See core/README.md §21. The sections below keep the names of their time (history).

## v1.6 additions (2026-10-27) — rename + public URL fallback
- Renamed Boxa → Boxaz: env `BOXAZ_*` (legacy `BOXA_*` still honoured when the new name is unset, with a deprecation log line), binary `boxaz-core`, reserved secrets `BOXAZ_SYS_*` (legacy `BOXA_SYS_*` read), cookies `boxaz_key` / `boxaz_locale` (legacy `boxa_key` / `boxa_locale` read), ingress token query `boxaz_token` (legacy `boxa_token`). API paths and shapes are unchanged. Provider-visible names stay neutral (`mgr-*`, `managed`, `sandbox-client/1`).
- Public URL fallback for ports of sandbox boxes, first step that works wins:
  1. `provider-preview` — the provider's preview host.
  2. `ingress` — `<BOXMES_PUBLIC_URL>/x/<key>-<port>/` (key = per-box HMAC), forwarded by core's ingress proxy. T2 boxes use their dialled-in boxd; T3 boxes get an in-box agent (`boxd ingress`, downloaded from `<public>/dl/boxd-linux-<arch>`, so `BOXMES_DIST_DIR` must hold the binaries) that dials `GET /v1/ingress/connect` (WebSocket, per-box token). Tried only when `BOXMES_PUBLIC_URL` is set and looks internet-facing (not localhost / `.local` / `.internal` / `.lan` / private IP; override with `BOXMES_INGRESS_ASSUME_PUBLIC=1`) and the agent connects within 25 s. Caveats: path-prefixed (app needs relative links), HTTP only and buffered through core (no WebSocket/streaming, 32 MB body cap), the first request to a sleeping box wakes it.
  3. `tunnel` — a Cloudflare quick tunnel started inside the sandbox (cloudflared downloaded on demand; free, no account) → `https://<random>.trycloudflare.com/`. Caveats: no uptime guarantee, the URL changes if the tunnel restarts (e.g. a wake that lost processes), first request can take a few seconds. Disabled by `settings.disable_tunnel: true` (`GET/PUT /settings`) or `BOXMES_DISABLE_TUNNEL=1`.
  4. `none` — `note` explains, `caveats` lists why each step was skipped.
  `caveats` always names the limits of the returned kind and the skipped steps. After a wake exposures are re-established (agent restarted / tunnel re-checked, new URL if it had to restart); `DELETE /boxes/:id` drops them. A sleeping box keeps reporting its last URL with `note` saying it is re-checked on wake.
- Live (Prized, `scripts/live-publicurl.sh`, 2026-10-27): tunnel — first `GET /ports` 7.9 s, outside curl → 200 in 0.11 s, URL unchanged after sleep→wake; ingress (core behind a cloudflared quick tunnel) — first `GET /ports` 0.53 s, outside curl → 200 in 0.23 s, URL unchanged after wake.


## v1.7 additions (2026-10-27) — supervised apps, gateway URLs with wake on request, URL tokens
- Box gains `wake_on_request` (bool, default true), `port_access` (`{"<port>": "public"|"token"}`, default public) and `app` (AppSpec | null). `PATCH /v1/boxes/{id} {wake_on_request}` changes the wake setting.
- **App supervisor.** AppSpec `{repo?, ref?, install?, start, port?, ready?: {http?: "/path", tcp?: bool}, restart?: always|on-failure|never, env?: {}, workdir?, backoff_max_s?}`. `GET /v1/boxes/{id}/app` → `{box_id, spec, status: {state: cloning|installing|starting|ready|backoff|exited|stopped|sleeping, ready, pid?, restarts, last_exit?, started_at?, ready_at?, message?, supervisor: boxd|script, spec_hash?}}`; `PUT /app` sets + starts it; `DELETE /app` stops + removes it; `POST /app/restart`; `GET /app/logs?offset=&max=` → `{data, offset, truncated?}` (`offset` omitted = tail; `?follow=1` streams SSE events `log` with the same object, `: ping` every ~15 s). The app is started again after wake, restore and clone; gateway traffic for `spec.port` waits until the readiness check passes.
- **Ports.** `PortView` adds `gateway_url` (stable Boxmes URL: `<BOXMES_PUBLIC_URL>/x/<key>-<port>/` or `https://<key>-<port>.<BOXMES_GATEWAY_DOMAIN>/`) and `access` (public | token). A token port reports `kind: gateway`, `url = gateway_url`, `public: false` (the provider preview URL is never handed out for it). `PUT /v1/boxes/{id}/ports/{port} {access}` → PortView.
- **Gateway behaviour.** WebSocket, SSE and chunked bodies pass through unbuffered. A request to a sleeping box wakes it and is held up to `settings.wake.hold_s` (default 10, env `BOXMES_WAKE_HOLD`), then answered; past the hold: an HTML "waking" page for browsers, else `503 BOX_SLEEPING` + `Retry-After` (`settings.wake.retry_after_s`, default 5). HEAD, bots, link previews and probe paths do not wake a box unless `settings.wake.wake_on_probes`. `wake_on_request: false` → 503 without waking.
- **URL tokens.** `POST /v1/boxes/{id}/ports/{port}/tokens {ttl_s? (default 86400, 60…2592000), label?, private? (default true → access=token)}` → 201 URLToken `{id: utok_…, box_id, port, label?, prefix, token (bxt_…, once), url (gateway_url?token=…, once), expires_at, created_at}`. `GET …/tokens` → `{box_id, port, access, gateway_url, items: [URLToken without token/url, + revoked_at?, expired?]}`. `DELETE …/tokens/{token_id}` revokes at once. The token is accepted as `Authorization: Bearer bxt_…`, `X-Boxmes-Token`, or `?token=` / `?boxmes_token=` (exchanged for an HttpOnly cookie via 302; non-`bxt_` query values belong to the app and are kept). Scoped to one box+port; never shares provider credentials.
  - **Caveat:** the token protects only the Boxmes gateway URL. On providers with their own preview host (E2B, Novita, Daytona) the provider preview URL of the same port is still reachable at the provider by anyone who knows the sandbox id. Clients must not treat a token port on those providers as private.
- **Capabilities.** `GET /v1/capabilities` → list of `{provider, group: vps|sandbox, memory_pause, wake_on_http_native, wake_via_gateway, fork, inbound_http, max_runtime (s, 0 = none/undocumented), note?}`.
- MCP: 26 tools (`box_app_set`, `box_app_status {restart?}`, `box_app_logs {offset?, max_bytes?}`, `box_url_token {action: create|list|revoke|public, ttl_s?, label?, token_id?}`); `box_url` answers include `gateway_url` + `access`. CLI: `box app set|status|restart|logs [-f]|rm`, `box url <box> <port> --token [--ttl 1h]`. llms.txt, `/.well-known/mcp.json` instructions and `/.well-known/agent.json` (`x-boxmes.capabilities`, `x-boxmes.url_tokens`) describe the flow and the preview caveat.
- Web: box page tab `ports` (Ports & app) — gateway URL per port, access toggle, issue (ttl + label) / list / revoke tokens, wake_on_request toggle, capability badges, app status + restart + SSE log viewer; mock data with `BOXMES_MOCK=1`.
- Live (2026-10-27, `scripts/live-app.sh`, Prized / E2B): sleep → request → 200 in 426 / 432 ms; kill → ready 1.7 / 1.6 s; restore → ready 2.0 / 1.5 s; clone → ready 1.7 / 1.7 s; token 401 / 200 / 302 / 401 (none / Bearer / `?token=` / revoked) on both.

## v1.8 additions (2026-10-27) — gateway on a real domain, private provider previews
- **Host-based gateway domain.** `BOXMES_GATEWAY_DOMAIN` (env, wins) or `settings.gateway.domain`: port URLs are `https://<key>-<port>.<domain>/` (`key` = 20 hex HMAC). Host routing in core: the `BOXMES_PUBLIC_URL` host and the bare domain → API/dashboard; a label matching `^[0-9a-f]{20}-[0-9]{1,5}$` → that box port; reserved subdomains (`admin, api, app, assets, auth, …, gw, hark, mail, mcp, status, www`, plus `BOXMES_GATEWAY_RESERVED`) and any other label → 404 `NOT_FOUND` (never a box). Explicit DNS records keep priority over the wildcard. One-level wildcard only (free Universal SSL).
- **Settings.** `settings.gateway {domain, provider_preview: ""|auto|private|public}` in `GET/PUT /settings`; invalid domain or mode → 400 `INVALID`. Env `BOXMES_PROVIDER_PREVIEW` wins over `provider_preview`.
- **`GET /v1/gateway[?check=1]`** → `{mode: host|path|relative, domain, domain_source: env|settings|none, public_url, url_pattern, internet_facing, reserved:[..], provider_preview: auto|private|public, provider_preview_source: env|settings|default, private_previews, providers:[{provider, preview_host, private_preview, note}], check?: {ok, status?, latency_ms, error?, checked_at}}` (`check` probes `<BOXMES_PUBLIC_URL>/v1/health`, cached 60 s).
- **Private provider previews.** `provider_preview=auto` (default) = private when the gateway is internet-facing (gateway domain + public URL, or an internet-facing `BOXMES_PUBLIC_URL`). New sandboxes on providers that support it are created refusing public traffic at their preview host: E2B `network.allowPublicTraffic=false`; Novita the same plus `secure: true` (required by Novita). The gateway adds `E2B-Traffic-Access-Token` upstream (a client-sent value is dropped); the token comes from create/fork and is re-fetched with `POST /sandboxes/{id}/connect` after a core restart. Native fork keeps the setting. Providers without a preview host (Prized) are reached only through core; others (Daytona, YAML adapters) stay public.
- **Shapes.** Host gains `private_traffic` (bool). PortView gains `provider_preview: private|public` (absent = no provider preview host); a port of a private sandbox reports `kind: gateway`, `url = gateway_url`, `public: true`; token ports add the "provider preview stays reachable" caveat only when `provider_preview=public`. With a gateway domain, ingress ports (Prized) report the host-based URL instead of `/x/<key>-<port>/`. Capability rows gain `private_preview`.
- **Deploy.** `core/deploy/cloudflare-tunnel/`: `setup.sh` (remotely-managed tunnel via API, ingress `gw.` + `*.` → core, proxied CNAMEs, token file; idempotent), `run.sh`, `up.sh` / `down.sh` (one command to start / stop core + cloudflared).
- Live (2026-10-27, `scripts/live-domain.sh`, boxs.tech via tunnel `5b1d9faa-acb1-4014-bda2-40903ecedb81`): outside → 200 first/warm E2B 275/115 ms, Novita 326/155 ms, Prized 183/109 ms; sleep → request → 200 in 378 / 489 / 531 ms; token 401/200/302/401 on all three; direct provider preview → 403 on E2B and Novita (also while asleep, without waking; clone stays private); SSE 5 events over 2.0 s through the domain; after a core restart E2B URL → 200 in 0.54 s.

## v1.9 additions (2026-10-27) — readable gateway URLs (box name + port label)
- **URL format.** Primary port: `https://<name>.<domain>/`; other ports: `https://<name>-<label>.<domain>/` (one DNS label, so a one-level wildcard + free Universal SSL still covers it). Path mode: `<public>/x/<name>/`, `<public>/x/<name>-<label>/`. The primary port is the app spec port when exposed, else the first exposed port. Raw port numbers never appear in the host.
- **Names.** `Box.subdomain` (string): DNS-safe `^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`, ≤ 40 chars, no `--`, at least one letter, not the legacy `<20 hex>-<port>` shape, not reserved (`ReservedSubdomains` + `BOXMES_GATEWAY_RESERVED`). Unique across the gateway including other boxes' `<name>-<label>` hosts. Default at create: `subdomain` field → `name` → requested `slug` → template id, slugified (accents folded, `đ`→`d`); a taken or reserved derived name gets a word suffix (`demo-app-blue`, `-green`, …; two words next; a letters-only random suffix as the last resort). Clones / forks start from the source name and get a suffix. Explicit names (`POST /boxes {subdomain}`, `PATCH /boxes/{id} {subdomain}`) are slugified; invalid → 400 `INVALID` "invalid box name %q: %s"; reserved → 400 "box name %q is reserved"; taken → 400 "box name %q is taken (%s is in use)"; both reserved and taken carry `next: ["box url <id> --name <free-name>"]`. A rename takes effect at once (old name → 404); terminating a box frees its name. Boxes created before v1.9 are named at core start.
- **Labels.** `Box.port_labels` (`{"<port>": "<label>"}`): `^[a-z]([a-z0-9-]*[a-z0-9])?$`, ≤ 20 chars, unique within the box, `<name>-<label>` unused by other boxes. Defaults from well-known ports (80/3000/4200/4321/5000/5173/8000/8080 → web, then app/site/ui; 3001/4000/8001 → api; 8081/9000 → admin; 9001 → console; 5432 → db; 3306 → mysql; 6379 → redis; 27017 → mongo; 9200 → search; 5672 → amqp; 15672 → rabbitmq; 8888 → jupyter; 7860 → gradio; 8501 → streamlit; 6006 → tensorboard; 11434 → ollama; 9229 → debug; 9090 → metrics; 6080 → vnc; 8765 → ws; 2368 → blog; 1880 → nodered; 5555 → flower), else NATO words (alpha, bravo, …). Set with `POST /boxes/{id}/ports {port, label?}`, `PUT /boxes/{id}/ports/{port} {access?, label?}` (at least one; errors "invalid port label %q: %s", "label %q is already used by port %d", "%s is already in use by another box"), or `AppSpec.label` (needs `port`).
- **Shapes.** `PortView` adds `label`, `primary` (bool), `alias_url` (legacy `<key>-<port>` URL, still served); `url` / `gateway_url` use the readable form. `PortsResult` adds `name`, `primary_port`. `GatewayInfo.url_pattern` = `https://<name>.<domain>/`, plus `label_pattern` (`https://<name>-<label>.<domain>/`) and `alias_pattern` (`https://<key>-<port>.<domain>/`). `/.well-known/agent.json` `x-boxmes.gateway_urls` describes the format.
- **Routing.** Legacy `<key>-<port>` labels resolve first; reserved labels → 404; any other DNS-label-shaped host is looked up among running / sleeping boxes (`<name>` → primary port, `<name>-<label>` → that port), else 404 `NOT_FOUND`. Token access, wake-on-request, readiness gating and private provider previews are unchanged.
- **Security note.** Name URLs are guessable (the legacy key was an HMAC); protect private apps with URL tokens (`access=token`).
- CLI: `box url <box> [port] --name <name>` (rename), `--as <label>` (or `--label` without `--token`) to label the port; listing shows the label (`*` = primary) and `alias:`. MCP `box_url {name?, label?}`, `box_app_set {label?}`. SDKs: Python `rename_url`, `set_port_label`, `expose(..., label=)`, `set_app(..., label=)`; TypeScript `renameUrl`, `setPortLabel`, `expose(id, port, label?)`, `AppSpec.label`.
- Web: Ports tab — box address card (open/copy primary URL, rename), label + primary badges, edit-label form, old address row, label field on Expose; Settings → Gateway card shows the label and alias patterns and the guessable-name note; mock mode implements names/labels; 4 locales.
- Live (2026-10-27, `scripts/live-names.sh`, E2B): `https://demo-app.boxs.tech/` → 200 first/warm 290/143 ms; alias → 200; `https://demo-app-files.boxs.tech/` (port 8001, label files) → 200; sleep → request → 200 in 577 ms; down → 404; E2B `[]` afterwards.

## v1.10 additions (2026-10-27) — box access: single-use tickets, durable terminal, TCP forward, SSH
- **Access tickets.** `POST /v1/boxes/{id}/tickets {purpose: "terminal"|"tcp"|"ssh", port? (tcp), session_id? (terminal), ttl_s? (default 60, 10…86400), allow? (tcp: add the port to the forward allowlist)}` → 201 `AccessTicket {id: "tkt_…", box_id, purpose, port?, session_id?, prefix, ticket: "bxa_<box>_<40 hex>", url (WebSocket on the box host with ?ticket=, or core when no gateway domain), core_url, command? (ssh), expires_at, created_at, actor, key_id?}`, `Cache-Control: no-store`. `ticket` / `url` / `command` are returned only here. Redeemable once before `expires_at`, for that box + purpose (+ port, + session). Errors: unknown purpose / bad port / ttl → 400 `INVALID`; tcp port not allowed without `allow` → 403 `POLICY_DENIED`; box gone → 404; box under human takeover (non-admin) → 403. Scope `box:exec`.
- `GET /v1/boxes/{id}/tickets` → `{items: [AccessTicket without ticket/url, + redeemed_at?, revoked_at?, live]}`; `DELETE /v1/boxes/{id}/tickets/{tid|all}` → `{revoked: n}` and closes every session bound to the revoked tickets.
- **Socket auth.** `GET /v1/boxes/{id}/{terminal|tcp|ssh}` and `wss://<name>.<domain>/.boxmes/{terminal|tcp|ssh}` accept `?ticket=bxa_…` or `Authorization: Bearer <ticket | API key>`. An API key in the URL (`?token=`) or the dashboard cookie → 401 `UNAUTHORIZED`. Browser `Origin` must be allowed (`BOXMES_DASHBOARD_URL`, `BOXMES_PUBLIC_URL`, `BOXMES_ACCESS_ORIGINS`, explicit CORS origins; loopback when core is on loopback; no Origin = native client) else 403 `POLICY_DENIED`, checked before the ticket is consumed.
- **Durable terminal.** Query: `ticket`, `session` (reattach; must match a session-bound ticket), `mode=control|view` (default control), `proto=2`, `cols`, `rows`, `name`. Sessions survive drops for `BOXMES_TERMINAL_DETACH_TTL` (default 10 m) and replay up to `BOXMES_TERMINAL_BUFFER` (256 KiB) on attach; the newest controller wins; opening wakes a sleeping box; sleep closes sessions. `proto=2`: output = binary frames; input = binary frames (text input ignored); JSON text frames — server → `{"type":"session","id","created","control","replay","cols","rows","viewers","detach_ttl_s"}`, `{"type":"viewers","n","control"}`, `{"type":"control","control":false}`, `{"type":"exit","code"?,"reason"?}`, `{"type":"ping"}` / `pong`; client → `{"type":"resize","cols","rows"}` (controller only), `{"type":"control","control":bool}`, `{"type":"ping"}`. Without `proto=2` the v1 text protocol is kept.
- `GET /v1/boxes/{id}/terminals` → `{items: [TerminalSession {id: "term_…", box_id, name?, cols, rows, viewers, controlled, created_at, last_io, detached_at?, expires_at?, exited?, exit_code?, buffered_bytes, actor?}], detach_ttl_s}`; `DELETE /v1/boxes/{id}/terminals/{sid|all}` → `{killed: n}` (404 unknown sid).
- **TCP stream.** `…/tcp?port=N`: binary frames = raw TCP bytes, server pings every 25 s. Allowed ports = exposed ports + the internal SSH port + `Box.forward_ports`. `POST /v1/boxes/{id}/forward-ports {port}` → `{box_id, allowed_ports}`; `PATCH /v1/boxes/{id} {forward_ports: [..]}` replaces the extra list (≤ 32). `Box.forward_ports` (int[]).
- **SSH gateway.** `…/ssh` carries an SSH session (core is the SSH server; user `boxmes`; auth = the ticket that opened the socket, or a registered key on the direct listener `BOXMES_SSH_ADDR` where the ticket may be the username). Shell, exec, pty, env, window-change, signals, `direct-tcpip` (ssh -L), sftp subsystem when the box has `sftp-server`; no remote forwarding / agent / X11. `GET /v1/ssh` → `{known_hosts, host_key_fingerprint, host_key_alias: "boxmes-ssh", direct, proxy_command: "box ssh-proxy %h", domain}`. `GET /v1/ssh/keys` → `{items: [SshKey {id: "sshk_…", name, public_key, fingerprint, agent_id?, key_id?, created_at}]}`; `POST /v1/ssh/keys {name, public_key}` → 201 (invalid → 400 `INVALID`; adding the same key again returns the existing one); `DELETE /v1/ssh/keys/{kid}` → `{deleted: true}`. Agent keys see / use only their own keys and boxes.
- **Access summary.** `GET /v1/boxes/{id}/access` → `AccessInfo {box_id, name, host?, link: boxd|agent|agent-on-demand|none, live: {terminal?, tcp?, ssh?}, tcp: {url, core_url, allowed_ports, auth}, terminal: {url, core_url, detach_ttl_s, sessions}, ssh: {url, core_url, host, proxy_command, command, config, known_hosts, host_key_fingerprint, direct?, features}, commands: {forward, ssh, ssh_exec, ssh_config, ssh_proxy, ssh_direct?}, notes?, tickets}`.
- **Reserved names** add `ssh`, `tcp`, `term`, `cdp`, `vnc`, `ide`.
- CLI: `box access`, `box ticket <box> terminal|tcp|ssh [--port N] [--ttl 60s] [--json]`, `box forward <box> <remote-port> [local-port] [--bind]`, `box ssh <box> [ssh args…] [-- cmd]`, `box ssh-proxy <host> [port]`, `box ssh-config [--write] [box…]`, `box ssh-key add <file.pub> [--name] | ls | rm <id>`, `box terminals <box> [--kill <sid>|all]`. MCP (30 tools): `box_ticket`, `box_forward`, `box_ssh_info`, `box_terminal_sessions`. SDKs: `access`, `ticket`, `tickets`, `revoke_ticket`, `allow_forward_port`, `terminals`, `kill_terminal`, `ssh_info`, SSH keys (TS camelCase).
- Discovery: `llms.txt` / `llms-full.txt` section "Box access" (vi/en/zh/ja by `Accept-Language` / `?lang=`); A2A card `x-boxmes.access` + localized `x-boxmes.access_detail`; MCP `instructions` localized.
- Web: Terminal tab fetches a ticket through the Next proxy (key stays server-side) and opens `…/terminal?ticket=…&proto=2&mode=…[&session=…]` (URL from `NEXT_PUBLIC_BOXMES_WS`, else the ticket's `core_url`, else `NEXT_PUBLIC_BOXMES_API`); session picker (list / reattach with replay / new / kill), control vs view mode, auto-reconnect with a fresh ticket, wake notice. Access tab (commands, ssh-config, forward ports, tickets + revoke all), Settings → SSH keys. Mock mode implements every new REST route (no WebSocket: simulated shell + notice); 4 locales.
- Live (2026-10-27, `scripts/live-access.sh`, E2B / Prized through boxs.tech): terminal ticket → first output 1.83 s / 0.81 s; reattach with replay 278 / 261 ms; `box forward` → curl via localhost 318 / 225 ms; `box ssh -- uname -a` 608 / 522 ms; `ssh -L` 683 / 612 ms.

## v1.11 additions (2026-10-28) — box access P1: SFTP, SSH certificates, browser (CDP), desktop (VNC), share links
- **SFTP always.** The `sftp` subsystem of the SSH gateway uses the box's `sftp-server` when present, else core serves SFTP itself over the box's shell commands (`BOXMES_SFTP=auto|core|box`). OpenSSH `sftp` / `scp` work through `ProxyCommand box ssh-proxy %h`; `box sftp <box> put|get|ls|rm|mkdir` uses a built-in client.
- **SSH user certificates.** `POST /v1/ssh/certs {box, public_key, ttl_s? (default 3600, 60…43200)}` → 201 `SshCert {id: "tkt_…", box_id, certificate ("<type>-cert-v01@openssh.com …", save as <key>-cert.pub), serial, principals: [box id, subdomain?, slug?], valid_after, valid_before, fingerprint, host, user, proxy_command: "box ssh-proxy --anon %h", known_hosts, command, direct_command?, config, user_ca}`. Extensions `permit-pty`, `permit-port-forwarding`. Authenticates on `…/ssh` WebSockets **without a ticket** and on the direct listener; revocable as a ticket (purpose `ssh_cert`) via `DELETE /v1/boxes/{id}/tickets/{id|all}` (closes open sessions). Errors: missing box / bad key / certificate passed / ttl out of range → 400 `INVALID`; box gone → 404; takeover (non-admin) → 403. `GET /v1/ssh` adds `user_ca`, `cert_max_ttl_s`, `sftp`.
- **Ticket purposes** add `cdp` (reusable until `expires_at`), `vnc` (single use), `vnc_share` (share links, token `bxs_<box>_<40 hex>`), `ssh_cert` (certificate record).
- **Browser (CDP).** `POST /v1/boxes/{id}/browser {ttl_s? (default 3600, ≤ 86400), headless?}` → 201 `BrowserSession {box_id, running, headless, source?: found|installed|existing, binary?, version? (/json/version), started, start_ms?, clients, idle_stop_s, port (9222), note?, cdp_url ("wss://<name>.<domain>/.boxmes/cdp?ticket=…", or core `/v1/boxes/{id}/cdp?ticket=…` without a gateway domain), http_url, core_url, ticket_id, expires_at, live_view?: "desktop"}`, `Cache-Control: no-store`. Wakes the box, finds or installs Chromium (`BOXMES_BROWSER_INSTALL=off` disables install), headless unless the desktop runs. `GET /v1/boxes/{id}/browser` → `BrowserState` (no wake, no start; `note` when the box is not running); `DELETE` → `{box_id, stopped, note?}`. Idle stop `BOXMES_BROWSER_IDLE` (10 m without clients). The CDP proxy (`/.boxmes/cdp[/json/…|/devtools/…]`, `/v1/boxes/{id}/cdp…`) rewrites `/json/version|list|new` URLs to the gateway and rewrites `Page.navigate` / `Target.createTarget` to cloud-metadata addresses to an unresolvable host.
- **Desktop (VNC).** `POST /v1/boxes/{id}/desktop {view_only?, width?, height?, ttl_s? (viewer ticket, default 60)}` → 201 `DesktopSession {box_id, running, display?, width?, height?, vnc_server?, wm?, started, start_ms?, viewers, shares, idle_stop_s, port (5900), browser_on_desktop, note?, vnc_url ("wss://…/.boxmes/vnc?ticket=…"), viewer_url ("https://<name>.<domain>/.boxmes/desktop?ticket=…[&view_only=1]", or core `/v1/boxes/{id}/desktop/view?ticket=…`), core_url, ticket_id, view_only, expires_at}`. `GET` → `DesktopState`; `DELETE` → `{box_id, stopped, note?}` (closes viewers). Idle stop `BOXMES_DESKTOP_IDLE` (15 m without viewers). The viewer page is vendored noVNC served by core (`/.boxmes/novnc/…`, `/v1/novnc/…`) with a strict CSP; `frame-ancestors` = `'self'` + dashboard / access origins for tickets, `*` for share links. View-only is enforced in core (client→server RFB input dropped).
- **Share links.** `POST /v1/boxes/{id}/desktop/shares {ttl_s? (default 3600, 60…86400), view_only? (default true)}` → 201 `DesktopShare {id: "shr_…", box_id, url ("…/.boxmes/desktop?share=bxs_…", create response only), prefix, view_only, expires_at, created_at, revoked_at?, viewers, uses, actor?}`. `GET …/desktop/shares` → `{items: [DesktopShare without url]}`; `DELETE …/desktop/shares/{sid|all}` → `{revoked, viewers_closed}` (404 unknown sid); a revoked or expired link answers **410**, an invalid one 401.
- **Access summary** adds `cdp: AccessAddon {url, core_url, start, running, clients}`, `desktop: AccessAddon {url, core_url, page, start, running, clients}`, `live.cdp`, `live.vnc` and `commands.{ssh_cert, sftp, scp, browser, desktop, desktop_share}`.
- **Security.** Bare gateway domain is not core; box `Set-Cookie` `Domain` attributes are stripped; origins that are box hosts never open access sockets; box egress blocks cloud metadata addresses by default (`BOXMES_EGRESS_ALLOW_METADATA=1` opts out).
- CLI: `box ssh-cert <box> [--key] [--ttl] [--out] [--json]`, `box ssh-config --cert [--ttl]`, `box ssh-proxy --anon <host>`, `box sftp <box> put|get|ls|rm|mkdir`, `box browser <box> [--ttl] [--status] [--stop] [--json]`, `box desktop <box> [--share [--ttl] [--interactive]] [--shares] [--revoke-shares] [--status] [--stop]`. MCP (32 tools): `box_browser`, `box_desktop`; `box_ssh_info {public_key?, ttl_s?}` signs a certificate. SDKs: `ssh_cert`, `browser`, `cdp_url`, `connect_playwright`, `browser_status`, `stop_browser`, `desktop`, `desktop_status`, `stop_desktop`, `share_desktop`, `desktop_shares`, `revoke_desktop_shares` (TS camelCase).
- Discovery: `llms.txt` / `llms-full.txt` section "Box access P1" (vi/en/zh/ja); A2A `x-boxmes.access` lists the v1.11 tools and `x-boxmes.access_detail` includes the localized P1 text.
- Web: Desktop tab (viewer ticket fetched through the Next proxy, noVNC page in an iframe, start/stop, interactive vs view-only, reconnect, full screen, new tab; share links: create with TTL, copy-once URL, list, revoke one/all), Browser tab (start/stop, TTL, force headless, copy CDP URLs, Playwright / puppeteer / SDK / curl / CLI snippets), Access tab adds SSH certificate + SFTP/scp cards. Mock mode implements every new REST route (no VNC/CDP sockets: simulated screen + notice); 4 locales.
- Live (2026-10-27/28, `scripts/live-access-p1.sh`, E2B / Prized through boxs.tech): `box sftp` put/get 1 MB 2.9 s / 0.84 s (E2B), 2.7 s / 0.99 s (Prized); OpenSSH sftp / scp ~0.9 s; certificate SSH 335 / 320 ms; E2B browser first start 17.4 s, warm 215–464 ms, CDP connect 222 ms, `Browser.getVersion` 35 ms; desktop first 6.6 s, warm 324 ms, VNC handshake 316–452 ms, first framebuffer update 83–101 ms; share revoke → 410 in 470 ms. Retest 2026-10-28: E2B 512 MB browser with low-memory flags — install 18.4 s, getVersion 59 ms, navigate example.com → "Example Domain" 408 ms, warm 256 ms; Prized (rootless, no apt lists) still fails: the private apt index now downloads the library .debs but Chromium still misses libraries, and the desktop needs root.

## v1.12 additions (2026-11-19) — Render provider (dial-only)
- New native sandbox adapter `render` (Render web services, `api.render.com/v1`, docs-verified against the official OpenAPI spec; mock-tested only, no live run yet). Env: `RENDER_API_KEY` (required), `RENDER_OWNER_ID`, `RENDER_AGENT_IMAGE`, `RENDER_DISK_GB`; JSON credential fields `token`, `owner_id`, `image`, `disk_gb`, `preview_port` (default 8000, `0` = none), `keep_awake` (free plan, default `1`).
- **Dial-only.** Render has no exec/file API for services, so a box is a web service running a stock image (`docker.io/library/buildpack-deps:bookworm-scm` unless overridden) whose start command downloads boxd from core and runs `boxd --dial` in the foreground. `GET /v1/sandbox/providers` items gain `dial_only` and `ephemeral_restart` (bools, omitted when false); a dial-only provider always reports `mode: dial` (prefs `sandbox_mode=shim` is ignored) and `blocked: "needs BOXMES_PUBLIC_URL …"` when core has no public URL; `/recommend` options of a dial-only provider are blocked for the same reason.
- **Fixed-price sizes.** `sizes[]` items may carry `fixed_price: true`, `price_hour` (USD/h, flat plan price ÷ 730, prorated per second) and `note`; catalog offers use that price instead of the per-second rates. Render plans: `free` $0 (0.1 CPU / 512 MB), `0.5c-512mb` $7/mo … `8c-32g` $450/mo; regions oregon, ohio, virginia, frankfurt, singapore. `from_usd_h` is `0` for Render (free plan).
- **Capabilities.** `pause/snapshot/fork/files: false`, `ports: true` (the service's `https://<name>.onrender.com` serves one in-box port, public at the provider), `isolation: container`. Sleep stops the box's processes through boxd (the service keeps running); a persistent box is allowed (no lifetime cap). Snapshots are boxd tarballs.
- **Restarts.** For `ephemeral_restart` providers the host's bootstrap token stays valid for that host, so boxd rejoins after a platform restart / free-plan spin-down; boxes whose files were lost are failed with `box missing on host after reconnect`.
- boxd: `BOXD_LISTEN_PORT` (may be `${PORT}`), `BOXD_HEALTH_PATH`, `BOXD_PREVIEW_PORT`, `BOXD_KEEPALIVE_URL` (may reference `${RENDER_EXTERNAL_URL}`), `BOXD_KEEPALIVE_EVERY` (default 5m): port holder that answers the health path and forwards other requests (WebSocket included) to the preview port; keepalive requests the service's own URL so a free service is not spun down while the box exists.

## v2 tenancy (auth layer)

- Public: `GET /auth/config` `{signup_mode: invite|open|closed, first_user, min_password_len, email_verification, email_configured}`; `POST /auth/signup` `{email, password, name?, invite_code?, workspace?}` → 201 `{token: "bxs_…", expires_at, user, workspace, role}`; `POST /auth/login` `{email, password}` → same shape; `POST /auth/verify-email` `{token}`; `POST /auth/password/forgot` `{email}` → 202 always; `POST /auth/password/reset` `{token, password}`; `POST /waitlist` `{email, note?}` → 201 `{ok, email, position}`.
- Session (`Authorization: Bearer bxs_…`): `POST /auth/logout[?all=1]`, `GET /auth/me` `{user, workspace:{…, role}, workspaces:[…]}`, `PATCH /auth/me` `{name}`, `POST /auth/password` `{current_password, new_password}`, `POST /auth/verify-email/resend` → 202, `POST /auth/workspace` `{workspace_id}`, `GET /workspaces`.
- Platform admin (admin key, or session of a `platform_admin` user, which also gets the whole v1 operator API): `GET /admin/users` → `[{id, email, name, email_verified, platform_admin, created_at, workspaces:[{id, name, role, max_boxes, max_ttl_hours}]}]`, `PATCH /admin/users/{id}` `{platform_admin?, email_verified?}`, `DELETE /admin/users/{id}`, `GET /admin/invites` (`+redemptions`), `POST /admin/invites` `{code?, max_uses=1, expires_in_hours=0, note?, email?}` → 201, `DELETE /admin/invites/{id|code}`, `GET /admin/waitlist`, `POST /admin/waitlist/invite` `{emails[] | count, expires_in_hours=336}` → `[{email, code, invite_id} | {email, error}]`, `DELETE /admin/waitlist/{email}`.
- Errors: no/invalid invite or closed sign-up → `POLICY_DENIED`/`INVALID` with `next: ["POST /v1/waitlist {email}"]`; wrong credentials → `UNAUTHORIZED`; 10 failures / 15 min per email → `RATE_LIMITED`. Sessions of non-admin users get `UNAUTHORIZED` on every other route until resources are workspace-scoped.
- Env: `BOXMES_SIGNUP_MODE`, `BOXMES_SESSION_TTL` (default 720h); mail links use `BOXMES_DASHBOARD_URL` (fallback `BOXMES_PUBLIC_URL`).

## v2.0 additions (2026-11-27) — tenancy: workspace isolation, plans & quotas, roles, members, invites, workspace keys, box locations

Every agent, box, key, secret, template, approval, event and ledger row belongs to one workspace (`workspace_id`; pre-v2.0 data lives in `ws_default`, plan `unlimited`). Member sessions now reach the **whole** API (including `/mcp`, `/a2a`) with their role; the "UNAUTHORIZED until resources are workspace-scoped" limitation of the auth layer is gone. Contract tests: `core/internal/e2e/v200_tenancy_test.go`.

### Callers and scope
- **Platform** (admin key, session of a `platform_admin` user): sees every workspace. `?workspace=<id>` on any route narrows reads to that workspace, and creates land there (else the session's current workspace, else `ws_default`).
- **Workspace** (member session `bxs_…`, workspace key `bx_ws_…`): sees only its workspace; `?workspace=` is ignored. Permissions come from the member's role or the key's scopes.
- **Agent key** (`bx_agent_…`): only its own agent's records inside the agent's workspace (unchanged).

### Roles and permissions
Permissions: `view`, `boxes:write`, `exec`, `agents:write`, `secrets:read`, `secrets:write`, `secrets:use`, `templates:write`, `billing:read`, `members:write`, `audit:read`, `emergency_stop`, `workspace:write`, `approvals:decide`.

| Role | Permissions |
|---|---|
| `owner` | all |
| `admin` | all except `billing:read` |
| `developer` | `view`, `boxes:write`, `exec`, `agents:write` (only agents/keys they created), `secrets:read`, `secrets:use`, `templates:write` |
| `billing` | `view`, `billing:read` |
| `viewer` | `view`, `secrets:read` |
| `auditor` | `view`, `secrets:read`, `audit:read` |

Legacy role `member` reads as `developer`. Only owners grant, change or remove the `owner` role; the last owner can be neither demoted nor removed (400 `INVALID`).

### Plans and quotas
`Quota`: `{plan, max_boxes, max_cpu_per_box, max_ram_mb_per_box, max_ttl_hours, public_url, egress: "restricted"|"open", max_members, max_agents, max_keys, max_secrets, max_boxes_per_minute}` — `0` = unlimited.

| | free | pro | team | unlimited |
|---|---|---|---|---|
| `max_boxes` (concurrent, non-sleeping) | 1 | 10 | 50 | 0 |
| `max_cpu_per_box` | 1 | 8 | 16 | 0 |
| `max_ram_mb_per_box` | 2048 | 16384 | 32768 | 0 |
| `max_ttl_hours` | 2 | 24 | 168 | 0 |
| `public_url` | false | true | true | true |
| `egress` | restricted | open | open | open |
| `max_members` (members + pending invites) | 1 | 3 | 0 | 0 |
| `max_agents` (excl. implicit `operator`) | 3 | 25 | 0 | 0 |
| `max_keys` (active workspace + agent keys) | 5 | 50 | 0 | 0 |
| `max_secrets` | 10 | 100 | 0 | 0 |
| `max_boxes_per_minute` | 5 | 30 | 120 | 0 |

- New workspaces get the instance default plan `BOXMES_DEFAULT_PLAN` (default `free`; an empty `plan` on a workspace also means this default). `ws_default` is `unlimited`.
- Effective quota = plan quota with the workspace's `quota_overrides` applied (any subset of the limit fields).
- Checked on box create / fork / clone / restore (cpu, ram, ttl, concurrency, create rate), on wake (takes a concurrency slot again), and when adding agents, keys, secrets and invites. A create without `ttl_seconds` gets the plan's TTL cap.
- `public_url: false`: new ports default to URL-token access; `PUT /boxes/:id/ports/:port {access:"public"}` → `QUOTA_EXCEEDED`.
- `egress: restricted`: a box without an egress policy gets the plan allowlist (package registries, git hosts, OS mirrors; `deny_by_default: true`, `source: "plan"`); an explicit allowlist must stay inside it, else `QUOTA_EXCEEDED` with `details.host`.
- **Error** `QUOTA_EXCEEDED` (HTTP 403): `{"error": {"code": "QUOTA_EXCEEDED", "message", "hint", "details": {"quota": "<limit name>", "limit", "used"?, "plan", "upgrade_url", "host"?}, "next": ["GET /v1/workspaces/current/usage", "GET /v1/plans"], "retry_after_s"?: 60}}` (`retry_after_s` only for `max_boxes_per_minute`; `upgrade_url` = `<BOXMES_DASHBOARD_URL>/settings/billing`). Owning more than 10 workspaces on `POST /workspaces` is also `QUOTA_EXCEEDED`.
- A `suspended` workspace may only read and use `/auth/*` / workspace routes; other writes → 403 `POLICY_DENIED`.

### Objects
- `Workspace`: `{id, name, owner_user_id, plan?, quota_overrides?, status?: active|suspended, created_at, created_by?, max_boxes, max_ttl_hours}` (`max_boxes`/`max_ttl_hours` are pre-v2.0 fields, superseded by `quota`).
- `CurrentWorkspace` = Workspace + `{role, plan, quota: Quota, permissions: [perm], member_count}`.
- `Me`: `{user: {id, email, name, email_verified, platform_admin, …}, workspace: (Workspace + {role}) | null, workspaces: [Workspace + {role}], role, permissions: [perm], platform_admin}`.
- `Usage`: `{workspace_id, plan, quota: Quota, used: {boxes (non-sleeping), boxes_total, agents, keys, secrets, members, spent_today_usd, spent_month_usd}}`.
- `Member`: `{user_id, email, name?, role, email_verified, joined_at}`.
- `WorkspaceInvite`: `{id, workspace_id, email, role, invited_by, expires_at, accepted_at|null, accepted_by?, created_at, status: pending|accepted|expired}`; create adds `{invite_url, mailed}` (token shown once).
- `InvitePreview`: `{workspace: {id, name}, email, role, invited_by, expires_at, status, account_exists}`.
- `ApiKey` adds `{workspace_id?, kind: platform|workspace|agent, expires_at|null, created_by?}`.
- `Box` adds `workspace_id` and `location: BoxLocation | null` (absent until placed).
- `BoxLocation`: `{region: eu|us|apac|sandbox|local|other, country? (ISO alpha-2), city?, provider, site: "<provider>-<code>" (e.g. hetzner-fsn), host?}`.
- `Event` rows of the workspace audit carry `workspace_id`; tenancy event types: `workspace.created`, `workspace.updated`, `member.invited`, `member.invite_revoked`, `member.joined`, `member.role_changed`, `member.removed`, `key.created`.

### Endpoints
Public (no auth):
- `GET /plans` → `{items: [{id: free|pro|team|unlimited, quota: Quota}]}`.
- `GET /workspace-invites/:token` → `InvitePreview`; unknown / malformed token → 404 `NOT_FOUND`.
- `POST /auth/signup` accepts `workspace_invite: "bxi_…"`: the account joins that workspace with the invited role (no invite code needed, no personal workspace); the email must match the invite.

Session (`bxs_…`):
- `GET /auth/me` → `Me`.
- `POST /auth/workspace` `{workspace_id}` → `Me` (switches the session; not a member → 404).
- `GET /workspaces` → `{items: [Workspace + {role}]}`.
- `POST /workspaces` `{name}` (1–60 chars) → 201 `CurrentWorkspace` (caller is owner; does not switch the session).
- `POST /workspace-invites/:token/accept` → `Me` (joins and switches). Other email → 403 `POLICY_DENIED`; used / expired → 400 `INVALID`.

Current workspace (session or workspace key; platform callers act on `?workspace=` or their current workspace):
- `GET /workspaces/current` → `CurrentWorkspace` (`view` or `billing:read`).
- `PATCH /workspaces/current` `{name?, plan?, quota_overrides?, status?}` → `CurrentWorkspace`. `workspace:write`; `plan`, `quota_overrides`, `status` are platform-admin only (403 `POLICY_DENIED`). Unknown plan / bad egress / bad status → 400.
- `GET /workspaces/current/usage` → `Usage` (`view`).
- `GET /workspaces/current/audit?actor=&type=&box=&agent=&limit=` → `{items: [Event]}` (`audit:read`).
- `GET /workspaces/current/members` → `{items: [Member]}` (`view` or `billing:read`).
- `PATCH /workspaces/current/members/:user_id` `{role}` → `Member` (`members:write`; owner rules above; unknown role → 400; not a member → 404).
- `DELETE /workspaces/current/members/:user_id` → `{ok: true}` (`members:write`, or anyone with their own user_id to leave).
- `GET /workspaces/current/invites` → `{items: [WorkspaceInvite]}` (`members:write`).
- `POST /workspaces/current/invites` `{email, role? = developer}` → 201 `WorkspaceInvite + {invite_url, mailed}` (`members:write`; inviting as owner needs an owner; already a member → 400; counts against `max_members`; a new invite for the same email replaces the open one; valid 7 days; link `<BOXMES_DASHBOARD_URL>/invite/<token>`, mailed when SMTP is configured).
- `DELETE /workspaces/current/invites/:id` → `{ok: true}` (`members:write`; other workspace → 404).

Keys:
- `POST /keys` `{name?, scopes?, expires_in_days?, expires_at?, agent_id?, workspace_id? (platform only), platform? (platform only)}` → 201 `ApiKey` with `key` once. Without `agent_id` from a workspace caller → workspace key (`kind: "workspace"`, prefix `bx_ws_`). Workspace scopes: `boxes:read` (view), `boxes:write` (view + boxes + templates), `exec`, `secrets:read` (read + use), `secrets:write`, `billing:read`, `admin` (all permissions inside the workspace); default `boxes:read`, `boxes:write`, `exec`. Needs `agents:write`; a key never grants more than its creator holds (403 `POLICY_DENIED`); unknown scope → 400; `expires_at` in the past or `expires_in_days < 0` → 400; counts against `max_keys`. Expired keys → 401 `UNAUTHORIZED` ("expired").
- `GET /keys` → keys of the caller's scope; `DELETE /keys/:id` (developers: only keys they created).

Boxes:
- `GET /boxes?region=&location=` (with the existing `status`, `agent`, `provider`, `lifecycle`, `all`): `region` matches `location.region`, `location` matches `location.site` (case-insensitive).
- `GET /boxes/locations` → `{items: [BoxLocation + {running, sleeping, total, cost_today_usd}] (per site, host omitted, sorted by total desc), regions: [{region, running, sleeping, total, cost_today_usd}], total: {region: "all", running, sleeping, total, cost_today_usd}}` (active boxes; terminated boxes only contribute cost; cost = UTC day).

Platform admin:
- `GET /admin/workspaces` → `{items: [Workspace + {plan, members, boxes, spent_month_usd}]}`.
- `PATCH /admin/workspaces/:id` `{name?, plan?, quota_overrides?, status?}` → `CurrentWorkspace` (unknown id → 404).

### Error semantics
- Records of another workspace → **404 `NOT_FOUND`**, never 403 (boxes, agents, keys, secrets, templates, approvals, invites, switching to a foreign workspace).
- Session whose role lacks a permission → **403 `POLICY_DENIED`** ("your role … does not allow …"); a user with no workspace → 403 with hint `POST /v1/workspaces`.
- Workspace / agent key missing a scope → **401 `UNAUTHORIZED`** ("key lacks scope …"); missing, invalid or expired key → 401. `/admin/*` for non-platform callers → 401.
- Plan limits → **403 `QUOTA_EXCEEDED`** with `details` (above).

### Known gaps
- `max_boxes_per_minute` is counted in core's memory (reset on restart, per process).
- Plan egress is enforced when a box is created and on `PUT /boxes/:id/egress`; changing a plan does not re-apply it to running boxes.
- Agents and boxes cannot be moved between workspaces.
- Plan prices / billing land in v2.1 (`GET /plans` returns quotas only).

### SDKs
Python (`boxmes`): `me`, `switch_workspace`, `list_workspaces`, `create_workspace`, `get_workspace`, `rename_workspace`, `workspace_usage`, `workspace_audit`, `plans`, `list_members`, `update_member_role`, `remove_member`, `create_invite`, `list_invites`, `revoke_invite`, `get_invite`, `accept_invite`, `create_key(agent_id=None, name, scopes, expires_in_days=, expires_at=)`, `list_keys`, `revoke_key`, `list_boxes(region=, location=, workspace=)`, `box_locations`, `admin_workspaces`, `admin_update_workspace`; `BoxmesError.details`. TypeScript (`@boxmes/sdk`): same in camelCase; `createKey(agentId, name, scopes, {expires_in_days, expires_at, workspace_id})`; `BoxmesError.details`.

## v2.2 additions (2026-11-27) — infra: provider accounts (M29 phase A)

A provider (adapter: `hetzner`, `vultr`, `e2b`, …) now has one or more **accounts**: each is one API credential of one real account / project / team at that provider. Hosts remember the account that created them and every later call (status, destroy, preview URL/headers, native fork, shim re-attach) uses **that** account's key, whatever the provider's default is by then. Design: `docs/boxmes-infra-accounts-spec.md` (decisions locked 2026-11-27). Contract tests: `core/internal/e2e/v220_accounts_test.go`.

All routes below are **platform admin only** (`/v1/admin/*`: admin key or `platform_admin` session; others → 401). Keys are write-only: no response, event or log carries a credential — only `key_fingerprint` (sha256 prefix + `…` + last 4 characters; keys shorter than 12 characters show no tail).

### Compatibility
- `PUT /providers/{id}/token` still works: it writes the provider's **default account** `pa_<provider>_default` (`is_default: true`, `credential_ref: "provider:<id>"`). Empty token disables it.
- Existing tokens become that default account at startup; live hosts get `account_id = pa_<provider>_default`; this month's ledger rows are stamped once.
- Env: unlabeled variables (`HETZNER_TOKEN`, `E2B_API_KEY`, …) feed the default account as before, except that a token set from the dashboard is no longer overridden (the env key becomes its own account `pa_<provider>_env`). **Labeled** keys `<VAR>__<LABEL>` (two underscores, label `[A-Za-z0-9_-]{1,32}`) each become an account `pa_<provider>_env_<label>`, e.g. `HETZNER_TOKEN__EU2`, `E2B_API_KEY__BACKUP`; multi-part credentials use the same label on every part (`CONTABO_CLIENT_ID__B`, …), a missing optional part falls back to the unlabeled variable. Env accounts can be enabled / disabled / drained / patched but not rotated (400); an env account whose variable disappeared is disabled.
- `GET /providers` rows add `accounts_summary: {total, active, degraded}`. `Host` adds `account_id`, `contract_ends_at?`. Ledger rows add `account_id`.
- New error class `payment` (HTTP 402, "insufficient balance", "payment required", "out of credit", …) in `provhealth` (`GET /providers/health` `error_classes`, `ValidateResult.error_class`).
- Notify events add `infra_alert` (critical infra alerts → Telegram at once) and `infra_digest` (daily email of open infra alerts at `daily_report_hour`); both are in the defaults of new installs — existing installs add them with `PUT /notify`.

### Objects
`ProviderAccount` (= `AccountView`; money in the account `currency`, `*_usd` are USD conversions):
```
{ id, provider, label, external_id?, owner_workspace_id?, tags: [string], owner?, notes?, console_url?, is_default,
  credential_ref: "provider:<id>" | "account" | "env:<VAR>__<LABEL>", credential_source: "api" | "env",
  key_fingerprint?, billing_key_fingerprint?, scopes: [string],
  token_expires_at?, refresh_expires_at?, rotated_at?, rotate_every_days?,
  enabled, priority (1 = first), weight (>=1), regions: [string] (empty = all), pool?, drain, auto_drained,
  status: active | cooling | rate_limited | quota_full | low_balance | out_of_credit | auth_failed | token_expiring | disabled | draining | archived,
  status_reason?, status_since?, cooldown_until?, error_streak, last_error_class?, last_error?, last_ok_at?, last_check_at?,
  billing_model?: prepaid | postpaid | contract | free_tier, currency: "USD" | "EUR" | …, fx_rate_usd? (USD per unit; 0 = default),
  balance?, balance_source?: manual | api | estimated, balance_at?, credit_total?, credit_remaining?, credit_expires_at?,
  pending_charges?, month_to_date?, spend_cap_day (0 = none), spend_cap_month, min_balance, low_balance_warn,
  billing_cycle?: monthly | hourly-prepaid | contract, invoice_day?, next_invoice_at?, trial_ends_at?, contract_ends_at?,
  limits: { <name>: {limit, used, source: manual | learned | api, at?} }   // servers, concurrent_sandboxes, vcpu, ram_gb, …
  thresholds: {runway_warn_days?, runway_critical_days?, limit_warn_pct?, limit_critical_pct?, expiry_warn_days?, expiry_critical_days?},
  created_at, created_by?, updated_at, updated_by?,
  stats: { hosts_live, boxes_live, vcpu_live, spend_today, spend_month, spend_today_usd, spend_month_usd, burn_per_hour_usd,
           balance_effective?, balance_effective_usd?, runway_hours?, runway_days?, forecast_month_end,
           eligible, ineligible?: "<why the scheduler skips it now>", breaker: closed | open | half_open, has_credential },
  warnings?: [string] }   // writes only: same external_id / key as another account ("not a real backup")
```
- `balance_effective` = `balance` − ledger spend of the account since `balance_at` (converted to the account currency). `burn_per_hour_usd` = max(ledger of the last 24 h / 24, Σ `price_hour` of live hosts on the account). `runway_hours` = (`balance_effective_usd` − `min_balance` in USD) / burn (absent when there is no balance or no burn). `forecast_month_end` = month spend + burn × hours left in the UTC month. "today" / "month" are UTC.
- FX defaults: USD 1, EUR 1.08, GBP 1.27; override per account (`fx_rate_usd`) or per instance (`BOXMES_FX_RATES="EUR=1.08,GBP=1.27"`).

`InfraAlert`: `{id: "<type>|<subject>" (dedupe key), type, level: info | warn | critical, status: open | acked | closed, provider?, account_id?, host_id?, message (English), data: {args: [...]}, count, opened_at, updated_at, closed_at?, acked_at?, acked_by?, notified_at?, notified_level?}`. Types: `low_balance`, `out_of_credit`, `auth_failed`, `token_expiring`, `credit_expiring`, `trial_ending`, `contract_ending`, `rotation_due`, `limit_near`, `spend_cap_near`, `failover`, `single_account`, `orphan_host`, `balance_stale`. Clients localize from `type` + `data.args` (catalog keys `infra.alert.<type>`).

`StrategyView`: `{provider, strategy ("" = default), effective_strategy, sticky_minutes, failover_across_accounts, strategies: [string]}`.

`AdminProviderRow`: `{id, name, tier, group: vps | sandbox, connected, implemented, health (provider breaker status), strategy, accounts_total, accounts_healthy (eligible now), accounts_degraded, hosts_live, boxes_live, balance_usd?, balance_by_currency?: {EUR: …}, spend_today_usd, spend_month_usd, forecast_month_usd, runway_days? (eligible accounts together), max_limit_pct, open_alerts}`.

`AccountTestResult`: `{ok, error?, error_class?, latency_ms, key_fingerprint?, duplicates?: [account id], account?: AccountView}`.

### Endpoints (`/v1/admin`)
- `GET /providers` → `{summary: {balance_usd, balance_by_currency, spend_today_usd, spend_month_usd, forecast_month_usd, shortest_runway?: {provider, account_id, label, days}, accounts_healthy, accounts_degraded, accounts_broken, open_alerts}, items: [AdminProviderRow]}` (every provider except `local`).
- `GET /providers/{p}` → `AdminProviderRow + {prefs: ProviderPrefs, strategy_config: StrategyView, accounts: [AccountView], alerts: [InfraAlert], capabilities?}`.
- `GET /providers/{p}/strategy` → `StrategyView`. `PUT /providers/{p}/strategy` `{strategy, sticky_minutes (0..1440), failover_across_accounts?}` → `StrategyView`; unknown strategy → 400. Default: `priority` for VPS providers, `drain_first` for sandbox providers.
- `GET /providers/{p}/accounts?archived=1` → `{items: [AccountView]}` sorted by priority.
- `POST /providers/{p}/accounts/validate` `{token, external_id?}` → `AccountTestResult` (nothing stored).
- `POST /providers/{p}/accounts` `AccountInput` (`label` and `token` required; `skip_validate` stores without calling the provider) → 201 `AccountView`. A rejected key → 400 `INVALID`. Without `priority` the account goes after the existing ones.
- `GET /providers/{p}/accounts/{a}` → `AccountView`.
- `PATCH /providers/{p}/accounts/{a}` `AccountInput` (merge-patch) → `AccountView`. `AccountInput` fields: `label, billing_token ("" removes), external_id, tags, owner, notes, console_url, scopes, enabled, priority (1..99), weight (1..1000), regions, pool, drain, billing_model, currency, fx_rate_usd, balance (manual: sets balance_source=manual, balance_at=now; clears out_of_credit when above min_balance), clear_balance, credit_total, credit_remaining, pending_charges, month_to_date, spend_cap_day, spend_cap_month, min_balance, low_balance_warn, billing_cycle, invoice_day (0..31), rotate_every_days, token_expires_at, refresh_expires_at, credit_expires_at, trial_ends_at, contract_ends_at, next_invoice_at (RFC 3339 or YYYY-MM-DD; "" clears), limits: {name: value} (0 removes), thresholds`. `token` in a PATCH → 400 (use rotate). An archived account only accepts `{enabled: true}` (restores it).
- `POST /providers/{p}/accounts/{a}/rotate` `{token, external_id?, skip_validate?}` → `AccountView`. When the account has an `external_id`, the body must carry the same one (a key of another provider account would orphan the hosts) → else 400. Swaps the key atomically, sets `rotated_at`, clears `auth_failed`; for the default account it writes the provider token. `warnings` when `external_id` is unknown.
- `POST /providers/{p}/accounts/{a}/test` → `AccountTestResult` (probes the stored key; updates status and the account breaker).
- `POST /providers/{p}/accounts/{a}/enable | disable | drain | undrain | reset` → `AccountView` (`reset`: breaker, cooldown, `auth_failed` / `out_of_credit` / `quota_full` and learned limits).
- `DELETE /providers/{p}/accounts/{a}?force=1&reason=…&purge=1` → 204. Archives (no new hosts; the key is kept so its hosts can still be deleted). Live hosts → 400 unless `force=1` with a `reason`. `purge=1` hard-deletes an account no host references (else 400).
- `GET /providers/{p}/accounts/{a}/timeline?limit=` → `{items: [Event]}` (`provider_account.*` events of the account, newest first).
- `GET /infra/alerts?status=` (`""` = open + acked, `open`, `acked`, `closed`, `all`) → `{items: [InfraAlert]}` (critical first). `POST /infra/alerts/{id}/ack` (`id` URL-encoded) → `InfraAlert`.

### Scheduling, failover, automation
- Eligibility (in order): enabled, not archived / `auth_failed` / `out_of_credit`, not draining, has a key, cooldown passed, account breaker closed, region allowed, `servers` / `concurrent_sandboxes` limit not reached, `balance_effective − 24 h of the new host ≥ min_balance`, runway ≥ critical threshold, daily / monthly spend cap not exceeded (postpaid: month-end forecast too). Ineligible accounts are listed in the error `<p>: no eligible account (…)` (`PROVIDER_FAILED`) and the scheduler moves on to the next offer / provider.
- Ranking: `priority` (lowest priority, then fewer hosts), `weighted` (random by weight), `round_robin`, `least_used` (lowest used/limit), `drain_first` (credit expiring first, then the smallest remaining credit/balance); `sticky_minutes` > 0 reuses the account a workspace used last.
- Failover per create (max 3 accounts / 6 attempts): `auth` → `auth_failed` + next account; `payment` → `out_of_credit` + next; `quota` → learned limit, `quota_full` for 1 h + next; `rate_limit` → cooldown from `retry-after` (10 s..15 min) + next; `server`/`timeout`/`network` → account breaker + next; `capacity` → provider×region blocked for 10 min, no account failover; `invalid` → stop. Each switch emits `provider_account.failover {from, to, class, request}`.
- Two-level breaker: with several accounts, account-level errors (auth, payment, quota, rate limit) only feed the account breaker; provider-level errors feed both.
- Runway < critical (default 2 days) → the account is drained automatically (`auto_drained: true`, event `provider_account.drained`); running hosts are **never** deleted; the drain lifts itself when runway recovers. `BOXMES_ACCOUNT_AUTODRAIN=0` disables it.
- Thresholds (owner decision 2026-11-27, overridable per account): runway 7 / 2 days, limits and spend caps 80 % / 95 %, token / credit / contract expiry 14 / 3 days (trial 7 / 1). One open alert per key; only the highest level crossed; critical alerts go to Telegram at once and repeat at most once per 24 h while not acknowledged (or when they escalate); alerts close themselves when the condition clears; `failover` notices close 15 min after the last failover.
- Destroy failing because the account key is missing / rejected → critical `orphan_host` alert + `provider_account.orphan_host` event (with `console_url`).
- Audit events (`Event.data` always has `provider`, `account_id`, never a key): `provider_account.created`, `updated` (`fields`), `enabled`, `disabled`, `drained`, `undrained`, `archived`, `deleted`, `reset`, `key_rotated` (`old_fingerprint`, `key_fingerprint`), `validated`, `check_failed`, `status_changed` (`from`, `to`, `reason`), `failover`, `balance_updated` (`old`, `new`, `source`), `limit_learned`, `alert_opened`, `alert_closed`, `alert_acked`, `orphan_host`; `provider.strategy_updated`.

### Known gaps (phase A)
- No balance / limit reading from provider APIs yet (`AccountReader`, phase B; Runpod first): balances, credit and limits are manual or learned from errors.
- `provider_account_snapshots` exists but is not written yet (no balance history chart).
- Credentials of non-default accounts are sealed in their `provider_accounts` row with the master key (not in the `secrets` table); the default account still uses the provider's `token_enc`.
- The old provider health snapshot is not copied to the default account's breaker (it starts closed).
- Sticky picks, round-robin cursors and the capacity blocklist live in memory.
- Billing read-only keys are stored (`billing_token`) but not used until phase B.
- Owner-workspace (BYOC) accounts are phase C.

### SDKs (v2.2)
Python (`boxmes`): `admin_providers`, `admin_provider`, `list_provider_accounts(provider, archived=)`, `get_provider_account`, `validate_provider_account(provider, token, external_id=)`, `create_provider_account(provider, label, token, **fields)`, `update_provider_account(provider, account_id, **fields)`, `rotate_provider_account(provider, account_id, token, external_id=)`, `test_provider_account`, `set_provider_account_state(provider, account_id, action)`, `delete_provider_account(provider, account_id, force=, reason=, purge=)`, `provider_strategy`, `set_provider_strategy(provider, strategy, sticky_minutes=, failover_across_accounts=)`, `infra_alerts(status)`, `ack_infra_alert(alert_id)`. TypeScript (`@boxmes/sdk`): same in camelCase with types `ProviderAccount`, `ProviderAccountInput`, `AccountStats`, `AccountTestResult`, `ProviderStrategy`, `InfraAlert`, `AdminProviderRow`, `InfraSummary`.
