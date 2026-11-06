// Command rbac-go is the minimal on-prem RBAC v1 replacement (SPEC doc/SPEC.md).
//
//	rbac-go serve [-addr :8080]            run the API (default)
//	rbac-go migrate                        apply the portable schema
//	rbac-go seed                            migrate + load Cost Administrator set
//	rbac-go bootstrap --org --account --users a,b,c
//
// DATABASE_URL selects the backend: sqlite:///path/to.db (dev/test),
// sqlite://:memory:, or postgres://... (prod/operator).
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"

	"rbac-go/internal/keycloak"
	"rbac-go/internal/seed"
	"rbac-go/internal/server"
	"rbac-go/internal/store"
)

func main() {
	if err := run(os.Args[1:], resolveDatabaseURL(os.Getenv)); err != nil {
		log.Fatal(err)
	}
}

// envOr returns the env value or a default.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// envBool parses a bool env with a default.
func envBool(key string, def bool) bool {
	switch os.Getenv(key) {
	case "true", "1", "yes":
		return true
	case "false", "0", "no":
		return false
	default:
		return def
	}
}

// resolveDatabaseURL prefers DATABASE_URL, else assembles postgres:// from
// the operator-injected DATABASE_{HOST,PORT,NAME,USER,PASSWORD} (SPEC §5),
// else falls back to a local dev SQLite file.
func resolveDatabaseURL(getenv func(string) string) string {
	if url := getenv("DATABASE_URL"); url != "" {
		return url
	}
	if host := getenv("DATABASE_HOST"); host != "" {
		user := getenv("DATABASE_USER")
		pass := getenv("DATABASE_PASSWORD")
		port := getenv("DATABASE_PORT")
		if port == "" {
			port = "5432"
		}
		name := getenv("DATABASE_NAME")
		auth := user
		if pass != "" {
			auth = user + ":" + pass
		}
		if auth != "" {
			auth += "@"
		}
		return "postgres://" + auth + host + ":" + port + "/" + name + "?sslmode=disable"
	}
	return "sqlite://./rbac.db"
}

func run(args []string, databaseURL string) error {
	cmd := "serve"
	rest := args
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, rest = args[0], args[1:]
	}
	open := func() (*store.DB, error) {
		return prepareDB(databaseURL, false)
	}
	switch cmd {
	case "serve":
		fs := flag.NewFlagSet("serve", flag.ContinueOnError)
		addr := fs.String("addr", ":8080", "listen address")
		withSeed := fs.Bool("seed", false, "run seed on startup (idempotent; operator Job covers this by default)")
		if err := fs.Parse(rest); err != nil {
			return err
		}
		bound, stop, err := startServerWith(databaseURL, *addr, *withSeed)
		if err != nil {
			return err
		}
		defer stop()
		log.Printf("rbac-go listening on %s", bound)
		select {}
	case "migrate":
		db, err := open()
		if err != nil {
			return err
		}
		defer db.Close()
		return nil
	case "seed":
		db, err := open()
		if err != nil {
			return err
		}
		defer db.Close()
		return seed.Seed(db)
	case "bootstrap":
		fs := flag.NewFlagSet("bootstrap", flag.ContinueOnError)
		org := fs.String("org", "", "org_id")
		account := fs.String("account", "", "account number")
		users := fs.String("users", "", "comma-separated usernames")
		if err := fs.Parse(rest); err != nil {
			return err
		}
		if *org == "" || *users == "" {
			return fmt.Errorf("bootstrap requires --org and --users")
		}
		db, err := open()
		if err != nil {
			return err
		}
		defer db.Close()
		return seed.Bootstrap(db, *org, *account, strings.Split(*users, ","))
	case "sync":
		fs := flag.NewFlagSet("sync", flag.ContinueOnError)
		keycloakURL := fs.String("keycloak-url", os.Getenv("KEYCLOAK_URL"), "Keycloak base URL [$KEYCLOAK_URL]")
		realm := fs.String("realm", os.Getenv("KEYCLOAK_REALM"), "Keycloak realm [$KEYCLOAK_REALM]")
		clientID := fs.String("client-id", envOr("KEYCLOAK_CLIENT_ID", "rbac-keycloak-sync"), "client id [$KEYCLOAK_CLIENT_ID]")
		clientSecret := fs.String("client-secret", os.Getenv("KEYCLOAK_CLIENT_SECRET"), "client secret [$KEYCLOAK_CLIENT_SECRET]")
		org := fs.String("org", os.Getenv("SYNC_ORG_ID"), "org_id: single-org sync [$SYNC_ORG_ID]")
		orgGroupPrefix := fs.String("org-group-prefix", envOr("SYNC_ORG_GROUP_PREFIX", "org-"), "discover orgs from Keycloak groups with this prefix when --org is empty [$SYNC_ORG_GROUP_PREFIX]")
		group := fs.String("group", envOr("SYNC_GROUP", "CI Test Admin"), "tenant group for synced users [$SYNC_GROUP]")
		prune := fs.Bool("prune-orphans", envBool("SYNC_PRUNE_ORPHANS", true), "delete principals gone from Keycloak [$SYNC_PRUNE_ORPHANS]")
		if err := fs.Parse(rest); err != nil {
			return err
		}
		if *keycloakURL == "" || *realm == "" {
			return fmt.Errorf("sync requires --keycloak-url and --realm (or KEYCLOAK_URL/KEYCLOAK_REALM env)")
		}
		db, err := open()
		if err != nil {
			return err
		}
		defer db.Close()
		return keycloak.Sync(db, keycloak.Config{
			BaseURL: *keycloakURL, Realm: *realm,
			ClientID: *clientID, ClientSecret: *clientSecret,
			OrgID: *org, OrgGroupPrefix: *orgGroupPrefix,
			Group: *group, PruneOrphans: *prune,
		})
	default:
		return fmt.Errorf("unknown command %q (serve|migrate|seed|bootstrap|sync)", cmd)
	}
}

// openDatabase maps DATABASE_URL to a backend: sqlite:///path (dev/test),
// sqlite://:memory:, or postgres://... (prod/operator).
func openDatabase(databaseURL string) (*store.DB, error) {
	if rest, ok := strings.CutPrefix(databaseURL, "sqlite://"); ok {
		if rest == "" || rest == ":memory:" {
			rest = "file::memory:?cache=shared"
		}
		return store.OpenSQLite(rest)
	}
	if strings.HasPrefix(databaseURL, "postgres://") || strings.HasPrefix(databaseURL, "postgresql://") {
		return store.OpenPostgres(databaseURL)
	}
	return nil, fmt.Errorf("unsupported DATABASE_URL %q (want sqlite:// or postgres://)", databaseURL)
}

// prepareDB is the shared startup path (the distroless-friendly "startup
// script"): open, migrate, and optionally seed. Used by serve --seed and by
// the migrate/seed/bootstrap/sync commands via run().
func prepareDB(databaseURL string, withSeed bool) (*store.DB, error) {
	db, err := openDatabase(databaseURL)
	if err != nil {
		return nil, err
	}
	if err := store.Migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	if withSeed {
		if err := seed.Seed(db); err != nil {
			db.Close()
			return nil, err
		}
	}
	return db, nil
}

// startServer opens, migrates, and serves on addr (use 127.0.0.1:0 for an
// ephemeral port). It returns the bound address and a stop func.
func startServer(databaseURL, addr string) (string, func(), error) {
	return startServerWith(databaseURL, addr, false)
}

func startServerWith(databaseURL, addr string, withSeed bool) (string, func(), error) {
	db, err := prepareDB(databaseURL, withSeed)
	if err != nil {
		return "", nil, err
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		db.Close()
		return "", nil, err
	}
	go func() {
		_ = http.Serve(ln, server.New(db))
	}()
	return ln.Addr().String(), func() {
		_ = ln.Close()
		_ = db.Close()
	}, nil
}
