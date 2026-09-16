package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aicontrol"
	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
)

func questPreflightFixture(t *testing.T) (automationExecutorFixture, AutomationStartRequest) {
	t.Helper()
	fixture := newAutomationExecutorFixture(t, 10, 1000)
	dir := t.TempDir()
	raw := []byte("synthetic preflight source")
	if err := os.WriteFile(filepath.Join(dir, "facts.conf"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	ref := aiknowledge.SourceRef{Path: "facts.conf", Line: 1, SHA256: aiknowledge.SHA256Hex(raw)}
	digest := aiknowledge.SHA256Hex(append(append([]byte("facts.conf\x00"), raw...), 0))
	completion := aiknowledge.MachineCondition{Kind: "character_level", Value: 99}
	task := aiknowledge.TaskDefinition{
		ID: "preflight-fixture", Name: "Preflight fixture", Status: aiknowledge.TaskVerified,
		PreparationReviewed: true, PreparationNotes: "Synthetic component fixture only.",
		EvidenceVerified: true, ExecutionVerified: true, DataFingerprint: digest,
		Steps: []aiknowledge.TaskStep{{ID: "walk", Action: aiknowledge.TaskAction{
			Skill: "move", Arguments: json.RawMessage(`{"floor":100,"x":31,"y":30}`)},
			SuccessConditions: []aiknowledge.MachineCondition{completion}, TimeoutSeconds: 30, CostKnown: true}},
		Success: []aiknowledge.SuccessCondition{{MachineCondition: completion}},
		Budget:  aiknowledge.Budget{Evidence: []aiknowledge.SourceRef{ref}},
	}
	fixture.executor.config.Knowledge = &aiknowledge.Knowledge{DataDir: dir, Digest: strings.Repeat("a", 64), TaskDefinitions: []aiknowledge.TaskDefinition{task}}
	// Preview occurs while the player retains manual control.
	state, _, err := fixture.tcp.gate.Switch(fixture.session.generation, aicontrol.Manual, "preview fixture")
	if err != nil {
		t.Fatal(err)
	}
	fixture.session.mode, fixture.session.generation = state.Mode, state.Generation
	return fixture, AutomationStartRequest{Mode: aicontrol.Quest, Generation: state.Generation, Config: AutomationConfig{TaskID: task.ID}}
}

func TestQuestPreviewChecksInstalledContractsAndLevelDetailsWithoutSideEffects(t *testing.T) {
	for _, test := range []struct{ name, skill, args, problem string }{
		{"missing-NPC", "npc.talk", `{"npc":"uninstalled"}`, "步骤暂不可执行：walk"},
		{"missing-stock", "item.stock", `{"item":"uninstalled"}`, "步骤暂不可执行：walk"},
		{"level", "move", `{"floor":100,"x":31,"y":30}`, "人物等级不满足：当前 10，要求至少 35"},
		{"backpack", "move", `{"floor":100,"x":31,"y":30}`, "背包资料尚未同步：任务要求至少 15 个空位"},
		{"route", "move", `{"floor":101,"x":31,"y":30}`, "出发路线暂不可用：请检查当前位置、人物等级及路线资料"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture, request := questPreflightFixture(t)
			task := &fixture.executor.config.Knowledge.TaskDefinitions[0]
			task.Steps[0].Action = aiknowledge.TaskAction{Skill: test.skill, Arguments: json.RawMessage(test.args)}
			if test.name == "level" {
				task.Preconditions = []aiknowledge.Precondition{{MachineCondition: aiknowledge.MachineCondition{Kind: "character_level", Value: 35}}}
			}
			if test.name == "backpack" {
				task.Preconditions = []aiknowledge.Precondition{{MachineCondition: aiknowledge.MachineCondition{Kind: "backpack_free_slots", Value: 15}}}
			}
			before := fixture.tcp.authoritativeSnapshot()
			gateBefore := fixture.tcp.gate.State()
			preview, err := fixture.executor.Preview(context.Background(), fixture.session, request)
			if err != nil || preview.Ready || !containsAutomationProblem(preview.Problems, test.problem) {
				t.Fatalf("preview=%+v err=%v", preview, err)
			}
			if fixture.tcp.authoritativeSnapshot().Revision != before.Revision || fixture.tcp.gate.State() != gateBefore {
				t.Fatal("preview changed observation or control lease")
			}
			binding, err := webAutomationBinding(before, request.Generation)
			if err != nil {
				t.Fatal(err)
			}
			plans, err := fixture.plans.List(context.Background(), binding.CharacterID)
			if err != nil || len(plans) != 0 {
				t.Fatalf("preview persisted plans=%+v err=%v", plans, err)
			}
			_ = fixture.peer.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
			var packet [1]byte
			n, readErr := fixture.peer.Read(packet[:])
			var timeout net.Error
			if n != 0 || !errors.As(readErr, &timeout) || !timeout.Timeout() {
				t.Fatalf("preview wrote a game packet: n=%d err=%v", n, readErr)
			}
		})
	}
}

func TestQuestPreviewRejectsStaleDisplayedQuote(t *testing.T) {
	fixture, request := questPreflightFixture(t)
	request.Config.Budget.ExpectedHigh = 999
	preview, err := fixture.executor.Preview(context.Background(), fixture.session, request)
	if err != nil || preview.Ready || !containsAutomationProblem(preview.Problems, "任务最高费用与知识库不一致") {
		t.Fatalf("preview accepted stale quote: %+v err=%v", preview, err)
	}
}
