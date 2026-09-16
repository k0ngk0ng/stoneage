package aisupervisor

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestLifeDeadlineDoesNotOverrideExecutionGuards(t *testing.T) {
	for _, test := range []struct {
		name     string
		snapshot Snapshot
		state    string
		message  string
		budget   bool
	}{
		{"not-ready", Snapshot{}, StateWaiting, "game readiness", false},
		{"active-task", Snapshot{GameReady: true, ActiveTasks: []TaskHandle{{Handle: "still-running", Status: TaskStatusRunning}}}, StateWaiting, "active skill tasks", false},
		{"budget", Snapshot{GameReady: true}, StatePaused, "", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := testSupervisorStore(t)
			input := lifeTestProfile("life-guard-"+test.name, 60)
			config := supervisorTestConfig()
			if test.budget {
				input.DailyTokenBudget = 1
				config.TokenCharge = 2
			}
			profile, err := store.CreateProfile(context.Background(), input)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			savePersistedCheckpoint(t, store, profile, persistedState{
				ProfileVersion: profile.Version, ThreadID: "life-guard-thread",
				ModelTurnDone: true, WaitingForWake: true, NextDecisionAt: now.Add(-time.Hour),
				Snapshot: test.snapshot, LastProgressAt: now,
			})
			session := &fakeSession{runner: &fakeRunner{}, wake: make(chan struct{}, 2), snapshot: test.snapshot}
			supervisor, err := New(context.Background(), store, &fakeFactory{sessions: map[string]*fakeSession{profile.ID: session}}, config)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = supervisor.Close() })
			if err := supervisor.Start(context.Background(), profile.ID); err != nil {
				t.Fatal(err)
			}
			waitFor(t, func() bool {
				status := profileStatus(t, supervisor, profile.ID)
				return status.State == test.state && strings.Contains(status.Message, test.message)
			})
			if calls := len(session.runner.Requests()); calls != 0 {
				t.Fatalf("expired life deadline bypassed %s: %d turns", test.name, calls)
			}
			if test.budget {
				usage, err := store.TokenUsage(context.Background(), profile.ID, now)
				if err != nil || usage.OverBudgetAttempts != 1 {
					t.Fatalf("life turn bypassed accounting: %+v err=%v", usage, err)
				}
			}
		})
	}
}
