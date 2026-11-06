package server

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"rbac-go/internal/store"
)

// mint builds an x-rh-identity header value for username in orgID.
func mint(t *testing.T, orgID, username string, isOrgAdmin bool) string {
	t.Helper()
	id := map[string]any{
		"identity": map[string]any{
			"account_number": "1111111",
			"org_id":         orgID,
			"type":           "User",
			"user": map[string]any{
				"username":     username,
				"email":        username + "@example.com",
				"is_org_admin": isOrgAdmin,
				"user_id":      "1111111",
			},
		},
	}
	raw, err := json.Marshal(id)
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(raw)
}

// seedAccess wires test_user in groupA with roleA granting app:*:* plus one
// equal resourceDefinition (mirrors access_data in test_view.py).
func seedAccess(t *testing.T, db *sql.DB) {
	t.Helper()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	exec(`INSERT INTO tenants(org_id, tenant_name, account_id, ready) VALUES('100001','acct1111111','1111111',1)`)
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

func testServerSeeded(t *testing.T) (*Server, string) {
	t.Helper()
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	seedAccess(t, db)
	return New(db), mint(t, "100001", "test_user", false)
}

// TestAccessRequiresIdentity: no x-rh-identity header maps to 401.
func TestAccessRequiresIdentity(t *testing.T) {
	s, _ := testServerSeeded(t)
	req := httptest.NewRequest(http.MethodGet, "/api/rbac/v1/access/?application=app", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

// TestAccessSuccess: identity → access.Query → meta/links/data shape per
// doc/openapi.v1-subset.json (AccessPagination).
func TestAccessSuccess(t *testing.T) {
	s, header := testServerSeeded(t)
	req := httptest.NewRequest(http.MethodGet, "/api/rbac/v1/access/?application=app&limit=100", nil)
	req.Header.Set("X-Rh-Identity", header)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var body struct {
		Meta struct {
			Count int `json:"count"`
		} `json:"meta"`
		Links map[string]string `json:"links"`
		Data  []struct {
			Permission          string `json:"permission"`
			ResourceDefinitions []struct {
				AttributeFilter map[string]any `json:"attributeFilter"`
			} `json:"resourceDefinitions"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Meta.Count != 1 || len(body.Data) != 1 {
		t.Fatalf("count = %d, len(data) = %d, want 1", body.Meta.Count, len(body.Data))
	}
	if body.Data[0].Permission != "app:*:*" {
		t.Errorf("permission = %q, want %q", body.Data[0].Permission, "app:*:*")
	}
	af := body.Data[0].ResourceDefinitions[0].AttributeFilter
	if af["key"] != "key1.id" || af["operation"] != "equal" || af["value"] != "value1" {
		t.Errorf("attributeFilter = %v, want key1.id/equal/value1", af)
	}
	first, ok := body.Links["first"]
	if !ok || first == "" {
		t.Fatalf("links.first missing: %v", body.Links)
	}
}

// TestAccessPaginationWalk mirrors koku/koku/rbac.py::_request_user_access:
// limit the page, follow links.next until exhausted, collect everything.
func TestAccessPaginationWalk(t *testing.T) {
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	seedAccess(t, db)
	for _, perm := range []string{"app:b:read", "app:c:read"} {
		parts := map[string]string{"app:b:read": "b", "app:c:read": "c"}
		res := parts[perm]
		if _, err := db.Exec(`INSERT INTO permissions(application, resource_type, verb, permission, tenant_id)
			VALUES('app',?, 'read',?,(SELECT id FROM tenants WHERE org_id='100001'))`, res, perm); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO access(role_id, permission_id, tenant_id) VALUES(
			(SELECT id FROM roles WHERE uuid='r-a'),
			(SELECT id FROM permissions WHERE permission=?),
			(SELECT id FROM tenants WHERE org_id='100001'))`, perm); err != nil {
			t.Fatal(err)
		}
	}
	s := New(db)
	header := mint(t, "100001", "test_user", false)

	// Koku's walk: GET page, append data, follow links.next as path.
	var collected []string
	next := "/api/rbac/v1/access/?application=app&limit=2"
	for next != "" {
		req := httptest.NewRequest(http.MethodGet, next, nil)
		req.Header.Set("X-Rh-Identity", header)
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: status = %d, want 200", next, rec.Code)
		}
		var body struct {
			Meta  struct{ Count int } `json:"meta"`
			Links map[string]*string  `json:"links"`
			Data  []struct {
				Permission string `json:"permission"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if body.Meta.Count != 3 {
			t.Fatalf("meta.count = %d, want 3", body.Meta.Count)
		}
		for _, d := range body.Data {
			collected = append(collected, d.Permission)
		}
		next = ""
		if body.Links["next"] != nil {
			next = *body.Links["next"]
		}
	}
	want := []string{"app:*:*", "app:b:read", "app:c:read"}
	if len(collected) != len(want) {
		t.Fatalf("walked %d permissions, want %d: %v", len(collected), len(want), collected)
	}
	for i := range want {
		if collected[i] != want[i] {
			t.Errorf("collected[%d] = %q, want %q (full: %v)", i, collected[i], want[i], collected)
		}
	}
}

func testServer(t *testing.T) *Server {
	t.Helper()
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return New(db)
}

// TestStatus mirrors the operator probe (GET /api/rbac/v1/status/) and the
// openapi Status schema: api_version is required.
func TestStatus(t *testing.T) {
	s := testServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/rbac/v1/status/", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["api_version"] != float64(1) {
		t.Errorf("api_version = %v, want 1", body["api_version"])
	}
}

// TestReady: 200 when the DB pings, 503 when it does not (readiness gate
// for the operator rollout, cf. ConditionRBACReady).
func TestReady(t *testing.T) {
	s := testServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/rbac/v1/ready/", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("healthy: status = %d, want 200", rec.Code)
	}

	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.Close() // unreachable DB
	sick := New(db)
	req = httptest.NewRequest(http.MethodGet, "/api/rbac/v1/ready/", nil)
	rec = httptest.NewRecorder()
	sick.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("unhealthy: status = %d, want 503", rec.Code)
	}
}
