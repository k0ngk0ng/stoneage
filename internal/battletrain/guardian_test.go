package battletrain

import (
	"context"
	"encoding/json"
	"io"
	"math/rand"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/battleenv"
	"github.com/k0ngk0ng/stoneage/internal/battlenet"
)

func TestGuardianConfigurationGroupingAndSearch(t *testing.T) {
	c := DefaultRunConfig()
	c.ReservePets, c.PetSkillMask = 2, 131
	s, group := scenarioFor(c, 0)
	if err := s.ValidateEnvironment("controlled-battle-v8"); err != nil {
		t.Fatal(err)
	}
	if !scenarioPetSkillsMatch(s, 131) || s.Reserves[0][0].SkillMask != 131 {
		t.Fatal("generated configuration lost guardian")
	}
	original, _ := Digest(s)
	swapScenario(&s)
	if ScenarioGroup(s) != group {
		t.Fatal("skill configuration changed group when swapping sides")
	}
	swapScenario(&s)
	if after, _ := Digest(s); original != after {
		t.Fatal("swap changed caller's configuration")
	}
	s.PetSkillMasks[0] = 3
	if ScenarioGroup(s) == group {
		t.Fatal("different active skill configuration aliases original group")
	}
	asymmetric := ScenarioGroup(s)
	swapScenario(&s)
	if ScenarioGroup(s) != asymmetric {
		t.Fatal("asymmetric skills changed family after side swap")
	}
	base, _ := scenarioFor(DefaultRunConfig(), 0)
	oldGroup := ScenarioGroup(base)
	applyPetSkills(&base, 127)
	if ScenarioGroup(base) != oldGroup {
		t.Fatal("explicit default can leak into another split")
	}
	ec := DefaultEvaluationConfig()
	ec.ReservePets, ec.PetSkillMask = 2, 131
	meta := experimentFixture(t).Environment
	meta.Scenario = "controlled-battle-v7"
	if _, err := NewExperiment(context.Background(), meta, ec, [3]int{2, 1, 1}); err == nil {
		t.Fatal("new configuration accepted old environment")
	}
	meta.Scenario = "controlled-battle-v8"
	x, err := NewExperiment(context.Background(), meta, ec, [3]int{2, 1, 1})
	if err != nil || !x.matches(c) {
		t.Fatal("experiment lost skill configuration", err)
	}
	for _, split := range []string{"validation", "test"} {
		for _, game := range mustEvaluationSuite(t, x, split) {
			if !scenarioPetSkillsMatch(game, 131) {
				t.Fatal("evaluation lost active skills")
			}
		}
	}
	bc := DefaultBuildSearchConfig()
	bc.ReservePets, bc.PetSkillMask = 2, 131
	rosters, err := initialRosters(bc)
	if err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewSource(17))
	for i := 0; i < 100; i++ {
		child := mutateRoster(rosters[0], bc, rng)
		if err = child.validate(bc); err != nil {
			t.Fatal(err)
		}
		if len(child.features(bc)) != 32 || child.Reserves[0][0].SkillMask != 131 {
			t.Fatal("search scorer lost eighth skill category")
		}
		game := buildScenario(bc, child, rosters[0], 0, 0, 0)
		if !scenarioPetSkillsMatch(game, 131) {
			t.Fatal("search changed active pet skill set")
		}
	}
}

func mustEvaluationSuite(t *testing.T, x Experiment, split string) []battleenv.Scenario {
	t.Helper()
	s, err := x.evaluationSuite(split)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestNativeGuardianSamplingAndPPO(t *testing.T) {
	raw := os.Getenv("STONEAGE_BATTLE_GUARDIAN_ENV_COMMAND")
	if raw == "" {
		t.Skip("explicit v8 native engine required")
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
	var trajectories []Trajectory
	selected := 0
	for game := 0; game < 4; game++ {
		c := DefaultRunConfig()
		c.PetSkillMask, c.ReservePets, c.MaxTurns = 131, 1, 40
		s, group := scenarioFor(c, uint64(game*8))
		tr, err := Collect(ctx, engine, s, [2]*battlenet.Model[float32]{m, m}, [2]*rand.Rand{rand.New(rand.NewSource(int64(game + 1))), rand.New(rand.NewSource(int64(game + 9)))}, group, engine.Metadata().Rules)
		if err != nil {
			t.Fatal(err)
		}
		for _, trajectory := range tr {
			for _, step := range trajectory.Steps {
				for i, slot := range step.Frame.Slots {
					if slot.Candidates[step.Choices[i]].Features[32] == 1 {
						selected++
					}
				}
			}
			trajectories = append(trajectories, trajectory)
		}
	}
	if selected == 0 {
		t.Fatal("no guardian actions sampled")
	}
	before, _ := ModelDigest(m)
	config := DefaultPPOConfig()
	config.Epochs = 2
	report, err := Train(ctx, m, &battlenet.Adam[float32]{}, trajectories, config)
	if err != nil || reflect.DeepEqual(before, report.CandidatePolicy) {
		t.Fatal("guardian trajectories did not update model", err)
	}
	t.Logf("native guardian sampled=%d trajectories=%d turns=%d", selected, len(trajectories), report.TeamTurns)
}
