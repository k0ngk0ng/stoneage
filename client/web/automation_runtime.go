package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
	"github.com/k0ngk0ng/stoneage/internal/ainavigation"
	"github.com/k0ngk0ng/stoneage/internal/aiservice"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

// configureAutomationRuntime opens the server-owned inputs for the Web
// deterministic executor. A partial set of paths is an operator error; an
// entirely empty set deliberately leaves ordinary Web sessions without an
// automation capability.
func configureAutomationRuntime(config Config) (Automation, func() error, error) {
	paths := []struct {
		name  string
		value string
	}{
		{"ai-knowledge-data-dir", config.AutomationKnowledgeDataDir},
		{"ai-map-data-dir", config.AutomationMapDataDir},
		{"ai-automation-db", config.AutomationDB},
		{"ai-receipt-db", config.ReceiptDB},
	}
	configured := 0
	for _, path := range paths {
		if strings.TrimSpace(path.value) != "" {
			configured++
		}
	}
	if configured == 0 && strings.TrimSpace(config.AutomationNPCRegistry) == "" && strings.TrimSpace(config.AutomationHealingItems) == "" && strings.TrimSpace(config.AutomationStockItems) == "" {
		return nil, nil, nil
	}
	if configured != len(paths) {
		missing := make([]string, 0, len(paths)-configured)
		for _, path := range paths {
			if strings.TrimSpace(path.value) == "" {
				missing = append(missing, path.name)
			}
		}
		return nil, nil, fmt.Errorf("AI automation configuration is incomplete; missing %s", strings.Join(missing, ", "))
	}

	ctx := context.Background()
	knowledge, err := aiknowledge.LoadDataDir(ctx, config.AutomationKnowledgeDataDir)
	if err != nil {
		return nil, nil, fmt.Errorf("load AI knowledge data: %w", err)
	}
	npcs, err := aiservice.LoadNPCRegistry(config.AutomationNPCRegistry, knowledge.Fingerprint())
	if err != nil {
		return nil, nil, fmt.Errorf("load AI NPC contracts: %w", err)
	}
	healingItems, err := aiservice.LoadHealingItemsForData(config.AutomationHealingItems, knowledge.Fingerprint(), filepath.Join(config.AutomationKnowledgeDataDir, "itemset.txt"))
	if err != nil {
		return nil, nil, fmt.Errorf("load AI healing item contracts: %w", err)
	}
	stockItems, err := aiservice.LoadStockItems(config.AutomationStockItems, knowledge.Fingerprint(), npcs, healingItems)
	if err != nil {
		return nil, nil, fmt.Errorf("load AI stock contracts: %w", err)
	}
	navigator, err := ainavigation.LoadDataDir(ctx, config.AutomationMapDataDir)
	if err != nil {
		return nil, nil, fmt.Errorf("load AI map data: %w", err)
	}
	plans, err := automation.OpenStore(config.AutomationDB)
	if err != nil {
		return nil, nil, fmt.Errorf("open AI automation store: %w", err)
	}
	releaseRecoveryLock, err := acquireAutomationRecoveryLock(config.AutomationDB)
	if err != nil {
		_ = plans.Close()
		return nil, nil, fmt.Errorf("own Web automation recovery database: %w", err)
	}
	lockRetained := false
	defer func() {
		if !lockRetained {
			releaseRecoveryLock()
		}
	}()
	receipts, err := aiservice.OpenReceiptStore(config.ReceiptDB)
	if err != nil {
		_ = plans.Close()
		return nil, nil, fmt.Errorf("open AI receipt store: %w", err)
	}
	executor, err := NewAutomationExecutor(AutomationExecutorConfig{
		Knowledge:         knowledge,
		Tiles:             navigator,
		Plans:             plans,
		Receipts:          receipts,
		NPCs:              npcs,
		HealingItems:      healingItems,
		StockItems:        stockItems,
		PollInterval:      config.AutomationPollInterval,
		NoProgressTimeout: config.AutomationNoProgressTimeout,
	})
	if err != nil {
		_ = receipts.Close()
		_ = plans.Close()
		return nil, nil, fmt.Errorf("configure AI automation executor: %w", err)
	}
	checkpoints, err := plans.ListRecoveryHistory(ctx)
	if err == nil {
		err = executor.restoreDurableRecoveries(ctx, checkpoints)
	}
	if err != nil {
		_ = receipts.Close()
		_ = plans.Close()
		return nil, nil, fmt.Errorf("restore Web automation recovery: %w", err)
	}
	var closeOnce sync.Once
	var closeErr error
	closeRuntime := func() error {
		closeOnce.Do(func() {
			closeErr = errors.Join(receipts.Close(), plans.Close())
			releaseRecoveryLock()
		})
		return closeErr
	}
	lockRetained = true
	return executor, closeRuntime, nil
}
