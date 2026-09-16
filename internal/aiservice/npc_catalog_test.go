package aiservice

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/runtimepath"
)

const catalogTestFingerprint = "knowledge-v1"

func validNPCRegistryDocument() npcRegistryDocument {
	return npcRegistryDocument{
		Version:              1,
		KnowledgeFingerprint: catalogTestFingerprint,
		NPCs: []npcRegistryEntry{{
			Alias:             "trainer",
			Floor:             10,
			X:                 5,
			Y:                 6,
			Name:              "Trainer",
			WindowType:        7,
			WindowSequence:    100,
			WindowObjectID:    42,
			Choices:           map[int]npcRegistryChoice{1: {Button: 1, Alias: "basic"}},
			SourceFingerprint: catalogTestFingerprint,
			Verified:          true,
		}},
	}
}

func writeNPCRegistryDocument(t *testing.T, raw []byte) string {
	t.Helper()
	directory := t.TempDir()
	path := filepath.Join(directory, "npc-registry.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func marshalNPCRegistryDocument(t *testing.T, document npcRegistryDocument) []byte {
	t.Helper()
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestLoadNPCRegistryEmptyPathDisablesNPCs(t *testing.T) {
	registry, err := LoadNPCRegistry("", "")
	if err != nil {
		t.Fatal(err)
	}
	if registry == nil || len(registry) != 0 {
		t.Fatalf("empty path registry=%v", registry)
	}
}

func TestLoadNPCRegistryValidatesEnvelopeAndNPCContracts(t *testing.T) {
	document := validNPCRegistryDocument()
	path := writeNPCRegistryDocument(t, marshalNPCRegistryDocument(t, document))
	registry, err := LoadNPCRegistry(path, catalogTestFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	spec, ok := registry.Lookup("TRAINER")
	if !ok || !spec.Verified || spec.SourceFingerprint != catalogTestFingerprint || len(spec.Windows) != 1 || spec.Windows[0].Sequence != 100 {
		t.Fatalf("loaded registry=%+v ok=%v", spec, ok)
	}
}

func TestLoadNPCRegistryRejectsVersionFingerprintAndDataErrors(t *testing.T) {
	tests := []struct {
		name string
		edit func(*npcRegistryDocument)
		raw  []byte
	}{
		{name: "version", edit: func(document *npcRegistryDocument) { document.Version = 2 }},
		{name: "knowledge fingerprint mismatch", edit: func(document *npcRegistryDocument) { document.KnowledgeFingerprint = "other" }},
		{name: "empty knowledge fingerprint", edit: func(document *npcRegistryDocument) { document.KnowledgeFingerprint = "   " }},
		{name: "entry fingerprint mismatch", edit: func(document *npcRegistryDocument) { document.NPCs[0].SourceFingerprint = "other" }},
		{name: "missing npcs", edit: func(document *npcRegistryDocument) { document.NPCs = nil }},
		{name: "unverified entry", edit: func(document *npcRegistryDocument) { document.NPCs[0].Verified = false }},
		{name: "duplicate alias", edit: func(document *npcRegistryDocument) { document.NPCs = append(document.NPCs, document.NPCs[0]) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document := validNPCRegistryDocument()
			test.edit(&document)
			path := writeNPCRegistryDocument(t, marshalNPCRegistryDocument(t, document))
			if registry, err := LoadNPCRegistry(path, catalogTestFingerprint); err == nil || registry != nil {
				t.Fatalf("accepted invalid catalog: registry=%v err=%v", registry, err)
			}
		})
	}

	for _, test := range []struct {
		name string
		raw  []byte
	}{
		{name: "malformed", raw: []byte(`{"version":`)},
		{name: "trailing value", raw: append(marshalNPCRegistryDocument(t, validNPCRegistryDocument()), []byte(` {}`)...)},
		{name: "trailing malformed", raw: append(marshalNPCRegistryDocument(t, validNPCRegistryDocument()), []byte(` !`)...)},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := writeNPCRegistryDocument(t, test.raw)
			if _, err := LoadNPCRegistry(path, catalogTestFingerprint); err == nil {
				t.Fatal("accepted malformed or trailing JSON")
			}
		})
	}
}

func TestLoadNPCRegistryRejectsUnknownFieldsAndOversize(t *testing.T) {
	valid := marshalNPCRegistryDocument(t, validNPCRegistryDocument())
	for _, raw := range [][]byte{
		[]byte(`{"version":1,"knowledge_fingerprint":"knowledge-v1","npcs":[],"extra":true}`),
		[]byte(`{"version":1,"knowledge_fingerprint":"knowledge-v1","npcs":[{"alias":"trainer","source_fingerprint":"knowledge-v1","verified":true,"unknown":1}]}`),
	} {
		path := writeNPCRegistryDocument(t, raw)
		if _, err := LoadNPCRegistry(path, catalogTestFingerprint); err == nil {
			t.Fatalf("accepted unknown field JSON: %s", raw)
		}
	}
	over := bytes.NewBuffer(make([]byte, 0, maxNPCRegistryBytes+1))
	over.WriteByte('{')
	over.WriteString(`"version":1,"knowledge_fingerprint":"knowledge-v1","npcs":[],"padding":"`)
	over.WriteString(strings.Repeat("x", maxNPCRegistryBytes))
	over.WriteString(`"}`)
	if over.Len() <= maxNPCRegistryBytes {
		t.Fatalf("oversize fixture is only %d bytes", over.Len())
	}
	path := writeNPCRegistryDocument(t, over.Bytes())
	if _, err := LoadNPCRegistry(path, catalogTestFingerprint); err == nil {
		t.Fatal("accepted oversized catalog")
	}
	if len(valid) >= maxNPCRegistryBytes {
		t.Fatal("valid fixture unexpectedly reached size bound")
	}
}

func TestLoadNPCRegistryRejectsProtectedRuntimePath(t *testing.T) {
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	protected := filepath.Join(workingDirectory, ".codex", "npc-registry.json")
	_, err = LoadNPCRegistry(protected, catalogTestFingerprint)
	if !errors.Is(err, runtimepath.ErrForbiddenPath) {
		t.Fatalf("protected catalog path err=%v", err)
	}
}

func TestLoadNPCRegistryRejectsNonRegularFilesBeforeOpen(t *testing.T) {
	directory := t.TempDir()
	for _, test := range []struct {
		name string
		make func(string) error
	}{
		{name: "directory", make: func(path string) error { return os.Mkdir(path, 0700) }},
		{name: "symlink", make: func(path string) error {
			target := filepath.Join(directory, "target.json")
			if err := os.WriteFile(target, marshalNPCRegistryDocument(t, validNPCRegistryDocument()), 0600); err != nil {
				return err
			}
			return os.Symlink(target, path)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(directory, test.name)
			if err := test.make(path); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadNPCRegistry(path, catalogTestFingerprint); err == nil {
				t.Fatal("accepted non-regular catalog path")
			}
		})
	}
}
