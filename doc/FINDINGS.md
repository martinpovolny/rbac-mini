# Findings (for future reference)

Investigation notes from scoping the rbac-go rewrite. All paths below are
relative to the repos as of 2026-11-06 unless noted.

## 1. Celery is not needed on the v1/on-prem path

`insights-rbac/rbac/{api,management}/tasks.py` hold ~30 tasks; all 60+
`.delay()` call sites are in `internal/views.py` (admin APIs) and the beat
schedule (`rbac/celery.py`). Categories:

- **v2/Kessel/workspace replication + parity** (`replicate_*_workspaces`,
  `recompute_tenant_role_bindings`, `run_kessel_parity_checks_*`, binding
  cleanup, disaster recovery) — v2 only, out of scope.
- **Async wrappers for admin `_private` ops** (`run_migrations/seeds/
  sync_schemas_in_worker`, `migrate_data`, `reset_imported_tenants`) — only
  triggered by the operator migration Job. Replaced by synchronous
  `rbac-go migrate|seed|bootstrap` (Job completion = done, no fire-and-forget).
- **Beat janitors**: `cross_account_cleanup` /
  `expire_orphaned_cross_account_requests_in_worker` (cross-account, SaaS-only;
  see §2), `run_redis_cache_health` (no cache in rbac-go), `principal_cleanup
  {_via_umb,_via_kafka,_via_message_bus}` (SaaS user-churn topics; on-prem
  equivalent is `rbac-go sync --prune-orphans` against Keycloak).
- Cache invalidation on group/policy/role writes is synchronous Django
  signals, not Celery — and moot without a cache.

The v1 request path (`GET /access/`, identity middleware) never touches the
broker. The on-prem beat entry `car-wash-daily` scans an always-empty table.

## 2. Cross-account expiry (SaaS-only, out of scope)

- Model: `api/cross_access/model.py` (`start_date`, `end_date`; validated:
  start within 60 days, duration ≤ 1 year).
- `check_cross_request_expiry()` (`api/cross_access/util.py:34`): flips
  `pending`/`approved` → `expired` past `end_date`, revokes roles incl. a
  Kessel dual-write for v2 tuples.
- Triggers: celery beat (`car-wash-daily`) + internal `api/cars/{expire,clean}`.
- On-prem is single-org per deployment: the operator never creates these
  rows, koku never queries them. rbac-go has no such table. If ever needed,
  it ports as a periodic `rbac-go expire-cross-account` CronJob entry.

## 3. Image entrypoints do not decide compatibility

- The old image **does** have an entrypoint (`Dockerfile:148`,
  `ENTRYPOINT ["./scripts/entrypoint.sh"]` → execs gunicorn; migrate/seed
  live in the operator migration Job).
- But K8s `command:` overrides image `ENTRYPOINT`, and the operator sets
  `Command:`/`Args:` on every workload (`gunicorn …`, `celery … worker`,
  `/bin/bash -c` + `manage.py …`). So compatibility is decided by the
  operator's command/args/env, not by image entrypoints — a pure image-name
  swap cannot work; the operator must branch rendering (see
  `OPERATOR-SWAP.md`).

## 4. Operator contract sources (not the Helm chart)

- `cost-onprem-chart/` is obsolete for this purpose. The contract is the
  operator's Go renderers: `rbacEnv()` in
  `koku-service-operator/internal/resources/rbac.go`, pinned by
  `internal/resources/rbac_test.go` (`TestRBACEnv*`, `TestRBACAPIService`,
  `TestRBACWorkerDeployment` — 7/7 passing).
- Env the Go binary must accept: `DATABASE_*` (honored), `REDIS_*`,
  `DJANGO_SECRET_KEY`, `ACCESS_CACHE_ENABLED` (accepted no-ops).
- Probes `GET /api/rbac/v1/status/`, port `8080/http`, ClusterIP Service,
  Envoy `/api/rbac/` route, `RBAC_SERVICE_{HOST,PORT,PATH,PROTOCOL}` — all
  unchanged by the swap.
- Note: `ROLE_CREATE_ALLOW_LIST=cost-management,sources` exists for the
  custom-role wizard's `GET /permissions/?allowed_only=true` (v1 endpoint
  outside our scope; flagged, not ported).

## 5. Keycloak sync semantics (what `sync` must replicate)

`operator/internal/resources/scripts/sync_keycloak_principals.py`:

- `discover_and_sync`: lists Keycloak groups with prefix `org-` (default;
  `SYNC_ORG_GROUP_PREFIX`), reads `attributes.org_id` / `account_number`,
  lists members per group.
- `sync(org_id, ...)`: `get_or_create` Tenant (`acct<account>`), upserts
  Principals (skips `service-account-*` and disabled users), prunes
  non-cross-account `user` principals absent from Keycloak
  (`SYNC_PRUNE_ORPHANS`, default true), then `bootstrap_tenants --org-id
  --force`.
- Org-admin access is JWT-based (`is_org_admin` + public "Default admin
  access" group), not per-tenant groups — so sync only manages principals.

## 6. Seed data that matters on-prem

`operator/internal/resources/rbac_seed/data/`:

- `cost-management.roles.json`: Cost Administrator (`cost-management:*:*`,
  admin_default), Price List Admin/Viewer, OCP cluster viewer, etc.
- `sources.roles.json`: Sources administrator (`sources:*:*`, admin_default).
- rbac-go seeds the two admin_default roles; price-list roles are an explicit
  open decision (needed only if on-prem UI delegation uses them).
