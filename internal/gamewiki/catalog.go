// Package gamewiki builds a read-only, public encyclopedia from native game data.
// It never opens character archives, account files, or game connections.
package gamewiki

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
	"github.com/k0ngk0ng/stoneage/internal/gamecatalog"
	"golang.org/x/text/encoding/simplifiedchinese"
)

type Field struct {
	Label string `json:"label"`
	Value string `json:"value"`
}
type Link struct {
	Key  string `json:"key,omitempty"`
	Name string `json:"name"`
	URL  string `json:"url,omitempty"`
}
type Table struct {
	Title   string     `json:"title"`
	Columns []string   `json:"columns"`
	Rows    [][]string `json:"rows"`
}
type Summary struct {
	Key         string `json:"key"`
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Location    string `json:"location,omitempty"`
	Level       string `json:"level,omitempty"`
	HP          string `json:"hp,omitempty"`
	Skills      string `json:"skills,omitempty"`
	Group       string `json:"group,omitempty"`
	Issues      int    `json:"issues"`
}
type Entry struct {
	Summary
	Fields  []Field  `json:"fields"`
	Tables  []Table  `json:"tables"`
	Notes   []string `json:"notes"`
	Links   []Link   `json:"links"`
	Sources []string `json:"sources"`
}
type Catalog struct {
	Entries  map[string]*Entry
	List     []Summary
	Counts   map[string]int
	LoadedAt time.Time
	Revision string
	Notes    []string
}
type builder struct {
	c       *Catalog
	k       *aiknowledge.Knowledge
	catalog *gamecatalog.Catalog
	bases   map[int]aiknowledge.EnemyBase
	enemies map[int]aiknowledge.Enemy
	skills  map[int]gamecatalog.PetSkill
	items   map[int]gamecatalog.Item
	maps    map[int]*Entry
	files   map[string]aiknowledge.NPCFile
}

func key(kind string, id any) string { return fmt.Sprintf("%s:%v", kind, id) }
func text(v any) string              { return fmt.Sprint(v) }
func span(a, b int) string {
	if a == b {
		return text(a)
	}
	return fmt.Sprintf("%d–%d", a, b)
}
func point(r aiknowledge.Rectangle) string {
	if r.X == r.X2 && r.Y == r.Y2 {
		return fmt.Sprintf("(%d, %d)", r.X, r.Y)
	}
	return fmt.Sprintf("(%d, %d)–(%d, %d)", r.X, r.Y, r.X2, r.Y2)
}
func source(s aiknowledge.SourceRef) string {
	if s.Line > 0 {
		return fmt.Sprintf("%s:%d", s.Path, s.Line)
	}
	return s.Path
}
func decode(raw []byte) string {
	if utf8.Valid(raw) {
		return string(raw)
	}
	v, _ := simplifiedchinese.GBK.NewDecoder().Bytes(raw)
	return string(v)
}
func atoi(s string) int {
	s = strings.TrimSpace(s)
	i := 0
	for i < len(s) && (s[i] >= '0' && s[i] <= '9' || i == 0 && (s[i] == '-' || s[i] == '+')) {
		i++
	}
	n, _ := strconv.Atoi(s[:i])
	return n
}
func ids(s string) []int {
	var out []int
	for _, v := range strings.Split(s, ",") {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n >= 0 {
			out = append(out, n)
		}
	}
	return out
}
func (e *Entry) field(label string, v any) { e.Fields = append(e.Fields, Field{label, text(v)}) }
func (e *Entry) link(kind string, id any, name string) {
	e.Links = append(e.Links, Link{Key: key(kind, id), Name: name})
}
func (b *builder) add(kind string, id any, name, description string) *Entry {
	e := &Entry{Summary: Summary{Key: key(kind, id), Kind: kind, Name: name, Description: description}}
	b.c.Entries[e.Key] = e
	return e
}
func (b *builder) mapName(id int) string {
	if e := b.maps[id]; e != nil {
		return fmt.Sprintf("%s [%d]", e.Name, id)
	}
	return fmt.Sprintf("未知地图 [%d]", id)
}
func itemKind(i gamecatalog.Item) string {
	if i.Type >= 1 && i.Type <= 15 || i.Type >= 17 && i.Type <= 19 {
		return "equipment"
	}
	return "item"
}
func (b *builder) itemLink(e *Entry, id int) {
	if i, ok := b.items[id]; ok {
		e.link(itemKind(i), id, i.Name)
	}
}
func (b *builder) skillNames(ids []int) (string, []string) {
	var names, missing []string
	seen := map[int]bool{}
	for _, id := range ids {
		if id < 0 || seen[id] {
			continue
		}
		seen[id] = true
		if s, ok := b.skills[id]; ok {
			names = append(names, fmt.Sprintf("%s [%d]", s.Name, id))
		} else {
			names = append(names, fmt.Sprintf("未定义技能 [%d]", id))
			missing = append(missing, fmt.Sprintf("技能 %d 在当前技能表中缺失", id))
		}
	}
	return strings.Join(names, "、"), missing
}

// Load reads only game tables and map headers below the configured native data root.
func Load(ctx context.Context, root string) (*Catalog, error) {
	group := "group.txt"
	// The deployed native setup uses group1.txt. Read only this one setting;
	// setup.cf itself (which can contain credentials) is never exposed.
	if raw, err := os.ReadFile(filepath.Join(root, "..", "setup.cf")); err == nil {
		for _, line := range strings.Split(string(raw), "\n") {
			k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
			if ok && k == "groupfile" {
				name := filepath.Base(strings.TrimSpace(v))
				if name == "group.txt" || name == "group1.txt" {
					group = name
				}
			}
		}
	}
	k, err := aiknowledge.Load(ctx, aiknowledge.Options{DataDir: root, GroupFile: group})
	if err != nil {
		return nil, err
	}
	catalog, err := gamecatalog.LoadDataDir(k.DataDir)
	if err != nil {
		return nil, err
	}
	b := &builder{c: &Catalog{Entries: map[string]*Entry{}, Counts: map[string]int{}, LoadedAt: time.Now().UTC()}, k: k, catalog: catalog, bases: map[int]aiknowledge.EnemyBase{}, enemies: map[int]aiknowledge.Enemy{}, skills: map[int]gamecatalog.PetSkill{}, items: map[int]gamecatalog.Item{}, maps: map[int]*Entry{}, files: map[string]aiknowledge.NPCFile{}}
	for _, x := range k.EnemyBases {
		b.bases[x.TemplateID] = x
	}
	for _, x := range k.EnemiesTable {
		b.enemies[x.ID] = x
	}
	for _, x := range catalog.PetSkills {
		b.skills[x.ID] = x
	}
	for _, x := range catalog.Items {
		b.items[x.ID] = x
	}
	for _, x := range k.NPC.Files {
		b.files[x.Path] = x
	}
	if err = b.loadMaps(ctx); err != nil {
		return nil, err
	}
	b.addSkills()
	b.addItems()
	if err = b.enrichItems(); err != nil {
		return nil, err
	}
	b.addPets()
	b.addEnemies()
	b.addNPCs()
	b.addEncounters()
	b.addQuests()
	b.addGameTasks()
	b.addGuides()
	b.c.Notes = []string{"游戏数据来自当前挂载的服务端资料；静态配置不等于已经实测可完成。", "血量为怪物创建时的满血基础理论范围，非当前剩余HP；攻防敏不含装备、技能等运行时修正。", "历史任务覆盖17173索引的2.0、2.5、早期各岛、转生洞窟及JOT/SOT栏目；攻略版本与本服验证状态分别标注。", "野外敌群表：" + group + "。NPC包括道场、宝物袋及任务战斗，不将它们全部称为Boss。"}
	for _, e := range b.c.Entries {
		seen := map[string]bool{}
		links := e.Links[:0]
		for _, l := range e.Links {
			if l.Key != "" && b.c.Entries[l.Key] == nil {
				continue
			}
			id := l.Key + l.URL
			if !seen[id] {
				links = append(links, l)
				seen[id] = true
			}
		}
		e.Links = links
		e.Issues = len(e.Notes)
		b.c.Counts[e.Kind]++
		b.c.List = append(b.c.List, e.Summary)
	}
	sort.Slice(b.c.List, func(i, j int) bool {
		a, z := b.c.List[i], b.c.List[j]
		if a.Kind != z.Kind {
			return a.Kind < z.Kind
		}
		return a.Key < z.Key
	})
	// Include catalog tables and the embedded guide revision as well as knowledge.
	h := sha256.New()
	h.Write([]byte(k.Digest))
	h.Write(questData)
	raw, _ := json.Marshal(catalog)
	h.Write(raw)
	b.c.Revision = hex.EncodeToString(h.Sum(nil))[:16]
	return b.c, nil
}

func (b *builder) loadMaps(ctx context.Context) error {
	return filepath.WalkDir(filepath.Join(b.k.DataDir, "map"), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		var h [44]byte
		_, err = io.ReadFull(f, h[:])
		f.Close()
		if err != nil || string(h[:6]) != "LS2MAP" {
			return nil
		}
		id := int(binary.BigEndian.Uint16(h[6:8]))
		name := strings.Split(decode([]byte(strings.SplitN(string(h[8:40]), "\x00", 2)[0])), "|")[0]
		rel, _ := filepath.Rel(b.k.DataDir, path)
		if old := b.maps[id]; old != nil {
			old.Notes = append(old.Notes, "相同地图编号有多个文件，名称/尺寸可能存在版本差异")
			old.Sources = append(old.Sources, filepath.ToSlash(rel))
			return nil
		}
		e := b.add("map", id, name, "服务端地图、NPC、野外遭遇与传送出口")
		e.field("地图编号", id)
		e.field("宽 × 高", fmt.Sprintf("%d × %d", binary.BigEndian.Uint16(h[40:42]), binary.BigEndian.Uint16(h[42:44])))
		e.Sources = []string{filepath.ToSlash(rel)}
		b.maps[id] = e
		return nil
	})
}
func (b *builder) addSkills() {
	for _, s := range b.catalog.PetSkills {
		e := b.add("skill", s.ID, s.Name, s.Description)
		e.field("技能编号", s.ID)
		e.field("使用范围", s.Field)
		e.field("目标类型", s.Target)
		e.field("使用类型", s.UseType)
		e.field("表内价格", s.Cost)
		e.field("处理函数", s.Function)
		e.Sources = []string{fmt.Sprintf("petskill.txt（记录%d）", s.RecordIndex+1)}
	}
}
func (b *builder) addItems() {
	for _, i := range b.catalog.Items {
		e := b.add(itemKind(i), i.ID, i.Name, i.Description)
		e.field("物品编号", i.ID)
		e.field("类型", itemType(i.Type))
		e.field("等级要求", i.Level)
		e.Level = text(i.Level)
		e.field("表内价格", i.Cost)
		e.field("图号", i.GraphicID)
		e.field("鉴定名称", i.SecretName)
		e.field("使用范围代码", i.Field)
		e.field("目标代码", i.Target)
		e.Sources = []string{fmt.Sprintf("itemset.txt（记录%d）", i.RecordIndex+1)}
	}
}
func itemType(t int) string {
	names := []string{"徒手", "斧", "棍", "枪", "弓", "盾", "头盔", "铠甲", "手镯", "乐器", "项链", "戒指", "腰带", "耳环", "鼻环", "护身符", "其他", "回旋镖", "投掷斧", "投掷石", "料理"}
	if t >= 0 && t < len(names) {
		return fmt.Sprintf("%s [%d]", names[t], t)
	}
	return text(t)
}
func (b *builder) addPets() {
	for _, p := range b.k.EnemyBases {
		e := b.add("pet", p.TemplateID, p.Name, "宠物/生物基础模板；是否可捕捉还取决于遭遇与游戏规则")
		e.field("模板编号", p.TemplateID)
		e.field("图号", p.ImageID)
		e.field("初始点数", p.InitNum)
		e.field("成长参数（服务端整数值）", p.LevelUpPoint)
		e.field("基础体/腕/耐/速", fmt.Sprintf("%d / %d / %d / %d", p.BaseVital, p.BaseStr, p.BaseTough, p.BaseDex))
		e.field("地/水/火/风", fmt.Sprint(p.Elements))
		e.field("毒/麻痹/睡眠/石化/酒醉/混乱抗性", fmt.Sprint(p.Resistances))
		e.field("技能槽", p.Slot)
		e.field("捕捉参数", p.Get)
		e.field("宠物标记", p.PetFlag)
		e.field("原表等级限制", p.Pet.LimitLevel)
		e.field("暴击/反击参数", fmt.Sprintf("%d / %d", p.Critical, p.Counter))
		e.Skills, e.Notes = b.skillNames(p.PetSkillSlots[:])
		e.field("模板技能", e.Skills)
		for _, i := range p.PetSkillSlots {
			if s, ok := b.skills[i]; ok {
				e.link("skill", i, s.Name)
			}
		}
		e.Sources = []string{source(p.Source)}
	}
}

// NativeStats models ENEMY_createEnemy followed by CHAR_initcharWorkInt.
// The four independent [-2,+2] jitters precede ten allocations shared by
// the four stats. Min/max therefore cannot give all four stats +10 at once.
func NativeStats(p aiknowledge.EnemyBase, levels aiknowledge.Range) (hp, attack, defense, speed aiknowledge.Range, ok bool) {
	if levels.Min < 1 || levels.Max < levels.Min || p.LevelUpPoint < 0 || p.InitNum < 0 || p.BaseVital < 2 || p.BaseStr < 2 || p.BaseTough < 2 || p.BaseDex < 2 {
		return
	}
	k1 := (int64(levels.Min)-1)*int64(p.LevelUpPoint) + int64(p.InitNum)
	k2 := (int64(levels.Max)-1)*int64(p.LevelUpPoint) + int64(p.InitNum)
	// Refuse values beyond native signed-int arithmetic instead of inventing stats.
	if k2 > 1_000_000 || max(p.BaseVital, p.BaseStr, p.BaseTough, p.BaseDex) > 10000 {
		return
	}
	v, s, t, d := int64(p.BaseVital), int64(p.BaseStr), int64(p.BaseTough), int64(p.BaseDex)
	hp = aiknowledge.Range{Min: int(float32(float64(k1*(4*v+s+t+d-4)) * 0.01)), Max: int(float32(float64(k2*(4*v+s+t+d+54)) * 0.01))}
	attack = aiknowledge.Range{Min: int(k1 * (2*v + 20*s + 2*t + d - 40) / 2000), Max: int(k2 * (2*v + 20*s + 2*t + d + 250) / 2000)}
	defense = aiknowledge.Range{Min: int(k1 * (2*v + 2*s + 20*t + d - 40) / 2000), Max: int(k2 * (2*v + 2*s + 20*t + d + 250) / 2000)}
	speed = aiknowledge.Range{Min: int(k1 * (d - 2) / 100), Max: int(k2 * (d + 12) / 100)}
	ok = true
	return
}
func rangeText(r aiknowledge.Range) string { return span(r.Min, r.Max) }
func (b *builder) addEnemies() {
	for _, n := range b.k.EnemiesTable {
		p, ok := b.bases[n.TemplateID]
		e := b.add("enemy", n.ID, n.Name, "敌人出战配置")
		e.Level = rangeText(n.Levels)
		e.field("敌人编号", n.ID)
		e.field("等级", e.Level)
		e.field("战斗策略", n.TacticsOption)
		e.field("经验配置", n.Experience)
		e.field("捕捉标记", n.PetFlag)
		e.field("基础模板", n.TemplateID)
		if ok {
			e.Name = p.Name
			e.field("出战配置名称", n.Name)
			hp, a, d, s, valid := NativeStats(p, n.Levels)
			if valid {
				e.HP = rangeText(hp)
				e.field("满血基础HP", e.HP)
				e.field("基础攻击", rangeText(a))
				e.field("基础防御", rangeText(d))
				e.field("基础敏捷", rangeText(s))
			}
			e.Skills, e.Notes = b.skillNames(p.PetSkillSlots[:])
			e.field("模板技能", e.Skills)
			e.link("pet", p.TemplateID, p.Name)
			b.c.Entries[key("pet", p.TemplateID)].link("enemy", n.ID, n.Name)
		} else {
			e.Notes = append(e.Notes, "基础模板缺失")
		}
		drops := Table{Title: "物品生成概率", Columns: []string{"物品", "编号", "原概率参数", "生成概率（非最终必得率）"}}
		for _, d := range n.Drops {
			if d.ItemID < 0 || d.Probability <= 0 {
				continue
			}
			name := "未定义物品"
			if i, ok := b.items[d.ItemID]; ok {
				name = i.Name
				b.itemLink(e, d.ItemID)
				b.c.Entries[key(itemKind(i), i.ID)].link("enemy", n.ID, e.Name)
			}
			drops.Rows = append(drops.Rows, []string{name, text(d.ItemID), text(d.Probability), fmt.Sprintf("%.1f%%", float64(min(d.Probability, 1000))/10)})
		}
		if len(drops.Rows) > 0 {
			e.Tables = append(e.Tables, drops)
		}
		e.Sources = []string{source(n.Source)}
	}
}
