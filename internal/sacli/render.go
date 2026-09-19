package sacli

import (
	"fmt"
	"strings"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
)

// renderSnapshot renders the observation a model reasons about. It is
// deliberately compact: every line is something the caller can act on.
func renderSnapshot(snapshot aigame.Snapshot, floorName string) string {
	var builder strings.Builder
	position := snapshot.Position
	fmt.Fprintf(&builder, "phase=%s character=%q revision=%d\n", snapshot.Phase, snapshot.Character, snapshot.Revision)
	// A character that is not in the world has no authoritative tile; the
	// zero value must never be rendered as a real coordinate.
	if inWorld(snapshot.Phase) && position.Floor >= 0 {
		fmt.Fprintf(&builder, "position floor=%d x=%d y=%d facing=%d\n", position.Floor, position.X, position.Y, position.Direction)
		if floorName != "" {
			fmt.Fprintf(&builder, "map_name=%q\n", floorName)
		}
		if snapshot.Player.ID > 0 {
			fmt.Fprintf(&builder, "player_id=%d\n", snapshot.Player.ID)
		}
	}
	player := snapshot.Player
	if player.HasStatus {
		fmt.Fprintf(&builder, "player level=%d hp=%d/%d mp=%d/%d exp=%d/%d gold=%d\n",
			player.Level, player.HP, player.MaxHP, player.MP, player.MaxMP, player.EXP, player.MaxEXP, player.Gold)
		if player.StatPointsKnown && player.UnspentStatPoints > 0 {
			fmt.Fprintf(&builder, "unspent_stat_points=%d\n", player.UnspentStatPoints)
		}
	}
	if others := otherActors(snapshot); len(others) > 0 {
		fmt.Fprintf(&builder, "nearby=%d\n", len(others))
		for _, actor := range others {
			label := actor.Name
			if label == "" {
				label = actor.FreeName
			}
			if label == "" {
				label = actor.Kind
			}
			fmt.Fprintf(&builder, "  - %s kind=%s id=%d x=%d y=%d", label, actor.Kind, actor.ID, actor.X, actor.Y)
			if actor.Level > 0 {
				fmt.Fprintf(&builder, " level=%d", actor.Level)
			}
			builder.WriteString("\n")
		}
	}
	if window := snapshot.ActiveWindow; window != nil && window.Open {
		renderWindow(&builder, window)
	}
	if len(snapshot.Windows) > 1 {
		fmt.Fprintf(&builder, "open_windows=%d\n", len(snapshot.Windows))
	}
	if battle := snapshot.Battle; battle.Active {
		fmt.Fprintf(&builder, "battle active turn=%d command_ready=%t player_submitted=%t pet_submitted=%t",
			battle.Turn, battle.CommandReady, battle.PlayerSubmitted, battle.PetSubmitted)
		if battle.Ended {
			fmt.Fprintf(&builder, " ended result=%q", battle.Result)
		}
		builder.WriteString("\n")
		for _, participant := range battle.Participants {
			fmt.Fprintf(&builder, "  - battle id=%d %q hp=%d/%d\n",
				participant.BattleID, participant.Name, participant.HP, participant.MaxHP)
		}
	}
	if len(snapshot.Inventory) > 0 {
		fmt.Fprintf(&builder, "inventory=%d\n", len(snapshot.Inventory))
		for _, item := range snapshot.Inventory {
			fmt.Fprintf(&builder, "  - slot=%d %q graphic=%d\n", item.Index, item.Name, item.Graphic)
		}
	}
	for _, pet := range snapshot.Pets {
		fmt.Fprintf(&builder, "pet slot=%d %q hp=%d/%d level=%d\n", pet.Slot, pet.Name, pet.HP, pet.MaxHP, pet.Level)
	}
	if len(snapshot.Skills) > 0 {
		parts := make([]string, 0, len(snapshot.Skills))
		for _, skill := range snapshot.Skills {
			parts = append(parts, fmt.Sprintf("%d:%d", skill.ID, skill.Level))
		}
		fmt.Fprintf(&builder, "skills=%s\n", strings.Join(parts, " "))
	}
	if len(snapshot.Party) > 0 {
		fmt.Fprintf(&builder, "party=%d\n", len(snapshot.Party))
		for _, member := range snapshot.Party {
			fmt.Fprintf(&builder, "  - slot=%d %q level=%d hp=%d/%d mp=%d\n",
				member.Slot, member.Name, member.Level, member.HP, member.MaxHP, member.MP)
		}
	}
	// The address book only means something after a complete AB response; a
	// partial update must not be shown as the contact list.
	if snapshot.AddressBookKnown {
		contacts := 0
		lines := make([]string, 0, len(snapshot.AddressBook))
		for _, entry := range snapshot.AddressBook {
			if !entry.Use {
				continue
			}
			contacts++
			state := "offline"
			if entry.Online {
				state = "online"
			}
			lines = append(lines, fmt.Sprintf("  - slot=%d %q %s level=%d", entry.Index, entry.Name, state, entry.Level))
		}
		fmt.Fprintf(&builder, "address_book=%d\n", contacts)
		for _, line := range lines {
			builder.WriteString(line + "\n")
		}
	}
	if trade := snapshot.Trade; trade.Active || trade.Pending {
		fmt.Fprintf(&builder, "trade phase=%s peer=%q own_locked=%t peer_locked=%t own_final=%t peer_final=%t\n",
			trade.Phase, trade.PeerName, trade.OwnLocked, trade.PeerLocked, trade.OwnFinal, trade.PeerFinal)
		for slot, offer := range trade.OwnOffers {
			if offer.Kind == "" {
				continue
			}
			fmt.Fprintf(&builder, "  - own_offer[%d] %s %q\n", slot, offer.Kind, offer.Name)
		}
		for slot, offer := range trade.PeerOffers {
			if offer.Kind == "" {
				continue
			}
			fmt.Fprintf(&builder, "  - peer_offer[%d] %s %q\n", slot, offer.Kind, offer.Name)
		}
	}
	if len(snapshot.Chat) > 0 {
		fmt.Fprintf(&builder, "chat_recent=%d\n", len(snapshot.Chat))
		for _, message := range snapshot.Chat {
			fmt.Fprintf(&builder, "  - [%s] %s: %s\n", message.Channel, message.SpeakerCharacterID, message.Text)
		}
	}
	if snapshot.LastError != "" {
		fmt.Fprintf(&builder, "last_error=%s\n", snapshot.LastError)
	}
	return strings.TrimRight(builder.String(), "\n")
}

// otherActors returns the visible actors excluding the player's own entry.
// The server never echoes an owner's own walk, so the local actor record
// keeps a stale tile while Position is confirmed by the status request;
// printing both would show the reader two different coordinates for one
// character.
func otherActors(snapshot aigame.Snapshot) []aigame.ActorSnapshot {
	others := make([]aigame.ActorSnapshot, 0, len(snapshot.Actors))
	for _, actor := range snapshot.Actors {
		if snapshot.Player.ID > 0 && actor.ID == snapshot.Player.ID {
			continue
		}
		if snapshot.Character != "" && actor.Name == snapshot.Character {
			continue
		}
		others = append(others, actor)
	}
	return others
}

// renderObservation renders one snapshot with the current floor's display
// name when the map data is available.
func (s *Server) renderObservation(snapshot aigame.Snapshot) string {
	return renderSnapshot(snapshot, s.floorName(int(snapshot.Position.Floor)))
}

// inWorld reports whether the session has an authoritative character tile.
func inWorld(phase aigame.Phase) bool {
	return phase == aigame.PhaseWorld || phase == aigame.PhaseBattle
}

// renderEvent renders one server event for `log` and `wait`.
func renderEvent(event aigame.Event) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "seq=%d function=%s", event.Sequence, event.Function)
	for index, field := range event.Fields {
		fmt.Fprintf(&builder, " [%d]=%s", index, field.String())
	}
	return builder.String()
}
