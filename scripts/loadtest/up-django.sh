#!/usr/bin/env bash
# Build (if missing), migrate, load the fixture, and start the Django baseline.
#   up-django.sh on    access-policy cache enabled (stock setting)
#   up-django.sh off   ACCESS_CACHE_ENABLED=False: every access request runs the query
# Set REBUILD=1 to rebuild the image from the insights-rbac checkout.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/env.sh"

case "${1:-}" in
  on) cache=True ;;
  off) cache=False ;;
  *) echo "usage: $0 on|off" >&2; exit 2 ;;
esac

if [ "${REBUILD:-0}" = 1 ] || ! docker image inspect "$DJANGO_IMAGE" >/dev/null 2>&1; then
  docker build -t "$DJANGO_IMAGE" "$INSIGHTS_RBAC_DIR"
fi

docker rm -f "$DJANGO_CONTAINER" >/dev/null 2>&1 || true

docker run --rm --entrypoint python -w /opt/rbac/rbac "${DJANGO_ENV[@]}" "$DJANGO_IMAGE" \
  manage.py migrate --noinput
docker run --rm -i --entrypoint python -w /opt/rbac/rbac "${DJANGO_ENV[@]}" "$DJANGO_IMAGE" \
  manage.py shell < "$LOADTEST_DIR/django_fixture.py"

docker run -d --name "$DJANGO_CONTAINER" -p "$DJANGO_PORT:8080" "${DJANGO_ENV[@]}" \
  -e "ACCESS_CACHE_ENABLED=$cache" "$DJANGO_IMAGE" >/dev/null

if ! wait_until 120 "django" probe_app "$DJANGO_PORT" "$DJANGO_PREFIX"; then
  docker logs --tail 40 "$DJANGO_CONTAINER" >&2
  exit 1
fi
echo "django up on :$DJANGO_PORT (ACCESS_CACHE_ENABLED=$cache)"
