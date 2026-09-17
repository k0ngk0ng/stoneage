package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// webConfigFile is deliberately separate from Config so the on-disk schema
// stays stable even if the bridge gains internal runtime fields. TOML keeps
// the production configuration readable and lets startup reject misspelled
// keys instead of silently serving assets from the wrong origin.
type webConfigFile struct {
	ListenAddress string                  `toml:"listen_address"`
	TCPUpstream   string                  `toml:"tcp_upstream"`
	GatewayAPIURL string                  `toml:"gateway_api_url"`
	Agent         webAgentConfigFile      `toml:"agent"`
	PacketLimit   int                     `toml:"packet_limit"`
	MaxSessions   int                     `toml:"max_sessions"`
	PollTimeout   string                  `toml:"poll_timeout"`
	IdleTimeout   string                  `toml:"idle_timeout"`
	DialTimeout   string                  `toml:"dial_timeout"`
	AllowedOrigin string                  `toml:"allowed_origin"`
	Static        webStaticConfigFile     `toml:"static"`
	Automation    webAutomationConfigFile `toml:"automation"`
}

type webAgentConfigFile struct {
	SocketPath     string `toml:"socket_path"`
	GameUpstream   string `toml:"game_upstream"`
	WorkerUpstream string `toml:"worker_upstream"`
}

type webAutomationConfigFile struct {
	KnowledgeDataDir  string `toml:"knowledge_data_dir"`
	MapDataDir        string `toml:"map_data_dir"`
	StockItems        string `toml:"stock_items"`
	HealingItems      string `toml:"healing_items"`
	NPCRegistry       string `toml:"npc_registry"`
	AutomationDB      string `toml:"automation_db"`
	ReceiptDB         string `toml:"receipt_db"`
	PollInterval      string `toml:"poll_interval"`
	NoProgressTimeout string `toml:"no_progress_timeout"`
}

type webStaticConfigFile struct {
	AssetsDirectory string           `toml:"assets_directory"`
	MapsDirectory   string           `toml:"maps_directory"`
	AudioDirectory  string           `toml:"audio_directory"`
	NPCDirectory    string           `toml:"npc_directory"`
	OSS             webOSSConfigFile `toml:"oss"`
	CDN             webCDNConfigFile `toml:"cdn"`
}

type webCDNConfigFile struct {
	BaseURL string `toml:"base_url"`
}

type webOSSConfigFile struct {
	Provider string `toml:"provider"`
	Endpoint string `toml:"endpoint"`
	Region   string `toml:"region"`
	Bucket   string `toml:"bucket"`
	Prefix   string `toml:"prefix"`
}

func applyNonEmpty(destination *string, value string) {
	if value = strings.TrimSpace(value); value != "" {
		*destination = value
	}
}

func applyFileDuration(destination *time.Duration, value, fieldName string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		return fmt.Errorf("%s must be a positive Go duration", fieldName)
	}
	*destination = parsed
	return nil
}

func loadWebConfigFile(filename string) (Config, error) {
	cfg := DefaultConfig()
	content, err := os.ReadFile(filename)
	if err != nil {
		return Config{}, fmt.Errorf("read web config %q: %w", filename, err)
	}
	var disk webConfigFile
	decoder := toml.NewDecoder(bytes.NewReader(content)).DisallowUnknownFields()
	if err := decoder.Decode(&disk); err != nil {
		return Config{}, fmt.Errorf("decode web TOML config %q: %w", filename, err)
	}

	applyNonEmpty(&cfg.ListenAddress, disk.ListenAddress)
	applyNonEmpty(&cfg.TCPUpstream, disk.TCPUpstream)
	applyNonEmpty(&cfg.GatewayAPIURL, disk.GatewayAPIURL)
	applyNonEmpty(&cfg.AgentSocketPath, disk.Agent.SocketPath)
	applyNonEmpty(&cfg.AgentGameUpstream, disk.Agent.GameUpstream)
	applyNonEmpty(&cfg.AgentWorkerUpstream, disk.Agent.WorkerUpstream)
	applyNonEmpty(&cfg.AssetsDirectory, disk.Static.AssetsDirectory)
	applyNonEmpty(&cfg.MapDirectory, disk.Static.MapsDirectory)
	applyNonEmpty(&cfg.AudioDirectory, disk.Static.AudioDirectory)
	applyNonEmpty(&cfg.NPCDirectory, disk.Static.NPCDirectory)
	applyNonEmpty(&cfg.AllowedOrigin, disk.AllowedOrigin)
	applyNonEmpty(&cfg.CDNBaseURL, disk.Static.CDN.BaseURL)
	applyNonEmpty(&cfg.OSS.Provider, disk.Static.OSS.Provider)
	applyNonEmpty(&cfg.OSS.Endpoint, disk.Static.OSS.Endpoint)
	applyNonEmpty(&cfg.OSS.Region, disk.Static.OSS.Region)
	applyNonEmpty(&cfg.OSS.Bucket, disk.Static.OSS.Bucket)
	applyNonEmpty(&cfg.OSS.Prefix, disk.Static.OSS.Prefix)
	applyNonEmpty(&cfg.AutomationKnowledgeDataDir, disk.Automation.KnowledgeDataDir)
	applyNonEmpty(&cfg.AutomationMapDataDir, disk.Automation.MapDataDir)
	applyNonEmpty(&cfg.AutomationStockItems, disk.Automation.StockItems)
	applyNonEmpty(&cfg.AutomationHealingItems, disk.Automation.HealingItems)
	applyNonEmpty(&cfg.AutomationNPCRegistry, disk.Automation.NPCRegistry)
	applyNonEmpty(&cfg.AutomationDB, disk.Automation.AutomationDB)
	applyNonEmpty(&cfg.ReceiptDB, disk.Automation.ReceiptDB)
	if disk.PacketLimit != 0 {
		cfg.PacketLimit = disk.PacketLimit
	}
	if disk.MaxSessions != 0 {
		cfg.MaxSessions = disk.MaxSessions
	}
	if err := applyFileDuration(&cfg.PollTimeout, disk.PollTimeout, "poll_timeout"); err != nil {
		return Config{}, err
	}
	if err := applyFileDuration(&cfg.IdleTimeout, disk.IdleTimeout, "idle_timeout"); err != nil {
		return Config{}, err
	}
	if err := applyFileDuration(&cfg.DialTimeout, disk.DialTimeout, "dial_timeout"); err != nil {
		return Config{}, err
	}
	if err := applyFileDuration(&cfg.AutomationPollInterval, disk.Automation.PollInterval, "automation.poll_interval"); err != nil {
		return Config{}, err
	}
	if err := applyFileDuration(&cfg.AutomationNoProgressTimeout, disk.Automation.NoProgressTimeout, "automation.no_progress_timeout"); err != nil {
		return Config{}, err
	}
	if gatewayAPIURL, err := normalizeGatewayAPIURL(cfg.GatewayAPIURL); err != nil {
		return Config{}, fmt.Errorf("invalid gateway_api_url: %w", err)
	} else {
		cfg.GatewayAPIURL = gatewayAPIURL
	}
	return cfg, nil
}

func configFromCommandLine(arguments []string) (Config, string, error) {
	var configPath string
	flags := flag.NewFlagSet("stoneage-web", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&configPath, "config", strings.TrimSpace(os.Getenv("STONEAGE_WEB_CONFIG")), "path to web TOML configuration")
	var automationKnowledgeDataDir, automationMapDataDir, automationNPCRegistry, automationHealingItems, automationStockItems, automationDB, receiptDB string
	var automationPollInterval, automationNoProgressTimeout time.Duration
	flags.StringVar(&automationKnowledgeDataDir, "ai-knowledge-data-dir", strings.TrimSpace(os.Getenv("STONEAGE_AI_KNOWLEDGE_DATA_DIR")), "server-owned read-only data directory for AI knowledge")
	flags.StringVar(&automationMapDataDir, "ai-map-data-dir", strings.TrimSpace(os.Getenv("STONEAGE_AI_MAP_DATA_DIR")), "server-owned read-only data directory for AI navigation")
	flags.StringVar(&automationStockItems, "ai-stock-items", strings.TrimSpace(os.Getenv("STONEAGE_AI_STOCK_ITEMS")), "server-owned stock offers referencing reviewed NPC and healing catalogs")
	flags.StringVar(&automationHealingItems, "ai-healing-items", strings.TrimSpace(os.Getenv("STONEAGE_AI_HEALING_ITEMS")), "server-owned healing item contracts matching the loaded knowledge fingerprint")
	flags.StringVar(&automationNPCRegistry, "ai-npc-registry", strings.TrimSpace(os.Getenv("STONEAGE_AI_NPC_REGISTRY")), "server-owned NPC contracts matching the loaded knowledge fingerprint")
	flags.StringVar(&automationDB, "ai-automation-db", strings.TrimSpace(os.Getenv("STONEAGE_AI_AUTOMATION_DB")), "private durable SQLite database for AI automation checkpoints")
	flags.StringVar(&receiptDB, "ai-receipt-db", strings.TrimSpace(os.Getenv("STONEAGE_AI_RECEIPT_DB")), "private durable SQLite database for AI action receipts")
	flags.DurationVar(&automationPollInterval, "ai-automation-poll-interval", 0, "AI automation polling interval")
	flags.DurationVar(&automationNoProgressTimeout, "ai-automation-no-progress-timeout", 0, "AI automation no-progress timeout")
	if err := flags.Parse(arguments); err != nil {
		return Config{}, "", fmt.Errorf("parse web arguments: %w", err)
	}
	if flags.NArg() != 0 {
		return Config{}, "", fmt.Errorf("unexpected web arguments: %s", strings.Join(flags.Args(), " "))
	}
	cfg := DefaultConfig()
	var err error
	if configPath = strings.TrimSpace(configPath); configPath != "" {
		cfg, err = loadWebConfigFile(configPath)
		if err != nil {
			return Config{}, configPath, err
		}
	}
	// Explicit command-line settings take precedence over environment values.
	cfg = applyEnvironmentConfig(cfg)
	applyNonEmpty(&cfg.AutomationKnowledgeDataDir, automationKnowledgeDataDir)
	applyNonEmpty(&cfg.AutomationMapDataDir, automationMapDataDir)
	applyNonEmpty(&cfg.AutomationStockItems, automationStockItems)
	applyNonEmpty(&cfg.AutomationHealingItems, automationHealingItems)
	applyNonEmpty(&cfg.AutomationNPCRegistry, automationNPCRegistry)
	applyNonEmpty(&cfg.AutomationDB, automationDB)
	applyNonEmpty(&cfg.ReceiptDB, receiptDB)
	if automationPollInterval != 0 {
		cfg.AutomationPollInterval = automationPollInterval
	}
	if automationNoProgressTimeout != 0 {
		cfg.AutomationNoProgressTimeout = automationNoProgressTimeout
	}
	return cfg, configPath, nil
}

var (
	ossBucketNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`)
)

func normalizeOSSConfig(value OSSConfig) (OSSConfig, error) {
	value.Provider = strings.ToLower(strings.TrimSpace(value.Provider))
	value.Endpoint = strings.TrimRight(strings.TrimSpace(value.Endpoint), "/")
	value.Region = strings.TrimSpace(value.Region)
	value.Bucket = strings.TrimSpace(value.Bucket)
	value.Prefix = strings.Trim(strings.TrimSpace(value.Prefix), "/")
	if value.Provider == "" {
		value.Provider = "aliyun-oss"
	}
	switch value.Provider {
	case "aliyun", "aliyun-oss":
		value.Provider = "aliyun-oss"
	case "r2", "cloudflare", "cloudflare-r2":
		value.Provider = "cloudflare-r2"
		if value.Region == "" {
			// Cloudflare's S3-compatible API uses the literal region "auto".
			value.Region = "auto"
		}
	default:
		return OSSConfig{}, fmt.Errorf("provider %q is unsupported (supported: aliyun-oss, cloudflare-r2)", value.Provider)
	}
	if (value.Endpoint == "") != (value.Bucket == "") {
		return OSSConfig{}, fmt.Errorf("endpoint and bucket must be configured together")
	}
	if value.Endpoint != "" {
		parsed, err := url.Parse(value.Endpoint)
		if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
			return OSSConfig{}, fmt.Errorf("endpoint must be an absolute HTTP(S) origin without credentials, path, query or fragment")
		}
		if !ossBucketNamePattern.MatchString(value.Bucket) {
			return OSSConfig{}, fmt.Errorf("bucket %q is not a valid object-storage bucket name", value.Bucket)
		}
	}
	if strings.ContainsAny(value.Prefix, "\"'`<>\\?#\r\n\t ") {
		return OSSConfig{}, fmt.Errorf("prefix contains an unsafe character")
	}
	for _, part := range strings.Split(value.Prefix, "/") {
		if part == "." || part == ".." {
			return OSSConfig{}, fmt.Errorf("prefix must not contain dot path segments")
		}
	}
	return value, nil
}

func ossPublicBaseURL(value OSSConfig) string {
	if value.Endpoint == "" || value.Bucket == "" {
		return ""
	}
	// R2 buckets are private by default and their S3 endpoint requires signed
	// requests.  A Cloudflare custom domain belongs in static.cdn.base_url;
	// never expose an authenticated R2 API URL as a browser asset origin.
	if value.Provider == "cloudflare-r2" {
		return ""
	}
	parsed, err := url.Parse(value.Endpoint)
	if err != nil || parsed.Host == "" {
		return ""
	}
	host := parsed.Host
	if !strings.HasPrefix(strings.ToLower(parsed.Hostname()), strings.ToLower(value.Bucket)+".") {
		host = value.Bucket + "." + host
	}
	base := parsed.Scheme + "://" + host
	if value.Prefix != "" {
		base += "/" + value.Prefix
	}
	return base
}
