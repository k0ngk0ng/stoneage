package battletrain

import (
	"context"
	"testing"
)

func TestHealingLoadoutFrozenAcrossTrainingSearchAndEvaluation(t *testing.T) {
	meta := experimentFixture(t).Environment
	groups := map[string]bool{}
	for _, magic := range []int{0, 10, 20} {
		c := DefaultRunConfig()
		c.HealingMagic = magic
		s, group := scenarioFor(c, 0)
		if s.HealingMagic != magic || groups[group] {
			t.Fatal("different loadouts share a scenario group")
		}
		groups[group] = true
		swapScenario(&s)
		s.Seed++
		if ScenarioGroup(s) != group {
			t.Fatal("swapping/reseeding changed the loadout family")
		}
		ec := DefaultEvaluationConfig()
		ec.HealingMagic = magic
		x, err := NewExperiment(context.Background(), meta, ec, [3]int{2, 1, 1})
		if err != nil || !x.matches(c) {
			t.Fatal("experiment failed to preserve configuration", err)
		}
		for _, f := range x.Families {
			if f.Scenario.HealingMagic != magic {
				t.Fatal("experiment family lost healing")
			}
		}
		for _, split := range []string{"validation", "test"} {
			suite, err := x.evaluationSuite(split)
			if err != nil {
				t.Fatal(err)
			}
			for _, s := range suite {
				if s.HealingMagic != magic {
					t.Fatal("heldout schedule changed loadout")
				}
			}
		}
		bad := x
		bad.HealingMagic = (magic + 10) % 30
		if bad.Validate() == nil || bad.matches(c) {
			t.Fatal("changed loadout accepted with old families")
		}
		pool := buildPoolFixture(t)
		pool.Config.HealingMagic = magic
		ec.Points, ec.PetPoints, ec.Level = pool.Config.Points, pool.Config.PetPoints, pool.Config.Level
		x, err = NewExperimentFromSources(context.Background(), meta, ec, [3]int{3, 1, 1}, &pool, nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range x.Families {
			if f.Scenario.HealingMagic != magic {
				t.Fatal("build search/pool inheritance dropped loadout")
			}
		}
		ec.HealingMagic = (magic + 10) % 30
		if _, err = NewExperimentFromSources(context.Background(), meta, ec, [3]int{3, 1, 1}, &pool, nil); err == nil {
			t.Fatal("pool accepted a changed loadout")
		}
	}
}

func TestConsumableLoadoutFrozenAcrossTrainingSearchAndEvaluation(t *testing.T) {
	meta := experimentFixture(t).Environment
	groups := map[string]bool{}
	for _, magic := range []int{0, 2, 15} {
		c := DefaultRunConfig()
		c.HealingItems = magic
		s, group := scenarioFor(c, 0)
		if s.HealingItems != magic || groups[group] {
			t.Fatal("different loadouts share a scenario group")
		}
		groups[group] = true
		swapScenario(&s)
		s.Seed++
		if ScenarioGroup(s) != group {
			t.Fatal("swapping/reseeding changed the loadout family")
		}
		ec := DefaultEvaluationConfig()
		ec.HealingItems = magic
		x, err := NewExperiment(context.Background(), meta, ec, [3]int{2, 1, 1})
		if err != nil || !x.matches(c) {
			t.Fatal("experiment failed to preserve configuration", err)
		}
		for _, f := range x.Families {
			if f.Scenario.HealingItems != magic {
				t.Fatal("experiment family lost healing")
			}
		}
		for _, split := range []string{"validation", "test"} {
			suite, err := x.evaluationSuite(split)
			if err != nil {
				t.Fatal(err)
			}
			for _, s := range suite {
				if s.HealingItems != magic {
					t.Fatal("heldout schedule changed loadout")
				}
			}
		}
		bad := x
		bad.HealingItems = (magic + 1) % 16
		if bad.Validate() == nil || bad.matches(c) {
			t.Fatal("changed loadout accepted with old families")
		}
		pool := buildPoolFixture(t)
		pool.Config.HealingItems = magic
		ec.Points, ec.PetPoints, ec.Level = pool.Config.Points, pool.Config.PetPoints, pool.Config.Level
		x, err = NewExperimentFromSources(context.Background(), meta, ec, [3]int{3, 1, 1}, &pool, nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range x.Families {
			if f.Scenario.HealingItems != magic {
				t.Fatal("build search/pool inheritance dropped loadout")
			}
		}
		ec.HealingItems = (magic + 1) % 16
		if _, err = NewExperimentFromSources(context.Background(), meta, ec, [3]int{3, 1, 1}, &pool, nil); err == nil {
			t.Fatal("pool accepted a changed loadout")
		}
	}
}
