package airuntime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	testRoot := workspaceAITestRoot()
	if err := os.MkdirAll(testRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	directory, err := os.MkdirTemp(testRoot, "store-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	store, err := OpenStore(filepath.Join(directory, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func testProfile() Profile {
	return Profile{
		ID:            "agent-1",
		Account:       AccountIdentity{ID: "account-1", Username: "agent-account"},
		Character:     CharacterIdentity{ID: "character-1", Name: "Agent"},
		ModelConfigID: "model-1",
		Personality:   Personality{Name: "scout", Traits: []string{"careful"}},
		Goal:          Goal{Kind: "level", TargetLevel: 20, StopWhenCompleted: true},
		Skills: []SkillVersion{{
			Name: "walk", Version: "1.0.0", Description: "walk in the game",
			Parameters: json.RawMessage(`{"type":"object","properties":{"x":{"type":"integer"},"y":{"type":"integer"}},"required":["x","y"],"additionalProperties":false}`),
		}},
		UnlimitedFunds:     true,
		DailyTokenBudget:   20,
		ExternalSpendLimit: 500,
	}
}

func TestProfileCASAuditAndNoPasswordColumn(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	created, err := store.CreateProfileAs(ctx, testProfile(), "admin:1")
	if err != nil {
		t.Fatal(err)
	}
	if created.Version != 1 || !created.UnlimitedFunds || created.ModelConfigID != "model-1" {
		t.Fatalf("created profile = %#v", created)
	}
	var tableSQL string
	if err := store.DB().QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='ai_profiles'`).Scan(&tableSQL); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(tableSQL), "password") {
		t.Fatalf("profile schema contains password material: %s", tableSQL)
	}
	loaded, err := store.GetProfile(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Skills[0].Name = "mutated"
	loaded.Personality.Traits[0] = "mutated"
	unchanged, err := store.GetProfile(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Skills[0].Name != "walk" || unchanged.Personality.Traits[0] != "careful" {
		t.Fatal("GetProfile returned mutable persisted state")
	}

	unlimited := false
	updated, err := store.UpdateProfileCAS(ctx, created.ID, 1, ProfilePatch{
		UnlimitedFunds: &unlimited, Actor: "admin:1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Version != 2 || updated.UnlimitedFunds {
		t.Fatalf("updated profile = %#v", updated)
	}
	if _, err := store.UpdateProfileCAS(ctx, created.ID, 1, ProfilePatch{Actor: "stale"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale CAS error = %v", err)
	}
	events, err := store.ListEvents(ctx, created.ID, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Kind != EventProfileUpdated || events[1].Kind != EventProfileCreated {
		t.Fatalf("profile audit events = %#v", events)
	}
	if events[0].Actor != "admin:1" {
		t.Fatalf("audit actor = %q", events[0].Actor)
	}
}

func TestNativeSkillMetadataProfileCRUDDoesNotRequireFunctionSchema(t *testing.T) {
	store := testStore(t)
	profileInput := testProfile()
	profileInput.ID = "native-agent"
	profileInput.Status = ""
	profileInput.Skills = []SkillVersion{{
		Name:        "stoneage-leveling",
		Version:     "2026.09.15",
		Description: "verified StoneAge leveling skill",
		Kind:        SkillKindNative,
		Path:        "skills/stoneage-leveling/SKILL.md",
		Digest:      "sha256:example",
	}}
	created, err := store.CreateProfile(context.Background(), profileInput)
	if err != nil {
		t.Fatal(err)
	}
	if created.Status != ProfileStatusStopped {
		t.Fatalf("default profile status = %q, want stopped", created.Status)
	}
	if len(created.Skills) != 1 || created.Skills[0].Kind != SkillKindNative || len(created.Skills[0].Parameters) != 0 {
		t.Fatalf("native skill = %#v", created.Skills)
	}
	loaded, err := store.GetProfile(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Skills[0].Path != profileInput.Skills[0].Path || loaded.Skills[0].Digest != "sha256:example" {
		t.Fatalf("native skill provenance was not persisted: %#v", loaded.Skills[0])
	}
	updatedSkills := []SkillVersion{{Name: "stoneage-chat", Version: "2", Kind: SkillKindNative, Digest: "sha256:chat"}}
	updated, err := store.UpdateProfileCAS(context.Background(), created.ID, created.Version, ProfilePatch{Skills: &updatedSkills, Actor: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.Skills) != 1 || updated.Skills[0].Kind != SkillKindNative || updated.Skills[0].Digest != "sha256:chat" {
		t.Fatalf("updated native skill = %#v", updated.Skills)
	}
}

func TestMemoryAndCheckpointRequireConfirmedEventAndCAS(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	profileInput := testProfile()
	profileInput.Status = ProfileStatusActive
	profile, err := store.CreateProfile(ctx, profileInput)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordConfirmedMemory(ctx, profile.ID, MemoryInput{Kind: "relationship", Confirmed: false}); err == nil {
		t.Fatal("unconfirmed memory was accepted")
	}
	event, err := store.AppendEvent(ctx, profile.ID, "game.trade.completed", "game", json.RawMessage(`{"other":"human-1"}`))
	if err != nil {
		t.Fatal(err)
	}
	memory, err := store.RecordConfirmedMemory(ctx, profile.ID, MemoryInput{
		Kind: "relationship", Subject: "human-1", Content: json.RawMessage(`{"trust":1}`),
		SourceEventID: event.ID, Confirmed: true, Actor: "game",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !memory.Confirmed || memory.SourceEventID != event.ID {
		t.Fatalf("memory = %#v", memory)
	}
	memories, err := store.ListMemories(ctx, profile.ID, 10)
	if err != nil || len(memories) != 1 {
		t.Fatalf("memories = %#v, err=%v", memories, err)
	}

	checkpoint, err := store.SaveCheckpointCAS(ctx, profile.ID, 0, json.RawMessage(`{"step":"start"}`), "runtime")
	if err != nil || checkpoint.Version != 1 {
		t.Fatalf("initial checkpoint = %#v, err=%v", checkpoint, err)
	}
	if _, err := store.SaveCheckpointCAS(ctx, profile.ID, 0, json.RawMessage(`{"step":"stale"}`), "runtime"); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale checkpoint error = %v", err)
	}
	checkpoint, err = store.SaveCheckpointCAS(ctx, profile.ID, 1, json.RawMessage(`{"step":"next"}`), "runtime")
	if err != nil || checkpoint.Version != 2 {
		t.Fatalf("second checkpoint = %#v, err=%v", checkpoint, err)
	}
}

func TestTokenBudgetChargesFailuresAndRejectsOverBudget(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	profileInput := testProfile()
	profileInput.Status = ProfileStatusActive
	profile, err := store.CreateProfile(ctx, profileInput)
	if err != nil {
		t.Fatal(err)
	}
	day := time.Date(2026, 9, 15, 8, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	reservation, err := store.BeginTokenAttempt(ctx, profile.ID, day, 12)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.FinishTokenAttempt(ctx, reservation, TokenUsage{InputTokens: 2, OutputTokens: 3}, true); err != nil {
		t.Fatal(err)
	}
	usage, err := store.TokenUsage(ctx, profile.ID, day)
	if err != nil {
		t.Fatal(err)
	}
	if usage.ChargedTokens != 12 || usage.ReservedTokens != 0 || usage.FailedAttempts != 1 || usage.TotalTokens != 5 {
		t.Fatalf("usage after failed call = %#v", usage)
	}
	if _, err := store.BeginTokenAttempt(ctx, profile.ID, day, 9); !errors.Is(err, ErrTokenBudgetExceeded) {
		t.Fatalf("over-budget error = %v", err)
	}
	usage, err = store.TokenUsage(ctx, profile.ID, day)
	if err != nil {
		t.Fatal(err)
	}
	if usage.Attempts != 2 || usage.OverBudgetAttempts != 1 {
		t.Fatalf("usage after rejected call = %#v", usage)
	}
	if err := store.FinishTokenAttempt(ctx, reservation, TokenUsage{}, false); !errors.Is(err, ErrAttemptSettled) {
		t.Fatalf("double settlement error = %v", err)
	}
}

func TestModelConfigAndPrivateSecretStore(t *testing.T) {
	testRoot := workspaceAITestRoot()
	if err := os.MkdirAll(testRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	directory, err := os.MkdirTemp(testRoot, "model-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	secrets, err := NewSecretStore(filepath.Join(directory, "secrets"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := secrets.KeyPath("../escape"); err == nil {
		t.Fatal("secret store accepted path traversal")
	}
	if err := secrets.WriteKey("model-1", "sk-test-secret"); err != nil {
		t.Fatal(err)
	}
	key, err := secrets.ReadKey("model-1")
	if err != nil || key != "sk-test-secret" {
		t.Fatalf("read key = %q, err=%v", key, err)
	}
	path, _ := secrets.KeyPath("model-1")
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("key permissions = %v, err=%v", info.Mode().Perm(), err)
	}

	store, err := OpenWithSecrets(filepath.Join(directory, "state.db"), filepath.Join(directory, "secrets"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	config, err := store.CreateModelConfig(context.Background(), ModelConfig{
		ID: "model-1", Name: "primary", Backend: ModelBackendCodex, Provider: "openai",
		BaseURL: "https://example.test/v1", Model: "user-specified-model",
		Timeout: 4 * time.Second, MaxOutputTokens: 64, DailyTokenBudget: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := store.GetModelConfig(context.Background(), config.ID)
	if err != nil || !loaded.HasKey || loaded.ReasoningEffort != "" || loaded.Backend != ModelBackendCodex || loaded.WireAPI != ModelProviderResponses {
		t.Fatalf("loaded config = %#v, err=%v", loaded, err)
	}
	encoded, _ := json.Marshal(loaded)
	if strings.Contains(string(encoded), "sk-test-secret") || strings.Contains(string(encoded), "api_key") {
		t.Fatalf("model config leaked key: %s", encoded)
	}
	if _, err := store.UpdateModelConfigCAS(context.Background(), config.ID, 1, ModelConfigPatch{
		Model: strptr("another-user-model"), WireAPI: strptr("legacy_completions"), Actor: "admin",
	}); !errors.Is(err, ErrInvalidProvider) {
		t.Fatalf("unsupported wire API error = %v", err)
	}
	loaded, err = store.GetModelConfig(context.Background(), config.ID)
	if err != nil || loaded.Model != "user-specified-model" || loaded.WireAPI != ModelProviderResponses {
		t.Fatalf("rejected wire API mutated config = %#v, err=%v", loaded, err)
	}
	if _, err := store.UpdateModelConfigCAS(context.Background(), config.ID, 1, ModelConfigPatch{
		Model: strptr("another-user-model"), Actor: "admin",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateModelConfigCAS(context.Background(), config.ID, 1, ModelConfigPatch{}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale model CAS error = %v", err)
	}
	if err := store.SetDefaultModelConfigID(context.Background(), config.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	if defaultID, err := store.GetDefaultModelConfigID(context.Background()); err != nil || defaultID != config.ID {
		t.Fatalf("default model config = %q, err=%v", defaultID, err)
	}
	if err := store.SetDefaultModelConfigID(context.Background(), "", "admin"); err != nil {
		t.Fatal(err)
	}
	if defaultID, err := store.GetDefaultModelConfigID(context.Background()); err != nil || defaultID != "" {
		t.Fatalf("cleared default model config = %q, err=%v", defaultID, err)
	}
}

func TestGenericReasoningEffortsRoundTripAndDeepSeekDefaults(t *testing.T) {
	store := testStore(t)
	for index, effort := range []string{"", "medium", "xhigh"} {
		config, err := store.CreateModelConfig(context.Background(), ModelConfig{
			ID: "generic-reasoning-" + string(rune('a'+index)), Name: "generic " + effort,
			Backend: ModelBackendCodex, Provider: "openai", BaseURL: "https://example.test/v1",
			Model: "gpt-4o", ReasoningEffort: effort, Timeout: time.Second, MaxOutputTokens: 8,
		})
		if err != nil {
			t.Fatalf("create generic effort %q: %v", effort, err)
		}
		loaded, err := store.GetModelConfig(context.Background(), config.ID)
		if err != nil || loaded.ReasoningEffort != effort {
			t.Fatalf("generic effort %q loaded as %q, err=%v", effort, loaded.ReasoningEffort, err)
		}
	}
	flash, err := store.CreateModelConfig(context.Background(), ModelConfig{
		ID: "deepseek-default-reasoning", Name: "DeepSeek Flash", Backend: ModelBackendCodex,
		Provider: "deepseek", BaseURL: "https://api.deepseek.com", Model: "deepseek-flash",
		Timeout: time.Second, MaxOutputTokens: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	if flash.ReasoningEffort != ReasoningEffortHigh {
		t.Fatalf("DeepSeek default reasoning = %q", flash.ReasoningEffort)
	}
}

func TestModelConfigRejectsUnsupportedWireAPI(t *testing.T) {
	store := testStore(t)
	config := ModelConfig{
		ID: "wire-api-invalid", Name: "invalid wire API", Backend: ModelBackendCodex,
		Provider: "openai", BaseURL: "https://example.test/v1", Model: "model",
		WireAPI: "legacy_completions", ReasoningEffort: ReasoningEffortHigh,
		Timeout: time.Second, MaxOutputTokens: 8,
	}
	if _, err := store.CreateModelConfig(context.Background(), config); !errors.Is(err, ErrInvalidProvider) {
		t.Fatalf("unsupported wire API error = %v", err)
	}
}

func TestLegacyModelConfigDefaultsWireAPIToResponses(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE ai_model_configs (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		backend TEXT NOT NULL,
		provider TEXT NOT NULL,
		base_url TEXT NOT NULL,
		model TEXT NOT NULL,
		reasoning_effort TEXT NOT NULL DEFAULT 'high' CHECK (reasoning_effort IN ('low','high','max')),
		timeout_ns INTEGER NOT NULL,
		max_output_tokens INTEGER NOT NULL,
		daily_token_budget INTEGER NOT NULL,
		has_key INTEGER NOT NULL DEFAULT 0,
		version INTEGER NOT NULL,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO ai_model_configs
		(id,name,backend,provider,base_url,model,reasoning_effort,timeout_ns,max_output_tokens,daily_token_budget,has_key,version,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		"legacy-model", "legacy", ModelBackendCodex, "openai", "https://example.test/v1", "legacy-model",
		ReasoningEffortHigh, int64(time.Second), 8, 0, 0, 1,
		"2026-09-15T00:00:00Z", "2026-09-15T00:00:00Z")
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	loaded, err := store.GetModelConfig(context.Background(), "legacy-model")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.WireAPI != ModelProviderResponses {
		t.Fatalf("legacy wire API = %q, want %q", loaded.WireAPI, ModelProviderResponses)
	}
	if loaded.Model != "legacy-model" || loaded.ReasoningEffort != ReasoningEffortHigh {
		t.Fatalf("legacy model data was not preserved: %#v", loaded)
	}
	created, err := store.CreateModelConfig(context.Background(), ModelConfig{
		ID: "legacy-generic", Name: "legacy generic", Backend: ModelBackendCodex,
		Provider: "openai", BaseURL: "https://example.test/v1", Model: "gpt-4o",
		ReasoningEffort: "medium", Timeout: time.Second, MaxOutputTokens: 8,
	})
	if err != nil || created.ReasoningEffort != "medium" {
		t.Fatalf("generic reasoning after legacy migration = %#v, err=%v", created, err)
	}
	var tableSQL string
	if err := store.DB().QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='ai_model_configs'`).Scan(&tableSQL); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToLower(tableSQL), "'medium'") || !strings.Contains(strings.ToLower(tableSQL), "'xhigh'") {
		t.Fatalf("legacy schema was not rebuilt for generic reasoning: %s", tableSQL)
	}
}

func TestModelConfigRejectsURLCredentialsAndClientSideSelectors(t *testing.T) {
	store := testStore(t)
	base := ModelConfig{
		Name: "primary", Backend: ModelBackendCodex, Provider: "deepseek",
		Model: "deepseek-flash", Timeout: time.Second, MaxOutputTokens: 8,
	}
	for index, value := range []string{
		"https://user:password@example.test/v1",
		"https://example.test/v1?api_key=secret",
		"https://example.test/v1#fragment",
	} {
		candidate := base
		candidate.ID = "invalid-url-" + string(rune('a'+index))
		candidate.BaseURL = value
		if _, err := store.CreateModelConfig(context.Background(), candidate); !errors.Is(err, ErrInvalidProvider) {
			t.Fatalf("URL %q error = %v", value, err)
		}
	}
}

func strptr(value string) *string { return &value }

func workspaceAITestRoot() string {
	_, filename, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", "..", "build", "ai"))
}

func TestConcurrentProfileCASHasSingleWinner(t *testing.T) {
	store := testStore(t)
	profile, err := store.CreateProfile(context.Background(), testProfile())
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	winners := 0
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			value := true
			if _, err := store.UpdateProfileCAS(context.Background(), profile.ID, 1, ProfilePatch{UnlimitedFunds: &value}); err == nil {
				mu.Lock()
				winners++
				mu.Unlock()
			} else if !errors.Is(err, ErrConflict) {
				t.Errorf("concurrent CAS error = %v", err)
			}
		}()
	}
	wg.Wait()
	if winners != 1 {
		t.Fatalf("CAS winners = %d", winners)
	}
}
