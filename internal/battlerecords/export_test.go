package battlerecords

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Synthetic recording-format fixture; tests do not simulate battle effects.
func fixture() []object {
	meta := object{"type": "match_start", "turn": 0, "side": -1, "mode": "pvp", "players_per_side": []int{1, 1}, "release": "fixture", "ruleset_id": strings.Repeat("a", 64), "players": []object{{"bid": 0, "side": 0, "policy_version": nil}, {"bid": 10, "side": 1, "policy_version": nil}}, "experiment": object{"generator": "native-equal-points-v1", "run_seed": 42, "combat_seed": 42, "match_index": 0, "pair_index": 0, "repetition": 0, "side_swap": false, "points": 5, "level": 1, "scenario": "bare-character-no-pets-v1", "allocation_generator": "mixture-v1", "policy_ids": []int{0, 0}, "max_turns": 200}}
	rows := []object{meta}
	refs := [2]int{}
	for turn := 0; turn <= 1; turn++ {
		for side := 0; side < 2; side++ {
			ref := len(rows) + 1
			refs[side] = ref
			rows = append(rows, object{"type": "observation", "turn": turn, "side": side, "my_bid": side * 10, "menu_flags": 8, "mp": 10, "field_attribute": 0, "legal_action_mask": nil, "server_private": "NEVER_EXPORT"})
			for bid := 0; bid <= 10; bid += 10 {
				hp := 100
				if turn == 1 && bid == 10 {
					hp = 0
				}
				rows = append(rows, object{"type": "visible_actor", "observation_id": ref, "turn": turn, "side": side, "bid": bid, "graphic": 100000, "level": 1, "hp": hp, "max_hp": 100, "flags": 5, "ride_flag": 0, "ride_pet_level": 0, "ride_pet_hp": 0, "ride_pet_max_hp": 0})
			}
			rows = append(rows,
				object{"type": "own_actor", "observation_id": ref, "turn": turn, "side": side, "pet_slot": -1, "level": 1, "graphic": 100000, "hp": 100, "max_hp": 100, "mp": 10, "max_mp": 10, "attack": 5, "defense": 5, "speed": 5, "elements": []int{100, 0, -100, 0}, "dead": false, "active_pet_slot": -1, "ride_pet_slot": -1},
				object{"type": "allocation", "observation_id": ref, "turn": turn, "side": side, "pet_slot": -1, "raw_units_per_point": 100, "vitality_raw": 200 - side*100, "strength_raw": 100 + side*100, "toughness_raw": 100, "dexterity_raw": 100, "unspent_points": 0, "allocated_raw": 500, "budget_raw": 500, "level": 1, "rebirths": 0, "charm": 100, "luck": 0, "base_elements": []int{100, 0, 0, 0}},
				object{"type": "observation_end", "observation_id": ref, "turn": turn, "side": side},
			)
		}
		if turn == 0 {
			for side := 0; side < 2; side++ {
				rows = append(rows, object{"type": "action_request", "turn": 0, "side": side, "observation_id": refs[side], "origin": "client", "opcode": "H", "arg1": (1 - side) * 10, "arg2": -1, "parsed": true, "mp_before": 10})
			}
			for side := 0; side < 2; side++ {
				rows = append(rows, object{"type": "execution_command", "turn": 1, "side": side, "decision_turn": 0, "bid": side * 10, "visibility": "server_only", "command": []int{1, (1 - side) * 10, -1}, "hp": 100, "mp": 10, "dead": false}, object{"type": "action_resolution", "turn": 1, "side": side, "decision_turn": 0, "bid": side * 10, "visibility": "server_only", "command": 1})
			}
		}
	}
	rows = append(rows, object{"type": "match_end", "turn": 1, "side": -1, "winner_side": 0, "end_reason": "defeat", "trajectory_complete": true, "storage_complete": true, "dropped_events": 0})
	for i, row := range rows {
		row["schema_version"], row["seq"], row["match_id"] = 1, i+1, "synthetic-1"
	}
	return rows
}

func saveFixture(t *testing.T, root string, rows []object, compressed bool) {
	t.Helper()
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	for file, row := range map[string]object{"metadata.json": rows[0], "result.json": rows[len(rows)-1]} {
		b, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, file), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	var raw bytes.Buffer
	for _, row := range rows[1 : len(rows)-1] {
		if err := json.NewEncoder(&raw).Encode(row); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(root, "events.jsonl")
	data := raw.Bytes()
	if compressed {
		var zipped bytes.Buffer
		z := gzip.NewWriter(&zipped)
		if _, err := z.Write(data); err != nil {
			t.Fatal(err)
		}
		if err := z.Close(); err != nil {
			t.Fatal(err)
		}
		data, path = zipped.Bytes(), path+".gz"
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func exported(t *testing.T, path string) []object {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := bufio.NewScanner(bytes.NewReader(b))
	s.Buffer(make([]byte, 4096), 1<<20)
	var rows []object
	for s.Scan() {
		row, err := decode(s.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		rows = append(rows, row)
	}
	if err := s.Err(); err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestExportOutcomesAndVisibility(t *testing.T) {
	for _, compressed := range []bool{false, true} {
		for _, truncated := range []bool{false, true} {
			root := t.TempDir()
			records := fixture()
			if truncated {
				records[len(records)-1]["winner_side"], records[len(records)-1]["end_reason"] = -1, "turn_limit"
			}
			saveFixture(t, root, records, compressed)
			output := filepath.Join(root, "transitions.jsonl")
			r, err := Export(context.Background(), Config{Root: root, Output: output, Format: "transitions", Mode: "pvp-1v1", EqualPoints: true})
			if err != nil {
				t.Fatal(err)
			}
			if r.ExportedMatches != 1 || r.Rows != 2 || r.InvalidMatches != 0 || r.OnPolicyPPO {
				t.Fatalf("%+v", r)
			}
			data, _ := os.ReadFile(output)
			h := sha256.Sum256(data)
			if r.SHA256 != hex.EncodeToString(h[:]) || r.Bytes != int64(len(data)) || bytes.Contains(data, []byte("NEVER_EXPORT")) {
				t.Fatal("digest or visibility")
			}
			for side, row := range exported(t, output) {
				want := int64(1 - 2*side)
				if truncated {
					want = 0
				}
				if integer(row["reward"]) != want || row["terminated"] != !truncated || row["truncated"] != truncated {
					t.Fatalf("bad terminal label: %v", row)
				}
				obs := row["observation"].(map[string]any)
				if _, ok := obs["resolution_commands"]; ok {
					t.Fatal("future action leaked")
				}
				if len(obs["allocations"].([]any)) != 1 || len(row["submitted_actions"].([]any)) != 1 || len(row["resolution_commands"].([]any)) != 1 || len(row["resolution_order"].([]any)) != 2 {
					t.Fatal("observations and labels lost")
				}
			}
			if _, err := Export(context.Background(), Config{Root: root, Output: output, Format: "builds", Mode: "all"}); err == nil {
				t.Fatal("overwrote existing data")
			}
			after, _ := os.ReadFile(output)
			if !bytes.Equal(data, after) {
				t.Fatal("existing data changed")
			}
		}
	}
}

func TestExportRejectsCorruptionBeforePublication(t *testing.T) {
	cases := map[string]func([]object){
		"invalid_rules":        func(r []object) { r[0]["ruleset_id"] = "native-validation" },
		"sequence_or_identity": func(r []object) { r[2]["seq"] = 999 },
		"incomplete_recording": func(r []object) { r[len(r)-1]["dropped_events"] = 1 },
		"invalid_outcome":      func(r []object) { r[len(r)-1]["winner_side"] = -1 },
		"point_budget_mismatch": func(r []object) {
			for _, e := range r {
				if e["type"] == "allocation" {
					e["budget_raw"] = 99
					break
				}
			}
		},
		"invalid_observation_field": func(r []object) {
			for _, e := range r {
				if e["type"] == "own_actor" {
					e["hp"] = []int{100}
					break
				}
			}
		},
		"unsupported_action_mask": func(r []object) { r[1]["legal_action_mask"] = []bool{true} },
		"broken_observation_reference": func(r []object) {
			for _, e := range r {
				if e["type"] == "action_request" {
					e["observation_id"] = 99999
					break
				}
			}
		},
		"missing_field": func(r []object) {
			for _, e := range r {
				if e["type"] == "allocation" {
					delete(e, "unspent_points")
					break
				}
			}
		},
	}
	for reason, mutate := range cases {
		t.Run(reason, func(t *testing.T) {
			root := t.TempDir()
			rows := fixture()
			mutate(rows)
			saveFixture(t, root, rows, false)
			output := filepath.Join(root, "out.jsonl")
			r, err := Export(context.Background(), Config{Root: root, Output: output, Format: "transitions", Mode: "all"})
			if err == nil || r.InvalidMatches != 1 || r.InvalidReasons[reason] != 1 {
				t.Fatalf("report=%+v err=%v", r, err)
			}
			if _, err := os.Stat(output); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("partial output published")
			}
			left, _ := filepath.Glob(filepath.Join(root, ".records-export-*"))
			if len(left) != 0 {
				t.Fatal("temporary output leaked")
			}
		})
	}
}

func TestBuildFairnessAndGroupedSplits(t *testing.T) {
	root := t.TempDir()
	rows := fixture()
	saveFixture(t, filepath.Join(root, "2026-09-29", "first"), rows, false)
	for _, r := range rows {
		r["match_id"] = "synthetic-2"
	}
	exp := rows[0]["experiment"].(object)
	exp["side_swap"], exp["match_index"] = true, 1
	saveFixture(t, filepath.Join(root, "2026-09-29", "second"), rows, true)
	output := filepath.Join(root, "builds.jsonl")
	r, err := Export(context.Background(), Config{Root: root, Output: output, Format: "builds", Mode: "pvp-1v1"})
	if err != nil || r.Rows != 2 {
		t.Fatalf("%+v %v", r, err)
	}
	a := exported(t, output)
	if a[0]["split_group"] != a[1]["split_group"] || a[0]["suggested_split"] != a[1]["suggested_split"] {
		t.Fatal("repeats leaked across splits")
	}
	fair := a[0]["fairness"].(map[string]any)
	if fair["equal_points"] != true || fair["controlled_context"] != true || fair["experimental_control_verified"] != false {
		t.Fatal(fair)
	}
	for _, change := range []string{"level", "hp", "budget"} {
		t.Run(change, func(t *testing.T) {
			r := fixture()
			for _, e := range r {
				if e["side"] != 1 {
					continue
				}
				if e["type"] == "allocation" && change == "level" {
					e["level"] = 2
				}
				if e["type"] == "own_actor" && change == "hp" {
					e["hp"] = 50
				}
				if e["type"] == "allocation" && change == "budget" {
					e["vitality_raw"], e["allocated_raw"], e["budget_raw"] = 200, 600, 600
				}
			}
			path := t.TempDir()
			saveFixture(t, path, r, false)
			report, err := Export(context.Background(), Config{Root: path, Output: filepath.Join(path, "out.jsonl"), Format: "builds", Mode: "all"})
			if err == nil || report.FilteredMatches != 1 || report.InvalidMatches != 0 {
				t.Fatalf("%+v %v", report, err)
			}
		})
	}
}

func TestMixedRecordsDuplicateAndCancellation(t *testing.T) {
	root := t.TempDir()
	a, b := fixture(), fixture()
	for _, r := range b {
		r["match_id"] = "synthetic-2"
	}
	b[len(b)-1]["storage_complete"] = false
	saveFixture(t, filepath.Join(root, "2026-09-29", "a"), a, false)
	saveFixture(t, filepath.Join(root, "2026-09-29", "b"), b, false)
	c := Config{Root: root, Output: filepath.Join(root, "out.jsonl"), Format: "transitions", Mode: "all"}
	r, err := Export(context.Background(), c)
	if err != nil || r.ExportedMatches != 1 || r.InvalidMatches != 1 || r.Rows != 2 {
		t.Fatalf("%+v %v", r, err)
	}
	saveFixture(t, filepath.Join(root, "2026-09-29", "c"), a, false)
	c.Output = filepath.Join(root, "duplicate.jsonl")
	if _, err := Export(context.Background(), c); err == nil || !strings.Contains(err.Error(), "duplicate match") {
		t.Fatal(err)
	}
	if _, err := os.Stat(c.Output); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("partial duplicate dataset published")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Export(ctx, c); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestGzipChecksumFailure(t *testing.T) {
	root := t.TempDir()
	saveFixture(t, root, fixture(), true)
	path := filepath.Join(root, "events.jsonl.gz")
	b, _ := os.ReadFile(path)
	b[len(b)-5] ^= 1
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	r, err := Export(context.Background(), Config{Root: root, Output: filepath.Join(root, "out.jsonl"), Format: "transitions", Mode: "all"})
	if err == nil || r.InvalidReasons["invalid_event_stream"] != 1 {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestModesAndAbnormalResults(t *testing.T) {
	root := t.TempDir()
	r := fixture()
	r[len(r)-1]["end_reason"], r[len(r)-1]["winner_side"] = "escape", -1
	saveFixture(t, root, r, false)
	c := Config{Root: root, Output: filepath.Join(root, "out.jsonl"), Format: "transitions", Mode: "all"}
	report, err := Export(context.Background(), c)
	if err == nil || report.FilteredMatches != 1 {
		t.Fatalf("%+v %v", report, err)
	}
	c.IncludeAbnormal = true
	report, err = Export(context.Background(), c)
	if err != nil || report.Rows != 2 {
		t.Fatalf("%+v %v", report, err)
	}
	for _, row := range exported(t, c.Output) {
		if row["end_reason"] != "escape" || integer(row["reward"]) != 0 || row["terminated"] != false || row["truncated"] != true {
			t.Fatal("abnormal end relabeled", row)
		}
	}
	c.Mode, c.Output = "pve", filepath.Join(root, "pve.jsonl")
	report, err = Export(context.Background(), c)
	if err == nil || report.FilteredMatches != 1 {
		t.Fatalf("mode not filtered: %+v %v", report, err)
	}
}

func TestNativeRecordedDataset(t *testing.T) {
	root := os.Getenv("STONEAGE_LEGACY_RECORD_FIXTURE")
	if root == "" {
		t.Skip("set STONEAGE_LEGACY_RECORD_FIXTURE to stopped native dataset")
	}
	var reports []Report
	for _, format := range []string{"builds", "transitions"} {
		output := filepath.Join(t.TempDir(), format+".jsonl")
		r, err := Export(context.Background(), Config{Root: root, Output: output, Format: format, Mode: "pvp-1v1", EqualPoints: true})
		if err != nil || r.InvalidMatches != 0 || r.FilteredMatches != 0 {
			t.Fatalf("%+v %v", r, err)
		}
		reports = append(reports, r)
		if format == "transitions" {
			byMatch := map[string][]int64{}
			for _, row := range exported(t, output) {
				if row["terminated"] == true {
					id := text(row["match_id"])
					byMatch[id] = append(byMatch[id], integer(row["reward"]))
				}
			}
			for _, rewards := range byMatch {
				if !reflect.DeepEqual(rewards, []int64{1, -1}) && !reflect.DeepEqual(rewards, []int64{-1, 1}) {
					t.Fatal(rewards)
				}
			}
		}
	}
	if reports[0].Rows != reports[1].ExportedMatches || reports[0].Rows < 2 {
		t.Fatal(reports)
	}
	t.Logf("native exports: %+v", reports)
}
