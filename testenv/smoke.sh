#!/usr/bin/env bash
set -euo pipefail
binary="$(cd "$(dirname "$1")" && pwd)/$(basename "$1")"
smoke_root="$(mktemp -d /tmp/pearl-smoke.XXXXXX)"
workspace="$smoke_root/workspace"
mkdir "$smoke_root/config" "$workspace"
export PEARL_CONFIG_DIR="$smoke_root/config"
export PEARL_SOCKET="$smoke_root/p.sock"
export NO_COLOR=1
trap '"$binary" daemon stop >/dev/null 2>&1 || true' EXIT
printf '%s\n' 'OPENROUTER_API_KEY=dummy-smoke-key' > "$PEARL_CONFIG_DIR/.env"
"$binary" version
"$binary" daemon --help
"$binary" daemon start
"$binary" job --workspace "$workspace" -n 'smoke job' 'Synthetic pending job; never execute'
"$binary" --workspace "$workspace" jobs --json | python3 -c 'import json,sys; j=json.load(sys.stdin); assert len(j)==1 and j[0]["status"]=="pending"'
"$binary" jobs view 'smoke job' --json | python3 -c 'import json,sys; assert json.load(sys.stdin)["job"]["id"]=="smoke job"'
"$binary" cancel 'smoke job'
"$binary" jobs view 'smoke job' --json | python3 -c 'import json,sys; assert json.load(sys.stdin)["job"]["status"]=="cancelled"'
printf '%s\n' 'CLI smoke test passed'
