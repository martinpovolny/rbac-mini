package store

import "testing"

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
