package group

import (
	"database/sql"
	"fmt"
	"strings"

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
	Created         string `json:"created"`
	Modified        string `json:"modified"`
}

// Filter mirrors the v1 group list query parameters the on-prem UI uses
// (Django GroupFilter). NameMatch empty or "partial" is a case-insensitive
// contains, "exact" is a case-insensitive equality.
type Filter struct {
	IncludeDefaults bool
	Name            string
	NameMatch       string
	PlatformDefault *bool
	AdminDefault    *bool
	System          *bool
	// OrderBy is "name" (default), "modified", "principalCount" or "policyCount".
	OrderBy string
	Desc    bool
}

// List returns tenant groups ordered by name. Like Django's
// _filter_default_groups, platform_default/admin_default groups are excluded
// unless explicitly requested. An explicit boolean filter takes over that
// exclusion, so ?platform_default=true returns the default group itself.
func List(db *store.DB, orgID string, f Filter, limit, offset int) (items []Group, total int, err error) {
	where := `g.tenant_id = (SELECT id FROM tenants WHERE org_id = ?)`
	args := []any{orgID}
	// Exclude each default class unless that class is what was asked for. A
	// system filter already speaks about the defaults, so it disables both.
	if !f.IncludeDefaults && f.System == nil {
		if f.PlatformDefault == nil {
			where += ` AND g.platform_default = 0`
		}
		if f.AdminDefault == nil {
			where += ` AND g.admin_default = 0`
		}
	}
	boolInt := func(b bool) int {
		if b {
			return 1
		}
		return 0
	}
	if f.PlatformDefault != nil {
		where += ` AND g.platform_default = ?`
		args = append(args, boolInt(*f.PlatformDefault))
	}
	if f.AdminDefault != nil {
		where += ` AND g.admin_default = ?`
		args = append(args, boolInt(*f.AdminDefault))
	}
	if f.System != nil {
		where += ` AND g.system = ?`
		args = append(args, boolInt(*f.System))
	}
	if f.Name != "" {
		if f.NameMatch == "exact" {
			where += ` AND LOWER(g.name) = ?`
			args = append(args, strings.ToLower(f.Name))
		} else {
			where += ` AND LOWER(g.name) LIKE ?`
			args = append(args, "%"+strings.ToLower(f.Name)+"%")
		}
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM groups g WHERE `+where, args...).Scan(&count); err != nil {
		return nil, 0, err
	}
	order := "LOWER(g.name)"
	switch f.OrderBy {
	case "", "name":
	case "modified":
		order = "g.modified"
	case "principalCount":
		order = `(SELECT COUNT(*) FROM group_principals WHERE group_id = g.id)`
	case "policyCount":
		order = `(SELECT COUNT(*) FROM policies pol WHERE pol.group_id = g.id)`
	}
	if f.Desc {
		order += " DESC"
	}
	q := `SELECT g.uuid, g.name, COALESCE(g.description,''),
		(SELECT COUNT(*) FROM group_principals WHERE group_id = g.id),
		(SELECT COUNT(DISTINCT r.id) FROM policies pol
			JOIN policy_roles pr ON pr.policy_id = pol.id
			JOIN roles r ON r.id = pr.role_id
			WHERE pol.group_id = g.id),
		g.platform_default, g.admin_default, g.system, COALESCE(g.created,''), COALESCE(g.modified,'')
		FROM groups g WHERE ` + where + ` ORDER BY ` + order + ` LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()
	items = []Group{}
	for rows.Next() {
		var g Group
		var desc string
		var pd, ad, sys int
		if err := rows.Scan(&g.UUID, &g.Name, &desc, &g.PrincipalCount, &g.RoleCount,
			&pd, &ad, &sys, &g.Created, &g.Modified); err != nil {
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

// ErrUnknownRole is returned for role UUIDs that do not exist.
type ErrUnknownRole struct{ UUID string }

func (e ErrUnknownRole) Error() string {
	return "role " + e.UUID + " not found"
}

// resolveRoleIDs maps role UUIDs (tenant or public) to ids.
func resolveRoleIDs(db *store.DB, orgID string, uuids []string) ([]int64, error) {
	ids := make([]int64, 0, len(uuids))
	for _, uuid := range uuids {
		var id int64
		err := db.QueryRow(`SELECT r.id FROM roles r
			JOIN tenants t ON t.id = r.tenant_id
			WHERE r.uuid = ? AND (t.org_id = ? OR t.tenant_name = 'public')`,
			uuid, orgID).Scan(&id)
		if err == sql.ErrNoRows {
			return nil, ErrUnknownRole{UUID: uuid}
		}
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func ensureGroupPolicy(tx *store.Tx, groupID, tenantID int64, groupName string) (int64, error) {
	var policyID int64
	err := tx.QueryRow(`SELECT id FROM policies WHERE group_id = ? LIMIT 1`, groupID).Scan(&policyID)
	if err == nil {
		return policyID, nil
	}
	if err != sql.ErrNoRows {
		return 0, err
	}
	if _, err := tx.Exec(`INSERT INTO policies(uuid, name, group_id, tenant_id)
		VALUES(?, ?, ?, ?) ON CONFLICT(name, tenant_id) DO NOTHING`,
		store.NewUUID(), groupName+" policy", groupID, tenantID); err != nil {
		return 0, err
	}
	err = tx.QueryRow(`SELECT id FROM policies WHERE group_id = ? LIMIT 1`, groupID).Scan(&policyID)
	return policyID, err
}

// AssignRoles grants roles to a group via its policy.
func AssignRoles(db *store.DB, orgID, groupUUID string, roleUUIDs []string) error {
	var groupID, tenantID int64
	var ad int
	if err := db.QueryRow(`SELECT g.id, g.tenant_id, g.admin_default FROM groups g
		JOIN tenants t ON t.id = g.tenant_id
		WHERE g.uuid = ? AND t.org_id = ?`, groupUUID, orgID).Scan(&groupID, &tenantID, &ad); err != nil {
		return err
	}
	if ad != 0 {
		return fmt.Errorf("default admin access cannot be modified")
	}
	roleIDs, err := resolveRoleIDs(db, orgID, roleUUIDs)
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	policyID, err := ensureGroupPolicy(tx, groupID, tenantID, groupNameOf(tx, groupID))
	if err != nil {
		return err
	}
	for _, roleID := range roleIDs {
		if _, err := tx.Exec(`INSERT INTO policy_roles(policy_id, role_id)
			VALUES(?, ?) ON CONFLICT(policy_id, role_id) DO NOTHING`, policyID, roleID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func groupNameOf(tx *store.Tx, groupID int64) string {
	var name string
	_ = tx.QueryRow(`SELECT name FROM groups WHERE id = ?`, groupID).Scan(&name)
	return name
}

// RemoveRoles detaches roles from all of the group's policies.
func RemoveRoles(db *store.DB, orgID, groupUUID string, roleUUIDs []string) error {
	var groupID int64
	var ad int
	if err := db.QueryRow(`SELECT g.id, g.admin_default FROM groups g
		JOIN tenants t ON t.id = g.tenant_id
		WHERE g.uuid = ? AND t.org_id = ?`, groupUUID, orgID).Scan(&groupID, &ad); err != nil {
		return err
	}
	if ad != 0 {
		return fmt.Errorf("default admin access cannot be modified")
	}
	roleIDs, err := resolveRoleIDs(db, orgID, roleUUIDs)
	if err != nil {
		return err
	}
	for _, roleID := range roleIDs {
		if _, err := db.Exec(`DELETE FROM policy_roles WHERE policy_id IN
			(SELECT id FROM policies WHERE group_id = ?) AND role_id = ?`, groupID, roleID); err != nil {
			return err
		}
	}
	return nil
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
	rows, err := db.Query(`SELECT DISTINCT r.uuid, r.name, r.display_name, r.system,
		COALESCE(r.created,''), COALESCE(r.modified,'')
		FROM roles r
		JOIN policy_roles pr ON pr.role_id = r.id
		JOIN policies pol ON pol.id = pr.policy_id
		WHERE pol.group_id = ? ORDER BY r.name LIMIT ? OFFSET ?`, groupID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()
	items = []role.Role{}
	for rows.Next() {
		var r role.Role
		var system int
		if err := rows.Scan(&r.UUID, &r.Name, &r.DisplayName, &system, &r.Created, &r.Modified); err != nil {
			return nil, 0, err
		}
		r.System = system != 0
		items = append(items, r)
	}
	return items, total, rows.Err()
}

// Members returns the principals of a group (members tab).
// Members lists the principals in a tenant group. It shares the principal
// filters, so members come back with the same stored profile contract and the
// same default status=enabled as GET /principals/.
func Members(db *store.DB, orgID, groupUUID string, f principal.Filter) (items []principal.Principal, total int, err error) {
	var found int
	err = db.QueryRow(`SELECT COUNT(*) FROM groups g
		JOIN tenants t ON t.id = g.tenant_id
		WHERE g.uuid = ? AND t.org_id = ?`, groupUUID, orgID).Scan(&found)
	if err != nil {
		return nil, 0, err
	}
	if found == 0 {
		return nil, 0, sql.ErrNoRows
	}
	f.GroupUUID = groupUUID
	return principal.List(db, orgID, f)
}

// reservedNames mirrors validate_group_name.
var reservedNames = map[string]bool{"default access": true, "custom default access": true}

// ErrReservedName mirrors the Django reserved-name validation.
type ErrReservedName struct{ Name string }

func (e ErrReservedName) Error() string {
	return e.Name + " is reserved, please use another name."
}

// ErrDuplicate is returned when the name is taken in the tenant.
type ErrDuplicate struct{ Name string }

func (e ErrDuplicate) Error() string {
	return "group " + e.Name + " already exists"
}

// Create inserts a tenant group (no policy until roles are assigned).
func Create(db *store.DB, orgID, name, description string) (Group, error) {
	if name == "" {
		return Group{}, fmt.Errorf("name is required")
	}
	if len(name) > 150 {
		return Group{}, fmt.Errorf("name is too long (max 150)")
	}
	if reservedNames[strings.ToLower(name)] {
		return Group{}, ErrReservedName{Name: name}
	}
	uuid := store.NewUUID()
	now := store.Now()
	_, err := db.Exec(`INSERT INTO groups(uuid, name, description, created, modified, tenant_id)
		VALUES(?, ?, ?, ?, ?, (SELECT id FROM tenants WHERE org_id=?))`,
		uuid, name, description, now, now, orgID)
	if err != nil {
		if isUniqueViolation(err) {
			return Group{}, ErrDuplicate{Name: name}
		}
		return Group{}, err
	}
	return Get(db, orgID, uuid)
}

func isUniqueViolation(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") || strings.Contains(msg, "duplicate key value")
}

// ErrUnknownPrincipals lists usernames that do not exist in the tenant.
type ErrUnknownPrincipals struct{ Names []string }

func (e ErrUnknownPrincipals) Error() string {
	return fmt.Sprintf("User(s) %v not found", e.Names)
}

// AddMembers adds existing tenant principals to a group. Unknown usernames
// are a 400 like Django's add/remove validation.
func AddMembers(db *store.DB, orgID, groupUUID string, usernames []string) error {
	return changeMembers(db, orgID, groupUUID, usernames, true)
}

// RemoveMembers removes principals from a group; unknown names are a 400.
func RemoveMembers(db *store.DB, orgID, groupUUID string, usernames []string) error {
	return changeMembers(db, orgID, groupUUID, usernames, false)
}

func changeMembers(db *store.DB, orgID, groupUUID string, usernames []string, add bool) error {
	var groupID int64
	var pd int
	if err := db.QueryRow(`SELECT g.id, g.platform_default FROM groups g
		JOIN tenants t ON t.id = g.tenant_id
		WHERE g.uuid = ? AND t.org_id = ?`, groupUUID, orgID).Scan(&groupID, &pd); err != nil {
		return err
	}
	action := "REMOVE"
	if add {
		action = "ADD"
	}
	if pd != 0 {
		return ErrProtected{Action: action, Kind: "platform_default"}
	}
	lowered := map[string]bool{}
	var distinct []string
	for _, u := range usernames {
		u = strings.ToLower(u)
		if !lowered[u] {
			lowered[u] = true
			distinct = append(distinct, u)
		}
	}
	if len(distinct) == 0 {
		return nil
	}
	placeholders := make([]string, 0, len(distinct))
	args := []any{orgID}
	for _, u := range distinct {
		placeholders = append(placeholders, "?")
		args = append(args, u)
	}
	rows, err := db.Query(`SELECT id, username FROM principals WHERE tenant_id =
		(SELECT id FROM tenants WHERE org_id = ?) AND type = 'user'
		AND cross_account = 0 AND username IN (`+strings.Join(placeholders, ",")+`)`, args...)
	if err != nil {
		return err
	}
	idByName := map[string]int64{}
	for rows.Next() {
		var id int64
		var u string
		if err := rows.Scan(&id, &u); err != nil {
			_ = rows.Close()
			return err
		}
		idByName[strings.ToLower(u)] = id
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	var missing []string
	for _, u := range distinct {
		if _, ok := idByName[u]; !ok {
			missing = append(missing, u)
		}
	}
	if len(missing) > 0 {
		return ErrUnknownPrincipals{Names: missing}
	}
	for _, u := range distinct {
		if add {
			if _, err := db.Exec(`INSERT INTO group_principals(group_id, principal_id)
				VALUES(?, ?) ON CONFLICT(group_id, principal_id) DO NOTHING`,
				groupID, idByName[u]); err != nil {
				return err
			}
		} else {
			if _, err := db.Exec(`DELETE FROM group_principals WHERE group_id=? AND principal_id=?`,
				groupID, idByName[u]); err != nil {
				return err
			}
		}
	}
	return nil
}

type ErrProtected struct{ Action, Kind string }

func (e ErrProtected) Error() string {
	return e.Action + " cannot be performed on " + e.Kind + " groups."
}

// Update renames/re-describes a group, honoring Django's default-group guards.
func Update(db *store.DB, orgID, uuid, name, description string, setDescription bool) (Group, error) {
	if name == "" {
		return Group{}, fmt.Errorf("name is required")
	}
	if len(name) > 150 {
		return Group{}, fmt.Errorf("name is too long (max 150)")
	}
	if reservedNames[strings.ToLower(name)] {
		return Group{}, ErrReservedName{Name: name}
	}
	var id int64
	var pd, ad int
	if err := db.QueryRow(`SELECT g.id, g.platform_default, g.admin_default FROM groups g
		JOIN tenants t ON t.id = g.tenant_id
		WHERE g.uuid = ? AND t.org_id = ?`, uuid, orgID).Scan(&id, &pd, &ad); err != nil {
		return Group{}, err
	}
	if pd != 0 {
		return Group{}, ErrProtected{Action: "UPDATE", Kind: "platform_default"}
	}
	if ad != 0 {
		return Group{}, fmt.Errorf("default admin access cannot be modified")
	}
	if setDescription {
		if _, err := db.Exec(`UPDATE groups SET name=?, description=?, modified=? WHERE id=?`,
			name, description, store.Now(), id); err != nil {
			return Group{}, mapUnique(err, name)
		}
	} else {
		if _, err := db.Exec(`UPDATE groups SET name=?, modified=? WHERE id=?`,
			name, store.Now(), id); err != nil {
			return Group{}, mapUnique(err, name)
		}
	}
	return Get(db, orgID, uuid)
}

func mapUnique(err error, name string) error {
	if isUniqueViolation(err) {
		return ErrDuplicate{Name: name}
	}
	return err
}

// Delete removes a custom group (memberships cascade).
func Delete(db *store.DB, orgID, uuid string) error {
	var id int64
	var pd, ad int
	if err := db.QueryRow(`SELECT g.id, g.platform_default, g.admin_default FROM groups g
		JOIN tenants t ON t.id = g.tenant_id
		WHERE g.uuid = ? AND t.org_id = ?`, uuid, orgID).Scan(&id, &pd, &ad); err != nil {
		return err
	}
	if pd != 0 {
		return ErrProtected{Action: "DELETE", Kind: "platform_default"}
	}
	if ad != 0 {
		return ErrProtected{Action: "DELETE", Kind: "admin_default"}
	}
	_, err := db.Exec(`DELETE FROM groups WHERE id=?`, id)
	return err
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
		g.platform_default, g.admin_default, g.system, COALESCE(g.created,''), COALESCE(g.modified,'')
		FROM groups g JOIN tenants t ON t.id = g.tenant_id
		WHERE g.uuid = ? AND t.org_id = ?`,
		uuid, orgID).Scan(&g.UUID, &g.Name, &desc, &g.PrincipalCount, &g.RoleCount,
		&pd, &ad, &sys, &g.Created, &g.Modified)
	if err != nil {
		return Group{}, err
	}
	g.Description = desc
	g.PlatformDefault = pd != 0
	g.AdminDefault = ad != 0
	g.System = sys != 0
	return g, nil
}
