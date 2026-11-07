package store

import (
	"testing"
	"time"
)

// oldPrincipals is the principals DDL shipped before the profile columns.
const oldPrincipals = `
CREATE TABLE principals (
  id INTEGER PRIMARY KEY,
  uuid TEXT NOT NULL UNIQUE,
  username TEXT NOT NULL,
  type TEXT NOT NULL DEFAULT 'user',
  service_account_id TEXT,
  user_id TEXT,
  cross_account INTEGER NOT NULL DEFAULT 0,
  tenant_id INTEGER NOT NULL,
  UNIQUE (username, tenant_id)
)`

// oldCatalogTables are the roles/groups DDL shipped before the modified
// column existed.
const oldCatalogTables = `
CREATE TABLE roles (
  id INTEGER PRIMARY KEY,
  uuid TEXT NOT NULL UNIQUE,
  name TEXT NOT NULL,
  display_name TEXT NOT NULL DEFAULT '',
  description TEXT,
  system INTEGER NOT NULL DEFAULT 0,
  platform_default INTEGER NOT NULL DEFAULT 0,
  admin_default INTEGER NOT NULL DEFAULT 0,
  tenant_id INTEGER NOT NULL,
  UNIQUE (name, tenant_id)
);
CREATE TABLE groups (
  id INTEGER PRIMARY KEY,
  uuid TEXT NOT NULL UNIQUE,
  name TEXT NOT NULL,
  description TEXT,
  platform_default INTEGER NOT NULL DEFAULT 0,
  system INTEGER NOT NULL DEFAULT 0,
  admin_default INTEGER NOT NULL DEFAULT 0,
  tenant_id INTEGER NOT NULL,
  UNIQUE (name, tenant_id)
)`

// TestMigrateBackfillsModified: rows created before the "Last modified"
// column existed must get a usable timestamp instead of "Invalid date".
func TestMigrateBackfillsModified(t *testing.T) {
	db, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(oldCatalogTables); err != nil {
		t.Fatalf("old schema: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO roles(uuid, name, tenant_id) VALUES('r-old','role-old',7)`); err != nil {
		t.Fatalf("old role: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO groups(uuid, name, tenant_id) VALUES('g-old','group-old',7)`); err != nil {
		t.Fatalf("old group: %v", err)
	}

	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	for _, q := range []string{
		`SELECT created FROM roles WHERE uuid='r-old'`,
		`SELECT created FROM groups WHERE uuid='g-old'`,
		`SELECT modified FROM roles WHERE uuid='r-old'`,
		`SELECT modified FROM groups WHERE uuid='g-old'`,
	} {
		var modified string
		if err := db.QueryRow(q).Scan(&modified); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		if modified == "" {
			t.Errorf("%s: modified backfill is empty", q)
		}
		if _, err := time.Parse(time.RFC3339, modified); err != nil {
			t.Errorf("%s: modified = %q, not RFC3339: %v", q, modified, err)
		}
	}
}

// TestMigrateBackfillsPrincipalProfiles: a database created by an older
// rbac-go must gain the profile columns with active principals, so an
// upgraded deployment does not show every user as inactive.
func TestMigrateBackfillsPrincipalProfiles(t *testing.T) {
	db, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(oldPrincipals); err != nil {
		t.Fatalf("old schema: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO principals(uuid, username, tenant_id)
		VALUES('pu-old','alice',7)`); err != nil {
		t.Fatalf("old row: %v", err)
	}

	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	var email, first, last string
	var active, admin int
	if err := db.QueryRow(`SELECT COALESCE(email,''), COALESCE(first_name,''), COALESCE(last_name,''),
		is_active, is_org_admin FROM principals WHERE username='alice'`).
		Scan(&email, &first, &last, &active, &admin); err != nil {
		t.Fatalf("select profile: %v", err)
	}
	if email != "" || first != "" || last != "" {
		t.Errorf("backfilled names = %q/%q/%q, want empty", email, first, last)
	}
	if active != 1 || admin != 0 {
		t.Errorf("backfilled flags = active:%d admin:%d, want 1/0", active, admin)
	}

	// Migrate stays idempotent after the backfill.
	if err := Migrate(db); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
}
