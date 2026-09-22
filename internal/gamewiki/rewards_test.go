package gamewiki

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestQuestRewardsReflectWeightedNativePools(t *testing.T) {
	root := "../../server/legacy/source/2.5/gmsv/data"
	if _, err := os.Stat(root); err != nil {
		t.Skip("native archive unavailable")
	}
	c, err := Load(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range c.Entries {
		if e.Kind == "quest" && (len(e.Tables) == 0 || e.Tables[0].Title != "任务奖励" || len(e.Tables[0].Rows) == 0) {
			t.Fatal("missing quest reward", e.Key)
		}
	}
	marju := c.Entries["quest:b5"].Tables[0]
	if len(marju.Rows) != 3 {
		t.Fatal(marju.Rows)
	}
	for _, row := range marju.Rows {
		if !strings.Contains(row[2], "1/3") {
			t.Fatal(row)
		}
	}
	weighted := false
	for _, row := range c.Entries["quest:jot02"].Tables[0].Rows {
		if strings.Contains(row[2], "2/6") {
			weighted = true
		}
	}
	if !weighted {
		t.Fatal("duplicated pet must retain double probability")
	}
	dream := c.Entries["quest:s2"].Tables[0]
	random, fixed := 0, 0
	for _, row := range dream.Rows {
		if strings.Contains(row[2], "1/20") {
			random++
		} else if strings.Contains(row[2], "固定") {
			fixed++
		}
	}
	if random != 20 || fixed != 3 {
		t.Fatal(random, fixed)
	}
}
