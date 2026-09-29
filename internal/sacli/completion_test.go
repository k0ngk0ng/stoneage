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
		{[]string{"ai", "r"}, []string{"run"}},
		{[]string{"ai", "run", "--f"}, []string{"--forever"}},
		{[]string{"ai", "run", "--config", "a directory/"}, []string{"@files"}},
		{[]string{"ai", "init", "--directory", "a directory/"}, []string{"@dirs"}},
		{[]string{"ai", "simulate", "--strategy", "l"}, []string{"learned"}},
		{[]string{"ai", "init", "--mode", ""}, []string{"1", "2", "3", "4", "5"}},
		{[]string{"--socket", "some socket", "ladder", "qu"}, []string{"queue"}},
		{[]string{"--json", "pet", "re"}, []string{"rename"}},
		{[]string{"auto-battle", "on", "st"}, []string{"stay"}},
		{[]string{"completion", "z"}, []string{"zsh"}},
		{[]string{"logout", "--in"}, []string{"--in-place"}},
		{[]string{"query", "k"}, []string{"k0", "k1", "k2", "k3", "k4"}},
		{[]string{"query", "B"}, []string{"BTIME"}},
		{[]string{"ai", "train", "--epochs", ""}, nil},
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
