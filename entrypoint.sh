#!/bin/sh
# Startup script for the rbac-go container (UBI has a shell, unlike distroless).
# `serve` always migrates first so fresh volumes boot cleanly; seeding stays
# explicit via the operator migration Job (`rbac-go migrate && rbac-go seed`).
set -e
if [ "${1:-serve}" = "serve" ]; then
  /usr/local/bin/rbac-go migrate
fi
exec /usr/local/bin/rbac-go "$@"
