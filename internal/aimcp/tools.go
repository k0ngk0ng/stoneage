package aimcp

import "encoding/json"

// ToolDefinition is the subset of the MCP tool declaration needed by this
// server.  InputSchema is a JSON Schema object and is also enforced by the
// server's strict decoder and per-tool validators.
type ToolDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

func toolDefinitions(schedule ...bool) []ToolDefinition {
	definitions := []ToolDefinition{
		{
			Name:        "game_observe",
			Description: "读取绑定角色当前由服务端确认的地图、战斗、人物、宠物、队伍、背包和任务相关状态。",
			InputSchema: objectSchema(map[string]any{}, nil),
		},
		{
			Name:        "game_query_knowledge",
			Description: "从本服已验证的 StoneAge 2.5 知识库查询任务、路线、练级地点或规则；没有验证资料时会明确返回缺口。",
			InputSchema: objectSchema(map[string]any{
				"kind":  map[string]any{"type": "string", "enum": []string{"facts", "task", "leveling", "route", "rule"}},
				"id":    map[string]any{"type": "string", "maxLength": 128},
				"text":  map[string]any{"type": "string", "maxLength": 512},
				"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 50, "default": 10},
			}, []string{"kind"}),
		},
		{
			Name:        "game_start_task",
			Description: "启动已审核任务；parameters.include_dependencies=true 可将前置任务合为一个句柄并核算全链预算，此时不要再单独启动其前置任务。默认只启动所选任务。必须用 game_task_status 确认结果。",
			InputSchema: objectSchema(map[string]any{
				"task_id":    map[string]any{"type": "string", "minLength": 1, "maxLength": 128},
				"parameters": map[string]any{"type": "object", "maxProperties": 32, "additionalProperties": true},
			}, []string{"task_id"}),
		},
		{
			Name:        "game_start_leveling",
			Description: "按本服已验证知识启动人物或指定宠物的练级目标；达标以服务端状态为准。",
			InputSchema: objectSchema(map[string]any{
				"target_kind":     map[string]any{"type": "string", "enum": []string{"character", "pet"}},
				"target_id":       map[string]any{"type": "string", "maxLength": 128},
				"target_level":    map[string]any{"type": "integer", "minimum": 1, "maximum": 1000},
				"target_policy":   map[string]any{"type": "string", "enum": []string{"all", "any"}, "default": "all"},
				"maximum_seconds": map[string]any{"type": "integer", "minimum": 0, "maximum": 2592000},
				"maximum_deaths":  map[string]any{"type": "integer", "minimum": 0, "maximum": 1000},
				"parameters":      map[string]any{"type": "object", "maxProperties": 32, "additionalProperties": true},
			}, []string{"target_kind", "target_level"}),
		},
		{
			Name:        "game_task_status",
			Description: "查询一个已启动任务的服务端状态和确认依据；不会重发任务或动作。",
			InputSchema: objectSchema(map[string]any{
				"handle": map[string]any{"type": "string", "minLength": 1, "maxLength": maxHandleBytes},
			}, []string{"handle"}),
		},
		{
			Name:        "game_cancel",
			Description: "请求取消绑定角色的长任务；取消结果仍须以服务端返回的 receipt 为准。",
			InputSchema: objectSchema(map[string]any{
				"handle": map[string]any{"type": "string", "minLength": 1, "maxLength": maxHandleBytes},
				"reason": map[string]any{"type": "string", "maxLength": maxTextBytes},
			}, []string{"handle"}),
		},
		{
			Name:        "game_action",
			Description: "提交一个受控的 StoneAge 游戏动作。只允许闭合 typed action；不接受 raw packet、shell、URL、账号或角色参数。",
			InputSchema: actionSchema(),
		},
	}
	if len(schedule) > 1 && schedule[1] {
		definitions = append(definitions,
			ToolDefinition{
				Name:        "game_memory_write",
				Description: "保存绑定 AI 玩家的私有自我笔记，用于记住计划、未完成事项和个人偏好；不会写入已确认的游戏事实。相同 key 会覆盖旧笔记。",
				InputSchema: objectSchema(map[string]any{
					"key":  map[string]any{"type": "string", "minLength": 1, "maxLength": MaxAgentNoteKeyBytes},
					"text": map[string]any{"type": "string", "minLength": 1, "maxLength": MaxAgentNoteTextBytes},
				}, []string{"key", "text"}),
			},
			ToolDefinition{
				Name:        "game_memory_list",
				Description: "读取绑定 AI 玩家的私有自我笔记；结果只属于当前绑定 profile，不会返回其他玩家的笔记或 confirmed memory。",
				InputSchema: objectSchema(map[string]any{
					"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": MaxAgentNoteLimit, "default": DefaultAgentNoteLimit},
				}, nil),
			},
			ToolDefinition{
				Name:        "game_memory_delete",
				Description: "删除绑定 AI 玩家的一个私有自我笔记；不会删除 confirmed memory 或游戏数据。",
				InputSchema: objectSchema(map[string]any{
					"key": map[string]any{"type": "string", "minLength": 1, "maxLength": MaxAgentNoteKeyBytes},
				}, []string{"key"}),
			},
		)
	}
	if len(schedule) > 0 && schedule[0] {
		definitions = append(definitions,
			ToolDefinition{
				Name:        "game_schedule_create",
				Description: "为绑定 AI 玩家创建持久定时唤醒；到期后由运行时把提示交给 Codex。只保存受限的活动提示，不直接执行游戏动作。",
				InputSchema: objectSchema(map[string]any{
					"kind":            map[string]any{"type": "string", "minLength": 1, "maxLength": maxScheduleKindBytes},
					"title":           map[string]any{"type": "string", "maxLength": maxScheduleTitleBytes},
					"prompt":          map[string]any{"type": "string", "minLength": 1, "maxLength": maxSchedulePromptBytes},
					"run_at":          map[string]any{"type": "string", "maxLength": 64, "description": "RFC3339 时间；与 delay_seconds 二选一"},
					"delay_seconds":   map[string]any{"type": "integer", "minimum": 1, "maximum": maxScheduleHorizonSeconds, "description": "从服务端当前时间起延迟秒数；与 run_at 二选一"},
					"repeat_seconds":  map[string]any{"type": "integer", "minimum": 0, "maximum": maxScheduleHorizonSeconds},
					"idempotency_key": map[string]any{"type": "string", "maxLength": maxScheduleIdempotencyKeyBytes},
				}, []string{"kind", "prompt"}),
			},
			ToolDefinition{
				Name:        "game_schedule_list",
				Description: "查询绑定 AI 玩家的持久定时唤醒；不会跨玩家读取，也不会执行到期内容。",
				InputSchema: objectSchema(map[string]any{
					"status": map[string]any{"type": "string", "enum": []string{"pending", "delivering", "delivered", "cancelled"}},
					"limit":  map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "default": 50},
				}, nil),
			},
			ToolDefinition{
				Name:        "game_schedule_cancel",
				Description: "取消绑定 AI 玩家的持久定时唤醒；已交付内容不会被回滚。",
				InputSchema: objectSchema(map[string]any{
					"schedule_id": map[string]any{"type": "string", "minLength": 1, "maxLength": maxHandleBytes},
					"reason":      map[string]any{"type": "string", "maxLength": maxTextBytes},
				}, []string{"schedule_id"}),
			},
		)
	}
	return definitions
}

func objectSchema(properties map[string]any, required []string) map[string]any {
	result := map[string]any{
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		result["required"] = required
	}
	return result
}

func actionSchema() map[string]any {
	properties := map[string]any{
		"kind":              map[string]any{"type": "string", "enum": []string{"move", "look", "talk", "window", "battle", "battle-end", "party", "duel", "chat", "mail", "item", "pet", "status", "allocate-stat", "social-setting", "trade"}},
		"expected_revision": map[string]any{"type": "integer", "minimum": 1, "maximum": 9007199254740991},
		"x":                 map[string]any{"type": "integer", "minimum": -1000000, "maximum": 1000000},
		"y":                 map[string]any{"type": "integer", "minimum": -1000000, "maximum": 1000000},
		"direction":         map[string]any{"type": "integer", "minimum": 0, "maximum": 7},
		"route":             map[string]any{"type": "string", "maxLength": maxRouteBytes},
		"target_id":         map[string]any{"type": "integer", "minimum": 0, "maximum": 2147483647, "description": "For battle actions, use the observed battle participant ID; pet wait uses 255 together with index=255."},
		"index":             map[string]any{"type": "integer", "minimum": 0, "maximum": 255, "description": "For allocate-stat: exactly one point, index 0=vital, 1=strength, 2=toughness, 3=dexterity; omit value/command. Requires flags[stat_points:known] and own_progress[stat_points]>0. Autonomous AI players must use own_progress[build_next_stat] only when flags[build:allocation_allowed] is true. Observe and query the same receipt handle for server confirmation afterward; never retry an uncertain write. For mail send: current observed address-book slot (0-79), not a persistent player ID. Otherwise zero-based slot (0-19). Only battle pet wait accepts index=255 with target_id=255."},
		"value":             map[string]any{"type": "integer", "minimum": -2147483648, "maximum": 2147483647},
		"value2":            map[string]any{"type": "integer", "minimum": -2147483648, "maximum": 2147483647},
		"window_type":       map[string]any{"type": "integer", "minimum": 0, "maximum": 2147483647},
		"window_button":     map[string]any{"type": "integer", "minimum": 0, "maximum": 2147483647},
		"window_sequence":   map[string]any{"type": "integer", "minimum": 0, "maximum": 2147483647},
		"window_object_id":  map[string]any{"type": "integer", "minimum": 0, "maximum": 2147483647},
		"window_select":     map[string]any{"type": "integer", "minimum": 0, "maximum": 2147483647},
		"command":           map[string]any{"type": "string", "maxLength": maxTextBytes, "description": "For kind=mail: list requests the current address-book slots, add records the player at the current x/y tile, and send delivers ordinary game mail to an observed index 0..79. For kind=trade: request (target_id=visible facing player), offer-item (index=offer slot 0..1,value=backpack slot 5..19), offer-gold (index=0..1,value=positive gold), offer-pet (pet_slot=0..4), lock, confirm, cancel. Read observed trade state before every step; confirm builds the exact current bilateral offer internally. For kind=social-setting: party, duel, party-chat, trade-card or trade; value must be 0 (disable) or 1 (enable). Observe FS confirmation before another setting. For kind=battle: attack, defend, guard, escape, wait (player), pet, item, or skill (character magic). Use typed index/target_id fields, never a raw protocol command."},
		"text":              map[string]any{"type": "string", "maxLength": maxTextBytes},
		"color":             map[string]any{"type": "integer", "minimum": 0, "maximum": 2147483647},
		"range":             map[string]any{"type": "integer", "minimum": 0, "maximum": 100},
		"party_request":     map[string]any{"type": "integer", "minimum": 0, "maximum": 16},
		"pet_slot":          map[string]any{"type": "integer", "minimum": 0, "maximum": 9},
	}
	return objectSchema(properties, []string{"kind", "expected_revision"})
}

func encodeJSON(value any) (json.RawMessage, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(encoded), nil
}
