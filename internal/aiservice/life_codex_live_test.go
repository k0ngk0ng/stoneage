package aiservice

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aicodex"
	"github.com/k0ngk0ng/stoneage/internal/aicontrol"
	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
	"github.com/k0ngk0ng/stoneage/internal/aimodels"
	"github.com/k0ngk0ng/stoneage/internal/airuntime"
	"github.com/k0ngk0ng/stoneage/internal/aisupervisor"
)

const (
	liveCodexLifeOptIn       = "STONEAGE_LIFE_CODEX_LIVE_TEST"
	liveCodexLifeKeyFile     = "STONEAGE_LIFE_CODEX_KEY_FILE"
	liveCodexLifeBinary      = "STONEAGE_LIFE_CODEX_BINARY"
	liveCodexLifeNoteKey     = "life-codex-heartbeat-note"
	liveCodexLifeNote        = "first heartbeat persisted this private note"
	liveCodexLifeReminderKey = "life-codex-heartbeat-reminder-v1"
)

// TestLiveCodexLifeHeartbeatFreshAI is an opt-in end-to-end check for the
// ongoing life loop. It provisions a fresh QA character, runs a real Codex
// turn which writes a private note and reminder, restarts the supervisor, and
// verifies a second real turn reads both durable values. The backend rejects
// every game mutation so this check can never move or alter the QA character.
func TestLiveCodexLifeHeartbeatFreshAI(t *testing.T) {
	if os.Getenv(liveCodexLifeOptIn) != "1" {
		t.Skip("set STONEAGE_LIFE_CODEX_LIVE_TEST=1 for the real Codex life-heartbeat QA check")
	}
	runLiveCodexLifeFreshAI(t, false)
}

func TestLiveCodexLifeReminderFreshAI(t *testing.T) {
	if os.Getenv("STONEAGE_LIFE_CODEX_REMINDER_LIVE_TEST") != "1" {
		t.Skip("set STONEAGE_LIFE_CODEX_REMINDER_LIVE_TEST=1 for real Codex due-reminder QA")
	}
	runLiveCodexLifeFreshAI(t, true)
}

func runLiveCodexLifeFreshAI(t *testing.T, dueReminder bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	fixture := movementCrossMapLiveProvisionFreshAIHometown(t, ctx, "life-codex", "aiservice-life-codex-live-test", 0)
	// Finish the asynchronous login/own-state synchronization before testing
	// an idle heartbeat. Newly discovered character facts are real events and
	// would legitimately trigger a follow-up decision during the first turn.
	before, err := fixture.Lease.Session.Observe(ctx)
	if err != nil {
		t.Fatal("observe fresh character before own-state synchronization")
	}
	if err := fixture.Lease.Session.ExecuteExpected(ctx, before.Revision, aigame.Action{Kind: aigame.ActionStatus, Command: "AI"}); err != nil {
		t.Fatal("request initial own-state synchronization")
	}
	if _, err := movementCrossMapLiveWaitSnapshot(ctx, fixture.Lease.Session, func(s aigame.Snapshot) bool {
		return s.AI.Received && s.AIObservationRevision > before.AIObservationRevision
	}); err != nil {
		t.Fatal("initial own-state synchronization did not complete")
	}

	codexBinary := strings.TrimSpace(os.Getenv(liveCodexLifeBinary))
	if codexBinary == "" {
		codexBinary = realGameFactoryCodex
	}
	if !filepath.IsAbs(codexBinary) {
		t.Fatal("real Codex binary path must be absolute")
	}
	if info, err := os.Stat(codexBinary); err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		t.Fatal("the official Codex CLI is unavailable")
	}
	// The version check uses only the fixture-owned home and cache. It does
	// not read the operator's .codex/config.toml.
	_ = realGameFactoryCodexVersion(t, fixture.Root, codexBinary)

	keyPath := strings.TrimSpace(os.Getenv(liveCodexLifeKeyFile))
	if keyPath == "" {
		keyPath = filepath.Join(fixture.RepoRoot, "vendor", "deepseek", "key")
	}
	if !filepath.IsAbs(keyPath) {
		t.Fatal("DeepSeek key path must be absolute")
	}
	key, err := readFactoryLiveKey(keyPath)
	if err != nil {
		// Do not include the error or path contents in test evidence. In
		// particular, never print the key itself.
		t.Fatal("configured DeepSeek key is unavailable")
	}

	installer, err := aimcp.NewSkillInstaller(filepath.Join(fixture.RepoRoot, "ai", "skills"))
	if err != nil {
		t.Fatal("create native skill installer")
	}
	spec, err := installer.Verify("stoneage-play")
	if err != nil {
		t.Fatal("verify native stoneage-play skill")
	}

	model, err := fixture.Profiles.CreateModelConfig(ctx, airuntime.ModelConfig{
		ID: "life-codex-live-model", Name: "DeepSeek Flash life heartbeat QA",
		Backend: airuntime.ModelBackendCodex, Provider: aimodels.DeepSeekProvider,
		BaseURL: aimodels.DeepSeekBaseURL, Model: aimodels.DeepSeekFlash,
		WireAPI: airuntime.ModelProviderResponses, ReasoningEffort: airuntime.ReasoningEffortHigh,
		Timeout: 4 * time.Minute, MaxOutputTokens: 1024, HasKey: true,
	})
	if err != nil {
		t.Fatal("create isolated DeepSeek model configuration")
	}
	modelSecrets, err := airuntime.NewSecretStore(filepath.Join(fixture.Root, "life-model-secrets"))
	if err != nil {
		t.Fatal("create isolated model secret store")
	}
	if err := modelSecrets.WriteKey(model.ID, key); err != nil {
		t.Fatal("write isolated DeepSeek model secret")
	}

	goal := airuntime.Goal{
		Kind: "life", Description: "keep living through durable heartbeat decisions",
		StopWhenCompleted: false,
		Life:              &airuntime.LifePolicy{DecisionIntervalSeconds: 60, Activities: []string{"memory-plan"}},
	}
	if dueReminder {
		// The reminder must wake well before the next idle heartbeat, so the
		// test cannot pass merely because both deadlines have elapsed.
		goal.Life.DecisionIntervalSeconds = 3600
	}
	skills := []airuntime.SkillVersion{{Name: spec.Name, Version: spec.Version, Kind: airuntime.SkillKindNative, Digest: "sha256:" + spec.SHA256}}
	// Codex reports aggregate input across its internal tool loop, including
	// cached context. Keep a finite budget large enough for two such turns.
	tokenBudget := int64(1000000)
	updated, err := fixture.Profiles.UpdateProfileCAS(ctx, fixture.Profile.ID, fixture.Profile.Version, airuntime.ProfilePatch{
		ModelConfigID: &model.ID, Goal: &goal, Skills: &skills, DailyTokenBudget: &tokenBudget, Actor: "aiservice-life-codex-live-test",
	})
	if err != nil {
		t.Fatal("configure fresh AI life goal")
	}
	fixture.Profile = updated

	mcpBinary := filepath.Join(fixture.Root, "bin", "stoneage-game-mcp")
	build := exec.CommandContext(ctx, "go", "build", "-mod=readonly", "-o", mcpBinary, "./cmd/stoneage-game-mcp")
	build.Dir = fixture.RepoRoot
	build.Env = movementCrossMapLiveGoEnvironment(fixture.Root)
	if output, err := build.CombinedOutput(); err != nil {
		_ = output
		t.Fatal("build isolated game MCP sidecar")
	}
	if info, err := os.Stat(mcpBinary); err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		t.Fatal("built game MCP sidecar is unavailable")
	}

	trace1 := &lifeCodexLiveTrace{}
	trace2 := &lifeCodexLiveTrace{}
	var providerMu sync.Mutex
	providerOpens := 0
	provider := SessionProviderFunc(func(_ context.Context, selected airuntime.Profile) (SessionLease, error) {
		if selected.ID != fixture.Profile.ID || selected.Account.Username != fixture.Profile.Account.Username || selected.Character.Name != fixture.Profile.Character.Name {
			return SessionLease{}, errors.New("life heartbeat profile identity mismatch")
		}
		providerMu.Lock()
		providerOpens++
		trace := trace1
		if providerOpens > 1 {
			trace = trace2
		}
		providerMu.Unlock()
		return SessionLease{
			Session: &lifeCodexReadOnlySession{GameSession: fixture.Lease.Session, trace: trace},
			Funding: func(context.Context) (bool, error) { return selected.UnlimitedFunds, nil },
			// The fresh-AI fixture owns the underlying session. Factory cleanup
			// revokes the Gateway capability, while this no-op leaves the
			// fixture session available for the restart half of the test.
			Close: func() {},
		}, nil
	})

	gateway := NewGateway()
	gatewayHTTP := httptest.NewServer(gateway)
	defer gatewayHTTP.Close()

	clock := newLifeCodexLiveClock(time.Now().UTC())
	factory1 := newLifeCodexLiveFactory(t, fixture, modelSecrets, installer, mcpBinary, codexBinary, provider, gateway, gatewayHTTP.URL+"/v1/game", trace1)
	t.Cleanup(func() { _ = factory1.Close() })
	counting1 := &lifeCodexCountingFactory{Factory: factory1}
	supervisor1, err := aisupervisor.New(ctx, fixture.Profiles, counting1, lifeCodexSupervisorConfig(clock, lifeCodexFirstPrompt()))
	if err != nil {
		t.Fatal("create first life supervisor")
	}
	t.Cleanup(func() { _ = supervisor1.Close() })
	if err := supervisor1.Start(ctx, fixture.Profile.ID); err != nil {
		t.Fatal("start first life supervisor")
	}
	waitLifeCodexLive(t, ctx, func() bool {
		status, statusErr := supervisor1.Status(ctx, fixture.Profile.ID)
		if statusErr == nil && (status.State == aisupervisor.StateError || status.State == aisupervisor.StatePaused) {
			for _, result := range counting1.runnerResults() {
				t.Logf("completed attempt usage: %+v", result.Usage)
			}
			t.Fatalf("first supervisor stopped: state=%s requests=%d results=%d reason=%s", status.State, len(counting1.runnerRequests()), len(counting1.runnerResults()), strings.ReplaceAll(status.LastError, key, "[redacted]"))
		}
		notes, notesErr := fixture.Profiles.ListAgentNotes(ctx, fixture.Profile.ID, 20)
		schedules, schedulesErr := fixture.Profiles.ListSchedules(ctx, fixture.Profile.ID, 20)
		return statusErr == nil && notesErr == nil && schedulesErr == nil && status.ModelTurnDone &&
			len(counting1.runnerRequests()) == 1 && lifeCodexHasNote(notes, liveCodexLifeNoteKey, liveCodexLifeNote) &&
			lifeCodexHasPendingReminder(schedules)
	})
	firstStatus, err := supervisor1.Status(ctx, fixture.Profile.ID)
	if err != nil {
		t.Fatal("read first life supervisor status")
	}
	firstCheckpoint, err := fixture.Profiles.GetCheckpoint(ctx, fixture.Profile.ID)
	if err != nil {
		t.Fatal("read first life checkpoint")
	}
	firstRequests := counting1.runnerRequests()
	firstResults := counting1.runnerResults()
	if len(firstRequests) != 1 || len(firstResults) != 1 {
		t.Fatalf("first supervisor did not complete exactly one real Codex turn: requests=%d results=%d", len(firstRequests), len(firstResults))
	}
	assertRealGameFactoryResultHasNoSecret(t, firstResults[0], key)
	if firstResults[0].Process.Status != aicodex.ProcessExited || firstResults[0].Process.ExitCode != 0 || firstResults[0].Turn.Status != aicodex.TurnCompleted || firstResults[0].ThreadID == "" {
		t.Fatalf("first Codex result is incomplete: process=%+v turn=%+v", firstResults[0].Process, firstResults[0].Turn)
	}
	if !firstStatus.ModelTurnDone || firstStatus.NextDecisionAt.IsZero() || firstStatus.ThreadID != firstResults[0].ThreadID {
		t.Fatalf("first supervisor did not persist a continuing life checkpoint: %+v", firstStatus)
	}
	if err := supervisor1.Close(); err != nil {
		t.Fatal("close first life supervisor")
	}
	t.Logf("first real Codex turn completed; private note and pending reminder persisted; usage=%+v", firstResults[0].Usage)
	reminders, err := fixture.Profiles.ListSchedules(ctx, fixture.Profile.ID, 20)
	if err != nil || len(reminders) != 1 {
		t.Fatal("expected one persisted reminder before restarting")
	}
	reminder := reminders[0]
	// Reopen the SQLite store too: neither the Factory nor a retained store
	// object may supply the supposedly durable memory and reminder values.
	if err := fixture.Profiles.Close(); err != nil {
		t.Fatal("close durable profile store")
	}
	fixture.Profiles, err = airuntime.OpenStore(filepath.Join(fixture.Root, "profiles.db"))
	if err != nil {
		t.Fatal("reopen durable profile store")
	}

	// A fresh Factory and supervisor must reuse the same durable profile,
	// Codex state and note/schedule stores. The fixture's game socket remains
	// owned by the outer fresh-AI test helper.
	factory2 := newLifeCodexLiveFactory(t, fixture, modelSecrets, installer, mcpBinary, codexBinary, provider, gateway, gatewayHTTP.URL+"/v1/game", trace2)
	t.Cleanup(func() { _ = factory2.Close() })
	counting2 := &lifeCodexCountingFactory{Factory: factory2}
	secondPrompt := lifeCodexSecondPrompt()
	if dueReminder {
		secondPrompt = lifeCodexDuePrompt()
	}
	supervisor2, err := aisupervisor.New(ctx, fixture.Profiles, counting2, lifeCodexSupervisorConfig(clock, secondPrompt))
	if err != nil {
		t.Fatal("create restarted life supervisor")
	}
	t.Cleanup(func() { _ = supervisor2.Close() })
	if err := supervisor2.Start(ctx, fixture.Profile.ID); err != nil {
		t.Fatal("start restarted life supervisor")
	}
	if dueReminder {
		dueAt := reminder.RunAt.Add(time.Second)
		if !dueAt.After(clock.Now()) || !dueAt.Before(firstStatus.NextDecisionAt) {
			t.Fatal("reminder deadline is not before the idle heartbeat")
		}
		clock.Advance(dueAt.Sub(clock.Now()))
	} else {
		clock.Advance(61 * time.Second)
	}
	waitLifeCodexLive(t, ctx, func() bool {
		status, statusErr := supervisor2.Status(ctx, fixture.Profile.ID)
		if statusErr == nil && (status.State == aisupervisor.StateError || status.State == aisupervisor.StatePaused) {
			t.Fatalf("restarted supervisor stopped: state=%s requests=%d results=%d reason=%s", status.State, len(counting2.runnerRequests()), len(counting2.runnerResults()), strings.ReplaceAll(status.LastError, key, "[redacted]"))
		}
		return statusErr == nil && status.ModelTurnDone && status.State == aisupervisor.StateWaiting && status.Checkpoint > firstCheckpoint.Version && len(counting2.runnerRequests()) == 1 && len(counting2.runnerResults()) == 1
	})
	secondRequests := counting2.runnerRequests()
	secondResults := counting2.runnerResults()
	if len(secondRequests) != 1 || len(secondResults) != 1 {
		t.Fatalf("restarted supervisor did not complete exactly one real Codex turn: requests=%d results=%d", len(secondRequests), len(secondResults))
	}
	assertRealGameFactoryResultHasNoSecret(t, secondResults[0], key)
	if !secondRequests[0].Resume || secondRequests[0].ThreadID != firstResults[0].ThreadID {
		t.Fatalf("restarted heartbeat did not resume the exact Codex thread: first=%q request=%+v", firstResults[0].ThreadID, secondRequests[0])
	}
	if dueReminder && (!strings.Contains(secondRequests[0].Prompt, "Due self-scheduled reminders") || !strings.Contains(secondRequests[0].Prompt, reminder.ID)) {
		t.Fatal("due reminder was not included in the actual Codex prompt")
	}
	if secondResults[0].Process.Status != aicodex.ProcessExited || secondResults[0].Process.ExitCode != 0 || secondResults[0].Turn.Status != aicodex.TurnCompleted || secondResults[0].ThreadID != firstResults[0].ThreadID {
		t.Fatalf("second Codex result is incomplete or changed thread: process=%+v turn=%+v thread=%q", secondResults[0].Process, secondResults[0].Turn, secondResults[0].ThreadID)
	}
	secondStatus, err := supervisor2.Status(ctx, fixture.Profile.ID)
	if err != nil {
		t.Fatal("read restarted life supervisor status")
	}
	if secondStatus.State == aisupervisor.StateCompleted || secondStatus.GoalComplete || secondStatus.NextDecisionAt.IsZero() {
		t.Fatalf("life heartbeat stopped after the second goal-shaped turn: %+v", secondStatus)
	}
	if err := supervisor2.Close(); err != nil {
		t.Fatal("close restarted life supervisor")
	}

	if err := trace1.verifyReadOnly(); err != nil {
		t.Fatal("life heartbeat used a forbidden game operation")
	}
	if err := trace2.verifyReadOnly(); err != nil {
		t.Fatal("restarted life heartbeat used a forbidden game operation")
	}
	if !trace1.hasCalls("observe", "memory_write", "schedule_create") {
		t.Fatal("first life heartbeat did not use observe, private memory and schedule tools")
	}
	if !trace2.hasCalls("observe", "memory_list", "schedule_list") {
		t.Fatal("restarted life heartbeat did not read the durable memory and schedule")
	}
	notes, err := fixture.Profiles.ListAgentNotes(ctx, fixture.Profile.ID, 20)
	if err != nil || !lifeCodexHasNote(notes, liveCodexLifeNoteKey, liveCodexLifeNote) {
		t.Fatal("private memory note did not survive supervisor restart")
	}
	schedules, err := fixture.Profiles.ListSchedules(ctx, fixture.Profile.ID, 20)
	if err != nil {
		t.Fatal("read reminder after restarted Codex turn")
	}
	if dueReminder {
		if len(schedules) != 1 || schedules[0].ID != reminder.ID || schedules[0].Status != airuntime.ScheduleDelivered {
			t.Fatal("due reminder was not durably settled with the completed Codex turn")
		}
		if strings.TrimSpace(secondResults[0].LastMessage) != "STONEAGE_LIFE_CODEX_REMINDER_OK" {
			t.Fatal("Codex did not acknowledge reading the due reminder")
		}
	} else if !lifeCodexHasPendingReminder(schedules) {
		t.Fatal("pending reminder did not survive supervisor restart")
	}
	t.Logf("life QA passed: turns=2 thread_resumed=%t due_reminder=%t store_reopened=true mcp_calls=%d second_usage=%+v", secondRequests[0].Resume, dueReminder, trace1.callCount()+trace2.callCount(), secondResults[0].Usage)
}

func lifeCodexDuePrompt() func(airuntime.Profile, aisupervisor.Snapshot) (string, error) {
	return func(airuntime.Profile, aisupervisor.Snapshot) (string, error) {
		return "A previously scheduled reminder is now due after a runtime restart. Read the attached due reminder, then call game_observe, game_memory_list and game_schedule_list. Verify the private note key `" + liveCodexLifeNoteKey + "` has text `" + liveCodexLifeNote + "` and the reminder says `confirm the first heartbeat note`. Its status may be delivering because settlement follows this turn. Use only these three read tools. Do not call game_action, tasks, write/delete memory, create/cancel reminders, move, battle, spend, chat or mail. Reply exactly STONEAGE_LIFE_CODEX_REMINDER_OK after reading the data.", nil
	}
}

func newLifeCodexLiveFactory(t *testing.T, fixture *movementCrossMapLiveFreshAI, secrets *airuntime.SecretStore, installer *aimcp.SkillInstaller, mcpBinary, codexBinary string, provider SessionProvider, gateway *Gateway, endpoint string, trace *lifeCodexLiveTrace) *Factory {
	t.Helper()
	factory, err := NewFactory(FactoryConfig{
		Models: fixture.Profiles, Secrets: secrets, Sessions: provider, Gateway: gateway, GatewayEndpoint: endpoint,
		SkillInstaller: installer, RuntimeRoot: filepath.Join(fixture.Root, "codex-runtime"),
		StateRoot:     filepath.Join(fixture.Root, "codex-runtime", "state"),
		WorkspaceRoot: filepath.Join(fixture.Root, "codex-runtime", "workspaces"),
		CodexHomeRoot: filepath.Join(fixture.Root, "codex-runtime", "codex"),
		CodexBinary:   codexBinary, MCPBinary: mcpBinary, GitBinary: realGameFactoryGitBinary(t),
		Environment: map[string]string{
			"GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": filepath.Join(fixture.Root, "gitconfig"), "GIT_CONFIG_SYSTEM": os.DevNull,
		},
		MemoryStore: fixture.Profiles, ScheduleStore: fixture.Profiles, TerminationGrace: 2 * time.Second,
		Backend: func(_ context.Context, input BackendInput) (aimcp.Backend, error) {
			if input.Session == nil || input.Gate == nil {
				return nil, aimcp.ErrBackend
			}
			game := &GameBackend{
				Binding: input.Binding, Gate: input.Gate, Owner: aicontrol.Agent, Session: input.Session,
				Funding: input.Funding, Schedules: input.ScheduleStore, AgentNotes: input.MemoryStore,
				OwnStateRefresh: &OwnStateRefresher{},
			}
			return &lifeCodexReadOnlyBackend{GameBackend: game, trace: trace}, nil
		},
	})
	if err != nil {
		t.Fatalf("create isolated Codex Factory: %v", err)
	}
	return factory
}

func lifeCodexSupervisorConfig(clock *lifeCodexLiveClock, prompt func(airuntime.Profile, aisupervisor.Snapshot) (string, error)) aisupervisor.Config {
	return aisupervisor.Config{
		PollInterval: 5 * time.Millisecond, ObserveTimeout: 15 * time.Second, TurnTimeout: 2 * time.Minute,
		FailureBackoff: 25 * time.Millisecond, MaxFailureBackoff: time.Second, MaxRunFailures: 2,
		MaxObservationFailures: 2, NoProgressLimit: 8, NoProgressWindow: time.Hour, TokenCharge: 1,
		Clock: clock.Now, Prompt: prompt,
	}
}

func lifeCodexFirstPrompt() func(airuntime.Profile, aisupervisor.Snapshot) (string, error) {
	return func(airuntime.Profile, aisupervisor.Snapshot) (string, error) {
		return "This is the first turn of a read-only life heartbeat QA. Use only game_observe, game_memory_write, game_memory_list, game_memory_delete, game_schedule_create, game_schedule_list, and game_schedule_cancel. Do not call game_query_knowledge, game_start_task, game_start_leveling, game_task_status, game_cancel, or game_action. Do not move, battle, spend, chat, mail, allocate stats, or change any game state. Call game_observe once. Then call game_memory_write with key `" + liveCodexLifeNoteKey + "` and text `" + liveCodexLifeNote + "`. Then call game_schedule_create with kind `reminder`, title `life heartbeat reminder`, prompt `confirm the first heartbeat note`, delay_seconds 300, and idempotency_key `" + liveCodexLifeReminderKey + "`. Return exactly STONEAGE_LIFE_CODEX_FIRST_OK and nothing else after those calls.", nil
	}
}

func lifeCodexSecondPrompt() func(airuntime.Profile, aisupervisor.Snapshot) (string, error) {
	return func(airuntime.Profile, aisupervisor.Snapshot) (string, error) {
		return "This is the second turn after a supervisor restart. Use only game_observe, game_memory_list, and game_schedule_list. Do not call game_query_knowledge, game_start_task, game_start_leveling, game_task_status, game_cancel, game_action, any memory write/delete, or any schedule create/cancel. Call game_observe, then game_memory_list and game_schedule_list. Verify that the private note key `" + liveCodexLifeNoteKey + "` has text `" + liveCodexLifeNote + "` and that the pending reminder has title `life heartbeat reminder` and prompt `confirm the first heartbeat note`. Return exactly STONEAGE_LIFE_CODEX_SECOND_OK only after reading both lists and nothing else.", nil
	}
}

func lifeCodexHasNote(notes []airuntime.AgentNote, key, text string) bool {
	for _, note := range notes {
		if note.Key == key && note.Text == text {
			return true
		}
	}
	return false
}

func lifeCodexHasPendingReminder(schedules []airuntime.Schedule) bool {
	for _, schedule := range schedules {
		if schedule.Kind == "reminder" && schedule.IdempotencyKey == liveCodexLifeReminderKey && schedule.Prompt == "confirm the first heartbeat note" && schedule.Status == airuntime.SchedulePending {
			return true
		}
	}
	return false
}

func waitLifeCodexLive(t *testing.T, ctx context.Context, predicate func() bool) {
	t.Helper()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		if predicate() {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("timed out waiting for life heartbeat QA")
		case <-ticker.C:
		}
	}
}

type lifeCodexLiveClock struct {
	mu  sync.Mutex
	now time.Time
}

func newLifeCodexLiveClock(now time.Time) *lifeCodexLiveClock {
	return &lifeCodexLiveClock{now: now.UTC()}
}

func (clock *lifeCodexLiveClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *lifeCodexLiveClock) Advance(delta time.Duration) {
	clock.mu.Lock()
	clock.now = clock.now.Add(delta)
	clock.mu.Unlock()
}

type lifeCodexCountingFactory struct {
	Factory *Factory
	mu      sync.Mutex
	runner  *lifeCodexCountingRunner
}

func (factory *lifeCodexCountingFactory) Open(ctx context.Context, profile airuntime.Profile) (aisupervisor.AgentSession, error) {
	session, err := factory.Factory.Open(ctx, profile)
	if err != nil {
		return aisupervisor.AgentSession{}, err
	}
	runner := &lifeCodexCountingRunner{Runner: session.Runner}
	factory.mu.Lock()
	factory.runner = runner
	factory.mu.Unlock()
	session.Runner = runner
	return session, nil
}

func (factory *lifeCodexCountingFactory) Close() error {
	if factory == nil || factory.Factory == nil {
		return nil
	}
	return factory.Factory.Close()
}

func (factory *lifeCodexCountingFactory) countingRunner() *lifeCodexCountingRunner {
	factory.mu.Lock()
	defer factory.mu.Unlock()
	return factory.runner
}

func (factory *lifeCodexCountingFactory) runnerRequests() []aicodex.RunRequest {
	runner := factory.countingRunner()
	if runner == nil {
		return nil
	}
	return runner.requestsSnapshot()
}

func (factory *lifeCodexCountingFactory) runnerResults() []aicodex.Result {
	runner := factory.countingRunner()
	if runner == nil {
		return nil
	}
	return runner.resultsSnapshot()
}

type lifeCodexCountingRunner struct {
	Runner   aisupervisor.Runner
	mu       sync.Mutex
	requests []aicodex.RunRequest
	results  []aicodex.Result
}

func (runner *lifeCodexCountingRunner) Run(ctx context.Context, request aicodex.RunRequest) (aicodex.Result, error) {
	runner.mu.Lock()
	if len(runner.requests) >= 1 {
		runner.mu.Unlock()
		return aicodex.Result{}, errors.New("life QA rejects an unexpected extra model dispatch in the same phase")
	}
	runner.requests = append(runner.requests, request)
	runner.mu.Unlock()
	result, err := runner.Runner.Run(ctx, request)
	runner.mu.Lock()
	runner.results = append(runner.results, result)
	runner.mu.Unlock()
	return result, err
}

func (runner *lifeCodexCountingRunner) requestsSnapshot() []aicodex.RunRequest {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	return append([]aicodex.RunRequest(nil), runner.requests...)
}

func (runner *lifeCodexCountingRunner) resultsSnapshot() []aicodex.Result {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	return append([]aicodex.Result(nil), runner.results...)
}

type lifeCodexReadOnlySession struct {
	GameSession GameSession
	trace       *lifeCodexLiveTrace
}

func (session *lifeCodexReadOnlySession) Observe(ctx context.Context) (aigame.Snapshot, error) {
	return session.GameSession.Observe(ctx)
}

func (session *lifeCodexReadOnlySession) ExecuteExpected(ctx context.Context, revision uint64, action aigame.Action) error {
	if session.trace != nil {
		session.trace.recordProtocol(action)
	}
	if action.Kind != aigame.ActionStatus || action.Command != "AI" {
		if session.trace != nil {
			session.trace.recordForbidden("protocol:" + string(action.Kind) + ":" + action.Command)
		}
		return errors.New("life heartbeat QA permits only status AI protocol reads")
	}
	return session.GameSession.ExecuteExpected(ctx, revision, action)
}

type lifeCodexReadOnlyBackend struct {
	*GameBackend
	trace *lifeCodexLiveTrace
}

func (backend *lifeCodexReadOnlyBackend) Observe(ctx context.Context, binding aimcp.Binding) (aimcp.Observation, error) {
	backend.trace.record("observe")
	return backend.GameBackend.Observe(ctx, binding)
}

func (backend *lifeCodexReadOnlyBackend) QueryKnowledge(context.Context, aimcp.Binding, aimcp.KnowledgeQuery) (aimcp.KnowledgeResult, error) {
	backend.trace.recordForbidden("knowledge")
	return aimcp.KnowledgeResult{}, aimcp.ErrBackend
}

func (backend *lifeCodexReadOnlyBackend) StartTask(context.Context, aimcp.Binding, aimcp.TaskRequest) (aimcp.TaskReceipt, error) {
	backend.trace.recordForbidden("start_task")
	return aimcp.TaskReceipt{}, aimcp.ErrBackend
}

func (backend *lifeCodexReadOnlyBackend) StartLeveling(context.Context, aimcp.Binding, aimcp.LevelingRequest) (aimcp.TaskReceipt, error) {
	backend.trace.recordForbidden("start_leveling")
	return aimcp.TaskReceipt{}, aimcp.ErrBackend
}

func (backend *lifeCodexReadOnlyBackend) TaskStatus(context.Context, aimcp.Binding, string) (aimcp.TaskReceipt, error) {
	backend.trace.recordForbidden("task_status")
	return aimcp.TaskReceipt{}, aimcp.ErrBackend
}

func (backend *lifeCodexReadOnlyBackend) Cancel(context.Context, aimcp.Binding, aimcp.CancelRequest) (aimcp.TaskReceipt, error) {
	backend.trace.recordForbidden("cancel")
	return aimcp.TaskReceipt{}, aimcp.ErrBackend
}

func (backend *lifeCodexReadOnlyBackend) GameAction(context.Context, aimcp.Binding, aimcp.TypedAction) (aimcp.ActionReceipt, error) {
	backend.trace.recordForbidden("action")
	return aimcp.ActionReceipt{}, aimcp.ErrBackend
}

func (backend *lifeCodexReadOnlyBackend) WriteAgentNote(ctx context.Context, binding aimcp.Binding, request aimcp.AgentNoteWriteRequest) (aimcp.AgentNote, error) {
	backend.trace.record("memory_write")
	return backend.GameBackend.WriteAgentNote(ctx, binding, request)
}

func (backend *lifeCodexReadOnlyBackend) ListAgentNotes(ctx context.Context, binding aimcp.Binding, request aimcp.AgentNoteListRequest) (aimcp.AgentNoteList, error) {
	backend.trace.record("memory_list")
	return backend.GameBackend.ListAgentNotes(ctx, binding, request)
}

func (backend *lifeCodexReadOnlyBackend) DeleteAgentNote(ctx context.Context, binding aimcp.Binding, request aimcp.AgentNoteDeleteRequest) error {
	backend.trace.record("memory_delete")
	return backend.GameBackend.DeleteAgentNote(ctx, binding, request)
}

func (backend *lifeCodexReadOnlyBackend) CreateSchedule(ctx context.Context, binding aimcp.Binding, request aimcp.ScheduleRequest) (aimcp.Schedule, error) {
	backend.trace.record("schedule_create")
	return backend.GameBackend.CreateSchedule(ctx, binding, request)
}

func (backend *lifeCodexReadOnlyBackend) ListSchedules(ctx context.Context, binding aimcp.Binding, request aimcp.ScheduleListRequest) (aimcp.ScheduleList, error) {
	backend.trace.record("schedule_list")
	return backend.GameBackend.ListSchedules(ctx, binding, request)
}

func (backend *lifeCodexReadOnlyBackend) CancelSchedule(ctx context.Context, binding aimcp.Binding, request aimcp.ScheduleCancelRequest) (aimcp.Schedule, error) {
	backend.trace.record("schedule_cancel")
	return backend.GameBackend.CancelSchedule(ctx, binding, request)
}

type lifeCodexLiveTrace struct {
	mu        sync.Mutex
	calls     []string
	forbidden []string
	protocol  []aigame.Action
}

func (trace *lifeCodexLiveTrace) record(name string) {
	if trace == nil {
		return
	}
	trace.mu.Lock()
	trace.calls = append(trace.calls, name)
	trace.mu.Unlock()
}

func (trace *lifeCodexLiveTrace) recordForbidden(name string) {
	if trace == nil {
		return
	}
	trace.mu.Lock()
	trace.calls = append(trace.calls, name)
	trace.forbidden = append(trace.forbidden, name)
	trace.mu.Unlock()
}

func (trace *lifeCodexLiveTrace) recordProtocol(action aigame.Action) {
	if trace == nil {
		return
	}
	trace.mu.Lock()
	trace.protocol = append(trace.protocol, action)
	trace.mu.Unlock()
}

func (trace *lifeCodexLiveTrace) callCount() int {
	trace.mu.Lock()
	defer trace.mu.Unlock()
	return len(trace.calls)
}

func (trace *lifeCodexLiveTrace) hasCalls(names ...string) bool {
	if trace == nil {
		return false
	}
	trace.mu.Lock()
	defer trace.mu.Unlock()
	seen := make(map[string]bool, len(trace.calls))
	for _, call := range trace.calls {
		seen[call] = true
	}
	for _, name := range names {
		if !seen[name] {
			return false
		}
	}
	return true
}

func (trace *lifeCodexLiveTrace) verifyReadOnly() error {
	trace.mu.Lock()
	defer trace.mu.Unlock()
	if len(trace.forbidden) != 0 {
		return errors.New("forbidden calls were attempted")
	}
	if len(trace.protocol) == 0 {
		return errors.New("no protocol observations were recorded")
	}
	for _, action := range trace.protocol {
		if action.Kind != aigame.ActionStatus || action.Command != "AI" {
			return errors.New("protocol guard observed a non-read-only action")
		}
	}
	for _, call := range trace.calls {
		switch call {
		case "observe", "memory_write", "memory_list", "memory_delete", "schedule_create", "schedule_list", "schedule_cancel":
		default:
			return errors.New("unexpected game capability call")
		}
	}
	return nil
}
