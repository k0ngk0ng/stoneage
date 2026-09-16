package aiservice

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aicontrol"
	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
	"github.com/k0ngk0ng/stoneage/internal/aimodels"
	"github.com/k0ngk0ng/stoneage/internal/airunner"
)

type containerLiveReadBackend struct {
	aimcp.UnavailableBackend
	game     *GameBackend
	mu       sync.Mutex
	observed []aimcp.Observation
	denied   []string
}

func (b *containerLiveReadBackend) Observe(ctx context.Context, binding aimcp.Binding) (aimcp.Observation, error) {
	o, err := b.game.Observe(ctx, binding)
	if err == nil {
		b.mu.Lock()
		b.observed = append(b.observed, o)
		b.mu.Unlock()
	}
	return o, err
}

func (b *containerLiveReadBackend) deny(operation string) error {
	b.mu.Lock()
	b.denied = append(b.denied, operation)
	b.mu.Unlock()
	return aimcp.ErrBackend
}
func (b *containerLiveReadBackend) QueryKnowledge(context.Context, aimcp.Binding, aimcp.KnowledgeQuery) (aimcp.KnowledgeResult, error) {
	return aimcp.KnowledgeResult{}, b.deny("knowledge")
}
func (b *containerLiveReadBackend) StartTask(context.Context, aimcp.Binding, aimcp.TaskRequest) (aimcp.TaskReceipt, error) {
	return aimcp.TaskReceipt{}, b.deny("start_task")
}
func (b *containerLiveReadBackend) StartLeveling(context.Context, aimcp.Binding, aimcp.LevelingRequest) (aimcp.TaskReceipt, error) {
	return aimcp.TaskReceipt{}, b.deny("start_leveling")
}
func (b *containerLiveReadBackend) TaskStatus(context.Context, aimcp.Binding, string) (aimcp.TaskReceipt, error) {
	return aimcp.TaskReceipt{}, b.deny("task_status")
}
func (b *containerLiveReadBackend) Cancel(context.Context, aimcp.Binding, aimcp.CancelRequest) (aimcp.TaskReceipt, error) {
	return aimcp.TaskReceipt{}, b.deny("cancel")
}
func (b *containerLiveReadBackend) GameAction(context.Context, aimcp.Binding, aimcp.TypedAction) (aimcp.ActionReceipt, error) {
	return aimcp.ActionReceipt{}, b.deny("action")
}

// Two actual Linux Codex processes share only their profile state. Both read
// a freshly provisioned real QA character through the production Gateway.
// All game mutation endpoints are unavailable; only S("AI") can reach GMSV.
func TestLiveContainerCodexRealGameResume(t *testing.T) {
	if os.Getenv("STONEAGE_CONTAINER_REAL_GAME_LIVE_TEST") != "1" {
		t.Skip("explicit real-game/container/provider opt-in required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	// Reviewed initialization and persistent/social identity build; provenance:
	// build/ai/initial-state-qa-20260916/manifest.json.
	const binaryHash = "82c7dc13286ad9cab181042a6bb46f1ffce33aa7023e969c7f475eb3b048689e"
	binary, err := exec.CommandContext(ctx, "docker", "exec", "stoneage-player-qa-gmsv-1", "sha256sum", "/proc/1/exe").Output()
	if err != nil || len(strings.Fields(string(binary))) != 2 || strings.Fields(string(binary))[0] != binaryHash {
		t.Fatal("reviewed isolated QA game binary required")
	}
	f := movementCrossMapLiveProvisionFreshAI(t, ctx, "container-observe", "container-observe-live-test")
	key, err := readFactoryLiveKey(filepath.Join(f.RepoRoot, "vendor", "deepseek", "key"))
	if err != nil {
		t.Fatal("private provider key unavailable")
	}
	gameSession, ok := f.Lease.Session.(*aigame.Session)
	if !ok {
		t.Fatal("expected raw QA protocol session")
	}
	readOnly := &realGameFactorySession{session: gameSession}
	gate := aicontrol.New()
	defer gate.Close()
	control, _, err := gate.Switch(gate.State().Generation, aicontrol.Agent, "container observation QA")
	if err != nil {
		t.Fatal(err)
	}
	binding := aimcp.Binding{AccountID: f.Created.Account.Username, CharacterID: f.Created.Binding.CharacterID, CharacterName: f.Created.Binding.CharacterName, Generation: control.Generation}
	game := &GameBackend{Binding: binding, Gate: gate, Owner: aicontrol.Agent, Session: readOnly, OwnStateRefresh: &OwnStateRefresher{}}
	initial, err := movementCrossMapLiveWaitObservation(ctx, game, binding, func(o aimcp.Observation) bool {
		return o.Connected && o.Ready && o.Phase == string(aigame.PhaseWorld) && o.Flags["inventory:known"] && o.Flags["savepoint:0"] && o.Floor == 1006 && o.Character.HP > 0 && o.Character.MaxHP > 0 && o.Character.Level > 0
	})
	if err != nil {
		t.Fatal("fresh authoritative state unavailable:", err)
	}
	stable, err := waitRealGameFactoryStablePet(ctx, readOnly)
	if err != nil || !stable.AI.SavePointsKnown {
		t.Fatal("authoritative pet/savepoint identity unavailable")
	}
	stablePetIDs := []string{}
	for _, pet := range stable.Pets {
		if pet.IdentityKnown && pet.StableID != "" {
			stablePetIDs = append(stablePetIDs, pet.StableID)
		}
	}
	backend := &containerLiveReadBackend{game: game}
	gateway := NewGateway()
	defer gateway.Close()
	token, revoke, err := gateway.Register(binding, backend)
	if err != nil {
		t.Fatal(err)
	}
	defer revoke()
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(gateway)
	server.Listener = listener
	server.Start()
	defer server.Close()
	endpoint := fmt.Sprintf("http://host.docker.internal:%d/v1/game", listener.Addr().(*net.TCPAddr).Port)
	artifacts := filepath.Join(f.RepoRoot, "build", "ai", "container-runtime-qa")
	state := filepath.Join(f.Root, "container-state")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	imageRaw, err := exec.CommandContext(ctx, "docker", "image", "inspect", "--format", "{{.Id}}", "gcc:13-bookworm").Output()
	if err != nil {
		t.Fatal("existing Linux image required; no pull attempted")
	}
	image := strings.TrimSpace(string(imageRaw))
	if os.Getuid() == 0 {
		t.Fatal("non-root bind owner required")
	}
	uid := fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	recall := hex.EncodeToString(nonce[:])
	request := airunner.ExecuteRequest{ProfileID: "real-game-container", RequestID: "observe-1",
		Run:    airunner.RunRequest{Prompt: "Read the complete installed .agents/skills/stoneage-play/SKILL.md using the shell tool and follow it. Remember this code for a later turn: " + recall + ". Call game_observe exactly once. Return only a JSON object with name=character_name, floor, x, y, hp=character.hp from the response. Never write to the game."},
		Model:  airunner.Model{Provider: aimodels.DeepSeekProvider, BaseURL: aimodels.DeepSeekBaseURL, Model: aimodels.DeepSeekFlash, ReasoningEffort: "high", APIKey: key},
		Skills: []airunner.Skill{{Name: "stoneage-play"}},
		MCP:    airunner.MCP{Endpoint: endpoint, Token: token, CharacterID: binding.CharacterID, CharacterName: binding.CharacterName, Generation: binding.Generation},
	}
	runner := containerLiveRunner{Context: ctx, RepoRoot: f.RepoRoot, Artifacts: artifacts, State: state, Image: image, UID: uid, Key: key, Token: token, NameSuffix: recall[:12]}
	run := func(request airunner.ExecuteRequest) airunner.Result { return runner.Run(t, request) }
	first := run(request)
	if !containerLiveSkillRead(first) {
		t.Fatal("native Skill read not observed")
	}
	request.RequestID = "observe-2"
	request.Run = airunner.RunRequest{Resume: true, ThreadID: first.ThreadID, Prompt: "Recall the code I asked you to remember. Call game_observe exactly once again. Return only JSON with name=character_name, floor, x, y, hp=character.hp from this new tool response, plus recall containing the earlier code. Never write to the game."}
	second := run(request)
	if second.ThreadID != first.ThreadID {
		t.Fatal("resume created a different thread")
	}
	backend.mu.Lock()
	observed := append([]aimcp.Observation(nil), backend.observed...)
	denied := append([]string(nil), backend.denied...)
	backend.mu.Unlock()
	if len(denied) != 0 {
		t.Fatalf("model attempted non-observation operations: %v", denied)
	}
	if len(observed) != 2 {
		t.Fatalf("expected two real MCP observations, got %d", len(observed))
	}
	for i, result := range []airunner.Result{first, second} {
		var reply struct {
			Name   string `json:"name"`
			Floor  int    `json:"floor"`
			X      int    `json:"x"`
			Y      int    `json:"y"`
			HP     int    `json:"hp"`
			Recall string `json:"recall"`
		}
		o := observed[i]
		if json.Unmarshal([]byte(result.LastMessage), &reply) != nil || reply.Name != o.CharacterName || reply.Floor != o.Floor || reply.X != o.X || reply.Y != o.Y || reply.HP != o.Character.HP || (i == 1 && reply.Recall != recall) {
			t.Fatal("model reply did not match real observation or persisted recall")
		}
		if !o.Connected || o.Phase != string(aigame.PhaseWorld) || !o.Ready || !o.Flags["savepoint:0"] || o.Character.Level != initial.Character.Level || o.Character.MaxHP != initial.Character.MaxHP || !o.Flags["inventory:known"] || o.CharacterID != binding.CharacterID || o.CharacterName != binding.CharacterName || o.Floor != initial.Floor || o.X != initial.X || o.Y != initial.Y || o.Character.HP != initial.Character.HP {
			t.Fatal("real game identity or state changed")
		}
		for _, id := range stablePetIDs {
			found := false
			for _, pet := range o.Pets {
				if pet.ID == id {
					found = true
				}
			}
			if !found {
				t.Fatal("stable pet identity missing from model observation")
			}
		}
	}
	for _, action := range readOnly.actionsSnapshot() {
		if action.Kind != aigame.ActionStatus || action.Command != "AI" {
			t.Fatal("non-observation protocol action attempted")
		}
	}
	revoke()
	httpRequest, err := http.NewRequestWithContext(ctx, "POST", server.URL+"/v1/game", strings.NewReader(`{"operation":"observe","arguments":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	httpRequest.Header.Set("Authorization", "Bearer "+token)
	httpRequest.Header.Set("Content-Type", "application/json")
	revoked, err := server.Client().Do(httpRequest)
	if err != nil {
		t.Fatal("check revoked capability")
	}
	revoked.Body.Close()
	if revoked.StatusCode != http.StatusUnauthorized {
		t.Fatal("revoked capability still usable")
	}
	evidence := map[string]any{"test": t.Name(), "passed": true, "model": aimodels.DeepSeekFlash, "qa_binary_sha256": binaryHash, "image": image, "real_game_verified": true, "released_image_verified": false, "broker_verified": false, "native_skill_read": true, "resume_thread_verified": true, "recall_verified": true, "capability_revocation_verified": true, "mcp_observe_calls": len(observed), "game_mutations": 0, "non_observation_attempts": len(denied), "actual_container_isolation_verified": true, "stable_pet_identity_verified": true, "savepoint_verified": true, "floor": initial.Floor, "x": initial.X, "y": initial.Y, "hp": initial.Character.HP, "first_usage": first.Usage, "resume_usage": second.Usage}
	raw, _ := json.MarshalIndent(evidence, "", "  ")
	if err := os.WriteFile(filepath.Join(artifacts, "real-game-evidence.json"), append(raw, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	t.Log("real QA observation, separate-container thread resume/recall and capability revocation passed")
}
