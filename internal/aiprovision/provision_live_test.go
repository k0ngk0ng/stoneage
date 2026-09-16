package aiprovision

import (
	"context"
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

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/airuntime"
	"github.com/k0ngk0ng/stoneage/internal/auth"
)

const (
	provisionLiveOptIn       = "STONEAGE_AIPROVISION_LIVE_TEST"
	provisionLiveUpstream    = "127.0.0.1:29065"
	provisionLiveEvidenceRel = "build/ai/provision-live-evidence.json"
	// The legacy server rejects names containing the GM marker. Excluding both
	// letters from the random suffix makes that check deterministic.
	provisionLiveCharacterAlphabet = "abcdefhijklnopqrstuvwxyz0123456789"
)

// TestLiveAIProvisionCreateBindOwnStateRelogin runs the complete server-owned
// identity path against a real numeric QA GMSV. It is deliberately opt-in:
// the test creates a persistent QA account and character, but does not call a
// model or perform any paid operation.
func TestLiveAIProvisionCreateBindOwnStateRelogin(t *testing.T) {
	if os.Getenv(provisionLiveOptIn) != "1" {
		t.Skip("set STONEAGE_AIPROVISION_LIVE_TEST=1 for the real AI provisioning QA check")
	}

	repoRoot := provisionLiveRepositoryRoot(t)
	base := filepath.Join(repoRoot, "build", "ai")
	if err := os.MkdirAll(base, 0o700); err != nil {
		t.Fatal("create build/ai")
	}
	root, err := os.MkdirTemp(base, "aiprovision-live-")
	if err != nil {
		t.Fatal("create isolated AI provisioning root")
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal("protect isolated AI provisioning root")
	}
	defer os.RemoveAll(root)
	for _, name := range []string{"bin", "gopath", "gotmp", "tmp", "home", "cache", "config", "data"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o700); err != nil {
			t.Fatal("create isolated AI provisioning directory")
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
	secrets, err := NewSecretStore(filepath.Join(root, "game-secrets"))
	if err != nil {
		t.Fatal("open isolated game secret store")
	}

	gatewayBinary := provisionLiveBuildGateway(t, repoRoot, root)
	gateway, err := provisionLiveStartGateway(t, gatewayBinary, root, authPath)
	if err != nil {
		t.Fatal("start isolated authenticated gateway")
	}
	defer gateway.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	provisioner, err := New(Config{
		ProviderConfig: ProviderConfig{
			Auth: authStore, Secrets: secrets, Game: AigameConnector{},
			GameConfig: aigame.Config{Address: gateway.Address(), DialTimeout: 5 * time.Second, EventBuffer: 256},
		},
		Profiles: profiles, AccountPrefix: "ai",
	})
	if err != nil {
		t.Fatal("create AI provisioner")
	}
	defer provisioner.Provider().Close()

	suffix, err := randomString(12, provisionLiveCharacterAlphabet)
	if err != nil {
		t.Fatal("generate random character name")
	}
	profileSuffix, err := randomString(12, usernameAlphabet)
	if err != nil {
		t.Fatal("generate random profile ID")
	}
	profileID := "live-" + profileSuffix
	characterName := "AIBot" + suffix
	created, err := provisioner.CreateAI(ctx, CreateRequest{
		ProfileID:       profileID,
		CharacterSlot:   0,
		CharacterName:   characterName,
		CharacterCreate: provisionLiveDefaultAICharacterCreate(),
		Profile: airuntime.Profile{
			Status: airuntime.ProfileStatusActive,
		},
		Actor: "aiprovision-live-test",
	})
	if err != nil {
		// Do not print a connector or credential error: the test owns the
		// password only through the private SecretStore boundary.
		t.Fatal("CreateAI did not create a real account and character")
	}
	if created.Account.ID <= 0 || created.Account.Status != auth.AccountActive ||
		created.Account.Username == "" || strings.EqualFold(created.Account.Username, "adminqa") ||
		!strings.HasPrefix(created.Account.Username, "ai_") {
		t.Fatal("CreateAI returned an unexpected dedicated account")
	}
	if created.Binding.CharacterSlot != 0 || created.Binding.CharacterName != characterName ||
		created.Binding.AccountID != created.Account.ID ||
		created.Binding.AccountUsername != created.Account.Username ||
		created.Binding.CharacterID != fmt.Sprintf("%s:%d", created.Account.Username, created.Binding.CharacterSlot) {
		t.Fatal("CreateAI binding does not describe the real created identity")
	}
	if err := validateProfileBinding(created.Profile, created.Binding); err != nil || !created.Profile.UnlimitedFunds {
		t.Fatal("CreateAI profile binding or unlimited-funds default is invalid")
	}
	secretPath := filepath.Join(root, "game-secrets", fmt.Sprintf("account-%d.key", created.Account.ID))
	secretInfo, err := os.Stat(secretPath)
	if err != nil || !secretInfo.Mode().IsRegular() || secretInfo.Mode().Perm()&0o077 != 0 {
		t.Fatal("CreateAI did not retain the credential in a private secret file")
	}

	persisted, err := profiles.GetProfile(ctx, created.Profile.ID)
	if err != nil {
		t.Fatal("created AI profile was not persisted")
	}
	if err := validateProfileBinding(persisted, created.Binding); err != nil || !persisted.UnlimitedFunds {
		t.Fatal("persisted AI profile binding does not match the created identity")
	}
	durableBinding, err := provisioner.Binding(ctx, created.Profile.ID)
	if err != nil || !sameProvisionLiveBinding(durableBinding, created.Binding) {
		t.Fatal("durable AI binding does not match the created identity")
	}

	firstLease, err := provisionLiveOpenWithRetry(ctx, provisioner.Provider(), persisted)
	if err != nil {
		t.Fatal("Provider.Open could not enter the newly created character")
	}
	_, err = provisionLiveObserveUntil(ctx, firstLease.Session, func(snapshot aigame.Snapshot) bool {
		return snapshot.Phase == aigame.PhaseWorld && snapshot.Connected &&
			snapshot.Account == created.Account.Username && snapshot.Character == characterName
	})
	if err != nil {
		firstLease.Close()
		t.Fatal("first real session did not enter the created character")
	}
	firstOwnState, err := provisionLiveRequestOwnState(ctx, firstLease.Session)
	if err != nil || !firstOwnState.AI.Received || firstOwnState.AIObservationRevision == 0 ||
		firstOwnState.Phase != aigame.PhaseWorld || !firstOwnState.Connected ||
		firstOwnState.Account != created.Account.Username || firstOwnState.Character != characterName {
		firstLease.Close()
		t.Fatal("status/AI did not return authoritative own-state")
	}
	if !sameProvisionLiveBinding(firstLease.Binding, created.Binding) {
		firstLease.Close()
		t.Fatal("Provider.Open returned a binding different from CreateAI")
	}
	firstLease.Close()

	// Read the same profile back from disk and reopen it. The retry covers the
	// legacy server's asynchronous character-save acknowledgement after the
	// first socket closes; no account or character is deleted from QA.
	persistedAgain, err := profiles.GetProfile(ctx, created.Profile.ID)
	if err != nil {
		t.Fatal("profile binding was not durable for relogin")
	}
	if err := validateProfileBinding(persistedAgain, created.Binding); err != nil {
		t.Fatal("profile binding was not durable for relogin")
	}
	secondLease, err := provisionLiveOpenWithRetry(ctx, provisioner.Provider(), persistedAgain)
	if err != nil {
		t.Fatal("Provider.Open could not relogin the same dedicated identity")
	}
	_, err = provisionLiveObserveUntil(ctx, secondLease.Session, func(snapshot aigame.Snapshot) bool {
		return snapshot.Phase == aigame.PhaseWorld && snapshot.Connected &&
			snapshot.Account == created.Account.Username && snapshot.Character == characterName
	})
	if err != nil {
		secondLease.Close()
		t.Fatal("relogin did not enter the same account and character")
	}
	secondOwnState, err := provisionLiveRequestOwnState(ctx, secondLease.Session)
	if err != nil || !secondOwnState.AI.Received || secondOwnState.AIObservationRevision == 0 ||
		secondOwnState.Phase != aigame.PhaseWorld || !secondOwnState.Connected ||
		secondOwnState.Account != created.Account.Username || secondOwnState.Character != characterName {
		secondLease.Close()
		t.Fatal("relogin status/AI did not return own-state for the same account and character")
	}
	if !sameProvisionLiveBinding(secondLease.Binding, created.Binding) {
		secondLease.Close()
		t.Fatal("relogin binding changed across Provider.Open")
	}
	secondLease.Close()

	evidence := provisionLiveEvidence{
		Test: "TestLiveAIProvisionCreateBindOwnStateRelogin", Status: "passed",
		Gateway: gateway.Address(), Upstream: provisionLiveUpstream, ProfileID: created.Profile.ID,
		AccountID: created.Account.ID, AccountUsername: created.Account.Username,
		CharacterSlot: created.Binding.CharacterSlot, CharacterID: created.Binding.CharacterID,
		CharacterName: created.Binding.CharacterName, Binding: makeProvisionLiveBindingEvidence(created.Binding),
		CreatedProfile: makeProvisionLiveBindingEvidenceFromProfile(created.Profile, created.Account.ID),
		FirstSession:   makeProvisionLiveSessionEvidence(firstOwnState),
		Relogin:        makeProvisionLiveSessionEvidence(secondOwnState),
		RecordedAt:     time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := persistProvisionLiveEvidence(repoRoot, evidence); err != nil {
		t.Fatal("write provision-live evidence")
	}
	t.Logf("AI provisioning live evidence written to %s", filepath.Join(repoRoot, provisionLiveEvidenceRel))
}

func provisionLiveDefaultAICharacterCreate() aigame.CharacterCreate {
	// Keep this in lockstep with cmd/stoneage-admin/defaultAICharacterCreate.
	return aigame.CharacterCreate{
		Image: 100000, FaceImage: 30000,
		Vital: 5, Strength: 5, Toughness: 5, Dexterity: 5,
		Earth: 10, Hometown: 0,
	}
}

func provisionLiveRepositoryRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve aiprovision test location")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
}

func provisionLiveBuildGateway(t *testing.T, repoRoot, root string) string {
	t.Helper()
	binary := filepath.Join(root, "bin", "stoneage-gateway")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, "go", "build", "-mod=readonly", "-o", binary, "./cmd/stoneage-gateway")
	command.Dir = repoRoot
	command.Env = provisionLiveGoEnvironment(root)
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		t.Fatal("build isolated stoneage-gateway")
	}
	info, err := os.Stat(binary)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		t.Fatal("built stoneage-gateway is unavailable")
	}
	return binary
}

type provisionLiveGateway struct {
	command  *exec.Cmd
	done     chan error
	logFile  *os.File
	address  string
	mu       sync.Mutex
	exited   bool
	stopOnce sync.Once
}

func provisionLiveStartGateway(t *testing.T, binary, root, authPath string) (*provisionLiveGateway, error) {
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
		"-listen", address, "-upstream", provisionLiveUpstream,
		"-auth-required", "-auth-db", authPath,
	)
	command.Dir = filepath.Dir(filepath.Dir(binary))
	command.Env = provisionLiveGoEnvironment(root)
	command.Stdout = logFile
	command.Stderr = logFile
	if err := command.Start(); err != nil {
		_ = logFile.Close()
		return nil, errors.New("start authenticated gateway")
	}
	gateway := &provisionLiveGateway{command: command, done: make(chan error, 1), logFile: logFile, address: address}
	go func() {
		err := command.Wait()
		gateway.mu.Lock()
		gateway.exited = true
		gateway.mu.Unlock()
		gateway.done <- err
	}()
	if !provisionLiveWaitForGateway(gateway, 15*time.Second) {
		gateway.Stop()
		return nil, errors.New("authenticated gateway did not become ready")
	}
	return gateway, nil
}

func (gateway *provisionLiveGateway) Address() string {
	if gateway == nil {
		return ""
	}
	return gateway.address
}

func (gateway *provisionLiveGateway) Stop() {
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

func (gateway *provisionLiveGateway) isExited() bool {
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	return gateway.exited
}

func provisionLiveWaitForGateway(gateway *provisionLiveGateway, timeout time.Duration) bool {
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

func provisionLiveGoEnvironment(root string) []string {
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

func provisionLiveOpenWithRetry(ctx context.Context, provider *ProfileSessionProvider, profile airuntime.Profile) (SessionLease, error) {
	deadlineCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	for {
		lease, err := provider.Open(deadlineCtx, profile)
		if err == nil {
			return lease, nil
		}
		if deadlineCtx.Err() != nil || (!errors.Is(err, ErrGameSession) &&
			!errors.Is(err, ErrCharacterUnavailable) && !errors.Is(err, ErrAlreadyOpen)) {
			return SessionLease{}, err
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-deadlineCtx.Done():
			timer.Stop()
			return SessionLease{}, deadlineCtx.Err()
		case <-timer.C:
		}
	}
}

func provisionLiveObserveUntil(ctx context.Context, session HeadlessSession, predicate func(aigame.Snapshot) bool) (aigame.Snapshot, error) {
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

func provisionLiveRequestOwnState(ctx context.Context, session HeadlessSession) (aigame.Snapshot, error) {
	for attempts := 0; attempts < 8; attempts++ {
		snapshot, err := session.Observe(ctx)
		if err != nil {
			return aigame.Snapshot{}, err
		}
		if snapshot.AI.Received {
			return snapshot, nil
		}
		err = session.ExecuteExpected(ctx, snapshot.Revision, aigame.Action{Kind: aigame.ActionStatus, Command: "AI"})
		if errors.Is(err, aigame.ErrStaleRevision) {
			continue
		}
		if err != nil {
			return aigame.Snapshot{}, err
		}
		return provisionLiveObserveUntil(ctx, session, func(current aigame.Snapshot) bool {
			return current.AI.Received
		})
	}
	return aigame.Snapshot{}, aigame.ErrStaleRevision
}

func sameProvisionLiveBinding(left, right Binding) bool {
	return left.ProfileID == right.ProfileID && left.AccountID == right.AccountID &&
		left.AccountUsername == right.AccountUsername && left.CharacterSlot == right.CharacterSlot &&
		left.CharacterID == right.CharacterID && left.CharacterName == right.CharacterName
}

type provisionLiveBindingEvidence struct {
	ProfileID       string `json:"profile_id"`
	AccountID       int64  `json:"account_id"`
	AccountUsername string `json:"account_username"`
	CharacterSlot   int    `json:"character_slot"`
	CharacterID     string `json:"character_id"`
	CharacterName   string `json:"character_name"`
}

func makeProvisionLiveBindingEvidence(binding Binding) provisionLiveBindingEvidence {
	return provisionLiveBindingEvidence{
		ProfileID: binding.ProfileID, AccountID: binding.AccountID, AccountUsername: binding.AccountUsername,
		CharacterSlot: binding.CharacterSlot, CharacterID: binding.CharacterID, CharacterName: binding.CharacterName,
	}
}

func makeProvisionLiveBindingEvidenceFromProfile(profile airuntime.Profile, accountID int64) provisionLiveBindingEvidence {
	return provisionLiveBindingEvidence{
		ProfileID: profile.ID, AccountID: accountID, AccountUsername: profile.Account.Username,
		CharacterID: profile.Character.ID, CharacterName: profile.Character.Name,
	}
}

type provisionLiveSessionEvidence struct {
	Account          string `json:"account"`
	Character        string `json:"character"`
	Phase            string `json:"phase"`
	Connected        bool   `json:"connected"`
	Revision         uint64 `json:"revision"`
	OwnStateReceived bool   `json:"own_state_received"`
	OwnStateRevision uint64 `json:"own_state_revision"`
}

func makeProvisionLiveSessionEvidence(snapshot aigame.Snapshot) provisionLiveSessionEvidence {
	return provisionLiveSessionEvidence{
		Account: snapshot.Account, Character: snapshot.Character, Phase: string(snapshot.Phase),
		Connected: snapshot.Connected, Revision: snapshot.Revision,
		OwnStateReceived: snapshot.AI.Received, OwnStateRevision: snapshot.AIObservationRevision,
	}
}

type provisionLiveEvidence struct {
	Test            string                       `json:"test"`
	Status          string                       `json:"status"`
	Gateway         string                       `json:"gateway"`
	Upstream        string                       `json:"upstream"`
	ProfileID       string                       `json:"profile_id"`
	AccountID       int64                        `json:"account_id"`
	AccountUsername string                       `json:"account_username"`
	CharacterSlot   int                          `json:"character_slot"`
	CharacterID     string                       `json:"character_id"`
	CharacterName   string                       `json:"character_name"`
	Binding         provisionLiveBindingEvidence `json:"binding"`
	CreatedProfile  provisionLiveBindingEvidence `json:"created_profile"`
	FirstSession    provisionLiveSessionEvidence `json:"first_session"`
	Relogin         provisionLiveSessionEvidence `json:"relogin"`
	RecordedAt      string                       `json:"recorded_at"`
}

func persistProvisionLiveEvidence(repoRoot string, evidence provisionLiveEvidence) error {
	data, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		return errors.New("marshal provision-live evidence")
	}
	data = append(data, '\n')
	base := filepath.Join(repoRoot, "build", "ai")
	if err := os.MkdirAll(base, 0o700); err != nil {
		return errors.New("create provision-live evidence directory")
	}
	target := filepath.Join(repoRoot, provisionLiveEvidenceRel)
	if info, err := os.Lstat(target); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return errors.New("provision-live evidence target is unsafe")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("inspect provision-live evidence target")
	}
	temporary, err := os.CreateTemp(base, ".provision-live-evidence-*.tmp")
	if err != nil {
		return errors.New("create provision-live evidence temporary file")
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return errors.New("protect provision-live evidence")
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return errors.New("write provision-live evidence")
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return errors.New("sync provision-live evidence")
	}
	if err := temporary.Close(); err != nil {
		return errors.New("close provision-live evidence")
	}
	if err := os.Rename(temporaryName, target); err != nil {
		return errors.New("install provision-live evidence")
	}
	return nil
}
