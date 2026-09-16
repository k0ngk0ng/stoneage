package aigame

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSocialSettingPreservesOtherFlagsAndWaitsForEcho(t *testing.T) {
	s, peer := worldTestSession(t)
	a := Action{Kind: ActionSocialSetting, Command: "trade", Value: 1}
	if err := s.Do(context.Background(), a); !errors.Is(err, ErrInvalidAction) {
		t.Fatalf("unknown flags: %v", err)
	}
	s.applyEvent(Event{Function: "FS", Fields: []Field{{Kind: FieldInt, Int: 5}}})
	done := make(chan error, 1)
	go func() { done <- s.ExecuteExpected(context.Background(), s.Snapshot().Revision, a) }()
	_ = peer.SetReadDeadline(time.Now().Add(time.Second))
	packet := make([]byte, 4096)
	n, err := peer.Read(packet)
	if err != nil {
		t.Fatal(err)
	}
	event, err := decodeEvent(packet[:n])
	if err != nil || event.Function != "FS" || event.Fields[0].Int != 37 {
		t.Fatalf("settings overwritten: %+v %v", event, err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if s.Snapshot().Player.SocialFlagsKnown {
		t.Fatal("socket write invented confirmation")
	}
	if err := s.Do(context.Background(), Action{Kind: ActionSocialSetting, Command: "party", Value: 0}); !errors.Is(err, ErrInvalidAction) {
		t.Fatalf("second unconfirmed setting: %v", err)
	}
	s.applyEvent(Event{Function: "FS", Fields: []Field{{Kind: FieldInt, Int: 37}}})
	values, _, err := validateActionLocked(&s.state, Action{Kind: ActionSocialSetting, Command: "party", Value: 0})
	if err != nil || values[0].integer != 36 {
		t.Fatalf("disable did not retain trade/duel: %v %v", values, err)
	}
	s.applyEvent(Event{Function: "FS", Fields: []Field{{Kind: FieldInt, Int: -1}}})
	if s.Snapshot().Player.SocialFlagsKnown {
		t.Fatal("invalid server flags became authoritative")
	}
}
