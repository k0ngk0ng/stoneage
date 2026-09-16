package aiprovision

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aiinitial"
	"github.com/k0ngk0ng/stoneage/internal/airuntime"
	"github.com/k0ngk0ng/stoneage/internal/characterbuild"
	"github.com/k0ngk0ng/stoneage/internal/playerdata"
)

// Initializer is server-owned. It validates catalog choices before creating
// an account and applies one native transaction to a newly created character.
type Initializer interface {
	Validate(context.Context, aiinitial.Resolved) error
	Apply(context.Context, string, int, aiinitial.Resolved) (playerdata.Snapshot, error)
}
type InitialState struct {
	PendingProfile *airuntime.Profile   `json:"pending_profile,omitempty"`
	Binding        *Binding             `json:"binding,omitempty"`
	Requested      aiinitial.Request    `json:"requested"`
	Resolved       aiinitial.Resolved   `json:"resolved"`
	Status         string               `json:"status"`
	Actual         *playerdata.Snapshot `json:"actual,omitempty"`
	UpdatedAt      time.Time            `json:"updated_at"`
}

func (p *Provisioner) InitialState(ctx context.Context, id string) (*InitialState, error) {
	var raw string
	err := p.config.Auth.DB().QueryRowContext(ctx, "SELECT record_json FROM ai_initial_states WHERE profile_id=?", id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var record InitialState
	if err := json.Unmarshal([]byte(raw), &record); err != nil {
		return nil, err
	}
	return &record, nil
}
func (p *Provisioner) reserveInitial(ctx context.Context, id string, request *aiinitial.Request, initializer Initializer) (*InitialState, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	// A previous attempt, including an uncertain or failed one, must never be
	// silently rerolled or reapplied to another character with the same ID.
	if old, err := p.InitialState(ctx, id); err != nil {
		return nil, err
	} else if old != nil {
		return nil, ErrBindingConflict
	}
	if request == nil || (request.Mode == "birth" && (request.Mount == nil || !*request.Mount)) {
		return nil, nil
	}
	if initializer == nil {
		return nil, errors.New("AI initial-state service is unavailable")
	}
	if request.Mode == "random" {
		// Every candidate must support the entire declared range, so validation
		// cannot succeed or fail depending on a later random draw.
		for _, id := range request.Random.PetTemplates {
			probe := aiinitial.Resolved{CharacterLevel: request.Random.CharacterLevel.Min, Hometown: request.Random.Hometowns[0], Weights: characterbuild.Weights{Vital: 1}, Pets: []aiinitial.Pet{{TemplateID: id, Level: request.Random.PetLevel.Max}}}
			probe.Mount = request.Mount != nil && *request.Mount
			if err := initializer.Validate(ctx, probe); err != nil {
				return nil, err
			}
		}
	}
	resolved, err := aiinitial.Resolve(request, nil)
	if err != nil {
		return nil, err
	}
	if err := initializer.Validate(ctx, *resolved); err != nil {
		return nil, err
	}
	rawRequest, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	var cloned aiinitial.Request
	if err = json.Unmarshal(rawRequest, &cloned); err != nil {
		return nil, err
	}
	record := &InitialState{Requested: cloned, Resolved: *resolved, Status: "reserved", UpdatedAt: time.Now().UTC()}
	raw, err := json.Marshal(record)
	if err != nil {
		return nil, err
	}
	_, err = p.config.Auth.DB().ExecContext(ctx, "INSERT INTO ai_initial_states(profile_id,account_id,record_json) VALUES(?,NULL,?)", id, string(raw))
	if err != nil {
		return nil, fmt.Errorf("%w: initial state already reserved or unavailable", ErrBindingConflict)
	}
	return record, nil
}
func (p *Provisioner) saveInitial(ctx context.Context, id string, accountID int64, r *InitialState) error {
	r.UpdatedAt = time.Now().UTC()
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	var account any
	if accountID != 0 {
		account = accountID
	}
	result, err := p.config.Auth.DB().ExecContext(ctx, "UPDATE ai_initial_states SET account_id=COALESCE(?,account_id), record_json=? WHERE profile_id=?", account, string(raw), id)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrBindingConflict
	}
	return nil
}
