# Operator swap: insights-rbac → rbac-go (on-prem)

How to point `koku-service-operator` at this binary. Code changes below are
all in the operator repo ([internal/resources/](https://github.com/project-koku/koku-service-operator/tree/main/internal/resources), [config/](https://github.com/project-koku/koku-service-operator/tree/main/config)); rbac-go needs no
changes except where marked TODO.

## 0. Open decisions

1. **Image registry + tag**: RESOLVED — `quay.io/martin_povolny/rbac-go`.
   Use the current verified tag (`v0.3.6` at the time of writing; see
   [LIVE-CLUSTER-TEST.md](LIVE-CLUSTER-TEST.md)); operator's
   `RBACConfig.Image` default becomes repository
   `quay.io/martin_povolny/rbac-go`.
2. **Multi-org Keycloak sync**: RESOLVED in rbac-go — `sync` without
   `--org` discovers orgs from Keycloak groups (`--org-group-prefix`,
   default `org-`, env `SYNC_ORG_GROUP_PREFIX`), reads per-group
   `org_id`/`account_number` attributes, and syncs members per org exactly
   like `discover_and_sync`/`sync` (tenant ensure, skip `service-account-*`,
   persist email/names/`enabled`/`org-admin`, keep disabled users, prune only
   users absent from Keycloak, no group touch). `SYNC_GROUP` is optional and
   empty by default; the org-admin source is the realm role named by
   `SYNC_ORG_ADMIN_SUBGROUP` (default `org-admin`). Single-org `--org` mode is
   kept for tests/ops.
3. **Seed parity**: rbac-go seeds `Cost Administrator` + `Sources
   administrator` (both `admin_default`), the public `Default admin access`
   group, and the granular cost-management/sources permission catalog the role
   wizard lists. The operator seed JSON also defines `Cost Price List
   Administrator/Viewer` etc.; those roles are still absent and are needed
   only if on-prem UI delegation uses them
   ([rbac_seed/data/](https://github.com/project-koku/koku-service-operator/tree/main/internal/resources/rbac_seed/data)).

## 1. API Deployment ([rbac.go: RBACAPIDeployment](https://github.com/project-koku/koku-service-operator/blob/main/internal/resources/rbac.go))

| Current (Django) | rbac-go |
|---|---|
| `Command: gunicorn`, `Args: rbac.wsgi --bind=0.0.0.0:8080 ...` | `Command: ["/rbac-go", "serve"]`, `Args: ["--addr", ":8080"]` |
| `WorkingDir: /opt/rbac/rbac` | remove (static binary, `WORKDIR /data` in image) |
| `rbacEnv()`: `DATABASE_*`, `REDIS_*`, `DJANGO_SECRET_KEY`, `ACCESS_CACHE_ENABLED`, … | keep passing all of them: `DATABASE_*` are now honored ([SPEC.md §5](SPEC.md#5-operator-integration), `resolveDatabaseURL`), `REDIS_*/DJANGO_SECRET_KEY/ACCESS_CACHE_ENABLED` are accepted no-ops keeping [test_external_valkey.py](https://github.com/project-koku/koku-service-operator/blob/main/test/pytest/suites/helm/test_external_valkey.py) green |
| Liveness/readiness `GET /api/rbac/v1/status/` | unchanged (optionally switch readiness to `/api/rbac/v1/ready/`, which actually pings the DB) |
| Port `8080/http`, Service, Envoy `/api/rbac/` route, `RBAC_SERVICE_{HOST,PORT,PATH}` | unchanged |

## 2. Deletions

* `RBACWorkerDeployment` (celery) and the beat scheduler: no async work left.
* `KeycloakSyncConfigMap` (embedded [sync_keycloak_principals.py](https://github.com/project-koku/koku-service-operator/blob/main/internal/resources/scripts/sync_keycloak_principals.py)): replaced by flags.
* [rbac_seed_configmap.go](https://github.com/project-koku/koku-service-operator/blob/main/internal/resources/rbac_seed_configmap.go) mounts (`…/role/permissions/*.json`): the permission catalog and admin-default group are compiled in; delete once price-list role parity (§0.3) is settled.
* `WaitForValkeyInitContainer` on rbac pods: rbac-go never dials Valkey. (Valkey itself stays — koku core still uses it.)

## 3. Migration/seed Job ([migration.go: RBACMigrationJob](https://github.com/project-koku/koku-service-operator/blob/main/internal/resources/migration.go))

Replace `python manage.py migrate --noinput && python manage.py seeds --skip-notifications`
(plus `PERMISSION/ROLE/GROUP_SEEDING_ENABLED=True`, tag `cmseed2`) with:

```
rbac-go migrate && rbac-go seed
```

Keep the Job + `rbacSeedRevision` bump mechanism (rename tag, e.g. `gomigrate1`).
Both commands are idempotent; safe to re-run on every deploy.

## 4. Keycloak sync CronJob ([rbac_keycloak_sync.go](https://github.com/project-koku/koku-service-operator/blob/main/internal/resources/rbac_keycloak_sync.go))

Replace `python manage.py shell < sync_keycloak_principals.py` with:

```
rbac-go sync --keycloak-url $KEYCLOAK_URL --realm $KEYCLOAK_REALM \
  --client-id $KEYCLOAK_CLIENT_ID --client-secret $KEYCLOAK_CLIENT_SECRET \
  --org $ORG_ID [--group $SYNC_GROUP] \
  [--org-admin-role $SYNC_ORG_ADMIN_SUBGROUP] [--prune-orphans=$SYNC_PRUNE_ORPHANS]
```

Env names map 1:1 except org/group resolution (see §0.2). Schedule, image,
`ServiceAccount`, `TLS_VERIFY` handling unchanged.

## 5. Database

Same `costonprem_rbac` database, new tables (`tenants`, `principals`,
`groups`, … — [SPEC.md §3](SPEC.md#3-data-model-10-tables--sqlite--postgres-portable)). They coexist with the Django tables; drop the old
ones in a later release. `waitForRBACDB` initContainer unchanged.

## 6. Verification after the swap

1. `make test-postgres` (rbac-go) green against the cluster DB image.
2. Operator e2e [test_rbac_access.py](https://github.com/project-koku/koku-service-operator/blob/main/test/pytest/suites/e2e/test_rbac_access.py) personas (alice/bob/carol) — covered by
   `TestPersonas` + `TestApplyAccessOCPInheritance` here.
3. [scripts/flush-rbac-cache.sh](https://github.com/project-koku/koku-service-operator/blob/main/scripts/flush-rbac-cache.sh) becomes koku-side cache flush only
   (rbac-go has no cache to purge).
4. Gateway/IQE [run-iqe-tests.sh](https://github.com/project-koku/koku-service-operator/blob/main/scripts/run-iqe-tests.sh) bootstrap (`manage.py shell` creating
   Tenant/Group/Policy/Principal) becomes
   `rbac-go migrate && rbac-go seed && rbac-go bootstrap --org … --users …`.
5. ServiceMonitor `/metrics/` target unchanged; `rbac_api_requests_total`
   replaces the Django metric names on dashboards.

## 7. Operator-side env contract (verified 2026-11-06)

Source of truth is the operator itself: `rbacEnv()` in
[koku-service-operator/internal/resources/rbac.go](https://github.com/project-koku/koku-service-operator/blob/main/internal/resources/rbac.go), pinned by its Go tests
(all passing — `go test ./internal/resources/ -run
'TestRBACEnv|TestRBACAPIService|TestRBACAPIDeployment|TestRBACWorkerDeployment'`
→ 7/7 PASS):

* `API_PATH_PREFIX=/api/rbac` (`TestRBACEnvAPIPathPrefix`).
* `ROLE_CREATE_ALLOW_LIST=cost-management,sources` (`TestRBACEnvRoleCreateAllowList`).
* `REDIS_HOST/PORT` from `spec.cache` with `6379` default (`TestRBACEnvUsesConfiguredCachePort`, `TestRBACEnvDefaultsCachePort`).
* Probes `GET /api/rbac/v1/status/`, Service `ClusterIP :8080`, `ServiceAccount`
  (`TestRBACAPIService`, `TestRBACAPIDeploymentServiceAccount`, `TestRBACWorkerDeployment`).

(Note: an earlier draft verified `cost-onprem-chart/cost-onprem` via
`helm template` — that chart is obsolete; the operator's Go renderers above
are the relevant contract. The `REDIS_*`/`DJANGO_SECRET_KEY`/
`ACCESS_CACHE_ENABLED` vars rbac-go accepts-but-ignores come from this same
`rbacEnv()`; they stay so these tests keep passing.)

Command/args replaced: `gunicorn rbac.wsgi …` → `rbac-go serve --addr :8080`;
`celery … worker` (rbac-worker) deleted. Probes, port, Service, Envoy route
all unchanged.
