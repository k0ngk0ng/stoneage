package aimcp

import (
	"context"
	"strings"
)

// AgentNote is the credential-free MCP projection of one private,
// self-authored note. Profile identity is intentionally omitted: the Server
// binds every call to the profile selected by its trusted process owner.
type AgentNote struct {
	ID        int64  `json:"id,omitempty"`
	Key       string `json:"key"`
	Text      string `json:"text"`
	CreatedAt string `json:"created_at,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

// AgentMemoryNote is kept as a descriptive alias for adapters that call this
// capability "memory" while the durable store calls it an agent note.
type AgentMemoryNote = AgentNote

type AgentNoteWriteRequest struct {
	Key  string `json:"key"`
	Text string `json:"text"`
}

type AgentNoteListRequest struct {
	Limit int `json:"limit,omitempty"`
}

type AgentNoteDeleteRequest struct {
	Key string `json:"key"`
}

type AgentNoteList struct {
	Notes []AgentNote `json:"notes"`
}

type AgentNoteDeleteResult struct {
	Deleted bool   `json:"deleted"`
	Key     string `json:"key,omitempty"`
}

// AgentMemoryBackend is an optional extension to Backend. Its methods are
// called with the immutable server binding, while profile_id remains absent
// from all model-controlled request parameters.
type AgentMemoryBackend interface {
	WriteAgentNote(context.Context, Binding, AgentNoteWriteRequest) (AgentNote, error)
	ListAgentNotes(context.Context, Binding, AgentNoteListRequest) (AgentNoteList, error)
	DeleteAgentNote(context.Context, Binding, AgentNoteDeleteRequest) error
}

// AgentNotesBackend is an alias for callers that use the durable-store name.
type AgentNotesBackend = AgentMemoryBackend

const (
	MaxAgentNoteKeyBytes  = 128
	MaxAgentNoteTextBytes = 4096
	DefaultAgentNoteLimit = 50
	MaxAgentNoteLimit     = 100
)

func (request AgentNoteWriteRequest) Validate() error {
	if !validAgentNoteKey(request.Key) || !validAgentNoteText(request.Text) {
		return ErrInvalidParams
	}
	return nil
}

func (request AgentNoteListRequest) Validate() error {
	if request.Limit < 0 || request.Limit > MaxAgentNoteLimit {
		return ErrInvalidParams
	}
	return nil
}

func (request AgentNoteDeleteRequest) Validate() error {
	if !validAgentNoteKey(request.Key) {
		return ErrInvalidParams
	}
	return nil
}

func (note AgentNote) Validate() error {
	if note.ID < 0 || !validAgentNoteKey(note.Key) || !validAgentNoteText(note.Text) {
		return ErrInvalidParams
	}
	if len([]byte(note.CreatedAt)) > 64 || len([]byte(note.UpdatedAt)) > 64 || !validText(note.CreatedAt, 64) || !validText(note.UpdatedAt, 64) {
		return ErrInvalidParams
	}
	return nil
}

func (list AgentNoteList) Validate() error {
	if len(list.Notes) > MaxAgentNoteLimit {
		return ErrInvalidParams
	}
	for _, note := range list.Notes {
		if err := note.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func validAgentNoteKey(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && len([]byte(value)) <= MaxAgentNoteKeyBytes && validText(value, MaxAgentNoteKeyBytes)
}

func validAgentNoteText(value string) bool {
	return strings.TrimSpace(value) != "" && len([]byte(value)) <= MaxAgentNoteTextBytes && validDisplayText(value, MaxAgentNoteTextBytes)
}
