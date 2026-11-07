# Findings (for future reference)

Investigation notes from scoping the rbac-go rewrite. All paths below are
relative to the repos as of 2026-11-06 unless noted.

## 1. Celery is not needed on the v1/on-prem path

[insights-rbac/rbac/api/tasks.py](https://github.com/RedHatInsights/insights-rbac/blob/master/rbac/api/tasks.py) and
[insights-rbac/rbac/management/tasks.py](https://github.com/RedHatInsights/insights-rbac/blob/master/rbac/management/tasks.py) hold ~30 tasks; all 60+
`.delay()` call sites are in [internal/views.py](https://github.com/RedHatInsights/insights-rbac/blob/master/rbac/internal/views.py) (admin APIs) and the beat
schedule ([rbac/celery.py](https://github.com/RedHatInsights/insights-rbac/blob/master/rbac/rbac/celery.py)). Categories:

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

- Model: [api/cross_access/model.py](https://github.com/RedHatInsights/insights-rbac/blob/master/rbac/api/cross_access/model.py) (`start_date`, `end_date`; validated:
  start within 60 days, duration ≤ 1 year).
- `check_cross_request_expiry()` ([api/cross_access/util.py:34](https://github.com/RedHatInsights/insights-rbac/blob/master/rbac/api/cross_access/util.py#L34)): flips
  `pending`/`approved` → `expired` past `end_date`, revokes roles incl. a
  Kessel dual-write for v2 tuples.
- Triggers: celery beat (`car-wash-daily`) + internal `api/cars/{expire,clean}`.
- On-prem is single-org per deployment: the operator never creates these
  rows, koku never queries them. rbac-go has no such table. If ever needed,
  it ports as a periodic `rbac-go expire-cross-account` CronJob entry.

## 3. Image entrypoints do not decide compatibility

- The old image **does** have an entrypoint ([Dockerfile:148](https://github.com/RedHatInsights/insights-rbac/blob/master/Dockerfile#L148),
  `ENTRYPOINT ["./scripts/entrypoint.sh"]` → execs gunicorn; migrate/seed
  live in the operator migration Job).
- But K8s `command:` overrides image `ENTRYPOINT`, and the operator sets
  `Command:`/`Args:` on every workload (`gunicorn …`, `celery … worker`,
  `/bin/bash -c` + `manage.py …`). So compatibility is decided by the
  operator's command/args/env, not by image entrypoints — a pure image-name
  swap cannot work; the operator must branch rendering (see
  [OPERATOR-SWAP.md](OPERATOR-SWAP.md)).

## 4. Operator contract sources (not the Helm chart)

- `cost-onprem-chart/` is obsolete for this purpose. The contract is the
  operator's Go renderers: `rbacEnv()` in
  [koku-service-operator/internal/resources/rbac.go](https://github.com/project-koku/koku-service-operator/blob/main/internal/resources/rbac.go), pinned by
  [internal/resources/rbac_test.go](https://github.com/project-koku/koku-service-operator/blob/main/internal/resources/rbac_test.go) (`TestRBACEnv*`, `TestRBACAPIService`,
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

[operator/internal/resources/scripts/sync_keycloak_principals.py](https://github.com/project-koku/koku-service-operator/blob/main/internal/resources/scripts/sync_keycloak_principals.py):

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

[operator/internal/resources/rbac_seed/data/](https://github.com/project-koku/koku-service-operator/tree/main/internal/resources/rbac_seed/data):

- [cost-management.roles.json](https://github.com/project-koku/koku-service-operator/blob/main/internal/resources/rbac_seed/data/cost-management.roles.json): Cost Administrator (`cost-management:*:*`,
  admin_default), Price List Admin/Viewer, OCP cluster viewer, etc.
- [sources.roles.json](https://github.com/project-koku/koku-service-operator/blob/main/internal/resources/rbac_seed/data/sources.roles.json): Sources administrator (`sources:*:*`, admin_default).
- rbac-go seeds the two admin_default roles; price-list roles are an explicit
  open decision (needed only if on-prem UI delegation uses them).

## 7. On-prem UI (koku-ui/apps/rbac-ui-onprem) API surface

The on-prem RBAC UI is a federated wrapper (`@koku-ui/rbac-ui-onprem`,
"no upstream repo edits — vendored upstream + on-prem shims") mounting the
full upstream `Iam` component; with no Kessel on-prem it serves the **v1
User Access** pages (`/iam/user-access/...`). Evidence:
`koku-ui/COST-8190-rbac-ui.md` (an untracked local analysis note for the on-prem CRC failure), shims in
`apps/rbac-ui-onprem/src/shims/insights-rbac/` (role schema, CostResources,
ReviewStep, browser 401→/logout, useAppLink).

Confirmed on-prem UI calls (traffic traces + source refs):

- `GET /api/rbac/v1/access/` — permission gating (`useAccessPermissions`).
- `GET /api/rbac/v1/principals/?limit=&offset=&usernames=…&status=…&admin_only=…`
  — Users page (401-loop trace) and the user-detail page, which looks a single
  principal up by `usernames=`.
- `GET /api/rbac/v1/groups/…` — Groups pages (list, members, group roles;
  34 refs in v1+shared). The list sends `name` + `name_match=exact` while the
  name field is typed, probes `platform_default=true` / `admin_default=true`
  for the protected groups, and sorts with `order_by=name|-modified`.
- `GET|POST|PUT|PATCH|DELETE /api/rbac/v1/roles/…` — Roles pages + creation
  wizard (21 refs). Availability checks send `name`/`display_name` with
  `name_match=exact`; the list sorts by `display_name` and the table renders
  `modified` ("Last modified"). User detail also renders `role.modified`.
- `GET /api/rbac/v1/permissions/?allowed_only=true&exclude_globals=true…` +
  `GET /api/rbac/v1/permissions/options/?field=application&allowed_only=true`
  — wizard Step 2 (needs `ROLE_CREATE_ALLOW_LIST=cost-management,sources`,
  fixed in the operator; see `TestRBACEnvRoleCreateAllowList`).
- Role create payloads carry `resourceDefinitions: []` for global scope
  (backend-valid; the wizard shim made them optional).

Implication: if the on-prem UI keeps working against rbac-go, the Go scope
grows beyond `/access/` to v1 principals/groups/roles/permissions
(list + roles/groups CRUD). Decision recorded separately.
