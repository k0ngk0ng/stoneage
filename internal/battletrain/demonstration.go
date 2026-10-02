package battletrain

import (
	"context"
	"fmt"
	"reflect"
	"sort"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/battlenet"
	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
)

// Demonstrations carry observable decisions and acknowledged submissions.
// They have no behavior probabilities, hidden enemy setup, bootstrap or
// reward. Never reinterpret them as native Trajectory/PPO evidence.
const DemonstrationSchema = "commander-demonstration-v1"

type DemonstratedSubmission struct {
	Member    int                    `json:"member"`
	Actor     string                 `json:"actor"`
	Status    string                 `json:"status"`
	Selection aigame.BattleSelection `json:"selection"`
}

type DemonstratedTurn struct {
	RecordID      int64                    `json:"record_id"`
	Frame         battlepolicy.Frame       `json:"frame"`
	Choices       []int                    `json:"choices"`
	Observations  []aigame.BattleView      `json:"observations"`
	History       aigame.BattleEventBatch  `json:"history"`
	InitialCursor uint64                   `json:"initial_cursor,omitempty"`
	Submissions   []DemonstratedSubmission `json:"submissions"`
}

type Demonstration struct {
	Schema       string             `json:"schema"`
	Source       string             `json:"source_database_sha256"`
	Match        string             `json:"match"`
	Group        string             `json:"group"`
	GroupKind    string             `json:"group_kind"`
	Roster       []string           `json:"roster"`
	Rules        string             `json:"rules"`
	Platform     string             `json:"platform"`
	Features     string             `json:"features"`
	Mode         int                `json:"mode"`
	Side         int                `json:"side"`
	Policy       string             `json:"policy"`
	Strategy     string             `json:"strategy"`
	Winner       int                `json:"winner"`
	Reason       string             `json:"reason"`
	Turns        int                `json:"turns"`
	ResultDigest string             `json:"result_digest"`
	Steps        []DemonstratedTurn `json:"steps"`
}

// This groups repeated games by the actual recorded roster. It deliberately
// does not claim to recover the enemy's hidden allocation/configuration.
func DemonstrationGroup(mode int, roster []string) string {
	id, _ := Digest(struct {
		Kind   string   `json:"kind"`
		Mode   int      `json:"mode"`
		Roster []string `json:"roster"`
	}{"recorded-roster-v1", mode, roster})
	return id
}

func (d Demonstration) Validate() error {
	if d.Schema != DemonstrationSchema || !digest(d.Source) || !digest(d.Rules) || !digest(d.Group) || !digest(d.ResultDigest) || d.GroupKind != "recorded-roster-v1" || d.Match == "" || d.Platform == "" || d.Policy == "" || d.Strategy == "" || !battlepolicy.SupportedFeatures(d.Features) || d.Mode < 1 || d.Mode > 5 || d.Side < 0 || d.Side > 1 || d.Reason != "defeat" || d.Winner < 0 || d.Winner > 1 || d.Turns < 1 || d.Turns > 10000 || len(d.Steps) != d.Turns {
		return fmt.Errorf("invalid recorded demonstration envelope")
	}
	if len(d.Roster) != d.Mode*2 || d.Group != DemonstrationGroup(d.Mode, d.Roster) {
		return fmt.Errorf("recorded roster group differs from source roster")
	}
	roster := map[string]bool{}
	for i, id := range d.Roster {
		if id == "" || i > 0 && id <= d.Roster[i-1] {
			return fmt.Errorf("recorded roster must be sorted and unique")
		}
		roster[id] = true
	}
	var stream string
	var cursor uint64
	var recordID int64
	var observer int32 = -1
	var characters []string
	for turn, s := range d.Steps {
		if s.RecordID <= recordID || len(s.Observations) != d.Mode || len(s.Submissions) != len(s.Choices) || s.Frame.Match != d.Match || s.Frame.Turn != int32(turn) || s.Frame.Side != d.Side || s.Frame.Mode != d.Mode || s.Frame.Schema != d.Features {
			return fmt.Errorf("incomplete or mixed demonstration at turn %d", turn)
		}
		recordID = s.RecordID
		if turn == 0 {
			characters = make([]string, d.Mode)
		}
		for member, view := range s.Observations {
			clock := view.Battle.Clock
			if clock.RulesVersion != "stoneage-native-ladder-v1" || clock.RulesDigest != d.Rules || clock.EnginePlatform != d.Platform || !roster[view.CharacterID] {
				return fmt.Errorf("recorded rules or character identity missing")
			}
			if turn == 0 {
				characters[member] = view.CharacterID
			}
			if characters[member] != view.CharacterID {
				return fmt.Errorf("recorded member identity changed")
			}
		}
		if turn == 0 {
			observer = s.History.Observation.Battle.MyNo
		}
		if s.History.Observation.Battle.MyNo != observer {
			return fmt.Errorf("recorded history observer changed")
		}
		h := battlepolicy.History{Batch: &s.History, PreviousStream: stream, PreviousCursor: cursor, First: turn == 0, InitialCursor: s.InitialCursor}
		frame, err := battlepolicy.EncodeVersion(s.Observations, h, d.Features)
		if err != nil {
			return fmt.Errorf("demonstration turn %d: %w", turn, err)
		}
		if frame.Events[12] != 0 || !reflect.DeepEqual(frame, s.Frame) {
			return fmt.Errorf("recorded frame or history provenance differs at turn %d", turn)
		}
		if _, err := frame.Selections(s.Choices); err != nil {
			return err
		}
		for i, slot := range frame.Slots {
			actual := s.Submissions[i]
			want := aigame.BattleSelection{MatchID: d.Match, Turn: int32(turn), ObservationID: slot.Observation, CandidateID: slot.Candidates[s.Choices[i]].ID}
			if actual.Status != "written" || actual.Member != slot.Member || actual.Actor != slot.Actor || actual.Selection != want {
				return fmt.Errorf("demonstrated action does not match written intent")
			}
		}
		stream, cursor = s.History.Stream, s.History.Cursor
	}
	return nil
}

// ImitateDemonstrations learns recorded choices, regardless of win/loss. The
// source must have been explicitly selected for imitation; a recorded winner
// is not automatically a good teacher. Value-head supervision is not added.
// Roster groups here are not synthetic ScenarioGroup identities and cannot be
// substituted into a native experiment's independent-evaluation manifest.
func ImitateDemonstrations(ctx context.Context, model *battlenet.Model[float32], optimizer *battlenet.Adam[float32], episodes []Demonstration, config ImitationConfig) (ImitationReport, error) {
	var report ImitationReport
	if err := config.validate(); err != nil {
		return report, err
	}
	if err := (LearningState{Schema: 1, Model: model, Optimizer: optimizer}).Validate(); err != nil {
		return report, err
	}
	if len(episodes) == 0 || len(episodes) > 10000 {
		return report, fmt.Errorf("demonstration update requires 1..10000 complete matches")
	}
	var err error
	report.BeforePolicy, err = ModelDigest(model)
	if err != nil {
		return report, err
	}
	report.Episodes = len(episodes)
	seen, teachers := map[string]bool{}, map[string]bool{}
	sequences := make([][]imitationStep, 0, len(episodes))
	for _, d := range episodes {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		if err := d.Validate(); err != nil {
			return report, err
		}
		first := episodes[0]
		key := fmt.Sprintf("%s:%d", d.Match, d.Side)
		if seen[key] || d.Rules != first.Rules || d.Platform != first.Platform || d.Mode != first.Mode || d.Features != first.Features || d.Features != battlepolicy.NetworkFeatures(model.Config) {
			return report, fmt.Errorf("duplicate or incompatible demonstration")
		}
		seen[key], teachers[d.Strategy+":"+d.Policy] = true, true
		sequence := make([]imitationStep, 0, len(d.Steps))
		for _, s := range d.Steps {
			sequence = append(sequence, imitationStep{s.Frame, s.Choices})
			report.Actions += len(s.Choices)
		}
		report.TeamTurns += len(sequence)
		sequences = append(sequences, sequence)
	}
	for teacher := range teachers {
		report.Teachers = append(report.Teachers, teacher)
	}
	sort.Strings(report.Teachers)
	return imitateValidated(ctx, model, optimizer, sequences, config, report)
}
