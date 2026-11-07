# What koku needs from RBAC (on-prem)

Verified against [koku/koku/rbac.py](https://github.com/project-koku/koku/blob/main/koku/koku/rbac.py), [koku/koku/middleware.py](https://github.com/project-koku/koku/blob/main/koku/koku/middleware.py), and
[koku/api/common/permissions/](https://github.com/project-koku/koku/tree/main/koku/api/common/permissions) (koku checkout, Oct 2026). This is the entire
authorization surface the Go rewrite must satisfy — nothing more calls RBAC.

## 1. The only calls koku makes

One client, one endpoint, two applications per request:

* `GET /api/rbac/v1/access/?application=cost-management&limit=100`
  (plus `?application=sources&limit=100` when `settings.ONPREM`),
  paginated via `links.next` (`{base_url}{next_link}`),
  header `x-rh-identity: <encoded>`.
* Entry point: `RbacService.get_access_for_user()` from
  `IdentityHeaderMiddleware` ([koku/koku/middleware.py](https://github.com/project-koku/koku/blob/main/koku/koku/middleware.py)), result cached
  30s per `(user.uuid, org_id)` in the `rbac` Redis cache.
* Non-200 (except ≥500) → treated as `[]` (deny). Connection error or
  ≥500 → HTTP 424, never silent deny.

Nothing else in koku, masu, or sources talks to RBAC over HTTP
([masu/api/status.py](https://github.com/project-koku/koku/blob/main/koku/masu/api/status.py) and [api/status/models.py](https://github.com/project-koku/koku/blob/main/koku/api/status/models.py) only read the cache-TTL
config value). No roles/groups/principals/permissions calls exist.

## 2. Permission granularity koku needs

Exact-match `application:resource_type:operation` strings
(`_extract_permission_data` requires exactly 3 colon-separated parts),
bucketed by `RESOURCE_TYPES`:

| resource type | ops | gates |
|---|---|---|
| `aws.account`, `aws.organizational_unit` | read | reports |
| `gcp.account`, `gcp.project` | read | reports |
| `azure.subscription_guid` | read | reports |
| `openshift.cluster` / `node` / `project` | read | reports, with cluster→node→project inheritance |
| `cost_model` | read, **write** | cost-models API (write = `*` in list, or URL uuid match) |
| `settings` | read, write | settings/ingress, integrations tab |
| `sources` (on-prem only) | read, write | sources/integrations tab |

Mapping rules koku applies client-side (mirrored in
[internal/contract](../internal/contract/contract.go)):

* Unknown applications fall into the `*` bucket and expand across **every**
  resource type — hence `_normalize_sources_acls` rewrites `sources:*:*` to
  `sources:sources:*` before anything else.
* `cost-management:rate:*` is legacy spelling for `cost_model`.
* `resourceDefinitions[].attributeFilter`: `equal` → single value,
  `in` → list (or comma-split string), empty/absent → `["*"]` (unrestricted).
  Values are matched against real IDs (account numbers, cluster IDs,
  project names) — the JSON must pass through byte-faithful.
* Write implies read; a `*` anywhere compacts the bucket to `["*"]`.
* `ENHANCED_ORG_ADMIN` + `request.user.admin` bypasses all checks.
* Malformed permission strings are skipped with a log line, never fatal.

## 3. How the wiki's Kessel-migration catalog maps to this

The personal wiki tracks the **SaaS** migration (Kessel/SpiceDB, ReBAC);
this repo serves **on-prem** (RBAC v1). Same vocabulary, different targets —
complementary, not conflicting.

| Wiki artifact | Relationship to this picture |
|---|---|
| [v1-permission-checks-koku.md](https://github.com/martinpovolny/personal-wki/blob/main/wiki/concepts/v1-permission-checks-koku.md) — permission vocabulary (10 resource defs × verbs from `rbac-config`) | Matches `RESOURCE_TYPES` exactly; the wiki already notes the one drift we also handle: on-prem-only `sources` (absent from prod `rbac-config`) |
| Same page — enforcement mapping ([koku/api/common/permissions/](https://github.com/project-koku/koku/tree/main/koku/api/common/permissions), one DRF class per resource type, `ENHANCED_ORG_ADMIN` bypass) | Matches the verified call sites; our [internal/contract](../internal/contract/contract.go) is the executable version of the same mapping |
| Endpoint inventory (109 rows, Kessel workbook + `koku-kessel-permission-matrix`) — endpoint → PDP-check mapping | Covers *which check each endpoint performs*; our port covers *check semantics*. The wiki flags code reconciliation as missing — for the on-prem subset, this repo now does it |
| Wiki gap: “no endpoint → resource/verb → attribute-filter triple” | Partially answered here: §2 + [doc/openapi.v1-subset.json](openapi.v1-subset.json) + contract tests tie endpoints to resources, verbs, and filter shapes for the on-prem surface |
| Wiki next step: record bypass + sources-normalization as V2 parity requirements | We implement the v1 behavior itself; any V2 model must reproduce: bypass, `sources:sources` rewrite, `rate`→`cost_model`, `*` compaction, OCP inheritance |
| KSL-75 (phase 0 = workspace-level checks, resource definitions disappear) | Explicitly SaaS-scoped; says nothing about on-prem, dual-read, or rollback. On-prem keeps v1 RD semantics — which is exactly what rbac-go preserves |
| [osac-cost-authz-mapping.md](https://github.com/martinpovolny/personal-wki/blob/main/wiki/overviews/osac-cost-authz-mapping.md) (OSAC Keycloak Organizations API vs realm groups; 1 OSAC group → 3 RBAC objects; Layer A/B/C gaps) | Our `keycloak.Sync` uses the realm Groups API, matching current on-prem Keycloak — *not* OSAC Organizations. If on-prem ever adopts the OSAC Keycloak layout, Layer A (Organizations-API discovery + materializing viewer/manager groups into Role+RD+Group+Policy) becomes required work |
| “~nine resource types” (Sep 29 sync) | Our 11 buckets (10 + on-prem `sources`) |

Net: the wiki catalogs *where koku is going* (SaaS/Kessel); this document
records *what koku needs today* (on-prem/RBAC v1). The Go rewrite is the
stable v1 floor the migration can later be reconciled against — it changes
none of the strings, shapes, or semantics the Kessel inventory describes.

## 4. On-prem UI: the principal profile contract (COST-8167)

koku never reads principals (§1). The other consumer of the shipped service
is the RBAC MFE in [koku-ui-onprem](https://github.com/project-koku/koku-ui/tree/main/apps/rbac-ui-onprem),
and its Users/Groups pages make the principal profile part of the contract.
rbac-go serves it from the database written by the Keycloak sync: there is no
request-path Keycloak call and no `principal-profile` sidecar. The behavior
below replaces the upstream `BYPASS_BOP_VERIFICATION` stubs
([management/principal/proxy.py](https://github.com/RedHatInsights/insights-rbac/blob/master/rbac/management/principal/proxy.py))
and is specified in [SPEC.md](SPEC.md).

Calls:

* `GET /api/rbac/v1/principals/` — Users list.
* `GET /api/rbac/v1/principals/?usernames=<username>` — user detail. A listed
  user must return 200; an unknown username returns a stub entry, so the UI,
  not the API, decides when to show "User not found".
* `GET /api/rbac/v1/groups/{uuid}/principals/` — group members, same item
  shape and filters.

Item shape (principal list, detail and group members):

| field | source |
|---|---|
| `username` | `principals.username` (lowercase) |
| `email` | synced Keycloak email; `<username>@local` when none |
| `first_name`, `last_name` | synced Keycloak names (empty when absent) |
| `is_active` | Keycloak `enabled`, default true |
| `is_org_admin` | member of the `org-admin` **realm role** (the JIRA's source; not a group) |
| `user_id` | Keycloak user id when known, else the username |
| `type` | `user` (service accounts are out of sync scope) |

Query parameters and defaults (mirroring the Django v1 view):

| parameter | values | default | notes |
|---|---|---|---|
| `usernames` | comma-separated | none | exact match is case-insensitive; `match_criteria=partial` uses only the first value as a prefix |
| `email` | string | none | exact or prefix with `match_criteria=partial`; matched against the effective email |
| `match_criteria` | `exact`, `partial` | `exact` | |
| `status` | `enabled`, `disabled`, `all` | `enabled` | filters on `is_active`; the UI's default Active chip relies on this |
| `admin_only` | `true`, `false` | `false` | only applied when no `usernames`/`email` filter is present (Django parity) |
| `username_only` | `true`, `false` | `false` | response items contain only `username` |
| `sort_order` | `asc`, `desc` | `asc` | by username |
| `type` | `user`, `service-account`, `all` | `user` | principal list only; group members use `principal_type` |
| `order_by` | `username`, `-username` | `username` | group members only |

Sync rules that make the contract work:

* The sync is the only component that reads Keycloak. It persists email,
  names, `enabled`, Keycloak id and the `org-admin` realm-role membership on
  every run, so profile changes appear after the CronJob interval, not per
  request.
* Disabled users are **kept** and count as present for `SYNC_PRUNE_ORPHANS`,
  so a temporary disable does not delete the principal or its group
  bindings. The default `status=enabled` hides them instead.
* Pruning still deletes principals absent from the org group, which is the
  only case where "user left the organization" is known.
* Because synced users now carry `is_active=true`, the Users list, the Active
  filter chip and the username filter (test plan E-07) agree; previously the
  missing flag made every row Inactive and `Active AND username=x` matched
  nothing.

Tests: [keycloak_test.go](../internal/keycloak/keycloak_test.go) covers
profile persistence, disabled retention, org-admin role add/remove, discovery
mode and role-lookup degradation; [principal_test.go](../internal/principal/principal_test.go)
covers the filters and the unknown-user stub; [server_test.go](../internal/server/server_test.go)
covers the HTTP shape for both endpoints and [migrate_test.go](../internal/store/migrate_test.go)
covers upgrading an existing database.
