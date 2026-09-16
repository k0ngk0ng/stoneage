package aiprovision

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/k0ngk0ng/stoneage/internal/auth"
)

const (
	secretFilePrefix = "account-"
	secretFileSuffix = ".key"
	passwordAlphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789!@#$%^&*_-"
	usernameAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
)

// SecretStore keeps game passwords below a server-owned private directory.
// It intentionally exposes writes for the provisioning workflow but does not
// expose a read method: only ProfileSessionProvider in this package can
// resolve a credential after checking the immutable binding.
type SecretStore struct {
	root string
}

// NewSecretStore creates or validates a private game-secret directory. The
// path must be absolute and may not be the filesystem root.
func NewSecretStore(root string) (*SecretStore, error) {
	root = filepath.Clean(strings.TrimSpace(root))
	if root == "." || !filepath.IsAbs(root) || root == string(filepath.Separator) {
		return nil, fmt.Errorf("%w: secret root must be a non-root absolute path", ErrInvalidConfig)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create game secret directory: %w", err)
	}
	if err := ensurePrivateDir(root); err != nil {
		return nil, err
	}
	return &SecretStore{root: root}, nil
}

func ensurePrivateDir(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect private directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("aiprovision: private path is not a real directory")
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return fmt.Errorf("protect private directory: %w", err)
	}
	return nil
}

func (store *SecretStore) path(accountID int64) (string, error) {
	if store == nil || store.root == "" || accountID <= 0 {
		return "", ErrSecretUnavailable
	}
	return filepath.Join(store.root, secretFilePrefix+strconv.FormatInt(accountID, 10)+secretFileSuffix), nil
}

// Put stores one validated game password atomically with mode 0600. The
// password bytes are copied only for the duration of the write.
func (store *SecretStore) Put(accountID int64, password []byte) error {
	if err := auth.ValidateGamePassword(password); err != nil {
		return fmt.Errorf("%w: invalid game password", ErrSecretUnavailable)
	}
	path, err := store.path(accountID)
	if err != nil {
		return err
	}
	if err := ensurePrivateDir(store.root); err != nil {
		return err
	}
	if info, statErr := os.Lstat(path); statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return errors.New("aiprovision: game secret target is unsafe")
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("inspect game secret: %w", statErr)
	}
	temporary, err := os.CreateTemp(store.root, ".account-secret-*-tmp")
	if err != nil {
		return fmt.Errorf("create game secret: %w", err)
	}
	tmpName := temporary.Name()
	defer os.Remove(tmpName)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(password); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write game secret: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync game secret: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close game secret: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("install game secret: %w", err)
	}
	return nil
}

func (store *SecretStore) read(accountID int64) ([]byte, error) {
	path, err := store.path(accountID)
	if err != nil {
		return nil, err
	}
	if err := ensurePrivateDir(store.root); err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: secret is missing", ErrSecretUnavailable)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: inspect secret", ErrSecretUnavailable)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("%w: secret file permissions are unsafe", ErrSecretUnavailable)
	}
	password, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: read secret", ErrSecretUnavailable)
	}
	if err := auth.ValidateGamePassword(password); err != nil {
		zeroBytes(password)
		return nil, fmt.Errorf("%w: stored secret is invalid", ErrSecretUnavailable)
	}
	return password, nil
}

// Delete removes one private credential file after verifying that the target
// is a regular file. It is used only when provisioning has to roll back.
func (store *SecretStore) Delete(accountID int64) error {
	path, err := store.path(accountID)
	if err != nil {
		return err
	}
	if err := ensurePrivateDir(store.root); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("aiprovision: game secret target is unsafe")
	}
	return os.Remove(path)
}

func zeroBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}

func randomString(length int, alphabet string) (string, error) {
	if length <= 0 || len(alphabet) == 0 {
		return "", errors.New("aiprovision: invalid random string request")
	}
	result := make([]byte, length)
	buffer := make([]byte, length)
	for offset := 0; offset < len(result); {
		if _, err := io.ReadFull(rand.Reader, buffer); err != nil {
			return "", err
		}
		for _, value := range buffer {
			// Rejection sampling avoids modulo bias in generated account names
			// and passwords.
			limit := byte(256 - (256 % len(alphabet)))
			if value >= limit {
				continue
			}
			result[offset] = alphabet[int(value)%len(alphabet)]
			offset++
			if offset == len(result) {
				break
			}
		}
	}
	return string(result), nil
}
