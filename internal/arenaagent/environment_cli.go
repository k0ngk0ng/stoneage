package arenaagent

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/battleenv"
)

const trainingEnvironmentVariable = "STONEAGE_TRAINING_ENVIRONMENT"
const trainingWorker = "/opt/stoneage/bin/start-training-worker.sh"

// The training image supplies this default. A normal installed client has no
// implicit engine or Docker dependency; an explicit CLI argument always wins.
func trainingEnvironmentFlag(f *flag.FlagSet) *string {
	return f.String("environment", os.Getenv(trainingEnvironmentVariable), "native worker argv JSON; defaults to STONEAGE_TRAINING_ENVIRONMENT when set")
}

type trainingImage struct {
	ID, OS, Architecture string
	Schema               string
}

func inspectTrainingImage(ctx context.Context, reference string) (trainingImage, error) {
	var image trainingImage
	if reference == "" || strings.HasPrefix(reference, "-") || strings.ContainsAny(reference, " \t\r\n") {
		return image, fmt.Errorf("--image requires an installed training image reference")
	}
	format := `{"ID":{{json .Id}},"OS":{{json .Os}},"Architecture":{{json .Architecture}},"Schema":{{with (index .Config "Labels")}}{{json (index . "org.stoneage.training.schema")}}{{else}}""{{end}}}`
	raw, err := exec.CommandContext(ctx, "docker", "image", "inspect", "--format", format, reference).Output()
	if err != nil {
		return image, fmt.Errorf("cannot inspect local image %q; install the matching ai-training image explicitly first: %w", reference, err)
	}
	if json.Unmarshal(raw, &image) != nil || !regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(image.ID) || image.OS != "linux" || (image.Architecture != "arm64" && image.Architecture != "amd64") || image.Schema != "1" {
		return image, fmt.Errorf("image is not a supported StoneAge training toolkit (schema 1, Linux amd64/arm64)")
	}
	return image, nil
}

func trainingContainerCommand(image trainingImage, records string) []string {
	var mount bytes.Buffer
	w := csv.NewWriter(&mount)
	_ = w.Write([]string{"type=bind", "src=" + records, "dst=/data/rules"})
	w.Flush()
	return []string{"docker", "run", "--rm", "-i", "--pull", "never", "--platform", image.OS + "/" + image.Architecture, "--network", "none", "--read-only",
		"--tmpfs", "/tmp:rw,size=64m", "--mount", strings.TrimSuffix(mount.String(), "\n"),
		"--env", "STONEAGE_BATTLE_RECORD_DIR=/data/rules", "--entrypoint", trainingWorker, image.ID}
}

// Join cleanup errors with the original failure and keep them visible to CLI
// callers. Explicit Close before success output uses the same cached result.
func closeNativeEngine(engine *battleenv.Native, result *error) {
	if err := engine.Close(); err != nil && !errors.Is(*result, err) {
		*result = errors.Join(*result, fmt.Errorf("native worker shutdown: %w", err))
	}
}

func checkTrainingEnvironment(ctx context.Context, command []string, stderr io.Writer) (meta battleenv.Metadata, resultErr error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	engine, err := battleenv.Start(ctx, command, stderr)
	if err != nil {
		return battleenv.Metadata{}, err
	}
	defer closeNativeEngine(engine, &resultErr)
	meta = engine.Metadata()
	if err := meta.Validate(); err != nil {
		return meta, err
	}
	return meta, nil
}

func environmentCommand(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" {
		_, err := fmt.Fprintln(out, "sactl ai environment: prepare or check a local native training worker\nCommands: init --image <installed-ai-training-image> --directory <new-directory>; check --environment <file>\nNo download, game login or network listener. init pins the local image ID and verifies the real worker before publishing environment.json.")
		return err
	}
	f := flag.NewFlagSet("sactl ai environment "+args[0], flag.ContinueOnError)
	f.SetOutput(out)
	var image, directory, environment string
	switch args[0] {
	case "init":
		f.StringVar(&image, "image", "", "already installed ai-training image; pinned to its immutable local ID, never pulled")
		f.StringVar(&directory, "directory", "", "new local directory for environment.json and persistent rule archives")
	case "check":
		f.StringVar(&environment, "environment", os.Getenv(trainingEnvironmentVariable), "worker argv JSON; defaults to STONEAGE_TRAINING_ENVIRONMENT")
	default:
		return fmt.Errorf("unknown environment command %q; use environment --help", args[0])
	}
	if err := f.Parse(args[1:]); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}
	if f.NArg() != 0 {
		return fmt.Errorf("unexpected environment arguments")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	ctx, interrupt := signal.NotifyContext(ctx, os.Interrupt)
	defer interrupt()
	if args[0] == "check" {
		if environment == "" {
			return fmt.Errorf("environment check requires --environment")
		}
		command, err := loadTrainingEnvironment(environment)
		if err != nil {
			return err
		}
		meta, err := checkTrainingEnvironment(ctx, command, os.Stderr)
		if err != nil {
			return err
		}
		_, err = out.Write(append(enc(Object{"event": "environment_checked", "environment": environment, "metadata": meta}), '\n'))
		return err
	}
	if image == "" || directory == "" {
		return fmt.Errorf("environment init requires --image and --directory")
	}
	dir, err := filepath.Abs(directory)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(dir); err == nil || !os.IsNotExist(err) {
		return fmt.Errorf("environment directory must be new")
	}
	local, err := inspectTrainingImage(ctx, image)
	if err != nil {
		return err
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		return err
	}
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		return err
	}
	records := filepath.Join(dir, "rules")
	if err := os.Mkdir(records, 0700); err != nil {
		return err
	}
	command := trainingContainerCommand(local, records)
	log, err := os.OpenFile(filepath.Join(dir, "worker.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer log.Close()
	meta, err := checkTrainingEnvironment(ctx, command, log)
	if err != nil {
		return fmt.Errorf("training worker check failed; evidence retained in %s: %w", dir, err)
	}
	if meta.Platform != "linux-"+local.Architecture {
		return fmt.Errorf("worker platform differs from inspected image")
	}
	path := filepath.Join(dir, "environment.json")
	file, err := os.CreateTemp(dir, ".environment-*.tmp")
	if err != nil {
		return err
	}
	data := append(enc(Object{"schema_version": 1, "command": command}), '\n')
	_, err = file.Write(data)
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return err
	}
	_, err = out.Write(append(enc(Object{"event": "environment_created", "environment": path, "image": local.ID, "platform": meta.Platform, "metadata": meta}), '\n'))
	return err
}
