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

const maxStockCatalogBytes = 1 << 20

// LoadStockItems loads the server-owned, reviewed shop offers used by the
// item.stock skill. An empty path intentionally disables shop automation and
// returns an empty map. A non-empty path must contain the versioned JSON
// envelope below; its knowledge fingerprint is checked before any offer is
// installed.
//
// The item and NPC maps are supplied by the already loaded server-owned
// catalogs. This loader only composes references to those verified contracts;
// it does not allow a stock catalog to describe a new item or NPC.
func LoadStockItems(path, expectedFingerprint string, npcs NPCRegistry, healingItems map[string]HealingItemContract) (map[string]StockContract, error) {
	if strings.TrimSpace(path) == "" {
		return map[string]StockContract{}, nil
	}
	if strings.TrimSpace(expectedFingerprint) == "" {
		return nil, errors.New("stock catalog expected fingerprint is required")
	}

	guard, err := runtimepath.NewGuard()
	if err != nil {
		return nil, fmt.Errorf("initialize stock catalog path guard: %w", err)
	}
	if err := guard.Check(path); err != nil {
		return nil, err
	}
	// Do not open a symlink, directory, FIFO, device, or socket. In
	// particular, opening a FIFO before this check could block the service.
	lstat, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("stat stock catalog: %w", err)
	}
	if lstat.Mode()&os.ModeSymlink != 0 || !lstat.Mode().IsRegular() {
		return nil, errors.New("stock catalog must be a regular file")
	}
	if lstat.Size() > maxStockCatalogBytes {
		return nil, fmt.Errorf("stock catalog exceeds %d bytes", maxStockCatalogBytes)
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open stock catalog: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat stock catalog: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("stock catalog must be a regular file")
	}
	if info.Size() > maxStockCatalogBytes {
		return nil, fmt.Errorf("stock catalog exceeds %d bytes", maxStockCatalogBytes)
	}
	// Recheck the canonical path after opening so a replaced path cannot make
	// the catalog escape the runtime path boundary between checks.
	if err := guard.Check(path); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(file, maxStockCatalogBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read stock catalog: %w", err)
	}
	if len(data) > maxStockCatalogBytes {
		return nil, fmt.Errorf("stock catalog exceeds %d bytes", maxStockCatalogBytes)
	}
	return decodeStockItems(data, expectedFingerprint, npcs, healingItems)
}

type stockCatalogDocument struct {
	Version              int                 `json:"version"`
	KnowledgeFingerprint string              `json:"knowledge_fingerprint"`
	Offers               []stockCatalogOffer `json:"offers"`
}

// Keep file DTOs separate from StockContract so the JSON format remains
// explicit and DisallowUnknownFields can reject unreviewed additions.
type stockCatalogOffer struct {
	Name      string `json:"name,omitempty"`
	Alias     string `json:"alias"`
	Item      string `json:"item"`
	NPC       string `json:"npc"`
	ShopIndex int    `json:"shop_index"`
	UnitPrice int64  `json:"unit_price"`
	X         int    `json:"x"`
	Y         int    `json:"y"`
}

func decodeStockItems(data []byte, expectedFingerprint string, npcs NPCRegistry, healingItems map[string]HealingItemContract) (map[string]StockContract, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var document stockCatalogDocument
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("decode stock catalog: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("decode stock catalog: trailing JSON values")
		}
		return nil, fmt.Errorf("decode stock catalog: trailing JSON: %w", err)
	}
	if document.Version != 1 {
		return nil, fmt.Errorf("unsupported stock catalog version %d", document.Version)
	}
	if strings.TrimSpace(document.KnowledgeFingerprint) == "" || document.KnowledgeFingerprint != expectedFingerprint {
		return nil, errors.New("stock catalog knowledge fingerprint mismatch")
	}
	if document.Offers == nil {
		return nil, errors.New("stock catalog offers is required")
	}

	contracts := make(map[string]StockContract, len(document.Offers))
	for index, offer := range document.Offers {
		alias := normalizeAlias(offer.Alias)
		if alias == "" {
			return nil, fmt.Errorf("stock catalog offer %d alias is required", index)
		}
		if _, exists := contracts[alias]; exists {
			return nil, fmt.Errorf("stock catalog duplicate alias %q", alias)
		}

		itemAlias := normalizeAlias(offer.Item)
		if itemAlias == "" {
			return nil, fmt.Errorf("stock catalog offer %d item is required", index)
		}
		item, ok := lookupStockHealingItem(healingItems, itemAlias)
		if !ok {
			return nil, fmt.Errorf("stock catalog offer %d references unknown healing item %q", index, itemAlias)
		}
		if !item.Verified || item.SourceFingerprint != document.KnowledgeFingerprint || strings.TrimSpace(item.ItemsetSHA256) == "" {
			return nil, fmt.Errorf("stock catalog offer %d references an unverified healing item %q", index, itemAlias)
		}

		npcAlias := normalizeAlias(offer.NPC)
		if npcAlias == "" {
			return nil, fmt.Errorf("stock catalog offer %d npc is required", index)
		}
		npc, ok := npcs.Lookup(npcAlias)
		if !ok {
			return nil, fmt.Errorf("stock catalog offer %d references unknown NPC %q", index, npcAlias)
		}
		if !npc.Verified || npc.SourceFingerprint != document.KnowledgeFingerprint {
			return nil, fmt.Errorf("stock catalog offer %d references an unverified NPC %q", index, npcAlias)
		}

		contract := StockContract{
			Name:       strings.TrimSpace(offer.Name),
			NPC:        npc,
			TemplateID: item.TemplateID,
			ShopIndex:  offer.ShopIndex,
			UnitPrice:  offer.UnitPrice,
			X:          offer.X,
			Y:          offer.Y,
		}
		registry, err := contract.registry(1)
		if err != nil {
			return nil, fmt.Errorf("validate stock catalog offer %d: %w", index, err)
		}
		// registry(1) normalizes and deep-copies the NPC contract. Retaining
		// that copy keeps the returned map independent of caller-owned maps.
		contract.NPC, _ = registry.Lookup(npc.Alias)
		contracts[alias] = contract
	}
	return contracts, nil
}

func lookupStockHealingItem(items map[string]HealingItemContract, alias string) (HealingItemContract, bool) {
	key := normalizeAlias(alias)
	var found HealingItemContract
	foundCount := 0
	for candidate, item := range items {
		if normalizeAlias(candidate) != key {
			continue
		}
		found = item
		foundCount++
	}
	return found, foundCount == 1
}
