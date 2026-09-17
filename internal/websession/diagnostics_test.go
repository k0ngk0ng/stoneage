package websession

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"testing"
)

func TestDiagnosticsNeverLogProviderSecrets(t *testing.T) {
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(previous)
	secret := "secret-token-password"
	err := errors.New("provider URL https://example.invalid/?key=" + secret)
	traceStage(context.Background(), "authenticate_game")(err)
	logHTTPFailure("/api/sessions/"+secret+"/control", 403, err)
	got := output.String()
	if strings.Contains(got, secret) || strings.Contains(got, "example.invalid") {
		t.Fatal("diagnostic leaked provider details")
	}
	for _, want := range []string{"stage=authenticate_game", "duration_ms=", "error_code=unclassified", "status=403", "/api/sessions/:session/control"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing diagnostic %q", want)
		}
	}
	if diagnosticCode(context.DeadlineExceeded) != "timeout" {
		t.Fatal("missing timeout classification")
	}
}
