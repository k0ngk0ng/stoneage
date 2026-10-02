package battleenv

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestDockerOwnershipDoesNotRewriteCustomWrappers(t *testing.T) {
	for _, original := range [][]string{{"sh", "worker.sh"}, {"ssh", "worker", "docker", "run"}, {"docker", "--context", "other", "run"}} {
		args, owner, err := ownDockerCommand(original, "identity")
		if err != nil || owner != nil || !reflect.DeepEqual(args, original) {
			t.Fatal("guessed custom wrapper ownership", args, owner, err)
		}
	}
	original := []string{"docker", "run", "--rm", "-i", "image", "command"}
	before := append([]string(nil), original...)
	args, owner, err := ownDockerCommand(original, "unique")
	if err != nil || owner == nil || !reflect.DeepEqual(original, before) || !reflect.DeepEqual(args[4:], original[2:]) {
		t.Fatal("rewrote caller argv", args, err)
	}
	if _, _, err := ownDockerCommand([]string{"docker", "run", "--label", nativeDockerOwnerLabel + "=override", "image"}, "unique"); err == nil {
		t.Fatal("caller could override ownership")
	}
}

func TestDockerCleanupRefusesUnverifiedOwnership(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture is a POSIX shell executable")
	}
	for _, mode := range []string{"mismatch", "ambiguous", "invalid", "daemon-failure"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			path, removed := filepath.Join(dir, "docker"), filepath.Join(dir, "removed")
			script := "#!/bin/sh\ncase \"$1\" in\nps)\n"
			switch mode {
			case "daemon-failure":
				script += "exit 7\n"
			case "invalid":
				script += "echo not-a-container-id\n"
			case "ambiguous":
				script += "echo " + strings.Repeat("a", 64) + "\necho " + strings.Repeat("b", 64) + "\n"
			default:
				script += "echo " + strings.Repeat("a", 64) + "\n"
			}
			script += ";;\ninspect) echo another-owner;;\nrm) : > \"$REMOVED_MARKER\";;\nesac\n"
			if err := os.WriteFile(path, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			owner := dockerOwner{executable: path, environment: append(os.Environ(), "REMOVED_MARKER="+removed), identity: "unique"}
			if err := owner.cleanup(); err == nil {
				t.Fatal("unverified container accepted")
			}
			if _, err := os.Stat(removed); !os.IsNotExist(err) {
				t.Fatal("deleted unverified container", err)
			}
		})
	}
}
