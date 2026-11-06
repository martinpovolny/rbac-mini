package seed

import (
	"database/sql"
	"testing"

	"rbac-go/internal/store"
)

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func counts(t *testing.T, db *sql.DB) map[string]int {
	t.Helper()
	out := map[string]int{}
	for _, table := range []string{"tenants", "roles", "permissions", "access", "resource_definitions"} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		out[table] = n
	}
	return out
}

// TestSeedCostAdministrator: the seed provides the public tenant plus the
// Cost Administrator role granting cost-management:*:*, and running it twice
// changes nothing (operator Job re-runs on every deploy).
func TestSeedCostAdministrator(t *testing.T) {
	db := testDB(t)

	if err := Seed(db); err != nil {
		t.Fatalf("first Seed returned error: %v", err)
	}
	var perm string
	err := db.QueryRow(`SELECT p.permission FROM permissions p
		JOIN access a ON a.permission_id = p.id
		JOIN roles r ON r.id = a.role_id
		JOIN tenants t ON t.id = r.tenant_id
		WHERE r.name = 'Cost Administrator' AND t.tenant_name = 'public'`).Scan(&perm)
	if err != nil {
		t.Fatalf("Cost Administrator grant missing: %v", err)
	}
	if perm != "cost-management:*:*" {
		t.Errorf("permission = %q, want %q", perm, "cost-management:*:*")
	}
	first := counts(t, db)

	if err := Seed(db); err != nil {
		t.Fatalf("second Seed returned error: %v", err)
	}
	second := counts(t, db)
	for table, n := range first {
		if second[table] != n {
			t.Errorf("%s: count changed %d -> %d, want idempotent", table, n, second[table])
		}
	}
}
