# Live-cluster test runbook (CRC: cost-operator, cost-byoi)

Goal: prove the `backend: go` swap end to end on a real cluster before
touching any defaults. Prerequisites: `crc start`, `oc login -u kubeadmin`,
namespaces `cost-operator` (operator) and `cost-byoi` (workloads) exist.

## 0. Preconditions

```bash
crc status                      # must be Running
oc whoami                       # kubeadmin
oc get namespaces cost-operator cost-byoi
oc -n cost-byoi get costmanagementserviceconfig  # note the CR name
```

## 1. Build + push rbac-go (multi-arch, sync with repo HEAD)

```bash
cd /Users/martinpovolny/Projects/koku/insights-rbac/rbac-go
docker buildx build --platform linux/amd64,linux/arm64 \
  -t quay.io/martin_povolny/rbac-go:v0.3.6 --push .
docker buildx imagetools inspect quay.io/martin_povolny/rbac-go:v0.3.6
# expect: linux/amd64 + linux/arm64 manifests
```

## 2. Build + push the operator from the swap branch

```bash
cd /Users/martinpovolny/worktrees/koku-service-operator/rbac-backend-go
git log --oneline -1            # feat/rbac-backend-go
make docker-build docker-push IMG=quay.io/martin_povolny/koku-service-operator:rbac-go-test
# (use the repo's documented image variables; adjust to its Makefile)
```

If CRC cannot pull from quay (private repo / no route), mirror both images
into the CRC internal registry instead and substitute the image references
below accordingly.

## 3. Deploy the test operator

```bash
oc -n cost-operator set image deployment/<operator-deploy> \
  manager=quay.io/martin_povolny/koku-service-operator:rbac-go-test
oc -n cost-operator rollout status deployment/<operator-deploy>
```

## 4. Flip one CR to the Go backend

```bash
CR=<cr-name>   # from step 0
oc -n cost-byoi patch costmanagementserviceconfig $CR --type=merge \
  -p '{"spec":{"rbac":{"backend":"go","image":{"repository":"quay.io/martin_povolny/rbac-go","tag":"v0.3.6"}}}}'
```

Watch the rollout (no worker expected):

```bash
oc -n cost-byoi get deployments | grep -i rbac     # api only, no rbac-worker
oc -n cost-byoi rollout status deployment/<cr>-rbac-api
oc -n cost-byoi get jobs | grep rbac-migrate       # migrate && seed ran
oc -n cost-byoi logs job/<cr>-rbac-migrate         # expect "Seeding complete"
```

## 5. Verify serving path

```bash
# Probes (operator already gates on /status/)
oc -n cost-byoi exec deploy/<cr>-rbac-api -- \
  wget -qO- http://localhost:8080/api/rbac/v1/status/
# → {"api_version":1}

# Access path through koku (admin bypass + seeded Cost Administrator):
# create alice/bob/carol groups via e2e RBAC_SETUP_SCRIPT equivalent, then
# run operator e2e: test/pytest/suites/e2e/test_rbac_access.py
```

## 6. Verify Keycloak sync CronJob

```bash
oc -n cost-byoi get cronjob | grep keycloak
oc -n cost-byoi create job --from=cronjob/<cr>-rbac-keycloak-sync keycloak-sync-manual
oc -n cost-byoi logs job/keycloak-sync-manual     # principals upserted/pruned
```

## 7. Roll back (proves the swap is non-destructive)

```bash
oc -n cost-byoi patch costmanagementserviceconfig $CR --type=merge \
  -p '{"spec":{"rbac":{"backend":""}}}'
# Rollback must ALSO restore the image (backend:"" alone keeps the Go image
# with a gunicorn command → CreateContainerError):
oc -n cost-byoi patch costmanagementserviceconfig $CR --type=merge \
  -p '{"spec":{"rbac":{"image":{"repository":"quay.io/martin_povolny/insights-rbac","tag":"crc-arm64"}}}}'
oc -n cost-byoi rollout status deployment/<cr>-rbac-api
# Django stack (incl. worker) returns; same costonprem_rbac database.

Verified live 2026-11-06 (both directions). Caveats found while testing:

* Django's `manage.py seeds` (run by the rollback migration Job) **prunes
  Go-created groups/policies** (e.g. the test `CI Test Admin` group vanished;
  tenant + principals survive) and re-seeds its own platform roles. After a
  rollback to Django, **re-run bootstrap** (or the e2e fixtures) to restore
  custom group wiring. Live symptom: Django `/access/` returned
  `aws.account:*` + `openshift.cluster:*` (public Default-access viewers)
  instead of the Go-seeded `cost-management:*:*`.
* That same test exposed a real Go parity gap (fixed in rbac-go):
  Django auto-includes the **public** tenant's platform-default groups in
  every `/access/` call, plus admin_default groups for org admins.
* Test-only rows (org `99999` tenant `org99999`, principal `testlive`) remain
  in `costonprem_rbac`; harmless, no cleanup job exists for them.
```

## 2026-11-07: principal profiles and the RBAC UI (v0.3.3)

Repeated the flip on CRC to validate COST-8167 end to end
(`backend: go`, `quay.io/martin_povolny/rbac-go:v0.3.3`).

Keycloak sync client prerequisites found on this cluster — the
`rbac-keycloak-sync` service account had no roles, so the first CronJob run
failed with `401 Unauthorized`:

* Realm-management client roles: `view-realm`, `view-users`, `query-groups`.
* Client scopes `roles` (emits `resource_access` claims) and `kc-admin-api`
  (adds the admin-API audience). Without these the service-account token has
  no role claims and Keycloak rejects `/admin/realms/<realm>/…` with 401,
  even though the role mappings exist.

Seed fixes needed for the UI:

* The public `Default admin access` group now exists and the two seeded roles
  are `admin_default`, so an org admin with no explicit group gets
  `cost-management:*:*` and `sources:*:*` from `/access/`.
* The granular permission catalog is seeded, so the role wizard
  (`allowed_only=true&exclude_globals=true`) lists permissions and custom
  roles can be created.
* `SYNC_GROUP` defaults to empty. The operator CronJob sets no value, so the
  previous `CI Test Admin` default failed every sync with `unknown group`.

Verified through the UI route `https://cost-management-ui-cost-byoi.apps-crc.testing`
as Keycloak user `admin` (realm role `org-admin`, password `admin`):

| Check | Result |
|---|---|
| Users list | `admin`, Active, Org. Administrator Yes, `admin@test.com`, names Admin/User |
| `?usernames=admin` detail | same row, `meta.count=1` (200) |
| `/access/?application=cost-management` | `cost-management:*:*` |
| `/access/?application=sources` | `sources:*:*` |
| Roles | list, create custom role, read access, delete |
| Groups | create, add member, assign role, member/role lists, delete |
| Permissions | `exclude_globals=true` returns 12 entries; options return `cost-management,sources` |
| Keycloak sync CronJob | manual job succeeds; principal `admin` has email, names, Keycloak id, `is_org_admin=1`, `is_active=1` |

### Data source for the RBAC resource-definition dropdowns

The role wizard's "Define Cost Management resources" dropdowns are fed by
koku, not rbac-go. On a fresh cluster koku has no tenant schema (`org<org_id>`)
and `/api/cost-management/v1/resource-types/` returns an empty page; the
schema is created when the first source is added.

Install the metrics operator and let it create the source:

```bash
# OLM: package costmanagement-metrics-operator, channel stable, redhat-operators
# (OwnNamespace in costmanagement-metrics-operator)
```

`CostManagementMetricsConfig` used on CRC:

```yaml
spec:
  api_url: https://cost-management-gateway-cost-byoi.apps-crc.testing
  authentication:
    type: service-account
    secret_name: cost-mgmt-operator-auth   # client_id/client_secret of cost-management-operator
    token_url: https://keycloak-keycloak.apps-crc.testing/realms/kubernetes/protocol/openid-connect/token
  upload:
    ingress_path: /api/ingress/v1/upload
    upload_cycle: 30
    upload_toggle: true
    validate_cert: false
  prometheus_config:
    service_address: https://thanos-querier.openshift-monitoring.svc:9091
    skip_tls_verification: false
    collect_previous_data: true
  source:
    sources_path: /api/cost-management/v1/
    create_source: true
    check_cycle: 1440
    name: crc-cost-management
```

Prerequisites that were needed:

* `org-admin` realm role on the `cost-management-operator` service account,
  and a `cost-mgmt-operator` principal in rbac-go (koku's sources permission
  checks `/access/`).
* The gateway must accept the Keycloak token and mint `x-rh-identity` for it.
* Object storage: the MinIO community image is no longer pullable, so the
  `minio` Deployment in `cost-byoi-infra` was replaced with SeaweedFS S3 on the
  same service/port (`weed server -s3 -s3.port=9000`). Lab-only.
* Kafka authorization (`simple`) was removed so the ingress/listener can use
  anonymous PLAINTEXT. Lab-only.

Result after the first upload: six OCP CSVs processed into `org1234567`, and
`/resource-types/` returned `openshift.cluster=1`, `openshift.node=1`,
`openshift.project=54`, `settings=1`. AWS/GCP/Azure rows stay empty because
the on-prem sources API only registers `openshift` source types.

`rbac-go v0.3.6` additionally returns `created`/`modified` on roles and groups
(including group roles and user-detail role rows), so the "Last modified"
columns render dates instead of "Invalid date"/"—".

## Notes

- Same `costonprem_rbac` database throughout; Go tables coexist with Django
  tables; nothing is dropped on either flip.
- [scripts/flush-rbac-cache.sh](https://github.com/project-koku/koku-service-operator/blob/main/scripts/flush-rbac-cache.sh) only affects koku-side cache now.
- Known gaps at time of writing: seed parity for price-list roles
  (see [OPERATOR-SWAP.md §0.3](OPERATOR-SWAP.md#0-open-decisions)); `main` flag polish is in-repo only.
