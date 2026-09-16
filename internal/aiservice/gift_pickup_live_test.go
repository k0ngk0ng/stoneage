package aiservice

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aicontrol"
	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
	"github.com/k0ngk0ng/stoneage/internal/ainavigation"
	"github.com/k0ngk0ng/stoneage/internal/aiplanner"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

const (
	giftPickupLiveOptIn       = "STONEAGE_GIFT_PICKUP_LIVE_TEST"
	giftPickupLiveEvidenceRel = "build/ai/gift-pickup-live-evidence.json"
	giftWindowMessageType     = 0
	giftWindowButtonOK        = 1 << 0
	giftWindowButtonYes       = 1 << 2
	giftWindowButtonNo        = 1 << 3
	giftWindowButtonYesNo     = giftWindowButtonYes | giftWindowButtonNo

	// This is the digest of the two reviewed source files below, using the
	// same path/NUL/content/NUL construction as aiknowledge task evidence.
	// Keeping it fixed makes a source edit fail the QA contract instead of
	// silently changing the meaning of the test.
	hometown0GiftSourceFingerprint = "6cadd5a821be756f2824199aef5f51a590d82bc971df09341ceab55247bbf5b1"
)

// TestLiveGiftPickupFreshAI proves the concrete, first step of the savepoint
// 0 hometown gift interaction against a freshly provisioned AI character. It
// intentionally does not mark the complete multi-NPC task as production
// verified: this test only verifies movement, the reviewed Himiko contract,
// the request choice, and the resulting own-state fields.
func TestLiveGiftPickupFreshAI(t *testing.T) {
	if os.Getenv(giftPickupLiveOptIn) != "1" {
		t.Skip("set STONEAGE_GIFT_PICKUP_LIVE_TEST=1 for the real fresh-AI gift pickup QA check")
	}
	runLiveGiftPickupFreshAI(t, nil, nil)
}

func runLiveGiftPickupFreshAI(t *testing.T, beforePickup, afterPickup func(context.Context, *movementCrossMapLiveFreshAI, *GameBackend, *MovementSkill)) {
	t.Helper()
	repoRoot := movementCrossMapLiveRepositoryRoot(t)
	timeout := 4 * time.Minute
	if afterPickup != nil {
		timeout = 8 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	fixture := movementCrossMapLiveProvisionFreshAI(t, ctx, "gift-pickup", "aiservice-gift-pickup-live-test")

	knowledge, err := aiknowledge.LoadDataDir(ctx, filepath.Join(repoRoot, "runtime", "legacy-server", "gmsv", "data"))
	if err != nil {
		t.Fatal("load verified StoneAge knowledge")
	}
	navigator, err := ainavigation.LoadDataDir(ctx, filepath.Join(repoRoot, "runtime", "legacy-server", "gmsv", "data"))
	if err != nil {
		t.Fatal("load verified StoneAge navigation")
	}
	dataEvidence, err := movementCrossMapLiveVerifyEffectiveData(ctx, repoRoot, navigator)
	if err != nil {
		t.Fatalf("verify QA GMSV data against the knowledge source: %v", err)
	}
	npcSourceHashes, err := giftPickupLiveVerifyNPCSources(repoRoot)
	if err != nil {
		t.Fatalf("verify Himiko scripts against the effective QA data: %v", err)
	}

	gate := aicontrol.New()
	defer gate.Close()
	state, _, err := gate.Switch(gate.State().Generation, aicontrol.Agent, "fresh AI hometown gift pickup QA")
	if err != nil {
		t.Fatal("claim Agent control")
	}
	binding := aimcp.Binding{
		AccountID: fixture.Created.Account.Username, CharacterID: fixture.Created.Binding.CharacterID,
		CharacterName: fixture.Created.Binding.CharacterName, Generation: state.Generation,
	}
	backend := &GameBackend{
		Binding: binding, Gate: gate, Owner: aicontrol.Agent, Session: fixture.Lease.Session,
		Funding:         func(context.Context) (bool, error) { return fixture.Profile.UnlimitedFunds, nil },
		Knowledge:       knowledge,
		OwnStateRefresh: &OwnStateRefresher{},
	}

	// Request the optional extension through the normal backend refresh path.
	// A missing field remains unknown and therefore expires this bounded wait;
	// the failure below reports that compatibility problem explicitly.
	initialContext, initialCancel := context.WithTimeout(ctx, 15*time.Second)
	initialObservation, err := movementCrossMapLiveWaitObservation(initialContext, backend, binding, func(observation aimcp.Observation) bool {
		return observation.Connected && observation.Ready && observation.Phase == string(aigame.PhaseWorld) &&
			observation.Floor == 1006 && observation.X == 15 && observation.Y == 22 &&
			observation.Flags["savepoint:0"] && observation.Flags["inventory:known"]
	})
	initialCancel()
	if err != nil {
		last, observeErr := fixture.Lease.Session.Observe(context.Background())
		t.Fatalf("QA server must expose validated savepoint and backpack fields before gift pickup: %v (last AI=%+v observe_error=%v)", err, last.AI, observeErr)
	}
	if !initialObservation.UnlimitedFunds {
		t.Fatal("fresh gift-pickup AI profile did not retain unlimited in-game funds")
	}
	initialSnapshot, err := fixture.Lease.Session.Observe(ctx)
	if err != nil {
		t.Fatal("observe initial gift-pickup session")
	}
	if !initialSnapshot.AI.Received || !initialSnapshot.AI.SavePointsKnown || !initialSnapshot.AI.ItemsKnown ||
		uint32(initialSnapshot.AI.SavePoints)&1 == 0 {
		t.Fatalf("initial own-state lacks the reviewed savepoint-0 prerequisites: %+v", initialSnapshot.AI)
	}

	target := aiknowledge.Point{Floor: 1000, X: 56, Y: 124}
	movementSkill := &MovementSkill{
		Backend: backend, Navigator: navigator, WarpGraph: aiplanner.NewWarpGraph(knowledge),
		SegmentTimeout: 5 * time.Second, WarpConfirmationTimeout: 8 * time.Second,
	}
	if beforePickup != nil {
		beforePickup(ctx, fixture, backend, movementSkill)
	}
	beforeMove, err := backend.Observe(ctx, binding)
	if err != nil {
		t.Fatal("observe after preparation:", err)
	}
	staticEdges, err := movementSkill.findCrossMapRoute(ctx,
		aiplanner.NewWarpGraph(knowledge), aiknowledge.Point{Floor: beforeMove.Floor, X: beforeMove.X, Y: beforeMove.Y}, target)
	if err != nil {
		t.Fatalf("verified knowledge has no route to the Himiko interaction tile: %v", err)
	}
	if len(staticEdges) == 0 {
		t.Fatal("verified knowledge returned an empty route to the Himiko interaction tile")
	}
	arguments, err := json.Marshal(movementArguments{Floor: target.Floor, X: target.X, Y: target.Y})
	if err != nil {
		t.Fatal("encode Himiko movement arguments")
	}
	beforeMove, err = backend.Observe(ctx, binding)
	if err != nil {
		t.Fatal(err)
	}
	moveAction := automation.Action{Skill: "move", ExpectedRevision: beforeMove.Revision, Arguments: arguments}
	if err := movementSkill.Execute(ctx, moveAction); err != nil {
		failedSnapshot, observeErr := fixture.Lease.Session.Observe(context.Background())
		t.Logf("gift-pickup movement failure snapshot=%+v observe_error=%v", failedSnapshot, observeErr)
		t.Fatalf("real movement to the Himiko interaction tile failed: %v", err)
	}
	afterMove, err := movementCrossMapLiveWaitObservation(ctx, backend, binding, func(observation aimcp.Observation) bool {
		return observation.Connected && observation.Ready && observation.Phase == string(aigame.PhaseWorld) &&
			observation.Floor == target.Floor && observation.X == target.X && observation.Y == target.Y
	})
	if err != nil {
		t.Fatalf("GameBackend did not confirm the exact Himiko interaction tile: %v", err)
	}

	spec := hometown0GiftHimikoNPCSpec(t, repoRoot)
	actors := make([]aimcp.VisibleActor, 0, 1)
	for _, actor := range afterMove.Actors {
		if actor.X != 57 || actor.Y != 124 || actor.ID < 0 {
			continue
		}
		if actor.Kind != "" && !strings.EqualFold(strings.TrimSpace(actor.Kind), "character") {
			continue
		}
		if actor.Name == spec.Name || actor.FreeName == spec.Name || actor.Title == spec.Name {
			actors = append(actors, actor)
		}
	}
	if len(actors) != 1 {
		t.Fatalf("expected exactly one visible NPC %q at (57,124), got %d: %+v", spec.Name, len(actors), afterMove.Actors)
	}
	npcActor := actors[0]
	if npcActor.Name != spec.Name && npcActor.FreeName != spec.Name && npcActor.Title != spec.Name {
		t.Fatalf("visible NPC identity did not match the reviewed name %q: %+v", spec.Name, npcActor)
	}
	if len(spec.Windows) != 2 || !spec.Windows[0].WindowObjectFromActor || !spec.Windows[1].WindowObjectFromActor {
		t.Fatalf("Himiko contract must retain both runtime-object windows: %+v", spec.Windows)
	}
	registry, err := NewNPCRegistry([]NPCSpec{spec})
	if err != nil {
		t.Fatal("validate source-reviewed Himiko NPC contract")
	}
	registered, ok := registry.Lookup(spec.Alias)
	if !ok || len(registered.Windows) != 2 || registered.Windows[0].Sequence != 234 || registered.Windows[1].Sequence != 231 {
		t.Fatalf("registered Himiko contract lost its multiple verified windows: ok=%v spec=%+v", ok, registered)
	}
	if choice, ok := registered.Windows[0].Choices[4]; !ok || choice.Button != 4 || choice.MaximumCost != 0 {
		t.Fatalf("registered Himiko request choice is not the reviewed YES=4 free action: %+v", registered.Windows[0].Choices)
	}

	// Re-observe immediately before TK so the revision fence belongs to the
	// same authoritative actor sample used to select the runtime object.
	talkObservation, err := backend.Observe(ctx, binding)
	if err != nil {
		t.Fatal("observe Himiko before talk")
	}
	talkArguments, err := json.Marshal(npcTalkArgumentsForLive(spec, npcActor.ID))
	if err != nil {
		t.Fatal("encode Himiko talk arguments")
	}
	npcSkill := NewNPCSkill(backend, registry)
	talkAction := automation.Action{Skill: "npc.talk", ExpectedRevision: talkObservation.Revision, MaximumCost: 0, Arguments: talkArguments}
	if err := npcSkill.Execute(ctx, talkAction); err != nil {
		t.Fatalf("NPCSkill.talk against the reviewed Himiko contract failed: %v", err)
	}
	talkWindow, err := movementCrossMapLiveWaitSnapshot(ctx, fixture.Lease.Session, func(snapshot aigame.Snapshot) bool {
		window := snapshot.ActiveWindow
		return window != nil && window.Open && window.Type == giftWindowMessageType &&
			window.ButtonType == giftWindowButtonYesNo && window.ButtonType&giftWindowButtonYes != 0 &&
			window.Sequence == 234 && window.ObjectID == int32(npcActor.ID)
	})
	if err != nil {
		t.Fatalf("Himiko talk did not produce MESSAGE sequence 234 with YES4 in YESNO12: %v", err)
	}
	if talkWindow.ActiveWindow == nil || talkWindow.ActiveWindow.Type != giftWindowMessageType || talkWindow.ActiveWindow.Sequence != 234 ||
		talkWindow.ActiveWindow.ButtonType != giftWindowButtonYesNo || talkWindow.ActiveWindow.ButtonType&giftWindowButtonYes == 0 ||
		talkWindow.ActiveWindow.ObjectID != int32(npcActor.ID) {
		t.Fatalf("unexpected Himiko request window: %+v", talkWindow.ActiveWindow)
	}

	windowObservation, err := backend.Observe(ctx, binding)
	if err != nil {
		t.Fatal("observe Himiko request window")
	}
	windowArguments, err := json.Marshal(npcWindowArgumentsForLive(spec.Alias, 234, 4))
	if err != nil {
		t.Fatal("encode Himiko request choice")
	}
	windowAction := automation.Action{Skill: "npc.window", ExpectedRevision: windowObservation.Revision, MaximumCost: 0, Arguments: windowArguments}
	verifyRecovery := beginGiftCheckpointRecovery(t, ctx, fixture.Root,
		&AutomationGame{Backend: backend, Skills: npcSkill, NPCs: registry}, windowAction, spec.SourceFingerprint)

	// The NPC script sends STARTMSG(231) after the server accepts YES=4. The
	// item assertion is based on the validated S("AI") template stream, never
	// on a display name or a guessed inventory slot.
	finalObservation, err := movementCrossMapLiveWaitObservation(ctx, backend, binding, func(observation aimcp.Observation) bool {
		window := observation.ActiveWindow
		return observation.Connected && observation.Ready && observation.Phase == string(aigame.PhaseWorld) &&
			observation.Floor == 1000 && observation.X == 56 && observation.Y == 124 &&
			observation.Flags["savepoint:0"] && observation.Flags["now:2"] && observation.Flags["inventory:known"] &&
			observation.Inventory["item:2415"] >= 1 && window != nil && window.Open &&
			window.Type == giftWindowMessageType && window.ButtonType == giftWindowButtonOK &&
			window.Sequence == 231 && window.ObjectID == npcActor.ID
	})
	if err != nil {
		last, observeErr := fixture.Lease.Session.Observe(context.Background())
		t.Fatalf("Himiko YES4 did not produce item template 2415, now:2 and STARTMSG(231): %v (last AI=%+v window=%+v observe_error=%v)", err, last.AI, last.ActiveWindow, observeErr)
	}
	finalSnapshot, err := fixture.Lease.Session.Observe(ctx)
	if err != nil {
		t.Fatal("observe final Himiko gift state")
	}
	if !finalSnapshot.AI.Received || !finalSnapshot.AI.SavePointsKnown || uint32(finalSnapshot.AI.SavePoints)&1 == 0 ||
		uint32(finalSnapshot.AI.NowEvents[0])&(uint32(1)<<2) == 0 {
		t.Fatalf("QA server did not return savepoint bit 0 and now-event bit 2 after gift pickup: %+v", finalSnapshot.AI)
	}
	if !finalSnapshot.AI.ItemsKnown {
		t.Fatalf("QA server did not return ItemsKnown after gift pickup: %+v", finalSnapshot.AI)
	}
	if !aiInventoryHasTemplate(finalSnapshot.AI.Items, 2415) {
		t.Fatalf("authoritative AI inventory does not contain item template 2415: %+v", finalSnapshot.AI.Items)
	}
	if finalObservation.Inventory["item:2415"] < 1 || !finalObservation.Flags["inventory:known"] {
		t.Fatalf("projected inventory did not retain the authoritative gift template: %+v", finalObservation)
	}
	if finalObservation.Revision <= initialObservation.Revision || finalSnapshot.Revision <= initialSnapshot.Revision {
		t.Fatalf("gift pickup did not advance authoritative revision: initial=%d/%d final=%d/%d", initialObservation.Revision, initialSnapshot.Revision, finalObservation.Revision, finalSnapshot.Revision)
	}
	// STARTMSG's OK branch has no game-state mutation or response in the
	// reviewed NPC handler. Exercise the actual WN before relogin, but record
	// only successful submission: reconnecting is not server acknowledgement.
	confirmArguments := confirmGiftStartMessage(t, ctx, fixture.Root, backend, registry, npcSkill)
	submittedObservation, err := backend.Observe(ctx, binding)
	if err != nil {
		t.Fatal(err)
	}
	if submittedObservation.ActiveWindow == nil || submittedObservation.ActiveWindow.Sequence != 231 || !submittedObservation.ActiveWindow.Submitted {
		t.Fatal("successful STARTMSG write was not marked submitted")
	}
	if err := npcSkill.Execute(ctx, automation.Action{Skill: "npc.window", ExpectedRevision: submittedObservation.Revision, Arguments: confirmArguments}); !errors.Is(err, ErrNPCWindowInactive) {
		t.Fatalf("submitted STARTMSG was not fenced against repeated clicks: %v", err)
	}
	t.Log("STARTMSG 231 OK submitted before relogin; no server close acknowledgement inferred")
	reloginObservation := reloginGiftPickup(t, ctx, fixture, backend)
	verifyRecovery()

	evidence := giftPickupLiveEvidence{
		Test: t.Name(), Status: "passed", Gateway: fixture.Gateway.Address(),
		Upstream: movementCrossMapLiveUpstream, ProfileID: fixture.Created.Profile.ID,
		AccountID: fixture.Created.Account.ID, AccountUsername: fixture.Created.Account.Username,
		CharacterID: fixture.Created.Binding.CharacterID, CharacterName: fixture.Created.Binding.CharacterName,
		UnlimitedFunds: fixture.Profile.UnlimitedFunds, KnowledgeDigest: knowledge.Fingerprint(),
		SourceFingerprint: spec.SourceFingerprint, NPCActorID: npcActor.ID,
		Initial:       movementCrossMapLiveObservationEvidence{Revision: initialObservation.Revision, Phase: initialObservation.Phase, Connected: initialObservation.Connected, Ready: initialObservation.Ready, Floor: initialObservation.Floor, X: initialObservation.X, Y: initialObservation.Y},
		AfterMove:     movementCrossMapLiveObservationEvidence{Revision: afterMove.Revision, Phase: afterMove.Phase, Connected: afterMove.Connected, Ready: afterMove.Ready, Floor: afterMove.Floor, X: afterMove.X, Y: afterMove.Y},
		RequestWindow: giftPickupLiveWindowEvidence{Type: giftWindowMessageType, ButtonType: int(talkWindow.ActiveWindow.ButtonType), Sequence: 234, ObjectID: npcActor.ID, Open: true},
		FinalWindow:   giftPickupLiveWindowEvidence{Type: finalObservation.ActiveWindow.Type, ButtonType: finalObservation.ActiveWindow.ButtonType, Sequence: finalObservation.ActiveWindow.Sequence, ObjectID: finalObservation.ActiveWindow.ObjectID, Open: finalObservation.ActiveWindow.Open},
		Final:         movementCrossMapLiveObservationEvidence{Revision: finalObservation.Revision, Phase: finalObservation.Phase, Connected: finalObservation.Connected, Ready: finalObservation.Ready, Floor: finalObservation.Floor, X: finalObservation.X, Y: finalObservation.Y},
		SavePoints:    finalSnapshot.AI.SavePoints, SavePointsKnown: finalSnapshot.AI.SavePointsKnown,
		NowEvent2:  uint32(finalSnapshot.AI.NowEvents[0])&(uint32(1)<<2) != 0,
		ItemsKnown: finalSnapshot.AI.ItemsKnown, ItemTemplateID: 2415, ItemCount: giftPickupLiveTemplateCount(finalSnapshot.AI.Items, 2415),
		TaskExecutionVerified: false, RecordedAt: time.Now().UTC().Format(time.RFC3339Nano), Data: dataEvidence,
		NPCSourceHashes:               npcSourceHashes,
		CheckpointRecoveryVerified:    true,
		ReloginRevision:               reloginObservation.Revision,
		ReloginPickupVerified:         true,
		StartMessageOKSubmitted:       true,
		StartMessageStepVerified:      true,
		StartMessageDuplicateRejected: true,
	}
	if err := persistGiftPickupLiveEvidence(repoRoot, evidence); err != nil {
		t.Fatal("write gift-pickup live evidence")
	}
	t.Logf("gift-pickup live evidence written to %s: actor=%d request=WN(type=0,buttons=%d,selected=4,sequence=234) item=2415 now:2 savepoints=%d", filepath.Join(repoRoot, giftPickupLiveEvidenceRel), npcActor.ID, talkWindow.ActiveWindow.ButtonType, finalSnapshot.AI.SavePoints)
	if afterPickup != nil {
		afterPickup(ctx, fixture, backend, movementSkill)
	}
}

func giftPickupLiveVerifyNPCSources(repoRoot string) (map[string]string, error) {
	knowledgeRoot := filepath.Join(repoRoot, "runtime", "legacy-server", "gmsv", "data")
	effectiveRoot := strings.TrimSpace(os.Getenv("STONEAGE_MOVEMENT_CROSS_MAP_EFFECTIVE_DATA_DIR"))
	if effectiveRoot == "" {
		effectiveRoot = filepath.Join(repoRoot, "build", "player-integration", "game", "gmsv", "data")
	}
	effectiveRoot, err := filepath.Abs(effectiveRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve effective QA data root: %w", err)
	}
	hashes := make(map[string]string, 2)
	for _, source := range []string{"npc/sainasu/event/event02.create", "npc/sainasu/event/event02_2"} {
		hash, err := movementCrossMapLiveCompareFile(knowledgeRoot, effectiveRoot, source)
		if err != nil {
			return nil, fmt.Errorf("compare effective QA source %s: %w", source, err)
		}
		hashes[source] = hash
	}
	return hashes, nil
}

func hometown0GiftHimikoNPCSpec(t *testing.T, repoRoot string) NPCSpec {
	t.Helper()
	dataRoot := filepath.Join(repoRoot, "runtime", "legacy-server", "gmsv", "data")
	sources := []string{"npc/sainasu/event/event02.create", "npc/sainasu/event/event02_2"}
	hash := sha256.New()
	for _, source := range sources {
		raw, err := os.ReadFile(filepath.Join(dataRoot, filepath.FromSlash(source)))
		if err != nil {
			t.Fatalf("read reviewed Himiko source %s: %v", source, err)
		}
		_, _ = hash.Write([]byte(source))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(raw)
		_, _ = hash.Write([]byte{0})
	}
	fingerprint := hex.EncodeToString(hash.Sum(nil))
	if fingerprint != hometown0GiftSourceFingerprint {
		t.Fatalf("reviewed Himiko source fingerprint changed: got %s want %s", fingerprint, hometown0GiftSourceFingerprint)
	}
	return NPCSpec{
		Alias: "sainasu-himiko", Floor: 1000, X: 57, Y: 124, Name: "日美子", TalkRange: 1,
		Windows: []NPCWindowSpec{
			{
				Type: giftWindowMessageType, Sequence: 234, ObjectID: 0, WindowObjectFromActor: true,
				Choices: map[int]NPCChoice{4: {Button: 4, MaximumCost: 0, Alias: "yes", ID: "YES", Name: "YES"}},
			},
			{
				Type: giftWindowMessageType, Sequence: 231, ObjectID: 0, WindowObjectFromActor: true,
				Choices: map[int]NPCChoice{1: {Button: 1, MaximumCost: 0, Alias: "ok", ID: "OK", Name: "OK"}},
			},
		},
		SourceFingerprint: hometown0GiftSourceFingerprint, Verified: true,
	}
}

func npcTalkArgumentsForLive(spec NPCSpec, actorID int) npcTalkArguments {
	return npcTalkArguments{NPC: spec.Alias, Floor: json.RawMessage(strconv.Itoa(spec.Floor)), X: json.RawMessage(strconv.Itoa(spec.X)), Y: json.RawMessage(strconv.Itoa(spec.Y)), ActorID: json.RawMessage(strconv.Itoa(actorID)), Command: "talk"}
}

func npcWindowArgumentsForLive(alias string, sequence, choice int) npcWindowArguments {
	return npcWindowArguments{NPC: alias, WindowSequence: json.RawMessage(strconv.Itoa(sequence)), Choice: json.RawMessage(strconv.Itoa(choice))}
}

func aiInventoryHasTemplate(items []aigame.AIInventoryItem, templateID int32) bool {
	return giftPickupLiveTemplateCount(items, templateID) > 0
}

func giftPickupLiveTemplateCount(items []aigame.AIInventoryItem, templateID int32) int {
	count := 0
	for _, item := range items {
		if item.TemplateID == templateID {
			count++
		}
	}
	return count
}

type giftPickupLiveWindowEvidence struct {
	Type       int  `json:"type"`
	ButtonType int  `json:"button_type"`
	Sequence   int  `json:"sequence"`
	ObjectID   int  `json:"object_id"`
	Open       bool `json:"open"`
}

type giftPickupLiveEvidence struct {
	StartMessageDuplicateRejected bool                                    `json:"start_message_duplicate_rejected"`
	StartMessageStepVerified      bool                                    `json:"start_message_step_verified"`
	StartMessageOKSubmitted       bool                                    `json:"start_message_ok_submitted"`
	ReloginRevision               uint64                                  `json:"relogin_revision"`
	ReloginPickupVerified         bool                                    `json:"relogin_pickup_verified"`
	CheckpointRecoveryVerified    bool                                    `json:"checkpoint_recovery_verified"`
	Test                          string                                  `json:"test"`
	Status                        string                                  `json:"status"`
	Gateway                       string                                  `json:"gateway"`
	Upstream                      string                                  `json:"upstream"`
	ProfileID                     string                                  `json:"profile_id"`
	AccountID                     int64                                   `json:"account_id"`
	AccountUsername               string                                  `json:"account_username"`
	CharacterID                   string                                  `json:"character_id"`
	CharacterName                 string                                  `json:"character_name"`
	UnlimitedFunds                bool                                    `json:"unlimited_funds"`
	KnowledgeDigest               string                                  `json:"knowledge_digest"`
	SourceFingerprint             string                                  `json:"source_fingerprint"`
	NPCActorID                    int                                     `json:"npc_actor_id"`
	Initial                       movementCrossMapLiveObservationEvidence `json:"initial"`
	AfterMove                     movementCrossMapLiveObservationEvidence `json:"after_move"`
	RequestWindow                 giftPickupLiveWindowEvidence            `json:"request_window"`
	FinalWindow                   giftPickupLiveWindowEvidence            `json:"final_window"`
	Final                         movementCrossMapLiveObservationEvidence `json:"final"`
	SavePoints                    int32                                   `json:"savepoints"`
	SavePointsKnown               bool                                    `json:"savepoints_known"`
	NowEvent2                     bool                                    `json:"now_event_2"`
	ItemsKnown                    bool                                    `json:"items_known"`
	ItemTemplateID                int32                                   `json:"item_template_id"`
	ItemCount                     int                                     `json:"item_count"`
	TaskExecutionVerified         bool                                    `json:"task_execution_verified"`
	NPCSourceHashes               map[string]string                       `json:"npc_source_hashes"`
	Data                          movementCrossMapLiveDataEvidence        `json:"data"`
	RecordedAt                    string                                  `json:"recorded_at"`
}

func persistGiftPickupLiveEvidence(repoRoot string, evidence giftPickupLiveEvidence) error {
	data, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		return errors.New("marshal gift-pickup live evidence")
	}
	data = append(data, '\n')
	base := filepath.Join(repoRoot, "build", "ai")
	if err := os.MkdirAll(base, 0o700); err != nil {
		return errors.New("create gift-pickup evidence directory")
	}
	if err := os.Chmod(base, 0o700); err != nil {
		return errors.New("protect gift-pickup evidence directory")
	}
	target := filepath.Join(repoRoot, giftPickupLiveEvidenceRel)
	if info, err := os.Lstat(target); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return errors.New("gift-pickup evidence target is unsafe")
		}
	} else if !os.IsNotExist(err) {
		return errors.New("inspect gift-pickup evidence target")
	}
	temporary, err := os.CreateTemp(base, ".gift-pickup-live-evidence-*.tmp")
	if err != nil {
		return errors.New("create gift-pickup evidence temporary file")
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return errors.New("protect gift-pickup live evidence")
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return errors.New("write gift-pickup live evidence")
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return errors.New("sync gift-pickup live evidence")
	}
	if err := temporary.Close(); err != nil {
		return errors.New("close gift-pickup live evidence")
	}
	if err := os.Rename(temporaryName, target); err != nil {
		return errors.New("install gift-pickup live evidence")
	}
	return nil
}
