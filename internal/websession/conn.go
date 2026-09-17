package websession

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"
)

// httpConn presents one Web /api/sessions resource as an ordered net.Conn so
// aigame can reuse the exact login and character-selection implementation.
// It is only used before attach for writes; post-attach gameplay writes are
// sent through Session.ExecuteExpected's typed private RPC.
type httpConn struct {
	client    *Client
	sessionID string
	serverID  string

	mu       sync.Mutex
	closed   bool
	preserve bool
	err      error
	done     chan struct{}
	ctx      context.Context
	cancel   context.CancelFunc

	readMu         sync.Mutex
	writeMu        sync.Mutex
	buffer         bytes.Buffer
	closedByServer bool
	readDeadline   time.Time
	writeDeadline  time.Time
}

func newHTTPConn(client *Client, sessionID, serverID string, greeting []byte) *httpConn {
	ctx, cancel := context.WithCancel(context.Background())
	connection := &httpConn{client: client, sessionID: sessionID, serverID: serverID, done: make(chan struct{}), ctx: ctx, cancel: cancel}
	connection.buffer.Write(greeting)
	return connection
}

func (connection *httpConn) Read(destination []byte) (int, error) {
	if len(destination) == 0 {
		return 0, nil
	}
	connection.readMu.Lock()
	defer connection.readMu.Unlock()
	for {
		connection.mu.Lock()
		if connection.buffer.Len() > 0 {
			count, _ := connection.buffer.Read(destination)
			connection.mu.Unlock()
			return count, nil
		}
		if connection.closed {
			err := connection.err
			if err == nil {
				err = net.ErrClosed
			}
			connection.mu.Unlock()
			return 0, err
		}
		deadline := connection.readDeadline
		connection.mu.Unlock()

		response, err := connection.poll(deadline)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				connection.mu.Lock()
				closed := connection.closed
				terminal := connection.err
				connection.mu.Unlock()
				if closed {
					if terminal == nil {
						terminal = net.ErrClosed
					}
					return 0, terminal
				}
			}
			return 0, err
		}
		connection.mu.Lock()
		if len(response) > 0 {
			for _, packet := range response {
				connection.buffer.Write(packet)
			}
		}
		if len(response) == 0 && connection.closedByServer {
			connection.closed = true
			connection.err = io.EOF
		}
		connection.mu.Unlock()
	}
}

func (connection *httpConn) Write(source []byte) (int, error) {
	if len(source) == 0 {
		return 0, nil
	}
	connection.writeMu.Lock()
	defer connection.writeMu.Unlock()
	connection.mu.Lock()
	if connection.closed {
		err := connection.err
		if err == nil {
			err = net.ErrClosed
		}
		connection.mu.Unlock()
		return 0, err
	}
	deadline := connection.writeDeadline
	connection.mu.Unlock()

	ctx, cancel := connection.requestContext(deadline)
	defer cancel()
	request, err := connection.client.newPublicRequest(ctx, http.MethodPost, "/api/sessions/"+url.PathEscape(connection.sessionID)+"/send", nil)
	if err != nil {
		return 0, err
	}
	request.Body = io.NopCloser(bytes.NewReader(source))
	request.ContentLength = int64(len(source))
	request.Header.Set("Content-Type", "application/octet-stream")
	response, err := connection.client.web.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		return 0, ErrLeaseUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return 0, statusError(response.StatusCode)
	}
	return len(source), nil
}

func (connection *httpConn) Close() error {
	connection.close(false, net.ErrClosed)
	return nil
}

func (connection *httpConn) abortPreserve(err error) {
	if err == nil {
		err = ErrLeaseRevoked
	}
	connection.close(true, err)
}

func (connection *httpConn) close(preserve bool, reason error) {
	connection.mu.Lock()
	if connection.closed {
		connection.mu.Unlock()
		return
	}
	connection.closed = true
	connection.preserve = connection.preserve || preserve
	connection.err = reason
	close(connection.done)
	cancel := connection.cancel
	connection.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if connection.preserve {
		return
	}
	// DELETE is deliberately best effort. The owning Session calls Detach
	// first; this second cleanup handles failures during login or logout.
	ctx, cancelDelete := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelDelete()
	request, err := connection.client.newPublicRequest(ctx, http.MethodDelete, "/api/sessions/"+url.PathEscape(connection.sessionID), nil)
	if err != nil {
		return
	}
	response, err := connection.client.web.Do(request)
	if err == nil {
		_ = response.Body.Close()
	}
}

func (connection *httpConn) LocalAddr() net.Addr  { return webAddr("web-http") }
func (connection *httpConn) RemoteAddr() net.Addr { return webAddr(connection.serverID) }

func (connection *httpConn) SetDeadline(deadline time.Time) error {
	connection.mu.Lock()
	connection.readDeadline, connection.writeDeadline = deadline, deadline
	connection.mu.Unlock()
	return nil
}

func (connection *httpConn) SetReadDeadline(deadline time.Time) error {
	connection.mu.Lock()
	connection.readDeadline = deadline
	connection.mu.Unlock()
	return nil
}

func (connection *httpConn) SetWriteDeadline(deadline time.Time) error {
	connection.mu.Lock()
	connection.writeDeadline = deadline
	connection.mu.Unlock()
	return nil
}

func (connection *httpConn) requestContext(deadline time.Time) (context.Context, context.CancelFunc) {
	connection.mu.Lock()
	base := connection.ctx
	if base == nil {
		base = context.Background()
	}
	connection.mu.Unlock()
	if !deadline.IsZero() {
		return context.WithDeadline(base, deadline)
	}
	return context.WithCancel(base)
}

func (connection *httpConn) poll(deadline time.Time) ([][]byte, error) {
	ctx, cancel := connection.requestContext(deadline)
	defer cancel()
	request, err := connection.client.newPublicRequest(ctx, http.MethodGet, "/api/sessions/"+url.PathEscape(connection.sessionID)+"/events", nil)
	if err != nil {
		return nil, err
	}
	query := request.URL.Query()
	timeout := connection.client.pollTimeout
	if !deadline.IsZero() {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, context.DeadlineExceeded
		}
		if remaining < timeout {
			timeout = remaining
		}
	}
	if timeout <= 0 {
		timeout = defaultPollTimeout
	}
	query.Set("timeout", strconv.FormatInt(timeout.Milliseconds(), 10))
	request.URL.RawQuery = query.Encode()
	response, err := connection.client.web.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrLeaseUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, statusError(response.StatusCode)
	}
	var payload eventsResponse
	if err := decodeJSON(response.Body, &payload); err != nil {
		return nil, err
	}
	packets := make([][]byte, 0, len(payload.Events))
	for _, event := range payload.Events {
		if event.Error != "" {
			return nil, fmt.Errorf("%w: Web event stream reported an error", ErrProtocol)
		}
		if event.Packet == "" {
			if event.Closed {
				connection.mu.Lock()
				connection.closedByServer = true
				connection.mu.Unlock()
			}
			continue
		}
		packet, err := base64.StdEncoding.DecodeString(event.Packet)
		if err != nil || len(packet) == 0 {
			return nil, fmt.Errorf("%w: invalid Web event packet", ErrProtocol)
		}
		packets = append(packets, packet)
	}
	if payload.Closed {
		connection.mu.Lock()
		connection.closedByServer = true
		connection.mu.Unlock()
	}
	return packets, nil
}

type webAddr string

func (address webAddr) Network() string { return "web-session" }
func (address webAddr) String() string  { return string(address) }
