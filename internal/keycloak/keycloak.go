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
	// Group is the tenant group synced users are added to.
	Group string
	// PruneOrphans deletes tenant principals gone from Keycloak.
	PruneOrphans bool
}

// Sync lists realm users via client-credentials and upserts them as tenant
// principals (lowercased usernames), adding them to Group. With PruneOrphans,
// tenant principals no longer present in Keycloak are deleted.
func Sync(db *store.DB, cfg Config) error {
	token, err := fetchToken(cfg)
	if err != nil {
		return err
	}
	users, err := fetchUsers(cfg, token)
	if err != nil {
		return err
	}
	return applyUsers(db, cfg, users)
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

func fetchUsers(cfg Config, token string) ([]string, error) {
	var users []string
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

func fetchUserPage(cfg Config, token string, first int) ([]string, error) {
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
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(body))
	for _, user := range body {
		if user.Username != "" {
			out = append(out, user.Username)
		}
	}
	return out, nil
}

func applyUsers(db *store.DB, cfg Config, users []string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var tenantID int64
	if err := tx.QueryRow(`SELECT id FROM tenants WHERE org_id=?`, cfg.OrgID).Scan(&tenantID); err != nil {
		return fmt.Errorf("unknown org %q: run bootstrap first: %w", cfg.OrgID, err)
	}
	var groupID int64
	if err := tx.QueryRow(`SELECT id FROM groups WHERE name=? AND tenant_id=?`,
		cfg.Group, tenantID).Scan(&groupID); err != nil {
		return fmt.Errorf("unknown group %q: run bootstrap first: %w", cfg.Group, err)
	}

	seen := map[string]bool{}
	for _, username := range users {
		username = strings.ToLower(username)
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
		if _, err := tx.Exec(`INSERT INTO group_principals(group_id, principal_id)
			VALUES(?, ?) ON CONFLICT(group_id, principal_id) DO NOTHING`,
			groupID, principalID); err != nil {
			return err
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
