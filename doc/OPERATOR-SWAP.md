# Operator swap: insights-rbac → rbac-go (on-prem)

How to point `koku-service-operator` at this binary. Code changes below are
all in the operator repo (`internal/resources/`, `config/`); rbac-go needs no
changes except where marked TODO.

## 0. Open decisions (blocking the swap)

1. **Image registry + tag**: `quay.io/project-koku/rbac-go:<tag>` (new repo)
   vs reusing the `insights-rbac` repo name. The operator's `RBACConfig.Image`
   just needs the new default; version with the operator release.
2. **Multi-org Keycloak sync**: the current `sync_keycloak_principals.py`
   iterates Keycloak groups (`org-` prefix) across orgs. `rbac-go sync` handles
   one `--org`/`--group` per invocation. Either extend `sync` to loop groups
   (TODO in rbac-go) or render one CronJob item per org (operator change).
3. **Seed parity**: rbac-go seeds `Cost Administrator` + `Sources
   administrator`. The operator seed JSON also defines `Cost Price List
   Administrator/Viewer` etc. Needed only if on-prem UI delegation uses them;
   otherwise drop from `rbac_seed/data/`.

## 1. API Deployment (`rbac.go: RBACAPIDeployment`)

| Current (Django) | rbac-go |
|---|---|
| `Command: gunicorn`, `Args: rbac.wsgi --bind=0.0.0.0:8080 ...` | `Command: ["/rbac-go", "serve"]`, `Args: ["--addr", ":8080"]` |
| `WorkingDir: /opt/rbac/rbac` | remove (static binary, `WORKDIR /data` in image) |
| `rbacEnv()`: `DATABASE_*`, `REDIS_*`, `DJANGO_SECRET_KEY`, `ACCESS_CACHE_ENABLED`, … | keep passing all of them: `DATABASE_*` are now honored (SPEC §5, `resolveDatabaseURL`), `REDIS_*/DJANGO_SECRET_KEY/ACCESS_CACHE_ENABLED` are accepted no-ops keeping `test_external_valkey.py` green |
| Liveness/readiness `GET /api/rbac/v1/status/` | unchanged (optionally switch readiness to `/api/rbac/v1/ready/`, which actually pings the DB) |
| Port `8080/http`, Service, Envoy `/api/rbac/` route, `RBAC_SERVICE_{HOST,PORT,PATH}` | unchanged |

## 2. Deletions

* `RBACWorkerDeployment` (celery) and the beat scheduler: no async work left.
* `KeycloakSyncConfigMap` (embedded `sync_keycloak_principals.py`): replaced by flags.
* `rbac_seed_configmap.go` mounts (`…/role/permissions/*.json`): seed data is compiled in; delete once seed parity (§0.3) is settled.
* `WaitForValkeyInitContainer` on rbac pods: rbac-go never dials Valkey. (Valkey itself stays — koku core still uses it.)

## 3. Migration/seed Job (`migration.go: RBACMigrationJob`)

Replace `python manage.py migrate --noinput && python manage.py seeds --skip-notifications`
(plus `PERMISSION/ROLE/GROUP_SEEDING_ENABLED=True`, tag `cmseed2`) with:

```
rbac-go migrate && rbac-go seed
```

Keep the Job + `rbacSeedRevision` bump mechanism (rename tag, e.g. `gomigrate1`).
Both commands are idempotent; safe to re-run on every deploy.

## 4. Keycloak sync CronJob (`rbac_keycloak_sync.go`)

Replace `python manage.py shell < sync_keycloak_principals.py` with:

```
rbac-go sync --keycloak-url $KEYCLOAK_URL --realm $KEYCLOAK_REALM \
  --client-id $KEYCLOAK_CLIENT_ID --client-secret $KEYCLOAK_CLIENT_SECRET \
  --org $ORG_ID --group $GROUP [--prune-orphans=$SYNC_PRUNE_ORPHANS]
```

Env names map 1:1 except org/group resolution (see §0.2). Schedule, image,
`ServiceAccount`, `TLS_VERIFY` handling unchanged.

## 5. Database

Same `costonprem_rbac` database, new tables (`tenants`, `principals`,
`groups`, … — SPEC §3). They coexist with the Django tables; drop the old
ones in a later release. `waitForRBACDB` initContainer unchanged.

## 6. Verification after the swap

1. `make test-postgres` (rbac-go) green against the cluster DB image.
2. Operator e2e `test_rbac_access.py` personas (alice/bob/carol) — covered by
   `TestPersonas` + `TestApplyAccessOCPInheritance` here.
3. `scripts/flush-rbac-cache.sh` becomes koku-side cache flush only
   (rbac-go has no cache to purge).
4. Gateway/IQE `run-iqe-tests.sh` bootstrap (`manage.py shell` creating
   Tenant/Group/Policy/Principal) becomes
   `rbac-go migrate && rbac-go seed && rbac-go bootstrap --org … --users …`.
5. ServiceMonitor `/metrics/` target unchanged; `rbac_api_requests_total`
   replaces the Django metric names on dashboards.
