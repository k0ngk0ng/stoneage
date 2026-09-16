package admin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

type classifiedProbeFailure string

func (e classifiedProbeFailure) Error() string              { return "private-key-and-upstream-body" }
func (e classifiedProbeFailure) ConnectionTestCode() string { return string(e) }

func TestAIModelConnectionFailureUsesOnlyReviewedCategories(t *testing.T) {
	for _, tc := range []struct {
		code   string
		status int
	}{
		{"missing_key", 400}, {"timeout", 504}, {"authentication", 502}, {"rate_limit", 502},
		{"model_unavailable", 502}, {"network", 502}, {"invalid_response", 502},
		{"runtime_unavailable", 503}, {"busy", 409}, {"failed", 502},
	} {
		status, code, message := aiModelConnectionFailure(fmt.Errorf("private wrapper: %w", classifiedProbeFailure(tc.code)))
		if status != tc.status || code != tc.code || message == "" || strings.Contains(message, "private") {
			t.Fatalf("category %s yielded status=%d code=%q message=%q", tc.code, status, code, message)
		}
	}
	for _, err := range []error{errors.New("private-key-and-upstream-body"), classifiedProbeFailure("private-key-and-upstream-body")} {
		status, code, message := aiModelConnectionFailure(err)
		if status != http.StatusBadGateway || code != "failed" || strings.Contains(message, "private") {
			t.Fatal("unreviewed diagnostic exposed")
		}
	}
	status, code, _ := aiModelConnectionFailure(fmt.Errorf("wrapped: %w", context.DeadlineExceeded))
	if status != http.StatusGatewayTimeout || code != "timeout" {
		t.Fatal("deadline was not classified")
	}
}
