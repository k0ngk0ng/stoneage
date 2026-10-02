package arenaagent

import (
	"bytes"
	"context"
	"encoding/csv"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestTrainingEnvironmentDefaultAndExplicitOverride(t *testing.T) {
	t.Setenv(trainingEnvironmentVariable, "image-environment.json")
	f := flag.NewFlagSet("test", flag.ContinueOnError)
	path := trainingEnvironmentFlag(f)
	if *path != "image-environment.json" {
		t.Fatal("image default missing")
	}
	if err := f.Parse([]string{"--environment", "another.json"}); err != nil || *path != "another.json" {
		t.Fatal("explicit environment lost", err)
	}
	for _, args := range [][]string{{"environment", "check"}, {"experiment", "--output", "unused"}, {"train", "--data-dir", "unused"}, {"evaluate", "--model", "unused", "--output", "unused"}, {"build-search", "--data-dir", "unused"}} {
		var out bytes.Buffer
		err := Main(context.Background(), args, "test", &out)
		if err == nil || !strings.Contains(err.Error(), "image-environment.json") {
			t.Fatal("command did not load image's default environment", args, err)
		}
	}
}

func TestTrainingContainerUsesPinnedImageAndSafeMount(t *testing.T) {
	image := trainingImage{ID: "sha256:" + strings.Repeat("a", 64), OS: "linux", Architecture: "arm64"}
	records := "/path/with spaces,quotes\"/rules"
	args := trainingContainerCommand(image, records)
	if args[len(args)-1] != image.ID {
		t.Fatal("mutable image tag used")
	}
	for _, pair := range [][2]string{{"--pull", "never"}, {"--platform", "linux/arm64"}, {"--network", "none"}, {"--entrypoint", trainingWorker}, {"--env", "STONEAGE_BATTLE_RECORD_DIR=/data/rules"}} {
		found := false
		for i, arg := range args[:len(args)-1] {
			found = found || arg == pair[0] && args[i+1] == pair[1]
		}
		if !found {
			t.Fatal("worker isolation/entrypoint changed", pair, args)
		}
	}
	for i, arg := range args {
		if arg == "--mount" {
			fields, err := csv.NewReader(strings.NewReader(args[i+1])).Read()
			if err != nil || !reflect.DeepEqual(fields, []string{"type=bind", "src=" + records, "dst=/data/rules"}) {
				t.Fatal("mount path changed or became another Docker option", fields, err)
			}
		}
	}
}

func TestEnvironmentFailureDoesNotPublishConfig(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake Docker executable uses a POSIX shell")
	}
	// Fake Docker runs in a separate process and emits real native greeting
	// framing. Production workers are separately checked by the native suite.
	dir := t.TempDir()
	docker := filepath.Join(dir, "docker")
	script := `#!/bin/sh
if [ "$1" = image ]; then
  printf '{"ID":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","OS":"linux","Architecture":"arm64","Schema":"%s"}\n' "$TEST_IMAGE_SCHEMA"
  exit 0
fi
if [ "$1" = ps ]; then exit 0; fi
if [ "$1" != run ]; then exit 3; fi
printf '{"schema_version":1,"ok":true,"ready":true,"rules_digest":"%s","platform":"linux-arm64","scenario":"controlled-battle-v8"}\n' "$TEST_RULES_DIGEST"
while read -r line; do :; done
`
	if err := os.WriteFile(docker, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TEST_IMAGE_SCHEMA", "0")
	t.Setenv("TEST_RULES_DIGEST", strings.Repeat("b", 64))
	newDir := filepath.Join(dir, "new")
	args := []string{"environment", "init", "--image", "example/training:v1", "--directory", newDir}
	var out bytes.Buffer
	if err := Main(context.Background(), args, "test", &out); err == nil {
		t.Fatal("old/nontraining image accepted")
	}
	if _, err := os.Stat(newDir); !os.IsNotExist(err) {
		t.Fatal("incompatible image created an environment")
	}
	t.Setenv("TEST_IMAGE_SCHEMA", "1")
	t.Setenv("TEST_RULES_DIGEST", "")
	if err := Main(context.Background(), args, "test", &out); err == nil {
		t.Fatal("worker without real rules digest accepted")
	}
	if _, err := os.Stat(filepath.Join(newDir, "environment.json")); !os.IsNotExist(err) {
		t.Fatal("failed worker published configuration")
	}
	// Keep the failed evidence and initialize at a new destination.
	newDir = filepath.Join(dir, "valid")
	args[len(args)-1] = newDir
	t.Setenv("TEST_RULES_DIGEST", strings.Repeat("b", 64))
	if err := Main(context.Background(), args, "test", &out); err != nil {
		t.Fatal(err)
	}
	command, err := loadTrainingEnvironment(filepath.Join(newDir, "environment.json"))
	if err != nil || command[len(command)-1] != "sha256:"+strings.Repeat("a", 64) {
		t.Fatal("published environment did not pin inspected image", command, err)
	}
	if err := Main(context.Background(), args, "test", &out); err == nil {
		t.Fatal("existing environment overwritten")
	}
}
