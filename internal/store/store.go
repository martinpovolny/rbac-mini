package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

// TimestampLayout is fixed-width so TEXT columns sort chronologically on both
// SQLite and Postgres. time.Parse(time.RFC3339, ...) reads it.
const TimestampLayout = "2006-01-02T15:04:05.000000000Z"

// Now returns the UTC timestamp format stored in TEXT columns.
func Now() string {
	return time.Now().UTC().Format(TimestampLayout)
}

// NewUUID returns a random v4 UUID string (DB stores UUIDs as TEXT).
func NewUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	dst := make([]byte, 36)
	hex.Encode(dst[0:8], b[0:4])
	dst[8] = '-'
	hex.Encode(dst[9:13], b[4:6])
	dst[13] = '-'
	hex.Encode(dst[14:18], b[6:8])
	dst[18] = '-'
	hex.Encode(dst[19:23], b[8:10])
	dst[23] = '-'
	hex.Encode(dst[24:], b[10:])
	return string(dst)
}

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
  email TEXT,
  first_name TEXT,
  last_name TEXT,
  is_org_admin INTEGER NOT NULL DEFAULT 0,
  is_active INTEGER NOT NULL DEFAULT 1,
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
  created TEXT,
  modified TEXT,
  tenant_id INTEGER NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  UNIQUE (name, tenant_id)
);
CREATE TABLE IF NOT EXISTS roles (
  id INTEGER PRIMARY KEY,
  uuid TEXT NOT NULL UNIQUE,
  name TEXT NOT NULL,
  display_name TEXT NOT NULL DEFAULT '',
  description TEXT,
  system INTEGER NOT NULL DEFAULT 0,
  platform_default INTEGER NOT NULL DEFAULT 0,
  admin_default INTEGER NOT NULL DEFAULT 0,
  created TEXT,
  modified TEXT,
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

// Driver names.
const (
	DriverSQLite   = "sqlite"
	DriverPostgres = "postgres"
)

// DB wraps sql.DB with its dialect so every query is rebound to the right
// placeholder style at the call site (SPEC §3).
type DB struct {
	*sql.DB
	Driver string
}

// Tx wraps sql.Tx with its dialect.
type Tx struct {
	*sql.Tx
	driver string
}

// Exec rebounds placeholders for the dialect.
func (db *DB) Exec(query string, args ...any) (sql.Result, error) {
	return db.DB.Exec(Rebind(db.Driver, query), args...)
}

// Query rebounds placeholders for the dialect.
func (db *DB) Query(query string, args ...any) (*sql.Rows, error) {
	return db.DB.Query(Rebind(db.Driver, query), args...)
}

// QueryRow rebounds placeholders for the dialect.
func (db *DB) QueryRow(query string, args ...any) *sql.Row {
	return db.DB.QueryRow(Rebind(db.Driver, query), args...)
}

// Begin starts a dialect-aware transaction.
func (db *DB) Begin() (*Tx, error) {
	tx, err := db.DB.Begin()
	if err != nil {
		return nil, err
	}
	return &Tx{Tx: tx, driver: db.Driver}, nil
}

// Exec rebounds placeholders for the dialect.
func (tx *Tx) Exec(query string, args ...any) (sql.Result, error) {
	return tx.Tx.Exec(Rebind(tx.driver, query), args...)
}

// QueryRow rebounds placeholders for the dialect.
func (tx *Tx) QueryRow(query string, args ...any) *sql.Row {
	return tx.Tx.QueryRow(Rebind(tx.driver, query), args...)
}

// OpenSQLite opens a SQLite DB (file or :memory:) with FK enforcement.
// Single connection: :memory: databases are per-connection.
func OpenSQLite(dsn string) (*DB, error) {
	raw, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	raw.SetMaxOpenConns(1)
	if _, err := raw.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		_ = raw.Close()
		return nil, err
	}
	return &DB{DB: raw, Driver: DriverSQLite}, nil
}

// Postgres pool sizing. database/sql keeps only 2 idle connections by default,
// so at concurrency 20 every burst closed and reopened connections. Keep the
// idle pool at the open limit so steady traffic reuses connections.
const (
	postgresMaxOpenConns    = 25
	postgresMaxIdleConns    = 25
	postgresConnMaxIdleTime = 5 * time.Minute
	postgresConnMaxLifetime = 30 * time.Minute
)

// OpenPostgres opens a Postgres DB (pgx stdlib, $n placeholders via Rebind).
func OpenPostgres(dsn string) (*DB, error) {
	raw, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	raw.SetMaxOpenConns(postgresMaxOpenConns)
	raw.SetMaxIdleConns(postgresMaxIdleConns)
	raw.SetConnMaxIdleTime(postgresConnMaxIdleTime)
	raw.SetConnMaxLifetime(postgresConnMaxLifetime)
	return &DB{DB: raw, Driver: DriverPostgres}, nil
}

// SchemaPostgres is Schema with auto-increment PKs; everything else is
// identical on purpose so both dialects behave the same.
var SchemaPostgres = strings.ReplaceAll(Schema, "INTEGER PRIMARY KEY", "BIGSERIAL PRIMARY KEY")

// Migrate applies the portable schema. Idempotent.
func Migrate(db *DB) error {
	schema := Schema
	if db.Driver == DriverPostgres {
		schema = SchemaPostgres
	}
	for _, stmt := range strings.Split(schema, ";") {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
	}
	// Backfill columns added after the initial schema for databases created
	// before them (CREATE TABLE IF NOT EXISTS never alters). Errors are
	// ignored: the only expected failure is "column already exists".
	backfills := []string{
		`ALTER TABLE roles ADD COLUMN description TEXT`,
		// Keycloak profile fields: the sync is the only component allowed to
		// read Keycloak, so the API serves them from here (SPEC §5).
		`ALTER TABLE principals ADD COLUMN email TEXT`,
		`ALTER TABLE principals ADD COLUMN first_name TEXT`,
		`ALTER TABLE principals ADD COLUMN last_name TEXT`,
		`ALTER TABLE principals ADD COLUMN is_org_admin INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE principals ADD COLUMN is_active INTEGER NOT NULL DEFAULT 1`,
		// "Last modified" in the RBAC UI (groups, roles, user roles).
		`ALTER TABLE roles ADD COLUMN created TEXT`,
		`ALTER TABLE roles ADD COLUMN modified TEXT`,
		`ALTER TABLE groups ADD COLUMN created TEXT`,
		`ALTER TABLE groups ADD COLUMN modified TEXT`,
	}
	for _, stmt := range backfills {
		_, _ = db.Exec(stmt)
	}
	now := Now()
	for _, table := range []string{"roles", "groups"} {
		_, _ = db.Exec(`UPDATE `+table+` SET created=? WHERE created IS NULL OR created=''`, now)
		_, _ = db.Exec(`UPDATE `+table+` SET modified=? WHERE modified IS NULL OR modified=''`, now)
	}
	return nil
}
