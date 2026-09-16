package playermanager

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/k0ngk0ng/stoneage/internal/aiinitial"
	"github.com/k0ngk0ng/stoneage/internal/playerbridge"
	"github.com/k0ngk0ng/stoneage/internal/playerdata"
)

// ValidateInitial checks server-owned catalog identities without creating an
// account or submitting a game operation.
func (m *Manager) ValidateInitial(ctx context.Context, plan aiinitial.Resolved) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := plan.Validate(); err != nil {
		return err
	}
	if m.Game == nil || m.Archives == nil {
		return playerdata.ErrUnavailable
	}
	if len(plan.Pets) == 0 {
		return nil
	}
	catalog := m.loadCatalog()
	if catalog == nil {
		return playerdata.ErrUnavailable
	}
	for _, pet := range plan.Pets {
		found := false
		for _, template := range catalog.Pets {
			if template.TemplateID != pet.TemplateID {
				continue
			}
			found = true
			if template.LimitLevel > 0 && pet.Level > template.LimitLevel {
				return fmt.Errorf("初始宠物 %s 的等级不能超过模板上限 %d", template.Name, template.LimitLevel)
			}
			break
		}
		if !found {
			return fmt.Errorf("初始宠物模板不存在：%d", pet.TemplateID)
		}
	}
	if plan.Mount {
		payload, err := plan.Payload()
		if err != nil {
			return err
		}
		result, err := m.Game.Call(ctx, map[string]string{"action": "validate_ai", "payload": payload, "value": "100000"})
		if err != nil {
			var native *playerbridge.Error
			if errors.As(err, &native) && native.Code == "invalid_mount" {
				return aiinitial.ErrInvalidMount
			}
			return fmt.Errorf("初始骑乘组合不可用（请检查首只宠物种类及等级）: %w", err)
		}
		graphic, err := strconv.Atoi(result["mount_graphic_id"])
		if err != nil || graphic <= 0 || graphic == 100000 || result["mounted"] != "1" {
			return fmt.Errorf("服务端尚未确认初始骑乘组合")
		}
	}
	return nil
}

// DefaultAIMount asks the effective native catalog for a compatible level-one
// pet. It does not create a pet or select a guessed template ID.
func (m *Manager) DefaultAIMount(ctx context.Context) (aiinitial.Pet, error) {
	if m == nil || m.Game == nil {
		return aiinitial.Pet{}, playerdata.ErrUnavailable
	}
	result, err := m.Game.Call(ctx, map[string]string{"action": "ai_mount_default", "value": "100000"})
	if err != nil {
		return aiinitial.Pet{}, fmt.Errorf("默认骑宠暂不可用: %w", err)
	}
	id, err := strconv.Atoi(result["template_id"])
	if err != nil || id < 0 || id > 2147483647 {
		return aiinitial.Pet{}, fmt.Errorf("服务端返回的默认骑宠无效")
	}
	return aiinitial.Pet{TemplateID: id, Level: 1}, nil
}

// InitializeAI is a dedicated creation operation, intentionally absent from
// the general HTTP mutation API. It sends one native transaction and verifies
// its durable save; an error never causes a mutation retry.
func (m *Manager) InitializeAI(ctx context.Context, account string, slot int, plan aiinitial.Resolved) (playerdata.Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.ValidateInitial(ctx, plan); err != nil {
		return playerdata.Snapshot{}, err
	}
	payload, err := plan.Payload()
	if err != nil {
		return playerdata.Snapshot{}, err
	}
	state, err := m.current(ctx, account, slot)
	if err != nil {
		return playerdata.Snapshot{}, err
	}
	if !state.snapshot.Online {
		return playerdata.Snapshot{}, fmt.Errorf("初始化需要专用角色在线")
	}
	response, err := m.Game.Call(ctx, map[string]string{
		"action": "initialize_ai", "account": account, "character": state.game["character"], "character_slot": strconv.Itoa(slot),
		"expected_revision": state.game["revision"], "expected_sequence": state.game["sequence"], "payload": payload,
	})
	if err != nil {
		return playerdata.Snapshot{}, err
	}
	archive, err := m.waitForSavedArchive(ctx, account, slot, response["save_data_hash"])
	if err != nil {
		return playerdata.Snapshot{}, err
	}
	// Verify the exact archive matching the native save receipt, not a later
	// online projection that has no persisted identity evidence.
	result, err := m.offlineSnapshot(ctx, archive)
	if err != nil {
		return playerdata.Snapshot{}, err
	}
	if result.PersistentCharacterID == "" || result.Name != state.snapshot.Name {
		return playerdata.Snapshot{}, fmt.Errorf("初始化存档角色身份尚未确认")
	}
	result.Online = true
	if err := VerifyInitial(result, plan); err != nil {
		return playerdata.Snapshot{}, err
	}
	return result, nil
}

func VerifyInitial(actual playerdata.Snapshot, plan aiinitial.Resolved) error {
	if plan.Mount && (len(plan.Pets) == 0 || actual.RidePetSlot == nil || *actual.RidePetSlot != 0 ||
		actual.LearnRide == nil || *actual.LearnRide < int64(plan.Pets[0].Level) || actual.GraphicID <= 0 || actual.GraphicID == 100000) {
		return fmt.Errorf("初始骑乘状态尚未确认")
	}
	value := func(attrs []playerdata.Attribute, key string) (int64, bool) {
		for _, a := range attrs {
			if a.Key == key {
				return a.Value, true
			}
		}
		return 0, false
	}
	level, known := value(actual.Attributes, "lv")
	exp, expKnown := value(actual.Attributes, "nexp")
	hp, hpKnown := value(actual.Attributes, "hp")
	if !known || level != int64(plan.CharacterLevel) || !expKnown || exp != 0 || !hpKnown || hp <= 0 {
		return fmt.Errorf("初始人物状态尚未确认")
	}
	// Initial allocation uses whole native points (100 serialized units each).
	// Verify the same largest-remainder split from the saved total, including
	// zero-weight attributes, without assuming a server-specific getSkup value.
	weights := plan.Weights.Values()
	var points [4]int64
	var total, weightTotal int64
	for i, key := range []string{"vi", "str", "tou", "dx"} {
		v, known := value(actual.Attributes, key)
		if !known || v < 0 || v%100 != 0 {
			return fmt.Errorf("初始人物配点尚未确认")
		}
		points[i] = v / 100
		total += points[i]
		weightTotal += int64(weights[i])
	}
	if total < 20 || weightTotal <= 0 {
		return fmt.Errorf("初始人物配点尚未确认")
	}
	var expected, remainders [4]int64
	var distributed int64
	for i, weight := range weights {
		product := total * int64(weight)
		expected[i], remainders[i] = product/weightTotal, product%weightTotal
		distributed += expected[i]
	}
	for ; distributed < total; distributed++ {
		best := 0
		for i := 1; i < 4; i++ {
			if remainders[i] > remainders[best] {
				best = i
			}
		}
		expected[best]++
		remainders[best] = -1
	}
	remaining, remainingKnown := value(actual.Attributes, "skup")
	if points != expected || !remainingKnown || remaining != 0 {
		return fmt.Errorf("初始人物配点尚未确认")
	}
	count := 0
	seen := make(map[int]bool)
	for _, pet := range actual.Possessions {
		if pet.Kind != "pet" || pet.Location != "inventory" {
			continue
		}
		if pet.Slot < 0 || pet.Slot >= len(plan.Pets) || seen[pet.Slot] {
			return fmt.Errorf("初始宠物槽位不符")
		}
		seen[pet.Slot] = true
		want := plan.Pets[pet.Slot]
		level, known := value(pet.Attributes, "lv")
		hp, hpKnown := value(pet.Attributes, "hp")
		if pet.ID != int64(want.TemplateID) || !known || level != int64(want.Level) || !hpKnown || hp <= 0 {
			return fmt.Errorf("初始宠物状态尚未确认")
		}
		count++
	}
	if count != len(plan.Pets) {
		return fmt.Errorf("初始宠物数量不符")
	}
	return nil
}

// VerifyAIInitial reads an offline saved character for publication recovery.
// It never sends initialize_ai and does not treat matching live memory as a save.
func (m *Manager) VerifyAIInitial(ctx context.Context, account string, slot int, plan aiinitial.Resolved) (playerdata.Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := plan.Validate(); err != nil {
		return playerdata.Snapshot{}, err
	}
	actual, err := m.Get(ctx, account, slot)
	if err != nil {
		return playerdata.Snapshot{}, err
	}
	if actual.Online {
		return playerdata.Snapshot{}, fmt.Errorf("请等待创建会话离线后再核验初始化存档")
	}
	if err := VerifyInitial(actual, plan); err != nil {
		return playerdata.Snapshot{}, err
	}
	return actual, nil
}
