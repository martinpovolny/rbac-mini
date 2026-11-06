package permission

import (
	"fmt"
	"strings"

	"rbac-go/internal/store"
)

// Permission mirrors the v1 API item shape.
type Permission struct {
	Application  string `json:"application"`
	ResourceType string `json:"resource_type"`
	Verb         string `json:"verb"`
	Permission   string `json:"permission"`
}

// Filter mirrors PermissionFilter (management/permission/view.py):
// comma-separated IN lists, globals exclusion, and the ROLE_CREATE_ALLOW_LIST
// gate used by the on-prem role wizard.
type Filter struct {
	Applications   []string
	ResourceTypes  []string
	Verbs          []string
	ExcludeGlobals bool
	AllowedOnly    bool
	AllowList      []string
}

func (f Filter) clauses() (string, []any) {
	var conds []string
	var args []any
	// Permissions are visible to a tenant plus the seeded public tenant.
	conds = append(conds, `p.tenant_id IN (
		SELECT id FROM tenants WHERE org_id = ? OR tenant_name = 'public')`)
	in := func(col string, vals []string) {
		if len(vals) == 0 {
			return
		}
		conds = append(conds, fmt.Sprintf("p.%s IN (%s)", col, strings.Repeat("?,", len(vals)-1)+"?"))
		for _, v := range vals {
			args = append(args, v)
		}
	}
	in("application", f.Applications)
	in("resource_type", f.ResourceTypes)
	in("verb", f.Verbs)
	if f.ExcludeGlobals {
		conds = append(conds, `(p.application <> '*' AND p.resource_type <> '*' AND p.verb <> '*')`)
	}
	if f.AllowedOnly {
		allow := f.AllowList
		if len(allow) == 0 {
			// Mirror Django: "".split(",") == [""] matches nothing.
			allow = []string{""}
		}
		conds = append(conds, fmt.Sprintf(`p.application IN (%s)`, strings.Repeat("?,", len(allow)-1)+"?"))
		for _, v := range allow {
			args = append(args, v)
		}
	}
	return strings.Join(conds, " AND "), args
}

// List returns matching permissions ordered by permission string.
func List(db *store.DB, orgID string, f Filter, limit, offset int) (items []Permission, total int, err error) {
	where, args := f.clauses()
	countQ := `SELECT COUNT(*) FROM permissions p WHERE ` + where
	if err := db.QueryRow(countQ, append([]any{orgID}, args...)...).Scan(&total); err != nil {
		return nil, 0, err
	}
	q := `SELECT p.application, p.resource_type, p.verb, p.permission
		FROM permissions p WHERE ` + where + ` ORDER BY p.permission LIMIT ? OFFSET ?`
	args = append(append([]any{orgID}, args...), limit, offset)
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items = []Permission{}
	for rows.Next() {
		var p Permission
		if err := rows.Scan(&p.Application, &p.ResourceType, &p.Verb, &p.Permission); err != nil {
			return nil, 0, err
		}
		items = append(items, p)
	}
	return items, total, rows.Err()
}

// Options returns distinct values of field (application|resource_type|verb)
// honoring the same filters (permission options dropdown).
func Options(db *store.DB, orgID, field string, f Filter) ([]string, error) {
	if field != "application" && field != "resource_type" && field != "verb" {
		return nil, fmt.Errorf("invalid field %q", field)
	}
	where, args := f.clauses()
	q := fmt.Sprintf(`SELECT DISTINCT p.%s FROM permissions p WHERE %s ORDER BY p.%s`, field, where, field)
	rows, err := db.Query(q, append([]any{orgID}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
