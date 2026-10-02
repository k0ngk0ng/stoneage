package battleenv

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
)

func TestScenarioBudget(t *testing.T) {
	s := Scenario{Seed: 1, Level: 35, MaxTurns: 200, Mode: 1, Builds: []Build{{30, 30, 30, 30}, {30, 30, 30, 30}}}
	if e := s.Validate(); e != nil {
		t.Fatal(e)
	}
	s.Builds[1][0]++
	if s.Validate() == nil {
		t.Fatal("unequal budgets accepted")
	}
	s.Builds[1][0] = 0
	if s.Validate() == nil {
		t.Fatal("zero allocation accepted")
	}
}

// This suite runs against the real engine, supplied explicitly so ordinary Go
// tests never download images or start any account/game service implicitly.
func TestNativeEnvironment(t *testing.T) {
	raw := os.Getenv("STONEAGE_BATTLE_ENV_COMMAND")
	if raw == "" {
		t.Skip("set STONEAGE_BATTLE_ENV_COMMAND to a JSON argv for the native engine")
	}
	var command []string
	if e := json.Unmarshal([]byte(raw), &command); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	engine, e := Start(ctx, command, io.Discard)
	if e != nil {
		t.Fatal(e)
	}
	defer engine.Close()
	for _, mode := range []int{1, 2, 3, 4, 5} {
		for _, pets := range []bool{false, true} {
			s := Scenario{Seed: 42, Level: 35, MaxTurns: 100, Mode: mode}
			for i := 0; i < mode*2; i++ {
				s.Builds = append(s.Builds, Build{30, 30, 30, 30})
				if pets {
					s.PetBuilds = append(s.PetBuilds, Build{30, 30, 30, 30})
				}
			}
			var original []int32
			for repeat := 0; repeat < 2; repeat++ {
				state, e := engine.Reset(ctx, s)
				if e != nil {
					t.Fatal(e)
				}
				var sequence []int32
				for !state.Terminated && !state.Truncated {
					choices := make([][]aigame.BattleSelection, len(state.Views))
					for i, v := range state.Views {
						target := -1
						for _, p := range v.Battle.Participants {
							if int(p.BattleID)/10 != i/mode && p.HP > 0 && (target < 0 || int(p.BattleID) < target) {
								target = int(p.BattleID)
							}
						}
						chosen := ""
						for _, c := range v.Candidates {
							if c.Actor == "player" && (c.Kind == "wait" || c.Kind == "attack" && int(c.Target) == target) {
								chosen = c.ID
								break
							}
						}
						if chosen == "" {
							t.Fatalf("no candidate mode=%d turn=%d member=%d: %+v", mode, state.Turn, i, v.Candidates)
						}
						choices[i] = []aigame.BattleSelection{{MatchID: v.MatchID, Turn: v.Turn, ObservationID: v.ID, CandidateID: chosen}}
						petChoice := ""
						for _, c := range v.Candidates {
							if c.Actor == "pet" && c.Kind == "wait" {
								petChoice = c.ID
							}
						}
						for _, c := range v.Candidates {
							if c.Actor == "pet" && c.SkillID == 1 && int(c.Target) == target {
								petChoice = c.ID
								break
							}
						}
						if petChoice != "" {
							choices[i] = append(choices[i], aigame.BattleSelection{MatchID: v.MatchID, Turn: v.Turn, ObservationID: v.ID, CandidateID: petChoice})
						}
						if pets && state.Turn == 0 && len(choices[i]) != 2 {
							t.Fatal("pet observation/actions missing")
						}

						sequence = append(sequence, v.Own.HP)
					}
					if state.Turn == 0 {
						stale := append([][]aigame.BattleSelection(nil), choices...)
						stale[0] = append([]aigame.BattleSelection(nil), choices[0]...)
						stale[0][0].ObservationID = "stale"
						if _, e = engine.Advance(ctx, stale); e == nil {
							t.Fatal("stale observation accepted")
						}
					}
					state, e = engine.Advance(ctx, choices)
					if e != nil {
						t.Fatal(e)
					}
				}
				if !state.Terminated || state.Winner < 0 {
					t.Fatalf("expected actual native victory: %+v", state)
				}
				sequence = append(sequence, int32(state.Winner), int32(state.Turn))
				if repeat == 0 {
					original = sequence
				} else if !reflect.DeepEqual(sequence, original) {
					t.Fatal("same seed and actions produced different native trajectory")
				}
				if _, e = engine.Advance(ctx, nil); e == nil {
					t.Fatal("terminal step accepted")
				}
				t.Logf("mode=%d pets=%t repeat=%d turns=%d winner=%d", mode, pets, repeat, state.Turn, state.Winner)
			}
		}
	}
	s := Scenario{Seed: 1, Level: 35, MaxTurns: 1, Mode: 1, Builds: []Build{{30, 30, 30, 30}, {30, 30, 30, 30}}}
	state, e := engine.Reset(ctx, s)
	if e != nil {
		t.Fatal(e)
	}
	var choices [][]aigame.BattleSelection
	for _, v := range state.Views {
		for _, c := range v.Candidates {
			if c.Kind == "guard" {
				choices = append(choices, []aigame.BattleSelection{{MatchID: v.MatchID, Turn: v.Turn, ObservationID: v.ID, CandidateID: c.ID}})
				break
			}
		}
	}
	state, e = engine.Advance(ctx, choices)
	if e != nil {
		t.Fatal(e)
	}
	if !state.Truncated || state.Terminated || state.Winner != -1 {
		t.Fatal("training limit mislabeled as win/draw")
	}
}

// TestNativeWorkerHelper is only entered by the subprocess transport tests.
func TestNativeWorkerHelper(t *testing.T) {
	mode := ""
	for _, a := range os.Args {
		if strings.HasPrefix(a, "--native-helper=") {
			mode = strings.TrimPrefix(a, "--native-helper=")
		}
	}
	if mode == "" {
		return
	}
	fmt.Println(`{"schema_version":1,"ok":true,"ready":true,"scenario":"controlled-battle-v7"}`)
	reader := bufio.NewScanner(os.Stdin)
	if reader.Scan() {
		if mode == "stall" {
			time.Sleep(time.Minute)
		} else {
			fmt.Println(`{"schema_version":1,"ok":true,"mode":1,"turn":0,"winner_side":-1,"members":[]}`)
		}
	}
	os.Exit(0)
}

func TestNativeWorkerFailureAndCancellation(t *testing.T) {
	exe, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	s := Scenario{Seed: 1, Level: 35, MaxTurns: 100, Mode: 1, Builds: []Build{{30, 30, 30, 30}, {30, 30, 30, 30}}}
	for _, mode := range []string{"stall", "bad-frame"} {
		t.Run(mode, func(t *testing.T) {
			parent, stop := context.WithTimeout(context.Background(), 5*time.Second)
			defer stop()
			n, e := Start(parent, []string{exe, "-test.run=^TestNativeWorkerHelper$", "--", "--native-helper=" + mode}, io.Discard)
			if e != nil {
				t.Fatal(e)
			}
			defer n.Close()
			ctx, cancel := context.WithTimeout(parent, 100*time.Millisecond)
			defer cancel()
			if _, e = n.Reset(ctx, s); e == nil {
				t.Fatal("failed native request was accepted")
			}
			if _, e = n.Reset(parent, s); e == nil {
				t.Fatal("poisoned worker reused")
			}
			select {
			case <-n.done:
			case <-time.After(2 * time.Second):
				t.Fatal("failed worker was left alive")
			}
		})
	}
}
