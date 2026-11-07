# Load test: GET /api/rbac/v1/access/ (koku critical path)

This measures the one endpoint koku calls on its hot path. It records a baseline
of three configurations, and every later optimization is measured against it:

1. **Django + Valkey (policy cache on)**: stock insights-rbac, the access policy cached per principal in Valkey.
2. **Django (policy cache off)**: the same service with `ACCESS_CACHE_ENABLED=False`.
3. **rbac-go, base**: the current rbac-go, no cache.

Question this answers: does rbac-go need its own cache before on-prem rollout, or
does koku's per-user cache already cover it?

Everything here is reproducible with the scripts in
[scripts/loadtest/](../scripts/loadtest/). Raw results, the environment record, the
generated report and the chart are committed under
[scripts/loadtest/results/](../scripts/loadtest/results/).

## Recorded baseline

![GET /access/ baseline: latency and throughput](../scripts/loadtest/results/baseline/report.svg)

Median of 3 runs per configuration, 5000 measured requests per run, 100 users,
concurrency 20. Latencies in ms. Max is the worst single run. Full per-run numbers
are in [REPORT.md](../scripts/loadtest/results/baseline/REPORT.md).

| System | runs | rps | mean ms | p50 ms | p95 ms | p99 ms | max ms (worst run) | errors |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| Django + Valkey (policy cache on) | 3 | 890 | 20.5 | 18.9 | 32.6 | 42.7 | 738.7 | 0 |
| Django (policy cache off) | 3 | 325 | 55.2 | 53.5 | 77.7 | 91.2 | 788.5 | 0 |
| rbac-go (no cache) | 3 | 1029 | 18.9 | 11.6 | 42.0 | 52.5 | 86.8 | 0 |

What the baseline says:

* **The Django cache is worth about 2.8×** on median latency (53.5 → 18.9 ms) and
  2.7× on throughput (325 → 890 req/s).
* **rbac-go without a cache beats Django without one on every metric**: 4.6× lower
  median (11.6 vs 53.5 ms), 3.2× the throughput, and about 55–60% of Django's p95 and p99.
* **rbac-go without a cache has a better median and throughput than Django with its
  cache**, but a worse tail: p95 42.0 vs 32.6 ms, p99 52.5 vs 42.7 ms. The tail is the
  open problem, and it is where the first optimization should aim ([Finding 2](#findings)).

## Reference: rbac-go as a host process

Not part of the three-way baseline. These rows isolate the cost of the container
and Docker VM path from the cost of the Go code.

| System | runs | rps | mean ms | p50 ms | p95 ms | p99 ms | max ms (worst run) | errors |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| rbac-go native, SQLite file DB | 3 | 4495 | 4.3 | 3.7 | 9.7 | 13.8 | 28.2 | 0 |
| rbac-go native, Postgres 16 on localhost | 3 | 2729 | 7.0 | 2.3 | 23.9 | 29.1 | 54.5 | 0 |

The container row (p50 11.6 ms) is about 9 ms slower at the median than the
native Postgres row (p50 2.3 ms). The native Postgres row also has a long tail
(p95 23.9 ms), so the tail is not only a Docker effect. See [Finding 2](#findings).

## Reproduce

### Prerequisites

* Docker (Docker Desktop was used; `host.docker.internal` must resolve from containers).
* Python 3 (stdlib only; no packages to install).
* `curl`, `git`.
* Go 1.27 toolchain, for `native.sh` only.
* `psql` on the host, for the native Postgres row only.
* Network access: the scripts pull `postgres:18.1` and `valkey/valkey:8`, build the
  rbac-go image (`golang:1.27` base), and build the insights-rbac image
  (`registry.access.redhat.com` base).

### Layout assumed by the scripts

The scripts assume rbac-go is checked out inside the insights-rbac repository:
`insights-rbac/rbac-go/`. The Django image is built from the parent directory.
Override with `RBAC_GO_DIR` and `INSIGHTS_RBAC_DIR` if the layout differs.

### Run the baseline

```bash
scripts/loadtest/baseline.sh
```

This runs, in order:

1. `up-baseline.sh`: starts the Postgres and Valkey containers and sets `max_connections=300`.
2. `up-django.sh off`, then the measurement for `django-nocache` (`RUNS` runs).
3. `up-django.sh on`, then the measurement for `django-warm` (`RUNS` runs).
4. `up-go.sh`, then the measurement for `go` (`RUNS` runs).
5. `capture-env.sh`: writes `ENV.txt`.
6. `report.py`: writes `REPORT.md` and `report.svg`.

Add `REBUILD=1` to rebuild the rbac-go and Django images from the checkouts first.

The first Django migration on an empty Postgres took about 10 minutes when this was
recorded. Later runs reuse the database and take a few minutes in total.

### Run the native reference rows

```bash
NATIVE_PG_ADMIN_DSN='postgres://me@127.0.0.1:55432/postgres?sslmode=disable' \
NATIVE_PG_DSN='postgres://me@127.0.0.1:55432/rbacgo_native?sslmode=disable' \
  scripts/loadtest/native.sh
```

Leave the two variables unset to measure SQLite only. The script builds the
rbac-go binary, starts it as a host process on a local port, measures it, and
stops it. The Postgres database `rbacgo_native` is dropped and recreated.

### Other entry points

| Command | Use |
|---|---|
| `scripts/loadtest/measure.sh <label> <port> <api-prefix>` | Measure one running configuration `RUNS` times |
| `scripts/loadtest/teardown.sh [--all]` | Remove the app containers; `--all` also removes Postgres and Valkey |
| `STAGE=go-pool scripts/loadtest/...` | Write results to `results/go-pool/` instead of `results/baseline/` |

### What each script does

| Script | Does |
|---|---|
| [env.sh](../scripts/loadtest/env.sh) | Shared settings, the probe identity, and helpers. Sourced by every other script |
| [baseline.sh](../scripts/loadtest/baseline.sh) | Runs the full baseline in the order above |
| [up-baseline.sh](../scripts/loadtest/up-baseline.sh) | Postgres and Valkey containers; sets `max_connections` (stops app containers first, because `ALTER SYSTEM` needs a free connection) |
| [up-django.sh](../scripts/loadtest/up-django.sh) | Django image, migrate, [django_fixture.py](../scripts/loadtest/django_fixture.py), container with `ACCESS_CACHE_ENABLED` set by `on` or `off` |
| [up-go.sh](../scripts/loadtest/up-go.sh) | rbac-go image; drops and recreates the `rbacgo` database; migrate, seed, bootstrap; container |
| [measure.sh](../scripts/loadtest/measure.sh) | Thin wrapper around `measure_config` in `env.sh` |
| [loadtest.py](../scripts/loadtest/loadtest.py) | The load generator (stdlib only) |
| [capture-env.sh](../scripts/loadtest/capture-env.sh) | Writes `ENV.txt`: commits and uncommitted changes, image IDs, server versions, host |
| [report.py](../scripts/loadtest/report.py) | Writes `REPORT.md` and `report.svg` from the run JSON files |
| [native.sh](../scripts/loadtest/native.sh) | Native reference rows |
| [teardown.sh](../scripts/loadtest/teardown.sh) | Removes containers |

### Settings

Set these in the environment to override the defaults in [env.sh](../scripts/loadtest/env.sh):

| Variable | Default | Meaning |
|---|---|---|
| `RUNS` | 3 | Measured runs per configuration |
| `REQUESTS` | 5000 | Measured requests per run (after a prime pass and 200 warmup requests) |
| `CONCURRENCY` | 20 | Concurrent threads, each with one keep-alive connection |
| `USER_COUNT` | 100 | Users `loaduser-0` .. `loaduser-(N-1)` in one group |
| `ORG_ID`, `ACCOUNT` | `100001`, `1111111` | Tenant the users belong to |
| `PG_MAX_CONNECTIONS` | 300 | Postgres `max_connections` for the baseline database |
| `PG_PORT`, `VALKEY_PORT`, `DJANGO_PORT`, `GO_PORT` | 15433, 16379, 18092, 18093 | Host ports |
| `RBAC_GO_DIR`, `INSIGHTS_RBAC_DIR` | derived from the script location | Checkout locations |
| `STAGE` | `baseline` | Results subdirectory under `results/` |

## Setup

| Component | Version | Runs as | Notes |
|---|---|---|---|
| rbac-go | commit `0356acd` at measurement time, image `rbac-go:loadtest` | container, port 18093 | [Dockerfile](../Dockerfile); migrates on start |
| insights-rbac (Django) | commit `aa7fd4b`, image `rbac-django-baseline` | container, port 18092 | gunicorn from the image; `CONN_MAX_AGE=60` per [settings.py](https://github.com/RedHatInsights/insights-rbac/blob/master/rbac/rbac/settings.py) |
| Postgres | `postgres:18.1` (server 18.1) | container `rbac-baseline-db`, port 15433 | `max_connections = 300` |
| Valkey | `valkey/valkey:8` (server 8.1.6) | container `rbac-baseline-redis`, port 16379 | Django access cache uses DB 1 |

Host: Apple M1 Max, 10 logical CPUs, macOS (Darwin arm64), Docker server 29.4.0. The
full record is in [ENV.txt](../scripts/loadtest/results/baseline/ENV.txt), which also
lists the tracked files that were uncommitted when the baseline ran.

Apps reach Postgres and Valkey through `host.docker.internal`. The load generator
runs on the host and hits the published ports.

### Data

Org `100001`, account `1111111`, 100 users `loaduser-0` .. `loaduser-99`, all in one
group with a `cost-management:*:*` grant.

* rbac-go: `migrate`, `seed`, `bootstrap --org 100001 --account 1111111 --users …`.
* Django: `migrate`, then [django_fixture.py](../scripts/loadtest/django_fixture.py)
  creates the same tenant, users, group, role and grant.

### Load profile

`GET <prefix>/access/?application=cost-management&limit=100` with a per-user
`X-Rh-Identity` header. The Django prefix is `/api/v1`; the rbac-go prefix is
`/api/rbac/v1`.

1. One sequential prime request per user, excluded from the stats. This makes the
   first measured request for each user a cache hit when the cache is on.
2. 200 warmup requests, excluded.
3. The measured requests: each picks a user uniformly at random, seeded per thread.

### What the two Django modes change

`ACCESS_CACHE_ENABLED` controls the per-principal access policy cache in
[cache.py](https://github.com/RedHatInsights/insights-rbac/blob/master/rbac/management/cache.py)
and [access/view.py](https://github.com/RedHatInsights/insights-rbac/blob/master/rbac/management/access/view.py).
The tenant and principal caches stay on in both modes. The "policy cache off" row
therefore still has Valkey round trips for tenant and principal lookups. It does
not measure a Django with no Redis at all.

## Measurement method

[loadtest.py](../scripts/loadtest/loadtest.py) writes one JSON file per run:

* `rps`: successful requests divided by wall-clock elapsed time.
* `mean_ms`, `p50_ms`, `p95_ms`, `p99_ms`, `max_ms`: over successful (200) responses.
* `errors`: non-200 responses plus exceptions. The tool does not record which kind,
  so a cause has to be inferred from logs.

[report.py](../scripts/loadtest/report.py) takes the median of the runs for each
latency and throughput value, the worst max, and the sum of errors. Medians are
used because single runs of the container configurations vary (see
[Finding 3](#findings)).

## Findings

1. **The Django cache is worth about 2.8× on median latency and throughput.**
   Without it (53.5 ms, 325 req/s) Django is well behind rbac-go on every metric.
   With it (18.9 ms, 890 req/s) it has the best p95 and p99 in the baseline.
   The cache hits were verified during setup: a repeated identical request logged
   no policy write, and the Valkey `rbac::policy::*` keys were present after a run.

2. **rbac-go's tail is the first target.** Median is good (11.6 ms container, 2.3 ms
   native Postgres), but p95 is 42 ms in the container and 24 ms natively, and p99 is
   52 ms in the container. The native SQLite row (p95 9.7 ms) shows the gap is in the
   Postgres path, not in request handling. The likely cause is `database/sql` pool
   defaults: [store.go](../internal/store/store.go) sets no pool limits for Postgres,
   so the default of 2 idle connections means each burst at concurrency 20 opens and
   closes connections. Django keeps its connections (`CONN_MAX_AGE=60`). **Not verified.**
   The first optimization stage tests it: set `SetMaxIdleConns` and `SetMaxOpenConns`
   to match the expected concurrency, then measure against this baseline.

3. **Container runs stall intermittently.** In an earlier session, two of three rbac-go
   container runs each held one request for about 20–30 s (one counted as an error, the
   tool's 30 s socket timeout, inferred from the elapsed time). The three baseline runs
   showed no stall (worst max 86.8 ms). The stalls are not reproducible on demand yet, so
   the baseline is not a guarantee against them. The next stage should run more repetitions
   of the rbac-go row and watch the max and elapsed columns.

4. **Django needs `max_connections` raised for this workload.** At the default of 100,
   51 of 2000 requests returned 500 with `too many clients already`. Each gunicorn worker
   thread holds its own persistent connection, so the connection budget has to cover
   workers × threads. The benchmark sets 300 on the test database so the run measures the
   request path, not connection exhaustion. On-prem sizing should use the same arithmetic.

5. **Single slow requests on Django.** Three Django runs had a worst request of 0.7 to
   0.8 s: warm run 1 (738.7 ms) and no-cache runs 1 and 2 (788.5 ms and 782.4 ms). The
   other Django runs peak at 157 ms or below. The cause is not established. Candidates
   are gunicorn workers opening their first database connections under load. The max
   column shows the worst run, so these are visible in the table.

6. **Earlier numbers were invalid and were replaced.** The first version of the tool
   walked the user list in lockstep, so all 20 threads hit the same user at once. That
   caused a cache stampede on each user's first request. The first Django run (377 req/s,
   mean 53 ms) was an artifact of it. The tool now picks users at random per thread and
   primes each user once. Every number in this document comes from the fixed tool.

## Open items

* **Stage `go-pool`**: test Finding 2. Rebuild the image with pool settings, then
  `REBUILD=1 STAGE=go-pool scripts/loadtest/up-go.sh` and
  `STAGE=go-pool scripts/loadtest/measure.sh go 18093 /api/rbac/v1`.
* **Stall reproduction** (Finding 3): run the rbac-go row with `RUNS=10` and record whether
  the max or elapsed columns show a stall.
* **Remove `host.docker.internal` from the path**: put Postgres, Valkey and both apps on
  one user-defined bridge network, to check whether the container tail depends on the
  Docker VM hop.
* **Record error kinds** in `loadtest.py` (status code or exception class), so a stall
  like Finding 3 can be attributed without inference.
* **Cross-stage comparison**: `report.py` reads one results directory. A comparison
  chart across stages (for example `baseline` against `go-pool`) is not written yet.
* **More data shapes**: one org, one group, one role here. Real koku traffic spans many
  orgs, groups and principals.
* **Django log error**: `Role seeding failed` (`ValueError: badly formed hexadecimal UUID`)
  appears at container start. It was not investigated. The measurement does not depend
  on seeds, because the fixture creates its own role and grant.

## Artifacts

Per stage, under [scripts/loadtest/results/](../scripts/loadtest/results/):

| File | Content |
|---|---|
| `<label>-<n>.json` | One run: the load tool's output |
| `ENV.txt` | Commits (with uncommitted changes listed), image IDs, server versions, host, load profile |
| `REPORT.md` | Medians table and per-run table |
| `report.svg` | The chart embedded at the top of this document |
| `<label>-server.log` | Native stage only: rbac-go server log |

Results committed here: [baseline](../scripts/loadtest/results/baseline/) (three
configurations, three runs each) and [native](../scripts/loadtest/results/native/)
(two configurations, three runs each).
