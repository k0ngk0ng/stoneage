package aimodels

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/runtimepath"
	"github.com/pelletier/go-toml/v2"
)

func materializeTestRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	base := filepath.Join(filepath.Dir(filename), "..", "..", "build", "ai")
	if err := os.MkdirAll(base, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(base, "aimodels-path-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	return root
}

func changeMaterializeTestDir(t *testing.T, path string) {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
}

func TestReviewedDeepSeekCapabilities(t *testing.T) {
	if err := ValidateCatalog(); err != nil {
		t.Fatal(err)
	}
	d, err := Flash()
	if err != nil {
		t.Fatal(err)
	}
	if d.DefaultReasoning != "high" || len(d.ReasoningLevels) != 3 || d.ReasoningLevels[1].Effort != "high" || len(d.InputModalities) != 2 || !d.SupportsParallelToolCalls {
		t.Fatal(d)
	}
	var raw struct {
		Models []map[string]json.RawMessage `json:"models"`
	}
	if err = json.Unmarshal(Catalog(), &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw.Models[0]["model_messages"]) == 0 {
		t.Fatal("Codex model instructions metadata omitted")
	}
	copy := Catalog()
	copy[0] = 'x'
	if !json.Valid(Catalog()) {
		t.Fatal("catalog aliases caller bytes")
	}
}

func TestRuntimeConfigurationHonorsExplicitPolicies(t *testing.T) {
	root := t.TempDir()
	key := "test-key-not-a-real-credential"
	paths, err := Materialize(root, RuntimeSettings{APIKey: key, MCPCommand: "/opt/stoneage/bin/stoneage-game-mcp", MCPArgs: []string{"serve"}})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(paths.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err = toml.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{"model": "deepseek-flash", "model_provider": "deepseek", "model_reasoning_effort": "high", "web_search": "disabled", "approval_policy": "never", "sandbox_mode": "danger-full-access", "model_catalog_json": paths.CatalogPath} {
		if config[k] != want {
			t.Fatalf("%s: %v", k, config[k])
		}
	}
	provider := config["model_providers"].(map[string]any)["deepseek"].(map[string]any)
	if provider["wire_api"] != "responses" || provider["experimental_bearer_token"] != key {
		t.Fatal("provider not configured")
	}
	for _, path := range []string{paths.ConfigPath, paths.CatalogPath} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatal(path, err)
		}
	}
	catalog, err := os.ReadFile(paths.CatalogPath)
	if err != nil || !bytes.Equal(catalog, runtimeCatalog()) {
		t.Fatal("runtime model catalog differs from explicit compatibility profile")
	}
	if bytes.Count(catalog, []byte(`"supports_search_tool": false`)) != 1 || !bytes.Equal(bytes.Replace(catalog, []byte(`"supports_search_tool": false`), []byte(`"supports_search_tool": true`), 1), Catalog()) {
		t.Fatal("runtime override changed upstream metadata beyond tool discovery")
	}
	digest := sha256.Sum256(catalog)
	if paths.CatalogSHA256 != hex.EncodeToString(digest[:]) {
		t.Fatal("runtime catalog digest does not describe installed bytes")
	}
}

func TestInvalidSettingsDoNotWrite(t *testing.T) {
	for _, s := range []RuntimeSettings{{APIKey: "x", ReasoningEffort: "medium"}, {APIKey: "x", Provider: "custom", Model: ""}, {APIKey: "x", BaseURL: "file:///tmp/provider"}, {APIKey: "x", BaseURL: "https://user:secret@example.invalid/v1"}, {APIKey: "x\nmalformed"}, {APIKey: "x", MCPCommand: "relative-binary"}} {
		d := filepath.Join(t.TempDir(), "not-created")
		if _, err := Materialize(d, s); err == nil {
			t.Fatal("accepted invalid settings")
		}
		if _, err := os.Stat(d); !os.IsNotExist(err) {
			t.Fatal("invalid input mutated filesystem")
		}
	}
}

func TestOpenAIAndCustomResponsesConfiguration(t *testing.T) {
	for _, s := range []RuntimeSettings{
		{Provider: "openai", Model: "gpt-4o", APIKey: "test-key"},
		{Provider: "custom", Model: "my-model", BaseURL: "https://models.example.invalid/api/v1", APIKey: "test-key", ContextWindow: 128000},
		{Provider: "custom", Model: "local-model", BaseURL: "http://llm:8000/v1", APIKey: "test-key"},
	} {
		t.Run(s.Model, func(t *testing.T) {
			files, err := Materialize(t.TempDir(), s)
			if err != nil {
				t.Fatal(err)
			}
			var config map[string]any
			raw, err := os.ReadFile(files.ConfigPath)
			if err != nil {
				t.Fatal(err)
			}
			if err := toml.Unmarshal(raw, &config); err != nil {
				t.Fatal(err)
			}
			if files.CatalogPath != "" || config["model_catalog_json"] != nil {
				t.Fatal("generic model inherited DeepSeek catalog")
			}
			if s.Provider == "openai" && files.ProviderID != "stoneage_openai" {
				t.Fatal("overrode reserved OpenAI provider ID")
			}
			provider := config["model_providers"].(map[string]any)[files.ProviderID].(map[string]any)
			url := s.BaseURL
			if url == "" {
				url = OpenAIBaseURL
			}
			if provider["base_url"] != url || provider["wire_api"] != "responses" || provider["requires_openai_auth"] != false || config["model"] != s.Model {
				t.Fatal("custom provider configuration lost")
			}
			if config["approval_policy"] != "never" || config["sandbox_mode"] != "danger-full-access" {
				t.Fatal("unattended policies changed")
			}
			if config["model_reasoning_effort"] != nil {
				t.Fatal("forced DeepSeek reasoning on generic model")
			}
		})
	}
}

func TestCustomDeepSeekEndpointRetainsReviewedModelCatalog(t *testing.T) {
	files, err := Materialize(t.TempDir(), RuntimeSettings{Provider: "custom", Model: DeepSeekFlash, BaseURL: "https://proxy.example.invalid/v1", APIKey: "test-key"})
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(runtimeCatalog())
	if files.CatalogSHA256 != hex.EncodeToString(digest[:]) || files.CatalogPath == "" {
		t.Fatal("lost DeepSeek model capabilities at custom endpoint")
	}
}

func TestRuntimeDoesNotFollowConfigSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "kept")
	if err := os.WriteFile(target, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "config.toml")); err != nil {
		t.Fatal(err)
	}
	if _, err := Materialize(root, RuntimeSettings{APIKey: "x"}); err == nil {
		t.Fatal("followed symlink")
	}
	data, _ := os.ReadFile(target)
	if string(data) != "unchanged" {
		t.Fatal("modified symlink target")
	}
}

func TestMaterializeRejectsOperatorCodexAndProjectAliasesBeforeWriting(t *testing.T) {
	root := materializeTestRoot(t)
	operatorHome := filepath.Join(root, "operator-home")
	operatorCodex := filepath.Join(operatorHome, ".codex")
	if err := os.MkdirAll(operatorCodex, 0700); err != nil {
		t.Fatal(err)
	}
	operatorAlias := filepath.Join(root, "operator-alias")
	if err := os.Symlink(operatorCodex, operatorAlias); err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(root, "project")
	projectCodexTarget := filepath.Join(root, "project-codex")
	if err := os.MkdirAll(projectCodexTarget, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(project, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(projectCodexTarget, filepath.Join(project, ".codex")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", operatorHome)
	t.Setenv("CODEX_HOME", operatorCodex)
	changeMaterializeTestDir(t, project)

	for _, path := range []string{
		operatorCodex,
		filepath.Join(operatorAlias, "agent"),
		filepath.Join(project, ".codex"),
		filepath.Join(projectCodexTarget, "agent"),
	} {
		if _, err := Materialize(path, RuntimeSettings{Provider: "custom", Model: "fixture-model", BaseURL: "http://llm:8000/v1", APIKey: "fixture-key"}); !errors.Is(err, runtimepath.ErrForbiddenPath) {
			t.Fatalf("Materialize(%q) error = %v, want protected path rejection", path, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(operatorCodex, "agent")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Materialize touched operator Codex home: %v", err)
	}
}

func TestMaterializeAllowsIndependentBuildAIRuntimeHome(t *testing.T) {
	root := materializeTestRoot(t)
	operatorHome := filepath.Join(root, "operator-home")
	if err := os.MkdirAll(filepath.Join(operatorHome, ".codex"), 0700); err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", operatorHome)
	t.Setenv("CODEX_HOME", filepath.Join(operatorHome, ".codex"))
	changeMaterializeTestDir(t, project)
	runtimeHome := filepath.Join(root, "runtime", "codex", "agent")
	paths, err := Materialize(runtimeHome, RuntimeSettings{Provider: "custom", Model: "fixture-model", BaseURL: "http://llm:8000/v1", APIKey: "fixture-key"})
	if err != nil {
		t.Fatal(err)
	}
	if paths.ProviderID != "custom" || paths.CatalogPath != "" || paths.ConfigPath != filepath.Join(runtimeHome, "config.toml") {
		t.Fatalf("runtime files = %+v", paths)
	}
	if _, err := os.Stat(paths.ConfigPath); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(operatorHome, ".codex", "config.toml")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("operator Codex config was touched: %v", err)
	}
}

func TestMinimumCodexVersion(t *testing.T) {
	for _, v := range []string{"codex-cli 0.144.0", "codex-cli 0.145.1", "1.0.0"} {
		if err := CheckCodexVersion(v); err != nil {
			t.Fatal(v, err)
		}
	}
	for _, v := range []string{"codex-cli 0.143.9", "unknown", ""} {
		if err := CheckCodexVersion(v); err == nil {
			t.Fatal(v)
		}
	}
}
