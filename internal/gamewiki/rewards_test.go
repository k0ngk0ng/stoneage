package gamewiki

import (
	"context"
	"fmt"
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

func TestCommissionRewardsUseActiveNativeBranches(t *testing.T) {
	root := "../../server/legacy/source/2.5/gmsv/data"
	if _, err := os.Stat(root); err != nil {
		t.Skip("native archive unavailable")
	}
	c, err := Load(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	for village := 1; village <= 4; village++ {
		for _, kind := range []string{"pet", "food"} {
			for _, letter := range "ABCDEF" {
				key := fmt.Sprintf("quest:wt%02d-%s-%c", village, kind, letter)
				rows := c.Entries[key].Tables[0].Rows
				if len(rows) == 0 || strings.Contains(rows[0][2], "未核") {
					t.Fatal(key, rows)
				}
			}
		}
		rows := c.Entries[fmt.Sprintf("quest:wt%02d-pet-F", village)].Tables[0].Rows
		want := "15000"
		if village >= 3 {
			want = "18000"
		}
		if len(rows) != 1 || rows[0][0] != "石币" || rows[0][1] != want {
			t.Fatal(village, rows)
		}
	}
	for _, key := range []string{"quest:wt01-pet-E", "quest:wt04-pet-E"} {
		rows := c.Entries[key].Tables[0].Rows
		if len(rows) != 11 {
			t.Fatal(key, rows)
		}
		for _, row := range rows {
			if !strings.Contains(row[2], "1/11") {
				t.Fatal(key, row)
			}
		}
	}
	if rows := c.Entries["quest:wt03-pet-D"].Tables[0].Rows; len(rows) != 1 || rows[0][2] != "发奖分支已停用" {
		t.Fatal("commented-out reward must not be offered", rows)
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

func TestQuestStagesQuantitiesAndFame(t *testing.T) {
	root := "../../server/legacy/source/2.5/gmsv/data"
	if _, err := os.Stat(root); err != nil {
		t.Skip("native archive unavailable")
	}
	c, err := Load(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	for id := range questRewardScripts {
		rows := c.Entries["quest:"+id].Tables[0].Rows
		if len(rows) == 0 {
			t.Fatal("mapped quest has no reward", id)
		}
	}
	fish := c.Entries["quest:n13"].Tables[0].Rows
	if len(fish) != 9 || fish[1][1] != "2 件" {
		t.Fatal("fishing reward quantity", fish)
	}
	grapes := c.Entries["quest:n8"].Tables[0].Rows
	if len(grapes) != 5 {
		t.Fatal("bait is not a final reward", grapes)
	}
	for _, row := range grapes[:4] {
		if !strings.Contains(row[2], "1/4") || !strings.Contains(row[0], "[245") {
			t.Fatal("distinct grapes need ID and probability", row)
		}
	}
	doctor := c.Entries["quest:b2"].Tables[0].Rows
	if len(doctor) != 9 || !strings.Contains(doctor[0][2], "1/8") || !strings.Contains(doctor[8][3], "30") {
		t.Fatal("doctor stages", doctor)
	}
	blessing := c.Entries["quest:news_10"].Tables[0].Rows
	if blessing[1][1] != "5 件" {
		t.Fatal("five blessings for the complete set", blessing)
	}
	king := c.Entries["quest:sa25_04"]
	found := false
	for _, table := range king.Tables {
		if table.Title == "声望奖励" {
			found = true
			if len(table.Rows) != 2 || table.Rows[0][1] != "150.00" || table.Rows[1][1] != "200.00" {
				t.Fatal(table.Rows)
			}
		}
	}
	if !found {
		t.Fatal("missing first-completion fame rewards")
	}
}
