package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/sacli"
)

// Opt-in against isolated native GMSV + SAAC + gateway fixtures. Never uses
// production credentials. Both clients go through the real Web HTTP handler.
func TestNativeSactlIdleLifecycle(t *testing.T) {
	upstream := os.Getenv("STONEAGE_TEST_NATIVE_GATEWAY")
	work := os.Getenv("STONEAGE_TEST_NATIVE_WORK")
	if upstream == "" || work == "" {
		t.Skip("requires isolated native fixture")
	}
	cfg := testConfig(upstream)
	cfg.IdleTimeout = 3 * time.Minute
	handler, err := NewHandler(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { handler.Close() })
	web := httptest.NewServer(handler)
	t.Cleanup(web.Close)
	for i := 0; i < 2; i++ {
		t.Run(fmt.Sprintf("client-%d", i), func(t *testing.T) {
			t.Parallel()
			account := fmt.Sprintf("ladderqa%02d", i)
			password, err := os.ReadFile(filepath.Join(work, account+".password"))
			if err != nil {
				t.Fatal(err)
			}
			config := sacli.DefaultConfig()
			config.Transport, config.WebBaseURL = "http", web.URL
			server := sacli.NewServer(config)
			binary := os.Getenv("STONEAGE_TEST_SACTL_BIN")
			socket := filepath.Join(t.TempDir(), "session.sock")
			if binary != "" {
				process := exec.Command(binary, "serve", "--foreground", "--interactive", "--socket", socket, "--web-base-url", web.URL)
				if err := process.Start(); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { process.Process.Kill(); process.Wait() })
				deadline := time.Now().Add(5 * time.Second)
				for {
					if _, err := os.Stat(socket); err == nil {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("daemon startup timeout")
					}
					time.Sleep(20 * time.Millisecond)
				}
			}
			call := func(command string, args ...string) sacli.Response {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				if binary != "" {
					if command == "login" {
						response, err := sacli.Call(ctx, socket, sacli.Request{Command: command, Args: args, JSON: true})
						if err != nil {
							t.Fatal(err)
						}
						return response
					}
					commandArgs := append([]string{"--socket", socket, "--json", command}, args...)
					output, err := exec.CommandContext(ctx, binary, commandArgs...).Output()
					var response sacli.Response
					if parseErr := json.Unmarshal(output, &response); parseErr != nil {
						t.Fatalf("CLI %s returned invalid JSON: %v (%v)", command, parseErr, err)
					}
					return response
				}
				return server.Dispatch(ctx, sacli.Request{Command: command, Args: args, JSON: true})
			}
			t.Cleanup(func() { call("logout") })
			if r := call("login", account, "invalid-fixture-password"); r.OK {
				t.Fatal("wrong password accepted")
			}
			if r := call("login", account, strings.TrimSpace(string(password))); !r.OK {
				t.Fatal(r.Error)
			}
			if r := call("enter", "MissingFixtureCharacter"); r.OK {
				t.Fatal("invalid character accepted")
			}
			if r := call("chars"); !r.OK {
				t.Fatal("failed character entry destroyed login", r.Error)
			}
			if i == 0 {
				time.Sleep(85 * time.Second)
				if r := call("chars"); !r.OK {
					t.Fatal("idle character selection lost session", r.Error)
				}
				t.Log("character selection remained usable after 85 seconds")
			}
			if r := call("enter", fmt.Sprintf("LadderQA%02d", i)); !r.OK {
				t.Fatal(r.Error)
			}
			for tick := 0; tick < 17; tick++ {
				time.Sleep(5 * time.Second)
				// Control follows the browser's 20-second Echo cadence.
				if i == 1 && tick%4 == 3 {
					if r := call("probe", "echo"); !r.OK {
						t.Fatal(r.Error)
					}
				}
				r := call("status")
				if !r.OK || !strings.Contains(string(r.Data), `"Connected":true`) {
					t.Fatalf("lost session at %ds: %s %s", (tick+1)*5, r.Text, r.Error)
				}
			}
			t.Log("remained connected through 85 seconds idle")
			for _, command := range []string{"observe", "battle-log"} {
				if r := call(command); !r.OK {
					t.Fatalf("%s failed: %s", command, r.Error)
				}
			}
			if r := call("ladder", "status"); !r.OK {
				t.Fatal("arena observation failed", r.Error)
			}

			// In-place logout must preserve authoritative coordinates across
			// immediate authentication/entry, through the real Web EOF path.
			var before aigame.Snapshot
			if r := call("observe"); !r.OK {
				t.Fatal(r.Error)
			} else if err := json.Unmarshal(r.Data, &before); err != nil {
				t.Fatal(err)
			}
			if r := call("logout", "--in-place"); !r.OK || !strings.Contains(string(r.Data), `"confirmed":true`) {
				t.Fatalf("in-place logout: %+v", r)
			}
			if r := call("login", account, strings.TrimSpace(string(password))); !r.OK {
				t.Fatal("immediate login after in-place logout", r.Error)
			}
			if r := call("enter", fmt.Sprintf("LadderQA%02d", i)); !r.OK {
				t.Fatal(r.Error)
			}
			deadline := time.Now().Add(5 * time.Second)
			for {
				var after aigame.Snapshot
				r := call("observe")
				if err := json.Unmarshal(r.Data, &after); err != nil {
					t.Fatal(err)
				}
				if after.Position.Floor == before.Position.Floor && after.Position.X == before.Position.X && after.Position.Y == before.Position.Y {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("in-place position changed: before=%+v after=%+v", before.Position, after.Position)
				}
				time.Sleep(50 * time.Millisecond)
			}
			t.Log("in-place logout and immediate re-entry preserved position")
			if r := call("logout"); !r.OK {
				t.Fatal(r.Error)
			}
			if r := call("status"); !strings.Contains(string(r.Data), `"Connected":false`) {
				t.Fatal("logout reconnected")
			}
			if r := call("login", account, strings.TrimSpace(string(password))); !r.OK {
				t.Fatal("login after logout failed", r.Error)
			}
		})
	}
}
