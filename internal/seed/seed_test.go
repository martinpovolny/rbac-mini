package seed

import (
	"testing"
	"time"

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
	for _, table := range []string{"tenants", "roles", "permissions", "access", "resource_definitions",
		"groups", "policies", "policy_roles"} {
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

	// The UI role wizard lists the granular catalog (allowed_only,
	// exclude_globals), not just the two application-wide grants.
	var permCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM permissions`).Scan(&permCount); err != nil || permCount < 20 {
		t.Errorf("permissions = %d, err = %v, want the full catalog", permCount, err)
	}
	var modelRead int
	if err := db.QueryRow(`SELECT COUNT(*) FROM permissions
		WHERE permission='cost-management:cost_model:read'`).Scan(&modelRead); err != nil || modelRead != 1 {
		t.Errorf("cost-management:cost_model:read count = %d, err = %v, want 1", modelRead, err)
	}

	// Both roles carry admin_default, and the public "Default admin access"
	// group links them (Django seed_group parity).
	for _, name := range []string{"Cost Administrator", "Sources administrator"} {
		var adminDefault int
		if err := db.QueryRow(`SELECT admin_default FROM roles WHERE name=?`, name).Scan(&adminDefault); err != nil || adminDefault != 1 {
			t.Errorf("role %q admin_default = %d, err = %v, want 1", name, adminDefault, err)
		}
	}
	var adminDefaultGroup int
	if err := db.QueryRow(`SELECT admin_default FROM groups WHERE name='Default admin access'`).Scan(&adminDefaultGroup); err != nil || adminDefaultGroup != 1 {
		t.Fatalf("Default admin access admin_default = %d, err = %v, want 1", adminDefaultGroup, err)
	}
	var linked int
	if err := db.QueryRow(`SELECT COUNT(*) FROM policy_roles pr
		JOIN policies p ON p.id = pr.policy_id
		WHERE p.name='Default admin access'`).Scan(&linked); err != nil || linked != 2 {
		t.Errorf("Default admin access roles = %d, err = %v, want 2", linked, err)
	}
	for _, q := range []string{
		`SELECT created FROM roles WHERE name='Cost Administrator'`,
		`SELECT modified FROM roles WHERE name='Cost Administrator'`,
		`SELECT created FROM groups WHERE name='Default admin access'`,
		`SELECT modified FROM groups WHERE name='Default admin access'`,
	} {
		var modified string
		if err := db.QueryRow(q).Scan(&modified); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		if _, err := time.Parse(time.RFC3339, modified); err != nil {
			t.Errorf("%s: modified = %q, not RFC3339: %v", q, modified, err)
		}
	}

	// An org admin with no explicit group gets the admin_default grants; a
	// regular user in the same tenant gets none until someone adds them.
	if _, err := db.Exec(`INSERT INTO tenants(org_id, tenant_name, ready) VALUES('200002','acct',1)`); err != nil {
		t.Fatalf("tenant: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO principals(uuid, username, tenant_id)
		SELECT 'pu-viewer', 'viewer', id FROM tenants WHERE org_id='200002'`); err != nil {
		t.Fatalf("principal: %v", err)
	}
	adminAccess, err := access.Query(db, "200002", "viewer", []string{"cost-management"}, true)
	if err != nil {
		t.Fatalf("admin Query: %v", err)
	}
	if len(adminAccess) != 1 || adminAccess[0].Permission != "cost-management:*:*" {
		t.Errorf("org admin access = %v, want [cost-management:*:*]", adminAccess)
	}
	userAccess, err := access.Query(db, "200002", "viewer", []string{"cost-management"}, false)
	if err != nil {
		t.Fatalf("user Query: %v", err)
	}
	if len(userAccess) != 0 {
		t.Errorf("non-admin access = %v, want empty", userAccess)
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
