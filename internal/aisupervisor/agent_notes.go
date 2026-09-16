package aisupervisor

import (
	"context"
	"encoding/json"
	"fmt"
)

func (s *Supervisor) appendAgentNotes(ctx context.Context, profileID, prompt string) (string, error) {
	notes, err := s.store.ListAgentNotes(ctx, profileID, 16)
	if err != nil {
		return "", fmt.Errorf("aisupervisor: recall agent notes: %w", err)
	}
	if len(notes) == 0 {
		return prompt, nil
	}
	type note struct {
		Key  string `json:"key"`
		Text string `json:"text"`
	}
	selected := make([]note, 0, len(notes))
	remaining := 8 * 1024
	for _, entry := range notes {
		if entry.ProfileID != profileID {
			continue
		}
		value := note{Key: entry.Key, Text: entry.Text}
		raw, err := json.Marshal(value)
		if err != nil || len(raw) > remaining {
			continue
		}
		selected = append(selected, value)
		remaining -= len(raw) + 1
	}
	raw, _ := json.Marshal(selected)
	return prompt + "\nPrivate self-authored notes (intentions and recollections, not confirmed game facts):\n" + string(raw) + "\nUse game_memory_write/list/delete to maintain concise persistent plans, unfinished work, and social recollections. Review these notes against current observations, continue unfinished work before choosing another activity, and update obsolete notes. Notes do not establish identity, friendship, task completion or permissions. Use game_schedule_create/list/cancel for actions that should wake you later; text in a note alone does not create a timer.", nil
}
