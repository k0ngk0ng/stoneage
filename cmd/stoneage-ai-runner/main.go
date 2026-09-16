// stoneage-ai-runner is the narrow process boundary for one StoneAge AI
// profile.  The authenticated control plane supplies one request on stdin;
// this command constructs the runner from fixed process configuration,
// executes one turn, and emits one JSON response on stdout.
//
// It intentionally has no HTTP listener, shell interpolation, model API
// implementation, or profile/path fields in the request.  A supervisor can
// start one instance per profile with a private state volume.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/k0ngk0ng/stoneage/internal/airunner"
)

type options struct {
	profile string
	state   string
	codex   string
	mcp     string
	skills  string
	git     string
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// run is kept separate from main so the protocol and signal behavior can be
// tested without replacing the test process.  It returns a conventional
// process status: zero only when the request was executed successfully.
func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	opts, err := parseOptions(args, os.Getenv)
	if err != nil {
		writeError(stderr, errorCode(err))
		return 2
	}

	executor, err := airunner.New(airunner.Config{
		ProfileID:   opts.profile,
		StateRoot:   opts.state,
		CodexBinary: opts.codex,
		MCPBinary:   opts.mcp,
		SkillRoot:   opts.skills,
		GitBinary:   opts.git,
	})
	if err != nil {
		writeError(stderr, errorCode(err))
		return 2
	}

	response, executeErr := executor.ExecuteJSON(ctx, stdin)
	if err := airunner.WriteResponse(stdout, response); err != nil {
		writeError(stderr, errorCode(err))
		return 1
	}
	if executeErr != nil {
		writeError(stderr, errorCode(executeErr))
		return 1
	}
	return 0
}

type getenv func(string) string

func parseOptions(args []string, lookup getenv) (options, error) {
	if lookup == nil {
		lookup = os.Getenv
	}
	defaults := options{
		profile: firstEnv(lookup, "STONEAGE_AI_PROFILE_ID", "STONEAGE_AI_PROFILE"),
		state:   firstEnv(lookup, "STONEAGE_AI_STATE_ROOT", "STONEAGE_AI_STATE"),
		codex:   firstEnv(lookup, "STONEAGE_AI_CODEX_BINARY", "STONEAGE_AI_CODEX"),
		mcp:     firstEnv(lookup, "STONEAGE_AI_MCP_BINARY", "STONEAGE_AI_MCP"),
		skills:  firstEnv(lookup, "STONEAGE_AI_SKILL_ROOT", "STONEAGE_AI_SKILLS"),
		git:     firstEnv(lookup, "STONEAGE_AI_GIT_BINARY", "STONEAGE_AI_GIT"),
	}
	opts := defaults
	flags := flag.NewFlagSet("stoneage-ai-runner", flag.ContinueOnError)
	// The flag package writes parse diagnostics by default. Keep the command's
	// stderr contract stable and let the caller receive only an error category.
	flags.SetOutput(io.Discard)
	flags.StringVar(&opts.profile, "profile", defaults.profile, "fixed AI profile identifier")
	flags.StringVar(&opts.profile, "profile-id", defaults.profile, "fixed AI profile identifier")
	flags.StringVar(&opts.state, "state", defaults.state, "private persistent AI state root")
	flags.StringVar(&opts.state, "state-root", defaults.state, "private persistent AI state root")
	flags.StringVar(&opts.codex, "codex", defaults.codex, "fixed Codex executable")
	flags.StringVar(&opts.codex, "codex-binary", defaults.codex, "fixed Codex executable")
	flags.StringVar(&opts.mcp, "mcp", defaults.mcp, "fixed StoneAge game MCP executable")
	flags.StringVar(&opts.mcp, "mcp-binary", defaults.mcp, "fixed StoneAge game MCP executable")
	flags.StringVar(&opts.skills, "skills", defaults.skills, "fixed native Skill root")
	flags.StringVar(&opts.skills, "skill-root", defaults.skills, "fixed native Skill root")
	flags.StringVar(&opts.git, "git", defaults.git, "optional fixed git executable")
	flags.StringVar(&opts.git, "git-binary", defaults.git, "optional fixed git executable")
	if err := flags.Parse(args); err != nil {
		return options{}, fmt.Errorf("%w: command arguments", airunner.ErrInvalidConfig)
	}
	if flags.NArg() != 0 {
		return options{}, fmt.Errorf("%w: positional arguments are not allowed", airunner.ErrInvalidConfig)
	}
	if opts.profile == "" || opts.state == "" || opts.codex == "" || opts.mcp == "" || opts.skills == "" {
		return options{}, airunner.ErrInvalidConfig
	}
	return opts, nil
}

func firstEnv(lookup getenv, names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(lookup(name)); value != "" {
			return value
		}
	}
	return ""
}

func errorCode(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, airunner.ErrOutput) {
		return "output_failed"
	}
	return airunner.ErrorCode(err)
}

func writeError(writer io.Writer, code string) {
	if strings.TrimSpace(code) == "" {
		code = "execution_failed"
	}
	_, _ = io.WriteString(writer, code+"\n")
}
