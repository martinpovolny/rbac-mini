package main

import (
	"encoding/json"
	"fmt"
	"net/http"
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
