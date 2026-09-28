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
		case "--config", "-config", "--socket", "-socket", "--model", "--database", "--output":
			return []string{"@files"}
		case "--directory", "--root", "--work":
			return []string{"@dirs"}
		case "--mode":
			return completePrefix("1 2 3 4 5", prefix)
		case "--strategy":
			return completePrefix("basic learned explore", prefix)
		case "--time":
			return completePrefix("M A N", prefix)
		case "--matches", "--epochs", "--seed", "--image", "--timeout", "-timeout",
			"--request-id", "--revision", "--color", "--range", "--hometown", "--slot", "--face",
			"--vital", "--strength", "--toughness", "--dexterity", "--earth", "--water", "--fire", "--wind":
			return nil
		}
	}
	// Skip global options and their values when locating an RPC command.
	positional := []string{}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--socket", "-socket", "--config", "-config", "--timeout", "-timeout":
			i++
		case "--json", "-json":
		default:
			positional = append(positional, args[i])
		}
	}
	if len(positional) == 0 {
		commands := "commands duel functions title probe --json --socket --config --timeout"
		if len(args) == 0 {
			commands += " arena completion serve --help --version"
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
	if command == "arena" {
		if len(positional) == 1 {
			return completePrefix("init check run train evaluate simulate version --help", prefix)
		}
		flags := map[string]string{
			"init": "--directory --mode", "check": "--config", "run": "--config --matches --forever",
			"train": "--database --output --seed --epochs", "evaluate": "--database --model",
			"simulate": "--root --work --mode --matches --strategy --model --seed --image --reconnect",
		}
		return completePrefix(flags[positional[1]]+" --help", prefix)
	}
	if command == "completion" {
		if len(positional) == 1 {
			return completePrefix("bash zsh", prefix)
		}
		return nil
	}
	if command == "serve" {
		return completePrefix("--config", prefix)
	}
	subcommands := map[string]string{
		"ladder": "status contacts create mode invite accept decline loadout ready unready queue cancel leave kick leader result ack wait strategy strategies",
		"item":   "use drop drop-gold move magic pickup", "mail": "list add send remove-contact",
		"pet": "status standby battle rename drop", "party": "invite leave accept decline",
		"trade": "request offer-item offer-gold offer-pet lock confirm cancel", "title": "equip text",
		"auto-battle": "on off status", "reply": "ok cancel yes no prev next",
		"walk": "up down left right n ne e se s sw w nw", "look": "up down left right n ne e se s sw w nw",
	}
	choices := "--json --socket --config --timeout"
	choices += " " + map[string]string{
		"say": "--color --range", "warp": "--time", "ladder": "--request-id --revision",
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
