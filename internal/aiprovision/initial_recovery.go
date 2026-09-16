package aiprovision

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aiinitial"
	"github.com/k0ngk0ng/stoneage/internal/airuntime"
	"github.com/k0ngk0ng/stoneage/internal/auth"
	"github.com/k0ngk0ng/stoneage/internal/playerdata"
)

var ErrInitialUnconfirmed = errors.New("aiprovision: initial state is not confirmed for publication")

type InitialRecoveryVerifier interface {
	Verify(context.Context, string, int, aiinitial.Resolved) (playerdata.Snapshot, error)
}

type InitialRecoveryView struct {
	ProfileID     string    `json:"profile_id"`
	CharacterName string    `json:"character_name"`
	Status        string    `json:"status"`
	Recoverable   bool      `json:"recoverable"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func initialPublicationReady(record *InitialState) bool {
	return record != nil && record.Actual != nil && record.PendingProfile != nil && record.Binding != nil &&
		aigame.ValidPersistentCharacterID(record.Actual.PersistentCharacterID) &&
		(record.Status == "applied" || record.Status == "publication_pending")
}

func (p *Provisioner) ListInitialRecoveries(ctx context.Context) ([]InitialRecoveryView, error) {
	rows, err := p.config.Auth.DB().QueryContext(ctx, "SELECT profile_id,record_json FROM ai_initial_states ORDER BY profile_id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []InitialRecoveryView{}
	for rows.Next() {
		var id, raw string
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		var record InitialState
		if err := json.Unmarshal([]byte(raw), &record); err != nil {
			return nil, err
		}
		if record.Status == "published" {
			continue
		}
		// Legacy successful records had no publication marker or recovery draft.
		// They remain usable, and should not be advertised as interrupted creation.
		if record.PendingProfile == nil && record.Status == "applied" && record.Actual != nil {
			if _, err := p.config.Profiles.GetProfile(ctx, id); err == nil {
				continue
			} else if !errors.Is(err, airuntime.ErrNotFound) {
				return nil, err
			}
		}
		view := InitialRecoveryView{ProfileID: id, Status: record.Status, Recoverable: initialPublicationReady(&record), UpdatedAt: record.UpdatedAt}
		if record.Binding != nil {
			view.CharacterName = record.Binding.CharacterName
		}
		result = append(result, view)
	}
	return result, rows.Err()
}

// checkInitialPublication closes the cross-database publication window. A
// profile row alone must not let the runtime open an unfinished initialization.
func checkInitialPublication(ctx context.Context, db *sql.DB, id string) error {
	var raw string
	err := db.QueryRowContext(ctx, "SELECT record_json FROM ai_initial_states WHERE profile_id=?", id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var record InitialState
	if err := json.Unmarshal([]byte(raw), &record); err != nil {
		return err
	}
	if record.Status == "published" && record.Actual != nil {
		return nil
	}
	if record.PendingProfile == nil && record.Status == "applied" && record.Actual != nil {
		return nil
	} // prior verified records
	return ErrInitialUnconfirmed
}

// RecoverInitial only publishes previously confirmed initialization. It never
// logs into the game, creates a character or calls Initializer.Apply.
func (p *Provisioner) RecoverInitial(ctx context.Context, id, actor string, actorID *int64, verifier InitialRecoveryVerifier) (airuntime.Profile, error) {
	if p == nil || !profileIDPattern.MatchString(id) {
		return airuntime.Profile{}, ErrInvalidConfig
	}
	release, err := p.lockInitialPublication()
	if err != nil {
		return airuntime.Profile{}, err
	}
	defer release()
	record, err := p.InitialState(ctx, id)
	if err != nil {
		return airuntime.Profile{}, err
	}
	if record != nil && record.Status == "published" {
		profile, err := p.config.Profiles.GetProfile(ctx, id)
		if err != nil {
			return airuntime.Profile{}, err
		}
		binding, err := p.Binding(ctx, id)
		if err != nil {
			return airuntime.Profile{}, err
		}
		if err := validateProfileBinding(profile, binding); err != nil {
			return airuntime.Profile{}, err
		}
		return profile, nil // idempotent; do not re-enable an account disabled later
	}
	if !initialPublicationReady(record) || verifier == nil {
		return airuntime.Profile{}, ErrInitialUnconfirmed
	}
	if err := record.Resolved.Validate(); err != nil {
		return airuntime.Profile{}, ErrInitialUnconfirmed
	}
	binding := *record.Binding
	if record.Actual.Name != binding.CharacterName {
		return airuntime.Profile{}, ErrInitialUnconfirmed
	}
	if binding.ProfileID != id || validateBinding(binding) != nil || record.PendingProfile.ID != id || validateProfileBinding(*record.PendingProfile, binding) != nil {
		return airuntime.Profile{}, ErrBindingConflict
	}
	var accountID int64
	if err := p.config.Auth.DB().QueryRowContext(ctx, "SELECT account_id FROM ai_initial_states WHERE profile_id=?", id).Scan(&accountID); err != nil || accountID != binding.AccountID {
		return airuntime.Profile{}, ErrBindingConflict
	}
	account, err := p.config.Auth.GetAccount(ctx, accountID)
	if err != nil || account.Username != binding.AccountUsername {
		return airuntime.Profile{}, ErrAccountUnavailable
	}
	password, err := p.config.Secrets.read(accountID)
	if err != nil {
		return airuntime.Profile{}, ErrSecretUnavailable
	}
	zeroBytes(password)
	actual, err := verifier.Verify(ctx, binding.AccountUsername, binding.CharacterSlot, record.Resolved)
	if err != nil {
		return airuntime.Profile{}, fmt.Errorf("%w: saved character verification failed", ErrInitialUnconfirmed)
	}
	if actual.Online || actual.Name != binding.CharacterName || actual.PersistentCharacterID != record.Actual.PersistentCharacterID {
		return airuntime.Profile{}, ErrInitialUnconfirmed
	}
	// The immutable binding may have committed before a crash in the other DB.
	existing, err := p.Binding(ctx, id)
	if errors.Is(err, ErrBindingNotFound) {
		if err := insertBinding(ctx, p.config.Auth.DB(), binding); err != nil {
			return airuntime.Profile{}, err
		}
	} else if err != nil {
		return airuntime.Profile{}, err
	} else if existing.AccountID != binding.AccountID || existing.CharacterSlot != binding.CharacterSlot || existing.AccountUsername != binding.AccountUsername || existing.CharacterID != binding.CharacterID || existing.CharacterName != binding.CharacterName {
		return airuntime.Profile{}, ErrBindingConflict
	}
	profile, err := p.config.Profiles.GetProfile(ctx, id)
	if errors.Is(err, airuntime.ErrNotFound) {
		profile = *record.PendingProfile
		profile.Status = airuntime.ProfileStatusStopped
		profile, err = p.config.Profiles.CreateProfileAs(ctx, profile, actor)
	} else if err == nil {
		if err := validateProfileBinding(profile, binding); err != nil {
			return airuntime.Profile{}, err
		}
		if profile.Status == airuntime.ProfileStatusDeleted {
			return airuntime.Profile{}, ErrBindingConflict
		}
		if profile.Status != airuntime.ProfileStatusStopped {
			status := airuntime.ProfileStatusStopped
			profile, err = p.config.Profiles.UpdateProfileCAS(ctx, id, profile.Version, airuntime.ProfilePatch{Status: &status, Actor: actor})
		}
	}
	if err != nil {
		return airuntime.Profile{}, err
	}
	if err := p.config.Auth.SetAccountStatusAs(ctx, actorID, accountID, auth.AccountActive); err != nil {
		return airuntime.Profile{}, err
	}
	record.Status = "published"
	record.Actual = &actual
	if err := p.saveInitial(ctx, id, accountID, record); err != nil {
		return airuntime.Profile{}, err
	}
	return profile, nil
}

// ReconcileInitialAccounts runs at admin runtime startup before AI sessions
// open. Holding the shared provisioning lock proves no current creator owns
// these intermediate rows. It quarantines accounts; it never retries gameplay.
func (p *Provisioner) ReconcileInitialAccounts(ctx context.Context) error {
	release, err := p.lockInitialPublication()
	if err != nil {
		return err
	}
	defer release()
	rows, err := p.config.Auth.DB().QueryContext(ctx, "SELECT profile_id,account_id,record_json FROM ai_initial_states")
	if err != nil {
		return err
	}
	type pending struct {
		id      string
		account sql.NullInt64
		record  InitialState
	}
	var records []pending
	for rows.Next() {
		var row pending
		var raw string
		if err := rows.Scan(&row.id, &row.account, &raw); err != nil {
			rows.Close()
			return err
		}
		if err := json.Unmarshal([]byte(raw), &row.record); err != nil {
			rows.Close()
			return err
		}
		records = append(records, row)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, row := range records {
		if row.record.Status == "published" {
			continue
		}
		if row.record.Status == "applied" && row.record.PendingProfile == nil && row.record.Actual != nil {
			if _, err := p.config.Profiles.GetProfile(ctx, row.id); err == nil {
				continue
			} else if !errors.Is(err, airuntime.ErrNotFound) {
				return err
			}
		}
		if row.account.Valid {
			account, err := p.config.Auth.GetAccount(ctx, row.account.Int64)
			if err != nil {
				return err
			}
			if account.Status != auth.AccountDisabled {
				if err := p.config.Auth.SetAccountStatusAs(ctx, nil, row.account.Int64, auth.AccountDisabled); err != nil {
					return err
				}
			}
		}
		status := "failed_or_unconfirmed"
		if initialPublicationReady(&row.record) {
			status = "publication_pending"
		}
		if row.record.Status != status {
			row.record.Status = status
			if err := p.saveInitial(ctx, row.id, row.account.Int64, &row.record); err != nil {
				return err
			}
		}
	}
	return nil
}
