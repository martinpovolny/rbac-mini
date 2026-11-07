package principal

import (
	"sort"
	"strings"

	"rbac-go/internal/store"
)

// Principal mirrors the on-prem GET /principals/ item shape. Keycloak profile
// fields are persisted by the sync (internal/keycloak), so the request path
// never calls Keycloak or the retired principal-profile sidecar.
type Principal struct {
	Username   string `json:"username"`
	Email      string `json:"email"`
	FirstName  string `json:"first_name"`
	LastName   string `json:"last_name"`
	IsActive   bool   `json:"is_active"`
	IsOrgAdmin bool   `json:"is_org_admin"`
	UserID     string `json:"user_id"`
	Type       string `json:"type"`
}

// Filter is the validated subset of GET /principals/ query parameters. Zero
// values follow the Django defaults: type=user, status=enabled,
// match_criteria=exact, ascending order.
type Filter struct {
	Type          string
	Usernames     []string
	MatchCriteria string
	Email         string
	Status        string
	AdminOnly     bool
	UsernameOnly  bool
	SortDesc      bool
	Limit         int
	Offset        int
	// GroupUUID restricts the result to members of one tenant group.
	GroupUUID string
}

// emailExpr is the effective email: the stored Keycloak address, or the
// synthetic <username>@local fallback used when none exists. `||` is portable
// across SQLite and Postgres.
const emailExpr = `CASE WHEN pr.email IS NULL OR pr.email = ''
	THEN pr.username || '@local' ELSE pr.email END`

// List returns tenant principals after the v1 filters, the filtered total for
// meta.count, and the requested page. Unknown usernames requested with
// match_criteria=exact are returned as BYPASS-shaped stubs (Django's
// "usersBy" behaviour), so the detail page can distinguish "not found" from
// "filtered out".
func List(db *store.DB, orgID string, f Filter) ([]Principal, int, error) {
	if f.Limit <= 0 {
		f.Limit = 10
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	principalType := f.Type
	if principalType == "" {
		principalType = "user"
	}
	status := f.Status
	if status == "" {
		status = "enabled"
	}
	criteria := f.MatchCriteria
	if criteria == "" {
		criteria = "exact"
	}

	where := []string{`t.org_id = ?`, `pr.cross_account = 0`}
	args := []any{orgID}
	if principalType != "all" {
		where = append(where, `pr.type = ?`)
		args = append(args, principalType)
	}
	if len(f.Usernames) > 0 {
		if criteria == "partial" {
			where = append(where, `LOWER(pr.username) LIKE ?`)
			args = append(args, strings.ToLower(f.Usernames[0])+"%")
		} else {
			placeholders := make([]string, len(f.Usernames))
			for i, name := range f.Usernames {
				placeholders[i] = "?"
				args = append(args, strings.ToLower(name))
			}
			where = append(where, `LOWER(pr.username) IN (`+strings.Join(placeholders, ",")+`)`)
		}
	}
	if f.Email != "" {
		if criteria == "partial" {
			where = append(where, `LOWER(`+emailExpr+`) LIKE ?`)
			args = append(args, strings.ToLower(f.Email)+"%")
		} else {
			where = append(where, `LOWER(`+emailExpr+`) = ?`)
			args = append(args, strings.ToLower(f.Email))
		}
	}
	switch status {
	case "enabled":
		where = append(where, `pr.is_active = 1`)
	case "disabled":
		where = append(where, `pr.is_active = 0`)
	}
	if f.AdminOnly {
		where = append(where, `pr.is_org_admin = 1`)
	}
	if f.GroupUUID != "" {
		where = append(where, `EXISTS (SELECT 1 FROM group_principals gp
			JOIN groups g ON g.id = gp.group_id
			WHERE gp.principal_id = pr.id AND g.uuid = ? AND g.tenant_id = t.id)`)
		args = append(args, f.GroupUUID)
	}

	order := " ASC"
	if f.SortDesc {
		order = " DESC"
	}
	q := `SELECT pr.username, ` + emailExpr + `,
		COALESCE(pr.first_name,''), COALESCE(pr.last_name,''),
		pr.is_active, pr.is_org_admin, COALESCE(pr.user_id,''), pr.type
		FROM principals pr JOIN tenants t ON t.id = pr.tenant_id
		WHERE ` + strings.Join(where, " AND ") + ` ORDER BY pr.username` + order
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()

	items := []Principal{}
	found := map[string]bool{}
	for rows.Next() {
		var item Principal
		var active, admin int
		if err := rows.Scan(&item.Username, &item.Email, &item.FirstName, &item.LastName,
			&active, &admin, &item.UserID, &item.Type); err != nil {
			return nil, 0, err
		}
		item.IsActive = active == 1
		item.IsOrgAdmin = admin == 1
		found[strings.ToLower(item.Username)] = true
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	if criteria != "partial" {
		items = append(items, unknownStubs(f.Usernames, found, status, f.AdminOnly, f.Email, criteria)...)
	}
	sortItems(items, f.SortDesc)

	total := len(items)
	if f.Offset >= total {
		return []Principal{}, total, nil
	}
	end := f.Offset + f.Limit
	if end > total {
		end = total
	}
	return items[f.Offset:end], total, nil
}

// unknownStubs returns the requested usernames that are not stored, shaped
// like Django's BYPASS payload (active, not org admin, <username>@local).
// They still honour the status, admin_only and email filters.
func unknownStubs(usernames []string, found map[string]bool,
	status string, adminOnly bool, email, criteria string) []Principal {
	if adminOnly || status == "disabled" {
		return nil
	}
	var out []Principal
	for _, name := range usernames {
		if name == "" || found[strings.ToLower(name)] {
			continue
		}
		stub := Principal{
			Username: name,
			Email:    name + "@local",
			IsActive: true,
			UserID:   name,
			Type:     "user",
		}
		if email != "" {
			if criteria == "partial" {
				if !strings.HasPrefix(strings.ToLower(stub.Email), strings.ToLower(email)) {
					continue
				}
			} else if !strings.EqualFold(stub.Email, email) {
				continue
			}
		}
		out = append(out, stub)
	}
	return out
}

func sortItems(items []Principal, desc bool) {
	sort.SliceStable(items, func(i, j int) bool {
		a, b := strings.ToLower(items[i].Username), strings.ToLower(items[j].Username)
		if desc {
			return a > b
		}
		return a < b
	})
}
