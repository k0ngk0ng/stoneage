package battletrain

import (
	"context"
	"encoding/json"
	"io"
	"math/rand"
	"os"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/battleenv"
	"github.com/k0ngk0ng/stoneage/internal/battlenet"
)

func TestReserveFamiliesSearchAndFrozenPool(t *testing.T) {
	for _, count := range []int{1, 2} {
		c := DefaultRunConfig()
		c.ReservePets = count
		s, group := scenarioFor(c, 0)
		original, _ := Digest(s)
		if err := s.Validate(); err != nil {
			t.Fatal(err)
		}
		if ScenarioGroup(s) != group {
			t.Fatal("unstable family")
		}
		if now, _ := Digest(s); now != original {
			t.Fatal("grouping mutated source")
		}
		swapScenario(&s)
		s.Seed++
		if ScenarioGroup(s) != group {
			t.Fatal("swap/reseed changed family")
		}
		s.Reserves[0][0].SkillMask = 127
		if ScenarioGroup(s) == group {
			t.Fatal("reserve skills omitted from family")
		}
		ec := DefaultEvaluationConfig()
		ec.ReservePets = count
		x, err := NewExperiment(context.Background(), experimentFixture(t).Environment, ec, [3]int{2, 1, 1})
		if err != nil || !x.matches(c) {
			t.Fatal("experiment settings lost", err)
		}
		for _, split := range []string{"validation", "test"} {
			suite, err := x.evaluationSuite(split)
			if err != nil {
				t.Fatal(err)
			}
			for _, s := range suite {
				if reserveCount(s.Reserves) != count {
					t.Fatal("evaluation lost reserves")
				}
			}
		}
		bad := x
		bad.ReservePets = 0
		if bad.Validate() == nil {
			t.Fatal("experiment accepted changed reserve count")
		}
		bc := DefaultBuildSearchConfig()
		bc.ReservePets = count
		for _, mode := range []int{1, 5} {
			bc.Mode = mode
			rosters, err := initialRosters(bc)
			if err != nil {
				t.Fatal(err)
			}
			scorer, err := battlenet.NewBuildScorer[float32](len(rosters[0].features(bc)), 1)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = scorer.Predict(rosters[0].features(bc)); err != nil {
				t.Fatal(err)
			}
			parent := rosters[0]
			before := parent.ID()
			rng := rand.New(rand.NewSource(123))
			for i := 0; i < 200; i++ {
				child := mutateRoster(parent, bc, rng)
				if err := child.validate(bc); err != nil {
					t.Fatal(err)
				}
				if parent.ID() != before {
					t.Fatal("mutation changed parent")
				}
				if len(child.features(bc)) != mode*(8+11*count) {
					t.Fatal("missing reserve scorer features")
				}
				s := buildScenario(bc, child, parent, 0, 0, 0)
				if err := s.Validate(); err != nil {
					t.Fatal(err)
				}
				child.Reserves[0][0].SkillMask = 127
				if reflect.DeepEqual(child.Reserves[0], s.Reserves[0]) {
					t.Fatal("scenario aliases roster")
				}
			}
		}
		pool := buildPoolFixture(t)
		pool.Config = DefaultBuildSearchConfig()
		pool.Config.ReservePets = count
		rosters, err := initialRosters(pool.Config)
		if err != nil {
			t.Fatal(err)
		}
		pool.Rosters = rosters[:2]
		sort.Slice(pool.Rosters, func(i, j int) bool { return pool.Rosters[i].ID() < pool.Rosters[j].ID() })
		x, err = NewExperimentFromSources(context.Background(), pool.Environment, ec, [3]int{3, 1, 1}, &pool, nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range x.Families {
			if reserveCount(f.Scenario.Reserves) != count {
				t.Fatal("pool lost reserves")
			}
		}
		ec.ReservePets = 0
		if _, err := NewExperimentFromSources(context.Background(), pool.Environment, ec, [3]int{3, 1, 1}, &pool, nil); err == nil {
			t.Fatal("pool accepted changed reserve count")
		}
	}
}

func TestNativeReserveSamplingAndPPO(t *testing.T) {
	raw := os.Getenv("STONEAGE_BATTLE_ENV_COMMAND")
	if raw == "" {
		t.Skip("explicit native engine required")
	}
	var command []string
	if err := json.Unmarshal([]byte(raw), &command); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	engine, err := battleenv.Start(ctx, command, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	m := testModel(t)
	var batch []Trajectory
	switches, activeReserves := 0, 0
	for game := 0; game < 2; game++ {
		c := DefaultRunConfig()
		c.ReservePets = 2
		c.MaxTurns = 60
		c.HealingItems = 2
		c.HealingMagic = 20
		s, group := scenarioFor(c, uint64(game*4))
		tr, err := Collect(ctx, engine, s, [2]*battlenet.Model[float32]{m, m}, [2]*rand.Rand{rand.New(rand.NewSource(int64(game + 1))), rand.New(rand.NewSource(int64(game + 9)))}, group, engine.Metadata().Rules)
		if err != nil {
			t.Fatal(err)
		}
		for _, trajectory := range tr {
			for _, step := range trajectory.Steps {
				for _, v := range step.Observations {
					if v.Own.BattlePetSlotKnown && v.Own.BattlePetSlot > 0 {
						activeReserves++
					}
				}
				for i, slot := range step.Frame.Slots {
					if step.Frame.Entities[slot.Entity][52] != 0 {
						t.Fatal("reserve acted without entering battle")
					}
					candidate := slot.Candidates[step.Choices[i]]
					if candidate.Features[30] == 1 {
						switches++
						if candidate.Target < 0 || step.Frame.Entities[candidate.Target][52] != 1 {
							t.Fatal("summon target lost reserve entity")
						}
					}
				}
			}
			batch = append(batch, trajectory)
		}
	}
	if switches == 0 || activeReserves == 0 {
		t.Fatal("no sampled switch to a different pet", switches, activeReserves)
	}
	c := DefaultPPOConfig()
	c.Epochs = 2
	c.SequenceLength = 4
	r, err := Train(ctx, m, &battlenet.Adam[float32]{}, batch, c)
	if err != nil {
		t.Fatal(err)
	}
	if r.BehaviorPolicy == r.CandidatePolicy {
		t.Fatal("reserve trajectories did not train")
	}
	t.Logf("trajectories=%d team_turns=%d summons=%d active_reserve_observations=%d epochs=%d", len(batch), r.TeamTurns, switches, activeReserves, len(r.Epochs))
}

func TestReserveCloneNoAlias(t *testing.T) {
	x := [][]battleenv.ReservePet{{{Build: battleenv.Build{30, 30, 30, 30}, SkillMask: 7}}}
	y := battleenv.CloneReserves(x)
	y[0][0].Build[0]++
	if x[0][0].Build[0] != 30 {
		t.Fatal("reserve clone aliases original")
	}
}
