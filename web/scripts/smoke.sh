#!/usr/bin/env bash
# Starts the mock dev server, runs the browser smoke scripts, always cleans up. Usage: scripts/smoke.sh [script.py ...]
set -u
cd "$(dirname "$0")/.."
PORT=3100
VITE_API_MODE=mock npx vite --port $PORT --host 127.0.0.1 >/tmp/vite.log 2>&1 &
PID=$!
trap 'kill $PID 2>/dev/null' EXIT
for _ in $(seq 1 40); do curl -sf "http://127.0.0.1:$PORT/" >/dev/null && break; sleep 0.5; done
rc=0
for s in "${@:-scripts/smoke.py scripts/smoke-setup.py}"; do for f in $s; do echo "== $f"; timeout 150 python3 "$f" || rc=1; done; done
exit $rc
