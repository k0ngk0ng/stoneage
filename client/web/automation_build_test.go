package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aicontrol"
	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
	"github.com/k0ngk0ng/stoneage/internal/characterbuild"
)

func TestHumanBuildHTTPIsOptionalAndStrict(t *testing.T) {
	for _, tc := range []struct {
		body           string
		ok, configured bool
	}{
		{`{"mode":"leveling"}`, true, false},
		{`{"mode":"leveling","character_build":{"weights":{"strength":2,"dexterity":1},"reserve_points":3}}`, true, true},
		{`{"mode":"quest","character_build":{"weights":{"strength":1}}}`, false, false},
		{`{"mode":"leveling","character_build":{"weights":{}}}`, false, false},
		{`{"mode":"leveling","character_build":{"weights":{"vital":-1}}}`, false, false},
		{`{"mode":"leveling","character_build":{"weights":{"vital":101}}}`, false, false},
		{`{"mode":"leveling","character_build":{"weights":{"vital":1},"reserve_points":1001}}`, false, false},
		{`{"mode":"leveling","character_build":{"weights":{"vital":1},"unknown":true}}`, false, false},
	} {
		input, err := decodeAutomationStart(httptest.NewRecorder(), httptest.NewRequest("POST", "/", strings.NewReader(tc.body)))
		if (err == nil) != tc.ok {
			t.Fatalf("input=%s err=%v", tc.body, err)
		}
		if tc.ok && (input.CharacterBuild != nil) != tc.configured {
			t.Fatalf("configuration changed: %+v", input)
		}
		if tc.configured && (input.CharacterBuild.Weights.Strength != 2 || input.CharacterBuild.ReservePoints != 3) {
			t.Fatalf("policy changed: %+v", input.CharacterBuild)
		}
	}
}

func TestHumanBuildRequiresCanonicalCharacterIdentity(t *testing.T) {
	build := &characterbuild.Policy{Weights: characterbuild.Weights{Strength: 1}}
	for _, tc := range []struct {
		name  string
		chars []aigame.Character
		ok    bool
	}{
		{"missing", nil, false},
		{"other", []aigame.Character{{Name: "other", Slot: 0}}, false},
		{"duplicate", []aigame.Character{{Name: "hero", Slot: 0}, {Name: "hero", Slot: 1}}, false},
		{"negative", []aigame.Character{{Name: "hero", Slot: -1}}, false},
		{"confirmed", []aigame.Character{{Name: "hero", Slot: 1}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snapshot := aigame.Snapshot{Account: "account", Character: "hero", Characters: tc.chars}
			b, err := webAutomationBuildBinding(snapshot, 1, build)
			if (err == nil) != tc.ok {
				t.Fatalf("binding=%+v err=%v", b, err)
			}
			if tc.ok && b.CharacterID != "account:1" {
				t.Fatalf("wrong durable identity: %+v", b)
			}
			if _, err := webAutomationBuildBinding(snapshot, 1, nil); err != nil {
				t.Fatalf("optional build altered ordinary automation: %v", err)
			}
		})
	}
}

func seedHumanBuildIdentity(t *testing.T, f automationExecutorFixture, points int) {
	t.Helper()
	f.tcp.applyAuthoritativePacket(webServerPacket(t, 6, "CharList", "successful", `AutomationHero|0\z0\z1\z10\z100\z20\z30\z4\z0\z50\z50\z50\z50\z0\zAutomationHero\zhome`))
	f.tcp.applyAuthoritativePacket(webServerPacket(t, 7, "CharLogin", "successful", ""))
	raw, _ := json.Marshal(points)
	f.tcp.applyAuthoritativePacket(webServerPacket(t, 8, "S", "AI|v=1|chara=12|end=0,0,0,0,0,0|now=0,0,0,0,0,0|ride=0|stat_points="+string(raw)))
}

func TestHumanBuildPreviewDoesNotCompleteBeforeAllocationSettles(t *testing.T) {
	f := newAutomationExecutorFixture(t, 10, 5000)
	seedHumanBuildIdentity(t, f, 2)
	request := levelingAutomationRequest(f.session.generation, 10, 0)
	request.Config.CharacterBuild = &characterbuild.Policy{Weights: characterbuild.Weights{Strength: 1}, ReservePoints: 1}
	before := f.tcp.authoritativeSnapshot()
	preview, err := f.executor.Preview(context.Background(), f.session, request)
	if err != nil || preview.AlreadyComplete || !preview.Ready {
		t.Fatalf("unsettled build preview=%+v err=%v", preview, err)
	}
	if f.tcp.authoritativeSnapshot().Revision != before.Revision {
		t.Fatal("preview changed gameplay")
	}
	seedHumanBuildIdentity(t, f, 1)
	preview, err = f.executor.Preview(context.Background(), f.session, request)
	if err != nil || !preview.AlreadyComplete {
		t.Fatalf("settled build preview=%+v err=%v", preview, err)
	}
	binding, err := webAutomationBuildBinding(f.tcp.authoritativeSnapshot(), f.session.generation, request.Config.CharacterBuild)
	if err != nil {
		t.Fatal(err)
	}
	binding.Generation = 1 // A prior control lease must still block the new run.
	if _, _, err := f.receipts.PrepareOnce(context.Background(), binding, aimcp.TypedAction{Kind: "allocate-stat", Index: 1, ExpectedRevision: 1}); err != nil {
		t.Fatal(err)
	}
	preview, err = f.executor.Preview(context.Background(), f.session, request)
	if err != nil || preview.AlreadyComplete || preview.Ready {
		t.Fatalf("unknown prior allocation was ignored: %+v %v", preview, err)
	}
}

func TestHumanBuildRejectsQuestAndInvalidPolicyBeforeStart(t *testing.T) {
	f := newAutomationExecutorFixture(t, 10, 5000)
	request := levelingAutomationRequest(f.session.generation, 10, 0)
	request.Config.CharacterBuild = &characterbuild.Policy{}
	if _, err := f.executor.Start(f.lease, f.session, request); err == nil {
		t.Fatal("invalid policy started")
	}
	request.Mode = aicontrol.Quest
	request.Config.CharacterBuild = &characterbuild.Policy{Weights: characterbuild.Weights{Strength: 1}}
	if _, err := f.executor.Start(f.lease, f.session, request); err == nil {
		t.Fatal("quest accepted unimplemented character build")
	}
}

func TestHumanBuildStartWiresSharedPolicyWithoutAIProfile(t *testing.T) {
	f := newAutomationExecutorFixture(t, 10, 5000)
	seedHumanBuildIdentity(t, f, 1)
	request := levelingAutomationRequest(f.session.generation, 10, 0)
	request.Config.CharacterBuild = &characterbuild.Policy{Weights: characterbuild.Weights{Strength: 2, Dexterity: 1}, ReservePoints: 1}
	value, err := f.executor.Start(f.lease, f.session, request)
	if err != nil {
		t.Fatal(err)
	}
	handle, ok := value.(*webAutomationHandle)
	if !ok {
		t.Fatalf("unexpected handle %T", value)
	}
	defer handle.Stop(context.Background())
	if handle.leveling.CharacterPreparation == nil || handle.backend.Owner != aicontrol.Leveling || handle.backend.Funding != nil {
		t.Fatal("ordinary player did not receive the shared deterministic preparation")
	}
	request.Config.CharacterBuild.Weights.Strength = 100
	if handle.backend.CharacterBuild.Weights.Strength != 2 || handle.backend.CharacterBuild.ReservePoints != 1 {
		t.Fatal("running character build changed with the caller's config")
	}
	waitAutomationReceipt(t, handle, func(receipt aimcp.TaskReceipt) bool { return receipt.Status == aimcp.ReceiptConfirmed })
}
