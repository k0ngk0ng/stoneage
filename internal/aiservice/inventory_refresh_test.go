package aiservice

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
)

type inventoryRefreshSession struct {
	*refreshTestSession
	reply       bool
	legacy      bool
	stale       bool
	rejects     int
	attempts    int
	submitError error
}

func (s *inventoryRefreshSession) ExecuteExpected(ctx context.Context, revision uint64, a aigame.Action) error {
	s.mu.Lock()
	s.attempts++
	if s.rejects > 0 {
		s.rejects--
		s.snapshot.Revision++
		s.mu.Unlock()
		return aigame.ErrStaleRevision
	}
	s.mu.Unlock()
	if s.submitError != nil {
		return s.submitError
	}
	if err := s.refreshTestSession.ExecuteExpected(ctx, revision, a); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reply {
		s.snapshot.Revision++
		s.snapshot.AIObservationRevision = s.snapshot.Revision
		s.snapshot.AI = aigame.AIObservation{RequestID: strings.TrimPrefix(a.Command, "AI:"), Received: true, ItemsKnown: !s.legacy, Items: []aigame.AIInventoryItem{{Slot: 5, TemplateID: 4}}}
		if s.stale {
			s.snapshot.AI.RequestID = "0000000000000000"
		}
	}
	return nil
}

func TestInventoryRefreshRequiresReceivedOwnStateNotOutgoingRevision(t *testing.T) {
	for _, kind := range []string{"fresh", "no-response", "legacy", "stale-response"} {
		b, f := gameFixture(t)
		f.snapshot.AI = aigame.AIObservation{Received: true, ItemsKnown: true, Items: []aigame.AIInventoryItem{{Slot: 5, TemplateID: 999}}}
		f.snapshot.AIObservationRevision = 1
		s := &inventoryRefreshSession{refreshTestSession: &refreshTestSession{snapshot: f.snapshot}, reply: kind != "no-response", legacy: kind == "legacy"}
		b.Session = s
		s.stale = kind == "stale-response"
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		got, err := refreshInventoryIdentity(ctx, NewNPCSkill(b, nil))
		cancel()
		switch kind {
		case "fresh":
			if err != nil || got.AI.Items[0].TemplateID != 4 {
				t.Fatalf("fresh: %+v %v", got.AI, err)
			}
		case "no-response", "stale-response":
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("no-response err=%v", err)
			}
		case "legacy":
			if err == nil {
				t.Fatal("accepted legacy response without item identity")
			}
		}
		if len(s.actions) != 1 || s.actions[0].Kind != aigame.ActionStatus || !strings.HasPrefix(s.actions[0].Command, "AI:") {
			t.Fatalf("requests=%v", s.actions)
		}
	}
}

func TestInventoryRefreshRetriesOnlyPreWriteStaleRevision(t *testing.T) {
	unknown := errors.New("write outcome unknown")
	for _, tc := range []struct {
		name                      string
		rejects, attempts, writes int
		submitError, wantError    error
	}{
		{name: "one stale", rejects: 1, attempts: 2, writes: 1},
		{name: "two stale", rejects: 2, attempts: 3, writes: 1},
		{name: "bounded", rejects: 4, attempts: 3, wantError: aigame.ErrStaleRevision},
		{name: "unknown write", attempts: 1, submitError: unknown, wantError: unknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, f := gameFixture(t)
			s := &inventoryRefreshSession{refreshTestSession: &refreshTestSession{snapshot: f.snapshot}, reply: true, rejects: tc.rejects, submitError: tc.submitError}
			b.Session = s
			got, err := refreshInventoryIdentity(context.Background(), NewNPCSkill(b, nil))
			if !errors.Is(err, tc.wantError) {
				t.Fatalf("error=%v want=%v", err, tc.wantError)
			}
			if s.attempts != tc.attempts || len(s.actions) != tc.writes {
				t.Fatalf("attempts=%d writes=%d", s.attempts, len(s.actions))
			}
			if err == nil && (!got.AI.ItemsKnown || got.AI.RequestID != strings.TrimPrefix(s.actions[0].Command, "AI:")) {
				t.Fatalf("missing correlated response: %+v", got.AI)
			}
		})
	}
}
