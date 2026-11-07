package role

import (
	"testing"
	"time"

	"rbac-go/internal/store"
	"rbac-go/internal/testdb"
)

func seedRoles(t *testing.T, db *store.DB) {
	t.Helper()
	for _, org := range []struct{ ID, Name string }{
		{"public", "public"}, {"100001", "acct1111111"}, {"200002", "acct2222222"},
	} {
		if _, err := db.Exec(`INSERT INTO tenants(org_id, tenant_name, ready) VALUES(?,?,1)`,
			org.ID, org.Name); err != nil {
			t.Fatalf("tenant %s: %v", org.ID, err)
		}
	}
	roles := []struct {
		OrgID, Name, DisplayName, Modified string
		System                             bool
	}{
		{"public", "Cost Administrator", "Cost administrator", "2024-01-01T00:00:00Z", true},
		{"public", "Sources administrator", "Cloud administrator", "2024-01-02T00:00:00Z", true},
		{"100001", "test-role", "Test Role", "2024-01-03T00:00:00Z", false},
		{"100001", "test-role-2", "Second Test", "2024-01-05T00:00:00Z", false},
		{"100001", "other-role", "Display Name A", "2024-01-04T00:00:00Z", false},
		{"200002", "tenant-two-role", "Tenant Two", "2024-01-06T00:00:00Z", false},
	}
	for _, r := range roles {
		system := 0
		if r.System {
			system = 1
		}
		if _, err := db.Exec(`INSERT INTO roles(uuid, name, display_name, system, created, modified, tenant_id)
			VALUES(?, ?, ?, ?, ?, ?, (SELECT id FROM tenants WHERE org_id=?))`,
			store.NewUUID(), r.Name, r.DisplayName, system, r.Modified, r.Modified, r.OrgID); err != nil {
			t.Fatalf("role %s: %v", r.Name, err)
		}
	}
}

func names(items []Role) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.Name)
	}
	return out
}

func TestListNameAndDisplayNameFilters(t *testing.T) {
	db := testdb.Open(t)
	seedRoles(t, db)
	ptr := func(b bool) *bool { return &b }

	cases := []struct {
		name   string
		filter Filter
		want   []string
	}{
		{"no filter", Filter{}, []string{
			"Cost Administrator", "other-role", "Sources administrator", "test-role", "test-role-2"}},
		{"name exact", Filter{Name: "test-role", NameMatch: "exact"}, []string{"test-role"}},
		{"name exact case insensitive", Filter{Name: "TEST-ROLE", NameMatch: "exact"}, []string{"test-role"}},
		{"name prefix is not exact", Filter{Name: "test-rol", NameMatch: "exact"}, []string{}},
		{"name exact missing", Filter{Name: "test", NameMatch: "exact"}, []string{}},
		{"name partial", Filter{Name: "test-role"}, []string{"test-role", "test-role-2"}},
		{"display exact", Filter{DisplayName: "Test Role", NameMatch: "exact"}, []string{"test-role"}},
		{"display exact case insensitive", Filter{DisplayName: "TEST ROLE", NameMatch: "exact"}, []string{"test-role"}},
		{"display exact missing", Filter{DisplayName: "test-role", NameMatch: "exact"}, []string{}},
		{"display partial", Filter{DisplayName: "display"}, []string{"other-role"}},
		{"name and display", Filter{Name: "test-role-2", DisplayName: "Second Test", NameMatch: "exact"},
			[]string{"test-role-2"}},
		{"system only", Filter{System: ptr(true)}, []string{"Cost Administrator", "Sources administrator"}},
	}
	for _, tc := range cases {
		items, total, err := List(db, "100001", tc.filter, 20, 0)
		if err != nil {
			t.Fatalf("%s: List: %v", tc.name, err)
		}
		got := names(items)
		if len(got) != len(tc.want) || total != len(tc.want) {
			t.Errorf("%s: roles = %v (total %d), want %v", tc.name, got, total, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("%s: roles = %v, want %v", tc.name, got, tc.want)
				break
			}
		}
	}
}

// TestModifiedTimestamps: the UI renders a "Last modified" column from the
// role's modified field; a missing value shows "Invalid date".
func TestModifiedTimestamps(t *testing.T) {
	db := testdb.Open(t)
	seedRoles(t, db)

	items, _, err := List(db, "100001", Filter{}, 20, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, item := range items {
		if item.Modified == "" || item.Created == "" {
			t.Errorf("role %q has no created/modified timestamp (%q/%q)", item.Name, item.Created, item.Modified)
			continue
		}
		if _, err := time.Parse(time.RFC3339, item.Modified); err != nil {
			t.Errorf("role %q modified = %q, not RFC3339: %v", item.Name, item.Modified, err)
		}
		if _, err := time.Parse(time.RFC3339, item.Created); err != nil {
			t.Errorf("role %q created = %q, not RFC3339: %v", item.Name, item.Created, err)
		}
	}

	items, total, err := List(db, "100001", Filter{OrderBy: "modified", Desc: true}, 20, 0)
	if err != nil {
		t.Fatalf("List by modified: %v", err)
	}
	if total != 5 || len(items) != 5 {
		t.Fatalf("roles = %v (total %d), want 5", names(items), total)
	}
	if items[0].Name != "test-role-2" || items[len(items)-1].Name != "Cost Administrator" {
		t.Errorf("modified desc order = %v", names(items))
	}

	var uuid string
	if err := db.QueryRow(`SELECT uuid FROM roles WHERE name='test-role'`).Scan(&uuid); err != nil {
		t.Fatalf("uuid: %v", err)
	}
	before, err := Get(db, "100001", uuid)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	updated, err := Update(db, "100001", uuid, "test-role", "Test Role", "", nil, nil)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Modified == "" || updated.Modified == before.Modified {
		t.Errorf("modified after update = %q (before %q), want a new timestamp", updated.Modified, before.Modified)
	}
	if updated.Created != before.Created {
		t.Errorf("created changed on update: %q -> %q", before.Created, updated.Created)
	}
	if _, err := time.Parse(time.RFC3339, updated.Modified); err != nil {
		t.Errorf("updated modified = %q, not RFC3339: %v", updated.Modified, err)
	}

	patched, err := Patch(db, "100001", uuid, map[string]string{"description": "changed"})
	if err != nil {
		t.Fatalf("Patch: %v", err)
	}
	if patched.Modified == updated.Modified {
		t.Errorf("modified after patch = %q, want a new timestamp", patched.Modified)
	}
}

func TestListOrderAndPagination(t *testing.T) {
	db := testdb.Open(t)
	seedRoles(t, db)

	items, total, err := List(db, "100001", Filter{OrderBy: "display_name", Desc: true}, 20, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 5 || len(items) != 5 {
		t.Fatalf("roles = %v (total %d), want 5", names(items), total)
	}
	if items[0].Name != "test-role" || items[len(items)-1].Name != "Sources administrator" {
		t.Errorf("display_name desc order = %v", names(items))
	}

	items, total, err = List(db, "100001", Filter{}, 2, 1)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 5 || len(items) != 2 || items[0].Name != "other-role" || items[1].Name != "Sources administrator" {
		t.Errorf("paged roles = %v (total %d), want [other-role Sources administrator]", names(items), total)
	}

	// Another tenant never sees this tenant's custom role.
	items, total, err = List(db, "200002", Filter{Name: "test-role"}, 20, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 0 || len(items) != 0 {
		t.Errorf("tenant isolation roles = %v (total %d), want none", names(items), total)
	}
}
