#!/usr/bin/env bash
# Footprint of each serving path: CPU and memory of the containers involved while the
# three configurations are measured. Samples `docker stats` about once a second during
# each configuration's runs and writes results/$STAGE/footprint-<label>.csv. The same
# load-test JSON runs are written too, so CPU per request can be derived, and each run's
# <label>-<n>.time holds the load generator's own CPU time and peak memory.
# Each configuration starts from the same containers as baseline.sh.
# Sampling costs a little while it runs, so this stage's latency numbers are not
# compared with the baseline; the footprint numbers are the point.
# Usage: STAGE=footprint scripts/loadtest/footprint.sh
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/env.sh"
LOAD_WRAPPER=(/usr/bin/time -l)

if [ -z "${STAGE:-}" ] || [ "$STAGE" = baseline ]; then
  echo "set STAGE to a new stage name (not 'baseline'), e.g. STAGE=footprint" >&2
  exit 2
fi

CONTAINERS=("$GO_CONTAINER" "$DJANGO_CONTAINER" "$PG_CONTAINER" "$VALKEY_CONTAINER")

sample_once() {  # sample_once <phase>
  local ts
  ts="$(date +%s)"
  docker stats --no-stream --format '{{.Name}},{{.CPUPerc}},{{.MemUsage}}' "${CONTAINERS[@]}" 2>/dev/null \
    | awk -v ts="$ts" -v phase="$1" '{print ts "," phase "," $0}'
}

sample_loop() {  # sample_loop <csv>
  while true; do
    sample_once load >> "$1" || true
    sleep 1
  done
}

footprint_config() {  # footprint_config <label> <port> <api-prefix> <requests>
  local label=$1 csv
  csv="$RESULTS_DIR/footprint-$label.csv"
  mkdir -p "$RESULTS_DIR"
  echo "timestamp,phase,container,cpu,mem" > "$csv"
  sample_once idle >> "$csv"
  sample_loop "$csv" &
  local sampler=$!
  REQUESTS="$4" measure_config "$label" "$2" "$3"
  kill "$sampler"
  wait "$sampler" 2>/dev/null || true
}

# Request counts are sized so each run lasts 20 to 35 s: docker stats takes about a second
# per sample, and at the baseline REQUESTS rbac-go finishes in under one second.
"$LOADTEST_DIR/up-baseline.sh"

"$LOADTEST_DIR/up-django.sh" off
footprint_config django-nocache "$DJANGO_PORT" "$DJANGO_PREFIX" 12000

"$LOADTEST_DIR/up-django.sh" on
footprint_config django-warm "$DJANGO_PORT" "$DJANGO_PREFIX" 30000

"$LOADTEST_DIR/up-go.sh"
footprint_config go "$GO_PORT" "$GO_PREFIX" 100000

{
  echo "rbac-go processes:     $(docker top "$GO_CONTAINER" | tail -n +2 | wc -l | tr -d ' ')"
  echo "django processes:      $(docker top "$DJANGO_CONTAINER" | tail -n +2 | wc -l | tr -d ' ')"
} > "$RESULTS_DIR/footprint-processes.txt"

"$LOADTEST_DIR/capture-env.sh"
python3 "$LOADTEST_DIR/footprint_report.py" --results-dir "$RESULTS_DIR"
