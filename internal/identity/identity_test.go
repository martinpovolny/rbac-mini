package identity

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

// Mint builds an x-rh-identity header value the same way
// tests/identity_request.py::_build_identity does.
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

func TestParseOrgAdmin(t *testing.T) {
	h := mint(t, "100001", "Admin_User", true)
	got, err := Parse(h)
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if !got.Admin {
		t.Errorf("Admin = false, want true (koku bypass path)")
	}
	if got.Username != "admin_user" {
		t.Errorf("Username = %q, want %q", got.Username, "admin_user")
	}
}

// mintSA builds a service-account x-rh-identity the same way
// tests/identity_request.py::_build_identity does with service_account_data.
func mintSA(t *testing.T, orgID, clientID string) string {
	t.Helper()
	username := "service-account-" + clientID
	id := map[string]any{
		"identity": map[string]any{
			"account_number": "1111111",
			"org_id":         orgID,
			"type":           "ServiceAccount",
			"service_account": map[string]any{
				"client_id": clientID,
				"username":  username,
			},
		},
	}
	raw, err := json.Marshal(id)
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(raw)
}

func TestParseServiceAccount(t *testing.T) {
	h := mintSA(t, "100001", "abc-123")
	got, err := Parse(h)
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if !got.IsServiceAcct {
		t.Errorf("IsServiceAcct = false, want true")
	}
	if got.ClientID != "abc-123" {
		t.Errorf("ClientID = %q, want %q", got.ClientID, "abc-123")
	}
	if got.Username != "service-account-abc-123" {
		t.Errorf("Username = %q, want %q", got.Username, "service-account-abc-123")
	}
	if got.Admin {
		t.Errorf("Admin = true, want false (service accounts are never org admins)")
	}
}

func TestParseInvalid(t *testing.T) {
	for name, header := range map[string]string{
		"not base64":       "!!!",
		"not json":         base64.StdEncoding.EncodeToString([]byte("hello")),
		"missing org":      mint(t, "", "someone", false),
		"missing username": mint(t, "100001", "", false),
	} {
		if _, err := Parse(header); err == nil {
			t.Errorf("%s: Parse succeeded, want error (handler maps to 401)", name)
		}
	}
}

func TestParseRegularUser(t *testing.T) {
	h := mint(t, "100001", "Test_User", false)
	got, err := Parse(h)
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if got.OrgID != "100001" {
		t.Errorf("OrgID = %q, want %q", got.OrgID, "100001")
	}
	if got.Username != strings.ToLower("Test_User") {
		t.Errorf("Username = %q, want lowercased %q", got.Username, "test_user")
	}
	if got.Admin {
		t.Errorf("Admin = true, want false")
	}
	if got.IsServiceAcct {
		t.Errorf("IsServiceAcct = true, want false")
	}
}
