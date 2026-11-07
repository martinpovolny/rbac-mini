# rbac-go Data Model (ERD)

Portable SQLite + Postgres schema ([internal/store/store.go](../internal/store/store.go)). Ten tables;
UUIDs and timestamps are `TEXT`, booleans are `INTEGER 0/1`, `attribute_filter`
is a JSON `TEXT` blob. Every business table carries `tenant_id`.

```mermaid
erDiagram
    tenants ||--o{ principals : "tenant_id"
    tenants ||--o{ groups : "tenant_id"
    tenants ||--o{ policies : "tenant_id"
    tenants ||--o{ roles : "tenant_id"
    tenants ||--o{ permissions : "tenant_id"
    tenants ||--o{ access : "tenant_id"
    tenants ||--o{ resource_definitions : "tenant_id"
    groups ||--o{ group_principals : "group_id"
    principals ||--o{ group_principals : "principal_id"
    groups ||--o{ policies : "group_id"
    policies ||--o{ policy_roles : "policy_id"
    roles ||--o{ policy_roles : "role_id"
    roles ||--o{ access : "role_id"
    permissions ||--o{ access : "permission_id"
    access ||--o{ resource_definitions : "access_id"
    tenants {
        INTEGER id PK
        TEXT org_id UK
        TEXT tenant_name
        TEXT account_id
        INTEGER ready
    }
    principals {
        INTEGER id PK
        TEXT uuid UK
        TEXT username
        TEXT type
        TEXT service_account_id
        TEXT user_id
        TEXT email
        TEXT first_name
        TEXT last_name
        INTEGER is_org_admin
        INTEGER is_active
        INTEGER cross_account
        INTEGER tenant_id FK
    }
    groups {
        INTEGER id PK
        TEXT uuid UK
        TEXT name
        TEXT description
        INTEGER platform_default
        INTEGER system
        INTEGER admin_default
        TEXT created
        TEXT modified
        INTEGER tenant_id FK
    }
    group_principals {
        INTEGER group_id PK, FK
        INTEGER principal_id PK, FK
    }
    policies {
        INTEGER id PK
        TEXT uuid UK
        TEXT name
        INTEGER group_id FK
        INTEGER system
        INTEGER tenant_id FK
    }
    policy_roles {
        INTEGER policy_id PK, FK
        INTEGER role_id PK, FK
    }
    roles {
        INTEGER id PK
        TEXT uuid UK
        TEXT name
        TEXT display_name
        TEXT description
        INTEGER system
        INTEGER platform_default
        INTEGER admin_default
        TEXT created
        TEXT modified
        INTEGER tenant_id FK
    }
    permissions {
        INTEGER id PK
        TEXT application
        TEXT resource_type
        TEXT verb
        TEXT permission UK
        INTEGER tenant_id FK
    }
    access {
        INTEGER id PK
        INTEGER role_id FK
        INTEGER permission_id FK
        INTEGER tenant_id FK
    }
    resource_definitions {
        INTEGER id PK
        INTEGER access_id FK
        TEXT attribute_filter
        INTEGER tenant_id FK
    }
```

## How the hot path joins it

`GET /api/rbac/v1/access/` ([internal/access/access.go](../internal/access/access.go)) walks one query:

```
tenants → principals (org_id + lower(username))
  → group_principals → groups
      (own tenant groups
       + platform_default of tenant and public tenant
       + admin_default of tenant and public tenant when org admin)
  → policies → policy_roles → roles → access → permissions (+application filter)
  → LEFT JOIN resource_definitions (JSON decoded in Go, grouped per permission)
```

Provisioning writes it without an ORM:

* `seed.Seed` — `public` tenant + `Cost Administrator`
  (`cost-management:*:*`) + `Sources administrator` (`sources:*:*`).
* `seed.Bootstrap` — tenant + `CI Test Admin` group + policy + principals.
* `keycloak.Sync` — tenant ensure + principal upserts/prunes (+ group
  membership only in single-org mode).
