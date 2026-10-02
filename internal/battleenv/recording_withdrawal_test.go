package battleenv

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
)

// The separately supplied native worker writes into an otherwise empty root.
// This catches transport placeholders accidentally recorded as real decisions
// after a knockout; contiguous event sequence numbers alone cannot detect it.
func TestNativeWithdrawnPlaceholdersPreserveObservationReferences(t *testing.T) {
	raw, records := os.Getenv("STONEAGE_BATTLE_ENV_COMMAND"), os.Getenv("STONEAGE_WITHDRAWAL_RECORDS")
	if raw == "" || records == "" {
		t.Skip("supply an isolated native worker and STONEAGE_WITHDRAWAL_RECORDS")
	}
	before, err := filepath.Glob(filepath.Join(records, "*", "*", "metadata.json"))
	if err != nil || len(before) != 0 {
		t.Fatal("record root must initially be empty", err)
	}
	var command []string
	if err := json.Unmarshal([]byte(raw), &command); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	engine, err := Start(ctx, command, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	scenario := Scenario{Seed: 42, Level: 35, MaxTurns: 100, Mode: 5}
	for i := 0; i < 10; i++ {
		build := Build{90, 10, 10, 10}
		if i%5 == 0 {
			build = Build{1, 117, 1, 1}
		}
		scenario.Builds = append(scenario.Builds, build)
		scenario.PetBuilds = append(scenario.PetBuilds, Build{1, 117, 1, 1})
	}
	state, err := engine.Reset(ctx, scenario)
	if err != nil {
		t.Fatal(err)
	}
	skipped := 0
	for !state.Terminated && !state.Truncated {
		orders := make([][]aigame.BattleSelection, len(state.Views))
		for i, v := range state.Views {
			if v.Withdrawn {
				if len(v.Candidates) != 0 {
					t.Fatal("withdrawn actor has candidates")
				}
				skipped++
				continue
			}
			target := int32(-1)
			for _, p := range v.Battle.Participants {
				if int(p.BattleID)/10 != i/scenario.Mode && p.HP > 0 && (target < 0 || p.BattleID < target) {
					target = p.BattleID
				}
			}
			for _, actor := range []string{"player", "pet"} {
				chosen := ""
				available := false
				for _, c := range v.Candidates {
					if c.Actor != actor {
						continue
					}
					available = true
					if c.Kind == "wait" && chosen == "" {
						chosen = c.ID
					}
					if c.Target == target && (actor == "player" && c.Kind == "attack" || actor == "pet" && c.SkillID == 1) {
						chosen = c.ID
						break
					}
				}
				if !available {
					continue
				}
				if chosen == "" {
					t.Fatalf("no %s selection turn%d member%d", actor, state.Turn, i)
				}
				orders[i] = append(orders[i], aigame.BattleSelection{MatchID: v.MatchID, Turn: v.Turn, ObservationID: v.ID, CandidateID: chosen})
			}
		}
		state, err = engine.Advance(ctx, orders)
		if err != nil {
			t.Fatal(err)
		}
	}
	if skipped == 0 {
		t.Fatal("fixture did not exercise withdrawn placeholders")
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	paths, err := filepath.Glob(filepath.Join(records, "*", "*", "events.jsonl"))
	if err != nil || len(paths) != 1 {
		t.Fatal("expected one closed native recording", paths, err)
	}
	f, err := os.Open(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	type event struct {
		Type        string `json:"type"`
		Sequence    int64  `json:"seq"`
		Turn        int    `json:"turn"`
		Side        int    `json:"side"`
		Observation int64  `json:"observation_id"`
	}
	type observation struct {
		turn, side int
		ended      bool
	}
	observed := map[int64]*observation{}
	sequence := int64(1)
	requests := 0
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		var e event
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			t.Fatal(err)
		}
		if e.Sequence != sequence+1 {
			t.Fatal("event sequence gap")
		}
		sequence = e.Sequence
		if e.Type == "observation" {
			observed[e.Sequence] = &observation{e.Turn, e.Side, false}
			continue
		}
		if e.Type == "observation_end" || e.Type == "action_request" || strings.Contains("|visible_actor|own_actor|allocation|pet_skill|item|", "|"+e.Type+"|") {
			o := observed[e.Observation]
			if o == nil || o.turn != e.Turn || o.side != e.Side || o.ended != (e.Type == "action_request") {
				t.Fatalf("broken observation reference at sequence%d type%s turn%d observation%d", e.Sequence, e.Type, e.Turn, e.Observation)
			}
			if e.Type == "observation_end" {
				o.ended = true
			}
			if e.Type == "action_request" {
				requests++
			}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if requests == 0 {
		t.Fatal("active commands were not recorded")
	}
	var end struct {
		Sequence   int64 `json:"seq"`
		Complete   bool  `json:"storage_complete"`
		Trajectory bool  `json:"trajectory_complete"`
		Dropped    int   `json:"dropped_events"`
		Winner     int   `json:"winner_side"`
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(paths[0]), "result.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &end); err != nil {
		t.Fatal(err)
	}
	if end.Sequence != sequence+1 || !end.Complete || !end.Trajectory || end.Dropped != 0 || end.Winner != state.Winner {
		t.Fatal("native recording outcome/completeness mismatch")
	}
	t.Logf("%d withdrawn member-turn placeholders, %d real action requests, all references current", skipped, requests)
}
