package aileveling

import (
	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"testing"
)

func TestLevelingPetAttackRequiresObservedSlotAndMatchingRoster(t *testing.T) {
	makeSnapshot := func() aigame.Snapshot {
		s := battleSnapshot()
		s.Player.BattlePetSlotKnown, s.Player.BattlePetSlot = true, 3
		s.Battle.Participants = append(s.Battle.Participants, aigame.BattleParticipant{BattleID: s.Battle.MyNo + 5, Name: "pet", Graphic: 100, HP: 20})
		s.Pets = []aigame.PetSnapshot{{Slot: 3, Name: "pet", Graphic: 100, HP: 20, Skills: []aigame.PetSkillSnapshot{{Index: 4, ID: 1, Field: 1, Target: 6, Name: "attack"}}}}
		return s
	}
	s := makeSnapshot()
	target, ok := chooseEnemy(s)
	if !ok {
		t.Fatal("fixture lacks enemy")
	}
	if got := levelingPetCommand(s); got != "W|4|"+battleHex(target.BattleID) {
		t.Fatalf("observed sparse skill not selected: %s", got)
	}
	for name, mutate := range map[string]func(*aigame.Snapshot){
		"unknown selection": func(s *aigame.Snapshot) { s.Player.BattlePetSlotKnown = false },
		"wrong slot":        func(s *aigame.Snapshot) { s.Player.BattlePetSlot = 0 },
		"different pet":     func(s *aigame.Snapshot) { s.Pets[0].Graphic++ },
		"different name":    func(s *aigame.Snapshot) { s.Pets[0].Name = "other" },
		"no skill":          func(s *aigame.Snapshot) { s.Pets[0].Skills = nil },
		"not normal attack": func(s *aigame.Snapshot) { s.Pets[0].Skills[0].ID = 2 },
		"wrong field":       func(s *aigame.Snapshot) { s.Pets[0].Skills[0].Field = 2 },
		"wrong target":      func(s *aigame.Snapshot) { s.Pets[0].Skills[0].Target = 5 },
		"dead target":       func(s *aigame.Snapshot) { s.Pets[0].Skills[0].DeadTarget = true },
		"invalid index":     func(s *aigame.Snapshot) { s.Pets[0].Skills[0].Index = 7 },
		"surprise":          func(s *aigame.Snapshot) { s.Battle.BPFlags |= aigame.BattleEnemySurprise },
		"pet menu off":      func(s *aigame.Snapshot) { s.Battle.BPFlags |= aigame.BattlePetMenuOff },
	} {
		t.Run(name, func(t *testing.T) {
			s := makeSnapshot()
			mutate(&s)
			if got := levelingPetCommand(s); got != "W|FF|FF" {
				t.Fatalf("unverified attack: %s", got)
			}
		})
	}
}
