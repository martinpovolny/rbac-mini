package role

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"rbac-go/internal/access"
	"rbac-go/internal/store"
)

// Role mirrors the v1 API role shape.
type Role struct {
	UUID        string         `json:"uuid"`
	Name        string         `json:"name"`
	DisplayName string         `json:"display_name"`
	Description string         `json:"description,omitempty"`
	System      bool           `json:"system"`
	Access      []access.Entry `json:"access,omitempty"`
	PolicyCount int            `json:"policyCount,omitempty"`
}

// AccessIn is one access grant in a create/update payload.
type AccessIn struct {
	Permission          string                      `json:"permission"`
	ResourceDefinitions []access.ResourceDefinition `json:"resourceDefinitions"`
}

// ErrAppNotAllowed mirrors "Custom roles cannot be created for {app}".
type ErrAppNotAllowed struct{ App string }

func (e ErrAppNotAllowed) Error() string {
	return fmt.Sprintf("Custom roles cannot be created for %s", e.App)
}

// ErrUnknownPermission mirrors "Permission does not exist: {perm}".
type ErrUnknownPermission struct{ Permission string }

func (e ErrUnknownPermission) Error() string {
	return fmt.Sprintf("Permission does not exist: %s", e.Permission)
}

// splitPermission parses app:resource:verb.
func splitPermission(p string) (app, res, verb string, err error) {
	parts := strings.Split(p, ":")
	if len(parts) != 3 {
		return "", "", "", fmt.Errorf("invalid permission %q", p)
	}
	return parts[0], parts[1], parts[2], nil
}

// Create validates (allow-list + permission existence, like validate_role)
// and stores the role with its access grants and resource definitions.
func Create(db *store.DB, orgID, name, displayName, description string, grants []AccessIn, allowList []string) (Role, error) {
	if name == "" {
		return Role{}, fmt.Errorf("name is required")
	}
	allowed := map[string]bool{}
	for _, a := range allowList {
		allowed[a] = true
	}
	type grant struct {
		permID int64
		rds    []access.ResourceDefinition
	}
	var resolved []grant
	for _, g := range grants {
		app, res, verb, err := splitPermission(g.Permission)
		if err != nil {
			return Role{}, &ErrUnknownPermission{Permission: g.Permission}
		}
		if !allowed[app] {
			return Role{}, &ErrAppNotAllowed{App: app}
		}
		var permID int64
		err = db.QueryRow(`SELECT p.id FROM permissions p
			JOIN tenants t ON t.id = p.tenant_id
			WHERE p.application=? AND p.resource_type=? AND p.verb=?
			AND (t.org_id=? OR t.tenant_name='public')`,
			app, res, verb, orgID).Scan(&permID)
		if err != nil {
			return Role{}, &ErrUnknownPermission{Permission: g.Permission}
		}
		resolved = append(resolved, grant{permID: permID, rds: g.ResourceDefinitions})
	}

	tx, err := db.Begin()
	if err != nil {
		return Role{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if displayName == "" {
		displayName = name
	}
	uuid := store.NewUUID()
	var tenantID int64
	if err := tx.QueryRow(`SELECT id FROM tenants WHERE org_id=?`, orgID).Scan(&tenantID); err != nil {
		return Role{}, err
	}
	if _, err := tx.Exec(`INSERT INTO roles(uuid, name, display_name, description, tenant_id)
		VALUES(?, ?, ?, ?, ?) ON CONFLICT(name, tenant_id) DO NOTHING`,
		uuid, name, displayName, description, tenantID); err != nil {
		return Role{}, err
	}
	var roleID int64
	if err := tx.QueryRow(`SELECT id FROM roles WHERE uuid=?`, uuid).Scan(&roleID); err != nil {
		// Name collision: role already exists for this tenant.
		return Role{}, fmt.Errorf("role %q already exists", name)
	}
	for _, g := range resolved {
		var accessID int64
		if err := tx.QueryRow(`SELECT id FROM access WHERE role_id=? AND permission_id=?`,
			roleID, g.permID).Scan(&accessID); err != nil {
			if _, err := tx.Exec(`INSERT INTO access(role_id, permission_id, tenant_id)
				VALUES(?,?,?) ON CONFLICT DO NOTHING`, roleID, g.permID, tenantID); err != nil {
				return Role{}, err
			}
			// No RETURNING (not portable): re-select. LastInsertId is
			// unsupported on Postgres.
			if err := tx.QueryRow(`SELECT id FROM access WHERE role_id=? AND permission_id=?`,
				roleID, g.permID).Scan(&accessID); err != nil {
				return Role{}, err
			}
		}
		for _, rd := range g.rds {
			raw, err := json.Marshal(rd.AttributeFilter)
			if err != nil {
				return Role{}, err
			}
			if _, err := tx.Exec(`INSERT INTO resource_definitions(access_id, attribute_filter, tenant_id)
				VALUES(?,?,?)`, accessID, string(raw), tenantID); err != nil {
				return Role{}, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return Role{}, err
	}
	return Role{UUID: uuid, Name: name, DisplayName: displayName, Description: description}, nil
}

// List returns tenant (+public platform) roles ordered by name.
func List(db *store.DB, orgID, nameFilter string, limit, offset int) (items []Role, total int, err error) {
	where := ` (t.org_id = ? OR t.tenant_name = 'public')`
	args := []any{orgID}
	if nameFilter != "" {
		where += ` AND LOWER(r.name) LIKE '%' || LOWER(?) || '%'`
		args = append(args, nameFilter)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM roles r
		JOIN tenants t ON t.id = r.tenant_id WHERE`+where, args...).Scan(&count); err != nil {
		return nil, 0, err
	}
	q := `SELECT r.uuid, r.name, r.display_name, COALESCE(r.description,''), r.system
		FROM roles r JOIN tenants t ON t.id = r.tenant_id
		WHERE` + where + ` ORDER BY r.name LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()
	items = []Role{}
	for rows.Next() {
		var r Role
		var system int
		var desc string
		if err := rows.Scan(&r.UUID, &r.Name, &r.DisplayName, &desc, &system); err != nil {
			return nil, 0, err
		}
		if desc != "" {
			r.Description = desc
		}
		r.System = system != 0
		items = append(items, r)
	}
	return items, count, rows.Err()
}

// Get returns a role with its access grants, tenant- or public-scoped.
func Get(db *store.DB, orgID, uuid string) (Role, error) {
	var r Role
	var system int
	var tenantID int64
	err := db.QueryRow(`SELECT r.uuid, r.name, r.display_name,
			COALESCE(r.description,''), r.system, r.tenant_id
		FROM roles r JOIN tenants t ON t.id = r.tenant_id
		WHERE r.uuid = ? AND (t.org_id = ? OR t.tenant_name = 'public')`,
		uuid, orgID).Scan(&r.UUID, &r.Name, &r.DisplayName, &r.Description, &system, &tenantID)
	if err != nil {
		return Role{}, err
	}
	r.System = system != 0
	rows, err := db.Query(`SELECT DISTINCT p.permission, rd.attribute_filter
		FROM access a
		JOIN permissions p ON p.id = a.permission_id
		LEFT JOIN resource_definitions rd ON rd.access_id = a.id
		WHERE a.role_id = (SELECT id FROM roles WHERE uuid = ?)
		ORDER BY p.permission`, uuid)
	if err != nil {
		return Role{}, err
	}
	defer func() { _ = rows.Close() }()
	byPerm := map[string]*access.Entry{}
	var order []string
	for rows.Next() {
		var perm string
		var s sql.NullString
		if err := rows.Scan(&perm, &s); err != nil {
			return Role{}, err
		}
		e, ok := byPerm[perm]
		if !ok {
			e = &access.Entry{Permission: perm, ResourceDefinitions: []access.ResourceDefinition{}}
			byPerm[perm] = e
			order = append(order, perm)
		}
		if s.Valid && s.String != "" && s.String != "{}" {
			var af map[string]any
			if err := json.Unmarshal([]byte(s.String), &af); err != nil {
				return Role{}, err
			}
			e.ResourceDefinitions = append(e.ResourceDefinitions, access.ResourceDefinition{AttributeFilter: af})
		}
	}
	if err := rows.Err(); err != nil {
		return Role{}, err
	}
	r.Access = []access.Entry{}
	for _, perm := range order {
		r.Access = append(r.Access, *byPerm[perm])
	}
	return r, nil
}

// ErrSystemProtected is returned for writes to system roles.
type ErrSystemProtected struct{ Name string }

func (e ErrSystemProtected) Error() string { return "System roles cannot be modified" }

// resolveGrants validates permissions (allow-list + existence) like validate_role.
func resolveGrants(db *store.DB, orgID string, grants []AccessIn, allowList []string) ([]struct {
	permID int64
	rds    []access.ResourceDefinition
}, error) {
	allowed := map[string]bool{}
	for _, a := range allowList {
		allowed[a] = true
	}
	var resolved []struct {
		permID int64
		rds    []access.ResourceDefinition
	}
	for _, g := range grants {
		app, res, verb, err := splitPermission(g.Permission)
		if err != nil {
			return nil, &ErrUnknownPermission{Permission: g.Permission}
		}
		if !allowed[app] {
			return nil, &ErrAppNotAllowed{App: app}
		}
		var permID int64
		err = db.QueryRow(`SELECT p.id FROM permissions p
			JOIN tenants t ON t.id = p.tenant_id
			WHERE p.application=? AND p.resource_type=? AND p.verb=?
			AND (t.org_id=? OR t.tenant_name='public')`,
			app, res, verb, orgID).Scan(&permID)
		if err != nil {
			return nil, &ErrUnknownPermission{Permission: g.Permission}
		}
		resolved = append(resolved, struct {
			permID int64
			rds    []access.ResourceDefinition
		}{permID: permID, rds: g.ResourceDefinitions})
	}
	return resolved, nil
}

func replaceAccess(tx *store.Tx, tenantID, roleID int64, resolved []struct {
	permID int64
	rds    []access.ResourceDefinition
}) error {
	if _, err := tx.Exec(`DELETE FROM resource_definitions WHERE access_id IN
		(SELECT id FROM access WHERE role_id=?)`, roleID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM access WHERE role_id=?`, roleID); err != nil {
		return err
	}
	for _, g := range resolved {
		var accessID int64
		if err := tx.QueryRow(`SELECT id FROM access WHERE role_id=? AND permission_id=?`,
			roleID, g.permID).Scan(&accessID); err != nil {
			if _, err := tx.Exec(`INSERT INTO access(role_id, permission_id, tenant_id)
				VALUES(?,?,?) ON CONFLICT DO NOTHING`, roleID, g.permID, tenantID); err != nil {
				return err
			}
			if err := tx.QueryRow(`SELECT id FROM access WHERE role_id=? AND permission_id=?`,
				roleID, g.permID).Scan(&accessID); err != nil {
				return err
			}
		}
		for _, rd := range g.rds {
			raw, err := json.Marshal(rd.AttributeFilter)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO resource_definitions(access_id, attribute_filter, tenant_id)
				VALUES(?,?,?)`, accessID, string(raw), tenantID); err != nil {
				return err
			}
		}
	}
	return nil
}

// Update replaces name/description/display_name and access grants (PUT).
func Update(db *store.DB, orgID, uuid, name, displayName, description string, grants []AccessIn, allowList []string) (Role, error) {
	resolved, err := resolveGrants(db, orgID, grants, allowList)
	if err != nil {
		return Role{}, err
	}
	tx, err := db.Begin()
	if err != nil {
		return Role{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var roleID, tenantID int64
	var system int
	var currentName string
	err = tx.QueryRow(`SELECT r.id, r.tenant_id, r.system, r.name FROM roles r
		JOIN tenants t ON t.id = r.tenant_id
		WHERE r.uuid=? AND (t.org_id=? OR t.tenant_name='public')`, uuid, orgID).Scan(&roleID, &tenantID, &system, &currentName)
	if err != nil {
		return Role{}, err
	}
	if system != 0 {
		return Role{}, ErrSystemProtected{Name: currentName}
	}
	if displayName == "" {
		displayName = name
	}
	if _, err := tx.Exec(`UPDATE roles SET name=?, display_name=?, description=? WHERE id=?`,
		name, displayName, description, roleID); err != nil {
		return Role{}, err
	}
	if err := replaceAccess(tx, tenantID, roleID, resolved); err != nil {
		return Role{}, err
	}
	if err := tx.Commit(); err != nil {
		return Role{}, err
	}
	return Get(db, orgID, uuid)
}

// Patch applies name/display_name/description changes only.
func Patch(db *store.DB, orgID, uuid string, fields map[string]string) (Role, error) {
	tx, err := db.Begin()
	if err != nil {
		return Role{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var roleID int64
	var system int
	var name string
	err = tx.QueryRow(`SELECT r.id, r.system, r.name FROM roles r
		JOIN tenants t ON t.id = r.tenant_id
		WHERE r.uuid=? AND (t.org_id=? OR t.tenant_name='public')`, uuid, orgID).Scan(&roleID, &system, &name)
	if err != nil {
		return Role{}, err
	}
	if system != 0 {
		return Role{}, ErrSystemProtected{Name: name}
	}
	cols := map[string]string{"name": "name", "display_name": "display_name", "description": "description"}
	for k, v := range fields {
		col, ok := cols[k]
		if !ok {
			return Role{}, fmt.Errorf("field '%s' is not supported. Please use one or more of: [name display_name description]", k)
		}
		if _, err := tx.Exec(fmt.Sprintf("UPDATE roles SET %s=? WHERE id=?", col), v, roleID); err != nil {
			return Role{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Role{}, err
	}
	return Get(db, orgID, uuid)
}

// Delete removes a tenant role (cascades detach policies).
func Delete(db *store.DB, orgID, uuid string) error {
	var roleID int64
	var system int
	var name string
	err := db.QueryRow(`SELECT r.id, r.system, r.name FROM roles r
		JOIN tenants t ON t.id = r.tenant_id
		WHERE r.uuid=? AND (t.org_id=? OR t.tenant_name='public')`, uuid, orgID).Scan(&roleID, &system, &name)
	if err != nil {
		return err
	}
	if system != 0 {
		return ErrSystemProtected{Name: name}
	}
	_, err = db.Exec(`DELETE FROM roles WHERE id=?`, roleID)
	return err
}
