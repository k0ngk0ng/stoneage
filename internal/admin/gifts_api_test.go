package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/auth"
	"github.com/k0ngk0ng/stoneage/internal/gamecatalog"
	"github.com/k0ngk0ng/stoneage/internal/playerbridge"
	"github.com/k0ngk0ng/stoneage/internal/playerdata"
)

type giftAPITestManager struct {
	mu          sync.Mutex
	characters  map[string][]playerdata.Character
	snapshots   map[string]playerdata.Snapshot
	applyErr    error
	applyCalls  []giftApplyCall
	applySignal chan struct{}
}

type giftApplyCall struct {
	Account  string
	Slot     int
	Mutation playerdata.Mutation
}

func (manager *giftAPITestManager) snapshotKey(account string, slot int) string {
	return account + ":" + strconv.Itoa(slot)
}

func (manager *giftAPITestManager) List(_ context.Context, account string) ([]playerdata.Character, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	characters := append([]playerdata.Character(nil), manager.characters[account]...)
	return characters, nil
}

func (manager *giftAPITestManager) Get(_ context.Context, account string, slot int) (playerdata.Snapshot, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	snapshot, ok := manager.snapshots[manager.snapshotKey(account, slot)]
	if !ok {
		return playerdata.Snapshot{}, playerdata.ErrNotFound
	}
	snapshot.Possessions = append([]playerdata.Possession(nil), snapshot.Possessions...)
	return snapshot, nil
}

func (manager *giftAPITestManager) Apply(_ context.Context, account string, slot int, mutation playerdata.Mutation) (playerdata.Snapshot, error) {
	manager.mu.Lock()
	manager.applyCalls = append(manager.applyCalls, giftApplyCall{Account: account, Slot: slot, Mutation: mutation})
	err := manager.applyErr
	snapshot := manager.snapshots[manager.snapshotKey(account, slot)]
	signal := manager.applySignal
	manager.mu.Unlock()
	if signal != nil {
		select {
		case signal <- struct{}{}:
		default:
		}
	}
	if err != nil {
		return playerdata.Snapshot{}, err
	}
	return snapshot, nil
}

func newGiftAPITestServer(t *testing.T, manager *giftAPITestManager, catalog *gamecatalog.Catalog) (*Server, *auth.Store, map[string]string) {
	t.Helper()
	store, err := auth.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(context.Background()); err != nil {
		store.Close()
		t.Fatal(err)
	}
	admins := map[string]string{}
	for _, username := range []string{"admin", "other"} {
		administrator, createErr := store.CreateAdmin(context.Background(), username, []byte("secret123"))
		if createErr != nil {
			store.Close()
			t.Fatal(createErr)
		}
		token, _, sessionErr := store.CreateSession(context.Background(), administrator.ID, time.Hour)
		if sessionErr != nil {
			store.Close()
			t.Fatal(sessionErr)
		}
		admins[username] = token
	}
	server, err := NewServer(store, Options{Players: manager, PlayerCatalog: catalog, CSRFSecret: []byte("gift-test-csrf")})
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		store.Close()
	})
	return server, store, admins
}

func giftCatalog() *gamecatalog.Catalog {
	return &gamecatalog.Catalog{
		Items: []gamecatalog.Item{
			{Entry: gamecatalog.Entry{Kind: gamecatalog.KindItem, ID: 11, TemplateID: 11, Name: "石斧"}},
			{Entry: gamecatalog.Entry{Kind: gamecatalog.KindItem, ID: 12, TemplateID: 12, Name: "木盾"}},
		},
		Pets: []gamecatalog.Pet{
			{Entry: gamecatalog.Entry{Kind: gamecatalog.KindPet, ID: 21, TemplateID: 21, Name: "小狗"}},
		},
	}
}

func giftAccountAndSnapshot(t *testing.T, store *auth.Store, manager *giftAPITestManager, username string, slots ...int) auth.Account {
	t.Helper()
	account, err := store.CreateAccount(context.Background(), username, []byte("localpass"))
	if err != nil {
		t.Fatal(err)
	}
	for _, slot := range slots {
		manager.characters[username] = append(manager.characters[username], playerdata.Character{Slot: slot, Name: fmt.Sprintf("%s-%d", username, slot)})
		manager.snapshots[manager.snapshotKey(username, slot)] = playerdata.Snapshot{
			Name:        fmt.Sprintf("%s-%d", username, slot),
			Revision:    strings.Repeat("a", 64),
			Capacities:  map[string]int{"item_inventory": 15, "pet_inventory": 5},
			Possessions: []playerdata.Possession{},
		}
	}
	return account
}

func giftJSONRequest(server *Server, token, method, path string, payload any) *httptest.ResponseRecorder {
	var body strings.Reader
	if payload == nil {
		body = *strings.NewReader("")
	} else {
		encoded, _ := json.Marshal(payload)
		body = *strings.NewReader(string(encoded))
	}
	request := httptest.NewRequest(method, path, &body)
	request.AddCookie(&http.Cookie{Name: "stoneage_admin_session", Value: token})
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set("X-CSRF-Token", server.csrfToken(token))
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	return response
}

func giftResponseJSON(t *testing.T, response *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil {
		t.Fatalf("response status=%d body=%s: %v", response.Code, response.Body, err)
	}
	return value
}

func createGiftPackageForAPI(t *testing.T, server *Server, token string, definition giftDefinition) int64 {
	t.Helper()
	response := giftJSONRequest(server, token, http.MethodPost, "/api/gift-packages", giftPackageRequest{Name: "测试礼包", Definition: definition})
	if response.Code != http.StatusCreated {
		t.Fatalf("create package = %d %s", response.Code, response.Body)
	}
	value := giftResponseJSON(t, response)["package"].(map[string]any)
	return int64(value["id"].(float64))
}

func giftPreviewForAPI(t *testing.T, server *Server, token string, packageID int64, scope string, accountID *int64, slot *int) map[string]any {
	t.Helper()
	payload := map[string]any{"package_id": packageID, "scope": scope}
	if accountID != nil {
		payload["account_id"] = *accountID
	}
	if slot != nil {
		payload["character_slot"] = *slot
	}
	response := giftJSONRequest(server, token, http.MethodPost, "/api/gift-preview", payload)
	if response.Code != http.StatusOK {
		t.Fatalf("preview = %d %s", response.Code, response.Body)
	}
	return giftResponseJSON(t, response)
}

func giftRunID(t *testing.T, response *httptest.ResponseRecorder) int64 {
	t.Helper()
	value := giftResponseJSON(t, response)
	run := value["run"].(map[string]any)
	return int64(run["id"].(float64))
}

func waitGiftDelivery(t *testing.T, store *auth.Store, runID int64, want string) auth.GiftDelivery {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		details, err := store.GetGiftRunDetails(context.Background(), runID, "", 20)
		if err == nil && len(details.Deliveries) == 1 && details.Deliveries[0].Status == want {
			return details.Deliveries[0]
		}
		time.Sleep(5 * time.Millisecond)
	}
	details, err := store.GetGiftRunDetails(context.Background(), runID, "", 20)
	t.Fatalf("run %d delivery did not become %q: details=%+v err=%v", runID, want, details, err)
	return auth.GiftDelivery{}
}

func TestGiftPackageAPIValidatesCatalogAndMergesIDs(t *testing.T) {
	manager := &giftAPITestManager{characters: map[string][]playerdata.Character{}, snapshots: map[string]playerdata.Snapshot{}}
	server, store, admins := newGiftAPITestServer(t, manager, giftCatalog())
	defer store.Close()
	response := giftJSONRequest(server, admins["admin"], http.MethodPost, "/api/gift-packages", map[string]any{
		"name": "  新手包 ",
		"definition": map[string]any{
			"items": []map[string]any{{"id": 11, "quantity": 1}, {"id": 11, "quantity": 2}},
			"pets":  []map[string]any{{"template_id": 21, "quantity": 1}},
		},
	})
	if response.Code != http.StatusCreated {
		t.Fatalf("create package = %d %s", response.Code, response.Body)
	}
	value := giftResponseJSON(t, response)["package"].(map[string]any)
	definition := value["definition"].(map[string]any)
	items := definition["items"].([]any)
	if len(items) != 1 || int(items[0].(map[string]any)["quantity"].(float64)) != 3 {
		t.Fatalf("merged definition = %#v", definition)
	}
	if _, ok := items[0].(map[string]any)["template_id"]; ok {
		t.Fatal("stored package unexpectedly used template_id")
	}
	response = giftJSONRequest(server, admins["admin"], http.MethodPost, "/api/gift-packages", map[string]any{
		"name":       "坏礼包",
		"definition": map[string]any{"items": []map[string]any{{"id": 999, "quantity": 1}}},
	})
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unknown catalog package status = %d %s", response.Code, response.Body)
	}
	response = giftJSONRequest(server, admins["admin"], http.MethodPost, "/api/gift-packages", map[string]any{
		"name":       "无效礼包",
		"definition": map[string]any{"items": []map[string]any{{"id": 11, "quantity": 16}}},
	})
	if response.Code != http.StatusBadRequest {
		t.Fatalf("over-capacity package status = %d %s", response.Code, response.Body)
	}
	response = giftJSONRequest(server, admins["admin"], http.MethodGet, "/api/gift-packages", nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "新手包") {
		t.Fatalf("list packages = %d %s", response.Code, response.Body)
	}
}

func TestGiftPreviewTokenOwnerExpiryAndIdempotentConfirm(t *testing.T) {
	manager := &giftAPITestManager{characters: map[string][]playerdata.Character{}, snapshots: map[string]playerdata.Snapshot{}}
	server, store, admins := newGiftAPITestServer(t, manager, giftCatalog())
	defer store.Close()
	account := giftAccountAndSnapshot(t, store, manager, "token-target", 0)
	packageID := createGiftPackageForAPI(t, server, admins["admin"], giftDefinition{Items: []giftDefinitionEntry{{ID: 11, Quantity: 1}}, Pets: []giftDefinitionEntry{}})
	slot := 0
	preview := giftPreviewForAPI(t, server, admins["admin"], packageID, auth.GiftTargetSingle, &account.ID, &slot)
	token := preview["preview_token"].(string)
	confirmPayload := map[string]any{"package_id": packageID, "scope": auth.GiftTargetSingle, "account_id": account.ID, "character_slot": slot, "preview_token": token}
	response := giftJSONRequest(server, admins["other"], http.MethodPost, "/api/gift-runs", confirmPayload)
	if response.Code != http.StatusForbidden {
		t.Fatalf("cross-admin token status = %d %s", response.Code, response.Body)
	}
	first := giftJSONRequest(server, admins["admin"], http.MethodPost, "/api/gift-runs", confirmPayload)
	if first.Code != http.StatusCreated {
		t.Fatalf("first confirmation = %d %s", first.Code, first.Body)
	}
	firstID := giftRunID(t, first)
	second := giftJSONRequest(server, admins["admin"], http.MethodPost, "/api/gift-runs", confirmPayload)
	if second.Code != http.StatusOK || giftRunID(t, second) != firstID {
		t.Fatalf("repeated confirmation = %d %s", second.Code, second.Body)
	}
	waitGiftDelivery(t, store, firstID, auth.GiftDeliveryApplied)
	manager.mu.Lock()
	callCount := len(manager.applyCalls)
	manager.mu.Unlock()
	if callCount != 1 {
		t.Fatalf("Apply calls = %d, want 1", callCount)
	}

	clock := time.Now()
	server.giftNowFunc = func() time.Time { return clock }
	preview = giftPreviewForAPI(t, server, admins["admin"], packageID, auth.GiftTargetSingle, &account.ID, &slot)
	clock = clock.Add(giftPreviewLifetime + time.Second)
	expired := giftJSONRequest(server, admins["admin"], http.MethodPost, "/api/gift-runs", map[string]any{
		"package_id": packageID, "scope": auth.GiftTargetSingle, "account_id": account.ID, "character_slot": slot,
		"preview_token": preview["preview_token"],
	})
	if expired.Code != http.StatusGone {
		t.Fatalf("expired token status = %d %s", expired.Code, expired.Body)
	}
}

func TestGiftPreviewSkipsCapacityAndBulkIncludesSecondRole(t *testing.T) {
	manager := &giftAPITestManager{characters: map[string][]playerdata.Character{}, snapshots: map[string]playerdata.Snapshot{}}
	server, store, admins := newGiftAPITestServer(t, manager, giftCatalog())
	defer store.Close()
	full := giftAccountAndSnapshot(t, store, manager, "full-target", 0)
	fullSnapshot := manager.snapshots[manager.snapshotKey(full.Username, 0)]
	fullSnapshot.Possessions = make([]playerdata.Possession, 14)
	for index := range fullSnapshot.Possessions {
		fullSnapshot.Possessions[index] = playerdata.Possession{Kind: "item", Location: "inventory", Slot: 5 + index}
	}
	manager.snapshots[manager.snapshotKey(full.Username, 0)] = fullSnapshot
	packageID := createGiftPackageForAPI(t, server, admins["admin"], giftDefinition{Items: []giftDefinitionEntry{{ID: 11, Quantity: 2}}})
	slot := 0
	preview := giftPreviewForAPI(t, server, admins["admin"], packageID, auth.GiftTargetSingle, &full.ID, &slot)
	if int(preview["targets"].(float64)) != 1 || int(preview["eligible"].(float64)) != 0 || len(preview["skipped"].([]any)) != 1 {
		t.Fatalf("capacity preview = %#v", preview)
	}
	if _, ok := preview["preview_token"]; ok {
		t.Fatal("capacity-only preview unexpectedly returned confirmation token")
	}

	bulk := giftAccountAndSnapshot(t, store, manager, "bulk-target", 0, 1)
	preview = giftPreviewForAPI(t, server, admins["admin"], packageID, auth.GiftTargetAll, nil, nil)
	if int(preview["targets"].(float64)) != 3 || int(preview["eligible"].(float64)) != 2 {
		t.Fatalf("bulk preview = %#v", preview)
	}
	confirm := giftJSONRequest(server, admins["admin"], http.MethodPost, "/api/gift-runs", map[string]any{
		"package_id": packageID, "scope": auth.GiftTargetAll, "preview_token": preview["preview_token"],
	})
	if confirm.Code != http.StatusCreated {
		t.Fatalf("bulk confirmation = %d %s", confirm.Code, confirm.Body)
	}
	runID := giftRunID(t, confirm)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		details, err := store.GetGiftRunDetails(context.Background(), runID, "", 20)
		if err == nil && len(details.Deliveries) == 3 {
			applied := 0
			for _, delivery := range details.Deliveries {
				if delivery.Status == auth.GiftDeliveryApplied {
					applied++
				}
			}
			if applied == 2 {
				manager.mu.Lock()
				defer manager.mu.Unlock()
				seenSlots := map[int]bool{}
				for _, call := range manager.applyCalls {
					if call.Account == bulk.Username {
						seenSlots[call.Slot] = true
					}
				}
				if !seenSlots[0] || !seenSlots[1] {
					t.Fatalf("bulk calls = %+v", manager.applyCalls)
				}
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("bulk run did not process both role slots")
}

func TestGiftWorkerCapacityRejectAfterPreviewBecomesSkip(t *testing.T) {
	// The preview observes free space, then the authoritative online bridge
	// rejects the grant after another mutation consumed it. The typed response
	// is a final capacity skip and must not be retried.
	manager := &giftAPITestManager{characters: map[string][]playerdata.Character{}, snapshots: map[string]playerdata.Snapshot{}}
	server, store, admins := newGiftAPITestServer(t, manager, giftCatalog())
	defer store.Close()
	account := giftAccountAndSnapshot(t, store, manager, "capacity-race", 0)
	packageID := createGiftPackageForAPI(t, server, admins["admin"], giftDefinition{Items: []giftDefinitionEntry{{ID: 11, Quantity: 1}}})
	slot := 0
	preview := giftPreviewForAPI(t, server, admins["admin"], packageID, auth.GiftTargetSingle, &account.ID, &slot)
	manager.mu.Lock()
	manager.applyErr = &playerbridge.Error{Code: "inventory_full", Message: "not enough empty item slots"}
	manager.mu.Unlock()
	response := giftJSONRequest(server, admins["admin"], http.MethodPost, "/api/gift-runs", map[string]any{
		"package_id": packageID, "scope": auth.GiftTargetSingle, "account_id": account.ID, "character_slot": slot,
		"preview_token": preview["preview_token"],
	})
	if response.Code != http.StatusCreated {
		t.Fatalf("capacity-race confirmation = %d %s", response.Code, response.Body)
	}
	runID := giftRunID(t, response)
	waitGiftDelivery(t, store, runID, auth.GiftDeliverySkipped)
	time.Sleep(100 * time.Millisecond)
	manager.mu.Lock()
	callCount := len(manager.applyCalls)
	manager.mu.Unlock()
	if callCount != 1 {
		t.Fatalf("capacity Apply calls = %d, want 1", callCount)
	}
}

func TestGiftWorkerBusyBattleApplyErrorFailsWithoutRetry(t *testing.T) {
	manager := &giftAPITestManager{characters: map[string][]playerdata.Character{}, snapshots: map[string]playerdata.Snapshot{}, applyErr: &playerbridge.Error{Code: "busy_battle", Message: "character is in battle"}}
	server, store, admins := newGiftAPITestServer(t, manager, giftCatalog())
	defer store.Close()
	account := giftAccountAndSnapshot(t, store, manager, "busy-battle", 0)
	packageID := createGiftPackageForAPI(t, server, admins["admin"], giftDefinition{Items: []giftDefinitionEntry{{ID: 11, Quantity: 1}}})
	slot := 0
	preview := giftPreviewForAPI(t, server, admins["admin"], packageID, auth.GiftTargetSingle, &account.ID, &slot)
	response := giftJSONRequest(server, admins["admin"], http.MethodPost, "/api/gift-runs", map[string]any{
		"package_id": packageID, "scope": auth.GiftTargetSingle, "account_id": account.ID, "character_slot": slot,
		"preview_token": preview["preview_token"],
	})
	if response.Code != http.StatusCreated {
		t.Fatalf("busy-battle confirmation = %d %s", response.Code, response.Body)
	}
	runID := giftRunID(t, response)
	waitGiftDelivery(t, store, runID, auth.GiftDeliveryFailed)
	time.Sleep(100 * time.Millisecond)
	manager.mu.Lock()
	callCount := len(manager.applyCalls)
	manager.mu.Unlock()
	if callCount != 1 {
		t.Fatalf("busy-battle Apply calls = %d, want 1", callCount)
	}
}

func TestGiftWorkerUnknownApplyErrorBecomesUncertainWithoutRetry(t *testing.T) {
	manager := &giftAPITestManager{characters: map[string][]playerdata.Character{}, snapshots: map[string]playerdata.Snapshot{}, applyErr: errors.New("bridge connection lost")}
	server, store, admins := newGiftAPITestServer(t, manager, giftCatalog())
	defer store.Close()
	account := giftAccountAndSnapshot(t, store, manager, "uncertain", 0)
	packageID := createGiftPackageForAPI(t, server, admins["admin"], giftDefinition{Items: []giftDefinitionEntry{{ID: 11, Quantity: 1}}})
	slot := 0
	preview := giftPreviewForAPI(t, server, admins["admin"], packageID, auth.GiftTargetSingle, &account.ID, &slot)
	response := giftJSONRequest(server, admins["admin"], http.MethodPost, "/api/gift-runs", map[string]any{
		"package_id": packageID, "scope": auth.GiftTargetSingle, "account_id": account.ID, "character_slot": slot,
		"preview_token": preview["preview_token"],
	})
	if response.Code != http.StatusCreated {
		t.Fatalf("uncertain confirmation = %d %s", response.Code, response.Body)
	}
	runID := giftRunID(t, response)
	waitGiftDelivery(t, store, runID, auth.GiftDeliveryUncertain)
	time.Sleep(100 * time.Millisecond)
	manager.mu.Lock()
	callCount := len(manager.applyCalls)
	manager.mu.Unlock()
	if callCount != 1 {
		t.Fatalf("unknown Apply error retried %d times", callCount)
	}
}
