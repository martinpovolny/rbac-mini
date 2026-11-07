#!/usr/bin/env bash
# Measure one running configuration RUNS times; results go to results/$STAGE/<label>-<n>.json.
# Usage: measure.sh <label> <port> <api-prefix>
#   STAGE=go-pool scripts/loadtest/measure.sh go 18093 /api/rbac/v1
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/env.sh"

if [ "$#" -ne 3 ]; then
  echo "usage: $0 <label> <port> <api-prefix>" >&2
  exit 2
fi
measure_config "$1" "$2" "$3"
