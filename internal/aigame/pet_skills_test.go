package aigame

import "testing"

func TestNativePetSkillsAndSelectedSlot(t *testing.T) {
	state := newGameState(true)
	state.upsertPet(3, PetSnapshot{Slot: 3, Name: "pet", Graphic: 100, HP: 20})
	// Two empty native slots, then a skill with an escaped display delimiter.
	state.applySystem(`W3|||||||||||1|1|6|攻\z击|普通\z攻击`)
	pet, _ := state.petForSlot(3)
	if len(pet.Skills) != 1 || pet.Skills[0].Index != 2 || pet.Skills[0].ID != 1 || pet.Skills[0].Target != 6 || pet.Skills[0].Field != 1 || pet.Skills[0].Name != "攻|击" || pet.Skills[0].Memo != "普通|攻击" {
		t.Fatalf("native sparse skill record: %+v", pet.Skills)
	}
	ks := func(slot, result int32) {
		applyEventLocked(&state, Event{Function: "KS", Fields: []Field{{Kind: FieldInt, Int: slot}, {Kind: FieldInt, Int: result}}})
	}
	ks(3, 1)
	if !state.snapshot.Player.BattlePetSlotKnown || state.snapshot.Player.BattlePetSlot != 3 {
		t.Fatal("KS success not recorded")
	}
	ks(1, 0)
	if state.snapshot.Player.BattlePetSlot != 3 {
		t.Fatal("failed KS changed selected pet")
	}
	ks(9, 1)
	if state.snapshot.Player.BattlePetSlot != 3 {
		t.Fatal("invalid KS changed selected pet")
	}
	state.applySystem("W3|7|1|106|revive")
	pet, _ = state.petForSlot(3)
	if len(pet.Skills) != 1 || !pet.Skills[0].DeadTarget || pet.Skills[0].Target != 6 {
		t.Fatal("dead-target metadata lost")
	}
	state.applySystem("W3||||||")
	pet, _ = state.petForSlot(3)
	if len(pet.Skills) != 0 {
		t.Fatal("empty skill list retained old skills")
	}
	state.applySystem("W3|invalid|1|6|attack")
	pet, _ = state.petForSlot(3)
	if len(pet.Skills) != 0 {
		t.Fatal("malformed skill became usable")
	}
	state.clearPet(3)
	if state.snapshot.Player.BattlePetSlotKnown {
		t.Fatal("removed pet retained selected identity")
	}
	state.applySystem("W3|1|1|6|attack|memo")
	if _, exists := state.petForSlot(3); exists {
		t.Fatal("skills invented an absent pet")
	}
	ks(-1, 1)
	if !state.snapshot.Player.BattlePetSlotKnown || state.snapshot.Player.BattlePetSlot != -1 {
		t.Fatal("pet recall not recorded")
	}
}
