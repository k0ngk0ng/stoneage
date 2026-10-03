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

	"github.com/k0ngk0ng/stoneage/internal/gamecatalog"
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
	return loadStockItems(path, expectedFingerprint, npcs, healingItems, "")
}

// LoadStockItemsForData additionally supports v2 reviewed quest materials.
// Materials are bound to the actual item table and never become healing items.
func LoadStockItemsForData(path, expectedFingerprint string, npcs NPCRegistry, healingItems map[string]HealingItemContract, itemsetPath string) (map[string]StockContract, error) {
	return loadStockItems(path, expectedFingerprint, npcs, healingItems, itemsetPath)
}

func loadStockItems(path, expectedFingerprint string, npcs NPCRegistry, healingItems map[string]HealingItemContract, itemsetPath string) (map[string]StockContract, error) {
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
	return decodeStockItemsForData(data, expectedFingerprint, npcs, healingItems, itemsetPath)
}

type stockMaterial struct {
	Alias      string `json:"alias"`
	TemplateID int32  `json:"template_id"`
	Verified   bool   `json:"verified"`
}

type stockCatalogDocument struct {
	ItemsetSHA256        string              `json:"itemset_sha256,omitempty"`
	Materials            []stockMaterial     `json:"materials,omitempty"`
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
	return decodeStockItemsForData(data, expectedFingerprint, npcs, healingItems, "")
}

func decodeStockItemsForData(data []byte, expectedFingerprint string, npcs NPCRegistry, healingItems map[string]HealingItemContract, itemsetPath string) (map[string]StockContract, error) {
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
	if document.Version != 1 && document.Version != 2 {
		return nil, fmt.Errorf("unsupported stock catalog version %d", document.Version)
	}
	if strings.TrimSpace(document.KnowledgeFingerprint) == "" || document.KnowledgeFingerprint != expectedFingerprint {
		return nil, errors.New("stock catalog knowledge fingerprint mismatch")
	}
	if document.Offers == nil {
		return nil, errors.New("stock catalog offers is required")
	}

	materials := map[string]int32{}
	if document.Version == 1 && (document.Materials != nil || document.ItemsetSHA256 != "") {
		return nil, errors.New("stock materials require catalog version 2")
	}
	if document.Version == 2 {
		if strings.TrimSpace(itemsetPath) == "" {
			return nil, errors.New("stock catalog version 2 requires the effective item table")
		}
		digest, err := hex.DecodeString(document.ItemsetSHA256)
		if err != nil || len(digest) != sha256.Size {
			return nil, errors.New("stock catalog itemset_sha256 is required")
		}
		raw, err := readStockItemTable(itemsetPath)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(raw)
		if !bytes.Equal(sum[:], digest) {
			return nil, errors.New("stock catalog itemset fingerprint mismatch")
		}
		items, err := gamecatalog.ParseItems(bytes.NewReader(raw))
		if err != nil {
			return nil, fmt.Errorf("parse stock item table: %w", err)
		}
		present := map[int32]int{}
		for _, item := range items {
			id := int32(item.ID)
			if item.ID > 0 && int64(id) == int64(item.ID) {
				present[id]++
			}
		}
		for _, entry := range document.Materials {
			alias := normalizeAlias(entry.Alias)
			if alias == "" || materials[alias] != 0 || !entry.Verified || present[entry.TemplateID] != 1 {
				return nil, errors.New("stock material requires unique alias and verified existing template")
			}
			for existing := range healingItems {
				if normalizeAlias(existing) == alias {
					return nil, fmt.Errorf("stock material overlaps healing alias %q", alias)
				}
			}
			materials[alias] = entry.TemplateID
		}
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
		templateID, material := materials[itemAlias]
		if !material {
			item, ok := lookupStockHealingItem(healingItems, itemAlias)
			if !ok {
				return nil, fmt.Errorf("stock catalog offer %d references unknown item %q", index, itemAlias)
			}
			if !item.Verified || item.SourceFingerprint != document.KnowledgeFingerprint || strings.TrimSpace(item.ItemsetSHA256) == "" {
				return nil, fmt.Errorf("stock catalog offer %d references an unverified healing item %q", index, itemAlias)
			}
			if document.Version == 2 && !strings.EqualFold(item.ItemsetSHA256, document.ItemsetSHA256) {
				return nil, errors.New("stock and healing item table fingerprints differ")
			}
			templateID = item.TemplateID
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
			TemplateID: templateID,
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

func readStockItemTable(path string) ([]byte, error) {
	guard, err := runtimepath.NewGuard()
	if err != nil {
		return nil, err
	}
	if err := guard.Check(path); err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	const limit = 64 << 20
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, errors.New("stock item table must be a bounded regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(info, opened) {
		return nil, errors.New("stock item table changed while opening")
	}
	if err := guard.Check(path); err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > limit {
		return nil, errors.New("stock item table exceeds size limit")
	}
	return raw, nil
}
