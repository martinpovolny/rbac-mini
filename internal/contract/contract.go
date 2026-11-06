// Package contract ports koku's read-side ACL mapping
// (koku/koku/rbac.py: _normalize_sources_acls, _extract_permission_data,
// _extract_resource_definitions, _process_acls) so the Go test suite proves
// rbac-go's /access/ output is consumable by koku's mapping.
package contract

import (
	"errors"
	"strings"

	"rbac-go/internal/access"
)

// NormalizeSourcesACL rewrites sources:<resource>:<op> to
// sources:sources:<op> (koku _normalize_sources_acls).
func NormalizeSourcesACL(permission string) string {
	parts := strings.Split(permission, ":")
	if len(parts) == 3 && parts[0] == "sources" {
		return "sources:sources:" + parts[2]
	}
	return permission
}

// ExtractPermission splits app:resource:verb (koku _extract_permission_data).
func ExtractPermission(permission string) (resource, operation string, err error) {
	parts := strings.Split(permission, ":")
	if len(parts) != 3 {
		return "", "", errors.New("invalid permission definition")
	}
	return parts[1], parts[2], nil
}

// ACLData is one grouped capability (koku acl_data).
type ACLData struct {
	Operation string
	Resources []any
}

// ProcessACLs groups /access/ entries by resource type (koku _process_acls):
// legacy cost-management:rate is rewritten to cost_model, malformed
// permissions are skipped, empty resourceDefinitions mean ["*"].
func ProcessACLs(entries []access.Entry) map[string][]ACLData {
	return nil
}
