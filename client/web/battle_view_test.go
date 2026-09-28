package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
)

func TestWebBattleSelectionRequiresCurrentGenerationAndObservation(t *testing.T) {
	h, s, peer := battleSessionFixture(t)
	seedWebBattle(t, s)
	snapshot, err := s.observeAuthoritative(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	view := aigame.NewBattleView(snapshot)
	selection := aigame.BattleSelection{MatchID: view.MatchID, Turn: view.Turn, ObservationID: view.ID, CandidateID: "player:guard:-1:0"}
	post := func(generation uint64, selection aigame.BattleSelection) *httptest.ResponseRecorder {
		data, _ := json.Marshal(map[string]any{"generation": generation, "selection": selection})
		response := httptest.NewRecorder()
		h.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/sessions/"+s.id+"/battle-state", bytes.NewReader(data)))
		return response
	}
	if r := post(9, selection); r.Code == http.StatusOK {
		t.Fatal("foreign control generation accepted")
	}
	stale := selection
	stale.ObservationID = "stale"
	if r := post(1, stale); r.Code == http.StatusOK {
		t.Fatal("stale state accepted")
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- post(1, selection) }()
	if command := readWebBattleCommandOrFail(t, peer, "structured selection"); command != "G" {
		t.Fatal(command)
	}
	if r := <-done; r.Code != http.StatusOK {
		t.Fatal(r.Code, r.Body.String())
	}
	if r := post(1, selection); r.Code == http.StatusOK {
		t.Fatal("duplicate player order accepted")
	}
	if _, err := readWebBattleCommand(t, peer, 20*time.Millisecond); err == nil {
		t.Fatal("rejected action wrote a packet")
	}
}
