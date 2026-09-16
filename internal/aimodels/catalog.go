// Package aimodels embeds the model capabilities required by the project's
// Codex runtime. A model name alone is not a complete provider integration.
package aimodels

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/k0ngk0ng/stoneage/internal/runtimepath"
	"github.com/pelletier/go-toml/v2"
)

const (
	DeepSeekFlash       = "deepseek-flash"
	DeepSeekProvider    = "deepseek"
	DeepSeekBaseURL     = "https://api.deepseek.com/"
	OpenAIBaseURL       = "https://api.openai.com/v1"
	MinimumCodexVersion = "0.144.0"
	SourceURL           = "https://api-docs.deepseek.com/quick_start/agent_integrations/codex"
	CatalogSHA256       = "4240127a851f7582bb4f92b9fe3b258f18393d560446e84430e5f90f473db9a1"
)

// The DeepSeek-Flash entry is copied from the official integration guide,
// including its model_messages. This is model metadata, never instructions
// for the host application. Game persona/skills are supplied separately.
//
//go:embed deepseek-flash.models.json
var catalog []byte

type Descriptor struct {
	Slug             string `json:"slug"`
	DisplayName      string `json:"display_name"`
	Description      string `json:"description"`
	ContextWindow    int    `json:"context_window"`
	MaxContextWindow int    `json:"max_context_window"`
	DefaultReasoning string `json:"default_reasoning_level"`
	ReasoningLevels  []struct {
		Effort      string `json:"effort"`
		Description string `json:"description"`
	} `json:"supported_reasoning_levels"`
	InputModalities           []string `json:"input_modalities"`
	MinimumClientVersion      string   `json:"minimal_client_version"`
	SupportsParallelToolCalls bool     `json:"supports_parallel_tool_calls"`
}

func Catalog() []byte { return append([]byte(nil), catalog...) }

// Keep the reviewed upstream artifact intact, but advertise eager native MCP
// tools in the runtime copy. DeepSeek's deferred tool discovery can produce
// shell invocations instead of calls to the discovered MCP namespace in Codex
// 0.154.0. This changes discovery only; the native MCP server is still used.
func runtimeCatalog() []byte {
	return bytes.Replace(catalog, []byte(`"supports_search_tool": true`), []byte(`"supports_search_tool": false`), 1)
}
func Flash() (Descriptor, error) {
	var parsed struct {
		Models []Descriptor `json:"models"`
	}
	if err := json.Unmarshal(catalog, &parsed); err != nil {
		return Descriptor{}, err
	}
	if len(parsed.Models) != 1 || parsed.Models[0].Slug != DeepSeekFlash {
		return Descriptor{}, errors.New("invalid embedded DeepSeek model catalog")
	}
	return parsed.Models[0], nil
}

func ValidateCatalog() error {
	h := sha256.Sum256(catalog)
	if hex.EncodeToString(h[:]) != CatalogSHA256 {
		return errors.New("embedded DeepSeek catalog digest differs from reviewed source")
	}
	d, err := Flash()
	if err != nil {
		return err
	}
	if d.ContextWindow != 1048576 || d.MaxContextWindow != 1048576 || d.MinimumClientVersion != MinimumCodexVersion {
		return errors.New("unexpected DeepSeek capability metadata")
	}
	return nil
}

var versionRE = regexp.MustCompile(`(?:^|\s)(\d+)\.(\d+)\.(\d+)(?:\s|$|-)`)

// CheckCodexVersion accepts the output of `codex --version`. Package versions
// below the catalog's declared minimum are rejected before any model request.
func CheckCodexVersion(output string) error {
	m := versionRE.FindStringSubmatch(strings.TrimSpace(output))
	if len(m) != 4 {
		return errors.New("cannot determine Codex runtime version")
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	if major == 0 && minor < 144 {
		return fmt.Errorf("DeepSeek-Flash requires Codex %s or later", MinimumCodexVersion)
	}
	return nil
}

type RuntimeSettings struct {
	Model           string
	Provider        string
	BaseURL         string
	ReasoningEffort string
	ContextWindow   int
	APIKey          string `json:"-"`
	MCPCommand      string
	MCPArgs         []string
	MCPEnv          map[string]string `json:"-"`
}

type RuntimeFiles struct {
	ProviderID    string
	ConfigPath    string
	CatalogPath   string
	CatalogSHA256 string
}

func (s RuntimeSettings) validated() (RuntimeSettings, error) {
	if s.Provider == "" {
		s.Provider = DeepSeekProvider
	}
	if !regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`).MatchString(s.Provider) {
		return s, errors.New("invalid model provider identifier")
	}
	if s.Model == "" && s.Provider == DeepSeekProvider {
		s.Model = DeepSeekFlash
	}
	if strings.TrimSpace(s.Model) == "" || len(s.Model) > 256 || strings.ContainsAny(s.Model, "\x00\r\n") {
		return s, errors.New("model name is missing or invalid")
	}
	if s.BaseURL == "" {
		switch s.Provider {
		case DeepSeekProvider:
			s.BaseURL = DeepSeekBaseURL
		case "openai":
			s.BaseURL = OpenAIBaseURL
		default:
			return s, errors.New("custom model API base URL is required")
		}
	}
	u, err := url.Parse(s.BaseURL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(s.BaseURL, "\x00\r\n") {
		return s, errors.New("model API base URL must be an HTTP(S) URL without credentials, query or fragment")
	}
	if s.ReasoningEffort == "" && s.Model == DeepSeekFlash {
		s.ReasoningEffort = "high"
	}
	if s.Model == DeepSeekFlash && s.ReasoningEffort != "low" && s.ReasoningEffort != "high" && s.ReasoningEffort != "max" {
		return s, errors.New("DeepSeek reasoning effort must be low, high or max")
	}
	switch s.ReasoningEffort {
	case "", "none", "minimal", "low", "medium", "high", "xhigh", "max":
	default:
		return s, errors.New("invalid model reasoning effort")
	}
	if s.ContextWindow < 0 || s.ContextWindow > 16*1024*1024 {
		return s, errors.New("invalid model context window")
	}
	if strings.TrimSpace(s.APIKey) == "" || strings.ContainsAny(s.APIKey, "\x00\r\n") {
		return s, errors.New("model API key is missing or invalid")
	}
	if s.MCPCommand != "" && !filepath.IsAbs(s.MCPCommand) {
		return s, errors.New("game MCP executable must be an absolute server-configured path")
	}
	return s, nil
}

// Materialize writes only into a server-owned, per-agent Codex home. The
// caller must quiesce the agent before replacing its configuration. It never
// reads or edits the operator's personal ~/.codex files. The file containing
// the API key is private; callers must not log its bytes.
func Materialize(directory string, settings RuntimeSettings) (RuntimeFiles, error) {
	s, err := settings.validated()
	if err != nil {
		return RuntimeFiles{}, err
	}
	if err = ValidateCatalog(); err != nil {
		return RuntimeFiles{}, err
	}
	if !filepath.IsAbs(directory) || directory == string(filepath.Separator) {
		return RuntimeFiles{}, errors.New("a dedicated absolute Codex home is required")
	}
	guard, err := runtimepath.NewGuard()
	if err != nil {
		return RuntimeFiles{}, errors.New("cannot initialize Codex runtime path guard")
	}
	if err := guard.Check(directory); err != nil {
		return RuntimeFiles{}, err
	}
	if err = os.MkdirAll(directory, 0700); err != nil {
		return RuntimeFiles{}, err
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return RuntimeFiles{}, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return RuntimeFiles{}, errors.New("Codex home must be a real directory")
	}
	if err = os.Chmod(directory, 0700); err != nil {
		return RuntimeFiles{}, err
	}
	// Recheck after directory creation so a path replacement between the
	// initial metadata check and MkdirAll cannot redirect generated files into
	// an operator-owned Codex home.
	if err := guard.Check(directory); err != nil {
		return RuntimeFiles{}, err
	}
	providerID := s.Provider
	// Built-in IDs cannot be overridden in current Codex. Use an application
	// namespace so OpenAI-compatible custom URLs and API-key auth are honored.
	switch providerID {
	case "openai", "ollama", "lmstudio":
		providerID = "stoneage_" + providerID
	}
	paths := RuntimeFiles{ProviderID: providerID, ConfigPath: filepath.Join(directory, "config.toml")}
	if s.Model == DeepSeekFlash {
		paths.CatalogPath = filepath.Join(directory, "models.json")
		digest := sha256.Sum256(runtimeCatalog())
		paths.CatalogSHA256 = hex.EncodeToString(digest[:])
	}
	// These two policies are explicitly required by the user for unattended
	// game agents. Game identity/lease/transfer checks remain server-enforced.
	config := map[string]any{
		"model": s.Model, "model_provider": providerID, "preferred_auth_method": "apikey", "forced_login_method": "api",
		"web_search":      "disabled",
		"approval_policy": "never", "sandbox_mode": "danger-full-access",
		"model_providers": map[string]any{providerID: map[string]any{"name": s.Provider, "base_url": s.BaseURL, "wire_api": "responses", "experimental_bearer_token": s.APIKey, "requires_openai_auth": false}},
	}
	if s.ReasoningEffort != "" {
		config["model_reasoning_effort"] = s.ReasoningEffort
	}
	if s.ContextWindow > 0 {
		config["model_context_window"] = s.ContextWindow
	}
	if paths.CatalogPath != "" {
		config["model_catalog_json"] = paths.CatalogPath
	}
	if s.MCPCommand != "" {
		config["mcp_servers"] = map[string]any{"stoneage": map[string]any{"command": s.MCPCommand, "args": s.MCPArgs, "env": s.MCPEnv, "required": true, "startup_timeout_sec": 20, "tool_timeout_sec": 60}}
	}
	data, err := toml.Marshal(config)
	if err != nil {
		return RuntimeFiles{}, errors.New("cannot encode Codex runtime configuration")
	}
	if paths.CatalogPath != "" {
		if err := guard.Check(paths.CatalogPath); err != nil {
			return RuntimeFiles{}, err
		}
		if err = writePrivate(paths.CatalogPath, runtimeCatalog()); err != nil {
			return RuntimeFiles{}, err
		}
	}
	if err := guard.Check(paths.ConfigPath); err != nil {
		return RuntimeFiles{}, err
	}
	if err = writePrivate(paths.ConfigPath, data); err != nil {
		return RuntimeFiles{}, err
	}
	return paths, nil
}

func writePrivate(path string, data []byte) error {
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return errors.New("runtime configuration target must be a regular file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".runtime-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(name, path); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
