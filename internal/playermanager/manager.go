// Package playermanager coordinates authoritative online game operations and
// SAAC compare-and-swap writes for offline characters.
package playermanager

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash/fnv"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/gamecatalog"
	"github.com/k0ngk0ng/stoneage/internal/playerbridge"
	"github.com/k0ngk0ng/stoneage/internal/playerdata"
	"golang.org/x/text/encoding/simplifiedchinese"
)

type Archives interface {
	Read(context.Context, string, int) ([]byte, error)
	Write(context.Context, string, int, []byte, []byte) error
}
type Game interface {
	Call(context.Context, map[string]string) (map[string]string, error)
}

type Manager struct {
	Archives      Archives
	Game          Game
	Catalog       *gamecatalog.Catalog
	CatalogLoader func() (*gamecatalog.Catalog, error)
	mu            sync.Mutex
	catalogMu     sync.RWMutex
}

type currentState struct {
	snapshot playerdata.Snapshot
	document *playerdata.Document
	archive  []byte
	game     map[string]string
}

func (m *Manager) List(ctx context.Context, account string) ([]playerdata.Character, error) {
	result := []playerdata.Character{}
	for slot := 0; slot < 2; slot++ {
		state, err := m.current(ctx, account, slot)
		if errors.Is(err, playerdata.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		result = append(result, playerdata.Character{Slot: slot, Name: state.snapshot.Name, Online: state.snapshot.Online})
	}
	return result, nil
}

func (m *Manager) Get(ctx context.Context, account string, slot int) (playerdata.Snapshot, error) {
	state, err := m.current(ctx, account, slot)
	if err != nil {
		return playerdata.Snapshot{}, err
	}
	return state.snapshot, nil
}

func (m *Manager) current(ctx context.Context, account string, slot int) (currentState, error) {
	var state currentState
	if m.Archives == nil || m.Game == nil {
		return state, playerdata.ErrUnavailable
	}
	archive, err := m.Archives.Read(ctx, account, slot)
	if err != nil {
		return state, err
	}
	doc, err := playerdata.ParseSave(archive)
	if err != nil {
		return state, fmt.Errorf("无法解析角色存档: %w", err)
	}
	name, err := doc.Character.Text("name")
	if err != nil {
		return state, err
	}
	encoded, err := encodeText(name)
	if err != nil {
		return state, err
	}
	game, err := m.Game.Call(ctx, map[string]string{"action": "snapshot", "account": account, "character": encoded, "character_slot": strconv.Itoa(slot)})
	if err != nil {
		var bridgeErr *playerbridge.Error
		if !errors.As(err, &bridgeErr) || bridgeErr.Code != "target_not_online" {
			return state, err
		}
		state.snapshot, err = m.offlineSnapshot(ctx, archive)
		if err != nil {
			return state, err
		}
	} else {
		state.snapshot, err = onlineSnapshot(game, account, slot)
		if err != nil {
			return state, err
		}
		state.game = game
	}
	state.document, state.archive = doc, archive
	m.enrich(&state.snapshot)
	return state, nil
}

func (m *Manager) Apply(ctx context.Context, account string, slot int, mutation playerdata.Mutation) (playerdata.Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state, err := m.current(ctx, account, slot)
	if err != nil {
		return playerdata.Snapshot{}, err
	}
	if mutation.Revision == "" || mutation.Revision != state.snapshot.Revision {
		return playerdata.Snapshot{}, playerdata.ErrConflict
	}
	if err = validateMutation(mutation); err != nil {
		return playerdata.Snapshot{}, err
	}
	if state.snapshot.Online {
		values := map[string]string{
			"action": mutation.Action, "account": account, "character": state.game["character"], "character_slot": strconv.Itoa(slot),
			"expected_revision": state.game["revision"], "expected_sequence": state.game["sequence"],
			"location": mutation.Location, "slot": strconv.Itoa(mutation.Slot), "field": mutation.Field,
			"value": strconv.FormatInt(mutation.Value, 10), "quantity": strconv.Itoa(mutation.Quantity),
			"template_id": strconv.Itoa(mutation.TemplateID), "skill_slot": strconv.Itoa(mutation.SkillSlot), "skill_id": strconv.Itoa(mutation.SkillID),
		}
		if strings.HasPrefix(mutation.Action, "grant_") {
			delete(values, "slot")
		}
		if mutation.Action == "set_character" {
			delete(values, "slot")
			delete(values, "location")
		}
		if mutation.Name != "" {
			values["name"], err = encodeText(mutation.Name)
			if err != nil {
				return playerdata.Snapshot{}, err
			}
		}
		if mutation.Action == "grant_bundle" {
			values["bundle_count"] = strconv.Itoa(len(mutation.Bundle))
			for i, entry := range mutation.Bundle {
				prefix := "bundle_" + strconv.Itoa(i) + "_"
				values[prefix+"kind"] = entry.Kind
				values[prefix+"template_id"] = strconv.Itoa(entry.TemplateID)
				values[prefix+"quantity"] = strconv.Itoa(entry.Quantity)
				if entry.Name != "" {
					values[prefix+"name"], err = encodeText(entry.Name)
					if err != nil {
						return playerdata.Snapshot{}, err
					}
				}
			}
		}
		if values["location"] == "" {
			delete(values, "location")
		}
		response, err := m.Game.Call(ctx, values)
		if err != nil {
			return playerdata.Snapshot{}, err
		}
		if err = m.waitForSave(ctx, account, slot, response["save_data_hash"]); err != nil {
			return playerdata.Snapshot{}, err
		}
		return m.Get(ctx, account, slot)
	}
	if err = m.applyOffline(ctx, account, state.document, mutation); err != nil {
		return playerdata.Snapshot{}, err
	}
	replacement, err := state.document.Bytes()
	if err != nil {
		return playerdata.Snapshot{}, err
	}
	if err = m.Archives.Write(ctx, account, slot, state.archive, replacement); err != nil {
		return playerdata.Snapshot{}, err
	}
	return m.Get(ctx, account, slot)
}

func (m *Manager) waitForSave(ctx context.Context, account string, slot int, want string) error {
	if len(want) != 16 {
		return fmt.Errorf("%w: 游戏已接受修改，缺少落盘确认，请刷新检查", playerdata.ErrUnavailable)
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return fmt.Errorf("%w: 游戏已接受修改，保存仍待确认，请刷新检查", playerdata.ErrUnavailable)
		}
		data, err := m.Archives.Read(ctx, account, slot)
		if ctx.Err() != nil {
			return fmt.Errorf("%w: 游戏已接受修改，保存仍待确认，请刷新检查", playerdata.ErrUnavailable)
		}
		if err == nil {
			doc, parseErr := playerdata.ParseSave(data)
			if parseErr == nil {
				hash := fnv.New64a()
				hash.Write(doc.Character.Bytes())
				if fmt.Sprintf("%016x", hash.Sum64()) == want {
					return nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w: 游戏已接受修改，保存仍待确认，请刷新检查", playerdata.ErrUnavailable)
		case <-ticker.C:
		}
	}
}

func encodeText(value string) (string, error) {
	if strings.ContainsAny(value, "\x00\r\n") {
		return "", fmt.Errorf("名称包含无效字符")
	}
	data, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte(value))
	return string(data), err
}
func decodeText(value string) (string, error) {
	data, err := simplifiedchinese.GBK.NewDecoder().Bytes([]byte(value))
	if err != nil || strings.ContainsRune(string(data), '\uFFFD') {
		return "", fmt.Errorf("游戏名称编码无效")
	}
	return string(data), nil
}

func onlineSnapshot(fields map[string]string, account string, slot int) (playerdata.Snapshot, error) {
	var result playerdata.Snapshot
	if fields["account"] != account || fields["character_slot"] != strconv.Itoa(slot) || len(fields["revision"]) != 16 {
		return result, fmt.Errorf("%w: 游戏角色响应身份无效", playerdata.ErrUnavailable)
	}
	name, err := decodeText(fields["character"])
	if err != nil {
		return result, err
	}
	record, err := playerdata.NewCharacter(name)
	if err != nil {
		return result, err
	}
	if graphic := fields["character.graphic_id"]; graphic != "" {
		if err = record.SetRaw("bi", []byte(graphic)); err != nil {
			return result, err
		}
	}
	for _, a := range playerdata.Definitions("character") {
		if value, ok := fields["attribute."+a.Key]; ok {
			if err = record.SetRaw(a.Key, []byte(value)); err != nil {
				return result, err
			}
		}
	}
	for _, group := range []struct {
		kind, location, prefix string
		count                  int
	}{{"item", "inventory", "item", 20}, {"item", "warehouse", "poolitem", 30}, {"pet", "inventory", "pet", 5}, {"pet", "warehouse", "poolpet", 15}} {
		for index := 0; index < group.count; index++ {
			prefix := group.kind + "." + group.location + "." + strconv.Itoa(index) + "."
			id, ok := fields[prefix+"id"]
			if !ok {
				continue
			}
			var child *playerdata.Record
			if group.kind == "pet" {
				child, err = playerdata.ParsePet(nil)
			} else {
				child, err = playerdata.ParseItem(nil)
			}
			if err != nil {
				return result, err
			}
			name, err := decodeText(fields[prefix+"name"])
			if err != nil {
				return result, err
			}
			nameKey, idKey := "na", "id"
			if group.kind == "pet" {
				nameKey, idKey = "name", "dmswc"
			}
			if err = child.SetText(nameKey, name, 127); err != nil {
				return result, err
			}
			if err = child.SetRaw(idKey, []byte(id)); err != nil {
				return result, err
			}
			if err = child.SetRaw("bi", []byte(fields[prefix+"graphic_id"])); err != nil {
				return result, err
			}
			for _, a := range playerdata.Definitions(group.kind) {
				if value, ok := fields[prefix+"field."+a.Key]; ok {
					if err = child.SetRaw(a.Key, []byte(value)); err != nil {
						return result, err
					}
				}
			}
			if group.kind == "pet" {
				// CHAR_ALLOCPOINT is a packed native field. It is intentionally
				// absent from Definitions("pet"); readPet expands it into the
				// four growth attributes.
				if value, ok := fields[prefix+"field.lvup"]; ok {
					if err = child.SetRaw("lvup", []byte(value)); err != nil {
						return result, err
					}
				}
				custom, err := decodeText(fields[prefix+"user_name"])
				if err != nil {
					return result, err
				}
				if err = child.SetText("ownt", custom, 127); err != nil {
					return result, err
				}
				for skill := 0; skill < 7; skill++ {
					if value, ok := fields[prefix+"skill."+strconv.Itoa(skill)]; ok {
						if err = child.SetRaw("psk"+strconv.Itoa(skill), []byte(value)); err != nil {
							return result, err
						}
					}
				}
			}
			if err = record.SetRaw(group.prefix+strconv.Itoa(index), child.Bytes()); err != nil {
				return result, err
			}
		}
	}
	revision := fmt.Sprintf("%x", sha256.Sum256([]byte(account+"\x00"+strconv.Itoa(slot)+"\x00"+fields["sequence"]+"\x00"+fields["revision"])))
	result, err = playerdata.SnapshotFromRecord(record, revision)
	result.Online = true
	return result, err
}

func (m *Manager) enrich(snapshot *playerdata.Snapshot) {
	catalog := m.loadCatalog()
	if catalog == nil {
		return
	}
	for i := range snapshot.Possessions {
		p := &snapshot.Possessions[i]
		if entry, ok := catalog.Find(gamecatalog.Kind(p.Kind), int(p.ID)); ok {
			if p.Name == "" {
				p.Name = entry.Name
			}
			if p.GraphicID == 0 {
				p.GraphicID = int64(entry.GraphicID)
			}
		}
		for j := range p.Skills {
			if entry, ok := catalog.Find(gamecatalog.KindPetSkill, int(p.Skills[j].ID)); ok {
				p.Skills[j].Name = entry.Name
			}
		}
	}
}

// loadCatalog keeps the existing Catalog pointer useful for tests and callers
// that already have a loaded catalog, while allowing the command server to
// provide a lazy loader for data that appears after startup. Successful loads
// are cached here; failed loads are left uncached so the next request retries.
func (m *Manager) loadCatalog() *gamecatalog.Catalog {
	m.catalogMu.RLock()
	catalog, loader := m.Catalog, m.CatalogLoader
	m.catalogMu.RUnlock()
	if catalog != nil {
		return catalog
	}
	if loader == nil {
		return nil
	}
	m.catalogMu.Lock()
	defer m.catalogMu.Unlock()
	if m.Catalog != nil {
		return m.Catalog
	}
	catalog, err := loader()
	if err != nil || catalog == nil {
		return nil
	}
	m.Catalog = catalog
	return catalog
}
