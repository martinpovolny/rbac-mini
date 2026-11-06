package access

import (
	"database/sql"
	"testing"

	"rbac-go/internal/store"
)

// setupPersonas mirrors operator
// test/pytest/suites/e2e/test_rbac_access.py::RBAC_SETUP_SCRIPT: three roles
// (Payment Namespace Viewer, Cluster Alpha Viewer, Cost Administrator) bound
// to alice/bob/carol via tenant groups. RBAC's job is to return the right
// resourceDefinitions per principal; koku enforces them on reports.
func setupPersonas(t *testing.T, db *sql.DB) {
	t.Helper()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	exec(`INSERT INTO tenants(org_id, tenant_name, account_id, ready) VALUES('100001','acct1111111','1111111',1)`)
	exec(`INSERT INTO tenants(org_id, tenant_name, ready) VALUES('public','public',1)`)
	pub := `(SELECT id FROM tenants WHERE org_id='public')`
	ten := `(SELECT id FROM tenants WHERE org_id='100001')`

	for _, rt := range []string{"openshift.cluster", "openshift.node", "openshift.project"} {
		exec(`INSERT INTO permissions(application, resource_type, verb, permission, tenant_id)
			VALUES('cost-management',?, 'read', 'cost-management:'||?||':read',`+pub+`)`, rt, rt)
	}
	exec(`INSERT INTO permissions(application, resource_type, verb, permission, tenant_id)
		VALUES('cost-management','*','*','cost-management:*:*',` + pub + `)`)

	for _, role := range []string{"Payment Namespace Viewer", "Cluster Alpha Viewer", "Cost Administrator"} {
		exec(`INSERT INTO roles(uuid, name, tenant_id) VALUES('r-`+role+`',?,`+pub+`)`, role)
	}
	// Payment role: project access carries in=[payment]; cluster/node open.
	exec(`INSERT INTO access(role_id, permission_id, tenant_id) VALUES(
		(SELECT id FROM roles WHERE uuid='r-Payment Namespace Viewer'),
		(SELECT id FROM permissions WHERE permission='cost-management:openshift.project:read'),` + pub + `)`)
	exec(`INSERT INTO resource_definitions(access_id, attribute_filter, tenant_id) VALUES(
		(SELECT id FROM access WHERE role_id=(SELECT id FROM roles WHERE uuid='r-Payment Namespace Viewer')
		 AND permission_id=(SELECT id FROM permissions WHERE permission='cost-management:openshift.project:read')),
		'{"key":"cost-management.openshift.project","operation":"in","value":["payment"]}',` + pub + `)`)
	// Alpha role: cluster access carries equal=alpha id.
	exec(`INSERT INTO access(role_id, permission_id, tenant_id) VALUES(
		(SELECT id FROM roles WHERE uuid='r-Cluster Alpha Viewer'),
		(SELECT id FROM permissions WHERE permission='cost-management:openshift.cluster:read'),` + pub + `)`)
	exec(`INSERT INTO resource_definitions(access_id, attribute_filter, tenant_id) VALUES(
		(SELECT id FROM access WHERE role_id=(SELECT id FROM roles WHERE uuid='r-Cluster Alpha Viewer')
		 AND permission_id=(SELECT id FROM permissions WHERE permission='cost-management:openshift.cluster:read')),
		'{"key":"cost-management.openshift.cluster","operation":"equal","value":"e2e-rbac-alpha-x"}',` + pub + `)`)
	// Admin role: full access.
	exec(`INSERT INTO access(role_id, permission_id, tenant_id) VALUES(
		(SELECT id FROM roles WHERE uuid='r-Cost Administrator'),
		(SELECT id FROM permissions WHERE permission='cost-management:*:*'),` + pub + `)`)

	groups := map[string]string{"RBAC Payment Team": "alice", "RBAC Cluster Alpha Ops": "bob", "RBAC Cost Admins": "carol"}
	roles := map[string]string{
		"RBAC Payment Team":      "Payment Namespace Viewer",
		"RBAC Cluster Alpha Ops": "Cluster Alpha Viewer",
		"RBAC Cost Admins":       "Cost Administrator",
	}
	for g, u := range groups {
		exec(`INSERT INTO principals(uuid, username, tenant_id) VALUES('pu-`+u+`',?,`+ten+`)`, u)
		exec(`INSERT INTO groups(uuid, name, tenant_id) VALUES('g-`+u+`',?,`+ten+`)`, g)
		exec(`INSERT INTO group_principals(group_id, principal_id) VALUES(
			(SELECT id FROM groups WHERE uuid='g-` + u + `'),
			(SELECT id FROM principals WHERE uuid='pu-` + u + `'))`)
		exec(`INSERT INTO policies(uuid, name, group_id, tenant_id) VALUES('pol-`+u+`',?||' Policy',
			(SELECT id FROM groups WHERE uuid='g-`+u+`'),`+ten+`)`, g)
		exec(`INSERT INTO policy_roles(policy_id, role_id) VALUES(
			(SELECT id FROM policies WHERE uuid='pol-` + u + `'),
			(SELECT id FROM roles WHERE uuid='r-` + roles[g] + `'))`)
	}
}

func findEntry(entries []Entry, perm string) *Entry {
	for i := range entries {
		if entries[i].Permission == perm {
			return &entries[i]
		}
	}
	return nil
}

func TestPersonas(t *testing.T) {
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	setupPersonas(t, db)

	// Alice sees only the payment namespace.
	alice, err := Query(db, "100001", "alice", []string{"cost-management"})
	if err != nil {
		t.Fatalf("alice Query: %v", err)
	}
	proj := findEntry(alice, "cost-management:openshift.project:read")
	if proj == nil || len(proj.ResourceDefinitions) != 1 {
		t.Fatalf("alice project access = %v, want one in-definition", alice)
	}
	af := proj.ResourceDefinitions[0].AttributeFilter
	if af["operation"] != "in" {
		t.Errorf("alice operation = %v, want in", af["operation"])
	}
	vals, _ := af["value"].([]any)
	if len(vals) != 1 || vals[0] != "payment" {
		t.Errorf("alice value = %v, want [payment]", af["value"])
	}

	// Bob is limited to cluster-alpha.
	bob, err := Query(db, "100001", "bob", []string{"cost-management"})
	if err != nil {
		t.Fatalf("bob Query: %v", err)
	}
	cluster := findEntry(bob, "cost-management:openshift.cluster:read")
	if cluster == nil || len(cluster.ResourceDefinitions) != 1 {
		t.Fatalf("bob cluster access = %v, want one equal-definition", bob)
	}
	if cluster.ResourceDefinitions[0].AttributeFilter["value"] != "e2e-rbac-alpha-x" {
		t.Errorf("bob value = %v, want e2e-rbac-alpha-x",
			cluster.ResourceDefinitions[0].AttributeFilter["value"])
	}

	// Carol has full access.
	carol, err := Query(db, "100001", "carol", []string{"cost-management"})
	if err != nil {
		t.Fatalf("carol Query: %v", err)
	}
	if findEntry(carol, "cost-management:*:*") == nil {
		t.Errorf("carol access = %v, want cost-management:*:*", carol)
	}
}
