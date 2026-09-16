package aigame

// char_base.h CHAR_FS_* values. Battle visibility bit 1 is deliberately not
// offered as a setting: the 2.5 FS handler does not implement it.
func socialSettingMask(name string) (int32, bool) {
	switch name {
	case "party":
		return 1, true
	case "duel":
		return 4, true
	case "party-chat":
		return 8, true
	case "trade-card":
		return 16, true
	case "trade":
		return 32, true
	default:
		return 0, false
	}
}

func (session *Session) invalidateSubmittedSocialFlags(action Action, epoch uint64) {
	if action.Kind != ActionSocialSetting {
		return
	}
	session.stateMu.Lock()
	defer session.stateMu.Unlock()
	if session.state.socialFlagsEpoch == epoch {
		session.state.snapshot.Player.SocialFlagsKnown = false
	}
}
