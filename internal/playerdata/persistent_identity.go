package playerdata

const (
	persistentCharacterIDPrefix = "pc1_"
	persistentCharacterIDLength = len(persistentCharacterIDPrefix) + 32
)

// persistentCharacterIDFromRecord returns only the native identity format
// accepted for a persisted character archive. Invalid or missing charid data
// remains unknown; reading a snapshot never repairs or writes the record.
func persistentCharacterIDFromRecord(record *Record) string {
	if record == nil {
		return ""
	}
	raw, ok := record.Raw("charid")
	if !ok || len(raw) != persistentCharacterIDLength {
		return ""
	}
	value := string(raw)
	if value[:len(persistentCharacterIDPrefix)] != persistentCharacterIDPrefix {
		return ""
	}
	for i := len(persistentCharacterIDPrefix); i < len(value); i++ {
		c := value[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return ""
		}
	}
	return value
}
