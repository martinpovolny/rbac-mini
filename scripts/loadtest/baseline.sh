#!/usr/bin/env bash
# Produce the recorded baseline, in order:
#   1. Django with the access-policy cache off  (django-nocache)
#   2. Django with the access-policy cache on   (django-warm)
#   3. base rbac-go, no cache                   (go)
# Each configuration is measured RUNS times. Artifacts land in results/baseline/:
# <label>-<n>.json, ENV.txt, REPORT.md, report.svg.
# REBUILD=1 rebuilds the images from the checkouts first.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/env.sh"

"$LOADTEST_DIR/up-baseline.sh"

"$LOADTEST_DIR/up-django.sh" off
measure_config django-nocache "$DJANGO_PORT" "$DJANGO_PREFIX"

"$LOADTEST_DIR/up-django.sh" on
measure_config django-warm "$DJANGO_PORT" "$DJANGO_PREFIX"

"$LOADTEST_DIR/up-go.sh"
measure_config go "$GO_PORT" "$GO_PREFIX"

"$LOADTEST_DIR/capture-env.sh"
python3 "$LOADTEST_DIR/report.py" --results-dir "$RESULTS_DIR" \
  --subtitle "$USER_COUNT users, org $ORG_ID, concurrency $CONCURRENCY, $REQUESTS requests per run, median of $RUNS runs; Docker Desktop on $(uname -sm)"
