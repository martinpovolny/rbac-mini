package keycloak

import (
	"rbac-go/internal/store"
)

// Config mirrors the operator keycloakSync spec
// (operator/internal/resources/rbac_keycloak_sync.go).
type Config struct {
	BaseURL      string
	Realm        string
	ClientID     string
	ClientSecret string
	OrgID        string
	// Group is the tenant group synced users are added to.
	Group string
	// PruneOrphans deletes tenant principals gone from Keycloak.
	PruneOrphans bool
}

// Sync lists realm users via client-credentials and upserts them as tenant
// principals (lowercased usernames), adding them to Group. With PruneOrphans,
// tenant principals no longer present in Keycloak are deleted.
func Sync(db *store.DB, cfg Config) error {
	return errNotImplemented
}

var errNotImplemented = errString("not implemented")

type errString string

func (e errString) Error() string { return string(e) }
