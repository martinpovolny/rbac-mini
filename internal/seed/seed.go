package seed

import (
	"database/sql"
	"errors"
)

// Seed loads the on-prem role/permission set (mirrors
// operator/internal/resources/rbac_seed/data/*.json plus
// `manage.py seeds --skip-notifications`). Idempotent: safe to run on every
// deploy like the operator migration Job.
func Seed(db *sql.DB) error {
	return errors.New("not implemented")
}
