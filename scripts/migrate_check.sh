#!/usr/bin/env bash
# Apply migrations 0001–0008 to a temp DB via the real store.Migrate path.
# Exit non-zero on any failure, including a renamed/missing test. Does not boot the daemon.
set -euo pipefail
cd "$(dirname "$0")/.."
log="$(mktemp)"; trap 'rm -f "$log"' EXIT
go test -count=1 -v -run '^TestMigrateAllToV8$' ./internal/store/ 2>&1 | tee "$log"
grep -qE '^=== RUN[[:space:]]+TestMigrateAllToV8$' "$log" || { echo "TestMigrateAllToV8 did not run" >&2; exit 1; }
