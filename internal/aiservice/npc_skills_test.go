package aiservice

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aicontrol"
	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

type npcActionRecord struct {
	expected uint64
	action   aigame.Action
}

type npcSkillSession struct {
	mu              sync.Mutex
	snapshot        aigame.Snapshot
	actions         []npcActionRecord
	advance         bool
	updateDirection bool
	statusDirection int32
	err             error
}

func (s *npcSkillSession) Observe(context.Context) (aigame.Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshot, nil
}

func (s *npcSkillSession) ExecuteExpected(_ context.Context, expected uint64, action aigame.Action) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.snapshot.Revision != expected {
		return aigame.ErrStaleRevision
	}
	s.actions = append(s.actions, npcActionRecord{expected: expected, action: action})
	if action.Kind == aigame.ActionLook {
		s.snapshot.LastFunction = "L"
	} else if action.Kind == aigame.ActionStatus && action.Command == "c" {
		if s.updateDirection {
			s.snapshot.Position.Direction = s.statusDirection
		}
	} else if action.Kind == aigame.ActionTalk {
		s.snapshot.LastFunction = "TK"
	} else if action.Kind == aigame.ActionWindow {
		s.snapshot.LastFunction = "WN"
	}
	if s.advance {
		s.snapshot.Revision++
	}
	return s.err
}

func verifiedNPCSpec() NPCSpec {
	return NPCSpec{
		Alias: "trainer", Floor: 10, X: 5, Y: 6, Name: "Trainer", TalkRange: 1,
		WindowType: 7, WindowSequence: 100, WindowObjectID: 42,
		Choices: map[int]NPCChoice{
			1: {Button: 1, MaximumCost: 5000, Alias: "basic", Data: ""},
			2: {Button: 2, MaximumCost: 0, Alias: "leave"},
		},
		SourceFingerprint: "server-source-v1", Verified: true,
	}
}

func verifiedMultiWindowNPCSpec() NPCSpec {
	spec := verifiedNPCSpec()
	spec.Choices = map[int]NPCChoice{
		1: {Button: 1, MaximumCost: 0, Alias: "basic", Data: "course=basic"},
		2: {Button: 2, MaximumCost: 0, Alias: "leave"},
	}
	spec.Windows = []NPCWindowSpec{
		{Type: 7, Sequence: 100, ObjectID: 42, Choices: spec.Choices},
		{Type: 0, Sequence: 110, ObjectID: 43, Choices: map[int]NPCChoice{
			1: {Button: 2, MaximumCost: 5000, Alias: "confirm", Data: "course=basic"},
		}},
	}
	return spec
}

func verifiedRuntimeObjectNPCSpec() NPCSpec {
	spec := verifiedMultiWindowNPCSpec()
	// Use the preferred multi-window form alone so the legacy static contract
	// does not introduce a conflicting duplicate sequence.
	spec.WindowType = 0
	spec.WindowSequence = 0
	spec.WindowObjectID = 0
	spec.Choices = nil
	for index := range spec.Windows {
		spec.Windows[index].WindowObjectFromActor = true
		// This stale value must never be used when the server-owned mode is
		// enabled; the visible actor's current ID is authoritative.
		spec.Windows[index].ObjectID = 999
	}
	return spec
}

func npcSkillFixture(t *testing.T, advance bool) (*NPCSkill, *npcSkillSession, NPCSpec) {
	t.Helper()
	backend, _ := gameFixture(t)
	session := &npcSkillSession{snapshot: aigame.Snapshot{
		Account: "account", Character: "character", Revision: 12, Connected: true, Phase: aigame.PhaseWorld,
		Position: aigame.Point{Floor: 10, X: 4, Y: 6}, Player: aigame.PlayerSnapshot{HasStatus: true, HP: 10},
		Actors: []aigame.ActorSnapshot{{ID: 42, Kind: "character", CharType: 1, X: 5, Y: 6, Name: "Trainer"}},
	}, advance: advance}
	backend.Session = session
	spec := verifiedNPCSpec()
	registry, err := NewNPCRegistry([]NPCSpec{spec})
	if err != nil {
		t.Fatal(err)
	}
	return NewNPCSkill(backend, registry), session, spec
}

func TestNPCRegistryRequiresRuntimeVerificationAndFingerprint(t *testing.T) {
	base := verifiedNPCSpec()
	for _, test := range []struct {
		name string
		edit func(*NPCSpec)
	}{
		{name: "unverified", edit: func(spec *NPCSpec) { spec.Verified = false }},
		{name: "missing fingerprint", edit: func(spec *NPCSpec) { spec.SourceFingerprint = "" }},
		{name: "missing identity", edit: func(spec *NPCSpec) { spec.Name = ""; spec.Template = "" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			spec := base
			test.edit(&spec)
			if _, err := NewNPCRegistry([]NPCSpec{spec}); err == nil {
				t.Fatal("accepted an untrusted NPC registry entry")
			}
		})
	}

	skill, session, _ := npcSkillFixture(t, false)
	if err := skill.ValidateSkill(context.Background(), automation.Action{Skill: "npc.talk", Arguments: json.RawMessage(`{"npc":"riderman"}`)}); !errors.Is(err, ErrNPCUnknown) || session.actions != nil {
		t.Fatalf("unknown riderman alias was accepted: err=%v actions=%d", err, len(session.actions))
	}
}

func TestNPCTalkLooksThenTalksWithAuthoritativeStatusConfirmation(t *testing.T) {
	skill, session, _ := npcSkillFixture(t, true)
	session.updateDirection = true
	session.statusDirection = 2
	action := automation.Action{Skill: "npc.talk", ExpectedRevision: 12, MaximumCost: 0, Arguments: json.RawMessage(`{"npc":"trainer","floor":10,"x":5,"y":6,"command":"talk"}`)}
	if err := skill.Execute(context.Background(), action); err != nil {
		t.Fatal(err)
	}
	if len(session.actions) != 3 {
		t.Fatalf("actions=%+v", session.actions)
	}
	look, status, talk := session.actions[0], session.actions[1], session.actions[2]
	if look.action.Kind != aigame.ActionLook || look.action.Direction != 2 || look.expected != 12 {
		t.Fatalf("look action=%+v", look)
	}
	if status.action.Kind != aigame.ActionStatus || status.action.Command != "c" || status.expected != 13 {
		t.Fatalf("status action=%+v", status)
	}
	if talk.action.Kind != aigame.ActionTalk || talk.expected != 14 || talk.action.Command != "P|hi" || talk.action.Range != 3 || talk.action.Color != 0 {
		t.Fatalf("talk action=%+v", talk)
	}
}

func TestNPCTalkTimesOutWithoutAuthoritativeStatusConfirmation(t *testing.T) {
	skill, session, _ := npcSkillFixture(t, true)
	skill.LookConfirmationTimeout = 20 * time.Millisecond
	session.updateDirection = false
	session.statusDirection = 2
	action := automation.Action{Skill: "npc.talk", ExpectedRevision: 12, MaximumCost: 0, Arguments: json.RawMessage(`{"npc":"trainer"}`)}
	err := skill.Execute(context.Background(), action)
	if !errors.Is(err, ErrNPCLookUnconfirmed) {
		t.Fatalf("unconfirmed look err=%v", err)
	}
	if len(session.actions) != 2 {
		t.Fatalf("actions=%+v", session.actions)
	}
	if session.actions[0].action.Kind != aigame.ActionLook || session.actions[1].action.Kind != aigame.ActionStatus || session.actions[1].action.Command != "c" {
		t.Fatalf("actions=%+v", session.actions)
	}
	for _, record := range session.actions {
		if record.action.Kind == aigame.ActionTalk {
			t.Fatalf("sent TK after unconfirmed look: %+v", session.actions)
		}
	}
}

func TestNPCTalkRejectsInvisibleOutOfRangeAndStaleTargets(t *testing.T) {
	tests := []struct {
		name string
		edit func(*npcSkillSession)
		want error
	}{
		{name: "invisible identity", edit: func(session *npcSkillSession) { session.snapshot.Actors[0].Name = "Other" }, want: ErrNPCNotVisible},
		{name: "out of range", edit: func(session *npcSkillSession) { session.snapshot.Position.X = 1 }, want: ErrNPCOutOfRange},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			skill, session, _ := npcSkillFixture(t, false)
			test.edit(session)
			err := skill.Execute(context.Background(), automation.Action{Skill: "npc.talk", ExpectedRevision: 12, Arguments: json.RawMessage(`{"npc":"trainer"}`)})
			if !errors.Is(err, test.want) || len(session.actions) != 0 {
				t.Fatalf("err=%v actions=%+v", err, session.actions)
			}
		})
	}

	skill, session, _ := npcSkillFixture(t, false)
	err := skill.Execute(context.Background(), automation.Action{Skill: "npc.talk", ExpectedRevision: 11, Arguments: json.RawMessage(`{"npc":"trainer"}`)})
	if !errors.Is(err, aigame.ErrStaleRevision) || len(session.actions) != 0 {
		t.Fatalf("stale talk err=%v actions=%+v", err, session.actions)
	}
}

func TestNPCWindowRequiresActiveVerifiedSequenceObjectChoiceAndQuote(t *testing.T) {
	skill, session, spec := npcSkillFixture(t, false)
	session.snapshot.ActiveWindow = &aigame.WindowSnapshot{Type: 7, Sequence: 100, ObjectID: 42, Open: true, Data: "server-data"}
	valid := automation.Action{Skill: "npc.window", ExpectedRevision: 12, MaximumCost: 5000, Arguments: json.RawMessage(`{"npc":"trainer","window_sequence":100,"choice":"basic"}`)}
	if err := skill.Execute(context.Background(), valid); err != nil {
		t.Fatal(err)
	}
	if len(session.actions) != 1 || session.actions[0].action.Kind != aigame.ActionWindow || session.actions[0].action.WindowSelect != 1 || session.actions[0].action.Text != "" {
		t.Fatalf("window action=%+v", session.actions)
	}

	for _, test := range []struct {
		name    string
		maximum int64
		args    string
	}{
		{name: "quote underflow", maximum: 4999, args: `{"npc":"trainer","window_sequence":100,"choice":1}`},
		{name: "unknown choice", maximum: 5000, args: `{"npc":"trainer","window_sequence":100,"choice":9}`},
		{name: "unknown sequence", maximum: 5000, args: `{"npc":"trainer","window_sequence":101,"choice":1}`},
		{name: "forged cost known", maximum: 5000, args: `{"npc":"trainer","window_sequence":100,"choice":1,"cost_known":true}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			before := len(session.actions)
			err := skill.Execute(context.Background(), automation.Action{Skill: "npc.window", ExpectedRevision: 12, MaximumCost: test.maximum, Arguments: json.RawMessage(test.args)})
			if err == nil || len(session.actions) != before {
				t.Fatalf("accepted invalid window err=%v actions=%+v", err, session.actions)
			}
		})
	}

	session.snapshot.ActiveWindow = &aigame.WindowSnapshot{Type: 7, Sequence: int32(spec.WindowSequence), ObjectID: 999, Open: true}
	if err := skill.Execute(context.Background(), valid); !errors.Is(err, ErrNPCWindowInactive) || len(session.actions) != 1 {
		t.Fatalf("forged window object err=%v actions=%+v", err, session.actions)
	}
}

func TestNPCWindowRejectsClosedWindowAndTakeover(t *testing.T) {
	skill, session, _ := npcSkillFixture(t, false)
	session.snapshot.ActiveWindow = &aigame.WindowSnapshot{Type: 7, Sequence: 100, ObjectID: 42, Open: false}
	action := automation.Action{Skill: "npc.window", ExpectedRevision: 12, MaximumCost: 5000, Arguments: json.RawMessage(`{"npc":"trainer","window_sequence":100,"choice":1}`)}
	if err := skill.Execute(context.Background(), action); !errors.Is(err, ErrNPCWindowInactive) || len(session.actions) != 0 {
		t.Fatalf("closed window err=%v actions=%+v", err, session.actions)
	}

	skill, session, _ = npcSkillFixture(t, false)
	if _, err := skill.Backend.Gate.Takeover("manual"); err != nil {
		t.Fatal(err)
	}
	session.snapshot.ActiveWindow = &aigame.WindowSnapshot{Type: 7, Sequence: 100, ObjectID: 42, Open: true}
	if err := skill.Execute(context.Background(), action); !errors.Is(err, aicontrol.ErrStale) || len(session.actions) != 0 {
		t.Fatalf("takeover window err=%v actions=%+v", err, session.actions)
	}
}

func TestNPCWindowSupportsMultipleVerifiedSequencesPerAlias(t *testing.T) {
	backend, _ := gameFixture(t)
	session := &npcSkillSession{snapshot: aigame.Snapshot{
		Account: "account", Character: "character", Revision: 12, Connected: true, Phase: aigame.PhaseWorld,
		Position: aigame.Point{Floor: 10, X: 4, Y: 6}, Player: aigame.PlayerSnapshot{HasStatus: true, HP: 10},
	}, advance: false}
	backend.Session = session
	spec := verifiedMultiWindowNPCSpec()
	registry, err := NewNPCRegistry([]NPCSpec{spec})
	if err != nil {
		t.Fatal(err)
	}
	got, ok := registry.Lookup("TRAINER")
	if !ok || len(got.Windows) != 2 {
		t.Fatalf("multi-window registry lookup: ok=%v spec=%+v", ok, got)
	}
	if got.Windows[0].Sequence != 100 || got.Windows[1].Sequence != 110 || got.Windows[1].Choices[1].MaximumCost != 5000 {
		t.Fatalf("window contracts=%+v", got.Windows)
	}
	skill := NewNPCSkill(backend, registry)
	session.snapshot.ActiveWindow = &aigame.WindowSnapshot{Type: 7, Sequence: 100, ObjectID: 42, Open: true}
	first := automation.Action{Skill: "npc.window", ExpectedRevision: 12, MaximumCost: 0, Arguments: json.RawMessage(`{"npc":"trainer","window_sequence":100,"choice":1}`)}
	if err := skill.Execute(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if len(session.actions) != 1 || session.actions[0].action.WindowSequence != 100 || session.actions[0].action.WindowObjectID != 42 || session.actions[0].action.WindowSelect != 1 {
		t.Fatalf("first window action=%+v", session.actions)
	}

	session.actions = nil
	session.snapshot.ActiveWindow = &aigame.WindowSnapshot{Type: 0, Sequence: 110, ObjectID: 43, Open: true}
	second := automation.Action{Skill: "npc.window", ExpectedRevision: 12, MaximumCost: 5000, Arguments: json.RawMessage(`{"npc":"trainer","window_sequence":110,"choice":"confirm"}`)}
	if err := skill.Execute(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	if len(session.actions) != 1 || session.actions[0].action.WindowSequence != 110 || session.actions[0].action.WindowObjectID != 43 || session.actions[0].action.WindowSelect != 2 || session.actions[0].action.Text != "course=basic" {
		t.Fatalf("second window action=%+v", session.actions)
	}

	for _, forged := range []automation.Action{
		{Skill: "npc.window", ExpectedRevision: 12, MaximumCost: 5000, Arguments: json.RawMessage(`{"npc":"trainer","window_sequence":110,"choice":9}`)},
		{Skill: "npc.window", ExpectedRevision: 12, MaximumCost: 0, Arguments: json.RawMessage(`{"npc":"trainer","window_sequence":110,"choice":"confirm","cost_known":true}`)},
	} {
		before := len(session.actions)
		if err := skill.Execute(context.Background(), forged); err == nil || len(session.actions) != before {
			t.Fatalf("forged multi-window action accepted: err=%v actions=%+v", err, session.actions)
		}
	}
}

func TestNPCRegistryObservedWindowsOnlyProjectsCurrentVerifiedWindow(t *testing.T) {
	spec := verifiedMultiWindowNPCSpec()
	registry := MustNPCRegistry([]NPCSpec{spec})
	observation := aimcp.Observation{
		Connected: true, Ready: true, Phase: string(aigame.PhaseWorld), Floor: 10,
		CharacterName: "character", Character: aimcp.Entity{ID: "character:0", Name: "character"},
		Actors:       []aimcp.VisibleActor{{ID: 42, Kind: "character", X: 5, Y: 6, Name: "Trainer"}},
		Windows:      []aimcp.WindowState{{Type: 7, Sequence: 100, ObjectID: 42, Open: true}, {Type: 0, Sequence: 110, ObjectID: 43, Open: true}},
		ActiveWindow: &aimcp.WindowState{Type: 0, Sequence: 110, ObjectID: 43, Open: true},
	}
	if got := registry.ObservedWindows(observation); len(got) != 1 || got["trainer"] != 110 {
		t.Fatalf("current verified window projection=%v", got)
	}

	stale := observation
	stale.ActiveWindow = &aimcp.WindowState{Type: 7, Sequence: 100, ObjectID: 42, Open: true}
	if got := registry.ObservedWindows(stale); len(got) != 0 {
		t.Fatalf("historical window was projected as current: %v", got)
	}

	wrongNPC := observation
	wrongNPC.Actors = []aimcp.VisibleActor{{ID: 42, Kind: "character", X: 5, Y: 6, Name: "Other"}}
	if got := registry.ObservedWindows(wrongNPC); len(got) != 0 {
		t.Fatalf("wrong visible NPC was accepted: %v", got)
	}

	closed := observation
	closed.ActiveWindow = &aimcp.WindowState{Type: 0, Sequence: 110, ObjectID: 43, Open: false}
	if got := registry.ObservedWindows(closed); len(got) != 0 {
		t.Fatalf("closed window was projected: %v", got)
	}
	submitted := observation
	submitted.ActiveWindow = &aimcp.WindowState{Type: 0, Sequence: 110, ObjectID: 43, Open: true, Submitted: true}
	if got := registry.ObservedWindows(submitted); len(got) != 0 {
		t.Fatalf("submitted window was still actionable: %v", got)
	}

	unverified := spec
	unverified.Verified = false
	if got := (NPCRegistry{"trainer": unverified}).ObservedWindows(observation); len(got) != 0 {
		t.Fatalf("unverified NPC window was projected: %v", got)
	}
}

func TestNPCWindowResolvesRuntimeObjectIDFromUniqueVisibleActor(t *testing.T) {
	backend, _ := gameFixture(t)
	session := &npcSkillSession{snapshot: aigame.Snapshot{
		Account: "account", Character: "character", Revision: 12, Connected: true, Phase: aigame.PhaseWorld,
		Position: aigame.Point{Floor: 10, X: 4, Y: 6}, Player: aigame.PlayerSnapshot{HasStatus: true, HP: 10},
		Actors: []aigame.ActorSnapshot{{ID: 42, Kind: "character", CharType: 1, X: 5, Y: 6, Name: "Trainer"}},
	}, advance: false}
	backend.Session = session
	registry, err := NewNPCRegistry([]NPCSpec{verifiedRuntimeObjectNPCSpec()})
	if err != nil {
		t.Fatal(err)
	}
	skill := NewNPCSkill(backend, registry)
	session.snapshot.ActiveWindow = &aigame.WindowSnapshot{Type: 7, Sequence: 100, ObjectID: 42, Open: true}
	action := automation.Action{Skill: "npc.window", ExpectedRevision: 12, MaximumCost: 0, Arguments: json.RawMessage(`{"npc":"trainer","window_sequence":100,"choice":1}`)}
	if err := skill.ValidateSkill(context.Background(), action); err != nil {
		t.Fatalf("runtime object contract rejected before observation: %v", err)
	}
	if err := skill.Execute(context.Background(), action); err != nil {
		t.Fatal(err)
	}
	if len(session.actions) != 1 || session.actions[0].action.WindowObjectID != 42 {
		t.Fatalf("runtime actor object was not used: %+v", session.actions)
	}

	withModelObject := action
	withModelObject.Arguments = json.RawMessage(`{"npc":"trainer","window_sequence":100,"window_object_id":42,"choice":1}`)
	if err := skill.ValidateSkill(context.Background(), withModelObject); err == nil {
		t.Fatal("model-supplied object ID bypassed server-owned runtime mode")
	}

	session.actions = nil
	session.snapshot.ActiveWindow.ObjectID = 999
	if err := skill.Execute(context.Background(), action); !errors.Is(err, ErrNPCWindowInactive) || len(session.actions) != 0 {
		t.Fatalf("fake active object accepted: err=%v actions=%+v", err, session.actions)
	}

	session.snapshot.ActiveWindow.ObjectID = 42
	session.snapshot.Actors = nil
	if err := skill.Execute(context.Background(), action); !errors.Is(err, ErrNPCNotVisible) || len(session.actions) != 0 {
		t.Fatalf("missing runtime actor accepted: err=%v actions=%+v", err, session.actions)
	}

	session.snapshot.Actors = []aigame.ActorSnapshot{
		{ID: 42, Kind: "character", CharType: 1, X: 5, Y: 6, Name: "Trainer"},
		{ID: 43, Kind: "character", CharType: 1, X: 5, Y: 6, Name: "Trainer"},
	}
	if err := skill.Execute(context.Background(), action); !errors.Is(err, ErrNPCAmbiguous) || len(session.actions) != 0 {
		t.Fatalf("ambiguous runtime actor accepted: err=%v actions=%+v", err, session.actions)
	}
}

func TestNPCRegistryObservedWindowsResolvesRuntimeObjectID(t *testing.T) {
	registry := MustNPCRegistry([]NPCSpec{verifiedRuntimeObjectNPCSpec()})
	base := aimcp.Observation{
		Connected: true, Ready: true, Phase: string(aigame.PhaseWorld), Floor: 10,
		CharacterName: "character", Character: aimcp.Entity{ID: "character:0", Name: "character"},
		Actors:       []aimcp.VisibleActor{{ID: 42, Kind: "character", X: 5, Y: 6, Name: "Trainer"}},
		Windows:      []aimcp.WindowState{{Type: 7, Sequence: 100, ObjectID: 42, Open: true}},
		ActiveWindow: &aimcp.WindowState{Type: 7, Sequence: 100, ObjectID: 42, Open: true},
	}
	if got := registry.ObservedWindows(base); len(got) != 1 || got["trainer"] != 100 {
		t.Fatalf("runtime object projection=%v", got)
	}

	fakeObject := base
	fakeObject.ActiveWindow = &aimcp.WindowState{Type: 7, Sequence: 100, ObjectID: 999, Open: true}
	fakeObject.Windows = []aimcp.WindowState{{Type: 7, Sequence: 100, ObjectID: 999, Open: true}}
	if got := registry.ObservedWindows(fakeObject); len(got) != 0 {
		t.Fatalf("fake runtime object was projected: %v", got)
	}

	actorGone := base
	actorGone.Actors = nil
	if got := registry.ObservedWindows(actorGone); len(got) != 0 {
		t.Fatalf("window remained confirmed after actor removal: %v", got)
	}

	ambiguous := base
	ambiguous.Actors = append(append([]aimcp.VisibleActor(nil), base.Actors...), aimcp.VisibleActor{ID: 43, Kind: "character", X: 5, Y: 6, Name: "Trainer"})
	if got := registry.ObservedWindows(ambiguous); len(got) != 0 {
		t.Fatalf("ambiguous runtime actor was projected: %v", got)
	}
}
