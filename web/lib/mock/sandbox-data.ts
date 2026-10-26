// Sinh từ GET /v1/sandbox/providers của boxa-core v1.4 (adapter metadata, capability, giá theo giây, mức xác minh).
// Chỉ phần tĩnh; trạng thái (connected, mode, health…) do lib/mock/v14.ts tính.
import type { SandboxInfo } from "../types";

export const SANDBOX_INFO: SandboxInfo[] = [
 {
  "id": "boxdsh",
  "name": "boxd.sh",
  "tier": "T2",
  "adapter": "native",
  "verification": "docs-verified",
  "verified_note": "grpc-web framing and field numbers from docs.boxd.sh/reference/grpc-api and grpc-proto (CreateVm, GetVm, DestroyVm, Exec, UploadFile, DownloadFile, SuspendVm, ResumeVm, ForkVm, CreateSnapshot, Whoami).",
  "docs": "https://docs.boxd.sh/reference/grpc-api",
  "website": "https://boxd.sh",
  "initials": "BX",
  "color": "#111827",
  "capabilities": {
   "background": true,
   "pause": true,
   "snapshot": true,
   "fork": true,
   "ports": true,
   "pty": true,
   "files": true,
   "gpu": false,
   "boot_ms": 300,
   "max_lifetime_s": 0,
   "max_cpu": 8,
   "max_ram_mb": 32768,
   "isolation": "microvm"
  },
  "pricing": {
   "cpu_second": 1.3055555555555555e-05,
   "gb_second": 1e-06,
   "disk_gb_second": 0,
   "flat_second": 0,
   "source": "https://boxd.sh/pricing",
   "confidence": "docs",
   "note": "€0.039/vCPU-h (US$0.047 listed) + €0.003/GiB-h RAM while running; disk billed by GiB used"
  },
  "sizes": [
   {
    "sku": "1x4",
    "cpu": 1,
    "ram_mb": 4096,
    "disk_gb": 100
   },
   {
    "sku": "2x8",
    "cpu": 2,
    "ram_mb": 8192,
    "disk_gb": 100
   },
   {
    "sku": "4x16",
    "cpu": 4,
    "ram_mb": 16384,
    "disk_gb": 100
   },
   {
    "sku": "8x32",
    "cpu": 8,
    "ram_mb": 32768,
    "disk_gb": 100
   }
  ],
  "regions": [
   {
    "id": "global",
    "name": "boxd.sh"
   }
  ],
  "templates": [
   "ubuntu:24.04"
  ],
  "default_template": "",
  "workdir": "/home/boxd",
  "credentials": [
   {
    "name": "token",
    "label": "API key",
    "secret": true,
    "optional": false,
    "hint": "bxd_…"
   },
   {
    "name": "org",
    "label": "Organization",
    "secret": false,
    "optional": true
   }
  ],
  "env_vars": [
   "BOXDSH_API_KEY",
   "BOXDSH_ORG"
  ],
  "notes": [
   "Persistent VMs with a public HTTPS domain; Boxa creates them isolated (no peers) and deletes them when the box ends."
  ],
  "from_usd_h": 0.0614
 },
 {
  "id": "prized",
  "name": "Prized Sandboxes",
  "tier": "T2",
  "adapter": "native",
  "verification": "live-verified",
  "verified_note": "Routes from prized.dev/docs/sandboxes-api; read-only calls (GET /me, /sandbox-templates, /sandboxes) succeeded with a real key on 2026-10-27. Create/exec not exercised (no spend).",
  "docs": "https://prized.dev/docs/sandboxes-api",
  "website": "https://prized.dev/sandboxes",
  "initials": "PZ",
  "color": "#7c5cff",
  "capabilities": {
   "background": true,
   "pause": true,
   "snapshot": false,
   "fork": true,
   "ports": false,
   "pty": false,
   "files": true,
   "gpu": false,
   "boot_ms": 3000,
   "max_lifetime_s": 604800,
   "max_cpu": 32,
   "max_ram_mb": 131072,
   "isolation": "microvm"
  },
  "pricing": {
   "cpu_second": 1.1111111111111112e-05,
   "gb_second": 3.4722222222222224e-06,
   "disk_gb_second": 2.7777777777777777e-08,
   "flat_second": 0,
   "source": "https://prized.dev/docs/billing#sandboxes",
   "confidence": "docs",
   "note": "per second, no minimum; paused = storage only"
  },
  "sizes": [
   {
    "sku": "1x2",
    "cpu": 1,
    "ram_mb": 2048,
    "disk_gb": 10
   },
   {
    "sku": "2x4",
    "cpu": 2,
    "ram_mb": 4096,
    "disk_gb": 10
   },
   {
    "sku": "4x8",
    "cpu": 4,
    "ram_mb": 8192,
    "disk_gb": 10
   },
   {
    "sku": "8x16",
    "cpu": 8,
    "ram_mb": 16384,
    "disk_gb": 10
   },
   {
    "sku": "16x32",
    "cpu": 16,
    "ram_mb": 32768,
    "disk_gb": 20
   }
  ],
  "regions": [
   {
    "id": "global",
    "name": "Prized (auto)"
   }
  ],
  "templates": [
   "prized-base-v1",
   "prized-agent-v1"
  ],
  "default_template": "prized-base-v1",
  "workdir": "/home/prized",
  "credentials": [
   {
    "name": "token",
    "label": "API key (Sandboxes: Write)",
    "secret": true,
    "optional": false,
    "hint": "dcp_…"
   }
  ],
  "env_vars": [
   "PRIZED_API_KEY"
  ],
  "notes": [
   "No inbound ports: boxd dials out, so this is fine for Boxa.",
   "Free tier: 2 vCPU / 4 GB and 60 min of running per sandbox."
  ],
  "from_usd_h": 0.066
 },
 {
  "id": "daytona",
  "name": "Daytona",
  "tier": "T2",
  "adapter": "native",
  "verification": "docs-verified",
  "verified_note": "From Daytona's published OpenAPI (docs/openapi.json, docs/toolbox-openapi.json); toolbox URL = toolboxProxyUrl + '/' + sandboxId as in the official TS SDK.",
  "docs": "https://www.daytona.io/docs/en/tools/api/",
  "website": "https://www.daytona.io",
  "initials": "DT",
  "color": "#0f62fe",
  "capabilities": {
   "background": true,
   "pause": true,
   "snapshot": true,
   "fork": true,
   "ports": true,
   "pty": true,
   "files": true,
   "gpu": true,
   "boot_ms": 1000,
   "max_lifetime_s": 0,
   "max_cpu": 4,
   "max_ram_mb": 8192,
   "isolation": "container"
  },
  "pricing": {
   "cpu_second": 1.4e-05,
   "gb_second": 4.5e-06,
   "disk_gb_second": 3e-08,
   "flat_second": 0,
   "source": "https://www.daytona.io/pricing",
   "confidence": "estimated",
   "note": "published per-second rates (pricing page renders client-side; not re-fetched)"
  },
  "sizes": [
   {
    "sku": "1x1",
    "cpu": 1,
    "ram_mb": 1024,
    "disk_gb": 3
   },
   {
    "sku": "1x2",
    "cpu": 1,
    "ram_mb": 2048,
    "disk_gb": 5
   },
   {
    "sku": "2x4",
    "cpu": 2,
    "ram_mb": 4096,
    "disk_gb": 10
   },
   {
    "sku": "4x8",
    "cpu": 4,
    "ram_mb": 8192,
    "disk_gb": 10
   }
  ],
  "regions": [
   {
    "id": "us",
    "name": "United States"
   },
   {
    "id": "eu",
    "name": "Europe"
   }
  ],
  "templates": [
   "(default snapshot)"
  ],
  "default_template": "",
  "workdir": "/home/daytona",
  "credentials": [
   {
    "name": "token",
    "label": "API key",
    "secret": true,
    "optional": false,
    "hint": "dtn_…"
   },
   {
    "name": "organization_id",
    "label": "Organization ID",
    "secret": false,
    "optional": true
   }
  ],
  "env_vars": [
   "DAYTONA_API_KEY",
   "DAYTONA_ORGANIZATION_ID"
  ],
  "from_usd_h": 0.066924,
  "notes": []
 },
 {
  "id": "novita",
  "name": "Novita Sandbox",
  "tier": "T2",
  "adapter": "native",
  "verification": "sdk-source",
  "verified_note": "E2B-compatible. Regions/domains and per-second prices from docs.novita.ai; REST paths and headers (X-API-KEY, api.<domain>, envd 49983) from the official novita-sandbox SDK 2.1.1 — Novita publishes no REST reference.",
  "docs": "https://docs.novita.ai/guides/sandbox-overview",
  "website": "https://novita.ai",
  "initials": "NV",
  "color": "#23d57c",
  "capabilities": {
   "background": true,
   "pause": true,
   "snapshot": false,
   "fork": false,
   "ports": true,
   "pty": true,
   "files": true,
   "gpu": false,
   "boot_ms": 1000,
   "max_lifetime_s": 14400,
   "max_cpu": 8,
   "max_ram_mb": 8192,
   "isolation": "microvm"
  },
  "pricing": {
   "cpu_second": 9.8e-06,
   "gb_second": 3.2e-06,
   "disk_gb_second": 0,
   "flat_second": 0,
   "source": "https://docs.novita.ai/guides/sandbox-pricing",
   "confidence": "docs",
   "note": "per second for CPU and RAM while running; 20 GB ephemeral disk included"
  },
  "sizes": [
   {
    "sku": "base",
    "cpu": 2,
    "ram_mb": 512,
    "disk_gb": 20
   }
  ],
  "regions": [
   {
    "id": "us-phx-1",
    "name": "US Phoenix (v2, default)"
   },
   {
    "id": "us-virginia-1",
    "name": "US Virginia (v1)"
   }
  ],
  "templates": [
   "base"
  ],
  "default_template": "base",
  "workdir": "/home/user",
  "credentials": [
   {
    "name": "token",
    "label": "API key",
    "secret": true,
    "optional": false,
    "hint": "sk_…"
   },
   {
    "name": "templates",
    "label": "Templates (id=CPUxMB, comma separated)",
    "secret": false,
    "optional": true,
    "hint": "base=2x512"
   }
  ],
  "env_vars": [
   "NOVITA_API_KEY",
   "NOVITA_TEMPLATES"
  ],
  "notes": [
   "Max session 1 h (free) to 4 h (default paid); CPU/RAM come from the template."
  ],
  "from_usd_h": 0.07632
 },
 {
  "id": "e2b",
  "name": "E2B",
  "tier": "T2",
  "adapter": "native",
  "verification": "docs-verified",
  "verified_note": "Control plane from E2B's OpenAPI spec (e2b-dev/infra spec/openapi.yml); envd files from envd.yaml; exec via Connect RPC process.Process/Start (process.proto).",
  "docs": "https://e2b.dev/docs",
  "website": "https://e2b.dev",
  "initials": "E2",
  "color": "#ff8800",
  "capabilities": {
   "background": true,
   "pause": true,
   "snapshot": false,
   "fork": true,
   "ports": true,
   "pty": true,
   "files": true,
   "gpu": false,
   "boot_ms": 500,
   "max_lifetime_s": 86400,
   "max_cpu": 8,
   "max_ram_mb": 8192,
   "isolation": "microvm"
  },
  "pricing": {
   "cpu_second": 1.4e-05,
   "gb_second": 4.5e-06,
   "disk_gb_second": 0,
   "flat_second": 0,
   "source": "https://e2b.dev/pricing",
   "confidence": "docs",
   "note": "per second while running; Hobby/Pro plans; sandbox resources come from the template"
  },
  "sizes": [
   {
    "sku": "base",
    "cpu": 2,
    "ram_mb": 512,
    "disk_gb": 10
   }
  ],
  "regions": [
   {
    "id": "global",
    "name": "E2B Cloud"
   }
  ],
  "templates": [
   "base"
  ],
  "default_template": "base",
  "workdir": "/home/user",
  "credentials": [
   {
    "name": "token",
    "label": "API key",
    "secret": true,
    "optional": false,
    "hint": "e2b_…"
   },
   {
    "name": "templates",
    "label": "Templates (id=CPUxMB, comma separated)",
    "secret": false,
    "optional": true,
    "hint": "base=2x512,my-agent=4x4096"
   }
  ],
  "env_vars": [
   "E2B_API_KEY",
   "E2B_TEMPLATES"
  ],
  "notes": [
   "CPU/RAM are fixed per template: build bigger templates in E2B and list them in the Templates field."
  ],
  "from_usd_h": 0.1089
 },
 {
  "id": "runloop",
  "name": "Runloop",
  "tier": "T2",
  "adapter": "yaml",
  "verification": "docs-verified",
  "verified_note": "Routes and fields from Runloop's published OpenAPI (docs.runloop.ai/openapi-specs/stainless-processed-openapi.json): POST /v1/devboxes, GET /v1/devboxes/{id}, POST …/execute_sync, …/shutdown, …/suspend, …/resume, …/write_file_contents, …/read_file_contents.",
  "docs": "https://docs.runloop.ai/api-reference",
  "website": "https://www.runloop.ai",
  "initials": "RL",
  "color": "#6d28d9",
  "capabilities": {
   "background": true,
   "pause": true,
   "snapshot": true,
   "fork": false,
   "ports": true,
   "pty": false,
   "files": true,
   "gpu": false,
   "boot_ms": 2000,
   "max_lifetime_s": 0,
   "max_cpu": 8,
   "max_ram_mb": 32768,
   "isolation": "microvm"
  },
  "pricing": {
   "cpu_second": 3e-05,
   "gb_second": 7e-06,
   "disk_gb_second": 0,
   "flat_second": 0,
   "source": "https://www.runloop.ai/pricing",
   "confidence": "docs",
   "note": "$0.108/CPU-h + $0.0252/GB-h while running; storage extra"
  },
  "sizes": [
   {
    "sku": "X_SMALL",
    "cpu": 0.5,
    "ram_mb": 1024,
    "disk_gb": 4
   },
   {
    "sku": "SMALL",
    "cpu": 1,
    "ram_mb": 2048,
    "disk_gb": 4
   },
   {
    "sku": "MEDIUM",
    "cpu": 2,
    "ram_mb": 4096,
    "disk_gb": 8
   },
   {
    "sku": "LARGE",
    "cpu": 2,
    "ram_mb": 8192,
    "disk_gb": 16
   },
   {
    "sku": "X_LARGE",
    "cpu": 4,
    "ram_mb": 16384,
    "disk_gb": 16
   },
   {
    "sku": "XX_LARGE",
    "cpu": 8,
    "ram_mb": 32768,
    "disk_gb": 16
   }
  ],
  "regions": [
   {
    "id": "global",
    "name": "Runloop"
   }
  ],
  "templates": [],
  "default_template": "",
  "workdir": "/home/user",
  "credentials": [
   {
    "name": "token",
    "label": "API key",
    "secret": true,
    "optional": false,
    "hint": "ak_…"
   }
  ],
  "env_vars": [
   "RUNLOOP_API_KEY"
  ],
  "from_usd_h": 0.0792,
  "notes": []
 },
 {
  "id": "blaxel",
  "name": "Blaxel",
  "tier": "T2",
  "adapter": "yaml",
  "verification": "docs-verified",
  "verified_note": "Control plane from docs.blaxel.ai controlplane.yml (POST/GET/DELETE /v0/sandboxes, metadata.url, Status enum); in-sandbox API from blaxel-ai/sandbox sandbox-api/docs/openapi.yml (POST /process with waitForCompletion, keepAlive for background processes).",
  "docs": "https://docs.blaxel.ai/api-reference/introduction",
  "website": "https://blaxel.ai",
  "initials": "BL",
  "color": "#f97316",
  "capabilities": {
   "background": true,
   "pause": true,
   "snapshot": false,
   "fork": false,
   "ports": true,
   "pty": false,
   "files": true,
   "gpu": false,
   "boot_ms": 25,
   "max_lifetime_s": 0,
   "max_cpu": 8,
   "max_ram_mb": 16384,
   "isolation": "microvm"
  },
  "pricing": {
   "cpu_second": 0,
   "gb_second": 1.15e-05,
   "disk_gb_second": 0,
   "flat_second": 0,
   "source": "https://blaxel.ai/pricing",
   "confidence": "docs",
   "note": "billed per GB of memory per second; CPU = memory/2048 (included); standby is not billed for compute"
  },
  "sizes": [
   {
    "sku": "2g",
    "cpu": 1,
    "ram_mb": 2048,
    "disk_gb": 10
   },
   {
    "sku": "4g",
    "cpu": 2,
    "ram_mb": 4096,
    "disk_gb": 10
   },
   {
    "sku": "8g",
    "cpu": 4,
    "ram_mb": 8192,
    "disk_gb": 10
   },
   {
    "sku": "16g",
    "cpu": 8,
    "ram_mb": 16384,
    "disk_gb": 10
   }
  ],
  "regions": [
   {
    "id": "us-pdx-1",
    "name": "US Portland"
   },
   {
    "id": "eu-lon-1",
    "name": "EU London"
   }
  ],
  "templates": [],
  "default_template": "blaxel/base-image:latest",
  "workdir": "/blaxel",
  "credentials": [
   {
    "name": "token",
    "label": "API key",
    "secret": true,
    "optional": false
   },
   {
    "name": "workspace",
    "label": "Workspace",
    "secret": false,
    "optional": false,
    "hint": "the name in the console URL"
   }
  ],
  "env_vars": [
   "BL_API_KEY",
   "BL_WORKSPACE"
  ],
  "from_usd_h": 0.0828,
  "notes": []
 },
 {
  "id": "upstash-box",
  "name": "Upstash Box",
  "tier": "T3",
  "adapter": "yaml",
  "verification": "sdk-source",
  "verified_note": "Upstash documents Box through its SDKs only. Paths (POST /v2/box, GET/DELETE /v2/box/{id}, POST /v2/box/{id}/exec with argv → {exit_code, output, error}), the X-Box-Api-Key header and the default base URL are read from the official @upstash/box SDK source.",
  "docs": "https://upstash.com/docs/box/overall/how-it-works",
  "website": "https://upstash.com/box",
  "initials": "UB",
  "color": "#00e9a3",
  "capabilities": {
   "background": false,
   "pause": true,
   "snapshot": true,
   "fork": false,
   "ports": true,
   "pty": false,
   "files": true,
   "gpu": false,
   "boot_ms": 1500,
   "max_lifetime_s": 0,
   "max_cpu": 2,
   "max_ram_mb": 4096,
   "isolation": "container"
  },
  "pricing": {
   "cpu_second": 1.4e-05,
   "gb_second": 4.5e-06,
   "disk_gb_second": 0,
   "flat_second": 0,
   "source": "https://upstash.com/pricing/box",
   "confidence": "estimated",
   "note": "usage-based; unit rates not stated in the docs — estimate"
  },
  "sizes": [
   {
    "sku": "small",
    "cpu": 1,
    "ram_mb": 2048,
    "disk_gb": 10
   }
  ],
  "regions": [
   {
    "id": "us-east-1",
    "name": "US East 1"
   }
  ],
  "templates": [],
  "default_template": "node",
  "workdir": "/workspace/home",
  "credentials": [
   {
    "name": "token",
    "label": "Box API key",
    "secret": true,
    "optional": false,
    "hint": "box_…"
   }
  ],
  "env_vars": [
   "UPSTASH_BOX_API_KEY"
  ],
  "from_usd_h": 0.0828,
  "notes": []
 },
 {
  "id": "agent37",
  "name": "Agent 37",
  "tier": "T2",
  "adapter": "yaml",
  "verification": "docs-verified",
  "verified_note": "From agent37.com/docs/agents-api (POST /v1/instances, instance object status values, DELETE /v1/instances/{id}, POST /v1/instances/{id}/exec on api.agent37.com with Bearer, answering exit_code/stdout/stderr).",
  "docs": "https://www.agent37.com/docs/agents-api/overview",
  "website": "https://www.agent37.com/cloud",
  "initials": "A37",
  "color": "#16a34a",
  "capabilities": {
   "background": true,
   "pause": true,
   "snapshot": false,
   "fork": true,
   "ports": true,
   "pty": false,
   "files": true,
   "gpu": false,
   "boot_ms": 5000,
   "max_lifetime_s": 0,
   "max_cpu": 8,
   "max_ram_mb": 16384,
   "isolation": "gvisor"
  },
  "pricing": {
   "cpu_second": 1.39e-05,
   "gb_second": 4.6e-06,
   "disk_gb_second": 0,
   "flat_second": 0,
   "source": "https://www.agent37.com/docs/agents-api/billing",
   "confidence": "estimated",
   "note": "metered per minute from a prepaid wallet; unit rates are shown in the dashboard, not the docs — estimate"
  },
  "sizes": [
   {
    "sku": "1x2",
    "cpu": 1,
    "ram_mb": 2048,
    "disk_gb": 4
   },
   {
    "sku": "2x4",
    "cpu": 2,
    "ram_mb": 4096,
    "disk_gb": 4
   },
   {
    "sku": "4x8",
    "cpu": 4,
    "ram_mb": 8192,
    "disk_gb": 8
   }
  ],
  "regions": [
   {
    "id": "us",
    "name": "United States"
   }
  ],
  "templates": [],
  "default_template": "agent37-ubuntu",
  "workdir": "/root",
  "credentials": [
   {
    "name": "token",
    "label": "API key",
    "secret": true,
    "optional": false,
    "hint": "sk_live_…"
   }
  ],
  "env_vars": [
   "AGENT37_API_KEY"
  ],
  "from_usd_h": 0.08316,
  "notes": []
 },
 {
  "id": "boat",
  "name": "boat",
  "tier": "T2",
  "adapter": "yaml",
  "verification": "docs-verified",
  "verified_note": "From docs.boat.dev/api/v1 (POST /sandboxes, GET /sandboxes/{id}, POST …/commands with exitCode/stdout/stderr, POST …/stop, POST …/resume, DELETE /sandboxes/{id}, detached commands for background jobs).",
  "docs": "https://docs.boat.dev/api/v1",
  "website": "https://boat.dev",
  "initials": "BT",
  "color": "#0ea5e9",
  "capabilities": {
   "background": true,
   "pause": true,
   "snapshot": true,
   "fork": true,
   "ports": true,
   "pty": true,
   "files": true,
   "gpu": false,
   "boot_ms": 3000,
   "max_lifetime_s": 0,
   "max_cpu": 8,
   "max_ram_mb": 16384,
   "isolation": "vm"
  },
  "pricing": {
   "cpu_second": 1.4e-05,
   "gb_second": 4.5e-06,
   "disk_gb_second": 0,
   "flat_second": 0,
   "source": "https://boat.dev/",
   "confidence": "estimated",
   "note": "billed per second on top of a $20/mo plan; per-unit rates not published in the API docs — estimate"
  },
  "sizes": [
   {
    "sku": "default",
    "cpu": 2,
    "ram_mb": 4096,
    "disk_gb": 20
   }
  ],
  "regions": [
   {
    "id": "global",
    "name": "boat"
   }
  ],
  "templates": [],
  "default_template": "",
  "workdir": "/home/ubuntu",
  "credentials": [
   {
    "name": "token",
    "label": "API key",
    "secret": true,
    "optional": false,
    "hint": "boat_…"
   }
  ],
  "env_vars": [
   "BOAT_API_KEY"
  ],
  "from_usd_h": 0.1656,
  "notes": []
 },
 {
  "id": "vercel",
  "name": "Vercel Sandbox",
  "tier": "T2",
  "adapter": "yaml",
  "verification": "docs-verified",
  "verified_note": "From vercel.com/docs/rest-api/sandboxes (POST /v2/sandboxes → {sandbox, session, routes}; POST /v2/sandboxes/sessions/{sessionId}/cmd; GET …/cmd/{cmdId}/logs NDJSON {stream,data}; GET …/cmd/{cmdId}?wait=true; DELETE /v2/sandboxes/{name}).",
  "docs": "https://vercel.com/docs/rest-api/sandboxes",
  "website": "https://vercel.com/sandbox",
  "initials": "VC",
  "color": "#000000",
  "capabilities": {
   "background": true,
   "pause": false,
   "snapshot": true,
   "fork": true,
   "ports": true,
   "pty": false,
   "files": true,
   "gpu": false,
   "boot_ms": 1500,
   "max_lifetime_s": 18000,
   "max_cpu": 8,
   "max_ram_mb": 16384,
   "isolation": "microvm"
  },
  "pricing": {
   "cpu_second": 3.56e-05,
   "gb_second": 5.9e-06,
   "disk_gb_second": 0,
   "flat_second": 0,
   "source": "https://vercel.com/docs/sandbox/pricing",
   "confidence": "estimated",
   "note": "Active CPU + provisioned memory; plan-dependent — estimate"
  },
  "sizes": [
   {
    "sku": "1",
    "cpu": 1,
    "ram_mb": 2048,
    "disk_gb": 10
   },
   {
    "sku": "2",
    "cpu": 2,
    "ram_mb": 4096,
    "disk_gb": 10
   },
   {
    "sku": "4",
    "cpu": 4,
    "ram_mb": 8192,
    "disk_gb": 10
   },
   {
    "sku": "8",
    "cpu": 8,
    "ram_mb": 16384,
    "disk_gb": 10
   }
  ],
  "regions": [
   {
    "id": "iad1",
    "name": "Washington DC (iad1)"
   }
  ],
  "templates": [],
  "default_template": "node24",
  "workdir": "/vercel/sandbox",
  "credentials": [
   {
    "name": "token",
    "label": "Access token",
    "secret": true,
    "optional": false
   },
   {
    "name": "project_id",
    "label": "Project ID",
    "secret": false,
    "optional": false,
    "hint": "prj_…"
   },
   {
    "name": "team_id",
    "label": "Team ID",
    "secret": false,
    "optional": true,
    "hint": "team_…"
   }
  ],
  "env_vars": [
   "VERCEL_TOKEN",
   "VERCEL_PROJECT_ID",
   "VERCEL_TEAM_ID"
  ],
  "from_usd_h": 0.17064,
  "notes": []
 }
];
