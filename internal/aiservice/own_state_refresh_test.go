package aiservice

import (
	"context"
	"sync"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
)

type refreshTestSession struct {
	mu       sync.Mutex
	snapshot aigame.Snapshot
	actions  []aigame.Action
}

func (session *refreshTestSession) Observe(context.Context) (aigame.Snapshot, error) {
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.snapshot, nil
}

func (session *refreshTestSession) ExecuteExpected(_ context.Context, revision uint64, action aigame.Action) error {
	session.mu.Lock()
	defer session.mu.Unlock()
	if revision != session.snapshot.Revision {
		return aigame.ErrStaleRevision
	}
	session.actions = append(session.actions, action)
	session.snapshot.Revision++
	return nil
}

func TestOwnStateRefreshIsBoundedAndNeverInventsConfirmation(t *testing.T) {
	backend, fixture := gameFixture(t)
	session := &refreshTestSession{snapshot: fixture.snapshot}
	backend.Session = session
	backend.OwnStateRefresh = &OwnStateRefresher{}
	var group sync.WaitGroup
	for index := 0; index < 20; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			observed, err := backend.Observe(context.Background(), backend.Binding)
			if err != nil {
				t.Error(err)
			}
			if _, known := observed.Skills["learn_ride"]; known {
				t.Error("outgoing request became skill evidence")
			}
		}()
	}
	group.Wait()
	if len(session.actions) != 1 || session.actions[0].Kind != aigame.ActionStatus || session.actions[0].Command != "AI" {
		t.Fatalf("refresh was not one bounded read: %+v", session.actions)
	}
	if _, err := backend.Gate.Takeover("manual"); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.Observe(context.Background(), backend.Binding); err == nil {
		t.Fatal("refresh survived ownership revocation")
	}
	if len(session.actions) != 1 {
		t.Fatal("sent a query after takeover")
	}
}

func TestOwnStateRefreshDoesNotQueryBeforeWorldReady(t *testing.T) {
	for _, phase := range []aigame.Phase{aigame.PhaseBattle, aigame.PhaseCharacterList, aigame.PhaseDisconnected} {
		backend, fixture := gameFixture(t)
		fixture.snapshot.Phase = phase
		backend.OwnStateRefresh = &OwnStateRefresher{}
		if _, err := backend.Observe(context.Background(), backend.Binding); err != nil {
			t.Fatal(err)
		}
		if fixture.writes != 0 {
			t.Fatalf("queried during %s", phase)
		}
	}
}

func TestOwnStateRefreshToleratesPreWriteRevisionChurn(t *testing.T) {
	for _, rejects := range []int{1, 4} {
		b, f := gameFixture(t)
		s := &inventoryRefreshSession{refreshTestSession: &refreshTestSession{snapshot: f.snapshot}, rejects: rejects}
		b.Session = s
		refresh := &OwnStateRefresher{}
		got, err := refresh.Refresh(context.Background(), b, f.snapshot)
		if err != nil {
			t.Fatal(err)
		}
		wantWrites := 1
		if rejects > 3 {
			wantWrites = 0
		}
		if len(s.actions) != wantWrites || s.attempts > 3 {
			t.Fatalf("attempts=%d writes=%d", s.attempts, len(s.actions))
		}
		if got.AI.ItemsKnown || got.AIObservationRevision != f.snapshot.AIObservationRevision {
			t.Fatal("outgoing refresh invented inventory identity")
		}
	}
}
