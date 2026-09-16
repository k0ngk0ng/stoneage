package aisupervisor

import (
	"encoding/json"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/airuntime"
)

// Validate the stored provenance, not merely the shape of a legacy Subject.
func attributedChatMemory(memory airuntime.Memory) bool {
	var content struct {
		SpeakerCharacterID string `json:"speaker_character_id"`
		SpeakerIdentity    string `json:"speaker_identity"`
		ContextID          string `json:"context_id"`
	}
	return json.Unmarshal(memory.Content, &content) == nil &&
		content.SpeakerIdentity == "server_chat_v1" && content.ContextID != "" && len(content.ContextID) <= 64 &&
		aigame.ValidPersistentCharacterID(content.SpeakerCharacterID) && memory.Subject == content.SpeakerCharacterID
}

// Only bounded recent server-observed speakers select historical recall. Chat
// text and display names never become lookup keys, including custom prompts.
func recentChatSubjects(snapshot Snapshot, now time.Time) []string {
	var observation struct {
		Connected bool   `json:"connected"`
		Ready     bool   `json:"ready"`
		Self      string `json:"persistent_character_id"`
		Chat      []struct {
			SpeakerCharacterID string `json:"speaker_character_id"`
			ContextID          string `json:"context_id"`
			At                 string `json:"at"`
		} `json:"chat"`
	}
	if !snapshot.GameReady || json.Unmarshal(snapshot.Context, &observation) != nil || !observation.Connected || !observation.Ready {
		return nil
	}
	var subjects []string
	seen := map[string]bool{observation.Self: true}
	for i := len(observation.Chat) - 1; i >= 0 && len(subjects) < 2; i-- {
		chat := observation.Chat[i]
		at, err := time.Parse(time.RFC3339Nano, chat.At)
		id := chat.SpeakerCharacterID
		if err != nil || at.After(now) || now.Sub(at) > 5*time.Minute || chat.ContextID == "" || len(chat.ContextID) > 64 || !aigame.ValidPersistentCharacterID(id) || seen[id] {
			continue
		}
		seen[id] = true
		subjects = append(subjects, id)
	}
	return subjects
}
