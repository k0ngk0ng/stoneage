package playermanager

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/k0ngk0ng/stoneage/internal/gamecatalog"
	"github.com/k0ngk0ng/stoneage/internal/playerdata"
)

func validateMutation(m playerdata.Mutation) error {
	if m.Action == "set_character" {
		return playerdata.ValidateAttribute("character", m.Field, m.Value)
	}
	if m.Location != "inventory" && m.Location != "warehouse" {
		return fmt.Errorf("请选择随身或仓库")
	}
	kind := "item"
	if strings.Contains(m.Action, "pet") {
		kind = "pet"
	}
	start, end := slotRange(kind, m.Location)
	if strings.HasPrefix(m.Action, "grant_") {
		if m.Action != "grant_item" && m.Action != "grant_pet" {
			return fmt.Errorf("不支持的赠送操作")
		}
		if m.TemplateID < 0 || m.Quantity < 1 || m.Quantity > end-start {
			return fmt.Errorf("赠送数量或模板编号无效")
		}
		return nil
	}
	if m.Slot < 0 || m.Slot >= end {
		return fmt.Errorf("物品或宠物槽位无效")
	}
	switch m.Action {
	case "set_item", "set_pet":
		if m.Field == "name" {
			name, err := encodeText(m.Name)
			if err != nil || strings.TrimSpace(m.Name) == "" || len(name) > 16 {
				return fmt.Errorf("名称须为 1–16 个游戏编码字节")
			}
			return nil
		}
		return playerdata.ValidateAttribute(kind, m.Field, m.Value)
	case "delete_item", "delete_pet":
		return nil
	case "set_pet_skill", "delete_pet_skill":
		if m.SkillSlot < 0 || m.SkillSlot >= 7 || m.Action == "set_pet_skill" && m.SkillID < 0 {
			return fmt.Errorf("宠物技能或技能槽位无效")
		}
		return nil
	}
	return fmt.Errorf("不支持的修改操作")
}

func slotRange(kind, location string) (int, int) {
	if kind == "pet" {
		if location == "warehouse" {
			return 0, 15
		}
		return 0, 5
	}
	if location == "warehouse" {
		return 0, 30
	}
	return 5, 20
}
func recordPrefix(kind, location string) string {
	if location == "warehouse" {
		return "pool" + kind
	}
	return kind
}

func (m *Manager) applyOffline(ctx context.Context, account string, doc *playerdata.Document, change playerdata.Mutation) error {
	if change.Action == "set_character" {
		if _, ok := doc.Character.Raw(change.Field); !ok {
			return fmt.Errorf("这个角色没有该属性")
		}
		return doc.Character.SetInteger(change.Field, change.Value)
	}
	kind := "item"
	if strings.Contains(change.Action, "pet") {
		kind = "pet"
	}
	prefix := recordPrefix(kind, change.Location)
	if strings.HasPrefix(change.Action, "grant_") {
		return m.grantOffline(ctx, account, doc, kind, prefix, change)
	}
	key := prefix + strconv.Itoa(change.Slot)
	raw, ok := doc.Character.Raw(key)
	if !ok || len(raw) == 0 {
		return playerdata.ErrConflict
	}
	var child *playerdata.Record
	var err error
	if kind == "pet" {
		child, err = playerdata.ParsePet(raw)
	} else {
		child, err = playerdata.ParseItem(raw)
	}
	if err != nil {
		return err
	}
	switch change.Action {
	case "delete_item", "delete_pet":
		doc.Character.Delete(key)
		if kind == "pet" && change.Location == "inventory" {
			for _, reference := range []string{"slt", "ridepet"} {
				if value, err := doc.Character.Integer(reference); err == nil && value == int64(change.Slot) {
					if err = doc.Character.SetInteger(reference, -1); err != nil {
						return err
					}
				}
			}
		}
		return nil
	case "set_item", "set_pet":
		if change.Field == "name" {
			nameKey := "na"
			if kind == "pet" {
				nameKey = "ownt"
			}
			err = child.SetText(nameKey, change.Name, 16)
		} else if kind == "pet" && strings.HasPrefix(change.Field, "growth_") {
			err = playerdata.SetPetGrowth(child, change.Field, change.Value)
		} else {
			if kind == "item" && change.Field == "upin" && change.Value > 1 {
				actual, inspectErr := m.inspectItem(ctx, child)
				if inspectErr != nil {
					return inspectErr
				}
				maximum, err := actual.Integer("canpile")
				if err != nil || maximum < change.Value {
					return fmt.Errorf("物品堆叠数量超过允许上限")
				}
			}
			if kind == "pet" && change.Field == "slt" {
				for skill := int(change.Value); skill < 7; skill++ {
					if value, err := child.Integer("psk" + strconv.Itoa(skill)); err == nil && value >= 0 {
						return fmt.Errorf("请先移除超出新技能格数的技能")
					}
				}
			}
			err = child.SetInteger(change.Field, change.Value)
		}
	case "set_pet_skill", "delete_pet_skill":
		skillKey := "psk" + strconv.Itoa(change.SkillSlot)
		if change.Action == "delete_pet_skill" {
			child.Delete(skillKey)
		} else {
			catalog := m.loadCatalog()
			if catalog == nil {
				return playerdata.ErrUnavailable
			}
			if _, ok := catalog.Find(gamecatalog.KindPetSkill, change.SkillID); !ok {
				return fmt.Errorf("宠物技能不存在")
			}
			slots, err := child.Integer("slt")
			if err != nil || change.SkillSlot >= int(slots) {
				return fmt.Errorf("技能槽位超出这只宠物的技能格数")
			}
			err = child.SetInteger(skillKey, int64(change.SkillID))
			if err != nil {
				return err
			}
		}
	}
	if err != nil {
		return err
	}
	return doc.Character.SetRaw(key, child.Bytes())
}

func (m *Manager) grantOffline(ctx context.Context, account string, doc *playerdata.Document, kind, prefix string, change playerdata.Mutation) error {
	catalog := m.loadCatalog()
	if catalog == nil {
		return playerdata.ErrUnavailable
	}
	if _, ok := catalog.Find(gamecatalog.Kind(kind), change.TemplateID); !ok {
		return fmt.Errorf("赠送模板不存在")
	}
	start, end := slotRange(kind, change.Location)
	if kind == "pet" && change.Location == "warehouse" {
		transmigration := int64(0)
		if _, present := doc.Character.Raw("trn"); present {
			var err error
			transmigration, err = doc.Character.Integer("trn")
			if err != nil {
				return err
			}
		}
		end = playerdata.PetWarehouseCapacity(transmigration)
	}
	available := []int{}
	for slot := start; slot < end; slot++ {
		if raw, ok := doc.Character.Raw(prefix + strconv.Itoa(slot)); !ok || len(raw) == 0 {
			available = append(available, slot)
		}
	}
	if len(available) < change.Quantity {
		return fmt.Errorf("空位不足，未赠送任何内容")
	}
	owner, err := doc.Character.Text("name")
	if err != nil {
		return err
	}
	for i := 0; i < change.Quantity; i++ {
		response, err := m.Game.Call(ctx, map[string]string{"action": "export_" + kind, "template_id": strconv.Itoa(change.TemplateID)})
		if err != nil {
			return err
		}
		if response["kind"] != kind || response["template_id"] != strconv.Itoa(change.TemplateID) || response["payload"] == "" {
			return fmt.Errorf("%w: 原生模板响应无效", playerdata.ErrUnavailable)
		}
		payload := []byte(response["payload"])
		if kind == "pet" {
			pet, err := playerdata.ParsePet(payload)
			if err != nil {
				return err
			}
			if err = pet.SetText("ocd", account, 31); err != nil {
				return err
			}
			if err = pet.SetText("ocn", owner, 32); err != nil {
				return err
			}
			payload = pet.Bytes()
		} else {
			item, err := playerdata.ParseItem(payload)
			if err != nil {
				return err
			}
			id, err := item.Integer("id")
			if err != nil || id != int64(change.TemplateID) {
				return fmt.Errorf("原生物品模板编号不匹配")
			}
		}
		if err = doc.Character.SetRaw(prefix+strconv.Itoa(available[i]), payload); err != nil {
			return err
		}
	}
	return nil
}
