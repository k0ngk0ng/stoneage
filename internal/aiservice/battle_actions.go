package aiservice

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/k0ngk0ng/stoneage/internal/aimcp"
)

// battleActionCommand translates the public semantic vocabulary to the
// native BattleCommandDispach protocol. Index and TargetID remain separate
// typed integers; a model cannot supply a raw B packet through Command.
func battleActionCommand(action aimcp.TypedAction) (string, error) {
	hex := func(value int32) string { return strings.ToUpper(strconv.FormatInt(int64(value), 16)) }
	target := func() (string, error) {
		if action.TargetID < 0 || action.TargetID > 255 {
			return "", fmt.Errorf("%w: battle target out of range", aimcp.ErrInvalidParams)
		}
		return hex(action.TargetID), nil
	}
	switch strings.ToLower(strings.TrimSpace(action.Command)) {
	case "defend", "guard":
		return "G", nil
	case "escape":
		return "E", nil
	case "wait":
		return "N", nil
	case "attack":
		if action.TargetID < 0 || action.TargetID >= 20 {
			return "", fmt.Errorf("%w: attack requires a battle roster target", aimcp.ErrInvalidParams)
		}
		return "H|" + hex(action.TargetID), nil
	case "pet", "item", "skill":
		petWait := strings.EqualFold(strings.TrimSpace(action.Command), "pet") && action.Index == 255 && action.TargetID == 255
		if !petWait && (action.Index < 0 || action.Index > 19) {
			return "", fmt.Errorf("%w: battle index out of range", aimcp.ErrInvalidParams)
		}
		to, err := target()
		if err != nil {
			return "", err
		}
		prefix := map[string]string{"pet": "W", "item": "I", "skill": "J"}[strings.ToLower(strings.TrimSpace(action.Command))]
		return prefix + "|" + hex(action.Index) + "|" + to, nil
	default:
		return "", fmt.Errorf("%w: unsupported semantic battle command", aimcp.ErrInvalidParams)
	}
}
