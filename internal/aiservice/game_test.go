package aiservice

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aicontrol"
	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
)

type fakeGame struct {
	snapshot    aigame.Snapshot
	writes      int
	writeError  error
	beforeWrite func()
}

func (f *fakeGame) Observe(context.Context) (aigame.Snapshot, error) { return f.snapshot, nil }
func (f *fakeGame) ExecuteExpected(_ context.Context, revision uint64, _ aigame.Action) error {
	if f.beforeWrite != nil {
		f.beforeWrite()
	}
	if f.snapshot.Revision != revision {
		return errors.New("stale")
	}
	f.writes++
	return f.writeError
}
func gameFixture(t *testing.T) (*GameBackend, *fakeGame) {
	t.Helper()
	g := aicontrol.New()
	s, _, err := g.Switch(1, aicontrol.Agent, "")
	if err != nil {
		t.Fatal(err)
	}
	r, err := OpenReceiptStore(filepath.Join(t.TempDir(), "receipts.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close(); g.Close() })
	f := &fakeGame{snapshot: aigame.Snapshot{Account: "account", Character: "character", Revision: 12, Connected: true, Phase: aigame.PhaseWorld, Player: aigame.PlayerSnapshot{HasStatus: true, HP: 10}}}
	b := &GameBackend{Binding: aimcp.Binding{AccountID: "account", CharacterID: "account:0", CharacterName: "character", Generation: s.Generation}, Gate: g, Owner: aicontrol.Agent, Session: f, Receipts: r}
	return b, f
}
func TestGameActionNeverTreatsWriteAsSuccess(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "written", true: "uncertain"}[fail], func(t *testing.T) {
			b, f := gameFixture(t)
			if fail {
				f.writeError = errors.New("short write")
			}
			r, err := b.GameAction(context.Background(), b.Binding, aimcp.TypedAction{Kind: "chat", Text: "hello", ExpectedRevision: 12})
			if err != nil || r.Status != aimcp.ReceiptUnknown || r.Handle == "" || f.writes != 1 {
				t.Fatalf("receipt=%+v err=%v writes=%d", r, err, f.writes)
			}
			loaded, err := b.TaskStatus(context.Background(), b.Binding, r.Handle)
			if err != nil || loaded.Status != aimcp.ReceiptUnknown || f.writes != 1 {
				t.Fatalf("poll replayed or changed unknown outcome: %+v %v", loaded, err)
			}
		})
	}
}

func TestRepeatedToolCallReturnsSameReceiptWithoutAnotherWrite(t *testing.T) {
	b, f := gameFixture(t)
	a := aimcp.TypedAction{Kind: "chat", Text: "hello", ExpectedRevision: 12}
	first, err := b.GameAction(context.Background(), b.Binding, a)
	if err != nil {
		t.Fatal(err)
	}
	again, err := b.GameAction(context.Background(), b.Binding, a)
	if err != nil || again.Handle != first.Handle || f.writes != 1 {
		t.Fatalf("duplicate tool action: first=%+v again=%+v writes=%d err=%v", first, again, f.writes, err)
	}
}
func TestBackendRejectsTakeoverAndRebinding(t *testing.T) {
	b, f := gameFixture(t)
	other := b.Binding
	other.CharacterID = "account:1"
	if _, err := b.Observe(context.Background(), other); err == nil {
		t.Fatal("allowed rebind")
	}
	if _, err := b.Gate.Takeover("manual"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.GameAction(context.Background(), b.Binding, aimcp.TypedAction{Kind: "chat", Text: "hello", ExpectedRevision: 12}); !errors.Is(err, aicontrol.ErrStale) {
		t.Fatalf("expected stale: %v", err)
	}
	if f.writes != 0 {
		t.Fatal("wrote after takeover")
	}
}
func TestBackendRechecksRevisionAtSubmission(t *testing.T) {
	b, f := gameFixture(t)
	f.beforeWrite = func() { f.snapshot.Revision++ }
	r, err := b.GameAction(context.Background(), b.Binding, aimcp.TypedAction{Kind: "chat", Text: "hello", ExpectedRevision: 12})
	if err != nil || r.Status != aimcp.ReceiptUnknown || f.writes != 0 {
		t.Fatalf("stale write: %+v %v %d", r, err, f.writes)
	}
}
func TestReceiptSurvivesRestartAndScopesCharacter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "receipts.db")
	s, err := OpenReceiptStore(path)
	if err != nil {
		t.Fatal(err)
	}
	b := aimcp.Binding{AccountID: "a", CharacterID: "a:0", Generation: 2}
	r, err := s.Prepare(context.Background(), b, aimcp.TypedAction{Kind: "item", ExpectedRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = OpenReceiptStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	b.Generation++ // a fresh lease may reconcile an older operation, never replay it
	if got, err := s.Load(context.Background(), b, r.Handle); err != nil || got.Status != aimcp.ReceiptUnknown {
		t.Fatalf("lost unknown action: %+v %v", got, err)
	}
	b.CharacterID = "a:1"
	if _, err := s.Load(context.Background(), b, r.Handle); err == nil {
		t.Fatal("cross-character receipt")
	}
}
func TestObservationDoesNotInventPetIdentity(t *testing.T) {
	b, f := gameFixture(t)
	f.snapshot.Pets = []aigame.PetSnapshot{{Slot: 0, StableID: "slot-zero", IdentityKnown: false}, {Slot: 1, StableID: "real-unique-code", IdentityKnown: true}}
	f.snapshot.Battle.Participants = []aigame.BattleParticipant{{BattleID: 10, Name: "enemy", HP: 5, MaxHP: 10}}
	o, err := b.Observe(context.Background(), b.Binding)
	if err != nil || o.Pets[0].ID != "" || o.Pets[1].ID != "real-unique-code" || len(o.Battle.Participants) != 1 {
		t.Fatalf("incorrect projection: %+v %v", o, err)
	}
}

func TestObservationProjectsCurrentFundingCapability(t *testing.T) {
	b, _ := gameFixture(t)
	allowed := true
	b.Funding = func(context.Context) (bool, error) { return allowed, nil }
	o, err := b.Observe(context.Background(), b.Binding)
	if err != nil || !o.UnlimitedFunds {
		t.Fatalf("funded observation = %+v, err=%v", o, err)
	}
	allowed = false
	o, err = b.Observe(context.Background(), b.Binding)
	if err != nil || o.UnlimitedFunds {
		t.Fatalf("revoked observation = %+v, err=%v", o, err)
	}
}

func TestObservationProjectsAIProgressSkillsAndPetIdentity(t *testing.T) {
	b, f := gameFixture(t)
	f.snapshot.Skills = []aigame.SkillSnapshot{{ID: 101, Level: 4}}
	f.snapshot.Pets = []aigame.PetSnapshot{
		{Slot: 0, Name: "pet", StableID: "pet-serial", IdentityKnown: true, Level: 9, HP: 20, MaxHP: 30, Alive: true, Skills: []aigame.PetSkillSnapshot{{ID: 9001}}},
		{Slot: 1, Level: 2, Alive: true},
	}
	f.snapshot.AI = aigame.AIObservation{
		Received:  true,
		LearnRide: 80,
		EndEvents: [aigame.AIObservationEventGroups]int32{1, 0, 3, 0, 0, 0},
		NowEvents: [aigame.AIObservationEventGroups]int32{0, 2, 0, 0, 0, 0},
		Pets: []aigame.PetSnapshot{
			{Slot: 0, StableID: "pet-serial", IdentityKnown: true, Level: 8, Skills: []aigame.PetSkillSnapshot{{ID: 9001}}},
			{Slot: 1, Level: 2, Alive: true},
		},
	}
	o, err := b.Observe(context.Background(), b.Binding)
	if err != nil {
		t.Fatal(err)
	}
	if o.Skills["101"] != 4 || o.Skills["learn_ride"] != 80 {
		t.Fatalf("character skills were not projected: %+v", o.Skills)
	}
	if o.OwnProgress["end_event_0"] != 1 || o.OwnProgress["now_event_1"] != 2 || !o.Flags["end_event_0"] || !o.Flags["now_event_1"] || o.Flags["end_event_1"] {
		t.Fatalf("AI progress was not projected: own=%+v flags=%+v", o.OwnProgress, o.Flags)
	}
	if len(o.Pets) != 2 || o.Pets[0].ID != "pet-serial" || o.Pets[0].Level != 9 || len(o.Pets[0].Skills) != 1 || o.Pets[0].Skills[0] != "9001" {
		t.Fatalf("latest authoritative pet was not preserved: %+v", o.Pets)
	}
	if o.Pets[1].ID != "" || o.Pets[1].Level != 2 {
		t.Fatalf("unknown AI pet identity was exposed: %+v", o.Pets[1])
	}
}

func TestProjectionDoesNotResurrectOldPetIdentityFromAIReply(t *testing.T) {
	backend, fixture := gameFixture(t)
	fixture.snapshot.AI = aigame.AIObservation{Received: true, Pets: []aigame.PetSnapshot{{Slot: 0, IdentityKnown: true, StableID: "old-pet", Level: 90}}}
	fixture.snapshot.Pets = []aigame.PetSnapshot{{Slot: 0, Level: 1}}
	observed, err := backend.Observe(context.Background(), backend.Binding)
	if err != nil {
		t.Fatal(err)
	}
	if len(observed.Pets) != 1 || observed.Pets[0].ID != "" || observed.Pets[0].Level != 1 {
		t.Fatalf("old AI identity or level was applied to a replacement: %+v", observed.Pets)
	}
	fixture.snapshot.Pets = nil
	observed, err = backend.Observe(context.Background(), backend.Binding)
	if err != nil || len(observed.Pets) != 0 {
		t.Fatalf("old AI reply resurrected a removed pet: %+v err=%v", observed.Pets, err)
	}
}

func TestProjectionPreservesIndividualEventBitsAndKnownFalse(t *testing.T) {
	backend, fixture := gameFixture(t)
	fixture.snapshot.AI = aigame.AIObservation{Received: true,
		EndEvents: [aigame.AIObservationEventGroups]int32{1, 0, -2147483648},
		NowEvents: [aigame.AIObservationEventGroups]int32{0, 2},
	}
	observed, err := backend.Observe(context.Background(), backend.Binding)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"end:0", "end:95", "now:33"} {
		if !observed.Flags[key] {
			t.Fatalf("server event bit missing: %s", key)
		}
	}
	for _, key := range []string{"end:1", "end:32", "now:0", "end_event_1"} {
		if value, known := observed.Flags[key]; value || !known {
			t.Fatalf("server false bit lost: %s", key)
		}
	}
	fixture.snapshot.AI.Received = false
	observed, err = backend.Observe(context.Background(), backend.Binding)
	if err != nil || len(observed.Flags) != 0 {
		t.Fatalf("unreceived flags became evidence: %+v err=%v", observed.Flags, err)
	}
}
