package websession

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"strings"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
)

// Only fixed classifications are logged. Network errors can contain URLs,
// and game replies can contain account or credential material.
func diagnosticCode(err error) string {
	if err == nil {
		return "none"
	}
	for _, entry := range []struct {
		err  error
		code string
	}{
		{context.DeadlineExceeded, "timeout"}, {context.Canceled, "canceled"},
		{ErrUnauthorized, "unauthorized"}, {ErrLeaseConflict, "control_conflict"},
		{ErrLeaseRevoked, "lease_revoked"}, {ErrLeaseUnavailable, "web_unavailable"},
		{ErrProtocol, "web_protocol"}, {ErrClosed, "closed"},
		{aigame.ErrUnexpectedReply, "game_reply_rejected"}, {aigame.ErrProtocol, "game_protocol"},
		{aigame.ErrNotAuthenticated, "game_not_authenticated"},
	} {
		if errors.Is(err, entry.err) {
			return entry.code
		}
	}
	var network net.Error
	if errors.As(err, &network) {
		if network.Timeout() {
			return "network_timeout"
		}
		return "network_error"
	}
	return "unclassified"
}

func traceStage(ctx context.Context, stage string) func(error) {
	started := time.Now()
	profile := ""
	log.Printf("event=ai_web_stage_started profile=%q stage=%s", profile, stage)
	return func(err error) {
		outcome := "completed"
		if err != nil {
			outcome = "failed"
		}
		log.Printf("event=ai_web_stage_%s profile=%q stage=%s duration_ms=%d error_code=%s error_type=%T", outcome, profile, stage, time.Since(started).Milliseconds(), diagnosticCode(err), err)
	}
}

func logHTTPFailure(endpoint string, status int, err error) {
	// Session IDs and lease tokens must never become log identifiers.
	if strings.HasPrefix(endpoint, "/api/sessions/") {
		parts := strings.Split(endpoint, "/")
		endpoint = "/api/sessions/:session"
		if len(parts) > 4 {
			endpoint += "/" + parts[4]
		}
	}
	log.Printf("event=ai_web_request_failed endpoint=%q status=%d error_code=%s error_type=%s", endpoint, status, diagnosticCode(err), fmt.Sprintf("%T", err))
}
