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
