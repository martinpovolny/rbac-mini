# rbac-go request guide: from Keycloak login to koku access decision

How an authenticated request becomes an RBAC access list, end to end.
For the API contract see `SPEC.md`, for the schema `ERD.md`.

## 1. The chain

```mermaid
sequenceDiagram
    participant U as User / ServiceAccount
    participant KC as Keycloak
    participant GW as Gateway (Envoy)
    participant Koku as koku (middleware)
    participant RBAC as rbac-go (:8080)
    participant DB as Postgres

    U->>KC: authenticate (password grant / client credentials)
    KC-->>U: JWT (org_id, username / client_id, is_org_admin)
    U->>GW: request + JWT
    GW->>GW: build x-rh-identity (base64 JSON)
    GW->>Koku: request + x-rh-identity
    Koku->>Koku: parse header → User{org_id, username, admin}
    Koku->>RBAC: GET /api/rbac/v1/access/?application=cost-management
    RBAC->>DB: resolve tenant → principal → groups → policies → roles → permissions
    DB-->>RBAC: flat ACL list [{permission, resourceDefinitions}]
    RBAC-->>Koku: 200 {meta, links, data}
    Koku->>Koku: fold ACLs → per-resource access dict
    Koku->>Koku: DRF permission check (403 or serve)
```

Step by step:

1. **Keycloak → header.** The user (or service account) authenticates against
   Keycloak. The gateway builds the `x-rh-identity` header: base64 JSON with
   `org_id`, `identity.type` (`User`/`ServiceAccount`), `username`,
   `is_org_admin`, and `client_id` for service accounts
   (`internal/identity` parses and lowercases this).
2. **koku parses it** (`koku/koku/middleware.py`) into a `User` object —
   org, username, admin flag. It does *not* know RBAC yet; it asks us:
   `GET /api/rbac/v1/access/?application=cost-management` with that header.
   Non-200 answers become `[]` (deny); connection errors/5xx become HTTP 424.
3. **rbac-go resolves the principal**: the `principals` row matching
   `(username, tenant_id)` — the tenant comes from `org_id` (`tenants` table).
   Unknown user → empty list (deny).
4. **Group membership**: `group_principals` join → the tenant's groups,
   **plus** auto-included platform-default groups (tenant's *and* the public
   tenant's `Default access`) and, for org admins, admin-default groups.
   The caller is treated as a member of those groups without a row.
5. **Group → roles**: `policies` (each belongs to exactly one group) →
   `policy_roles` → `roles`.
6. **Roles → permissions**: `access` rows → `permissions`
   (`application:resource:verb`), filtered by the requested `application`,
   plus `resource_definitions` (`attributeFilter` JSON: `equal`/`in` with
   values like project names or cluster IDs).
7. **Back in koku**: it folds that flat ACL list into the per-resource access
   dict (`aws.account: {read: [...]}`, `cost_model: {read, write}`, OCP
   cluster→node→project inheritance, `*` compaction) that its DRF permission
   classes check (`internal/contract` is our Go port of that mapping).

## 2. The data path (one query)

`GET /access/` resolves in a single join (`internal/access`):

```mermaid
flowchart LR
    T["tenants<br/>(org_id)"] --> P["principals<br/>(username)"]
    P --> GP["group_principals"]
    GP --> G["groups<br/>(+ platform/admin defaults *)"]
    G --> POL["policies<br/>(one per group)"]
    POL --> PR["policy_roles"]
    PR --> R["roles"]
    R --> A["access"]
    A --> PERM["permissions<br/>(application filter)"]
    A --> RD["resource_definitions<br/>(attributeFilter JSON)"]
    PERM --> OUT(["[{permission,<br/>resourceDefinitions}]"])
    RD --> OUT
```

`*` The platform/admin-default groups are injected by the query itself
(`g.platform_default = 1 … OR g.admin_default = 1` for org admins) —
no membership rows needed. See `FINDINGS.md` §“live-cluster test” for why:
without this, on-prem loses the public `Default access` grants.

## 3. Groups vs group_principals — why two entities?

They answer different questions:

| | `groups` | `group_principals` |
|---|---|---|
| Represents | a group as a thing: name, description, `platform_default`, `admin_default`, `system` flags | one membership edge: *principal X belongs to group Y* |
| Shape | one row per group | one row per (group, principal) pair; composite PK, no payload |
| Deleted when | group deleted (cascades to edges, policies keep working until then) | either side deleted |

A principal belongs to many groups and a group has many principals — a
many-to-many relationship, which in portable SQL (runs unchanged on SQLite
and Postgres, SPEC §3) must be a join table; there are no array columns.
Keeping flags on `groups` (not on the edge) is what makes
“platform_default applies to everyone” expressible at all: the access query
can test `g.platform_default = 1` without joining any membership row.
Django hides this table behind `ManyToManyField`; rbac-go declares it
because every table here is explicit, portable DDL.

## 4. Where the pieces live

* Identity parsing: `internal/identity` (+ `SPEC.md` §4).
* Join + filters + pagination: `internal/access`.
* koku-side folding port (normalize/process/apply, OCP inheritance):
  `internal/contract` — proves our output is consumable by koku's mapping.
* Tenant/group/principal provisioning: `seed.Bootstrap`, `keycloak.Sync`.
* Full endpoint list: `SPEC.md` §1–§2, OpenAPI subset:
  `doc/openapi.v1-subset.json`.
