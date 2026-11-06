package server

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"rbac-go/internal/seed"
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

// TestCreateRole mirrors the wizard Step 4: POST a role with access list →
// 201 with uuid; the grant shows up in access.
func TestCreateRole(t *testing.T) {
	s, header := testServerSeeded(t)
	t.Setenv("ROLE_CREATE_ALLOW_LIST", "cost-management")
	body := `{"name":"roleWizard","access":[{"permission":"cost-management:aws.account:read","resourceDefinitions":[]}]}`
	req := httptest.NewRequest(http.MethodPost, "/api/rbac/v1/roles/", strings.NewReader(body))
	req.Header.Set("X-Rh-Identity", header)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body: %s)", rec.Code, rec.Body.String())
	}
	var created struct {
		UUID string `json:"uuid"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if created.UUID == "" || created.Name != "roleWizard" {
		t.Errorf("created = %+v, want uuid + name roleWizard", created)
	}

	// The grant is visible on detail.
	req = httptest.NewRequest(http.MethodGet, "/api/rbac/v1/roles/"+created.UUID+"/", nil)
	req.Header.Set("X-Rh-Identity", header)
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("detail status = %d, want 200", rec.Code)
	}
}

// TestCreateRoleValidation mirrors validate_role: unknown allow-list app and
// unknown permission are 400.
func TestCreateRoleValidation(t *testing.T) {
	s, header := testServerSeeded(t)
	t.Setenv("ROLE_CREATE_ALLOW_LIST", "cost-management")
	for name, pw := range map[string][2]string{
		"foreign app":  {"app:foo:bar", "Custom roles cannot be created for app"},
		"unknown perm": {"cost-management:nope:nope", "Permission does not exist"},
	} {
		body := `{"name":"roleX","access":[{"permission":"` + pw[0] + `","resourceDefinitions":[]}]}`
		req := httptest.NewRequest(http.MethodPost, "/api/rbac/v1/roles/", strings.NewReader(body))
		req.Header.Set("X-Rh-Identity", header)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", name, rec.Code)
			continue
		}
		if !strings.Contains(rec.Body.String(), pw[1]) {
			t.Errorf("%s: body %s, want %q", name, rec.Body.String(), pw[1])
		}
	}
}

// TestRolesList: GET /roles/ returns the tenant's roles paginated.
func TestRolesList(t *testing.T) {
	s, header := testServerSeeded(t)
	req := httptest.NewRequest(http.MethodGet, "/api/rbac/v1/roles/?limit=20", nil)
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
		Data []struct {
			UUID string `json:"uuid"`
			Name string `json:"name"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Meta.Count < 1 || len(body.Data) < 1 {
		t.Fatalf("count = %d, len(data) = %d, want >= 1 (fixture roleA)", body.Meta.Count, len(body.Data))
	}
	found := false
	for _, r := range body.Data {
		if r.Name == "roleA" && r.UUID != "" {
			found = true
		}
	}
	if !found {
		t.Errorf("data = %+v, want fixture roleA with uuid", body.Data)
	}
}

// createRole posts a role and returns its uuid.
func createRole(t *testing.T, s *Server, header, name, perm string) string {
	t.Helper()
	t.Setenv("ROLE_CREATE_ALLOW_LIST", "cost-management")
	body := `{"name":"` + name + `","access":[{"permission":"` + perm + `","resourceDefinitions":[]}]}`
	req := httptest.NewRequest(http.MethodPost, "/api/rbac/v1/roles/", strings.NewReader(body))
	req.Header.Set("X-Rh-Identity", header)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("setup POST role: status = %d (body: %s)", rec.Code, rec.Body.String())
	}
	var created struct {
		UUID string `json:"uuid"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return created.UUID
}

// TestUpdateRole: PUT replaces name + access grants.
func TestUpdateRole(t *testing.T) {
	s, header := testServerSeeded(t)
	t.Setenv("ROLE_CREATE_ALLOW_LIST", "cost-management")
	uuid := createRole(t, s, header, "roleUp", "cost-management:aws.account:read")

	body := `{"name":"roleUpRenamed","access":[{"permission":"cost-management:aws.account:read","resourceDefinitions":[{"attributeFilter":{"key":"k","operation":"equal","value":"v"}}]}]}`
	req := httptest.NewRequest(http.MethodPut, "/api/rbac/v1/roles/"+uuid+"/", strings.NewReader(body))
	req.Header.Set("X-Rh-Identity", header)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/rbac/v1/roles/"+uuid+"/", nil)
	req.Header.Set("X-Rh-Identity", header)
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	var detail struct {
		Name   string `json:"name"`
		Access []struct {
			ResourceDefinitions []struct {
				AttributeFilter map[string]any `json:"attributeFilter"`
			} `json:"resourceDefinitions"`
		} `json:"access"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Name != "roleUpRenamed" {
		t.Errorf("name = %q, want roleUpRenamed", detail.Name)
	}
	if len(detail.Access) != 1 || len(detail.Access[0].ResourceDefinitions) != 1 {
		t.Fatalf("access = %+v, want 1 grant with 1 resourceDefinition", detail.Access)
	}
}

// TestPatchRole: PATCH allows name/display_name/description only.
func TestPatchRole(t *testing.T) {
	s, header := testServerSeeded(t)
	uuid := createRole(t, s, header, "rolePatch", "cost-management:aws.account:read")

	req := httptest.NewRequest(http.MethodPatch, "/api/rbac/v1/roles/"+uuid+"/",
		strings.NewReader(`{"description":"patched"}`))
	req.Header.Set("X-Rh-Identity", header)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPatch, "/api/rbac/v1/roles/"+uuid+"/",
		strings.NewReader(`{"access":[]}`))
	req.Header.Set("X-Rh-Identity", header)
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("patch access: status = %d, want 400", rec.Code)
	}
}

// TestDeleteRole: DELETE removes a custom role (204, then 404); system
// roles (seeded Cost Administrator) are protected.
func TestDeleteRole(t *testing.T) {
	db := testdb.Open(t)
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	seedAccess(t, db)
	if err := seed.Seed(db); err != nil {
		t.Fatalf("seed: %v", err)
	}
	s := New(db)
	header := mint(t, "100001", "test_user", false)
	uuid := createRole(t, s, header, "roleDel", "cost-management:aws.account:read")

	req := httptest.NewRequest(http.MethodDelete, "/api/rbac/v1/roles/"+uuid+"/", nil)
	req.Header.Set("X-Rh-Identity", header)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/rbac/v1/roles/"+uuid+"/", nil)
	req.Header.Set("X-Rh-Identity", header)
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("detail after delete: status = %d, want 404", rec.Code)
	}

	// System roles are protected: find Cost Administrator via list.
	req = httptest.NewRequest(http.MethodGet, "/api/rbac/v1/roles/?name=Cost+Administrator", nil)
	req.Header.Set("X-Rh-Identity", header)
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	var list struct {
		Data []struct {
			UUID string `json:"uuid"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Data) != 1 {
		t.Fatalf("admin lookup: %v %+v", err, list)
	}
	adminUUID := list.Data[0].UUID
	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		var rreq *http.Request
		if method == http.MethodPut {
			rreq = httptest.NewRequest(method, "/api/rbac/v1/roles/"+adminUUID+"/",
				strings.NewReader(`{"name":"hacked"}`))
			rreq.Header.Set("Content-Type", "application/json")
		} else {
			rreq = httptest.NewRequest(method, "/api/rbac/v1/roles/"+adminUUID+"/", nil)
		}
		rreq.Header.Set("X-Rh-Identity", header)
		rrec := httptest.NewRecorder()
		s.ServeHTTP(rrec, rreq)
		if rrec.Code != http.StatusBadRequest {
			t.Errorf("system role %s: status = %d, want 400", method, rrec.Code)
		}
	}
}

// TestGroupsList: Users/Groups page. Default/system groups are excluded like
// Django's _filter_default_groups; counts are annotated per group.
func TestGroupsList(t *testing.T) {
	s, header := testServerSeeded(t)
	db := s.db
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	exec(`INSERT INTO groups(uuid, name, platform_default, tenant_id) VALUES('g-pd','Default access',1,
		(SELECT id FROM tenants WHERE org_id='100001'))`)
	exec(`INSERT INTO groups(uuid, name, admin_default, tenant_id) VALUES('g-ad','Admin default',1,
		(SELECT id FROM tenants WHERE org_id='100001'))`)

	req := httptest.NewRequest(http.MethodGet, "/api/rbac/v1/groups/?limit=20", nil)
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
		Data []struct {
			UUID           string `json:"uuid"`
			Name           string `json:"name"`
			PrincipalCount int    `json:"principalCount"`
			RoleCount      int    `json:"roleCount"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Meta.Count != 1 || len(body.Data) != 1 {
		t.Fatalf("count = %d, len(data) = %d, want 1 (groupA only)", body.Meta.Count, len(body.Data))
	}
	g := body.Data[0]
	if g.Name != "groupA" || g.PrincipalCount != 1 || g.RoleCount != 1 {
		t.Errorf("group = %+v, want groupA with 1 principal and 1 role", g)
	}
}

// groupUUID resolves a group uuid by name via the list endpoint.
func groupUUID(t *testing.T, s *Server, header, name string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/rbac/v1/groups/?limit=100", nil)
	req.Header.Set("X-Rh-Identity", header)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("groups list: status = %d", rec.Code)
	}
	var body struct {
		Data []struct {
			UUID string `json:"uuid"`
			Name string `json:"name"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, g := range body.Data {
		if g.Name == name {
			return g.UUID
		}
	}
	t.Fatalf("group %q not found in %+v", name, body.Data)
	return ""
}

// TestGroupPrincipals: members tab of a group.
func TestGroupPrincipals(t *testing.T) {
	s, header := testServerSeeded(t)
	uuid := groupUUID(t, s, header, "groupA")

	req := httptest.NewRequest(http.MethodGet, "/api/rbac/v1/groups/"+uuid+"/principals/?limit=20", nil)
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
		Data []struct {
			Username string `json:"username"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Meta.Count != 1 || len(body.Data) != 1 || body.Data[0].Username != "test_user" {
		t.Errorf("members = %+v, want [test_user]", body.Data)
	}
}

// TestGroupRoles: roles tab of a group (roles granted via its policies).
func TestGroupRoles(t *testing.T) {
	s, header := testServerSeeded(t)
	uuid := groupUUID(t, s, header, "groupA")

	req := httptest.NewRequest(http.MethodGet, "/api/rbac/v1/groups/"+uuid+"/roles/?limit=20", nil)
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
		Data []struct {
			UUID string `json:"uuid"`
			Name string `json:"name"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Meta.Count != 1 || len(body.Data) != 1 || body.Data[0].Name != "roleA" {
		t.Errorf("roles = %+v, want [roleA]", body.Data)
	}
}

// TestCreateGroup: POST /groups/ with a name → 201; duplicates and reserved
// names ("Default access") are 400 (validate_group_name).
func TestCreateGroup(t *testing.T) {
	s, header := testServerSeeded(t)
	create := func(name string) (int, []byte) {
		t.Helper()
		body := `{"name":"` + name + `"}`
		req := httptest.NewRequest(http.MethodPost, "/api/rbac/v1/groups/", strings.NewReader(body))
		req.Header.Set("X-Rh-Identity", header)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		return rec.Code, rec.Body.Bytes()
	}
	code, respBody := create("team-custom")
	if code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body: %s)", code, respBody)
	}
	var created struct {
		UUID string `json:"uuid"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(respBody, &created); err != nil || created.UUID == "" || created.Name != "team-custom" {
		t.Fatalf("created = %+v, err = %v; want uuid + team-custom", created, err)
	}
	if code, _ := create("team-custom"); code != http.StatusBadRequest {
		t.Errorf("duplicate: status = %d, want 400", code)
	}
	if code, body := create("Default access"); code != http.StatusBadRequest || !strings.Contains(string(body), "reserved") {
		t.Errorf("reserved name: status = %d body %s, want 400 + reserved", code, body)
	}
}

// createGroup posts a group and returns its uuid.
func createGroup(t *testing.T, s *Server, header, name string) string {
	t.Helper()
	body := `{"name":"` + name + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/rbac/v1/groups/", strings.NewReader(body))
	req.Header.Set("X-Rh-Identity", header)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("setup POST group: status = %d (body: %s)", rec.Code, rec.Body.String())
	}
	var created struct {
		UUID string `json:"uuid"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	return created.UUID
}

// TestUpdateGroup: PUT renames + re-describes a custom group.
func TestUpdateGroup(t *testing.T) {
	s, header := testServerSeeded(t)
	uuid := createGroup(t, s, header, "team-rename")

	req := httptest.NewRequest(http.MethodPut, "/api/rbac/v1/groups/"+uuid+"/",
		strings.NewReader(`{"name":"team-renamed","description":"new desc"}`))
	req.Header.Set("X-Rh-Identity", header)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var updated struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Name != "team-renamed" || updated.Description != "new desc" {
		t.Errorf("updated = %+v, want renamed + new desc", updated)
	}
}

// TestDeleteGroup: custom groups delete (204, then 404); platform_default and
// admin_default groups are protected like Django's protect_special_groups.
func TestDeleteGroup(t *testing.T) {
	s, header := testServerSeeded(t)
	db := s.db
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	exec(`INSERT INTO groups(uuid, name, platform_default, tenant_id) VALUES('g-pd','Default access',1,
		(SELECT id FROM tenants WHERE org_id='100001'))`)
	exec(`INSERT INTO groups(uuid, name, admin_default, tenant_id) VALUES('g-ad','Admin default',1,
		(SELECT id FROM tenants WHERE org_id='100001'))`)
	uuid := createGroup(t, s, header, "team-gone")

	doDelete := func(id string) int {
		t.Helper()
		req := httptest.NewRequest(http.MethodDelete, "/api/rbac/v1/groups/"+id+"/", nil)
		req.Header.Set("X-Rh-Identity", header)
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := doDelete(uuid); code != http.StatusNoContent {
		t.Errorf("custom delete: status = %d, want 204", code)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/rbac/v1/groups/"+uuid+"/", nil)
	req.Header.Set("X-Rh-Identity", header)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("detail after delete: status = %d, want 404", rec.Code)
	}
	for id, kind := range map[string]string{"g-pd": "platform_default", "g-ad": "admin_default"} {
		if code := doDelete(id); code != http.StatusBadRequest {
			t.Errorf("delete %s: status = %d, want 400", kind, code)
		}
		if code := func() int {
			req := httptest.NewRequest(http.MethodPut, "/api/rbac/v1/groups/"+id+"/",
				strings.NewReader(`{"name":"hacked"}`))
			req.Header.Set("X-Rh-Identity", header)
			req.Header.Set("Content-Type", "application/json")
			r := httptest.NewRecorder()
			s.ServeHTTP(r, req)
			return r.Code
		}(); code != http.StatusBadRequest {
			t.Errorf("rename %s: status = %d, want 400", kind, code)
		}
	}
}

// TestGroupMemberManagement: POST adds existing tenant principals, unknown
// usernames are 400, DELETE removes, platform_default groups are protected.
func TestGroupMemberManagement(t *testing.T) {
	s, header := testServerSeeded(t)
	uuid := createGroup(t, s, header, "team-members")
	members := func(id string) ([]string, int) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/rbac/v1/groups/"+id+"/principals/?limit=20", nil)
		req.Header.Set("X-Rh-Identity", header)
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("members: status = %d", rec.Code)
		}
		var body struct {
			Data []struct {
				Username string `json:"username"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		out := make([]string, 0, len(body.Data))
		for _, m := range body.Data {
			out = append(out, m.Username)
		}
		return out, rec.Code
	}
	postMembers := func(id, method, payload string) (int, string) {
		t.Helper()
		req := httptest.NewRequest(method, "/api/rbac/v1/groups/"+id+"/principals/", strings.NewReader(payload))
		req.Header.Set("X-Rh-Identity", header)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}

	if code, body := postMembers(uuid, http.MethodPost, `{"usernames":["test_user"]}`); code != http.StatusOK {
		t.Fatalf("add: status = %d, want 200 (body: %s)", code, body)
	}
	if got, _ := members(uuid); len(got) != 1 || got[0] != "test_user" {
		t.Fatalf("members = %v, want [test_user]", got)
	}
	if code, body := postMembers(uuid, http.MethodPost, `{"usernames":["ghost"]}`); code != http.StatusBadRequest || !strings.Contains(body, "not found") {
		t.Errorf("unknown user: status = %d body %s, want 400 + not found", code, body)
	}
	if code, _ := postMembers(uuid, http.MethodDelete, `{"usernames":["test_user"]}`); code != http.StatusOK {
		t.Errorf("remove: status = %d, want 200", code)
	}
	if got, _ := members(uuid); len(got) != 0 {
		t.Errorf("members after remove = %v, want []", got)
	}
}

// TestGroupRolesAssign: POST grants roles to the group (via its policy),
// DELETE ?roles= removes them; admin_default groups are protected.
func TestGroupRolesAssign(t *testing.T) {
	s, header := testServerSeeded(t)
	groupUUID := groupUUID(t, s, header, "groupA")
	roleUUID := createRole(t, s, header, "roleGrant", "cost-management:aws.account:read")

	assign := func(method, query, payload string) (int, string) {
		t.Helper()
		req := httptest.NewRequest(method, "/api/rbac/v1/groups/"+groupUUID+"/roles/"+query,
			strings.NewReader(payload))
		req.Header.Set("X-Rh-Identity", header)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	roles := func() []string {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/rbac/v1/groups/"+groupUUID+"/roles/?limit=20", nil)
		req.Header.Set("X-Rh-Identity", header)
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("roles: status = %d", rec.Code)
		}
		var body struct {
			Data []struct {
				Name string `json:"name"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		out := make([]string, 0, len(body.Data))
		for _, r := range body.Data {
			out = append(out, r.Name)
		}
		return out
	}

	if code, body := assign(http.MethodPost, "", `{"roles":["`+roleUUID+`"]}`); code != http.StatusOK {
		t.Fatalf("assign: status = %d, want 200 (body: %s)", code, body)
	}
	if got := roles(); len(got) != 2 {
		t.Fatalf("roles = %v, want [roleA roleGrant]", got)
	}
	if code, _ := assign(http.MethodDelete, "?roles="+roleUUID, ""); code != http.StatusNoContent {
		t.Errorf("remove: status = %d, want 204", code)
	}
	if got := roles(); len(got) != 1 || got[0] != "roleA" {
		t.Errorf("roles after remove = %v, want [roleA]", got)
	}
}

// TestRoleAccess: GET /roles/{uuid}/access/ returns the role's grants in the
// access pagination shape.
func TestRoleAccess(t *testing.T) {
	s, header := testServerSeeded(t)
	uuid := groupUUID(t, s, header, "groupA") // any uuid resolver; roles listed below
	_ = uuid
	req := httptest.NewRequest(http.MethodGet, "/api/rbac/v1/roles/", nil)
	req.Header.Set("X-Rh-Identity", header)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	var list struct {
		Data []struct {
			UUID string `json:"uuid"`
			Name string `json:"name"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Data) == 0 {
		t.Fatalf("roles list: %v %+v", err, list)
	}
	var roleUUID string
	for _, r := range list.Data {
		if r.Name == "roleA" {
			roleUUID = r.UUID
		}
	}
	req = httptest.NewRequest(http.MethodGet, "/api/rbac/v1/roles/"+roleUUID+"/access/?limit=20", nil)
	req.Header.Set("X-Rh-Identity", header)
	rec = httptest.NewRecorder()
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
		t.Fatal(err)
	}
	if len(body.Data) != 1 || body.Data[0].Permission != "app:*:*" {
		t.Errorf("data = %+v, want [app:*:*]", body.Data)
	}
}
