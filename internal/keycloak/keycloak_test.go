package keycloak

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"rbac-go/internal/seed"
	"rbac-go/internal/store"
	"rbac-go/internal/testdb"
)

// stubKeycloak emulates the two Keycloak calls the sync needs: the
// client-credentials token endpoint and the realm users list.
func stubKeycloak(t *testing.T, users []string, seen *[]string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/realms/onprem/protocol/openid-connect/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("token form: %v", err)
		}
		if r.Form.Get("grant_type") != "client_credentials" {
			t.Errorf("grant_type = %q, want client_credentials", r.Form.Get("grant_type"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "test-token"})
	})
	mux.HandleFunc("/admin/realms/onprem/users", func(w http.ResponseWriter, r *http.Request) {
		*seen = append(*seen, r.Header.Get("Authorization"))
		out := make([]map[string]any, 0, len(users))
		for _, u := range users {
			out = append(out, map[string]any{"username": u})
		}
		_ = json.NewEncoder(w).Encode(out)
	})
	return httptest.NewServer(mux)
}

func TestSyncUpsertsAndPrunes(t *testing.T) {
	db := testdb.Open(t)
	if err := seed.Seed(db); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	if err := seed.Bootstrap(db, "100001", "1111111", []string{"alice", "stale"}); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}

	var seen []string
	srv := stubKeycloak(t, []string{"alice", "bob"}, &seen)
	defer srv.Close()

	err := Sync(db, Config{
		BaseURL: srv.URL, Realm: "onprem",
		ClientID: "rbac-keycloak-sync", ClientSecret: "s3cret",
		OrgID: "100001", Group: "CI Test Admin", PruneOrphans: true,
	})
	if err != nil {
		t.Fatalf("first Sync returned error: %v", err)
	}
	if len(seen) != 1 || seen[0] != "Bearer test-token" {
		t.Errorf("users call auth = %v, want [Bearer test-token]", seen)
	}

	usernames := func() map[string]bool {
		rows, err := db.Query(`SELECT username FROM principals pr
			JOIN tenants t ON t.id = pr.tenant_id WHERE t.org_id='100001'`)
		if err != nil {
			t.Fatalf("list principals: %v", err)
		}
		defer rows.Close()
		out := map[string]bool{}
		for rows.Next() {
			var u string
			if err := rows.Scan(&u); err != nil {
				t.Fatal(err)
			}
			out[strings.ToLower(u)] = true
		}
		return out
	}()
	if !usernames["alice"] || !usernames["bob"] {
		t.Errorf("principals = %v, want alice and bob present", usernames)
	}
	if usernames["stale"] {
		t.Errorf("principals = %v, want stale pruned", usernames)
	}

	var bobInGroup int
	if err := db.QueryRow(`SELECT COUNT(*) FROM principals pr
		JOIN group_principals gp ON gp.principal_id = pr.id
		JOIN groups g ON g.id = gp.group_id
		JOIN tenants t ON t.id = pr.tenant_id AND t.id = g.tenant_id
		WHERE t.org_id='100001' AND pr.username='bob' AND g.name='CI Test Admin'`).Scan(&bobInGroup); err != nil {
		t.Fatalf("bob membership: %v", err)
	}
	if bobInGroup != 1 {
		t.Errorf("bob in group count = %d, want 1", bobInGroup)
	}
}

// kcStubUser is one full Keycloak user representation as the sync sees it.
type kcStubUser struct {
	ID        string
	Username  string
	Email     string
	FirstName string
	LastName  string
	Enabled   bool
}

// profileStub emulates the profile calls: the realm user list, the org-admin
// realm role members, and (for tests that use it) group discovery.
func profileStub(t *testing.T, users []kcStubUser, admins []string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/realms/onprem/protocol/openid-connect/token", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "test-token"})
	})
	mux.HandleFunc("/admin/realms/onprem/users", func(w http.ResponseWriter, r *http.Request) {
		out := make([]map[string]any, 0, len(users))
		for _, u := range users {
			out = append(out, map[string]any{
				"id": u.ID, "username": u.Username, "email": u.Email,
				"firstName": u.FirstName, "lastName": u.LastName, "enabled": u.Enabled,
			})
		}
		_ = json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("/admin/realms/onprem/roles/org-admin/users", func(w http.ResponseWriter, r *http.Request) {
		out := make([]map[string]any, 0, len(admins))
		for _, name := range admins {
			out = append(out, map[string]any{"username": name})
		}
		_ = json.NewEncoder(w).Encode(out)
	})
	return httptest.NewServer(mux)
}

// principalRow is the subset of the principals row the profile sync owns.
type principalRow struct {
	Email      string
	FirstName  string
	LastName   string
	UserID     string
	IsOrgAdmin bool
	IsActive   bool
}

func readPrincipal(t *testing.T, db *store.DB, username string) (principalRow, bool) {
	t.Helper()
	var row principalRow
	var admin, active int
	err := db.QueryRow(`SELECT COALESCE(email,''), COALESCE(first_name,''), COALESCE(last_name,''),
		COALESCE(user_id,''), is_org_admin, is_active
		FROM principals pr JOIN tenants t ON t.id = pr.tenant_id
		WHERE t.org_id = '100001' AND pr.username = ?`, username).
		Scan(&row.Email, &row.FirstName, &row.LastName, &row.UserID, &admin, &active)
	if err != nil {
		return row, false
	}
	row.IsOrgAdmin = admin == 1
	row.IsActive = active == 1
	return row, true
}

// TestSyncPersistsProfilesAndKeepsDisabled: the sync is the only place that
// may read Keycloak, so profiles are persisted there. Disabled users are kept
// (they can be re-enabled and must not lose their group bindings), and
// org-admin comes from the realm role the sidecar used to read.
func TestSyncPersistsProfilesAndKeepsDisabled(t *testing.T) {
	db := testdb.Open(t)
	if err := seed.Seed(db); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	if err := seed.Bootstrap(db, "100001", "1111111", []string{"alice", "bob", "carol"}); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}

	users := []kcStubUser{
		{ID: "kc-alice", Username: "Alice", Email: "alice@example.com", FirstName: "Alice", LastName: "Admin", Enabled: true},
		{ID: "kc-bob", Username: "bob", Email: "bob@example.com", FirstName: "Bob", LastName: "User", Enabled: false},
		{ID: "kc-sa", Username: "service-account-rbac", Enabled: true},
	}
	srv := profileStub(t, users, []string{"alice"})
	defer srv.Close()

	cfg := Config{
		BaseURL: srv.URL, Realm: "onprem",
		ClientID: "rbac-keycloak-sync", ClientSecret: "s3cret",
		OrgID: "100001", Group: "CI Test Admin", PruneOrphans: true,
	}
	if err := Sync(db, cfg); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	alice, ok := readPrincipal(t, db, "alice")
	if !ok {
		t.Fatal("alice missing after sync")
	}
	if alice.Email != "alice@example.com" || alice.FirstName != "Alice" || alice.LastName != "Admin" {
		t.Errorf("alice profile = %+v", alice)
	}
	if alice.UserID != "kc-alice" {
		t.Errorf("alice user_id = %q, want kc-alice", alice.UserID)
	}
	if !alice.IsOrgAdmin || !alice.IsActive {
		t.Errorf("alice flags = org_admin:%v active:%v, want both true", alice.IsOrgAdmin, alice.IsActive)
	}

	bob, ok := readPrincipal(t, db, "bob")
	if !ok {
		t.Fatal("disabled bob must not be pruned")
	}
	if bob.IsActive {
		t.Error("bob is_active = true, want false")
	}
	if bob.IsOrgAdmin {
		t.Error("bob is_org_admin = true, want false")
	}

	if _, ok := readPrincipal(t, db, "service-account-rbac"); ok {
		t.Error("service-account user was synced as a principal")
	}
	// carol is neither in Keycloak nor in the org group: prune removes her.
	if _, ok := readPrincipal(t, db, "carol"); ok {
		t.Error("carol was not pruned")
	}

	// Keycloak stays the source of truth: a re-enabled user becomes active
	// again, changed profiles are refreshed, and losing the realm role drops
	// org-admin.
	users[1].Enabled = true
	users[1].Email = "bob+new@example.com"
	users[0].Email = ""
	noAdmins := profileStub(t, users, nil)
	defer noAdmins.Close()
	cfg.BaseURL = noAdmins.URL
	if err := Sync(db, cfg); err != nil {
		t.Fatalf("second Sync: %v", err)
	}
	bob, _ = readPrincipal(t, db, "bob")
	if !bob.IsActive {
		t.Error("bob is_active = false after re-enable, want true")
	}
	if bob.Email != "bob+new@example.com" {
		t.Errorf("bob email = %q, want the refreshed value", bob.Email)
	}
	alice, _ = readPrincipal(t, db, "alice")
	if alice.IsOrgAdmin {
		t.Error("alice is_org_admin = true after role removal, want false")
	}
	if alice.Email != "" {
		t.Errorf("alice email = %q, want cleared when Keycloak has none", alice.Email)
	}
}

// TestSyncRoleMissingMeansNoAdmins: a realm without the org-admin role must
// sync profiles rather than fail.
func TestSyncRoleMissingMeansNoAdmins(t *testing.T) {
	db := testdb.Open(t)
	if err := seed.Seed(db); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/realms/onprem/protocol/openid-connect/token", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "test-token"})
	})
	mux.HandleFunc("/admin/realms/onprem/users", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{{"username": "alice", "enabled": true}})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	if err := Sync(db, Config{
		BaseURL: srv.URL, Realm: "onprem",
		ClientID: "id", ClientSecret: "secret", OrgID: "100001",
	}); err != nil {
		t.Fatalf("Sync without org-admin role: %v", err)
	}
	alice, ok := readPrincipal(t, db, "alice")
	if !ok || alice.IsOrgAdmin {
		t.Errorf("alice = %+v, present=%v, want present and not org admin", alice, ok)
	}

	// A sync client without role-read permission must still sync users.
	denied := http.NewServeMux()
	denied.HandleFunc("/realms/onprem/protocol/openid-connect/token", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "test-token"})
	})
	denied.HandleFunc("/admin/realms/onprem/users", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{{"username": "alice", "enabled": true}})
	})
	denied.HandleFunc("/admin/realms/onprem/roles/org-admin/users", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})
	deniedSrv := httptest.NewServer(denied)
	defer deniedSrv.Close()
	if err := Sync(db, Config{
		BaseURL: deniedSrv.URL, Realm: "onprem",
		ClientID: "id", ClientSecret: "secret", OrgID: "100001",
	}); err != nil {
		t.Fatalf("Sync with forbidden role lookup: %v", err)
	}
	alice, ok = readPrincipal(t, db, "alice")
	if !ok || alice.IsOrgAdmin {
		t.Errorf("alice after 403 = %+v, present=%v, want present and not org admin", alice, ok)
	}
}

// stubKeycloakGroups emulates group discovery: one org- group with attributes,
// its members endpoint and the org-admin realm role (mirrors
// sync_keycloak_principals.py plus the sidecar's role lookup).
func stubKeycloakGroups(t *testing.T, members []kcStubUser, admins []string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/realms/onprem/protocol/openid-connect/token", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "test-token"})
	})
	mux.HandleFunc("/admin/realms/onprem/groups", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"id":   "g1",
			"name": "org-100001",
			"attributes": map[string]any{
				"org_id":         []string{"100001"},
				"account_number": []string{"1111111"},
			},
		}})
	})
	mux.HandleFunc("/admin/realms/onprem/groups/g1/members", func(w http.ResponseWriter, r *http.Request) {
		out := make([]map[string]any, 0, len(members))
		for _, u := range members {
			out = append(out, map[string]any{
				"id": u.ID, "username": u.Username, "email": u.Email,
				"firstName": u.FirstName, "lastName": u.LastName, "enabled": u.Enabled,
			})
		}
		_ = json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("/admin/realms/onprem/roles/org-admin/users", func(w http.ResponseWriter, r *http.Request) {
		out := make([]map[string]any, 0, len(admins))
		for _, name := range admins {
			out = append(out, map[string]any{"username": name})
		}
		_ = json.NewEncoder(w).Encode(out)
	})
	return httptest.NewServer(mux)
}

// TestSyncDiscoversOrgs: without --org, sync discovers org- groups, creates
// the tenant, and syncs principals with no group assignment (platform/admin
// defaults grant access, as in the Django script). The discovery path must
// persist the same profile as the single-org path.
func TestSyncDiscoversOrgs(t *testing.T) {
	db := testdb.Open(t)
	if err := seed.Seed(db); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	members := []kcStubUser{
		{ID: "kc-alice", Username: "alice", Email: "alice@example.com",
			FirstName: "Alice", LastName: "Admin", Enabled: true},
		{ID: "kc-bob", Username: "bob", Enabled: false},
		{ID: "kc-sa", Username: "service-account-sync", Enabled: true},
	}
	srv := stubKeycloakGroups(t, members, []string{"alice"})
	defer srv.Close()

	err := Sync(db, Config{
		BaseURL: srv.URL, Realm: "onprem",
		ClientID: "rbac-keycloak-sync", ClientSecret: "s3cret",
		OrgGroupPrefix: "org-", PruneOrphans: true,
	})
	if err != nil {
		t.Fatalf("discovery Sync returned error: %v", err)
	}
	var tenant string
	var ready int
	if err := db.QueryRow(`SELECT tenant_name, ready FROM tenants WHERE org_id='100001'`).Scan(&tenant, &ready); err != nil {
		t.Fatalf("tenant missing: %v", err)
	}

	alice, ok := readPrincipal(t, db, "alice")
	if !ok {
		t.Fatal("alice missing after discovery sync")
	}
	if alice.Email != "alice@example.com" || alice.FirstName != "Alice" || alice.LastName != "Admin" {
		t.Errorf("alice profile = %+v", alice)
	}
	if alice.UserID != "kc-alice" || !alice.IsOrgAdmin || !alice.IsActive {
		t.Errorf("alice identity/flags = %+v, want kc-alice, org admin and active", alice)
	}
	bob, ok := readPrincipal(t, db, "bob")
	if !ok || bob.IsActive {
		t.Errorf("bob = %+v, present=%v, want present and inactive", bob, ok)
	}
	if _, ok := readPrincipal(t, db, "service-account-sync"); ok {
		t.Error("service-account member was synced as a principal")
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM principals pr
		JOIN tenants t ON t.id = pr.tenant_id WHERE t.org_id='100001'`).Scan(&count); err != nil || count != 2 {
		t.Errorf("principal count = %d, err = %v, want alice and bob", count, err)
	}
}
