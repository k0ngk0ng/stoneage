package aigame

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/server/go/namedproto"
)

func stringEvent(function, value string) Event {
	return Event{Function: function, Fields: []Field{{Kind: FieldString, Text: []byte(value)}}, At: time.Now()}
}

func TestStateProjectionAndPetIdentityEpoch(t *testing.T) {
	client, peer := net.Pipe()
	session := NewSession(client, Config{})
	defer session.Close()

	session.applyEvent(stringEvent("CharLogin", "successful"))
	session.applyEvent(stringEvent("S", "C100|30|30|4|5"))
	p1 := "P1|10|20|3|4|5|6|7|8|9|10|11|12|13|14|15|16|17|18|19|20|1000|2|3|4|5|6|7|8|Hero|称号"
	session.applyEvent(stringEvent("S", p1))
	session.applyEvent(stringEvent("S", "K0|1|100|20|30|5|10|100|200|3|4|5|6|0|0|0|0|0|0|0|0|狼|小狼"))
	first := session.Snapshot()
	if first.Phase != PhaseWorld || first.Position.Floor != 100 || first.Position.X != 4 || first.Position.Y != 5 {
		t.Fatalf("position/phase: %+v", first)
	}
	if first.Player.Gold != 1000 || first.Player.Name != "Hero" || first.Player.Title != "称号" {
		t.Fatalf("player status: %+v", first.Player)
	}
	if len(first.Pets) != 1 || first.Pets[0].Identity == "" || first.Pets[0].IdentityKnown {
		t.Fatalf("pet identity: %+v", first.Pets)
	}
	identity := first.Pets[0].Identity
	// A masked update keeps the continuity identity.
	session.applyEvent(stringEvent("S", "K0|4|25"))
	second := session.Snapshot()
	if len(second.Pets) != 1 || second.Pets[0].Identity != identity || second.Pets[0].HP != 25 {
		t.Fatalf("pet update: %+v", second.Pets)
	}
	// Empty then reuse creates a new session-local epoch.
	session.applyEvent(stringEvent("S", "K0|0"))
	session.applyEvent(stringEvent("S", "K0|1|101|21|31|5|10|100|200|3|4|5|6|0|0|0|0|0|0|0|狼2|小狼2"))
	third := session.Snapshot()
	if len(third.Pets) != 1 || third.Pets[0].Identity == identity || third.Pets[0].IdentityEpoch <= second.Pets[0].IdentityEpoch {
		t.Fatalf("pet reuse identity: %+v", third.Pets)
	}
	_ = peer.Close()
}

func TestAIObservationProjectionIsValidatedAndCopied(t *testing.T) {
	client, peer := net.Pipe()
	session := NewSession(client, Config{})
	defer session.Close()
	defer peer.Close()

	session.applyEvent(stringEvent("S", "AI|v=1|chara=17|pet=1,stable\\cpet\\z1,42|pet=0,pet-zero,8|end=1,2,3,4,5,6|now=6,5,4,3,2,1|ride=80|sp=0"))
	first := session.Snapshot()
	if !first.AI.Received || first.AI.Version != 1 || first.AI.CharacterIndex != 17 ||
		first.AI.LearnRide != 80 || !first.AI.SavePointsKnown || first.AI.SavePoints != 0 ||
		first.AIObservationRevision != first.Revision {
		t.Fatalf("AI observation metadata: %+v snapshot=%+v", first.AI, first)
	}
	if len(first.AI.Pets) != 2 || first.AI.Pets[0].Slot != 0 || first.AI.Pets[1].Slot != 1 ||
		first.AI.Pets[1].StableID != "stable,pet|1" || !first.AI.Pets[1].IdentityKnown || first.AI.Pets[1].Level != 42 {
		t.Fatalf("AI observation pets: %+v", first.AI.Pets)
	}
	if first.AI.EndEvents != [AIObservationEventGroups]int32{1, 2, 3, 4, 5, 6} ||
		first.AI.NowEvents != [AIObservationEventGroups]int32{6, 5, 4, 3, 2, 1} {
		t.Fatalf("AI observation events: %+v", first.AI)
	}

	first.AI.Pets[0].StableID = "changed"
	if current := session.Snapshot(); current.AI.Pets[0].StableID == "changed" {
		t.Fatal("AI observation pets alias mutable session state")
	}

	// A malformed extension packet must leave the last complete observation
	// intact instead of applying a partially parsed response.
	session.applyEvent(stringEvent("S", "AI|v=1|chara=17|end=1,2,3|now=6,5,4,3,2,1|ride=0"))
	current := session.Snapshot()
	if current.AI.LearnRide != 80 || current.AIObservationRevision != first.AIObservationRevision {
		t.Fatalf("malformed AI observation replaced valid state: %+v", current)
	}
}

func TestAIObservationSavePointsOptionalAndStrict(t *testing.T) {
	base := []string{"v=1", "chara=17", "end=0,0,0,0,0,0", "now=0,0,0,0,0,0", "ride=0"}
	with := func(extra ...string) []string {
		fields := append([]string(nil), base...)
		return append(fields, extra...)
	}
	tests := []struct {
		name      string
		fields    []string
		want      int32
		known     bool
		wantValid bool
	}{
		{name: "missing is compatible", fields: with(), wantValid: true},
		{name: "zero is known", fields: with("sp=0"), want: 0, known: true, wantValid: true},
		{name: "signed bit31 is known", fields: with("sp=-2147483648"), want: -2147483648, known: true, wantValid: true},
		{name: "empty is invalid", fields: with("sp="), wantValid: false},
		{name: "duplicate is invalid", fields: with("sp=1", "sp=2"), wantValid: false},
		{name: "above int32 is invalid", fields: with("sp=2147483648"), wantValid: false},
		{name: "below int32 is invalid", fields: with("sp=-2147483649"), wantValid: false},
		{name: "text is invalid", fields: with("sp=money"), wantValid: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observation, ok := parseAIObservation(test.fields)
			if ok != test.wantValid {
				t.Fatalf("parse validity = %v, want %v: %+v", ok, test.wantValid, observation)
			}
			if !ok {
				return
			}
			if observation.SavePoints != test.want || observation.SavePointsKnown != test.known {
				t.Fatalf("save points = %d/%v, want %d/%v", observation.SavePoints, observation.SavePointsKnown, test.want, test.known)
			}
		})
	}
}

func TestAIObservationSavePointsCompatibilityAndInvalidFrameHandling(t *testing.T) {
	client, peer := net.Pipe()
	session := NewSession(client, Config{})
	defer session.Close()
	defer peer.Close()

	valid := "AI|v=1|chara=17|end=0,0,0,0,0,0|now=0,0,0,0,0,0|ride=0"
	session.applyEvent(stringEvent("S", valid+"|sp=-2147483648"))
	known := session.Snapshot()
	if !known.AI.SavePointsKnown || known.AI.SavePoints != -2147483648 {
		t.Fatalf("signed save point was not retained: %+v", known.AI)
	}

	// A malformed extension packet must not discard the last complete value.
	session.applyEvent(stringEvent("S", valid+"|sp=2147483648"))
	invalid := session.Snapshot()
	if !invalid.AI.SavePointsKnown || invalid.AI.SavePoints != -2147483648 {
		t.Fatalf("invalid save point replaced valid state: %+v", invalid.AI)
	}

	// Older servers omit the optional field. A complete legacy response is
	// accepted and explicitly reports that save points are unknown.
	session.applyEvent(stringEvent("S", valid))
	legacy := session.Snapshot()
	if legacy.AI.SavePointsKnown || legacy.AI.SavePoints != 0 {
		t.Fatalf("legacy response did not clear optional save point knowledge: %+v", legacy.AI)
	}
}

func TestAIObservationItemsOptionalAndStrict(t *testing.T) {
	base := []string{"v=1", "chara=17", "end=0,0,0,0,0,0", "now=0,0,0,0,0,0", "ride=0"}
	with := func(extra ...string) []string {
		fields := append([]string(nil), base...)
		return append(fields, extra...)
	}
	tests := []struct {
		name      string
		fields    []string
		want      []AIInventoryItem
		known     bool
		wantValid bool
	}{
		{name: "missing is compatible", fields: with(), wantValid: true},
		{name: "none is known empty", fields: with("items=none"), want: []AIInventoryItem{}, known: true, wantValid: true},
		{name: "items are sorted by slot", fields: with("items=19,2414;5,2415"), want: []AIInventoryItem{{Slot: 5, TemplateID: 2415}, {Slot: 19, TemplateID: 2414}}, known: true, wantValid: true},
		{name: "duplicate slot is invalid", fields: with("items=5,2415;5,2414"), wantValid: false},
		{name: "equipment slot is invalid", fields: with("items=4,2415"), wantValid: false},
		{name: "past backpack slot is invalid", fields: with("items=20,2415"), wantValid: false},
		{name: "zero template is invalid", fields: with("items=5,0"), wantValid: false},
		{name: "negative template is invalid", fields: with("items=5,-1"), wantValid: false},
		{name: "out of range template is invalid", fields: with("items=5,2147483648"), wantValid: false},
		{name: "malformed record is invalid", fields: with("items=5,2415;"), wantValid: false},
		{name: "duplicate field is invalid", fields: with("items=5,2415", "items=6,2414"), wantValid: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observation, ok := parseAIObservation(test.fields)
			if ok != test.wantValid {
				t.Fatalf("parse validity = %v, want %v: %+v", ok, test.wantValid, observation)
			}
			if !ok {
				return
			}
			if observation.ItemsKnown != test.known || !reflect.DeepEqual(observation.Items, test.want) {
				t.Fatalf("items = %#v/%v, want %#v/%v", observation.Items, observation.ItemsKnown, test.want, test.known)
			}
		})
	}
}

func TestAIObservationItemsStateClearsOnLegacyFrameAndCopies(t *testing.T) {
	client, peer := net.Pipe()
	session := NewSession(client, Config{})
	defer session.Close()
	defer peer.Close()

	base := "AI|v=1|chara=17|end=0,0,0,0,0,0|now=0,0,0,0,0,0|ride=0"
	session.applyEvent(stringEvent("S", base+"|items=5,2415;6,2414"))
	first := session.Snapshot()
	if !first.AI.ItemsKnown || !reflect.DeepEqual(first.AI.Items, []AIInventoryItem{{Slot: 5, TemplateID: 2415}, {Slot: 6, TemplateID: 2414}}) {
		t.Fatalf("items were not retained: %+v", first.AI)
	}
	first.AI.Items[0].TemplateID = 9999
	if current := session.Snapshot(); current.AI.Items[0].TemplateID != 2415 {
		t.Fatal("AI item observation aliases mutable session state")
	}

	session.applyEvent(stringEvent("S", base+"|items=5,0"))
	invalid := session.Snapshot()
	if !invalid.AI.ItemsKnown || len(invalid.AI.Items) != 2 || invalid.AI.Items[0].TemplateID != 2415 {
		t.Fatalf("invalid item observation replaced valid state: %+v", invalid.AI)
	}

	// A complete response from an older bridge has no items field. It is
	// accepted but must clear the previous known list instead of resurrecting
	// stale item requirements.
	session.applyEvent(stringEvent("S", base))
	legacy := session.Snapshot()
	if legacy.AI.ItemsKnown || len(legacy.AI.Items) != 0 {
		t.Fatalf("legacy response did not clear optional items: %+v", legacy.AI)
	}
}

func TestAIStatusRequestWritesNamedPacket(t *testing.T) {
	client, peer := net.Pipe()
	session := NewSession(client, Config{})
	defer session.Close()
	defer peer.Close()
	session.stateMu.Lock()
	session.state.snapshot.Phase = PhaseWorld
	session.stateMu.Unlock()

	result := make(chan error, 1)
	go func() {
		result <- session.Execute(context.Background(), Action{Kind: ActionStatus, Command: "AI"})
	}()
	if err := peer.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	packet := make([]byte, 4096)
	n, err := peer.Read(packet)
	if err != nil {
		t.Fatal(err)
	}
	event, err := decodeEvent(packet[:n])
	if err != nil {
		t.Fatal(err)
	}
	if event.Function != "S" || len(event.Fields) != 1 || event.Fields[0].String() != "AI" {
		t.Fatalf("AI status request wire event: %+v", event)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}

func TestAIObservationMergesStablePetIntoSnapshotAndKPartialKeepsIt(t *testing.T) {
	client, peer := net.Pipe()
	session := NewSession(client, Config{})
	defer session.Close()
	defer peer.Close()

	// AI own-state can be the first authoritative pet packet; Snapshot.Pets
	// must be useful even when no K status has arrived yet.
	session.applyEvent(stringEvent("S", "AI|v=1|chara=17|pet=0,stable-pet,10|end=0,0,0,0,0,0|now=0,0,0,0,0,0|ride=0"))
	first := session.Snapshot()
	if len(first.Pets) != 1 || !first.Pets[0].IdentityKnown || first.Pets[0].StableID != "stable-pet" || first.Pets[0].Level != 10 {
		t.Fatalf("AI pet was not merged into Snapshot.Pets: %+v", first.Pets)
	}
	identity := first.Pets[0].Identity

	// K's masked update is a continuity-preserving update. Include HP and
	// level to ensure the mutable level comes from the newest K packet.
	mask := namedproto.EncodeInt(4 | 256) // HP, Level
	session.applyEvent(stringEvent("S", fmt.Sprintf("K0|%s|25|11", mask)))
	updated := session.Snapshot()
	if len(updated.Pets) != 1 || updated.Pets[0].Identity != identity ||
		updated.Pets[0].StableID != "stable-pet" || updated.Pets[0].Level != 11 || updated.Pets[0].HP != 25 {
		t.Fatalf("K partial update lost AI identity or level: %+v", updated.Pets)
	}
}

func TestAIObservationAndKReplacementDoNotReuseOldPetIdentity(t *testing.T) {
	client, peer := net.Pipe()
	session := NewSession(client, Config{})
	defer session.Close()
	defer peer.Close()

	session.applyEvent(stringEvent("S", "AI|v=1|chara=17|pet=0,old-pet,10|end=0,0,0,0,0,0|now=0,0,0,0,0,0|ride=0"))
	old := session.Snapshot()
	oldIdentity := old.Pets[0].Identity
	session.applyEvent(stringEvent("S", "K0|0"))
	if current := session.Snapshot(); len(current.Pets) != 0 {
		t.Fatalf("K clear retained old pet: %+v", current.Pets)
	}

	// A full K record has no stable ID and may represent a reused slot. It
	// starts a fresh unknown identity until a later AI response confirms it.
	session.applyEvent(stringEvent("S", "K0|1|100|20|30|5|10|100|200|3|4|5|6|0|0|0|0|0|0|0|0|Pet|Owner"))
	current := session.Snapshot()
	if len(current.Pets) != 1 || current.Pets[0].Identity == oldIdentity || current.Pets[0].IdentityKnown || current.Pets[0].StableID != "" {
		t.Fatalf("K replacement reused old identity: %+v old=%s", current.Pets, oldIdentity)
	}
	if len(current.AI.Pets) != 1 || current.AI.Pets[0].StableID != "old-pet" {
		t.Fatalf("latest raw AI observation was rewritten by K: %+v", current.AI.Pets)
	}
}

func TestAIObservationUnknownIdentityDoesNotInventStablePetID(t *testing.T) {
	client, peer := net.Pipe()
	session := NewSession(client, Config{})
	defer session.Close()
	defer peer.Close()

	session.applyEvent(stringEvent("S", "AI|v=1|chara=17|pet=0,unknown,9|end=0,0,0,0,0,0|now=0,0,0,0,0,0|ride=0"))
	current := session.Snapshot()
	if len(current.Pets) != 1 || current.Pets[0].IdentityKnown || current.Pets[0].StableID != "" || current.Pets[0].Level != 9 {
		t.Fatalf("unknown AI identity was exposed as stable: %+v", current.Pets)
	}

	// A partial K level update remains on the same unidentified local pet.
	mask := namedproto.EncodeInt(256)
	identity := current.Pets[0].Identity
	session.applyEvent(stringEvent("S", fmt.Sprintf("K0|%s|12", mask)))
	updated := session.Snapshot()
	if len(updated.Pets) != 1 || updated.Pets[0].Identity != identity || updated.Pets[0].IdentityKnown || updated.Pets[0].Level != 12 {
		t.Fatalf("unknown pet K update changed continuity: %+v", updated.Pets)
	}
}

func TestAIObservationEmptyPetListClearsOwnedSnapshotPets(t *testing.T) {
	client, peer := net.Pipe()
	session := NewSession(client, Config{})
	defer session.Close()
	defer peer.Close()

	session.applyEvent(stringEvent("S", "AI|v=1|chara=17|pet=0,old-pet,10|end=0,0,0,0,0,0|now=0,0,0,0,0,0|ride=0"))
	session.applyEvent(stringEvent("S", "AI|v=1|chara=17|end=0,0,0,0,0,0|now=0,0,0,0,0,0|ride=0"))
	current := session.Snapshot()
	if len(current.Pets) != 0 || len(current.AI.Pets) != 0 {
		t.Fatalf("empty AI own-state did not clear owned pet: pets=%+v ai=%+v", current.Pets, current.AI.Pets)
	}
}

func TestAIObservationPreservesKDetailsOnlyWhenIdentityIsUnproven(t *testing.T) {
	client, peer := net.Pipe()
	session := NewSession(client, Config{})
	defer session.Close()
	defer peer.Close()

	// Full K creates a fresh unidentified local pet with mutable state.
	session.applyEvent(stringEvent("S", "K0|1|100|20|30|5|10|100|200|3|4|5|6|0|0|0|0|0|0|0|0|Pet|Owner"))
	session.stateMu.Lock()
	pet, ok := session.state.petForSlot(0)
	if !ok {
		session.stateMu.Unlock()
		t.Fatal("full K did not create pet")
	}
	pet.Skills = []PetSkillSnapshot{{ID: 9001, Name: "attack"}}
	session.state.pets[pet.Identity] = pet
	session.stateMu.Unlock()

	session.applyEvent(stringEvent("S", "AI|v=1|chara=17|pet=0,confirmed-pet,12|end=0,0,0,0,0,0|now=0,0,0,0,0,0|ride=0"))
	confirmed := session.Snapshot()
	if len(confirmed.Pets) != 1 || !confirmed.Pets[0].IdentityKnown || confirmed.Pets[0].StableID != "confirmed-pet" ||
		confirmed.Pets[0].HP != 20 || len(confirmed.Pets[0].Skills) != 1 || confirmed.Pets[0].Skills[0].ID != 9001 {
		t.Fatalf("AI confirmation did not preserve unproven K details: %+v", confirmed.Pets)
	}
	oldIdentity := confirmed.Pets[0].Identity

	// A changed stable ID proves slot reuse; no mutable state may cross the
	// identity boundary.
	session.applyEvent(stringEvent("S", "AI|v=1|chara=17|pet=0,replacement-pet,3|end=0,0,0,0,0,0|now=0,0,0,0,0,0|ride=0"))
	replacement := session.Snapshot()
	if len(replacement.Pets) != 1 || replacement.Pets[0].Identity == oldIdentity || replacement.Pets[0].StableID != "replacement-pet" ||
		replacement.Pets[0].HP != 0 || len(replacement.Pets[0].Skills) != 0 {
		t.Fatalf("replacement AI identity inherited old mutable state: %+v", replacement.Pets)
	}
}

func TestBattleCommandReadinessFollowsBPBCMovieOrdering(t *testing.T) {
	state := newGameState(true)
	state.snapshot.Phase = PhaseWorld
	applyEventLocked(&state, Event{Function: "EN", Fields: []Field{{Kind: FieldInt, Int: 1}, {Kind: FieldInt, Int: 218}}, At: time.Now()})
	if !state.snapshot.Battle.Active || state.snapshot.Phase != PhaseBattle {
		t.Fatalf("battle did not start: %+v", state.snapshot.Battle)
	}
	applyEventLocked(&state, stringEvent("B", "BP|0|2|10"))
	if state.snapshot.Battle.CommandReady {
		t.Fatal("BP without BC opened command menu")
	}
	applyEventLocked(&state, stringEvent("B", "BC|0|0|enemy||100|1|10|10|0|0|0|0"))
	if !state.snapshot.Battle.CommandReady || len(state.snapshot.Battle.Participants) != 1 {
		t.Fatalf("BC did not release command: %+v", state.snapshot.Battle)
	}
	applyEventLocked(&state, stringEvent("B", "BVS|0|10|64|"))
	if !state.snapshot.Battle.CommandReady || state.snapshot.Battle.Movie {
		t.Fatal("display snapshot incorrectly changed battle readiness")
	}
	applyEventLocked(&state, stringEvent("B", "BA|1|2"))
	if !state.snapshot.Battle.CommandReady {
		t.Fatal("BA incorrectly locked command")
	}
	applyEventLocked(&state, stringEvent("B", "BV|0|1|"))
	if !state.snapshot.Battle.Movie {
		t.Fatal("legacy attribute-change movie must remain a movie")
	}
	applyEventLocked(&state, stringEvent("B", "H|0"))
	if state.snapshot.Battle.CommandReady {
		t.Fatal("movie command did not lock menu")
	}
	applyEventLocked(&state, stringEvent("B", "BP|0|0|10"))
	if !state.snapshot.Battle.CommandReady {
		t.Fatal("next BP did not reopen command")
	}
	applyEventLocked(&state, stringEvent("B", "BU"))
	if state.snapshot.Phase != PhaseWorld || state.snapshot.Battle.Active || state.snapshot.Battle.CommandReady {
		t.Fatalf("battle did not end: phase=%s battle=%+v", state.snapshot.Phase, state.snapshot.Battle)
	}
}

func TestSnapshotIsDeepCopy(t *testing.T) {
	client, peer := net.Pipe()
	session := NewSession(client, Config{})
	defer session.Close()
	session.stateMu.Lock()
	session.state.snapshot.Phase = PhaseWorld
	session.state.snapshot.Position = Point{Floor: 1, X: 2, Y: 3}
	session.state.characters = []Character{{Slot: 0, Name: "one"}}
	session.state.inventory[0] = InventoryItem{Index: 0, Name: "item"}
	session.state.chat = []ChatMessage{{Text: "hello"}}
	session.state.snapshot.Battle.Participants = []BattleParticipant{{BattleID: 0, Name: "enemy"}}
	session.state.pets["pet"] = PetSnapshot{Slot: 0, Identity: "pet", Skills: []PetSkillSnapshot{{ID: 1, Name: "attack"}}}
	session.stateMu.Unlock()

	copy := session.Snapshot()
	copy.Characters[0].Name = "changed"
	copy.Inventory[0].Name = "changed"
	copy.Chat[0].Text = "changed"
	copy.Battle.Participants[0].Name = "changed"
	copy.Pets[0].Skills[0].Name = "changed"
	copy.Position.X = 99
	current := session.Snapshot()
	if current.Characters[0].Name != "one" || current.Inventory[0].Name != "item" || current.Chat[0].Text != "hello" || current.Battle.Participants[0].Name != "enemy" || current.Pets[0].Skills[0].Name != "attack" || current.Position.X != 2 {
		t.Fatalf("snapshot aliases mutable state: %+v", current)
	}
	_ = peer.Close()
}

func TestDoValidatesPositionAndWritesOneNamedPacket(t *testing.T) {
	client, peer := net.Pipe()
	session := NewSession(client, Config{})
	defer session.Close()
	session.stateMu.Lock()
	session.state.snapshot.Phase = PhaseWorld
	session.state.snapshot.Position = Point{Floor: 1, X: 2, Y: 3}
	session.stateMu.Unlock()

	if err := session.Do(context.Background(), Move(1, 3, "a")); !errors.Is(err, ErrInvalidAction) {
		t.Fatalf("wrong-position move error = %v", err)
	}
	result := make(chan error, 1)
	go func() { result <- session.Do(context.Background(), Move(2, 3, "ab")) }()
	packet := make([]byte, 4096)
	_ = peer.SetReadDeadline(time.Now().Add(time.Second))
	n, err := peer.Read(packet)
	if err != nil {
		t.Fatal(err)
	}
	message, err := decodeEvent(packet[:n])
	if err != nil || message.Function != "W" || len(message.Fields) != 3 || message.Fields[2].String() != "ab" {
		t.Fatalf("wire action: event=%+v err=%v", message, err)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	_ = peer.Close()
}

func TestMoveRejectsRouteLongerThanServerLimitWithoutWriting(t *testing.T) {
	client, peer := net.Pipe()
	session := NewSession(client, Config{})
	defer session.Close()
	defer peer.Close()
	session.stateMu.Lock()
	session.state.snapshot.Phase = PhaseWorld
	session.state.snapshot.Position = Point{Floor: 1, X: 2, Y: 3}
	session.stateMu.Unlock()

	route := strings.Repeat("a", maxRouteLength+1)
	err := session.Do(context.Background(), Move(2, 3, route))
	if !errors.Is(err, ErrInvalidAction) || !strings.Contains(err.Error(), "32") {
		t.Fatalf("overlong route error = %v", err)
	}
	if err := peer.SetReadDeadline(time.Now().Add(50 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if _, err := peer.Read(make([]byte, 4096)); !isTimeout(err) {
		t.Fatalf("overlong route wrote packet: %v", err)
	}
}

func TestLookWritesNativeDirectionPacket(t *testing.T) {
	client, peer := net.Pipe()
	session := NewSession(client, Config{})
	defer session.Close()
	defer peer.Close()
	session.stateMu.Lock()
	session.state.snapshot.Phase = PhaseWorld
	session.state.snapshot.Position = Point{Floor: 1, X: 2, Y: 3}
	session.stateMu.Unlock()

	result := make(chan error, 1)
	go func() { result <- session.Do(context.Background(), Look(4)) }()
	packet := make([]byte, 4096)
	if err := peer.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	n, err := peer.Read(packet)
	if err != nil {
		t.Fatal(err)
	}
	event, err := decodeEvent(packet[:n])
	if err != nil {
		t.Fatal(err)
	}
	if event.Function != "L" || len(event.Fields) != 1 || event.Fields[0].Raw != namedproto.EncodeInt(4) {
		t.Fatalf("wire look action: %+v", event)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if got := session.Snapshot(); got.LastFunction != "L" || got.Position.Direction != 0 {
		t.Fatalf("look must not synthesize authoritative position: %+v", got)
	}
}

func TestFieldActorDirectionsKeepNativeWireValues(t *testing.T) {
	for direction := int32(0); direction <= 7; direction++ {
		t.Run(fmt.Sprintf("direction-%d", direction), func(t *testing.T) {
			state := newGameState(true)
			state.snapshot.Character = "Hero"

			id := namedproto.EncodeInt(10)
			c := fmt.Sprintf("1|%s|10|20|%d|100|1|0|Hero||||||0|0", id, direction)
			applyEventLocked(&state, stringEvent("C", c))
			actor, ok := state.actors[10]
			if !ok || actor.Direction != direction {
				t.Fatalf("C direction = %+v, want %d", actor, direction)
			}
			if state.snapshot.Player.ID != 10 || state.snapshot.Position.Direction != direction {
				t.Fatalf("C owner direction = %+v, want %d", state.snapshot.Position, direction)
			}

			ca := fmt.Sprintf("%s|11|20|3|%d", id, direction)
			applyEventLocked(&state, stringEvent("CA", ca))
			actor = state.actors[10]
			if actor.Direction != direction {
				t.Fatalf("CA direction = %d, want %d", actor.Direction, direction)
			}
			if state.snapshot.Position.Direction != direction {
				t.Fatalf("CA owner direction = %d, want %d", state.snapshot.Position.Direction, direction)
			}

			applyEventLocked(&state, Event{Function: "PME", Fields: []Field{
				{Kind: FieldInt, Int: 20}, {Kind: FieldInt, Int: 200},
				{Kind: FieldInt, Int: 12}, {Kind: FieldInt, Int: 21},
				{Kind: FieldInt, Int: direction}, {Kind: FieldInt, Int: 4},
				{Kind: FieldInt, Int: 0},
			}, At: time.Now()})
			pet, ok := state.actors[20]
			if !ok || pet.Direction != direction {
				t.Fatalf("PME direction = %+v, want %d", pet, direction)
			}
		})
	}
}

func TestLoginAndEnterUseNamedHandshake(t *testing.T) {
	client, peer := net.Pipe()
	serverDone := make(chan error, 1)
	go func() {
		defer peer.Close()
		if _, err := peer.Write([]byte{'L', 0}); err != nil {
			serverDone <- err
			return
		}
		reader := make([]byte, 8192)
		readMessage := func() (namedproto.Message, error) {
			n, err := peer.Read(reader)
			if err != nil {
				return namedproto.Message{}, err
			}
			raw, err := namedproto.DecodePacket(reader[:n])
			if err != nil {
				return namedproto.Message{}, err
			}
			return namedproto.ParseMessage(raw)
		}
		writeResponse := func(id uint32, function string, fields []string) error {
			raw, err := namedproto.RawMessage(id, function, fields)
			if err != nil {
				return err
			}
			packet, err := namedproto.EncodePacket(raw)
			if err != nil {
				return err
			}
			_, err = peer.Write(packet)
			return err
		}
		message, err := readMessage()
		if err != nil {
			serverDone <- err
			return
		}
		if message.Function != "ClientLogin" || len(message.Fields) != 2 {
			serverDone <- fmt.Errorf("login request: %+v", message)
			return
		}
		if err := writeResponse(message.ID, "ClientLogin", []string{namedproto.EncodeString([]byte("ok"))}); err != nil {
			serverDone <- err
			return
		}
		message, err = readMessage()
		if err != nil {
			serverDone <- err
			return
		}
		if message.Function != "CharList" || len(message.Fields) != 0 {
			serverDone <- fmt.Errorf("char list request: %+v", message)
			return
		}
		list := namedproto.EncodeString([]byte("Hero|0\\z0\\z1\\z10\\z100\\z20\\z30\\z4\\z0\\z50\\z50\\z50\\z50\\z0\\zHero\\zhome"))
		if err := writeResponse(message.ID, "CharList", []string{namedproto.EncodeString([]byte("successful")), list}); err != nil {
			serverDone <- err
			return
		}
		message, err = readMessage()
		if err != nil {
			serverDone <- err
			return
		}
		if message.Function != "CharLogin" || len(message.Fields) != 1 {
			serverDone <- fmt.Errorf("char login request: %+v", message)
			return
		}
		if err := writeResponse(message.ID, "CharLogin", []string{namedproto.EncodeString([]byte("successful")), namedproto.EncodeString(nil)}); err != nil {
			serverDone <- err
			return
		}
		serverDone <- nil
	}()

	config := Config{Address: "pipe", Dial: func(context.Context, string) (net.Conn, error) { return client, nil }}
	session, err := Login(context.Background(), config, Credentials{Account: "acct", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if got := session.Snapshot(); got.Phase != PhaseCharacterList || got.Account != "acct" {
		t.Fatalf("login snapshot: %+v", got)
	}
	if chars := session.Characters(); len(chars) != 1 || chars[0].Name != "Hero" {
		t.Fatalf("character list: %+v", chars)
	}
	if err := session.Enter(context.Background(), "Hero"); err != nil {
		t.Fatal(err)
	}
	if got := session.Snapshot(); got.Phase != PhaseWorld || got.Character != "Hero" {
		t.Fatalf("world snapshot: %+v", got)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
	_ = session.Close()
}

func TestAuthenticateContextCancellationClosesSocket(t *testing.T) {
	client, peer := net.Pipe()
	session := NewSession(client, Config{})
	defer peer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := session.Authenticate(ctx, Credentials{Account: "acct", Password: "secret"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled authenticate: %v", err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = peer.Read(make([]byte, 1))
	if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
		// net.Pipe can report ErrClosed rather than EOF depending on which
		// endpoint observes the close first; both prove the cancellation closed
		// the connection and avoided a leaked blocking read.
		t.Fatalf("peer remained open: %v", err)
	}
}
