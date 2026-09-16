package aibroker

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/airunner"
)

type recoveryDocker struct {
	mu       sync.Mutex
	state    DockerContainerState
	result   DockerResult
	readErrs []error
	remove   []error
	runs     int
	reads    int
	removes  int
}

func (docker *recoveryDocker) Run(context.Context, RunSpec, []byte) (DockerResult, error) {
	docker.mu.Lock()
	docker.runs++
	docker.mu.Unlock()
	return DockerResult{}, errors.New("recovery Docker.Run must not be called")
}

func (*recoveryDocker) Stop(context.Context, string) error { return nil }

func (docker *recoveryDocker) Inspect(context.Context, string) (DockerContainerState, error) {
	docker.mu.Lock()
	defer docker.mu.Unlock()
	return docker.state, nil
}

func (docker *recoveryDocker) ReadResult(context.Context, string) (DockerResult, error) {
	docker.mu.Lock()
	defer docker.mu.Unlock()
	docker.reads++
	if len(docker.readErrs) > 0 {
		err := docker.readErrs[0]
		docker.readErrs = docker.readErrs[1:]
		return DockerResult{}, err
	}
	return docker.result, nil
}

func (docker *recoveryDocker) Remove(context.Context, string) error {
	docker.mu.Lock()
	defer docker.mu.Unlock()
	docker.removes++
	if len(docker.remove) == 0 {
		return nil
	}
	err := docker.remove[0]
	docker.remove = docker.remove[1:]
	return err
}

func (docker *recoveryDocker) counts() (runs, reads, removes int) {
	docker.mu.Lock()
	defer docker.mu.Unlock()
	return docker.runs, docker.reads, docker.removes
}

func completedRecoveryResponse(profileID, requestID string) []byte {
	return mustJSON(airunner.Response{OK: true, ProfileID: profileID, RequestID: requestID,
		Result: &airunner.Result{ProfileID: profileID, ThreadID: "recovered-thread",
			Turn: airunner.Turn{Status: "completed"}, Process: airunner.Process{Status: "exited", ExitCode: 0}}})
}

func mustJSON(value any) []byte {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return raw
}

func seedRecovery(t *testing.T, docker *recoveryDocker, profileID, requestID string) (*Broker, *MemoryJournal, JournalEntry) {
	t.Helper()
	journal := NewMemoryJournal()
	entry := JournalEntry{ProfileID: profileID, RequestID: requestID, PayloadHash: strings.Repeat("a", 64), State: RunRunning,
		ContainerName: ContainerName(profileID, requestID), VolumeName: ProfileVolumeName(profileID), UpdatedAt: time.Now().UTC()}
	if created, err := journal.Create(context.Background(), entry); err != nil || !created {
		t.Fatalf("seed running entry: created=%v err=%v", created, err)
	}
	broker, err := New(Config{Docker: docker, Journal: journal, Image: "fixture:local", Network: "none", StopTimeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = broker.Close() })
	return broker, journal, entry
}

func TestBrokerRecoversExitedCompletedResultAndRetriesCleanup(t *testing.T) {
	docker := &recoveryDocker{state: DockerContainerExited,
		result: DockerResult{ExitCode: 0, Stdout: completedRecoveryResponse("orphan-profile", "request-1")},
		remove: []error{ErrDocker, nil}}
	broker, journal, entry := seedRecovery(t, docker, "orphan-profile", "request-1")

	updated, err := journal.Get(context.Background(), entry.ProfileID, entry.RequestID)
	if err != nil || updated.State != RunCompleted {
		t.Fatalf("startup recovery state=%s err=%v", updated.State, err)
	}
	if _, err := broker.Lookup(context.Background(), entry.ProfileID, entry.RequestID); err != nil {
		t.Fatalf("completed lookup err=%v", err)
	}
	runs, reads, removes := docker.counts()
	if runs != 0 || reads != 1 || removes != 2 {
		t.Fatalf("recovery Docker calls: runs=%d reads=%d removes=%d", runs, reads, removes)
	}
	result, err := broker.Lookup(context.Background(), entry.ProfileID, entry.RequestID)
	if err != nil || result.State != RunCompleted || result.Response.Result == nil || result.Response.Result.ThreadID != "recovered-thread" {
		t.Fatalf("durable recovered result=%+v err=%v", result, err)
	}
}

func TestBrokerKeepsExitedRunWhenResultReadFailsAndRetries(t *testing.T) {
	docker := &recoveryDocker{state: DockerContainerExited,
		readErrs: []error{ErrDocker},
		result:   DockerResult{ExitCode: 0, Stdout: completedRecoveryResponse("retry-profile", "request-1")}}
	broker, journal, entry := seedRecovery(t, docker, "retry-profile", "request-1")
	updated, err := journal.Get(context.Background(), entry.ProfileID, entry.RequestID)
	if err != nil || updated.State != RunRunning {
		t.Fatalf("temporary result error changed state=%s err=%v", updated.State, err)
	}
	if result, err := broker.Lookup(context.Background(), entry.ProfileID, entry.RequestID); err != nil || result.State != RunCompleted {
		t.Fatalf("retry lookup result=%+v err=%v", result, err)
	}
	updated, err = journal.Get(context.Background(), entry.ProfileID, entry.RequestID)
	if err != nil || updated.State != RunCompleted {
		t.Fatalf("retry did not recover state=%s err=%v", updated.State, err)
	}
	_, reads, _ := docker.counts()
	if reads != 2 {
		t.Fatalf("result reads=%d, want startup failure plus lookup retry", reads)
	}
}

func TestBrokerRejectsMismatchedRecoveredEnvelope(t *testing.T) {
	docker := &recoveryDocker{state: DockerContainerExited,
		result: DockerResult{ExitCode: 0, Stdout: completedRecoveryResponse("wrong-profile", "request-1")}}
	broker, journal, entry := seedRecovery(t, docker, "expected-profile", "request-1")
	updated, err := journal.Get(context.Background(), entry.ProfileID, entry.RequestID)
	if err != nil || updated.State != RunUnknown {
		t.Fatalf("mismatched response state=%s err=%v", updated.State, err)
	}
	if result, err := broker.Lookup(context.Background(), entry.ProfileID, entry.RequestID); !errors.Is(err, ErrRunUnknown) || result.Response.Result != nil {
		t.Fatalf("mismatched response replay=%+v err=%v", result, err)
	}
}

func TestBrokerTreatsOverlimitRecoveredLogsAsUnknown(t *testing.T) {
	docker := &recoveryDocker{state: DockerContainerExited,
		result: DockerResult{ExitCode: 0, Stdout: []byte(strings.Repeat("x", DefaultMaxStdoutBytes+1))}}
	_, journal, entry := seedRecovery(t, docker, "overlimit-profile", "request-1")
	updated, err := journal.Get(context.Background(), entry.ProfileID, entry.RequestID)
	if err != nil || updated.State != RunUnknown {
		t.Fatalf("overlimit response state=%s err=%v", updated.State, err)
	}
}

func TestBrokerRetainsRemovingAndDeadRecoveryStates(t *testing.T) {
	for _, test := range []struct {
		name  string
		state DockerContainerState
	}{
		{name: "removing", state: DockerContainerRemoving},
		{name: "running", state: DockerContainerRunning},
	} {
		t.Run(test.name, func(t *testing.T) {
			docker := &recoveryDocker{state: test.state}
			broker, journal, entry := seedRecovery(t, docker, "state-"+test.name, "request-1")
			updated, err := journal.Get(context.Background(), entry.ProfileID, entry.RequestID)
			if err != nil || updated.State != RunRunning {
				t.Fatalf("%s recovery state=%s err=%v", test.state, updated.State, err)
			}
			if _, err := broker.Lookup(context.Background(), entry.ProfileID, entry.RequestID); !errors.Is(err, ErrRunRunning) {
				t.Fatalf("%s lookup err=%v", test.state, err)
			}
		})
	}
	dead := &recoveryDocker{state: DockerContainerDead}
	broker, journal, entry := seedRecovery(t, dead, "state-dead", "request-1")
	defer broker.Close()
	updated, err := journal.Get(context.Background(), entry.ProfileID, entry.RequestID)
	if err != nil || updated.State != RunUnknown {
		t.Fatalf("dead recovery state=%s err=%v", updated.State, err)
	}
}
