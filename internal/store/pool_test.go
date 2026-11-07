package store

import "testing"

func TestOpenPostgresSizesPool(t *testing.T) {
	db, err := OpenPostgres("postgres://user@127.0.0.1:1/unused?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if got := db.Stats().MaxOpenConnections; got != postgresMaxOpenConns {
		t.Fatalf("MaxOpenConnections = %d, want %d", got, postgresMaxOpenConns)
	}
}
