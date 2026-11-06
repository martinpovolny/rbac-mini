package group

import (
	"rbac-go/internal/principal"
	"rbac-go/internal/role"
	"rbac-go/internal/store"
)

// Group mirrors the v1 group list item (GroupOut).
type Group struct {
	UUID            string `json:"uuid"`
	Name            string `json:"name"`
	Description     string `json:"description,omitempty"`
	PrincipalCount  int    `json:"principalCount"`
	RoleCount       int    `json:"roleCount"`
	PlatformDefault bool   `json:"platform_default"`
	AdminDefault    bool   `json:"admin_default"`
	System          bool   `json:"system"`
}

// List returns tenant groups ordered by name. Like Django's
// _filter_default_groups, platform_default/admin_default groups are excluded
// unless explicitly requested.
func List(db *store.DB, orgID string, includeDefaults bool, limit, offset int) (items []Group, total int, err error) {
	where := `g.tenant_id = (SELECT id FROM tenants WHERE org_id = ?)`
	args := []any{orgID}
	if !includeDefaults {
		where += ` AND g.platform_default = 0 AND g.admin_default = 0`
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM groups g WHERE `+where, args...).Scan(&count); err != nil {
		return nil, 0, err
	}
	q := `SELECT g.uuid, g.name, COALESCE(g.description,''),
		(SELECT COUNT(*) FROM group_principals WHERE group_id = g.id),
		(SELECT COUNT(DISTINCT r.id) FROM policies pol
			JOIN policy_roles pr ON pr.policy_id = pol.id
			JOIN roles r ON r.id = pr.role_id
			WHERE pol.group_id = g.id),
		g.platform_default, g.admin_default, g.system
		FROM groups g WHERE ` + where + ` ORDER BY g.name LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items = []Group{}
	for rows.Next() {
		var g Group
		var desc string
		var pd, ad, sys int
		if err := rows.Scan(&g.UUID, &g.Name, &desc, &g.PrincipalCount, &g.RoleCount, &pd, &ad, &sys); err != nil {
			return nil, 0, err
		}
		g.Description = desc
		g.PlatformDefault = pd != 0
		g.AdminDefault = ad != 0
		g.System = sys != 0
		items = append(items, g)
	}
	return items, count, rows.Err()
}

// Roles returns the distinct roles granted to a group through its policies.
func Roles(db *store.DB, orgID, groupUUID string, limit, offset int) (items []role.Role, total int, err error) {
	var groupID int64
	err = db.QueryRow(`SELECT g.id FROM groups g
		JOIN tenants t ON t.id = g.tenant_id
		WHERE g.uuid = ? AND t.org_id = ?`, groupUUID, orgID).Scan(&groupID)
	if err != nil {
		return nil, 0, err
	}
	countQ := `SELECT COUNT(DISTINCT r.id) FROM roles r
		JOIN policy_roles pr ON pr.role_id = r.id
		JOIN policies pol ON pol.id = pr.policy_id
		WHERE pol.group_id = ?`
	if err := db.QueryRow(countQ, groupID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := db.Query(`SELECT DISTINCT r.uuid, r.name, r.display_name, r.system
		FROM roles r
		JOIN policy_roles pr ON pr.role_id = r.id
		JOIN policies pol ON pol.id = pr.policy_id
		WHERE pol.group_id = ? ORDER BY r.name LIMIT ? OFFSET ?`, groupID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items = []role.Role{}
	for rows.Next() {
		var r role.Role
		var system int
		if err := rows.Scan(&r.UUID, &r.Name, &r.DisplayName, &system); err != nil {
			return nil, 0, err
		}
		r.System = system != 0
		items = append(items, r)
	}
	return items, total, rows.Err()
}

// Members returns the principals of a group (members tab).
func Members(db *store.DB, orgID, groupUUID string, limit, offset int) (items []principal.Principal, total int, err error) {
	var groupID int64
	err = db.QueryRow(`SELECT g.id FROM groups g
		JOIN tenants t ON t.id = g.tenant_id
		WHERE g.uuid = ? AND t.org_id = ?`, groupUUID, orgID).Scan(&groupID)
	if err != nil {
		return nil, 0, err
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM group_principals WHERE group_id = ?`,
		groupID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := db.Query(`SELECT pr.username FROM principals pr
		JOIN group_principals gp ON gp.principal_id = pr.id
		WHERE gp.group_id = ? ORDER BY pr.username LIMIT ? OFFSET ?`, groupID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items = []principal.Principal{}
	for rows.Next() {
		var username string
		if err := rows.Scan(&username); err != nil {
			return nil, 0, err
		}
		items = append(items, principal.Principal{
			Username: username, Email: "baz", FirstName: "foo", LastName: "bar",
		})
	}
	return items, total, rows.Err()
}

// Get returns one tenant group by uuid.
func Get(db *store.DB, orgID, uuid string) (Group, error) {
	var g Group
	var desc string
	var pd, ad, sys int
	err := db.QueryRow(`SELECT g.uuid, g.name, COALESCE(g.description,''),
		(SELECT COUNT(*) FROM group_principals WHERE group_id = g.id),
		(SELECT COUNT(DISTINCT r.id) FROM policies pol
			JOIN policy_roles pr ON pr.policy_id = pol.id
			JOIN roles r ON r.id = pr.role_id
			WHERE pol.group_id = g.id),
		g.platform_default, g.admin_default, g.system
		FROM groups g JOIN tenants t ON t.id = g.tenant_id
		WHERE g.uuid = ? AND t.org_id = ?`,
		uuid, orgID).Scan(&g.UUID, &g.Name, &desc, &g.PrincipalCount, &g.RoleCount, &pd, &ad, &sys)
	if err != nil {
		return Group{}, err
	}
	g.Description = desc
	g.PlatformDefault = pd != 0
	g.AdminDefault = ad != 0
	g.System = sys != 0
	return g, nil
}
