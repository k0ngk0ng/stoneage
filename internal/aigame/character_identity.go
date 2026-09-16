package aigame

// ValidPersistentCharacterID validates the optional native pc1 identity. It
// says nothing about persistence by itself; only authoritative observations
// from the server's loaded-identity path establish that evidence.
func ValidPersistentCharacterID(value string) bool {
	if len(value) != 36 || value[:4] != "pc1_" {
		return false
	}
	for _, c := range value[4:] {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}
