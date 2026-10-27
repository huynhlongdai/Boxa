#!/usr/bin/env bash
# Live end-to-end test against Novita Sandbox (E2B-compatible): same flow as
# live-e2b.sh. NOVITA_API_KEY=sk_… scripts/live-novita.sh
exec env SANDBOX_PROVIDER=novita PORT=${PORT:-18777} "$(dirname "$0")/live-e2b.sh" "$@"
