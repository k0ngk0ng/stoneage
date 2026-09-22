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
		if strings.Contains(row[2], "1/14") {
			random++
		} else if strings.Contains(row[2], "固定") {
			fixed++
		}
	}
	if random != 14 || fixed != 3 {
		t.Fatal(random, fixed)
	}
	if rows := c.Entries["quest:n2"].Tables[0].Rows; len(rows) != 3 || !strings.Contains(rows[0][3], "只能选一件") {
		t.Fatal("dragon equipment selection", rows)
	}
	bandit := c.Entries["quest:n6"].Tables[0].Rows
	if len(bandit) != 6 {
		t.Fatal("bandit reward stages", bandit)
	}
	for _, row := range bandit[1:] {
		if !strings.Contains(row[2], "1/5") {
			t.Fatal(row)
		}
	}
	shell := c.Entries["quest:n9"].Tables[0].Rows
	if len(shell) != 2 || !strings.Contains(shell[0][2], "固定") || !strings.Contains(shell[1][3], "最后一枚") {
		t.Fatal("shell rewards depend on inventory, not random selection", shell)
	}
}

func TestNativeRewardListBufferBoundary(t *testing.T) {
	raw := "450,451,449,549,552,543,653,646,12623,12648,11895,11926,11936,12052,11803"
	got := nativeRewardIDs(raw, "GetRandItem")
	if len(got) != 14 || got[13] != 1 {
		t.Fatalf("native char[64] truncation not preserved: %v", got)
	}
	if got := nativeRewardIDs("1,2,", "GetRandItem"); len(got) != 3 || got[2] != 0 {
		t.Fatal(got)
	}
}

func TestRewardPoolTracksNativeReaderCapacity(t *testing.T) {
	path := "../../server/legacy/source/2.5/gmsv/npc/npc_exchangeman.c"
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skip("native archive unavailable")
	}
	source := string(raw)
	start := strings.Index(source, "BOOL NPC_EventAdd(int meindex,int talker,int mode)\n{")
	if start < 0 {
		t.Fatal("native reward reader changed; re-audit capacity")
	}
	declarations := source[start : start+300]
	if !strings.Contains(declarations, "char buf[64]") || !strings.Contains(declarations, "char buff2[128]") {
		t.Fatal("native reward reader capacity changed")
	}
	root := "../../server/legacy/source/2.5/gmsv/data"
	c, err := Load(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	rows := c.Entries["quest:n12"].Tables[0].Rows
	if len(rows) != 34 {
		t.Fatal("both 17-slot bags must be listed", len(rows))
	}
	for _, row := range rows {
		if !strings.Contains(row[2], "1/17") {
			t.Fatal(row)
		}
	}
}
