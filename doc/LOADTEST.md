# Load test: GET /api/rbac/v1/access/ (koku critical path)

Tool: `tools/loadtest/loadtest.py` (stdlib only — `urllib` + threads, no
`pip install` needed). Each worker thread keeps one persistent HTTP
connection, so the numbers reflect server + DB time, not handshakes.

## Method

Same shape koku uses: `GET /api/rbac/v1/access/?application=cost-management&limit=100`
with a real `x-rh-identity` header, 100 distinct users, 20 concurrent
workers, 200-request warmup (excluded), 2000 measured requests.

```bash
# 1. seed (100 users; all in one group for this round)
USERS=$(python3 -c "print(','.join(f'u{i}' for i in range(100)))")
DATABASE_URL=... rbac-go migrate && rbac-go seed
DATABASE_URL=... rbac-go bootstrap --org 100001 --account 1111111 --users "$USERS"
DATABASE_URL=... rbac-go serve --addr 127.0.0.1:8080

# 2. measure
python3 tools/loadtest/loadtest.py --base-url http://127.0.0.1:8080 \
  --org 100001 --users 'u0..99' --concurrency 20 --requests 2000 \
  --save /tmp/go-sqlite.json
# compare a second run: --compare /tmp/go-sqlite.json
```

## rbac-go results (2026-11-07, Apple Silicon, 100 users, C20, 2000 req)

| backend | rps | mean | p50 | p95 | p99 | max | errors |
|---|---|---|---|---|---|---|---|
| SQLite (`:memory:` semantics, file DB) | 4431 | 4.4ms | 3.7ms | 9.9ms | 14.3ms | 31.6ms | 0 |
| Postgres 16 (localhost TCP) | 2693 | 7.0ms | 2.4ms | 23.6ms | 27.1ms | 44.5ms | 0 |

Raw JSON: `/tmp/go-sqlite.json`, `/tmp/go-pg.json` (regenerate with the
commands above; result files are not committed).

Caveats:

* SQLite opens with `MaxOpenConns(1)`, so concurrent requests serialize on
  one connection — fine for dev, irrelevant for prod (Postgres).
* rbac-go has **no cache**: every request runs the full join. koku shields
  this with its own 30s per-user Redis cache, so steady-state load is
  ~1 query per user per 30s, not per request.
* All 100 users share one group here (bootstrap shape). Per-user groups
  would add rows, not joins — same query plan.

## Django + Redis baseline (pending)

Same tool, same shape, against stock insights-rbac:

```bash
# postgres:15432 + redis:6379 up (docker), pipenv install, then:
DATABASE_HOST=localhost DATABASE_PORT=15432 REDIS_HOST=localhost \
  python manage.py migrate --noinput && python manage.py seeds --skip-notifications
# bootstrap equivalent of the Go seed (CI Test Admin + Cost Administrator),
# serve with gunicorn (runserver is single-threaded — do NOT use it for numbers):
# gunicorn rbac.wsgi --bind=0.0.0.0:8000 --workers=2 --threads=4
python3 tools/loadtest/loadtest.py --base-url http://localhost:8000 \
  --org <org> --users 'u0..99' --concurrency 20 --requests 2000 \
  --compare /tmp/go-pg.json
```

First request warms the Redis `AccessCache`; steady state should be
cache-hit dominated — that gap (cached Django vs uncached Go) is exactly
what this baseline is for: it tells us whether Go needs a cache before
on-prem rollout, or whether koku-side caching already covers it.
