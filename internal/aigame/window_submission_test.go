package aigame

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

func windowSubmissionEvent() Event {
	return Event{Function: "WN", Fields: []Field{{Kind: FieldInt, Int: 0}, {Kind: FieldInt, Int: 1}, {Kind: FieldInt, Int: 231}, {Kind: FieldInt, Int: 42}, {Kind: FieldString, Text: []byte("start")}}}
}

func TestWindowSubmissionFencesDuplicateButNotNewEnvelope(t *testing.T) {
	s, peer := worldTestSession(t)
	s.applyEvent(windowSubmissionEvent())
	write := func() {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- s.ExecuteExpected(ctx, s.Snapshot().Revision, Window(2, 3, 231, 42, 1, "")) }()
		_ = peer.SetReadDeadline(time.Now().Add(time.Second))
		buf := make([]byte, 4096)
		n, err := peer.Read(buf)
		if err != nil {
			t.Fatal(err)
		}
		event, err := decodeEvent(buf[:n])
		if err != nil || event.Function != "WN" || eventInt(event, 2, -1) != 231 || eventInt(event, 4, -1) != 1 {
			t.Fatal("unexpected window packet", err)
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	write()
	w := s.Snapshot().ActiveWindow
	if w == nil || !w.Submitted || !w.Open {
		t.Fatal("write must record submission without inventing server closure")
	}
	if windows := s.Snapshot().Windows; len(windows) == 0 || !windows[len(windows)-1].Submitted {
		t.Fatal("window history disagrees with active submission")
	}
	err := s.Do(context.Background(), Window(2, 3, 231, 42, 1, ""))
	if !errors.Is(err, ErrInvalidAction) {
		t.Fatalf("repeat submitted window: %v", err)
	}
	_ = peer.SetReadDeadline(time.Now().Add(30 * time.Millisecond))
	if _, err := peer.Read(make([]byte, 4096)); !isTimeout(err) {
		t.Fatalf("duplicate wrote packet: %v", err)
	}
	// Even identical sequence/object/data from a new server event is fresh.
	s.applyEvent(windowSubmissionEvent())
	if s.Snapshot().ActiveWindow.Submitted {
		t.Fatal("new server window inherited old submission")
	}
	windows := s.Snapshot().Windows
	if len(windows) != 2 || !windows[0].Submitted || windows[1].Submitted {
		t.Fatal("replacement lost prior submission or consumed the fresh window")
	}
	write()
}

func TestWindowSubmissionCannotMarkReplacementEnvelope(t *testing.T) {
	state := gameState{}
	state.applyWindow(windowSubmissionEvent())
	previous := state.activeWindow
	state.applyWindow(windowSubmissionEvent())
	markWindowSubmittedLocked(&state, previous)
	if state.activeWindow.Submitted {
		t.Fatal("old completion consumed identical replacement window")
	}
	markWindowSubmittedLocked(&state, state.activeWindow)
	if !state.activeWindow.Submitted {
		t.Fatal("current submission missing")
	}
}

func TestFailedWindowWriteDoesNotClaimSubmission(t *testing.T) {
	s, peer := worldTestSession(t)
	s.applyEvent(windowSubmissionEvent())
	_ = peer.Close()
	err := s.Do(context.Background(), Window(2, 3, 231, 42, 1, ""))
	if err == nil || s.Snapshot().ActiveWindow.Submitted {
		t.Fatal("failed write claimed submission")
	}
	if !errors.Is(err, net.ErrClosed) && s.Snapshot().LastError == "" {
		t.Fatal("write failure was not recorded")
	}
}
