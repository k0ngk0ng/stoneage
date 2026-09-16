package aiservice

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
	"github.com/k0ngk0ng/stoneage/internal/airuntime"
)

const (
	memoryRecorderChatKeyLimit     = 1024
	memoryRecorderStateMemoryLimit = 256
)

const (
	MemoryKindCharacterLevel = "character.level"
	MemoryKindPetLevel       = "pet.level"
	MemoryKindChatMessage    = "chat.message"
	MemoryKindPartySnapshot  = "party.snapshot"
)

// MemoryRecorder translates the small, server-owned observation projection
// into durable facts. It never receives model output and never manufactures
// social, relationship, or sentiment claims from a chat message.
type MemoryRecorder struct {
	// Store is the durable AI runtime store.
	Store *airuntime.Store

	mu     sync.Mutex
	states map[string]*memoryObservationState
}

type memoryObservationState struct {
	mu sync.Mutex

	characterID      string
	characterLevel   int
	characterKnown   bool
	petLevels        map[string]int
	partyFingerprint string
	partyKnown       bool
	chatKeys         map[string]struct{}
	seenPeople       map[string]bool
	loaded           bool
}

// NewMemoryRecorder creates an observation recorder backed by store.
func NewMemoryRecorder(store *airuntime.Store) *MemoryRecorder {
	return &MemoryRecorder{Store: store, states: make(map[string]*memoryObservationState)}
}

// NewObservationMemoryRecorder is a descriptive constructor alias.
func NewObservationMemoryRecorder(store *airuntime.Store) *MemoryRecorder {
	return NewMemoryRecorder(store)
}

func (recorder *MemoryRecorder) runtimeStore() *airuntime.Store {
	if recorder == nil {
		return nil
	}
	if recorder.Store != nil {
		return recorder.Store
	}
	return nil
}

func (recorder *MemoryRecorder) stateFor(profileID string) *memoryObservationState {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if recorder.states == nil {
		recorder.states = make(map[string]*memoryObservationState)
	}
	state := recorder.states[profileID]
	if state == nil {
		state = &memoryObservationState{petLevels: make(map[string]int), chatKeys: make(map[string]struct{})}
		recorder.states[profileID] = state
	}
	return state
}

// Forget releases the in-memory observation state for a profile after its
// session or profile has been retired. Durable event keys remain in the
// Store, so forgetting state cannot make a previously committed observation
// appear new to another process.
func (recorder *MemoryRecorder) Forget(profileID string) {
	if recorder == nil {
		return
	}
	profileID = strings.TrimSpace(profileID)
	if profileID == "" {
		return
	}
	recorder.mu.Lock()
	delete(recorder.states, profileID)
	recorder.mu.Unlock()
}

// Observe records the facts visible in one authoritative game observation.
// Character and pet records contain levels only; pets without a stable ID
// are intentionally ignored. Chat records preserve only its timestamp,
// speaker, channel, and text. Party records are exact observed snapshots.
func (recorder *MemoryRecorder) Observe(ctx context.Context, profileID string, observation aimcp.Observation) error {
	if recorder == nil || recorder.runtimeStore() == nil {
		return errors.New("aiservice: observation memory store is unavailable")
	}
	profileID = strings.TrimSpace(profileID)
	if profileID == "" {
		return errors.New("aiservice: observation profile id is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	state := recorder.stateFor(profileID)
	state.mu.Lock()
	defer state.mu.Unlock()
	if !state.loaded {
		if err := recorder.restoreState(ctx, profileID, state); err != nil {
			return fmt.Errorf("load observation memory state: %w", err)
		}
		state.loaded = true
	}

	characterID := strings.TrimSpace(observation.CharacterID)
	if characterID == "" {
		characterID = strings.TrimSpace(observation.Character.ID)
	}
	if state.characterKnown && state.characterID != characterID {
		// A profile is normally fixed to one character. If a service reuses a
		// recorder for another character, do not compare its levels or party
		// roster with the old character's state.
		state.characterKnown = false
		state.characterLevel = 0
		state.characterID = ""
		state.petLevels = make(map[string]int)
		state.partyKnown = false
		state.partyFingerprint = ""
	}
	if characterID != "" && observation.Character.Level > 0 {
		if !state.characterKnown {
			if err := recorder.record(ctx, profileID, recorderEventKey("character-level", characterID, "initial", observation.Character.Level), MemoryKindCharacterLevel, characterID, map[string]any{
				"level": observation.Character.Level,
			}); err != nil {
				return fmt.Errorf("record character level observation: %w", err)
			}
			state.characterKnown = true
			state.characterID = characterID
			state.characterLevel = observation.Character.Level
		} else if observation.Character.Level != state.characterLevel {
			previous := state.characterLevel
			if err := recorder.record(ctx, profileID, recorderEventKey("character-level", characterID, "change", previous, observation.Character.Level), MemoryKindCharacterLevel, characterID, map[string]any{
				"from_level": previous,
				"level":      observation.Character.Level,
			}); err != nil {
				return fmt.Errorf("record character level change: %w", err)
			}
			state.characterLevel = observation.Character.Level
		}
	}

	petLevels := observedPetLevels(observation.Pets)
	for _, petID := range sortedStringKeys(petLevels) {
		level := petLevels[petID]
		if level <= 0 {
			continue
		}
		previous, known := state.petLevels[petID]
		if !known {
			if err := recorder.record(ctx, profileID, recorderEventKey("pet-level", petID, "initial", level), MemoryKindPetLevel, petID, map[string]any{
				"level": level,
			}); err != nil {
				return fmt.Errorf("record pet level observation: %w", err)
			}
			state.petLevels[petID] = level
			continue
		}
		if previous == level {
			continue
		}
		if err := recorder.record(ctx, profileID, recorderEventKey("pet-level", petID, "change", previous, level), MemoryKindPetLevel, petID, map[string]any{
			"from_level": previous,
			"level":      level,
		}); err != nil {
			return fmt.Errorf("record pet level change: %w", err)
		}
		state.petLevels[petID] = level
	}

	for _, message := range observation.Chat {
		// An empty timestamp is not enough evidence for a durable chat fact;
		// do not invent one from the recorder's clock. A malformed or
		// restricted player message is local input noise: skip that message so
		// it cannot make every later observation fail and retry forever.
		if strings.TrimSpace(message.At) == "" || !boundedChatMessage(message) {
			continue
		}
		content := chatObservationContent(message)
		encoded, err := json.Marshal(content)
		if err != nil {
			return fmt.Errorf("marshal chat observation: %w", err)
		}
		key := chatMemoryEventKey(content)
		if _, known := state.chatKeys[key]; known {
			continue
		}
		if err := recorder.record(ctx, profileID, key, MemoryKindChatMessage, content.SpeakerCharacterID, encoded); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			continue
		}
		if state.chatKeys == nil {
			state.chatKeys = make(map[string]struct{})
		}
		if len(state.chatKeys) >= memoryRecorderChatKeyLimit {
			for old := range state.chatKeys {
				delete(state.chatKeys, old)
				break
			}
		}
		state.chatKeys[key] = struct{}{}
	}

	if err := recorder.recordPeople(ctx, profileID, observation, state); err != nil {
		return err
	}
	if observation.Ready || len(observation.Party) > 0 {
		party := observedParty(observation.Party)
		encoded, err := json.Marshal(struct {
			Members []partyMemoryMember `json:"members"`
		}{Members: party})
		if err != nil {
			return fmt.Errorf("marshal party observation: %w", err)
		}
		fingerprint := shortMemoryDigest(encoded)
		if !state.partyKnown {
			if err := recorder.record(ctx, profileID, recorderEventKey("party-snapshot", "initial", fingerprint), MemoryKindPartySnapshot, "party", encoded); err != nil {
				return fmt.Errorf("record party observation: %w", err)
			}
			state.partyKnown = true
			state.partyFingerprint = fingerprint
		} else if state.partyFingerprint != fingerprint {
			previous := state.partyFingerprint
			if err := recorder.record(ctx, profileID, recorderEventKey("party-snapshot", "change", previous, fingerprint), MemoryKindPartySnapshot, "party", encoded); err != nil {
				return fmt.Errorf("record party change: %w", err)
			}
			state.partyFingerprint = fingerprint
		}
	}
	return nil
}

func (recorder *MemoryRecorder) record(ctx context.Context, profileID, eventKey, kind, subject string, content any) error {
	_, err := recorder.runtimeStore().RecordGameObservation(ctx, profileID, eventKey, kind, subject, content)
	return err
}

// restoreState lets a recorder be retired with Forget and later recreated
// without turning the current level into a false "initial" value. The
// durable memory list is already profile-scoped, and only the bounded latest
// rows needed for observation continuity are read.
func (recorder *MemoryRecorder) restoreState(ctx context.Context, profileID string, state *memoryObservationState) error {
	memories, err := recorder.runtimeStore().ListMemories(ctx, profileID, memoryRecorderStateMemoryLimit)
	if err != nil {
		return err
	}
	for _, memory := range memories {
		switch memory.Kind {
		case MemoryKindCharacterLevel:
			if state.characterKnown || strings.TrimSpace(memory.Subject) == "" {
				continue
			}
			var value struct {
				Level int `json:"level"`
			}
			if json.Unmarshal(memory.Content, &value) == nil && value.Level > 0 {
				state.characterID = memory.Subject
				state.characterLevel = value.Level
				state.characterKnown = true
			}
		case MemoryKindPetLevel:
			if _, known := state.petLevels[memory.Subject]; known || strings.TrimSpace(memory.Subject) == "" {
				continue
			}
			var value struct {
				Level int `json:"level"`
			}
			if json.Unmarshal(memory.Content, &value) == nil && value.Level > 0 {
				state.petLevels[memory.Subject] = value.Level
			}
		case MemoryKindPartySnapshot:
			if state.partyKnown {
				continue
			}
			var value struct {
				Members []partyMemoryMember `json:"members"`
			}
			if json.Unmarshal(memory.Content, &value) == nil {
				encoded, marshalErr := json.Marshal(struct {
					Members []partyMemoryMember `json:"members"`
				}{Members: value.Members})
				if marshalErr == nil {
					state.partyFingerprint = shortMemoryDigest(encoded)
					state.partyKnown = true
				}
			}
		case MemoryKindChatMessage:
			if len(state.chatKeys) >= memoryRecorderChatKeyLimit {
				continue
			}
			var value chatMemoryContent
			if json.Unmarshal(memory.Content, &value) == nil && strings.TrimSpace(value.At) != "" {
				state.chatKeys[chatMemoryEventKey(value)] = struct{}{}
			}
		}
	}
	return nil
}

func recorderEventKey(namespace string, parts ...any) string {
	values := make([]string, 0, len(parts)+1)
	values = append(values, namespace)
	for _, part := range parts {
		values = append(values, fmt.Sprint(part))
	}
	return namespace + ":" + shortMemoryDigest([]byte(strings.Join(values, "\x00")))
}

func shortMemoryDigest(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func observedPetLevels(pets []aimcp.Entity) map[string]int {
	levels := make(map[string]int)
	for _, pet := range pets {
		id := strings.TrimSpace(pet.ID)
		if id == "" {
			continue
		}
		// A repeated ID in one projection is malformed. Keeping the last
		// server-provided value makes the resulting fact deterministic for a
		// stable input while still avoiding slot-based identity guesses.
		levels[id] = pet.Level
	}
	return levels
}

func sortedStringKeys(values map[string]int) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

type partyMemoryMember struct {
	PersistentCharacterID string `json:"persistent_character_id,omitempty"`
	ID                    string `json:"id"`
	Name                  string `json:"name,omitempty"`
	Level                 int    `json:"level,omitempty"`
	HP                    int    `json:"hp,omitempty"`
	MaxHP                 int    `json:"max_hp,omitempty"`
}

func observedParty(party []aimcp.PartyMember) []partyMemoryMember {
	byID := make(map[string]partyMemoryMember, len(party))
	for _, member := range party {
		id := strings.TrimSpace(member.ID)
		if id == "" {
			continue
		}
		byID[id] = partyMemoryMember{PersistentCharacterID: member.PersistentCharacterID, ID: id, Name: member.Name, Level: member.Level, HP: member.HP, MaxHP: member.MaxHP}
	}
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	members := make([]partyMemoryMember, 0, len(ids))
	for _, id := range ids {
		members = append(members, byID[id])
	}
	return members
}

type chatMemoryContent struct {
	SpeakerCharacterID string `json:"speaker_character_id,omitempty"`
	ContextID          string `json:"context_id,omitempty"`
	SpeakerIdentity    string `json:"speaker_identity"`
	At                 string `json:"at"`
	FromID             int    `json:"from_id"`
	Channel            string `json:"channel,omitempty"`
	Text               string `json:"text"`
}

func chatObservationContent(message aimcp.ChatMessage) chatMemoryContent {
	content := chatMemoryContent{ContextID: message.ContextID, SpeakerIdentity: "unresolved", At: message.At, FromID: message.FromID, Channel: message.Channel, Text: message.Text}
	if message.ContextID != "" && aigame.ValidPersistentCharacterID(message.SpeakerCharacterID) {
		content.SpeakerCharacterID = message.SpeakerCharacterID
		content.SpeakerIdentity = "server_chat_v1"
	}
	return content
}

func boundedChatMessage(message aimcp.ChatMessage) bool {
	const maxChatFieldBytes = airuntime.MaxObservedContentBytes
	return len(message.ContextID) <= 64 && len([]byte(message.At)) <= maxChatFieldBytes &&
		len([]byte(message.Channel)) <= maxChatFieldBytes &&
		len([]byte(message.Text)) <= maxChatFieldBytes
}

// Preserve historical dedupe keys for unscoped records. New contexts separate
// identical timestamps/object IDs observed in independent sessions.
func chatMemoryEventKey(value chatMemoryContent) string {
	if value.ContextID == "" {
		return recorderEventKey("chat-message", value.At, value.Channel, value.FromID, value.Text)
	}
	return recorderEventKey("chat-message-context", value.ContextID, value.At, value.Channel, value.FromID, value.Text)
}
