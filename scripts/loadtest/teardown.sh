#!/usr/bin/env bash
# Remove the app containers. Images and results are kept.
# Pass --all to also remove the Postgres and Valkey containers.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/env.sh"

docker rm -f "$DJANGO_CONTAINER" "$GO_CONTAINER" >/dev/null 2>&1 || true
if [ "${1:-}" = "--all" ]; then
  docker rm -f "$PG_CONTAINER" "$VALKEY_CONTAINER" >/dev/null 2>&1 || true
fi
echo "load-test containers removed"
