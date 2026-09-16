package aigame

import (
	"context"
	"strings"
	"testing"
	"time"
)

const partyObservationBase = "AI|v=1|chara=17|end=0,0,0,0,0,0|now=0,0,0,0,0,0|ride=0"

func TestPartyModeObservationValidation(t *testing.T) {
	for _, suffix := range []string{"", "|party_mode=0", "|party_mode=1", "|party_mode=2"} {
		o, ok := parseAIObservation(strings.Split(partyObservationBase+suffix, "|")[1:])
		if !ok || o.PartyModeKnown != (suffix != "") {
			t.Fatalf("suffix %q: %+v valid=%v", suffix, o, ok)
		}
	}
	for _, suffix := range []string{"|party_mode=-1", "|party_mode=3", "|party_mode=x", "|party_mode=0|party_mode=1"} {
		if _, ok := parseAIObservation(strings.Split(partyObservationBase+suffix, "|")[1:]); ok {
			t.Fatalf("accepted invalid mode %q", suffix)
		}
	}
}

func TestPartyModeInvalidationAndRefresh(t *testing.T) {
	for name, event := range map[string]Event{
		"acknowledgement": {Function: "PR"},
		"empty-row":       stringEvent("S", "N0|0"),
		"login":           stringEvent("CharLogin", "successful"),
		"logout":          stringEvent("CharLogout", "successful"),
		"older-server":    stringEvent("S", partyObservationBase),
	} {
		t.Run(name, func(t *testing.T) {
			session, _ := worldTestSession(t)
			session.applyEvent(stringEvent("S", partyObservationBase+"|party_mode=0"))
			if !session.Snapshot().AI.PartyModeKnown {
				t.Fatal("missing initial observation")
			}
			session.applyEvent(event)
			if session.Snapshot().AI.PartyModeKnown {
				t.Fatal("old solo evidence survived change")
			}
			session.applyEvent(stringEvent("S", partyObservationBase+"|party_mode=2"))
			if a := session.Snapshot().AI; !a.PartyModeKnown || a.PartyMode != 2 {
				t.Fatalf("refresh: %+v", a)
			}
		})
	}
}

func TestPartyWriteInvalidatesSoloBeforeReply(t *testing.T) {
	session, peer := worldTestSession(t)
	session.applyEvent(stringEvent("S", partyObservationBase+"|party_mode=0"))
	result := make(chan error, 1)
	go func() { result <- session.Do(context.Background(), Party(2, 3, 1)) }()
	_ = peer.SetReadDeadline(time.Now().Add(time.Second))
	packet := make([]byte, 4096)
	if _, err := peer.Read(packet); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if session.Snapshot().AI.PartyModeKnown {
		t.Fatal("party write retained solo evidence")
	}
}

func TestPartyManualAndFailedWritesInvalidateSolo(t *testing.T) {
	t.Run("manual bridge packet", func(t *testing.T) {
		session, _ := worldTestSession(t)
		session.applyEvent(stringEvent("S", partyObservationBase+"|party_mode=0"))
		packet, err := session.buildPacket("PR", []wireValue{{kind: wireInt, integer: 2}, {kind: wireInt, integer: 3}, {kind: wireInt, integer: 1}})
		if err != nil {
			t.Fatal(err)
		}
		if err := session.ApplyClientPacket(packet); err != nil {
			t.Fatal(err)
		}
		if session.Snapshot().AI.PartyModeKnown {
			t.Fatal("manual request retained solo evidence")
		}
	})
	t.Run("failed delivery", func(t *testing.T) {
		session, peer := worldTestSession(t)
		session.applyEvent(stringEvent("S", partyObservationBase+"|party_mode=0"))
		_ = peer.Close()
		if err := session.Do(context.Background(), Party(2, 3, 1)); err == nil {
			t.Fatal("closed peer write succeeded")
		}
		if session.Snapshot().AI.PartyModeKnown {
			t.Fatal("uncertain write retained solo evidence")
		}
	})
}
