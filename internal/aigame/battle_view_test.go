package aigame

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func decisionFixture() Snapshot {
	return Snapshot{Revision: 10, SessionToken: "connection-a", Account: "never-in-model-input", Phase: PhaseBattle,
		Player: PlayerSnapshot{Level: 35, BattlePetSlotKnown: true, BattlePetSlot: 0},
		Pets:   []PetSnapshot{{Slot: 0, Name: "pet", Graphic: 1, Skills: []PetSkillSnapshot{{Index: 0, ID: 1, Field: 1, Target: 6, Name: "attack"}}}},
		Battle: BattleSnapshot{Active: true, LadderID: "match-a", Turn: 3, MyNoKnown: true, MyNo: 0, MyMP: 20,
			BPReceived: true, BCReceived: true, CommandReady: true, Participants: []BattleParticipant{
				{BattleID: 0, HP: 100, MaxHP: 100, Player: true},
				{BattleID: 5, HP: 100, MaxHP: 100, Name: "pet", Graphic: 1},
				{BattleID: 10, HP: 50, MaxHP: 100, Player: true},
				{BattleID: 11, HP: 0, MaxHP: 100, Dead: true, Player: true},
			}},
	}
}

func TestBattleSelectionFencesAndPlayerPetPlan(t *testing.T) {
	s := decisionFixture()
	before, _ := json.Marshal(s)
	v := NewBattleView(s)
	selection := BattleSelection{MatchID: v.MatchID, Turn: v.Turn, ObservationID: v.ID, CandidateID: "player:guard:-1:0"}
	a, err := ResolveBattleSelection(s, selection)
	if err != nil || a.Command != "G" {
		t.Fatalf("guard: %+v %v", a, err)
	}
	after, _ := json.Marshal(s)
	if string(before) != string(after) {
		t.Fatal("observation mutated game snapshot")
	}
	payload, _ := json.Marshal(v)
	if strings.Contains(string(payload), s.Account) {
		t.Fatal("account leaked")
	}
	for _, mutate := range []func(*Snapshot){
		func(s *Snapshot) { s.Battle.Turn++ }, func(s *Snapshot) { s.Battle.LadderID = "other" },
		func(s *Snapshot) { s.SessionToken = "new-login" }, func(s *Snapshot) { s.Battle.MyMP-- },
	} {
		changed := s
		mutate(&changed)
		if _, err := ResolveBattleSelection(changed, selection); !errors.Is(err, ErrStaleRevision) {
			t.Fatal("stale plan accepted", err)
		}
	}
	s.Battle.PlayerSubmitted = true
	s.Battle.LastCommand = "G"
	s.Revision++
	if NewBattleView(s).ID != v.ID {
		t.Fatal("player write invalidated unchanged pet plan")
	}
	if _, err := ResolveBattleSelection(s, selection); !errors.Is(err, ErrBattleNotReady) {
		t.Fatal("duplicate player command", err)
	}
	selection.CandidateID = "pet:skill:0:10"
	if a, err := ResolveBattleSelection(s, selection); err != nil || a.Command != "W|0|A" {
		t.Fatal(a, err)
	}
	s.Battle.PetSubmitted = true
	if _, err := ResolveBattleSelection(s, selection); !errors.Is(err, ErrBattleNotReady) {
		t.Fatal("duplicate pet command", err)
	}
}

func TestBattleCandidatesHonorNativeConditions(t *testing.T) {
	s := decisionFixture()
	s.Magic = []MagicSnapshot{{Index: 0, UseFlag: 1, MP: 10, Field: 1, Target: 8, Name: "side"},
		{Index: 1, UseFlag: 1, MP: 21, Field: 1, Target: 1}, {Index: 2, UseFlag: 1, Field: 2, Target: 1}}
	s.Inventory = []InventoryItem{{Index: 5, Name: "revive", Field: 1, Target: 1, DeadTarget: true},
		{Index: 6, Name: "too-high", Field: 1, Target: 1, Level: 99}, {Index: 0, Name: "equipment", Field: 1, Target: 1}}
	ids := map[string]bool{}
	for _, c := range NewBattleView(s).Candidates {
		ids[c.ID] = true
	}
	for _, id := range []string{"player:magic:0:20", "player:magic:0:21", "player:item:5:11", "pet:skill:0:0"} {
		if !ids[id] {
			t.Fatal("missing native candidate", id)
		}
	}
	for _, id := range []string{"player:attack:-1:11", "player:magic:1:10", "player:magic:2:10", "player:item:6:10", "player:item:0:10", "pet:skill:0:5"} {
		if ids[id] {
			t.Fatal("invalid candidate", id)
		}
	}
	s.Battle.BPFlags = BattlePlayerMenuOff | BattlePetMenuOff
	v := NewBattleView(s)
	if len(v.Candidates) != 2 || v.Candidates[0].Command != "N" || v.Candidates[1].Command != "W|FF|FF" {
		t.Fatal(v.Candidates)
	}
}

func TestBattleTargetsBothFormations(t *testing.T) {
	for _, myNo := range []int32{0, 10} {
		b := BattleSnapshot{MyNoKnown: true, MyNo: myNo}
		for id := int32(0); id < 20; id++ {
			b.Participants = append(b.Participants, BattleParticipant{BattleID: id, HP: 100})
		}
		for target := int32(0); target <= 7; target++ {
			got := BattleTargets(b, "pet", target, false)
			var want []int32
			switch target {
			case 0, 5:
				want = []int32{myNo + 5}
			case 2:
				want = []int32{20 + myNo/10}
			case 3:
				want = []int32{21 - myNo/10}
			case 4:
				want = []int32{22}
			default:
				for id := int32(0); id < 20; id++ {
					if target == 6 && id == myNo+5 || target == 7 && (id == myNo || id == myNo+5) {
						continue
					}
					want = append(want, id)
				}
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("owner %d target %d got %v want %v", myNo, target, got, want)
			}
		}
	}
}

func TestMagicProjectionSlotsEscapesAndLifecycle(t *testing.T) {
	s := &Session{}
	s.applyEvent(stringEvent("S", "J2|1|2|1|1|heal\\zspell|memo|"))
	v := s.Snapshot()
	if len(v.Magic) != 1 || v.Magic[0].Index != 2 || v.Magic[0].MP != 2 || v.Magic[0].Name != "heal|spell" {
		t.Fatal(v.Magic)
	}
	v.Magic[0].MP = 99
	if s.Snapshot().Magic[0].MP != 2 {
		t.Fatal("magic slice aliases state")
	}
	s.applyEvent(stringEvent("S", "J2|0|0|0|0|||"))
	if len(s.Snapshot().Magic) != 1 || s.Snapshot().Magic[0].UseFlag != 0 {
		t.Fatal("slot removal not observed")
	}
	s.applyEvent(stringEvent("CharLogin", "successful"))
	if len(s.Snapshot().Magic) != 0 {
		t.Fatal("magic survived character login")
	}
}

func TestStandbyPetCandidatesAndSubmittedBroadcast(t *testing.T) {
	s := decisionFixture()
	s.Player.RidePet, s.Player.RidePetKnown = -1, true
	s.Player.SummonPetMask, s.Player.SummonPetMaskKnown = 3, true
	s.Player.StandbyPetMaskKnown = true
	s.Player.StandbyPetMask = 3
	s.Pets = append(s.Pets, PetSnapshot{Slot: 1, UseFlag: 1, HP: 30, Name: "reserve"}, PetSnapshot{Slot: 2, UseFlag: 1, HP: 50, Name: "rest"})
	v := NewBattleView(s)
	found := false
	for _, c := range v.Candidates {
		if c.Kind == "switch_pet" && c.Index == 1 {
			found = c.Command == "S|1"
		}
		if c.Kind == "switch_pet" && c.Index == 2 {
			t.Fatal("out-of-mask pet available")
		}
	}
	if !found {
		t.Fatal("standby pet absent")
	}
	s.Battle.AnimationFlags = 0xffff
	if NewBattleView(s).ID != v.ID {
		t.Fatal("submission broadcast invalidated the joint plan")
	}
	s.Player.StandbyPetMaskKnown = false
	for _, c := range NewBattleView(s).Candidates {
		if c.Kind == "switch_pet" && c.Index >= 0 {
			t.Fatal("unknown standby mask guessed")
		}
	}
}
