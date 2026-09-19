package battleauto

import (
	"strings"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
)

// A ready turn: both menus are open, the roster has arrived, and nobody has
// answered yet.
func readyBattle() aigame.BattleSnapshot {
	return aigame.BattleSnapshot{
		Active:     true,
		MyNo:       0,
		MyNoKnown:  true,
		BPReceived: true,
		BCReceived: true,
		Participants: []aigame.BattleParticipant{
			{BattleID: 0, Name: "Hero", HP: 100, MaxHP: 100, Player: true},
			{BattleID: 5, Name: "Puku", Graphic: 100, HP: 60, MaxHP: 60},
			{BattleID: 10, Name: "Slime", HP: 40, MaxHP: 40},
			{BattleID: 11, Name: "Bat", HP: 30, MaxHP: 30},
		},
	}
}

func tables() *aiknowledge.RecoveryTables {
	return &aiknowledge.RecoveryTables{
		Magic: []aiknowledge.MagicRecovery{
			{ID: 0, Amount: 80, Target: 0, Name: "治愈的精灵 Lv1"},  // self only
			{ID: 10, Amount: 65, Target: 1, Name: "滋润的精灵 Lv1"}, // one ally
			{ID: 11, Amount: 130, Target: 1, Name: "滋润的精灵 Lv2"},
		},
		Items: map[string]aiknowledge.ItemRecovery{
			"小块肉":  {Name: "小块肉", Amount: 20},
			"带骨的肉": {Name: "带骨的肉", Amount: 65},
		},
	}
}

func decide(t *testing.T, battle aigame.BattleSnapshot, player aigame.PlayerSnapshot, inventory []aigame.InventoryItem, skills []aigame.SkillSnapshot, policy Policy) Decision {
	t.Helper()
	snapshot := aigame.Snapshot{Battle: battle, Player: player, Inventory: inventory, Skills: skills}
	decision, ok := Decide(snapshot, tables(), policy)
	if !ok {
		t.Fatal("Decide returned no decision for a ready turn")
	}
	return decision
}

func command(t *testing.T, decision Decision) string {
	t.Helper()
	if decision.Action.Kind != aigame.ActionBattle {
		t.Fatalf("action kind = %v, want a battle command", decision.Action.Kind)
	}
	return decision.Action.Command
}

func TestAttacksTheLowestNumberedLivingEnemy(t *testing.T) {
	battle := readyBattle()
	battle.Participants[2].Dead = true // the first enemy is down
	decision := decide(t, battle, aigame.PlayerSnapshot{HP: 100, MaxHP: 100}, nil, nil, DefaultPolicy())
	if got := command(t, decision); got != "H|B" {
		t.Fatalf("command = %q, want the next living enemy", got)
	}
}

func TestHealsTheMostHurtFromTheBagFirst(t *testing.T) {
	battle := readyBattle()
	battle.Participants[0].HP = 20 // the character is at 20%
	bag := []aigame.InventoryItem{{Index: 3, Name: "小块肉"}, {Index: 7, Name: "带骨的肉"}}
	decision := decide(t, battle, aigame.PlayerSnapshot{HP: 20, MaxHP: 100}, bag, nil, DefaultPolicy())
	// 80 missing: 66 does not cover it, 65 neither, so the larger is used.
	if got := command(t, decision); got != "I|7|0" {
		t.Fatalf("command = %q, want the strongest available item on the character", got)
	}
}

func TestHealsTheHurtPetWithAnItemAndTargetsItsRow(t *testing.T) {
	battle := readyBattle()
	battle.Participants[1].HP = 12 // the pet is at 20%
	bag := []aigame.InventoryItem{{Index: 2, Name: "小块肉"}}
	decision := decide(t, battle, aigame.PlayerSnapshot{HP: 100, MaxHP: 100}, bag, nil, DefaultPolicy())
	if got := command(t, decision); got != "I|2|5" {
		t.Fatalf("command = %q, want the item aimed at the pet's row", got)
	}
}

func TestFallsBackToMagicWhenTheBagHasNothing(t *testing.T) {
	battle := readyBattle()
	battle.Participants[1].HP = 12 // the pet is hurt, the bag is empty
	skills := []aigame.SkillSnapshot{{ID: 0, Level: 1}, {ID: 10, Level: 1}}
	decision := decide(t, battle, aigame.PlayerSnapshot{HP: 100, MaxHP: 100}, nil, skills, DefaultPolicy())
	// Skill index 0 is the self-only spell and cannot reach the pet, so the
	// ally-targeting spell at index 1 must be chosen.
	if got := command(t, decision); got != "J|1|5" {
		t.Fatalf("command = %q, want the ally spell aimed at the pet", got)
	}
}

func TestSelfOnlySpellIsNeverAimedAtThePet(t *testing.T) {
	battle := readyBattle()
	battle.Participants[1].HP = 6
	skills := []aigame.SkillSnapshot{{ID: 0, Level: 1}} // self only
	decision := decide(t, battle, aigame.PlayerSnapshot{HP: 100, MaxHP: 100}, nil, skills, DefaultPolicy())
	if got := command(t, decision); strings.HasPrefix(got, "J|") {
		t.Fatalf("command = %q: a self-only spell was aimed at the pet", got)
	}
}

func TestAllyOnlySpellIsNeverAimedAtItself(t *testing.T) {
	battle := readyBattle()
	battle.Participants[0].HP = 10
	skills := []aigame.SkillSnapshot{{ID: 6, Level: 1}} // one ally, never the caster
	spells := &aiknowledge.RecoveryTables{Magic: []aiknowledge.MagicRecovery{
		{ID: 6, Amount: 200, Target: 6, Name: "not self"},
	}}
	snapshot := aigame.Snapshot{Battle: battle, Player: aigame.PlayerSnapshot{HP: 10, MaxHP: 100}, Skills: skills}
	decision, ok := Decide(snapshot, spells, DefaultPolicy())
	if !ok {
		t.Fatal("no decision")
	}
	if got := command(t, decision); strings.HasPrefix(got, "J|") {
		t.Fatalf("command = %q: a spell that refuses its caster was aimed at the character", got)
	}
}

func TestHealsWhoeverIsFurtherBelow(t *testing.T) {
	battle := readyBattle()
	battle.Participants[0].HP = 45 // 45%
	battle.Participants[1].HP = 12 // 20% of 60
	bag := []aigame.InventoryItem{{Index: 1, Name: "带骨的肉"}}
	decision := decide(t, battle, aigame.PlayerSnapshot{HP: 45, MaxHP: 100}, bag, nil, DefaultPolicy())
	if got := command(t, decision); got != "I|1|5" {
		t.Fatalf("command = %q, want the pet, which is further below", got)
	}
}

func TestDoesNotHealAboveTheThreshold(t *testing.T) {
	battle := readyBattle()
	battle.Participants[1].HP = 40 // 66%, above the default 30%
	bag := []aigame.InventoryItem{{Index: 1, Name: "带骨的肉"}}
	decision := decide(t, battle, aigame.PlayerSnapshot{HP: 100, MaxHP: 100}, bag, nil, DefaultPolicy())
	if got := command(t, decision); got != "H|A" {
		t.Fatalf("command = %q, want an attack when nobody is hurt enough", got)
	}
}

func TestNeverRunsAway(t *testing.T) {
	for _, hurt := range []int32{100, 50, 10, 1} {
		battle := readyBattle()
		battle.Participants[0].HP = hurt
		decision := decide(t, battle, aigame.PlayerSnapshot{HP: hurt, MaxHP: 100}, nil, nil, DefaultPolicy())
		if got := command(t, decision); got == "E" {
			t.Fatalf("at %d%% the policy tried to escape", hurt)
		}
	}
}

func TestAcknowledgesTheBattleResult(t *testing.T) {
	battle := readyBattle()
	battle.Result = "win"
	snapshot := aigame.Snapshot{Battle: battle}
	decision, ok := Decide(snapshot, tables(), DefaultPolicy())
	if !ok {
		t.Fatal("no decision for a finished battle")
	}
	if decision.Action.Kind != aigame.ActionBattleEnd {
		t.Fatalf("action kind = %v, want the battle end acknowledgement", decision.Action.Kind)
	}
}

func TestClosedPlayerMenuForcesWait(t *testing.T) {
	battle := readyBattle()
	battle.BPFlags = aigame.BattlePlayerMenuOff
	decision := decide(t, battle, aigame.PlayerSnapshot{HP: 100, MaxHP: 100}, nil, nil, DefaultPolicy())
	if got := command(t, decision); got != "N" {
		t.Fatalf("command = %q, want the single legal wait", got)
	}
}

func TestSubmitsThePetCommandAfterTheCharacter(t *testing.T) {
	battle := readyBattle()
	battle.PlayerSubmitted = true
	pets := []aigame.PetSnapshot{{
		Slot: 0, ID: 1, Graphic: 100, Name: "Puku",
		Skills: []aigame.PetSkillSnapshot{{Index: 2, ID: 1, Field: 1, Target: 6}},
	}}
	snapshot := aigame.Snapshot{
		Battle: battle,
		Player: aigame.PlayerSnapshot{BattlePetSlot: 0, BattlePetSlotKnown: true},
		Pets:   pets,
	}
	decision, ok := Decide(snapshot, tables(), DefaultPolicy())
	if !ok {
		t.Fatal("no decision once the character has answered")
	}
	if got := command(t, decision); got != "W|2|A" {
		t.Fatalf("command = %q, want the pet attacking the same target", got)
	}
}

func TestNoDecisionWhileTheTurnIsNotReady(t *testing.T) {
	battle := readyBattle()
	battle.PlayerSubmitted = true
	battle.Participants[1].Dead = true // no pet command is owed either
	snapshot := aigame.Snapshot{Battle: battle, Player: aigame.PlayerSnapshot{HP: 100, MaxHP: 100}}
	if _, ok := Decide(snapshot, tables(), DefaultPolicy()); ok {
		t.Fatal("a decision was produced after the turn was already answered")
	}
}

// With a live pet the turn is not over when the character answers: the pet
// command is still owed, and the policy has to produce it.
func TestPetCommandIsOwedAfterTheCharacterAnswers(t *testing.T) {
	battle := readyBattle()
	battle.PlayerSubmitted = true
	snapshot := aigame.Snapshot{Battle: battle, Player: aigame.PlayerSnapshot{HP: 100, MaxHP: 100}}
	decision, ok := Decide(snapshot, tables(), DefaultPolicy())
	if !ok {
		t.Fatal("the pet command was not produced")
	}
	if got := command(t, decision); got != "W|FF|FF" {
		t.Fatalf("command = %q, want the pet's default action", got)
	}
}

// A defeated master ends the battle even while the pet still stands: the
// server stops resolving at that point and only the client's EO closes it.
func TestEndsTheBattleWhenTheCharacterIsDown(t *testing.T) {
	battle := readyBattle()
	battle.Participants[0].HP, battle.Participants[0].Dead = 0, true
	snapshot := aigame.Snapshot{Battle: battle, Player: aigame.PlayerSnapshot{Name: "Hero"}}
	decision, ok := Decide(snapshot, tables(), DefaultPolicy())
	if !ok {
		t.Fatal("no decision for a defeated character")
	}
	if decision.Action.Kind != aigame.ActionBattleEnd {
		t.Fatalf("action kind = %v, want the battle end acknowledgement", decision.Action.Kind)
	}
}

// A living party member keeps the battle going even after the character with
// the connection has fallen.
func TestKeepsFightingWhileAnotherPlayerStands(t *testing.T) {
	battle := readyBattle()
	battle.Participants[0].HP, battle.Participants[0].Dead = 0, true
	battle.Participants = append(battle.Participants, aigame.BattleParticipant{
		BattleID: 1, Name: "Friend", HP: 50, MaxHP: 50, Player: true,
	})
	snapshot := aigame.Snapshot{Battle: battle, Player: aigame.PlayerSnapshot{Name: "Hero"}}
	decision, ok := Decide(snapshot, tables(), DefaultPolicy())
	if !ok {
		t.Fatal("no decision")
	}
	if decision.Action.Kind == aigame.ActionBattleEnd {
		t.Fatal("the battle was ended while a party member was still standing")
	}
}

// A concluded battle stays in the projection until the next one starts, so
// answering it must stop at the first EO. A walking loop that kept answering
// never looks for another fight, and the panel shows it as "looking" the whole
// time.
func TestConcludedBattleIsAnsweredOnce(t *testing.T) {
	snapshot := worldSnapshot()
	snapshot.Battle = aigame.BattleSnapshot{Ended: true, Result: "win"}
	policy := DefaultPolicy()
	policy.SeekEncounters = true

	decision, ok := Decide(snapshot, nil, policy)
	if !ok || decision.Action.Kind != aigame.ActionBattleEnd {
		t.Fatalf("a concluded battle must be ended once: %+v ok=%v", decision, ok)
	}
	// The EO is applied: the projection records it and keeps the battle.
	snapshot.Battle.LastCommand = "EO"
	if decision, ok := Decide(snapshot, nil, policy); ok {
		t.Fatalf("a battle already answered with EO must not be answered again: %+v", decision)
	}
}
