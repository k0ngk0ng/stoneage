package battlepolicy

import (
	"bufio"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/battleenv"
)

// The native harness runs the same builds/orders through the extracted
// environment and actual arena entry/command/settlement hooks. Read its public
// packet captures here; never construct model observations from engine memory.
// No Docker or service is started by ordinary Go tests.
func TestNativeArenaProjectionParity(t *testing.T) {
	root := os.Getenv("STONEAGE_BATTLE_PARITY_DIR")
	if root == "" {
		t.Skip("set STONEAGE_BATTLE_PARITY_DIR to a completed native parity run")
	}
	raw, err := os.ReadFile(filepath.Join(root, "passed.json"))
	if err != nil {
		t.Fatal(err)
	}
	var report struct {
		Schema    int    `json:"schema_version"`
		Scenario  string `json:"scenario_version"`
		Scenarios []struct{ Mode, Pets, Seed, Turns, Healing, Items, Reserves int }
	}
	if err = json.Unmarshal(raw, &report); err != nil || !battleenv.SupportedScenario(report.Scenario) || report.Schema != 1 || len(report.Scenarios) != 120 {
		t.Fatalf("complete native parity report required: %v", err)
	}
	for _, scenario := range report.Scenarios {
		name := fmt.Sprintf("%dv%d-pets%d-seed%d-heal%d-items%d", scenario.Mode, scenario.Mode, scenario.Pets, scenario.Seed, scenario.Healing, scenario.Items)
		if scenario.Reserves > 0 {
			name += fmt.Sprintf("-reserves%d", scenario.Reserves)
		}
		t.Run(name, func(t *testing.T) {
			left := parityProjection(t, filepath.Join(root, name+"-offline", "stdout.log"), scenario.Mode, scenario.Turns)
			right := parityProjection(t, filepath.Join(root, name+"-arena", "stdout.log"), scenario.Mode, scenario.Turns)
			if !reflect.DeepEqual(left, right) {
				for i := range left {
					if !reflect.DeepEqual(left[i], right[i]) {
						l, _ := json.MarshalIndent(left[i], "", "  ")
						r, _ := json.MarshalIndent(right[i], "", "  ")
						t.Fatalf("turn %d projected state/candidates/features differ:\noffline=%s\narena=%s", i, l, r)
					}
				}
			}
		})
	}
}

type parityDecision struct {
	Views    []aigame.BattleView
	Features []Frame
	Ended    bool
	Winner   int
}

func TestNativeGuardianArenaProjectionParity(t *testing.T) {
	root := os.Getenv("STONEAGE_GUARDIAN_PARITY_DIR")
	if root == "" {
		t.Skip("explicit native guardian parity capture required")
	}
	raw, err := os.ReadFile(filepath.Join(root, "guardian-parity-passed.json"))
	if err != nil {
		t.Fatal(err)
	}
	var report struct {
		Schema   int    `json:"schema_version"`
		Scenario string `json:"scenario_version"`
		Cases    []struct {
			Name        string
			Mode, Turns int
			Hits        int `json:"guardian_hits"`
		}
	}
	if err = json.Unmarshal(raw, &report); err != nil || report.Schema != 1 || report.Scenario != "controlled-battle-v8" || len(report.Cases) != 6 {
		t.Fatal("invalid guardian parity report", err)
	}
	for _, tc := range report.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			left := parityProjection(t, filepath.Join(root, tc.Name+"-offline", "stdout.log"), tc.Mode, tc.Turns)
			right := parityProjection(t, filepath.Join(root, tc.Name+"-arena", "stdout.log"), tc.Mode, tc.Turns)
			if tc.Hits <= 0 || !reflect.DeepEqual(left, right) {
				t.Fatal("guardian state/candidates/features differ between native paths")
			}
			candidates := 0
			for _, decision := range left {
				for _, frame := range decision.Features {
					for _, slot := range frame.Slots {
						for _, candidate := range slot.Candidates {
							if candidate.Supported && candidate.Features[32] == 1 {
								candidates++
							}
						}
					}
				}
			}
			if candidates == 0 {
				t.Fatal("native guardian skill omitted from actual model candidates")
			}
		})
	}
}

func parityProjection(t *testing.T, path string, mode, turns int) []parityDecision {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	replays := make([]*aigame.BattleReplay, mode*2)
	streams := make([]string, mode*2)
	cursors := make([]uint64, mode*2)
	for i := range replays {
		replays[i], err = aigame.NewBattleReplay("parity", mode)
		if err != nil {
			t.Fatal(err)
		}
	}
	var result []parityDecision
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 65536), 8<<20)
	for scanner.Scan() {
		if !strings.HasPrefix(scanner.Text(), "FRAME|") {
			continue
		}
		var frame struct {
			Mode       int
			Turn       int
			Terminated bool
			Truncated  bool
			Winner     int `json:"winner_side"`
			Members    []battleenv.Member
		}
		if err = json.Unmarshal(scanner.Bytes()[6:], &frame); err != nil {
			t.Fatal(err)
		}
		if frame.Mode != mode || frame.Turn != len(result) || frame.Truncated || len(frame.Members) != mode*2 {
			t.Fatalf("invalid captured frame %s: turn %d", path, frame.Turn)
		}
		d := parityDecision{Ended: frame.Terminated, Winner: frame.Winner}
		histories := make([]History, mode*2)
		for i, member := range frame.Members {
			if member.Side != i/mode || member.Seat != i%mode {
				t.Fatal("misordered capture")
			}
			for _, packet := range member.Packets {
				payload, err := hex.DecodeString(packet.Hex)
				if err != nil {
					t.Fatal(err)
				}
				if err = replays[i].Apply(packet.Function, payload); err != nil {
					t.Fatal(err)
				}
			}
			v := replays[i].View(int32(frame.Turn))
			b := replays[i].Events(int32(frame.Turn), streams[i], cursors[i])
			histories[i] = History{Batch: &b, PreviousStream: streams[i], PreviousCursor: cursors[i], First: frame.Turn == 0}
			streams[i], cursors[i] = b.Stream, b.Cursor
			d.Views = append(d.Views, v)
		}
		if !frame.Terminated {
			for side := 0; side < 2; side++ {
				f, err := Encode(d.Views[side*mode:(side+1)*mode], histories[side*mode])
				if err != nil {
					t.Fatalf("%s turn %d side %d: policy rejection: %v", path, frame.Turn, side, err)
				}
				if err = f.Validate(); err != nil || f.Events[12] != 0 {
					t.Fatalf("invalid frame or missing history: %v", err)
				}
				// Observation hashes include persistent character identity. That
				// identity is intentionally absent from numerical model features.
				for i := range f.Slots {
					f.Slots[i].Observation = "observation"
				}
				d.Features = append(d.Features, f)
			}
		}
		for i := range d.Views {
			d.Views[i].ID = "observation"
			d.Views[i].CharacterID = "character"
			for j := range d.Views[i].Pets {
				d.Views[i].Pets[j].Identity = "pet"
			}
		}
		result = append(result, d)
	}
	if err = scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if len(result) != turns+1 || !result[len(result)-1].Ended {
		t.Fatalf("incomplete captured battle: %s", path)
	}
	return result
}
