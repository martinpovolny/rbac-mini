.PHONY: test test-sqlite test-postgres build vet fmt

# Scratch Postgres for test-postgres (override to point elsewhere):
#   initdb -D /tmp/rbac-pg/data -E UTF8 --auth=trust
#   pg_ctl -D /tmp/rbac-pg/data -o "-p 55432 -c listen_addresses='127.0.0.1'" -l /tmp/rbac-pg/log start
#   psql -h 127.0.0.1 -p 55432 -d postgres -c "CREATE DATABASE rbac_test;"
RBAC_TEST_POSTGRES ?= postgres://martinpovolny@127.0.0.1:55432/rbac_test?sslmode=disable

test: test-sqlite

test-sqlite:
	go test ./... -count=1

# -p 1 serializes packages against the shared scratch instance.
test-postgres:
	RBAC_TEST_POSTGRES='$(RBAC_TEST_POSTGRES)' go test ./... -count=1 -p 1

build:
	go build ./...

vet:
	go vet ./...

fmt:
	gofmt -l .
