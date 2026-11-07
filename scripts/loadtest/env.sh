# Shared settings and helpers for the load-test scripts. Sourced, not executed.
# Every value can be overridden from the environment, for example:
#   REQUESTS=10000 RUNS=5 scripts/loadtest/baseline.sh

LOADTEST_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RBAC_GO_DIR="${RBAC_GO_DIR:-$(cd "$LOADTEST_DIR/../.." && pwd)}"
INSIGHTS_RBAC_DIR="${INSIGHTS_RBAC_DIR:-$(cd "$RBAC_GO_DIR/.." && pwd)}"

# Results for one stage live in results/<stage>/. Stages: baseline, native, and
# later optimisation stages.
STAGE="${STAGE:-baseline}"
RESULTS_DIR="${RESULTS_DIR:-$LOADTEST_DIR/results/$STAGE}"

ORG_ID="${ORG_ID:-100001}"
ACCOUNT="${ACCOUNT:-1111111}"
USER_COUNT="${USER_COUNT:-100}"
USER_SPEC="loaduser-0..$((USER_COUNT - 1))"
USERS_CSV="$(python3 -c "print(','.join(f'loaduser-{i}' for i in range($USER_COUNT)))")"
PROBE_USER="loaduser-0"
APPLICATION="${APPLICATION:-cost-management}"
LIMIT="${LIMIT:-100}"
CONCURRENCY="${CONCURRENCY:-20}"
REQUESTS="${REQUESTS:-5000}"
RUNS="${RUNS:-3}"

PG_IMAGE="${PG_IMAGE:-postgres:18.1}"
PG_CONTAINER="${PG_CONTAINER:-rbac-baseline-db}"
PG_PORT="${PG_PORT:-15433}"
PG_PASSWORD="${PG_PASSWORD:-postgres}"
PG_MAX_CONNECTIONS="${PG_MAX_CONNECTIONS:-300}"

VALKEY_IMAGE="${VALKEY_IMAGE:-valkey/valkey:8}"
VALKEY_CONTAINER="${VALKEY_CONTAINER:-rbac-baseline-redis}"
VALKEY_PORT="${VALKEY_PORT:-16379}"

DJANGO_IMAGE="${DJANGO_IMAGE:-rbac-django-baseline}"
DJANGO_CONTAINER="${DJANGO_CONTAINER:-rbac-django-srv}"
DJANGO_PORT="${DJANGO_PORT:-18092}"
DJANGO_PREFIX="/api/v1"

GO_IMAGE="${GO_IMAGE:-rbac-go:loadtest}"
GO_CONTAINER="${GO_CONTAINER:-rbac-go-srv}"
GO_PORT="${GO_PORT:-18093}"
GO_PREFIX="/api/rbac/v1"

# Name the containers use for the host (Docker Desktop provides this name).
HOST_FROM_CONTAINER="${HOST_FROM_CONTAINER:-host.docker.internal}"

DJANGO_ENV=(
  -e "DATABASE_HOST=$HOST_FROM_CONTAINER" -e "DATABASE_PORT=$PG_PORT" -e DATABASE_NAME=postgres
  -e DATABASE_USER=postgres -e "DATABASE_PASSWORD=$PG_PASSWORD"
  -e "REDIS_HOST=$HOST_FROM_CONTAINER" -e "REDIS_PORT=$VALKEY_PORT"
  -e BYPASS_BOP_VERIFICATION=True -e DJANGO_SECRET_KEY=loadtest
)
GO_DATABASE_URL="postgres://postgres:${PG_PASSWORD}@${HOST_FROM_CONTAINER}:${PG_PORT}/rbacgo?sslmode=disable"

PROBE_IDENTITY="$(python3 -c "
import base64, json
user = '$PROBE_USER'
payload = {'org_id': '$ORG_ID', 'identity': {'org_id': '$ORG_ID', 'account_number': '$ACCOUNT',
           'type': 'User', 'user': {'username': user, 'email': user + '@example.com', 'is_org_admin': False}}}
print(base64.b64encode(json.dumps(payload).encode()).decode())
")"

# wait_until <seconds> <description> <command...>
wait_until() {
  local timeout=$1 description=$2 i
  shift 2
  for ((i = 0; i < timeout; i++)); do
    if "$@" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  echo "timed out after ${timeout}s waiting for $description" >&2
  return 1
}

# probe_app <port> <api-prefix>: succeeds once the access endpoint answers 200.
probe_app() {
  curl -fsS -o /dev/null -H "X-Rh-Identity: $PROBE_IDENTITY" \
    "http://localhost:$1$2/access/?application=$APPLICATION&limit=5"
}

# measure_config <label> <port> <api-prefix>: RUNS measured runs, one JSON file each.
measure_config() {
  local label=$1 port=$2 prefix=$3 i
  mkdir -p "$RESULTS_DIR"
  rm -f "$RESULTS_DIR/$label"-*.json
  for ((i = 1; i <= RUNS; i++)); do
    echo "== $label run $i/$RUNS"
    python3 "$LOADTEST_DIR/loadtest.py" --base-url "http://localhost:$port" --api-prefix "$prefix" \
      --org "$ORG_ID" --users "$USER_SPEC" --application "$APPLICATION" --limit "$LIMIT" \
      --concurrency "$CONCURRENCY" --requests "$REQUESTS" --save "$RESULTS_DIR/$label-$i.json"
  done
}
