package gamewiki

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Column positions follow ITEM_itemconfentries in the checked-in 2.5 server.
// INTFUNC random attributes occupy two columns; scalar attributes occupy one.
func (b *builder) enrichItems() error {
	f, err := os.Open(filepath.Join(b.k.DataDir, "itemset.txt"))
	if err != nil {
		return err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(decode(scanner.Bytes()))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		r := strings.Split(line, ",")
		if len(r) < 84 {
			continue
		}
		id := atoi(r[16])
		item, ok := b.items[id]
		if !ok {
			continue
		}
		e := b.c.Entries[key(itemKind(item), id)]
		if e == nil {
			continue
		}
		t := Table{Title: "物品生成属性（原表范围）", Columns: []string{"属性", "数值"}}
		for _, p := range []struct {
			name string
			col  int
		}{{"攻击", 37}, {"防御", 39}, {"敏捷", 41}, {"HP", 43}, {"MP", 45}, {"幸运", 47}, {"魅力", 49}, {"闪避", 51}, {"毒抗性", 63}, {"麻痹抗性", 65}, {"睡眠抗性", 67}, {"石化抗性", 69}, {"酒醉抗性", 71}, {"混乱抗性", 73}, {"暴击", 75}} {
			a, z := atoi(r[p.col]), atoi(r[p.col+1])
			if a != 0 || z != 0 {
				t.Rows = append(t.Rows, []string{p.name, span(a, z)})
			}
		}
		if len(t.Rows) > 0 {
			e.Tables = append(e.Tables, t)
		}
		e.field("攻击次数", span(atoi(r[35]), atoi(r[36])))
		e.field("耐久损耗 / 上限原值", fmt.Sprintf("%d / %d", atoi(r[30]), atoi(r[31])))
		if strings.TrimSpace(r[55]) != "" && atoi(r[55]) >= 0 {
			e.field("精灵魔法编号", atoi(r[55]))
			e.field("魔法耗气", atoi(r[57]))
		}
		e.field("登出掉落", atoi(r[78]) == 1)
		e.field("丢弃消失", atoi(r[79]) == 1)
		e.field("宠物邮件可寄", atoi(r[81]) == 1)
		if strings.TrimSpace(r[3]) != "" {
			e.field("效果参数", r[3])
		}
	}
	return scanner.Err()
}
