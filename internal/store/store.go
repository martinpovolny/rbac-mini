package store

import (
	"database/sql"
	"strings"

	_ "modernc.org/sqlite"
)

// Schema is the portable DDL for the 10 v1 tables (SPEC §3).
// Types are restricted to INTEGER / TEXT so the same statements run on
// SQLite (dev/test) and Postgres (prod DDL adds SERIAL equivalents
// separately). No DISTINCT ON, no JSON operators, no expression indexes.
const Schema = `
CREATE TABLE IF NOT EXISTS tenants (
  id INTEGER PRIMARY KEY,
  org_id TEXT NOT NULL UNIQUE,
  tenant_name TEXT NOT NULL,
  account_id TEXT,
  ready INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS principals (
  id INTEGER PRIMARY KEY,
  uuid TEXT NOT NULL UNIQUE,
  username TEXT NOT NULL,
  type TEXT NOT NULL DEFAULT 'user',
  service_account_id TEXT,
  user_id TEXT,
  cross_account INTEGER NOT NULL DEFAULT 0,
  tenant_id INTEGER NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  UNIQUE (username, tenant_id)
);
CREATE TABLE IF NOT EXISTS groups (
  id INTEGER PRIMARY KEY,
  uuid TEXT NOT NULL UNIQUE,
  name TEXT NOT NULL,
  description TEXT,
  platform_default INTEGER NOT NULL DEFAULT 0,
  system INTEGER NOT NULL DEFAULT 0,
  admin_default INTEGER NOT NULL DEFAULT 0,
  tenant_id INTEGER NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  UNIQUE (name, tenant_id)
);
CREATE TABLE IF NOT EXISTS group_principals (
  group_id INTEGER NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
  principal_id INTEGER NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  PRIMARY KEY (group_id, principal_id)
);
CREATE TABLE IF NOT EXISTS policies (
  id INTEGER PRIMARY KEY,
  uuid TEXT NOT NULL UNIQUE,
  name TEXT NOT NULL,
  group_id INTEGER REFERENCES groups(id) ON DELETE CASCADE,
  system INTEGER NOT NULL DEFAULT 0,
  tenant_id INTEGER NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  UNIQUE (name, tenant_id)
);
CREATE TABLE IF NOT EXISTS policy_roles (
  policy_id INTEGER NOT NULL REFERENCES policies(id) ON DELETE CASCADE,
  role_id INTEGER NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
  PRIMARY KEY (policy_id, role_id)
);
CREATE TABLE IF NOT EXISTS roles (
  id INTEGER PRIMARY KEY,
  uuid TEXT NOT NULL UNIQUE,
  name TEXT NOT NULL,
  display_name TEXT NOT NULL DEFAULT '',
  system INTEGER NOT NULL DEFAULT 0,
  platform_default INTEGER NOT NULL DEFAULT 0,
  admin_default INTEGER NOT NULL DEFAULT 0,
  tenant_id INTEGER NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  UNIQUE (name, tenant_id)
);
CREATE TABLE IF NOT EXISTS permissions (
  id INTEGER PRIMARY KEY,
  application TEXT NOT NULL,
  resource_type TEXT NOT NULL,
  verb TEXT NOT NULL,
  permission TEXT NOT NULL UNIQUE,
  tenant_id INTEGER NOT NULL REFERENCES tenants(id) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS access (
  id INTEGER PRIMARY KEY,
  role_id INTEGER NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
  permission_id INTEGER NOT NULL REFERENCES permissions(id) ON DELETE CASCADE,
  tenant_id INTEGER NOT NULL REFERENCES tenants(id) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS resource_definitions (
  id INTEGER PRIMARY KEY,
  access_id INTEGER NOT NULL REFERENCES access(id) ON DELETE CASCADE,
  attribute_filter TEXT NOT NULL DEFAULT '{}',
  tenant_id INTEGER NOT NULL REFERENCES tenants(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_principals_tenant ON principals(tenant_id);
CREATE INDEX IF NOT EXISTS idx_groups_tenant ON groups(tenant_id);
CREATE INDEX IF NOT EXISTS idx_policies_tenant ON policies(tenant_id);
CREATE INDEX IF NOT EXISTS idx_roles_tenant ON roles(tenant_id);
CREATE INDEX IF NOT EXISTS idx_permissions_app ON permissions(application);
CREATE INDEX IF NOT EXISTS idx_access_tenant ON access(tenant_id);
CREATE INDEX IF NOT EXISTS idx_resdef_access ON resource_definitions(access_id);
`

// OpenSQLite opens a SQLite DB (file or :memory:) with FK enforcement.
func OpenSQLite(dsn string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// Migrate applies the portable schema. Idempotent.
func Migrate(db *sql.DB) error {
	for _, stmt := range strings.Split(Schema, ";") {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}
