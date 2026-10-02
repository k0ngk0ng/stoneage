package sacli

import (
	"embed"
	"fmt"
	"sort"
	"strings"
)

//go:embed completions/*
var completionScripts embed.FS

func CompletionScript(shell string) (string, error) {
	name := map[string]string{"bash": "sactl.bash", "zsh": "_sactl"}[shell]
	if name == "" {
		return "", fmt.Errorf("usage: sactl completion <bash|zsh>")
	}
	b, err := completionScripts.ReadFile("completions/" + name)
	return string(b), err
}

// Complete is local and side-effect free. The final word is the current prefix;
// @files and @dirs delegate path completion to the shell (including quoting).
func Complete(words []string) []string {
	if len(words) == 0 {
		words = []string{""}
	}
	prefix := words[len(words)-1]
	args := words[:len(words)-1]
	if len(args) > 0 {
		previous := args[len(args)-1]
		switch previous {
		case "--config", "-config", "--socket", "-socket", "--model", "--database", "--demonstrations", "--output", "--report", "--environment", "--manifest", "--experiment", "--mixed-experiment", "--validation-comparison", "--left-experiment", "--right-experiment", "--opponent-model", "--from-model", "--build-pool", "--baseline", "--candidate":
			return []string{"@files"}
		case "--directory", "--root", "--work", "--native-dir", "--data-dir", "--search-dir", "--records", "--collection", "--state-dir":
			return []string{"@dirs"}
		case "--profile", "use":
			return completePrefix(strings.Join(KnownProfiles(), " "), prefix)
		case "--transport":
			return completePrefix("http tcp", prefix)
		case "--map-directory":
			return []string{"@dirs"}
		case "--web-base-url", "--address", "--server-id", "--character", "--checkpoint", "--opponent-mix", "--initial-policy-scale":
			return nil
		case "--mode":
			if len(args) >= 3 && args[0] == "ai" && args[1] == "export-data" {
				return completePrefix("all pve pvp pvp-1v1", prefix)
			}
			return completePrefix("1 2 3 4 5", prefix)
		case "--format":
			if len(args) >= 3 && args[0] == "ai" && args[1] == "export-data" {
				return completePrefix("builds transitions", prefix)
			}
			return nil
		case "--strategy", "--opponent-strategy":
			if len(args) >= 2 && args[0] == "ai" && (args[1] == "run" || args[1] == "check") {
				return completePrefix("basic learned llm hybrid", prefix)
			}
			return completePrefix("basic learned explore", prefix)
		case "--llm-response-format":
			return completePrefix("json_object json_schema none", prefix)
		case "--llm-endpoint", "--llm-model", "--llm-api-key-env", "--llm-timeout", "--llm-context-bytes", "--pet-mask":
			return nil
		case "--opponent-sampling":
			return completePrefix("uniform weakness-v1", prefix)
		case "--plan-features":
			return completePrefix("none target-counts-v1", prefix)
		case "--plan-scope":
			return completePrefix("team member", prefix)
		case "--action-weighting", "--warmup-action-weighting":
			return completePrefix("none sqrt-action-frequency-v1", prefix)
		case "--features":
			return completePrefix("commander-observed-v6 commander-observed-v7 commander-observed-v8", prefix)
		case "--split":
			return completePrefix("validation test", prefix)
		case "--opponent", "--warmup-teacher", "--policy", "--rule-opponent", "--teacher":
			return completePrefix("basic focus guard-break defensive sustain control independent-control", prefix)
		case "--time":
			return completePrefix("M A N", prefix)
		case "--matches", "--epochs", "--seed", "--image", "--timeout", "-timeout", "--to", "--reason", "--min-groups", "--groups", "--alpha", "--rule-score", "--champion-margin",
			"--request-id", "--revision", "--color", "--range", "--hometown", "--slot", "--face",
			"--vital", "--strength", "--toughness", "--dexterity", "--earth", "--water", "--fire", "--wind",
			"--weights", "--families-per-batch", "--allocation", "--member-allocation", "--fixture-level", "--min-battle-turns", "--batches", "--workers", "--stop-at-data-bytes", "--batch-matches", "--batch-episodes", "--gradient-clip", "--points", "--pet-points", "--reserve-pets", "--pet-skills", "--healing-items", "--healing-magic", "--level", "--max-turns", "--sequence-length", "--learning-rate", "--target-kl", "--gae-lambda", "--entropy-weight", "--opening-rollouts", "--policy-advantage", "--warmup-matches", "--warmup-epochs", "--warmup-batch-episodes", "--warmup-learning-rate", "--pool-train-groups", "--train-groups", "--validation-groups", "--test-groups", "--initial-candidates", "--generations", "--proposals", "--native-candidates", "--finalists", "--fit-epochs", "--search-groups":
			return nil
		}
	}
	// Skip global options and their values when locating an RPC command.
	positional := []string{}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--socket", "-socket", "--config", "-config", "--timeout", "-timeout", "--profile", "--transport", "--web-base-url", "--address", "--server-id", "--character", "--map-directory":
			i++
		case "--json", "-json":
		default:
			positional = append(positional, args[i])
		}
	}
	if len(positional) == 0 {
		commands := "sessions use commands duel functions title probe init serve --json --socket --config --profile --timeout"
		if len(args) == 0 {
			commands += " ai completion --help --version"
		}
		// Reuse the public command help instead of maintaining a second root list.
		for _, line := range strings.Split(CommandHelp, "\n") {
			if strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "   ") {
				name := strings.Fields(line)[0]
				if name != "version" || len(args) == 0 {
					commands += " " + name
				}
			}
		}
		return completePrefix(commands, prefix)
	}
	command := positional[0]
	if command == "ai" {
		if len(positional) == 1 {
			return completePrefix("init check run environment experiment experiment-mix experiment-compare train collect-feedback train-feedback evaluate verify-evaluation compare-evaluations build-search build-pool build-validate export-data import-demonstrations export-model league champion simulate version --help", prefix)
		}
		if positional[1] == "environment" {
			if len(positional) == 2 {
				return completePrefix("init check --help", prefix)
			}
			if positional[2] == "init" {
				return completePrefix("--image --directory --help", prefix)
			}
			return completePrefix("--environment --help", prefix)
		}
		if positional[1] == "build-validate" {
			if len(positional) == 2 {
				return completePrefix("init run verify compare --help", prefix)
			}
			options := map[string]string{
				"init":   "--experiment --from-model --output --seed --groups",
				"run":    "--manifest --model --environment --output --resume",
				"verify": "--report", "compare": "--baseline --candidate",
			}
			return completePrefix("--search-dir --help "+options[positional[2]], prefix)
		}
		if positional[1] == "champion" {
			if len(positional) == 2 {
				return completePrefix("init challenge status rollback abandon --help", prefix)
			}
			options := map[string]string{
				"init":      "--experiment --mixed-experiment --min-groups --alpha --rule-score --champion-margin",
				"challenge": "--experiment --mixed-experiment --model --environment",
				"rollback":  "--to --reason", "abandon": "--reason", "status": "",
			}
			return completePrefix("--directory --help "+options[positional[2]], prefix)
		}
		flags := map[string]string{
			"init": "--directory --mode", "check": "--config --profile --strategy --model --mode --state-dir --pet-mask --llm-endpoint --llm-model --llm-api-key-env --llm-response-format --llm-timeout --llm-context-bytes", "run": "--config --profile --strategy --model --mode --state-dir --pet-mask --llm-endpoint --llm-model --llm-api-key-env --llm-response-format --llm-timeout --llm-context-bytes --matches --forever",
			"experiment":            "--environment --output --build-pool --pool-train-groups --from-model --seed --mode --points --pet-points --reserve-pets --pet-skills --healing-items --healing-magic --level --max-turns --train-groups --validation-groups --test-groups",
			"build-pool":            "--search-dir --output",
			"experiment-compare":    "--left-experiment --right-experiment --output",
			"experiment-mix":        "--experiment --weights --families-per-batch --from-model --output",
			"export-model":          "--data-dir --checkpoint --output --model",
			"collect-feedback":      "--environment --data-dir --experiment --model --resume --workers --stop-at-data-bytes --seed --matches --teacher --greedy --opponent",
			"train-feedback":        "--data-dir --collection --model --resume --epochs --output --stop-at-data-bytes --batch-episodes --sequence-length --learning-rate --gradient-clip --action-weighting",
			"export-data":           "--records --output --format --mode --equal-points --include-abnormal",
			"import-demonstrations": "--database --output --features",
			"league":                "--data-dir",
			"verify-evaluation":     "--report",
			"compare-evaluations":   "--baseline --candidate",
			"build-search":          "--environment --data-dir --resume --model --policy --opponent --opponent-model --seed --mode --points --pet-points --reserve-pets --pet-skills --healing-items --healing-magic --level --max-turns --initial-candidates --generations --proposals --native-candidates --finalists --fit-epochs --search-groups --validation-groups --test-groups",
			"train":                 "--environment --data-dir --demonstrations --batch-episodes --gradient-clip --action-weighting --experiment --mixed-experiment --from-model --initial-policy-scale --resume --batches --workers --stop-at-data-bytes --output --seed --epochs --batch-matches --mode --points --pet-points --reserve-pets --pet-skills --healing-items --healing-magic --level --max-turns --sequence-length --learning-rate --target-kl --gae-lambda --entropy-weight --opening-rollouts --policy-advantage --plan-scope --plan-features --warmup-matches --warmup-epochs --warmup-batch-episodes --warmup-learning-rate --warmup-action-weighting --warmup-teacher --rule-opponent --opponent-sampling --opponent-mix --database",
			"evaluate":              "--environment --model --output --resume --experiment --mixed-experiment --validation-comparison --split --seed --mode --points --pet-points --reserve-pets --pet-skills --healing-items --healing-magic --level --max-turns --matches --opponent --opponent-model --database",
			"simulate":              "--root --work --mode --matches --strategy --model --opponent-strategy --opponent-model --native-dir --allocation --member-allocation --fixture-level --require-withdrawal --min-battle-turns --seed --image --reconnect --timeout",
		}
		return completePrefix(flags[positional[1]]+" --help", prefix)
	}
	if command == "ladder" {
		command = "arena"
	}
	if command == "arena" && len(positional) == 2 && (positional[1] == "create" || positional[1] == "mode") {
		return completePrefix("1 2 3 4 5", prefix)
	}
	if command == "completion" {
		if len(positional) == 1 {
			return completePrefix("bash zsh", prefix)
		}
		return nil
	}
	if command == "serve" {
		return completePrefix("--config --profile --socket --transport --address --web-base-url --server-id --character --map-directory --foreground --help", prefix)
	}
	if command == "login" {
		return completePrefix("--config --profile --socket --transport --address --web-base-url --server-id --character --map-directory --help", prefix)
	}
	if command == "init" {
		return completePrefix("--config --profile --help", prefix)
	}
	subcommands := map[string]string{
		"query": strings.Join(QueryCodes(), " ") + " --help",
		"arena": "status contacts create mode invite accept decline loadout ready unready queue cancel leave kick leader result ack wait strategy strategies",
		"item":  "use drop drop-gold move magic pickup", "mail": "list add send remove-contact",
		"pet": "status standby battle rename drop", "party": "invite leave accept decline",
		"trade": "request offer-item offer-gold offer-pet lock confirm cancel", "title": "equip text",
		"auto-battle": "on off status", "reply": "ok cancel yes no prev next",
		"walk": "up down left right n ne e se s sw w nw", "look": "up down left right n ne e se s sw w nw",
	}
	choices := "--json --socket --config --profile --timeout"
	choices += " " + map[string]string{
		"logout": "--record-point --in-place --help",
		"say":    "--color --range", "warp": "--time", "arena": "--request-id --revision",
		"create-character": "--hometown --slot --image --face --vital --strength --toughness --dexterity --earth --water --fire --wind",
	}[command]
	if len(positional) == 1 {
		choices += " " + subcommands[command]
	}
	if command == "auto-battle" && len(positional) == 2 && positional[1] == "on" {
		choices += " walk stay"
	}
	return completePrefix(choices, prefix)
}

func completePrefix(choices, prefix string) []string {
	seen := map[string]bool{}
	result := []string{}
	for _, word := range strings.Fields(choices) {
		if strings.HasPrefix(word, prefix) && !seen[word] {
			result = append(result, word)
			seen[word] = true
		}
	}
	sort.Strings(result)
	return result
}
