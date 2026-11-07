# rbac-mini

[![CI](https://github.com/martinpovolny/rbac-mini/actions/workflows/ci.yml/badge.svg)](https://github.com/martinpovolny/rbac-mini/actions/workflows/ci.yml)

A minimal implementation of the subset of **RBAC v1** needed for **koku on-prem**,
with minimal footprint and maintenance cost.

It answers the one question koku asks at request time — *"what is this user
allowed to do?"* — plus the small set of management APIs the on-prem
RBAC UI needs. Everything else from insights-rbac (v2/Kessel workspaces,
Celery/beat, Kafka/Debezium, MCP, audit logs, cross-account requests) is
deliberately left out.

## What it implements

All under `/api/rbac/v1/` ([interactive ReDoc](https://redocly.github.io/redoc/?url=https://raw.githubusercontent.com/martinpovolny/rbac-mini/master/doc/openapi.v1-subset.json), live from [doc/openapi.v1-subset.json](doc/openapi.v1-subset.json) on main):

| Endpoint | Used by |
|---|---|
| `GET /access/` | koku permission checks (the hot path) |
| `GET /principals/` | on-prem UI Users page |
| `GET /permissions/`, `GET /permissions/options/` | on-prem UI role wizard (with `allowed_only`/`exclude_globals`) |
| `GET/POST /roles/`, `GET/PUT/PATCH/DELETE /roles/{uuid}/`, `GET …/access/` | on-prem UI roles |
| `GET/POST /groups/`, `GET/PUT/DELETE /groups/{uuid}/`, `GET …/principals/`, `POST/DELETE …/principals/`, `GET/POST/DELETE …/roles/` | on-prem UI groups |
| `GET /status/`, `GET /ready/`, `GET /metrics` | operator probes, readiness, ServiceMonitor |

Jobs (CLI subcommands, no daemon, no queue):

* `migrate` / `seed` — portable schema + Cost Administrator and
  Sources administrator roles (idempotent; runs as the operator migration Job).
* `bootstrap --org --account --users` — tenant + admin group + principals.
* `sync` — Keycloak users → principals, with org discovery (`--org-group-prefix`)
  and orphan pruning; reads the same env the operator already injects.

Batteries explicitly **not** included: no Valkey/Redis (koku caches for 30s),
no Kessel, no Kafka, no Celery, no BOP lookups (on-prem runs with
`BYPASS_BOP_VERIFICATION` semantics: principals come straight from the DB).

## Quick start

Requirements: Go 1.27+, and PostgreSQL 16 for the Postgres leg.

```bash
go test ./...                                   # SQLite, no Docker needed
make test-sqlite                               # same, explicit
make test-postgres                             # needs scratch PG, see Makefile header
make lint                                      # go vet + golangci-lint
make validate-spec                             # openapi_spec_validator on the subset
go run ./cmd/rbac-go --help
DATABASE_URL=sqlite://./rbac.db go run ./cmd/rbac-go migrate
DATABASE_URL=sqlite://./rbac.db go run ./cmd/rbac-go seed
DATABASE_URL=sqlite://./rbac.db go run ./cmd/rbac-go serve --addr :8080
```

Postgres via `DATABASE_URL=postgres://…` (or the legacy `DATABASE_{HOST,PORT,NAME,USER,PASSWORD}`
vars the operator injects). The same suite runs unchanged on both backends.

## Layout

```
cmd/rbac-go/        binary: serve|migrate|seed|bootstrap|sync
internal/identity/  x-rh-identity parsing (user / org-admin / service-account)
internal/access/    GET /access/ join query (tenant + public platform-default groups)
internal/principal/ GET /principals/
internal/permission/ GET /permissions[/options] (+ ROLE_CREATE_ALLOW_LIST gating)
internal/role/     roles CRUD + validate_role parity
internal/group/    groups CRUD + members/roles association
internal/seed/     idempotent seed + bootstrap
internal/keycloak/ Keycloak client-credentials sync (single + discovery mode)
internal/contract/ koku acl-mapping port (normalize/process/apply, incl. OCP inheritance)
internal/server/   HTTP handlers (v1 envelopes, 400/401/404 shapes)
internal/store/    portable DDL (? placeholders, rebound to $n for Postgres)
internal/testdb/  per-test isolated DBs (SQLite :memory: or fresh PG database)
```

## Documentation

* [Request guide](doc/GUIDE.md) — Keycloak → koku → rbac-go → DB chain, with diagrams
* [What koku needs](doc/KOKU-NEEDS.md) — the exact call contract, permission vocabulary, and koku-side mapping rules
* [SPEC](doc/SPEC.md) — scope, API contract, portable-SQL rules, acceptance criteria
* [ERD](doc/ERD.md) — mermaid ER diagram of the 10-table schema
* [OpenAPI subset](doc/openapi.v1-subset.json) (+ [live ReDoc](https://redocly.github.io/redoc/?url=https://raw.githubusercontent.com/martinpovolny/rbac-mini/master/doc/openapi.v1-subset.json))
* [Findings](doc/FINDINGS.md) — investigation notes (Celery, cross-account, entrypoint, operator contract, Keycloak sync, seeds, UI surface)
* [Operator swap](doc/OPERATOR-SWAP.md) — `spec.rbac.backend` plan and rollout deltas
* [Live-cluster test](doc/LIVE-CLUSTER-TEST.md) — CRC runbook, evidence, rollback caveats
* [Load test](doc/LOADTEST.md) — `GET /access/` critical path, rbac-go vs Django + Valkey, method and results

## Deploying

Via `koku-service-operator` with `spec.rbac.backend: go`
(see [doc/OPERATOR-SWAP.md](doc/OPERATOR-SWAP.md)): one field switches the rendered Deployment,
migration Job, and Keycloak-sync CronJob; worker and seed ConfigMaps are
dropped. Live-cluster verification (CRC, incl. koku end-to-end and rollback)
is recorded in [doc/LIVE-CLUSTER-TEST.md](doc/LIVE-CLUSTER-TEST.md).

## License

GNU AGPL v3 — same as the upstream project this replaces. See [LICENSE](LICENSE).
