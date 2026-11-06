package access

import (
	"database/sql"
	"testing"

	"rbac-go/internal/store"
)

// seedFixture mirrors tests/management/access/test_view.py: access_data +
// principal/group/policy/role wiring: test_user in groupA, roleA grants
// app:*:* with one equal resourceDefinition.
func seedFixture(t *testing.T, db *sql.DB) {
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
