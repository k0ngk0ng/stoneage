package aigame

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/server/go/namedproto"
)

func statPointsEvent(points int32) Event {
	return Event{Function: "SKUP", Fields: []Field{{Kind: FieldInt, Int: points}}}
}

func TestLevelChangeInvalidatesOldStatPointCount(t *testing.T) {
	for name, packet := range map[string]string{
		"masked": "P" + string(namedproto.EncodeInt(2048)) + "|6",
		"full":   "P1|10|20|3|4|5|6|7|8|9|10|6|12|13|14|15|16|17|18|19|20|1000|2|3|4|5|6|7|8|Hero|title",
	} {
		t.Run(name, func(t *testing.T) {
			session, _ := worldTestSession(t)
			session.state.snapshot.Player.Level = 5
			session.applyEvent(statPointsEvent(0))
			session.applyEvent(stringEvent("S", packet))
			if p := session.Snapshot().Player; p.Level != 6 || p.StatPointsKnown {
				t.Fatalf("level-up reused old point count: %+v", p)
			}
			session.applyEvent(statPointsEvent(3))
			session.applyEvent(stringEvent("S", packet))
			if p := session.Snapshot().Player; !p.StatPointsKnown || p.UnspentStatPoints != 3 {
				t.Fatalf("unchanged level discarded refreshed points: %+v", p)
			}
		})
	}
}

func TestStatPointsObservationAndAllocationFence(t *testing.T) {
	session, peer := worldTestSession(t)
	session.state.snapshot.Player.HasStatus = true
	session.state.snapshot.Player.HP = 20
	action := Action{Kind: ActionAllocateStat, Index: 0}
	if err := session.Do(context.Background(), action); !errors.Is(err, ErrInvalidAction) {
		t.Fatalf("unknown points accepted: %v", err)
	}
	session.applyEvent(statPointsEvent(2))
	result := make(chan error, 1)
	go func() { result <- session.ExecuteExpected(context.Background(), session.Snapshot().Revision, action) }()
	_ = peer.SetReadDeadline(time.Now().Add(time.Second))
	packet := make([]byte, 4096)
	n, err := peer.Read(packet)
	if err != nil {
		t.Fatal(err)
	}
	event, err := decodeEvent(packet[:n])
	if err != nil || event.Function != "SKUP" || len(event.Fields) != 1 || event.Fields[0].Int != 0 {
		t.Fatalf("allocation packet: %+v %v", event, err)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if session.Snapshot().Player.StatPointsKnown {
		t.Fatal("write invented authoritative remaining points")
	}
	if err := session.Do(context.Background(), action); !errors.Is(err, ErrInvalidAction) {
		t.Fatalf("second allocation before observation accepted: %v", err)
	}
	// The legacy server does not echo SKUP after allocation. Modern S:AI
	// refreshes the value, including an authoritative zero.
	base := "AI|v=1|chara=17|end=0,0,0,0,0,0|now=0,0,0,0,0,0|ride=0|stat_points="
	session.applyEvent(stringEvent("S", base+"0"))
	if p := session.Snapshot().Player; !p.StatPointsKnown || p.UnspentStatPoints != 0 {
		t.Fatalf("zero remaining points not observed: %+v", p)
	}
	if err := session.Do(context.Background(), action); !errors.Is(err, ErrInvalidAction) {
		t.Fatalf("zero points accepted: %v", err)
	}
	session.applyEvent(statPointsEvent(-1))
	if session.Snapshot().Player.StatPointsKnown {
		t.Fatal("negative points accepted")
	}
}

func TestStatAllocationValidationAndRacingRefresh(t *testing.T) {
	state := newGameState(true)
	state.snapshot.Phase = PhaseWorld
	state.snapshot.Player = PlayerSnapshot{HasStatus: true, HP: 10, StatPointsKnown: true, UnspentStatPoints: 2}
	for index := int32(0); index < 4; index++ {
		values, function, err := validateActionLocked(&state, Action{Kind: ActionAllocateStat, Index: index})
		if err != nil || function != "SKUP" || len(values) != 1 || values[0].integer != index {
			t.Fatalf("index %d: %v", index, err)
		}
	}
	for _, action := range []Action{{Kind: ActionAllocateStat, Index: -1}, {Kind: ActionAllocateStat, Index: 4}, {Kind: ActionAllocateStat, Value: 2}} {
		if _, _, err := validateActionLocked(&state, action); err == nil {
			t.Fatalf("invalid action accepted: %+v", action)
		}
	}
	state.snapshot.Player.HP = 0
	if _, _, err := validateActionLocked(&state, Action{Kind: ActionAllocateStat}); err == nil {
		t.Fatal("dead character accepted")
	}
	session, _ := worldTestSession(t)
	epoch := session.state.statPointsEpoch
	session.applyEvent(statPointsEvent(3))
	session.invalidateSubmittedStatPoints(Action{Kind: ActionAllocateStat}, epoch)
	if !session.Snapshot().Player.StatPointsKnown {
		t.Fatal("new server point response discarded")
	}
}
