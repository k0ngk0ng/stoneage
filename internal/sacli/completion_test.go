package sacli

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCompletionContexts(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want []string
	}{
		{[]string{"ar"}, []string{"arena"}},
		{[]string{"arena", "qu"}, []string{"queue"}},
		{[]string{"arena", "create", ""}, []string{"1", "2", "3", "4", "5"}},
		{[]string{"ai", "experiment", "--pool-t"}, []string{"--pool-train-groups"}},
		{[]string{"ai", "experiment", "--pool-train-groups", ""}, nil},
		{[]string{"ai", "build-validate", ""}, []string{"--help", "compare", "init", "run", "verify"}},
		{[]string{"ai", "build-validate", "run", "--man"}, []string{"--manifest"}},
		{[]string{"ai", "build-validate", "run", "--manifest", ""}, []string{"@files"}},
		{[]string{"ai", "build-validate", "init", "--groups", ""}, nil},
		{[]string{"ai", "build-validate", "compare", "--c"}, []string{"--candidate"}},
		{[]string{"ai", "build-validate", "verify", "--search-dir", ""}, []string{"@dirs"}},
		{[]string{"ai", "r"}, []string{"run"}},
		{[]string{"ai", "run", "--f"}, []string{"--forever"}},
		{[]string{"ai", "run", "--config", "a directory/"}, []string{"@files"}},
		{[]string{"ai", "init", "--directory", "a directory/"}, []string{"@dirs"}},
		{[]string{"ai", "simulate", "--strategy", "l"}, []string{"learned"}},
		{[]string{"ai", "simulate", "--opponent-s"}, []string{"--opponent-strategy"}},
		{[]string{"ai", "simulate", "--opponent-strategy", "l"}, []string{"learned"}},
		{[]string{"ai", "simulate", "--opponent-model", ""}, []string{"@files"}},
		{[]string{"ai", "simulate", "--native-d"}, []string{"--native-dir"}},
		{[]string{"ai", "simulate", "--native-dir", ""}, []string{"@dirs"}},
		{[]string{"ai", "simulate", "--fixture-l"}, []string{"--fixture-level"}},
		{[]string{"ai", "simulate", "--require-w"}, []string{"--require-withdrawal"}},
		{[]string{"ai", "simulate", "--min-battle-t"}, []string{"--min-battle-turns"}},
		{[]string{"ai", "simulate", "--fixture-level", ""}, nil},
		{[]string{"ai", "environment", "i"}, []string{"init"}},
		{[]string{"ai", "environment", "init", "--im"}, []string{"--image"}},
		{[]string{"ai", "environment", "check", "--en"}, []string{"--environment"}},
		{[]string{"ai", "init", "--mode", ""}, []string{"1", "2", "3", "4", "5"}},
		{[]string{"--socket", "some socket", "ladder", "qu"}, []string{"queue"}},
		{[]string{"--json", "pet", "re"}, []string{"rename"}},
		{[]string{"auto-battle", "on", "st"}, []string{"stay"}},
		{[]string{"completion", "z"}, []string{"zsh"}},
		{[]string{"logout", "--in"}, []string{"--in-place"}},
		{[]string{"query", "k"}, []string{"k0", "k1", "k2", "k3", "k4"}},
		{[]string{"query", "B"}, []string{"BTIME", "BTRULES"}},
		{[]string{"ai", "train", "--epochs", ""}, nil},
		{[]string{"ai", "train", "--stop-at"}, []string{"--stop-at-data-bytes"}},
		{[]string{"ai", "train", "--stop-at-data-bytes", ""}, nil},
		{[]string{"ai", "train", "--work"}, []string{"--workers"}},
		{[]string{"ai", "train", "--workers", ""}, nil},
		{[]string{"ai", "train", "--demo"}, []string{"--demonstrations"}},
		{[]string{"ai", "train", "--demonstrations", ""}, []string{"@files"}},
		{[]string{"ai", "train", "--batch-episodes", ""}, nil},
		{[]string{"ai", "train", "--gradient-clip", ""}, nil},
		{[]string{"ai", "train", "--gae-"}, []string{"--gae-lambda"}},
		{[]string{"ai", "train", "--gae-lambda", ""}, nil},
		{[]string{"ai", "train", "--entropy-"}, []string{"--entropy-weight"}},
		{[]string{"ai", "train", "--entropy-weight", ""}, nil},
		{[]string{"ai", "train", "--opening-"}, []string{"--opening-rollouts"}},
		{[]string{"ai", "train", "--initial-policy-"}, []string{"--initial-policy-scale"}},
		{[]string{"ai", "train", "--initial-policy-scale", ""}, nil},
		{[]string{"ai", "train", "--opening-rollouts", ""}, nil},
		{[]string{"ai", "train", "--policy-ad"}, []string{"--policy-advantage"}},
		{[]string{"ai", "train", "--policy-advantage", ""}, nil},
		{[]string{"ai", "train", "--rule-opponent", "s"}, []string{"sustain"}},
		{[]string{"ai", "train", "--rule-opponent", "c"}, []string{"control"}},
		{[]string{"ai", "train", "--warmup-teacher", "c"}, []string{"control"}},
		{[]string{"ai", "evaluate", "--opponent", "c"}, []string{"control"}},
		{[]string{"ai", "build-search", "--policy", "c"}, []string{"control"}},
		{[]string{"ai", "train", "--rule-opponent", "ind"}, []string{"independent-control"}},
		{[]string{"ai", "train", "--warmup-teacher", "ind"}, []string{"independent-control"}},
		{[]string{"ai", "evaluate", "--opponent", "ind"}, []string{"independent-control"}},
		{[]string{"ai", "build-search", "--policy", "ind"}, []string{"independent-control"}},
		{[]string{"ai", "train", "--opponent-sampling", "w"}, []string{"weakness-v1"}},
		{[]string{"ai", "train", "--plan-s"}, []string{"--plan-scope"}},
		{[]string{"ai", "train", "--plan-scope", ""}, []string{"member", "team"}},
		{[]string{"ai", "train", "--plan-f"}, []string{"--plan-features"}},
		{[]string{"ai", "train", "--plan-features", ""}, []string{"none", "target-counts-v1"}},
		{[]string{"ai", "train", "--warmup-action-weighting", ""}, []string{"none", "sqrt-action-frequency-v1"}},
		{[]string{"ai", "train", "--action-weighting", "sqrt"}, []string{"sqrt-action-frequency-v1"}},
		{[]string{"ai", "train-feedback", "--action-weighting", ""}, []string{"none", "sqrt-action-frequency-v1"}},
		{[]string{"ai", "train", "--opponent-m"}, []string{"--opponent-mix"}},
		{[]string{"ai", "train", "--opponent-mix", ""}, nil},
		{[]string{"ai", "le"}, []string{"league"}},
		{[]string{"ai", "ch"}, []string{"champion", "check"}},
		{[]string{"ai", "champion", "ch"}, []string{"challenge"}},
		{[]string{"ai", "champion", "init", "--mixed-"}, []string{"--mixed-experiment"}},
		{[]string{"ai", "champion", "challenge", "--mixed-experiment", ""}, []string{"@files"}},
		{[]string{"ai", "champion", "rollback", "--t"}, []string{"--to"}},
		{[]string{"ai", "champion", "challenge", "--directory", ""}, []string{"@dirs"}},
		{[]string{"ai", "verify-"}, []string{"verify-evaluation"}},
		{[]string{"ai", "compare-"}, []string{"compare-evaluations"}},
		{[]string{"ai", "collect-"}, []string{"collect-feedback"}},
		{[]string{"ai", "train-f"}, []string{"train-feedback"}},
		{[]string{"ai", "collect-feedback", "--g"}, []string{"--greedy"}},
		{[]string{"ai", "collect-feedback", "--teacher", "sus"}, []string{"sustain"}},
		{[]string{"ai", "train-feedback", "--collection", ""}, []string{"@dirs"}},
		{[]string{"ai", "compare-evaluations", "--b"}, []string{"--baseline"}},
		{[]string{"ai", "compare-evaluations", "--ca"}, []string{"--candidate"}},
		{[]string{"ai", "compare-evaluations", "--baseline", ""}, []string{"@files"}},
		{[]string{"ai", "compare-evaluations", "--candidate", ""}, []string{"@files"}},
		{[]string{"ai", "verify-evaluation", "--report", ""}, []string{"@files"}},
		{[]string{"ai", "league", "--data-dir", ""}, []string{"@dirs"}},
		{[]string{"ai", "train", "--warmup-teacher", "s"}, []string{"sustain"}},
		{[]string{"ai", "train", "--healing-items", ""}, nil},
		{[]string{"ai", "train", "--reserve-pets", ""}, nil},
		{[]string{"ai", "train", "--pet-skills", ""}, nil},
		{[]string{"ai", "experiment", "--pet-s"}, []string{"--pet-skills"}},
		{[]string{"ai", "experiment", "--reserve-"}, []string{"--reserve-pets"}},
		{[]string{"ai", "experiment", "--healing-"}, []string{"--healing-items", "--healing-magic"}},
		{[]string{"ai", "ex"}, []string{"experiment", "experiment-compare", "experiment-mix", "export-data", "export-model"}},
		{[]string{"ai", "experiment-compare", "--left-experiment", ""}, []string{"@files"}},
		{[]string{"ai", "experiment-compare", "--right-experiment", ""}, []string{"@files"}},
		{[]string{"ai", "evaluate", "--validation-comparison", ""}, []string{"@files"}},
		{[]string{"ai", "evaluate", "--validation-"}, []string{"--validation-comparison"}},
		{[]string{"ai", "experiment-mix", "--f"}, []string{"--families-per-batch", "--from-model"}},
		{[]string{"ai", "experiment-mix", "--weights", ""}, nil},
		{[]string{"ai", "train", "--mixed-experiment", ""}, []string{"@files"}},
		{[]string{"ai", "evaluate", "--mixed-"}, []string{"--mixed-experiment"}},
		{[]string{"ai", "export-data", "--records", ""}, []string{"@dirs"}},
		{[]string{"ai", "export-data", "--format", ""}, []string{"builds", "transitions"}},
		{[]string{"ai", "export-data", "--mode", "pv"}, []string{"pve", "pvp", "pvp-1v1"}},
		{[]string{"ai", "export-data", "--equal-"}, []string{"--equal-points"}},
		{[]string{"ai", "im"}, []string{"import-demonstrations"}},
		{[]string{"ai", "import-demonstrations", "--data"}, []string{"--database"}},
		{[]string{"ai", "import-demonstrations", "--features", "commander-observed-v8"}, []string{"commander-observed-v8"}},
		{[]string{"ai", "train", "--ex"}, []string{"--experiment"}},
		{[]string{"ai", "evaluate", "--split", ""}, []string{"test", "validation"}},
		{[]string{"ai", "evaluate", "--resu"}, []string{"--resume"}},
		{[]string{"ai", "train", "--environment", ""}, []string{"@files"}},
		{[]string{"ai", "train", "--data-dir", ""}, []string{"@dirs"}},
		{[]string{"ai", "experiment", "--train-groups", ""}, nil},
		{[]string{"ai", "b"}, []string{"build-pool", "build-search", "build-validate"}},
		{[]string{"ai", "build-pool", "--search-dir", ""}, []string{"@dirs"}},
		{[]string{"ai", "experiment", "--build-pool", ""}, []string{"@files"}},
		{[]string{"ai", "train", "--from-model", ""}, []string{"@files"}},
		{[]string{"ai", "export-model", "--data-dir", ""}, []string{"@dirs"}},
		{[]string{"ai", "build-search", "--native-candidates", ""}, nil},
		{[]string{"ai", "build-search", "--policy", "g"}, []string{"guard-break"}},
	} {
		if got := Complete(tc.args); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%q: got %q, want %q", tc.args, got, tc.want)
		}
	}
}

func TestCompletionShells(t *testing.T) {
	if _, err := CompletionScript("fish"); err == nil {
		t.Fatal("unsupported shell accepted")
	}
	for _, shell := range []string{"bash", "zsh"} {
		t.Run(shell, func(t *testing.T) {
			program, err := exec.LookPath(shell)
			if err != nil {
				t.Skip("shell unavailable")
			}
			dir := t.TempDir()
			script, err := CompletionScript(shell)
			if err != nil {
				t.Fatal(err)
			}
			name := "sactl.bash"
			if shell == "zsh" {
				name = "_sactl"
			}
			if err = os.WriteFile(filepath.Join(dir, name), []byte(script), 0600); err != nil {
				t.Fatal(err)
			}
			// Keep engine fixtures separate: here verify word boundaries, path
			// quoting and the first zsh autoload invocation without a daemon.
			fixture := "#!/bin/sh\ncase \"$*\" in\n  '__complete ai r') echo run;;\n  '__complete ai run --config a ') echo @files;;\n  *) exit 1;;\nesac\n"
			if err = os.WriteFile(filepath.Join(dir, "sactl"), []byte(fixture), 0700); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(dir, "a file.json"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			body := `source ./sactl.bash
COMP_WORDS=(sactl ai r); COMP_CWORD=2
_sactl_complete
[[ ${COMPREPLY[*]} == run ]] || exit 11
COMP_WORDS=(sactl ai run --config 'a '); COMP_CWORD=4
_sactl_complete
[[ ${#COMPREPLY[@]} == 1 && ${COMPREPLY[0]} == 'a file.json' ]] || exit 12
`
			if shell == "zsh" {
				body = `fpath=("$PWD" $fpath)
autoload -Uz compinit
compinit -D -i
[[ $_comps[sactl] == _sactl ]] || exit 14
compadd() { [[ $candidates == run ]] || exit 13; print 'command-ok'; }
_files() { print 'path-ok'; }
autoload -Uz _sactl
words=(sactl ai r); CURRENT=3
_sactl
words=(sactl ai run --config 'a '); CURRENT=5
_sactl
source ./_sactl
words=(sactl ai r); CURRENT=3
_sactl
`
			}
			cmd := exec.Command(program, "-f", "-c", body)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("shell: %v: %s", err, out)
			}
			if shell == "zsh" && strings.TrimSpace(string(out)) != "command-ok\npath-ok\ncommand-ok" {
				t.Fatalf("autoload/source did not complete: %s", out)
			}
		})
	}
}
