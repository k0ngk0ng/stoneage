package sacli

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aicontrol"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
	"github.com/k0ngk0ng/stoneage/internal/websession"
)

const QuestHelp = `sactl quest: automatic quests on the current logged-in HTTP session
  list                               list tasks, requirements and review blockers
  preview <task-id> [options]         validate the route, requirements and budget
  start <task-id> [options]           start the shared Web task executor
  status                             show progress, result or recovery offer
  pause                              pause the current quest
  resume                             resume the paused quest or its saved recovery
  cancel                             cancel this quest or discard its saved recovery

Options for preview/start (after task-id):
  --include-dependencies             include prerequisite tasks
  --pet <stable-id>                   explicitly select a pet when required
  --maximum-seconds <N>               total time limit (default 1800)
  --maximum-deaths <N>                allowed deaths (default 0)
  --maximum-spend <N>                 maximum gold to spend (default 0)
  --reserve <N>                       gold to keep (default 0)

Use --json for structured results and --profile to select a logged-in character.
The task continues after this command returns while the game session stays connected.
After reconnecting, inspect status and explicitly resume or cancel saved work.
Unverified tasks remain blocked; this command does not bypass review.
Direct TCP sessions do not yet support quest control.`

type questControl struct {
	Control  aicontrol.State    `json:"control"`
	Active   bool               `json:"automation_active"`
	Mode     aicontrol.Mode     `json:"automation_mode"`
	Task     *aimcp.TaskReceipt `json:"automation_task"`
	Recovery *struct {
		Handle string         `json:"handle"`
		Mode   aicontrol.Mode `json:"mode"`
		Reason string         `json:"reason"`
	} `json:"automation_recovery"`
	RecoveryUnavailable bool `json:"automation_recovery_unavailable"`
}

// Called with autoMu held, without acquiring connectMu (login takes the
// locks in the other order). The game connection itself scopes the query.
func (s *Server) checkRemoteQuestControl() error {
	s.mu.Lock()
	game := s.game
	s.mu.Unlock()
	client, ok := game.(websession.AutomationClient)
	if !ok {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	raw, err := client.AutomationCall(ctx, "status", nil)
	if err != nil {
		return err
	}
	var status questControl
	if err := json.Unmarshal(raw, &status); err != nil || status.Control.Generation == 0 {
		return fmt.Errorf("cannot verify remote task control; auto-battle was not started")
	}
	if status.Active && (status.Mode == aicontrol.Quest || status.Mode == aicontrol.Leveling) || status.Recovery != nil || status.RecoveryUnavailable {
		return fmt.Errorf("resolve the current or saved automatic task before starting auto-battle")
	}
	return nil
}

func questStartBody(args []string) (map[string]any, error) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") || strings.TrimSpace(args[0]) == "" {
		return nil, fmt.Errorf("task-id is required; run sactl quest list")
	}
	set := flag.NewFlagSet("quest", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	dependencies := set.Bool("include-dependencies", false, "")
	pet := set.String("pet", "", "")
	seconds := set.Int("maximum-seconds", 1800, "")
	deaths := set.Int("maximum-deaths", 0, "")
	spend := set.Int64("maximum-spend", 0, "")
	reserve := set.Int64("reserve", 0, "")
	if err := set.Parse(args[1:]); err != nil {
		return nil, err
	}
	if set.NArg() != 0 || *seconds <= 0 || *deaths < 0 || *spend < 0 || *reserve < 0 {
		return nil, fmt.Errorf("invalid quest arguments or limits; run sactl quest --help")
	}
	return map[string]any{"mode": "quest", "task_id": args[0], "include_dependencies": *dependencies,
		"selected_pet_id": *pet, "maximum_seconds": *seconds, "maximum_deaths": *deaths,
		"budget": map[string]int64{"maximum_spend": *spend, "reserve": *reserve}}, nil
}

func (s *Server) commandQuest(ctx context.Context, request Request) Response {
	args := request.Args
	if len(args) == 0 || len(args) == 1 && (args[0] == "help" || args[0] == "--help" || args[0] == "-h") {
		return Response{OK: true, Text: QuestHelp}
	}
	op := args[0]
	var body map[string]any
	var err error
	switch op {
	case "preview", "start":
		body, err = questStartBody(args[1:])
	case "list", "status", "pause", "resume", "cancel":
		if len(args) != 1 {
			err = fmt.Errorf("quest %s does not accept arguments", op)
		}
	default:
		err = fmt.Errorf("unknown quest operation %q; run sactl quest --help", op)
	}
	if err != nil {
		return failure(KindUsage, "%v", err)
	}
	// Keep the authenticated character stable for observation + mutation.
	s.connectMu.Lock()
	defer s.connectMu.Unlock()
	game, err := s.connectSession(ctx)
	if err != nil {
		return sessionFailure(err)
	}
	client, ok := game.(websession.AutomationClient)
	if !ok {
		return failure(KindAction, "quest control requires an HTTP game session; direct TCP is not supported")
	}
	if op == "list" {
		return questCall(ctx, client, "tasks", nil)
	}
	if op == "status" {
		return questCall(ctx, client, "status", nil)
	}
	// A local auto-battle loop cannot be allowed to compete with a quest.
	s.autoMu.Lock()
	defer s.autoMu.Unlock()
	if (op == "start" || op == "resume") && s.autoRunning {
		return failure(KindAction, "stop auto-battle before starting or resuming a quest")
	}
	raw, err := client.AutomationCall(ctx, "status", nil)
	if err != nil {
		return actionFailure(err)
	}
	var status questControl
	if err := json.Unmarshal(raw, &status); err != nil || status.Control.Generation == 0 {
		return failure(KindServer, "quest control response lacks a valid generation; no action sent")
	}
	if op == "start" || op == "preview" {
		body["generation"] = status.Control.Generation
	} else {
		body = map[string]any{"generation": status.Control.Generation, "mode": "quest"}
		if status.Recovery != nil && (op == "resume" || op == "cancel") {
			if status.Recovery.Mode != aicontrol.Quest {
				return failure(KindAction, "saved automation is not a quest")
			}
			body["recovery_handle"] = status.Recovery.Handle
		} else if !status.Active || status.Mode != aicontrol.Quest {
			return failure(KindAction, "no active quest; run sactl quest status")
		}
	}
	return questCall(ctx, client, op, body)
}

func questCall(ctx context.Context, client websession.AutomationClient, operation string, body any) Response {
	raw, err := client.AutomationCall(ctx, operation, body)
	if err != nil {
		return actionFailure(err)
	}
	text, err := renderQuest(operation, raw)
	if err != nil {
		return failure(KindServer, "invalid quest response: %v; inspect quest status before retrying", err)
	}
	return Response{OK: true, Text: text, Data: raw}
}

func renderQuest(operation string, raw json.RawMessage) (string, error) {
	var out strings.Builder
	if operation == "tasks" {
		var catalog struct {
			Tasks []struct {
				ID, Name     string
				Requirements []string `json:"requirements"`
				Blockers     []string `json:"review_blockers"`
			} `json:"tasks"`
		}
		if err := json.Unmarshal(raw, &catalog); err != nil {
			return "", err
		}
		for _, task := range catalog.Tasks {
			fmt.Fprintf(&out, "%s  %s\n", task.ID, task.Name)
			for _, item := range task.Requirements {
				fmt.Fprintf(&out, "  requires: %s\n", item)
			}
			for _, item := range task.Blockers {
				fmt.Fprintf(&out, "  blocked: %s\n", item)
			}
		}
		if len(catalog.Tasks) == 0 {
			out.WriteString("no automatic quests available\n")
		}
	} else if operation == "preview" {
		var pretty bytes.Buffer
		if err := json.Indent(&pretty, raw, "", "  "); err != nil {
			return "", err
		}
		return pretty.String(), nil
	} else {
		var status questControl
		if err := json.Unmarshal(raw, &status); err != nil {
			return "", err
		}
		if status.Control.Generation == 0 {
			return "", fmt.Errorf("missing control generation")
		}
		fmt.Fprintf(&out, "control=%s active=%t mode=%s\n", status.Control.Mode, status.Active, status.Mode)
		if task := status.Task; task != nil {
			fmt.Fprintf(&out, "task=%s status=%s state=%s\n", task.Handle, task.Status, task.State)
			if p := task.Progress; p != nil {
				fmt.Fprintf(&out, "steps=%d/%d stages=%d/%d deaths=%d\n", p.Step, p.Steps, p.Stage, p.Stages, p.Deaths)
			}
			if task.Reason != "" {
				fmt.Fprintln(&out, task.Reason)
			}
		}
		if status.Recovery != nil {
			fmt.Fprintf(&out, "saved %s: %s\nuse quest resume or quest cancel for a saved quest\n", status.Recovery.Mode, status.Recovery.Reason)
		}
		if status.RecoveryUnavailable {
			out.WriteString("saved task status unavailable; retry status before starting a new task\n")
		}
	}
	return strings.TrimSpace(out.String()), nil
}
