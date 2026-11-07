# Load test: GET /api/rbac/v1/access/ (koku critical path)

This measures the one endpoint koku calls on its hot path. It compares rbac-go
(no cache) with stock insights-rbac (Django, Valkey-cached access policy).

Question this answers: before on-prem rollout, does rbac-go need its own cache,
or is koku's per-user cache enough? Django with a warm Valkey cache is the
baseline.

## Setup

| Component | Version | Runs as | Notes |
|---|---|---|---|
| rbac-go | commit `a88f438` ([README](../README.md)), image `rbac-go:loadtest` | container, host port 18093 | built from [Dockerfile](../Dockerfile) |
| insights-rbac (Django) | commit `aa7fd4b`, image `rbac-django-baseline` | container, host port 18092 | gunicorn from the image; `CONN_MAX_AGE=60` per [settings](https://github.com/RedHatInsights/insights-rbac/blob/master/rbac/rbac/settings.py) |
| Postgres | `postgres:18.1` | container `rbac-baseline-db`, port 15433 | `max_connections = 300` (see [Finding 3](#findings)) |
| Valkey | `valkey/valkey:8` | container `rbac-baseline-redis`, port 16379 | Django access cache uses DB 1 |
| rbac-go, native | local binary | host | SQLite file DB and Homebrew Postgres 16 on `127.0.0.1:55432` |

Apps reach Postgres and Valkey through `host.docker.internal` (Docker Desktop).
The load generator runs on the host and hits the published ports.

Host: Apple Silicon Mac, Docker Desktop. Load generator: Python 3.14, stdlib only.

### Data

Org `100001`, account `1111111`, 100 users `loaduser-0` .. `loaduser-99`, all in
one group with a `cost-management:*:*` grant.

* rbac-go: `migrate`, `seed`, `bootstrap --org 100001 --account 1111111 --users …`
* Django: `migrate`, then [django_fixture.py](../tools/loadtest/django_fixture.py)
  creates the same tenant, user, group, role and grant.

### Load profile

`GET <prefix>/access/?application=cost-management&limit=100` with a per-user
`X-Rh-Identity` header. Django prefix is `/api/v1`, rbac-go is `/api/rbac/v1`.

* 20 concurrent threads, each with one keep-alive connection.
* One sequential prime request per user (excluded), then 200 warmup requests
  (excluded), then the measured requests (2000 or 5000).
* Each request picks a user uniformly at random, seeded per thread.

### Tool

[tools/loadtest/loadtest.py](../tools/loadtest/loadtest.py). Output fields:

* `rps`: successful requests divided by wall-clock elapsed time.
* `mean_ms`, `p50_ms`, `p95_ms`, `p99_ms`, `max_ms`: over 200 responses only.
* `errors`: non-200 responses plus exceptions. The tool does not record which
  kind, so an error's cause has to be inferred from logs.

## Reproduce

Commands are bash (the `$ENV` and `$GO_DB` variables rely on word splitting).

Baseline DB and Valkey:

```bash
docker run -d --name rbac-baseline-db -e POSTGRES_USER=postgres -e POSTGRES_PASSWORD=postgres \
  -e POSTGRES_DB=postgres -p 15433:5432 postgres:18.1
docker exec rbac-baseline-db psql -U postgres -c "ALTER SYSTEM SET max_connections = 300;"
docker restart rbac-baseline-db
docker run -d --name rbac-baseline-redis -p 16379:6379 valkey/valkey:8
```

Django baseline (migrations take several minutes):

```bash
cd insights-rbac && docker build -t rbac-django-baseline .
ENV="-e DATABASE_HOST=host.docker.internal -e DATABASE_PORT=15433 -e DATABASE_NAME=postgres \
  -e DATABASE_USER=postgres -e DATABASE_PASSWORD=postgres -e REDIS_HOST=host.docker.internal \
  -e REDIS_PORT=16379 -e BYPASS_BOP_VERIFICATION=True -e DJANGO_SECRET_KEY=loadtest"
docker run --rm --entrypoint python -w /opt/rbac/rbac $ENV rbac-django-baseline manage.py migrate --noinput
docker run --rm -i --entrypoint python -w /opt/rbac/rbac $ENV rbac-django-baseline manage.py shell \
  < rbac-go/tools/loadtest/django_fixture.py
docker run -d --name rbac-django-srv -p 18092:8080 $ENV rbac-django-baseline
```

rbac-go container:

```bash
cd rbac-go && docker build -t rbac-go:loadtest .
docker exec rbac-baseline-db psql -U postgres -c "CREATE DATABASE rbacgo;"
GO_DB="-e DATABASE_URL=postgres://postgres:postgres@host.docker.internal:15433/rbacgo?sslmode=disable"
docker run --rm $GO_DB --entrypoint /usr/local/bin/rbac-go rbac-go:loadtest migrate
docker run --rm $GO_DB --entrypoint /usr/local/bin/rbac-go rbac-go:loadtest seed
docker run --rm $GO_DB --entrypoint /usr/local/bin/rbac-go rbac-go:loadtest \
  bootstrap --org 100001 --account 1111111 --users "loaduser-0,…,loaduser-99"
docker run -d --name rbac-go-srv -p 18093:8080 $GO_DB rbac-go:loadtest
```

Measure:

```bash
python3 tools/loadtest/loadtest.py --base-url http://localhost:18093 --api-prefix /api/rbac/v1 \
  --org 100001 --users 'loaduser-0..99' --concurrency 20 --requests 5000 --save go.json
python3 tools/loadtest/loadtest.py --base-url http://localhost:18092 --api-prefix /api/v1 \
  --org 100001 --users 'loaduser-0..99' --concurrency 20 --requests 5000 --save django.json
```

Django's gunicorn runs `workers = cpu_resources × 2` with `threads = 10` by
default ([gunicorn.py](https://github.com/RedHatInsights/insights-rbac/blob/master/rbac/gunicorn.py)).
Each thread that serves a request keeps a Postgres connection open, so a
connection budget of 100 is too small (Finding 3).

## Results

All rows: 100 users, org 100001, C20, 5000 measured requests unless noted.
Latencies in ms.

### Native (rbac-go on the host)

| Backend | rps | mean | p50 | p95 | p99 | max | errors |
|---|---:|---:|---:|---:|---:|---:|---:|
| SQLite file DB | 4486 | 4.3 | 3.7 | 9.9 | 13.7 | 23.5 | 0 |
| Postgres 16, localhost | 2735 | 6.9 | 2.1 | 24.3 | 29.8 | 45.0 | 0 |

### Docker (Postgres 18 and Valkey containers, apps in containers)

| System | rps | mean | p50 | p95 | p99 | max | errors |
|---|---:|---:|---:|---:|---:|---:|---:|
| Django + Valkey, warm (run 1, 2000 req) ¹ | 708 | 26.4 | 21.8 | 44.4 | 78.9 | 935.7 | 0 |
| Django + Valkey, warm (run 2) | 846 | 21.7 | 20.4 | 35.4 | 43.6 | 72.3 | 0 |
| rbac-go, no cache (run 1) | 1034 | 18.8 | 11.5 | 41.8 | 51.2 | 74.7 | 0 |
| rbac-go, no cache (run 2) ² | 150 | 17.5 | 10.2 | 40.3 | 50.1 | 71.0 | 1 |
| rbac-go, no cache (run 3) ² | 226 | 21.2 | 10.0 | 40.9 | 51.6 | 19557.8 | 0 |

¹ First run after restarting the Django container, so gunicorn worker boot is
in the max. Run 2 is the representative Django number.

² Stalled runs. Run 2 ran 33.4 s elapsed with one error; its cause is inferred
(about 30 s, the tool's socket timeout) because the tool does not record error
types. Run 3 ran 22.1 s elapsed with one request at 19.6 s. Both `rps` values are
dominated by the stall, so compare percentiles across rows instead.

Raw JSON was written to `/tmp` and is not committed; regenerate with the
commands above.

## Findings

1. **Go without a cache matches Django with a warm cache on the latency that
   matters.** In the Docker setup, rbac-go p50 is 10–12 ms against 20 ms for
   Django, and p95 is about 40 ms against 35–44 ms. Django's warm hits are real:
   a repeated identical request logged no policy write, and the Valkey
   `rbac::policy::*` keys were present after the run. The earlier lockstep tool
   produced about 1600 policy writes (Finding 4). The cache's benefit on a cold
   Django was not measured (see [Open items](#open-items)).

2. **rbac-go has tail stalls in the container path.** Two of three container
   runs had a single request held for about 20–30 s. Neither the native runs
   nor the Django runs showed any. A likely cause is `database/sql` pool
   defaults: [store.go](../internal/store/store.go) sets no pool limits, so the
   default of 2 idle connections means every burst at concurrency 20 opens and
   closes connections across the Docker boundary. Django keeps its connections
   (`CONN_MAX_AGE=60`). **Not verified.** Next step: set `SetMaxIdleConns` to at
   least the expected concurrency, then rerun three times.

3. **Django exhausts Postgres at the default `max_connections = 100`.** The first
   container run had 51 of 2000 requests return 500 with `too many clients
   already`. Each gunicorn worker thread holds its own persistent connection.
   The benchmark sets `max_connections = 300` on the test database so the run
   measures the request path, not connection exhaustion. The on-prem
   connection budget should use the same arithmetic.

4. **The first Django and native Go numbers were invalid, and were measured
   again.** The first version of the tool walked the user list in lockstep, so
   all 20 threads hit the same user at once. That caused a cache stampede on each
   user's first request. The first Django run (377 rps, mean 53 ms) was an
   artifact of that. The tool now picks users at random per thread and primes
   each user once. All numbers in this document come from the fixed tool.

## Open items

* **Django with Valkey disabled.** Not run. Would show what the cache is
  worth: restart `rbac-django-srv` with `-e ACCESS_CACHE_ENABLED=False`
  (read in [settings.py](https://github.com/RedHatInsights/insights-rbac/blob/master/rbac/rbac/settings.py))
  and repeat the Django row.
* **Connection-pool fix for rbac-go.** Hypothesis in Finding 2 is unverified.
* **Remove `host.docker.internal` from the path.** Put Postgres, Valkey and
  both apps on one user-defined bridge network so the stalls can be checked
  against a path with no VM hop.
* **More runs and data shapes.** Three container runs are not enough to settle
  the rps numbers. The workload is also one org, one group, one role. Real koku
  traffic has many orgs, groups and principals.
* **Django log error.** `Role seeding failed` (`ValueError: badly formed
  hexadecimal UUID`) appeared at container start. Not investigated. The
  measurement does not depend on seeds: the fixture creates its own role and grant.
* **rbac-go error types.** The tool should record error kinds (status code or
  exception class) so a stall like run 2 can be attributed without guessing.
