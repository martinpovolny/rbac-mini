# rbac-go — Minimal RBAC v1 Replacement Spec

Go replacement for `insights-rbac`, scoped to what `../koku` and
`../koku-service-operator` need on-prem. No full rewrite.

## 1. Scope

### In scope (must-have)

* `GET /api/rbac/v1/access/?application=&limit=&offset=&order_by=` — the only
  endpoint koku calls at runtime ([koku/koku/rbac.py:189-262](https://github.com/project-koku/koku/blob/main/koku/koku/rbac.py#L189-L262),
  [rbac/management/access/view.py:192-221](https://github.com/RedHatInsights/insights-rbac/blob/master/rbac/management/access/view.py#L192-L221)).
* `GET /api/rbac/v1/status/`, `GET /api/rbac/v1/ready/` — probes
  ([rbac/api/urls.py:27-31](https://github.com/RedHatInsights/insights-rbac/blob/master/rbac/api/urls.py#L27-L31), [operator/internal/resources/rbac.go:129](https://github.com/project-koku/koku-service-operator/blob/main/internal/resources/rbac.go#L129)).
* `GET /metrics` — Prometheus, ServiceMonitor compat.
* Seed job — loads `cost-management` / `sources` roles+permissions from the
  existing JSON seed files into Postgres (replaces
  `python manage.py migrate + seeds --skip-notifications`,
  [operator/internal/resources/migration.go:147-205](https://github.com/project-koku/koku-service-operator/blob/main/internal/resources/migration.go#L147-L205),
  [rbac_seed_configmap.go:12-104](https://github.com/project-koku/koku-service-operator/blob/main/internal/resources/rbac_seed_configmap.go#L12-L104)).
* Bootstrap/sync job — creates `Tenant`, `Group`+`Policy`+`Principal` rows and
  syncs Keycloak users into `Principal` (replaces `manage.py shell` ORM
  scripts in [test/pytest/rbac_bootstrap_scripts.py](https://github.com/project-koku/koku-service-operator/blob/main/test/pytest/rbac_bootstrap_scripts.py),
  [test/pytest/suites/e2e/test_rbac_access.py:198-317](https://github.com/project-koku/koku-service-operator/blob/main/test/pytest/suites/e2e/test_rbac_access.py#L198-L317), and
  [rbac_keycloak_sync.go:34-128](https://github.com/project-koku/koku-service-operator/blob/main/internal/resources/rbac_keycloak_sync.go#L34-L128)). Direct SQL, no Django ORM.
* Identity header auth — decode `x-rh-identity` (base64 JSON), resolve tenant,
  derive `User{username, admin, org_id, client_id, is_service_account}`
  ([`rbac/rbac/middleware.py`](https://github.com/RedHatInsights/insights-rbac/blob/master/rbac/rbac/middleware.py), [`api/models.py:110-152`](https://github.com/RedHatInsights/insights-rbac/blob/master/rbac/api/models.py#L110-L152)).
* On-prem UI (v1 User Access, [koku-ui/apps/rbac-ui-onprem](https://github.com/project-koku/koku-ui/tree/main/apps/rbac-ui-onprem) federated wrapper;
  see doc/FINDINGS.md §7). The operator runs with
  `BYPASS_BOP_VERIFICATION=True`, and rbac-go replaces the upstream
  `foo`/`bar`/`baz` stubs ([management/principal/proxy.py:173](https://github.com/RedHatInsights/insights-rbac/blob/master/rbac/management/principal/proxy.py#L173))
  with the profile the Keycloak sync persisted (`email`, `first_name`,
  `last_name`, `is_active`, `is_org_admin`, `user_id`, `type`; email falls
  back to `<username>@local`). The same contract is returned for group
  members.
  * `GET /api/rbac/v1/principals/?limit=&offset=&usernames=&email=&match_criteria=&status=&admin_only=&username_only=&sort_order=&type=`
    — Users page (paginated, `meta{count,limit,offset}`). Defaults mirror
    Django: `status=enabled`, `match_criteria=exact`, `sort_order=asc`,
    `type=user`; `admin_only` applies only to unfiltered requests. An exact
    `usernames=` lookup of an unknown user returns a BYPASS-shaped stub, so
    the user-detail page can tell "filtered out" from "does not exist".
  * `GET /api/rbac/v1/roles/` + `POST|GET|PUT|PATCH|DELETE /api/rbac/v1/roles/{uuid}/`
    (+ `GET .../access/`) — Roles pages + creation wizard. Create validates
    `application ∈ ROLE_CREATE_ALLOW_LIST`
    ([management/role/view.py:validate_role](https://github.com/RedHatInsights/insights-rbac/blob/master/rbac/management/role/view.py)); `resourceDefinitions: []`
    means global scope and is valid. The list honours `name`/`display_name`
    with `name_match`, `system`, and `order_by=[-](name|display_name|modified|policyCount)`.
  * `GET /api/rbac/v1/groups/` + membership/role sub-paths used by the UI
    (list, detail, principals, roles) — Groups pages. The list honours
    `name` (with `name_match`), `platform_default`, `admin_default`,
    `system`, and `order_by=[-](name|modified|principalCount|policyCount)`.
    Group members reuse the principal contract and filters.
  * `GET /api/rbac/v1/permissions/?allowed_only=&exclude_globals=` +
    `GET /api/rbac/v1/permissions/options/?field=application&allowed_only=` —
    wizard Step 2 (`allowed_only` filters to `ROLE_CREATE_ALLOW_LIST`).

### Out of scope

* All of v2: `workspaces/, role-bindings/, roles/, principals/` + Kessel/SpiceDB.
* Rest of v1: `auditlogs/`, `cross-account-requests/`, `tenant/opt-in/`,
  service-accounts/IT-service paths — not used by koku or the on-prem UI.
  (Note: e2e bypasses role CRUD via ORM only because the Django API rejects
  custom roles for cost-management allow-list reasons;
  [test_rbac_access.py:507-508](https://github.com/project-koku/koku-service-operator/blob/main/test/pytest/suites/e2e/test_rbac_access.py#L507-L508) — rbac-go implements the validated path
  directly.)
* `MCP mcp/`, `_private/*` (~30 utils), `migration_tool`, Kafka/Debezium
  outbox, Celery worker/beat, BOP/IT/UMB/notifications.
* Valkey/Redis cache. Phase 1 = direct SQL on every request (no cache
  process). `ACCESS_CACHE_ENABLED` / `REDIS_*` envs accepted as no-ops so Helm
  chart tests pass; `cache.clear()` / `FLUSHALL` become no-ops. Koku-side 30s
  cache ([koku/middleware.py:380-393](https://github.com/project-koku/koku/blob/main/koku/koku/middleware.py#L380-L393)) absorbs the load. Add `go-redis` only
  if load tests demand it, using existing key format
  `rbac::policy::tenant={org}::user={uuid}` ([management/cache.py:200-260](https://github.com/RedHatInsights/insights-rbac/blob/master/rbac/management/cache.py#L200-L260)).

## 2. API Contract

### GET /api/rbac/v1/access/

Request: `application=cost-management[,sources]`, `limit` (default 10 in DRF,
koku sends `limit=100`), `offset`, `order_by=[-](application|resource_type|verb)`.
Header `X-Rh-Identity: <base64>` required. No DRF permission gate
([`view.py:161`](https://github.com/RedHatInsights/insights-rbac/blob/master/rbac/management/access/view.py#L161)).

Response (must be byte-compatible — koku follows `links.next` as
`base_url + next_link`, [koku/rbac.py:198-238](https://github.com/project-koku/koku/blob/main/koku/koku/rbac.py#L198-L238)). Canonical contract is
[openapi.v1-subset.json](openapi.v1-subset.json), extracted from
[docs/source/specs/openapi.json](https://github.com/RedHatInsights/insights-rbac/blob/master/docs/source/specs/openapi.json) (paths `/access/`, `/status/` only):

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

Query semantics ([management/querysets.py:244-270](https://github.com/RedHatInsights/insights-rbac/blob/master/rbac/management/querysets.py#L244-L270), [access/view.py:163-190](https://github.com/RedHatInsights/insights-rbac/blob/master/rbac/management/access/view.py#L163-L190)):

1. Resolve `Principal` by username + tenant (lowercased, [principal/model.py:55-58](https://github.com/RedHatInsights/insights-rbac/blob/master/rbac/management/principal/model.py#L55-L58)).
   Unknown principal → empty `data`, HTTP 200 (koku treats `[]` as denied).
2. Join principal → `group_principals` → groups (tenant-scoped + `public`
   tenant platform roles) → policies → policy_roles → roles → access →
   permission + resourceDefinitions.
3. Filter `permission.application IN (requested apps)`. Include
   `platform_default` and `admin_default` (org-admin only) groups
   ([querysets.py:291-316](https://github.com/RedHatInsights/insights-rbac/blob/master/rbac/management/querysets.py#L291-L316)).
4. Deduplicate on `(permission_id, attribute_filter)` with portable
   `SELECT DISTINCT` / `GROUP BY` (never Postgres `DISTINCT ON` —
   [access/view.py:163-166](https://github.com/RedHatInsights/insights-rbac/blob/master/rbac/management/access/view.py#L163-L166) is PG-only). Order/paginate in Go. `status` param:
   only `enabled` supported.

Errors: `400` on bad `order_by`/`status`/v2-org misuse; never `403` from this
endpoint (koku maps non-200 `[]` → deny, `5xx`/conn-error → `424`).

### Role and group shapes

`created` and `modified` are fixed-width UTC timestamps
(`2006-01-02T15:04:05.000000000Z`), so TEXT ordering is chronological on both
databases. Existing rows are backfilled by `migrate`; create/update/patch stamp
them. The UI renders "Last modified" from `modified` on roles, groups, group
roles and user-detail role rows.

* Role list item: `uuid, name, display_name, description?, system, created,
  modified`. Role detail adds `access: [{permission, resourceDefinitions}]`.
* Group list/detail: `uuid, name, description?, principalCount, roleCount,
  platform_default, admin_default, system, created, modified`.
* Group members and `?username_only=true` return the principal contract from
  §4, with the same `status`/`admin_only` filters as `/principals/`.

### GET /api/rbac/v1/status/, /ready/, /metrics

* `status/` → `{"api_version": 1}` (probe target, [`rbac.go:48`](https://github.com/project-koku/koku-service-operator/blob/main/internal/resources/rbac.go#L48) `BYPASS_BOP`).
* `ready/` → `200 {"ready": true}` when the SQL DB is reachable (`sql.Ping`),
  else `503`. Must work identically for SQLite file and Postgres.
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
| `principals` | `id INTEGER PK, uuid TEXT UNIQUE, username TEXT (lowercase), type TEXT, service_account_id TEXT, user_id TEXT, email TEXT, first_name TEXT, last_name TEXT, is_org_admin INTEGER, is_active INTEGER, cross_account INTEGER, tenant_id INTEGER FK` + unique `(username, tenant_id)` |
| `groups` | `id INTEGER PK, uuid TEXT UNIQUE, name TEXT, description TEXT, platform_default INTEGER, system INTEGER, admin_default INTEGER, created TEXT, modified TEXT, tenant_id INTEGER FK` + unique `(name, tenant_id)` |
| `group_principals` | `group_id INTEGER FK, principal_id INTEGER FK`, PK `(group_id, principal_id)` |
| `policies` | `id INTEGER PK, uuid TEXT UNIQUE, name TEXT, group_id INTEGER FK NULL, system INTEGER, tenant_id INTEGER FK` + unique `(name, tenant_id)` |
| `policy_roles` | `policy_id INTEGER FK, role_id INTEGER FK`, PK `(policy_id, role_id)` |
| `roles` | `id INTEGER PK, uuid TEXT UNIQUE, name TEXT, display_name TEXT, description TEXT, system INTEGER, platform_default INTEGER, admin_default INTEGER, created TEXT, modified TEXT, tenant_id INTEGER FK` + unique `(name, tenant_id)` |
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

Seed data: `public` tenant + `Cost Administrator` (`cost-management:*:*`)
and `Sources administrator`, both `admin_default`, plus the public
`Default admin access` group that links them (Django's `seed_group()`
parity, so an org admin gets access without explicit membership), and the
granular permission catalog the role wizard lists. Same permission strings
as [operator/internal/resources/rbac_seed/data/*.json](https://github.com/project-koku/koku-service-operator/tree/main/internal/resources/rbac_seed/data/).
The seed job must be idempotent (`get_or_create` semantics) and must upgrade
rows seeded before a flag existed (the code issues an `UPDATE` after
`INSERT ... ON CONFLICT DO NOTHING`).

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
  [migration.go:147-205](https://github.com/project-koku/koku-service-operator/blob/main/internal/resources/migration.go#L147-L205) (idempotent SQL, no `manage.py`).
* Keycloak sync as `CronJob` equivalent: list realm users via client-credentials,
  upsert `principals` with their profile (`email`, names, `enabled`) and the
  realm role named by `SYNC_ORG_ADMIN_SUBGROUP` (default `org-admin`; in
  rbac-go the value is the realm role, not the nested group the Django script
  logs), add to `SYNC_GROUP` when set (empty by default: principals only,
  access comes from the platform/admin default groups), `SYNC_PRUNE_ORPHANS`
  deletes stale rows. A 403 on the role lookup keeps the user sync and leaves
  `is_org_admin` false. The Keycloak client needs `realm-management`
  `view-realm`, `view-users`, `query-groups`, and the `roles` and
  `kc-admin-api` client scopes; without the latter Keycloak answers 401. Disabled users are
  kept: they still exist, keep group bindings, and can be re-enabled; the API
  hides them with the default `status=enabled` filter. This replaces the
  separate `principal-profile` sidecar — Keycloak is read only by the sync.
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
3. Koku [koku/test_rbac.py](https://github.com/project-koku/koku/blob/main/koku/koku/test_rbac.py) logic passes against it (follow `links.next`,
   `424` only on `5xx`/down).
4. Operator e2e [`test_rbac_access.py`](https://github.com/project-koku/koku-service-operator/blob/main/test/pytest/suites/e2e/test_rbac_access.py) personas (alice=project `payment` only,
   bob=cluster-alpha only, carol=full) return correct filtering after ORM-seed
   equivalent loaded via seed/bootstrap jobs.
5. Helm [`test_external_valkey.py`](https://github.com/project-koku/koku-service-operator/blob/main/test/pytest/suites/helm/test_external_valkey.py) env assertions still pass (envs present).
6. Query latency (BenchmarkAccessQuery: 200 principals, 5 permissions with
   resourceDefinitions each, Apple M1 Max): SQLite :memory: ~0.7ms/op,
   Postgres 16 over localhost TCP ~3.7ms/op — far inside the 50ms budget
   for the single join query behind koku's 30s cache. Re-run with
   `go test ./internal/access/ -run NONE -bench AccessQuery`.
7. `go vet` + `sqlfluff`-style review: `grep -riE "DISTINCT ON|FOR UPDATE|RETURNING|ILIKE|::jsonb|GIN|pgcrypto|ArrayField" rbac-go/` is clean.

## 8. Test Reuse (adapted, not copied verbatim)

Django tests require ORM + Postgres + mocks; Go tests are black-box HTTP +
SQLite with the same scenarios/fixtures. Sources (parent repo paths):

* [`tests/management/access/test_view.py`](https://github.com/RedHatInsights/insights-rbac/blob/master/tests/management/access/test_view.py) — fixture graph (`setUp:39-135`:
  tenant → principal → group → policy → role → access/permission +
  `resourceDefinitions`) and helpers (`create_role:147`, `create_policy:161`,
  `create_platform_default_resource:170`, `create_role_and_permission:182`)
  map 1:1 to the §3 tables. Port as table-driven Go tests:
  `test_get_access_success:190` (2 records, `meta.limit`, platform-default
  fallback), `empty_access_with_service_account:228`,
  `with_service_account:243`, `unique_records:340`, `null_value:384`,
  `none_value:461`, `no_app:637`, `multiple_apps:679`,
  `no_partial_match:719`, `no_subset:763`, `no_match:942`, `limit:986`,
  `pagination:1048`, `invalid_ordering/limit:1096-1107`.
  Drop: `@patch is_v2_access_check…`, `TenantCache`/`PRINCIPAL_CACHE`,
  `V2TenantBootstrapService`, `Workspace` bootstrap (`:66-68,83-92`).
* [`tests/identity_request.py`](https://github.com/RedHatInsights/insights-rbac/blob/master/tests/identity_request.py) (`_create_request_context`) — reimplement the
  `x-rh-identity` minting helper in Go (user, org-admin, service-account
  variants) for all fixture setup.
* [koku/koku/test_rbac.py:120](https://github.com/project-koku/koku/blob/main/koku/koku/test_rbac.py#L120) + [koku/rbac.py:67-181](https://github.com/project-koku/koku/blob/main/koku/koku/rbac.py#L67-L181)
  (`_normalize/_process_acls`, `links.next` following, `[]` on non-200) —
  port as [contract_test.go](../internal/contract/contract_test.go) against [openapi.v1-subset.json](openapi.v1-subset.json).
* [operator/test/pytest/suites/e2e/test_rbac_access.py:198-317](https://github.com/project-koku/koku-service-operator/blob/main/test/pytest/suites/e2e/test_rbac_access.py#L198-L317)
  (`RBAC_SETUP_SCRIPT`) — alice/bob/carol personas as Go seed-fixture tests.
* Explicitly not ported: [tests/rbac/test_cache.py](https://github.com/RedHatInsights/insights-rbac/blob/master/tests/rbac/test_cache.py) (no Valkey),
  mocked redis/kafka/kessel/BOP tests, v2/Kessel assertions
  (`test_access_v2_*:1297-1398`), cross-account (`:527-573`, out of scope).

Layout: [internal/access/access_test.go](../internal/access/access_test.go) (SQLite fixtures mirroring
[test_view.py:setUp](https://github.com/RedHatInsights/insights-rbac/blob/master/tests/management/access/test_view.py#L39), one subtest per case above) + [contract_test.go](../internal/contract/contract_test.go)
(koku client logic). Both run under `make test-sqlite` and
`make test-postgres`.
