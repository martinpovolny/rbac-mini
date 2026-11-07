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
