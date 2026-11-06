package seed

import (
	"testing"

	"rbac-go/internal/access"
	"rbac-go/internal/store"
	"rbac-go/internal/testdb"
)

func testDB(t *testing.T) *store.DB {
	t.Helper()
	return testdb.Open(t)
}

func counts(t *testing.T, db *store.DB) map[string]int {
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
	// On-prem koku also queries ?application=sources, which lives in a
	// different application bucket: the Sources administrator role.
	var sourcesPerm string
	err = db.QueryRow(`SELECT p.permission FROM permissions p
		JOIN access a ON a.permission_id = p.id
		JOIN roles r ON r.id = a.role_id
		JOIN tenants t ON t.id = r.tenant_id
		WHERE r.name = 'Sources administrator' AND t.tenant_name = 'public'`).Scan(&sourcesPerm)
	if err != nil {
		t.Fatalf("Sources administrator grant missing: %v", err)
	}
	if sourcesPerm != "sources:*:*" {
		t.Errorf("permission = %q, want %q", sourcesPerm, "sources:*:*")
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

// TestBootstrapGrantsCostAdministrator mirrors
// operator render_bootstrap_script: tenant + "CI Test Admin" group carrying
// the seeded Cost Administrator role + one principal per service account.
// End-to-end check: the bootstrapped principal sees cost-management:*:*.
func TestBootstrapGrantsCostAdministrator(t *testing.T) {
	db := testDB(t)
	if err := Seed(db); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	users := []string{"cost-mgmt-operator", "admin"}
	if err := Bootstrap(db, "100001", "acct1111111", users); err != nil {
		t.Fatalf("first Bootstrap returned error: %v", err)
	}

	var tenantName string
	var ready int
	if err := db.QueryRow(`SELECT tenant_name, ready FROM tenants WHERE org_id='100001'`).Scan(&tenantName, &ready); err != nil {
		t.Fatalf("tenant missing: %v", err)
	}
	if ready != 1 {
		t.Errorf("tenant ready = %d, want 1", ready)
	}

	var role string
	err := db.QueryRow(`SELECT r.name FROM policies pol
		JOIN groups g ON g.id = pol.group_id
		JOIN policy_roles pr ON pr.policy_id = pol.id
		JOIN roles r ON r.id = pr.role_id
		JOIN tenants t ON t.id = pol.tenant_id
		WHERE t.org_id='100001' AND g.name='CI Test Admin'`).Scan(&role)
	if err != nil {
		t.Fatalf("CI Test Admin policy missing: %v", err)
	}
	if role != "Cost Administrator" {
		t.Errorf("role = %q, want %q", role, "Cost Administrator")
	}

	for _, u := range users {
		var inGroup int
		err := db.QueryRow(`SELECT COUNT(*) FROM principals pr
			JOIN group_principals gp ON gp.principal_id = pr.id
			JOIN groups g ON g.id = gp.group_id
			JOIN tenants t ON t.id = pr.tenant_id AND t.id = g.tenant_id
			WHERE t.org_id='100001' AND pr.username=? AND g.name='CI Test Admin'`, u).Scan(&inGroup)
		if err != nil || inGroup != 1 {
			t.Errorf("principal %q in group: count=%d, err=%v, want 1", u, inGroup, err)
		}
	}

	got, err := access.Query(db, "100001", "cost-mgmt-operator", []string{"cost-management"}, false)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 1 || got[0].Permission != "cost-management:*:*" {
		t.Errorf("access = %v, want [cost-management:*:*]", got)
	}

	// Idempotent like the seed: re-running changes nothing.
	before := counts(t, db)
	if err := Bootstrap(db, "100001", "acct1111111", users); err != nil {
		t.Fatalf("second Bootstrap returned error: %v", err)
	}
	after := counts(t, db)
	for table, n := range before {
		if after[table] != n {
			t.Errorf("%s: count changed %d -> %d, want idempotent", table, n, after[table])
		}
	}
}
