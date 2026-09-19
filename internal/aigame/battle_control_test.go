package aigame

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

const playerPetRoster = "BC|0|0|Hero||100|1|23|23|4|5|Pet||100|1|23|23|0|A|Enemy||100|1|10|10|0"

func battleControlSession(t *testing.T, flags string, pet bool) (*Session, net.Conn) {
	t.Helper()
	session, peer := worldTestSession(t)
	session.applyEvent(Event{Function: "EN", Fields: []Field{{Kind: FieldInt, Int: 1}, {Kind: FieldInt, Int: 218}}})
	session.applyEvent(stringEvent("B", "BP|0|"+flags+"|10"))
	roster := playerPetRoster
	if !pet {
		roster = "BC|0|0|Hero||100|1|23|23|4|A|Enemy||100|1|10|10|0"
	}
	session.applyEvent(stringEvent("B", roster))
	return session, peer
}

func writeBattleTestAction(t *testing.T, session *Session, peer net.Conn, command string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- session.ExecuteExpected(ctx, session.Snapshot().Revision, Battle(command)) }()
	if err := peer.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	packet := make([]byte, 4096)
	n, err := peer.Read(packet)
	if err != nil {
		t.Fatal(err)
	}
	event, err := decodeEvent(packet[:n])
	if err != nil || event.Function != "B" || eventText(event, 0) != command {
		t.Fatalf("wire event=%+v error=%v want B(%q)", event, err, command)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestBattlePlayerAndPetCommandsHaveIndependentSubmissionLocks(t *testing.T) {
	session, peer := battleControlSession(t, "0", true)
	if !session.Snapshot().Battle.PlayerCommandReady() || session.Snapshot().Battle.PetCommandReady() {
		t.Fatal("player must choose before the active pet")
	}
	if err := session.Execute(context.Background(), Battle("W|FF|FF")); !errors.Is(err, ErrBattleNotReady) {
		t.Fatalf("pet before player: %v", err)
	}
	writeBattleTestAction(t, session, peer, "E")
	battle := session.Snapshot().Battle
	if battle.PlayerCommandReady() || !battle.PetCommandReady() || !battle.CommandReady {
		t.Fatalf("player escape must leave the pet command available: %+v", battle)
	}
	// A roster refresh is not a new command round and cannot authorize a
	// second player action while the first is awaiting the pet command.
	session.applyEvent(stringEvent("B", playerPetRoster))
	if err := session.Execute(context.Background(), Battle("E")); !errors.Is(err, ErrBattleNotReady) {
		t.Fatalf("duplicate player command: %v", err)
	}
	writeBattleTestAction(t, session, peer, "W|FF|FF")
	if session.Snapshot().Battle.CommandReady {
		t.Fatal("both commands submitted must close the round")
	}
	if err := session.Execute(context.Background(), Battle("W|FF|FF")); !errors.Is(err, ErrBattleNotReady) {
		t.Fatalf("duplicate pet command: %v", err)
	}
	session.applyEvent(stringEvent("B", "BP|0|0|10"))
	if !session.Snapshot().Battle.PlayerCommandReady() || session.Snapshot().Battle.PetCommandReady() {
		t.Fatal("new authoritative BP did not reset the two command locks")
	}
}

func TestBattleDisabledMenusAcceptOnlyNativeDefaults(t *testing.T) {
	session, peer := battleControlSession(t, "A", true)
	if err := session.Execute(context.Background(), Battle("E")); !errors.Is(err, ErrBattleNotReady) {
		t.Fatalf("disabled player menu accepted escape: %v", err)
	}
	writeBattleTestAction(t, session, peer, "N")
	if err := session.Execute(context.Background(), Battle("W|0|A")); !errors.Is(err, ErrBattleNotReady) {
		t.Fatalf("disabled pet menu accepted a skill: %v", err)
	}
	writeBattleTestAction(t, session, peer, "W|FF|FF")
}

func TestBattleWithoutActivePetNeedsOnlyPlayerCommand(t *testing.T) {
	session, peer := battleControlSession(t, "0", false)
	writeBattleTestAction(t, session, peer, "E")
	if session.Snapshot().Battle.CommandReady || session.Snapshot().Battle.HasActivePet() {
		t.Fatal("standby or absent pet must not create a second command")
	}
}

func TestBattleSurpriseDefaultsAndBoomerangFlagFollowNativeBits(t *testing.T) {
	// 0x10 is enemy surprise; 0x04 is boomerang and must not lock the pet.
	for _, flags := range []string{"10", "4"} {
		session, peer := battleControlSession(t, flags, true)
		if flags == "10" {
			if err := session.Execute(context.Background(), Battle("E")); !errors.Is(err, ErrBattleNotReady) {
				t.Fatalf("surprise accepted escape: %v", err)
			}
			writeBattleTestAction(t, session, peer, "N")
			writeBattleTestAction(t, session, peer, "W|FF|FF")
		} else {
			writeBattleTestAction(t, session, peer, "H|A")
			writeBattleTestAction(t, session, peer, "W|0|A")
		}
	}
}

func TestTerminalBattleCannotAuthorizeEOAfterCharacterLogout(t *testing.T) {
	session, _ := battleControlSession(t, "0", false)
	session.applyEvent(stringEvent("B", "BU"))
	session.applyEvent(stringEvent("CharLogout", "successful"))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := session.Execute(ctx, EndBattle()); !errors.Is(err, ErrWrongPhase) && !errors.Is(err, ErrCharacterRequired) {
		t.Fatalf("old terminal battle authorized EO after logout: %v", err)
	}
}

func TestEscapeRequiresLocalSuccessBeforeTerminalHandshake(t *testing.T) {
	session, peer := battleControlSession(t, "0", false)
	for _, movie := range []string{"BP|BE|eA|f1|", "BP|BE|e0|f0|", "BP|BE|et0|f1|", "BP|BE|e0|f11|"} {
		session.applyEvent(stringEvent("B", movie))
		if session.Snapshot().Battle.Result != "" {
			t.Fatalf("nonlocal/failed escape confirmed: %q", movie)
		}
		if err := session.Execute(context.Background(), EndBattle()); !errors.Is(err, ErrBattleNotReady) {
			t.Fatalf("unfinished battle accepted EO: %v", err)
		}
	}
	session.applyEvent(stringEvent("B", "BP|BE|e0|f1|"))
	if battle := session.Snapshot().Battle; battle.Result != "escaped" || !battle.Active {
		t.Fatalf("escape must wait for terminal handshake: %+v", battle)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- session.Execute(ctx, EndBattle()) }()
	peer.SetReadDeadline(time.Now().Add(time.Second))
	packet := make([]byte, 4096)
	n, err := peer.Read(packet)
	if err != nil {
		t.Fatal(err)
	}
	event, err := decodeEvent(packet[:n])
	if err != nil || event.Function != "EO" || eventInt(event, 0, -1) != 0 {
		t.Fatalf("EO packet=%+v err=%v", event, err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if session.Snapshot().Phase != PhaseWorld || session.Snapshot().Battle.Active {
		t.Fatal("terminal handshake did not return to world")
	}
	session.applyEvent(stringEvent("B", "BP|0|0|10"))
	session.applyEvent(stringEvent("B", playerPetRoster))
	if session.Snapshot().Battle.Active {
		t.Fatal("late controls resurrected the escaped battle")
	}
	if err := session.Execute(context.Background(), EndBattle()); !errors.Is(err, ErrBattleNotReady) {
		t.Fatalf("duplicate EO: %v", err)
	}
}

func TestBattleResultLocksCommandsUntilEO(t *testing.T) {
	session, _ := battleControlSession(t, "0", true)
	session.applyEvent(stringEvent("RS", "win"))
	battle := session.Snapshot().Battle
	if battle.CommandReady || battle.PlayerCommandReady() || battle.PetCommandReady() {
		t.Fatalf("terminal result reopened command input: %+v", battle)
	}
	if err := session.Execute(context.Background(), Battle("E")); !errors.Is(err, ErrBattleNotReady) {
		t.Fatalf("command accepted after terminal result: %v", err)
	}
}

// A server event that lands while a command write is completing already
// describes the next turn. Recording the in-flight submission against that
// newer turn would mark it answered and leave the caller waiting for a command
// it never sends, which is a battle that never advances again.
func TestBattleSubmissionIsNotRecordedAgainstANewerTurn(t *testing.T) {
	session, peer := battleControlSession(t, "0", false)
	revision := session.Snapshot().Revision
	done := make(chan error, 1)
	go func() { done <- session.ExecuteExpected(context.Background(), revision, Battle("H|A")) }()

	// An unbuffered peer only completes the write while this read is in
	// progress, so the next turn's menu packet is applied in the same instant
	// the write returns.
	if err := peer.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	packet := make([]byte, 4096)
	if _, err := peer.Read(packet); err != nil {
		t.Fatal(err)
	}
	session.applyEvent(stringEvent("B", "BP|0|0|10"))
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	battle := session.Snapshot().Battle
	if battle.LastCommand != "" || battle.PlayerSubmitted || !battle.PlayerCommandReady() {
		t.Fatalf("the new turn was recorded as answered: %+v", battle)
	}
}
