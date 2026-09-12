package playerdata

import (
	"fmt"
	"strconv"
)

// Attribute definitions are the editor's labels and bounds, not array offsets.
// The keys are the stable serialization names from CHAR_setintdata.
type Attribute struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Value int64  `json:"value"`
	Min   int64  `json:"min"`
	Max   int64  `json:"max"`
}

type Possession struct {
	Kind       string         `json:"kind"`
	Location   string         `json:"location"`
	Slot       int            `json:"slot"`
	Name       string         `json:"name"`
	ID         int64          `json:"id"`
	GraphicID  int64          `json:"graphic_id"`
	Attributes []Attribute    `json:"attributes"`
	Skills     []PetSkillSlot `json:"skills,omitempty"`
}

type PetSkillSlot struct {
	Slot int    `json:"slot"`
	ID   int64  `json:"id"`
	Name string `json:"name,omitempty"`
}

type Snapshot struct {
	Online      bool           `json:"online"`
	Name        string         `json:"name"`
	Revision    string         `json:"revision"`
	Attributes  []Attribute    `json:"attributes"`
	Possessions []Possession   `json:"possessions"`
	Capacities  map[string]int `json:"capacities"`
}

var characterAttributes = []Attribute{
	{Key: "gld", Label: "随身石币", Max: 10000000},
	{Key: "bankgld", Label: "银行石币", Max: 100000000},
	{Key: "personaglod", Label: "个人银行", Max: 50000000},
	{Key: "lv", Label: "等级", Min: 1, Max: 140},
	{Key: "nexp", Label: "经验", Max: 2147483647},
	{Key: "hp", Label: "当前生命", Max: 2147483647},
	{Key: "mp", Label: "当前气力", Max: 2147483647},
	{Key: "mmp", Label: "最大气力", Max: 2147483647},
	{Key: "vi", Label: "体力", Max: 2147483647},
	{Key: "str", Label: "腕力", Max: 2147483647},
	{Key: "tou", Label: "耐力", Max: 2147483647},
	{Key: "dx", Label: "速度", Max: 2147483647},
	{Key: "lvup", Label: "剩余能力点", Max: 2147483647},
	{Key: "skup", Label: "剩余技能点", Max: 2147483647},
	{Key: "chr", Label: "魅力", Max: 100},
	{Key: "luc", Label: "运气", Max: 100},
	{Key: "aea", Label: "地属性", Max: 100},
	{Key: "awa", Label: "水属性", Max: 100},
	{Key: "afi", Label: "火属性", Max: 100},
	{Key: "awi", Label: "风属性", Max: 100},
	{Key: "trn", Label: "转生次数", Max: 2147483647},
	{Key: "duel", Label: "竞技场积分", Max: 100000000},
	{Key: "fame", Label: "个人声望", Max: 100000000},
	{Key: "memberpoint", Label: "会员点数", Max: 2147483647},
}

var itemAttributes = []Attribute{
	{Key: "dmce", Label: "耐久", Max: 100000000},
	{Key: "mdmce", Label: "最大耐久", Max: 100000000},
	{Key: "upin", Label: "堆叠数量", Min: 1, Max: 9999},
	{Key: "lv", Label: "等级要求", Max: 140},
	{Key: "cs", Label: "价格", Max: 100000000},
	{Key: "ma", Label: "攻击", Min: -100000000, Max: 100000000},
	{Key: "md", Label: "防御", Min: -100000000, Max: 100000000},
	{Key: "mh", Label: "敏捷", Min: -100000000, Max: 100000000},
	{Key: "mm", Label: "生命加成", Min: -100000000, Max: 100000000},
	{Key: "mq", Label: "气力加成", Min: -100000000, Max: 100000000},
	{Key: "ml", Label: "运气加成", Min: -100000000, Max: 100000000},
	{Key: "mc", Label: "魅力加成", Min: -100000000, Max: 100000000},
	{Key: "mv", Label: "回避加成", Min: -100000000, Max: 100000000},
	{Key: "ann", Label: "最少攻击次数", Min: 1, Max: 20},
	{Key: "anx", Label: "最多攻击次数", Min: 1, Max: 20},
	{Key: "mat", Label: "属性类型", Max: 4},
	{Key: "mav", Label: "属性数值", Max: 100},
	{Key: "mid", Label: "精灵技能编号", Min: -1, Max: 100000000},
	{Key: "mpr", Label: "精灵触发率", Max: 100},
	{Key: "mu", Label: "精灵消耗气力", Max: 100000000},
}

// Definitions returns only fields meaningful for the selected kind. Several
// legacy pet slots alias player fields and therefore need different labels.
func Definitions(kind string) []Attribute {
	if kind == "item" {
		return append([]Attribute(nil), itemAttributes...)
	}
	if kind != "pet" {
		return append([]Attribute(nil), characterAttributes...)
	}
	result := []Attribute{}
	for _, a := range characterAttributes {
		switch a.Key {
		case "lv", "nexp", "hp", "mp", "mmp", "vi", "str", "tou", "dx", "aea", "awa", "afi", "awi", "trn":
			result = append(result, a)
		case "chr":
			a.Label = "忠诚基础"
			result = append(result, a)
		case "luc":
			a.Label = "忠诚变化"
			a.Min = -10000
			a.Max = 10000
			result = append(result, a)
		}
	}
	return append(result,
		Attribute{Key: "growth_vi", Label: "体力成长基础", Min: 0, Max: 255},
		Attribute{Key: "growth_str", Label: "腕力成长基础", Min: 0, Max: 255},
		Attribute{Key: "growth_tou", Label: "耐力成长基础", Min: 0, Max: 255},
		Attribute{Key: "growth_dx", Label: "速度成长基础", Min: 0, Max: 255},
		Attribute{Key: "slt", Label: "技能格数", Min: 1, Max: 7},
		Attribute{Key: "llt", Label: "成长档次", Min: 0, Max: 5},
	)
}

func ValidateAttribute(kind, key string, value int64) error {
	for _, a := range Definitions(kind) {
		if a.Key == key {
			if value < a.Min || value > a.Max {
				return fmt.Errorf("%s超出允许范围", a.Label)
			}
			return nil
		}
	}
	return fmt.Errorf("不支持修改这个属性")
}

func SnapshotFromRecord(r *Record, revision string) (Snapshot, error) {
	return (&Document{Character: r, Revision: revision}).Snapshot()
}

func NewCharacter(name string) (*Record, error) {
	r, err := ParseCharacter([]byte("name=\n"))
	if err != nil {
		return nil, err
	}
	if err = r.SetText("name", name, 32); err != nil {
		return nil, err
	}
	return r, nil
}

func attributes(r *Record, definitions []Attribute) ([]Attribute, error) {
	result := make([]Attribute, 0, len(definitions))
	for _, def := range definitions {
		if _, ok := r.Raw(def.Key); !ok {
			continue
		}
		value, err := r.Integer(def.Key)
		if err != nil {
			return nil, err
		}
		def.Value = value
		result = append(result, def)
	}
	return result, nil
}

func (d *Document) Snapshot() (Snapshot, error) {
	transmigration := int64(0)
	if _, ok := d.Character.Raw("trn"); ok {
		var err error
		transmigration, err = d.Character.Integer("trn")
		if err != nil {
			return Snapshot{}, err
		}
	}
	result := Snapshot{
		Revision:    d.Revision,
		Possessions: []Possession{},
		Capacities: map[string]int{
			"item_inventory": 15,
			"item_warehouse": 30,
			"pet_inventory":  5,
			"pet_warehouse":  PetWarehouseCapacity(transmigration),
		},
	}
	var err error
	result.Name, err = d.Character.Text("name")
	if err != nil {
		return result, err
	}
	result.Attributes, err = attributes(d.Character, characterAttributes)
	if err != nil {
		return result, err
	}
	for _, group := range []struct {
		prefix, kind, location string
		count                  int
	}{
		{"item", "item", "inventory", 20}, {"poolitem", "item", "warehouse", 30},
		{"pet", "pet", "inventory", 5}, {"poolpet", "pet", "warehouse", 15},
	} {
		for slot := 0; slot < group.count; slot++ {
			raw, ok := d.Character.Raw(group.prefix + strconv.Itoa(slot))
			if !ok || len(raw) == 0 {
				continue
			}
			p := Possession{Kind: group.kind, Location: group.location, Slot: slot}
			if group.kind == "pet" {
				err = readPet(raw, &p)
			} else {
				err = readItem(raw, &p)
			}
			if err != nil {
				return result, fmt.Errorf("%s%d: %w", group.prefix, slot, err)
			}
			result.Possessions = append(result.Possessions, p)
		}
	}
	return result, nil
}

func readPet(raw []byte, p *Possession) error {
	r, err := ParsePet(raw)
	if err != nil {
		return err
	}
	p.Name, err = r.Text("name")
	if err != nil {
		return err
	}
	if custom, err := r.Text("ownt"); err == nil && custom != "" {
		p.Name = custom
	}
	p.ID, _ = r.Integer("dmswc")
	p.GraphicID, _ = r.Integer("bi")
	if p.GraphicID == 0 {
		p.GraphicID, _ = r.Integer("bbi")
	}
	p.Attributes, err = petAttributes(r)
	if err != nil {
		return err
	}
	p.Skills = make([]PetSkillSlot, 7)
	for i := range p.Skills {
		p.Skills[i] = PetSkillSlot{Slot: i, ID: -1}
		if _, ok := r.Raw("psk" + strconv.Itoa(i)); ok {
			p.Skills[i].ID, err = r.Integer("psk" + strconv.Itoa(i))
			if err != nil {
				return err
			}
		}
	}
	return nil
}

const (
	petGrowthVI  = "growth_vi"
	petGrowthStr = "growth_str"
	petGrowthTou = "growth_tou"
	petGrowthDX  = "growth_dx"
)

func petGrowthShift(field string) (uint, bool) {
	switch field {
	case petGrowthVI:
		return 24, true
	case petGrowthStr:
		return 16, true
	case petGrowthTou:
		return 8, true
	case petGrowthDX:
		return 0, true
	default:
		return 0, false
	}
}

func petAttributes(r *Record) ([]Attribute, error) {
	packed := uint32(0)
	hasPacked := false
	if _, ok := r.Raw("lvup"); ok {
		value, err := r.Integer("lvup")
		if err != nil {
			return nil, err
		}
		packed = uint32(int32(value))
		hasPacked = true
	}
	result := make([]Attribute, 0, len(Definitions("pet")))
	for _, definition := range Definitions("pet") {
		if shift, ok := petGrowthShift(definition.Key); ok {
			if !hasPacked {
				continue
			}
			definition.Value = int64((packed >> shift) & 0xff)
			result = append(result, definition)
			continue
		}
		if _, ok := r.Raw(definition.Key); !ok {
			continue
		}
		value, err := r.Integer(definition.Key)
		if err != nil {
			return nil, err
		}
		definition.Value = value
		result = append(result, definition)
	}
	return result, nil
}

// SetPetGrowth updates one byte of a pet's packed CHAR_ALLOCPOINT value while
// retaining the other three bytes. The signed int32 representation is the
// format used by the legacy record codec.
func SetPetGrowth(r *Record, field string, value int64) error {
	shift, ok := petGrowthShift(field)
	if !ok {
		return fmt.Errorf("unsupported pet growth field %s", field)
	}
	if value < 0 || value > 255 {
		return fmt.Errorf("pet growth field %s is outside 0..255", field)
	}
	packed := uint32(0)
	if _, present := r.Raw("lvup"); present {
		current, err := r.Integer("lvup")
		if err != nil {
			return err
		}
		packed = uint32(int32(current))
	}
	mask := uint32(0xff) << shift
	packed = (packed &^ mask) | (uint32(value) << shift)
	return r.SetInteger("lvup", int64(int32(packed)))
}

// PetWarehouseCapacity returns the number of usable warehouse pet slots for
// a character's transmigration count. Existing records beyond this limit are
// still readable; the value only limits new placements.
func PetWarehouseCapacity(transmigration int64) int {
	if transmigration <= 0 {
		return 5
	}
	if transmigration >= 5 {
		return 15
	}
	return int(5 + 2*transmigration)
}

func readItem(raw []byte, p *Possession) error {
	r, err := ParseItem(raw)
	if err != nil {
		return err
	}
	p.ID, err = r.Integer("id")
	if err != nil {
		return err
	}
	// Simplified item saves omit unchanged names/graphics. The catalog resolves
	// these from the template ID when the API assembles the complete response.
	if _, ok := r.Raw("na"); ok {
		p.Name, err = r.Text("na")
		if err != nil {
			return err
		}
	}
	p.GraphicID, _ = r.Integer("bi")
	p.Attributes, err = attributes(r, itemAttributes)
	return err
}
