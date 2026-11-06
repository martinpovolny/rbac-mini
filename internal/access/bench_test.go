package access

import (
	"fmt"
	"testing"

	"rbac-go/internal/testdb"
)

// BenchmarkAccessQuery seeds 200 principals (own group/policy/role, 5
// permissions with resourceDefinitions each) and times one user's access
// lookup — the per-request hot path behind koku's middleware.
func BenchmarkAccessQuery(b *testing.B) {
	db := testdb.Open(b)
	exec := func(q string, args ...any) {
		b.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			b.Fatalf("seed: %v", err)
		}
	}
	exec(`INSERT INTO tenants(org_id, tenant_name, ready) VALUES('100001','acct1',1)`)
	exec(`INSERT INTO tenants(org_id, tenant_name, ready) VALUES('public','public',1)`)
	for i := 0; i < 5; i++ {
		exec(`INSERT INTO permissions(application, resource_type, verb, permission, tenant_id)
			VALUES('app', 'res', 'read', ?, (SELECT id FROM tenants WHERE org_id='public'))`,
			fmt.Sprintf("app:res%d:read", i))
	}
	for i := 0; i < 200; i++ {
		u := fmt.Sprintf("user-%d", i)
		exec(`INSERT INTO principals(uuid, username, tenant_id) VALUES(?, ?, (SELECT id FROM tenants WHERE org_id='100001'))`, "pu-"+u, u)
		exec(`INSERT INTO groups(uuid, name, tenant_id) VALUES(?, ?, (SELECT id FROM tenants WHERE org_id='100001'))`, "g-"+u, "grp-"+u)
		exec(`INSERT INTO group_principals(group_id, principal_id) VALUES(
			(SELECT id FROM groups WHERE uuid=?), (SELECT id FROM principals WHERE uuid=?))`, "g-"+u, "pu-"+u)
		exec(`INSERT INTO roles(uuid, name, tenant_id) VALUES(?, ?, (SELECT id FROM tenants WHERE org_id='100001'))`, "r-"+u, "role-"+u)
		for j := 0; j < 5; j++ {
			exec(`INSERT INTO access(role_id, permission_id, tenant_id) VALUES(
				(SELECT id FROM roles WHERE uuid=?),
				(SELECT id FROM permissions WHERE permission=?),
				(SELECT id FROM tenants WHERE org_id='100001'))`, "r-"+u, fmt.Sprintf("app:res%d:read", j))
			exec(`INSERT INTO resource_definitions(access_id, attribute_filter, tenant_id) VALUES(
				(SELECT id FROM access WHERE role_id=(SELECT id FROM roles WHERE uuid=?) LIMIT 1),
				'{"key":"k","operation":"in","value":["a","b"]}',
				(SELECT id FROM tenants WHERE org_id='100001'))`, "r-"+u)
		}
		exec(`INSERT INTO policies(uuid, name, group_id, tenant_id) VALUES(?, ?,
			(SELECT id FROM groups WHERE uuid=?), (SELECT id FROM tenants WHERE org_id='100001'))`, "pol-"+u, "p-"+u, "g-"+u)
		exec(`INSERT INTO policy_roles(policy_id, role_id) VALUES(
			(SELECT id FROM policies WHERE uuid=?), (SELECT id FROM roles WHERE uuid=?))`, "pol-"+u, "r-"+u)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := Query(db, "100001", "user-42", []string{"app"}); err != nil {
			b.Fatal(err)
		}
	}
}
