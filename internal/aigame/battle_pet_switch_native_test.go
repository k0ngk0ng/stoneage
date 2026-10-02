package aigame

import (
	"bufio"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Consume actual native packets; don't inject active selection, reserve
// attributes or standby permissions into the projected client state.
func TestNativePetSwitchProjection(t *testing.T) {
	root := os.Getenv("STONEAGE_BATTLE_SWITCH_DIR")
	if root == "" {
		t.Skip("explicit native switch capture required")
	}
	file, err := os.Open(filepath.Join(root, "pet-switch", "stdout.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	replay, err := NewBattleReplay("native-switch", 1)
	if err != nil {
		t.Fatal(err)
	}
	selected := []int32{0, 1, -1, 0}
	skillIDs := []int32{1, 2, -1, 1}
	switchTo := []int32{1, -1, 0, 1}
	frames := 0
	var stream string
	var cursor uint64
	scan := bufio.NewScanner(file)
	scan.Buffer(make([]byte, 4096), 2<<20)
	for scan.Scan() {
		if !strings.HasPrefix(scan.Text(), "SWITCHFRAME|") {
			continue
		}
		var frame struct {
			Turn    int32
			Members []struct {
				Side, Seat int
				Packets    []struct{ Function, Hex string }
			}
		}
		if err := json.Unmarshal(scan.Bytes()[12:], &frame); err != nil {
			t.Fatal(err)
		}
		if frames >= 4 || frame.Turn != int32(frames) || len(frame.Members) != 2 || frame.Members[0].Side != 0 || frame.Members[0].Seat != 0 {
			t.Fatal("unexpected native frame", frames, frame.Turn)
		}
		for _, packet := range frame.Members[0].Packets {
			raw, err := hex.DecodeString(packet.Hex)
			if err != nil {
				t.Fatal(err)
			}
			if err := replay.Apply(packet.Function, raw); err != nil {
				t.Fatal(err)
			}
		}
		view := replay.View(frame.Turn)
		batch := replay.Events(frame.Turn, stream, cursor)
		var switches []string
		for _, event := range batch.Events {
			for _, effect := range event.Effects {
				if effect.Kind == "pet_recall" || effect.Kind == "pet_summon" {
					switches = append(switches, effect.Kind)
				}
			}
		}
		wantEffects := []string{"", "pet_recall,pet_summon", "pet_recall", "pet_summon"}
		if strings.Join(switches, ",") != wantEffects[frames] {
			t.Fatal("native switch movie order lost", frames, switches)
		}
		stream, cursor = batch.Stream, batch.Cursor
		if !view.Own.BattlePetSlotKnown || view.Own.BattlePetSlot != selected[frames] ||
			!view.Own.StandbyPetMaskKnown || view.Own.StandbyPetMask != 3 ||
			!view.Own.SummonPetMaskKnown || view.Own.SummonPetMask != 3 ||
			!view.Own.RidePetKnown || view.Own.RidePet != -1 || len(view.Pets) != 2 {
			t.Fatalf("native own/bench projection at turn %d: %+v; pets=%+v", frame.Turn, view.Own, view.Pets)
		}
		petCommands := 0
		var switchChoice, petChoice string
		for _, candidate := range view.Candidates {
			if candidate.Kind == "switch_pet" && candidate.Index == switchTo[frames] {
				switchChoice = candidate.ID
			}
			if candidate.Actor == "pet" {
				petCommands++
				if candidate.Kind == "skill" && candidate.Index == 0 {
					if candidate.SkillID != skillIDs[frames] {
						t.Fatal("pet skill belongs to wrong active pet", frame.Turn, candidate)
					}
					petChoice = candidate.ID
				}
			}
		}
		if switchChoice == "" || selected[frames] == -1 && petCommands != 0 || selected[frames] >= 0 && petChoice == "" {
			t.Fatalf("native candidates missing or assigned to absent pet at turn %d: %+v", frame.Turn, view.Candidates)
		}
		choices := []BattleSelection{{MatchID: view.MatchID, Turn: view.Turn, ObservationID: view.ID, CandidateID: switchChoice}}
		if petChoice != "" {
			choices = append(choices, BattleSelection{MatchID: view.MatchID, Turn: view.Turn, ObservationID: view.ID, CandidateID: petChoice})
		}
		if _, err := replay.ResolvePlan(frame.Turn, choices); err != nil {
			t.Fatal("native outgoing-pet plan cannot execute through client rules", frame.Turn, err)
		}
		if replay.View(frame.Turn).ID != view.ID {
			t.Fatal("planning mutated observation")
		}
		frames++
	}
	if err := scan.Err(); err != nil || frames != 4 {
		t.Fatal("incomplete native switch capture", frames, err)
	}
}
