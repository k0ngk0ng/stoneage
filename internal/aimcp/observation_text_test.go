package aimcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Legacy dialog and item descriptions contain display line breaks. The
// remote boundary must preserve them without relaxing identifier validation.
func TestRemoteObservationDisplayLineBreaks(t *testing.T) {
	binding := Binding{AccountID: "account", CharacterID: "character", Generation: 1}
	for _, field := range []string{"active_window", "window", "memo", "chat"} {
		t.Run(field, func(t *testing.T) {
			value := "第一行\r\n第二行\t说明"
			observation := Observation{CharacterID: binding.CharacterID}
			var mu sync.Mutex
			set := func(value string) {
				mu.Lock()
				defer mu.Unlock()
				switch field {
				case "active_window":
					observation.ActiveWindow = &WindowState{Data: value}
				case "window":
					observation.Windows = []WindowState{{Data: value}}
				case "memo":
					observation.InventoryItems = []InventoryItem{{Index: 5, Memo: value}}
				case "chat":
					observation.Chat = []ChatMessage{{Text: value}}
				}
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				_ = json.NewEncoder(w).Encode(map[string]any{"result": observation})
			}))
			defer server.Close()
			remote, err := NewRemoteBackend(RemoteBackendConfig{Endpoint: server.URL + "/v1/game", Token: strings.Repeat("a", 43)})
			if err != nil {
				t.Fatal(err)
			}
			set(value)
			got, err := remote.Observe(context.Background(), binding)
			if err != nil {
				t.Fatalf("legitimate multiline display text rejected: %v", err)
			}
			wantJSON, _ := json.Marshal(observation)
			gotJSON, _ := json.Marshal(got)
			if string(wantJSON) != string(gotJSON) {
				t.Fatal("display text changed in transport")
			}
			for _, bad := range []string{"bad\x00text", "bad\x1btext", strings.Repeat("x", maxTextBytes+1)} {
				set(bad)
				if _, err := remote.Observe(context.Background(), binding); !errors.Is(err, ErrBackend) {
					t.Fatalf("invalid display data accepted: %v", err)
				}
			}
		})
	}
	for _, id := range []string{"name\nline", "name\rline"} {
		if validateObservation(Observation{Character: Entity{ID: id}}, binding) == nil {
			t.Fatal("multiline entity identifier accepted")
		}
	}
}
