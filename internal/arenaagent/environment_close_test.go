package arenaagent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnvironmentCloseHelper(t *testing.T) {
	mode := ""
	for _, arg := range os.Args {
		if strings.HasPrefix(arg, "--environment-close=") {
			mode = strings.TrimPrefix(arg, "--environment-close=")
		}
	}
	if mode == "" {
		return
	}
	fmt.Printf("{\"schema_version\":1,\"ok\":true,\"ready\":true,\"scenario\":\"controlled-battle-v8\",\"platform\":\"linux-arm64\",\"rules_digest\":%q}\n", strings.Repeat("a", 64))
	_, _ = io.Copy(io.Discard, os.Stdin)
	if mode == "failure" {
		os.Exit(7)
	}
	os.Exit(0)
}

func TestEnvironmentAndExperimentRequireCleanWorkerExit(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"success", "failure"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "environment.json")
			data, _ := json.Marshal(Object{"schema_version": 1, "command": []string{exe, "-test.run=^TestEnvironmentCloseHelper$", "--", "--environment-close=" + mode}})
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			manifest := filepath.Join(dir, "experiment.json")
			for _, args := range [][]string{
				{"environment", "check", "--environment", path},
				{"experiment", "--environment", path, "--output", manifest},
			} {
				var out bytes.Buffer
				err := Main(context.Background(), args, "test", &out)
				if mode == "success" {
					if err != nil || out.Len() == 0 {
						t.Fatalf("valid shutdown failed: %s %v", out.String(), err)
					}
				} else if err == nil || !strings.Contains(err.Error(), "exit status 7") || out.Len() != 0 {
					t.Fatalf("failed shutdown claimed success: %s %v", out.String(), err)
				}
			}
			if mode == "failure" {
				if _, err := os.Stat(manifest); !os.IsNotExist(err) {
					t.Fatal("published manifest after failed worker exit", err)
				}
			}
		})
	}
}
