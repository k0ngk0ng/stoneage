package arenaagent

import (
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"

	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
	"github.com/k0ngk0ng/stoneage/internal/battletrain"
)

// A native gate is deliberately not an online arena certificate. Keep its
// complete conditions beside each selection, including on recovery/rollback.
type modelSelection struct {
	Schema         string                       `json:"schema"`
	Directory      string                       `json:"champion_directory"`
	RegistryID     string                       `json:"registry_digest"`
	Registry       battletrain.ChampionRegistry `json:"registry"`
	Head           string                       `json:"event_digest"`
	Artifact       string                       `json:"artifact_digest"`
	Policy         string                       `json:"policy"`
	ArenaCertified bool                         `json:"arena_certified"`
}

func selectedDigest(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == 32 && hex.EncodeToString(b) == s
}

type championLoader func(context.Context, string) (battletrain.ChampionStatus, error)

func selectChampion(ctx context.Context, c Config, load championLoader) (Strategy, *modelSelection, error) {
	if err := c.validateModelSource(); err != nil {
		return nil, nil, err
	}
	if load == nil {
		load = battletrain.LoadChampionRegistry
	}
	status, err := load(ctx, c.ChampionDirectory)
	if err != nil {
		return nil, nil, fmt.Errorf("verify native champion: %w", err)
	}
	if status.Purpose != "controlled-native-champion" || status.Champion == "" || status.Head == "" {
		return nil, nil, fmt.Errorf("champion registry has no model that passed its native gates")
	}
	if err := status.Registry.Validate(); err != nil {
		return nil, nil, err
	}
	a, err := battlepolicy.LoadArtifact(status.Model)
	if err != nil {
		return nil, nil, err
	}
	if hash(a) != status.Champion {
		return nil, nil, fmt.Errorf("champion artifact changed after verification")
	}
	selection := &modelSelection{Schema: "commander-native-selection-v1", Directory: c.ChampionDirectory,
		RegistryID: hash(status.Registry), Registry: status.Registry, Head: status.Head, Artifact: status.Champion}
	s, err := selectedStrategy(c, selection, a, false)
	if err != nil {
		return nil, nil, err
	}
	selection.Policy = s.ID() + ":" + s.Version()
	return s, selection, ctx.Err()
}

func selectedStrategy(c Config, selection *modelSelection, a battlepolicy.Artifact, restored bool) (Strategy, error) {
	if err := selection.Registry.Validate(); err != nil {
		return nil, err
	}
	if conditions := selection.Registry.MixedConditions; len(conditions) > 0 {
		if a.Schema != 6 || len(a.Modes) != len(conditions) {
			return nil, fmt.Errorf("selected mixed champion lost a declared mode")
		}
		for i, condition := range conditions {
			if a.Modes[i] != condition.Mode {
				return nil, fmt.Errorf("selected mixed champion mode coverage differs from registry")
			}
		}
	}
	if selection.Schema != "commander-native-selection-v1" || selection.Directory != c.ChampionDirectory ||
		selection.ArenaCertified || !selection.Registry.SupportsMode(c.Mode) ||
		!selectedDigest(selection.Head) || !selectedDigest(selection.Artifact) ||
		selection.RegistryID != hash(selection.Registry) || selection.Artifact != hash(a) ||
		selection.Registry.Environment != a.Environment {
		return nil, fmt.Errorf("saved/selected native champion differs from configuration or artifact")
	}
	local, err := learnedArtifact(a, c.Mode)
	if err != nil {
		return nil, err
	}
	s, err := strategyWithLearned(c, local)
	if err != nil {
		return nil, err
	}
	if restored && selection.Policy != s.ID()+":"+s.Version() {
		return nil, fmt.Errorf("saved commander policy differs; restore the original strategy/LLM configuration")
	}
	return s, nil
}

func configuredStrategy(ctx context.Context, c Config) (Strategy, *modelSelection, error) {
	if err := c.validateModelSource(); err != nil {
		return nil, nil, err
	}
	if c.ChampionDirectory != "" {
		return selectChampion(ctx, c, nil)
	}
	s, err := makeStrategy(c)
	return s, nil, err
}

// Artifact bytes, pointer and evidence commit together before queue can be
// sent. Restarts never depend on the source registry still being available.
func (s *Store) saveSelection(selection *modelSelection, strategy Strategy) error {
	local := localModel(strategy)
	if local == nil || local.neural == nil || selection.Artifact != hash(*local.neural) || selection.Policy != strategy.ID()+":"+strategy.Version() {
		return fmt.Errorf("cannot persist inconsistent commander selection")
	}
	s.tx(func(tx *sql.Tx) error {
		if _, err := tx.Exec("INSERT OR IGNORE INTO commander_models VALUES(?,?)", selection.Artifact, enc(local.neural)); err != nil {
			return err
		}
		var saved []byte
		if err := tx.QueryRow("SELECT body FROM commander_models WHERE artifact=?", selection.Artifact).Scan(&saved); err != nil {
			return err
		}
		a, err := battlepolicy.DecodeArtifact(saved)
		if err != nil || hash(a) != selection.Artifact {
			return fmt.Errorf("cached commander artifact is corrupt")
		}
		if _, err := tx.Exec("INSERT OR REPLACE INTO commander_selection VALUES(1,?)", string(enc(selection))); err != nil {
			return err
		}
		return record(tx, "queue_model", selection, "", "")
	})
	return s.Err()
}

func (s *Store) restoreSelection(c Config) (Strategy, *modelSelection, error) {
	var selection *modelSelection
	var raw []byte
	s.tx(func(tx *sql.Tx) error {
		var body string
		err := tx.QueryRow("SELECT body FROM commander_selection WHERE id=1").Scan(&body)
		if err == sql.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		if err = decode([]byte(body), &selection); err != nil {
			return err
		}
		if selection == nil {
			return fmt.Errorf("invalid saved commander selection")
		}
		return tx.QueryRow("SELECT body FROM commander_models WHERE artifact=?", selection.Artifact).Scan(&raw)
	})
	if err := s.Err(); err != nil {
		return nil, nil, err
	}
	if selection == nil {
		return nil, nil, nil
	}
	a, err := battlepolicy.DecodeArtifact(raw)
	if err != nil {
		return nil, nil, err
	}
	strategy, err := selectedStrategy(c, selection, a, true)
	return strategy, selection, err
}

func (r *Runner) pinMatch(match string) error {
	if r.selection != nil && !r.selectionSaved {
		return fmt.Errorf("active match has no saved model selection; refusing to choose a new champion mid-match")
	}
	return r.store.pinSelection(match, r.strategy.ID()+":"+r.strategy.Version(), r.selection)
}

// Called only after every member is observed idle and ready. In particular,
// an uncertain prior queue request must keep the model saved for that request.
func (r *Runner) selectForQueue(ctx context.Context) error {
	if r.config.ChampionDirectory == "" {
		return nil
	}
	for _, member := range r.members {
		if pending := r.store.Pending(member.cfg.ID); str(pending["operation"]) == "queue" {
			if !r.selectionSaved {
				return fmt.Errorf("uncertain queue request has no saved commander model")
			}
			return r.store.Err()
		}
	}
	if err := r.store.Err(); err != nil {
		return err
	}
	strategy, selection, err := selectChampion(ctx, r.config, r.loadChampion)
	if err != nil {
		return err
	}
	// Validate the selected model against all actual servers before it can be
	// persisted or queued. A registry's native scenario does not certify arena.
	if err = r.validateSelectedServer(ctx, localModel(strategy)); err != nil {
		return err
	}
	if err = r.store.saveSelection(selection, strategy); err != nil {
		return err
	}
	r.strategy, r.selection, r.selectionSaved = strategy, selection, true
	r.report("model_selected", Object{"selection": selection})
	return nil
}

func (r *Runner) validateSelectedServer(ctx context.Context, local *Learned) error {
	for _, m := range r.members {
		if _, err := m.request(ctx, "query", "BTRULES"); err != nil {
			return err
		}
		v, err := waitServerCapabilities(ctx, true, func(ctx context.Context) (Object, error) { return m.data(ctx, "battle-state") })
		if err != nil {
			return err
		}
		if err := local.validateServer(v); err != nil {
			return err
		}
	}
	return ctx.Err()
}
