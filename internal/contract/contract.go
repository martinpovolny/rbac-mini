// Package contract ports koku's read-side ACL mapping
// (koku/koku/rbac.py: _normalize_sources_acls, _extract_permission_data,
// _extract_resource_definitions, _process_acls) so the Go test suite proves
// rbac-go's /access/ output is consumable by koku's mapping.
package contract

import "errors"

// NormalizeSourcesACL rewrites sources:<resource>:<op> to
// sources:sources:<op> (koku _normalize_sources_acls).
func NormalizeSourcesACL(permission string) string {
	return permission
}

// ExtractPermission splits app:resource:verb (koku _extract_permission_data).
func ExtractPermission(permission string) (resource, operation string, err error) {
	return "", "", errors.New("not implemented")
}
