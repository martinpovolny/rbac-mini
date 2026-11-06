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
	if err := run(os.Args[1:], os.Getenv("DATABASE_URL")); err != nil {
		log.Fatal(err)
	}
}

func run(args []string, databaseURL string) error {
	cmd := "serve"
	rest := args
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, rest = args[0], args[1:]
	}
	open := func() (*store.DB, error) {
		db, err := openDatabase(databaseURL)
		if err != nil {
			return nil, err
		}
		return db, store.Migrate(db)
	}
	switch cmd {
	case "serve":
		fs := flag.NewFlagSet("serve", flag.ContinueOnError)
		addr := fs.String("addr", ":8080", "listen address")
		if err := fs.Parse(rest); err != nil {
			return err
		}
		bound, stop, err := startServer(databaseURL, *addr)
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
		keycloakURL := fs.String("keycloak-url", "", "Keycloak base URL")
		realm := fs.String("realm", "", "Keycloak realm")
		clientID := fs.String("client-id", "rbac-keycloak-sync", "client id")
		clientSecret := fs.String("client-secret", "", "client secret")
		org := fs.String("org", "", "org_id")
		group := fs.String("group", "CI Test Admin", "tenant group for synced users")
		prune := fs.Bool("prune-orphans", true, "delete principals gone from Keycloak")
		if err := fs.Parse(rest); err != nil {
			return err
		}
		if *keycloakURL == "" || *realm == "" || *org == "" {
			return fmt.Errorf("sync requires --keycloak-url, --realm and --org")
		}
		db, err := open()
		if err != nil {
			return err
		}
		defer db.Close()
		return keycloak.Sync(db, keycloak.Config{
			BaseURL: *keycloakURL, Realm: *realm,
			ClientID: *clientID, ClientSecret: *clientSecret,
			OrgID: *org, Group: *group, PruneOrphans: *prune,
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

// startServer opens, migrates, and serves on addr (use 127.0.0.1:0 for an
// ephemeral port). It returns the bound address and a stop func.
func startServer(databaseURL, addr string) (string, func(), error) {
	db, err := openDatabase(databaseURL)
	if err != nil {
		return "", nil, err
	}
	if err := store.Migrate(db); err != nil {
		db.Close()
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
