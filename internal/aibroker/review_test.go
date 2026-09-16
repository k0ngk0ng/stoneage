package aibroker

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func unknownReviewEntry(profileID, requestID string, updatedAt time.Time) JournalEntry {
	entry := runningJournalEntry(profileID, requestID)
	entry.State = RunUnknown
	entry.ErrorCode = string(RunUnknown)
	entry.Response = marshalUnknownResponse(profileID, requestID)
	entry.UpdatedAt = updatedAt
	return entry
}

func reviewJournalCases(t *testing.T) []struct {
	name    string
	journal Journal
	close   func()
} {
	t.Helper()
	cases := []struct {
		name    string
		journal Journal
		close   func()
	}{{name: "memory", journal: NewMemoryJournal()}}
	root := testJournalRoot(t)
	sqlite, err := OpenSQLiteJournal(filepath.Join(root, "review.db"))
	if err != nil {
		t.Fatal(err)
	}
	cases = append(cases, struct {
		name    string
		journal Journal
		close   func()
	}{name: "sqlite", journal: sqlite, close: func() { _ = sqlite.Close() }})
	return cases
}

func TestBrokerReviewsUnknownWithoutChangingOutcome(t *testing.T) {
	for _, test := range reviewJournalCases(t) {
		t.Run(test.name, func(t *testing.T) {
			if test.close != nil {
				t.Cleanup(test.close)
			}
			updatedAt := time.Date(2026, 9, 15, 10, 11, 12, 0, time.UTC)
			entry := unknownReviewEntry("review-profile-"+test.name, "request-1", updatedAt)
			if created, err := test.journal.Create(context.Background(), entry); err != nil || !created {
				t.Fatalf("seed unknown created=%v err=%v", created, err)
			}
			docker := &recoveryDocker{state: DockerContainerExited}
			broker := newFakeBroker(t, docker, test.journal)
			t.Cleanup(func() { _ = broker.Close() })

			reviewed, err := broker.ReviewUnknown(context.Background(), entry.ProfileID, entry.RequestID, updatedAt, "operator", ReviewReasonAcceptUncertainOutcome)
			if err != nil {
				t.Fatalf("review unknown: %v", err)
			}
			if reviewed.State != RunUnknown || reviewed.Review == nil || reviewed.Review.Actor != "operator" || reviewed.Review.Reason != ReviewReasonAcceptUncertainOutcome || reviewed.Review.ReviewedAt.IsZero() {
				t.Fatalf("reviewed entry=%+v", reviewed)
			}
			if string(reviewed.Response) != string(entry.Response) || reviewed.UpdatedAt != entry.UpdatedAt {
				t.Fatalf("review changed durable outcome: before=%+v after=%+v", entry, reviewed)
			}
			if runs, _, removes := docker.counts(); runs != 0 || removes != 1 {
				t.Fatalf("review Docker calls runs=%d removes=%d", runs, removes)
			}

			// Repeating the exact disposition returns the durable review and does
			// not require a second container inspection or removal.
			retried, err := broker.ReviewUnknown(context.Background(), entry.ProfileID, entry.RequestID, updatedAt, "operator", ReviewReasonAcceptUncertainOutcome)
			if err != nil || retried.Review == nil || retried.Review.ReviewedAt != reviewed.Review.ReviewedAt {
				t.Fatalf("idempotent review=%+v err=%v", retried, err)
			}
			if _, _, removes := docker.counts(); removes != 1 {
				t.Fatalf("idempotent review removed container again: %d", removes)
			}

			// A stale version, a different actor, or a different mutation path
			// cannot revise or clear the immutable review.
			if _, err := broker.ReviewUnknown(context.Background(), entry.ProfileID, entry.RequestID, updatedAt.Add(time.Second), "operator", ReviewReasonAcceptUncertainOutcome); err != nil {
				t.Fatalf("idempotent retry with stale expected version err=%v", err)
			}
			if _, err := broker.ReviewUnknown(context.Background(), entry.ProfileID, entry.RequestID, updatedAt, "other-operator", ReviewReasonAcceptUncertainOutcome); !errors.Is(err, ErrJournalState) {
				t.Fatalf("different review err=%v, want journal state", err)
			}
			candidate := reviewed
			candidate.Review = nil
			candidate.State = RunCompleted
			candidate.Response = []byte(`{"ok":true}`)
			if err := test.journal.Update(context.Background(), candidate); !errors.Is(err, ErrJournalState) {
				t.Fatalf("reviewed Update err=%v, want journal state", err)
			}
			if cas, ok := test.journal.(JournalCAS); ok {
				if err := cas.UpdateIfState(context.Background(), RunUnknown, candidate); !errors.Is(err, ErrJournalState) {
					t.Fatalf("reviewed CAS err=%v, want journal state", err)
				}
			}

			// The reviewed unknown row remains replayable as unknown, while a
			// later request can claim the profile after the atomic index release.
			if result, err := broker.Lookup(context.Background(), entry.ProfileID, entry.RequestID); !errors.Is(err, ErrRunUnknown) || result.State != RunUnknown || result.Response.OK {
				t.Fatalf("old request replay=%+v err=%v", result, err)
			}
			newEntry := runningJournalEntry(entry.ProfileID, "request-2")
			newEntry.UpdatedAt = updatedAt.Add(time.Minute)
			if created, err := test.journal.Create(context.Background(), newEntry); err != nil || !created {
				t.Fatalf("new claim after review created=%v err=%v", created, err)
			}
		})
	}
}

func TestBrokerRejectsUnknownReviewForLiveOrUninspectableContainer(t *testing.T) {
	for _, test := range []struct {
		name  string
		state DockerContainerState
	}{
		{name: "created", state: DockerContainerCreated},
		{name: "running", state: DockerContainerRunning},
		{name: "paused", state: DockerContainerPaused},
		{name: "restarting", state: DockerContainerRestarting},
		{name: "removing", state: DockerContainerRemoving},
	} {
		t.Run(test.name, func(t *testing.T) {
			journal := NewMemoryJournal()
			at := time.Now().UTC()
			entry := unknownReviewEntry("live-review-"+test.name, "request-1", at)
			if _, err := journal.Create(context.Background(), entry); err != nil {
				t.Fatal(err)
			}
			broker := newFakeBroker(t, &recoveryDocker{state: test.state}, journal)
			defer broker.Close()
			if _, err := broker.ReviewUnknown(context.Background(), entry.ProfileID, entry.RequestID, at, "operator", ReviewReasonAcceptUncertainOutcome); !errors.Is(err, ErrRunRunning) {
				t.Fatalf("state=%s review err=%v, want live-run rejection", test.state, err)
			}
			got, err := journal.Get(context.Background(), entry.ProfileID, entry.RequestID)
			if err != nil || got.Review != nil {
				t.Fatalf("state=%s journal=%+v err=%v", test.state, got, err)
			}
		})
	}

	t.Run("inspect-error", func(t *testing.T) {
		journal := NewMemoryJournal()
		at := time.Now().UTC()
		entry := unknownReviewEntry("inspect-error-review", "request-1", at)
		if _, err := journal.Create(context.Background(), entry); err != nil {
			t.Fatal(err)
		}
		docker := &inspectDocker{errors: []error{ErrDocker}}
		broker := newFakeBroker(t, docker, journal)
		defer broker.Close()
		if _, err := broker.ReviewUnknown(context.Background(), entry.ProfileID, entry.RequestID, at, "operator", ReviewReasonAcceptUncertainOutcome); !errors.Is(err, ErrDocker) {
			t.Fatalf("inspect failure err=%v, want Docker error", err)
		}
	})

	t.Run("missing-inspector", func(t *testing.T) {
		journal := NewMemoryJournal()
		at := time.Now().UTC()
		entry := unknownReviewEntry("missing-inspector-review", "request-1", at)
		if _, err := journal.Create(context.Background(), entry); err != nil {
			t.Fatal(err)
		}
		broker := newFakeBroker(t, &fakeDocker{}, journal)
		defer broker.Close()
		if _, err := broker.ReviewUnknown(context.Background(), entry.ProfileID, entry.RequestID, at, "operator", ReviewReasonAcceptUncertainOutcome); !errors.Is(err, ErrDocker) {
			t.Fatalf("missing inspector err=%v, want Docker error", err)
		}
	})
}

func TestBrokerReviewRejectsLocalActiveRun(t *testing.T) {
	journal := NewMemoryJournal()
	at := time.Now().UTC()
	entry := unknownReviewEntry("active-review", "request-1", at)
	if _, err := journal.Create(context.Background(), entry); err != nil {
		t.Fatal(err)
	}
	docker := &recoveryDocker{state: DockerContainerExited}
	broker := newFakeBroker(t, docker, journal)
	defer broker.Close()
	broker.mu.Lock()
	broker.active[entry.ContainerName] = func() {}
	broker.mu.Unlock()
	if _, err := broker.ReviewUnknown(context.Background(), entry.ProfileID, entry.RequestID, at, "operator", ReviewReasonAcceptUncertainOutcome); !errors.Is(err, ErrProfileBusy) {
		t.Fatalf("local active review err=%v, want profile busy", err)
	}
	broker.mu.Lock()
	delete(broker.active, entry.ContainerName)
	broker.mu.Unlock()
}

func TestBrokerReviewAndNewClaimReleaseAtomically(t *testing.T) {
	for _, test := range reviewJournalCases(t) {
		t.Run(test.name, func(t *testing.T) {
			if test.close != nil {
				t.Cleanup(test.close)
			}
			at := time.Now().UTC()
			entry := unknownReviewEntry("review-race-"+test.name, "request-1", at)
			if _, err := test.journal.Create(context.Background(), entry); err != nil {
				t.Fatal(err)
			}
			docker := &blockingReviewDocker{recoveryDocker: recoveryDocker{state: DockerContainerExited}, started: make(chan struct{}), release: make(chan struct{})}
			broker := newFakeBroker(t, docker, test.journal)
			defer broker.Close()
			result := make(chan error, 1)
			go func() {
				_, err := broker.ReviewUnknown(context.Background(), entry.ProfileID, entry.RequestID, at, "operator", ReviewReasonAcceptUncertainOutcome)
				result <- err
			}()
			select {
			case <-docker.started:
			case <-time.After(time.Second):
				t.Fatal("review did not inspect container")
			}
			newEntry := runningJournalEntry(entry.ProfileID, "request-2")
			if created, err := test.journal.Create(context.Background(), newEntry); created || !errors.Is(err, ErrProfileBusy) {
				t.Fatalf("concurrent claim created=%v err=%v, want profile busy", created, err)
			}
			close(docker.release)
			select {
			case err := <-result:
				if err != nil {
					t.Fatalf("review after claim race: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("review did not finish")
			}
			if created, err := test.journal.Create(context.Background(), newEntry); err != nil || !created {
				t.Fatalf("claim after review created=%v err=%v", created, err)
			}
		})
	}
}

type blockingReviewDocker struct {
	recoveryDocker
	started chan struct{}
	release chan struct{}
}

func (docker *blockingReviewDocker) Inspect(ctx context.Context, containerName string) (DockerContainerState, error) {
	select {
	case <-docker.started:
	default:
		close(docker.started)
	}
	select {
	case <-docker.release:
		return docker.state, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func TestSQLiteJournalMigratesLegacySchemaAndReviewSurvivesRestart(t *testing.T) {
	root := testJournalRoot(t)
	path := filepath.Join(root, "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE ai_broker_runs (
  profile_id TEXT NOT NULL,
  request_id TEXT NOT NULL,
  payload_hash TEXT NOT NULL,
  state TEXT NOT NULL CHECK (state IN ('running','completed','unknown')),
  container_name TEXT NOT NULL,
  volume_name TEXT NOT NULL,
  response BLOB,
  error_code TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL,
  PRIMARY KEY (profile_id, request_id)
);
CREATE UNIQUE INDEX ai_broker_active_profile_idx ON ai_broker_runs(profile_id) WHERE state IN ('running','unknown');`)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	at := time.Date(2026, 9, 15, 11, 12, 13, 0, time.UTC)
	entry := unknownReviewEntry("legacy-review", "request-1", at)
	if _, err := db.Exec(`INSERT INTO ai_broker_runs(profile_id,request_id,payload_hash,state,container_name,volume_name,response,error_code,updated_at) VALUES(?,?,?,?,?,?,?,?,?)`, entry.ProfileID, entry.RequestID, entry.PayloadHash, string(entry.State), entry.ContainerName, entry.VolumeName, entry.Response, entry.ErrorCode, formatJournalTime(entry.UpdatedAt)); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	journal, err := OpenSQLiteJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	reviewed, err := journal.ReviewUnknown(context.Background(), entry.ProfileID, entry.RequestID, at, "operator", ReviewReasonAcceptUncertainOutcome)
	if err != nil || reviewed.Review == nil {
		_ = journal.Close()
		t.Fatalf("legacy review=%+v err=%v", reviewed, err)
	}
	newEntry := runningJournalEntry(entry.ProfileID, "request-2")
	if created, err := journal.Create(context.Background(), newEntry); err != nil || !created {
		_ = journal.Close()
		t.Fatalf("legacy claim after review created=%v err=%v", created, err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenSQLiteJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err := reopened.Get(context.Background(), entry.ProfileID, entry.RequestID)
	if err != nil || got.Review == nil || got.Review.Actor != "operator" || got.State != RunUnknown || string(got.Response) != string(entry.Response) {
		t.Fatalf("reopened review=%+v err=%v", got, err)
	}
}

func TestReviewInputRejectsFreeTextAndInvalidActor(t *testing.T) {
	journal := NewMemoryJournal()
	at := time.Now().UTC()
	entry := unknownReviewEntry("review-input", "request-1", at)
	if _, err := journal.Create(context.Background(), entry); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, actor, reason string
	}{
		{name: "empty-actor", actor: "", reason: ReviewReasonAcceptUncertainOutcome},
		{name: "newline-actor", actor: "operator\nsecret", reason: ReviewReasonAcceptUncertainOutcome},
		{name: "oversize-actor", actor: strings.Repeat("x", maxReviewActorBytes+1), reason: ReviewReasonAcceptUncertainOutcome},
		{name: "free-text-reason", actor: "operator", reason: "provider accepted"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := journal.ReviewUnknown(context.Background(), entry.ProfileID, entry.RequestID, at, test.actor, test.reason); !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("review err=%v, want invalid request", err)
			}
		})
	}
}

var _ JournalReviewer = (*SQLiteJournal)(nil)
var _ JournalReviewer = (*MemoryJournal)(nil)
