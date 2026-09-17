package aiservice

import (
	"context"
	"errors"
	"time"
)

type factoryOpenFailure struct {
	profile, stage, code string
	duration             time.Duration
	cause                error
}

func (e *factoryOpenFailure) Error() string { return "aiservice: agent startup failed" }
func (e *factoryOpenFailure) Unwrap() error { return e.cause }
func (e *factoryOpenFailure) StartFailureDetails() (string, string, string, time.Duration) {
	return e.profile, e.stage, e.code, e.duration
}
func factoryErrorCode(err error) string {
	if err == nil {
		return "none"
	}
	for _, v := range []struct {
		err  error
		code string
	}{
		{context.Canceled, "canceled"}, {context.DeadlineExceeded, "timeout"},
		{ErrFactoryConfig, "factory_invalid_config"}, {ErrFactoryBusy, "already_open"},
		{ErrFactorySession, "game_session_unavailable"}, {ErrFactoryModel, "model_config_unavailable"},
		{ErrFactoryCredentials, "model_credentials_unavailable"}, {ErrFactoryProvision, "runtime_provision_failed"},
		{ErrFactoryRuntime, "codex_unavailable"}, {ErrContainerRunnerConfig, "runner_config_invalid"},
		{ErrContainerRunnerCredential, "runner_credentials_unavailable"},
	} {
		if errors.Is(err, v.err) {
			return v.code
		}
	}
	return "factory_open_failed"
}
