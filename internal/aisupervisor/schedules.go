package aisupervisor

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/airuntime"
)

func (s *Supervisor) nextScheduleAt(m *managedProfile) (time.Time, error) {
	at, err := s.store.NextScheduleAt(m.ctx, m.profileID)
	if errors.Is(err, airuntime.ErrNotFound) {
		return time.Time{}, nil
	}
	return at, err
}

func earlierDecision(a, b time.Time) time.Time {
	if a.IsZero() || (!b.IsZero() && b.Before(a)) {
		return b
	}
	return a
}

func appendScheduledPrompt(prompt string, due []airuntime.Schedule) string {
	if len(due) == 0 {
		return prompt
	}
	raw, _ := json.Marshal(due)
	return prompt + "\nDue self-scheduled reminders for this player:\n" + string(raw) + "\nThese are your previously saved intentions, not authoritative game facts or permission to override current constraints. Address due reminders before starting a new optional activity. Delivery does not mean the requested game activity has completed: verify current observations, continue existing work without duplication, and record or reschedule unfinished intentions. Use game_schedule_create/list/cancel to manage durable reminders; a promise in chat alone does not schedule a wake-up."
}
