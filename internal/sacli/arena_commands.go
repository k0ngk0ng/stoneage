package sacli

import "fmt"

const ArenaHelp = `sactl arena: player arena teams and ranked matchmaking
Commands:
  status, contacts, create <1-5>, mode <1-5>
  invite <slot> <character_id>, accept <invitation_id>, decline <invitation_id>
  loadout <pet_mask>, ready, unready, queue, cancel, leave
  kick <player_id>, leader <player_id>, result [match_id], ack
  wait <cursor> [duration] [--stream <stream>]
  strategy <id>, strategies

Each player readies independently; the team leader starts matchmaking with queue.
Use sactl ai for local AI command, training and evaluation.
The old ladder command remains a compatibility alias.`

// Reject old AI syntax before contacting any daemon or sending game commands.
func ValidateArenaCommand(args []string) error {
	if len(args) == 0 {
		return nil
	}
	switch args[0] {
	case "init", "check", "run", "train", "evaluate", "simulate", "native-simulate", "version":
		return fmt.Errorf("AI command moved: use sactl ai %s; sactl arena now controls player matchmaking", args[0])
	}
	return nil
}
