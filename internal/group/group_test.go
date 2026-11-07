package group

import (
	"testing"

	"rbac-go/internal/store"
	"rbac-go/internal/testdb"
)

func seedGroups(t *testing.T, db *store.DB) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO tenants(org_id, tenant_name, account_id, ready)
		VALUES('100001','acct1111111','1111111',1)`); err != nil {
		t.Fatalf("tenant: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO tenants(org_id, tenant_name, account_id, ready)
		VALUES('200002','acct2222222','2222222',1)`); err != nil {
		t.Fatalf("tenant: %v", err)
	}
	groups := []struct {
		OrgID, Name string
		Platform    bool
		Admin       bool
	}{
		{"100001", "Default access", true, false},
		{"100001", "Default admin access", false, true},
		{"100001", "test-group-one", false, false},
		{"100001", "test-group-two", false, false},
		{"100001", "Other Group", false, false},
		{"200002", "test-group-other", false, false},
	}
	for _, g := range groups {
		platform, admin := 0, 0
		if g.Platform {
			platform = 1
		}
		if g.Admin {
			admin = 1
		}
		system := 0
		if g.Platform || g.Admin {
			system = 1
		}
		if _, err := db.Exec(`INSERT INTO groups(uuid, name, platform_default, admin_default, system, tenant_id)
			VALUES(?, ?, ?, ?, ?, (SELECT id FROM tenants WHERE org_id=?))`,
			store.NewUUID(), g.Name, platform, admin, system, g.OrgID); err != nil {
			t.Fatalf("group %s: %v", g.Name, err)
		}
	}
}

func names(items []Group) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.Name)
	}
	return out
}

// TestListNameFilter mirrors Django's name_filter: partial (case-insensitive
// contains) by default, exact (case-insensitive equality) for name_match.
func TestListNameFilter(t *testing.T) {
	db := testdb.Open(t)
	seedGroups(t, db)

	cases := []struct {
		name       string
		nameFilter string
		match      string
		want       []string
	}{
		{"no filter", "", "", []string{"Other Group", "test-group-one", "test-group-two"}},
		{"exact", "test-group-two", "exact", []string{"test-group-two"}},
		{"exact case insensitive", "TEST-GROUP-ONE", "exact", []string{"test-group-one"}},
		{"exact prefix is not a match", "test-group-t", "exact", []string{}},
		{"exact missing", "does-not-exist", "exact", []string{}},
		{"partial default", "test-group", "", []string{"test-group-one", "test-group-two"}},
		{"partial case insensitive", "GROUP", "", []string{"Other Group", "test-group-one", "test-group-two"}},
		{"partial no match", "zzz", "partial", []string{}},
	}
	for _, tc := range cases {
		items, total, err := List(db, "100001", Filter{Name: tc.nameFilter, NameMatch: tc.match}, 20, 0)
		if err != nil {
			t.Fatalf("%s: List: %v", tc.name, err)
		}
		got := names(items)
		if len(got) != len(tc.want) || total != len(tc.want) {
			t.Errorf("%s: groups = %v (total %d), want %v", tc.name, got, total, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("%s: groups = %v, want %v", tc.name, got, tc.want)
				break
			}
		}
	}

	// Default groups stay hidden even when their name matches.
	items, total, err := List(db, "100001", Filter{Name: "Default"}, 20, 0)
	if err != nil {
		t.Fatalf("default filter: %v", err)
	}
	if total != 0 || len(items) != 0 {
		t.Errorf("default groups leaked into the list: %v", names(items))
	}
	items, total, err = List(db, "100001", Filter{IncludeDefaults: true, Name: "Default"}, 20, 0)
	if err != nil {
		t.Fatalf("include defaults: %v", err)
	}
	if total != 2 || len(items) != 2 {
		t.Errorf("include defaults = %v (total %d), want both default groups", names(items), total)
	}

	// Another tenant's group must never match.
	items, total, err = List(db, "200002", Filter{Name: "test-group"}, 20, 0)
	if err != nil {
		t.Fatalf("tenant isolation: %v", err)
	}
	if total != 1 || len(items) != 1 || items[0].Name != "test-group-other" {
		t.Errorf("other tenant groups = %v (total %d), want [test-group-other]", names(items), total)
	}

	// Pagination applies after filtering.
	items, total, err = List(db, "100001", Filter{Name: "test-group"}, 1, 1)
	if err != nil {
		t.Fatalf("pagination: %v", err)
	}
	if total != 2 || len(items) != 1 || items[0].Name != "test-group-two" {
		t.Errorf("paged = %v (total %d), want [test-group-two]", names(items), total)
	}
}

// TestListDefaultGroupFilters: the UI probes ?platform_default=true and
// ?admin_default=true to find the protected groups. An explicit filter must
// override the default exclusion and return the default group, not a custom
// one.
func TestListDefaultGroupFilters(t *testing.T) {
	db := testdb.Open(t)
	seedGroups(t, db)
	ptr := func(b bool) *bool { return &b }

	cases := []struct {
		name   string
		filter Filter
		want   []string
	}{
		{"platform default true", Filter{PlatformDefault: ptr(true)}, []string{"Default access"}},
		{"admin default true", Filter{AdminDefault: ptr(true)}, []string{"Default admin access"}},
		{"platform default false", Filter{PlatformDefault: ptr(false)},
			[]string{"Other Group", "test-group-one", "test-group-two"}},
		{"admin default false", Filter{AdminDefault: ptr(false)},
			[]string{"Other Group", "test-group-one", "test-group-two"}},
		{"system true", Filter{System: ptr(true)},
			[]string{"Default access", "Default admin access"}},
		{"system false", Filter{System: ptr(false)},
			[]string{"Other Group", "test-group-one", "test-group-two"}},
		{"name and admin default", Filter{Name: "default admin", NameMatch: "partial", AdminDefault: ptr(true)},
			[]string{"Default admin access"}},
	}
	for _, tc := range cases {
		items, total, err := List(db, "100001", tc.filter, 20, 0)
		if err != nil {
			t.Fatalf("%s: List: %v", tc.name, err)
		}
		got := names(items)
		if len(got) != len(tc.want) || total != len(tc.want) {
			t.Errorf("%s: groups = %v (total %d), want %v", tc.name, got, total, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("%s: groups = %v, want %v", tc.name, got, tc.want)
				break
			}
		}
	}
}
