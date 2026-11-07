# Concurrency sweep: supporting observations

The tables and the chart in this directory come from the `sweep-*` and
`native-sweep-*` runs. This file records the two measurements that are not in
the load tool's JSON.

## Postgres sessions during a C100 run

Command, with `$GO_PORT` from `scripts/loadtest/env.sh`:

```bash
python3 scripts/loadtest/loadtest.py \
  --base-url "http://localhost:18093" --api-prefix /api/rbac/v1 \
  --org 100001 --users 'loaduser-0..99' --application cost-management \
  --limit 100 --concurrency 100 --requests 40000 --save /tmp/c100-probe.json &
while :; do
  psql 'postgres://postgres:postgres@127.0.0.1:15433/rbacgo?sslmode=disable' -tAc \
    "select count(*) || '/' ||
            count(*) filter (where state = 'active')
       from pg_stat_activity where datname = 'rbacgo'"
  sleep 1
done
```

Samples (`total/active`): `26/1`, `26/2`, `26/2`, `26/5`, `26/2`. The 26th
session is the psql probe; the other 25 are the rbac-go pool. At most 5 of
the 25 were running a query at any sample.

Result of that run: 5886 req/s, p50 14.6 ms, p95 32.7 ms, 0 errors.

## Two load-generator processes at C50 each

Run together against the same container:

| Process | rps | p50 ms | p95 ms | errors |
|---|---:|---:|---:|---:|
| a | 3014.9 | 14.6 | 32.0 | 0 |
| b | 3015.5 | 14.6 | 32.4 | 0 |
| total | 6030.4 | | | |

The total is the same as one process at C100 (5849 req/s median), so the
container cap is not the load generator.

## Load-generator cost at C100

`/usr/bin/time -l` around a 40000-request C100 run: 7.08 s real, 2.71 s user
+ 2.02 s system (0.67 cores), 41 MiB peak RSS. The same tool at 5382 req/s in
the footprint stage used 0.61 cores and 41 MiB; see
[FOOTPRINT.md](../footprint/FOOTPRINT.md).
