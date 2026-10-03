package websession

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"unicode"
)

// AutomationClient is carried by the authenticated public game connection.
// It never accepts a session identifier, arbitrary URL or protocol action.
type AutomationClient interface {
	AutomationCall(context.Context, string, any) (json.RawMessage, error)
}

func (connection *httpConn) AutomationCall(ctx context.Context, operation string, body any) (json.RawMessage, error) {
	method, endpoint := http.MethodPost, ""
	switch operation {
	case "tasks":
		method, endpoint = http.MethodGet, "automation/tasks"
	case "status":
		method, endpoint = http.MethodGet, "control"
	case "preview", "start", "cancel":
		endpoint = "automation/" + operation
	case "pause", "resume":
		endpoint = operation
	default:
		return nil, fmt.Errorf("unsupported automation operation %q", operation)
	}
	connection.mu.Lock()
	closed := connection.closed
	connection.mu.Unlock()
	if closed {
		return nil, net.ErrClosed
	}
	request, err := connection.client.newPublicRequest(ctx, method, "/api/sessions/"+url.PathEscape(connection.sessionID)+"/"+endpoint, body)
	if err != nil {
		return nil, err
	}
	// Never retry mutations: loss of the response means the outcome is unknown.
	response, err := connection.client.web.Do(request)
	if err != nil {
		return nil, fmt.Errorf("automation request did not return a result; check quest status before retrying: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		message := strings.TrimSpace(strings.Map(func(r rune) rune {
			if unicode.IsControl(r) && r != '\n' && r != '\t' {
				return -1
			}
			return r
		}, string(data)))
		return nil, fmt.Errorf("automation HTTP %d: %s", response.StatusCode, message)
	}
	var result json.RawMessage
	if err := decodeJSON(response.Body, &result); err != nil {
		return nil, fmt.Errorf("automation response is invalid; check quest status before retrying: %w", err)
	}
	connection.rememberControl(result)
	return result, nil
}

// rememberControl also consumes event-poll control updates, so completing a
// background task does not leave the next manual command on its old token.
func (connection *httpConn) rememberControl(raw json.RawMessage) {
	var status struct {
		Control struct {
			Generation uint64 `json:"generation"`
		} `json:"control"`
	}
	if json.Unmarshal(raw, &status) == nil && status.Control.Generation != 0 {
		connection.mu.Lock()
		if status.Control.Generation > connection.generation {
			connection.generation = status.Control.Generation
		}
		connection.mu.Unlock()
	}
}
