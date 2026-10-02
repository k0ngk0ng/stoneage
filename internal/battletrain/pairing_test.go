package battletrain

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/battleenv"
)

func rosterAt(s battleenv.Scenario, side int) string {
	var pets []battleenv.Build
	var reserves [][]battleenv.ReservePet
	if len(s.PetBuilds) > 0 {
		pets = s.PetBuilds[side*s.Mode : (side+1)*s.Mode]
	}
	if len(s.Reserves) > 0 {
		reserves = s.Reserves[side*s.Mode : (side+1)*s.Mode]
	}
	id, _ := Digest(Roster{Players: s.Builds[side*s.Mode : (side+1)*s.Mode], Pets: pets, Reserves: reserves})
	return id
}

func TestPairingCrossesRosterSideAndSeedInAllModes(t *testing.T) {
	for mode := 1; mode <= 5; mode++ {
		for _, pairing := range []string{"", BalancedPairing} {
			c := DefaultRunConfig()
			c.Pairing, c.Mode, c.ReservePets = pairing, mode, 2
			first, group := scenarioFor(c, 0)
			identities := [2]string{rosterAt(first, 0), rosterAt(first, 1)}
			seen := map[string]bool{}
			rosters, seeds := map[string]bool{}, map[int]bool{}
			for i := 0; i < familyGames(pairing); i++ {
				s, g := scenarioFor(c, uint64(i))
				side := i % 2
				own := rosterAt(s, side)
				key := fmt.Sprintf("%s:%d:%d", own, side, s.Seed)
				if g != group || seen[key] {
					t.Fatal("duplicate or cross-family assignment", mode, pairing, i)
				}
				seen[key], rosters[own], seeds[s.Seed] = true, true, true
				other := rosterAt(s, 1-side)
				if own == other || !(own == identities[0] && other == identities[1] || own == identities[1] && other == identities[0]) {
					t.Fatal("partial roster swap")
				}
				// PPO's learner is opposite opponentAt.Side. It must receive
				// the complementary complete roster, not always the same one.
				choice := (Checkpoint{Config: c}).opponentAt(uint64(i))
				if choice.Side != side || rosterAt(s, 1-choice.Side) != other {
					t.Fatal("learner assignment mismatch")
				}
			}
			want := 1
			if pairing == BalancedPairing {
				want = 2
			}
			if len(rosters) != want || len(seeds) != 2 {
				t.Fatal("missing independent coverage", mode, pairing, rosters, seeds)
			}
		}
	}
}

func TestPairingExperimentSchemasAndTeacherAssignments(t *testing.T) {
	meta := experimentFixture(t).Environment
	for _, pairing := range []string{"", BalancedPairing} {
		c := DefaultEvaluationConfig()
		c.Pairing = pairing
		c.ReservePets = 2
		c.Mode = 3
		x, e := NewExperiment(context.Background(), meta, c, [3]int{2, 1, 1})
		if e != nil {
			t.Fatal(e)
		}
		run := DefaultRunConfig()
		run.Pairing = pairing
		run.Warmup.Teachers = []string{"focus", "sustain"}
		family := x.groups("train")[0]
		teacherRosters := map[string]map[string]bool{}
		for i := 0; i < familyGames(pairing); i++ {
			s, g, policies := warmupScenario(run, uint64(i), &x)
			if g != family.Group {
				t.Fatal("teacher schedule moved family early")
			}
			for side, p := range policies {
				if teacherRosters[p.Rule] == nil {
					teacherRosters[p.Rule] = map[string]bool{}
				}
				teacherRosters[p.Rule][rosterAt(s, side)] = true
			}
		}
		for _, rosters := range teacherRosters {
			want := 1
			if pairing == BalancedPairing {
				want = 2
			}
			if len(rosters) != want {
				t.Fatal("teacher didn't cross roster ownership", teacherRosters)
			}
		}
		if championConditions(x).Pairing != pairing {
			t.Fatal("registry drops pairing")
		}
		bad := x
		if pairing == "" {
			bad.Pairing = BalancedPairing
		} else {
			bad.Pairing = ""
		}
		if bad.Validate() == nil {
			t.Fatal("schema allows silently changing pairing")
		}
		bad = x
		bad.Pairing = "unknown"
		if bad.Validate() == nil {
			t.Fatal("unknown pairing accepted")
		}
	}
}

func TestBalancedUnboundReportRejectsUncrossedOrRelabeledGames(t *testing.T) {
	c := DefaultRunConfig()
	ec := DefaultEvaluationConfig()
	ec.MatchesPerOpponent = 8
	r := EvaluationReport{Schema: evaluationSchema(ec.Pairing), Candidate: strings.Repeat("0", 64), Config: ec, Environment: experimentFixture(t).Environment}
	for i := 0; i < 8; i++ {
		s, g := scenarioFor(c, uint64(i))
		r.Games = append(r.Games, EvaluationGame{Opponent: "basic", OpponentPolicy: strings.Repeat("1", 64), Group: g, Scenario: s, CandidateSide: i % 2, Winner: 0, Terminated: true, Turns: 1})
	}
	r.Comparisons = Summarize(r.Games, ec.Seed)
	if e := ValidateEvaluation(r); e != nil {
		t.Fatal(e)
	}
	bad := r
	bad.Games = append([]EvaluationGame(nil), r.Games...)
	// Preserve budget, group, count and summary, but undo ownership crossing.
	for _, i := range []int{2, 3, 6, 7} {
		s := bad.Games[i].Scenario
		swapScenario(&s)
		bad.Games[i].Scenario = s
	}
	if ValidateEvaluation(bad) == nil {
		t.Fatal("eight fixed-roster games passed as balanced")
	}
	bad = r
	bad.Config.Pairing = ""
	if ValidateEvaluation(bad) == nil {
		t.Fatal("balanced report silently downgraded")
	}
	bad = r
	bad.Schema = "commander-evaluation-v2"
	if ValidateEvaluation(bad) == nil {
		t.Fatal("old reader schema allowed balanced data")
	}
}

func TestNativeBalancedSameRuleHasEqualRosterScore(t *testing.T) {
	raw := os.Getenv("STONEAGE_BATTLE_ENV_COMMAND")
	if raw == "" {
		t.Skip("explicit native engine required")
	}
	var command []string
	if e := json.Unmarshal([]byte(raw), &command); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	engine, e := battleenv.Start(ctx, command, io.Discard)
	if e != nil {
		t.Fatal(e)
	}
	defer engine.Close()
	for _, mode := range []int{1, 2, 3, 4, 5} {
		c := DefaultRunConfig()
		c.Mode = mode
		c.ReservePets = 2
		c.HealingMagic = 20
		c.HealingItems = 2
		wins, losses := 0, 0
		var results []int
		for i := 0; i < 8; i++ {
			s, g := scenarioFor(c, uint64(i))
			p := Policy{Rule: "sustain"}
			tr, e := CollectPolicies(ctx, engine, s, [2]Policy{p, p}, [2]*rand.Rand{}, g, engine.Metadata().Rules)
			if e != nil {
				t.Fatal(e)
			}
			if tr[0].Truncated {
				t.Fatal("fixture must finish normally", mode, i)
			}
			results = append(results, tr[0].Winner)
			if tr[0].Winner == i%2 {
				wins++
			} else if tr[0].Winner >= 0 {
				losses++
			}
		}
		if wins != losses || !reflect.DeepEqual([]int{results[0], results[1], results[4], results[5]}, []int{results[3], results[2], results[7], results[6]}) {
			t.Fatal("same-rule roster/side control is not balanced", mode, wins, losses, results)
		}
		t.Logf("mode=%d same-rule eight-game group: wins=%d losses=%d", mode, wins, losses)
	}
}
