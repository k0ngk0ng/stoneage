package aiservice

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aimcp"
)

type boundGame struct {
	aimcp.UnavailableBackend
	seen aimcp.Binding
}

func (b *boundGame) Observe(ctx context.Context, binding aimcp.Binding) (aimcp.Observation, error) {
	b.seen = binding
	return aimcp.Observation{CharacterID: binding.CharacterID, Ready: true, Connected: true}, ctx.Err()
}

func call(g *Gateway, token, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/v1/game", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	return w
}

func TestCapabilityBindsCharacterAndCannotBeReboundByBody(t *testing.T) {
	g := NewGateway()
	defer g.Close()
	backend := &boundGame{}
	binding := aimcp.Binding{AccountID: "A", CharacterID: "A:0", Generation: 7}
	token, revoke, err := g.Register(binding, backend)
	if err != nil {
		t.Fatal(err)
	}
	w := call(g, token, `{"operation":"observe","arguments":{}}`)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if backend.seen != binding {
		t.Fatal("wrong identity")
	}
	for _, body := range []string{`{"operation":"observe","arguments":{"character_id":"B:0"}}`, `{"operation":"observe","arguments":{},"binding":{"character_id":"B:0"}}`, `{"operation":"raw","arguments":{}}`} {
		if w := call(g, token, body); w.Code != 400 {
			t.Fatal(w.Code)
		}
	}
	revoke()
	if w := call(g, token, `{"operation":"observe"}`); w.Code != 401 {
		t.Fatal("revoked token accepted")
	}
}

func TestOnlyOneAgentCapabilityPerCharacter(t *testing.T) {
	g := NewGateway()
	defer g.Close()
	binding := aimcp.Binding{CharacterID: "A:0", Generation: 1}
	_, revoke, err := g.Register(binding, &boundGame{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = g.Register(binding, &boundGame{}); err == nil {
		t.Fatal("two sessions for one character")
	}
	revoke()
	if _, _, err = g.Register(binding, &boundGame{}); err != nil {
		t.Fatal(err)
	}
}

func TestGatewayErrorsDoNotExposeCapability(t *testing.T) {
	g := NewGateway()
	defer g.Close()
	token, _, err := g.Register(aimcp.Binding{CharacterID: "A:0", Generation: 1}, aimcp.UnavailableBackend{})
	if err != nil {
		t.Fatal(err)
	}
	w := call(g, token, `{"operation":"observe"}`)
	if w.Code != 503 {
		t.Fatal(w.Code)
	}
	if strings.Contains(w.Body.String(), token) {
		t.Fatal("credential leaked")
	}
	var body map[string]any
	if err = json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["error"] != "game_backend_unavailable" {
		t.Fatal(body)
	}
}
