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
	SAAC     string `json:"saac"`
	Database string `json:"database"`
}

type DeploymentStatus struct {
	Version   string `json:"version"`
	Phase     string `json:"phase"`
	Message   string `json:"message,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
	Backup    string `json:"backup,omitempty"`
}

// Operator is deliberately narrow. The web process can request status, fixed
// service actions and the fixed asset-sync job, but it cannot execute arbitrary
// shell commands or access Docker.
type Operator interface {
	Status(context.Context) (ServiceStatus, error)
	Restart(context.Context) error
}

// TargetedOperator is implemented by the production Unix operator. Keeping
// targeted restarts optional preserves the small Operator test seam while
// allowing older integrations to continue supporting the all-services action.
type TargetedOperator interface {
	RestartGateway(context.Context) error
	RestartGMSV(context.Context) error
	RestartSAAC(context.Context) error
	RestartGame(context.Context) error
}

// StoppingOperator exposes the same fixed, service-scoped stop actions as the
// restart actions. It remains optional so older test and deployment adapters
// can continue to implement the read/restart surface only.
type StoppingOperator interface {
	Stop(context.Context) error
	StopGateway(context.Context) error
	StopGMSV(context.Context) error
	StopSAAC(context.Context) error
	StopGame(context.Context) error
}

// NotifyingOperator is the optional, fixed online-announcement capability.
// It is intentionally separate from Operator so test and older integrations
// can keep the status/restart surface without having to execute a command.
type NotifyingOperator interface {
	Notify(context.Context, string) error
}

// DeployingOperator exposes only the fixed, version-based release operation.
// The web process cannot provide a shell command or an image reference.
type DeployingOperator interface {
	DeployVersion(context.Context, string) error
	DeploymentStatus(context.Context) (DeploymentStatus, error)
}

// AssetSyncingOperator exposes one fixed, asynchronous bulk upload. It never
// accepts a path, bucket or shell command from the browser; the operator owns
// those values and invokes the checked-in sync-assets.sh script.
type AssetSyncingOperator interface {
	SyncAssets(context.Context) error
	AssetSyncStatus(context.Context) (AssetSyncStatus, error)
}

type AssetSyncStatus struct {
	Phase     string `json:"phase"`
	Message   string `json:"message,omitempty"`
	StartedAt string `json:"started_at,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

type UnixOperator struct {
	Socket string
}

type operatorRequest struct {
	Action  string `json:"action"`
	Message string `json:"message,omitempty"`
	Version string `json:"version,omitempty"`
}

type operatorResponse struct {
	OK         bool             `json:"ok"`
	Error      string           `json:"error,omitempty"`
	Status     ServiceStatus    `json:"status,omitempty"`
	Deployment DeploymentStatus `json:"deployment,omitempty"`
	AssetSync  AssetSyncStatus  `json:"asset_sync,omitempty"`
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

func (operator UnixOperator) RestartGMSV(ctx context.Context) error {
	return operator.restartAction(ctx, "restart_gmsv")
}

func (operator UnixOperator) RestartSAAC(ctx context.Context) error {
	return operator.restartAction(ctx, "restart_saac")
}

func (operator UnixOperator) Stop(ctx context.Context) error {
	return operator.stopAction(ctx, "stop")
}

func (operator UnixOperator) StopGateway(ctx context.Context) error {
	return operator.stopAction(ctx, "stop_gateway")
}

func (operator UnixOperator) StopGMSV(ctx context.Context) error {
	return operator.stopAction(ctx, "stop_gmsv")
}

func (operator UnixOperator) StopSAAC(ctx context.Context) error {
	return operator.stopAction(ctx, "stop_saac")
}

func (operator UnixOperator) StopGame(ctx context.Context) error {
	return operator.stopAction(ctx, "stop_game")
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

func (operator UnixOperator) DeployVersion(ctx context.Context, version string) error {
	response, err := operator.callVersion(ctx, "deploy_version", version)
	if err != nil {
		return err
	}
	if !response.OK {
		return errors.New(response.Error)
	}
	return nil
}

func (operator UnixOperator) DeploymentStatus(ctx context.Context) (DeploymentStatus, error) {
	response, err := operator.call(ctx, "deploy_status")
	if err != nil {
		return DeploymentStatus{}, err
	}
	if !response.OK {
		return DeploymentStatus{}, errors.New(response.Error)
	}
	return response.Deployment, nil
}

func (operator UnixOperator) SyncAssets(ctx context.Context) error {
	response, err := operator.call(ctx, "sync_assets")
	if err != nil {
		return err
	}
	if !response.OK {
		return errors.New(response.Error)
	}
	return nil
}

func (operator UnixOperator) AssetSyncStatus(ctx context.Context) (AssetSyncStatus, error) {
	response, err := operator.call(ctx, "sync_assets_status")
	if err != nil {
		return AssetSyncStatus{}, err
	}
	if !response.OK {
		return AssetSyncStatus{}, errors.New(response.Error)
	}
	return response.AssetSync, nil
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

func (operator UnixOperator) stopAction(ctx context.Context, action string) error {
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
	case "restart", "restart_server", "restart_game", "restart_gateway", "restart_gmsv", "restart_saac", "stop", "stop_server", "stop_game", "stop_gateway", "stop_gmsv", "stop_saac", "deploy_status", "sync_assets", "sync_assets_status":
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

func (operator UnixOperator) callVersion(ctx context.Context, action, version string) (operatorResponse, error) {
	if operator.Socket == "" {
		return operatorResponse{}, errors.New("operator socket is not configured")
	}
	dialer := net.Dialer{}
	connection, err := dialer.DialContext(ctx, "unix", operator.Socket)
	if err != nil {
		return operatorResponse{}, fmt.Errorf("connect operator: %w", err)
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(10 * time.Second))
	if err := json.NewEncoder(connection).Encode(operatorRequest{Action: action, Version: version}); err != nil {
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
