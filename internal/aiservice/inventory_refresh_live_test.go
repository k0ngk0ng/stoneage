package aiservice

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aicontrol"
	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
)

const (
	inventoryRefreshLiveOptIn        = "STONEAGE_INVENTORY_REFRESH_LIVE_TEST"
	inventoryRefreshLiveUpstream     = "127.0.0.1:29065"
	inventoryRefreshLiveQABinaryHash = "51f57aa068a72148078d8cf0118fde3f9aafbfa62a0323fe936d29be9b0f2c19"
	inventoryRefreshLiveEvidenceRel  = "build/ai/inventory-refresh-live-evidence.json"
)

// TestLiveInventoryRefreshFreshAI proves that two read-only, correlated AI
// status requests receive fresh server observations for the same provisioned
// character. It is opt-in because the fixture creates a real QA identity.
func TestLiveInventoryRefreshFreshAI(t *testing.T) {
	if os.Getenv(inventoryRefreshLiveOptIn) != "1" {
		t.Skip("set STONEAGE_INVENTORY_REFRESH_LIVE_TEST=1 for the real inventory refresh QA check")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	raw, err := exec.CommandContext(ctx, "docker", "exec", "stoneage-player-qa-gmsv-1", "sha256sum", "/proc/1/exe").Output()
	fields := strings.Fields(string(raw))
	if err != nil || len(fields) != 2 || fields[0] != inventoryRefreshLiveQABinaryHash {
		t.Fatal("running QA binary must match the reviewed inventory-observation build")
	}

	fixture := movementCrossMapLiveProvisionFreshAI(t, ctx, "inventory-refresh", "aiservice-inventory-refresh-live-test")
	dataRoot := filepath.Join(fixture.RepoRoot, "runtime", "legacy-server", "gmsv", "data")
	knowledge, err := aiknowledge.LoadDataDir(ctx, dataRoot)
	if err != nil {
		t.Fatal("load verified StoneAge knowledge:", err)
	}

	gate := aicontrol.New()
	defer gate.Close()
	state, _, err := gate.Switch(gate.State().Generation, aicontrol.Agent, "isolated inventory refresh QA")
	if err != nil {
		t.Fatal("claim Agent control:", err)
	}
	binding := aimcp.Binding{
		AccountID: fixture.Created.Account.Username, CharacterID: fixture.Created.Binding.CharacterID,
		CharacterName: fixture.Created.Binding.CharacterName, Generation: state.Generation,
	}
	if err := binding.Validate(); err != nil {
		t.Fatal("fresh AI binding is invalid:", err)
	}
	backend := &GameBackend{
		Binding: binding, Gate: gate, Owner: aicontrol.Agent, Session: fixture.Lease.Session,
		Knowledge: knowledge,
	}
	npc := NewNPCSkill(backend, nil)

	firstBefore, err := fixture.Lease.Session.Observe(ctx)
	if err != nil {
		t.Fatal("observe fresh AI before first inventory refresh:", err)
	}
	if err := inventoryRefreshLiveCheckIdentity(firstBefore, binding); err != nil {
		t.Fatal("fresh AI identity before first refresh:", err)
	}
	first, err := refreshInventoryIdentity(ctx, npc)
	if err != nil {
		t.Fatal("first authoritative inventory refresh:", err)
	}
	if err := inventoryRefreshLiveCheckResult(first, firstBefore, binding); err != nil {
		t.Fatal("first inventory refresh result:", err)
	}

	secondBefore, err := fixture.Lease.Session.Observe(ctx)
	if err != nil {
		t.Fatal("observe fresh AI before second inventory refresh:", err)
	}
	if err := inventoryRefreshLiveCheckIdentity(secondBefore, binding); err != nil {
		t.Fatal("fresh AI identity before second refresh:", err)
	}
	second, err := refreshInventoryIdentity(ctx, npc)
	if err != nil {
		t.Fatal("second authoritative inventory refresh:", err)
	}
	if err := inventoryRefreshLiveCheckResult(second, secondBefore, binding); err != nil {
		t.Fatal("second inventory refresh result:", err)
	}

	if first.AI.RequestID == "" || second.AI.RequestID == "" || first.AI.RequestID == second.AI.RequestID {
		t.Fatalf("inventory refresh request nonce was not unique: first=%q second=%q", first.AI.RequestID, second.AI.RequestID)
	}
	if second.AIObservationRevision <= first.AIObservationRevision {
		t.Fatalf("second AI observation did not advance the authoritative revision: first=%d second=%d", first.AIObservationRevision, second.AIObservationRevision)
	}
	if first.AI.CharacterIndex != second.AI.CharacterIndex {
		t.Fatalf("AI response character identity changed within one bound session: first=%d second=%d", first.AI.CharacterIndex, second.AI.CharacterIndex)
	}
	if !inventoryRefreshLiveItemsEqual(first.AI.Items, second.AI.Items) {
		t.Fatalf("authoritative inventory changed between read-only refreshes: first=%v second=%v", first.AI.Items, second.AI.Items)
	}

	evidence := inventoryRefreshLiveEvidence{
		Test: t.Name(), Status: "passed", Gateway: fixture.Gateway.Address(),
		Upstream: inventoryRefreshLiveUpstream, ProfileID: fixture.Created.Profile.ID,
		AccountUsername: fixture.Created.Account.Username, CharacterID: fixture.Created.Binding.CharacterID,
		CharacterName: fixture.Created.Binding.CharacterName, CharacterSlot: fixture.Created.Binding.CharacterSlot,
		QABinarySHA256: inventoryRefreshLiveQABinaryHash, KnowledgeDigest: knowledge.Fingerprint(),
		NonceDistinct: true, IdentityConsistent: true, InventoryKnown: true,
		First: inventoryRefreshLiveSnapshotEvidenceFrom(first), Second: inventoryRefreshLiveSnapshotEvidenceFrom(second),
		RecordedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := persistInventoryRefreshLiveEvidence(fixture.RepoRoot, evidence); err != nil {
		t.Fatal("write inventory-refresh live evidence:", err)
	}
	t.Logf("inventory refresh evidence written to %s: first=%s/%d second=%s/%d items=%d", filepath.Join(fixture.RepoRoot, inventoryRefreshLiveEvidenceRel), first.AI.RequestID, first.AIObservationRevision, second.AI.RequestID, second.AIObservationRevision, len(second.AI.Items))
}

func inventoryRefreshLiveCheckIdentity(snapshot aigame.Snapshot, binding aimcp.Binding) error {
	if snapshot.Account != binding.AccountID || snapshot.Character != binding.CharacterName {
		return aimcp.ErrInvalidBinding
	}
	if !snapshot.Connected || snapshot.Phase != aigame.PhaseWorld || !snapshot.Player.HasStatus {
		return os.ErrInvalid
	}
	return nil
}

func inventoryRefreshLiveCheckResult(snapshot, before aigame.Snapshot, binding aimcp.Binding) error {
	if err := inventoryRefreshLiveCheckIdentity(snapshot, binding); err != nil {
		return err
	}
	if !snapshot.AI.Received || !snapshot.AI.ItemsKnown || snapshot.AI.RequestID == "" {
		return os.ErrInvalid
	}
	if snapshot.AIObservationRevision == 0 || snapshot.AIObservationRevision <= before.Revision || snapshot.AIObservationRevision > snapshot.Revision {
		return os.ErrInvalid
	}
	for _, item := range snapshot.AI.Items {
		if item.Slot < 5 || item.Slot >= 20 || item.TemplateID <= 0 {
			return os.ErrInvalid
		}
	}
	return nil
}

func inventoryRefreshLiveItemsEqual(left, right []aigame.AIInventoryItem) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

type inventoryRefreshLiveSnapshotEvidence struct {
	Revision              uint64                   `json:"revision"`
	AIObservationRevision uint64                   `json:"ai_observation_revision"`
	RequestID             string                   `json:"request_id"`
	CharacterIndex        int32                    `json:"character_index"`
	Account               string                   `json:"account"`
	Character             string                   `json:"character"`
	Phase                 string                   `json:"phase"`
	Connected             bool                     `json:"connected"`
	ItemsKnown            bool                     `json:"items_known"`
	Items                 []aigame.AIInventoryItem `json:"items"`
}

func inventoryRefreshLiveSnapshotEvidenceFrom(snapshot aigame.Snapshot) inventoryRefreshLiveSnapshotEvidence {
	return inventoryRefreshLiveSnapshotEvidence{
		Revision: snapshot.Revision, AIObservationRevision: snapshot.AIObservationRevision,
		RequestID: snapshot.AI.RequestID, CharacterIndex: snapshot.AI.CharacterIndex,
		Account: snapshot.Account, Character: snapshot.Character, Phase: string(snapshot.Phase),
		Connected: snapshot.Connected, ItemsKnown: snapshot.AI.ItemsKnown,
		Items: append([]aigame.AIInventoryItem(nil), snapshot.AI.Items...),
	}
}

type inventoryRefreshLiveEvidence struct {
	Test               string                               `json:"test"`
	Status             string                               `json:"status"`
	Gateway            string                               `json:"gateway"`
	Upstream           string                               `json:"upstream"`
	ProfileID          string                               `json:"profile_id"`
	AccountUsername    string                               `json:"account_username"`
	CharacterID        string                               `json:"character_id"`
	CharacterName      string                               `json:"character_name"`
	CharacterSlot      int                                  `json:"character_slot"`
	QABinarySHA256     string                               `json:"qa_binary_sha256"`
	KnowledgeDigest    string                               `json:"knowledge_digest"`
	NonceDistinct      bool                                 `json:"nonce_distinct"`
	IdentityConsistent bool                                 `json:"identity_consistent"`
	InventoryKnown     bool                                 `json:"inventory_known"`
	First              inventoryRefreshLiveSnapshotEvidence `json:"first"`
	Second             inventoryRefreshLiveSnapshotEvidence `json:"second"`
	RecordedAt         string                               `json:"recorded_at"`
}

func persistInventoryRefreshLiveEvidence(repoRoot string, evidence inventoryRefreshLiveEvidence) error {
	data, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		return err
	}
	base := filepath.Join(repoRoot, "build", "ai")
	if err := os.MkdirAll(base, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(repoRoot, inventoryRefreshLiveEvidenceRel), append(data, '\n'), 0o600)
}
