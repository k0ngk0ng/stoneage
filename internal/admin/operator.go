package admin

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"
)

type ServiceStatus struct {
	Gateway  string `json:"gateway"`
	GMSV     string `json:"gmsv"`
	Database string `json:"database"`
}

// Operator is deliberately narrow. The web process can request status and a
// restart, but it cannot execute arbitrary shell commands or access Docker.
type Operator interface {
	Status(context.Context) (ServiceStatus, error)
	Restart(context.Context) error
}

type UnixOperator struct {
	Socket string
}

type operatorRequest struct {
	Action string `json:"action"`
}

type operatorResponse struct {
	OK     bool          `json:"ok"`
	Error  string        `json:"error,omitempty"`
	Status ServiceStatus `json:"status,omitempty"`
}

func (operator UnixOperator) Status(ctx context.Context) (ServiceStatus, error) {
	response, err := operator.call(ctx, "status")
	if err != nil {
		return ServiceStatus{}, err
	}
	return response.Status, nil
}

func (operator UnixOperator) Restart(ctx context.Context) error {
	response, err := operator.call(ctx, "restart")
	if err != nil {
		return err
	}
	if !response.OK {
		return errors.New(response.Error)
	}
	return nil
}

func (operator UnixOperator) call(ctx context.Context, action string) (operatorResponse, error) {
	if operator.Socket == "" {
		return operatorResponse{}, errors.New("operator socket is not configured")
	}
	dialer := net.Dialer{}
	connection, err := dialer.DialContext(ctx, "unix", operator.Socket)
	if err != nil {
		return operatorResponse{}, fmt.Errorf("connect operator: %w", err)
	}
	defer connection.Close()
	deadline := 5 * time.Second
	if action == "restart" {
		deadline = 100 * time.Second
	}
	_ = connection.SetDeadline(time.Now().Add(deadline))
	if err := json.NewEncoder(connection).Encode(operatorRequest{Action: action}); err != nil {
		return operatorResponse{}, fmt.Errorf("send operator request: %w", err)
	}
	var response operatorResponse
	if err := json.NewDecoder(bufio.NewReader(connection)).Decode(&response); err != nil {
		return operatorResponse{}, fmt.Errorf("read operator response: %w", err)
	}
	if !response.OK && response.Error == "" {
		response.Error = "operator rejected request"
	}
	return response, nil
}
