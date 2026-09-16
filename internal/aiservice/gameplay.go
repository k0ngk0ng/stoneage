package aiservice

import (
	"context"
	"errors"

	"github.com/k0ngk0ng/stoneage/internal/aicontrol"
	"github.com/k0ngk0ng/stoneage/internal/aileveling"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
	"github.com/k0ngk0ng/stoneage/internal/aiplanner"
	"github.com/k0ngk0ng/stoneage/internal/automation"
	"github.com/k0ngk0ng/stoneage/internal/characterbuild"
)

// GameplayConfig supplies the shared durable plan store and reviewed game
// data adapters. An empty NPC registry keeps unverified quests unavailable.
type GameplayConfig struct {
	Plans        automation.Store
	Tiles        TileNavigator
	NPCs         NPCRegistry
	Healers      map[string]HealerContract
	HealingItems map[string]HealingItemContract
	StockItems   map[string]StockContract
}

func NewGameplayBuilder(config GameplayConfig) (BackendBuilder, error) {
	if config.Plans == nil || config.Tiles == nil {
		return nil, errors.New("gameplay plan store and tile navigation required")
	}
	healingItems := make(map[string]HealingItemContract, len(config.HealingItems))
	for alias, item := range config.HealingItems {
		if alias == "" || !item.Verified || item.TemplateID <= 0 || item.BaseHP <= 0 || item.SourceFingerprint == "" {
			return nil, errors.New("invalid healing item contract")
		}
		healingItems[alias] = item
	}
	config.HealingItems = healingItems
	stockItems := make(map[string]StockContract, len(config.StockItems))
	for alias, offer := range config.StockItems {
		if alias == "" {
			return nil, errors.New("stock alias is required")
		}
		registry, err := offer.registry(1)
		if err != nil {
			return nil, err
		}
		offer.NPC, _ = registry.Lookup(offer.NPC.Alias)
		stockItems[alias] = offer
	}
	config.StockItems = stockItems
	if config.Healers == nil {
		var err error
		config.Healers, err = config.NPCs.HealerContracts()
		if err != nil {
			return nil, err
		}
	}
	for alias, healer := range config.Healers {
		if alias != healer.NPC.Alias {
			return nil, errors.New("healer contract alias mismatch")
		}
		if _, err := healer.registry(0); err != nil {
			return nil, err
		}
	}
	return func(ctx context.Context, in BackendInput) (aimcp.Backend, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if in.Session == nil || in.Gate == nil || in.Lease == nil || in.Knowledge == nil || in.Receipts == nil {
			return nil, aimcp.ErrBackend
		}
		for _, item := range config.HealingItems {
			if item.SourceFingerprint != in.Knowledge.Fingerprint() {
				return nil, errors.New("healing item knowledge fingerprint mismatch")
			}
		}
		for _, offer := range config.StockItems {
			if offer.NPC.SourceFingerprint != in.Knowledge.Fingerprint() {
				return nil, errors.New("stock knowledge fingerprint mismatch")
			}
		}
		owner := in.Gate.State().Mode
		// Agent profiles predate the explicit backend input and retain their
		// persisted build as a compatibility fallback. Ordinary Web leveling
		// only accepts the server-selected explicit policy, so a profile value
		// can never silently enable character allocation there.
		var characterBuild *characterbuild.Policy
		switch owner {
		case aicontrol.Leveling:
			characterBuild = in.CharacterBuild
		case aicontrol.Agent:
			characterBuild = in.CharacterBuild
			if characterBuild == nil {
				characterBuild = in.Profile.Goal.CharacterBuild
			}
		}
		backend := &GameBackend{Binding: in.Binding, Gate: in.Gate, Owner: owner, Session: in.Session, Funding: in.Funding, Knowledge: in.Knowledge, Receipts: in.Receipts}
		backend.Schedules = in.ScheduleStore
		backend.AgentNotes = in.MemoryStore
		if err := characterBuild.Validate(); err != nil {
			return nil, err
		}
		backend.CharacterBuild = characterBuild.Clone()
		backend.OwnStateRefresh = &OwnStateRefresher{}
		backend.stockOffers = config.StockItems
		move := &MovementSkill{Backend: backend, Navigator: config.Tiles, BattleRecovery: &TravelBattle{Backend: backend}, SafeTravel: true}
		var healthRecovery aileveling.HealthRecovery
		var supplies aileveling.Supplies
		npc := NewNPCSkill(backend, config.NPCs)
		skills := SkillSet{"move": move, "npc.talk": npc, "npc.window": npc}
		if len(config.HealingItems) > 0 {
			healing := &ItemHealingSkill{Backend: backend, Contracts: config.HealingItems}
			skills["item.heal"] = healing
			healthRecovery = &TravelItemRecovery{Healing: healing}
			move.HealthRecovery = healthRecovery
		}
		if len(config.StockItems) > 0 {
			stock := &StockSkill{Backend: backend, Movement: move, Contracts: config.StockItems}
			skills["item.stock"] = stock
			supplies = &LevelingStock{Stock: stock, HealingItems: config.HealingItems}
		}
		if len(config.Healers) > 0 {
			healer := &HealerSkill{Backend: backend, Contracts: config.Healers}
			skills["npc.heal"] = healer
			skills["npc.recover"] = &HealerRecoverySkill{Healer: healer, Tiles: config.Tiles}
		}
		game := &AutomationGame{Backend: backend, Skills: skills, NPCs: config.NPCs, Funding: in.Funding}
		quests := &Tasks{Engine: &automation.Engine{Game: game, Store: config.Plans}, Builder: &QuestPlans{Backend: backend, Planner: aiplanner.New(in.Knowledge), CharacterID: in.Binding.CharacterID}, CharacterID: in.Binding.CharacterID, Lease: in.Lease, Wake: in.Wake}
		leveling := &aileveling.Coordinator{Game: in.Session, Navigator: &LevelingNavigator{Knowledge: in.Knowledge, Tiles: config.Tiles}, Store: config.Plans, Gate: in.Gate, Lease: in.Lease, CharacterID: in.Binding.CharacterID, CharacterName: in.Binding.CharacterName, UnlimitedFunds: in.Funding, Owner: owner, HealthRecovery: healthRecovery}
		backend.Tasks = &GameTasks{Quests: quests, Leveling: leveling, Lease: in.Lease, Wake: in.Wake}
		leveling.Supplies = supplies
		if (owner == aicontrol.Agent || owner == aicontrol.Leveling) && backend.CharacterBuild != nil {
			leveling.CharacterPreparation = &LevelingCharacterBuild{Backend: backend}
		}
		return backend, nil
	}, nil
}

// SkillSet is a fixed dispatch table built by the application. It does not
// install code or accept a handler supplied by an MCP request.
type SkillSet map[string]DeterministicSkill

func (s SkillSet) ValidateSkill(ctx context.Context, a automation.Action) error {
	handler := s[a.Skill]
	if handler == nil {
		return errors.New("deterministic skill is not installed")
	}
	return handler.ValidateSkill(ctx, a)
}
func (s SkillSet) Execute(ctx context.Context, a automation.Action) error {
	handler := s[a.Skill]
	if handler == nil {
		return errors.New("deterministic skill is not installed")
	}
	return handler.Execute(ctx, a)
}

func (b *GameBackend) Close() {
	if b == nil || isNilRuntimeValue(b.Tasks) {
		return
	}
	if tasks, ok := b.Tasks.(interface{ Close() }); ok {
		tasks.Close()
	}
}
