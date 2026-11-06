package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// TestServeAnswersStatus boots the real binary path (open + migrate + listen)
// on a scratch SQLite file and checks the probe, like the operator rollout
// check does against the deployed pod.
func TestServeAnswersStatus(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "rbac.db")
	addr, stop, err := startServer("sqlite://"+dbPath, "127.0.0.1:0")
	if err != nil {
		t.Fatalf("startServer: %v", err)
	}
	defer stop()
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("sqlite file not created: %v", err)
	}

	resp, err := http.Get(fmt.Sprintf("http://%s/api/rbac/v1/status/", addr))
	if err != nil {
		t.Fatalf("GET /status/: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["api_version"] != float64(1) {
		t.Errorf("api_version = %v, want 1", body["api_version"])
	}
}

// stubKeycloak emulates the token + users endpoints the sync needs.
func stubKeycloak(t *testing.T, users []string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/realms/onprem/protocol/openid-connect/token", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "test-token"})
	})
	mux.HandleFunc("/admin/realms/onprem/users", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("users auth = %q, want Bearer test-token", r.Header.Get("Authorization"))
		}
		out := make([]map[string]any, 0, len(users))
		for _, u := range users {
			out = append(out, map[string]any{"username": u})
		}
		_ = json.NewEncoder(w).Encode(out)
	})
	return httptest.NewServer(mux)
}

func principals(t *testing.T, dsn, org string) map[string]bool {
	t.Helper()
	db, err := openDatabase(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT username FROM principals pr
		JOIN tenants t ON t.id = pr.tenant_id WHERE t.org_id=?`, org)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			t.Fatal(err)
		}
		out[u] = true
	}
	return out
}

// TestSyncCommand wires the operator CronJob equivalent end to end:
// migrate → seed → bootstrap → sync against a stub Keycloak prunes stale.
func TestSyncCommand(t *testing.T) {
	dsn := "sqlite://" + filepath.Join(t.TempDir(), "rbac.db")
	steps := [][]string{
		{"migrate"},
		{"seed"},
		{"bootstrap", "--org", "100001", "--account", "1111111", "--users", "alice,stale"},
	}
	for _, args := range steps {
		if err := run(args, dsn); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}

	srv := stubKeycloak(t, []string{"alice", "bob"})
	defer srv.Close()
	err := run([]string{"sync",
		"--keycloak-url", srv.URL, "--realm", "onprem",
		"--client-id", "rbac-keycloak-sync", "--client-secret", "s3cret",
		"--org", "100001", "--group", "CI Test Admin", "--prune-orphans"}, dsn)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}

	got := principals(t, dsn, "100001")
	if !got["alice"] || !got["bob"] {
		t.Errorf("principals = %v, want alice and bob", got)
	}
	if got["stale"] {
		t.Errorf("principals = %v, want stale pruned", got)
	}
}

// TestRunFlagErrors pins the CLI contract: unknown commands, missing required
// flags and bad backends fail fast with an error instead of serving half
// configured (operator Job/CronJob would otherwise retry-loop).
func TestRunFlagErrors(t *testing.T) {
	dsn := "sqlite://" + filepath.Join(t.TempDir(), "rbac.db")
	for name, args := range map[string][]string{
		"unknown command":   {"frobnicate"},
		"bootstrap no org":  {"bootstrap", "--users", "a"},
		"sync no url":       {"sync", "--realm", "x", "--org", "y"},
		"serve bad backend": {"serve", "--addr", "127.0.0.1:0"},
	} {
		dbURL := dsn
		if name == "serve bad backend" {
			dbURL = "bogus://nope"
		}
		if err := run(args, dbURL); err == nil {
			t.Errorf("%s: run succeeded, want error", name)
		}
	}
	if err := run([]string{"serve", "-h"}, dsn); err != flag.ErrHelp {
		t.Errorf("serve -h: err = %v, want flag.ErrHelp", err)
	}
}

// TestResolveDatabaseURL: DATABASE_URL wins; otherwise the operator's legacy
// DATABASE_{HOST,PORT,NAME,USER,PASSWORD} assembles a postgres URL. The
// operator always injects those, so the binary must not require DATABASE_URL.
func TestResolveDatabaseURL(t *testing.T) {
	get := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}
	if got := resolveDatabaseURL(get(map[string]string{
		"DATABASE_URL":  "sqlite:///x.db",
		"DATABASE_HOST": "db", "DATABASE_PORT": "5432",
		"DATABASE_NAME": "n", "DATABASE_USER": "u", "DATABASE_PASSWORD": "p",
	})); got != "sqlite:///x.db" {
		t.Errorf("explicit URL = %q, want sqlite:///x.db", got)
	}
	got := resolveDatabaseURL(get(map[string]string{
		"DATABASE_HOST": "db", "DATABASE_PORT": "5432",
		"DATABASE_NAME": "costonprem_rbac", "DATABASE_USER": "rbac", "DATABASE_PASSWORD": "p",
	}))
	want := "postgres://rbac:p@db:5432/costonprem_rbac?sslmode=disable"
	if got != want {
		t.Errorf("legacy env = %q, want %q", got, want)
	}
	if got := resolveDatabaseURL(get(map[string]string{})); got == "" {
		t.Errorf("empty env resolved empty, want dev default")
	}
}

// TestPrepareDBSeeds is the distroless-friendly "startup script": opening the
// DB through one helper that migrates and optionally seeds, shared by serve
// and the operator migration Job path.
func TestPrepareDBSeeds(t *testing.T) {
	dsn := "sqlite://" + t.TempDir() + "/rbac.db"
	db, err := prepareDB(dsn, true)
	if err != nil {
		t.Fatalf("prepareDB: %v", err)
	}
	db.Close()
	var n int
	db2, err := openDatabase(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	if err := db2.QueryRow(`SELECT COUNT(*) FROM roles WHERE name='Cost Administrator'`).Scan(&n); err != nil || n != 1 {
		t.Errorf("Cost Administrator rows = %d, err = %v; want 1", n, err)
	}
}

// TestSyncEnvFallback mirrors the operator CronJob: static bits come from
// env (KEYCLOAK_*, SYNC_*) with flags overriding, so the same env block
// serves both backends.
func TestSyncEnvFallback(t *testing.T) {
	dsn := "sqlite://" + filepath.Join(t.TempDir(), "rbac.db")
	for _, args := range [][]string{{"migrate"}, {"seed"}, {"bootstrap", "--org", "100001", "--users", "alice"}} {
		if err := run(args, dsn); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	srv := stubKeycloak(t, []string{"alice", "bob"})
	defer srv.Close()
	t.Setenv("KEYCLOAK_URL", srv.URL)
	t.Setenv("KEYCLOAK_REALM", "onprem")
	t.Setenv("KEYCLOAK_CLIENT_ID", "rbac-keycloak-sync")
	t.Setenv("KEYCLOAK_CLIENT_SECRET", "s3cret")
	t.Setenv("SYNC_ORG_GROUP_PREFIX", "org-")
	if err := run([]string{"sync", "--org", "100001", "--group", "CI Test Admin", "--prune-orphans=false"}, dsn); err != nil {
		t.Fatalf("sync via env: %v", err)
	}
	got := principals(t, dsn, "100001")
	if !got["alice"] || !got["bob"] {
		t.Errorf("principals = %v, want alice and bob", got)
	}
}
