package access

import (
	"testing"

	"rbac-go/internal/store"
	"rbac-go/internal/testdb"
)

// seedFixture mirrors tests/management/access/test_view.py: access_data +
// principal/group/policy/role wiring: test_user in groupA, roleA grants
// app:*:* with one equal resourceDefinition.
func seedFixture(t *testing.T, db *store.DB) {
	t.Helper()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	exec(`INSERT INTO tenants(org_id, tenant_name, account_id, ready) VALUES('100001','acct1111111','1111111',1)`)
	exec(`INSERT INTO tenants(org_id, tenant_name, ready) VALUES('public','public',1)`)
	exec(`INSERT INTO principals(uuid, username, tenant_id) VALUES('pu-test','test_user',(SELECT id FROM tenants WHERE org_id='100001'))`)
	exec(`INSERT INTO groups(uuid, name, tenant_id) VALUES('g-a','groupA',(SELECT id FROM tenants WHERE org_id='100001'))`)
	exec(`INSERT INTO group_principals(group_id, principal_id) VALUES(
		(SELECT id FROM groups WHERE uuid='g-a'),
		(SELECT id FROM principals WHERE uuid='pu-test'))`)
	exec(`INSERT INTO roles(uuid, name, tenant_id) VALUES('r-a','roleA',(SELECT id FROM tenants WHERE org_id='100001'))`)
	exec(`INSERT INTO permissions(application, resource_type, verb, permission, tenant_id)
		VALUES('app','*','*','app:*:*',(SELECT id FROM tenants WHERE org_id='100001'))`)
	exec(`INSERT INTO access(role_id, permission_id, tenant_id) VALUES(
		(SELECT id FROM roles WHERE uuid='r-a'),
		(SELECT id FROM permissions WHERE permission='app:*:*'),
		(SELECT id FROM tenants WHERE org_id='100001'))`)
	exec(`INSERT INTO resource_definitions(access_id, attribute_filter, tenant_id) VALUES(
		(SELECT id FROM access LIMIT 1),
		'{"key":"key1.id","operation":"equal","value":"value1"}',
		(SELECT id FROM tenants WHERE org_id='100001'))`)
	exec(`INSERT INTO policies(uuid, name, group_id, tenant_id) VALUES('pol-a','policyA',
		(SELECT id FROM groups WHERE uuid='g-a'),
		(SELECT id FROM tenants WHERE org_id='100001'))`)
	exec(`INSERT INTO policy_roles(policy_id, role_id) VALUES(
		(SELECT id FROM policies WHERE uuid='pol-a'),
		(SELECT id FROM roles WHERE uuid='r-a'))`)
}

func testDB(t *testing.T) *store.DB {
	t.Helper()
	return testdb.Open(t)
}

func TestGetAccessNoAppSupplied(t *testing.T) {
	db := testDB(t)
	seedFixture(t, db)

	// No application filter returns everything (openapi: "When no application
	// is supplied, all permissions for the principal are returned").
	got, err := Query(db, "100001", "test_user", nil)
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	if len(got) != 1 || got[0].Permission != "app:*:*" {
		t.Errorf("got = %v, want [app:*:*]", got)
	}
}

func TestGetAccessUnknownPrincipalEmpty(t *testing.T) {
	db := testDB(t)
	seedFixture(t, db)

	// Unknown user in a known tenant, and known user in an unknown tenant:
	// both yield [] with no error (koku maps empty access to deny).
	for name, pair := range map[string][2]string{
		"unknown user":   {"100001", "nobody-unassigned"},
		"unknown tenant": {"999999", "test_user"},
	} {
		got, err := Query(db, pair[0], pair[1], []string{"app"})
		if err != nil {
			t.Fatalf("%s: Query returned error: %v", name, err)
		}
		if len(got) != 0 {
			t.Errorf("%s: len(access) = %d, want 0", name, len(got))
		}
	}
}

func TestGetAccessSuccess(t *testing.T) {
	db := testDB(t)
	seedFixture(t, db)

	got, err := Query(db, "100001", "test_user", []string{"app"})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(access) = %d, want 1", len(got))
	}
	if got[0].Permission != "app:*:*" {
		t.Errorf("permission = %q, want %q", got[0].Permission, "app:*:*")
	}
	if len(got[0].ResourceDefinitions) != 1 {
		t.Fatalf("len(resourceDefinitions) = %d, want 1", len(got[0].ResourceDefinitions))
	}
	af := got[0].ResourceDefinitions[0].AttributeFilter
	if af["key"] != "key1.id" || af["operation"] != "equal" || af["value"] != "value1" {
		t.Errorf("attributeFilter = %v, want key1.id/equal/value1", af)
	}
}

// addSecondApp grants roleA a cost-management permission alongside app:*:*.
func addSecondApp(t *testing.T, db *store.DB) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO permissions(application, resource_type, verb, permission, tenant_id)
		VALUES('cost-management','aws.account','read','cost-management:aws.account:read',
		(SELECT id FROM tenants WHERE org_id='100001'))`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO access(role_id, permission_id, tenant_id) VALUES(
		(SELECT id FROM roles WHERE uuid='r-a'),
		(SELECT id FROM permissions WHERE permission='cost-management:aws.account:read'),
		(SELECT id FROM tenants WHERE org_id='100001'))`); err != nil {
		t.Fatal(err)
	}
}

// TestGetAccessMultipleApps mirrors test_get_access_multiple_apps_supplied:
// a comma-separated application list returns each app's permissions.
func TestGetAccessMultipleApps(t *testing.T) {
	db := testDB(t)
	seedFixture(t, db)
	addSecondApp(t, db)

	got, err := Query(db, "100001", "test_user", []string{"app", "cost-management"})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(access) = %d, want 2", len(got))
	}
}

// TestGetAccessExactMatchOnly mirrors no_partial_match / no_subset_match:
// the application filter is an exact match, never LIKE.
func TestGetAccessExactMatchOnly(t *testing.T) {
	db := testDB(t)
	seedFixture(t, db)
	addSecondApp(t, db)

	for _, apps := range [][]string{{"cost"}, {"cost-management:aws"}, {"App"}} {
		got, err := Query(db, "100001", "test_user", apps)
		if err != nil {
			t.Fatalf("Query(%v) returned error: %v", apps, err)
		}
		if len(got) != 0 {
			t.Errorf("Query(%v) = %v, want empty (exact match only)", apps, got)
		}
	}

	got, err := Query(db, "100001", "test_user", []string{"cost-management"})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	if len(got) != 1 || got[0].Permission != "cost-management:aws.account:read" {
		t.Errorf("got = %v, want [cost-management:aws.account:read]", got)
	}
}

// TestGetAccessIncludesPublicPlatformDefault mirrors Django's
// default_group_set: the public tenant "Default access" platform-default
// group grants apply to every tenant (e.g. rbac:principal:read for gateway
// tests, base viewer roles on-prem).
func TestGetAccessIncludesPublicPlatformDefault(t *testing.T) {
	db := testDB(t)
	seedFixture(t, db)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	exec(`INSERT INTO permissions(application, resource_type, verb, permission, tenant_id)
		VALUES('rbac','principal','read','rbac:principal:read',
		(SELECT id FROM tenants WHERE org_id='public'))`)
	exec(`INSERT INTO roles(uuid, name, system, platform_default, tenant_id) VALUES('r-pub','Viewer',1,1,
		(SELECT id FROM tenants WHERE org_id='public'))`)
	exec(`INSERT INTO access(role_id, permission_id, tenant_id) VALUES(
		(SELECT id FROM roles WHERE uuid='r-pub'),
		(SELECT id FROM permissions WHERE permission='rbac:principal:read'),
		(SELECT id FROM tenants WHERE org_id='public'))`)
	exec(`INSERT INTO groups(uuid, name, platform_default, tenant_id) VALUES('g-pub','Default access',1,
		(SELECT id FROM tenants WHERE org_id='public'))`)
	exec(`INSERT INTO policies(uuid, name, group_id, tenant_id) VALUES('pol-pub','Default access Policy',
		(SELECT id FROM groups WHERE uuid='g-pub'),
		(SELECT id FROM tenants WHERE org_id='public'))`)
	exec(`INSERT INTO policy_roles(policy_id, role_id) VALUES(
		(SELECT id FROM policies WHERE uuid='pol-pub'),
		(SELECT id FROM roles WHERE uuid='r-pub'))`)

	got, err := Query(db, "100001", "test_user", []string{"rbac"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 1 || got[0].Permission != "rbac:principal:read" {
		t.Errorf("got = %+v, want [rbac:principal:read] via public Default access", got)
	}
}
