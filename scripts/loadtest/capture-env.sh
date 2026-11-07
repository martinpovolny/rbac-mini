#!/usr/bin/env bash
# Record what a result set was produced on: commits, image IDs, server versions, host, settings.
# Writes results/$STAGE/ENV.txt. Run after the services are up.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/env.sh"

mkdir -p "$RESULTS_DIR"
out="$RESULTS_DIR/ENV.txt"

commit() {
  local dir=$1 head changes
  head="$(git -C "$dir" rev-parse --short HEAD)"
  changes="$(git -C "$dir" status --short --untracked-files=no)"
  if [ -n "$changes" ]; then
    printf '%s (uncommitted tracked changes:)\n%s\n' "$head" "$(sed 's/^/    /' <<<"$changes")"
  else
    echo "$head (clean)"
  fi
}

image_id() {
  docker image inspect --format '{{.Id}}' "$1" 2>/dev/null || echo "missing"
}

{
  echo "date:           $(date -u +%Y-%m-%dT%H:%M:%SZ)"
  echo "stage:          $STAGE"
  echo "host:           $(uname -sm)"
  echo "cpu:            $(sysctl -n machdep.cpu.brand_string 2>/dev/null || echo unknown)"
  echo "logical cpus:   $(sysctl -n hw.ncpu 2>/dev/null || echo unknown)"
  echo "docker server:  $(docker version --format '{{.Server.Version}}' 2>/dev/null || echo unknown)"
  echo
  echo "rbac-go commit:        $(commit "$RBAC_GO_DIR")"
  echo "insights-rbac commit:  $(commit "$INSIGHTS_RBAC_DIR")"
  echo "rbac-go image:         $GO_IMAGE $(image_id "$GO_IMAGE")"
  echo "django image:          $DJANGO_IMAGE $(image_id "$DJANGO_IMAGE")"
  echo
  echo "postgres:       $(docker exec "$PG_CONTAINER" psql -U postgres -tAc 'SHOW server_version' 2>/dev/null || echo unknown)"
  echo "postgres max_connections: $(docker exec "$PG_CONTAINER" psql -U postgres -tAc 'SHOW max_connections' 2>/dev/null || echo unknown)"
  echo "valkey:         $(docker exec "$VALKEY_CONTAINER" valkey-cli INFO server 2>/dev/null | tr -d '\r' | grep -E '^(valkey|redis)_version:' | paste -sd' ' - || echo unknown)"
  echo
  echo "load profile:   app=$APPLICATION limit=$LIMIT users=$USER_COUNT (org $ORG_ID) concurrency=$CONCURRENCY requests=$REQUESTS runs=$RUNS"
} > "$out"

echo "wrote $out"
