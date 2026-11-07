#!/usr/bin/env bash
# Reference rows: rbac-go as a host process, with no containers and no Docker VM hop.
# SQLite is always measured. Postgres is measured when both variables are set:
#   NATIVE_PG_ADMIN_DSN  a DSN on a server where the script may drop and create rbacgo_native
#   NATIVE_PG_DSN        the same server, database rbacgo_native
# Example:
#   NATIVE_PG_ADMIN_DSN='postgres://me@127.0.0.1:55432/postgres?sslmode=disable' \
#   NATIVE_PG_DSN='postgres://me@127.0.0.1:55432/rbacgo_native?sslmode=disable' scripts/loadtest/native.sh
set -euo pipefail
export STAGE="${STAGE:-native}"
source "$(dirname "${BASH_SOURCE[0]}")/env.sh"

mkdir -p "$RESULTS_DIR"
workdir="$(mktemp -d)"
trap 'kill $(jobs -p) 2>/dev/null || true; rm -rf "$workdir"' EXIT

bin="$workdir/rbac-go"
(cd "$RBAC_GO_DIR" && go build -o "$bin" ./cmd/rbac-go)

native_run() {  # native_run <label> <port> <database-url>
  local label=$1 port=$2 url=$3 pid
  export DATABASE_URL="$url"
  "$bin" migrate
  "$bin" seed
  "$bin" bootstrap --org "$ORG_ID" --account "$ACCOUNT" --users "$USERS_CSV"
  "$bin" serve --addr "127.0.0.1:$port" >"$RESULTS_DIR/$label-server.log" 2>&1 &
  pid=$!
  if ! wait_until 30 "$label" probe_app "$port" "$GO_PREFIX"; then
    cat "$RESULTS_DIR/$label-server.log" >&2
    exit 1
  fi
  measure_config "$label" "$port" "$GO_PREFIX"
  kill "$pid"
  wait "$pid" 2>/dev/null || true
}

native_run native-sqlite 18190 "sqlite:///$workdir/native.db"
groups=(--group "native-sqlite=rbac-go native, SQLite file DB")

if [ -n "${NATIVE_PG_ADMIN_DSN:-}" ] && [ -n "${NATIVE_PG_DSN:-}" ]; then
  psql "$NATIVE_PG_ADMIN_DSN" -q -c "DROP DATABASE IF EXISTS rbacgo_native WITH (FORCE)" \
    -c "CREATE DATABASE rbacgo_native"
  native_run native-pg 18191 "$NATIVE_PG_DSN"
  groups+=(--group "native-pg=rbac-go native, Postgres 16 on localhost")
fi

"$LOADTEST_DIR/capture-env.sh"
python3 "$LOADTEST_DIR/report.py" --results-dir "$RESULTS_DIR" "${groups[@]}" \
  --subtitle "host process, no containers; $USER_COUNT users, concurrency $CONCURRENCY, $REQUESTS requests per run, median of $RUNS runs"
