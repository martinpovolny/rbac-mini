#!/usr/bin/env bash
# Measure the current rbac-go checkout as a named stage and compare it with the baseline.
# Rebuilds the image, runs RUNS measurements into results/$STAGE/, captures ENV.txt, and
# writes REPORT.md and report.svg with the baseline rows alongside.
# Usage: STAGE=go-pool scripts/loadtest/go-stage.sh
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/env.sh"

if [ -z "${STAGE:-}" ] || [ "$STAGE" = baseline ]; then
  echo "set STAGE to a new stage name (not 'baseline'), e.g. STAGE=go-pool" >&2
  exit 2
fi

REBUILD=1 "$LOADTEST_DIR/up-go.sh"
measure_config go "$GO_PORT" "$GO_PREFIX"
"$LOADTEST_DIR/capture-env.sh"

python3 "$LOADTEST_DIR/report.py" --results-dir "$RESULTS_DIR" \
  --group "baseline/django-warm=Django + Valkey (policy cache on)" \
  --group "baseline/django-nocache=Django (policy cache off)" \
  --group "baseline/go=rbac-go base (no cache)" \
  --group "go=rbac-go $STAGE" \
  --subtitle "100 users, org $ORG_ID, concurrency $CONCURRENCY, $REQUESTS requests per run, median of $RUNS runs; Docker Desktop on $(uname -sm)"
