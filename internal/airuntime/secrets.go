package airuntime

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SecretStore maps a model configuration ID to a private, server-side key
// file.  It does not accept a path from an HTTP request and never exposes key
// material through ModelConfig or JSON serialization.
type SecretStore struct {
	dir string
}

func NewSecretStore(dir string) (*SecretStore, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("airuntime: secret directory is empty")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create AI secret directory: %w", err)
	}
	if info, err := os.Lstat(dir); err != nil {
		return nil, fmt.Errorf("inspect AI secret directory: %w", err)
	} else if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, errors.New("airuntime: secret directory must be a real directory")
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, fmt.Errorf("protect AI secret directory: %w", err)
	}
	return &SecretStore{dir: dir}, nil
}

func (secrets *SecretStore) Dir() string {
	if secrets == nil {
		return ""
	}
	return secrets.dir
}

func validConfigID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, character := range id {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '-' || character == '_' {
			continue
		}
		return false
	}
	return true
}

func (secrets *SecretStore) KeyPath(configID string) (string, error) {
	if secrets == nil || secrets.dir == "" {
		return "", errors.New("airuntime: secret store is not configured")
	}
	if !validConfigID(configID) {
		return "", errors.New("airuntime: invalid model config id")
	}
	return filepath.Join(secrets.dir, "model-"+configID+".key"), nil
}

func (secrets *SecretStore) HasKey(configID string) (bool, error) {
	path, err := secrets.KeyPath(configID)
	if err != nil {
		return false, err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect AI model key: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return false, errors.New("airuntime: model key must be a regular file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return false, errors.New("airuntime: model key file must be private (0600)")
	}
	return true, nil
}

func (secrets *SecretStore) ReadKey(configID string) (string, error) {
	path, err := secrets.KeyPath(configID)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", errors.New("airuntime: model key is not configured")
		}
		return "", fmt.Errorf("inspect AI model key: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", errors.New("airuntime: model key must be a regular file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return "", errors.New("airuntime: model key file must be private (0600)")
	}
	value, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read AI model key: %w", err)
	}
	key := strings.TrimSpace(string(value))
	if key == "" {
		return "", errors.New("airuntime: model key is empty")
	}
	return key, nil
}

// WriteKey is intended for server-side provisioning or a future admin
// handler.  The key is never returned by this package's public profile/model
// configuration types.
func (secrets *SecretStore) WriteKey(configID, key string) error {
	path, err := secrets.KeyPath(configID)
	if err != nil {
		return err
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return errors.New("airuntime: model key is empty")
	}
	if info, statErr := os.Lstat(path); statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return errors.New("airuntime: model key must be a regular file")
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("inspect AI model key: %w", statErr)
	}
	// WriteFile with 0600 followed by Chmod also protects an existing file.
	if err := os.WriteFile(path, []byte(key+"\n"), 0o600); err != nil {
		return fmt.Errorf("write AI model key: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("protect AI model key: %w", err)
	}
	return nil
}

func (secrets *SecretStore) RemoveKey(configID string) error {
	path, err := secrets.KeyPath(configID)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove AI model key: %w", err)
	}
	return nil
}
