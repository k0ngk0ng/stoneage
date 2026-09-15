package admin

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/auth"
	"github.com/k0ngk0ng/stoneage/internal/playerdata"
)

func TestGiftConcurrentConfirmationCreatesOneRun(t *testing.T) {
	manager := &giftAPITestManager{characters: map[string][]playerdata.Character{}, snapshots: map[string]playerdata.Snapshot{}}
	server, store, admins := newGiftAPITestServer(t, manager, giftCatalog())
	account := giftAccountAndSnapshot(t, store, manager, "concurrent", 0)
	id := createGiftPackageForAPI(t, server, admins["admin"], giftDefinition{Items: []giftDefinitionEntry{{ID: 11, Quantity: 1}}, Pets: []giftDefinitionEntry{{ID: 21, Quantity: 1}}})
	slot := 0
	preview := giftPreviewForAPI(t, server, admins["admin"], id, auth.GiftTargetSingle, &account.ID, &slot)
	payload := map[string]any{"package_id": id, "scope": "single", "account_id": account.ID, "character_slot": 0, "preview_token": preview["preview_token"]}
	responses := make([]*httptest.ResponseRecorder, 8)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range responses {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			responses[i] = giftJSONRequest(server, admins["admin"], http.MethodPost, "/api/gift-runs", payload)
		}(i)
	}
	close(start)
	wg.Wait()
	var runID int64
	created := 0
	for _, response := range responses {
		if response.Code != http.StatusOK && response.Code != http.StatusCreated {
			t.Fatalf("confirm: %d %s", response.Code, response.Body)
		}
		if response.Code == http.StatusCreated {
			created++
		}
		id := giftRunID(t, response)
		if runID != 0 && runID != id {
			t.Fatalf("duplicate runs: %d and %d", runID, id)
		}
		runID = id
	}
	if created != 1 {
		t.Fatalf("created responses = %d", created)
	}
	waitGiftDelivery(t, store, runID, auth.GiftDeliveryApplied)
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if len(manager.applyCalls) != 1 || len(manager.applyCalls[0].Mutation.Bundle) != 2 {
		t.Fatalf("mixed bundle calls: %+v", manager.applyCalls)
	}
}

func TestGiftChangedPackageRejectsOldPreview(t *testing.T) {
	manager := &giftAPITestManager{characters: map[string][]playerdata.Character{}, snapshots: map[string]playerdata.Snapshot{}}
	server, store, admins := newGiftAPITestServer(t, manager, giftCatalog())
	account := giftAccountAndSnapshot(t, store, manager, "changed", 0)
	id := createGiftPackageForAPI(t, server, admins["admin"], giftDefinition{Items: []giftDefinitionEntry{{ID: 11, Quantity: 1}}})
	slot := 0
	preview := giftPreviewForAPI(t, server, admins["admin"], id, auth.GiftTargetSingle, &account.ID, &slot)
	response := giftJSONRequest(server, admins["admin"], http.MethodPut, fmt.Sprintf("/api/gift-packages/%d", id), giftPackageRequest{Name: "更新后的礼包", Definition: giftDefinition{Pets: []giftDefinitionEntry{{ID: 21, Quantity: 1}}}})
	if response.Code != http.StatusOK {
		t.Fatalf("update: %d %s", response.Code, response.Body)
	}
	response = giftJSONRequest(server, admins["admin"], http.MethodPost, "/api/gift-runs", map[string]any{"package_id": id, "scope": "single", "account_id": account.ID, "character_slot": 0, "preview_token": preview["preview_token"]})
	if response.Code != http.StatusConflict {
		t.Fatalf("confirm changed package: %d %s", response.Code, response.Body)
	}
	runs, err := store.ListGiftRuns(context.Background(), "", 100)
	if err != nil || len(runs.Runs) != 0 {
		t.Fatalf("unexpected run: %+v %v", runs, err)
	}
}
