package aisupervisor

import (
	"encoding/json"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/airuntime"
)

func currentPersonSubjects(snapshot Snapshot) []string {
	var observation struct {
		Connected bool   `json:"connected"`
		Ready     bool   `json:"ready"`
		Self      string `json:"persistent_character_id"`
		Party     []struct {
			ID string `json:"persistent_character_id"`
		} `json:"party"`
		Actors []struct {
			ID string `json:"persistent_character_id"`
		} `json:"actors"`
	}
	if !snapshot.GameReady || json.Unmarshal(snapshot.Context, &observation) != nil || !observation.Connected || !observation.Ready {
		return nil
	}
	var subjects []string
	seen := map[string]bool{observation.Self: true}
	add := func(id string) {
		if len(subjects) < 2 && aigame.ValidPersistentCharacterID(id) && !seen[id] {
			subjects = append(subjects, id)
			seen[id] = true
		}
	}
	for _, p := range observation.Party {
		add(p.ID)
	}
	for _, p := range observation.Actors {
		add(p.ID)
	}
	return subjects
}

func attributedPersonMemory(memory airuntime.Memory) bool {
	var content struct {
		ID       string `json:"persistent_character_id"`
		Evidence string `json:"identity_evidence"`
		Role     string `json:"role"`
	}
	return json.Unmarshal(memory.Content, &content) == nil && content.Evidence == "server_person_v1" &&
		(content.Role == "visible" || content.Role == "party") && aigame.ValidPersistentCharacterID(content.ID) && content.ID == memory.Subject
}
