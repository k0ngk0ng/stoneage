package admin

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/airuntime"
	"github.com/k0ngk0ng/stoneage/internal/aisupervisor"
)

type deleteCheckRuntime struct {
	status      AIExecutionStatus
	statusErr   error
	recovery    aisupervisor.UnknownRecoveryStatus
	recoveryErr error
	store       *airuntime.Store
	prepared    []string
	prepareErr  error
}

func (runtime *deleteCheckRuntime) ProfileStatus(context.Context, string) (AIExecutionStatus, error) {
	return runtime.status, runtime.statusErr
}
func (*deleteCheckRuntime) StartProfile(context.Context, string) error { return nil }
func (*deleteCheckRuntime) PauseProfile(context.Context, string) error { return nil }
func (*deleteCheckRuntime) StopProfile(context.Context, string) error  { return nil }
func (runtime *deleteCheckRuntime) UnknownRecovery(context.Context, string) (aisupervisor.UnknownRecoveryStatus, error) {
	return runtime.recovery, runtime.recoveryErr
}
func (*deleteCheckRuntime) ReviewUnknown(context.Context, aisupervisor.UnknownRecoveryRequest) error {
	return nil
}
func (runtime *deleteCheckRuntime) PrepareProfileDeletion(ctx context.Context, id string, expectedVersion int64) (int64, error) {
	runtime.prepared = append(runtime.prepared, id)
	if runtime.prepareErr != nil {
		return 0, runtime.prepareErr
	}
	if runtime.store == nil {
		return expectedVersion + 1, nil
	}
	profile, err := runtime.store.GetProfile(ctx, id)
	if err != nil {
		return 0, err
	}
	status := profile.Status
	updated, err := runtime.store.UpdateProfileCAS(ctx, id, expectedVersion, airuntime.ProfilePatch{Status: &status, Actor: "executor_change"})
	if err != nil {
		return 0, err
	}
	return updated.Version, nil
}

func createDeleteCheckProfile(t *testing.T, store *airuntime.Store, id, status string) airuntime.Profile {
	t.Helper()
	profile, err := store.CreateProfileAs(context.Background(), airuntime.Profile{
		ID:               id,
		Account:          airuntime.AccountIdentity{ID: id + "-account"},
		Character:        airuntime.CharacterIdentity{ID: id + "-character", Name: "Delete check"},
		Personality:      airuntime.Personality{Name: "test"},
		Goal:             airuntime.Goal{Kind: "leveling", TargetLevel: 1},
		DailyTokenBudget: 100,
		Status:           status,
	}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	return profile
}

func TestAIProfileDeleteRequiresStoppedProfileAndRuntime(t *testing.T) {
	tests := []struct {
		name        string
		id          string
		profile     string
		runtime     AIExecutionStatus
		runtimeErr  error
		recovery    aisupervisor.UnknownRecoveryStatus
		recoveryErr error
		wantStatus  int
		wantMessage string
	}{
		{name: "allowed", id: "delete-allowed", profile: airuntime.ProfileStatusStopped, runtime: AIExecutionStatus{State: aisupervisor.StateStopped}, recoveryErr: airuntime.ErrNotFound, wantStatus: http.StatusOK},
		{name: "active profile", id: "delete-active", profile: airuntime.ProfileStatusActive, runtime: AIExecutionStatus{State: aisupervisor.StateStopped}, recoveryErr: airuntime.ErrNotFound, wantStatus: http.StatusConflict, wantMessage: "仍在运行或尚未停止"},
		{name: "paused profile", id: "delete-paused", profile: airuntime.ProfileStatusPaused, runtime: AIExecutionStatus{State: aisupervisor.StateStopped}, recoveryErr: airuntime.ErrNotFound, wantStatus: http.StatusConflict, wantMessage: "仍在运行或尚未停止"},
		{name: "running runtime", id: "delete-running", profile: airuntime.ProfileStatusStopped, runtime: AIExecutionStatus{State: aisupervisor.StateRunning}, recoveryErr: airuntime.ErrNotFound, wantStatus: http.StatusConflict, wantMessage: "仍在运行或尚未停止"},
		{name: "runtime status error", id: "delete-status-error", profile: airuntime.ProfileStatusStopped, runtimeErr: errors.New("runtime unavailable"), recoveryErr: airuntime.ErrNotFound, wantStatus: http.StatusConflict, wantMessage: "无法确认"},
		{name: "unresolved execution", id: "delete-recovery", profile: airuntime.ProfileStatusStopped, runtime: AIExecutionStatus{State: aisupervisor.StateStopped}, recovery: aisupervisor.UnknownRecoveryStatus{ProfileID: "delete-recovery", AttemptID: "attempt-1"}, wantStatus: http.StatusConflict, wantMessage: "异常执行仍在处理中"},
		{name: "reconciled execution", id: "delete-reconciled", profile: airuntime.ProfileStatusStopped, runtime: AIExecutionStatus{State: aisupervisor.StateStopped}, recovery: aisupervisor.UnknownRecoveryStatus{ProfileID: "delete-reconciled", AttemptID: "attempt-1", Ready: true, Execution: aisupervisor.UnknownExecution{ContainerStopped: true}}, wantStatus: http.StatusOK},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtime := &deleteCheckRuntime{status: test.runtime, statusErr: test.runtimeErr, recovery: test.recovery, recoveryErr: test.recoveryErr}
			_, store, server, client, csrf := newAIProvisionHTTPFixture(t, nil, runtime)
			runtime.store = store
			profile := createDeleteCheckProfile(t, store, test.id, test.profile)
			request, err := http.NewRequest(http.MethodDelete, server.URL+"/api/ai/profiles/"+profile.ID+"?expected_version=1", nil)
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("X-CSRF-Token", csrf)
			response, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(response.Body)
			response.Body.Close()
			if response.StatusCode != test.wantStatus {
				t.Fatalf("status=%d want=%d body=%s", response.StatusCode, test.wantStatus, body)
			}
			if test.wantMessage != "" && !strings.Contains(string(body), test.wantMessage) {
				t.Fatalf("body=%s missing %q", body, test.wantMessage)
			}
			if test.wantStatus == http.StatusOK {
				if test.name == "reconciled execution" && strings.Join(runtime.prepared, ",") != profile.ID {
					t.Fatalf("prepare calls=%v, want %s", runtime.prepared, profile.ID)
				}
				if _, err := store.GetProfile(context.Background(), profile.ID); !errors.Is(err, airuntime.ErrNotFound) {
					t.Fatalf("profile after delete err=%v", err)
				}
			}
		})
	}
}
