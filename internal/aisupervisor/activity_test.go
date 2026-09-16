package aisupervisor

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestLifeActivityPersistsAndGoalCompletionDoesNotStopHeartbeat(t *testing.T) {
	ctx := context.Background()
	store := testSupervisorStore(t)
	input := lifeTestProfile("persistent-idle", 60)
	input.Goal.Life.Activities = []string{"idle"}
	profile, err := store.CreateProfile(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	session := &fakeSession{runner: &fakeRunner{}, wake: make(chan struct{}, 2), snapshot: Snapshot{GameReady: true, GoalComplete: true, ProgressKey: "idle"}}
	factory := &fakeFactory{sessions: map[string]*fakeSession{profile.ID: session}}
	supervisor, err := New(ctx, store, factory, supervisorTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := supervisor.Start(ctx, profile.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		return len(session.runner.Requests()) == 1 && profileStatus(t, supervisor, profile.ID).ModelTurnDone
	})
	status := profileStatus(t, supervisor, profile.ID)
	if status.State == StateCompleted || status.GoalComplete || status.Activity.Kind != "idle" || status.NextDecisionAt.IsZero() {
		t.Fatalf("life stopped or lost activity: %+v", status)
	}
	if !strings.Contains(session.runner.Requests()[0].Prompt, "Runtime-selected life activity") || !strings.Contains(session.runner.Requests()[0].Prompt, `"kind":"idle"`) {
		t.Fatal("selected activity missing from actual runner prompt")
	}
	if !strings.Contains(session.runner.Requests()[0].Prompt, "Activity instructions and completion criteria") || !strings.Contains(session.runner.Requests()[0].Prompt, `"completion":`) {
		t.Fatal("activity catalog instructions missing from actual runner prompt")
	}
	_, saved := loadPersistedCheckpoint(t, store, profile.ID)
	if saved.Activity != status.Activity {
		t.Fatal("activity was not persisted before dispatch")
	}
	if err := supervisor.Close(); err != nil {
		t.Fatal(err)
	}
	// Restart while an idle task is still in progress: it keeps its deadline
	// and does not reroll or dispatch another model turn immediately.
	resumed := &fakeSession{runner: &fakeRunner{}, wake: make(chan struct{}, 2), snapshot: session.snapshot}
	restarted, err := New(ctx, store, &fakeFactory{sessions: map[string]*fakeSession{profile.ID: resumed}}, supervisorTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if err := restarted.Start(ctx, profile.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return profileStatus(t, restarted, profile.ID).State == StateWaiting })
	current := profileStatus(t, restarted, profile.ID)
	if current.Activity != saved.Activity || len(resumed.runner.Requests()) != 0 {
		t.Fatalf("restart rerolled/restarted idle: %+v", current)
	}
}

func TestLifeActiveTaskKeepsItsAssignment(t *testing.T) {
	now := time.Now().UTC()
	current := LifeActivity{Kind: "wander", StartedAt: now.Add(-2 * time.Minute), Until: now.Add(-time.Minute)}
	supervisor := &Supervisor{cfg: Config{Clock: func() time.Time { return now }}}
	managed := &managedProfile{state: Status{Activity: current}}
	profile := lifeTestProfile("busy", 60)
	got, err := supervisor.ensureLifeActivity(managed, 1, profile, Snapshot{ActiveTasks: []TaskHandle{{Handle: "ongoing", Status: TaskStatusRunning}}})
	if err != nil || got != current {
		t.Fatalf("busy task replaced: %+v %v", got, err)
	}
}

func TestLifeActivityReplacementAfterRestart(t *testing.T) {
	for _, completedTask := range []bool{false, true} {
		name := "expired"
		if completedTask {
			name = "completed-task"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			store := testSupervisorStore(t)
			input := lifeTestProfile("expired-activity", 60)
			input.Goal.Life.Activities = []string{"rest"}
			profile, err := store.CreateProfile(ctx, input)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			snapshot := Snapshot{GameReady: true, ProgressKey: "quiet"}
			prior := cloneSnapshot(snapshot)
			next := now.Add(-time.Minute)
			if completedTask {
				prior.ActiveTasks = []TaskHandle{{Handle: "previous-task", Status: TaskStatusRunning}}
				next = now.Add(time.Minute)
			}
			savePersistedCheckpoint(t, store, profile, persistedState{
				ProfileVersion: profile.Version, ThreadID: "life-thread", ModelTurnDone: true,
				WaitingForWake: true, NextDecisionAt: next,
				Activity: LifeActivity{Kind: "wander", StartedAt: now.Add(-2 * time.Minute), Until: next},
				Snapshot: prior, LastProgressAt: now,
			})
			session := &fakeSession{runner: &fakeRunner{}, wake: make(chan struct{}, 2), snapshot: snapshot}
			config := supervisorTestConfig()
			config.Clock = func() time.Time { return now }
			supervisor, err := New(ctx, store, &fakeFactory{sessions: map[string]*fakeSession{profile.ID: session}}, config)
			if err != nil {
				t.Fatal(err)
			}
			defer supervisor.Close()
			if err := supervisor.Start(ctx, profile.ID); err != nil {
				t.Fatal(err)
			}
			waitFor(t, func() bool {
				return profileStatus(t, supervisor, profile.ID).State == StateWaiting && len(session.runner.Requests()) == 1
			})
			status := profileStatus(t, supervisor, profile.ID)
			if status.Activity.Kind != "rest" || !status.Activity.StartedAt.Equal(now) || !status.Activity.Until.Equal(now.Add(time.Minute)) {
				t.Fatalf("expired activity not replaced: %+v", status.Activity)
			}
			request := session.runner.Requests()[0]
			if !request.Resume || request.ThreadID != "life-thread" || !strings.Contains(request.Prompt, `"kind":"rest"`) {
				t.Fatalf("activity did not use existing thread: %+v", request)
			}

		})
	}
}
