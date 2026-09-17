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
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/admin"
	"github.com/k0ngk0ng/stoneage/internal/aifunding"
	"github.com/k0ngk0ng/stoneage/internal/aimodels"
	"github.com/k0ngk0ng/stoneage/internal/airuntime"
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
	signalCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	databasePath := flags.String("db", envOr("STONEAGE_AUTH_DB", "data/stoneage-auth.db"), "SQLite account database")
	listenAddress := flags.String("listen", envOr("STONEAGE_ADMIN_LISTEN", "127.0.0.1:8080"), "HTTP listen address (put HTTPS proxy in front)")
	setupToken := flags.String("setup-token", os.Getenv("STONEAGE_ADMIN_SETUP_TOKEN"), "one-time web setup token")
	configPath := flags.String("config", os.Getenv("STONEAGE_SERVER_CONFIG"), "GMSV setup.cf path")
	saacConfigPath := flags.String("saac-config", os.Getenv("STONEAGE_SAAC_CONFIG"), "SAAC acserv.cf path")
	operatorSocket := flags.String("operator-socket", os.Getenv("STONEAGE_OPERATOR_SOCKET"), "restricted operator Unix socket")
	aiDatabasePath := flags.String("ai-db", os.Getenv("STONEAGE_AI_DB"), "AI runtime SQLite database (defaults beside the admin database)")
	aiSecretDir := flags.String("ai-secrets", os.Getenv("STONEAGE_AI_SECRET_DIR"), "private AI model-key directory (defaults beside the admin database)")
	aiModelKeyFile := flags.String("ai-model-key-file", os.Getenv("STONEAGE_AI_MODEL_KEY_FILE"), "optional private DeepSeek API-key file used for first-start bootstrap")
	aiCodexBinary := flags.String("ai-codex-binary", os.Getenv("STONEAGE_AI_CODEX_BINARY"), "absolute server Codex executable path (enables explicit model connection tests)")
	aiCodexWorkRoot := flags.String("ai-codex-work-root", os.Getenv("STONEAGE_AI_CODEX_WORK_ROOT"), "private root for disposable AI connection-test workspaces")
	aiCodexWorkspaceRoot := flags.String("ai-codex-workspace-root", os.Getenv("STONEAGE_AI_CODEX_WORKSPACE_ROOT"), "alias for -ai-codex-work-root")
	aiCodexStateRoot := flags.String("ai-codex-state-root", os.Getenv("STONEAGE_AI_CODEX_STATE_ROOT"), "private root for disposable AI connection-test state")
	aiConnectionTimeout := flags.Duration("ai-model-test-timeout", durationEnv("STONEAGE_AI_MODEL_TEST_TIMEOUT", 60*time.Second), "maximum duration for an explicit model connection test")
	aiWebBaseURL := flags.String("ai-web-base-url", os.Getenv("STONEAGE_AI_WEB_BASE_URL"), "Web HTTP origin used by admin AI sessions")
	aiWebPublicURL := flags.String("ai-web-public-url", os.Getenv("STONEAGE_AI_WEB_PUBLIC_URL"), "explicit public Web base URL used in copied worker commands")
	aiWebServerID := flags.String("ai-web-server-id", envOr("STONEAGE_AI_WEB_SERVER_ID", "line-1"), "server-owned Web game server identifier")
	aiWebAgentSocket := flags.String("ai-web-agent-socket", envOr("STONEAGE_AI_WEB_AGENT_SOCKET", "/run/stoneage-web/agent.sock"), "private Web agent Unix socket")
	aiRuntimeRoot := flags.String("ai-runtime-root", os.Getenv("STONEAGE_AI_RUNTIME_ROOT"), "private root for persistent AI agent runtime state (enables game agents)")
	aiMCPBinary := flags.String("ai-mcp-binary", os.Getenv("STONEAGE_AI_MCP_BINARY"), "absolute server stoneage-game-mcp executable path")
	aiSkillRoot := flags.String("ai-skill-root", os.Getenv("STONEAGE_AI_SKILL_ROOT"), "absolute server root containing the hash-pinned native AI skills")
	aiRuntimeImage := flags.String("ai-runtime-image", os.Getenv("STONEAGE_AI_RUNTIME_IMAGE"), "fixed AI runtime container image (enables container agents)")
	aiContainerNetwork := flags.String("ai-container-network", os.Getenv("STONEAGE_AI_CONTAINER_NETWORK"), "fixed Docker network for AI runtime containers")
	aiDockerBinary := flags.String("ai-docker-binary", os.Getenv("STONEAGE_AI_DOCKER_BINARY"), "absolute Docker executable path for AI runtime containers")
	aiBrokerDB := flags.String("ai-broker-db", os.Getenv("STONEAGE_AI_BROKER_DB"), "private durable SQLite database for AI container request journaling (defaults inside the AI runtime root)")
	aiContainerGatewayURL := flags.String("ai-container-gateway-url", os.Getenv("STONEAGE_AI_CONTAINER_GATEWAY_URL"), "advertised AI Gateway URL reachable from runtime containers")
	aiFundingDir := flags.String("ai-funding-dir", envOr("STONEAGE_AI_FUNDING_DIR", aifunding.DefaultPolicyDir), "private server policy directory for AI funding capabilities")
	aiKnowledgeDataDir := flags.String("ai-knowledge-data-dir", os.Getenv("STONEAGE_AI_KNOWLEDGE_DATA_DIR"), "server-owned read-only gmsv data directory for AI knowledge")
	aiMapDataDir := flags.String("ai-map-data-dir", os.Getenv("STONEAGE_AI_MAP_DATA_DIR"), "server-owned read-only gmsv data directory for AI navigation")
	aiStockItems := flags.String("ai-stock-items", os.Getenv("STONEAGE_AI_STOCK_ITEMS"), "server-owned stock offers referencing reviewed NPC and healing catalogs")
	aiHealingItems := flags.String("ai-healing-items", os.Getenv("STONEAGE_AI_HEALING_ITEMS"), "server-owned healing item contracts matching the loaded knowledge fingerprint")
	aiNPCRegistry := flags.String("ai-npc-registry", os.Getenv("STONEAGE_AI_NPC_REGISTRY"), "server-owned NPC contracts matching the loaded knowledge fingerprint")
	aiAutomationDB := flags.String("ai-automation-db", os.Getenv("STONEAGE_AI_AUTOMATION_DB"), "private durable SQLite database for AI automation checkpoints")
	aiReceiptDB := flags.String("ai-receipt-db", os.Getenv("STONEAGE_AI_RECEIPT_DB"), "private durable SQLite database for AI action receipts")
	aiGatewayListen := flags.String("ai-gateway-listen", envOr("STONEAGE_AI_GATEWAY_LISTEN", "127.0.0.1:0"), "AI Gateway listen address (loopback locally; fixed private or 0.0.0.0 address for containers)")
	aiGatewayPort := flags.Int("ai-gateway-port", envInt("STONEAGE_AI_GATEWAY_PORT", -1), "optional AI Gateway port (overrides the port in -ai-gateway-listen)")
	trustedProxies := flags.String("trusted-proxies", envOr("STONEAGE_ADMIN_TRUSTED_PROXIES", "127.0.0.1/32,::1/128"), "comma-separated trusted reverse-proxy IPs or CIDRs")
	cookieSecure := flags.Bool("cookie-secure", envBool("STONEAGE_ADMIN_COOKIE_SECURE", false), "set Secure on admin session cookies")
	playerRoot := flags.String("player-admin-root", envOr("STONEAGE_PLAYER_ADMIN_ROOT", "/run/stoneage/player-admin"), "private legacy player management queues")
	catalogRoot := flags.String("player-catalog-root", envOr("STONEAGE_PLAYER_CATALOG_ROOT", "/game/gmsv/data"), "native game catalog directory")
	assetsRoot := flags.String("player-assets-root", envOr("STONEAGE_PLAYER_ASSETS_ROOT", "/opt/stoneage/web-assets"), "native Web sprite directory")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if strings.TrimSpace(*aiCodexWorkRoot) == "" {
		*aiCodexWorkRoot = strings.TrimSpace(*aiCodexWorkspaceRoot)
	}
	if strings.TrimSpace(*aiDatabasePath) == "" {
		*aiDatabasePath = filepath.Join(filepath.Dir(*databasePath), "stoneage-ai.db")
	}
	if strings.TrimSpace(*aiSecretDir) == "" {
		*aiSecretDir = filepath.Join(filepath.Dir(*databasePath), "ai-secrets")
	}
	aiDataRoot, err := filepath.Abs(filepath.Dir(*aiDatabasePath))
	if err != nil {
		return fmt.Errorf("resolve AI data root: %w", err)
	}
	if strings.TrimSpace(*aiCodexWorkRoot) == "" {
		*aiCodexWorkRoot = filepath.Join(aiDataRoot, "ai-workspaces")
	}
	if strings.TrimSpace(*aiCodexStateRoot) == "" {
		*aiCodexStateRoot = filepath.Join(aiDataRoot, "ai-state")
	}
	aiOptions := aiRuntimeOptions{
		WebBaseURL:          *aiWebBaseURL,
		WebPublicURL:        *aiWebPublicURL,
		WebServerID:         *aiWebServerID,
		WebAgentSocket:      *aiWebAgentSocket,
		RuntimeRoot:         *aiRuntimeRoot,
		MCPBinary:           *aiMCPBinary,
		SkillRoot:           *aiSkillRoot,
		RuntimeImage:        *aiRuntimeImage,
		ContainerNetwork:    *aiContainerNetwork,
		DockerBinary:        *aiDockerBinary,
		BrokerDB:            *aiBrokerDB,
		ContainerGatewayURL: *aiContainerGatewayURL,
		FundingDir:          *aiFundingDir,
		CodexBinary:         *aiCodexBinary,
		KnowledgeDataDir:    *aiKnowledgeDataDir,
		MapDataDir:          *aiMapDataDir,
		NPCRegistry:         *aiNPCRegistry,
		HealingItems:        *aiHealingItems,
		StockItems:          *aiStockItems,
		AutomationDB:        *aiAutomationDB,
		ReceiptDB:           *aiReceiptDB,
		GatewayListen:       *aiGatewayListen,
		GatewayPort:         *aiGatewayPort,
	}
	aiStore, err := airuntime.OpenWithSecrets(*aiDatabasePath, *aiSecretDir)
	if err != nil {
		return fmt.Errorf("open AI runtime store: %w", err)
	}
	defer aiStore.Close()
	aiSecrets, err := airuntime.NewSecretStore(*aiSecretDir)
	if err != nil {
		return fmt.Errorf("open AI secret store: %w", err)
	}
	if err := bootstrapAIModel(context.Background(), aiStore, aiSecrets, *aiModelKeyFile); err != nil {
		return err
	}
	// Model connection tests are deliberately independent from Codex,
	// containers, the Gateway, and the gameplay runtime. The tester reads the
	// saved model and secret, then sends one bounded Responses API request when
	// an administrator explicitly clicks Test in the console.
	tester, err := admin.NewAIHTTPModelConnectionTester(admin.AIHTTPModelConnectionTesterOptions{
		Models: aiStore, Secrets: aiSecrets, ConnectionTimeout: *aiConnectionTimeout,
	})
	if err != nil {
		return fmt.Errorf("configure AI HTTP connection tester: %w", err)
	}
	var connectionTester admin.AIModelConnectionTester = tester
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
	aiRuntime, err := configureAIRuntime(context.Background(), store, aiStore, aiSecrets, aiOptions)
	if err != nil {
		return err
	}
	if aiRuntime != nil {
		defer aiRuntime.Close()
	} else {
		missing := missingAIRuntimeOptions(aiOptions)
		if len(missing) > 0 {
			log.Printf("AI runtime unavailable; missing server-owned settings: %s", strings.Join(missing, ", "))
		}
	}
	var aiProvisioner admin.AIPlayerProvisioner
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
	if aiRuntime != nil {
		aiProvisioner = newAIPlayerProvisionerAdapter(aiRuntime.provisioner, initialPlayerState{manager: players})
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
		AIStore:             aiStore,
		AISecrets:           aiSecrets,
		AIProvisioner:       aiProvisioner,
		AIRuntime: func() admin.AIProfileRuntime {
			if aiRuntime == nil {
				return nil
			}
			return aiRuntime.AdminRuntime()
		}(),
		AIConnectionTester: connectionTester,
		AILocalExecutor: func() admin.AILocalExecutor {
			if aiRuntime == nil || aiRuntime.remote == nil {
				return nil
			}
			return aiRuntime
		}(),
		AIWorkerHandler: func() http.Handler {
			if aiRuntime == nil || aiRuntime.remote == nil {
				return nil
			}
			return aiRuntime.remote.Handler()
		}(),
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
	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		return err
	}
	log.Printf("StoneAge admin listening on %s", listener.Addr())
	return serveAdminWithLifecycle(signalCtx, server, listener, aiRuntime)
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

const maxAIModelKeyFileBytes = 4096

// bootstrapAIModel imports one explicitly configured server-side key only
// when no durable default model exists. It is intentionally idempotent: an
// existing model key is never replaced by the contents of a later key file.
func bootstrapAIModel(ctx context.Context, store *airuntime.Store, secrets *airuntime.SecretStore, keyFile string) error {
	keyFile = strings.TrimSpace(keyFile)
	if keyFile == "" {
		return nil
	}
	if store == nil || secrets == nil {
		return errors.New("AI bootstrap requires model and secret stores")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	defaultID, err := store.GetDefaultModelConfigID(ctx)
	if err != nil {
		return fmt.Errorf("read default AI model: %w", err)
	}
	if strings.TrimSpace(defaultID) != "" {
		return nil
	}
	models, err := store.ListModelConfigs(ctx)
	if err != nil {
		return fmt.Errorf("list AI models for bootstrap: %w", err)
	}
	for _, model := range models {
		if !isReviewedDeepSeekModel(model) {
			continue
		}
		hasKey, err := secrets.HasKey(model.ID)
		if err != nil {
			return fmt.Errorf("inspect existing AI model key: %w", err)
		}
		if !hasKey {
			key, readErr := readAIModelKeyFile(keyFile)
			if readErr != nil {
				return readErr
			}
			if err := secrets.WriteKey(model.ID, key); err != nil {
				return fmt.Errorf("save AI model key: %w", err)
			}
			present := true
			if _, err := store.UpdateModelConfigCAS(ctx, model.ID, model.Version, airuntime.ModelConfigPatch{HasKey: &present, Actor: "bootstrap"}); err != nil {
				return fmt.Errorf("record AI model key: %w", err)
			}
		}
		if err := store.SetDefaultModelConfigID(ctx, model.ID, "bootstrap"); err != nil {
			return fmt.Errorf("set default AI model: %w", err)
		}
		return nil
	}

	key, err := readAIModelKeyFile(keyFile)
	if err != nil {
		return err
	}
	config, err := admin.NewReviewedAIModelConfig()
	if err != nil {
		return fmt.Errorf("load reviewed AI model catalog: %w", err)
	}
	created, err := store.CreateModelConfigAs(ctx, config, "bootstrap")
	if err != nil {
		return fmt.Errorf("create default AI model: %w", err)
	}
	if err := secrets.WriteKey(created.ID, key); err != nil {
		_ = store.DeleteModelConfigCAS(context.Background(), created.ID, created.Version, "bootstrap")
		return fmt.Errorf("save AI model key: %w", err)
	}
	present := true
	if _, err := store.UpdateModelConfigCAS(ctx, created.ID, created.Version, airuntime.ModelConfigPatch{HasKey: &present, Actor: "bootstrap"}); err != nil {
		return fmt.Errorf("record AI model key: %w", err)
	}
	if err := store.SetDefaultModelConfigID(ctx, created.ID, "bootstrap"); err != nil {
		return fmt.Errorf("set default AI model: %w", err)
	}
	return nil
}

func isReviewedDeepSeekModel(model airuntime.ModelConfig) bool {
	return model.Backend == airuntime.ModelBackendCodex && model.Provider == aimodels.DeepSeekProvider &&
		strings.TrimRight(strings.TrimSpace(model.BaseURL), "/") == strings.TrimRight(aimodels.DeepSeekBaseURL, "/") &&
		model.Model == aimodels.DeepSeekFlash
}

func readAIModelKeyFile(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("read AI model key file: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return "", errors.New("AI model key file must be a private regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("read AI model key file: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxAIModelKeyFileBytes+1))
	if err != nil {
		return "", fmt.Errorf("read AI model key file: %w", err)
	}
	if len(data) > maxAIModelKeyFileBytes {
		return "", errors.New("AI model key file is too large")
	}
	key := strings.TrimSpace(string(data))
	if key == "" || strings.ContainsAny(key, "\x00\r\n") {
		return "", errors.New("AI model key file is invalid")
	}
	return key, nil
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  stoneage-admin serve [flags]
  stoneage-admin create-admin -username NAME [-password-stdin]
  stoneage-admin import-legacy -char-dir PATH [-default-password PASSWORD]

serve environment:
  STONEAGE_AUTH_DB, STONEAGE_ADMIN_LISTEN, STONEAGE_ADMIN_SETUP_TOKEN,
  STONEAGE_ADMIN_USER, STONEAGE_ADMIN_PASSWORD, STONEAGE_SERVER_CONFIG,
	STONEAGE_OPERATOR_SOCKET, STONEAGE_SAAC_CONFIG, STONEAGE_AI_DB,
	STONEAGE_AI_SECRET_DIR, STONEAGE_AI_MODEL_KEY_FILE, STONEAGE_AI_CODEX_BINARY,
	STONEAGE_AI_CODEX_WORK_ROOT, STONEAGE_AI_CODEX_WORKSPACE_ROOT,
	STONEAGE_AI_CODEX_STATE_ROOT, STONEAGE_AI_MODEL_TEST_TIMEOUT,
	STONEAGE_AI_WEB_BASE_URL, STONEAGE_AI_WEB_PUBLIC_URL, STONEAGE_AI_WEB_SERVER_ID,
	STONEAGE_AI_WEB_AGENT_SOCKET, STONEAGE_AI_RUNTIME_ROOT, STONEAGE_AI_MCP_BINARY,
	STONEAGE_AI_SKILL_ROOT, STONEAGE_AI_RUNTIME_IMAGE, STONEAGE_AI_CONTAINER_NETWORK,
	STONEAGE_AI_DOCKER_BINARY, STONEAGE_AI_BROKER_DB, STONEAGE_AI_CONTAINER_GATEWAY_URL,
	STONEAGE_AI_FUNDING_DIR, STONEAGE_AI_GATEWAY_LISTEN,
	STONEAGE_AI_KNOWLEDGE_DATA_DIR, STONEAGE_AI_MAP_DATA_DIR,
	STONEAGE_AI_AUTOMATION_DB, STONEAGE_AI_RECEIPT_DB, STONEAGE_AI_GATEWAY_PORT,
	STONEAGE_ADMIN_COOKIE_SECURE`)
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

func durationEnv(name string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func envInt(name string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}
