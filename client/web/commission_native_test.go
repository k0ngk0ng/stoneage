package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aicontrol"
	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
	"github.com/k0ngk0ng/stoneage/internal/auth"
	"github.com/k0ngk0ng/stoneage/internal/playerbridge"
	"github.com/k0ngk0ng/stoneage/internal/playerdata"
	"github.com/k0ngk0ng/stoneage/internal/playermanager"
	"github.com/k0ngk0ng/stoneage/internal/sacli"
)

// Opt-in only: uses the isolated native QA service and retains every fixture.
// Executes actual sactl commands over HTTP and the configured shared runtime;
// no plan, NPC, item or pet contract is constructed in the test.
func TestNativeCommissionViaSactlHTTP(t *testing.T) {
	base := os.Getenv("STONEAGE_TEST_COMMISSION_WORK")
	if base == "" {
		t.Skip("requires isolated commission native QA fixture")
	}
	taskID := os.Getenv("STONEAGE_TEST_COMMISSION_TASK")
	if taskID == "" {
		taskID = "marinas-pet-commission-a"
	}
	cases := map[string]struct{ home, voucher, budget, level int }{
		"marinas-pet-commission-a":  {1, 20031, 146, 1},
		"samgil-pet-commission-a":   {0, 20001, 136, 2},
		"jaja-pet-commission-a":     {2, 20061, 136, 5},
		"karutana-pet-commission-a": {3, 20091, 136, 2},
	}
	variant, ok := cases[taskID]
	if !ok {
		t.Fatal("unknown native commission fixture")
	}
	t.Log("task:", taskID)

	if !filepath.IsAbs(base) {
		t.Fatal("fixture path must be absolute")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(root, base)
	if err != nil || strings.HasPrefix(rel, "..") {
		t.Fatal("fixture must be in the repository")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	dir, err := os.MkdirTemp(base, "http-commission-")
	if err != nil {
		t.Fatal(err)
	}
	t.Log("fixture:", dir)
	db, err := auth.Open(filepath.Join(dir, "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, 10)
	if _, err = rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	account, password, name := "quest"+hex.EncodeToString(nonce[:4]), hex.EncodeToString(nonce[4:]), "Q"+hex.EncodeToString(nonce[:4])
	if _, err = db.CreateAccount(ctx, account, []byte(password)); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	log, err := os.Create(filepath.Join(dir, "gateway.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	gateway := exec.CommandContext(ctx, filepath.Join(base, "stoneage-gateway"), "-listen", address, "-upstream", "127.0.0.1:29066", "-auth-db", filepath.Join(dir, "auth.db"), "-auth-required")
	gateway.Stdout, gateway.Stderr = log, log
	if err = gateway.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { gateway.Process.Kill(); gateway.Wait() }()
	ready := false
	for i := 0; i < 100; i++ {
		raw, _ := os.ReadFile(filepath.Join(dir, "gateway.log"))
		if strings.Contains(string(raw), "listening on ") {
			ready = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !ready {
		t.Fatal("gateway not ready")
	}
	cfg := testConfig(address)
	cfg.IdleTimeout = 3 * time.Minute
	data := filepath.Join(root, "runtime/legacy-server/gmsv/data")
	cfg.AutomationKnowledgeDataDir = data
	cfg.AutomationMapDataDir = data
	cfg.AutomationDB = filepath.Join(dir, "plans.db")
	cfg.ReceiptDB = filepath.Join(dir, "receipts.db")
	cfg.AutomationNPCRegistry = filepath.Join(root, "ai/catalogs/quests-2.5.json")
	cfg.AutomationHealingItems = filepath.Join(root, "ai/catalogs/healing-items-2.5.json")
	cfg.AutomationStockItems = filepath.Join(root, "ai/catalogs/quest-stock-items-2.5.json")
	handler, err := NewHandler(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer handler.Close()
	// Native acceptance may exercise only the selected statically-reviewed
	// draft in this isolated test. Production code has no such override. Keep
	// the checked-in execution flag false until this exact workflow succeeds.
	if os.Getenv("STONEAGE_TEST_COMMISSION_DRAFT") == "1" {
		executor := handler.automation.(*AutomationExecutor)
		found := false
		for i := range executor.config.Knowledge.TaskDefinitions {
			task := &executor.config.Knowledge.TaskDefinitions[i]
			if task.ID != taskID {
				continue
			}
			if task.Status != aiknowledge.TaskUnverified || !task.EvidenceVerified || !task.PreparationReviewed {
				t.Fatal("draft does not have verified static inputs")
			}
			task.Status, task.ExecutionVerified = aiknowledge.TaskVerified, true
			found = true
		}
		if !found {
			t.Fatal("selected draft missing")
		}
		t.Log("isolated acceptance of selected draft; source execution flag remains disabled")
	}
	web := httptest.NewServer(handler)
	defer func() { handler.Close(); web.Close() }()
	cliConfig := sacli.DefaultConfig()
	cliConfig.SocketPath = filepath.Join(dir, "sactl.sock")
	cliConfig.WebBaseURL = web.URL
	cliConfig.MapDirectory = data
	client := sacli.NewServer(cliConfig)
	call := func(command string, args ...string) sacli.Response {
		timeout := 30 * time.Second
		if command == "warp" || command == "goto" {
			timeout = 2 * time.Minute
		}
		c, stop := context.WithTimeout(ctx, timeout)
		defer stop()
		r := client.Dispatch(c, sacli.Request{Command: command, Args: args, JSON: true})
		if !r.OK {
			t.Fatalf("%s: %s", command, r.Error)
		}
		return r
	}
	call("login", account, password)
	defer func() {
		c, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		client.Dispatch(c, sacli.Request{Command: "logout"})
	}()
	call("create-character", name, "--hometown", strconv.Itoa(variant.home), "--vital", "5", "--strength", "5", "--toughness", "5", "--dexterity", "5", "--earth", "10")
	call("enter", name)
	observe := func() aigame.Snapshot {
		var s aigame.Snapshot
		r := call("observe")
		if err := json.Unmarshal(r.Data, &s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	before := observe()
	t.Logf("initial level=%d hp=%d gold=%d pets=%v", before.Player.Level, before.Player.HP, before.Player.Gold, before.AI.Pets)
	queue := func(role string) playerbridge.Queue {
		return playerbridge.Queue{Requests: filepath.Join(base, "native-runtime/queues", role, "requests"), Responses: filepath.Join(base, "native-runtime/queues", role, "responses")}
	}
	manager := &playermanager.Manager{Archives: playerbridge.SAAC{Queue: queue("saac")}, Game: playerbridge.GMSV{Queue: queue("gmsv")}}
	for i := 0; i < 10; i++ {
		st, e := manager.Get(ctx, account, 0)
		if e != nil {
			t.Fatal(e)
		}
		_, e = manager.Apply(ctx, account, 0, playerdata.Mutation{Revision: st.Revision, Action: "set_character", Field: "gld", Value: 10000})
		if errors.Is(e, playerdata.ErrConflict) && i < 9 {
			continue
		}
		if e != nil {
			t.Fatal(e)
		}
		break
	}
	t.Log("only fixture gold reduced to 10000; no level, stats, pets, items or flags injected")
	// Poll to observe the asynchronous fixture update; no write retry.
	for i := 0; i < 50; i++ {
		before = observe()
		if before.Player.Gold == 10000 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	list := call("quest", "list")
	if !strings.Contains(string(list.Data), taskID) {
		t.Fatal("formal task absent")
	}
	if variant.level > 1 {
		// A low-level character must be rejected before buying a voucher.
		raw := call("quest", "preview", taskID, "--maximum-spend", strconv.Itoa(variant.budget))
		var pre AutomationPreview
		if err := json.Unmarshal(raw.Data, &pre); err != nil || pre.Ready {
			t.Fatalf("under-level preparation accepted: %s", raw.Data)
		}
		t.Logf("under-level preview=%s", raw.Data)
		if os.Getenv("STONEAGE_TEST_COMMISSION_PREPARATION") == "field-battle" {
			if taskID != "karutana-pet-commission-a" {
				t.Fatal("no reviewed field-battle preparation for this fixture")
			}
			// Explicit fixture preparation through ordinary player commands.
			// This is not acceptance of the conservative leveling coordinator:
			// its level-1 route limitation remains recorded separately.
			call("warp", "4000")
			call("warp", "4004")
			call("goto", "16", "15")
			// The shop accepts TALK from two tiles across its counter. CLI
			// named talk still has an adjacent-only UI guard, so use its
			// ordinary facing/chat commands here and keep that gap explicit.
			call("look", "n")
			call("say", "hi")
			waitWindow := func(sequence int32) {
				for i := 0; i < 50; i++ {
					w := observe().ActiveWindow
					if w != nil && w.Open && !w.Submitted && w.Sequence == sequence {
						return
					}
					time.Sleep(100 * time.Millisecond)
				}
				t.Fatalf("fixture shop window %d did not arrive", sequence)
			}
			waitWindow(240)
			call("reply", "ok", "1")
			waitWindow(242)
			call("reply", "ok", "1|2")
			waitWindow(242)
			call("reply", "0", "0") // Return from the shop's purchase pane.
			waitWindow(240)
			call("reply", "0", "3") // Native shop menu: leave.
			t.Log("fixture bought two small meats for ordinary battle preparation")
			call("warp", "4000")
			call("goto", "104", "55")
			call("warp", "200")
			t.Logf("field preparation position=%+v", observe().Position)
			call("auto-battle", "on", "walk")
			defer func() {
				c, stop := context.WithTimeout(context.Background(), 5*time.Second)
				defer stop()
				client.Dispatch(c, sacli.Request{Command: "auto-battle", Args: []string{"off"}})
			}()
			deadline := time.NewTimer(3 * time.Minute)
			defer deadline.Stop()
			settling := false
			var worldSince time.Time
			nextReport := time.Now()
			for {
				current := observe()
				if !time.Now().Before(nextReport) {
					t.Logf("preparation phase=%s position=%+v level=%d hp=%d/%d battle=%+v window=%+v loop=%s", current.Phase, current.Position, current.Player.Level, current.Player.HP, current.Player.MaxHP, current.Battle.Active, current.ActiveWindow, call("auto-battle", "status").Text)
					nextReport = time.Now().Add(10 * time.Second)
				}
				if current.Player.HP <= 0 {
					t.Fatal("fixture died during ordinary battle preparation")
				}
				if !settling && current.Player.Level >= int32(variant.level) {
					call("auto-battle", "off")
					call("auto-battle", "on", "stay")
					settling = true
				}
				if settling && !current.Battle.Active && current.Phase == aigame.PhaseWorld {
					if worldSince.IsZero() {
						worldSince = time.Now()
					}
					if time.Since(worldSince) >= time.Second {
						call("auto-battle", "off")
						call("query", "AI")
						break
					}
				} else {
					worldSince = time.Time{}
				}
				select {
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case <-deadline.C:
					t.Fatalf("field preparation timeout: %s", call("auto-battle", "status").Text)
				case <-time.After(200 * time.Millisecond):
				}
			}
			for attempt := 0; attempt < 2; attempt++ {
				current := observe()
				if current.Player.HP >= current.Player.MaxHP {
					break
				}
				slot := int32(-1)
				for _, item := range current.Inventory {
					if item.TemplateIDKnown && item.TemplateID == 2344 {
						slot = item.Index
						break
					}
				}
				if slot < 0 {
					t.Fatal("purchased preparation supplies are unavailable")
				}
				call("item", "use", strconv.Itoa(int(slot)))
			}
			t.Log("prepared through explicit CLI field travel and ordinary auto-battle; no level/stat mutations")
		} else {
			// Prepare naturally using the existing Web leveling handler. No
			// attribute, experience or level mutations are sent to the fixture.
			handler.sessions.mu.RLock()
			var session *tcpSession
			if len(handler.sessions.sessions) == 1 {
				for _, s := range handler.sessions.sessions {
					session = s
				}
			}
			handler.sessions.mu.RUnlock()
			if session == nil {
				t.Fatal("expected exactly one isolated authenticated session")
			}
			generation := session.gate.State().Generation
			body, err := json.Marshal(automationStartHTTP{
				Generation: &generation, Mode: string(aicontrol.Leveling),
				Targets:      []AutomationTarget{{Kind: "character", Level: variant.level}},
				TargetPolicy: "all", MaximumSeconds: 900, MaximumDeaths: 0,
				Budget: AutomationBudget{MaximumSpend: 300},
			})
			if err != nil {
				t.Fatal(err)
			}
			response := httptest.NewRecorder()
			handler.startAutomation(response, httptest.NewRequest(http.MethodPost, "/automation/start", bytes.NewReader(body)).WithContext(ctx), session)
			if response.Code != http.StatusAccepted {
				t.Fatalf("natural preparation refused: %d %s", response.Code, response.Body.String())
			}
			for {
				select {
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case <-time.After(time.Second):
				}
				raw := call("quest", "status")
				var status controlResponse
				if err := json.Unmarshal(raw.Data, &status); err != nil {
					t.Fatal(err)
				}
				if status.Control.Mode == aicontrol.Paused || status.Task != nil && status.Task.State == "paused" {
					t.Fatalf("natural preparation paused: %s", raw.Data)
				}
				if !status.AutomationActive {
					if status.Task == nil || status.Task.State != "completed" {
						t.Fatalf("natural preparation stopped: %s", raw.Data)
					}
					break
				}
			}
		}
		prepared := observe()
		if prepared.Player.Level < int32(variant.level) {
			t.Fatal("natural preparation did not reach required level")
		}
		t.Logf("naturally prepared level=%d hp=%d gold=%d", prepared.Player.Level, prepared.Player.HP, prepared.Player.Gold)
	}
	preview := call("quest", "preview", taskID, "--maximum-spend", strconv.Itoa(variant.budget))
	var pre AutomationPreview
	if err = json.Unmarshal(preview.Data, &pre); err != nil {
		t.Fatal(err)
	}
	t.Logf("preview=%s", preview.Data)
	if !pre.Ready {
		t.Fatalf("formal preflight blocked: %v", pre.Problems)
	}
	started := call("quest", "start", taskID, "--maximum-spend", strconv.Itoa(variant.budget))
	t.Logf("started=%s", started.Data)
	last := ""
	for {
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(time.Second):
		}
		status := call("quest", "status")
		var result struct {
			Active bool               `json:"automation_active"`
			Task   *aimcp.TaskReceipt `json:"automation_task"`
		}
		if err := json.Unmarshal(status.Data, &result); err != nil {
			t.Fatal(err)
		}
		if status.Text != last {
			t.Log(status.Text)
			last = status.Text
		}
		if result.Task != nil && result.Task.State == "paused" {
			t.Fatalf("task paused: %s", status.Data)
		}
		if result.Active {
			continue
		}
		if result.Task == nil || result.Task.State != "completed" {
			t.Fatalf("task stopped without completion: %s", status.Data)
		}
		break
	}
	after := observe()
	t.Logf("final level=%d gold=%d pets=%v inventory=%v", after.Player.Level, after.Player.Gold, after.AI.Pets, after.AI.Items)
	if len(after.AI.Pets) != len(before.AI.Pets) {
		t.Fatal("original pet count changed")
	}
	for i, p := range before.AI.Pets {
		if after.AI.Pets[i].StableID != p.StableID {
			t.Fatal("original pet identity changed")
		}
	}
	for _, item := range after.AI.Items {
		if item.TemplateID == int32(variant.voucher) {
			t.Fatal("voucher not consumed")
		}
	}
}
