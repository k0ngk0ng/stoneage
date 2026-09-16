package automation

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

type fakeGame struct {
	observation Observation
	actions     []Action
	execute     func(Action) error
}

type unavailableSkillGame struct{ *fakeGame }

func (unavailableSkillGame) ValidateSkill(context.Context, Action) error {
	return errors.New("skill unavailable")
}

func TestPreflightRejectsUninstalledDeterministicSkill(t *testing.T) {
	e, g, p := fixture(t)
	e.Game = unavailableSkillGame{g}
	pre, err := e.Preflight(context.Background(), p)
	if err != nil || pre.Ready {
		t.Fatalf("unavailable skill preflight: %+v %v", pre, err)
	}
	if _, err = e.Start(context.Background(), p); err == nil {
		t.Fatal("started unavailable skill")
	}
	if len(g.actions) != 0 {
		t.Fatal("preflight sent game action")
	}
}

func (g *fakeGame) Observe(context.Context) (Observation, error) { return g.observation, nil }
func (g *fakeGame) Execute(ctx context.Context, a Action) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	g.actions = append(g.actions, a)
	if g.execute != nil {
		return g.execute(a)
	}
	return nil
}

func fixture(t *testing.T) (*Engine, *fakeGame, Plan) {
	t.Helper()
	store, err := OpenStore(filepath.Join(t.TempDir(), "automation.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	g := &fakeGame{observation: Observation{Revision: 1, CharacterID: "account:0", Connected: true, Ready: true, Character: Entity{ID: "account:0", Level: 10, HP: 100, MaxHP: 100}, Gold: 500, Spent: 12, SpendingKnown: true, Inventory: map[string]int{}, Flags: map[string]bool{}}}
	p := Plan{ID: "quest-1", CharacterID: "account:0", Mode: "quest", KnowledgeRevision: "test-v1", Title: "delivery", MaximumSeconds: 3600, MaximumDeaths: 0, Budget: Budget{Known: true, MaximumSpend: 100, Minimum: 10, Reserve: 50}, Completion: []Condition{{Kind: "flag_set", ID: "quest_done"}}, Steps: []Step{{ID: "deliver", Description: "交付物品", Action: Action{Skill: "npc.window", Arguments: json.RawMessage(`{"choice":1}`)}, Success: []Condition{{Kind: "flag_set", ID: "quest_done"}}, TimeoutSeconds: 10, CostKnown: true, MaximumCost: 10}}}
	return &Engine{Store: store, Game: g}, g, p
}

func TestLostDeliveryIsNeverRetried(t *testing.T) {
	ctx := context.Background()
	e, g, p := fixture(t)
	if _, err := e.Start(ctx, p); err != nil {
		t.Fatal(err)
	}
	g.execute = func(Action) error { return errors.New("response lost") }
	c, err := e.Tick(ctx, p.ID)
	if err == nil || c.Status != Paused || c.Phase != "prepared" {
		t.Fatal(c, err)
	}
	if _, err = e.Resume(ctx, p.ID); err == nil {
		t.Fatal("uncertain delivery retried")
	}
	if len(g.actions) != 1 {
		t.Fatal(g.actions)
	}
	g.observation.Flags["quest_done"] = true
	c, err = e.Resume(ctx, p.ID)
	if err != nil || c.Status != Completed {
		t.Fatal(c, err)
	}
	if len(g.actions) != 1 {
		t.Fatal("duplicate delivery")
	}
}

func TestLevelTargetStopsBeforeNextActionAndUsesPetIdentity(t *testing.T) {
	ctx := context.Background()
	e, g, p := fixture(t)
	p.Mode = "leveling"
	p.TargetPolicy = "all"
	p.Targets = []Target{{Kind: "pet", ID: "pet-stable-A", Level: 20}}
	g.observation.Pets = []Entity{{ID: "pet-stable-B", Level: 99}, {ID: "pet-stable-A", Level: 19}}
	if _, err := e.Start(ctx, p); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Tick(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	g.observation.Pets = []Entity{{ID: "pet-stable-A", Level: 21}, {ID: "pet-stable-B", Level: 99}}
	g.observation.Battle = true
	c, err := e.Tick(ctx, p.ID)
	if err != nil || c.Status != Completed {
		t.Fatal(c, err)
	}
	if len(g.actions) != 1 {
		t.Fatal("another action after target")
	}
}

func TestCrashPreparedCheckpointReconcilesWithoutResending(t *testing.T) {
	ctx := context.Background()
	e, g, p := fixture(t)
	c, err := e.Start(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	c.Phase = "prepared"
	c.Revision++
	if err = e.Store.Save(ctx, c, 1); err != nil {
		t.Fatal(err)
	}
	c, err = e.Tick(ctx, p.ID)
	if err != nil || c.Status != Paused {
		t.Fatal(c, err)
	}
	if len(g.actions) != 0 {
		t.Fatal("replayed crash command")
	}
}

func TestBudgetRejectsNextPurchaseWithoutTrustingIncome(t *testing.T) {
	ctx := context.Background()
	e, g, p := fixture(t)
	if _, err := e.Start(ctx, p); err != nil {
		t.Fatal(err)
	}
	g.observation.Spent += 95
	g.observation.Gold = 10000
	c, err := e.Tick(ctx, p.ID)
	if err != nil || c.Status != Paused {
		t.Fatal(c, err)
	}
	if len(g.actions) != 0 {
		t.Fatal("budget overrun")
	}
}

func TestUnknownCostsAndForeignCharacterCannotStart(t *testing.T) {
	ctx := context.Background()
	e, g, p := fixture(t)
	p.Steps[0].CostKnown = false
	if _, err := e.Start(ctx, p); err == nil {
		t.Fatal("unknown cost accepted")
	}
	p.Steps[0].CostKnown = true
	g.observation.CharacterID = "other:0"
	if _, err := e.Start(ctx, p); err == nil {
		t.Fatal("wrong character accepted")
	}
}

func TestCheckpointCASAndOneActivePlanPerCharacter(t *testing.T) {
	ctx := context.Background()
	e, _, p := fixture(t)
	c, err := e.Start(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	p.ID = "other"
	if _, err = e.Start(ctx, p); err == nil {
		t.Fatal("two active plans")
	}
	c.Status = Paused
	c.Revision++
	if err = e.Store.Save(ctx, c, 1); err != nil {
		t.Fatal(err)
	}
	if err = e.Store.Save(ctx, c, 1); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
}

func TestTimeoutWaitsForAuthoritativeSuccess(t *testing.T) {
	ctx := context.Background()
	e, g, p := fixture(t)
	now := time.Now()
	e.Now = func() time.Time { return now }
	if _, err := e.Start(ctx, p); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Tick(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	now = now.Add(11 * time.Second)
	c, err := e.Tick(ctx, p.ID)
	if err != nil || c.Status != Paused {
		t.Fatal(c, err)
	}
	if len(g.actions) != 1 {
		t.Fatal("retry on timeout")
	}
}

func TestUnknownFlagIsNotFalseEvidence(t *testing.T) {
	if (Condition{Kind: "flag_clear", ID: "unobserved"}).Match(Observation{}) {
		t.Fatal("missing data treated as known false")
	}
}

func TestLegacyBudgetUsesDurableMaximumReservation(t *testing.T) {
	ctx := context.Background()
	e, g, p := fixture(t)
	g.observation.SpendingKnown = false
	p.Budget.MaximumSpend = 10
	if _, err := e.Start(ctx, p); err != nil {
		t.Fatal(err)
	}
	c, err := e.Tick(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if c.ReservedSpend != 10 || len(g.actions) != 1 || g.actions[0].MaximumCost != 10 {
		t.Fatal("cost was not fenced before execution")
	}
	stored, err := e.Store.Load(ctx, p.ID)
	if err != nil || stored.ReservedSpend != 10 {
		t.Fatal("reservation is not durable")
	}
}

func TestServerUnlimitedFundingDoesNotPauseForEmptyGold(t *testing.T) {
	ctx := context.Background()
	e, g, p := fixture(t)
	g.observation.Gold = 0
	g.observation.UnlimitedFunds = true
	p.Budget.MaximumSpend = 0
	p.Preconditions = []Condition{{Kind: "gold_at_least", Value: 5000}}
	if _, err := e.Start(ctx, p); err != nil {
		t.Fatal(err)
	}
	c, err := e.Tick(ctx, p.ID)
	if err != nil || c.Status != Running || len(g.actions) != 1 {
		t.Fatal(c, err)
	}
	// A funding revocation must be enforced on the next operation.
	g.observation.UnlimitedFunds = false
	c, err = e.Tick(ctx, p.ID)
	if err != nil || c.Status != Paused {
		t.Fatal(c, err)
	}
}
