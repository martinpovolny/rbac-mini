package server

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"rbac-go/internal/store"
	"rbac-go/internal/testdb"
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
func seedAccess(t *testing.T, db *store.DB) {
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
	seedPermissions(t, db)
}

func testServerSeeded(t *testing.T) (*Server, string) {
	t.Helper()
	db := testdb.Open(t)
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
	db := testdb.Open(t)
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
	return New(testdb.Open(t))
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

// TestMetrics: the operator ServiceMonitor scrapes GET /metrics/;
// it must serve Prometheus exposition including the per-endpoint counter.
func TestMetrics(t *testing.T) {
	s := testServer(t)
	// Generate traffic so the counter has observations.
	req := httptest.NewRequest(http.MethodGet, "/api/rbac/v1/status/", nil)
	s.ServeHTTP(httptest.NewRecorder(), req)

	req = httptest.NewRequest(http.MethodGet, "/metrics/", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"rbac_api_requests_total", `endpoint="status"`} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics body missing %q", want)
		}
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

	db := testdb.Open(t)
	db.Close() // unreachable DB
	sick := New(db)
	req = httptest.NewRequest(http.MethodGet, "/api/rbac/v1/ready/", nil)
	rec = httptest.NewRecorder()
	sick.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("unhealthy: status = %d, want 503", rec.Code)
	}
}

// TestAccessInvalidParams mirrors test_get_access_with_invalid_limit /
// invalid_ordering_value: malformed query params are 400 with the v1 errors
// envelope, not 500.
func TestAccessInvalidParams(t *testing.T) {
	s, header := testServerSeeded(t)
	for name, query := range map[string]string{
		"bad limit":  "application=app&limit=abc",
		"zero limit": "application=app&limit=0",
		"bad order":  "application=app&order_by=bogus",
		"bad status": "application=app&status=bogus",
		"bad offset": "application=app&offset=-1",
	} {
		req := httptest.NewRequest(http.MethodGet, "/api/rbac/v1/access/?"+query, nil)
		req.Header.Set("X-Rh-Identity", header)
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", name, rec.Code)
			continue
		}
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Errorf("%s: body is not JSON: %v", name, err)
			continue
		}
		if _, ok := body["errors"]; !ok {
			t.Errorf("%s: body has no errors envelope: %s", name, rec.Body.String())
		}
	}

	// Sanity: a valid request still passes.
	req := httptest.NewRequest(http.MethodGet, "/api/rbac/v1/access/?application=app&limit=10", nil)
	req.Header.Set("X-Rh-Identity", header)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("valid: status = %d, want 200", rec.Code)
	}
}

// TestPrincipalsList: Users page. Seeded tenant principals come back
// paginated (meta{count,limit,offset}) with on-prem placeholder attrs,
// mirroring BYPASS_BOP_VERIFICATION in management/principal/proxy.py.
func TestPrincipalsList(t *testing.T) {
	s, header := testServerSeeded(t)
	req := httptest.NewRequest(http.MethodGet, "/api/rbac/v1/principals/?limit=20", nil)
	req.Header.Set("X-Rh-Identity", header)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var body struct {
		Meta struct {
			Count  int `json:"count"`
			Limit  int `json:"limit"`
			Offset int `json:"offset"`
		} `json:"meta"`
		Data []struct {
			Username string `json:"username"`
			Email    string `json:"email"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Meta.Count != 1 || len(body.Data) != 1 {
		t.Fatalf("count = %d, len(data) = %d, want 1 (test_user)", body.Meta.Count, len(body.Data))
	}
	if body.Data[0].Username != "test_user" || body.Data[0].Email == "" {
		t.Errorf("data[0] = %+v, want test_user with email", body.Data[0])
	}
}

// seedPermissions adds cost-management permissions (one scoped, one global)
// next to the fixture's app:*:*.
func seedPermissions(t *testing.T, db interface {
	Exec(query string, args ...any) (sql.Result, error)
}) {
	t.Helper()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	exec(`INSERT INTO permissions(application, resource_type, verb, permission, tenant_id)
		VALUES('cost-management','aws.account','read','cost-management:aws.account:read',
		(SELECT id FROM tenants WHERE org_id='100001'))`)
	exec(`INSERT INTO permissions(application, resource_type, verb, permission, tenant_id)
		VALUES('cost-management','*','*','cost-management:*:*',
		(SELECT id FROM tenants WHERE org_id='100001'))`)
}

// TestPermissionsList mirrors the wizard Step 2 query: allowed_only restricts
// to ROLE_CREATE_ALLOW_LIST, exclude_globals drops *-permissions.
func TestPermissionsList(t *testing.T) {
	s, header := testServerSeeded(t)
	t.Setenv("ROLE_CREATE_ALLOW_LIST", "cost-management")
	req := httptest.NewRequest(
		http.MethodGet, "/api/rbac/v1/permissions/?allowed_only=true&exclude_globals=true&limit=20", nil)
	req.Header.Set("X-Rh-Identity", header)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var body struct {
		Data []struct {
			Permission string `json:"permission"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Data) != 1 || body.Data[0].Permission != "cost-management:aws.account:read" {
		t.Errorf("data = %+v, want [cost-management:aws.account:read]", body.Data)
	}
}

// TestPermissionsOptions mirrors the application dropdown query.
func TestPermissionsOptions(t *testing.T) {
	s, header := testServerSeeded(t)
	t.Setenv("ROLE_CREATE_ALLOW_LIST", "cost-management")
	req := httptest.NewRequest(
		http.MethodGet, "/api/rbac/v1/permissions/options/?field=application&allowed_only=true", nil)
	req.Header.Set("X-Rh-Identity", header)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var body struct {
		Data []string `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Data) != 1 || body.Data[0] != "cost-management" {
		t.Errorf("data = %v, want [cost-management]", body.Data)
	}
}
