package aiservice

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aicontrol"
	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
	"github.com/k0ngk0ng/stoneage/internal/ainavigation"
	"github.com/k0ngk0ng/stoneage/internal/aiplanner"
	"github.com/k0ngk0ng/stoneage/internal/aiprovision"
	"github.com/k0ngk0ng/stoneage/internal/airuntime"
	"github.com/k0ngk0ng/stoneage/internal/auth"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

const (
	movementCrossMapLiveOptIn       = "STONEAGE_MOVEMENT_CROSS_MAP_LIVE_TEST"
	movementCrossMapLiveUpstream    = "127.0.0.1:29065"
	movementCrossMapLiveEvidenceRel = "build/ai/cross-map-live-evidence.json"
)

// TestLiveMovementCrossMapFreshAI proves the complete server-owned path for a
// newly provisioned AI character: account creation, character login, native
// tile movement to a verified mapwarp source, server-side floor transition,
// and authoritative destination confirmation. It is opt-in because it creates
// a persistent QA account and changes that account's game position.
func TestLiveMovementCrossMapFreshAI(t *testing.T) {
	if os.Getenv(movementCrossMapLiveOptIn) != "1" {
		t.Skip("set STONEAGE_MOVEMENT_CROSS_MAP_LIVE_TEST=1 for the real fresh-AI cross-map movement QA check")
	}

	repoRoot := movementCrossMapLiveRepositoryRoot(t)
	base := filepath.Join(repoRoot, "build", "ai")
	if err := os.MkdirAll(base, 0o700); err != nil {
		t.Fatal("create build/ai")
	}
	if err := os.Chmod(base, 0o700); err != nil {
		t.Fatal("protect build/ai")
	}
	root, err := os.MkdirTemp(base, "movement-cross-map-live-")
	if err != nil {
		t.Fatal("create isolated cross-map QA root")
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal("protect isolated cross-map QA root")
	}
	if os.Getenv("STONEAGE_MOVEMENT_CROSS_MAP_KEEP_ROOT") == "1" {
		t.Logf("retaining isolated cross-map QA root: %s", root)
	} else {
		defer os.RemoveAll(root)
	}
	for _, name := range []string{"bin", "gopath", "gotmp", "tmp", "home", "cache", "config", "data"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o700); err != nil {
			t.Fatal("create isolated cross-map QA directory")
		}
	}
	for _, name := range []string{"go-cache", "go-modcache"} {
		if err := os.MkdirAll(filepath.Join(base, name), 0o700); err != nil {
			t.Fatal("create reusable Go cache")
		}
	}

	authPath := filepath.Join(root, "auth.db")
	authStore, err := auth.Open(authPath)
	if err != nil {
		t.Fatal("open isolated auth database")
	}
	defer authStore.Close()
	if err := authStore.Migrate(context.Background()); err != nil {
		t.Fatal("migrate isolated auth database")
	}

	profiles, err := airuntime.OpenStore(filepath.Join(root, "profiles.db"))
	if err != nil {
		t.Fatal("open isolated AI profile database")
	}
	defer profiles.Close()
	secrets, err := aiprovision.NewSecretStore(filepath.Join(root, "game-secrets"))
	if err != nil {
		t.Fatal("open isolated game secret store")
	}

	gatewayBinary := movementCrossMapLiveBuildGateway(t, repoRoot, root)
	gateway, err := movementCrossMapLiveStartGateway(t, gatewayBinary, root, authPath)
	if err != nil {
		t.Fatal("start isolated authenticated gateway")
	}
	defer gateway.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	provisioner, err := aiprovision.New(aiprovision.Config{
		ProviderConfig: aiprovision.ProviderConfig{
			Auth: authStore, Secrets: secrets, Game: aiprovision.AigameConnector{},
			GameConfig: aigame.Config{Address: gateway.Address(), DialTimeout: 5 * time.Second, EventBuffer: 256},
		},
		Profiles: profiles, AccountPrefix: "ai",
	})
	if err != nil {
		t.Fatal("create isolated AI provisioner")
	}
	provider := provisioner.Provider()
	defer provider.Close()

	characterName := "AIBot" + movementCrossMapLiveHex(t, 12)
	profileID := "cross-map-" + movementCrossMapLiveHex(t, 12)
	created, err := provisioner.CreateAI(ctx, aiprovision.CreateRequest{
		ProfileID:       profileID,
		CharacterSlot:   0,
		CharacterName:   characterName,
		CharacterCreate: movementCrossMapLiveDefaultCharacterCreate(),
		Profile: airuntime.Profile{
			Status:         airuntime.ProfileStatusActive,
			UnlimitedFunds: true,
		},
		Actor: "aiservice-cross-map-live-test",
	})
	if err != nil {
		t.Fatal("CreateAI did not create a fresh game identity")
	}
	if created.Account.ID <= 0 || created.Account.Status != auth.AccountActive ||
		created.Account.Username == "" || !strings.HasPrefix(created.Account.Username, "ai_") ||
		created.Binding.CharacterSlot != 0 || created.Binding.CharacterName != characterName ||
		created.Binding.AccountID != created.Account.ID || !created.Profile.UnlimitedFunds {
		t.Fatal("CreateAI returned an unexpected fresh AI identity")
	}

	// Read the profile back from the isolated store. Provider.Open must resolve
	// the same durable binding rather than relying on an in-memory create result.
	profile, err := profiles.GetProfile(ctx, created.Profile.ID)
	if err != nil {
		t.Fatal("fresh AI profile was not persisted")
	}
	if profile.Account.Username != created.Account.Username || profile.Character.Name != characterName || !profile.UnlimitedFunds {
		t.Fatal("persisted fresh AI profile does not match CreateAI")
	}

	lease, err := movementCrossMapLiveOpenWithRetry(ctx, provider, profile)
	if err != nil {
		t.Fatal("Provider.Open could not enter the fresh AI character")
	}
	defer lease.Close()
	initialSnapshot, err := movementCrossMapLiveWaitSnapshot(ctx, lease.Session, func(snapshot aigame.Snapshot) bool {
		return snapshot.Account == created.Account.Username && snapshot.Character == characterName &&
			snapshot.Phase == aigame.PhaseWorld && snapshot.Connected && snapshot.Player.HasStatus &&
			snapshot.Position.Floor == 1006 && snapshot.Position.X == 15 && snapshot.Position.Y == 22
	})
	if err != nil {
		t.Fatalf("fresh AI did not reach the authoritative hometown position: %v", err)
	}

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

	gate := aicontrol.New()
	defer gate.Close()
	state, _, err := gate.Switch(gate.State().Generation, aicontrol.Agent, "fresh AI cross-map movement QA")
	if err != nil {
		t.Fatal("claim Agent control")
	}
	binding := aimcp.Binding{
		AccountID: created.Account.Username, CharacterID: created.Binding.CharacterID,
		CharacterName: created.Binding.CharacterName, Generation: state.Generation,
	}
	backend := &GameBackend{
		Binding: binding, Gate: gate, Owner: aicontrol.Agent, Session: lease.Session,
		Funding:         func(context.Context) (bool, error) { return profile.UnlimitedFunds, nil },
		Knowledge:       knowledge,
		OwnStateRefresh: &OwnStateRefresher{},
	}
	ownStateContext, ownStateCancel := context.WithTimeout(ctx, 5*time.Second)
	observed, err := movementCrossMapLiveWaitObservation(ownStateContext, backend, binding, func(observation aimcp.Observation) bool {
		return observation.Flags["savepoint:0"] && observation.Flags["inventory:known"]
	})
	ownStateCancel()
	if err != nil {
		t.Fatalf("observe fresh AI savepoint and backpack through GameBackend: %v", err)
	}
	if observed.Floor != 1006 || observed.X != 15 || observed.Y != 22 || !observed.Connected || !observed.Ready {
		t.Fatalf("GameBackend did not expose the fresh hometown position: %+v", observed)
	}

	target := aiknowledge.Point{Floor: 1000, X: 98, Y: 44}
	skill := &MovementSkill{
		Backend: backend, Navigator: navigator, WarpGraph: aiplanner.NewWarpGraph(knowledge),
		SegmentTimeout: 5 * time.Second, WarpConfirmationTimeout: 8 * time.Second,
	}
	staticEdges, err := skill.findCrossMapRoute(ctx,
		aiplanner.NewWarpGraph(knowledge), aiknowledge.Point{Floor: observed.Floor, X: observed.X, Y: observed.Y}, target)
	if err != nil {
		t.Fatalf("verified knowledge has no walkable cross-map route: %v", err)
	}
	if len(staticEdges) == 0 {
		t.Fatal("verified knowledge returned an empty cross-map route")
	}

	arguments, err := json.Marshal(movementArguments{Floor: target.Floor, X: target.X, Y: target.Y})
	if err != nil {
		t.Fatal("encode cross-map movement arguments")
	}
	action := automation.Action{Skill: "move", ExpectedRevision: observed.Revision, Arguments: arguments}
	if err := skill.Execute(ctx, action); err != nil {
		failedSnapshot, observeErr := lease.Session.Observe(context.Background())
		t.Logf("cross-map action failure snapshot=%+v observe_error=%v", failedSnapshot, observeErr)
		t.Fatalf("real cross-map MovementSkill execution failed: %v", err)
	}

	finalObservation, err := movementCrossMapLiveWaitObservation(ctx, backend, binding, func(observation aimcp.Observation) bool {
		return observation.Connected && observation.Ready && observation.Phase == string(aigame.PhaseWorld) &&
			observation.Floor == target.Floor && observation.X == target.X && observation.Y == target.Y
	})
	if err != nil {
		t.Fatalf("GameBackend did not confirm the exact cross-map destination: %v", err)
	}
	finalSnapshot, err := movementCrossMapLiveWaitSnapshot(ctx, lease.Session, func(snapshot aigame.Snapshot) bool {
		return snapshot.Phase == aigame.PhaseWorld && snapshot.Connected && snapshot.Player.HasStatus &&
			snapshot.Position.Floor == int32(target.Floor) && snapshot.Position.X == int32(target.X) && snapshot.Position.Y == int32(target.Y)
	})
	if err != nil {
		t.Fatalf("session snapshot did not confirm the exact cross-map destination: %v", err)
	}
	if finalObservation.Revision <= observed.Revision || finalSnapshot.Revision <= initialSnapshot.Revision {
		t.Fatalf("cross-map action did not advance the authoritative revision: initial=%d observed=%d final=%d snapshot=%d", initialSnapshot.Revision, observed.Revision, finalObservation.Revision, finalSnapshot.Revision)
	}
	if !finalSnapshot.AI.Received || !finalSnapshot.AI.SavePointsKnown || !finalSnapshot.AI.ItemsKnown {
		t.Fatal("QA server must expose the current savepoint and backpack observation extension")
	}
	if !finalObservation.Flags["savepoint:0"] || !finalObservation.Flags["inventory:known"] {
		t.Fatal("fresh hometown-0 character's authoritative quest prerequisites were not projected")
	}

	evidence := movementCrossMapLiveEvidence{
		Test: "TestLiveMovementCrossMapFreshAI", Status: "passed", Gateway: gateway.Address(),
		Upstream: movementCrossMapLiveUpstream, ProfileID: created.Profile.ID,
		AccountID: created.Account.ID, AccountUsername: created.Account.Username,
		CharacterID: created.Binding.CharacterID, CharacterName: created.Binding.CharacterName,
		UnlimitedFunds: profile.UnlimitedFunds, KnowledgeDigest: knowledge.Fingerprint(),
		SavePoints:       finalSnapshot.AI.SavePoints,
		SavePointsKnown:  finalSnapshot.AI.SavePointsKnown,
		BackpackKnown:    finalSnapshot.AI.ItemsKnown,
		BackpackSlots:    len(finalSnapshot.AI.Items),
		Data:             dataEvidence,
		Initial:          movementCrossMapLiveSnapshotEvidence{Revision: initialSnapshot.Revision, Phase: string(initialSnapshot.Phase), Connected: initialSnapshot.Connected, Floor: int(initialSnapshot.Position.Floor), X: int(initialSnapshot.Position.X), Y: int(initialSnapshot.Position.Y)},
		Observed:         movementCrossMapLiveObservationEvidence{Revision: observed.Revision, Phase: observed.Phase, Connected: observed.Connected, Ready: observed.Ready, Floor: observed.Floor, X: observed.X, Y: observed.Y},
		Target:           movementCrossMapLivePointEvidence{Floor: target.Floor, X: target.X, Y: target.Y},
		Final:            movementCrossMapLiveObservationEvidence{Revision: finalObservation.Revision, Phase: finalObservation.Phase, Connected: finalObservation.Connected, Ready: finalObservation.Ready, Floor: finalObservation.Floor, X: finalObservation.X, Y: finalObservation.Y},
		FinalSession:     movementCrossMapLiveSnapshotEvidence{Revision: finalSnapshot.Revision, Phase: string(finalSnapshot.Phase), Connected: finalSnapshot.Connected, Floor: int(finalSnapshot.Position.Floor), X: int(finalSnapshot.Position.X), Y: int(finalSnapshot.Position.Y)},
		ExpectedRevision: action.ExpectedRevision, WarpEdges: movementCrossMapLiveWarpEvidenceList(staticEdges),
		RecordedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := persistMovementCrossMapLiveEvidence(repoRoot, evidence); err != nil {
		t.Fatal("write cross-map live evidence")
	}
	t.Logf("cross-map live evidence written to %s: initial=%d (%d,%d,%d) final=%d (%d,%d,%d)",
		filepath.Join(repoRoot, movementCrossMapLiveEvidenceRel), initialSnapshot.Revision,
		initialSnapshot.Position.Floor, initialSnapshot.Position.X, initialSnapshot.Position.Y,
		finalObservation.Revision, finalObservation.Floor, finalObservation.X, finalObservation.Y)
}

func movementCrossMapLiveDefaultCharacterCreate() aigame.CharacterCreate {
	return aigame.CharacterCreate{
		Image: 100000, FaceImage: 30000,
		Vital: 5, Strength: 5, Toughness: 5, Dexterity: 5,
		Earth: 10, Hometown: 0,
	}
}

// movementCrossMapLiveFreshAI is the isolated identity/session fixture shared
// by opt-in live checks which need a newly created character. The fixture
// owns every temporary store and process so a test cannot accidentally reuse
// an operator account or leave a live session behind.
type movementCrossMapLiveFreshAI struct {
	Root        string
	RepoRoot    string
	AuthStore   *auth.Store
	Profiles    *airuntime.Store
	Provisioner *aiprovision.Provisioner
	Provider    *aiprovision.ProfileSessionProvider
	Gateway     *movementCrossMapLiveGateway
	Created     aiprovision.Provisioned
	Profile     airuntime.Profile
	Lease       aiprovision.SessionLease
	Initial     aigame.Snapshot
}

// movementCrossMapLiveProvisionFreshAI provisions and opens one isolated AI
// character for a concrete live QA check. It deliberately mirrors the
// movement test's fresh-account path and keeps the default profile capability
// explicit: the AI character has unlimited in-game funds.
func movementCrossMapLiveProvisionFreshAI(t *testing.T, ctx context.Context, profilePrefix, actor string) *movementCrossMapLiveFreshAI {
	return movementCrossMapLiveProvisionFreshAIHometown(t, ctx, profilePrefix, actor, 0)
}

func movementCrossMapLiveProvisionFreshAIHometown(t *testing.T, ctx context.Context, profilePrefix, actor string, hometown int) *movementCrossMapLiveFreshAI {
	t.Helper()
	// char_data.c elders[]: normal character creation, no teleport or stat edits.
	starts := []aigame.Point{{Floor: 1006, X: 15, Y: 22}, {Floor: 2006, X: 20, Y: 16}, {Floor: 3006, X: 21, Y: 16}, {Floor: 4006, X: 14, Y: 20}}
	if hometown < 0 || hometown >= len(starts) {
		t.Fatal("invalid fresh QA hometown")
	}
	start := starts[hometown]
	if ctx == nil {
		ctx = context.Background()
	}
	repoRoot := movementCrossMapLiveRepositoryRoot(t)
	base := filepath.Join(repoRoot, "build", "ai")
	if err := os.MkdirAll(base, 0o700); err != nil {
		t.Fatal("create build/ai")
	}
	if err := os.Chmod(base, 0o700); err != nil {
		t.Fatal("protect build/ai")
	}
	root, err := os.MkdirTemp(base, "movement-cross-map-live-")
	if err != nil {
		t.Fatal("create isolated live QA root")
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal("protect isolated live QA root")
	}
	fixture := &movementCrossMapLiveFreshAI{Root: root, RepoRoot: repoRoot}
	keepRoot := os.Getenv("STONEAGE_MOVEMENT_CROSS_MAP_KEEP_ROOT") == "1"
	t.Cleanup(func() {
		if fixture.Lease.Close != nil {
			fixture.Lease.Close()
		}
		if fixture.Provider != nil {
			fixture.Provider.Close()
		}
		if fixture.Gateway != nil {
			fixture.Gateway.Stop()
		}
		if fixture.Profiles != nil {
			fixture.Profiles.Close()
		}
		if fixture.AuthStore != nil {
			fixture.AuthStore.Close()
		}
		if keepRoot {
			t.Logf("retaining isolated live QA root: %s", fixture.Root)
			return
		}
		_ = os.RemoveAll(fixture.Root)
	})
	for _, name := range []string{"bin", "gopath", "gotmp", "tmp", "home", "cache", "config", "data"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o700); err != nil {
			t.Fatal("create isolated live QA directory")
		}
	}
	for _, name := range []string{"go-cache", "go-modcache"} {
		if err := os.MkdirAll(filepath.Join(base, name), 0o700); err != nil {
			t.Fatal("create reusable Go cache")
		}
	}

	authPath := filepath.Join(root, "auth.db")
	fixture.AuthStore, err = auth.Open(authPath)
	if err != nil {
		t.Fatal("open isolated auth database")
	}
	if err := fixture.AuthStore.Migrate(context.Background()); err != nil {
		t.Fatal("migrate isolated auth database")
	}
	fixture.Profiles, err = airuntime.OpenStore(filepath.Join(root, "profiles.db"))
	if err != nil {
		t.Fatal("open isolated AI profile database")
	}
	secrets, err := aiprovision.NewSecretStore(filepath.Join(root, "game-secrets"))
	if err != nil {
		t.Fatal("open isolated game secret store")
	}

	gatewayBinary := movementCrossMapLiveBuildGateway(t, repoRoot, root)
	fixture.Gateway, err = movementCrossMapLiveStartGateway(t, gatewayBinary, root, authPath)
	if err != nil {
		t.Fatal("start isolated authenticated gateway")
	}
	fixture.Provisioner, err = aiprovision.New(aiprovision.Config{
		ProviderConfig: aiprovision.ProviderConfig{
			Auth: fixture.AuthStore, Secrets: secrets, Game: aiprovision.AigameConnector{},
			GameConfig: aigame.Config{Address: fixture.Gateway.Address(), DialTimeout: 5 * time.Second, EventBuffer: 256},
		},
		Profiles: fixture.Profiles, AccountPrefix: "ai",
	})
	if err != nil {
		t.Fatal("create isolated AI provisioner")
	}
	fixture.Provider = fixture.Provisioner.Provider()
	if fixture.Provider == nil {
		t.Fatal("create isolated AI session provider")
	}

	profilePrefix = strings.TrimSpace(profilePrefix)
	if profilePrefix == "" {
		profilePrefix = "live"
	}
	actor = strings.TrimSpace(actor)
	if actor == "" {
		actor = "aiservice-live-test"
	}
	characterName := "AIBot" + movementCrossMapLiveHex(t, 12)
	profileID := profilePrefix + "-" + movementCrossMapLiveHex(t, 12)
	characterCreate := movementCrossMapLiveDefaultCharacterCreate()
	characterCreate.Hometown = int32(hometown)
	fixture.Created, err = fixture.Provisioner.CreateAI(ctx, aiprovision.CreateRequest{
		ProfileID:       profileID,
		CharacterSlot:   0,
		CharacterName:   characterName,
		CharacterCreate: characterCreate,
		Profile: airuntime.Profile{
			Status:         airuntime.ProfileStatusActive,
			UnlimitedFunds: true,
		},
		Actor: actor,
	})
	if err != nil {
		t.Fatal("CreateAI did not create a fresh game identity")
	}
	if fixture.Created.Account.ID <= 0 || fixture.Created.Account.Status != auth.AccountActive ||
		fixture.Created.Account.Username == "" || !strings.HasPrefix(fixture.Created.Account.Username, "ai_") ||
		fixture.Created.Binding.CharacterSlot != 0 || fixture.Created.Binding.CharacterName != characterName ||
		fixture.Created.Binding.AccountID != fixture.Created.Account.ID ||
		!fixture.Created.Profile.UnlimitedFunds {
		t.Fatal("CreateAI returned an unexpected fresh AI identity")
	}
	fixture.Profile, err = fixture.Profiles.GetProfile(ctx, fixture.Created.Profile.ID)
	if err != nil {
		t.Fatal("fresh AI profile was not persisted")
	}
	if fixture.Profile.Account.Username != fixture.Created.Account.Username ||
		fixture.Profile.Character.Name != characterName || !fixture.Profile.UnlimitedFunds {
		t.Fatal("persisted fresh AI profile does not match CreateAI")
	}
	fixture.Lease, err = movementCrossMapLiveOpenWithRetry(ctx, fixture.Provider, fixture.Profile)
	if err != nil {
		t.Fatal("Provider.Open could not enter the fresh AI character")
	}
	fixture.Initial, err = movementCrossMapLiveWaitSnapshot(ctx, fixture.Lease.Session, func(snapshot aigame.Snapshot) bool {
		return snapshot.Account == fixture.Created.Account.Username && snapshot.Character == characterName &&
			snapshot.Phase == aigame.PhaseWorld && snapshot.Connected && snapshot.Player.HasStatus &&
			snapshot.Position.Floor == start.Floor && snapshot.Position.X == start.X && snapshot.Position.Y == start.Y
	})
	if err != nil {
		t.Fatal("fresh AI did not reach the authoritative hometown position")
	}
	return fixture
}

func movementCrossMapLiveRepositoryRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve cross-map live test location")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
}

func movementCrossMapLiveHex(t *testing.T, size int) string {
	t.Helper()
	if size <= 0 {
		t.Fatal("invalid cross-map live random suffix size")
	}
	raw := make([]byte, (size+1)/2)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal("generate cross-map live identity suffix")
	}
	return hex.EncodeToString(raw)[:size]
}

func movementCrossMapLiveBuildGateway(t *testing.T, repoRoot, root string) string {
	t.Helper()
	binary := filepath.Join(root, "bin", "stoneage-gateway")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, "go", "build", "-mod=readonly", "-o", binary, "./cmd/stoneage-gateway")
	command.Dir = repoRoot
	command.Env = movementCrossMapLiveGoEnvironment(root)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("build isolated stoneage-gateway: %v: %s", err, strings.TrimSpace(string(output)))
	}
	info, err := os.Stat(binary)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		t.Fatal("built stoneage-gateway is unavailable")
	}
	return binary
}

type movementCrossMapLiveGateway struct {
	command  *exec.Cmd
	done     chan error
	logFile  *os.File
	address  string
	mu       sync.Mutex
	exited   bool
	stopOnce sync.Once
}

func movementCrossMapLiveStartGateway(t *testing.T, binary, root, authPath string) (*movementCrossMapLiveGateway, error) {
	t.Helper()
	reservation, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, errors.New("reserve loopback gateway port")
	}
	address := reservation.Addr().String()
	_ = reservation.Close()

	logFile, err := os.OpenFile(filepath.Join(root, "gateway.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, errors.New("create private gateway log")
	}
	command := exec.Command(binary,
		"-listen", address, "-upstream", movementCrossMapLiveUpstream, "-trace",
		"-auth-required", "-auth-db", authPath,
	)
	command.Dir = root
	command.Env = movementCrossMapLiveGoEnvironment(root)
	command.Stdout = logFile
	command.Stderr = logFile
	if err := command.Start(); err != nil {
		_ = logFile.Close()
		return nil, errors.New("start authenticated gateway")
	}
	gateway := &movementCrossMapLiveGateway{command: command, done: make(chan error, 1), logFile: logFile, address: address}
	go func() {
		err := command.Wait()
		gateway.mu.Lock()
		gateway.exited = true
		gateway.mu.Unlock()
		gateway.done <- err
	}()
	if !movementCrossMapLiveWaitForGateway(gateway, 15*time.Second) {
		gateway.Stop()
		return nil, errors.New("authenticated gateway did not become ready")
	}
	return gateway, nil
}

func (gateway *movementCrossMapLiveGateway) Address() string {
	if gateway == nil {
		return ""
	}
	return gateway.address
}

func (gateway *movementCrossMapLiveGateway) Stop() {
	if gateway == nil {
		return
	}
	gateway.stopOnce.Do(func() {
		if !gateway.isExited() && gateway.command.Process != nil {
			_ = gateway.command.Process.Signal(syscall.SIGTERM)
			select {
			case <-gateway.done:
			case <-time.After(5 * time.Second):
				_ = gateway.command.Process.Kill()
				select {
				case <-gateway.done:
				case <-time.After(2 * time.Second):
				}
			}
		}
		_ = gateway.logFile.Close()
	})
}

func (gateway *movementCrossMapLiveGateway) isExited() bool {
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	return gateway.exited
}

func movementCrossMapLiveWaitForGateway(gateway *movementCrossMapLiveGateway, timeout time.Duration) bool {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		connection, err := net.DialTimeout("tcp", gateway.Address(), 200*time.Millisecond)
		if err == nil {
			_ = connection.Close()
			return true
		}
		if gateway.isExited() {
			return false
		}
		select {
		case <-deadline.C:
			return false
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func movementCrossMapLiveGoEnvironment(root string) []string {
	base := filepath.Dir(root)
	paths := map[string]string{
		"HOME":            filepath.Join(root, "home"),
		"TMPDIR":          filepath.Join(root, "tmp"),
		"TMP":             filepath.Join(root, "tmp"),
		"TEMP":            filepath.Join(root, "tmp"),
		"GOCACHE":         filepath.Join(base, "go-cache"),
		"GOMODCACHE":      filepath.Join(base, "go-modcache"),
		"GOPATH":          filepath.Join(root, "gopath"),
		"GOTMPDIR":        filepath.Join(root, "gotmp"),
		"XDG_CACHE_HOME":  filepath.Join(root, "cache"),
		"XDG_CONFIG_HOME": filepath.Join(root, "config"),
		"XDG_DATA_HOME":   filepath.Join(root, "data"),
		"GOPROXY":         "off",
		"GOSUMDB":         "off",
	}
	result := make([]string, 0, len(os.Environ())+len(paths))
	for _, entry := range os.Environ() {
		key, _, ok := strings.Cut(entry, "=")
		if ok {
			if _, replace := paths[key]; replace {
				continue
			}
		}
		result = append(result, entry)
	}
	for key, value := range paths {
		result = append(result, key+"="+value)
	}
	return result
}

func movementCrossMapLiveOpenWithRetry(ctx context.Context, provider *aiprovision.ProfileSessionProvider, profile airuntime.Profile) (aiprovision.SessionLease, error) {
	deadlineCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	for {
		lease, err := provider.Open(deadlineCtx, profile)
		if err == nil {
			return lease, nil
		}
		if deadlineCtx.Err() != nil || (!errors.Is(err, aiprovision.ErrGameSession) &&
			!errors.Is(err, aiprovision.ErrCharacterUnavailable) && !errors.Is(err, aiprovision.ErrAlreadyOpen)) {
			return aiprovision.SessionLease{}, err
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-deadlineCtx.Done():
			timer.Stop()
			return aiprovision.SessionLease{}, deadlineCtx.Err()
		case <-timer.C:
		}
	}
}

func movementCrossMapLiveWaitSnapshot(ctx context.Context, session aiprovision.HeadlessSession, predicate func(aigame.Snapshot) bool) (aigame.Snapshot, error) {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		snapshot, err := session.Observe(ctx)
		if err != nil {
			return aigame.Snapshot{}, err
		}
		if predicate(snapshot) {
			return snapshot, nil
		}
		select {
		case <-ctx.Done():
			return aigame.Snapshot{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

func movementCrossMapLiveWaitObservation(ctx context.Context, backend *GameBackend, binding aimcp.Binding, predicate func(aimcp.Observation) bool) (aimcp.Observation, error) {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		observation, err := backend.Observe(ctx, binding)
		if err != nil {
			return aimcp.Observation{}, err
		}
		if predicate(observation) {
			return observation, nil
		}
		select {
		case <-ctx.Done():
			return aimcp.Observation{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

type movementCrossMapLivePointEvidence struct {
	Floor int `json:"floor"`
	X     int `json:"x"`
	Y     int `json:"y"`
}

type movementCrossMapLiveSnapshotEvidence struct {
	Revision  uint64 `json:"revision"`
	Phase     string `json:"phase"`
	Connected bool   `json:"connected"`
	Floor     int    `json:"floor"`
	X         int    `json:"x"`
	Y         int    `json:"y"`
}

type movementCrossMapLiveObservationEvidence struct {
	Revision  uint64 `json:"revision"`
	Phase     string `json:"phase"`
	Connected bool   `json:"connected"`
	Ready     bool   `json:"ready"`
	Floor     int    `json:"floor"`
	X         int    `json:"x"`
	Y         int    `json:"y"`
}

type movementCrossMapLiveWarpEvidence struct {
	Type      string                            `json:"type"`
	Time      string                            `json:"time"`
	From      movementCrossMapLivePointEvidence `json:"from"`
	To        movementCrossMapLivePointEvidence `json:"to"`
	Attribute string                            `json:"attribute,omitempty"`
	Source    aiknowledge.SourceRef             `json:"source"`
}

type movementCrossMapLiveEvidence struct {
	SavePoints       int32                                   `json:"savepoints"`
	SavePointsKnown  bool                                    `json:"savepoints_known"`
	BackpackKnown    bool                                    `json:"backpack_known"`
	BackpackSlots    int                                     `json:"backpack_occupied_slots"`
	Test             string                                  `json:"test"`
	Status           string                                  `json:"status"`
	Gateway          string                                  `json:"gateway"`
	Upstream         string                                  `json:"upstream"`
	ProfileID        string                                  `json:"profile_id"`
	AccountID        int64                                   `json:"account_id"`
	AccountUsername  string                                  `json:"account_username"`
	CharacterID      string                                  `json:"character_id"`
	CharacterName    string                                  `json:"character_name"`
	UnlimitedFunds   bool                                    `json:"unlimited_funds"`
	KnowledgeDigest  string                                  `json:"knowledge_digest"`
	Data             movementCrossMapLiveDataEvidence        `json:"data"`
	Initial          movementCrossMapLiveSnapshotEvidence    `json:"initial"`
	Observed         movementCrossMapLiveObservationEvidence `json:"observed"`
	Target           movementCrossMapLivePointEvidence       `json:"target"`
	Final            movementCrossMapLiveObservationEvidence `json:"final"`
	FinalSession     movementCrossMapLiveSnapshotEvidence    `json:"final_session"`
	ExpectedRevision uint64                                  `json:"expected_revision"`
	WarpEdges        []movementCrossMapLiveWarpEvidence      `json:"warp_edges"`
	RecordedAt       string                                  `json:"recorded_at"`
}

type movementCrossMapLiveDataEvidence struct {
	KnowledgeRoot     string `json:"knowledge_root"`
	EffectiveRoot     string `json:"effective_root"`
	EffectiveVerified bool   `json:"effective_verified"`
	MapwarpSHA256     string `json:"mapwarp_sha256"`
	MapsetSHA256      string `json:"mapset_sha256"`
	Floor1006Source   string `json:"floor_1006_source"`
	Floor1006SHA256   string `json:"floor_1006_sha256"`
	Floor1000Source   string `json:"floor_1000_source"`
	Floor1000SHA256   string `json:"floor_1000_sha256"`
}

// movementCrossMapLiveVerifyEffectiveData checks the map files used to plan
// the action against the mounted QA data. The server process itself remains
// untouched; this catches a QA container accidentally serving a different
// mapwarp or LS2MAP revision than the reviewed knowledge snapshot.
func movementCrossMapLiveVerifyEffectiveData(ctx context.Context, repoRoot string, navigator *ainavigation.Navigator) (movementCrossMapLiveDataEvidence, error) {
	knowledgeRoot := filepath.Join(repoRoot, "runtime", "legacy-server", "gmsv", "data")
	effectiveRoot := movementCrossMapLiveEffectiveRoot(repoRoot)
	effectiveRoot, err := filepath.Abs(effectiveRoot)
	if err != nil {
		return movementCrossMapLiveDataEvidence{}, fmt.Errorf("resolve effective data root: %w", err)
	}
	info, err := os.Stat(effectiveRoot)
	if err != nil || !info.IsDir() {
		return movementCrossMapLiveDataEvidence{}, fmt.Errorf("effective QA data root %q is unavailable; set STONEAGE_MOVEMENT_CROSS_MAP_EFFECTIVE_DATA_DIR to the mounted GMSV data", effectiveRoot)
	}
	effectiveNavigator, err := ainavigation.LoadDataDir(ctx, effectiveRoot)
	if err != nil {
		return movementCrossMapLiveDataEvidence{}, fmt.Errorf("load effective QA navigation: %w", err)
	}
	mapwarpHash, err := movementCrossMapLiveCompareFile(knowledgeRoot, effectiveRoot, "map/mapwarp.txt")
	if err != nil {
		return movementCrossMapLiveDataEvidence{}, err
	}
	mapsetHash, err := movementCrossMapLiveCompareFile(knowledgeRoot, effectiveRoot, "map/mapset.txt")
	if err != nil {
		return movementCrossMapLiveDataEvidence{}, err
	}
	result := movementCrossMapLiveDataEvidence{
		KnowledgeRoot: filepath.ToSlash(filepath.Join("runtime", "legacy-server", "gmsv", "data")),
		EffectiveRoot: movementCrossMapLiveEvidencePath(repoRoot, effectiveRoot), EffectiveVerified: true,
		MapwarpSHA256: mapwarpHash, MapsetSHA256: mapsetHash,
	}
	for _, floorID := range []int{1006, 1000} {
		knowledgeFloor, ok := navigator.Floor(floorID)
		if !ok {
			return movementCrossMapLiveDataEvidence{}, fmt.Errorf("knowledge source has no floor %d", floorID)
		}
		effectiveFloor, ok := effectiveNavigator.Floor(floorID)
		if !ok {
			return movementCrossMapLiveDataEvidence{}, fmt.Errorf("effective QA data has no floor %d", floorID)
		}
		// Floor.Source is relative to the map directory, as retained by
		// ainavigation.LoadDataDir, rather than relative to the data root.
		knowledgePath := filepath.Join(knowledgeRoot, "map", filepath.FromSlash(knowledgeFloor.Source))
		effectivePath := filepath.Join(effectiveRoot, "map", filepath.FromSlash(effectiveFloor.Source))
		knowledgeHash, err := movementCrossMapLiveSHA256File(knowledgePath)
		if err != nil {
			return movementCrossMapLiveDataEvidence{}, fmt.Errorf("hash knowledge floor %d: %w", floorID, err)
		}
		effectiveHash, err := movementCrossMapLiveSHA256File(effectivePath)
		if err != nil {
			return movementCrossMapLiveDataEvidence{}, fmt.Errorf("hash effective floor %d: %w", floorID, err)
		}
		if knowledgeHash != effectiveHash {
			return movementCrossMapLiveDataEvidence{}, fmt.Errorf("effective QA floor %d bytes differ from knowledge source", floorID)
		}
		if floorID == 1006 {
			result.Floor1006Source, result.Floor1006SHA256 = effectiveFloor.Source, effectiveHash
		} else {
			result.Floor1000Source, result.Floor1000SHA256 = effectiveFloor.Source, effectiveHash
		}
	}
	return result, nil
}

func movementCrossMapLiveCompareFile(leftRoot, rightRoot, relative string) (string, error) {
	left, err := movementCrossMapLiveSHA256File(filepath.Join(leftRoot, filepath.FromSlash(relative)))
	if err != nil {
		return "", fmt.Errorf("hash knowledge %s: %w", relative, err)
	}
	right, err := movementCrossMapLiveSHA256File(filepath.Join(rightRoot, filepath.FromSlash(relative)))
	if err != nil {
		return "", fmt.Errorf("hash effective QA %s: %w", relative, err)
	}
	if left != right {
		return "", fmt.Errorf("effective QA %s bytes differ from knowledge source", relative)
	}
	return right, nil
}

func movementCrossMapLiveSHA256File(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func movementCrossMapLiveEvidencePath(repoRoot, path string) string {
	if relative, err := filepath.Rel(repoRoot, path); err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return filepath.ToSlash(relative)
	}
	return filepath.ToSlash(path)
}

func movementCrossMapLiveWarpEvidenceList(edges []aiplanner.WarpEdge) []movementCrossMapLiveWarpEvidence {
	result := make([]movementCrossMapLiveWarpEvidence, 0, len(edges))
	for _, edge := range edges {
		result = append(result, movementCrossMapLiveWarpEvidence{
			Type: edge.Type, Time: edge.Time,
			From:      movementCrossMapLivePointEvidence{Floor: edge.From.Floor, X: edge.From.X, Y: edge.From.Y},
			To:        movementCrossMapLivePointEvidence{Floor: edge.To.Floor, X: edge.To.X, Y: edge.To.Y},
			Attribute: edge.Attribute, Source: edge.Source,
		})
	}
	return result
}

func persistMovementCrossMapLiveEvidence(repoRoot string, evidence movementCrossMapLiveEvidence) error {
	data, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		return errors.New("marshal cross-map live evidence")
	}
	data = append(data, '\n')
	base := filepath.Join(repoRoot, "build", "ai")
	if err := os.MkdirAll(base, 0o700); err != nil {
		return errors.New("create cross-map live evidence directory")
	}
	if err := os.Chmod(base, 0o700); err != nil {
		return errors.New("protect cross-map live evidence directory")
	}
	target := filepath.Join(repoRoot, movementCrossMapLiveEvidenceRel)
	if info, err := os.Lstat(target); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return errors.New("cross-map live evidence target is unsafe")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("inspect cross-map live evidence target")
	}
	temporary, err := os.CreateTemp(base, ".cross-map-live-evidence-*.tmp")
	if err != nil {
		return errors.New("create cross-map live evidence temporary file")
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return errors.New("protect cross-map live evidence")
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return errors.New("write cross-map live evidence")
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return errors.New("sync cross-map live evidence")
	}
	if err := temporary.Close(); err != nil {
		return errors.New("close cross-map live evidence")
	}
	if err := os.Rename(temporaryName, target); err != nil {
		return errors.New("install cross-map live evidence")
	}
	return nil
}

func movementCrossMapLiveEffectiveRoot(repoRoot string) string {
	effectiveRoot := strings.TrimSpace(os.Getenv("STONEAGE_MOVEMENT_CROSS_MAP_EFFECTIVE_DATA_DIR"))
	if effectiveRoot == "" {
		effectiveRoot = filepath.Join(repoRoot, "build", "player-integration", "game", "gmsv", "data")
	}
	return effectiveRoot
}
