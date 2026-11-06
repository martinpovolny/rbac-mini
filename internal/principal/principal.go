package principal

import (
	"rbac-go/internal/store"
)

// Principal mirrors the on-prem GET /principals/ item shape. The operator
// runs with BYPASS_BOP_VERIFICATION, so names/emails come straight from the
// DB with the same placeholders Django uses
// (management/principal/proxy.py:173: foo/bar/baz).
type Principal struct {
	Username  string `json:"username"`
	Email     string `json:"email"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
}

// List returns tenant user principals (non-cross-account), ordered by
// username, with the total count for meta.
func List(db *store.DB, orgID string, limit, offset int) (items []Principal, total int, err error) {
	if err := db.QueryRow(`SELECT COUNT(*) FROM principals pr
		JOIN tenants t ON t.id = pr.tenant_id
		WHERE t.org_id = ? AND pr.type = 'user' AND pr.cross_account = 0`,
		orgID).Scan(&total); err != nil {
		return nil, 0, err
	}
	q := `SELECT pr.username FROM principals pr
		JOIN tenants t ON t.id = pr.tenant_id
		WHERE t.org_id = ? AND pr.type = 'user' AND pr.cross_account = 0
		ORDER BY pr.username LIMIT ? OFFSET ?`
	rows, err := db.Query(q, orgID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items = []Principal{}
	for rows.Next() {
		var username string
		if err := rows.Scan(&username); err != nil {
			return nil, 0, err
		}
		items = append(items, Principal{
			Username: username, Email: "baz", FirstName: "foo", LastName: "bar",
		})
	}
	return items, total, rows.Err()
}
