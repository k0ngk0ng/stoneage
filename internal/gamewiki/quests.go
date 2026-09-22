package gamewiki

import (
	"encoding/json"
)

type Quest struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Group         string   `json:"group"`
	Version       string   `json:"version"`
	Summary       string   `json:"summary"`
	Prerequisites string   `json:"prerequisites"`
	Reward        string   `json:"reward"`
	Steps         []string `json:"steps"`
	Sources       []Link   `json:"sources"`
	Notes         []string `json:"notes"`
}

func (b *builder) addQuests() {
	var quests []Quest
	if err := json.Unmarshal(questData, &quests); err != nil {
		panic("invalid embedded wiki quest catalog: " + err.Error())
	}
	for _, q := range quests {
		e := b.add("quest", q.ID, q.Name, q.Summary)
		e.Group = q.Group
		e.field("任务前提", q.Prerequisites)
		e.field("任务奖励", q.Reward)
		e.field("本服状态", "攻略已收录；未逐项完成本服全流程验收")
		e.Notes = append(e.Notes, q.Notes...)
		steps := Table{Title: "流程摘要", Columns: []string{"步骤", "说明"}}
		for i, s := range q.Steps {
			steps.Rows = append(steps.Rows, []string{text(i + 1), s})
		}
		e.Tables = append(e.Tables, b.questRewards(e, q), steps)

	}
}
