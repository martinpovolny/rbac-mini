package principal

import (
	"errors"

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
	return nil, 0, errors.New("not implemented")
}
