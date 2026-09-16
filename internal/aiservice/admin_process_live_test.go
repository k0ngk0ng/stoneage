package aiservice

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aimcp"
	"github.com/k0ngk0ng/stoneage/internal/aimodels"
	"github.com/k0ngk0ng/stoneage/internal/airuntime"
)

const adminProcessLiveOptIn = "STONEAGE_ADMIN_PROCESS_LIVE_TEST"

// This test runs the actual admin binary against an isolated QA character.
// It consumes model tokens and creates a persistent QA account only when opted in.
func TestLiveStoneAgeAdminProcessRestart(t *testing.T) {
	if os.Getenv(adminProcessLiveOptIn) != "1" {
		t.Skip("set STONEAGE_ADMIN_PROCESS_LIVE_TEST=1 for real admin restart QA")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Minute)
	defer cancel()
	f := movementCrossMapLiveProvisionFreshAIHometown(t, ctx, "admin-restart", "admin-process-live-test", 0)
	f.Lease.Close()
	f.Lease.Close = nil
	codex := realGameFactoryCodex
	_ = realGameFactoryCodexVersion(t, f.Root, codex)
	key, err := readFactoryLiveKey(filepath.Join(f.RepoRoot, "vendor", "deepseek", "key"))
	if err != nil {
		t.Fatal("read private model key")
	}
	secrets, err := airuntime.NewSecretStore(filepath.Join(f.Root, "model-secrets"))
	if err != nil {
		t.Fatal("create model secrets")
	}
	model, err := f.Profiles.CreateModelConfig(ctx, airuntime.ModelConfig{
		ID: "admin-restart-model", Name: "admin restart QA", Backend: airuntime.ModelBackendCodex,
		Provider: aimodels.DeepSeekProvider, BaseURL: aimodels.DeepSeekBaseURL, Model: aimodels.DeepSeekFlash,
		WireAPI: airuntime.ModelProviderResponses, ReasoningEffort: airuntime.ReasoningEffortHigh,
		Timeout: 3 * time.Minute, MaxOutputTokens: 1024, HasKey: true,
	})
	if err != nil {
		t.Fatal("create model configuration")
	}
	if err := secrets.WriteKey(model.ID, key); err != nil {
		t.Fatal("write model secret")
	}
	installer, err := aimcp.NewSkillInstaller(filepath.Join(f.RepoRoot, "ai", "skills"))
	if err != nil {
		t.Fatal("create skill installer")
	}
	spec, err := installer.Verify("stoneage-play")
	if err != nil {
		t.Fatal("verify play skill")
	}
	skills := []airuntime.SkillVersion{{Name: spec.Name, Version: spec.Version, Kind: airuntime.SkillKindNative, Digest: "sha256:" + spec.SHA256}}
	goal := airuntime.Goal{Kind: "life", Description: `Read-only process restart QA. Each turn call game_observe, game_memory_list and game_schedule_list. If private note admin-restart-note is missing, use game_memory_write to save exactly "process restart memory" under that key. If reminder with idempotency key admin-restart-reminder is missing, use game_schedule_create to create it with delay_seconds=1800, title="process restart reminder", prompt="remember process restart memory". Otherwise preserve both unchanged. Do not use game_action, game_start_task, game_start_leveling or other game mutations. After both are present return ADMIN_RESTART_OK and wait for the next heartbeat.`, Life: &airuntime.LifePolicy{DecisionIntervalSeconds: 60, Activities: []string{"memory-plan"}}}
	// Codex usage aggregates repeated input across its internal tool loop,
	// including cached context. Bound the process check, which requires at
	// least three turns and can also wake on initial game synchronization.
	budget := int64(3000000)
	status := airuntime.ProfileStatusActive
	f.Profile, err = f.Profiles.UpdateProfileCAS(ctx, f.Profile.ID, f.Profile.Version, airuntime.ProfilePatch{ModelConfigID: &model.ID, Goal: &goal, Skills: &skills, DailyTokenBudget: &budget, Status: &status, Actor: "admin-process-live-test"})
	if err != nil {
		t.Fatal("configure life profile")
	}
	for _, target := range []string{"stoneage-admin", "stoneage-game-mcp"} {
		cmd := exec.CommandContext(ctx, "go", "build", "-mod=readonly", "-o", filepath.Join(f.Root, "bin", target), "./cmd/"+target)
		cmd.Dir = f.RepoRoot
		cmd.Env = movementCrossMapLiveGoEnvironment(f.Root)
		if err := cmd.Run(); err != nil {
			t.Fatal("build isolated admin/MCP binaries")
		}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("reserve loopback port")
	}
	address := listener.Addr().String()
	_ = listener.Close()

	start := func() *adminProcess {
		cmd := adminProcessLiveCommand(f, codex, address, filepath.Join(f.Root, "player-admin"))
		p := startAdminProcess(t, cmd)
		client := &http.Client{Timeout: time.Second}
		readyCtx, stop := context.WithTimeout(ctx, 20*time.Second)
		defer stop()
		waitLifeCodexLive(t, readyCtx, func() bool {
			select {
			case err := <-p.done:
				p.done <- err
				t.Fatal("admin exited before HTTP readiness")
			default:
			}
			response, err := client.Get("http://" + address + "/login")
			if err != nil {
				return false
			}
			_ = response.Body.Close()
			return response.StatusCode < 500
		})
		return p
	}
	first := start()
	checkpoint, eventID := waitAdminProcessTurn(t, ctx, f, 0, time.Time{}, key)
	t.Log("first process completed model turn and persisted note/reminder")
	first.stop(t)
	if profile, err := f.Profiles.GetProfile(ctx, f.Profile.ID); err != nil || profile.Status != airuntime.ProfileStatusActive {
		t.Fatal("graceful shutdown changed durable active status")
	}
	second := start()
	resumed, resumedEvent := waitAdminProcessTurn(t, ctx, f, eventID, time.Time{}, key)
	t.Log("second process completed model turn")
	if checkpoint.ThreadID == "" || resumed.ThreadID != checkpoint.ThreadID {
		t.Fatal("restart did not resume the same Codex thread")
	}
	// Leave the restarted process alone until an ordinary wall-clock life
	// boundary has passed. No fake clock, /start request or game action wakes it.
	heartbeat, _ := waitAdminProcessTurn(t, ctx, f, resumedEvent, resumed.NextDecisionAt, key)
	if heartbeat.ThreadID != checkpoint.ThreadID {
		t.Fatal("natural heartbeat changed the Codex thread")
	}
	t.Log("restarted process completed a subsequent natural wall-clock heartbeat")
	second.stop(t)
	notes, err := f.Profiles.ListAgentNotes(ctx, f.Profile.ID, 20)
	if err != nil || !lifeCodexHasNote(notes, "admin-restart-note", "process restart memory") {
		t.Fatal("durable memory lost")
	}
	reminders, err := f.Profiles.ListSchedules(ctx, f.Profile.ID, 20)
	if err != nil || len(reminders) != 1 || reminders[0].IdempotencyKey != "admin-restart-reminder" || reminders[0].Status != airuntime.SchedulePending {
		t.Fatal("durable reminder lost or duplicated")
	}
	if _, err := f.Profiles.PendingTokenAttempt(ctx, f.Profile.ID); !errors.Is(err, airuntime.ErrNotFound) {
		t.Fatal("model attempt remained unsettled")
	}
	usage, err := f.Profiles.TokenUsage(ctx, f.Profile.ID, time.Now().UTC())
	if err != nil {
		t.Fatal("read process QA token accounting")
	}
	t.Logf("process QA usage: %+v", usage)
	t.Log("actual admin SIGTERM/restart passed: active profile restored automatically, original thread/memory/reminder retained, real model turns completed")
}

type adminProcessCheckpoint struct {
	ThreadID         string    `json:"thread_id"`
	PendingAttemptID string    `json:"pending_attempt_id"`
	ModelTurnDone    bool      `json:"model_turn_done"`
	WaitingForWake   bool      `json:"waiting_for_wake"`
	NextDecisionAt   time.Time `json:"next_decision_at"`
}

func waitAdminProcessTurn(t *testing.T, ctx context.Context, f *movementCrossMapLiveFreshAI, after int64, notBefore time.Time, key string) (adminProcessCheckpoint, int64) {
	t.Helper()
	var saved adminProcessCheckpoint
	var latest int64
	var completedAt time.Time
	waitLifeCodexLive(t, ctx, func() bool {
		profile, err := f.Profiles.GetProfile(ctx, f.Profile.ID)
		if err != nil {
			return false
		}
		if profile.Status != airuntime.ProfileStatusActive {
			usage, _ := f.Profiles.TokenUsage(ctx, f.Profile.ID, time.Now().UTC())
			t.Logf("stopped process QA usage: %+v", usage)
			t.Fatalf("AI stopped during process QA: status=%s", profile.Status)
		}
		events, err := f.Profiles.ListEvents(ctx, f.Profile.ID, 1000)
		if err != nil {
			t.Fatalf("read audit events: %s", strings.ReplaceAll(err.Error(), key, "[redacted]"))
		}
		for _, event := range events {
			if event.Kind == "supervisor.turn_completed" && event.ID > latest {
				latest = event.ID
				completedAt = event.CreatedAt
			}
		}
		if latest <= after || completedAt.Before(notBefore) {
			return false
		}
		cp, err := f.Profiles.GetCheckpoint(ctx, f.Profile.ID)
		if err != nil {
			t.Fatal("read completed checkpoint")
		}
		// Omitted JSON fields must clear the previous polling snapshot too.
		// In particular pending_attempt_id disappears once a turn is settled.
		saved = adminProcessCheckpoint{}
		if err := json.Unmarshal(cp.State, &saved); err != nil {
			t.Fatal("decode completed checkpoint: ", err)
		}
		notes, err := f.Profiles.ListAgentNotes(ctx, f.Profile.ID, 20)
		if err != nil {
			t.Fatalf("read notes: %s", strings.ReplaceAll(err.Error(), key, "[redacted]"))
		}
		if !lifeCodexHasNote(notes, "admin-restart-note", "process restart memory") {
			return false
		}
		schedules, err := f.Profiles.ListSchedules(ctx, f.Profile.ID, 20)
		if err != nil {
			t.Fatalf("read reminders: %s", strings.ReplaceAll(err.Error(), key, "[redacted]"))
		}
		if len(schedules) != 1 {
			return false
		}
		return saved.ModelTurnDone && saved.WaitingForWake && saved.PendingAttemptID == "" && !saved.NextDecisionAt.IsZero()
	})
	return saved, latest
}

func adminProcessLiveCommand(f *movementCrossMapLiveFreshAI, codex, address, playerRoot string) *exec.Cmd {
	data := filepath.Join(f.RepoRoot, "runtime", "legacy-server", "gmsv", "data")
	args := []string{"serve", "-listen", address, "-db", filepath.Join(f.Root, "auth.db"), "-ai-db", filepath.Join(f.Root, "profiles.db"),
		"-ai-secrets", filepath.Join(f.Root, "model-secrets"), "-ai-runtime-root", f.Root,
		"-ai-game-address", f.Gateway.Address(), "-ai-codex-binary", codex,
		"-ai-mcp-binary", filepath.Join(f.Root, "bin", "stoneage-game-mcp"), "-ai-skill-root", filepath.Join(f.RepoRoot, "ai", "skills"),
		"-ai-funding-dir", filepath.Join(f.Root, "funding"), "-ai-knowledge-data-dir", data, "-ai-map-data-dir", data,
		"-ai-automation-db", filepath.Join(f.Root, "automation.db"), "-ai-receipt-db", filepath.Join(f.Root, "receipts.db"),
		"-ai-codex-work-root", filepath.Join(f.Root, "connection-work"), "-ai-codex-state-root", filepath.Join(f.Root, "connection-state"),
		"-player-admin-root", playerRoot, "-player-catalog-root", data}
	cmd := exec.Command(filepath.Join(f.Root, "bin", "stoneage-admin"), args...)
	cmd.Dir = f.Root
	cmd.Env = realGameFactoryIsolatedEnvironment(f.Root)
	for i, entry := range cmd.Env {
		if strings.HasPrefix(entry, "CODEX_HOME=") {
			cmd.Env[i] = "CODEX_HOME=" + filepath.Join(f.Root, "admin-codex-home")
		}
	}
	return cmd
}
