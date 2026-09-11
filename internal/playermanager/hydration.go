package playermanager

import (
	"context"
	"fmt"
	"strconv"

	"github.com/k0ngk0ng/stoneage/internal/playerdata"
)

// Native saves omit item values matching their template. Hydrate a separate
// display document so reading a character never rewrites those omitted fields.
func (m *Manager) offlineSnapshot(ctx context.Context, archive []byte) (playerdata.Snapshot, error) {
	doc, err := playerdata.ParseSave(archive)
	if err != nil {
		return playerdata.Snapshot{}, err
	}
	for _, group := range []struct {
		prefix string
		count  int
	}{{"item", 20}, {"poolitem", 30}} {
		for slot := 0; slot < group.count; slot++ {
			key := group.prefix + strconv.Itoa(slot)
			raw, present := doc.Character.Raw(key)
			if !present || len(raw) == 0 {
				continue
			}
			item, err := playerdata.ParseItem(raw)
			if err != nil {
				return playerdata.Snapshot{}, err
			}
			item, err = m.inspectItem(ctx, item)
			if err != nil {
				return playerdata.Snapshot{}, err
			}
			if err = doc.Character.SetRaw(key, item.Bytes()); err != nil {
				return playerdata.Snapshot{}, err
			}
		}
	}
	return doc.Snapshot()
}

func (m *Manager) inspectItem(ctx context.Context, original *playerdata.Record) (*playerdata.Record, error) {
	id, err := original.Integer("id")
	if err != nil {
		return nil, err
	}
	fields, err := m.Game.Call(ctx, map[string]string{"action": "inspect_item", "payload": string(original.Bytes())})
	if err != nil {
		return nil, err
	}
	if fields["kind"] != "item" || fields["template_id"] != strconv.FormatInt(id, 10) || fields["item.inspected.id"] != strconv.FormatInt(id, 10) {
		return nil, fmt.Errorf("%w: 原生物品属性响应不匹配", playerdata.ErrUnavailable)
	}
	item, err := playerdata.ParseItem(original.Bytes())
	if err != nil {
		return nil, err
	}
	for _, key := range append(itemAttributeKeys(), "bi", "canpile") {
		value, present := fields["item.inspected.field."+key]
		if !present {
			continue
		}
		number, err := strconv.ParseInt(value, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("%w: 原生物品属性无效", playerdata.ErrUnavailable)
		}
		if err = item.SetInteger(key, number); err != nil {
			return nil, err
		}
	}
	name, err := decodeText(fields["item.inspected.name"])
	if err != nil {
		return nil, err
	}
	if err = item.SetText("na", name, 127); err != nil {
		return nil, err
	}
	return item, nil
}

func itemAttributeKeys() []string {
	keys := []string{}
	for _, field := range playerdata.Definitions("item") {
		keys = append(keys, field.Key)
	}
	return keys
}
