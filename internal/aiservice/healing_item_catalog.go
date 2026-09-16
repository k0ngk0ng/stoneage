package aiservice

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/k0ngk0ng/stoneage/internal/runtimepath"
)

const maxHealingItemCatalogBytes = 1 << 20

// LoadHealingItems loads the server-owned, reviewed portable-healing item
// contracts. An empty path intentionally disables item healing and returns an
// empty map. A non-empty path must contain the versioned JSON envelope below;
// the envelope fingerprint is checked before any contract is installed.
//
// The path is checked against runtimepath before it is opened. This keeps a
// service catalog from reading an operator's ~/.codex or a project .codex
// path. The loader only inspects path metadata and never reads Codex config.
func LoadHealingItems(path, expectedFingerprint string) (map[string]HealingItemContract, error) {
	if strings.TrimSpace(path) == "" {
		return map[string]HealingItemContract{}, nil
	}
	if strings.TrimSpace(expectedFingerprint) == "" {
		return nil, errors.New("healing item catalog expected fingerprint is required")
	}

	guard, err := runtimepath.NewGuard()
	if err != nil {
		return nil, fmt.Errorf("initialize healing item catalog path guard: %w", err)
	}
	if err := guard.Check(path); err != nil {
		return nil, err
	}
	// Do not follow a symlink, directory, FIFO, device, or socket. Opening a
	// FIFO before this check could block the service indefinitely.
	lstat, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("stat healing item catalog: %w", err)
	}
	if lstat.Mode()&os.ModeSymlink != 0 || !lstat.Mode().IsRegular() {
		return nil, errors.New("healing item catalog must be a regular file")
	}
	if lstat.Size() > maxHealingItemCatalogBytes {
		return nil, fmt.Errorf("healing item catalog exceeds %d bytes", maxHealingItemCatalogBytes)
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open healing item catalog: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat healing item catalog: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("healing item catalog must be a regular file")
	}
	if info.Size() > maxHealingItemCatalogBytes {
		return nil, fmt.Errorf("healing item catalog exceeds %d bytes", maxHealingItemCatalogBytes)
	}
	// Recheck the canonical path after opening so a replaced path cannot make
	// the catalog escape the runtime path boundary between checks.
	if err := guard.Check(path); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(file, maxHealingItemCatalogBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read healing item catalog: %w", err)
	}
	if len(data) > maxHealingItemCatalogBytes {
		return nil, fmt.Errorf("healing item catalog exceeds %d bytes", maxHealingItemCatalogBytes)
	}
	return decodeHealingItems(data, expectedFingerprint)
}

type healingItemCatalogDocument struct {
	ItemsetSHA256        string                    `json:"itemset_sha256"`
	Version              int                       `json:"version"`
	KnowledgeFingerprint string                    `json:"knowledge_fingerprint"`
	Items                []healingItemCatalogEntry `json:"items"`
}

// Keep file DTOs separate from HealingItemContract so the JSON format remains
// explicit and DisallowUnknownFields can reject unreviewed additions.
type healingItemCatalogEntry struct {
	Alias      string `json:"alias"`
	TemplateID int32  `json:"template_id"`
	BaseHP     int32  `json:"base_hp"`
	Verified   bool   `json:"verified"`
}

func decodeHealingItems(data []byte, expectedFingerprint string) (map[string]HealingItemContract, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var document healingItemCatalogDocument
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("decode healing item catalog: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("decode healing item catalog: trailing JSON values")
		}
		return nil, fmt.Errorf("decode healing item catalog: trailing JSON: %w", err)
	}
	if document.Version != 1 {
		return nil, fmt.Errorf("unsupported healing item catalog version %d", document.Version)
	}
	if strings.TrimSpace(document.KnowledgeFingerprint) == "" || document.KnowledgeFingerprint != expectedFingerprint {
		return nil, errors.New("healing item catalog knowledge fingerprint mismatch")
	}
	digest, err := hex.DecodeString(document.ItemsetSHA256)
	if err != nil || len(digest) != sha256.Size {
		return nil, errors.New("healing item catalog itemset_sha256 is required")
	}
	if document.Items == nil {
		return nil, errors.New("healing item catalog items is required")
	}

	contracts := make(map[string]HealingItemContract, len(document.Items))
	for index, entry := range document.Items {
		alias := normalizeAlias(entry.Alias)
		if alias == "" {
			return nil, fmt.Errorf("healing item catalog entry %d alias is required", index)
		}
		if _, exists := contracts[alias]; exists {
			return nil, fmt.Errorf("healing item catalog duplicate alias %q", alias)
		}
		if entry.TemplateID <= 0 {
			return nil, fmt.Errorf("healing item catalog entry %d has invalid template_id", index)
		}
		if entry.BaseHP <= 0 {
			return nil, fmt.Errorf("healing item catalog entry %d has invalid base_hp", index)
		}
		if !entry.Verified {
			return nil, fmt.Errorf("healing item catalog entry %d is not verified", index)
		}
		contracts[alias] = HealingItemContract{
			ItemsetSHA256:     strings.ToLower(document.ItemsetSHA256),
			TemplateID:        entry.TemplateID,
			BaseHP:            entry.BaseHP,
			SourceFingerprint: document.KnowledgeFingerprint,
			Verified:          true,
		}
	}
	return contracts, nil
}

// LoadHealingItemsForData also binds contracts to the effective item table.
// The general knowledge fingerprint does not currently cover itemset.txt.
func LoadHealingItemsForData(path, expectedFingerprint, itemsetPath string) (map[string]HealingItemContract, error) {
	items, err := LoadHealingItems(path, expectedFingerprint)
	if err != nil || len(items) == 0 {
		return items, err
	}
	guard, err := runtimepath.NewGuard()
	if err != nil {
		return nil, err
	}
	if err := guard.Check(itemsetPath); err != nil {
		return nil, err
	}
	info, err := os.Lstat(itemsetPath)
	if err != nil {
		return nil, err
	}
	const maxItemsetBytes = 64 << 20
	if !info.Mode().IsRegular() || info.Size() > maxItemsetBytes {
		return nil, errors.New("healing item table must be a bounded regular file")
	}
	file, err := os.Open(itemsetPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(info, opened) {
		return nil, errors.New("healing item table changed while opening")
	}
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(file, maxItemsetBytes+1))
	if err != nil {
		return nil, err
	}
	if n > maxItemsetBytes {
		return nil, errors.New("healing item table exceeds size limit")
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	for _, item := range items {
		if item.ItemsetSHA256 != digest {
			return nil, errors.New("healing item catalog itemset fingerprint mismatch")
		}
	}
	return items, nil
}
