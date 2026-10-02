package battleenv

import (
	"context"
	"encoding/hex"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const nativeDockerOwnerLabel = "org.stoneage.training.worker"

// Only a direct Docker run is managed. Arbitrary shell/SSH wrappers keep their
// own remote-process lifecycle contract; never guess their container identity.
type dockerOwner struct {
	executable  string
	environment []string
	identity    string
}

func ownDockerCommand(command []string, identity string) ([]string, *dockerOwner, error) {
	name := strings.ToLower(filepath.Base(command[0]))
	if (name != "docker" && name != "docker.exe") || len(command) < 2 || command[1] != "run" {
		return command, nil, nil
	}
	for _, arg := range command[2:] {
		if strings.Contains(arg, nativeDockerOwnerLabel) {
			return nil, nil, fmt.Errorf("native worker command uses reserved ownership label %s", nativeDockerOwnerLabel)
		}
	}
	args := append([]string{command[0], "run", "--label", nativeDockerOwnerLabel + "=" + identity}, command[2:]...)
	return args, &dockerOwner{identity: identity}, nil
}

// Docker CLI termination does not terminate its remote container. Resolve only
// the private label assigned to this invocation, verify it again on the full ID,
// and remove that exact ID. Volumes and unrelated containers are never removed.
func (owner *dockerOwner) cleanup() error {
	if owner == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := func(args ...string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, owner.executable, args...)
		cmd.Env = owner.environment
		return cmd
	}
	out, err := command("ps", "-aq", "--no-trunc", "--filter", "label="+nativeDockerOwnerLabel+"="+owner.identity).Output()
	if err != nil {
		return fmt.Errorf("inspect owned native Docker container: %w", err)
	}
	ids := strings.Fields(string(out))
	if len(ids) > 1 {
		return fmt.Errorf("native Docker ownership is ambiguous; refused cleanup")
	}
	for _, id := range ids {
		if raw, err := hex.DecodeString(id); err != nil || len(raw) != 32 || len(id) != 64 {
			return fmt.Errorf("invalid owned native Docker container ID; refused cleanup")
		}
		out, err := command("inspect", "--format", `{{index .Config.Labels "`+nativeDockerOwnerLabel+`"}}`, id).Output()
		if err != nil {
			// --rm may have won the race after the initial listing. Verify
			// absence using a successful daemon query, not error-text matching.
			left, checkErr := command("ps", "-aq", "--no-trunc", "--filter", "id="+id).Output()
			if checkErr == nil && strings.TrimSpace(string(left)) == "" {
				continue
			}
			return fmt.Errorf("verify owned native Docker container %s: %w", id, err)
		}
		if strings.TrimSpace(string(out)) != owner.identity {
			return fmt.Errorf("native Docker ownership mismatch for %s; refused cleanup", id)
		}
		if err := command("rm", "-f", id).Run(); err != nil {
			left, checkErr := command("ps", "-aq", "--no-trunc", "--filter", "id="+id).Output()
			if checkErr == nil && strings.TrimSpace(string(left)) == "" {
				continue
			}
			return fmt.Errorf("remove owned native Docker container %s: %w", id, err)
		}
	}
	return nil
}
