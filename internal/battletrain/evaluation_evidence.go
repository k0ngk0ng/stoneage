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

type evaluationOpponent struct {
	Name     string `json:"name"`
	Rule     string `json:"rule,omitempty"`
	Artifact string `json:"artifact,omitempty"`
}

// Frozen before the first game. Models and raw trajectories travel with the
// report, rather than depending on original training paths or mutable files.
type evaluationSpec struct {
	Comparison *MixedValidationComparison `json:"validation_comparison,omitempty"`
	Mixed      *MixedExperiment           `json:"mixed_experiment,omitempty"`
	Schema     string                     `json:"schema"`
	Candidate  string                     `json:"candidate_artifact"`
	Opponents  []evaluationOpponent       `json:"opponents"`
	Config     EvaluationConfig           `json:"config"`
	Experiment *Experiment                `json:"experiment,omitempty"`
	Split      string                     `json:"split,omitempty"`
}

// EvaluationRecordingOptions makes recovery explicit. Restored runs after every
// reused game has passed the same policy replay as VerifyEvaluation; subsequent
// progress callbacks describe newly collected games only.
type EvaluationRecordingOptions struct {
	Resume   bool
	Restored func(completed, total int) error
}

type evaluationRecorder struct {
	preflightOnly bool // Read-only recovery check; no adoption commits or callbacks.
	root, id      string
	options       EvaluationRecordingOptions
}

func (r *evaluationRecorder) freeze(report EvaluationReport, candidate battlepolicy.Artifact, opponents []Opponent) error {
	save := func(a battlepolicy.Artifact) (string, error) {
		id, e := Digest(a)
		if e != nil {
			return "", e
		}
		path := filepath.Join(r.root, "models", id+".json")
		if r.options.Resume {
			frozen, err := battlepolicy.LoadArtifact(path)
			if err != nil {
				return "", err
			}
			actual, err := Digest(frozen)
			if err != nil || actual != id {
				return "", fmt.Errorf("frozen evaluation model checksum mismatch")
			}
			return id, nil
		}
		return id, writeObject(path, a)
	}
	if e := os.MkdirAll(filepath.Join(r.root, "models"), 0700); e != nil {
		return e
	}
	id, e := save(candidate)
	if e != nil {
		return e
	}
	spec := evaluationSpec{Schema: "commander-evaluation-evidence-v1", Candidate: id, Config: report.Config, Experiment: report.Experiment, Split: report.Split}
	if report.MixedExperiment != nil {
		spec.Schema, spec.Mixed = "commander-mixed-evaluation-evidence-v1", report.MixedExperiment
	}
	if report.ValidationComparison != nil {
		spec.Schema, spec.Comparison = "commander-mixed-comparison-evidence-v1", report.ValidationComparison
	}
	for _, o := range opponents {
		entry := evaluationOpponent{Name: o.Name, Rule: o.Rule}
		if o.Model != nil {
			entry.Artifact, e = save(*o.Model)
			if e != nil {
				return e
			}
		}
		spec.Opponents = append(spec.Opponents, entry)
	}
	r.id, e = Digest(spec)
	if e != nil {
		return e
	}
	if r.options.Resume {
		var frozen evaluationSpec
		if err := readObject(filepath.Join(r.root, "spec.json"), &frozen, mixedValidationComparisonBytes+mixedExperimentBytes+(32<<20)); err != nil {
			return err
		}
		if !reflect.DeepEqual(frozen, spec) {
			return fmt.Errorf("resume differs from frozen evaluation specification")
		}
		return nil
	}
	return writeObject(filepath.Join(r.root, "spec.json"), spec)
}

// EvaluateRecorded writes a new report plus <report>.data, retaining both
// perspectives of every native game. Interrupted attempts require Resume and
// the exact frozen specification; verified completed games are not executed again.
// This does not promote a model or grant online arena certification.
func EvaluateRecorded(ctx context.Context, engine *battleenv.Native, candidate battlepolicy.Artifact, opponents []Opponent, c EvaluationConfig, experiment *Experiment, split, output string, progress func(EvaluationGame) error, options ...EvaluationRecordingOptions) (EvaluationReport, error) {
	return evaluateRecordedWithMixed(ctx, engine, candidate, opponents, c, experiment, nil, split, output, progress, options...)
}

func evaluateRecordedWithMixed(ctx context.Context, engine *battleenv.Native, candidate battlepolicy.Artifact, opponents []Opponent, c EvaluationConfig, experiment *Experiment, mixed *MixedExperiment, split, output string, progress func(EvaluationGame) error, options ...EvaluationRecordingOptions) (EvaluationReport, error) {
	return evaluateRecordedWithComparison(ctx, engine, candidate, opponents, c, experiment, mixed, nil, split, output, progress, options...)
}

func evaluateRecordedWithComparison(ctx context.Context, engine *battleenv.Native, candidate battlepolicy.Artifact, opponents []Opponent, c EvaluationConfig, experiment *Experiment, mixed *MixedExperiment, comparison *MixedValidationComparison, split, output string, progress func(EvaluationGame) error, options ...EvaluationRecordingOptions) (EvaluationReport, error) {
	var empty EvaluationReport
	if len(options) > 1 {
		return empty, fmt.Errorf("at most one evaluation recording option is allowed")
	}
	var option EvaluationRecordingOptions
	if len(options) == 1 {
		option = options[0]
	}
	if output == "" {
		return empty, fmt.Errorf("evaluation output required")
	}
	if e := ctx.Err(); e != nil {
		return empty, e
	}
	if _, e := os.Stat(output); e == nil {
		return empty, fmt.Errorf("evaluation report exists; choose a new output")
	} else if !os.IsNotExist(e) {
		return empty, e
	}
	recorder := &evaluationRecorder{root: output + ".data", options: option}
	if e := os.MkdirAll(recorder.root, 0700); e != nil {
		return empty, e
	}
	lock, e := os.OpenFile(filepath.Join(recorder.root, ".evaluation.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return empty, e
	}
	defer lock.Close()
	if e = lockTraining(lock); e != nil {
		return empty, fmt.Errorf("evaluation is already running: %w", e)
	}
	// Recheck after locking: a competing writer may have finished while this
	// invocation was being prepared.
	if _, e := os.Stat(output); e == nil {
		return empty, fmt.Errorf("evaluation report exists; choose a new output")
	} else if !os.IsNotExist(e) {
		return empty, e
	}
	_, e = os.Stat(filepath.Join(recorder.root, "spec.json"))
	if e != nil && !os.IsNotExist(e) {
		return empty, e
	}
	if option.Resume && os.IsNotExist(e) {
		return empty, fmt.Errorf("resume requires existing frozen evaluation evidence")
	}
	if !option.Resume && e == nil {
		return empty, fmt.Errorf("interrupted evaluation exists; use --resume with the same specification")
	}
	if !option.Resume {
		entries, err := os.ReadDir(recorder.root)
		if err != nil {
			return empty, err
		}
		for _, entry := range entries {
			if entry.Name() != ".evaluation.lock" {
				return empty, fmt.Errorf("incomplete evaluation evidence without frozen specification; preserve it and choose a new output")
			}
		}
	}
	report, e := evaluateWithComparison(ctx, engine, candidate, opponents, c, experiment, mixed, comparison, split, progress, recorder)
	if e != nil {
		return report, e
	}
	if e = ctx.Err(); e != nil {
		return report, e
	}
	return report, SaveEvaluation(output, report)
}

// VerifyEvaluation is a read-only evidence check. It recreates the schedule
// and source-overlap limitations from frozen artifacts, checks both raw perspectives and recomputes
// statistics. Hashes provide integrity, not a signature from a trusted server;
// this is not a replay of the C engine or proof of online execution.
func VerifyEvaluation(ctx context.Context, path string) (EvaluationReport, error) {
	var report EvaluationReport
	if e := ctx.Err(); e != nil {
		return report, e
	}
	if e := readObject(path, &report, 256<<20); e != nil {
		return report, e
	}
	if e := ValidateEvaluation(report); e != nil {
		return report, e
	}
	if !digest(report.Evidence) {
		return report, fmt.Errorf("report has no complete evaluation evidence; collect a new recorded evaluation")
	}
	root := path + ".data"
	var spec evaluationSpec
	if e := readObject(filepath.Join(root, "spec.json"), &spec, mixedValidationComparisonBytes+mixedExperimentBytes+(32<<20)); e != nil {
		return report, e
	}
	id, e := Digest(spec)
	schema := "commander-evaluation-evidence-v1"
	if spec.Mixed != nil {
		schema = "commander-mixed-evaluation-evidence-v1"
	}
	if spec.Comparison != nil {
		schema = "commander-mixed-comparison-evidence-v1"
	}
	if e != nil || id != report.Evidence || spec.Schema != schema {
		return report, fmt.Errorf("evaluation evidence specification mismatch")
	}
	load := func(id string) (battlepolicy.Artifact, error) {
		if !digest(id) {
			return battlepolicy.Artifact{}, fmt.Errorf("invalid frozen model identity")
		}
		a, e := battlepolicy.LoadArtifact(filepath.Join(root, "models", id+".json"))
		if e != nil {
			return a, e
		}
		actual, _ := Digest(a)
		if actual != id {
			return a, fmt.Errorf("frozen evaluation model checksum mismatch")
		}
		return a, nil
	}
	candidate, e := load(spec.Candidate)
	if e != nil {
		return report, e
	}
	if len(spec.Opponents) < 1 || len(spec.Opponents) > 32 {
		return report, fmt.Errorf("invalid frozen opponent count")
	}
	var opponents []Opponent
	for _, o := range spec.Opponents {
		if e := ctx.Err(); e != nil {
			return report, e
		}
		entry := Opponent{Name: o.Name, Rule: o.Rule}
		if o.Artifact != "" {
			a, e := load(o.Artifact)
			if e != nil {
				return report, e
			}
			entry.Model = &a
		}
		opponents = append(opponents, entry)
	}
	schedule, e := prepareEvaluationWithComparison(ctx, report.Environment, candidate, opponents, spec.Config, spec.Experiment, spec.Mixed, spec.Comparison, spec.Split)
	if e != nil {
		return report, e
	}
	want := schedule.Report
	want.Evidence = report.Evidence
	header := report
	header.Games, header.Comparisons = nil, nil
	if !reflect.DeepEqual(want, header) || spec.Config != report.Config || len(report.Games) != len(opponents)*len(schedule.Suite) {
		return report, fmt.Errorf("evaluation report differs from frozen specification")
	}
	for index, g := range report.Games {
		trajectories, _, e := LoadShard(filepath.Join(root, "shards"), g.Shard)
		if e != nil {
			return report, e
		}
		if e := verifyEvaluationGame(ctx, trajectories, candidate, opponents, schedule, index, g); e != nil {
			return report, e
		}
	}
	return report, nil
}

// EvaluateMixedComparisonRecorded retains both immutable model identities and
// both declarations. It releases only the explicitly shared validation split.
func EvaluateMixedComparisonRecorded(ctx context.Context, engine *battleenv.Native, left, right battlepolicy.Artifact, x MixedValidationComparison, mode int, output string, progress func(EvaluationGame) error, options ...EvaluationRecordingOptions) (EvaluationReport, error) {
	if _, err := x.ValidatePair(left, right, mode, "validation"); err != nil {
		return EvaluationReport{}, err
	}
	part, err := x.Left.evaluationPart(mode)
	if err != nil {
		return EvaluationReport{}, err
	}
	opponents := []Opponent{{Name: fmt.Sprintf("model-0:%s", right.WeightsDigest), Model: &right}}
	return evaluateRecordedWithComparison(ctx, engine, left, opponents, experimentEvaluationConfig(part, "validation"), &part, &x.Left, &x, "validation", output, progress, options...)
}
