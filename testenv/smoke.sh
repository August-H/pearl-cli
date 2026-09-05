#!/usr/bin/env bash
set -euo pipefail
binary="$(cd "$(dirname "$1")" && pwd)/$(basename "$1")"
smoke_root="$(mktemp -d /tmp/pearl-smoke.XXXXXX)"
export PEARL_CONFIG_DIR="$smoke_root"
export PEARL_SOCKET="$smoke_root/p.sock"
export NO_COLOR=1
trap '"$binary" daemon stop >/dev/null 2>&1 || true' EXIT
printf '%s\n' 'OPENROUTER_API_KEY=dummy-smoke-key' > "$smoke_root/.env"
"$binary" version
"$binary" daemon --help
"$binary" daemon start
"$binary" job --workspace "$smoke_root" -n 'smoke job' 'Synthetic pending job; never execute'
"$binary" --workspace "$smoke_root" jobs --json | python3 -c 'import json,sys; j=json.load(sys.stdin); assert len(j)==1 and j[0]["status"]=="pending"'
"$binary" jobs view 'smoke job' --json | python3 -c 'import json,sys; assert json.load(sys.stdin)["job"]["id"]=="smoke job"'
"$binary" cancel 'smoke job'
"$binary" jobs view 'smoke job' --json | python3 -c 'import json,sys; assert json.load(sys.stdin)["job"]["status"]=="cancelled"'
printf '%s\n' 'CLI smoke test passed'
