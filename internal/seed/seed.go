package seed

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"strings"
)

// Seed loads the on-prem role/permission set (mirrors
// operator/internal/resources/rbac_seed/data/*.json plus
// `manage.py seeds --skip-notifications`). Idempotent: safe to run on every
// deploy like the operator migration Job. Only portable SQL: INSERT ..
// ON CONFLICT DO NOTHING (SQLite + Postgres) plus SELECT-then-INSERT inside
// one transaction for the access row.
func Seed(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`INSERT INTO tenants(org_id, tenant_name, ready)
		VALUES('public','public',1) ON CONFLICT(org_id) DO NOTHING`); err != nil {
		return err
	}
	var tenantID int64
	if err := tx.QueryRow(`SELECT id FROM tenants WHERE org_id='public'`).Scan(&tenantID); err != nil {
		return err
	}

	roleID, err := getOrCreate(tx,
		`SELECT id FROM roles WHERE name='Cost Administrator' AND tenant_id=?`, []any{tenantID},
		`INSERT INTO roles(uuid, name, display_name, system, tenant_id)
		 VALUES(?, 'Cost Administrator', 'Cost Administrator', 1, ?)
		 ON CONFLICT(name, tenant_id) DO NOTHING`, newUUID(), tenantID)
	if err != nil {
		return err
	}
	permID, err := getOrCreate(tx,
		`SELECT id FROM permissions WHERE permission='cost-management:*:*'`, nil,
		`INSERT INTO permissions(application, resource_type, verb, permission, tenant_id)
		 VALUES('cost-management','*','*','cost-management:*:*',?)
		 ON CONFLICT(permission) DO NOTHING`, tenantID)
	if err != nil {
		return err
	}
	var accessID int64
	if err := tx.QueryRow(`SELECT id FROM access WHERE role_id=? AND permission_id=?`,
		roleID, permID).Scan(&accessID); err == sql.ErrNoRows {
		if _, err := tx.Exec(`INSERT INTO access(role_id, permission_id, tenant_id)
			VALUES(?,?,?)`, roleID, permID, tenantID); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	return tx.Commit()
}

// Bootstrap wires an org for on-prem use (mirrors operator
// render_bootstrap_script): tenant + "CI Test Admin" group carrying the
// seeded Cost Administrator role + one principal per username. Idempotent.
func Bootstrap(db *sql.DB, orgID, accountID string, usernames []string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`INSERT INTO tenants(org_id, tenant_name, account_id, ready)
		VALUES(?, 'acct'||?, ?, 1) ON CONFLICT(org_id) DO NOTHING`,
		orgID, accountID, accountID); err != nil {
		return err
	}
	var tenantID int64
	if err := tx.QueryRow(`SELECT id FROM tenants WHERE org_id=?`, orgID).Scan(&tenantID); err != nil {
		return err
	}
	groupID, err := getOrCreate(tx,
		`SELECT id FROM groups WHERE name='CI Test Admin' AND tenant_id=?`, []any{tenantID},
		`INSERT INTO groups(uuid, name, description, system, tenant_id)
		 VALUES(?, 'CI Test Admin', 'CI service account admin access', 1, ?)
		 ON CONFLICT(name, tenant_id) DO NOTHING`, newUUID(), tenantID)
	if err != nil {
		return err
	}
	policyID, err := getOrCreate(tx,
		`SELECT id FROM policies WHERE name='CI Test Admin Policy' AND tenant_id=?`, []any{tenantID},
		`INSERT INTO policies(uuid, name, group_id, tenant_id)
		 VALUES(?, 'CI Test Admin Policy', ?, ?)
		 ON CONFLICT(name, tenant_id) DO NOTHING`, newUUID(), groupID, tenantID)
	if err != nil {
		return err
	}
	var roleID int64
	if err := tx.QueryRow(`SELECT r.id FROM roles r
		JOIN tenants t ON t.id = r.tenant_id
		WHERE r.name='Cost Administrator' AND t.tenant_name='public'`).Scan(&roleID); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO policy_roles(policy_id, role_id)
		VALUES(?, ?) ON CONFLICT(policy_id, role_id) DO NOTHING`, policyID, roleID); err != nil {
		return err
	}
	for _, username := range usernames {
		username = strings.ToLower(username)
		principalID, err := getOrCreate(tx,
			`SELECT id FROM principals WHERE username=? AND tenant_id=?`, []any{username, tenantID},
			`INSERT INTO principals(uuid, username, type, tenant_id)
			 VALUES(?, ?, 'user', ?) ON CONFLICT(username, tenant_id) DO NOTHING`,
			newUUID(), username, tenantID)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO group_principals(group_id, principal_id)
			VALUES(?, ?) ON CONFLICT(group_id, principal_id) DO NOTHING`,
			groupID, principalID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// getOrCreate selects the row id, inserting (which may be a no-op on
// conflict) and re-selecting when absent.
func getOrCreate(tx *sql.Tx, selectQ string, selectArgs []any, insertQ string, insertArgs ...any) (int64, error) {
	var id int64
	err := tx.QueryRow(selectQ, selectArgs...).Scan(&id)
	if err == nil {
		return id, nil
	}
	if err != sql.ErrNoRows {
		return 0, err
	}
	if _, err := tx.Exec(insertQ, insertArgs...); err != nil {
		return 0, err
	}
	err = tx.QueryRow(selectQ, selectArgs...).Scan(&id)
	return id, err
}

func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	dst := make([]byte, 36)
	hex.Encode(dst[0:8], b[0:4])
	dst[8] = '-'
	hex.Encode(dst[9:13], b[4:6])
	dst[13] = '-'
	hex.Encode(dst[14:18], b[6:8])
	dst[18] = '-'
	hex.Encode(dst[19:23], b[8:10])
	dst[23] = '-'
	hex.Encode(dst[24:], b[10:])
	return string(dst)
}
