package config

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"gopkg.in/yaml.v3"
)

var configWriteMu sync.Mutex

// WriteConfigAtomic validates and publishes a complete config file without
// exposing a partially written target to concurrent readers.
func WriteConfigAtomic(configFile string, data []byte) error {
	configWriteMu.Lock()
	defer configWriteMu.Unlock()
	return atomicWriteConfigUnlocked(configFile, data)
}

func validateConfigYAML(data []byte) error {
	if len(bytes.TrimSpace(data)) == 0 {
		return fmt.Errorf("config YAML is empty")
	}
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return fmt.Errorf("parse config YAML: %w", err)
	}
	if root.Kind != yaml.DocumentNode || len(root.Content) == 0 || root.Content[0] == nil || root.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("config YAML root must be a mapping")
	}
	var cfg Config
	if err := root.Decode(&cfg); err != nil {
		return fmt.Errorf("decode config YAML: %w", err)
	}
	if err := ValidateModelPolicy(cfg.ModelPolicy); err != nil {
		return fmt.Errorf("invalid model policy: %w", err)
	}
	return nil
}

func atomicWriteConfigUnlocked(configFile string, data []byte) (err error) {
	if err = validateConfigYAML(data); err != nil {
		return err
	}

	absolute, err := filepath.Abs(configFile)
	if err != nil {
		return fmt.Errorf("resolve config path: %w", err)
	}
	dir := filepath.Dir(absolute)
	base := filepath.Base(absolute)
	if err = os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}

	mode := os.FileMode(0o600)
	targetExists := false
	if info, statErr := os.Stat(absolute); statErr == nil {
		mode = info.Mode().Perm()
		targetExists = true
	} else if !os.IsNotExist(statErr) {
		return fmt.Errorf("stat config target: %w", statErr)
	}

	staging, err := os.CreateTemp(dir, "."+base+".*.tmp")
	if err != nil {
		return fmt.Errorf("create config staging file: %w", err)
	}
	stagingPath := staging.Name()
	defer func() {
		_ = staging.Close()
		_ = os.Remove(stagingPath)
	}()
	if err = staging.Chmod(mode); err != nil {
		return fmt.Errorf("set config staging permissions: %w", err)
	}
	if _, err = staging.Write(data); err != nil {
		return fmt.Errorf("write config staging file: %w", err)
	}
	if err = staging.Sync(); err != nil {
		return fmt.Errorf("flush config staging file: %w", err)
	}
	if err = staging.Close(); err != nil {
		return fmt.Errorf("close config staging file: %w", err)
	}

	var backupPath string
	if targetExists {
		backup, backupErr := os.CreateTemp(dir, "."+base+".*.rollback")
		if backupErr != nil {
			return fmt.Errorf("create config rollback file: %w", backupErr)
		}
		backupPath = backup.Name()
		defer func() { _ = os.Remove(backupPath) }()
		source, openErr := os.Open(absolute)
		if openErr != nil {
			_ = backup.Close()
			return fmt.Errorf("open current config for rollback: %w", openErr)
		}
		_, copyErr := io.Copy(backup, source)
		closeSourceErr := source.Close()
		syncErr := backup.Sync()
		closeBackupErr := backup.Close()
		switch {
		case copyErr != nil:
			return fmt.Errorf("copy current config for rollback: %w", copyErr)
		case closeSourceErr != nil:
			return fmt.Errorf("close current config after rollback copy: %w", closeSourceErr)
		case syncErr != nil:
			return fmt.Errorf("flush config rollback file: %w", syncErr)
		case closeBackupErr != nil:
			return fmt.Errorf("close config rollback file: %w", closeBackupErr)
		}
		if err = os.Chmod(backupPath, mode); err != nil {
			return fmt.Errorf("set config rollback permissions: %w", err)
		}
	}

	if err = replaceFile(stagingPath, absolute); err != nil {
		return fmt.Errorf("publish config atomically: %w", err)
	}
	stagingPath = ""

	published, readErr := os.ReadFile(absolute)
	if readErr == nil {
		readErr = validateConfigYAML(published)
	}
	if readErr == nil && bytes.Equal(published, data) {
		if info, statErr := os.Stat(absolute); statErr == nil && info.Mode().Perm() == mode {
			return nil
		} else if statErr != nil {
			readErr = statErr
		} else {
			readErr = fmt.Errorf("published config permissions changed from %v to %v", mode, info.Mode().Perm())
		}
	}
	if readErr == nil {
		readErr = fmt.Errorf("published config content differs from staged content")
	}

	if backupPath != "" {
		if restoreErr := replaceFile(backupPath, absolute); restoreErr != nil {
			return fmt.Errorf("verify published config: %v; restore previous config: %w", readErr, restoreErr)
		}
		backupPath = ""
	} else {
		_ = os.Remove(absolute)
	}
	return fmt.Errorf("verify published config: %w", readErr)
}
