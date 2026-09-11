// stoneage-admin serves the authenticated web control plane and provides the
// one-time bootstrap/migration commands needed on a new server.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/admin"
	"github.com/k0ngk0ng/stoneage/internal/auth"
	"github.com/k0ngk0ng/stoneage/internal/gamecatalog"
	"github.com/k0ngk0ng/stoneage/internal/playerassets"
	"github.com/k0ngk0ng/stoneage/internal/playerbridge"
	"github.com/k0ngk0ng/stoneage/internal/playermanager"
)

var legacyCharacterFile = regexp.MustCompile(`^(.+)\.[0-9]+\.char$`)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "serve":
		err = serve(os.Args[2:])
	case "create-admin":
		err = createAdmin(os.Args[2:])
	case "import-legacy":
		err = importLegacy(os.Args[2:])
	case "help", "-h", "--help":
		usage()
		return
	default:
		usage()
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "stoneage-admin: %v\n", err)
		os.Exit(1)
	}
}

func serve(arguments []string) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	databasePath := flags.String("db", envOr("STONEAGE_AUTH_DB", "data/stoneage-auth.db"), "SQLite account database")
	listenAddress := flags.String("listen", envOr("STONEAGE_ADMIN_LISTEN", "127.0.0.1:8080"), "HTTP listen address (put HTTPS proxy in front)")
	setupToken := flags.String("setup-token", os.Getenv("STONEAGE_ADMIN_SETUP_TOKEN"), "one-time web setup token")
	configPath := flags.String("config", os.Getenv("STONEAGE_SERVER_CONFIG"), "GMSV setup.cf path")
	saacConfigPath := flags.String("saac-config", os.Getenv("STONEAGE_SAAC_CONFIG"), "SAAC acserv.cf path")
	operatorSocket := flags.String("operator-socket", os.Getenv("STONEAGE_OPERATOR_SOCKET"), "restricted operator Unix socket")
	trustedProxies := flags.String("trusted-proxies", envOr("STONEAGE_ADMIN_TRUSTED_PROXIES", "127.0.0.1/32,::1/128"), "comma-separated trusted reverse-proxy IPs or CIDRs")
	cookieSecure := flags.Bool("cookie-secure", envBool("STONEAGE_ADMIN_COOKIE_SECURE", false), "set Secure on admin session cookies")
	playerRoot := flags.String("player-admin-root", envOr("STONEAGE_PLAYER_ADMIN_ROOT", "/run/stoneage/player-admin"), "private legacy player management queues")
	catalogRoot := flags.String("player-catalog-root", envOr("STONEAGE_PLAYER_CATALOG_ROOT", "/game/gmsv/data"), "native game catalog directory")
	assetsRoot := flags.String("player-assets-root", envOr("STONEAGE_PLAYER_ASSETS_ROOT", "/opt/stoneage/web-assets"), "native Web sprite directory")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	store, err := openStore(*databasePath)
	if err != nil {
		return err
	}
	defer store.Close()
	hasAdmins, err := store.HasAdmins(context.Background())
	if err != nil {
		return err
	}
	if !hasAdmins {
		bootstrapUser, bootstrapPassword := os.Getenv("STONEAGE_ADMIN_USER"), os.Getenv("STONEAGE_ADMIN_PASSWORD")
		if bootstrapUser != "" || bootstrapPassword != "" {
			if bootstrapUser == "" || bootstrapPassword == "" {
				return errors.New("STONEAGE_ADMIN_USER and STONEAGE_ADMIN_PASSWORD must be provided together")
			}
			if _, err := store.CreateAdmin(context.Background(), bootstrapUser, []byte(bootstrapPassword)); err != nil {
				return fmt.Errorf("bootstrap administrator: %w", err)
			}
			log.Printf("created bootstrap administrator %q", bootstrapUser)
		}
	}
	var operator admin.Operator
	if *operatorSocket != "" {
		operator = admin.UnixOperator{Socket: *operatorSocket}
	}
	catalogLoader := gamecatalog.NewLoader(func() (*gamecatalog.Catalog, error) {
		return gamecatalog.Load(*catalogRoot)
	})
	queue := func(role string) playerbridge.Queue {
		return playerbridge.Queue{
			Requests:  filepath.Join(*playerRoot, role, "requests"),
			Responses: filepath.Join(*playerRoot, role, "responses"),
		}
	}
	players := &playermanager.Manager{
		Archives:      playerbridge.SAAC{Queue: queue("saac")},
		Game:          playerbridge.GMSV{Queue: queue("gmsv")},
		CatalogLoader: catalogLoader.Load,
	}
	control, err := admin.NewServer(store, admin.Options{
		Players:             players,
		PlayerCatalogLoader: catalogLoader.Load,
		PlayerAssets:        &playerassets.Handler{Root: *assetsRoot},
		CookieSecure:        *cookieSecure,
		TrustedProxies:      strings.Split(*trustedProxies, ","),
		SetupToken:          *setupToken,
		Operator:            operator,
		Config:              admin.ConfigManager{Path: *configPath},
		SAACConfig:          admin.ConfigManager{Path: *saacConfigPath, Service: "saac"},
	})
	if err != nil {
		return err
	}
	server := &http.Server{
		Addr:              *listenAddress,
		Handler:           control.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	log.Printf("StoneAge admin listening on %s", *listenAddress)
	return server.ListenAndServe()
}

func createAdmin(arguments []string) error {
	flags := flag.NewFlagSet("create-admin", flag.ContinueOnError)
	databasePath := flags.String("db", envOr("STONEAGE_AUTH_DB", "data/stoneage-auth.db"), "SQLite account database")
	username := flags.String("username", "", "administrator username")
	password := flags.String("password", "", "administrator password (prefer -password-stdin)")
	passwordStdin := flags.Bool("password-stdin", false, "read administrator password from stdin")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if *username == "" {
		return errors.New("-username is required")
	}
	secret := *password
	if *passwordStdin {
		reader := bufio.NewReader(os.Stdin)
		value, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return fmt.Errorf("read password: %w", err)
		}
		secret = strings.TrimSuffix(strings.TrimSuffix(value, "\n"), "\r")
	}
	if secret == "" {
		return errors.New("password is required")
	}
	store, err := openStore(*databasePath)
	if err != nil {
		return err
	}
	defer store.Close()
	adminUser, err := store.CreateAdmin(context.Background(), *username, []byte(secret))
	if err != nil {
		return err
	}
	fmt.Printf("created administrator %s (id=%d)\n", adminUser.Username, adminUser.ID)
	return nil
}

func importLegacy(arguments []string) error {
	flags := flag.NewFlagSet("import-legacy", flag.ContinueOnError)
	databasePath := flags.String("db", envOr("STONEAGE_AUTH_DB", "data/stoneage-auth.db"), "SQLite account database")
	characterDirectory := flags.String("char-dir", envOr("STONEAGE_CHAR_DIR", "runtime/legacy-server/saac/char"), "SAAC character directory")
	defaultPassword := flags.String("default-password", "", "password assigned to imported accounts (otherwise disabled)")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	store, err := openStore(*databasePath)
	if err != nil {
		return err
	}
	defer store.Close()
	accounts, err := legacyAccounts(*characterDirectory)
	if err != nil {
		return err
	}
	ctx := context.Background()
	created := 0
	for _, username := range accounts {
		inserted, err := store.ImportLegacyAccount(ctx, username, []byte(*defaultPassword))
		if err != nil {
			return fmt.Errorf("import %s: %w", username, err)
		}
		if inserted {
			created++
		}
	}
	if *defaultPassword == "" {
		fmt.Printf("imported %d accounts as disabled; assign passwords in the web console\n", created)
	} else {
		fmt.Printf("imported %d active accounts\n", created)
	}
	return nil
}

func legacyAccounts(directory string) ([]string, error) {
	seen := map[string]struct{}{}
	err := filepath.WalkDir(directory, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		match := legacyCharacterFile.FindStringSubmatch(entry.Name())
		if len(match) != 2 {
			return nil
		}
		seen[match[1]] = struct{}{}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan legacy characters: %w", err)
	}
	accounts := make([]string, 0, len(seen))
	for username := range seen {
		accounts = append(accounts, username)
	}
	sort.Strings(accounts)
	return accounts, nil
}

func openStore(path string) (*auth.Store, error) {
	store, err := auth.Open(path)
	if err != nil {
		return nil, err
	}
	if err := store.Migrate(context.Background()); err != nil {
		store.Close()
		return nil, err
	}
	return store, nil
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  stoneage-admin serve [flags]
  stoneage-admin create-admin -username NAME [-password-stdin]
  stoneage-admin import-legacy -char-dir PATH [-default-password PASSWORD]

serve environment:
  STONEAGE_AUTH_DB, STONEAGE_ADMIN_LISTEN, STONEAGE_ADMIN_SETUP_TOKEN,
  STONEAGE_ADMIN_USER, STONEAGE_ADMIN_PASSWORD, STONEAGE_SERVER_CONFIG,
  STONEAGE_OPERATOR_SOCKET, STONEAGE_SAAC_CONFIG, STONEAGE_ADMIN_COOKIE_SECURE`)
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func envBool(name string, fallback bool) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}
