package sacli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"time"
)

// Request is one command sent from the CLI process to the daemon.
type Request struct {
	Command string        `json:"command"`
	Args    []string      `json:"args,omitempty"`
	JSON    bool          `json:"json,omitempty"`
	Timeout time.Duration `json:"timeout,omitempty"`
}

// Response is the daemon's answer. Text is the readable form for humans and
// terminal agents; Data carries the structured form when --json was asked
// for. Kind classifies failures so the CLI can choose an exit code.
type Response struct {
	OK    bool            `json:"ok"`
	Text  string          `json:"text,omitempty"`
	Data  json.RawMessage `json:"data,omitempty"`
	Error string          `json:"error,omitempty"`
	Kind  string          `json:"kind,omitempty"`
}

// Response kinds. The CLI maps these to exit codes; the model reads Text.
const (
	KindUsage   = "usage"
	KindAction  = "action"
	KindUnknown = "unknown"
	KindSession = "session"
	KindServer  = "server"
)

// Call sends one request to the daemon socket and returns its answer.
func Call(ctx context.Context, socketPath string, request Request) (Response, error) {
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return Response{}, fmt.Errorf("connect %s: %w", socketPath, err)
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return Response{}, fmt.Errorf("encode request: %w", err)
	}
	if _, err := conn.Write(append(payload, '\n')); err != nil {
		return Response{}, fmt.Errorf("send request: %w", err)
	}
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		return Response{}, fmt.Errorf("read response: %w", err)
	}
	var response Response
	if err := json.Unmarshal(line, &response); err != nil {
		return Response{}, fmt.Errorf("decode response: %w", err)
	}
	return response, nil
}

// replyJSON marshals one structured value for Response.Data.
func replyJSON(value any) json.RawMessage {
	payload, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	return payload
}

func failure(kind string, format string, args ...any) Response {
	return Response{OK: false, Kind: kind, Error: fmt.Sprintf(format, args...)}
}
