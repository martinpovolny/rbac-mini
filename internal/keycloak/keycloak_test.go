package keycloak

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"rbac-go/internal/seed"
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

// stubKeycloakGroups emulates group discovery: one org- group with attributes
// plus its members endpoint (mirrors sync_keycloak_principals.py).
func stubKeycloakGroups(t *testing.T, members []string) *httptest.Server {
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
			out = append(out, map[string]any{"username": u, "enabled": true})
		}
		_ = json.NewEncoder(w).Encode(out)
	})
	return httptest.NewServer(mux)
}

// TestSyncDiscoversOrgs: without --org, sync discovers org- groups, creates
// the tenant, and syncs principals with no group assignment (platform/admin
// defaults grant access, as in the Django script).
func TestSyncDiscoversOrgs(t *testing.T) {
	db := testdb.Open(t)
	if err := seed.Seed(db); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	srv := stubKeycloakGroups(t, []string{"alice", "bob"})
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
	for _, u := range []string{"alice", "bob"} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM principals pr
			JOIN tenants t ON t.id = pr.tenant_id
			WHERE t.org_id='100001' AND pr.username=?`, u).Scan(&n); err != nil || n != 1 {
			t.Errorf("principal %q count = %d, err = %v, want 1", u, n, err)
		}
	}
}
