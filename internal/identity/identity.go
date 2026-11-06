package identity

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
)

// User is the request identity derived from x-rh-identity.
type User struct {
	Username        string
	OrgID           string
	Admin           bool
	IsServiceAcct   bool
	ClientID        string
}

type rhIdentity struct {
	OrgID    string `json:"org_id"`
	Identity struct {
		OrgID          string `json:"org_id"`
		AccountNumber  string `json:"account_number"`
		Type           string `json:"type"`
		User           *struct {
			Username  string `json:"username"`
			IsOrgAdmin bool   `json:"is_org_admin"`
		} `json:"user"`
		ServiceAccount *struct {
			ClientID string `json:"client_id"`
			Username string `json:"username"`
		} `json:"service_account"`
	} `json:"identity"`
}

// Parse decodes an x-rh-identity header value (base64 JSON) into a User.
// Mirrors tests/identity_request.py::_build_identity and SPEC §4.
func Parse(header string) (User, error) {
	raw, err := base64.StdEncoding.DecodeString(header)
	if err != nil {
		return User{}, errors.New("invalid x-rh-identity encoding")
	}
	var id rhIdentity
	if err := json.Unmarshal(raw, &id); err != nil {
		return User{}, errors.New("invalid x-rh-identity payload")
	}
	orgID := id.Identity.OrgID
	if orgID == "" {
		orgID = id.OrgID
	}
	if orgID == "" {
		return User{}, errors.New("missing org_id in x-rh-identity")
	}
	if id.Identity.ServiceAccount != nil && (id.Identity.Type == "ServiceAccount" || id.Identity.User == nil) {
		username := strings.ToLower(id.Identity.ServiceAccount.Username)
		if username == "" {
			username = "service-account-" + id.Identity.ServiceAccount.ClientID
		}
		return User{
			Username:      username,
			OrgID:         orgID,
			IsServiceAcct: true,
			ClientID:      id.Identity.ServiceAccount.ClientID,
		}, nil
	}
	if id.Identity.User == nil || id.Identity.User.Username == "" {
		return User{}, errors.New("missing user in x-rh-identity")
	}
	return User{
		Username: strings.ToLower(id.Identity.User.Username),
		OrgID:    orgID,
		Admin:    id.Identity.User.IsOrgAdmin,
	}, nil
}
