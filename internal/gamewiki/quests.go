package gamewiki

import (
	"encoding/json"
)

type QuestRewardPool struct {
	Name        string   `json:"name"`
	Candidates  []string `json:"candidates"`
	Quantity    string   `json:"quantity"`
	Probability string   `json:"probability"`
	Condition   string   `json:"condition"`
}

type Quest struct {
	RewardPools   []QuestRewardPool `json:"reward_pools,omitempty"`
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Group         string            `json:"group"`
	Version       string            `json:"version"`
	Summary       string            `json:"summary"`
	Prerequisites string            `json:"prerequisites"`
	Reward        string            `json:"reward"`
	Steps         []string          `json:"steps"`
	Sources       []Link            `json:"sources"`
	Notes         []string          `json:"notes"`
}

func (b *builder) addQuests() {
	b.fame = loadNativeQuestFame(b.k.DataDir)
	var quests []Quest
	if err := json.Unmarshal(questData, &quests); err != nil {
		panic("invalid embedded wiki quest catalog: " + err.Error())
	}
	for _, q := range quests {
		e := b.add("quest", q.ID, q.Name, q.Summary)
		e.Group = q.Group
		rewards := b.questRewards(e, q)
		if _, voucher := commissionRewardScript(q.ID); voucher != 0 && len(rewards.Rows) > 0 {
			q.Reward = "随机获得下表奖励池中的 1 件物品"
			if len(rewards.Rows) == 1 {
				q.Reward = rewards.Rows[0][0]
				if q.Reward == "石币" {
					q.Reward = rewards.Rows[0][1] + " 石币"
				}
			}
		}
		e.field("任务前提", q.Prerequisites)
		e.field("任务奖励", q.Reward)
		e.field("本服状态", "攻略已收录；未逐项完成本服全流程验收")
		e.Notes = append(e.Notes, q.Notes...)
		steps := Table{Title: "流程摘要", Columns: []string{"步骤", "说明"}}
		for i, s := range q.Steps {
			steps.Rows = append(steps.Rows, []string{text(i + 1), s})
		}
		e.Tables = append([]Table{rewards, steps}, e.Tables...)

	}
}
