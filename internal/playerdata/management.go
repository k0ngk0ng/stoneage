package playerdata

import (
	"context"
	"errors"
)

var (
	ErrConflict    = errors.New("角色数据已变化，请刷新后再操作")
	ErrUnavailable = errors.New("玩家管理服务暂时不可用")
	ErrNotFound    = errors.New("角色不存在")
)

type Character struct {
	Slot   int    `json:"slot"`
	Name   string `json:"name"`
	Online bool   `json:"online"`
}

// BundleEntry describes one template in a bundle. Quantity is the number of
// separate game objects to create; a stackable item's in-game stack size is
// intentionally left to the existing item mutation path.
type BundleEntry struct {
	Kind       string `json:"kind"`
	TemplateID int    `json:"template_id"`
	Quantity   int    `json:"quantity"`
	Name       string `json:"name,omitempty"`
}

type Mutation struct {
	Revision   string        `json:"revision"`
	Action     string        `json:"action"`
	Location   string        `json:"location,omitempty"`
	Slot       int           `json:"slot,omitempty"`
	TemplateID int           `json:"template_id,omitempty"`
	Field      string        `json:"field,omitempty"`
	Value      int64         `json:"value,omitempty"`
	SkillSlot  int           `json:"skill_slot,omitempty"`
	SkillID    int           `json:"skill_id,omitempty"`
	Name       string        `json:"name,omitempty"`
	Quantity   int           `json:"quantity,omitempty"`
	Bundle     []BundleEntry `json:"bundle,omitempty"`
}

// Manager owns concurrency and persistence. A returned successful mutation
// means it was applied by the authoritative game/account service.
type Manager interface {
	List(context.Context, string) ([]Character, error)
	Get(context.Context, string, int) (Snapshot, error)
	Apply(context.Context, string, int, Mutation) (Snapshot, error)
}
