package battleauto

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
)

// Strategy is a local, deterministic decision extension. It receives an
// immutable observation, never a socket or credentials. The runner alone
// validates and submits its decision against that observation's revision.
// Implementations must honor cancellation; no subprocess/LLM loader exists.
type Strategy interface {
	Info() StrategyInfo
	Decide(context.Context, aigame.Snapshot) (Decision, bool, error)
}

type StrategyInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Tier        string `json:"tier"`
	Description string `json:"description"`
}

// Strategies is immutable after construction. Extensions shared by multiple
// sessions must be safe for concurrent calls. A missing selection is an error,
// never an implicit fallback.
type Strategies struct{ entries map[string]Strategy }

func (r Strategies) Has(id string) bool { _, ok := r.entries[id]; return ok }

func NewStrategies(tables *aiknowledge.RecoveryTables, extensions ...Strategy) (Strategies, error) {
	registry := Strategies{entries: map[string]Strategy{"basic": basicStrategy{tables}}}
	for _, strategy := range extensions {
		if strategy == nil {
			return Strategies{}, fmt.Errorf("nil battle strategy")
		}
		info := strategy.Info()
		if !strategyID(info.ID) || info.ID == "manual" || info.Name == "" || (info.Tier != "intermediate" && info.Tier != "advanced") {
			return Strategies{}, fmt.Errorf("invalid battle strategy metadata")
		}
		if _, exists := registry.entries[info.ID]; exists {
			return Strategies{}, fmt.Errorf("duplicate battle strategy %q", info.ID)
		}
		registry.entries[info.ID] = strategy
	}
	return registry, nil
}

func strategyID(id string) bool {
	if len(id) == 0 || len(id) > 32 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

func (r Strategies) List() []StrategyInfo {
	result := []StrategyInfo{{ID: "manual", Name: "手动 / 本地脚本", Tier: "manual", Description: "停止内置自动出招，使用正常战斗操作或 sactl 控制。"}}
	for _, strategy := range r.entries {
		result = append(result, strategy.Info())
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func (r Strategies) Decide(ctx context.Context, snapshot aigame.Snapshot) (Decision, bool, error) {
	if snapshot.Ladder == nil || snapshot.Battle.LadderID == "" || !snapshot.Battle.Active || snapshot.Battle.Ended ||
		snapshot.Ladder.Snapshot.Phase != "battle" || snapshot.Ladder.Snapshot.Match == nil || snapshot.Ladder.Snapshot.Match.ID != snapshot.Battle.LadderID {
		return Decision{}, false, nil
	}
	selected := snapshot.Ladder.Snapshot.Self.Strategy
	if selected == "manual" || snapshot.Ladder.Snapshot.Self.Abandoned {
		return Decision{}, false, nil
	}
	strategy, ok := r.entries[selected]
	if !ok {
		return Decision{}, false, fmt.Errorf("ladder strategy %q is not installed", selected)
	}
	// A late decision must not outlive its caller or cross into the next
	// match/turn. ExecuteExpected performs the final revision check.
	ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	decision, ready, err := strategy.Decide(ctx, snapshot)
	if err != nil {
		return Decision{}, false, err
	}
	if err = ctx.Err(); err != nil {
		return Decision{}, false, err
	}
	if ready && decision.Action.Kind != aigame.ActionBattle {
		return Decision{}, false, fmt.Errorf("ladder strategy returned a non-battle action")
	}
	return decision, ready, nil
}

type basicStrategy struct{ tables *aiknowledge.RecoveryTables }

func (s basicStrategy) Info() StrategyInfo {
	return StrategyInfo{ID: "basic", Name: "初级自动战斗", Tier: "basic", Description: "优先治疗，按固定顺序攻击存活目标，使用宠物默认技能；不逃跑、不走动。"}
}
func (s basicStrategy) Decide(ctx context.Context, snapshot aigame.Snapshot) (Decision, bool, error) {
	if err := ctx.Err(); err != nil {
		return Decision{}, false, err
	}
	decision, ok := Decide(snapshot, s.tables, DefaultPolicy())
	return decision, ok, nil
}
