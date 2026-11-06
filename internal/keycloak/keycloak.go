package keycloak

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"rbac-go/internal/store"
)

// Config mirrors the operator keycloakSync spec
// (operator/internal/resources/rbac_keycloak_sync.go).
type Config struct {
	BaseURL      string
	Realm        string
	ClientID     string
	ClientSecret string
	OrgID        string
	// OrgGroupPrefix, when OrgID is empty, discovers orgs by listing Keycloak
	// groups with this prefix (mirrors discover_and_sync).
	OrgGroupPrefix string
	// Group is the tenant group synced users are added to. Empty means no
	// group assignment (principals only, as in sync_keycloak_principals.py).
	Group string
	// PruneOrphans deletes tenant principals gone from Keycloak.
	PruneOrphans bool
}

// Sync lists realm users via client-credentials and upserts them as tenant
// principals (lowercased usernames), adding them to Group. With PruneOrphans,
// tenant principals no longer present in Keycloak are deleted. Without OrgID,
// orgs are discovered from Keycloak groups with OrgGroupPrefix.
func Sync(db *store.DB, cfg Config) error {
	token, err := fetchToken(cfg)
	if err != nil {
		return err
	}
	if cfg.OrgID != "" {
		users, err := fetchUsers(cfg, token)
		if err != nil {
			return err
		}
		return applyOrg(db, cfg, cfg.OrgID, "", users)
	}
	groups, err := listGroups(cfg, token)
	if err != nil {
		return err
	}
	for _, g := range groups {
		members, err := fetchGroupMembers(cfg, token, g.ID)
		if err != nil {
			return err
		}
		if err := applyOrg(db, cfg, g.OrgID, g.AccountNumber, members); err != nil {
			return err
		}
	}
	return nil
}

var httpClient = &http.Client{Timeout: 30 * time.Second}

func fetchToken(cfg Config) (string, error) {
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {cfg.ClientID},
		"client_secret": {cfg.ClientSecret},
	}
	resp, err := httpClient.PostForm(
		strings.TrimSuffix(cfg.BaseURL, "/")+"/realms/"+cfg.Realm+"/protocol/openid-connect/token", form)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token endpoint: %s", resp.Status)
	}
	var body struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", err
	}
	if body.AccessToken == "" {
		return "", fmt.Errorf("token endpoint: empty access_token")
	}
	return body.AccessToken, nil
}

func fetchUsers(cfg Config, token string) ([]kcUser, error) {
	var users []kcUser
	for first := 0; ; first += 100 {
		page, err := fetchUserPage(cfg, token, first)
		if err != nil {
			return nil, err
		}
		if len(page) == 0 {
			break
		}
		users = append(users, page...)
		if len(page) < 100 {
			break
		}
	}
	return users, nil
}

// kcUser mirrors the Keycloak user fields the sync needs.
type kcUser struct {
	Username string
	Enabled  *bool
}

func (u kcUser) usable() bool {
	if strings.HasPrefix(strings.ToLower(u.Username), "service-account-") {
		return false
	}
	return u.Enabled == nil || *u.Enabled
}

type kcGroup struct {
	ID            string
	OrgID         string
	AccountNumber string
}

func keycloakGet(cfg Config, token, path string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodGet, strings.TrimSuffix(cfg.BaseURL, "/")+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return httpClient.Do(req)
}

// listGroups discovers org groups by prefix (mirrors discover_and_sync).
func listGroups(cfg Config, token string) ([]kcGroup, error) {
	prefix := cfg.OrgGroupPrefix
	if prefix == "" {
		prefix = "org-"
	}
	var out []kcGroup
	for first := 0; ; first += 100 {
		u := "/admin/realms/" + cfg.Realm + "/groups?search=" + url.QueryEscape(prefix) +
			"&briefRepresentation=false&first=" + strconv.Itoa(first) + "&max=100"
		resp, err := keycloakGet(cfg, token, u)
		if err != nil {
			return nil, err
		}
		var body []struct {
			ID         string              `json:"id"`
			Name       string              `json:"name"`
			Attributes map[string][]string `json:"attributes"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			resp.Body.Close()
			return nil, err
		}
		resp.Body.Close()
		if len(body) == 0 {
			break
		}
		for _, g := range body {
			if !strings.HasPrefix(g.Name, prefix) {
				continue
			}
			org := firstOf(g.Attributes["org_id"])
			if org == "" {
				continue
			}
			out = append(out, kcGroup{ID: g.ID, OrgID: org, AccountNumber: firstOf(g.Attributes["account_number"])})
		}
		if len(body) < 100 {
			break
		}
	}
	return out, nil
}

func firstOf(v []string) string {
	if len(v) == 0 {
		return ""
	}
	return v[0]
}

// fetchGroupMembers pages an org group's members (mirrors the sync loop).
func fetchGroupMembers(cfg Config, token, groupID string) ([]kcUser, error) {
	var out []kcUser
	for first := 0; ; first += 100 {
		u := "/admin/realms/" + cfg.Realm + "/groups/" + groupID + "/members" +
			"?briefRepresentation=false&first=" + strconv.Itoa(first) + "&max=100"
		resp, err := keycloakGet(cfg, token, u)
		if err != nil {
			return nil, err
		}
		var body []struct {
			Username string `json:"username"`
			Enabled  *bool  `json:"enabled"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			resp.Body.Close()
			return nil, err
		}
		resp.Body.Close()
		if len(body) == 0 {
			break
		}
		for _, m := range body {
			out = append(out, kcUser{Username: m.Username, Enabled: m.Enabled})
		}
		if len(body) < 100 {
			break
		}
	}
	return out, nil
}

func fetchUserPage(cfg Config, token string, first int) ([]kcUser, error) {
	u := strings.TrimSuffix(cfg.BaseURL, "/") + "/admin/realms/" + cfg.Realm +
		"/users?first=" + strconv.Itoa(first) + "&max=100"
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return nil, fmt.Errorf("users endpoint: %s: %s", resp.Status, detail)
	}
	var body []struct {
		Username string `json:"username"`
		Enabled  *bool  `json:"enabled"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	out := make([]kcUser, 0, len(body))
	for _, user := range body {
		if user.Username != "" {
			out = append(out, kcUser{Username: user.Username, Enabled: user.Enabled})
		}
	}
	return out, nil
}

// applyOrg ensures the tenant and upserts its principals (mirrors sync()).
// Principals join cfg.Group only when set; the Keycloak discovery path syncs
// principals without group assignment, like the Django script.
func applyOrg(db *store.DB, cfg Config, orgID, accountNumber string, users []kcUser) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var tenantID int64
	err = tx.QueryRow(`SELECT id FROM tenants WHERE org_id=?`, orgID).Scan(&tenantID)
	if err == sql.ErrNoRows {
		if _, err := tx.Exec(`INSERT INTO tenants(org_id, tenant_name, account_id, ready)
			VALUES(?, 'acct'||?, ?, 1) ON CONFLICT(org_id) DO NOTHING`,
			orgID, accountNumber, accountNumber); err != nil {
			return err
		}
		if err := tx.QueryRow(`SELECT id FROM tenants WHERE org_id=?`, orgID).Scan(&tenantID); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	var groupID int64
	if cfg.Group != "" {
		if err := tx.QueryRow(`SELECT id FROM groups WHERE name=? AND tenant_id=?`,
			cfg.Group, tenantID).Scan(&groupID); err != nil {
			return fmt.Errorf("unknown group %q: run bootstrap first: %w", cfg.Group, err)
		}
	}

	seen := map[string]bool{}
	for _, u := range users {
		if !u.usable() {
			continue
		}
		username := strings.ToLower(u.Username)
		seen[username] = true
		var principalID int64
		err := tx.QueryRow(`SELECT id FROM principals WHERE username=? AND tenant_id=?`,
			username, tenantID).Scan(&principalID)
		if err == sql.ErrNoRows {
			if _, err := tx.Exec(`INSERT INTO principals(uuid, username, type, tenant_id)
				VALUES(?, ?, 'user', ?) ON CONFLICT(username, tenant_id) DO NOTHING`,
				store.NewUUID(), username, tenantID); err != nil {
				return err
			}
			if err := tx.QueryRow(`SELECT id FROM principals WHERE username=? AND tenant_id=?`,
				username, tenantID).Scan(&principalID); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		if cfg.Group != "" {
			if _, err := tx.Exec(`INSERT INTO group_principals(group_id, principal_id)
				VALUES(?, ?) ON CONFLICT(group_id, principal_id) DO NOTHING`,
				groupID, principalID); err != nil {
				return err
			}
		}
	}

	if cfg.PruneOrphans && len(seen) > 0 {
		placeholders := make([]string, 0, len(seen))
		args := []any{tenantID}
		for u := range seen {
			placeholders = append(placeholders, "?")
			args = append(args, u)
		}
		q := `DELETE FROM principals WHERE tenant_id=? AND type='user'
			AND username NOT IN (` + strings.Join(placeholders, ",") + `)`
		if _, err := tx.Exec(q, args...); err != nil {
			return err
		}
	}
	return tx.Commit()
}
