package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	// Account buffers allow 15 bytes plus NUL. The original client's password
	// input and saved-password fields allow only 12 bytes; enforce that limit
	// across account creation, password resets and game authentication.
	MaxGameUsernameBytes = 15
	MaxGamePasswordBytes = 12
	MinGamePasswordBytes = 1

	argonMemoryKiB   = 64 * 1024
	argonIterations  = 3
	argonParallelism = 2
	argonSaltBytes   = 16
	argonKeyBytes    = 32
)

var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrInvalidUsername    = errors.New("invalid username")
	ErrInvalidPassword    = errors.New("invalid password")
)

// ValidateGameUsername validates the byte-oriented account identifier used by
// the preserved client. ASCII is intentional: it gives the web UI a
// lossless, unambiguous representation across its UTF-8 and the client's
// historical CP936 boundary.
func ValidateGameUsername(username []byte) error {
	if len(username) == 0 || len(username) > MaxGameUsernameBytes {
		return ErrInvalidUsername
	}
	for _, character := range username {
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			character == '_' || character == '-' || character == '.' {
			continue
		}
		return ErrInvalidUsername
	}
	return nil
}

// CanonicalGameUsername returns the stable account key used by the 2.5 game
// boundary.  Legacy account buffers are ASCII identifiers; only A–Z are
// folded so passwords and non-ASCII bytes are never silently changed.  Trim
// the same outer whitespace the web login form removes before the value is
// looked up or sent to GMSV.
func CanonicalGameUsername(username string) string {
	value := []byte(strings.TrimSpace(username))
	for index, character := range value {
		if character >= 'A' && character <= 'Z' {
			value[index] = character + ('a' - 'A')
		}
	}
	return string(value)
}

func ValidateGamePassword(password []byte) error {
	if len(password) < MinGamePasswordBytes || len(password) > MaxGamePasswordBytes {
		return ErrInvalidPassword
	}
	for _, character := range password {
		if character < 0x21 || character > 0x7e {
			return ErrInvalidPassword
		}
	}
	return nil
}

func ValidateAdminUsername(username string) error {
	if len(username) == 0 || len(username) > 64 {
		return ErrInvalidUsername
	}
	for _, character := range []byte(username) {
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			character == '_' || character == '-' || character == '.' || character == '@' {
			continue
		}
		return ErrInvalidUsername
	}
	return nil
}

func ValidateAdminPassword(password []byte) error {
	if len(password) < 8 || len(password) > 256 {
		return ErrInvalidPassword
	}
	return nil
}

// HashGamePassword creates an Argon2id password hash for the exact bytes sent
// by the old client. The encoded form is self-contained and can be migrated to
// another auth service without exposing the password.
func HashGamePassword(password []byte) (string, error) {
	if err := ValidateGamePassword(password); err != nil {
		return "", err
	}
	return hashArgon2id(password)
}

func HashAdminPassword(password []byte) (string, error) {
	if err := ValidateAdminPassword(password); err != nil {
		return "", err
	}
	return hashArgon2id(password)
}

func VerifyPassword(encoded string, password []byte) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v=19" {
		return false
	}
	var memoryKiB, iterations, parallelism uint32
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memoryKiB, &iterations, &parallelism); err != nil {
		return false
	}
	if memoryKiB < 8*1024 || memoryKiB > 512*1024 || iterations == 0 || iterations > 10 || parallelism == 0 || parallelism > 16 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) < 8 || len(salt) > 64 {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) == 0 || len(want) > 64 {
		return false
	}
	got := argon2.IDKey(password, salt, iterations, memoryKiB, uint8(parallelism), uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

func hashArgon2id(password []byte) (string, error) {
	salt := make([]byte, argonSaltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate password salt: %w", err)
	}
	key := argon2.IDKey(password, salt, argonIterations, argonMemoryKiB, argonParallelism, argonKeyBytes)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s",
		argonMemoryKiB,
		argonIterations,
		argonParallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

func randomUnusableGamePasswordHash() (string, error) {
	password := make([]byte, MaxGamePasswordBytes)
	if _, err := rand.Read(password); err != nil {
		return "", fmt.Errorf("generate import password: %w", err)
	}
	return hashArgon2id(password)
}
