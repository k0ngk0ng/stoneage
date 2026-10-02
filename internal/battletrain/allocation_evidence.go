package battletrain

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"

	"github.com/k0ngk0ng/stoneage/internal/battleenv"
	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
)

const allocationEvidenceSchema = "commander-allocation-evidence-v1"
const allocationReportSchema = "commander-allocation-evaluation-v1"

var allocationNames = [2]string{"balanced", "selected"}

type allocationEvidenceSpec struct {
	Schema     string               `json:"schema"`
	Validation AllocationValidation `json:"validation"`
	Candidate  string               `json:"candidate_artifact"`
	Opponents  []evaluationOpponent `json:"opponents"`
}
type AllocationEvaluationResult struct {
	Allocation  string           `json:"allocation"`
	Games       []EvaluationGame `json:"games"`
	Comparisons []Comparison     `json:"comparisons"`
}

// This separate report domain is not accepted by normal final-test/champion
// readers. Both perspectives and exact model snapshots live in <output>.data;
// source-search evidence is retained separately and required for verification.
type AllocationEvaluationReport struct {
	Schema     string                       `json:"schema"`
	Evidence   string                       `json:"evidence"`
	Validation string                       `json:"validation"`
	Candidate  string                       `json:"candidate_artifact"`
	Status     string                       `json:"status"` // complete or incomplete-outcomes (collection cutoffs).
	Results    []AllocationEvaluationResult `json:"results"`
}

func verifyAllocationSource(ctx context.Context, root string, v AllocationValidation) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := v.Validate(); err != nil {
		return err
	}
	s, err := LoadBuildSearch(root)
	if err != nil {
		return err
	}
	id, err := Digest(s)
	if err != nil || id != v.Experiment.Pool.SourceState {
		return fmt.Errorf("allocation evidence source search differs from manifest")
	}
	return verifyBuildSearchDataContext(ctx, root, s)
}

func prepareAllocationEvaluation(v AllocationValidation, candidate battlepolicy.Artifact, opponents []Opponent) (allocationEvidenceSpec, [2]evaluationSchedule, error) {
	var schedules [2]evaluationSchedule
	spec := allocationEvidenceSpec{Schema: allocationEvidenceSchema, Validation: v}
	if err := v.ValidateCandidate(candidate); err != nil {
		return spec, schedules, err
	}
	var err error
	spec.Candidate, err = Digest(candidate)
	if err != nil {
		return spec, schedules, err
	}
	if len(opponents) != len(v.Source.Spec.Opponents) {
		return spec, schedules, fmt.Errorf("allocation opponent count differs from source")
	}
	var policies []Policy
	var versions []string
	for i, o := range opponents {
		p, id, err := buildPolicy(o, v.Experiment.Environment, v.Experiment.Mode)
		if err != nil {
			return spec, schedules, err
		}
		if id != v.Source.Spec.Opponents[i] {
			return spec, schedules, fmt.Errorf("allocation opponent policy differs from source")
		}
		if p.Model == nil {
			p.Features = candidate.Features
		}
		policies = append(policies, p)
		versions = append(versions, id.Version)
		spec.Opponents = append(spec.Opponents, evaluationOpponent{Name: id.Name, Rule: id.Rule, Artifact: id.Artifact})
	}
	c := DefaultEvaluationConfig()
	c.Pairing = "" // Four games retain owned allocation; no ownership swap.
	c.Seed = v.Seed
	c.Mode, c.Points, c.PetPoints, c.Level, c.MaxTurns = v.Experiment.Mode, v.Experiment.Points, v.Experiment.PetPoints, v.Experiment.Level, v.Experiment.MaxTurns
	c.ReservePets, c.HealingItems, c.HealingMagic, c.PetSkillMask = v.Experiment.ReservePets, v.Experiment.HealingItems, v.Experiment.HealingMagic, v.Experiment.PetSkillMask
	c.MatchesPerOpponent = v.Groups * 4
	for ai, own := range v.ownedRosters() {
		q := evaluationSchedule{StrictPolicyStatistics: true, Policies: policies, Versions: versions, Report: EvaluationReport{Environment: v.Experiment.Environment, Config: c}}
		for i, enemy := range v.OpponentRosters {
			for repeat := 0; repeat < 4; repeat++ {
				q.Suite = append(q.Suite, buildScenario(v.config(), own, enemy, i, repeat, 3))
			}
		}
		schedules[ai] = q
	}
	return spec, schedules, nil
}

func allocationRecorders(root, id string) [2]*evaluationRecorder {
	var out [2]*evaluationRecorder
	for i, name := range allocationNames {
		branch, _ := Digest(struct{ Evidence, Allocation string }{id, name})
		out[i] = &evaluationRecorder{root: filepath.Join(root, name), id: branch, options: EvaluationRecordingOptions{Resume: true}, preflightOnly: true}
	}
	return out
}

func loadAllocationModel(root, id string) (battlepolicy.Artifact, error) {
	var empty battlepolicy.Artifact
	if !digest(id) {
		return empty, fmt.Errorf("invalid allocation model identity")
	}
	a, err := battlepolicy.LoadArtifact(filepath.Join(root, "models", id+".json"))
	if err != nil {
		return a, err
	}
	actual, err := Digest(a)
	if err != nil || actual != id {
		return a, fmt.Errorf("allocation model checksum mismatch")
	}
	return a, nil
}

func loadAllocationSpec(root string) (allocationEvidenceSpec, battlepolicy.Artifact, []Opponent, [2]evaluationSchedule, error) {
	var spec allocationEvidenceSpec
	var candidate battlepolicy.Artifact
	var opponents []Opponent
	var schedules [2]evaluationSchedule
	if err := readObject(filepath.Join(root, "spec.json"), &spec, allocationValidationBytes+(1<<20)); err != nil {
		return spec, candidate, nil, schedules, err
	}
	if spec.Schema != allocationEvidenceSchema {
		return spec, candidate, nil, schedules, fmt.Errorf("unsupported allocation evidence schema")
	}
	var err error
	candidate, err = loadAllocationModel(root, spec.Candidate)
	if err != nil {
		return spec, candidate, nil, schedules, err
	}
	for _, entry := range spec.Opponents {
		o := Opponent{Name: entry.Name, Rule: entry.Rule}
		if entry.Artifact != "" {
			a, e := loadAllocationModel(root, entry.Artifact)
			if e != nil {
				return spec, candidate, nil, schedules, e
			}
			o.Model = &a
		}
		opponents = append(opponents, o)
	}
	want, schedules, err := prepareAllocationEvaluation(spec.Validation, candidate, opponents)
	if err == nil && !reflect.DeepEqual(want, spec) {
		err = fmt.Errorf("allocation evidence specification mismatch")
	}
	return spec, candidate, opponents, schedules, err
}

// RunAllocationValidation reuses the ordinary evaluator's native collector,
// per-game commits, orphan adoption and policy replay. It preflights BOTH
// allocation branches before any adoption or new collection. Resume requires
// the exact unchanged manifest/candidate; partial results never become a report.
func RunAllocationValidation(ctx context.Context, engine *battleenv.Native, v AllocationValidation, candidate battlepolicy.Artifact, sourceRoot, output string, progress func(allocation string, game EvaluationGame) error, options ...EvaluationRecordingOptions) (AllocationEvaluationReport, error) {
	var report AllocationEvaluationReport
	if len(options) > 1 || engine == nil || output == "" {
		return report, fmt.Errorf("allocation evaluation requires engine/output and at most one option")
	}
	var option EvaluationRecordingOptions
	if len(options) == 1 {
		option = options[0]
	}
	if err := ctx.Err(); err != nil {
		return report, err
	}
	if engine.Metadata() != v.Experiment.Environment {
		return report, fmt.Errorf("allocation engine environment mismatch")
	}
	// Validate the candidate before any output mutation or expensive source scan.
	if err := v.ValidateCandidate(candidate); err != nil {
		return report, err
	}
	if err := verifyAllocationSource(ctx, sourceRoot, v); err != nil {
		return report, err
	}
	var opponents []Opponent
	for _, id := range v.Source.Spec.Opponents {
		o, err := LoadBuildSearchPolicy(sourceRoot, id)
		if err != nil {
			return report, err
		}
		opponents = append(opponents, o)
	}
	spec, schedules, err := prepareAllocationEvaluation(v, candidate, opponents)
	if err != nil {
		return report, err
	}
	root := output + ".data"
	if err = os.MkdirAll(root, 0700); err != nil {
		return report, err
	}
	lock, err := os.OpenFile(filepath.Join(root, ".allocation.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return report, err
	}
	defer lock.Close()
	if err = lockTraining(lock); err != nil {
		return report, fmt.Errorf("allocation evaluation already running: %w", err)
	}
	if _, err = os.Stat(output); err == nil {
		return report, fmt.Errorf("allocation report exists; choose a new output")
	} else if !os.IsNotExist(err) {
		return report, err
	}
	if option.Resume {
		frozen, _, _, _, e := loadAllocationSpec(root)
		if e != nil {
			return report, e
		}
		if !reflect.DeepEqual(frozen, spec) {
			return report, fmt.Errorf("resume differs from frozen allocation specification")
		}
	} else {
		entries, e := os.ReadDir(root)
		if e != nil {
			return report, e
		}
		for _, entry := range entries {
			if entry.Name() != ".allocation.lock" {
				return report, fmt.Errorf("allocation evidence exists; preserve it and explicitly resume")
			}
		}
		if err = os.MkdirAll(filepath.Join(root, "models"), 0700); err != nil {
			return report, err
		}
		models := []battlepolicy.Artifact{candidate}
		for _, o := range opponents {
			if o.Model != nil {
				models = append(models, *o.Model)
			}
		}
		for _, a := range models {
			id, e := Digest(a)
			if e != nil {
				return report, e
			}
			if e = writeObject(filepath.Join(root, "models", id+".json"), a); e != nil {
				return report, e
			}
		}
		if err = writeObject(filepath.Join(root, "spec.json"), spec); err != nil {
			return report, err
		}
	}
	// Execute owned disk snapshots, not caller-held mutable network pointers.
	// This also checks the complete frozen header and every copied model before
	// recovery can publish adoption commits or collect a new game.
	frozen, ownedCandidate, ownedOpponents, ownedSchedules, err := loadAllocationSpec(root)
	if err != nil {
		return report, err
	}
	if !reflect.DeepEqual(frozen, spec) {
		return report, fmt.Errorf("allocation specification changed while freezing")
	}
	spec, candidate, opponents, schedules = frozen, ownedCandidate, ownedOpponents, ownedSchedules
	v = spec.Validation
	id, err := Digest(spec)
	if err != nil {
		return report, err
	}
	recorders := allocationRecorders(root, id)
	var restored [2][]EvaluationGame
	completed, total := 0, 0
	for ai, r := range recorders {
		restored[ai], err = r.restore(ctx, candidate, opponents, schedules[ai])
		if err != nil {
			return report, err
		}
		completed += len(restored[ai])
		total += len(schedules[ai].Suite) * len(opponents)
	}
	if len(restored[1]) > 0 && len(restored[0]) != len(schedules[0].Suite)*len(opponents) {
		return report, fmt.Errorf("allocation branches are not a contiguous prefix")
	}
	if err = ctx.Err(); err != nil {
		return report, err
	}
	// No branch writes occur until all retained evidence passed the preflight.
	for ai, r := range recorders {
		for i, g := range restored[ai] {
			if err = ctx.Err(); err != nil {
				return report, err
			}
			if err = r.commit(i, g.Shard); err != nil {
				return report, err
			}
		}
	}
	if option.Restored != nil {
		if err = option.Restored(completed, total); err != nil {
			return report, err
		}
	}
	validationID, err := Digest(v)
	if err != nil {
		return report, err
	}
	report = AllocationEvaluationReport{Schema: allocationReportSchema, Evidence: id, Validation: validationID, Candidate: spec.Candidate, Status: "complete"}
	for ai, r := range recorders {
		current := schedules[ai].Report
		current.Games = restored[ai]
		var callback func(EvaluationGame) error
		if progress != nil {
			callback = func(g EvaluationGame) error { return progress(allocationNames[ai], g) }
		}
		current, err = collectPreparedEvaluation(ctx, engine, candidate, opponents, schedules[ai], current, callback, r)
		if err != nil {
			return report, err
		}
		report.Results = append(report.Results, AllocationEvaluationResult{Allocation: allocationNames[ai], Games: current.Games, Comparisons: current.Comparisons})
		for _, g := range current.Games {
			if !g.Terminated || g.Truncated {
				report.Status = "incomplete-outcomes"
			}
		}
	}
	if err = ctx.Err(); err != nil {
		return report, err
	}
	if err = os.MkdirAll(filepath.Dir(output), 0700); err != nil {
		return report, err
	}
	return report, writeObject(output, report)
}

// VerifyAllocationEvaluation is read-only. Source/raw trajectories and frozen
// model decisions/values/probabilities are checked; C outcomes are not rerun.
func VerifyAllocationEvaluation(ctx context.Context, path, sourceRoot string) (AllocationEvaluationReport, error) {
	var report AllocationEvaluationReport
	if err := ctx.Err(); err != nil {
		return report, err
	}
	if err := readObject(path, &report, 256<<20); err != nil {
		return report, err
	}
	if report.Schema != allocationReportSchema || len(report.Results) != 2 {
		return report, fmt.Errorf("unsupported/incomplete allocation report")
	}
	root := path + ".data"
	spec, candidate, opponents, schedules, err := loadAllocationSpec(root)
	if err != nil {
		return report, err
	}
	if err = verifyAllocationSource(ctx, sourceRoot, spec.Validation); err != nil {
		return report, err
	}
	id, err := Digest(spec)
	if err != nil {
		return report, err
	}
	validationID, err := Digest(spec.Validation)
	if err != nil {
		return report, err
	}
	if report.Evidence != id || report.Validation != validationID || report.Candidate != spec.Candidate {
		return report, fmt.Errorf("allocation report identity mismatch")
	}
	recorders := allocationRecorders(root, id)
	status := "complete"
	for ai, r := range recorders {
		games, e := r.restore(ctx, candidate, opponents, schedules[ai])
		if e != nil {
			return report, e
		}
		row := report.Results[ai]
		if row.Allocation != allocationNames[ai] || len(games) != len(schedules[ai].Suite)*len(opponents) || !reflect.DeepEqual(games, row.Games) || !reflect.DeepEqual(Summarize(games, spec.Validation.Seed), row.Comparisons) {
			return report, fmt.Errorf("allocation report differs from raw scheduled games")
		}
		for _, g := range games {
			if !g.Terminated || g.Truncated {
				status = "incomplete-outcomes"
			}
		}
	}
	if report.Status != status {
		return report, fmt.Errorf("allocation report cutoff status mismatch")
	}
	return report, nil
}
