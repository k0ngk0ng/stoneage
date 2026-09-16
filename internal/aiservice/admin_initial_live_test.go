package aiservice

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aiinitial"
	"github.com/k0ngk0ng/stoneage/internal/airuntime"
	"github.com/k0ngk0ng/stoneage/internal/characterbuild"
	"github.com/k0ngk0ng/stoneage/internal/gamecatalog"
	"github.com/k0ngk0ng/stoneage/internal/playerbridge"
	"github.com/k0ngk0ng/stoneage/internal/playerdata"
	"github.com/k0ngk0ng/stoneage/internal/playermanager"
)

// TestLiveAdminInitialStates uses the actual admin HTTP creation path and
// native QA initialization bridge. It creates persistent QA characters but
// never starts a model or modifies an operator-owned character.
func TestLiveAdminInitialStates(t *testing.T) {
	if os.Getenv("STONEAGE_ADMIN_INITIAL_LIVE_TEST") != "1" {
		t.Skip("set STONEAGE_ADMIN_INITIAL_LIVE_TEST=1 for real admin initial-state QA")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	f := movementCrossMapLiveProvisionFreshAIHometown(t, ctx, "admin-initial", "admin-initial-live-test", 0)
	f.Lease.Close()
	f.Lease.Close = nil
	stopped := airuntime.ProfileStatusStopped
	var err error
	f.Profile, err = f.Profiles.UpdateProfileCAS(ctx, f.Profile.ID, f.Profile.Version, airuntime.ProfilePatch{Status: &stopped, Actor: "admin-initial-live-test"})
	if err != nil {
		t.Fatal("stop fixture profile before admin startup")
	}
	queueRoot := filepath.Join(f.RepoRoot, "build", "player-integration", "queues")
	queue := func(role string) playerbridge.Queue {
		return playerbridge.Queue{Requests: filepath.Join(queueRoot, role, "requests"), Responses: filepath.Join(queueRoot, role, "responses"), Timeout: 10 * time.Second}
	}
	catalog, err := gamecatalog.Load(filepath.Join(f.RepoRoot, "runtime", "legacy-server", "gmsv", "data"))
	if err != nil {
		t.Fatal("load native catalog")
	}
	manager := &playermanager.Manager{Archives: playerbridge.SAAC{Queue: queue("saac")}, Game: playerbridge.GMSV{Queue: queue("gmsv")}, Catalog: catalog}
	mount, err := manager.DefaultAIMount(ctx)
	if err != nil {
		t.Fatalf("QA native server did not provide a default mount: %v", err)
	}
	password := movementCrossMapLiveHex(t, 32)
	if _, err := f.AuthStore.CreateAdmin(ctx, "initialqa", []byte(password)); err != nil {
		t.Fatal("create isolated QA administrator")
	}
	for _, target := range []string{"stoneage-admin", "stoneage-game-mcp"} {
		cmd := exec.CommandContext(ctx, "go", "build", "-mod=readonly", "-o", filepath.Join(f.Root, "bin", target), "./cmd/"+target)
		cmd.Dir = f.RepoRoot
		cmd.Env = movementCrossMapLiveGoEnvironment(f.Root)
		if err := cmd.Run(); err != nil {
			t.Fatal("build isolated admin binaries")
		}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("reserve admin port")
	}
	address := listener.Addr().String()
	_ = listener.Close()
	child := startAdminProcess(t, adminProcessLiveCommand(f, realGameFactoryCodex, address, queueRoot))
	readyCtx, stopReady := context.WithTimeout(ctx, 20*time.Second)
	defer stopReady()
	probe := &http.Client{Timeout: time.Second}
	waitLifeCodexLive(t, readyCtx, func() bool {
		select {
		case err := <-child.done:
			child.done <- err
			t.Fatal("admin exited before readiness")
		default:
		}
		response, err := probe.Get("http://" + address + "/login")
		if err != nil {
			return false
		}
		_ = response.Body.Close()
		return response.StatusCode < 500
	})
	client, csrf := adminLiveHTTPLogin(t, "http://"+address, "initialqa", password)
	// Creation can wait for the native save receipt; keep a separate, bounded
	// timeout for this operation rather than the login helper's short timeout.
	client.Timeout = 45 * time.Second
	cases := []struct {
		name    string
		initial *aiinitial.Request
	}{
		{name: "birth"}, // Omitted initial_state exercises default riding in the adapter.
		{name: "custom", initial: &aiinitial.Request{Mode: "custom", CharacterLevel: 35, Hometown: 2, Weights: characterbuild.Weights{Vital: 1, Strength: 3, Toughness: 1, Dexterity: 2}, Pets: []aiinitial.Pet{{TemplateID: mount.TemplateID, Level: 12}}}},
		{name: "random", initial: &aiinitial.Request{Mode: "random", Random: &aiinitial.RandomRules{CharacterLevel: aiinitial.Range{Min: 20, Max: 25}, PetLevel: aiinitial.Range{Min: 5, Max: 8}, PetCount: aiinitial.Range{Min: 1, Max: 2}, PetTemplates: []int{mount.TemplateID}, Hometowns: []int{0, 1, 2, 3}}}},
	}
	starts := []aigame.Point{{Floor: 1006, X: 15, Y: 22}, {Floor: 2006, X: 20, Y: 16}, {Floor: 3006, X: 21, Y: 16}, {Floor: 4006, X: 14, Y: 20}}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			payload := map[string]any{"character_name": "AIBot" + movementCrossMapLiveHex(t, 12), "character_slot": 0, "goal": map[string]any{"kind": "life", "description": "initial-state QA"}, "skill_names": []string{"stoneage-play"}, "daily_token_budget": 100000, "status": "stopped"}
			if test.initial != nil {
				payload["initial_state"] = test.initial
			}
			body, err := json.Marshal(payload)
			if err != nil {
				t.Fatal("encode initial state")
			}
			request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+address+"/api/ai/profiles/provision", bytes.NewReader(body))
			if err != nil {
				t.Fatal("construct creation request")
			}
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("X-CSRF-Token", csrf)
			response, err := client.Do(request)
			if err != nil {
				t.Fatal("admin creation request failed")
			}
			data, readErr := io.ReadAll(io.LimitReader(response.Body, 2<<20))
			_ = response.Body.Close()
			if readErr != nil || response.StatusCode != http.StatusCreated {
				t.Fatalf("admin creation did not confirm: HTTP %d", response.StatusCode)
			}
			var result struct {
				Profile struct {
					ID string `json:"id"`
				} `json:"profile"`
			}
			if json.Unmarshal(data, &result) != nil || result.Profile.ID == "" {
				t.Fatal("admin creation omitted profile identity")
			}
			profile, err := f.Profiles.GetProfile(ctx, result.Profile.ID)
			if err != nil || profile.Status != airuntime.ProfileStatusStopped || !profile.UnlimitedFunds {
				t.Fatal("created profile defaults are invalid")
			}
			record, err := f.Provisioner.InitialState(ctx, profile.ID)
			if err != nil || record == nil || record.Status != "published" || record.Actual == nil || !record.Resolved.Mount {
				t.Fatal("initial state not durably published with default mount")
			}
			plan := record.Resolved
			if err := playermanager.VerifyInitial(*record.Actual, plan); err != nil {
				t.Fatal("published initial snapshot does not match resolved plan")
			}
			switch test.name {
			case "birth":
				if plan.CharacterLevel != 1 || plan.Hometown != 0 || len(plan.Pets) != 1 || plan.Pets[0] != mount {
					t.Fatal("birth mount defaults differ from native catalog")
				}
			case "custom":
				if plan.CharacterLevel != 35 || plan.Hometown != 2 || plan.Weights != test.initial.Weights || !reflect.DeepEqual(plan.Pets, test.initial.Pets) {
					t.Fatal("custom state was altered")
				}
			case "random":
				if plan.CharacterLevel < 20 || plan.CharacterLevel > 25 || len(plan.Pets) < 1 || len(plan.Pets) > 2 || plan.Hometown < 0 || plan.Hometown > 3 {
					t.Fatal("random state outside configured bounds")
				}
				for _, pet := range plan.Pets {
					if pet.TemplateID != mount.TemplateID || pet.Level < 5 || pet.Level > 8 {
						t.Fatal("random pet outside configured bounds")
					}
				}
			}
			// Re-read the offline native archive independently of the creation response.
			archive, err := manager.Archives.Read(ctx, profile.Account.Username, 0)
			if err != nil {
				t.Fatal("read persisted native archive")
			}
			document, err := playerdata.ParseSave(archive)
			if err != nil {
				t.Fatal("parse persisted native archive")
			}
			saved, err := document.Snapshot()
			if err != nil || saved.PersistentCharacterID == "" {
				t.Fatal("persisted native identity missing")
			}
			if err := playermanager.VerifyInitial(saved, plan); err != nil {
				t.Fatalf("persisted state does not match initialization: %v", err)
			}

			lease, err := movementCrossMapLiveOpenWithRetry(ctx, f.Provider, profile)
			if err != nil {
				t.Fatal("relogin created profile")
			}
			defer lease.Close()
			loginCtx, cancelLogin := context.WithTimeout(ctx, 20*time.Second)
			defer cancelLogin()
			_, err = movementCrossMapLiveWaitSnapshot(loginCtx, lease.Session, func(s aigame.Snapshot) bool {
				return s.Phase == aigame.PhaseWorld && s.Player.HasStatus && int(s.Player.Level) == plan.CharacterLevel && s.Position.Floor == starts[plan.Hometown].Floor && s.Position.X == starts[plan.Hometown].X && s.Position.Y == starts[plan.Hometown].Y
			})
			if err != nil {
				t.Fatal("relogin did not retain character level and hometown")
			}
			actual, err := manager.Get(ctx, profile.Account.Username, 0)
			if err != nil || !actual.Online {
				t.Fatal("read authoritative online initialized character")
			}
			if err := playermanager.VerifyInitial(actual, plan); err != nil {
				t.Fatal("relogin did not retain stats, pet levels or riding")
			}
			again, err := f.Provisioner.InitialState(ctx, profile.ID)
			if err != nil || again == nil || !reflect.DeepEqual(again.Resolved, plan) {
				t.Fatal("initial state was rerolled on login")
			}
			t.Logf("%s creation confirmed: level=%d hometown=%d pets=%d mounted=%t; saved archive and relogin match", test.name, plan.CharacterLevel, plan.Hometown, len(plan.Pets), plan.Mount)
		})
	}
	child.stop(t)
}
