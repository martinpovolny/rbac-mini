// Package testdb opens a scratch database for tests: :memory: SQLite by
// default, or an isolated Postgres database per test when
// RBAC_TEST_POSTGRES is set to a bootstrap DSN
// (e.g. postgres://user@127.0.0.1:55432/rbac_test?sslmode=disable).
// Postgres databases are dropped on cleanup, so packages can run in parallel.
package testdb

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strings"

	"rbac-go/internal/store"
)

// T is the subset of testing.T/B used here, so benchmarks can reuse Open.
type T interface {
	Helper()
	Fatal(args ...any)
	Fatalf(format string, args ...any)
	Logf(format string, args ...any)
	Cleanup(func())
}

func Open(t T) *store.DB {
	t.Helper()
	if dsn := os.Getenv("RBAC_TEST_POSTGRES"); dsn != "" {
		return openPostgres(t, dsn)
	}
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func openPostgres(t T, bootstrapDSN string) *store.DB {
	t.Helper()
	admin, err := store.OpenPostgres(bootstrapDSN)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	var b [4]byte
	_, _ = rand.Read(b[:])
	name := fmt.Sprintf("rbac_test_%d_%s", os.Getpid(), hex.EncodeToString(b[:]))
	if _, err := admin.Exec("CREATE DATABASE " + name); err != nil {
		admin.Close()
		t.Fatalf("create database: %v", err)
	}
	db, err := store.OpenPostgres(withDB(bootstrapDSN, name))
	if err != nil {
		admin.Close()
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() {
		db.Close()
		if _, err := admin.Exec("DROP DATABASE IF EXISTS " + name + " WITH (FORCE)"); err != nil {
			t.Logf("drop database %s: %v", name, err)
		}
		admin.Close()
	})
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func withDB(dsn, name string) string {
	// Replace the path component of postgres://.../<db>?... with /<name>.
	base, query, _ := strings.Cut(dsn, "?")
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[:i+1] + name
	}
	if query != "" {
		base += "?" + query
	}
	return base
}
