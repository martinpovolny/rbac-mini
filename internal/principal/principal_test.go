package principal

import (
	"testing"

	"rbac-go/internal/store"
	"rbac-go/internal/testdb"
)

// seedTenant inserts one tenant plus the given principals. profile columns
// mirror what the Keycloak sync persists.
type seeded struct {
	Username   string
	Email      string
	FirstName  string
	LastName   string
	Type       string
	UserID     string
	IsOrgAdmin bool
	IsActive   bool
}

func seedPrincipals(t *testing.T, db *store.DB) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO tenants(org_id, tenant_name, account_id, ready)
		VALUES('100001','acct1111111','1111111',1)`); err != nil {
		t.Fatalf("tenant: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO tenants(org_id, tenant_name, account_id, ready)
		VALUES('200002','acct2222222','2222222',1)`); err != nil {
		t.Fatalf("tenant: %v", err)
	}
	rows := []seeded{
		{Username: "alice", Email: "alice@example.com", FirstName: "Alice", LastName: "Admin",
			Type: "user", UserID: "kc-alice", IsOrgAdmin: true, IsActive: true},
		{Username: "bob", Email: "bob@example.com", FirstName: "Bob", LastName: "User",
			Type: "user", UserID: "kc-bob", IsActive: false},
		{Username: "carol", FirstName: "Carol", Type: "user", IsActive: true},
		{Username: "svc", Type: "service-account", IsActive: true},
	}
	for _, r := range rows {
		insertPrincipal(t, db, "100001", r)
	}
	insertPrincipal(t, db, "200002", seeded{Username: "mallory", Type: "user", IsActive: true})
}

func insertPrincipal(t *testing.T, db *store.DB, orgID string, r seeded) {
	t.Helper()
	kind := r.Type
	if kind == "" {
		kind = "user"
	}
	active := 0
	if r.IsActive {
		active = 1
	}
	admin := 0
	if r.IsOrgAdmin {
		admin = 1
	}
	if _, err := db.Exec(`INSERT INTO principals(uuid, username, type, user_id, email,
		first_name, last_name, is_org_admin, is_active, tenant_id)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, (SELECT id FROM tenants WHERE org_id=?))`,
		store.NewUUID(), r.Username, kind, r.UserID, r.Email,
		r.FirstName, r.LastName, admin, active, orgID); err != nil {
		t.Fatalf("principal %s: %v", r.Username, err)
	}
}

func usernames(items []Principal) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.Username)
	}
	return out
}

func TestListReturnsProfiles(t *testing.T) {
	db := testdb.Open(t)
	seedPrincipals(t, db)

	items, total, err := List(db, "100001", Filter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 2 || len(items) != 2 {
		t.Fatalf("total = %d, items = %v, want 2 enabled users", total, usernames(items))
	}
	alice := items[0]
	if alice.Username != "alice" || alice.Email != "alice@example.com" ||
		alice.FirstName != "Alice" || alice.LastName != "Admin" {
		t.Errorf("alice = %+v", alice)
	}
	if !alice.IsActive || !alice.IsOrgAdmin {
		t.Errorf("alice flags = active:%v admin:%v, want both true", alice.IsActive, alice.IsOrgAdmin)
	}
	if alice.UserID != "kc-alice" || alice.Type != "user" {
		t.Errorf("alice user_id/type = %q/%q", alice.UserID, alice.Type)
	}
}

// TestListEmailFallsBackToLocal mirrors the BYPASS payload: a principal with
// no Keycloak email still gets a usable, non-placeholder address.
func TestListEmailFallsBackToLocal(t *testing.T) {
	db := testdb.Open(t)
	seedPrincipals(t, db)

	items, _, err := List(db, "100001", Filter{Usernames: []string{"carol"}})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 1 || items[0].Email != "carol@local" {
		t.Fatalf("carol = %+v, want email carol@local", items)
	}
	if items[0].FirstName != "Carol" || items[0].LastName != "" {
		t.Errorf("carol names = %q/%q", items[0].FirstName, items[0].LastName)
	}
}

func TestListStatus(t *testing.T) {
	db := testdb.Open(t)
	seedPrincipals(t, db)

	cases := []struct {
		status string
		want   []string
	}{
		{"", []string{"alice", "carol"}},
		{"enabled", []string{"alice", "carol"}},
		{"disabled", []string{"bob"}},
		{"all", []string{"alice", "bob", "carol"}},
	}
	for _, tc := range cases {
		items, total, err := List(db, "100001", Filter{Status: tc.status})
		if err != nil {
			t.Fatalf("List(%q): %v", tc.status, err)
		}
		if total != len(tc.want) || len(items) != len(tc.want) {
			t.Errorf("status %q: total=%d items=%v, want %v", tc.status, total, usernames(items), tc.want)
			continue
		}
		for i, want := range tc.want {
			if items[i].Username != want {
				t.Errorf("status %q: items = %v, want %v", tc.status, usernames(items), tc.want)
				break
			}
		}
	}
}

func TestListAdminOnly(t *testing.T) {
	db := testdb.Open(t)
	seedPrincipals(t, db)

	items, total, err := List(db, "100001", Filter{AdminOnly: true})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 1 || len(items) != 1 || items[0].Username != "alice" {
		t.Fatalf("admin_only items = %v (total %d), want [alice]", usernames(items), total)
	}
}

// TestListUsernamesExactAddsUnknownStub: the UI detail page requests a single
// username and must get 200 even when that user is not (or no longer) stored.
func TestListUsernamesExactAddsUnknownStub(t *testing.T) {
	db := testdb.Open(t)
	seedPrincipals(t, db)

	items, total, err := List(db, "100001", Filter{
		Usernames: []string{"Alice", "ghost"},
		Status:    "enabled",
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 2 || len(items) != 2 {
		t.Fatalf("total=%d items=%v, want alice and ghost", total, usernames(items))
	}
	if items[0].Username != "alice" || items[0].Email != "alice@example.com" {
		t.Errorf("first item = %+v, want alice with profile", items[0])
	}
	ghost := items[1]
	if ghost.Username != "ghost" || ghost.Email != "ghost@local" || !ghost.IsActive ||
		ghost.IsOrgAdmin || ghost.Type != "user" || ghost.UserID != "ghost" {
		t.Errorf("ghost = %+v", ghost)
	}
}

// TestListUsernamesPartial: match_criteria=partial uses only the first
// username as a prefix (Django's principalStartsWith).
func TestListUsernamesPartial(t *testing.T) {
	db := testdb.Open(t)
	seedPrincipals(t, db)

	items, total, err := List(db, "100001", Filter{
		Usernames: []string{"AL", "carol"}, MatchCriteria: "partial", Status: "all",
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 1 || len(items) != 1 || items[0].Username != "alice" {
		t.Fatalf("partial items = %v (total %d), want [alice]", usernames(items), total)
	}
}

func TestListEmailMatch(t *testing.T) {
	db := testdb.Open(t)
	seedPrincipals(t, db)

	items, _, err := List(db, "100001", Filter{Email: "alice@", MatchCriteria: "partial"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 1 || items[0].Username != "alice" {
		t.Fatalf("partial email items = %v, want [alice]", usernames(items))
	}

	items, _, err = List(db, "100001", Filter{Email: "bob@example.com"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("exact email default status items = %v, want none (bob disabled)", usernames(items))
	}

	items, _, err = List(db, "100001", Filter{Email: "bob@example.com", Status: "all"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 1 || items[0].Username != "bob" {
		t.Fatalf("exact email items = %v, want [bob]", usernames(items))
	}
}

func TestListSortAndPagination(t *testing.T) {
	db := testdb.Open(t)
	seedPrincipals(t, db)

	items, total, err := List(db, "100001", Filter{Status: "all", SortDesc: true})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got := usernames(items); total != 3 || len(got) != 3 || got[0] != "carol" || got[2] != "alice" {
		t.Fatalf("desc items = %v (total %d), want [carol bob alice]", got, total)
	}

	items, total, err = List(db, "100001", Filter{Status: "all", Limit: 1, Offset: 1})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 3 || len(items) != 1 || items[0].Username != "bob" {
		t.Fatalf("paged items = %v (total %d), want [bob]", usernames(items), total)
	}

	items, total, err = List(db, "100001", Filter{Status: "all", Limit: 10, Offset: 10})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 3 || items == nil || len(items) != 0 {
		t.Fatalf("offset past end = %#v (total %d), want empty non-nil", items, total)
	}
}

func TestListTenantIsolationAndType(t *testing.T) {
	db := testdb.Open(t)
	seedPrincipals(t, db)

	items, total, err := List(db, "100001", Filter{Status: "all", Type: "all"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 4 || len(items) != 4 {
		t.Fatalf("type=all items = %v (total %d), want all four tenant principals", usernames(items), total)
	}
	for _, item := range items {
		if item.Username == "mallory" {
			t.Error("List leaked a principal from another tenant")
		}
	}

	items, total, err = List(db, "100001", Filter{Status: "all", Type: "service-account"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 1 || len(items) != 1 || items[0].Username != "svc" {
		t.Fatalf("service accounts = %v (total %d), want [svc]", usernames(items), total)
	}
}
