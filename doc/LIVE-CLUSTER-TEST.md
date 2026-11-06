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
  -t quay.io/martin_povolny/rbac-go:v0.2.0 --push .
docker buildx imagetools inspect quay.io/martin_povolny/rbac-go:v0.2.0
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
  -p '{"spec":{"rbac":{"backend":"go","image":{"repository":"quay.io/martin_povolny/rbac-go","tag":"v0.2.0"}}}}'
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
oc -n cost-byoi rollout status deployment/<cr>-rbac-api
# Django stack (incl. worker) returns; same costonprem_rbac database.
```

## Notes

- Same `costonprem_rbac` database throughout; Go tables coexist with Django
  tables; nothing is dropped on either flip.
- `scripts/flush-rbac-cache.sh` only affects koku-side cache now.
- Known gaps at time of writing: seed parity for price-list roles
  (see OPERATOR-SWAP.md §0.3); `main` flag polish is in-repo only.
