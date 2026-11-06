package access

import (
	"database/sql"
	"encoding/json"
	"strings"

	"rbac-go/internal/store"
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
// applications (exact match; empty means all applications). Unknown principal
// or tenant yields an empty list and no error (koku maps [] to deny).
//
// Portable SQL only: no DISTINCT ON, no JSON operators, ? placeholders
// (rebind to $n for Postgres at the call site). attribute_filter is selected
// as text and decoded in Go.
func Query(db *store.DB, orgID, username string, applications []string) ([]Entry, error) {
	placeholders := ""
	if len(applications) > 0 {
		placeholders = " AND p.application IN (" + strings.Repeat("?,", len(applications)-1) + "?)"
	}
	q := `
SELECT DISTINCT p.permission, rd.attribute_filter
FROM tenants t
JOIN principals pr ON pr.tenant_id = t.id
JOIN group_principals gp ON gp.principal_id = pr.id
JOIN groups g ON g.id = gp.group_id AND g.tenant_id = t.id
JOIN policies pol ON pol.group_id = g.id AND pol.tenant_id = t.id
JOIN policy_roles prl ON prl.policy_id = pol.id
JOIN roles r ON r.id = prl.role_id
JOIN access a ON a.role_id = r.id
JOIN permissions p ON p.id = a.permission_id
LEFT JOIN resource_definitions rd ON rd.access_id = a.id
WHERE t.org_id = ? AND pr.username = ?` + placeholders + `
ORDER BY p.permission`
	args := make([]any, 0, len(applications)+2)
	args = append(args, orgID, strings.ToLower(username))
	for _, app := range applications {
		args = append(args, app)
	}
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	byPerm := map[string]*Entry{}
	order := []string{}
	for rows.Next() {
		var perm string
		var raw sql.NullString
		if err := rows.Scan(&perm, &raw); err != nil {
			return nil, err
		}
		e, ok := byPerm[perm]
		if !ok {
			e = &Entry{Permission: perm, ResourceDefinitions: []ResourceDefinition{}}
			byPerm[perm] = e
			order = append(order, perm)
		}
		if raw.Valid && raw.String != "" && raw.String != "{}" {
			var af map[string]any
			if err := json.Unmarshal([]byte(raw.String), &af); err != nil {
				return nil, err
			}
			e.ResourceDefinitions = append(e.ResourceDefinitions, ResourceDefinition{AttributeFilter: af})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(order))
	for _, perm := range order {
		out = append(out, *byPerm[perm])
	}
	return out, nil
}
