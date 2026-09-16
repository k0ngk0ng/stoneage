package aisupervisor

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/airuntime"
)

func TestDueScheduleWakesBeforeHeartbeatAndDeliversOnce(t *testing.T) {
	for _, life := range []bool{true, false} {
		name := "life"
		if !life {
			name = "event-driven"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			store := testSupervisorStore(t)
			input := lifeTestProfile("scheduled-"+name, 60)
			if !life {
				input.Goal.Kind = "leveling"
				input.Goal.Life = nil
			}
			profile, err := store.CreateProfile(ctx, input)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			reminder, err := store.CreateSchedule(ctx, profile.ID, airuntime.ScheduleInput{
				Kind: "reminder", Prompt: "remember-the-riverside-meeting", RunAt: now.Add(2 * time.Second),
			})
			if err != nil {
				t.Fatal(err)
			}
			snapshot := Snapshot{GameReady: true, ProgressKey: "quiet"}
			next := time.Time{}
			if life {
				next = now.Add(time.Minute)
			}
			savePersistedCheckpoint(t, store, profile, persistedState{
				ProfileVersion: profile.Version, ThreadID: "scheduled-thread", ModelTurnDone: true,
				WaitingForWake: true, NextDecisionAt: next, Snapshot: snapshot, LastProgressAt: now,
			})
			var clock atomic.Int64
			clock.Store(now.UnixNano())
			config := supervisorTestConfig()
			config.Clock = func() time.Time { return time.Unix(0, clock.Load()).UTC() }
			session := &fakeSession{runner: &fakeRunner{}, wake: make(chan struct{}, 2), snapshot: snapshot}
			supervisor, err := New(ctx, store, &fakeFactory{sessions: map[string]*fakeSession{profile.ID: session}}, config)
			if err != nil {
				t.Fatal(err)
			}
			defer supervisor.Close()
			if err := supervisor.Start(ctx, profile.ID); err != nil {
				t.Fatal(err)
			}
			waitFor(t, func() bool { return profileStatus(t, supervisor, profile.ID).State == StateWaiting })
			if len(session.runner.Requests()) != 0 {
				t.Fatal("reminder delivered before due time")
			}
			clock.Store(now.Add(3 * time.Second).UnixNano())
			waitFor(t, func() bool {
				entry, err := store.GetSchedule(ctx, profile.ID, reminder.ID)
				return err == nil && entry.Status == airuntime.ScheduleDelivered
			})
			requests := session.runner.Requests()
			if len(requests) != 1 || !strings.Contains(requests[0].Prompt, reminder.Prompt) || !requests[0].Resume || requests[0].ThreadID != "scheduled-thread" {
				t.Fatalf("reminder delivery lost prompt/thread or duplicated: %+v", requests)
			}
			waitFor(t, func() bool { return profileStatus(t, supervisor, profile.ID).State == StateWaiting })
			if len(session.runner.Requests()) != 1 {
				t.Fatal("delivered reminder woke a second turn")
			}
		})
	}
}
