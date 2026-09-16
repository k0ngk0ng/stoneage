// Package aiinitial resolves creation-only AI starting states. It never mutates
// characters; the native initialization boundary applies a resolved plan once.
package aiinitial

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"math/big"
	"strconv"
	"strings"

	"github.com/k0ngk0ng/stoneage/internal/characterbuild"
)

const MaximumLevel = 140

type Range struct {
	Min int `json:"min"`
	Max int `json:"max"`
}

func (r Range) valid(min, max int) bool { return r.Min >= min && r.Max >= r.Min && r.Max <= max }

type Pet struct {
	TemplateID int `json:"template_id"`
	Level      int `json:"level"`
}
type RandomRules struct {
	CharacterLevel Range `json:"character_level"`
	PetLevel       Range `json:"pet_level"`
	PetCount       Range `json:"pet_count"`
	PetTemplates   []int `json:"pet_templates"`
	Hometowns      []int `json:"hometowns"`
}
type Request struct {
	Mode           string                 `json:"mode"`
	CharacterLevel int                    `json:"character_level,omitempty"`
	Hometown       int                    `json:"hometown,omitempty"`
	Weights        characterbuild.Weights `json:"weights"`
	Pets           []Pet                  `json:"pets,omitempty"`
	Random         *RandomRules           `json:"random,omitempty"`
	Mount          *bool                  `json:"mount,omitempty"`
}
type Resolved struct {
	CharacterLevel int                    `json:"character_level"`
	Hometown       int                    `json:"hometown"`
	Weights        characterbuild.Weights `json:"weights"`
	Pets           []Pet                  `json:"pets"`
	Mount          bool                   `json:"mount"`
}

// mounted reports the requested mount capability. A nil pointer is the
// legacy request shape and deliberately means false.
func (r *Request) mounted() bool {
	return r != nil && r.Mount != nil && *r.Mount
}

func (r *Request) Validate() error {
	if r == nil {
		return nil
	}
	switch r.Mode {
	case "birth":
		if r.CharacterLevel != 0 || r.Hometown != 0 || r.Weights != (characterbuild.Weights{}) || r.Random != nil {
			return errors.New("出生模式不接受额外初始状态")
		}
		if !r.mounted() && len(r.Pets) != 0 {
			return errors.New("出生模式不接受额外初始状态")
		}
		if r.mounted() && len(r.Pets) > 1 {
			return errors.New("出生骑宠模式最多接受一只等级1宠物")
		}
		if len(r.Pets) == 1 && (r.Pets[0].Level != 1 || r.Pets[0].TemplateID < 0 || r.Pets[0].TemplateID > 2147483647) {
			return errors.New("出生骑宠宠物模板必须有效且等级必须为1")
		}
	case "custom":
		if r.Random != nil {
			return errors.New("自定义模式不能同时随机")
		}
		return (Resolved{CharacterLevel: r.CharacterLevel, Hometown: r.Hometown, Weights: r.Weights, Pets: r.Pets, Mount: r.mounted()}).Validate()
	case "random":
		if r.CharacterLevel != 0 || r.Hometown != 0 || r.Weights != (characterbuild.Weights{}) || len(r.Pets) != 0 {
			return errors.New("随机模式请使用范围与候选列表")
		}
		q := r.Random
		if q == nil || !q.CharacterLevel.valid(1, MaximumLevel) || !q.PetLevel.valid(1, MaximumLevel) || !q.PetCount.valid(0, 5) {
			return errors.New("初始等级范围须为1–140，宠物数量为0–5")
		}
		if r.mounted() && q.PetCount.Min < 1 {
			return errors.New("默认骑乘需要至少一只初始宠物")
		}
		if len(q.Hometowns) < 1 || len(q.Hometowns) > 4 || len(q.PetTemplates) > 100 || (q.PetCount.Max > 0 && len(q.PetTemplates) == 0) {
			return errors.New("请选择出生村和随机宠物候选")
		}
		seen := map[int]bool{}
		for _, v := range q.Hometowns {
			if v < 0 || v > 3 || seen[v] {
				return errors.New("出生村候选无效或重复")
			}
			seen[v] = true
		}
		seen = map[int]bool{}
		for _, v := range q.PetTemplates {
			if v < 0 || v > 2147483647 || seen[v] {
				return errors.New("宠物候选无效或重复")
			}
			seen[v] = true
		}
	default:
		return errors.New("初始状态须为birth、random或custom")
	}
	return nil
}
func (r Resolved) Validate() error {
	if r.CharacterLevel < 1 || r.CharacterLevel > MaximumLevel || r.Hometown < 0 || r.Hometown > 3 || len(r.Pets) > 5 || (r.Mount && len(r.Pets) == 0) {
		return errors.New("初始等级、出生村或宠物数量无效")
	}
	if err := (&characterbuild.Policy{Weights: r.Weights}).Validate(); err != nil {
		return err
	}
	for _, p := range r.Pets {
		if p.TemplateID < 0 || p.TemplateID > 2147483647 || p.Level < 1 || p.Level > MaximumLevel {
			return errors.New("初始宠物模板或等级无效")
		}
	}
	return nil
}

// Resolve draws all choices before provisioning. Callers must persist its
// result before issuing any game mutations; recovery must not call it again.
func Resolve(r *Request, entropy io.Reader) (*Resolved, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	if r == nil {
		return nil, nil
	}
	if r.Mode == "birth" {
		if !r.mounted() {
			return nil, nil
		}
		if len(r.Pets) != 1 {
			return nil, errors.New("出生骑宠模式需要一只默认宠物")
		}
		return &Resolved{CharacterLevel: 1, Hometown: 0, Weights: characterbuild.Weights{Vital: 1, Strength: 1, Toughness: 1, Dexterity: 1}, Pets: append([]Pet{}, r.Pets...), Mount: true}, nil
	}
	if r.Mode == "custom" {
		return &Resolved{CharacterLevel: r.CharacterLevel, Hometown: r.Hometown, Weights: r.Weights, Pets: append([]Pet{}, r.Pets...), Mount: r.mounted()}, nil
	}
	if entropy == nil {
		entropy = rand.Reader
	}
	draw := func(min, max int) (int, error) {
		v, err := rand.Int(entropy, big.NewInt(int64(max-min+1)))
		if err != nil {
			return 0, err
		}
		return min + int(v.Int64()), nil
	}
	q := r.Random
	out := &Resolved{Pets: []Pet{}, Mount: r.mounted()}
	var err error
	if out.CharacterLevel, err = draw(q.CharacterLevel.Min, q.CharacterLevel.Max); err != nil {
		return nil, err
	}
	i, err := draw(0, len(q.Hometowns)-1)
	if err != nil {
		return nil, err
	}
	out.Hometown = q.Hometowns[i]
	weights := []*int{&out.Weights.Vital, &out.Weights.Strength, &out.Weights.Toughness, &out.Weights.Dexterity}
	// Positive weights ensure every generated character has a viable allocation;
	// custom mode still permits zero weights for specialized builds.
	for _, w := range weights {
		if *w, err = draw(1, 10); err != nil {
			return nil, err
		}
	}
	n, err := draw(q.PetCount.Min, q.PetCount.Max)
	if err != nil {
		return nil, err
	}
	for j := 0; j < n; j++ {
		i, err := draw(0, len(q.PetTemplates)-1)
		if err != nil {
			return nil, err
		}
		level, err := draw(q.PetLevel.Min, q.PetLevel.Max)
		if err != nil {
			return nil, err
		}
		out.Pets = append(out.Pets, Pet{q.PetTemplates[i], level})
	}
	return out, out.Validate()
}
func (r Resolved) Payload() (string, error) {
	if err := r.Validate(); err != nil {
		return "", err
	}
	version := "1"
	values := []string{version, strconv.Itoa(r.CharacterLevel), strconv.Itoa(r.Weights.Vital), strconv.Itoa(r.Weights.Strength), strconv.Itoa(r.Weights.Toughness), strconv.Itoa(r.Weights.Dexterity)}
	if r.Mount {
		version = "2"
		values[0] = version
		values = append(values, "1")
	}
	values = append(values, strconv.Itoa(len(r.Pets)))
	for _, p := range r.Pets {
		values = append(values, fmt.Sprintf("%d:%d", p.TemplateID, p.Level))
	}
	return strings.Join(values, "|"), nil
}
