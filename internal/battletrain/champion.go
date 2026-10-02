package battletrain

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"

	"github.com/k0ngk0ng/stoneage/internal/battleenv"
	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
)

// A registry is scoped to one controlled native scenario family and mode.
// Its winner remains an ordinary candidate artifact for the online client;
// this registry does not claim full arena execution/parity certification.
type ChampionRegistry struct {
	MixedConditions []EvaluationConfig `json:"mixed_conditions,omitempty"`
	Schema          string             `json:"schema"`
	Environment     battleenv.Metadata `json:"environment"`
	Conditions      EvaluationConfig   `json:"conditions"`
	Gate            PromotionGate      `json:"gate"`
	Rules           map[string]string  `json:"rule_versions"`
}

type ChampionAttempt struct {
	Mixed      *MixedExperiment `json:"mixed_experiment,omitempty"`
	Schema     string           `json:"schema"`
	Registry   string           `json:"registry"`
	Previous   string           `json:"previous_event,omitempty"`
	Champion   string           `json:"previous_champion,omitempty"`
	Candidate  string           `json:"candidate_artifact"`
	Experiment Experiment       `json:"experiment"`
	Number     int              `json:"attempt_number"`
}

type ChampionEvent struct {
	Schema     string               `json:"schema"`
	Registry   string               `json:"registry"`
	Previous   string               `json:"previous_event,omitempty"`
	Kind       string               `json:"kind"`
	Attempt    string               `json:"attempt,omitempty"`
	Report     string               `json:"report_digest,omitempty"`
	Assessment *PromotionAssessment `json:"assessment,omitempty"`
	Champion   string               `json:"champion_artifact,omitempty"`
	Target     string               `json:"rollback_event,omitempty"`
	Reason     string               `json:"reason,omitempty"`
}

type ChampionStatus struct {
	Purpose  string           `json:"purpose"`
	Registry ChampionRegistry `json:"registry"`
	Head     string           `json:"head,omitempty"`
	Champion string           `json:"champion_artifact,omitempty"`
	Model    string           `json:"model,omitempty"`
	Attempts int              `json:"attempts"`
	Events   []ChampionEvent  `json:"events"`
	Pending  string           `json:"pending_attempt,omitempty"`
	EventIDs []string         `json:"event_ids"`
	used     map[string]bool
}

func championConditions(x Experiment) EvaluationConfig {
	c := experimentEvaluationConfig(x, "test")
	c.Seed, c.MatchesPerOpponent = 0, 0
	return c
}

func (r ChampionRegistry) Validate() error {
	if len(r.MixedConditions) > 0 {
		return r.validateMixed()
	}
	if len(r.Rules) != len(promotionRules()) {
		return fmt.Errorf("invalid frozen champion rule pool")
	}
	for _, rule := range promotionRules() {
		version, e := (Policy{Rule: rule}).Version()
		if e != nil || r.Rules[rule] != version {
			return fmt.Errorf("champion rule implementation changed; use a new registry")
		}
	}
	if r.Schema != championSchema(r.Conditions.Pairing) || r.Conditions.Seed != 0 || r.Conditions.MatchesPerOpponent != 0 {
		return fmt.Errorf("invalid native champion registry")
	}
	if e := r.Environment.Validate(); e != nil {
		return e
	}
	c := r.Conditions
	x := Experiment{Pairing: c.Pairing, Schema: experimentSchema(c.Pairing), Environment: r.Environment, Mode: c.Mode, Points: c.Points, PetPoints: c.PetPoints, Level: c.Level, MaxTurns: c.MaxTurns, HealingMagic: c.HealingMagic, HealingItems: c.HealingItems, ReservePets: c.ReservePets, PetSkillMask: c.PetSkillMask}
	if e := x.validateSettings(); e != nil {
		return e
	}
	return r.Gate.Validate()
}

func championLock(root string) (*os.File, error) {
	if root == "" {
		return nil, fmt.Errorf("champion directory required")
	}
	if e := os.MkdirAll(root, 0700); e != nil {
		return nil, e
	}
	f, e := os.OpenFile(filepath.Join(root, ".champion.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	if e = lockTraining(f); e != nil {
		f.Close()
		return nil, fmt.Errorf("champion registry is busy: %w", e)
	}
	return f, nil
}

func InitChampionRegistry(root string, x Experiment, gate PromotionGate) error {
	if e := x.Validate(); e != nil {
		return e
	}
	r := ChampionRegistry{Schema: championSchema(x.Pairing), Environment: x.Environment, Conditions: championConditions(x), Gate: gate, Rules: map[string]string{}}
	for _, rule := range promotionRules() {
		version, e := (Policy{Rule: rule}).Version()
		if e != nil {
			return e
		}
		r.Rules[rule] = version
	}
	if e := r.Validate(); e != nil {
		return e
	}
	lock, e := championLock(root)
	if e != nil {
		return e
	}
	defer lock.Close()
	return writeObject(filepath.Join(root, "registry.json"), r)
}

func championObject(root, category, id string, value any) error {
	if !digest(id) {
		return fmt.Errorf("invalid champion object identity")
	}
	if e := readObject(filepath.Join(root, category, id+".json"), value, mixedExperimentBytes+(32<<20)); e != nil {
		return e
	}
	actual, e := Digest(value)
	if e != nil || actual != id {
		return fmt.Errorf("champion object checksum mismatch")
	}
	return nil
}

func championPut(root, category string, value any) (string, error) {
	id, e := Digest(value)
	if e != nil {
		return "", e
	}
	dir := filepath.Join(root, category)
	if e = os.MkdirAll(dir, 0700); e != nil {
		return "", e
	}
	return id, writeObject(filepath.Join(dir, id+".json"), value)
}

func championModel(root, id string) (battlepolicy.Artifact, error) {
	if !digest(id) {
		return battlepolicy.Artifact{}, fmt.Errorf("invalid champion model identity")
	}
	a, e := battlepolicy.LoadArtifact(filepath.Join(root, "models", id+".json"))
	if e != nil {
		return a, e
	}
	actual, _ := Digest(a)
	if actual != id {
		return a, fmt.Errorf("champion model checksum mismatch")
	}
	return a, nil
}

func championOpponents(root, champion string) ([]Opponent, error) {
	var opponents []Opponent
	for _, rule := range promotionRules() {
		opponents = append(opponents, Opponent{Name: rule, Rule: rule})
	}
	if champion != "" {
		a, e := championModel(root, champion)
		if e != nil {
			return nil, e
		}
		opponents = append(opponents, Opponent{Name: "champion", Model: &a})
	}
	return opponents, nil
}

func championReportPath(root, attempt string) string {
	return filepath.Join(root, "evaluations", attempt+".json")
}

func championReservation(root, head string) string {
	if head == "" {
		head = "initial"
	}
	return filepath.Join(root, "reservations", head+".json")
}

// LoadChampionRegistry verifies all committed native evidence and recomputes
// every decision. The immutable event chain also includes failed/abandoned
// attempts so rollback cannot erase test exposure or reset alpha spending.
func LoadChampionRegistry(ctx context.Context, root string) (ChampionStatus, error) {
	return loadChampionRegistry(ctx, root, VerifyEvaluation)
}

func loadChampionRegistry(ctx context.Context, root string, verify func(context.Context, string) (EvaluationReport, error)) (ChampionStatus, error) {
	s := ChampionStatus{Purpose: "controlled-native-champion", used: map[string]bool{}}
	if e := ctx.Err(); e != nil {
		return s, e
	}
	if e := readObject(filepath.Join(root, "registry.json"), &s.Registry, 1<<20); e != nil {
		return s, e
	}
	if e := s.Registry.Validate(); e != nil {
		return s, e
	}
	registryID, _ := Digest(s.Registry)
	var pointer struct {
		Event string `json:"event"`
	}
	if e := readObject(filepath.Join(root, "champion.json"), &pointer, 1024); e != nil && !os.IsNotExist(e) {
		return s, e
	}
	s.Head = pointer.Event
	seen := map[string]bool{}
	for id := s.Head; id != ""; {
		if e := ctx.Err(); e != nil {
			return s, e
		}
		if seen[id] || len(seen) >= 1000000 {
			return s, fmt.Errorf("invalid champion event chain")
		}
		seen[id] = true
		var event ChampionEvent
		if e := championObject(root, "events", id, &event); e != nil {
			return s, e
		}
		if event.Schema != s.Registry.eventSchema() || event.Registry != registryID {
			return s, fmt.Errorf("champion event belongs to different registry")
		}
		s.Events = append(s.Events, event)
		s.EventIDs = append(s.EventIDs, id)
		id = event.Previous
	}
	for i, j := 0, len(s.Events)-1; i < j; i, j = i+1, j-1 {
		s.Events[i], s.Events[j] = s.Events[j], s.Events[i]
		s.EventIDs[i], s.EventIDs[j] = s.EventIDs[j], s.EventIDs[i]
	}
	eligible := map[string]string{}
	for i, event := range s.Events {
		if e := ctx.Err(); e != nil {
			return s, e
		}
		before := s.Champion
		switch event.Kind {
		case "challenge", "abandon":
			var attempt ChampionAttempt
			if e := championObject(root, "attempts", event.Attempt, &attempt); e != nil {
				return s, e
			}
			if e := validateChampionAttempt(ctx, root, s, attempt, event.Previous); e != nil {
				return s, e
			}
			s.Attempts++
			for _, x := range attempt.experiments() {
				for _, f := range x.groups("test") {
					s.used[f.Group] = true
				}
			}
			if event.Target != "" {
				return s, fmt.Errorf("challenge contains rollback target")
			}
			if event.Kind == "challenge" {
				reports, e := verifyChampionReports(ctx, root, event.Attempt, attempt, verify)
				if e != nil {
					return s, e
				}
				id, _ := championReportsDigest(attempt, reports)
				if id != event.Report || event.Reason != "" {
					return s, fmt.Errorf("champion event evaluation mismatch")
				}
				a, e := assessChampionReports(reports, attempt, s.Registry.Gate)
				if e != nil {
					return s, e
				}
				if event.Assessment == nil || !reflect.DeepEqual(a, *event.Assessment) {
					return s, fmt.Errorf("champion assessment differs from evaluation")
				}
				if a.Passed {
					s.Champion = attempt.Candidate
					eligible[s.EventIDs[i]] = s.Champion
				}
			} else if event.Report != "" || event.Assessment != nil || event.Reason == "" {
				return s, fmt.Errorf("invalid abandonment event")
			}
		case "rollback":
			selected := eligible[event.Target]
			if selected == "" || selected == before || event.Attempt != "" || event.Report != "" || event.Assessment != nil || event.Reason == "" {
				return s, fmt.Errorf("invalid rollback target or event")
			}
			s.Champion = selected
		default:
			return s, fmt.Errorf("unknown champion event kind")
		}
		if event.Champion != s.Champion {
			return s, fmt.Errorf("champion pointer differs from verified gates")
		}
	}
	var reserved struct {
		Attempt string `json:"attempt"`
	}
	if e := readObject(championReservation(root, s.Head), &reserved, 1024); e != nil && !os.IsNotExist(e) {
		return s, e
	}
	if reserved.Attempt != "" {
		var a ChampionAttempt
		if e := championObject(root, "attempts", reserved.Attempt, &a); e != nil {
			return s, e
		}
		if e := validateChampionAttempt(ctx, root, s, a, s.Head); e != nil {
			return s, e
		}
		s.Pending = reserved.Attempt
	}
	if s.Champion != "" {
		if _, e := championModel(root, s.Champion); e != nil {
			return s, e
		}
		s.Model = filepath.Join(root, "models", s.Champion+".json")
	}
	return s, nil
}

func validateChampionAttempt(ctx context.Context, root string, s ChampionStatus, a ChampionAttempt, previous string) error {
	if len(s.Registry.MixedConditions) > 0 || a.Mixed != nil {
		return validateMixedChampionAttempt(ctx, root, s, a, previous)
	}
	registry, _ := Digest(s.Registry)
	if a.Schema != "native-champion-attempt-v1" || a.Registry != registry || a.Previous != previous || a.Champion != s.Champion || a.Number != s.Attempts+1 || a.Candidate == a.Champion || a.Experiment.Environment != s.Registry.Environment || championConditions(a.Experiment) != s.Registry.Conditions {
		return fmt.Errorf("champion attempt differs from frozen registry/history")
	}
	if e := a.Experiment.Validate(); e != nil {
		return e
	}
	if len(a.Experiment.groups("test")) < s.Registry.Gate.MinGroups {
		return fmt.Errorf("test split has fewer families than the frozen promotion gate")
	}
	for _, family := range a.Experiment.groups("test") {
		if s.used[family.Group] {
			return fmt.Errorf("promotion test family was already exposed by an earlier attempt")
		}
	}
	candidate, e := championModel(root, a.Candidate)
	if e != nil {
		return e
	}
	opponents, e := championOpponents(root, a.Champion)
	if e != nil {
		return e
	}
	_, e = prepareEvaluation(ctx, s.Registry.Environment, candidate, opponents, experimentEvaluationConfig(a.Experiment, "test"), &a.Experiment, "test")
	return e
}

func matchChampionReport(root string, a ChampionAttempt, r EvaluationReport) error {
	xid, _ := Digest(a.Experiment)
	if a.Mixed != nil {
		xid, _ = Digest(*a.Mixed)
		if r.MixedExperimentDigest != xid || r.MixedExperiment == nil || a.Mixed.validateEvaluationPart(r.Experiment) != nil {
			return fmt.Errorf("mixed promotion report differs from complete envelope")
		}
	} else if r.ExperimentDigest != xid || r.MixedExperiment != nil || r.MixedExperimentDigest != "" {
		return fmt.Errorf("promotion report differs from single-mode experiment")
	}
	if r.Split != "test" || r.CandidateArtifact != a.Candidate {
		return fmt.Errorf("promotion requires the complete frozen final test")
	}
	opponents, e := championOpponents(root, a.Champion)
	if e != nil {
		return e
	}
	versions := map[string]string{}
	for _, o := range opponents {
		p := Policy{Rule: o.Rule}
		if o.Model != nil {
			p = Policy{Model: o.Model.Network, Greedy: true}
		}
		id, e := p.Version()
		if e != nil {
			return e
		}
		versions[o.Name] = id
	}
	for _, game := range r.Games {
		if versions[game.Opponent] == "" || game.OpponentPolicy != versions[game.Opponent] {
			return fmt.Errorf("promotion opponent identity changed")
		}
	}
	return nil
}

func commitChampionEvent(root string, event ChampionEvent) (string, error) {
	id, e := championPut(root, "events", event)
	if e != nil {
		return "", e
	}
	f, e := os.CreateTemp(root, ".champion-*")
	if e != nil {
		return "", e
	}
	defer f.Close()
	defer os.Remove(f.Name())
	if e = json.NewEncoder(f).Encode(struct {
		Event string `json:"event"`
	}{id}); e != nil {
		return "", e
	}
	if e = f.Sync(); e != nil {
		return "", e
	}
	if e = f.Close(); e != nil {
		return "", e
	}
	if e = os.Rename(f.Name(), filepath.Join(root, "champion.json")); e != nil {
		return "", e
	}
	dir, e := os.Open(root)
	if e != nil {
		return "", e
	}
	defer dir.Close()
	return id, dir.Sync()
}

// ChallengeChampion automatically commits a new native champion only after
// the frozen independent gate passes. Failure still commits an audit event,
// retaining the previous champion. Partial evaluations are retried unchanged.
func ChallengeChampion(ctx context.Context, root string, engine *battleenv.Native, candidate battlepolicy.Artifact, x Experiment, selectionPath string, progress func(EvaluationGame) error) (ChampionEvent, error) {
	return challengeChampion(ctx, root, engine, candidate, x, nil, selectionPath, progress)
}

func challengeChampion(ctx context.Context, root string, engine *battleenv.Native, candidate battlepolicy.Artifact, x Experiment, mixed *MixedExperiment, selectionPath string, progress func(EvaluationGame) error) (ChampionEvent, error) {
	var event ChampionEvent
	lock, e := championLock(root)
	if e != nil {
		return event, e
	}
	defer lock.Close()
	s, e := LoadChampionRegistry(ctx, root)
	if e != nil {
		return event, e
	}
	if engine == nil || engine.Metadata() != s.Registry.Environment {
		return event, fmt.Errorf("champion engine differs from frozen registry")
	}
	if e := candidate.Validate(); e != nil {
		return event, e
	}
	candidateID, e := championPut(root, "models", candidate)
	if e != nil {
		return event, e
	}
	registryID, _ := Digest(s.Registry)
	a := ChampionAttempt{Schema: "native-champion-attempt-v1", Registry: registryID, Previous: s.Head, Champion: s.Champion, Candidate: candidateID, Experiment: x, Number: s.Attempts + 1}
	if mixed != nil {
		a.Schema, a.Mixed = "native-mixed-champion-attempt-v1", mixed
	}
	if e := validateChampionAttempt(ctx, root, s, a, s.Head); e != nil {
		return event, e
	}
	attemptID, e := championPut(root, "attempts", a)
	if e != nil {
		return event, e
	}
	if s.Pending != "" && s.Pending != attemptID {
		return event, fmt.Errorf("another attempt is pending; resume it or explicitly abandon it")
	}
	opponents, e := championOpponents(root, s.Champion)
	if e != nil {
		return event, e
	}
	xid, _ := Digest(x)
	if mixed != nil {
		xid, _ = Digest(*mixed)
	}
	if e = os.MkdirAll(filepath.Join(root, "selections"), 0700); e != nil {
		return event, e
	}
	if e = freezeChampionSelection(ctx, filepath.Join(root, "selections", xid+".json"), a, candidate, opponents); e != nil {
		return event, e
	}
	if selectionPath != "" {
		if e = freezeChampionSelection(ctx, selectionPath, a, candidate, opponents); e != nil {
			return event, e
		}
	}
	if e = os.MkdirAll(filepath.Join(root, "reservations"), 0700); e != nil {
		return event, e
	}
	if e = writeObject(championReservation(root, s.Head), struct {
		Attempt string `json:"attempt"`
	}{attemptID}); e != nil {
		return event, e
	}
	for _, part := range a.experiments() {
		path := championModeReportPath(root, attemptID, a, part.Mode)
		if _, e := os.Stat(path); os.IsNotExist(e) {
			// Repeating this reserved challenge resumes its exact frozen attempt.
			_, statErr := os.Stat(filepath.Join(path+".data", "spec.json"))
			if statErr != nil && !os.IsNotExist(statErr) {
				return event, statErr
			}
			options := EvaluationRecordingOptions{Resume: statErr == nil}
			if mixed != nil {
				_, e = EvaluateMixedRecorded(ctx, engine, candidate, opponents, *mixed, part.Mode, "test", path, progress, options)
			} else {
				_, e = EvaluateRecorded(ctx, engine, candidate, opponents, experimentEvaluationConfig(x, "test"), &x, "test", path, progress, options)
			}
			if e != nil {
				return event, e
			}
		} else if e != nil {
			return event, e
		}
	}
	reports, e := verifyChampionReports(ctx, root, attemptID, a, VerifyEvaluation)
	if e != nil {
		return event, e
	}
	assessment, e := assessChampionReports(reports, a, s.Registry.Gate)
	if e != nil {
		return event, e
	}
	reportID, _ := championReportsDigest(a, reports)
	event = ChampionEvent{Schema: s.Registry.eventSchema(), Registry: registryID, Previous: s.Head, Kind: "challenge", Attempt: attemptID, Report: reportID, Assessment: &assessment, Champion: s.Champion}
	if assessment.Passed {
		event.Champion = candidateID
	}
	if e := ctx.Err(); e != nil {
		return event, e
	}
	_, e = commitChampionEvent(root, event)
	return event, e
}

func ChangeChampion(ctx context.Context, root, target, reason string, abandon bool) (ChampionEvent, error) {
	return changeChampion(ctx, root, target, reason, abandon, VerifyEvaluation)
}

func changeChampion(ctx context.Context, root, target, reason string, abandon bool, verify func(context.Context, string) (EvaluationReport, error)) (ChampionEvent, error) {
	var event ChampionEvent
	if reason == "" || len(reason) > 2048 {
		return event, fmt.Errorf("a reason of 1..2048 bytes is required")
	}
	lock, e := championLock(root)
	if e != nil {
		return event, e
	}
	defer lock.Close()
	s, e := loadChampionRegistry(ctx, root, verify)
	if e != nil {
		return event, e
	}
	registry, _ := Digest(s.Registry)
	event = ChampionEvent{Schema: s.Registry.eventSchema(), Registry: registry, Previous: s.Head, Champion: s.Champion, Reason: reason}
	if abandon {
		if s.Pending == "" || target != "" {
			return event, fmt.Errorf("abandon requires a pending attempt and no target")
		}
		event.Kind, event.Attempt = "abandon", s.Pending
	} else {
		if s.Pending != "" {
			return event, fmt.Errorf("resume or abandon pending attempt before rollback")
		}
		for i, id := range s.EventIDs {
			if id == target && s.Events[i].Kind == "challenge" && s.Events[i].Assessment != nil && s.Events[i].Assessment.Passed {
				event.Champion = s.Events[i].Champion
			}
		}
		if event.Champion == "" || event.Champion == s.Champion {
			return event, fmt.Errorf("rollback target must be an earlier passing event with a different model")
		}
		event.Kind, event.Target = "rollback", target
	}
	if e := ctx.Err(); e != nil {
		return event, e
	}
	_, e = commitChampionEvent(root, event)
	return event, e
}
