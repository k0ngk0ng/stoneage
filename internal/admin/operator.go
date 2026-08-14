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
// full restart, but it cannot execute arbitrary shell commands or access Docker.
type Operator interface {
	Status(context.Context) (ServiceStatus, error)
	Restart(context.Context) error
}

// TargetedOperator is implemented by the production Unix operator. Keeping
// targeted restarts optional preserves the small Operator test seam while
// allowing older integrations to continue supporting the all-services action.
type TargetedOperator interface {
	RestartGateway(context.Context) error
	RestartGame(context.Context) error
}

// NotifyingOperator is the optional, fixed online-announcement capability.
// It is intentionally separate from Operator so test and older integrations
// can keep the status/restart surface without having to execute a command.
type NotifyingOperator interface {
	Notify(context.Context, string) error
}

type UnixOperator struct {
	Socket string
}

type operatorRequest struct {
	Action  string `json:"action"`
	Message string `json:"message,omitempty"`
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
	return operator.restartAction(ctx, "restart")
}

func (operator UnixOperator) RestartGateway(ctx context.Context) error {
	return operator.restartAction(ctx, "restart_gateway")
}

func (operator UnixOperator) RestartGame(ctx context.Context) error {
	return operator.restartAction(ctx, "restart_game")
}

func (operator UnixOperator) Notify(ctx context.Context, message string) error {
	response, err := operator.callWithMessage(ctx, "notify", message)
	if err != nil {
		return err
	}
	if !response.OK {
		return errors.New(response.Error)
	}
	return nil
}

func (operator UnixOperator) restartAction(ctx context.Context, action string) error {
	response, err := operator.call(ctx, action)
	if err != nil {
		return err
	}
	if !response.OK {
		return errors.New(response.Error)
	}
	return nil
}

func (operator UnixOperator) call(ctx context.Context, action string) (operatorResponse, error) {
	return operator.callWithMessage(ctx, action, "")
}

func (operator UnixOperator) callWithMessage(ctx context.Context, action, message string) (operatorResponse, error) {
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
	switch action {
	case "restart", "restart_server", "restart_game", "restart_gateway":
		deadline = 100 * time.Second
	case "notify":
		deadline = 10 * time.Second
	}
	_ = connection.SetDeadline(time.Now().Add(deadline))
	if err := json.NewEncoder(connection).Encode(operatorRequest{Action: action, Message: message}); err != nil {
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
