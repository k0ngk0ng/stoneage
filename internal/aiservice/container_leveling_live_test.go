package aiservice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
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
	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
	"github.com/k0ngk0ng/stoneage/internal/aimodels"
	"github.com/k0ngk0ng/stoneage/internal/ainavigation"
	"github.com/k0ngk0ng/stoneage/internal/airunner"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

// Records real protocol submissions without replacing the session's behavior.
type containerLevelingSession struct {
	GameSession
	mu             sync.Mutex
	actions        map[aigame.ActionKind]int
	failures       []string
	petAttacks     int
	battleCommands []string
	itemUses       [][2]int32
	itemTemplates  []int32
	stockPurchases []string
	stockPositions []aigame.Point
}

func (s *containerLevelingSession) WaitForMapEvent(ctx context.Context, sequence int32) (aigame.Event, error) {
	acker, ok := s.GameSession.(interface {
		WaitForMapEvent(context.Context, int32) (aigame.Event, error)
	})
	if !ok {
		return aigame.Event{}, errors.New("traced session cannot acknowledge map events")
	}
	return acker.WaitForMapEvent(ctx, sequence)
}

func (s *containerLevelingSession) ExecuteExpected(ctx context.Context, revision uint64, action aigame.Action) error {
	var template int32
	var stockPosition aigame.Point
	if action.Kind == aigame.ActionWindow && action.WindowSequence == 242 {
		before, err := s.GameSession.Observe(ctx)
		if err == nil && before.Revision == revision {
			stockPosition = before.Position
		}
	}
	if action.Kind == aigame.ActionItem && action.Command == "" {
		before, err := s.GameSession.Observe(ctx)
		if err == nil && before.Revision == revision && before.AI.Received && before.AI.ItemsKnown {
			for _, item := range before.AI.Items {
				if item.Slot == action.Index {
					template = item.TemplateID
					break
				}
			}
		}
	}
	err := s.GameSession.ExecuteExpected(ctx, revision, action)
	if err == nil {
		s.mu.Lock()
		s.actions[action.Kind]++
		if action.Kind == aigame.ActionWindow && action.WindowSequence == 242 {
			s.stockPurchases = append(s.stockPurchases, action.Text)
			s.stockPositions = append(s.stockPositions, stockPosition)
		}
		if action.Kind == aigame.ActionItem && action.Command == "" {
			s.itemUses = append(s.itemUses, [2]int32{action.Index, action.TargetID})
			s.itemTemplates = append(s.itemTemplates, template)
		}
		if action.Kind == aigame.ActionBattle {
			s.battleCommands = append(s.battleCommands, action.Command)
		}
		if action.Kind == aigame.ActionBattle && strings.HasPrefix(action.Command, "W|") && action.Command != "W|FF|FF" {
			s.petAttacks++
		}
		s.mu.Unlock()
	} else {
		category := "execution_error"
		if errors.Is(err, aigame.ErrStaleRevision) {
			category = "stale_revision"
		}
		s.mu.Lock()
		s.failures = append(s.failures, string(action.Kind)+":"+category)
		s.mu.Unlock()
	}
	return err
}

// All calls go to the production backend. Only successful MCP results are
// recorded; no fixture result or test-side task start is supplied to the model.
type containerLevelingBackend struct {
	aimcp.Backend
	mu            sync.Mutex
	operations    []string
	observations  []aimcp.Observation
	queries       []aimcp.KnowledgeResult
	queryRequests []aimcp.KnowledgeQuery
	startReceipts []aimcp.TaskReceipt
	statusHandles []string
	mutatingCalls int
	closeCalls    int
	starts        []aimcp.LevelingRequest
	statuses      []aimcp.TaskReceipt
}

func (b *containerLevelingBackend) Close() {
	b.mu.Lock()
	b.closeCalls++
	b.mu.Unlock()
	if closer, ok := b.Backend.(interface{ Close() }); ok {
		closer.Close()
	}
}

func (b *containerLevelingBackend) Observe(ctx context.Context, binding aimcp.Binding) (aimcp.Observation, error) {
	r, err := b.Backend.Observe(ctx, binding)
	if err == nil {
		b.mu.Lock()
		b.operations = append(b.operations, "observe")
		b.observations = append(b.observations, r)
		b.mu.Unlock()
	}
	return r, err
}
func (b *containerLevelingBackend) QueryKnowledge(ctx context.Context, binding aimcp.Binding, q aimcp.KnowledgeQuery) (aimcp.KnowledgeResult, error) {
	r, err := b.Backend.QueryKnowledge(ctx, binding, q)
	if err == nil {
		b.mu.Lock()
		b.operations = append(b.operations, "knowledge")
		b.queries = append(b.queries, r)
		b.queryRequests = append(b.queryRequests, q)
		b.mu.Unlock()
	}
	return r, err
}
func (b *containerLevelingBackend) StartLeveling(ctx context.Context, binding aimcp.Binding, q aimcp.LevelingRequest) (aimcp.TaskReceipt, error) {
	r, err := b.Backend.StartLeveling(ctx, binding, q)
	b.mu.Lock()
	b.operations = append(b.operations, "start_leveling")
	b.starts = append(b.starts, q)
	b.startReceipts = append(b.startReceipts, r)
	b.mu.Unlock()
	return r, err
}
func (b *containerLevelingBackend) TaskStatus(ctx context.Context, binding aimcp.Binding, handle string) (aimcp.TaskReceipt, error) {
	b.mu.Lock()
	b.statusHandles = append(b.statusHandles, handle)
	b.mu.Unlock()
	r, err := b.Backend.TaskStatus(ctx, binding, handle)
	if err == nil {
		b.mu.Lock()
		b.operations = append(b.operations, "task_status")
		b.statuses = append(b.statuses, r)
		b.mu.Unlock()
	}
	return r, err
}

func (b *containerLevelingBackend) GameAction(ctx context.Context, binding aimcp.Binding, q aimcp.TypedAction) (aimcp.ActionReceipt, error) {
	b.mu.Lock()
	b.mutatingCalls++
	b.mu.Unlock()
	return b.Backend.GameAction(ctx, binding, q)
}
func (b *containerLevelingBackend) StartTask(ctx context.Context, binding aimcp.Binding, q aimcp.TaskRequest) (aimcp.TaskReceipt, error) {
	b.mu.Lock()
	b.mutatingCalls++
	b.mu.Unlock()
	return b.Backend.StartTask(ctx, binding, q)
}

func TestLiveContainerCodexLeveling(t *testing.T) {
	if os.Getenv("STONEAGE_CONTAINER_LEVELING_LIVE_TEST") != "1" {
		t.Skip("explicit real-game/container/provider opt-in required")
	}
	runLiveContainerCodexLeveling(t, "")
}

func runLiveContainerCodexLeveling(t *testing.T, brokerImage string) {
	var evidenceFiles []string
	// Registered first, so this runs after provider/funding/container cleanup.
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		raw, _ := json.Marshal(map[string]any{"test": t.Name(), "passed": false, "status": "failed"})
		for _, path := range evidenceFiles {
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Error("invalidate failed live evidence")
			}
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	// Reviewed initialization and persistent/social identity build; provenance:
	// build/ai/initial-state-qa-20260916/manifest.json.
	const binaryHash = "82c7dc13286ad9cab181042a6bb46f1ffce33aa7023e969c7f475eb3b048689e"
	raw, err := exec.CommandContext(ctx, "docker", "exec", "stoneage-player-qa-gmsv-1", "sha256sum", "/proc/1/exe").Output()
	fields := strings.Fields(string(raw))
	if err != nil || len(fields) != 2 || fields[0] != binaryHash {
		t.Fatal("reviewed QA binary required")
	}
	f := movementCrossMapLiveProvisionFreshAIHometown(t, ctx, "container-leveling", "container-leveling-live-test", 1)
	data := filepath.Join(f.RepoRoot, "runtime", "legacy-server", "gmsv", "data")
	knowledge, err := aiknowledge.LoadDataDir(ctx, data)
	if err != nil {
		t.Fatal(err)
	}
	tiles, err := ainavigation.LoadDataDir(ctx, data)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := movementCrossMapLiveVerifyEffectiveData(ctx, f.RepoRoot, tiles); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int{2006, 2000, 2004, 100} {
		floor, ok := tiles.Floor(id)
		if !ok {
			t.Fatalf("required hometown 1 map %d missing", id)
		}
		if _, err := movementCrossMapLiveCompareFile(data, movementCrossMapLiveEffectiveRoot(f.RepoRoot), filepath.Join("map", floor.Source)); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"enemybase.txt", "enemy.txt", "group.txt", "encount.txt", "petskill.txt"} {
		if _, err := movementCrossMapLiveCompareFile(data, movementCrossMapLiveEffectiveRoot(f.RepoRoot), name); err != nil {
			t.Fatal(err)
		}
	}
	plans, err := automation.OpenStore(filepath.Join(f.Root, "plans.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer plans.Close()
	receipts, err := OpenReceiptStore(filepath.Join(f.Root, "receipts.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer receipts.Close()
	gate := aicontrol.New()
	defer gate.Close()
	control, lease, err := gate.Switch(gate.State().Generation, aicontrol.Agent, "model container leveling QA")
	if err != nil {
		t.Fatal(err)
	}
	binding := aimcp.Binding{AccountID: f.Created.Account.Username, ProfileID: "real-leveling-container", CharacterID: f.Created.Binding.CharacterID, CharacterName: f.Created.Binding.CharacterName, Generation: control.Generation}
	session := &containerLevelingSession{GameSession: f.Lease.Session, actions: map[aigame.ActionKind]int{}}
	config := GameplayConfig{Plans: plans, Tiles: tiles}
	funding := configureSuppliedLeveling(t, ctx, f, knowledge, &config)
	builder, err := NewGameplayBuilder(config)
	if err != nil {
		t.Fatal(err)
	}
	game, err := builder(ctx, BackendInput{Binding: binding, Gate: gate, Session: session, Knowledge: knowledge, Receipts: receipts, Lease: lease, Funding: funding})
	if err != nil {
		t.Fatal(err)
	}
	defer game.(*GameBackend).Close()
	backend := &containerLevelingBackend{Backend: game}
	gateway := NewGateway()
	defer gateway.Close()
	var token string
	if brokerImage == "" {
		registered, revoke, registerErr := gateway.Register(binding, backend)
		if registerErr != nil {
			t.Fatal(registerErr)
		}
		token = registered
		defer revoke()
	}
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(gateway)
	server.Listener = listener
	server.Start()
	defer server.Close()
	endpoint := fmt.Sprintf("http://host.docker.internal:%d/v1/game", listener.Addr().(*net.TCPAddr).Port)
	key, err := readFactoryLiveKey(filepath.Join(f.RepoRoot, "vendor", "deepseek", "key"))
	if err != nil {
		t.Fatal("private provider key unavailable")
	}
	artifacts := filepath.Join(f.RepoRoot, "build", "ai", "container-runtime-qa")
	if brokerImage != "" {
		artifacts = filepath.Join(artifacts, "factory-broker")
	}
	if err := os.MkdirAll(artifacts, 0700); err != nil {
		t.Fatal(err)
	}
	evidenceFiles = append(evidenceFiles, filepath.Join(artifacts, "leveling-evidence.json"))
	if brokerImage != "" {
		evidenceFiles = append(evidenceFiles, filepath.Join(artifacts, "factory-broker-evidence.json"))
	}
	// Invalidate the previous successful run before invoking the model. A failed
	// turn must not leave an older passed artifact looking like current proof.
	startedAt := time.Now().UTC().Format(time.RFC3339Nano)
	pendingEvidence, err := json.Marshal(map[string]any{"test": t.Name(), "passed": false, "started_at": startedAt, "status": "not_confirmed"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(artifacts, "leveling-evidence.json"), append(pendingEvidence, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	request := airunner.ExecuteRequest{ProfileID: "real-leveling-container", RequestID: "leveling-1",
		Run:    airunner.RunRequest{Prompt: "Read the installed .agents/skills/stoneage-play/SKILL.md and .agents/skills/stoneage-leveling/SKILL.md using the shell tool and follow them. Use native stoneage MCP tools only for game operations; do not write an ad hoc MCP client or read runtime credentials. Observe first. Query leveling knowledge for area id 28 and rule knowledge with text supply. Select the verified supply offer whose shop floor is 2004; use its returned alias, not an invented value. Start character leveling once with target_level=2, target_policy=all, maximum_seconds=300, maximum_deaths=0 and parameters containing area_id=28 and supply_item set to the selected alias. Let the service acquire supplies and level the character. Poll that same handle with game_task_status until terminal, allowing about two seconds between polls. Do not restart a failed or uncertain task. After a confirmed receipt, observe again. Return only JSON {handle,status,level,floor,x,y,hp}, copying status from the terminal receipt and state fields from the final observation. Report the actual error/status if any required knowledge or operation is unavailable."},
		Model:  airunner.Model{Provider: aimodels.DeepSeekProvider, BaseURL: aimodels.DeepSeekBaseURL, Model: aimodels.DeepSeekFlash, ReasoningEffort: "high", APIKey: key},
		Skills: []airunner.Skill{{Name: "stoneage-play"}, {Name: "stoneage-leveling"}},
		MCP:    airunner.MCP{Endpoint: endpoint, Token: token, CharacterID: binding.CharacterID, CharacterName: binding.CharacterName, Generation: binding.Generation},
	}
	var result airunner.Result
	if brokerImage != "" {
		result = runFactoryBrokerLiveLeveling(t, ctx, f, binding, gate, backend, gateway, endpoint, request, brokerImage, artifacts)
	} else {
		state := filepath.Join(f.Root, "container-state")
		if err := os.Mkdir(state, 0700); err != nil {
			t.Fatal(err)
		}
		imageRaw, err := exec.CommandContext(ctx, "docker", "image", "inspect", "--format", "{{.Id}}", "gcc:13-bookworm").Output()
		if err != nil {
			t.Fatal("existing Linux image required; no pull attempted")
		}
		if os.Getuid() == 0 {
			t.Fatal("non-root bind owner required")
		}
		runner := containerLiveRunner{Context: ctx, RepoRoot: f.RepoRoot, Artifacts: artifacts, State: state, Image: strings.TrimSpace(string(imageRaw)), UID: fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()), Key: key, Token: token, NameSuffix: movementCrossMapLiveHex(t, 12)}
		result = runner.Run(t, request)
	}

	if !containerLiveNamedSkillRead(result, "stoneage-play") || !containerLiveNamedSkillRead(result, "stoneage-leveling") {
		t.Fatal("native Skill read not observed")
	}
	backend.mu.Lock()
	operations := append([]string(nil), backend.operations...)
	starts := append([]aimcp.LevelingRequest(nil), backend.starts...)
	statuses := append([]aimcp.TaskReceipt(nil), backend.statuses...)
	observations := append([]aimcp.Observation(nil), backend.observations...)
	queries := append([]aimcp.KnowledgeResult(nil), backend.queries...)
	queryRequests := append([]aimcp.KnowledgeQuery(nil), backend.queryRequests...)
	startReceipts := append([]aimcp.TaskReceipt(nil), backend.startReceipts...)
	statusHandles := append([]string(nil), backend.statusHandles...)
	mutatingCalls := backend.mutatingCalls
	backend.mu.Unlock()
	// Preserve provider behavior for local diagnosis before assertions, without
	// including the private request, capability, credentials or full game state.
	session.mu.Lock()
	failures := append([]string(nil), session.failures...)
	petCommands := append([]string(nil), session.battleCommands...)
	session.mu.Unlock()
	petSnapshot, _ := session.Observe(ctx)
	eventTypes := map[string]int{}
	nativeTools := map[string]bool{}
	lastNativeObserve, lastNativeStatus := -1, -1
	for eventIndex, event := range result.Events {
		var item struct {
			Type   string `json:"type"`
			Server string `json:"server"`
			Tool   string `json:"tool"`
			Status string `json:"status"`
		}
		if json.Unmarshal(event.Item, &item) == nil && item.Type != "" {
			eventTypes[item.Type]++
			if item.Type == "mcp_tool_call" && item.Server == "stoneage" && item.Status == "completed" {
				nativeTools[item.Tool] = true
				if item.Tool == "game_observe" {
					lastNativeObserve = eventIndex
				}
				if item.Tool == "game_task_status" {
					lastNativeStatus = eventIndex
				}
			}
		}
	}
	diagnostic, marshalErr := json.MarshalIndent(map[string]any{"operations": operations, "observations": observations, "starts": starts, "statuses": statuses, "model_reply": result.LastMessage, "protocol_errors": failures, "codex_item_types": eventTypes, "battle_commands": petCommands, "selected_pet_slot": petSnapshot.Player.BattlePetSlot, "selected_pet_known": petSnapshot.Player.BattlePetSlotKnown, "pets": petSnapshot.Pets, "battle_roster": petSnapshot.Battle.Participants}, "", "  ")
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if err := os.WriteFile(filepath.Join(artifacts, "leveling-diagnostic.json"), diagnostic, 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"game_observe", "game_query_knowledge", "game_start_leveling", "game_task_status"} {
		if !nativeTools[name] {
			t.Fatalf("Codex did not complete native MCP call %s", name)
		}
	}
	if lastNativeObserve <= lastNativeStatus {
		t.Fatal("no successful native MCP observation after the final task status")
	}
	seenObserve, seenArea, seenSupply := false, false, false
	queryCursor := 0
	lastObserve, lastStatus := -1, -1
	for i, operation := range operations {
		switch operation {
		case "observe":
			seenObserve = true
			lastObserve = i
		case "knowledge":
			if queryCursor >= len(queries) || queryCursor >= len(queryRequests) {
				t.Fatal("knowledge trace is incomplete")
			}
			query := queries[queryCursor]
			request := queryRequests[queryCursor]
			queryCursor++
			for _, entry := range query.Entries {
				if request.Kind == "leveling" && request.ID == "28" && query.Kind == "leveling" && query.Revision == knowledge.Fingerprint() && entry.Verified && entry.ID == "28" {
					seenArea = true
				}
				if request.Kind == "rule" && request.Text == "supply" && query.Kind == "rule" && query.Revision == knowledge.Fingerprint() && entry.Verified && entry.ID == "supply:hometown-1-small-meat" {
					seenSupply = true
				}
			}
		case "start_leveling":
			if !seenObserve || !seenArea || !seenSupply {
				t.Fatal("model started before observing and querying knowledge")
			}
		case "task_status":
			lastStatus = i
		}
	}
	if lastObserve <= lastStatus {
		t.Fatal("model did not observe after its final task status")
	}
	if len(starts) != 1 || starts[0].TargetKind != "character" || starts[0].TargetLevel != 2 || starts[0].TargetPolicy != "all" || starts[0].MaximumSeconds != 300 || starts[0].MaximumDeaths != 0 || string(starts[0].Parameters["area_id"]) != "28" {
		t.Fatalf("model did not start exactly the requested task: start count=%d", len(starts))
	}
	if mutatingCalls != 0 {
		t.Fatal("model bypassed leveling through another mutating MCP tool")
	}
	if len(startReceipts) != 1 || startReceipts[0].Handle == "" {
		t.Fatal("missing start handle")
	}
	for _, handle := range statusHandles {
		if handle != startReceipts[0].Handle {
			t.Fatal("model polled a different task")
		}
	}
	var selectedSupply string
	if json.Unmarshal(starts[0].Parameters["supply_item"], &selectedSupply) != nil || selectedSupply != "hometown-1-small-meat" {
		t.Fatal("model did not select the reviewed hometown 1 supply offer")
	}
	verifiedArea := false
	verifiedSupply := false
	for _, query := range queries {
		for _, entry := range query.Entries {
			if query.Kind == "leveling" && query.Revision == knowledge.Fingerprint() && entry.ID == "28" && entry.Verified {
				verifiedArea = true
			}
			if query.Kind == "rule" && query.Revision == knowledge.Fingerprint() && entry.ID == "supply:"+selectedSupply && entry.Verified {
				verifiedSupply = true
			}
		}
	}
	if !verifiedArea {
		t.Fatal("model did not obtain verified area knowledge")
	}
	if !verifiedSupply {
		t.Fatal("model did not obtain verified supply knowledge")
	}
	if len(statuses) == 0 || len(observations) < 2 {
		t.Fatal("missing real task polling or final observation")
	}
	terminal := statuses[len(statuses)-1]
	final := observations[len(observations)-1]
	checkpoint, err := plans.Load(ctx, terminal.Handle)
	if err != nil {
		t.Fatal(err)
	}
	if terminal.Status != aimcp.ReceiptConfirmed || len(terminal.Evidence) == 0 || checkpoint.Status != automation.Completed || checkpoint.Confirmation == nil || final.Character.Level < 2 || final.Battle.Active || observations[0].Character.Level != 1 {
		t.Fatalf("real model-driven leveling unconfirmed: receipt=%s checkpoint=%s initial=%d final=%d", terminal.Status, checkpoint.Status, observations[0].Character.Level, final.Character.Level)
	}
	var reply struct {
		Handle string `json:"handle"`
		Status string `json:"status"`
		Level  int    `json:"level"`
		Floor  int    `json:"floor"`
		X      int    `json:"x"`
		Y      int    `json:"y"`
		HP     int    `json:"hp"`
	}
	// The application's last message is display text, not a structured-output
	// API. Accept one Markdown JSON fence, but still compare every reported
	// value with the independently recorded authoritative MCP responses.
	replyJSON := strings.TrimSpace(result.LastMessage)
	if strings.HasPrefix(replyJSON, "```json\n") && strings.HasSuffix(replyJSON, "\n```") {
		replyJSON = strings.TrimSuffix(strings.TrimPrefix(replyJSON, "```json\n"), "\n```")
	}
	if json.Unmarshal([]byte(replyJSON), &reply) != nil || reply.Handle != terminal.Handle || reply.Status != terminal.Status || reply.Level != final.Character.Level || reply.Floor != final.Floor || reply.X != final.X || reply.Y != final.Y || reply.HP != final.Character.HP {
		t.Fatal("model completion text disagrees with real MCP evidence")
	}
	session.mu.Lock()
	actions := map[aigame.ActionKind]int{}
	for kind, count := range session.actions {
		actions[kind] = count
	}
	petAttacks := session.petAttacks
	stockPurchases := append([]string(nil), session.stockPurchases...)
	stockPositions := append([]aigame.Point(nil), session.stockPositions...)
	session.mu.Unlock()
	if len(stockPurchases) == 0 || len(stockPositions) != len(stockPurchases) || stockPurchases[0] != "1|10" || stockPositions[0].Floor != 2004 {
		t.Fatal("model-started leveling never purchased its initial supplies")
	}
	if actions[aigame.ActionMove] == 0 || actions[aigame.ActionMapEvent] == 0 || actions[aigame.ActionBattle] == 0 || petAttacks == 0 {
		t.Fatalf("real movement/warp/combat not observed: %v", actions)
	}
	evidence := map[string]any{"test": t.Name(), "passed": true, "model": aimodels.DeepSeekFlash, "knowledge": knowledge.Fingerprint(), "qa_binary_sha256": binaryHash, "native_skill_read": true, "native_mcp_verified": true, "codex_item_types": eventTypes, "actual_container_isolation_verified": true, "released_image_verified": false, "broker_verified": false, "mcp_operations": operations, "initial_level": observations[0].Character.Level, "final_level": final.Character.Level, "final_position": []int{final.Floor, final.X, final.Y}, "final_hp": final.Character.HP, "task_status": terminal.Status, "checkpoint_status": checkpoint.Status, "protocol_submissions": actions, "pet_attacks": petAttacks, "usage": result.Usage}
	evidence["started_at"] = startedAt
	if brokerImage != "" {
		evidence["broker_verified"] = true
		evidence["factory_verified"] = true
		evidence["packaged_image_verified"] = true
		evidence["packaged_image_id"] = brokerImage
	}
	evidence["supply_offer"] = selectedSupply
	evidence["stock_purchases"] = stockPurchases
	evidence["stock_positions"] = stockPositions
	evidence["knowledge_requests"] = queryRequests
	evidence["status_handles"] = statusHandles
	encoded, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(artifacts, "leveling-evidence.json"), append(encoded, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	if brokerImage != "" {
		path := filepath.Join(artifacts, "factory-broker-evidence.json")
		raw, err := os.ReadFile(path)
		var detail map[string]any
		if err != nil || json.Unmarshal(raw, &detail) != nil {
			t.Fatal("read broker component evidence")
		}
		detail["passed"], detail["status"] = true, "passed"
		raw, err = json.MarshalIndent(detail, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("real model-driven level %d -> %d, terminal %s", observations[0].Character.Level, final.Character.Level, terminal.Status)
}
