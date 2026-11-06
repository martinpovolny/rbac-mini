# rbac-go — Minimal RBAC v1 Replacement Spec

Go replacement for `insights-rbac`, scoped to what `../koku` and
`../koku-service-operator` need on-prem. No full rewrite.

## 1. Scope

### In scope (must-have)

* `GET /api/rbac/v1/access/?application=&limit=&offset=&order_by=` — the only
  endpoint koku calls at runtime (`koku/koku/rbac.py:189-262`,
  `rbac/management/access/view.py:192-221`).
* `GET /api/rbac/v1/status/`, `GET /api/rbac/v1/ready/` — probes
  (`rbac/api/urls.py:27-31`, `operator/internal/resources/rbac.go:129`).
* `GET /metrics` — Prometheus, ServiceMonitor compat.
* Seed job — loads `cost-management` / `sources` roles+permissions from the
  existing JSON seed files into Postgres (replaces
  `python manage.py migrate + seeds --skip-notifications`,
  `operator/internal/resources/migration.go:147-205`,
  `rbac_seed_configmap.go:12-104`).
* Bootstrap/sync job — creates `Tenant`, `Group`+`Policy`+`Principal` rows and
  syncs Keycloak users into `Principal` (replaces `manage.py shell` ORM
  scripts in `test/pytest/rbac_bootstrap_scripts.py`,
  `test/pytest/suites/e2e/test_rbac_access.py:198-317`, and
  `rbac_keycloak_sync.go:34-128`). Direct SQL, no Django ORM.
* Identity header auth — decode `x-rh-identity` (base64 JSON), resolve tenant,
  derive `User{username, admin, org_id, client_id, is_service_account}`
  (`rbac/rbac/middleware.py`, `api/models.py:110-152`).

### Out of scope

* All of v2: `workspaces/, role-bindings/, roles/, principals/` + Kessel/SpiceDB.
* Rest of v1 CRUD: `groups/, roles/, permissions/, principals/, auditlogs/,`
  `cross-account-requests/, tenant/opt-in/` — e2e bypasses it via ORM because
  "RBAC API rejects custom roles for cost-management"
  (`test_rbac_access.py:507-508`).
* `MCP mcp/`, `_private/*` (~30 utils), `migration_tool`, Kafka/Debezium
  outbox, Celery worker/beat, BOP/IT/UMB/notifications.
* Valkey/Redis cache. Phase 1 = direct SQL on every request (no cache
  process). `ACCESS_CACHE_ENABLED` / `REDIS_*` envs accepted as no-ops so Helm
  chart tests pass; `cache.clear()` / `FLUSHALL` become no-ops. Koku-side 30s
  cache (`koku/middleware.py:380-393`) absorbs the load. Add `go-redis` only
  if load tests demand it, using existing key format
  `rbac::policy::tenant={org}::user={uuid}` (`management/cache.py:200-260`).

## 2. API Contract

### GET /api/rbac/v1/access/

Request: `application=cost-management[,sources]`, `limit` (default 10 in DRF,
koku sends `limit=100`), `offset`, `order_by=[-](application|resource_type|verb)`.
Header `X-Rh-Identity: <base64>` required. No DRF permission gate
(`view.py:161`).

Response (must be byte-compatible — koku follows `links.next` as
`base_url + next_link`, `koku/rbac.py:198-238`). Canonical contract is
`rbac-go/openapi.v1-subset.json`, extracted from
`docs/source/specs/openapi.json` (paths `/access/`, `/status/` only):

```json
{
  "meta": {"count": 1},
  "links": {"first": "...", "next": "...", "previous": "...", "last": "..."},
  "data": [
    {"permission": "cost-management:aws.account:read",
     "resourceDefinitions": [{"attributeFilter": {"key": "...", "operation": "equal|in", "value": "..."}}]}
  ]
}
```

Query semantics (`management/querysets.py:244-270`, `access/view.py:163-190`):

1. Resolve `Principal` by username + tenant (lowercased, `principal/model.py:55-58`).
   Unknown principal → empty `data`, HTTP 200 (koku treats `[]` as denied).
2. Join principal → `group_principals` → groups (tenant-scoped + `public`
   tenant platform roles) → policies → policy_roles → roles → access →
   permission + resourceDefinitions.
3. Filter `permission.application IN (requested apps)`. Include
   `platform_default` and `admin_default` (org-admin only) groups
   (`querysets.py:291-316`).
4. Deduplicate on `(permission_id, attribute_filter)` with portable
   `SELECT DISTINCT` / `GROUP BY` (never Postgres `DISTINCT ON` —
   `access/view.py:163-166` is PG-only). Order/paginate in Go. `status` param:
   only `enabled` supported.

Errors: `400` on bad `order_by`/`status`/v2-org misuse; never `403` from this
endpoint (koku maps non-200 `[]` → deny, `5xx`/conn-error → `424`).

### GET /api/rbac/v1/status/, /ready/, /metrics

* `status/` → `{"status": "ok"}` (probe target, `rbac.go:48` `BYPASS_BOP`).
* `ready/` → `200` when the SQL DB is reachable (`sql.Ping`), else `503`.
  Must work identically for SQLite file and Postgres.
* `metrics/` → Prometheus exposition (at minimum `up`, request counters by
  `url_name`; keep label names stable for `dashboards/`).

## 3. Data Model (10 tables) — SQLite + Postgres portable

Fresh schema, no Django tables. One migration chain applied to both engines
(`golang-migrate` or `goose` with per-dialect files kept identical in effect).
SQLite is the default for dev/test (`DATABASE_URL=sqlite://file.db` or
in-memory); Postgres is prod (operator). CI runs the same suite against both.

Portable type rules (no PG-only DDL):

* PKs: `INTEGER PRIMARY KEY` (autoincrement on both; no `BIGSERIAL`, no
  `SERIAL`, no `GENERATED ...`).
* UUIDs: `TEXT` (36-char string, generated in Go via `google/uuid`; no
  `UUID`/`pgcrypto` type).
* Booleans: `INTEGER 0/1` with Go `bool` mapping (no `BOOL DEFAULT TRUE`
  assumptions; always write explicit `0/1`).
* Timestamps: `TEXT` ISO-8601 UTC (`created`, `modified`); never `timestamptz`,
  `now()`, or PG date functions.
* JSON: `TEXT` column holding a JSON document (`attribute_filter`); marshal /
  unmarshal in Go. No `JSONB`, no JSON operators (`->`, `->>`, `@>`,
  `has_key`), no `GIN` indexes. Filtering on `attributeFilter` content happens
  in Go after `SELECT`, exactly as the Django serializer does.
* Strings: `TEXT` / `VARCHAR(n)` only; `LIKE` with explicit `LOWER(col)`
  instead of `ILIKE`/`icontains`.
* Constraints: plain `UNIQUE`, `FOREIGN KEY ... ON DELETE CASCADE`,
  single-column indexes only. No `ArrayField`, `GIN`/`Trigram`, `CONCURRENTLY`,
  partial/expression indexes.

| Table | Columns (portable types) |
|---|---|
| `tenants` | `id INTEGER PK, org_id TEXT UNIQUE, tenant_name TEXT, account_id TEXT, ready INTEGER` |
| `principals` | `id INTEGER PK, uuid TEXT UNIQUE, username TEXT (lowercase), type TEXT, service_account_id TEXT, user_id TEXT, cross_account INTEGER, tenant_id INTEGER FK` + unique `(username, tenant_id)` |
| `groups` | `id INTEGER PK, uuid TEXT UNIQUE, name TEXT, description TEXT, platform_default INTEGER, system INTEGER, admin_default INTEGER, tenant_id INTEGER FK` + unique `(name, tenant_id)` |
| `group_principals` | `group_id INTEGER FK, principal_id INTEGER FK`, PK `(group_id, principal_id)` |
| `policies` | `id INTEGER PK, uuid TEXT UNIQUE, name TEXT, group_id INTEGER FK NULL, system INTEGER, tenant_id INTEGER FK` + unique `(name, tenant_id)` |
| `policy_roles` | `policy_id INTEGER FK, role_id INTEGER FK`, PK `(policy_id, role_id)` |
| `roles` | `id INTEGER PK, uuid TEXT UNIQUE, name TEXT, display_name TEXT, system INTEGER, platform_default INTEGER, admin_default INTEGER, tenant_id INTEGER FK` + unique `(name, tenant_id)` |
| `permissions` | `id INTEGER PK, application TEXT, resource_type TEXT, verb TEXT, permission TEXT UNIQUE, tenant_id INTEGER FK` |
| `access` | `id INTEGER PK, role_id INTEGER FK, permission_id INTEGER FK, tenant_id INTEGER FK` |
| `resource_definitions` | `id INTEGER PK, access_id INTEGER FK, attribute_filter TEXT (JSON), tenant_id INTEGER FK` |

Indexes: all `tenant_id` FKs, `permissions(application)`, `principals(username, tenant_id)`.

Banned in all queries/migrations (PG-only, all present in the Django codebase):
`DISTINCT ON`, `RETURNING` (except where abstracted), `SELECT ... FOR UPDATE`,
`pgtransaction`/serializable retries, `RawSQL` recursive CTEs, `ArrayField`,
`GIN`/`Trigram`, `ILIKE`, JSON operators, `psycopg2`-specific error handling.
Concurrency: single-writer seed/sync jobs + idempotent `INSERT ... ON CONFLICT
DO NOTHING` where supported by both dialects (else `SELECT`-then-`INSERT` in a
transaction); the read path takes no locks.

Placeholder policy: write queries once with `?` placeholders and rebind to
`$n` for Postgres (e.g. `sqlx.Rebind`), so the same query text runs on both.

Seed data: `public` tenant + `Cost Administrator` (`cost-management:*:*`),
`Sources administrator`, platform-default viewer roles — same permission
strings as `operator/internal/resources/rbac_seed/data/*.json`. Seed job must be
idempotent (`get_or_create` semantics).

## 4. Identity Header

`x-rh-identity` = base64 JSON: `{org_id, identity:{type: User|ServiceAccount,
user:{username, is_org_admin}, service_account:{client_id}}, entitlements}`.
Lowercase usernames. Service accounts never org-admin. Missing/invalid header
→ `401`; valid header, unknown tenant → auto-create tenant row (`ready=true`),
empty access (matches bootstrap behavior).

## 5. Operator Integration

* Env accepted: `API_PATH_PREFIX=/api/rbac`, `DATABASE_URL` (preferred:
  `sqlite:///path.db` for dev/test, `postgres://...` for prod) or legacy
  `DATABASE_{HOST,PORT,NAME,USER,PASSWORD}` (Postgres),
  `REDIS_HOST/PORT/*` (ignored), `DJANGO_SECRET_KEY` (ignored),
  `ACCESS_CACHE_ENABLED` (ignored, treat as false), `ROLE_CREATE_ALLOW_LIST`.
* Listens `:8080`, plain HTTP (`RBACAPIDeployment`, `RBACAPIService`,
  `RBAC_SERVICE_{HOST,PORT,PATH=/api/rbac/v1/access/,PROTOCOL=http}`).
* Migration+seed as `initContainer` or `Job` equivalent to
  `migration.go:147-205` (idempotent SQL, no `manage.py`).
* Keycloak sync as `CronJob` equivalent: list realm users via client-credentials,
  upsert `principals`, add to org group, `SYNC_PRUNE_ORPHANS` deletes stale rows.
* Probes `GET /api/rbac/v1/status/`; no Celery, no Kafka, no Clowder dependency
  (`CLOWDER_ENABLED=false, KAFKA_ENABLED=false`).

## 6. Non-Goals / Non-Requirements

No Valkey client, no JWT/BOP/IT-service calls, no write endpoints, no authz
decision beyond the access-list query, no cross-account flows.

## 7. Acceptance

1. `go test ./...` runs with **no Docker** against SQLite (in-memory/file) and
   covers: access query (admin, grouped user, unknown user,
   service account, `in`/`equal` filters, pagination links shape), identity
   parsing, seed idempotency.
2. Same suite passes against real Postgres in CI (`make test-sqlite`,
   `make test-postgres`); no query text differs except placeholder rebinding.
3. Koku `koku/test_rbac.py` logic passes against it (follow `links.next`,
   `424` only on `5xx`/down).
4. Operator e2e `test_rbac_access.py` personas (alice=project `payment` only,
   bob=cluster-alpha only, carol=full) return correct filtering after ORM-seed
   equivalent loaded via seed/bootstrap jobs.
5. Helm `test_external_valkey.py` env assertions still pass (envs present).
6. p99 `<50ms` for `/access/` on seeded dataset without cache (single join query).
7. `go vet` + `sqlfluff`-style review: `grep -riE "DISTINCT ON|FOR UPDATE|RETURNING|ILIKE|::jsonb|GIN|pgcrypto|ArrayField" rbac-go/` is clean.
