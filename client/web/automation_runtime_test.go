package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAutomationRuntimePartialConfigurationDoesNotOpenStores(t *testing.T) {
	// A deployment typo must fail before creating databases or loading paths.
	// Exercise every partial combination, including a database-only setup.
	for mask := 1; mask < 15; mask++ {
		root := t.TempDir()
		config := Config{}
		fields := []*string{&config.AutomationKnowledgeDataDir, &config.AutomationMapDataDir, &config.AutomationDB, &config.ReceiptDB}
		for index, field := range fields {
			if mask&(1<<index) != 0 {
				*field = filepath.Join(root, []string{"knowledge", "maps", "plans.db", "receipts.db"}[index])
			}
		}
		executor, closeRuntime, err := configureAutomationRuntime(config)
		if err == nil || !strings.Contains(err.Error(), "configuration is incomplete") || executor != nil || closeRuntime != nil {
			t.Fatalf("partial configuration %04b did not fail closed: %v", mask, err)
		}
		entries, err := os.ReadDir(root)
		if err != nil || len(entries) != 0 {
			t.Fatalf("partial configuration %04b wrote runtime state: %v", mask, err)
		}
	}
}

func TestAutomationRuntimeUnconfiguredLeavesManualPlayAvailable(t *testing.T) {
	executor, closeRuntime, err := configureAutomationRuntime(Config{})
	if err != nil || executor != nil || closeRuntime != nil {
		t.Fatalf("unconfigured automation should not prevent manual play: %v", err)
	}
}

func TestAutomationNPCRegistryConfigurationPrecedence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "web.toml")
	if err := os.WriteFile(path, []byte("[automation]\nnpc_registry = 'file-contract.json'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STONEAGE_AI_NPC_REGISTRY", "")
	config, _, err := configFromCommandLine([]string{"-config", path})
	if err != nil || config.AutomationNPCRegistry != "file-contract.json" {
		t.Fatalf("file NPC registry config: %q %v", config.AutomationNPCRegistry, err)
	}
	t.Setenv("STONEAGE_AI_NPC_REGISTRY", "env-contract.json")
	config, _, err = configFromCommandLine([]string{"-config", path})
	if err != nil || config.AutomationNPCRegistry != "env-contract.json" {
		t.Fatalf("environment NPC registry override: %q %v", config.AutomationNPCRegistry, err)
	}
	config, _, err = configFromCommandLine([]string{"-config", path, "-ai-npc-registry", "flag-contract.json"})
	if err != nil || config.AutomationNPCRegistry != "flag-contract.json" {
		t.Fatalf("flag NPC registry override: %q %v", config.AutomationNPCRegistry, err)
	}
	// A registry alone must not silently disable automation or open stores.
	_, _, err = configureAutomationRuntime(Config{AutomationNPCRegistry: "contract.json"})
	if err == nil || !strings.Contains(err.Error(), "configuration is incomplete") {
		t.Fatalf("registry-only setup did not report missing dependencies: %v", err)
	}
}

func TestAutomationHealingItemsConfigurationPrecedence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "web.toml")
	if err := os.WriteFile(path, []byte("[automation]\nhealing_items = 'file-contract.json'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STONEAGE_AI_HEALING_ITEMS", "")
	config, _, err := configFromCommandLine([]string{"-config", path})
	if err != nil || config.AutomationHealingItems != "file-contract.json" {
		t.Fatalf("file healing item config: %q %v", config.AutomationHealingItems, err)
	}
	t.Setenv("STONEAGE_AI_HEALING_ITEMS", "env-contract.json")
	config, _, err = configFromCommandLine([]string{"-config", path})
	if err != nil || config.AutomationHealingItems != "env-contract.json" {
		t.Fatalf("environment healing item override: %q %v", config.AutomationHealingItems, err)
	}
	config, _, err = configFromCommandLine([]string{"-config", path, "-ai-healing-items", "flag-contract.json"})
	if err != nil || config.AutomationHealingItems != "flag-contract.json" {
		t.Fatalf("flag healing item override: %q %v", config.AutomationHealingItems, err)
	}
	// A registry alone must not silently disable automation or open stores.
	_, _, err = configureAutomationRuntime(Config{AutomationHealingItems: "contract.json"})
	if err == nil || !strings.Contains(err.Error(), "configuration is incomplete") {
		t.Fatalf("registry-only setup did not report missing dependencies: %v", err)
	}
}

func TestAutomationRuntimeLoadsHealingItemCatalog(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join("..", "..", "server", "legacy", "source", "2.5", "gmsv", "data")
	config := Config{
		AutomationKnowledgeDataDir: data, AutomationMapDataDir: data,
		AutomationDB: filepath.Join(root, "plans.db"), ReceiptDB: filepath.Join(root, "receipts.db"),
		AutomationHealingItems: filepath.Join("..", "..", "ai", "catalogs", "healing-items-2.5.json"),
	}
	executor, closeRuntime, err := configureAutomationRuntime(config)
	if err != nil {
		t.Fatal(err)
	}
	defer closeRuntime()
	item := executor.(*AutomationExecutor).config.HealingItems["small-meat"]
	if item.TemplateID != 2344 || item.BaseHP != 20 || !item.Verified {
		t.Fatalf("healing catalog not wired: %+v", item)
	}
}

func TestAutomationStockItemsConfigurationPrecedence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "web.toml")
	if err := os.WriteFile(path, []byte("[automation]\nstock_items = 'file-contract.json'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STONEAGE_AI_STOCK_ITEMS", "")
	config, _, err := configFromCommandLine([]string{"-config", path})
	if err != nil || config.AutomationStockItems != "file-contract.json" {
		t.Fatalf("file healing item config: %q %v", config.AutomationStockItems, err)
	}
	t.Setenv("STONEAGE_AI_STOCK_ITEMS", "env-contract.json")
	config, _, err = configFromCommandLine([]string{"-config", path})
	if err != nil || config.AutomationStockItems != "env-contract.json" {
		t.Fatalf("environment healing item override: %q %v", config.AutomationStockItems, err)
	}
	config, _, err = configFromCommandLine([]string{"-config", path, "-ai-stock-items", "flag-contract.json"})
	if err != nil || config.AutomationStockItems != "flag-contract.json" {
		t.Fatalf("flag healing item override: %q %v", config.AutomationStockItems, err)
	}
	// A registry alone must not silently disable automation or open stores.
	_, _, err = configureAutomationRuntime(Config{AutomationStockItems: "contract.json"})
	if err == nil || !strings.Contains(err.Error(), "configuration is incomplete") {
		t.Fatalf("registry-only setup did not report missing dependencies: %v", err)
	}
}

func TestAutomationRuntimeLoadsStockCatalogAndRequiresDependencies(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join("..", "..", "server", "legacy", "source", "2.5", "gmsv", "data")
	catalog := filepath.Join("..", "..", "ai", "catalogs")
	config := Config{
		AutomationKnowledgeDataDir: data, AutomationMapDataDir: data,
		AutomationDB: filepath.Join(root, "plans.db"), ReceiptDB: filepath.Join(root, "receipts.db"),
		AutomationStockItems: filepath.Join(catalog, "stock-items-2.5.json"),
	}
	if _, _, err := configureAutomationRuntime(config); err == nil {
		t.Fatal("stock without required NPC/item catalogs enabled")
	}
	if _, err := os.Stat(config.AutomationDB); !os.IsNotExist(err) {
		t.Fatal("failed stock configuration created a store")
	}
	config.AutomationNPCRegistry = filepath.Join(catalog, "shops-2.5.json")
	config.AutomationHealingItems = filepath.Join(catalog, "healing-items-2.5.json")
	executor, closeRuntime, err := configureAutomationRuntime(config)
	if err != nil {
		t.Fatal(err)
	}
	defer closeRuntime()
	offer := executor.(*AutomationExecutor).config.StockItems["small-meat"]
	if offer.TemplateID != 2344 || offer.NPC.Floor != 1004 || offer.ShopIndex != 1 || offer.UnitPrice != 12 || offer.X != 17 || offer.Y != 15 {
		t.Fatalf("stock catalog not wired: %+v", offer)
	}
}
