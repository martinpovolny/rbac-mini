package access

import (
	"database/sql"
	"errors"
)

// ResourceDefinition mirrors the v1 API shape:
// {"attributeFilter": {"key": ..., "operation": "equal|in", "value": ...}}.
type ResourceDefinition struct {
	AttributeFilter map[string]any `json:"attributeFilter"`
}

// Entry is one row of the GET /access/ data array.
type Entry struct {
	Permission          string               `json:"permission"`
	ResourceDefinitions []ResourceDefinition `json:"resourceDefinitions"`
}

// Query returns the access list for username in orgID filtered to the given
// applications (exact match). Unknown principal or tenant yields an empty
// list and no error (koku maps [] to deny).
func Query(db *sql.DB, orgID, username string, applications []string) ([]Entry, error) {
	return nil, errors.New("not implemented")
}
