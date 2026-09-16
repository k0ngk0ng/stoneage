package airuntime

import (
	"context"
	"testing"
	"time"
)

func TestLifePolicyValidationAndPersistence(t *testing.T) {
	if (Goal{Kind: "life"}).LifeDecisionInterval() != 5*time.Minute || (Goal{Kind: "leveling"}).LifeDecisionInterval() != 0 {
		t.Fatal("incorrect opt-in/default cadence")
	}
	store := testStore(t)
	ctx := context.Background()
	for _, goal := range []Goal{
		{Kind: "leveling", Life: &LifePolicy{DecisionIntervalSeconds: 60}},
		{Kind: "life", Life: &LifePolicy{DecisionIntervalSeconds: -1}},
		{Kind: "life", Life: &LifePolicy{DecisionIntervalSeconds: 59}},
		{Kind: "life", Life: &LifePolicy{DecisionIntervalSeconds: 3601}},
	} {
		profile := testProfile()
		profile.Goal = goal
		if _, err := store.CreateProfile(ctx, profile); err == nil {
			t.Fatalf("persisted invalid policy: %+v", goal)
		}
	}
	profile := testProfile()
	profile.Goal = Goal{Kind: "life", Life: &LifePolicy{DecisionIntervalSeconds: 60}}
	created, err := store.CreateProfile(ctx, profile)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := store.GetProfile(ctx, created.ID)
	if err != nil || loaded.Goal.LifeDecisionInterval() != time.Minute {
		t.Fatalf("loaded policy=%+v err=%v", loaded.Goal, err)
	}
	copy := cloneProfile(loaded)
	copy.Goal.Life.DecisionIntervalSeconds = 3600
	if loaded.Goal.Life.DecisionIntervalSeconds != 60 {
		t.Fatal("profile copy aliases life policy")
	}
	updated, err := store.UpdateProfileCAS(ctx, created.ID, created.Version, ProfilePatch{Goal: &copy.Goal})
	if err != nil || updated.Goal.LifeDecisionInterval() != time.Hour {
		t.Fatalf("updated policy=%+v err=%v", updated.Goal, err)
	}
}

func TestLifeActivityPoolValidationAndIsolation(t *testing.T) {
	goal := Goal{Kind: "life", Life: &LifePolicy{Activities: []string{"idle", "chat"}}}
	if err := goal.ValidateLife(); err != nil {
		t.Fatal(err)
	}
	pool := goal.LifeActivities()
	pool[0] = "quest"
	if goal.Life.Activities[0] != "idle" {
		t.Fatal("activity pool aliases stored configuration")
	}
	profile := cloneProfile(Profile{Goal: goal})
	profile.Goal.Life.Activities[0] = "rest"
	if goal.Life.Activities[0] != "idle" {
		t.Fatal("profile clone aliases activity pool")
	}
	for _, pool := range [][]string{{"unknown"}, {"idle", "idle"}} {
		goal.Life.Activities = pool
		if goal.ValidateLife() == nil || goal.LifeDecisionInterval() != 0 {
			t.Fatal("invalid pool accepted")
		}
	}
}
