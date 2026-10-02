package aigame

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestPetSelectionWriteRequiresAuthoritativeRefresh(t *testing.T) {
	for _, command := range []string{"status", "standby"} {
		for _, fail := range []bool{false, true} {
			t.Run(fmt.Sprint(command, "/", fail), func(t *testing.T) {
				session, peer := worldTestSession(t)
				session.applyEvent(stringEvent("S", partyObservationBase+"|standby_pet_mask=3|summon_pet_mask=3"))
				action := Action{Kind: ActionPet, Command: command, PetSlot: 1, Value: 0}
				result := make(chan error, 1)
				if fail {
					peer.Close()
				}
				go func() { result <- session.Do(context.Background(), action) }()
				if !fail {
					peer.SetReadDeadline(time.Now().Add(time.Second))
					buf := make([]byte, 4096)
					n, err := peer.Read(buf)
					if err != nil {
						t.Fatal(err)
					}
					event, err := decodeEvent(buf[:n])
					want := "PETST"
					if command == "standby" {
						want = "SPET"
					}
					if err != nil || event.Function != want {
						t.Fatal(event, err)
					}
				}
				err := <-result
				if (err != nil) != fail {
					t.Fatal(err)
				}
				p := session.Snapshot().Player
				if command == "status" && p.SummonPetMaskKnown || command == "standby" && p.StandbyPetMaskKnown {
					t.Fatal("socket write preserved stale selection", p)
				}
				if needed, _ := session.identityRefreshState(); !needed {
					t.Fatal("no read-only refresh scheduled")
				}
				session.applyEvent(stringEvent("S", partyObservationBase+"|standby_pet_mask=1|summon_pet_mask=1"))
				p = session.Snapshot().Player
				if !p.SummonPetMaskKnown || p.SummonPetMask != 1 || !p.StandbyPetMaskKnown || p.StandbyPetMask != 1 {
					t.Fatal("server selection not restored", p)
				}
			})
		}
	}
}

func TestPetSwitchCommandUsesCanonicalDecimalSlot(t *testing.T) {
	for _, command := range []string{"S|-1", "S|0", "S|1", "S|2", "S|3", "s|4"} {
		if got, err := validBattleCommand(command); err != nil || got != strings.ToUpper(command) {
			t.Fatalf("valid switch %q: %q %v", command, got, err)
		}
	}
	for _, command := range []string{"S|", "S|A", "S|FF", "S|5", "S|-2", "S|01", "S|+1", "S|1|0", "S|1x", "S|1\t", "S|-01"} {
		if _, err := validBattleCommand(command); !errors.Is(err, ErrInvalidAction) {
			t.Fatalf("malformed switch could become a native recall: %q %v", command, err)
		}
	}
}

func TestStandbyMaskObservationRefresh(t *testing.T) {
	s := &Session{state: newGameState(false)}
	for _, mask := range []int{3, 0, 31, 5} {
		s.applyEvent(stringEvent("S", partyObservationBase+fmt.Sprintf("|standby_pet_mask=%d", mask)))
		p := s.Snapshot().Player
		if !p.StandbyPetMaskKnown || p.StandbyPetMask != int32(mask) {
			t.Fatal("mask not refreshed", p)
		}
	}
	before := s.Snapshot().Player
	for _, value := range []string{"", "-1", "32", "1.5", "2147483648", "x", "0|standby_pet_mask=1"} {
		packet := partyObservationBase + "|standby_pet_mask=" + value
		if _, ok := parseAIObservation(strings.Split(packet, "|")[1:]); ok {
			t.Fatal("invalid mask accepted", value)
		}
		s.applyEvent(stringEvent("S", packet))
		if s.Snapshot().Player != before {
			t.Fatal("invalid mask changed selection")
		}
	}
	s.applyEvent(stringEvent("S", partyObservationBase))
	if s.Snapshot().Player != before || s.Snapshot().AI.StandbyPetMaskKnown {
		t.Fatal("older response erased or invented mask")
	}
}

func TestPetSwitchCandidatesAndSharedCommandGate(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mutate  func(*Snapshot)
		slot    int32
		allowed bool
	}{
		{"reserve", func(s *Snapshot) {}, 1, true},
		{"recall", func(s *Snapshot) {}, -1, true},
		{"same pet", func(s *Snapshot) {}, 0, false},
		{"out of mask", func(s *Snapshot) { s.Player.StandbyPetMask = 1 }, 1, false},
		{"unknown mask", func(s *Snapshot) { s.Player.StandbyPetMaskKnown = false }, 1, false},
		{"not summonable", func(s *Snapshot) { s.Player.SummonPetMask = 1 }, 1, false},
		{"unknown summon status", func(s *Snapshot) { s.Player.SummonPetMaskKnown = false }, 1, false},
		{"dead reserve", func(s *Snapshot) { s.Pets[1].HP = 0 }, 1, false},
		{"unused", func(s *Snapshot) { s.Pets[1].UseFlag = 0 }, 1, false},
		{"riding", func(s *Snapshot) { s.Player.RidePet = 1 }, 1, false},
		{"unknown riding", func(s *Snapshot) { s.Player.RidePetKnown = false }, 1, false},
		{"unknown active", func(s *Snapshot) { s.Player.BattlePetSlotKnown = false }, 1, false},
		{"unknown recall", func(s *Snapshot) { s.Player.BattlePetSlotKnown = false }, -1, false},
		{"nothing to recall", func(s *Snapshot) { s.Player.BattlePetSlot = -1 }, -1, false},
		{"summon without active", func(s *Snapshot) {
			s.Player.BattlePetSlot = -1
			s.Battle.Participants = append(s.Battle.Participants[:1], s.Battle.Participants[2:]...)
		}, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := decisionFixture()
			s.Player.RidePet, s.Player.RidePetKnown = -1, true
			s.Player.SummonPetMask, s.Player.SummonPetMaskKnown = 3, true
			s.Player.StandbyPetMask, s.Player.StandbyPetMaskKnown = 3, true
			s.Pets = append(s.Pets, PetSnapshot{Slot: 1, UseFlag: 1, HP: 30, Name: "reserve"})
			tc.mutate(&s)
			found := false
			for _, c := range NewBattleView(s).Candidates {
				found = found || c.Kind == "switch_pet" && c.Index == tc.slot
			}
			if found != tc.allowed {
				t.Fatal("candidate gate differs", found)
			}
			state := newGameState(false)
			state.snapshot = s
			for _, pet := range s.Pets {
				state.replacePet(pet.Slot, pet)
			}
			// Slot rebuilds can clear stale active evidence; restore this test's
			// explicitly observed own state after installing the owned objects.
			state.snapshot.Player = s.Player
			_, _, err := validateActionLocked(&state, Battle(fmt.Sprintf("S|%d", tc.slot)))
			if tc.allowed && err != nil || !tc.allowed && !errors.Is(err, ErrInvalidAction) {
				t.Fatal("typed/raw CLI gate differs", err)
			}
			if state.snapshot.Battle.PlayerSubmitted || state.snapshot.Player.BattlePetSlot != s.Player.BattlePetSlot {
				t.Fatal("validation changed active pet or submitted an action")
			}
		})
	}
}

func TestSwitchPlanStillCommandsOutgoingPet(t *testing.T) {
	s := decisionFixture()
	s.Player.RidePet, s.Player.RidePetKnown = -1, true
	s.Player.SummonPetMask, s.Player.SummonPetMaskKnown = 3, true
	s.Player.StandbyPetMask, s.Player.StandbyPetMaskKnown = 3, true
	s.Pets = append(s.Pets, PetSnapshot{Slot: 1, UseFlag: 1, HP: 50, Name: "reserve", Skills: []PetSkillSnapshot{{Index: 0, ID: 2, Field: 1, Target: 5}}})
	v := NewBattleView(s)
	choice := BattleSelection{MatchID: v.MatchID, Turn: v.Turn, ObservationID: v.ID, CandidateID: "player:switch_pet:1:0"}
	if a, err := ResolveBattleSelection(s, choice); err != nil || a.Command != "S|1" {
		t.Fatal(a, err)
	}
	s.Battle.PlayerSubmitted = true
	s.Battle.LastCommand = "S|1"
	if after := NewBattleView(s); after.ID != v.ID {
		t.Fatal("pending switch invalidated unchanged outgoing-pet plan")
	}
	choice.CandidateID = "pet:skill:0:10"
	if a, err := ResolveBattleSelection(s, choice); err != nil || a.Command != "W|0|A" {
		t.Fatal("outgoing attack not available", a, err)
	}
}

func TestSummonMaskObservation(t *testing.T) {
	s := &Session{state: newGameState(false)}
	for _, mask := range []int{0, 9, 31} {
		s.applyEvent(stringEvent("S", partyObservationBase+fmt.Sprintf("|summon_pet_mask=%d", mask)))
		if p := s.Snapshot().Player; !p.SummonPetMaskKnown || p.SummonPetMask != int32(mask) {
			t.Fatal(p)
		}
	}
	before := s.Snapshot().Player
	for _, value := range []string{"", "-1", "32", "x", "1.5", "0|summon_pet_mask=1"} {
		s.applyEvent(stringEvent("S", partyObservationBase+"|summon_pet_mask="+value))
		if s.Snapshot().Player != before {
			t.Fatal("invalid mask changed own state", value)
		}
	}
	s.applyEvent(stringEvent("CharLogin", "successful"))
	if s.Snapshot().Player.SummonPetMaskKnown {
		t.Fatal("selection survived character change")
	}
}
