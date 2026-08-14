package admin

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type ConfigManager struct {
	Path string
}

var editableConfigKeys = map[string]struct{}{
	"enable_nu_flow_control": {},
	"debuglevel":             {},
}

func (manager ConfigManager) Load() (map[string]string, error) {
	values := map[string]string{}
	if manager.Path == "" {
		return values, errors.New("server config path is not configured")
	}
	if err := manager.validateFile(); err != nil {
		return values, err
	}
	file, err := os.Open(manager.Path)
	if err != nil {
		return values, fmt.Errorf("open server config: %w", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		key, value, ok := strings.Cut(line, "=")
		if !ok || key == "" {
			continue
		}
		if _, editable := editableConfigKeys[key]; editable {
			values[key] = value
		}
	}
	if err := scanner.Err(); err != nil {
		return values, err
	}
	return values, nil
}

func (manager ConfigManager) Update(values map[string]string) error {
	if manager.Path == "" {
		return errors.New("server config path is not configured")
	}
	for key, value := range values {
		if _, ok := editableConfigKeys[key]; !ok {
			return fmt.Errorf("config key %q is not editable", key)
		}
		if err := validateConfigValue(key, value); err != nil {
			return err
		}
	}
	if err := manager.validateFile(); err != nil {
		return err
	}
	input, err := os.ReadFile(manager.Path)
	if err != nil {
		return fmt.Errorf("read server config: %w", err)
	}
	lines := strings.Split(string(input), "\n")
	found := map[string]bool{}
	for index, line := range lines {
		key, _, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		value, exists := values[key]
		if !exists {
			continue
		}
		lines[index] = key + "=" + value
		found[key] = true
	}
	for _, key := range []string{"enable_nu_flow_control", "debuglevel"} {
		if value, exists := values[key]; exists && !found[key] {
			lines = append(lines, key+"="+value)
		}
	}
	content := []byte(strings.Join(lines, "\n"))
	mode := os.FileMode(0o640)
	if info, statErr := os.Stat(manager.Path); statErr == nil {
		mode = info.Mode().Perm()
	}
	directory := filepath.Dir(manager.Path)
	temporary, err := os.CreateTemp(directory, ".setup.cf.*.tmp")
	if err != nil {
		return fmt.Errorf("create config temporary: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		temporary.Close()
		return fmt.Errorf("write config temporary: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync config temporary: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryName, manager.Path); err != nil {
		return fmt.Errorf("replace server config: %w", err)
	}
	return nil
}

func (manager ConfigManager) validateFile() error {
	info, err := os.Lstat(manager.Path)
	if err != nil {
		return fmt.Errorf("inspect server config: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("server config must not be a symbolic link")
	}
	if !info.Mode().IsRegular() {
		return errors.New("server config is not a regular file")
	}
	return nil
}

func validateConfigValue(key, value string) error {
	switch key {
	case "enable_nu_flow_control":
		if value != "0" && value != "1" {
			return errors.New("enable_nu_flow_control must be 0 or 1")
		}
	case "debuglevel":
		level, err := strconv.Atoi(value)
		if err != nil || level < 0 || level > 3 {
			return errors.New("debuglevel must be between 0 and 3")
		}
	default:
		return fmt.Errorf("config key %q is not editable", key)
	}
	return nil
}
