package aisupervisor

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"math/big"

	"github.com/k0ngk0ng/stoneage/internal/airuntime"
)

func (s *Supervisor) ensureLifeActivity(m *managedProfile, generation uint64, profile airuntime.Profile, snapshot Snapshot) (LifeActivity, error) {
	if profile.Goal.LifeDecisionInterval() == 0 {
		return LifeActivity{}, nil
	}
	m.stateMu.RLock()
	current := m.state.Activity
	m.stateMu.RUnlock()
	if len(snapshot.ActiveTasks) > 0 {
		return current, nil
	}
	now := s.cfg.Clock().UTC()
	if airuntime.ValidLifeActivity(current.Kind) && !current.StartedAt.After(now) && current.Until.After(now) {
		return current, nil
	}
	activities := profile.Goal.LifeActivities()
	if len(activities) == 0 {
		return LifeActivity{}, errors.New("life activity pool unavailable")
	}
	pick, err := rand.Int(rand.Reader, big.NewInt(int64(len(activities))))
	if err != nil {
		return LifeActivity{}, err
	}
	next := LifeActivity{Kind: activities[pick.Int64()], StartedAt: now, Until: now.Add(profile.Goal.LifeDecisionInterval())}
	m.stateMu.Lock()
	m.state.Activity = next
	m.stateMu.Unlock()
	if err := s.persist(m, generation); err != nil {
		return LifeActivity{}, err
	}
	return next, nil
}

func appendActivityPrompt(prompt string, activity LifeActivity) string {
	if activity.Kind == "" {
		return prompt
	}
	raw, _ := json.Marshal(activity)
	prompt += "\nRuntime-selected life activity (persisted across turns and restarts):\n" + string(raw)
	if preset, ok := airuntime.LookupLifeActivityPreset(activity.Kind); ok {
		description, _ := json.Marshal(preset)
		prompt += "\nActivity instructions and completion criteria:\n" + string(description)
	}
	return prompt + "\nContinue existing unfinished game tasks before starting another. When no task is pending, pursue this selected activity using installed tools and your personality. idle/rest are intentional activities: observe if needed, then wait until the activity deadline; no movement or public message is required. All activities require current game prerequisites and available capabilities; if infeasible, rest and explain the reason. When a one-shot activity is already complete within this interval, record its outcome and wait; do not repeat a greeting, mail or game operation merely because another event woke you. Completing an activity or a Codex turn does not end your life goal. Reconsider at the next heartbeat; do not start a duplicate task."
}

func (a LifeActivity) Label() string {
	if preset, ok := airuntime.LookupLifeActivityPreset(a.Kind); ok {
		return preset.Label
	}
	return ""
}
