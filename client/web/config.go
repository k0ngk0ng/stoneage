package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

// webConfigFile is deliberately separate from Config so the on-disk schema
// stays stable even if the bridge gains internal runtime fields. JSON keeps
// the production binary self-contained and lets startup reject misspelled
// keys instead of silently serving assets from the wrong origin.
type webConfigFile struct {
	ListenAddress string              `json:"listen_address"`
	TCPUpstream   string              `json:"tcp_upstream"`
	PacketLimit   int                 `json:"packet_limit"`
	MaxSessions   int                 `json:"max_sessions"`
	PollTimeout   string              `json:"poll_timeout"`
	IdleTimeout   string              `json:"idle_timeout"`
	DialTimeout   string              `json:"dial_timeout"`
	AllowedOrigin string              `json:"allowed_origin"`
	Static        webStaticConfigFile `json:"static"`
}

type webStaticConfigFile struct {
	AssetsDirectory string           `json:"assets_directory"`
	MapsDirectory   string           `json:"maps_directory"`
	AudioDirectory  string           `json:"audio_directory"`
	NPCDirectory    string           `json:"npc_directory"`
	OSS             webOSSConfigFile `json:"oss"`
	CDN             webCDNConfigFile `json:"cdn"`
}

type webCDNConfigFile struct {
	BaseURL string `json:"base_url"`
}

type webOSSConfigFile struct {
	Provider           string `json:"provider"`
	Endpoint           string `json:"endpoint"`
	Region             string `json:"region"`
	Bucket             string `json:"bucket"`
	Prefix             string `json:"prefix"`
	AccessKeyIDEnv     string `json:"access_key_id_env"`
	AccessKeySecretEnv string `json:"access_key_secret_env"`
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
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&disk); err != nil {
		return Config{}, fmt.Errorf("decode web config %q: %w", filename, err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			err = fmt.Errorf("multiple JSON values")
		}
		return Config{}, fmt.Errorf("decode web config %q: %w", filename, err)
	}

	applyNonEmpty(&cfg.ListenAddress, disk.ListenAddress)
	applyNonEmpty(&cfg.TCPUpstream, disk.TCPUpstream)
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
	applyNonEmpty(&cfg.OSS.AccessKeyIDEnv, disk.Static.OSS.AccessKeyIDEnv)
	applyNonEmpty(&cfg.OSS.AccessKeySecretEnv, disk.Static.OSS.AccessKeySecretEnv)
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
	return cfg, nil
}

func configFromCommandLine(arguments []string) (Config, string, error) {
	var configPath string
	flags := flag.NewFlagSet("stoneage-web", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&configPath, "config", strings.TrimSpace(os.Getenv("STONEAGE_WEB_CONFIG")), "path to web JSON configuration")
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
	return applyEnvironmentConfig(cfg), configPath, nil
}

var (
	ossBucketNamePattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`)
	environmentNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

func normalizeOSSConfig(value OSSConfig) (OSSConfig, error) {
	value.Provider = strings.ToLower(strings.TrimSpace(value.Provider))
	value.Endpoint = strings.TrimRight(strings.TrimSpace(value.Endpoint), "/")
	value.Region = strings.TrimSpace(value.Region)
	value.Bucket = strings.TrimSpace(value.Bucket)
	value.Prefix = strings.Trim(strings.TrimSpace(value.Prefix), "/")
	value.AccessKeyIDEnv = strings.TrimSpace(value.AccessKeyIDEnv)
	value.AccessKeySecretEnv = strings.TrimSpace(value.AccessKeySecretEnv)
	if value.Provider == "" {
		value.Provider = "aliyun-oss"
	}
	if value.Provider != "aliyun-oss" {
		return OSSConfig{}, fmt.Errorf("provider %q is unsupported", value.Provider)
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
			return OSSConfig{}, fmt.Errorf("bucket %q is not a valid Aliyun OSS bucket name", value.Bucket)
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
	for name, configured := range map[string]string{
		"access_key_id_env":     value.AccessKeyIDEnv,
		"access_key_secret_env": value.AccessKeySecretEnv,
	} {
		if configured != "" && !environmentNamePattern.MatchString(configured) {
			return OSSConfig{}, fmt.Errorf("%s must name an environment variable", name)
		}
	}
	return value, nil
}

func ossPublicBaseURL(value OSSConfig) string {
	if value.Endpoint == "" || value.Bucket == "" {
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
