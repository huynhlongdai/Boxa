# Boxaz API contract v1 (shared by core + web) — 2026-10-26

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
- Providers: adapters for `digitalocean`, `contabo`, `netcup` (same lifecycle as hetzner/vultr). Provider gains `implemented:bool, chain_rank:int (1-based scheduler fallback order, 0 = not in chain), token_source: env|api`. `GET /providers/:id/regions` → `{items:[{id, name, available}]}`; `GET /providers/:id/servers` → `{items:[{id, name, ip, status, region, sku, tags}]}` (needs a connected token). Tokens: DO = personal access token; Contabo = `client_id:client_secret:api_user:api_password` or JSON with those keys; netcup = SCP refresh token (Boxaz claims pre-ordered servers nicknamed `mgr-spare-*`; legacy `boxa-spare-*` still claimed). Env at startup: `HETZNER_TOKEN, VULTR_API_KEY, DIGITALOCEAN_TOKEN, CONTABO_CLIENT_ID/_SECRET/API_USER/API_PASSWORD, NETCUP_REFRESH_TOKEN`; chain via `BOXAZ_PROVIDER_CHAIN` (default `hetzner,vultr,digitalocean,contabo,netcup`). Scheduler: existing pool → first connected provider in the chain (within agent policy); a failed create excludes that provider and tries the next.
- Snapshot storage (S3 API: Cloudflare R2, Backblaze B2, AWS S3, MinIO). Snapshot gains `storage: local|s3, remote_url?, upload_error?`. Core presigns PUT/GET URLs; boxd streams the tar.gz. With `storage=s3` restore/fork work on any host. Retention: `settings.reaper.keep_snapshots` + `settings.reaper.snapshot_retention_days` (0 = off); remote objects are deleted too. `GET /storage` → `{provider: local|r2|b2|s3|minio, endpoint, region, bucket, prefix, access_key_id, configured, source: env|settings|none, has_secret, keep_snapshots, snapshot_retention_days, fallback}`; `PUT /storage` `{provider, endpoint?, region?, bucket?, prefix?, access_key_id?, secret_access_key?}` (secret write-only; `provider:"local"` turns it off; 400 `INVALID` when env-configured); `POST /storage/test` → `{ok, error?, latency_ms, bucket}`.
- Notifications: `settings.notify` gains `timezone` (IANA) and `smtp:{host, port, username, from, tls: starttls|tls|none}`; events: `daily_report, budget, approval, emergency_stop, box_failed, host_down`. `GET /notify` → settings.notify + `{has_telegram_token, has_smtp_password, telegram_configured, email_configured, events_available:[..], recent:[{ts, kind, channel: telegram|email, ok, error?, title}]}`; `PUT /notify` = settings.notify + write-only `telegram_bot_token?, smtp_password?, clear_telegram_token?, clear_smtp_password?` (stored in the encrypted secrets store as reserved `BOXAZ_SYS_*`, hidden from `/secrets`); `POST /notify/test` `{channel: telegram|email|all, kind: test|daily_report}` → `{results:[Delivery]}`.
- Egress: `POST /boxes` (and box.yaml, MCP `box_up`) accept `egress:{allow:[domain|*.domain|ip|cidr[:port]], deny_by_default?}` (box.yaml also accepts a plain list). `PUT /boxes/:id/egress` `{allow, deny_by_default}` → Box (empty + false = fall back). Precedence: box → agent `policy.egress_allowlist` (also a ceiling: entries outside it → 403 `POLICY_DENIED`) → `settings.egress_deny_by_default`. Box gains `egress:{allow, deny_by_default, source: box|agent|settings, enforced, mode: incus-acl|nftables|none, rules, warning?, applied_at}|null`. Enforced on incus hosts (Incus network ACL or nftables, DNS to the resolver always allowed); the local driver reports `enforced:false` + `warning`.
- Jobs: status adds `canceled`; `POST /jobs/:id/cancel` → Job.
- A2A JSON-RPC 2.0 at `POST /a2a` (same Bearer auth, protocol 0.3): `message/send` (alias `tasks/send`), `message/stream` (alias `tasks/sendSubscribe`, SSE of `Task` → `status-update` / `artifact-update` → final `status-update`), `tasks/get`, `tasks/cancel`, `tasks/resubscribe` `{id}`. A text part (command) + `metadata.box_id`, or a data part `{box_id, command, cwd?, timeout_s?}`, starts an async exec job — task id = job id; a data part `{tool, arguments}` calls any MCP tool synchronously. States: queued→submitted, running→working, done→completed, failed→failed, canceled→canceled. Errors: -32601 method not found, -32602 invalid params, -32001 task not found, -32002 not cancelable. Agent card: `url=<base>/a2a`, `preferredTransport: JSONRPC`, `capabilities.streaming: true`, skills = `exec` + every tool.

## v1.3 additions (2026-10-26) — smart providers, box selection, speed, mesh
- Provider health: `GET /v1/providers/health`, `GET /v1/providers/{id}/health`, `POST /v1/providers/{id}/validate` (checks a token without saving; lists regions/sizes), `POST /v1/providers/{id}/check`, `POST /v1/providers/{id}/reset`, `GET|PUT /v1/providers/{id}/prefs` (enabled, priority, max $/h, max $/mo, max servers, allowed regions, sandbox_mode). Circuit breaker: 3 consecutive failures or ≥50% failure rate; backoff 60 s → 15 min; Retry-After honoured; auth errors never trip.
- Merged catalog: `GET /v1/catalog` (Battleships feed + provider APIs; live price wins; source, staleness, arch, tier).
- Selection: `GET /v1/profiles` (7 workload profiles + 22 agent-kind defaults), `POST /v1/recommend` (requirements → ranked options with 6-factor score, ETA, price, reasons, rejected options). `box up` accepts `profile` / `requirements` (also in box.yaml).
- Right-sizing: `GET /v1/boxes/{id}/rightsize`, `GET /v1/rightsize`.
- Speed: `GET|PUT /v1/warm-pool`, `POST /v1/boxes/{id}/clone` (CoW), `GET /v1/speed`; placement `BOXAZ_PLACEMENT=binpack|spread`; exec session mode + output compaction counted in `tokens_saved`.
- Agent support: `POST /v1/boxes/ensure` (idempotent get-or-create), `GET /v1/boxes/{id}/resume`; create responses carry `estimate` (kind, cost, ETA, actual seconds, warm hit); errors carry `next` (suggested tool calls). MCP adds `box_ensure`, `box_recommend`, `box_clone`.
- Mesh (headscale): `GET /v1/mesh`, `/v1/mesh/config`, `/v1/mesh/acl`, `POST /v1/mesh/keys`; disabled → hint (keys → 400).

## v1.4 additions (2026-10-27) — sandbox API providers (T2/T3)
- Tiers: T1 VPS, T2 sandbox that runs `boxd --dial` inside, T3 exec-only via gateway-side shim. One sandbox hosts one box and is destroyed with it.
- Native adapters: Prized (live-verified), E2B (live-verified, Hobby), Novita (E2B-compatible; live-verified create/exec/files/delete), Daytona, boxd.sh. YAML adapters: Runloop, Blaxel, Vercel Sandbox, boat, Agent 37, Upstash Box (T3). Skipped: Modal (gRPC only), Cloudflare Sandbox (needs a user-deployed Worker), Koyeb (WebSocket exec), zipbox (not verified this release).
- `GET /v1/sandbox/providers` (tier, adapter, verification, capabilities, per-second prices, regions, docs), `POST /v1/sandbox/adapters/lint` (validate a custom YAML adapter). `PUT /v1/providers/{id}/token` returns `group`, `adapter`, `verification`, `docs`.
- `POST /v1/recommend` accepts `untrusted` and `fork`; container-based sandboxes are rejected for untrusted code. Catalog rows carry `tier` and per-second price.
- Env: `BOXAZ_SANDBOX_ADAPTERS_DIR`, `BOXAZ_SANDBOX_MODE=auto|dial|shim`, `BOXAZ_AUTO_SANDBOX`, `BOXAZ_PROVIDER_CHAIN` (T1 chain then sandbox chain), `BOXAZ_PUBLIC_URL` must be internet-reachable for T2 dial mode.

## v1.5 additions (2026-10-27) — deploying apps on sandbox boxes
- `GET /v1/boxes/{id}/ports` → `{box_id, url, ports:[{port, url, kind: provider-preview|ingress|tunnel|none, public, note?, caveats?:[string]}]}`; `POST /v1/boxes/{id}/ports {port}` exposes a port of a live box (same answer, the new port first). The box `url` is the first port's URL. Sandbox boxes answer with the provider's preview host (E2B `https://<port>-<id>.e2b.app`, Novita `https://<port>-<id>.<domain>`); providers without previews (Prized) go through the public-URL fallback chain below.
- MCP adds `box_url {box_id, port?}` (22 tools); CLI `box url <box> [port]`, `box clone <box> [--slug]`.
- T3 (shim) boxes now support `box_snapshot` / `box_restore` / `box_fork`: the snapshot is a tar.gz of `/work`, `/memory`, `AGENTS.md` kept by core (`location=local://…`, ≤64 MB unless object storage is configured, then uploaded from the sandbox); a T3 snapshot restores into any T3 box and forks onto a fresh sandbox of the same provider.
- `POST /v1/boxes/{id}/clone` on a sandbox box: provider-native fork when the provider has one (E2B, Prized; memory and running processes included), else snapshot → new sandbox → restore (Novita). `placement_reason` names the method.
- `box_sleep` on a sandbox box pauses it at the provider; wake resumes it (also for reaper idle sleep).

## v1.6 additions (2026-10-27) — rename + public URL fallback
- Renamed Boxa → Boxaz: env `BOXAZ_*` (legacy `BOXA_*` still honoured when the new name is unset, with a deprecation log line), binary `boxaz-core`, reserved secrets `BOXAZ_SYS_*` (legacy `BOXA_SYS_*` read), cookies `boxaz_key` / `boxaz_locale` (legacy `boxa_key` / `boxa_locale` read), ingress token query `boxaz_token` (legacy `boxa_token`). API paths and shapes are unchanged. Provider-visible names stay neutral (`mgr-*`, `managed`, `sandbox-client/1`).
- Public URL fallback for ports of sandbox boxes, first step that works wins:
  1. `provider-preview` — the provider's preview host.
  2. `ingress` — `<BOXAZ_PUBLIC_URL>/x/<key>-<port>/` (key = per-box HMAC), forwarded by core's ingress proxy. T2 boxes use their dialled-in boxd; T3 boxes get an in-box agent (`boxd ingress`, downloaded from `<public>/dl/boxd-linux-<arch>`, so `BOXAZ_DIST_DIR` must hold the binaries) that dials `GET /v1/ingress/connect` (WebSocket, per-box token). Tried only when `BOXAZ_PUBLIC_URL` is set and looks internet-facing (not localhost / `.local` / `.internal` / `.lan` / private IP; override with `BOXAZ_INGRESS_ASSUME_PUBLIC=1`) and the agent connects within 25 s. Caveats: path-prefixed (app needs relative links), HTTP only and buffered through core (no WebSocket/streaming, 32 MB body cap), the first request to a sleeping box wakes it.
  3. `tunnel` — a Cloudflare quick tunnel started inside the sandbox (cloudflared downloaded on demand; free, no account) → `https://<random>.trycloudflare.com/`. Caveats: no uptime guarantee, the URL changes if the tunnel restarts (e.g. a wake that lost processes), first request can take a few seconds. Disabled by `settings.disable_tunnel: true` (`GET/PUT /settings`) or `BOXAZ_DISABLE_TUNNEL=1`.
  4. `none` — `note` explains, `caveats` lists why each step was skipped.
  `caveats` always names the limits of the returned kind and the skipped steps. After a wake exposures are re-established (agent restarted / tunnel re-checked, new URL if it had to restart); `DELETE /boxes/:id` drops them. A sleeping box keeps reporting its last URL with `note` saying it is re-checked on wake.
- Live (Prized, `scripts/live-publicurl.sh`, 2026-10-27): tunnel — first `GET /ports` 7.9 s, outside curl → 200 in 0.11 s, URL unchanged after sleep→wake; ingress (core behind a cloudflared quick tunnel) — first `GET /ports` 0.53 s, outside curl → 200 in 0.23 s, URL unchanged after wake.

