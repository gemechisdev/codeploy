#!/usr/bin/env bash
# Apply migrations 0001–0007 to a temp DB via the real store.Migrate path.
# Exit non-zero on any failure. Does not boot the daemon.
set -euo pipefail
cd "$(dirname "$0")/.."
exec go test -count=1 ./internal/store/ -run TestMigrateAllToV7
