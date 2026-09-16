package admin

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aiinitial"
)

func TestInitialMountFailureShowsSafeActionableMessage(t *testing.T) {
	authStore, aiStore, server, client, csrf := newAIProvisionHTTPFixture(t, &fakeAIPlayerProvisioner{err: fmt.Errorf("private-bridge-path: %w", aiinitial.ErrInvalidMount)})
	defer authStore.Close()
	defer aiStore.Close()
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/ai/profiles/provision", bytes.NewBufferString(`{"character_name":"骑宠角色","character_slot":0,"personality":{},"goal":{},"skill_names":["stoneage-play"]}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", csrf)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "首只宠物") || strings.Contains(string(body), "private-bridge-path") {
		t.Fatalf("mount failure response: %d %s", response.StatusCode, body)
	}
}
