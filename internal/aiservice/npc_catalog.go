package aiservice

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/k0ngk0ng/stoneage/internal/runtimepath"
)

const maxNPCRegistryBytes = 1 << 20

// LoadNPCRegistry loads a server-owned, reviewed NPC contract. An empty path
// intentionally disables NPC automation and returns an empty registry. A
// non-empty path must contain the versioned JSON envelope below; its
// knowledge fingerprint and every per-NPC source fingerprint are checked
// before NewNPCRegistry applies the runtime verification rules.
//
// The path is checked against runtimepath before it is opened. This prevents a
// service configuration from accidentally reading the operator's ~/.codex or
// a project .codex path. The loader never reads the current Codex config.
func LoadNPCRegistry(path, expectedFingerprint string) (NPCRegistry, error) {
	if strings.TrimSpace(path) == "" {
		return NPCRegistry{}, nil
	}
	if strings.TrimSpace(expectedFingerprint) == "" {
		return nil, errors.New("NPC catalog expected fingerprint is required")
	}

	guard, err := runtimepath.NewGuard()
	if err != nil {
		return nil, fmt.Errorf("initialize NPC catalog path guard: %w", err)
	}
	if err := guard.Check(path); err != nil {
		return nil, err
	}
	// Never open a FIFO, device, directory, or symlink. In particular, opening
	// a FIFO for reading can block the service before any context or size check
	// runs; this catalog format has no reason to follow links.
	lstat, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("stat NPC catalog: %w", err)
	}
	if lstat.Mode()&os.ModeSymlink != 0 || !lstat.Mode().IsRegular() {
		return nil, errors.New("NPC catalog must be a regular file")
	}
	if lstat.Size() > maxNPCRegistryBytes {
		return nil, fmt.Errorf("NPC catalog exceeds %d bytes", maxNPCRegistryBytes)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open NPC catalog: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat NPC catalog: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("NPC catalog must be a regular file")
	}
	if info.Size() > maxNPCRegistryBytes {
		return nil, fmt.Errorf("NPC catalog exceeds %d bytes", maxNPCRegistryBytes)
	}
	// Recheck the path after opening. Guard.Check resolves existing symlink
	// components, so this catches a path replacement before the bytes are
	// consumed while keeping the guard independent of file contents.
	if err := guard.Check(path); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(file, maxNPCRegistryBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read NPC catalog: %w", err)
	}
	if len(data) > maxNPCRegistryBytes {
		return nil, fmt.Errorf("NPC catalog exceeds %d bytes", maxNPCRegistryBytes)
	}
	return decodeNPCRegistry(data, expectedFingerprint)
}

type npcRegistryDocument struct {
	Version              int                `json:"version"`
	KnowledgeFingerprint string             `json:"knowledge_fingerprint"`
	NPCs                 []npcRegistryEntry `json:"npcs"`
}

// The catalog file uses snake_case JSON independently of the internal Go
// contract types. Keeping these DTOs explicit lets DisallowUnknownFields
// reject an unreviewed field without adding serialization tags or a file
// format dependency to NPCSkill.
type npcRegistryEntry struct {
	Alias                 string                    `json:"alias"`
	Floor                 int                       `json:"floor"`
	X                     int                       `json:"x"`
	Y                     int                       `json:"y"`
	Name                  string                    `json:"name"`
	Template              string                    `json:"template"`
	ActorID               int                       `json:"actor_id"`
	ActorIDKnown          bool                      `json:"actor_id_known"`
	TalkRange             int                       `json:"talk_range"`
	WindowType            int                       `json:"window_type"`
	WindowSequence        int                       `json:"window_sequence"`
	WindowObjectID        int                       `json:"window_object_id"`
	Choices               map[int]npcRegistryChoice `json:"choices"`
	Windows               []npcRegistryWindow       `json:"windows"`
	WindowObjectFromActor bool                      `json:"window_object_from_actor"`
	SourceFingerprint     string                    `json:"source_fingerprint"`
	Verified              bool                      `json:"verified"`
	Healer                *HealerRates              `json:"healer,omitempty"`
}

type npcRegistryWindow struct {
	Type                  int                       `json:"type"`
	Sequence              int                       `json:"sequence"`
	ObjectID              int                       `json:"object_id"`
	Choices               map[int]npcRegistryChoice `json:"choices"`
	WindowObjectFromActor bool                      `json:"window_object_from_actor"`
}

type npcRegistryChoice struct {
	Button      int    `json:"button"`
	MaximumCost int64  `json:"maximum_cost"`
	Price       int64  `json:"price"`
	Quote       int64  `json:"quote"`
	Alias       string `json:"alias"`
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Data        string `json:"data"`
}

func decodeNPCRegistry(data []byte, expectedFingerprint string) (NPCRegistry, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var document npcRegistryDocument
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("decode NPC catalog: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("decode NPC catalog: trailing JSON values")
		}
		return nil, fmt.Errorf("decode NPC catalog: trailing JSON: %w", err)
	}
	if document.Version != 1 {
		return nil, fmt.Errorf("unsupported NPC catalog version %d", document.Version)
	}
	if strings.TrimSpace(document.KnowledgeFingerprint) == "" || document.KnowledgeFingerprint != expectedFingerprint {
		return nil, errors.New("NPC catalog knowledge fingerprint mismatch")
	}
	if document.NPCs == nil {
		return nil, errors.New("NPC catalog npcs is required")
	}

	specs := make([]NPCSpec, 0, len(document.NPCs))
	for index, entry := range document.NPCs {
		if entry.SourceFingerprint != document.KnowledgeFingerprint {
			return nil, fmt.Errorf("NPC catalog entry %d source fingerprint mismatch", index)
		}
		specs = append(specs, entry.toNPCSpec())
	}
	registry, err := NewNPCRegistry(specs)
	if err != nil {
		return nil, fmt.Errorf("validate NPC catalog: %w", err)
	}
	return registry, nil
}

func (entry npcRegistryEntry) toNPCSpec() NPCSpec {
	choices := make(map[int]NPCChoice, len(entry.Choices))
	for id, choice := range entry.Choices {
		choices[id] = choice.toNPCChoice()
	}
	windows := make([]NPCWindowSpec, 0, len(entry.Windows))
	for _, window := range entry.Windows {
		windowChoices := make(map[int]NPCChoice, len(window.Choices))
		for id, choice := range window.Choices {
			windowChoices[id] = choice.toNPCChoice()
		}
		windows = append(windows, NPCWindowSpec{
			Type:                  window.Type,
			Sequence:              window.Sequence,
			ObjectID:              window.ObjectID,
			Choices:               windowChoices,
			WindowObjectFromActor: window.WindowObjectFromActor,
		})
	}
	return NPCSpec{
		Alias:                 entry.Alias,
		Floor:                 entry.Floor,
		X:                     entry.X,
		Y:                     entry.Y,
		Name:                  entry.Name,
		Template:              entry.Template,
		ActorID:               entry.ActorID,
		ActorIDKnown:          entry.ActorIDKnown,
		TalkRange:             entry.TalkRange,
		WindowType:            entry.WindowType,
		WindowSequence:        entry.WindowSequence,
		WindowObjectID:        entry.WindowObjectID,
		Choices:               choices,
		Windows:               windows,
		WindowObjectFromActor: entry.WindowObjectFromActor,
		SourceFingerprint:     entry.SourceFingerprint,
		Verified:              entry.Verified,
		Healer:                entry.Healer,
	}
}

func (choice npcRegistryChoice) toNPCChoice() NPCChoice {
	return NPCChoice{
		Button: choice.Button, MaximumCost: choice.MaximumCost, Price: choice.Price,
		Quote: choice.Quote, Alias: choice.Alias, ID: choice.ID, Name: choice.Name,
		Description: choice.Description, Data: choice.Data,
	}
}
